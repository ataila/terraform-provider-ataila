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

const tenantAddr = "ataila_tenant.test"

// tenantHCL is a customer "test" and a tenant "test" under it.
func tenantHCL(allowDestroy bool, slug string, extra ...string) string {
	return providerBlock(allowDestroy) + customerHCL("EXAMPLE", "example") + fmt.Sprintf(`
resource "ataila_tenant" "test" {
  customer_id = ataila_customer.test.id
  slug        = %q
  name        = "Build Farm"
%s
}
`, slug, indent(extra))
}

func gone(get func(string) (map[string]any, bool), id *string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if obj, found := get(*id); found {
			return fmt.Errorf("%s still exists: %v", *id, obj)
		}
		return nil
	}
}

func TestAccTenantResource_Lifecycle(t *testing.T) {
	m := newMock(t)
	var id, customer string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		CheckDestroy: resource.ComposeAggregateTestCheckFunc(
			gone(m.Tenant, &id),
			archived(m, &customer),
		),
		Steps: []resource.TestStep{
			{
				Config: tenantHCL(false, "example-builds"),
				Check: resource.ComposeAggregateTestCheckFunc(
					stateAttr(tenantAddr, "id", &id),
					stateAttr(customerAddr, "id", &customer),
					resource.TestCheckResourceAttrPair(tenantAddr, "customer_id", customerAddr, "id"),
					resource.TestCheckResourceAttr(tenantAddr, "is_primary", "false"),
					resource.TestCheckResourceAttr(tenantAddr, "project_count", "0"),
					resource.TestCheckResourceAttr(tenantAddr, "member_count", "0"),
					resource.TestCheckNoResourceAttr(tenantAddr, "description"),
					resource.TestCheckNoResourceAttr(tenantAddr, "default_router_id"),
					resource.TestCheckResourceAttrSet(tenantAddr, "created_at"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				Config: tenantHCL(false, "example-builds",
					`description       = "CI runners"`, `default_router_id = "2"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(tenantAddr, plancheck.ResourceActionUpdate),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPtr(tenantAddr, "id", &id),
					resource.TestCheckResourceAttr(tenantAddr, "description", "CI runners"),
					resource.TestCheckResourceAttr(tenantAddr, "default_router_id", "2"),
					mockField(m.Tenant, &id, "default_router_id", "2"),
				),
			},
			{
				// Without description it is cleared; without default_router_id it stays.
				Config: tenantHCL(false, "example-builds"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(tenantAddr, "description"),
					mockField(m.Tenant, &id, "description", nil),
					mockField(m.Tenant, &id, "default_router_id", "2"),
				),
			},
			{ResourceName: tenantAddr, ImportState: true, ImportStateVerify: true},
			{ResourceName: tenantAddr, ImportState: true, ImportStateId: "slug:example-builds", ImportStateVerify: true},
			{
				ResourceName: tenantAddr, ImportState: true, ImportStateId: "slug:no-such-tenant",
				ExpectError: words(`0 tenants have slug "no-such-tenant"`),
			},
			{PreConfig: allowDestroyEverywhere(m), Config: tenantHCL(true, "example-builds")},
		},
	})
}

func TestAccTenantResource_FrozenKeysFailThePlan(t *testing.T) {
	m := newMock(t)
	var mutations int
	other := `
resource "ataila_customer" "other" {
  short_name            = "OTHER"
  long_name             = "Other Company"
  gitlab_group          = "other"
  primary_contact_email = "ops@other.example"
  primary_contact_name  = "Ops Desk"
}
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{
				Config: tenantHCL(false, "example-builds") + other,
				Check:  check(func() error { mutations = m.Mutations(); return nil }),
			},
			{
				Config:      tenantHCL(false, "example-ci") + other,
				PlanOnly:    true,
				ExpectError: words("Cannot change slug of an existing tenant .* Replacing a tenant would delete it"),
			},
			{
				Config: providerBlock(false) + customerHCL("EXAMPLE", "example") + other + `
resource "ataila_tenant" "test" {
  customer_id = ataila_customer.other.id
  slug        = "example-builds"
  name        = "Build Farm"
}
`,
				PlanOnly:    true,
				ExpectError: words("Cannot change customer_id of an existing tenant"),
			},
			{
				PreConfig: func() {
					if got := m.Mutations(); got != mutations {
						t.Errorf("refused frozen-key changes sent %d mutating requests", got-mutations)
					}
					m.SetTokenAllowDestroy(true)
				},
				Config: tenantHCL(true, "example-builds") + other,
			},
		},
	})
}

// Changes in the portal show in the next plan: a renamed tenant is renamed
// back, a deleted one is created again.
func TestAccTenantResource_Drift(t *testing.T) {
	m := newMock(t)
	var id string
	cfg := tenantHCL(false, "example-builds")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{Config: cfg, Check: stateAttr(tenantAddr, "id", &id)},
			{
				PreConfig: func() { m.SetTenantField(id, "name", "Renamed In The Portal") },
				Config:    cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(tenantAddr, plancheck.ResourceActionUpdate),
					},
				},
				Check: mockField(m.Tenant, &id, "name", "Build Farm"),
			},
			{
				PreConfig: func() { m.RemoveTenant(id) },
				Config:    cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(tenantAddr, plancheck.ResourceActionCreate),
					},
				},
				Check: stateAttr(tenantAddr, "id", &id),
			},
			{PreConfig: allowDestroyEverywhere(m), Config: tenantHCL(true, "example-builds")},
		},
	})
}

func TestAccTenantResource_DestroyRefusedByDefault(t *testing.T) {
	m := newMock(t)
	var id string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		CheckDestroy:             gone(m.Tenant, &id),
		Steps: []resource.TestStep{
			{Config: tenantHCL(false, "example-builds"), Check: stateAttr(tenantAddr, "id", &id)},
			{
				Config:  tenantHCL(false, "example-builds"),
				Destroy: true,
				ExpectError: words("Destroying a tenant is not allowed .* allow_destroy = true " +
					".* tofu state rm ataila_tenant.<name> .* terraform state rm ataila_tenant.<name>"),
			},
			{
				PreConfig: func() {
					if n := m.Calls("DELETE", "/"); n != 0 {
						t.Errorf("%d DELETE requests were sent although destroy is off", n)
					}
					m.SetTokenAllowDestroy(true)
				},
				Config: tenantHCL(true, "example-builds"),
			},
		},
	})
}

// The platform deletes only an empty tenant, and never a primary one; each
// refusal is reported with its code and what blocks it.
func TestAccTenantResource_DestroyRefusedByThePlatform(t *testing.T) {
	m := newMock(t)
	var id, primary string
	primaryHCL := `
resource "ataila_tenant" "primary" {
  customer_id = ataila_customer.test.id
  slug        = "example"
  name        = "Example Holdings Ltd"
}
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		PreCheck:                 allowDestroyEverywhere(m),
		CheckDestroy:             gone(m.Tenant, &id),
		Steps: []resource.TestStep{
			{
				Config: tenantHCL(true, "example-builds"),
				Check: resource.ComposeAggregateTestCheckFunc(
					stateAttr(tenantAddr, "id", &id),
					stateAttr(customerAddr, "primary_tenant_id", &primary),
				),
			},
			{
				PreConfig: func() { m.SetTenantBlockers(id, 1, 2, 0, 0) },
				Config:    tenantHCL(true, "example-builds"),
				Destroy:   true,
				ExpectError: words("The platform refused to delete the tenant (tenant_has_projects) " +
					".* Only an empty tenant can be deleted .* blockers: contracts=2, projects=1"),
			},
			{
				PreConfig: func() { m.SetTenantBlockers(id, 0, 0, 0, 0) },
				Config:    tenantHCL(true, "example-builds") + primaryHCL,
				// Adopt the customer's primary tenant into the state.
				ResourceName:       "ataila_tenant.primary",
				ImportState:        true,
				ImportStateId:      "slug:example",
				ImportStatePersist: true,
			},
			{
				Config: tenantHCL(true, "example-builds") + primaryHCL,
				Check:  resource.TestCheckResourceAttr("ataila_tenant.primary", "is_primary", "true"),
			},
			{
				// Removing it from the configuration asks the platform to delete it.
				Config:      tenantHCL(true, "example-builds"),
				ExpectError: words("The platform refused to delete the tenant (tenant_is_primary) .* never deleted"),
			},
			{
				// Clean-up: the primary tenant leaves the state when it cannot be read.
				PreConfig: func() { m.RemoveTenant(primary) },
				Config:    tenantHCL(true, "example-builds"),
			},
		},
	})
}
