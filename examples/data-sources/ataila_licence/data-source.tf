data "ataila_licence" "this" {}

output "licence" {
  value = {
    state       = data.ataila_licence.this.state
    tier        = data.ataila_licence.this.tier
    modules     = data.ataila_licence.this.modules
    valid_until = data.ataila_licence.this.valid_until
  }
}
