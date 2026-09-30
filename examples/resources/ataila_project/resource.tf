# A project record. Creating it provisions nothing: it stays "planned" until
# an ataila_project_provisioning runs.
resource "ataila_project" "shop" {
  tenant_id        = ataila_tenant.builds.id
  short_name       = "shop"
  gitlab_repo_slug = "shop-app"
  primary_domain   = "shop.example.com"
  long_name        = "Example Shop"

  # Settings left out get the platform's default at create.
  frontend_variant = "vue"
  enable_ai        = true
}

output "shop_frontend_url" {
  value = ataila_project.shop.urls.frontend
}
