data "ataila_ai_load_targets" "all" {}

output "loadable_targets" {
  value = [for t in data.ataila_ai_load_targets.all.targets : t.hostname if t.loadable]
}
