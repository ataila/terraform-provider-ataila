variable "developer_user_id" {
  description = "Id of a platform user who is a member of one of the customer's tenants."
  type        = string
}

resource "ataila_project_member" "developer" {
  project_id  = ataila_project.shop.id
  user_id     = var.developer_user_id
  role        = "developer"
  gitlab_role = "developer" # recorded, not enforced yet
}
