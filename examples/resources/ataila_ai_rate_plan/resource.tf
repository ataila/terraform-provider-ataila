# The tenant's WHOLE rate plan: its own price for these tiers. A tier that is
# not listed costs the list rate (ataila_ai_rate_card). Destroying this ends
# the plan today.
resource "ataila_ai_rate_plan" "example" {
  tenant_id = ataila_customer.example.primary_tenant_id

  # Price the whole month at the plan (rating happens when usage is read).
  valid_from = "2026-10-01"

  rates = {
    general = { eur_per_1m_input = 0.06, eur_per_1m_output = 0.24, note = "Pilot pricing." }
    embed   = { eur_per_1m_input = 0.01, eur_per_1m_output = 0 }
  }
}
