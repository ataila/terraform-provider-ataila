// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"encoding/base64"
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/ataila/terraform-provider-ataila/internal/acctest"
	"github.com/ataila/terraform-provider-ataila/internal/client"
)

const brandAppAddr = "ataila_brand_app.shop"

// brandAppMock serves application profiles (platform 1.0.305 on).
func brandAppMock(t *testing.T) *acctest.MockAPI {
	t.Helper()
	m := newMock(t)
	m.SetMeta(func(meta map[string]any) { meta["platform_version"] = client.ReleaseServiceAccounts })
	return m
}

func brandAppGone(m *acctest.MockAPI, slug string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if p := m.BrandApp(slug); p != nil {
			return fmt.Errorf("the profile of %s still exists: %v", slug, p)
		}
		return nil
	}
}

func TestAccBrandApp_lifecycle(t *testing.T) {
	m := brandAppMock(t)
	logo := base64.StdEncoding.EncodeToString(pngBytes(300, 7))
	asset := fmt.Sprintf(`
resource "ataila_brand_asset" "logo" {
  kind           = "logo"
  content_base64 = %q
}
`, logo)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		CheckDestroy:             brandAppGone(m, "shop"),
		Steps: []resource.TestStep{
			{
				Config: providerBlock(false) + asset + `
resource "ataila_brand_app" "shop" {
  app_slug             = "shop"
  display_name         = "Example Shop"
  display_name_accent  = "Shop"
  primary_color        = "#0b6e4f"
  secondary_color      = "#ffcf00"
  header_logo_asset_id = ataila_brand_asset.logo.id
  footer_links = [
    { label = "Privacy", url = "https://shop.example.com/privacy" },
  ]
  legal_privacy_url = "https://shop.example.com/privacy"
  default_language  = "en"
}

data "ataila_brand_app" "shop" {
  app_slug   = ataila_brand_app.shop.app_slug
  depends_on = [ataila_brand_app.shop]
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(brandAppAddr, "id", "shop"),
					resource.TestCheckResourceAttr(brandAppAddr, "version", "1"),
					resource.TestCheckResourceAttr(brandAppAddr, "footer_links.#", "1"),
					resource.TestCheckNoResourceAttr(brandAppAddr, "font_preset"),
					resource.TestCheckResourceAttrPair(brandAppAddr, "header_logo_asset_id", "ataila_brand_asset.logo", "id"),
					resource.TestCheckResourceAttr("data.ataila_brand_app.shop", "display_name", "Example Shop"),
					resource.TestCheckResourceAttr("data.ataila_brand_app.shop", "primary_color", "#0b6e4f"),
					resource.TestCheckResourceAttr("data.ataila_brand_app.shop", "source.display_name", "profile"),
					resource.TestCheckResourceAttr("data.ataila_brand_app.shop", "source.font_preset", "default"),
					resource.TestCheckResourceAttr("data.ataila_brand_app.shop", "font_preset", "inter"),
					resource.TestCheckResourceAttr("data.ataila_brand_app.shop", "version", "1"),
				),
			},
			{
				// A changed colour, no links at all, the secondary colour inherited again.
				Config: providerBlock(false) + asset + `
resource "ataila_brand_app" "shop" {
  app_slug             = "shop"
  display_name         = "Example Shop"
  display_name_accent  = "Shop"
  primary_color        = "#123456"
  header_logo_asset_id = ataila_brand_asset.logo.id
  footer_links         = []
  legal_privacy_url    = "https://shop.example.com/privacy"
  default_language     = "en"
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(brandAppAddr, "version", "2"),
					resource.TestCheckResourceAttr(brandAppAddr, "primary_color", "#123456"),
					resource.TestCheckResourceAttr(brandAppAddr, "footer_links.#", "0"),
					resource.TestCheckNoResourceAttr(brandAppAddr, "secondary_color"),
					check(func() error {
						p := m.BrandApp("shop")
						if p["secondary_color"] != nil || p["footer_links"] == nil {
							return fmt.Errorf("stored profile %v: want secondary_color null and footer_links []", p)
						}
						return nil
					}),
				),
			},
			{ResourceName: brandAppAddr, ImportState: true, ImportStateId: "shop", ImportStateVerify: true},
		},
	})
}

func TestAccBrandApp_refusesAProfileMadeInThePortal(t *testing.T) {
	m := brandAppMock(t)
	m.SetBrandAppInPortal("shop", map[string]any{"display_name": "From the portal"})
	config := providerBlock(false) + `
resource "ataila_brand_app" "shop" {
  app_slug     = "shop"
  display_name = "Example Shop"
}
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{Config: config, ExpectError: words("The application already has a brand profile")},
			{
				// Imported, the configuration then replaces it.
				Config:             config,
				ResourceName:       brandAppAddr,
				ImportState:        true,
				ImportStateId:      "shop",
				ImportStatePersist: true,
			},
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(brandAppAddr, "display_name", "Example Shop"),
					resource.TestCheckResourceAttr(brandAppAddr, "version", "2"),
				),
			},
		},
	})
}

func TestAccBrandApp_changedInThePortalBetweenReadAndWrite(t *testing.T) {
	m := brandAppMock(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{Config: providerBlock(false) + `
resource "ataila_brand_app" "shop" {
  app_slug     = "shop"
  display_name = "Example Shop"
}
`},
			{
				PreConfig: func() { m.ChangeBrandAppAfterNextRead("shop") },
				Config: providerBlock(false) + `
resource "ataila_brand_app" "shop" {
  app_slug     = "shop"
  display_name = "Example Shop 2"
}
`,
				ExpectError: words("The brand profile changed outside Terraform since the last read"),
			},
		},
	})
}

func TestAccBrandApp_validation(t *testing.T) {
	brandAppMock(t)
	for name, tc := range map[string]struct{ body, err string }{
		"accent not in the name": {`display_name = "Example Shop"
  display_name_accent = "Store"`, "Not part of the display name"},
		"accent without a name": {`display_name_accent = "Shop"`, "An accent needs its name"},
		"upper-case colour":     {`primary_color = "#0B6E4F"`, "lower-case six-digit hex colour"},
		"plain http":            {`legal_imprint_url = "http://shop.example.com/imprint"`, "must start with https://"},
		"bad slug":              {`display_name = "Example"`, "lower-case letters, digits and hyphens"},
	} {
		slug := "shop"
		if name == "bad slug" {
			slug = "Shop.App"
		}
		t.Run(name, func(t *testing.T) {
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: protoV6,
				Steps: []resource.TestStep{{
					Config: providerBlock(false) + fmt.Sprintf(`
resource "ataila_brand_app" "shop" {
  app_slug = %q
  %s
}
`, slug, tc.body),
					PlanOnly:    true,
					ExpectError: words(tc.err),
				}},
			})
		})
	}
}

func TestAccBrandApp_olderPlatformRefused(t *testing.T) {
	m := newMock(t)
	m.SetMeta(func(meta map[string]any) { meta["platform_version"] = "1.0.300" })
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config: providerBlock(false) + `
resource "ataila_brand_app" "shop" {
  app_slug     = "shop"
  display_name = "Example Shop"
}
`,
			ExpectError: words("needs platform release " + client.ReleaseBrandApps + " or later"),
		}},
	})
	if p := m.BrandApp("shop"); p != nil {
		t.Fatalf("nothing should have been sent, the mock holds %v", p)
	}
}
