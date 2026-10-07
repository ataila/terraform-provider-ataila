data "ataila_ai_gateway_usage" "this_month" {}

output "eur_by_tenant" {
  value = { for t in data.ataila_ai_gateway_usage.this_month.tenants : t.tenant_name => t.rated_eur }
}
