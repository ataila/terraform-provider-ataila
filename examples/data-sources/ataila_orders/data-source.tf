# Every order of one tenant waiting for an operator's decision. Approve or
# reject them in the portal: orders are read-only here.
data "ataila_orders" "waiting" {
  tenant_id = "3f0c9a52-4d1e-4b8a-9d61-2f5b7e0c1a44"
  status    = "awaiting_approval"
}

output "waiting" {
  value = [for o in data.ataila_orders.waiting.orders : "${o.catalogue_item_name} (${o.created_at})"]
}
