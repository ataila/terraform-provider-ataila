# A service provider's customer tenant, from code: its quotas, an AI gateway
# key, and its orders read back.
#
#   export ATAILA_ENDPOINT=https://portal.example.com
#   export ATAILA_TOKEN=...   # orders-admin-global, ai-gateway-admin-global,
#                             # tenancy-read-global; no destroy needed to apply
#   tofu init && tofu apply   # or: terraform init && terraform apply
#
# Needs a platform release that serves tenant quotas and orders (the provider
# names it, and refuses them at plan time, on an older platform).

terraform {
  required_providers {
    ataila = {
      source  = "ataila/ataila"
      version = "~> 1.2"
    }
  }
}

provider "ataila" {}

variable "tenant_slug" {
  description = "The tenant to manage, by slug."
  type        = string
  default     = "example"
}

data "ataila_tenant" "customer" {
  slug = var.tenant_slug
}

# What the customer may consume, and what the platform does over the limit.
# This resource owns the tenant's WHOLE quota set: a dimension left out here
# loses its limit (every order touching it then goes to an approval card).
resource "ataila_tenant_quota" "customer" {
  tenant_id = data.ataila_tenant.customer.id

  quota = {
    ai_tpm              = { limit = 200000 }
    ai_budget_eur_month = { limit = 500, note = "Soft: alerts, never stops serving." }
    desktops            = { limit = 2, policy = "hard_cap" }
  }
}

# One AI gateway key for the customer's application, inside the quota above.
resource "ataila_ai_gateway_key" "app" {
  organization_id = data.ataila_tenant.customer.id
  env             = "dev"
  app             = "chat"
  models          = ["general"]
  tpm_limit       = 50000
  soft_budget_usd = 50
  budget_duration = "30d"

  depends_on = [ataila_tenant_quota.customer]
}

# What the customer ordered itself, from the catalogue in the portal.
data "ataila_orders" "customer" {
  tenant_id = data.ataila_tenant.customer.id

  depends_on = [ataila_tenant_quota.customer]
}

output "quota_usage" {
  value = {
    for u in ataila_tenant_quota.customer.usage : u.dimension => "${coalesce(u.allocated, 0)} of ${coalesce(u.limit, 0)}"
    if u.limit != null
  }
}

output "key_alias" {
  value = ataila_ai_gateway_key.app.key_alias
}

output "orders" {
  value = [for o in data.ataila_orders.customer.orders : {
    item     = o.catalogue_item_key
    status   = o.status
    decision = o.quota_decision
    key      = o.key_alias
  }]
}
