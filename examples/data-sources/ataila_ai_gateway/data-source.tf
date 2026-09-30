# Needs a platform with an AI gateway.
data "ataila_ai_gateway" "this" {}

output "openai_base_url" {
  value = data.ataila_ai_gateway.this.base_url
}
