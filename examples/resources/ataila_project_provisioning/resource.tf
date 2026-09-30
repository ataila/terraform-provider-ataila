# Provisions the project and waits until every stage is done. After a change
# of the project leaves stages stale, the next plan shows an in-place update
# here, and applying it re-applies just those stages.
resource "ataila_project_provisioning" "shop" {
  project_id = ataila_project.shop.id

  timeouts {
    create = "90m"
    update = "30m"
  }
}

output "shop_provisioned" {
  value = ataila_project_provisioning.shop.provisioned
}
