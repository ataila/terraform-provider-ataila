# The PROD database VMs of a project.
data "ataila_vms" "shop_prod_db" {
  project_id  = ataila_project.shop.id
  environment = "prod"
  role        = "db"
}
