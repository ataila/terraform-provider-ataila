data "ataila_ai_model" "coder" {
  repo = "example-lab/example-coder-32B"
}

output "coder_status" {
  value = data.ataila_ai_model.coder.status
}
