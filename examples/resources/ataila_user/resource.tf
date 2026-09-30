# A person. No password is sent or returned: they sign in after a password
# reset in the portal or a self-service reset.
resource "ataila_user" "dana" {
  email            = "dana.example@example.com"
  first_name       = "Dana"
  last_name        = "Example"
  locale           = "en"
  needs_git_access = true
}

output "dana_username" {
  value = ataila_user.dana.username
}
