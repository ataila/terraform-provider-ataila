# The exports of a project still waiting to be downloaded in the portal.
data "ataila_project_backup_exports" "ready" {
  project_id = ataila_project.shop.id
  status     = "success"
}
