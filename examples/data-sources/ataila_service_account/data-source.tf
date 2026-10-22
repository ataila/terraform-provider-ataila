# A service account made in the portal, read by name.
data "ataila_service_account" "deployer" {
  name = "ci-deployer"
}

output "deployer_live_tokens" {
  value = data.ataila_service_account.deployer.active_tokens
}
