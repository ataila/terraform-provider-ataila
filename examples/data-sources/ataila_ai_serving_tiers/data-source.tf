data "ataila_ai_serving_tiers" "all" {}

output "serving_now" {
  value = { for t in data.ataila_ai_serving_tiers.all.tiers : t.key => t.resolved_model if t.resolved }
}
