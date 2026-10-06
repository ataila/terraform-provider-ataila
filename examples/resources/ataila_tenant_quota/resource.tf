# The WHOLE quota set of one tenant. A dimension that is not listed here has no
# limit after the apply: every order touching it then goes to an operator's
# approval card. Destroying this removes every limit of the tenant.
resource "ataila_tenant_quota" "example" {
  tenant_id = ataila_customer.example.primary_tenant_id

  quota = {
    # AI gateway tokens per minute: the sum of the tenant's key limits.
    ai_tpm = { limit = 200000 }

    # Monthly AI budget in EUR. Over it, an order goes to an approval card.
    ai_budget_eur_month = { limit = 500, policy = "auto", note = "Soft budget, reviewed monthly." }

    # Desktops: refused outright above two.
    desktops = { limit = 2, policy = "hard_cap" }
  }
}

output "ai_tpm_remaining" {
  value = one([for u in ataila_tenant_quota.example.usage : u.remaining if u.dimension == "ai_tpm"])
}
