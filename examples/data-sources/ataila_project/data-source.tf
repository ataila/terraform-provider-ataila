data "ataila_project" "shop" {
  short_name = "shop"
}

output "shop_repositories" {
  value = [for r in data.ataila_project.shop.gitlab_repositories : r.path]
}
