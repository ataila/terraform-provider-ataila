# Copies the weights from the central store to the node's local disk.
resource "ataila_ai_model_node_cache" "coder_on_ai_a" {
  model_id = ataila_ai_model.coder.id
  node     = "ai-a"

  timeouts {
    create = "6h"
  }
}
