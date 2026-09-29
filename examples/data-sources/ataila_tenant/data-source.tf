# Exactly one of id or slug.
data "ataila_tenant" "builds" {
  slug = "example-builds"
}

output "builds_projects" {
  value = data.ataila_tenant.builds.project_count
}
