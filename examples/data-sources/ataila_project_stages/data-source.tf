data "ataila_project_stages" "shop" {
  project_id = ataila_project.shop.id
}

output "shop_stages_not_done" {
  value = [for s in data.ataila_project_stages.shop.stages : s.key if s.status != "success" && !s.deferred]
}
