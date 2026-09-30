data "ataila_ai_model_launch_catalog" "enabled" {
  enabled = true
}

output "launchable" {
  value = [for e in data.ataila_ai_model_launch_catalog.enabled.entries : "${e.host}: ${e.model}"]
}
