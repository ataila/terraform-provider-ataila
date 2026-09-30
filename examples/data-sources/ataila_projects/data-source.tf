# Every active project of one customer.
data "ataila_projects" "active" {
  customer_id = ataila_customer.example.id
  status      = "active"
}

output "active_projects" {
  value = [for p in data.ataila_projects.active.projects : p.short_name]
}
