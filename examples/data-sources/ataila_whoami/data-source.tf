data "ataila_whoami" "me" {}

output "acting_as" {
  value = "${data.ataila_whoami.me.principal.name} (${data.ataila_whoami.me.auth_kind})"
}

output "token_expires_at" {
  value = data.ataila_whoami.me.expires_at
}
