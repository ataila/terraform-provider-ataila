// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func TestAccUserDataSources(t *testing.T) {
	m := newMock(t)
	for i := 0; i < 205; i++ {
		m.AddPerson(fmt.Sprintf("bulk%03d@example.com", i), "Bulk", fmt.Sprintf("Person%03d", i))
	}
	cfg := providerBlock(true) + userHCL("Dana.Lookup@Example.com", `last_name  = "Lookup"`) + `
data "ataila_user" "by_id" {
  id = ataila_user.test.id
}

data "ataila_user" "by_email" {
  email = "DANA.LOOKUP@example.com"
  depends_on = [ataila_user.test]
}

data "ataila_user" "by_username" {
  username = ataila_user.test.username
}

data "ataila_users" "people" {
  depends_on = [ataila_user.test]
}

data "ataila_users" "admins" {
  role = "admin"
}

data "ataila_users" "everyone" {
  kind       = "all"
  depends_on = [ataila_user.test]
}

data "ataila_permission_catalog" "all" {}

data "ataila_permission_catalog" "users" {
  feature = "users"
}
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		PreCheck:                 allowDestroyEverywhere(m),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					// By id: the address as the platform stores it.
					resource.TestCheckResourceAttr("data.ataila_user.by_id", "email", "dana.lookup@example.com"),
					resource.TestCheckResourceAttrPair("data.ataila_user.by_id", "created_at", userAddr, "created_at"),
					resource.TestCheckResourceAttrPair("data.ataila_user.by_email", "id", userAddr, "id"),
					resource.TestCheckResourceAttr("data.ataila_user.by_email", "email", "DANA.LOOKUP@example.com"),
					resource.TestCheckResourceAttrPair("data.ataila_user.by_username", "id", userAddr, "id"),
					resource.TestCheckResourceAttr("data.ataila_user.by_username", "name", "Dana Lookup"),
					// 205 seeded, the seeded admin and the one created here: two pages of 200.
					resource.TestCheckResourceAttr("data.ataila_users.people", "users.#", "207"),
					resource.TestCheckResourceAttr("data.ataila_users.admins", "users.#", "1"),
					resource.TestCheckResourceAttr("data.ataila_users.admins", "users.0.email", "platform.admin@example.com"),
					resource.TestCheckResourceAttr("data.ataila_users.everyone", "users.#", "208"),
					resource.TestCheckResourceAttr("data.ataila_permission_catalog.all", "permissions.#", "40"),
					resource.TestCheckResourceAttr("data.ataila_permission_catalog.users", "permissions.#", "4"),
					resource.TestCheckResourceAttr("data.ataila_permission_catalog.users", "permissions.0.key", "users-admin-global"),
					resource.TestCheckResourceAttr("data.ataila_permission_catalog.users", "permissions.0.grantable", "true"),
					resource.TestCheckResourceAttr("data.ataila_permission_catalog.users", "permissions.1.key", "users-admin-tenant"),
					resource.TestCheckResourceAttr("data.ataila_permission_catalog.users", "permissions.1.mintable", "false"),
					check(func() error {
						for _, r := range m.Requests() {
							if r.Method == "GET" && r.Path == "/users" && strings.Contains(r.Query, "cursor=") {
								return nil
							}
						}
						return fmt.Errorf("no second page of users was requested")
					}),
				),
			},
			{
				Config:      cfg + "\ndata \"ataila_user\" \"missing\" {\n  email = \"nobody@example.com\"\n}\n",
				ExpectError: words("User not found .* 0 users have e-mail address nobody@example.com"),
			},
			{Config: cfg},
		},
	})
}
