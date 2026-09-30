# Exactly one of id, email or username.
data "ataila_user" "dana" {
  email = "dana.example@example.com"
}

output "dana_roles" {
  value = data.ataila_user.dana.roles
}
