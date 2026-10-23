# Who changed users in the last week, newest first.
data "ataila_audit_events" "user_changes" {
  action        = "user.*"
  occurred_from = "2026-10-16T00:00:00Z"
  limit         = 200
}

output "user_changes" {
  value = [for e in data.ataila_audit_events.user_changes.events : "${e.occurred_at} ${e.actor} ${e.action} ${e.entity_id}"]
}
