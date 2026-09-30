// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/ataila/terraform-provider-ataila/internal/acctest"
	"github.com/ataila/terraform-provider-ataila/internal/provider"
)

const (
	projectAddr      = "ataila_project.test"
	provisioningAddr = "ataila_project_provisioning.test"
	projectMemberAdr = "ataila_project_member.test"
)

// projectHCL is an ataila_project named "test" of the given tenant, with
// extra attribute lines.
func projectHCL(allowDestroy bool, tenantID, short string, extra ...string) string {
	return providerBlock(allowDestroy) + fmt.Sprintf(`
resource "ataila_project" "test" {
  tenant_id        = %q
  short_name       = %q
  gitlab_repo_slug = "%s-app"
  primary_domain   = "%s.example.com"
  long_name        = "Example Shop"
%s
}
`, tenantID, short, short, short, indent(extra))
}

// provisioningHCL adds an ataila_project_provisioning of that project.
func provisioningHCL(extra ...string) string {
	return fmt.Sprintf(`
resource "ataila_project_provisioning" "test" {
  project_id = ataila_project.test.id
%s
}
`, indent(extra))
}

// withLongName replaces the long_name of a projectHCL.
func withLongName(hcl, long string) string {
	return strings.Replace(hcl, `long_name        = "Example Shop"`, fmt.Sprintf(`long_name        = %q`, long), 1)
}

// projectStatus checks a project's status in the mock.
func projectStatus(m *acctest.MockAPI, id *string, want string) resource.TestCheckFunc {
	return mockField(m.Project, id, "status", want)
}

// noIPv4 fails when any attribute of the resource holds an IPv4 literal: the
// outputs never name an internal address.
func noIPv4(addr string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[addr]
		if !ok {
			return fmt.Errorf("%s is not in the state", addr)
		}
		for k, v := range rs.Primary.Attributes {
			if provider.HasIPv4(v) {
				return fmt.Errorf("%s.%s holds an IPv4 literal: %q", addr, k, v)
			}
		}
		return nil
	}
}

func TestHasIPv4(t *testing.T) {
	// Private addresses are built at run time: the leak guard refuses them as literals.
	for s, want := range map[string]bool{
		"10." + "0.0.1": true, "https://192." + "168.1.20:8443/x": true, "host 172." + "16.4.10": true,
		"203.0.113.7": true, "https://app.example.com": false, "v1.0.162": false, "1.2.3": false,
		"projects/example/dev": false, "2026-09-30T10:00:00Z": false, "1.2.3.4.5": false,
	} {
		if got := provider.HasIPv4(s); got != want {
			t.Errorf("HasIPv4(%q) = %v, want %v", s, got, want)
		}
	}
}

// Create a record (planned, nothing provisioned), change it in place, import
// it by id and by short name, and retire it with both switches on.
func TestAccProjectResource_Lifecycle(t *testing.T) {
	m := newMock(t)
	customerID, tenantID := m.AddCustomer("EXAMPLE", "example")
	var id string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		CheckDestroy:             projectStatus(m, &id, "retired"),
		Steps: []resource.TestStep{
			{
				Config: projectHCL(false, tenantID, "shop"),
				Check: resource.ComposeAggregateTestCheckFunc(
					stateAttr(projectAddr, "id", &id),
					resource.TestCheckResourceAttr(projectAddr, "customer_id", customerID),
					resource.TestCheckResourceAttr(projectAddr, "project_index", "4"),
					resource.TestCheckResourceAttr(projectAddr, "status", "planned"),
					resource.TestCheckResourceAttr(projectAddr, "is_self", "false"),
					resource.TestCheckResourceAttr(projectAddr, "deployment_backend", "k8s"),
					resource.TestCheckResourceAttr(projectAddr, "network_only", "false"),
					resource.TestCheckResourceAttr(projectAddr, "frontend_variant", "react"),
					resource.TestCheckResourceAttr(projectAddr, "enable_static_site", "true"),
					resource.TestCheckResourceAttr(projectAddr, "prod_minio_node_count", "2"),
					resource.TestCheckNoResourceAttr(projectAddr, "description"),
					resource.TestCheckResourceAttr(projectAddr, "stale_stages.#", "0"),
					resource.TestCheckResourceAttr(projectAddr, "urls.frontend", "https://app.shop.example.com"),
					resource.TestCheckNoResourceAttr(projectAddr, "urls.ai"),
					resource.TestCheckResourceAttr(projectAddr, "harbor_namespace", "example-shop"),
					resource.TestCheckResourceAttr(projectAddr, "gitlab_repositories.#", "2"),
					resource.TestCheckResourceAttr(projectAddr, "gitlab_repositories.0.path", "example/shop-app"),
					resource.TestCheckResourceAttr(projectAddr, "kubernetes_namespaces.#", "3"),
					resource.TestCheckResourceAttr(projectAddr, "vault_paths.#", "2"),
					resource.TestCheckResourceAttr(projectAddr, "warnings.#", "0"),
					resource.TestCheckResourceAttrSet(projectAddr, "created_at"),
					noIPv4(projectAddr),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				Config: withLongName(projectHCL(false, tenantID, "shop", `enable_ai   = true`,
					`description = "The shop"`), "Example Shop 2"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(projectAddr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPtr(projectAddr, "id", &id),
					mockField(m.Project, &id, "long_name", "Example Shop 2"),
					mockField(m.Project, &id, "description", "The shop"),
					resource.TestCheckResourceAttr(projectAddr, "urls.ai", "https://ai.shop.example.com"),
					// Never provisioned: nothing is stale.
					resource.TestCheckResourceAttr(projectAddr, "stale_stages.#", "0"),
				),
			},
			{
				// Leaving description out clears it; the other settings keep their values.
				Config: withLongName(projectHCL(false, tenantID, "shop"), "Example Shop 2"),
				Check: resource.ComposeAggregateTestCheckFunc(
					mockField(m.Project, &id, "description", nil),
					resource.TestCheckResourceAttr(projectAddr, "enable_ai", "true"),
				),
			},
			{ResourceName: projectAddr, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"warnings"}},
			{ResourceName: projectAddr, ImportState: true, ImportStateId: "short_name:shop", ImportStateVerify: true, ImportStateVerifyIgnore: []string{"warnings"}},
			{
				ResourceName: projectAddr, ImportState: true, ImportStateId: "short_name:none",
				ExpectError: words("0 projects have short_name \"none\""),
			},
			{
				// A primary_domain differing only in letter case is no change.
				Config: strings.Replace(withLongName(projectHCL(false, tenantID, "shop"), "Example Shop 2"),
					`"shop.example.com"`, `"Shop.Example.COM"`, 1),
				PlanOnly: true,
			},
			{PreConfig: allowDestroyEverywhere(m), Config: withLongName(projectHCL(true, tenantID, "shop"), "Example Shop 2")},
		},
	})
}

// Every frozen key fails the plan, naming the key; none turns into a
// replacement.
func TestAccProjectResource_FrozenKeys(t *testing.T) {
	m := newMock(t)
	_, tenantID := m.AddCustomer("EXAMPLE", "example")
	_, otherTenant := m.AddCustomer("OTHER", "other")
	base := func(extra ...string) string {
		return projectHCL(false, tenantID, "shop", append([]string{`project_index = 7`}, extra...)...)
	}
	frozen := func(key, cfg string) resource.TestStep {
		return resource.TestStep{
			Config:      cfg,
			ExpectError: regexp.MustCompile(`Cannot change ` + key + ` of an existing project`),
		}
	}
	steps := []resource.TestStep{{
		Config: base(),
		Check:  resource.TestCheckResourceAttr(projectAddr, "project_index", "7"),
	}}
	steps = append(steps,
		frozen("project_index", strings.Replace(base(), "project_index = 7", "project_index = 8", 1)),
		frozen("short_name", strings.Replace(base(), `short_name       = "shop"`, `short_name       = "shop2"`, 1)),
		frozen("gitlab_repo_slug", strings.Replace(base(), `"shop-app"`, `"shop-web"`, 1)),
		frozen("primary_domain", strings.Replace(base(), `"shop.example.com"`, `"shop.example.org"`, 1)),
		frozen("tenant_id", strings.Replace(base(), tenantID, otherTenant, 1)),
		frozen("deployment_backend", base(`deployment_backend = "vm"`)),
		frozen("network_only", base(`network_only = true`)),
		resource.TestStep{PreConfig: allowDestroyEverywhere(m), Config: projectHCL(true, tenantID, "shop", `project_index = 7`)},
	)
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: protoV6, Steps: steps})
}

// Destroy retires, behind both switches; a retired project refuses changes
// at plan time, and destroying it only forgets it.
func TestAccProjectResource_RetireAndRetired(t *testing.T) {
	m := newMock(t)
	_, tenantID := m.AddCustomer("EXAMPLE", "example")
	var id string
	cfg := func(allow bool, extra ...string) string { return projectHCL(allow, tenantID, "shop", extra...) }
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{Config: cfg(false), Check: stateAttr(projectAddr, "id", &id)},
			{
				Config: providerBlock(false), Destroy: true,
				ExpectError: words("Destroying a project is not allowed .* destroy means retire"),
			},
			{
				// The provider allows it, the token does not.
				Config: providerBlock(true), Destroy: true,
				ExpectError: words("The token was not created with allow_destroy"),
			},
			{
				// Retired outside the configuration: a change fails the plan.
				PreConfig:   func() { m.Retire(id) },
				Config:      cfg(false, `enable_ai = true`),
				ExpectError: words("The project is retired"),
			},
			{
				// Destroying a retired project needs no switch: it only forgets it.
				Config:  cfg(false),
				Destroy: true,
				Check:   projectStatus(m, &id, "retired"),
			},
		},
	})
}

// The short name of a retired project stays reserved.
func TestAccProjectResource_RetiredShortNameStaysTaken(t *testing.T) {
	m := newMock(t)
	_, tenantID := m.AddCustomer("EXAMPLE", "example")
	var id string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		PreCheck:                 allowDestroyEverywhere(m),
		Steps: []resource.TestStep{
			{Config: projectHCL(true, tenantID, "shop"), Check: stateAttr(projectAddr, "id", &id)},
			{Config: providerBlock(true), Check: projectStatus(m, &id, "retired")},
			{
				// Another repository, the same short name.
				Config:      strings.Replace(projectHCL(true, tenantID, "shop"), `"shop-app"`, `"shop-new"`, 1),
				ExpectError: words("short_name_taken"),
			},
		},
	})
}

// The platform's own projects: import refused; one that became is_self
// refuses changes and destroy at plan time.
func TestAccProjectResource_PlatformProjects(t *testing.T) {
	m := newMock(t)
	_, tenantID := m.AddCustomer("EXAMPLE", "example")
	platform := m.AddPlatformProject(tenantID, "portal")
	var id string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		PreCheck:                 allowDestroyEverywhere(m),
		Steps: []resource.TestStep{
			{
				Config:        projectHCL(true, tenantID, "portal"),
				ResourceName:  projectAddr,
				ImportState:   true,
				ImportStateId: platform,
				ExpectError:   words("The platform's own projects cannot be imported"),
			},
			{Config: projectHCL(true, tenantID, "shop"), Check: stateAttr(projectAddr, "id", &id)},
			{
				PreConfig:   func() { m.MarkPlatformProject(id, true) },
				Config:      projectHCL(true, tenantID, "shop", `enable_ai = true`),
				ExpectError: words("The platform's own projects are read-only"),
			},
			{
				Config: projectHCL(true, tenantID, "shop"), Destroy: true,
				ExpectError: words("The platform's own projects are read-only .* refuses to destroy"),
			},
			{
				// Not is_self any more, so the test can end with a retire.
				PreConfig: func() { m.MarkPlatformProject(id, false) },
				Config:    projectHCL(true, tenantID, "shop"),
			},
		},
	})
}

// Combinations the platform would refuse or override fail validation.
func TestAccProjectResource_ValidateConfig(t *testing.T) {
	m := newMock(t)
	_, tenantID := m.AddCustomer("EXAMPLE", "example")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{
				Config:      projectHCL(false, tenantID, "shop", `network_only = true`, `enable_static_site = true`),
				ExpectError: words("Not possible on a network-only project"),
			},
			{
				Config:      projectHCL(false, tenantID, "shop", `enable_mssql = true`),
				ExpectError: words("Windows workloads need the vm backend"),
			},
			{
				Config:      withLongName(projectHCL(false, tenantID, "shop"), "Joe's shop"),
				ExpectError: words("must not contain quotes, apostrophes, backslashes or control characters"),
			},
		},
	})
}

// ── provisioning ─────────────────────────────────────────────────────────────

func fastPoll(t *testing.T) {
	t.Cleanup(provider.SetProvisioningPollInterval(5 * time.Millisecond))
}

// Provision to converged; a change of the project leaves a stage stale, the
// provisioning plan then shows an in-place update, and applying it converges
// again by re-running only the stale stage.
func TestAccProjectProvisioning_ConvergeAndStale(t *testing.T) {
	fastPoll(t)
	m := newMock(t)
	_, tenantID := m.AddCustomer("EXAMPLE", "example")
	var id string
	cfg1 := projectHCL(false, tenantID, "shop") + provisioningHCL()
	cfg2 := withLongName(cfg1, "Example Shop Two")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{
				Config: cfg1,
				Check: resource.ComposeAggregateTestCheckFunc(
					stateAttr(projectAddr, "id", &id),
					resource.TestCheckResourceAttrPair(provisioningAddr, "id", projectAddr, "id"),
					resource.TestCheckResourceAttr(provisioningAddr, "converged", "true"),
					resource.TestCheckResourceAttr(provisioningAddr, "provisioned", "true"),
					resource.TestCheckResourceAttr(provisioningAddr, "simulated", "false"),
					resource.TestCheckResourceAttr(provisioningAddr, "dispatch_mode", "live"),
					resource.TestCheckResourceAttr(provisioningAddr, "state", "complete"),
					resource.TestCheckResourceAttr(provisioningAddr, "stages.#", "13"),
					resource.TestCheckResourceAttr(provisioningAddr, "stages.0.key", "prep:reserve-ipam"),
					resource.TestCheckResourceAttr(provisioningAddr, "stages.0.status", "success"),
					resource.TestCheckResourceAttr(provisioningAddr, "operation_id", "provision:1"),
					projectStatus(m, &id, "active"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// The change is recorded; the stage it affects becomes stale,
				// so the plan after it is not empty.
				Config:             cfg2,
				ExpectNonEmptyPlan: true,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(projectAddr, "stale_stages.#", "1"),
					resource.TestCheckResourceAttr(projectAddr, "stale_stages.0", "gitlab:populate-repo"),
				),
			},
			{
				Config: cfg2,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(provisioningAddr, plancheck.ResourceActionUpdate),
						plancheck.ExpectResourceAction(projectAddr, plancheck.ResourceActionNoop),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(provisioningAddr, "converged", "true"),
					resource.TestCheckResourceAttr(provisioningAddr, "stale_stages.#", "0"),
					resource.TestCheckResourceAttr(provisioningAddr, "operation_id", "provision:2"),
				),
			},
			{
				ResourceName: provisioningAddr, ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"operation_id", "timeouts"},
			},
			{
				// Destroy forgets: nothing is torn down, the project stays active.
				Config: withLongName(projectHCL(false, tenantID, "shop"), "Example Shop Two"),
				Check:  projectStatus(m, &id, "active"),
			},
			{PreConfig: allowDestroyEverywhere(m), Config: providerBlock(true)},
		},
	})
}

// Under simulate the project converges without being provisioned.
func TestAccProjectProvisioning_Simulate(t *testing.T) {
	fastPoll(t)
	m := newMock(t)
	m.SetDispatchMode("simulate")
	_, tenantID := m.AddCustomer("EXAMPLE", "example")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		PreCheck:                 allowDestroyEverywhere(m),
		Steps: []resource.TestStep{{
			Config: projectHCL(true, tenantID, "shop") + provisioningHCL(),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(provisioningAddr, "converged", "true"),
				resource.TestCheckResourceAttr(provisioningAddr, "provisioned", "false"),
				resource.TestCheckResourceAttr(provisioningAddr, "simulated", "true"),
				resource.TestCheckResourceAttr(provisioningAddr, "dispatch_mode", "simulate"),
				resource.TestCheckResourceAttr(provisioningAddr, "stages.3.simulated", "true"),
			),
			ConfigPlanChecks: resource.ConfigPlanChecks{
				PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
			},
		}},
	})
}

// Under dryrun the provider fails at once, before starting anything.
func TestAccProjectProvisioning_DryrunFailsFast(t *testing.T) {
	fastPoll(t)
	m := newMock(t)
	m.SetDispatchMode("dryrun")
	_, tenantID := m.AddCustomer("EXAMPLE", "example")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		PreCheck:                 allowDestroyEverywhere(m),
		Steps: []resource.TestStep{
			{
				Config:      projectHCL(true, tenantID, "shop") + provisioningHCL(),
				ExpectError: words("Provisioning can never finish on this platform .* dryrun"),
			},
			{
				Config: projectHCL(true, tenantID, "shop"),
				Check: check(func() error {
					if n := m.Calls("POST", "/projects/1/provisioning"); n != 0 {
						return fmt.Errorf("%d provisioning starts under dryrun, want 0", n)
					}
					return nil
				}),
			},
		},
	})
}

// A failed stage fails the apply and taints the resource; after the cause is
// fixed, the next apply replaces it (forget + start) and converges.
func TestAccProjectProvisioning_FailedStage(t *testing.T) {
	fastPoll(t)
	m := newMock(t)
	m.FailStage("gitlab:create-project", true)
	_, tenantID := m.AddCustomer("EXAMPLE", "example")
	cfg := projectHCL(true, tenantID, "shop") + provisioningHCL()
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		PreCheck:                 allowDestroyEverywhere(m),
		Steps: []resource.TestStep{
			{
				Config:      cfg,
				ExpectError: words("Provisioning failed .* stage gitlab:create-project"),
			},
			{
				PreConfig: func() { m.FailStage("gitlab:create-project", false) },
				Config:    cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(provisioningAddr, plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
				Check: resource.TestCheckResourceAttr(provisioningAddr, "converged", "true"),
			},
		},
	})
}

// A stage waiting for an operator keeps the provider waiting (with a
// warning); past the timeout the resource is tainted naming the stage, and
// the next apply adopts the run still going instead of starting another.
func TestAccProjectProvisioning_OperatorTimeoutAndAdopt(t *testing.T) {
	fastPoll(t)
	m := newMock(t)
	_, tenantID := m.AddCustomer("EXAMPLE", "example")
	factories, rec := recordingProvider()
	m.ManualStage("dns:add-domain", 3)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		PreCheck:                 allowDestroyEverywhere(m),
		Steps: []resource.TestStep{
			{
				// Waits three polls for the operator, then converges.
				Config: projectHCL(true, tenantID, "shop") + provisioningHCL(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(provisioningAddr, "converged", "true"),
					rec.expectWarning("A stage waited for an operator", "dns:add-domain"),
				),
			},
		},
	})

	m2 := newMock(t)
	_, tenant2 := m2.AddCustomer("EXAMPLE", "example")
	m2.ManualStage("dns:add-domain", -1)
	factories2, rec2 := recordingProvider()
	cfg := projectHCL(true, tenant2, "shop") + provisioningHCL(`timeouts {`, `  create = "2s"`, `}`)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories2,
		PreCheck:                 allowDestroyEverywhere(m2),
		Steps: []resource.TestStep{
			{
				Config:      cfg,
				ExpectError: words("Provisioning did not finish in time .* Last stage: dns:add-domain"),
			},
			{
				PreConfig: func() { m2.ResolveManual("1") },
				Config:    cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(provisioningAddr, plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(provisioningAddr, "converged", "true"),
					// The run that timed out was adopted, not a second one started.
					resource.TestCheckResourceAttr(provisioningAddr, "operation_id", "provision:1"),
					rec2.expectWarning("Provisioning was already running", "provision:1"),
				),
			},
		},
	})
}

// ── members ──────────────────────────────────────────────────────────────────

func projectMemberHCL(tenantID, userID string, extra ...string) string {
	return projectHCL(true, tenantID, "shop") + fmt.Sprintf(`
resource "ataila_project_member" "test" {
  project_id = ataila_project.test.id
  user_id    = %q
%s
}
`, userID, indent(extra))
}

func TestAccProjectMember_Lifecycle(t *testing.T) {
	m := newMock(t)
	_, tenantID := m.AddCustomer("EXAMPLE", "example")
	alice := m.AddUser("alice@example.com")
	outsider := m.AddUser("outsider@example.com")
	m.SetMembership(tenantID, alice, "member")
	var project string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		PreCheck:                 allowDestroyEverywhere(m),
		Steps: []resource.TestStep{
			{
				Config: projectMemberHCL(tenantID, alice),
				Check: resource.ComposeAggregateTestCheckFunc(
					stateAttr(projectAddr, "id", &project),
					resource.TestCheckResourceAttr(projectMemberAdr, "role", "developer"),
					resource.TestCheckNoResourceAttr(projectMemberAdr, "gitlab_role"),
					resource.TestCheckResourceAttrSet(projectMemberAdr, "created_at"),
					check(func() error {
						if r := m.ProjectMemberRole(project, alice); r != "developer" {
							return fmt.Errorf("mock role %q", r)
						}
						return nil
					}),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				Config: projectMemberHCL(tenantID, alice, `role        = "viewer"`, `gitlab_role = "reporter"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(projectMemberAdr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.TestCheckResourceAttr(projectMemberAdr, "gitlab_role", "reporter"),
			},
			{
				ResourceName: projectMemberAdr, ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return project + "/" + alice, nil },
			},
			{
				Config:      projectMemberHCL(tenantID, alice, `gitlab_role = "maintainer"`),
				ExpectError: words("maintainer is for platform staff only"),
			},
			{
				Config:      projectMemberHCL(tenantID, outsider),
				ExpectError: words("Only platform staff and active members of a tenant"),
			},
			{
				// A retired project takes no member changes, but removal works.
				PreConfig:   func() { m.Retire(project) },
				Config:      projectMemberHCL(tenantID, alice, `role = "admin"`),
				ExpectError: words("it takes no new members and no role changes"),
			},
			{
				Config: projectHCL(true, tenantID, "shop"),
				Check: check(func() error {
					if r := m.ProjectMemberRole(project, alice); r != "" {
						return fmt.Errorf("alice still a member (%q)", r)
					}
					return nil
				}),
			},
		},
	})
}

// ── data sources ─────────────────────────────────────────────────────────────

func TestAccProjectDataSources(t *testing.T) {
	fastPoll(t)
	m := newMock(t)
	customerID, tenantID := m.AddCustomer("EXAMPLE", "example")
	_, otherTenant := m.AddCustomer("OTHER", "other")
	m.AddPlatformProject(otherTenant, "portal")
	cfg := projectHCL(true, tenantID, "shop") + provisioningHCL() + fmt.Sprintf(`
data "ataila_project" "by_id" {
  id = ataila_project.test.id
  depends_on = [ataila_project_provisioning.test]
}
data "ataila_project" "by_name" {
  short_name = "portal"
}
data "ataila_projects" "customer" {
  customer_id = %q
  depends_on  = [ataila_project.test]
}
data "ataila_projects" "all" {
  depends_on = [ataila_project.test]
}
data "ataila_project_stages" "test" {
  project_id = ataila_project_provisioning.test.project_id
}
`, customerID)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		PreCheck:                 allowDestroyEverywhere(m),
		Steps: []resource.TestStep{{
			Config: cfg,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttrPair("data.ataila_project.by_id", "short_name", projectAddr, "short_name"),
				resource.TestCheckResourceAttr("data.ataila_project.by_id", "status", "active"),
				resource.TestCheckResourceAttr("data.ataila_project.by_id", "urls.frontend", "https://app.shop.example.com"),
				resource.TestCheckResourceAttr("data.ataila_project.by_id", "long_name", "Example Shop"),
				noIPv4("data.ataila_project.by_id"),
				resource.TestCheckResourceAttr("data.ataila_project.by_name", "is_self", "true"),
				resource.TestCheckResourceAttr("data.ataila_projects.customer", "projects.#", "1"),
				resource.TestCheckResourceAttr("data.ataila_projects.customer", "projects.0.short_name", "shop"),
				resource.TestCheckResourceAttr("data.ataila_projects.all", "projects.#", "2"),
				resource.TestCheckResourceAttr("data.ataila_project_stages.test", "state", "complete"),
				resource.TestCheckResourceAttr("data.ataila_project_stages.test", "percent", "100"),
				resource.TestCheckResourceAttr("data.ataila_project_stages.test", "stages.#", "15"),
				resource.TestCheckResourceAttr("data.ataila_project_stages.test", "stages.13.deferred", "true"),
				resource.TestCheckResourceAttr("data.ataila_project_stages.test", "stages.1.deps.0", "prep:reserve-ipam"),
				resource.TestCheckResourceAttrSet("data.ataila_project_stages.test", "stages.0.finished_at"),
			),
		}},
	})
}
