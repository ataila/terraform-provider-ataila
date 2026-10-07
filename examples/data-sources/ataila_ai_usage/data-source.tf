# This month (leave month out) or any earlier one.
data "ataila_ai_usage" "example" {
  tenant_id = "3f0c9a52-4d1e-4b8a-9d61-2f5b7e0c1a44"
  month     = "2026-10"
}

output "month_to_date" {
  value = {
    tokens   = data.ataila_ai_usage.example.totals.total_tokens
    eur      = data.ataila_ai_usage.example.rated_eur
    complete = data.ataila_ai_usage.example.rated_complete
    unpriced = data.ataila_ai_usage.example.unrated_tiers
    forecast = data.ataila_ai_usage.example.forecast.rated_eur
  }
}
