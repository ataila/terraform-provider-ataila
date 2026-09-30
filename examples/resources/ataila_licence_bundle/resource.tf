# The bundle issued for this platform, kept next to the configuration.
resource "ataila_licence_bundle" "this" {
  bundle = trimspace(file("${path.module}/licence.acplic1"))
}

output "licence_state" {
  value = ataila_licence_bundle.this.state
}
