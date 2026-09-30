resource "ataila_project_prod_lock" "shop" {
  project_id = ataila_project.shop.id
  locked     = true
}

# To unlock:
#   locked         = false
#   confirm_unlock = "shop"   # the project's short name
