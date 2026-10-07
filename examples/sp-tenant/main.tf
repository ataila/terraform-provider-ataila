# A service provider's customer tenant, from code: its quotas, an AI gateway
# key, its own AI rates, and its orders and month's usage read back.
#
#   export ATAILA_ENDPOINT=https://portal.example.com
#   export ATAILA_TOKEN=...   # orders-admin-global, ai-gateway-admin-global,
#                             # tenancy-read-global; no destroy needed to apply
#   (a build that is not published yet: see README.md beside this file)
#   tofu init && tofu apply   # or: terraform init && terraform apply
#
# Needs a platform release that serves tenant quotas and orders, and AI usage
# and rates (the provider names it, and refuses them at plan time, on an
# older platform).

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

variable "plan_from" {
  description = "The first day (YYYY-MM-DD) the tenant's own rates apply; the first of the month prices the whole month."
  type        = string
  default     = null
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

# What the customer pays: its own rate for the tier it uses most, from
# var.plan_from (every other tier costs the list rate). This resource owns the
# tenant's WHOLE plan: a tier left out goes back to the list rate.
resource "ataila_ai_rate_plan" "customer" {
  tenant_id  = data.ataila_tenant.customer.id
  valid_from = var.plan_from

  rates = {
    general = { eur_per_1m_input = 0.06, eur_per_1m_output = 0.24 }
  }
}

# What the customer used this month, rated at that plan.
data "ataila_ai_usage" "customer" {
  tenant_id = data.ataila_tenant.customer.id

  depends_on = [ataila_ai_rate_plan.customer]
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

output "usage_this_month" {
  value = {
    month    = data.ataila_ai_usage.customer.month
    tokens   = data.ataila_ai_usage.customer.totals.total_tokens
    eur      = data.ataila_ai_usage.customer.rated_eur
    basis    = data.ataila_ai_usage.customer.rate_basis
    unpriced = data.ataila_ai_usage.customer.unrated_tiers
    forecast = data.ataila_ai_usage.customer.forecast.rated_eur
  }
}
