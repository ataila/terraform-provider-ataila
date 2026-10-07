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
)

const keyAddr = "ataila_ai_gateway_key.test"

// keyHCL is a customer (its primary tenant owns the key) and a key.
func keyHCL(allowDestroy bool, extra ...string) string {
	return providerBlock(allowDestroy) + customerHCL("GATE", "gate") + fmt.Sprintf(`
resource "ataila_ai_gateway_key" "test" {
  organization_id = ataila_customer.test.primary_tenant_id
  env             = "prod"
  app             = "chatbot"
%s
}
`, indent(extra))
}

func keyGone(m *acctest.MockAPI, id *string, alias string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if k, found := m.GatewayKey(*id); found {
			return fmt.Errorf("key %s still registered: %v", *id, k)
		}
		if m.OnGateway(alias) || m.SecretValue(alias) != "" {
			return fmt.Errorf("key %s still on the gateway or in the secrets store", alias)
		}
		return nil
	}
}

// Create with the value exposed once, read back without re-reading it,
// change limits in place, import both ways, rotate through the trigger,
// see drift on the gateway, and delete (not destroy-gated).
func TestAccGatewayKey_Lifecycle(t *testing.T) {
	m := newMock(t)
	var id, secret string
	base := []string{`models          = ["general"]`, `expose_secret   = true`, `soft_budget_usd = 0.1`}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		PreCheck:                 allowDestroyEverywhere(m),
		CheckDestroy:             keyGone(m, &id, "gate-prod-chatbot"),
		Steps: []resource.TestStep{
			{
				Config: keyHCL(false, append(base, `rpm_limit       = 60`)...),
				Check: resource.ComposeAggregateTestCheckFunc(
					stateAttr(keyAddr, "id", &id),
					stateAttr(keyAddr, "secret", &secret),
					resource.TestCheckResourceAttr(keyAddr, "key_alias", "gate-prod-chatbot"),
					resource.TestCheckResourceAttr(keyAddr, "origin", "api"),
					resource.TestCheckResourceAttr(keyAddr, "soft_budget_usd", "0.1"),
					resource.TestCheckResourceAttrSet(keyAddr, "secret_path"),
					resource.TestCheckResourceAttrSet(keyAddr, "token_hash_prefix"),
					check(func() error {
						if secret != m.SecretValue("gate-prod-chatbot") {
							return fmt.Errorf("the state's secret is not the stored value")
						}
						return nil
					}),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				Config: keyHCL(false, append(base, `rpm_limit       = 120`, `models          = ["general", "code"]`)[1:]...),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(keyAddr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPtr(keyAddr, "secret", &secret),
					resource.TestCheckResourceAttr(keyAddr, "models.#", "2"),
					mockField(m.GatewayKey, &id, "rpm_limit", 120),
				),
			},
			{
				ResourceName: keyAddr, ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"secret", "expose_secret", "rotation_trigger"},
			},
			{
				ResourceName: keyAddr, ImportState: true, ImportStateId: "alias:gate-prod-chatbot", ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"secret", "expose_secret", "rotation_trigger"},
			},
			{
				// A change of the trigger rotates: a new value, the old one gone.
				Config: keyHCL(false, append(base[1:], `rpm_limit       = 120`, `models          = ["general", "code"]`,
					`rotation_trigger = { at = "2026-10" }`)...),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet(keyAddr, "rotated_at"),
					check(func() error {
						k, _ := m.GatewayKey(id)
						if m.SecretValue("gate-prod-chatbot") == secret || k["rotated_at"] == nil {
							return fmt.Errorf("the key was not rotated")
						}
						return nil
					}),
					func(s *terraform.State) error {
						now := s.RootModule().Resources[keyAddr].Primary.Attributes["secret"]
						if now == secret || now != m.SecretValue("gate-prod-chatbot") {
							return fmt.Errorf("the state does not hold the new value")
						}
						return nil
					},
				),
			},
			{
				// Changed on the gateway: the next plan sets it back.
				PreConfig: func() { m.SetGatewayKeyLimits("gate-prod-chatbot", 999, 1.5) },
				Config: keyHCL(false, append(base[1:], `rpm_limit       = 120`, `models          = ["general", "code"]`,
					`rotation_trigger = { at = "2026-10" }`)...),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(keyAddr, plancheck.ResourceActionUpdate)},
				},
				Check: mockField(m.GatewayKey, &id, "rpm_limit", 120),
			},
			{
				// The key alone is deleted, with allow_destroy off.
				Config: providerBlock(false) + customerHCL("GATE", "gate"),
				Check:  keyGone(m, &id, "gate-prod-chatbot"),
			},
			{Config: providerBlock(true) + customerHCL("GATE", "gate")},
		},
	})
}

// Without expose_secret the value never reaches the state; a tier that
// serves nothing is a warning; a key the gateway lost is reported.
func TestAccGatewayKey_NoSecretWarningsAndALostKey(t *testing.T) {
	m := newMock(t)
	factories, rec := recordingProvider()
	cfg := keyHCL(true, `models = ["general", "code-max"]`, `feature = "summaries"`)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		PreCheck:                 allowDestroyEverywhere(m),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(keyAddr, "key_alias", "gate-prod-chatbot-summaries"),
					resource.TestCheckNoResourceAttr(keyAddr, "secret"),
					resource.TestCheckResourceAttr(keyAddr, "warnings.0.code", "tier_not_serving"),
					rec.expectWarning("ATAILA API warning: tier_not_serving", "code-max"),
				),
			},
			{
				PreConfig: func() { m.LoseGatewayKey("gate-prod-chatbot-summaries") },
				Config:    cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(keyAddr, "live", "missing"),
					rec.expectWarning("The AI gateway lost this key", "-replace=ataila_ai_gateway_key"),
				),
			},
		},
	})
}

// The four keys that make up the alias are frozen.
func TestAccGatewayKey_FrozenKeysFailThePlan(t *testing.T) {
	m := newMock(t)
	var mutations int
	models := `models = ["general"]`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		PreCheck:                 allowDestroyEverywhere(m),
		Steps: []resource.TestStep{
			{Config: keyHCL(true, models), Check: check(func() error { mutations = m.Mutations(); return nil })},
			{
				Config: providerBlock(true) + customerHCL("GATE", "gate") + `
resource "ataila_ai_gateway_key" "test" {
  organization_id = ataila_customer.test.primary_tenant_id
  env             = "dev"
  app             = "chatbot"
  models          = ["general"]
}
`,
				PlanOnly: true, ExpectError: words("Cannot change env of an existing AI gateway key .* irreversibly"),
			},
			{
				Config: providerBlock(true) + customerHCL("GATE", "gate") + `
resource "ataila_ai_gateway_key" "test" {
  organization_id = ataila_customer.test.primary_tenant_id
  env             = "prod"
  app             = "helpdesk"
  models          = ["general"]
}
`,
				PlanOnly: true, ExpectError: words("Cannot change app of an existing AI gateway key"),
			},
			{
				Config:   keyHCL(true, models, `feature = "extra"`),
				PlanOnly: true, ExpectError: words("Cannot change feature of an existing AI gateway key"),
			},
			{
				PreConfig: func() {
					if got := m.Mutations(); got != mutations {
						t.Errorf("refused changes sent %d mutating requests", got-mutations)
					}
				},
				Config: keyHCL(true, models),
			},
		},
	})
}

// A platform without a gateway: a plan succeeds (it calls nothing), the
// apply fails naming gateway_not_configured after one request, and nothing
// is created. With an empty tier catalogue as well, the platform still names
// the gateway: it checks the gateway before the body.
func TestAccGatewayKey_PlatformWithoutGateway(t *testing.T) {
	m := newMock(t)
	m.SetGateway("not_configured")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		PreCheck:                 allowDestroyEverywhere(m),
		Steps: []resource.TestStep{
			{Config: providerBlock(true) + customerHCL("GATE", "gate")},
			{Config: keyHCL(true, `models = ["general"]`), PlanOnly: true, ExpectNonEmptyPlan: true},
			{
				Config:      keyHCL(true, `models = ["general"]`),
				ExpectError: words("The AI gateway is not available (gateway_not_configured) .* need a platform with a gateway"),
			},
			{
				Config: providerBlock(true) + customerHCL("GATE", "gate") + `
data "ataila_ai_gateway" "this" {}
`,
				ExpectError: words("The AI gateway is not available (gateway_not_configured)"),
			},
			{
				PreConfig:   m.ClearTiers,
				Config:      keyHCL(true, `models = ["general"]`),
				ExpectError: words("The AI gateway is not available (gateway_not_configured)"),
			},
			{
				Config: providerBlock(true) + customerHCL("GATE", "gate") + `
data "ataila_ai_serving_tiers" "all" {}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.ataila_ai_serving_tiers.all", "tiers.#", "0"),
					check(func() error {
						if n := m.Calls("POST", "/ai/gateway/keys"); n != 2 {
							return fmt.Errorf("POST /ai/gateway/keys sent %d times, want 2 (no retries)", n)
						}
						return nil
					}),
				),
			},
		},
	})
}

// The same on a platform whose catalogue has the tier: the 503 names the code.
func TestAccGatewayKey_GatewayUnreachableIsFinal(t *testing.T) {
	m := newMock(t)
	m.SetGateway("unreachable")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		PreCheck:                 allowDestroyEverywhere(m),
		Steps: []resource.TestStep{
			{Config: providerBlock(true) + customerHCL("GATE", "gate")},
			{
				Config:      keyHCL(true, `models = ["general"]`),
				ExpectError: words("The AI gateway is not available (gateway_unreachable) .* does not retry"),
			},
			{
				PreConfig: func() {
					if n := m.Calls("POST", "/ai/gateway/keys"); n != 1 {
						t.Errorf("POST sent %d times although Retry-After was 30: a gateway 503 is final", n)
					}
				},
				Config: providerBlock(true) + customerHCL("GATE", "gate"),
			},
		},
	})
}

// An adopted key is imported and managed, but its rotation fails the plan.
func TestAccGatewayKey_AdoptedKeyIsNotRotated(t *testing.T) {
	m := newMock(t)
	var tenant string
	adopted := func(extra string) string {
		return providerBlock(true) + customerHCL("GATE", "gate") + `
resource "ataila_ai_gateway_key" "adopted" {
  organization_id = ataila_customer.test.primary_tenant_id
  env             = "prod"
  app             = "adopted"
  models          = ["general"]
` + extra + `
}
`
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		PreCheck:                 allowDestroyEverywhere(m),
		Steps: []resource.TestStep{
			{
				Config: providerBlock(true) + customerHCL("GATE", "gate"),
				Check: resource.ComposeAggregateTestCheckFunc(
					stateAttr(customerAddr, "primary_tenant_id", &tenant),
					check(func() error { m.AddAdoptedKey(tenant, "gate-legacy", "general"); return nil }),
				),
			},
			{
				Config: adopted(""), ResourceName: "ataila_ai_gateway_key.adopted",
				ImportState: true, ImportStateId: "alias:gate-legacy", ImportStatePersist: true,
			},
			{Config: adopted(""), PlanOnly: true},
			{
				Config:      adopted(`  rotation_trigger = { at = "now" }`),
				PlanOnly:    true,
				ExpectError: words("An adopted key cannot be rotated here"),
			},
		},
	})
}

// A tier's pin and flag: set, read back, drift, import, and a destroy that
// only forgets.
func TestAccServingTier_Lifecycle(t *testing.T) {
	m := newMock(t)
	const addr = "ataila_ai_serving_tier.code"
	tier := func(extra ...string) string {
		return providerBlock(false) + fmt.Sprintf(`
resource "ataila_ai_serving_tier" "code" {
  key = "code"
%s
}
`, indent(extra))
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		CheckDestroy: func(*terraform.State) error {
			t, _ := m.Tier("code")
			if t["enabled"] != false {
				return fmt.Errorf("destroy must only forget the tier, which keeps its flag: %v", t)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: tier(`pinned_model = "model-general"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "source", "pin"),
					resource.TestCheckResourceAttr(addr, "resolved_model", "model-general"),
					resource.TestCheckResourceAttr(addr, "enabled", "true"),
					resource.TestCheckResourceAttr(addr, "label", "Code"),
					resource.TestCheckResourceAttr(addr, "candidate_models.#", "2"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				Config: tier(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(addr, "pinned_model"),
					resource.TestCheckResourceAttr(addr, "source", "auto"),
				),
			},
			{ResourceName: addr, ImportState: true, ImportStateId: "code", ImportStateVerify: true},
			{
				PreConfig: func() { p := "model-general"; m.SetTierOutOfBand("code", &p, true) },
				Config:    tier(),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.TestCheckResourceAttr(addr, "source", "auto"),
			},
			{
				Config:      tier(`pinned_model = "model-offline"`),
				ExpectError: words("The model is not loaded: model-offline .* candidates: model-code, model-general"),
			},
			{
				Config: tier(`pinned_model = "model-offline"`, `allow_unloaded_pin = true`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(addr, "source", "pin-offline"),
					resource.TestCheckResourceAttr(addr, "resolved", "false"),
					resource.TestCheckResourceAttr(addr, "warnings.0.code", "model_not_loaded"),
				),
			},
			{Config: tier(`enabled = false`)},
			{
				Config: providerBlock(false) + `
resource "ataila_ai_serving_tier" "nope" {
  key = "nope"
}
`,
				ExpectError: words("No such serving tier: nope"),
			},
		},
	})
}

func TestAccGatewayDataSources(t *testing.T) {
	newMock(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config: providerBlock(false) + `
data "ataila_ai_gateway" "this" {}

data "ataila_ai_serving_tiers" "all" {}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.ataila_ai_gateway.this", "base_url", acctest.GatewayBaseURL),
				resource.TestCheckResourceAttr("data.ataila_ai_gateway.this", "tiers.#", "3"),
				resource.TestCheckResourceAttr("data.ataila_ai_serving_tiers.all", "tiers.#", "3"),
				resource.TestCheckResourceAttr("data.ataila_ai_serving_tiers.all", "tiers.0.key", "code"),
				resource.TestCheckResourceAttr("data.ataila_ai_serving_tiers.all", "tiers.0.source", "auto"),
				resource.TestCheckResourceAttr("data.ataila_ai_serving_tiers.all", "tiers.1.key", "code-max"),
				resource.TestCheckResourceAttr("data.ataila_ai_serving_tiers.all", "tiers.1.resolved", "false"),
			),
		}},
	})
}
