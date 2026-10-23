# The brand of an application built on the platform. Fields left out are
# inherited from the platform's own brand.
resource "ataila_brand_app" "shop" {
  app_slug             = "shop"
  display_name         = "Example Shop"
  display_name_accent  = "Shop"
  primary_color        = "#0b6e4f"
  header_logo_asset_id = ataila_brand_asset.shop_logo.id
  default_language     = "en"
  legal_privacy_url    = "https://shop.example.com/privacy"

  footer_links = [
    { label = "Privacy", url = "https://shop.example.com/privacy" },
    { label = "Contact", url = "https://shop.example.com/contact" },
  ]
}
