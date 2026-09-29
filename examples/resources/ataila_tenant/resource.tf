resource "ataila_tenant" "builds" {
  customer_id = ataila_customer.example.id
  slug        = "example-builds"
  name        = "Build Farm"
  description = "CI runners and build caches."
}
