// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

const membershipAddr = "ataila_tenant_membership.test"

func membershipHCL(allowDestroy bool, userID, role string) string {
	return tenantHCL(allowDestroy, "example-builds") + fmt.Sprintf(`
resource "ataila_tenant_membership" "test" {
  tenant_id = ataila_tenant.test.id
  user_id   = %q
  role      = %q
}
`, userID, role)
}

func role(get func(string, string) string, tenant *string, user string, want string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if got := get(*tenant, user); got != want {
			return fmt.Errorf("mock role of %s = %q, want %q", user, got, want)
		}
		return nil
	}
}

// Create, change the role in place, import, and replace on a different user.
// Memberships are not destroy-gated: replacing one works with allow_destroy off.
func TestAccTenantMembershipResource_Lifecycle(t *testing.T) {
	m := newMock(t)
	alice := m.AddUser("alice@example.com")
	bob := m.AddUser("bob@example.com")
	var tenant string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{
				Config: membershipHCL(false, alice, "viewer"),
				Check: resource.ComposeAggregateTestCheckFunc(
					stateAttr(tenantAddr, "id", &tenant),
					resource.TestCheckResourceAttrPair(membershipAddr, "tenant_id", tenantAddr, "id"),
					resource.TestCheckResourceAttr(membershipAddr, "user_id", alice),
					resource.TestCheckResourceAttr(membershipAddr, "role", "viewer"),
					resource.TestCheckResourceAttrSet(membershipAddr, "created_at"),
					role(m.MembershipRole, &tenant, alice, "viewer"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				Config: membershipHCL(false, alice, "admin"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(membershipAddr, plancheck.ResourceActionUpdate),
					},
				},
				Check: role(m.MembershipRole, &tenant, alice, "admin"),
			},
			{
				ResourceName:      membershipAddr,
				ImportState:       true,
				ImportStateIdFunc: func(*terraform.State) (string, error) { return tenant + "/" + alice, nil },
				ImportStateVerify: true,
			},
			{
				ResourceName: membershipAddr, ImportState: true, ImportStateId: "no-slash",
				ExpectError: words("The import id must be <tenant_id>/<user_id>"),
			},
			{
				Config: membershipHCL(false, bob, "admin"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(membershipAddr, plancheck.ResourceActionReplace),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					role(m.MembershipRole, &tenant, alice, ""),
					role(m.MembershipRole, &tenant, bob, "admin"),
				),
			},
			{
				// Removing the membership needs no allow_destroy.
				Config: tenantHCL(false, "example-builds"),
				Check:  role(m.MembershipRole, &tenant, bob, ""),
			},
			{PreConfig: allowDestroyEverywhere(m), Config: tenantHCL(true, "example-builds")},
		},
	})
}

func TestAccTenantMembershipResource_Drift(t *testing.T) {
	m := newMock(t)
	carol := m.AddUser("carol@example.com")
	var tenant string
	cfg := membershipHCL(false, carol, "member")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{Config: cfg, Check: stateAttr(tenantAddr, "id", &tenant)},
			{
				PreConfig: func() { m.SetMembership(tenant, carol, "owner") },
				Config:    cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(membershipAddr, plancheck.ResourceActionUpdate),
					},
				},
				Check: role(m.MembershipRole, &tenant, carol, "member"),
			},
			{
				PreConfig: func() { m.SetMembership(tenant, carol, "") },
				Config:    cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(membershipAddr, plancheck.ResourceActionCreate),
					},
				},
				Check: role(m.MembershipRole, &tenant, carol, "member"),
			},
			{PreConfig: allowDestroyEverywhere(m), Config: membershipHCL(true, carol, "member")},
		},
	})
}

// The customer's primary contact is already a member of the primary tenant;
// declaring that membership adopts it, sets the role, and says so.
func TestAccTenantMembershipResource_AdoptsAnExistingMembership(t *testing.T) {
	m := newMock(t)
	contact := m.AddUser("ops@example.com")
	factories, rec := recordingProvider()
	var primary string
	cfg := providerBlock(true) + customerHCL("EXAMPLE", "example") + fmt.Sprintf(`
resource "ataila_tenant_membership" "test" {
  tenant_id = ataila_customer.test.primary_tenant_id
  user_id   = %q
  role      = "owner"
}
`, contact)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		PreCheck:                 allowDestroyEverywhere(m),
		Steps: []resource.TestStep{{
			Config: cfg,
			Check: resource.ComposeAggregateTestCheckFunc(
				stateAttr(customerAddr, "primary_tenant_id", &primary),
				role(m.MembershipRole, &primary, contact, "owner"),
				rec.expectWarning("Existing membership adopted", "set its role to owner"),
			),
		}},
	})
}

// The legacy role developer: an imported membership holding it shows no
// difference while the configuration leaves role out or says developer;
// planning to set it, on a new or an existing membership, fails the plan.
func TestAccTenantMembershipResource_LegacyDeveloperRole(t *testing.T) {
	m := newMock(t)
	dev := m.AddUser("legacy.dev@example.com")
	other := m.AddUser("new.dev@example.com")
	var tenant string
	withRole := func(user, roleLine string) string {
		return tenantHCL(false, "example-builds") + fmt.Sprintf(`
resource "ataila_tenant_membership" "test" {
  tenant_id = ataila_tenant.test.id
  user_id   = %q
%s
}
`, user, roleLine)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{
				Config: tenantHCL(false, "example-builds"),
				Check:  stateAttr(tenantAddr, "id", &tenant),
			},
			{
				// A legacy membership, made outside the provider.
				PreConfig:          func() { m.SetMembership(tenant, dev, "developer") },
				Config:             withRole(dev, ""),
				ResourceName:       membershipAddr,
				ImportState:        true,
				ImportStateIdFunc:  func(*terraform.State) (string, error) { return tenant + "/" + dev, nil },
				ImportStatePersist: true,
			},
			{
				// Role left out: no difference, the legacy role stays.
				Config:   withRole(dev, ""),
				PlanOnly: true,
			},
			{
				Config:   withRole(dev, `  role      = "developer"`),
				PlanOnly: true,
			},
			{
				// Moving away from developer is an ordinary change.
				Config: withRole(dev, `  role      = "member"`),
				Check:  role(m.MembershipRole, &tenant, dev, "member"),
			},
			{
				// Back to developer: refused at plan time.
				Config:      withRole(dev, `  role      = "developer"`),
				PlanOnly:    true,
				ExpectError: words("The role developer can no longer be set"),
			},
			{
				// A new membership (another user, so a replacement) cannot be developer either.
				Config:      withRole(other, `  role      = "developer"`),
				PlanOnly:    true,
				ExpectError: words("The role developer can no longer be set"),
			},
			{
				// A new membership needs a role.
				Config:      withRole(other, ""),
				PlanOnly:    true,
				ExpectError: words("A new membership needs a role"),
			},
			{PreConfig: allowDestroyEverywhere(m), Config: tenantHCL(true, "example-builds")},
		},
	})
}
