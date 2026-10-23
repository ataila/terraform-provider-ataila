# Certificates that need attention.
data "ataila_certificates" "expiring" {
  status = "expiring"
}
