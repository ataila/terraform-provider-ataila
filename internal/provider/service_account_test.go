// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/ataila/terraform-provider-ataila/internal/acctest"
	"github.com/ataila/terraform-provider-ataila/internal/client"
)

const serviceAccountAddr = "ataila_service_account.ci"

// serviceAccountMock is a mock whose token holds the service-account keys and
// the two keys the tests hand out, on the release that serves them, with one
// customer.
func serviceAccountMock(t *testing.T) (m *acctest.MockAPI, customerID string) {
	t.Helper()
	m = newMock(t)
	m.SetScopes(append(acctest.DefaultScopes(), "service-accounts-admin-global", "service-accounts-read-global")...)
	m.SetMeta(func(meta map[string]any) { meta["platform_version"] = client.ReleaseServiceAccounts })
	customerID, _ = m.AddCustomer("EXAMPLE", "example")
	return m, customerID
}

func serviceAccountHCL(allowDestroy bool, name string, lines ...string) string {
	return providerBlock(allowDestroy) + fmt.Sprintf(`
resource "ataila_service_account" "ci" {
  name = %q
%s}
`, name, indent(lines))
}

func serviceAccountIs(m *acctest.MockAPI, id *string, active bool, owner string, roles ...string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		gotActive, gotOwner, gotRoles := m.ServiceAccountState(*id)
		sameRoles := len(gotRoles) == len(roles) && (len(roles) == 0 || reflect.DeepEqual(gotRoles, roles))
		if gotActive != active || gotOwner != owner || !sameRoles {
			return fmt.Errorf("the platform holds active=%t owner=%q roles=%v, want active=%t owner=%q roles=%v",
				gotActive, gotOwner, gotRoles, active, owner, roles)
		}
		return nil
	}
}

// The whole life: create with an owner and a role, rename, change roles and
// drop the owner in place, import by id and by name, and a destroy behind
// both switches that deactivates the account (it stays, with its roles).
func TestAccServiceAccount_Lifecycle(t *testing.T) {
	m, cid := serviceAccountMock(t)
	m.SetTokenAllowDestroy(true)
	var id string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		CheckDestroy: func(*terraform.State) error {
			return serviceAccountIs(m, &id, false, "", "tenancy-read-global", "users-read-global")(nil)
		},
		Steps: []resource.TestStep{
			{
				Config: serviceAccountHCL(true, "ci-terraform",
					fmt.Sprintf("owner_customer_id = %q", cid), `roles = ["users-read-global"]`),
				Check: resource.ComposeAggregateTestCheckFunc(
					stateAttr(serviceAccountAddr, "id", &id),
					resource.TestCheckResourceAttr(serviceAccountAddr, "email", "ci-terraform@service-account.invalid"),
					resource.TestCheckResourceAttr(serviceAccountAddr, "is_active", "true"),
					resource.TestCheckResourceAttr(serviceAccountAddr, "owner_customer_id", cid),
					resource.TestCheckResourceAttr(serviceAccountAddr, "owner_customer_name", "EXAMPLE"),
					resource.TestCheckResourceAttr(serviceAccountAddr, "roles.#", "1"),
					resource.TestCheckTypeSetElemAttr(serviceAccountAddr, "roles.*", "users-read-global"),
					resource.TestCheckResourceAttr(serviceAccountAddr, "ci_links.#", "0"),
					resource.TestCheckNoResourceAttr(serviceAccountAddr, "token"),
				),
			},
			{
				Config: serviceAccountHCL(true, "ci-renamed",
					`roles = ["users-read-global", "tenancy-read-global"]`),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(serviceAccountAddr, plancheck.ResourceActionUpdate),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(serviceAccountAddr, "name", "ci-renamed"),
					resource.TestCheckResourceAttr(serviceAccountAddr, "email", "ci-renamed@service-account.invalid"),
					resource.TestCheckNoResourceAttr(serviceAccountAddr, "owner_customer_id"),
					resource.TestCheckResourceAttr(serviceAccountAddr, "roles.#", "2"),
					serviceAccountIs(m, &id, true, "", "tenancy-read-global", "users-read-global"),
				),
			},
			{
				ResourceName: serviceAccountAddr, ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"warnings"},
			},
			{
				ResourceName: serviceAccountAddr, ImportState: true, ImportStateVerify: true,
				ImportStateId: "name:ci-renamed", ImportStateVerifyIgnore: []string{"warnings"},
			},
		},
	})
}

// Destroy is double opt-in: off in the provider, nothing is sent.
func TestAccServiceAccount_DestroyRefusedByDefault(t *testing.T) {
	m, _ := serviceAccountMock(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{Config: serviceAccountHCL(false, "ci-terraform")},
			{
				Config: serviceAccountHCL(false, "ci-terraform"), Destroy: true,
				ExpectError: words("Destroying a service account is not allowed .* destroy means deactivate"),
			},
			{
				Config:      serviceAccountHCL(false, "ci-terraform", "is_active = false"),
				ExpectError: words("Setting is_active = false deactivates the service account"),
			},
			{
				PreConfig: func() {
					if n := m.Calls("DELETE", "/service-accounts/"); n != 0 {
						t.Errorf("%d DELETE requests were sent although destroy is off", n)
					}
					if n := m.Calls("PATCH", "/service-accounts/"); n != 0 {
						t.Errorf("%d PATCH requests were sent although destroy is off", n)
					}
					m.SetTokenAllowDestroy(true)
				},
				Config: serviceAccountHCL(true, "ci-terraform"),
			},
		},
	})
}

// The account's GitLab CI links are shown, read-only: an operator makes them
// in the portal, and a refresh picks them up without a planned change.
func TestAccServiceAccount_CILinksAreReadOnly(t *testing.T) {
	m, _ := serviceAccountMock(t)
	m.SetTokenAllowDestroy(true)
	var id string
	link := map[string]any{"id": "b6a4d0de-0000-4000-8000-000000000001", "issuer": "https://gitlab.example.test",
		"project_path": "infra/estate", "scopes": []string{"users-read-global"}, "allow_destroy": false,
		"protected_refs_only": true, "live_tokens": 2, "created_at": "2026-10-22T13:00:00Z"}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{
				Config: serviceAccountHCL(true, "ci-terraform"),
				Check:  stateAttr(serviceAccountAddr, "id", &id),
			},
			{
				PreConfig: func() { m.SetServiceAccountCILinks(id, link) },
				Config: serviceAccountHCL(true, "ci-terraform") + `
data "ataila_service_account" "ci" {
  name = ataila_service_account.ci.name
}
`,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(serviceAccountAddr, plancheck.ResourceActionNoop),
				}},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(serviceAccountAddr, "ci_links.#", "1"),
					resource.TestCheckResourceAttr(serviceAccountAddr, "ci_links.0.project_path", "infra/estate"),
					resource.TestCheckResourceAttr(serviceAccountAddr, "ci_links.0.protected_refs_only", "true"),
					resource.TestCheckNoResourceAttr(serviceAccountAddr, "ci_links.0.live_tokens"),
					resource.TestCheckResourceAttr("data.ataila_service_account.ci", "ci_links.0.live_tokens", "2"),
					resource.TestCheckResourceAttr("data.ataila_service_account.ci", "active_tokens", "0"),
					resource.TestCheckResourceAttrPair("data.ataila_service_account.ci", "id", serviceAccountAddr, "id"),
				),
			},
		},
	})
}

// Roles left out of the configuration are the portal's: no change is planned
// and nothing is sent.
func TestAccServiceAccount_RolesLeftToThePortal(t *testing.T) {
	m, _ := serviceAccountMock(t)
	m.SetTokenAllowDestroy(true)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{Config: serviceAccountHCL(true, "ci-terraform", `roles = ["users-read-global"]`)},
			{
				Config: serviceAccountHCL(true, "ci-terraform"),
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(serviceAccountAddr, plancheck.ResourceActionNoop),
				}},
				Check: resource.TestCheckResourceAttr(serviceAccountAddr, "roles.#", "1"),
			},
		},
	})
}

// A token hands out only what it carries: the platform's refusal stops the
// create, and nothing is left behind.
func TestAccServiceAccount_ARoleTheTokenLacksIsRefused(t *testing.T) {
	m, _ := serviceAccountMock(t)
	var scopes []string
	for _, s := range acctest.DefaultScopes() {
		if !strings.HasPrefix(s, "projects-") {
			scopes = append(scopes, s)
		}
	}
	m.SetScopes(append(scopes, "service-accounts-admin-global", "service-accounts-read-global")...)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config:      serviceAccountHCL(false, "ci-terraform", `roles = ["projects-read-global"]`),
			ExpectError: words("role_not_held_by_token"),
		}},
	})
	if n := m.Calls("POST", "/service-accounts"); n == 0 {
		t.Error("the create was never sent")
	}
}

// A deactivated account keeps its name: the error says to import it.
func TestAccServiceAccount_NameTakenSuggestsImport(t *testing.T) {
	m, _ := serviceAccountMock(t)
	m.AddServiceAccount("ci-terraform")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config:      serviceAccountHCL(false, "ci-terraform"),
			ExpectError: words("A service account with this name exists .* name:ci-terraform .* is_active = true"),
		}},
	})
}

// On a platform before the release that serves them, nothing is sent.
func TestAccServiceAccount_OlderPlatform(t *testing.T) {
	m, _ := serviceAccountMock(t)
	m.SetMeta(func(meta map[string]any) { meta["platform_version"] = client.ReleaseEntitlements })
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config:      serviceAccountHCL(false, "ci-terraform"),
			ExpectError: words("ataila_service_account needs platform release " + client.ReleaseServiceAccounts),
		}},
	})
	if n := m.Calls("POST", "/service-accounts"); n != 0 {
		t.Errorf("%d creates were sent to a platform that does not serve them", n)
	}
}

// The data source by id, and an unknown name.
func TestAccServiceAccountDataSource(t *testing.T) {
	m, _ := serviceAccountMock(t)
	id := m.AddServiceAccount("ci-portal")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{
				Config: providerBlock(false) + fmt.Sprintf(`
data "ataila_service_account" "ci" {
  id = %q
}
`, id),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.ataila_service_account.ci", "name", "ci-portal"),
					resource.TestCheckResourceAttr("data.ataila_service_account.ci", "is_active", "true"),
					resource.TestCheckNoResourceAttr("data.ataila_service_account.ci", "owner_customer_id"),
				),
			},
			{
				Config: providerBlock(false) + `
data "ataila_service_account" "ci" {
  name = "no-such-account"
}
`,
				ExpectError: words(`No service account is named "no-such-account"`),
			},
		},
	})
}
