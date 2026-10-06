// Copyright (c) 2026 ATAILA Kft.
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/ataila/terraform-provider-ataila/internal/acctest"
	"github.com/ataila/terraform-provider-ataila/internal/client"
)

const tenantQuotaAddr = "ataila_tenant_quota.test"

// ordersMock is a mock whose token holds the orders keys, on a platform
// release that serves tenant quotas, the catalogue and orders.
func ordersMock(t *testing.T) *acctest.MockAPI {
	t.Helper()
	m := newMock(t)
	m.SetScopes(append(acctest.DefaultScopes(), "orders-admin-global", "orders-read-global")...)
	m.SetMeta(func(meta map[string]any) { meta["platform_version"] = client.ReleaseQuotasOrders })
	return m
}

// releaseBelow is the platform release just before r ("1.0.240" -> "1.0.239").
func releaseBelow(t *testing.T, r string) string {
	t.Helper()
	parts := strings.Split(r, ".")
	n, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil || n == 0 {
		t.Fatalf("cannot count below release %q", r)
	}
	parts[len(parts)-1] = strconv.Itoa(n - 1)
	return strings.Join(parts, ".")
}

// tenantQuotaHCL is an ataila_tenant_quota named "test" with the given
// `quota` entries ("ai_tpm = { limit = 200000 }").
func tenantQuotaHCL(tenantID string, entries ...string) string {
	return providerBlock(false) + fmt.Sprintf(`
resource "ataila_tenant_quota" "test" {
  tenant_id = %q
  quota = {
%s
  }
}
`, tenantID, indent(entries))
}

// mockQuotaSet checks the tenant's stored set ("dimension" -> "limit/policy").
func mockQuotaSet(m *acctest.MockAPI, tenantID string, want map[string]string) resource.TestCheckFunc {
	return check(func() error {
		if got := m.TenantQuotaSet(tenantID); fmt.Sprint(got) != fmt.Sprint(want) {
			return fmt.Errorf("mock quota set = %v, want %v", got, want)
		}
		return nil
	})
}

// The quota set: created over a set the portal had (which the resource
// replaces whole), changed, a dimension dropped (removed on the platform,
// with a plan warning), drift from the portal put back, imported; destroy
// leaves the tenant without limits.
func TestAccTenantQuota_Lifecycle(t *testing.T) {
	m := ordersMock(t)
	_, tenantID := m.AddCustomer("EXAMPLE", "example")
	m.SetTenantQuota(tenantID, "vcpu", 16, "auto") // set on the portal before Terraform
	providers, rec := recordingProvider()
	tpm := `ai_tpm = { limit = 200000 }`
	budget := `ai_budget_eur_month = { limit = 500.5, policy = "hard_cap", note = "demo" }`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: providers,
		CheckDestroy: func(*terraform.State) error {
			if got := m.TenantQuotaSet(tenantID); len(got) != 0 {
				return fmt.Errorf("after destroy the tenant still has limits: %v", got)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: tenantQuotaHCL(tenantID, tpm, budget),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(tenantQuotaAddr, "id", tenantID),
					resource.TestCheckResourceAttr(tenantQuotaAddr, "tenant_name", "EXAMPLE Ltd"),
					resource.TestCheckResourceAttr(tenantQuotaAddr, "quota.%", "2"),
					resource.TestCheckResourceAttr(tenantQuotaAddr, "quota.ai_tpm.limit", "200000"),
					resource.TestCheckResourceAttr(tenantQuotaAddr, "quota.ai_tpm.policy", "auto"),
					resource.TestCheckNoResourceAttr(tenantQuotaAddr, "quota.ai_tpm.note"),
					resource.TestCheckResourceAttr(tenantQuotaAddr, "quota.ai_budget_eur_month.limit", "500.5"),
					resource.TestCheckResourceAttr(tenantQuotaAddr, "quota.ai_budget_eur_month.policy", "hard_cap"),
					resource.TestCheckResourceAttr(tenantQuotaAddr, "quota.ai_budget_eur_month.note", "demo"),
					resource.TestCheckResourceAttr(tenantQuotaAddr, "usage.#", "8"),
					resource.TestCheckResourceAttr(tenantQuotaAddr, "usage.0.dimension", "vcpu"),
					resource.TestCheckNoResourceAttr(tenantQuotaAddr, "usage.0.limit"), // vcpu lost its limit
					resource.TestCheckResourceAttr(tenantQuotaAddr, "usage.5.dimension", "ai_tpm"),
					resource.TestCheckResourceAttr(tenantQuotaAddr, "usage.5.remaining", "200000"),
					resource.TestCheckResourceAttr(tenantQuotaAddr, "warnings.#", "0"),
					mockQuotaSet(m, tenantID, map[string]string{"ai_tpm": "200000/auto",
						"ai_budget_eur_month": "500.5/hard_cap"}),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// A changed limit is an in-place update.
				Config: tenantQuotaHCL(tenantID, `ai_tpm = { limit = 250000 }`, budget),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(tenantQuotaAddr, plancheck.ResourceActionUpdate)},
				},
				Check: mockQuotaSet(m, tenantID, map[string]string{"ai_tpm": "250000/auto",
					"ai_budget_eur_month": "500.5/hard_cap"}),
			},
			{
				// A dimension left out loses its limit on the platform; the plan warns.
				Config: tenantQuotaHCL(tenantID, `ai_tpm = { limit = 250000 }`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(tenantQuotaAddr, "quota.%", "1"),
					mockQuotaSet(m, tenantID, map[string]string{"ai_tpm": "250000/auto"}),
					rec.expectWarning("The apply removes limits: ai_budget_eur_month"),
				),
			},
			{
				// A limit set on the portal meanwhile is drift: the plan removes it.
				PreConfig:          func() { m.SetTenantQuota(tenantID, "gpu", 2, "hard_cap") },
				Config:             tenantQuotaHCL(tenantID, `ai_tpm = { limit = 250000 }`),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: tenantQuotaHCL(tenantID, `ai_tpm = { limit = 250000 }`),
				Check:  mockQuotaSet(m, tenantID, map[string]string{"ai_tpm": "250000/auto"}),
			},
			{
				ResourceName: tenantQuotaAddr, ImportState: true, ImportStateId: tenantID,
				ImportStateVerify: true,
			},
		},
	})
}

// The platform keeps four decimal places: a limit with more is stored
// rounded, and the state keeps the configuration's value, so the next plan is
// empty. An empty map removes every limit.
func TestAccTenantQuota_RoundingAndEmptySet(t *testing.T) {
	m := ordersMock(t)
	_, tenantID := m.AddCustomer("EXAMPLE", "example")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{
				Config: tenantQuotaHCL(tenantID, `ai_budget_eur_month = { limit = 10.123456 }`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(tenantQuotaAddr, "quota.ai_budget_eur_month.limit", "10.123456"),
					mockQuotaSet(m, tenantID, map[string]string{"ai_budget_eur_month": "10.1235/auto"}),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				Config: tenantQuotaHCL(tenantID),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(tenantQuotaAddr, "quota.%", "0"),
					mockQuotaSet(m, tenantID, map[string]string{}),
				),
			},
		},
	})
}

// What the plan refuses before any request: an unknown dimension or policy, a
// limit out of range, a note too long, a tenant id that is not one.
func TestAccTenantQuota_Validation(t *testing.T) {
	m := ordersMock(t)
	_, tenantID := m.AddCustomer("EXAMPLE", "example")
	cases := []struct{ cfg, err string }{
		{tenantQuotaHCL(tenantID, `cores = { limit = 4 }`), "cores"},
		{tenantQuotaHCL(tenantID, `vcpu = { limit = -1 }`), "between"},
		{tenantQuotaHCL(tenantID, `vcpu = { limit = 10000000000 }`), "between"},
		{tenantQuotaHCL(tenantID, `vcpu = { limit = 4, policy = "sometimes" }`), "sometimes"},
		{tenantQuotaHCL(tenantID, fmt.Sprintf(`vcpu = { limit = 4, note = %q }`, strings.Repeat("x", 501))), "500"},
		{tenantQuotaHCL("not-a-tenant", `vcpu = { limit = 4 }`), "must be a tenant id"},
	}
	var steps []resource.TestStep
	for _, c := range cases {
		steps = append(steps, resource.TestStep{Config: c.cfg, PlanOnly: true, ExpectError: words(c.err)})
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: protoV6, Steps: steps})
	if n := m.Calls("PUT", "/tenants/"); n != 0 {
		t.Errorf("%d quota requests for configurations the plan refuses", n)
	}
}

// The per-feature gate: on a platform release before the one that serves
// tenant quotas (still at or above the provider's minimum) the resource and
// the data source are refused at plan time, before any request; from that
// release on the same configuration applies.
func TestAccTenantQuota_PlatformRelease(t *testing.T) {
	below := releaseBelow(t, client.ReleaseQuotasOrders)
	t.Run(below+" refused", func(t *testing.T) {
		m := ordersMock(t)
		m.ServeQuotasOrders(false)
		m.SetMeta(func(meta map[string]any) { meta["platform_version"] = below })
		_, tenantID := m.AddCustomer("EXAMPLE", "example")
		resource.Test(t, resource.TestCase{
			ProtoV6ProviderFactories: protoV6,
			Steps: []resource.TestStep{
				{
					Config:      tenantQuotaHCL(tenantID, `ai_tpm = { limit = 200000 }`),
					ExpectError: words("ataila_tenant_quota needs platform release " + client.ReleaseQuotasOrders + " or later"),
				},
				{
					Config:      providerBlock(false) + fmt.Sprintf("data \"ataila_tenant_quota\" \"q\" {\n  tenant_id = %q\n}\n", tenantID),
					ExpectError: words("This platform is release " + below),
				},
			},
		})
		if n := m.Calls("PUT", "/tenants/") + m.Calls("GET", "/tenants/"+tenantID+"/quotas"); n != 0 {
			t.Errorf("%d quota requests to a platform that does not serve them", n)
		}
	})
	t.Run(client.ReleaseQuotasOrders+" accepted", func(t *testing.T) {
		m := ordersMock(t)
		_, tenantID := m.AddCustomer("EXAMPLE", "example")
		resource.Test(t, resource.TestCase{
			ProtoV6ProviderFactories: protoV6,
			Steps: []resource.TestStep{{
				Config: tenantQuotaHCL(tenantID, `ai_tpm = { limit = 200000 }`),
				Check:  resource.TestCheckResourceAttr(tenantQuotaAddr, "quota.ai_tpm.limit", "200000"),
			}},
		})
	})
}

// Without orders-admin-global the platform refuses the replacement; the error
// names the key, and nothing is stored.
func TestAccTenantQuota_NeedsTheOrdersKey(t *testing.T) {
	m := ordersMock(t)
	m.SetScopes(append(acctest.DefaultScopes(), "orders-read-global")...)
	_, tenantID := m.AddCustomer("EXAMPLE", "example")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config:      tenantQuotaHCL(tenantID, `ai_tpm = { limit = 200000 }`),
			ExpectError: words("orders-admin-global"),
		}},
	})
	if got := m.TenantQuotaSet(tenantID); len(got) != 0 {
		t.Errorf("a refused replacement stored %v", got)
	}
}

// A tenant that does not exist is the platform's 404, explained.
func TestAccTenantQuota_NoSuchTenant(t *testing.T) {
	ordersMock(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config:      tenantQuotaHCL("00000000-0000-4000-8000-00000000dead", `ai_tpm = { limit = 1 }`),
			ExpectError: words("No such tenant"),
		}},
	})
}

// The data source reads the same set, with who set each limit and when, and
// the counts' caveats.
func TestAccTenantQuota_DataSource(t *testing.T) {
	m := ordersMock(t)
	_, tenantID := m.AddCustomer("EXAMPLE", "example")
	held := 50000.0
	m.SetTenantAllocation(tenantID, "ai_tpm", &held)
	m.SetTenantAllocation(tenantID, "gpu", nil)
	ds := "data.ataila_tenant_quota.q"
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config: tenantQuotaHCL(tenantID, `ai_tpm = { limit = 200000, note = "demo" }`) + `
data "ataila_tenant_quota" "q" {
  tenant_id = ataila_tenant_quota.test.tenant_id
}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(ds, "tenant_name", "EXAMPLE Ltd"),
				resource.TestCheckResourceAttr(ds, "quota.%", "1"),
				resource.TestCheckResourceAttr(ds, "quota.ai_tpm.limit", "200000"),
				resource.TestCheckResourceAttr(ds, "quota.ai_tpm.note", "demo"),
				resource.TestCheckResourceAttr(ds, "quota.ai_tpm.set_by", "00000000-0000-4000-8000-000000000001"),
				resource.TestCheckResourceAttrSet(ds, "quota.ai_tpm.set_at"),
				resource.TestCheckResourceAttr(ds, "usage.5.allocated", "50000"),
				resource.TestCheckResourceAttr(ds, "usage.5.remaining", "150000"),
				resource.TestCheckNoResourceAttr(ds, "usage.7.allocated"),
				resource.TestCheckResourceAttr(ds, "notes.#", "1"),
				resource.TestCheckResourceAttr(ds, "notes.0", "The gpu allocation cannot be counted."),
			),
		}},
	})
}
