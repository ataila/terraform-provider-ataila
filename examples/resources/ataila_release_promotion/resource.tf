# DEV deploys a named build.
resource "ataila_release_promotion" "api_dev" {
  project_id = ataila_project.shop.id
  component  = "app-api"
  target_env = "dev"
  version    = "1.4.0"
}

# UAT takes the version DEV last reported.
resource "ataila_release_promotion" "api_uat" {
  project_id = ataila_project.shop.id
  component  = "app-api"
  target_env = "uat"

  depends_on = [ataila_release_promotion.api_dev]
}

# PROD is a request a person approves in the portal; wait for the decision.
resource "ataila_release_promotion" "api_prod" {
  project_id        = ataila_project.shop.id
  component         = "app-api"
  target_env        = "prod"
  wait_for_approval = true

  timeouts {
    create = "4h"
  }

  depends_on = [ataila_release_promotion.api_uat]
}
