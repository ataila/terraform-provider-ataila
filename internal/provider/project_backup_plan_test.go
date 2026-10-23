// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/ataila/terraform-provider-ataila/internal/acctest"
	"github.com/ataila/terraform-provider-ataila/internal/client"
)

const backupPlanAddr = "ataila_project_backup_plan.shop"

// backupProject is a project with its seeded default plan on a platform that
// serves backups (1.0.399 on).
func backupProject(t *testing.T) (*acctest.MockAPI, string) {
	t.Helper()
	m := newMock(t)
	m.SetMeta(func(meta map[string]any) { meta["platform_version"] = client.ReleaseBackupPlans })
	_, tenant := m.AddCustomer("EXAMPLE", "example")
	pid := m.AddTestProject(tenant, "shop", "k8s")
	m.SeedBackupPlan(pid)
	return m, pid
}

func backupPlanHCL(pid, body string) string {
	return fmt.Sprintf(`
resource "ataila_project_backup_plan" "shop" {
  project_id = %q
%s
}
`, pid, body)
}

const ownTiersHCL = `
  tiers = {
    daily   = { keep = 14, at = "01:30" }
    weekly  = { keep = 8, day = "sat" }
    monthly = { keep = 12, day = 15, at = "04:00" }
    yearly  = { keep = 5, enabled = false }
  }`

const uatDatabaseHCL = `
  environments = {
    dev  = { code = true, database = true, files = false }
    uat  = { code = false, database = true, files = false }
    prod = { code = true, database = true, files = true }
  }`

// backupPlanIsDefault is the CheckDestroy: destroy returns the plan to the default.
func backupPlanIsDefault(m *acctest.MockAPI, pid string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if p := m.BackupPlan(pid); p["is_default"] != true || p["enabled"] != true {
			return fmt.Errorf("the plan of project %s is not the default: %v", pid, p)
		}
		return nil
	}
}

func backupPlanField(m *acctest.MockAPI, pid string, want map[string]any) resource.TestCheckFunc {
	return check(func() error {
		p := m.BackupPlan(pid)
		for k, v := range want {
			if fmt.Sprint(p[k]) != fmt.Sprint(v) {
				return fmt.Errorf("plan %s = %v, want %v (plan %v)", k, p[k], v, p)
			}
		}
		return nil
	})
}

func TestAccProjectBackupPlan_lifecycle(t *testing.T) {
	m, pid := backupProject(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		CheckDestroy:             backupPlanIsDefault(m, pid),
		Steps: []resource.TestStep{
			{
				// The default, declared: nothing changes on the platform.
				Config: providerBlock(false) + backupPlanHCL(pid, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(backupPlanAddr, "id", pid),
					resource.TestCheckResourceAttr(backupPlanAddr, "enabled", "true"),
					resource.TestCheckResourceAttr(backupPlanAddr, "version", "1"),
					resource.TestCheckResourceAttr(backupPlanAddr, "is_default", "true"),
					resource.TestCheckResourceAttr(backupPlanAddr, "tiers_overridden", "false"),
					resource.TestCheckNoResourceAttr(backupPlanAddr, "environments.%"),
					resource.TestCheckNoResourceAttr(backupPlanAddr, "tiers.%"),
				),
			},
			{
				// UAT's database backed up, and the project's own tiers.
				Config: providerBlock(false) + backupPlanHCL(pid, uatDatabaseHCL+ownTiersHCL),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(backupPlanAddr, "version", "2"),
					resource.TestCheckResourceAttr(backupPlanAddr, "is_default", "false"),
					resource.TestCheckResourceAttr(backupPlanAddr, "tiers_overridden", "true"),
					resource.TestCheckResourceAttr(backupPlanAddr, "environments.uat.database", "true"),
					resource.TestCheckResourceAttr(backupPlanAddr, "tiers.daily.enabled", "true"),
					resource.TestCheckResourceAttr(backupPlanAddr, "tiers.yearly.enabled", "false"),
					resource.TestCheckNoResourceAttr(backupPlanAddr, "tiers.weekly.at"),
					check(func() error {
						p := m.BackupPlan(pid)
						tiers := p["tiers"].(map[string]any)
						daily, weekly := tiers["daily"].(map[string]any), tiers["weekly"].(map[string]any)
						if fmt.Sprint(daily["keep"]) != "14" || daily["at"] != "01:30" || weekly["day"] != "sat" ||
							weekly["at"] != nil {
							return fmt.Errorf("stored tiers %v", tiers)
						}
						return nil
					}),
				),
			},
			{
				// Tiers left out: back to following the installation default.
				Config: providerBlock(false) + backupPlanHCL(pid, uatDatabaseHCL),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(backupPlanAddr, "version", "3"),
					resource.TestCheckResourceAttr(backupPlanAddr, "tiers_overridden", "false"),
					resource.TestCheckNoResourceAttr(backupPlanAddr, "tiers.%"),
					backupPlanField(m, pid, map[string]any{"tiers_overridden": false}),
				),
			},
			{
				// The installation default moves; a plan that follows it shows no change.
				PreConfig: func() { m.SetDefaultBackupTiers(9) },
				Config:    providerBlock(false) + backupPlanHCL(pid, uatDatabaseHCL),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: check(func() error {
					tiers := m.BackupPlan(pid)["tiers"].(map[string]any)
					if fmt.Sprint(tiers["daily"].(map[string]any)["keep"]) != "9" {
						return fmt.Errorf("the plan does not follow the new default: %v", tiers)
					}
					return nil
				}),
			},
			{ResourceName: backupPlanAddr, ImportState: true, ImportStateId: pid, ImportStateVerify: true},
		},
	})
}

func TestAccProjectBackupPlan_switchingOffNeedsBothSwitches(t *testing.T) {
	m, pid := backupProject(t)
	off := backupPlanHCL(pid, "  enabled = false")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		CheckDestroy:             backupPlanIsDefault(m, pid),
		Steps: []resource.TestStep{
			{
				// Refused at plan time: nothing is sent.
				Config:      providerBlock(false) + off,
				PlanOnly:    true,
				ExpectError: words("Setting enabled = false switches the project's backups off"),
			},
			{
				// The provider's switch on, the token's off: the platform refuses.
				PreConfig:   func() { m.SetTokenAllowDestroy(false) },
				Config:      providerBlock(true) + off,
				ExpectError: words("Setting enabled = false switches the project's backups off"),
			},
			{
				PreConfig: func() { m.SetTokenAllowDestroy(true) },
				Config:    providerBlock(true) + off,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(backupPlanAddr, "enabled", "false"),
					backupPlanField(m, pid, map[string]any{"enabled": false}),
				),
			},
			{
				// Off already: keeping it off is no switch, whatever the provider says.
				Config: providerBlock(false) + off,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				Config: providerBlock(false) + backupPlanHCL(pid, ""),
				Check:  backupPlanField(m, pid, map[string]any{"enabled": true}),
			},
			{
				// On again, then off with the provider's switch off: refused at plan time.
				Config:      providerBlock(false) + off,
				ExpectError: words("Setting enabled = false switches the project's backups off"),
			},
			{
				// Destroy is "back to the defaults": no switch needed, even with the token's off.
				PreConfig: func() { m.SetTokenAllowDestroy(false) },
				Config:    providerBlock(false) + backupPlanHCL(pid, uatDatabaseHCL),
				Check:     backupPlanField(m, pid, map[string]any{"is_default": false}),
			},
		},
	})
}

func TestAccProjectBackupPlan_portalChanges(t *testing.T) {
	m, pid := backupProject(t)
	config := providerBlock(false) + backupPlanHCL(pid, "")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{Config: config},
			{
				// The portal gives UAT a database backup and own tiers: the next apply takes both back.
				PreConfig: func() { m.SetBackupPlanInPortal(pid, true, true, 21) },
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(backupPlanAddr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					backupPlanField(m, pid, map[string]any{"is_default": true}),
					resource.TestCheckNoResourceAttr(backupPlanAddr, "environments.%"),
					resource.TestCheckNoResourceAttr(backupPlanAddr, "tiers.%"),
				),
			},
			{
				// The portal changes the plan between the provider's read and its write.
				PreConfig:   func() { m.ChangeBackupPlanAfterNextRead(pid) },
				Config:      providerBlock(false) + backupPlanHCL(pid, uatDatabaseHCL),
				ExpectError: words("The backup plan changed outside Terraform since the last read"),
			},
		},
	})
}

func TestAccProjectBackupPlan_validation(t *testing.T) {
	m, pid := backupProject(t)
	_ = m
	tier := func(daily, weekly, monthly string) string {
		return fmt.Sprintf(`
  tiers = {
    daily   = { %s }
    weekly  = { %s }
    monthly = { %s }
    yearly  = { keep = 3 }
  }`, daily, weekly, monthly)
	}
	cases := map[string]struct {
		body string
		want *regexp.Regexp
	}{
		"keep_zero":     {tier(`keep = 0, at = "02:00"`, `keep = 4, day = "sun"`, `keep = 6, day = 1`), words("between 1 and 366")},
		"bad_time":      {tier(`keep = 7, at = "25:00"`, `keep = 4, day = "sun"`, `keep = 6, day = 1`), words("must be a time HH:MM")},
		"bad_weekday":   {tier(`keep = 7, at = "02:00"`, `keep = 4, day = "someday"`, `keep = 6, day = 1`), words("value must be one of")},
		"monthly_29":    {tier(`keep = 7, at = "02:00"`, `keep = 4, day = "sun"`, `keep = 6, day = 29`), words("between 1 and 28")},
		"daily_no_time": {tier(`keep = 7`, `keep = 4, day = "sun"`, `keep = 6, day = 1`), words(`attribute "at" is required`)},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: protoV6,
				Steps: []resource.TestStep{
					{Config: providerBlock(false) + backupPlanHCL(pid, tc.body), PlanOnly: true, ExpectError: tc.want},
				},
			})
		})
	}
}

func TestAccProjectBackupPlan_olderPlatformRefused(t *testing.T) {
	m, pid := backupProject(t)
	m.SetMeta(func(meta map[string]any) { meta["platform_version"] = client.ReleaseBrandApps })
	refused := words("needs platform release " + client.ReleaseBackupPlans)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{Config: providerBlock(false) + backupPlanHCL(pid, ""), ExpectError: refused},
			{
				Config: providerBlock(false) + fmt.Sprintf(`
data "ataila_project_backup_sets" "shop" {
  project_id = %q
}
`, pid),
				ExpectError: refused,
			},
			{
				Config: providerBlock(false) + fmt.Sprintf(`
data "ataila_project_backup_exports" "shop" {
  project_id = %q
}
`, pid),
				ExpectError: refused,
			},
		},
	})
}

func TestAccProjectBackupSets_read(t *testing.T) {
	m, pid := backupProject(t)
	for i := 0; i < 3; i++ {
		m.AddBackupSet(pid, "prod", "daily", "success")
	}
	m.AddBackupSet(pid, "dev", "daily", "success")
	failed := m.AddBackupSet(pid, "prod", "manual", "failed")
	m.AddBackupExport(pid, "success", "prod")
	m.AddBackupExport(pid, "expired", "dev", "prod")
	factories, rec := recordingProvider()
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: providerBlock(false) + fmt.Sprintf(`
data "ataila_project_backup_sets" "all" {
  project_id = %[1]q
}

data "ataila_project_backup_sets" "prod_daily" {
  project_id  = %[1]q
  environment = "prod"
  tier        = "daily"
  limit       = 2
}

data "ataila_project_backup_sets" "failed" {
  project_id = %[1]q
  status     = "failed"
}

data "ataila_project_backup_exports" "all" {
  project_id = %[1]q
}

data "ataila_project_backup_exports" "ready" {
  project_id = %[1]q
  status     = "success"
}
`, pid),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.ataila_project_backup_sets.all", "sets.#", "5"),
					resource.TestCheckResourceAttr("data.ataila_project_backup_sets.all", "more", "false"),
					resource.TestCheckResourceAttr("data.ataila_project_backup_sets.all", "sets.0.id", failed),
					resource.TestCheckResourceAttr("data.ataila_project_backup_sets.prod_daily", "sets.#", "2"),
					resource.TestCheckResourceAttr("data.ataila_project_backup_sets.prod_daily", "more", "true"),
					resource.TestCheckResourceAttr("data.ataila_project_backup_sets.prod_daily", "sets.0.environment", "prod"),
					resource.TestCheckResourceAttr("data.ataila_project_backup_sets.prod_daily", "sets.0.has_checksums", "true"),
					resource.TestCheckResourceAttr("data.ataila_project_backup_sets.prod_daily", "sets.0.bytes", "1048576"),
					resource.TestCheckResourceAttr("data.ataila_project_backup_sets.failed", "sets.#", "1"),
					resource.TestCheckResourceAttr("data.ataila_project_backup_sets.failed", "sets.0.error",
						"the database dump failed"),
					resource.TestCheckNoResourceAttr("data.ataila_project_backup_sets.failed", "sets.0.bytes"),
					resource.TestCheckResourceAttr("data.ataila_project_backup_exports.all", "exports.#", "2"),
					resource.TestCheckResourceAttr("data.ataila_project_backup_exports.all", "exports.0.status", "expired"),
					resource.TestCheckResourceAttr("data.ataila_project_backup_exports.all", "exports.0.environments.#", "2"),
					resource.TestCheckResourceAttr("data.ataila_project_backup_exports.ready", "exports.#", "1"),
					resource.TestCheckResourceAttr("data.ataila_project_backup_exports.ready", "exports.0.sha256",
						"abababababababababababababababababababababababababababababababab"),
					resource.TestCheckResourceAttr("data.ataila_project_backup_exports.ready", "exports.0.contents.0",
						"manifest.json"),
					rec.expectWarning("More backup sets matched", "limit = 2"),
				),
			},
		},
	})
}

func TestAccProjectBackupSets_needsTheBackupKey(t *testing.T) {
	m, pid := backupProject(t)
	var scopes []string
	for _, s := range acctest.DefaultScopes() {
		if !strings.HasPrefix(s, "project-backup-") {
			scopes = append(scopes, s)
		}
	}
	m.SetScopes(scopes...)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{
				Config: providerBlock(false) + fmt.Sprintf(`
data "ataila_project_backup_sets" "shop" {
  project_id = %q
}
`, pid),
				ExpectError: words("project-backup-read-global"),
			},
		},
	})
}
