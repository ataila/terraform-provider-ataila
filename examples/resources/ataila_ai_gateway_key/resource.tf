# A virtual key for one tenant's chatbot. Needs a platform with an AI gateway.
resource "ataila_ai_gateway_key" "chatbot" {
  organization_id = ataila_customer.example.primary_tenant_id
  env             = "prod"
  app             = "chatbot"
  models          = ["general", "code"]
  rpm_limit       = 120
  soft_budget_usd = 50
  budget_duration = "30d"

  # Return the value once, into the sensitive `secret` attribute. It is in
  # Vault at `vault_path` either way.
  expose_secret = true

  # Change this to rotate the key.
  rotation_trigger = {
    at = "2026-10"
  }
}

output "chatbot_key" {
  value     = ataila_ai_gateway_key.chatbot.secret
  sensitive = true
}
