// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/ataila/terraform-provider-ataila/internal/acctest"
	"github.com/ataila/terraform-provider-ataila/internal/client"
)

const (
	rateCardAddr = "ataila_ai_rate_card.general"
	ratePlanAddr = "ataila_ai_rate_plan.test"
)

// aiMock is a mock whose token holds the orders keys too, on the platform
// release that serves AI usage and rates.
func aiMock(t *testing.T) *acctest.MockAPI {
	t.Helper()
	m := newMock(t)
	m.SetScopes(append(acctest.DefaultScopes(), "orders-admin-global", "orders-read-global")...)
	m.SetMeta(func(meta map[string]any) { meta["platform_version"] = client.ReleaseAIBilling })
	return m
}

func rateCardHCL(lines ...string) string {
	return providerBlock(false) + fmt.Sprintf(`
resource "ataila_ai_rate_card" "general" {
  tier = "general"
%s}
`, indent(lines))
}

func ratePlanHCL(tenantID string, extra string, entries ...string) string {
	return providerBlock(false) + fmt.Sprintf(`
resource "ataila_ai_rate_plan" "test" {
  tenant_id = %q
%s
  rates = {
%s
  }
}
`, tenantID, extra, indent(entries))
}

func mockCheck(f func() error) resource.TestCheckFunc { return check(f) }

func firstOfMonth() string { return acctest.AIToday()[:8] + "01" }

// The list rate of one tier: set (valid_from = today when left out), a second
// apply changes nothing, changed in place, drift from the portal put back,
// imported; destroy forgets (the rate stays on the platform).
func TestAccAIRateCard_Lifecycle(t *testing.T) {
	m := aiMock(t)
	today := acctest.AIToday()
	var puts int
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		CheckDestroy: func(*terraform.State) error {
			if got := m.ListRateInForce("general"); got != "0.6/1.5" {
				return fmt.Errorf("destroy must only forget the list rate; the platform has %q", got)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: rateCardHCL(`eur_per_1m_input  = 0.5`, `eur_per_1m_output = 1.5`, `note = "demo"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(rateCardAddr, "id", "general"),
					resource.TestCheckResourceAttr(rateCardAddr, "eur_per_1m_input", "0.5"),
					resource.TestCheckResourceAttr(rateCardAddr, "valid_from", today),
					resource.TestCheckNoResourceAttr(rateCardAddr, "valid_to"),
					resource.TestCheckResourceAttr(rateCardAddr, "note", "demo"),
					resource.TestCheckResourceAttr(rateCardAddr, "warnings.#", "0"),
					mockCheck(func() error {
						if got := m.ListRateInForce("general"); got != "0.5/1.5" {
							return fmt.Errorf("mock list rate = %q", got)
						}
						return nil
					}),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// The same configuration again: nothing to send.
				PreConfig: func() { puts = m.Calls("PUT", "/ai/rate-card/") },
				Config:    rateCardHCL(`eur_per_1m_input  = 0.5`, `eur_per_1m_output = 1.5`, `note = "demo"`),
				Check: mockCheck(func() error {
					if n := m.Calls("PUT", "/ai/rate-card/"); n != puts {
						return fmt.Errorf("%d rate requests for an unchanged rate", n-puts)
					}
					return nil
				}),
			},
			{
				// A new rate the same day edits it; the note left out is cleared.
				Config: rateCardHCL(`eur_per_1m_input  = 0.6`, `eur_per_1m_output = 1.5`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(rateCardAddr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(rateCardAddr, "eur_per_1m_input", "0.6"),
					resource.TestCheckResourceAttr(rateCardAddr, "valid_from", today),
					resource.TestCheckNoResourceAttr(rateCardAddr, "note"),
				),
			},
			{
				// The rate changed on the portal: drift, shown and put back.
				PreConfig:          func() { m.SetListRate("general", 9, 9, today) },
				Config:             rateCardHCL(`eur_per_1m_input  = 0.6`, `eur_per_1m_output = 1.5`),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: rateCardHCL(`eur_per_1m_input  = 0.6`, `eur_per_1m_output = 1.5`),
				Check: mockCheck(func() error {
					if got := m.ListRateInForce("general"); got != "0.6/1.5" {
						return fmt.Errorf("mock list rate = %q", got)
					}
					return nil
				}),
			},
			{ResourceName: rateCardAddr, ImportState: true, ImportStateId: "general", ImportStateVerify: true},
		},
	})
}

// valid_from: a past day applies the rate from then (and stays), a future day
// schedules it; both plan empty afterwards.
func TestAccAIRateCard_ValidFrom(t *testing.T) {
	m := aiMock(t)
	from, later := firstOfMonth(), acctest.AIToday()
	later = later[:8] + "28"
	if later <= acctest.AIToday() {
		later = ""
	}
	steps := []resource.TestStep{{
		Config: rateCardHCL(`eur_per_1m_input  = 0.5`, `eur_per_1m_output = 1`, fmt.Sprintf(`valid_from = %q`, from)),
		Check:  resource.TestCheckResourceAttr(rateCardAddr, "valid_from", from),
		ConfigPlanChecks: resource.ConfigPlanChecks{
			PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
		},
	}}
	if later != "" {
		steps = append(steps, resource.TestStep{
			Config: rateCardHCL(`eur_per_1m_input  = 0.7`, `eur_per_1m_output = 1`, fmt.Sprintf(`valid_from = %q`, later)),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(rateCardAddr, "valid_from", later),
				resource.TestCheckResourceAttr(rateCardAddr, "eur_per_1m_input", "0.7"),
				mockCheck(func() error {
					if got := m.ListRateInForce("general"); got != "0.5/1" {
						return fmt.Errorf("a scheduled rate must not apply today; mock = %q", got)
					}
					return nil
				}),
			),
			ConfigPlanChecks: resource.ConfigPlanChecks{
				PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
			},
		})
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: protoV6, Steps: steps})
}

// A tenant's whole plan: set from the first of the month, a tier dropped
// (back to the list rate, with a plan warning), drift put back, imported;
// destroy ends the plan.
func TestAccAIRatePlan_Lifecycle(t *testing.T) {
	m := aiMock(t)
	_, tenantID := m.AddCustomer("EXAMPLE", "example")
	m.SetListRate("general", 1, 2, "2026-01-01")
	providers, rec := recordingProvider()
	from := fmt.Sprintf("  valid_from = %q", firstOfMonth())
	general := `general = { eur_per_1m_input = 0.05, eur_per_1m_output = 0.2, note = "pilot" }`
	code := `code    = { eur_per_1m_input = 0.1, eur_per_1m_output = 0.4 }`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: providers,
		CheckDestroy: func(*terraform.State) error {
			if got := m.PlanInForce(tenantID); len(got) != 0 {
				return fmt.Errorf("after destroy the tenant still has its own rates: %v", got)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: ratePlanHCL(tenantID, from, general, code),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(ratePlanAddr, "id", tenantID),
					resource.TestCheckResourceAttr(ratePlanAddr, "tenant_name", "EXAMPLE Ltd"),
					resource.TestCheckResourceAttr(ratePlanAddr, "rates.%", "2"),
					resource.TestCheckResourceAttr(ratePlanAddr, "rates.general.eur_per_1m_input", "0.05"),
					resource.TestCheckResourceAttr(ratePlanAddr, "rates.general.note", "pilot"),
					resource.TestCheckNoResourceAttr(ratePlanAddr, "rates.code.note"),
					resource.TestCheckResourceAttr(ratePlanAddr, "contract_included", "false"),
					resource.TestCheckResourceAttr(ratePlanAddr, "effective.#", "2"),
					resource.TestCheckResourceAttr(ratePlanAddr, "effective.1.tier", "general"),
					resource.TestCheckResourceAttr(ratePlanAddr, "effective.1.basis", "plan"),
					resource.TestCheckResourceAttr(ratePlanAddr, "effective.1.list_eur_per_1m_input", "1"),
					mockCheck(func() error {
						want := map[string]string{"general": "0.05/0.2", "code": "0.1/0.4"}
						if got := m.PlanInForce(tenantID); fmt.Sprint(got) != fmt.Sprint(want) {
							return fmt.Errorf("mock plan = %v, want %v", got, want)
						}
						return nil
					}),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// code left out: back to the list rate, and the plan warns.
				Config: ratePlanHCL(tenantID, from, general),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(ratePlanAddr, "rates.%", "1"),
					rec.expectWarning("The apply returns tiers to the list rate: code"),
					mockCheck(func() error {
						if got := m.PlanInForce(tenantID); fmt.Sprint(got) != fmt.Sprint(map[string]string{"general": "0.05/0.2"}) {
							return fmt.Errorf("mock plan = %v", got)
						}
						return nil
					}),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// A tier priced on the portal meanwhile is drift.
				PreConfig:          func() { m.SetPlanRate(tenantID, "embed", 0.01, 0, firstOfMonth()) },
				Config:             ratePlanHCL(tenantID, from, general),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: ratePlanHCL(tenantID, from, general),
				Check: mockCheck(func() error {
					if got := m.PlanInForce(tenantID); fmt.Sprint(got) != fmt.Sprint(map[string]string{"general": "0.05/0.2"}) {
						return fmt.Errorf("mock plan = %v", got)
					}
					return nil
				}),
			},
			{
				ResourceName: ratePlanAddr, ImportState: true, ImportStateId: tenantID, ImportStateVerify: true,
				// valid_from is sent, never read back: an import has none.
				ImportStateVerifyIgnore: []string{"valid_from"},
			},
		},
	})
}

// What the plan refuses before any request.
func TestAccAIRates_Validation(t *testing.T) {
	m := aiMock(t)
	_, tenantID := m.AddCustomer("EXAMPLE", "example")
	cases := []struct{ cfg, err string }{
		{providerBlock(false) + "resource \"ataila_ai_rate_card\" \"x\" {\n  tier = \"-bad\"\n  eur_per_1m_input = 1\n  eur_per_1m_output = 1\n}\n", "serving tier"},
		{rateCardHCL(`eur_per_1m_input  = -1`, `eur_per_1m_output = 1`), "between"},
		{rateCardHCL(`eur_per_1m_input  = 1`, `eur_per_1m_output = 1000001`), "between"},
		{rateCardHCL(`eur_per_1m_input  = 1`, `eur_per_1m_output = 1`, `valid_from = "1 Oct"`), "YYYY-MM-DD"},
		{rateCardHCL(`eur_per_1m_input  = 1`, `eur_per_1m_output = 1`, `note = ""`), "length"},
		{ratePlanHCL(tenantID, "", `"bad tier!" = { eur_per_1m_input = 1, eur_per_1m_output = 1 }`), "serving tier"},
		{ratePlanHCL("not-a-tenant", "", `general = { eur_per_1m_input = 1, eur_per_1m_output = 1 }`), "tenant id"},
	}
	var steps []resource.TestStep
	for _, c := range cases {
		steps = append(steps, resource.TestStep{Config: c.cfg, PlanOnly: true, ExpectError: words(c.err)})
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: protoV6, Steps: steps})
	if n := m.Mutations(); n != 0 {
		t.Errorf("%d requests for configurations the plan refuses", n)
	}
}

// The per-feature gate: on 1.0.235 (it serves part A's quotas, not the rates)
// the rate resources and the usage data sources are refused at plan time,
// before any request, while ataila_tenant_quota applies.
func TestAccAIBilling_PlatformRelease(t *testing.T) {
	below := releaseBelow(t, client.ReleaseAIBilling)
	m := aiMock(t)
	m.ServeAIBilling(false)
	m.SetMeta(func(meta map[string]any) { meta["platform_version"] = below })
	_, tenantID := m.AddCustomer("EXAMPLE", "example")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{
				Config:      rateCardHCL(`eur_per_1m_input  = 1`, `eur_per_1m_output = 1`),
				ExpectError: words("ataila_ai_rate_card needs platform release " + client.ReleaseAIBilling + " or later"),
			},
			{
				Config:      ratePlanHCL(tenantID, "", `general = { eur_per_1m_input = 1, eur_per_1m_output = 1 }`),
				ExpectError: words("This platform is release " + below),
			},
			{
				Config:      providerBlock(false) + fmt.Sprintf("data \"ataila_ai_usage\" \"u\" {\n  tenant_id = %q\n}\n", tenantID),
				ExpectError: words("ataila_ai_usage data source needs platform release " + client.ReleaseAIBilling),
			},
			{
				Config:      providerBlock(false) + "data \"ataila_ai_rate_card\" \"c\" {}\n",
				ExpectError: words("needs platform release " + client.ReleaseAIBilling),
			},
			{
				// Part A's feature group is older: it is served.
				Config: tenantQuotaHCL(tenantID, `ai_tpm = { limit = 1000 }`),
				Check:  resource.TestCheckResourceAttr(tenantQuotaAddr, "quota.ai_tpm.limit", "1000"),
			},
		},
	})
	if n := m.Calls("PUT", "/ai/") + m.Calls("PUT", "/tenants/"+tenantID+"/ai-rate-plan") +
		m.Calls("GET", "/tenants/"+tenantID+"/ai-usage"); n != 0 {
		t.Errorf("%d usage or rate requests to a platform that does not serve them", n)
	}
}

// Without either admin key the platform refuses the plan; the error names them.
func TestAccAIRatePlan_NeedsTheKey(t *testing.T) {
	m := aiMock(t)
	m.SetScopes("orders-read-global", "ai-gateway-read-global", "tenancy-read-global")
	_, tenantID := m.AddCustomer("EXAMPLE", "example")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config:      ratePlanHCL(tenantID, "", `general = { eur_per_1m_input = 1, eur_per_1m_output = 1 }`),
			ExpectError: words("orders-admin-global"),
		}},
	})
}

var sampleUsage = map[string]any{
	"tenant_name": "EXAMPLE Ltd", "tenant_slug": "example",
	"totals": map[string]any{"calls": 6, "prompt_tokens": 1200000, "completion_tokens": 800000,
		"total_tokens": 2000000, "failures": 1, "cache_hits": 0, "shadow_usd": 1.0},
	"by_key": []any{map[string]any{"key_alias": "example-dev-chat", "env": "dev", "app": "chat",
		"project_id": "4", "attribution": "registry", "last_seen": "2026-10-03T12:00:00Z",
		"totals":    map[string]any{"calls": 6, "total_tokens": 2000000, "shadow_usd": 1.0},
		"rated_eur": 1.4,
		"rows": []any{map[string]any{"tier": "general", "model": "m", "calls": 3, "total_tokens": 1000000,
			"rated_eur": 1.4, "rate_basis": "list", "rated_complete": true}}}},
	"by_tier": []any{map[string]any{"tier": "general", "calls": 3, "rated_eur": 1.4, "rate_basis": "list",
		"rated_complete": true, "eur_per_1m_input": 1.0, "eur_per_1m_output": 2.0},
		map[string]any{"tier": "embed", "calls": 3, "rated_eur": nil, "rate_basis": "none", "rated_complete": false}},
	"by_day": []any{map[string]any{"day": "2026-10-01", "calls": 0, "total_tokens": 0, "shadow_usd": 0.0,
		"rated_eur": 0.0}},
	"forecast": map[string]any{"elapsed_days": 7, "days_in_month": 31, "total_tokens": 8857143, "calls": 27,
		"shadow_usd": 4.43, "rated_eur": 6.2},
	"rated_eur": 1.4, "rate_basis": "list", "rate_bases": []any{"list"}, "unrated_tiers": []any{"embed"},
	"rated_complete": false,
	"budget": map[string]any{"limit": 10.0, "policy": "auto", "spent": 1.4, "pct": 14.0, "state": "ok",
		"forecast": 6.2, "forecast_pct": 62.0, "complete": false, "enforced_on_gateway": false},
	"oldest_month": "2026-09",
}

// The usage data sources read the platform's month, the current one when no
// month is given.
func TestAccAIUsage_DataSources(t *testing.T) {
	m := aiMock(t)
	_, tenantID := m.AddCustomer("EXAMPLE", "example")
	m.SetTenantAIUsage(tenantID, sampleUsage)
	m.SetGatewayAIUsage(map[string]any{
		"totals": map[string]any{"calls": 9},
		"tenants": []any{map[string]any{"tenant_id": tenantID, "tenant_name": "EXAMPLE Ltd", "keys": 1,
			"key_aliases": []any{"example-dev-chat"}, "attribution": map[string]any{"registry": 2},
			"calls": 6, "rated_eur": 1.4, "rate_basis": "list", "unrated_tiers": []any{"embed"}}},
		"unattributed": map[string]any{"tenant_id": nil, "keys": 1, "calls": 3},
		"rated_eur":    1.4, "rate_basis": "none", "unrated_tiers": []any{"embed"}, "rated_complete": false,
	})
	u, g, sep := "data.ataila_ai_usage.u", "data.ataila_ai_gateway_usage.g", "data.ataila_ai_usage.sep"
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config: providerBlock(false) + fmt.Sprintf(`
data "ataila_ai_usage" "u" {
  tenant_id = %[1]q
}

data "ataila_ai_usage" "sep" {
  tenant_id = %[1]q
  month     = "2026-09"
}

data "ataila_ai_gateway_usage" "g" {}
`, tenantID),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(u, "month", acctest.AIToday()[:7]),
				resource.TestCheckResourceAttr(sep, "month", "2026-09"),
				resource.TestCheckResourceAttr(u, "tenant_name", "EXAMPLE Ltd"),
				resource.TestCheckResourceAttr(u, "totals.calls", "6"),
				resource.TestCheckResourceAttr(u, "totals.shadow_usd", "1"),
				resource.TestCheckResourceAttr(u, "rated_eur", "1.4"),
				resource.TestCheckResourceAttr(u, "rate_basis", "list"),
				resource.TestCheckResourceAttr(u, "rated_complete", "false"),
				resource.TestCheckResourceAttr(u, "unrated_tiers.0", "embed"),
				resource.TestCheckResourceAttr(u, "by_key.0.key_alias", "example-dev-chat"),
				resource.TestCheckResourceAttr(u, "by_key.0.attribution", "registry"),
				resource.TestCheckResourceAttr(u, "by_key.0.rows.0.tier", "general"),
				resource.TestCheckResourceAttr(u, "by_tier.1.tier", "embed"),
				resource.TestCheckNoResourceAttr(u, "by_tier.1.rated_eur"),
				resource.TestCheckResourceAttr(u, "by_day.0.day", "2026-10-01"),
				resource.TestCheckResourceAttr(u, "forecast.rated_eur", "6.2"),
				resource.TestCheckResourceAttr(u, "budget.state", "ok"),
				resource.TestCheckResourceAttr(u, "budget.limit", "10"),
				resource.TestCheckResourceAttr(g, "tenants.0.tenant_id", tenantID),
				resource.TestCheckResourceAttr(g, "tenants.0.attribution.registry", "2"),
				resource.TestCheckResourceAttr(g, "unattributed.keys", "1"),
				resource.TestCheckNoResourceAttr(g, "unattributed.tenant_id"),
				resource.TestCheckResourceAttr(g, "rate_basis", "none"),
			),
		}},
	})
	if n := m.Mutations(); n != 0 {
		t.Errorf("reading usage sent %d requests that could change something", n)
	}
}

// The rate card and a tenant's plan, read.
func TestAccAIRates_DataSources(t *testing.T) {
	m := aiMock(t)
	_, tenantID := m.AddCustomer("EXAMPLE", "example")
	m.SetListRate("general", 1, 2, "2026-01-01")
	m.SetPlanRate(tenantID, "general", 0.5, 1, "2026-01-01")
	c, p := "data.ataila_ai_rate_card.c", "data.ataila_ai_rate_plan.p"
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config: providerBlock(false) + fmt.Sprintf(`
data "ataila_ai_rate_card" "c" {}

data "ataila_ai_rate_plan" "p" {
  tenant_id = %q
}
`, tenantID),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(c, "currency", "EUR"),
				resource.TestCheckResourceAttr(c, "tiers.#", "3"),
				resource.TestCheckResourceAttr(c, "tiers.2.tier", "general"),
				resource.TestCheckResourceAttr(c, "tiers.2.current.eur_per_1m_output", "2"),
				resource.TestCheckResourceAttr(c, "tiers.2.current.valid_from", "2026-01-01"),
				resource.TestCheckNoResourceAttr(c, "tiers.1.current"),
				resource.TestCheckResourceAttr(c, "tiers.1.seedable", "false"),
				resource.TestCheckResourceAttr(c, "history.#", "1"),
				resource.TestCheckResourceAttr(p, "tenant_name", "EXAMPLE Ltd"),
				resource.TestCheckResourceAttr(p, "rates.0.tier", "general"),
				resource.TestCheckResourceAttr(p, "rates.0.eur_per_1m_input", "0.5"),
				resource.TestCheckResourceAttr(p, "effective.0.basis", "plan"),
				resource.TestCheckResourceAttr(p, "effective.0.list_eur_per_1m_input", "1"),
			),
		}},
	})
}
