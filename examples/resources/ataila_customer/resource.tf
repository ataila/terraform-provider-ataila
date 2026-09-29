# A customer, with its primary tenant and GitLab group created alongside.
resource "ataila_customer" "example" {
  short_name            = "EXAMPLE"
  long_name             = "Example Holdings Ltd"
  gitlab_group          = "example"
  primary_contact_email = "it@example.com"
  primary_contact_name  = "Example IT Desk"

  # Optional. Omit customer_index and the platform allocates the next free one.
  edition      = "sp"
  billing_tier = "PAYING"
  notes        = "Managed with OpenTofu / Terraform."
}

output "primary_tenant_id" {
  value = ataila_customer.example.primary_tenant_id
}
