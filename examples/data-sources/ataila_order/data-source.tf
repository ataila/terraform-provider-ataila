data "ataila_order" "chat_key" {
  id = "9b2e4c1d-7a3f-4e5b-8c6d-0f1e2d3c4b5a"
}

# The key the order delivered (never its value), and the order's timeline.
output "delivered_key" {
  value = data.ataila_order.chat_key.key_alias
}

output "timeline" {
  value = [for e in data.ataila_order.chat_key.events : "${e.at} ${e.event} by ${e.actor}"]
}
