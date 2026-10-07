# The list rate of the "general" tier, from the first of October. A second
# apply changes nothing; destroying this only forgets it (a list rate is never
# removed on the platform).
resource "ataila_ai_rate_card" "general" {
  tier              = "general"
  eur_per_1m_input  = 0.09
  eur_per_1m_output = 0.36
  valid_from        = "2026-10-01"
  note              = "List rate, Q4."
}
