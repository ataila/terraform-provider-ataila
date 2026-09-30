data "ataila_permission_catalog" "users" {
  feature = "users"
}

output "grantable_user_keys" {
  value = [for p in data.ataila_permission_catalog.users.permissions : p.key if p.grantable]
}
