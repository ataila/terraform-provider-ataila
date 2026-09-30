# Every active holder of a role; every page is read.
data "ataila_users" "user_admins" {
  role      = "users-admin-global"
  is_active = true
}

output "user_admins" {
  value = [for u in data.ataila_users.user_admins.users : u.email]
}
