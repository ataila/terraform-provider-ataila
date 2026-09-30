data "ataila_brand_asset" "logo" {
  sha256 = filesha256("${path.module}/brand/logo.png")
}

output "logo_url" {
  value = data.ataila_brand_asset.logo.url
}
