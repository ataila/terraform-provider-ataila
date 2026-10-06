data "ataila_catalogue_items" "all" {}

# The order form of the AI gateway key item.
output "ai_key_form" {
  value = jsondecode(one([
    for i in data.ataila_catalogue_items.all.items : i.spec_schema_json if i.key == "ai-gateway-key"
  ]))
}
