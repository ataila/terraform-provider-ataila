data "ataila_ai_nodes" "fleet" {}

output "fleet" {
  value = {
    monitored = data.ataila_ai_nodes.fleet.prometheus_reachable
    nodes     = [for n in data.ataila_ai_nodes.fleet.nodes : n.hostname]
  }
}
