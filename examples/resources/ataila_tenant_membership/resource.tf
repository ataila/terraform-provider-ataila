variable "developer_user_id" {
  description = "Id of a platform user, as the portal shows it."
  type        = string
}

resource "ataila_tenant_membership" "developer" {
  tenant_id = ataila_tenant.builds.id
  user_id   = var.developer_user_id
  role      = "member"
}
