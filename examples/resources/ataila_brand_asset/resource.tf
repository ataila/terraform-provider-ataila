# Everything uploaded is readable without signing in: upload only public files.
resource "ataila_brand_asset" "logo" {
  kind   = "logo"
  source = "${path.module}/brand/logo.png"
}

resource "ataila_brand_asset" "favicon" {
  kind   = "favicon"
  source = "${path.module}/brand/favicon.ico"
}
