# What the shop application shows, and where each value comes from.
data "ataila_brand_app" "shop" {
  app_slug = "shop"
}

output "shop_colour" {
  value = "${data.ataila_brand_app.shop.primary_color} (from ${data.ataila_brand_app.shop.source["primary_color"]})"
}
