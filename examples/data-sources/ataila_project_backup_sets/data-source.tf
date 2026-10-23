# The newest successful PROD sets of a project.
data "ataila_project_backup_sets" "prod" {
  project_id  = ataila_project.shop.id
  environment = "prod"
  status      = "success"
  limit       = 10
}

output "last_prod_backup" {
  value = try(data.ataila_project_backup_sets.prod.sets[0].finished_at, null)
}
