# Exactly one of id, short_name or gitlab_group.
data "ataila_customer" "example" {
  short_name = "EXAMPLE"
}

output "example_status" {
  value = data.ataila_customer.example.status
}
