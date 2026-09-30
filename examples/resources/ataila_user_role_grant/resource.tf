# One role for one person. The token applying this must itself carry the role.
resource "ataila_user_role_grant" "dana_reads_users" {
  user_id = ataila_user.dana.id
  role    = "users-read-global"
}
