# One item in full, with its thread. Reading it never fetches the brief, so the
# item's status does not change.
data "ataila_feedback_item" "item" {
  number = 41
}
