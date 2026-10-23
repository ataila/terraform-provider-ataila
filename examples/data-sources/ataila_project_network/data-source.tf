data "ataila_project_network" "shop" {
  project_id = ataila_project.shop.id
}

# Every VM whose firewall the platform has not applied yet.
output "not_yet_protected" {
  value = [for vm in data.ataila_project_network.shop.vms : vm.name if !(vm.enabled && vm.applied)]
}
