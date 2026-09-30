data "ataila_release_state" "shop" {
  project_id = ataila_project.shop.id
}

output "shop_versions" {
  value = { for v in data.ataila_release_state.shop.versions : "${v.env}/${v.component}" => v.last_reported_version }
}
