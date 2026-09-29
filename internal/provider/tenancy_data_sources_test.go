// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

// The customer data source finds the same customer by id, short name and
// GitLab group, and refuses anything but exactly one of them.
func TestAccCustomerDataSource(t *testing.T) {
	m := newMock(t)
	cfg := providerBlock(true) + customerHCL("EXAMPLE", "example", `notes = "Found."`) + `
data "ataila_customer" "by_id" {
  id = ataila_customer.test.id
}

data "ataila_customer" "by_short_name" {
  short_name = ataila_customer.test.short_name
}

data "ataila_customer" "by_group" {
  gitlab_group = ataila_customer.test.gitlab_group
}
`
	same := func(ds string) resource.TestCheckFunc {
		var checks []resource.TestCheckFunc
		for _, a := range []string{"id", "customer_index", "short_name", "long_name", "gitlab_group", "edition",
			"primary_contact_email", "primary_contact_name", "default_email_tier", "billing_tier", "status",
			"notes", "primary_tenant_id", "created_at"} {
			checks = append(checks, resource.TestCheckResourceAttrPair(ds, a, customerAddr, a))
		}
		return resource.ComposeAggregateTestCheckFunc(checks...)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		PreCheck:                 allowDestroyEverywhere(m),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					same("data.ataila_customer.by_id"),
					same("data.ataila_customer.by_short_name"),
					same("data.ataila_customer.by_group"),
				),
			},
			{
				Config:      cfg + "\ndata \"ataila_customer\" \"nothing\" {}\n",
				ExpectError: words("Missing Attribute Configuration"),
			},
			{
				Config:      cfg + "\ndata \"ataila_customer\" \"both\" {\n  id         = \"1\"\n  short_name = \"EXAMPLE\"\n}\n",
				ExpectError: words("Invalid Attribute Combination"),
			},
			{
				Config:      cfg + "\ndata \"ataila_customer\" \"missing\" {\n  short_name = \"NOSUCH\"\n}\n",
				ExpectError: words(`Customer not found .* 0 customers have short_name "NOSUCH"`),
			},
			{Config: cfg},
		},
	})
}

func TestAccTenantDataSource(t *testing.T) {
	m := newMock(t)
	cfg := tenantHCL(true, "example-builds", `description = "CI runners"`) + `
data "ataila_tenant" "by_id" {
  id = ataila_tenant.test.id
}

data "ataila_tenant" "by_slug" {
  slug = ataila_tenant.test.slug
}

data "ataila_tenant" "primary" {
  id = ataila_customer.test.primary_tenant_id
}
`
	same := func(ds string) resource.TestCheckFunc {
		var checks []resource.TestCheckFunc
		for _, a := range []string{"id", "customer_id", "slug", "name", "description", "is_primary", "created_at"} {
			checks = append(checks, resource.TestCheckResourceAttrPair(ds, a, tenantAddr, a))
		}
		return resource.ComposeAggregateTestCheckFunc(checks...)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		PreCheck:                 allowDestroyEverywhere(m),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					same("data.ataila_tenant.by_id"),
					same("data.ataila_tenant.by_slug"),
					resource.TestCheckResourceAttr("data.ataila_tenant.primary", "is_primary", "true"),
					resource.TestCheckResourceAttr("data.ataila_tenant.primary", "slug", "example"),
					resource.TestCheckResourceAttr("data.ataila_tenant.primary", "default_router_id", "3"),
				),
			},
			{
				Config:      cfg + "\ndata \"ataila_tenant\" \"missing\" {\n  slug = \"no-such-tenant\"\n}\n",
				ExpectError: words(`Tenant not found .* 0 tenants have slug "no-such-tenant"`),
			},
			{Config: cfg},
		},
	})
}

// ataila_tenants reads every page: 205 seeded tenants plus the primary one
// need two pages of 200.
func TestAccTenantsDataSource_ReadsEveryPage(t *testing.T) {
	m := newMock(t)
	var customer string
	base := providerBlock(true) + customerHCL("BULK", "bulk") + `
resource "ataila_customer" "other" {
  short_name            = "OTHER"
  long_name             = "Other Company"
  gitlab_group          = "other"
  primary_contact_email = "ops@other.example"
  primary_contact_name  = "Ops Desk"
}
`
	cfg := base + `
data "ataila_tenants" "bulk" {
  customer_id = ataila_customer.test.id
}

data "ataila_tenants" "all" {}
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		PreCheck:                 allowDestroyEverywhere(m),
		Steps: []resource.TestStep{
			{Config: base, Check: stateAttr(customerAddr, "id", &customer)},
			{
				PreConfig: func() { m.SeedTenants(customer, "bulk-t", 205) },
				Config:    cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.ataila_tenants.bulk", "tenants.#", "206"),
					resource.TestCheckResourceAttr("data.ataila_tenants.bulk", "tenants.0.slug", "bulk"),
					resource.TestCheckResourceAttr("data.ataila_tenants.bulk", "tenants.0.is_primary", "true"),
					resource.TestCheckResourceAttr("data.ataila_tenants.bulk", "tenants.205.slug", "bulk-t-204"),
					resource.TestCheckResourceAttr("data.ataila_tenants.all", "tenants.#", "207"),
					resource.TestCheckResourceAttr("data.ataila_tenants.all", "tenants.206.slug", "other"),
					check(func() error {
						for _, r := range m.Requests() {
							if r.Method == "GET" && r.Path == "/tenants" && strings.Contains(r.Query, "cursor=") {
								return nil
							}
						}
						return fmt.Errorf("no second page was requested")
					}),
				),
			},
		},
	})
}
