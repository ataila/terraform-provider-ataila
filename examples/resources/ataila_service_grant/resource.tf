variable "developer_user_id" {
  description = "Id of a platform user who belongs to the customer, as the portal shows it."
  type        = string
}

# The developer may use Coder under the customer's entitlement.
resource "ataila_service_grant" "developer_coder" {
  user_id     = var.developer_user_id
  customer_id = ataila_service_entitlement.coder.customer_id
  service_key = ataila_service_entitlement.coder.service_key
  grant_level = "user"
}
