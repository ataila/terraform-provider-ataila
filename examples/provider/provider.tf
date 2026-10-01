terraform {
  required_providers {
    ataila = {
      source  = "ataila/ataila"
      # 0.x: a minor release may break, so stay on 0.7.x.
      version = "~> 0.7.0"
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
