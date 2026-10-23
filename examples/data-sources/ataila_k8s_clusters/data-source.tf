# The clusters a Kubernetes project can be homed on.
data "ataila_k8s_clusters" "all" {}

output "targetable_clusters" {
  value = [for c in data.ataila_k8s_clusters.all.clusters : c.name if c.targetable]
}
