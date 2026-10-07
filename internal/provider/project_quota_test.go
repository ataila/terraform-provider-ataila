// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"

	"github.com/ataila/terraform-provider-ataila/internal/acctest"
)

// quotaScopes are the mock token's scopes plus the quota editor's key.
func quotaScopes(m *acctest.MockAPI) {
	m.SetScopes(append(acctest.DefaultScopes(), "k8s-gpu-admin-global", "k8s-gpu-read-global")...)
}

// envHCL is one environment's block of k8s_quota; quotaHCL wraps them.
func envHCL(env string, lines ...string) string {
	return fmt.Sprintf("  %s = {\n  %s\n  }", env, strings.ReplaceAll(indent(lines), "\n", "\n  "))
}

func quotaHCL(envs ...string) string {
	return "k8s_quota = {\n" + strings.Join(envs, "\n") + "\n}"
}

var prodGPU = envHCL("prod", `pods          = "25"`, `gpu_exclusive = "1"`, `gpu_borrow    = "1"`,
	`reason        = "training"`)
var prodGPU40 = strings.Replace(prodGPU, `"25"`, `"40"`, 1)

// The same quota with a new reason, then with `pods` spelled with a leading zero.
var prodGPU40Re = strings.Replace(prodGPU40, `"training"`, `"retraining"`, 1)
var prodGPU040Re = strings.Replace(prodGPU40Re, `"40"`, `"040"`, 1)

// The namespace quota (k8s_quota): set at create, changed, kept when left
// out, read by the data source, imported; and the platform's refusal of a
// GPU key without the GPU queue is an apply error that names the environment
// (an update, so the project is not tainted).
func TestAccProjectResource_K8sQuota(t *testing.T) {
	m := newMock(t)
	quotaScopes(m)
	m.SetGPUQueueCluster("prod-k8s", 1)
	_, tenantID := m.AddCustomer("EXAMPLE", "example")
	var id string
	var patches int
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		CheckDestroy:             projectStatus(m, &id, "retired"),
		Steps: []resource.TestStep{
			{
				// Create with a quota: the create, then one PATCH for prod.
				Config: projectHCL(false, tenantID, "shop", quotaHCL(prodGPU)),
				Check: resource.ComposeAggregateTestCheckFunc(
					stateAttr(projectAddr, "id", &id),
					resource.TestCheckResourceAttr(projectAddr, "k8s_quota.prod.pods", "25"),
					resource.TestCheckResourceAttr(projectAddr, "k8s_quota.prod.gpu_exclusive", "1"),
					resource.TestCheckResourceAttr(projectAddr, "k8s_quota.prod.gpu_borrow", "1"),
					resource.TestCheckResourceAttr(projectAddr, "k8s_quota.prod.fair_weight", "1"),
					resource.TestCheckResourceAttr(projectAddr, "k8s_quota.prod.gpu_enabled", "true"),
					resource.TestCheckResourceAttr(projectAddr, "k8s_quota.prod.gpu_queue", "true"),
					resource.TestCheckResourceAttr(projectAddr, "k8s_quota.prod.cluster", "prod-k8s"),
					resource.TestCheckResourceAttr(projectAddr, "k8s_quota.prod.namespace", "shop-prod"),
					resource.TestCheckResourceAttr(projectAddr, "k8s_quota.prod.tier", "prod"),
					resource.TestCheckResourceAttr(projectAddr, "k8s_quota.prod.overridden", "true"),
					resource.TestCheckResourceAttr(projectAddr, "k8s_quota.prod.overridden_keys.#", "3"),
					resource.TestCheckResourceAttr(projectAddr, "k8s_quota.prod.reason", "training"),
					resource.TestCheckResourceAttr(projectAddr, "k8s_quota.prod.req_cpu", "8"),
					resource.TestCheckResourceAttr(projectAddr, "k8s_quota.dev.pods", "20"),
					resource.TestCheckResourceAttr(projectAddr, "k8s_quota.dev.overridden", "false"),
					resource.TestCheckResourceAttr(projectAddr, "k8s_quota.dev.gpu_queue", "false"),
					resource.TestCheckResourceAttr(projectAddr, "k8s_quota.dev.gpu_enabled", "false"),
					resource.TestCheckNoResourceAttr(projectAddr, "k8s_quota.dev.reason"),
					resource.TestCheckResourceAttr(projectAddr, "k8s_quota.uat.tier", "nonprod"),
					resource.TestCheckResourceAttr(projectAddr, "warnings.#", "0"),
					check(func() error {
						got := m.ProjectQuota(id, "prod")
						want := map[string]string{"pods": "25", "gpu_exclusive": "1", "gpu_borrow": "1"}
						if fmt.Sprint(got) != fmt.Sprint(want) {
							return fmt.Errorf("mock prod quota = %v, want %v", got, want)
						}
						if m.ProjectQuota(id, "dev") != nil {
							return fmt.Errorf("mock dev quota = %v, want none", m.ProjectQuota(id, "dev"))
						}
						return nil
					}),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// A changed key is an update of exactly that key; the others stay.
				Config: projectHCL(false, tenantID, "shop", quotaHCL(prodGPU40)),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(projectAddr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(projectAddr, "k8s_quota.prod.pods", "40"),
					resource.TestCheckResourceAttr(projectAddr, "k8s_quota.prod.gpu_exclusive", "1"),
					check(func() error {
						if got := m.ProjectQuota(id, "prod")["pods"]; got != "40" {
							return fmt.Errorf("mock prod pods = %q, want 40", got)
						}
						return nil
					}),
				),
			},
			{
				// A new reason alone is an update too: the platform records the
				// reason on the override, so the overridden keys go again,
				// unchanged, with it.
				Config: projectHCL(false, tenantID, "shop", quotaHCL(prodGPU40Re)),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(projectAddr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(projectAddr, "k8s_quota.prod.reason", "retraining"),
					resource.TestCheckResourceAttr(projectAddr, "k8s_quota.prod.pods", "40"),
					check(func() error {
						if got := m.ProjectQuotaReason(id, "prod"); got != "retraining" {
							return fmt.Errorf("mock prod reason = %q, want retraining", got)
						}
						got := m.ProjectQuota(id, "prod")
						want := map[string]string{"pods": "40", "gpu_exclusive": "1", "gpu_borrow": "1"}
						if fmt.Sprint(got) != fmt.Sprint(want) {
							return fmt.Errorf("mock prod quota = %v, want %v", got, want)
						}
						return nil
					}),
				),
			},
			{
				// A count spelled with a leading zero is the same value to the
				// platform: nothing is sent, the state keeps the configuration's
				// spelling, and the next plan is empty.
				PreConfig: func() { patches = m.QuotaPatches() },
				Config:    projectHCL(false, tenantID, "shop", quotaHCL(prodGPU040Re)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(projectAddr, "k8s_quota.prod.pods", "040"),
					check(func() error {
						if got := m.QuotaPatches(); got != patches {
							return fmt.Errorf("quota PATCHes = %d, want %d (none for a respelling)", got, patches)
						}
						return nil
					}),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// Left out entirely: the quota keeps its values and the plan is empty.
				Config:   projectHCL(false, tenantID, "shop"),
				PlanOnly: true,
			},
			{
				// A second environment, with the default reason, and the data source.
				Config: projectHCL(false, tenantID, "shop", quotaHCL(prodGPU40, envHCL("dev", `req_mem = "12Gi"`))) + `
data "ataila_project" "q" {
  id = ataila_project.test.id
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(projectAddr, "k8s_quota.dev.req_mem", "12Gi"),
					resource.TestCheckResourceAttr(projectAddr, "k8s_quota.dev.overridden_keys.0", "req_mem"),
					resource.TestCheckNoResourceAttr(projectAddr, "k8s_quota.dev.reason"), // the default was sent, not recorded
					resource.TestCheckResourceAttr(projectAddr, "k8s_quota.prod.pods", "40"),
					resource.TestCheckResourceAttr("data.ataila_project.q", "k8s_quota.prod.pods", "40"),
					resource.TestCheckResourceAttr("data.ataila_project.q", "k8s_quota.prod.gpu_exclusive", "1"),
					resource.TestCheckResourceAttr("data.ataila_project.q", "k8s_quota.dev.req_mem", "12Gi"),
					resource.TestCheckResourceAttr("data.ataila_project.q", "k8s_quota.dev.cluster", "nonprod-k8s"),
					resource.TestCheckNoResourceAttr("data.ataila_project.q", "k8s_quota.dev.reason"),
				),
			},
			{
				ResourceName: projectAddr, ImportState: true, ImportStateVerify: true,
				// The platform does not report the reason; an import has none.
				ImportStateVerifyIgnore: []string{"warnings", "k8s_quota.prod.reason", "k8s_quota.dev.reason"},
			},
			{
				// A GPU key on an environment whose cluster runs no GPU queue: the
				// platform's 422, as an apply error naming the environment. The
				// same apply renames the project, which the platform has done by
				// then: the state records the rename and the quota as the
				// platform holds it, not the plan.
				Config: withLongName(projectHCL(false, tenantID, "shop",
					quotaHCL(prodGPU40, envHCL("dev", `req_mem = "12Gi"`, `gpu_borrow = "1"`))), "Example Shop Two"),
				ExpectError: words("refused the dev quota"),
			},
			{
				// So without the refused key nothing is left to do.
				Config: withLongName(projectHCL(false, tenantID, "shop",
					quotaHCL(prodGPU40, envHCL("dev", `req_mem = "12Gi"`))), "Example Shop Two"),
				PlanOnly: true,
			},
			{PreConfig: allowDestroyEverywhere(m), Config: withLongName(projectHCL(true, tenantID, "shop",
				quotaHCL(prodGPU40, envHCL("dev", `req_mem = "12Gi"`))), "Example Shop Two")},
		},
	})
}

// The cohort guard: with one tenant card held by another project's floor, a
// floor is the platform's 409 (on an update, so nothing is tainted) and a pure
// borrower is fine.
func TestAccProjectResource_K8sQuotaCohort(t *testing.T) {
	m := newMock(t)
	quotaScopes(m)
	m.SetGPUQueueCluster("prod-k8s", 1)
	_, tenantID := m.AddCustomer("EXAMPLE", "example")
	holder := m.AddTestProject(tenantID, "first", "k8s")
	m.SetProjectQuota(holder, "prod", map[string]string{"gpu_exclusive": "1"})
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{Config: projectHCL(false, tenantID, "shop")},
			{
				Config:      projectHCL(false, tenantID, "shop", quotaHCL(envHCL("prod", `gpu_exclusive = "1"`, `reason = "also training"`))),
				ExpectError: words("conflicts with the cluster's capacity"),
			},
			{
				Config: projectHCL(false, tenantID, "shop", quotaHCL(envHCL("prod", `gpu_borrow = "1"`, `reason = "borrows when idle"`))),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(projectAddr, "k8s_quota.prod.gpu_exclusive", "0"),
					resource.TestCheckResourceAttr(projectAddr, "k8s_quota.prod.gpu_borrow", "1"),
					resource.TestCheckResourceAttr(projectAddr, "k8s_quota.prod.gpu_enabled", "true"),
				),
			},
			{PreConfig: allowDestroyEverywhere(m), Config: projectHCL(true, tenantID, "shop",
				quotaHCL(envHCL("prod", `gpu_borrow = "1"`, `reason = "borrows when idle"`)))},
		},
	})
}

// A VM project has no namespace quota: k8s_quota on one fails the plan, and a
// read of one carries no k8s_quota at all.
func TestAccProjectResource_K8sQuotaVMProject(t *testing.T) {
	m := newMock(t)
	quotaScopes(m)
	_, tenantID := m.AddCustomer("EXAMPLE", "example")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{
				Config: projectHCL(false, tenantID, "vmshop", `deployment_backend = "vm"`,
					quotaHCL(envHCL("prod", `pods = "25"`, `reason = "x"`))),
				ExpectError: words("Only a Kubernetes project has a namespace quota"),
			},
			{
				Config: projectHCL(false, tenantID, "vmshop", `deployment_backend = "vm"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(projectAddr, "deployment_backend", "vm"),
					resource.TestCheckNoResourceAttr(projectAddr, "k8s_quota"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{PreConfig: allowDestroyEverywhere(m), Config: projectHCL(true, tenantID, "vmshop", `deployment_backend = "vm"`)},
		},
	})
}

// A platform release older than k8s_quota: the project reads without one, and
// adding k8s_quota to an existing project fails at plan time, saying why,
// before anything is sent.
func TestAccProjectResource_K8sQuotaOlderPlatform(t *testing.T) {
	m := newMock(t)
	quotaScopes(m)
	m.ServeProjectQuota(false)
	_, tenantID := m.AddCustomer("EXAMPLE", "example")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{
				Config: projectHCL(false, tenantID, "shop"),
				Check:  resource.TestCheckNoResourceAttr(projectAddr, "k8s_quota"),
			},
			{
				Config:      projectHCL(false, tenantID, "shop", quotaHCL(envHCL("prod", `pods = "25"`))),
				ExpectError: words("reports no namespace quota"),
			},
			{PreConfig: allowDestroyEverywhere(m), Config: projectHCL(true, tenantID, "shop")},
		},
	})
}

// The per-feature gate: on a platform before 1.0.203 (still at or above the
// provider's minimum, 1.0.187) a project without k8s_quota works, and one that
// sets k8s_quota is refused at plan time, on create, before any request; from
// 1.0.203 on the same configuration applies.
func TestAccProjectResource_K8sQuotaPlatformRelease(t *testing.T) {
	t.Run("1.0.202 refused", func(t *testing.T) {
		m := newMock(t)
		quotaScopes(m)
		m.ServeProjectQuota(false)
		m.SetMeta(func(meta map[string]any) { meta["platform_version"] = "1.0.202" })
		_, tenantID := m.AddCustomer("EXAMPLE", "example")
		resource.Test(t, resource.TestCase{
			ProtoV6ProviderFactories: protoV6,
			Steps: []resource.TestStep{
				{
					Config:      projectHCL(false, tenantID, "shop", quotaHCL(envHCL("prod", `pods = "25"`))),
					ExpectError: words("k8s_quota needs platform release 1.0.203 or later"),
				},
				{
					// Without k8s_quota the project is as before: the core minimum is 1.0.187.
					Config: projectHCL(false, tenantID, "shop"),
					Check:  resource.TestCheckNoResourceAttr(projectAddr, "k8s_quota"),
				},
				{
					Config:      projectHCL(false, tenantID, "shop", quotaHCL(envHCL("prod", `pods = "25"`))),
					ExpectError: words("This platform is release 1.0.202"),
				},
				{PreConfig: allowDestroyEverywhere(m), Config: projectHCL(true, tenantID, "shop")},
			},
		})
		if n := m.Calls("PATCH", "/projects/1/k8s-quota/prod"); n != 0 {
			t.Errorf("%d quota requests on a platform that does not serve them", n)
		}
	})
	t.Run("1.0.203 accepted", func(t *testing.T) {
		m := newMock(t)
		quotaScopes(m)
		m.SetMeta(func(meta map[string]any) { meta["platform_version"] = "1.0.203" })
		_, tenantID := m.AddCustomer("EXAMPLE", "example")
		resource.Test(t, resource.TestCase{
			ProtoV6ProviderFactories: protoV6,
			Steps: []resource.TestStep{
				{
					Config: projectHCL(false, tenantID, "shop", quotaHCL(envHCL("prod", `pods = "25"`))),
					Check:  resource.TestCheckResourceAttr(projectAddr, "k8s_quota.prod.pods", "25"),
				},
				{PreConfig: allowDestroyEverywhere(m), Config: projectHCL(true, tenantID, "shop")},
			},
		})
	})
}

// Without the quota editor's key a create-with-quota creates the project and
// then meets the platform's 403: the resource is tainted, the error says how
// to keep the project. The last step lets the replacement retire it and
// creates another, so the test can clean up.
func TestAccProjectResource_K8sQuotaNeedsTheGPUKey(t *testing.T) {
	m := newMock(t)
	m.SetGPUQueueCluster("prod-k8s", 1)
	_, tenantID := m.AddCustomer("EXAMPLE", "example")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{
				Config:      projectHCL(false, tenantID, "shop", quotaHCL(envHCL("prod", `pods = "25"`, `reason = "x"`))),
				ExpectError: words("k8s-gpu-admin-global"),
			},
			{PreConfig: allowDestroyEverywhere(m), Config: projectHCL(true, tenantID, "shop2")},
		},
	})
}

// An update whose read-back fails after its quota request went through still
// records what changed: the error fails the apply, and the next plan is empty
// — the settings change and the quota environment (its reason, which only the
// state holds, included) are in the state, not dropped with the read.
func TestAccProjectResource_K8sQuotaReadBackFails(t *testing.T) {
	m := newMock(t)
	quotaScopes(m)
	_, tenantID := m.AddCustomer("EXAMPLE", "example")
	quota := quotaHCL(envHCL("prod", `pods = "25"`, `reason = "more pods"`))
	var patches int
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{Config: projectHCL(false, tenantID, "shop")},
			{
				PreConfig:   m.FailReadAfterNextQuota,
				Config:      withLongName(projectHCL(false, tenantID, "shop", quota), "Example Shop Two"),
				ExpectError: words("after changing its quota"),
			},
			{
				PreConfig: func() { patches = m.QuotaPatches() },
				Config:    withLongName(projectHCL(false, tenantID, "shop", quota), "Example Shop Two"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(projectAddr, "long_name", "Example Shop Two"),
					resource.TestCheckResourceAttr(projectAddr, "k8s_quota.prod.pods", "25"),
					resource.TestCheckResourceAttr(projectAddr, "k8s_quota.prod.reason", "more pods"),
					check(func() error {
						if got := m.QuotaPatches(); got != patches {
							return fmt.Errorf("quota PATCHes = %d, want %d (nothing re-sent)", got, patches)
						}
						return nil
					}),
				),
			},
			{PreConfig: allowDestroyEverywhere(m), Config: projectHCL(true, tenantID, "shop", quota)},
		},
	})
}
