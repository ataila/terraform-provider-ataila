# An automation identity of the customer, allowed to read users and tenancy.
# No token comes from here: mint one in the portal, or link a GitLab project to
# the account there and let the pipeline exchange its ID token (ci_jwt).
resource "ataila_service_account" "ci" {
  name              = "ci-terraform"
  owner_customer_id = ataila_customer.example.id
  roles             = ["users-read-global", "tenancy-read-global"]
}

output "linked_projects" {
  value = [for l in ataila_service_account.ci.ci_links : l.project_path]
}
