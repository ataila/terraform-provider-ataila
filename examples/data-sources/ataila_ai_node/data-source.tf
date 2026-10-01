data "ataila_ai_node" "ai_a" {
  hostname = "ai-a"
}

output "ai_a_status" {
  value = data.ataila_ai_node.ai_a.monitoring_reachable ? data.ataila_ai_node.ai_a.status : "unknown"
}
