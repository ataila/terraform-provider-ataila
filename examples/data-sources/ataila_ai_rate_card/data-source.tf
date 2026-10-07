data "ataila_ai_rate_card" "list" {}

# Tiers whose usage is not priced yet.
output "unpriced_tiers" {
  value = [for t in data.ataila_ai_rate_card.list.tiers : t.tier if t.current == null]
}
