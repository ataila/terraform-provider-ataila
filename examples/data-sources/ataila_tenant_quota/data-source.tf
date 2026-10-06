data "ataila_tenant_quota" "example" {
  tenant_id = "3f0c9a52-4d1e-4b8a-9d61-2f5b7e0c1a44"
}

# What the tenant holds against each limit.
output "usage" {
  value = {
    for u in data.ataila_tenant_quota.example.usage : u.dimension => {
      limit     = u.limit
      allocated = u.allocated
      remaining = u.remaining
    }
  }
}
