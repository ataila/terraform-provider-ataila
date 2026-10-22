data "ataila_services" "all" {}

# The services that need an operator's approval before they are in force.
output "needs_approval" {
  value = [for s in data.ataila_services.all.services : s.key if s.requires_approval]
}
