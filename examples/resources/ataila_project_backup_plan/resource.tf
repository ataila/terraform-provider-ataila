# The default plan, declared: enabled, the default environments, and the
# installation's default tiers followed as they change.
resource "ataila_project_backup_plan" "shop" {
  project_id = ataila_project.shop.id
}

# UAT's database backed up too, and the project's own tiers.
resource "ataila_project_backup_plan" "ledger" {
  project_id = ataila_project.ledger.id

  environments = {
    dev  = { code = true, database = true, files = false }
    uat  = { code = false, database = true, files = false }
    prod = { code = true, database = true, files = true }
  }

  tiers = {
    daily   = { keep = 14, at = "01:30" }
    weekly  = { keep = 8, day = "sun" }
    monthly = { keep = 12, day = 1 }
    yearly  = { keep = 7 }
  }
}
