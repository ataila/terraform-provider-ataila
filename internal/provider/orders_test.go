// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/ataila/terraform-provider-ataila/internal/acctest"
	"github.com/ataila/terraform-provider-ataila/internal/client"
)

// jsonAttr checks that an attribute holds JSON text whose decoded value has
// member == want.
func jsonAttr(name, key, member string, want any) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[name]
		if !ok {
			return fmt.Errorf("%s is not in the state", name)
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(rs.Primary.Attributes[key]), &v); err != nil {
			return fmt.Errorf("%s.%s is not a JSON object: %v", name, key, err)
		}
		if fmt.Sprint(v[member]) != fmt.Sprint(want) {
			return fmt.Errorf("%s.%s: %s = %v, want %v", name, key, member, v[member], want)
		}
		return nil
	}
}

// The catalogue: every enabled item in display order, the free-form members
// as JSON text.
func TestAccCatalogue_Items(t *testing.T) {
	ordersMock(t)
	ds := "data.ataila_catalogue_items.all"
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config: providerBlock(false) + "data \"ataila_catalogue_items\" \"all\" {}\n",
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(ds, "items.#", "3"),
				resource.TestCheckResourceAttr(ds, "items.0.key", "vdi-desktop"),
				resource.TestCheckResourceAttr(ds, "items.2.key", "ai-gateway-key"),
				resource.TestCheckResourceAttr(ds, "items.2.kind", "ai-gateway-key"),
				resource.TestCheckResourceAttr(ds, "items.2.edition", "sp"),
				resource.TestCheckResourceAttr(ds, "items.2.requires_approval", "false"),
				resource.TestCheckResourceAttr(ds, "items.2.sort_order", "30"),
				resource.TestCheckNoResourceAttr(ds, "items.2.price_hint_json"),
				jsonAttr(ds, "items.0.price_hint_json", "unit_keys", map[string]any{"vcpu_month": "vcpu"}),
				jsonAttr(ds, "items.2.spec_schema_json", "type", "object"),
				jsonAttr(ds, "items.2.quota_dimensions_json", "ai_tpm", map[string]any{"field": "tpm"}),
			),
		}},
	})
}

// A tenant's orders, newest first, filtered by state; one order with its
// timeline; an AI key order names the key it delivered, never its value.
func TestAccOrders_ReadBack(t *testing.T) {
	m := ordersMock(t)
	_, tenantID := m.AddCustomer("EXAMPLE", "example")
	_, otherID := m.AddCustomer("OTHER", "other")
	keyID := "6f1d2a7e-1111-4c4c-8d8d-0123456789ab"
	delivered := m.AddOrder(tenantID, acctest.MockOrder{Dispatch: map[string]any{
		"key_id": keyID, "key_alias": "example-dev-chat", "outcome": "delivered"}})
	waiting := m.AddOrder(tenantID, acctest.MockOrder{Status: "awaiting_approval"})
	m.AddOrder(otherID, acctest.MockOrder{})
	all, one, filtered := "data.ataila_orders.all", "data.ataila_order.one", "data.ataila_orders.waiting"
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config: providerBlock(false) + fmt.Sprintf(`
data "ataila_orders" "all" {
  tenant_id = %[1]q
}

data "ataila_orders" "waiting" {
  tenant_id = %[1]q
  status    = "awaiting_approval"
}

data "ataila_order" "one" {
  id = %[2]q
}
`, tenantID, delivered),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(all, "orders.#", "2"),
				resource.TestCheckResourceAttr(all, "orders.0.id", waiting), // newest first
				resource.TestCheckResourceAttr(all, "orders.1.id", delivered),
				resource.TestCheckResourceAttr(all, "orders.1.tenant_name", "EXAMPLE Ltd"),
				resource.TestCheckResourceAttr(all, "orders.1.key_alias", "example-dev-chat"),
				resource.TestCheckResourceAttr(all, "orders.1.key_id", keyID),
				resource.TestCheckResourceAttr(all, "orders.1.quota_result", "within"),
				resource.TestCheckResourceAttr(all, "orders.1.quota_decision", "auto_approved"),
				resource.TestCheckResourceAttr(all, "orders.1.operation_id", "order:"+delivered),
				resource.TestCheckNoResourceAttr(all, "orders.0.key_alias"),
				resource.TestCheckResourceAttr(all, "orders.0.dispatch_json", "{}"),
				resource.TestCheckResourceAttr(filtered, "orders.#", "1"),
				resource.TestCheckResourceAttr(filtered, "orders.0.status", "awaiting_approval"),
				resource.TestCheckResourceAttr(one, "status", "delivered"),
				resource.TestCheckResourceAttr(one, "catalogue_item_key", "ai-gateway-key"),
				resource.TestCheckResourceAttr(one, "key_alias", "example-dev-chat"),
				resource.TestCheckResourceAttr(one, "events.#", "2"),
				resource.TestCheckResourceAttr(one, "events.0.event", "submitted"),
				resource.TestCheckResourceAttr(one, "events.1.actor", "system:quota-engine"),
				resource.TestCheckResourceAttr(one, "events.1.detail_json", `{"result":"within"}`),
				jsonAttr(one, "spec_json", "tpm", 50000),
				jsonAttr(one, "quota_check_json", "decision", "auto_approved"),
				jsonAttr(one, "dispatch_json", "outcome", "delivered"),
				resource.TestCheckResourceAttr(one, "created_at", "2026-10-06T09:01:00Z"),
			),
		}},
	})
	if n := m.Mutations(); n != 0 {
		t.Errorf("reading orders sent %d requests that could change something", n)
	}
}

// Errors: an order or a tenant that does not exist; a state the platform does
// not know (refused at plan time); and, on a platform release before the one
// that serves orders, every order data source refused before any request.
func TestAccOrders_Refusals(t *testing.T) {
	m := ordersMock(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{
				Config:      providerBlock(false) + "data \"ataila_order\" \"x\" {\n  id = \"00000000-0000-4000-8000-00000000dead\"\n}\n",
				ExpectError: words("No such order"),
			},
			{
				Config:      providerBlock(false) + "data \"ataila_orders\" \"x\" {\n  tenant_id = \"00000000-0000-4000-8000-00000000dead\"\n}\n",
				ExpectError: words("No such tenant"),
			},
			{
				Config: providerBlock(false) + "data \"ataila_orders\" \"x\" {\n  tenant_id = \"00000000-0000-4000-8000-00000000dead\"\n" +
					"  status    = \"lost\"\n}\n",
				ExpectError: words("lost"),
			},
		},
	})
	below := releaseBelow(t, client.ReleaseQuotasOrders)
	m.SetMeta(func(meta map[string]any) { meta["platform_version"] = below })
	m.ServeQuotasOrders(false)
	before := m.Calls("GET", "/catalogue") + m.Calls("GET", "/orders/")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{
				Config:      providerBlock(false) + "data \"ataila_catalogue_items\" \"all\" {}\n",
				ExpectError: words("ataila_catalogue_items data source needs platform release " + client.ReleaseQuotasOrders),
			},
			{
				Config:      providerBlock(false) + "data \"ataila_order\" \"x\" {\n  id = \"00000000-0000-4000-8000-00000000dead\"\n}\n",
				ExpectError: words("This platform is release " + below),
			},
		},
	})
	if n := m.Calls("GET", "/catalogue") + m.Calls("GET", "/orders/") - before; n != 0 {
		t.Errorf("%d order requests to a platform that does not serve them", n)
	}
}

// Without an orders key the platform refuses the read; the error names the keys.
func TestAccOrders_NeedTheOrdersKey(t *testing.T) {
	m := ordersMock(t)
	m.SetScopes(acctest.DefaultScopes()...)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config:      providerBlock(false) + "data \"ataila_catalogue_items\" \"all\" {}\n",
			ExpectError: words("orders-read-global"),
		}},
	})
}
