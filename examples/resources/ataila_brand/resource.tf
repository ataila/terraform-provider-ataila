resource "ataila_brand" "this" {
  product_name        = "Example Cloud"
  product_name_accent = "Cloud"
  brand_color         = "#1e88e5"
  page_title          = "Example Cloud portal"
  logo_size           = "medium"
  logo_asset_id       = ataila_brand_asset.logo.id
}
