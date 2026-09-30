data "ataila_licence_socket_facts" "this" {}

output "sockets_counted" {
  value = data.ataila_licence_socket_facts.this.total
}
