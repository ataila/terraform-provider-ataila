data "ataila_customer" "example" {
  short_name = "EXAMPLE"
}

# Every tenant of one customer; omit customer_id for every tenant on the platform.
data "ataila_tenants" "example" {
  customer_id = data.ataila_customer.example.id
}

output "tenant_slugs" {
  value = [for t in data.ataila_tenants.example.tenants : t.slug]
}
