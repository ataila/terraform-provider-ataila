# The customer holds Coder for up to ten people.
resource "ataila_service_entitlement" "coder" {
  customer_id = ataila_customer.example.id
  service_key = "coder"
  seats       = 10
}
