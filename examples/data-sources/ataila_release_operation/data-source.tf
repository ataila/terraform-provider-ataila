data "ataila_release_operation" "last" {
  id = ataila_release_promotion.api_prod.id
}

output "prod_request_status" {
  value = data.ataila_release_operation.last.status
}
