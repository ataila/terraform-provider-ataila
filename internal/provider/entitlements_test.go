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
	entitlementAddr  = "ataila_service_entitlement.test"
	serviceGrantAddr = "ataila_service_grant.test"
)

// entitlementsMock is a mock whose token holds the entitlement keys, on the
// platform release that serves them, with one customer and one person.
func entitlementsMock(t *testing.T) (m *acctest.MockAPI, customerID, userID string) {
	t.Helper()
	m = newMock(t)
	m.SetScopes(append(acctest.DefaultScopes(), "entitlements-admin-global", "entitlements-read-global")...)
	m.SetMeta(func(meta map[string]any) { meta["platform_version"] = client.ReleaseEntitlements })
	customerID, _ = m.AddCustomer("EXAMPLE", "example")
	userID = m.AddUser("dana@example.com")
	return m, customerID, userID
}

func entitlementHCL(allowDestroy bool, customerID, service string, lines ...string) string {
	return providerBlock(allowDestroy) + fmt.Sprintf(`
resource "ataila_service_entitlement" "test" {
  customer_id = %q
  service_key = %q
%s}
`, customerID, service, indent(lines))
}

func serviceGrantHCL(allowDestroy bool, customerID, userID string, lines ...string) string {
	return entitlementHCL(allowDestroy, customerID, "coder", `seats = 2`) + fmt.Sprintf(`
resource "ataila_service_grant" "test" {
  user_id     = %q
  customer_id = ataila_service_entitlement.test.customer_id
  service_key = ataila_service_entitlement.test.service_key
%s}
`, userID, indent(lines))
}

// The whole life of an entitlement: create, change in place, import, and a
// destroy behind both switches that withdraws it (disabled, not removed).
func TestAccServiceEntitlement_Lifecycle(t *testing.T) {
	m, cid, _ := entitlementsMock(t)
	m.SetTokenAllowDestroy(true)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		CheckDestroy: func(*terraform.State) error {
			if got := m.EntitlementState(cid, "coder"); got != "disabled" {
				return fmt.Errorf("after destroy the entitlement is %q, want disabled", got)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: entitlementHCL(true, cid, "coder", `seats = 3`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(entitlementAddr, "id", cid+"/coder"),
					resource.TestCheckResourceAttr(entitlementAddr, "state", "enabled"),
					resource.TestCheckResourceAttr(entitlementAddr, "effective_state", "enabled"),
					resource.TestCheckResourceAttr(entitlementAddr, "seats", "3"),
					resource.TestCheckResourceAttr(entitlementAddr, "seats_used", "0"),
					resource.TestCheckResourceAttr(entitlementAddr, "service_name", "Coder"),
					resource.TestCheckResourceAttr(entitlementAddr, "approved", "false"),
				),
			},
			{
				Config: entitlementHCL(true, cid, "coder", `seats = 5`, `notes = "pilot"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(entitlementAddr, plancheck.ResourceActionUpdate)}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(entitlementAddr, "seats", "5"),
					resource.TestCheckResourceAttr(entitlementAddr, "notes", "pilot"),
				),
			},
			{ResourceName: entitlementAddr, ImportState: true, ImportStateVerify: true},
			{
				ResourceName: entitlementAddr, ImportState: true, ImportStateId: "not-a-customer/coder",
				ExpectError: words(`The import id must be <customer_id>/<service_key>`),
			},
			{
				Config: entitlementHCL(true, cid, "coder", `seats = 5`, `notes = "pilot"`, `state = "disabled"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(entitlementAddr, "state", "disabled"),
					resource.TestCheckResourceAttr(entitlementAddr, "effective_state", "disabled"),
				),
			},
		},
	})
}

// The provider never approves: a service that needs approval is stored
// pending_approval with a warning, the configuration shows no difference, and
// the operator's approval in the portal is kept on the next apply.
func TestAccServiceEntitlement_ApprovalStaysInThePortal(t *testing.T) {
	m, cid, _ := entitlementsMock(t)
	m.SetTokenAllowDestroy(true)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{
				Config: entitlementHCL(true, cid, "n8n"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(entitlementAddr, "state", "enabled"),
					resource.TestCheckResourceAttr(entitlementAddr, "effective_state", "pending_approval"),
					resource.TestCheckResourceAttr(entitlementAddr, "approved", "false"),
				),
			},
			{
				Config: entitlementHCL(true, cid, "n8n"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectEmptyPlan()}},
			},
			{
				PreConfig: func() { m.ApproveEntitlement(cid, "n8n") },
				Config:    entitlementHCL(true, cid, "n8n", `seats = 4`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(entitlementAddr, "effective_state", "enabled"),
					resource.TestCheckResourceAttr(entitlementAddr, "approved", "true"),
				),
			},
		},
	})
	if n := m.Calls("PUT", "/customers/"+cid+"/services/n8n"); n < 2 {
		t.Errorf("PUT was sent %d times, want at least 2", n)
	}
}

// Destroy is double opt-in: off in the provider, nothing is sent.
func TestAccServiceEntitlement_DestroyRefusedByDefault(t *testing.T) {
	m, cid, _ := entitlementsMock(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{Config: entitlementHCL(false, cid, "gitlab")},
			{
				Config:  entitlementHCL(false, cid, "gitlab"),
				Destroy: true,
				ExpectError: words("Destroying a service entitlement is not allowed .* destroy means withdraw " +
					".* tofu state rm ataila_service_entitlement.<name>"),
			},
			{
				PreConfig: func() {
					if n := m.Calls("DELETE", "/customers/"); n != 0 {
						t.Errorf("%d DELETE requests were sent although destroy is off", n)
					}
					m.SetTokenAllowDestroy(true)
				},
				Config: entitlementHCL(true, cid, "gitlab"),
			},
		},
	})
}

// The per-feature gate: on the release before entitlements, everything is
// refused at plan time, before any request.
func TestAccServiceEntitlement_PlatformRelease(t *testing.T) {
	below := releaseBelow(t, client.ReleaseEntitlements)
	m, cid, _ := entitlementsMock(t)
	m.SetMeta(func(meta map[string]any) { meta["platform_version"] = below })
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{
				Config: entitlementHCL(false, cid, "coder"),
				ExpectError: words("ataila_service_entitlement needs platform release " +
					client.ReleaseEntitlements + " or later"),
			},
			{
				Config: providerBlock(false) + `data "ataila_services" "all" {}`,
				ExpectError: words("The ataila_services data source needs platform release " +
					client.ReleaseEntitlements + " or later"),
			},
		},
	})
	if n := m.Calls("PUT", "/customers/"); n != 0 {
		t.Errorf("%d PUT requests were sent to a platform without entitlements", n)
	}
}

// A grant's life: made under the entitlement, changed in place, imported,
// re-made after a revocation outside Terraform, and revoked on destroy.
func TestAccServiceGrant_Lifecycle(t *testing.T) {
	m, cid, uid := entitlementsMock(t)
	m.SetTokenAllowDestroy(true)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		CheckDestroy: func(*terraform.State) error {
			if !m.GrantRevoked(uid, cid, "coder") {
				return fmt.Errorf("after destroy the grant is not revoked")
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: serviceGrantHCL(true, cid, uid, `grant_level = "admin"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(serviceGrantAddr, "id", uid+"/"+cid+"/coder"),
					resource.TestCheckResourceAttr(serviceGrantAddr, "grant_level", "admin"),
					resource.TestCheckResourceAttr(serviceGrantAddr, "effective", "true"),
					resource.TestCheckNoResourceAttr(serviceGrantAddr, "blocked_by"),
					resource.TestCheckResourceAttr(serviceGrantAddr, "user_email", "dana@example.com"),
				),
			},
			{
				Config: serviceGrantHCL(true, cid, uid),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(serviceGrantAddr, plancheck.ResourceActionUpdate)}},
				Check: resource.TestCheckResourceAttr(serviceGrantAddr, "grant_level", "user"),
			},
			{ResourceName: serviceGrantAddr, ImportState: true, ImportStateVerify: true},
			{
				PreConfig: func() { m.RevokeGrantOutOfBand(uid, cid, "coder") },
				Config:    serviceGrantHCL(true, cid, uid),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(serviceGrantAddr, plancheck.ResourceActionCreate)}},
				Check: resource.TestCheckResourceAttr(serviceGrantAddr, "effective", "true"),
			},
		},
	})
}

// The platform's refusals reach the diagnostic with their code: no seat left.
func TestAccServiceGrant_SeatsExhausted(t *testing.T) {
	m, cid, uid := entitlementsMock(t)
	other := m.AddUser("eli@example.com")
	m.SetTokenAllowDestroy(true)
	grant := func(name, user string) string {
		return fmt.Sprintf(`
resource "ataila_service_grant" %q {
  user_id     = %q
  customer_id = ataila_service_entitlement.test.customer_id
  service_key = ataila_service_entitlement.test.service_key
}
`, name, user)
	}
	one := entitlementHCL(true, cid, "coder", `seats = 1`) + grant("test", uid)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{Config: one},
			{Config: one + grant("second", other), ExpectError: words("seats_exhausted")},
		},
	})
}

// The catalogue and a customer's entitlements, as data sources.
func TestAccServicesDataSource(t *testing.T) {
	m, cid, _ := entitlementsMock(t)
	m.SetTokenAllowDestroy(true)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config: entitlementHCL(true, cid, "coder", `seats = 3`) + fmt.Sprintf(`
data "ataila_services" "offered" {}

data "ataila_services" "every" {
  include_inactive = true
}

data "ataila_customer_entitlements" "test" {
  customer_id = %q
  depends_on  = [ataila_service_entitlement.test]
}
`, cid),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.ataila_services.offered", "services.#", "3"),
				resource.TestCheckResourceAttr("data.ataila_services.offered", "services.0.key", "coder"),
				resource.TestCheckResourceAttr("data.ataila_services.offered", "services.2.requires_approval", "true"),
				resource.TestCheckResourceAttr("data.ataila_services.every", "services.#", "4"),
				resource.TestCheckResourceAttr("data.ataila_services.every", "services.3.active", "false"),
				resource.TestCheckResourceAttr("data.ataila_customer_entitlements.test", "entitlements.#", "1"),
				resource.TestCheckResourceAttr("data.ataila_customer_entitlements.test",
					"entitlements.0.service_key", "coder"),
				resource.TestCheckResourceAttr("data.ataila_customer_entitlements.test", "entitlements.0.seats", "3"),
			),
		}},
	})
}
