data "ataila_brand" "this" {}

output "brand_color" {
  value = data.ataila_brand.this.brand_color
}
