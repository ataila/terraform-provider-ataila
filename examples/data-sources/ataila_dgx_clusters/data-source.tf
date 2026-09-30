data "ataila_dgx_clusters" "all" {}

output "clusters" {
  value = [for c in data.ataila_dgx_clusters.all.clusters : "${c.name}: ${c.status}"]
}
