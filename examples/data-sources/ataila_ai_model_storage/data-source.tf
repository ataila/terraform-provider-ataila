data "ataila_ai_model_storage" "this" {}

output "central_shares" {
  value = data.ataila_ai_model_storage.this.shares
}
