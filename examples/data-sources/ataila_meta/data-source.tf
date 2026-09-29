data "ataila_meta" "this" {}

output "platform" {
  value = {
    api_version = data.ataila_meta.this.api_version
    tier        = data.ataila_meta.this.tier
    licence     = data.ataila_meta.this.licence.state
  }
}
