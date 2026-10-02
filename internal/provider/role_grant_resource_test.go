// Copyright (c) 2026 ATAILA Kft.
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

const grantAddr = "ataila_user_role_grant.test"

func grantHCL(role string) string {
	return providerBlock(false) + userHCL("granted@example.com") + fmt.Sprintf(`
resource "ataila_user_role_grant" "test" {
  user_id = ataila_user.test.id
  role    = %q
}
`, role)
}

func holds(m *acctest.MockAPI, id *string, role string, want bool) resource.TestCheckFunc {
	return func(*terraform.State) error {
		p, _ := m.Person(*id)
		held := false
		for _, r := range p["roles"].([]string) {
			held = held || r == role
		}
		if held != want {
			return fmt.Errorf("user %s holds %s: %v, want %v", *id, role, held, want)
		}
		return nil
	}
}

// Grant, read back, import, replace with another role (safe), drift, remove.
// Removing a grant needs no allow_destroy.
func TestAccUserRoleGrant_Lifecycle(t *testing.T) {
	m := newMock(t)
	var user string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		PreCheck:                 allowDestroyEverywhere(m),
		Steps: []resource.TestStep{
			{
				Config: grantHCL("users-read-global"),
				Check: resource.ComposeAggregateTestCheckFunc(
					stateAttr(userAddr, "id", &user),
					resource.TestCheckResourceAttrPair(grantAddr, "user_id", userAddr, "id"),
					resource.TestCheckResourceAttrSet(grantAddr, "granted_at"),
					holds(m, &user, "users-read-global", true),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				ResourceName: grantAddr, ImportState: true, ImportStateVerify: true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return user + "/users-read-global", nil },
			},
			{
				ResourceName: grantAddr, ImportState: true, ImportStateId: "no-slash",
				ExpectError: words("The import id must be <user_id>/<role>"),
			},
			{
				Config: grantHCL("tenancy-read-global"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(grantAddr, plancheck.ResourceActionReplace)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					holds(m, &user, "users-read-global", false),
					holds(m, &user, "tenancy-read-global", true),
				),
			},
			{
				// Removed in the portal: the next plan grants it again.
				PreConfig: func() { m.SetPersonRole(user, "tenancy-read-global", false) },
				Config:    grantHCL("tenancy-read-global"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(grantAddr, plancheck.ResourceActionCreate)},
				},
				Check: holds(m, &user, "tenancy-read-global", true),
			},
			{
				// The grant alone is removed, with allow_destroy off.
				Config: providerBlock(false) + userHCL("granted@example.com"),
				Check:  holds(m, &user, "tenancy-read-global", false),
			},
			{Config: providerBlock(true) + userHCL("granted@example.com")},
		},
	})
}

// The platform's role rules for a token are clear, final errors.
func TestAccUserRoleGrant_Refusals(t *testing.T) {
	m := newMock(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		PreCheck:                 allowDestroyEverywhere(m),
		Steps: []resource.TestStep{
			{Config: providerBlock(false) + userHCL("granted@example.com")},
			{Config: grantHCL("admin"), ExpectError: words("An API token cannot grant or revoke admin")},
			{Config: grantHCL("api-tokens-read-global"), ExpectError: words("An API token cannot grant or revoke api-tokens-read-global")},
			{Config: grantHCL("users-read-tenant"), ExpectError: words("The role users-read-tenant is not grantable")},
			{Config: grantHCL("no-such-role"), ExpectError: words("No such role: no-such-role")},
			{
				Config: providerBlock(false) + userHCL("granted@example.com") + `
resource "ataila_user_role_grant" "self" {
  user_id = "00000000-0000-4000-8000-000000000001"
  role    = "users-read-global"
}
`,
				ExpectError: words("The user is a service account"),
			},
			{
				PreConfig: func() {
					if n := m.Calls("PUT", "/users"); n != 5 {
						t.Errorf("PUT sent %d times, want 5: a refusal is never retried", n)
					}
				},
				Config: providerBlock(true) + userHCL("granted@example.com"),
			},
		},
	})
}

// A role the user already holds is adopted, with a warning.
func TestAccUserRoleGrant_AdoptsAHeldRole(t *testing.T) {
	m := newMock(t)
	id := m.AddPerson("holder@example.com", "Holder", "")
	m.SetPersonRole(id, "users-read-global", true)
	factories, rec := recordingProvider()
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{{
			Config: providerBlock(false) + fmt.Sprintf(`
resource "ataila_user_role_grant" "test" {
  user_id = %q
  role    = "users-read-global"
}
`, id),
			Check: rec.expectWarning("Existing role grant adopted", "already held users-read-global"),
		}},
	})
}
