# The blocker bugs planned for a week, on the platform that issues releases.
data "ataila_feedback_items" "blockers" {
  kind = "blocker_bug"
  week = "2026-W44"
}

output "blockers" {
  value = [for i in data.ataila_feedback_items.blockers.items : "#${i.number} ${i.title} (${i.status})"]
}
