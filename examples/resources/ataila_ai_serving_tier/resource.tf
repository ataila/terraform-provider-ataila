# Pin the "code" tier to one served model. Destroying this only forgets it.
resource "ataila_ai_serving_tier" "code" {
  key          = "code"
  pinned_model = "model-code-large"
  enabled      = true
}
