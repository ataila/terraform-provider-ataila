terraform {
  required_providers {
    ataila = {
      source  = "ataila/ataila"
      # Any 1.x: within a major version nothing is removed or renamed.
      version = "~> 1.0"
    }
  }
}

# The token is best kept out of files: export ATAILA_TOKEN instead.
provider "ataila" {
  endpoint = "https://portal.example.com"

  # Only needed when the portal's certificate comes from a private
  # certificate authority.
  ca_cert_file = "ca.pem"
}
