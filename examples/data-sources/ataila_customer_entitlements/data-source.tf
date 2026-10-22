data "ataila_customer_entitlements" "example" {
  customer_id = ataila_customer.example.id
}

# Seats left per service.
output "seats_left" {
  value = {
    for e in data.ataila_customer_entitlements.example.entitlements :
    e.service_key => e.seats == null ? null : e.seats - e.seats_used
  }
}
