data "ataila_ai_rate_plan" "example" {
  tenant_id = "3f0c9a52-4d1e-4b8a-9d61-2f5b7e0c1a44"
}

# What each tier costs the tenant today, and why.
output "effective" {
  value = { for e in data.ataila_ai_rate_plan.example.effective : e.tier => {
    eur_per_1m_input = e.eur_per_1m_input
    basis            = e.basis
  } }
}
