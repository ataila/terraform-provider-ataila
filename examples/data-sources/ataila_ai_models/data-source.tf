data "ataila_ai_models" "serving" {
  status = "serving"
}

output "serving_models" {
  value = [for m in data.ataila_ai_models.serving.models : m.repo]
}
