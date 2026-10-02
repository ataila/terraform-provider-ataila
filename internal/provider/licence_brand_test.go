// Copyright (c) 2026 ATAILA Kft.
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/ataila/terraform-provider-ataila/internal/acctest"
	"github.com/ataila/terraform-provider-ataila/internal/client"
)

const (
	bundleAddr = "ataila_licence_bundle.this"
	brandAddr  = "ataila_brand.this"
	assetAddr  = "ataila_brand_asset.logo"
)

func bundleHCL(bundle string) string {
	return providerBlock(false) + fmt.Sprintf(`
resource "ataila_licence_bundle" "this" {
  bundle = %q
}
`, bundle)
}

func validBundle(epoch int) string {
	return acctest.MockBundle(epoch, acctest.MockInstanceID, "mock-valid")
}

func digestOf(t *testing.T, bundle string) string {
	t.Helper()
	d, err := client.BundleDocumentDigest(bundle)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func puts(m *acctest.MockAPI, path string, want int) resource.TestCheckFunc {
	return check(func() error {
		if n := m.Calls("PUT", path); n != want {
			return fmt.Errorf("%d PUT %s, want %d", n, path, want)
		}
		return nil
	})
}

func TestBundleDocumentDigest(t *testing.T) {
	b := validBundle(3)
	d := digestOf(t, b)
	if len(d) != 64 {
		t.Fatalf("digest %q", d)
	}
	// The raw JSON form gives the same digest.
	raw, _ := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(b, "acplic1."))
	if d2 := digestOf(t, "  "+string(raw)+"\n"); d2 != d {
		t.Errorf("raw JSON digest %s, want %s", d2, d)
	}
	for _, bad := range []string{"", "acplic1.!!!", "hello", "acplic1." + base64.RawURLEncoding.EncodeToString([]byte(`{"x":1}`))} {
		if _, err := client.BundleDocumentDigest(bad); err == nil {
			t.Errorf("%q: no error", bad)
		}
	}
}

// Install, adopt the same document, refuse an older one after a newer was
// installed outside, move to the newer one without re-sending it, import,
// and forget on destroy.
func TestAccLicenceBundle_Lifecycle(t *testing.T) {
	m := newMock(t)
	b3, b5, b7 := validBundle(3), validBundle(5), validBundle(7)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{
				Config: bundleHCL(b3),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(bundleAddr, "id", "current"),
					resource.TestCheckResourceAttr(bundleAddr, "state", "ACTIVE"),
					resource.TestCheckResourceAttr(bundleAddr, "licence_epoch", "3"),
					resource.TestCheckResourceAttr(bundleAddr, "tier", "sp"),
					resource.TestCheckResourceAttr(bundleAddr, "document_digest", digestOf(t, b3)),
					resource.TestCheckResourceAttrSet(bundleAddr, "installed_at"),
					puts(m, "/licence/bundle", 1),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// A newer bundle was installed outside: the older one is refused, not retried.
				PreConfig:   func() { m.InstallLicence(b5) },
				Config:      bundleHCL(b3),
				ExpectError: words("The platform refused the bundle (stale_epoch) .* epoch 5"),
			},
			{
				// Configuring the installed bundle adopts it without sending it.
				Config: bundleHCL(b5),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(bundleAddr, "licence_epoch", "5"),
					resource.TestCheckResourceAttr(bundleAddr, "document_digest", digestOf(t, b5)),
					puts(m, "/licence/bundle", 2),
				),
			},
			{
				Config: bundleHCL(b7),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(bundleAddr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(bundleAddr, "licence_epoch", "7"),
					puts(m, "/licence/bundle", 3),
				),
			},
			{
				ResourceName: bundleAddr, ImportState: true, ImportStateId: "current",
				ImportStateVerify: true, ImportStateVerifyIgnore: []string{"bundle"},
			},
			{
				ResourceName: bundleAddr, ImportState: true, ImportStateId: "licence",
				ExpectError: words(`its import id is "current"`),
			},
			{
				// Destroy forgets: the licence stays installed.
				Config: providerBlock(false),
				Check: check(func() error {
					if m.LicenceDigest() != digestOf(t, b7) {
						return fmt.Errorf("the licence changed on destroy")
					}
					return nil
				}),
			},
		},
	})
}

// An import followed by the configured bundle, which is the installed one,
// sends nothing.
func TestAccLicenceBundle_ImportThenConfigure(t *testing.T) {
	m := newMock(t)
	b4 := validBundle(4)
	m.InstallLicence(b4)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{
				Config: bundleHCL(b4), ResourceName: bundleAddr, ImportState: true, ImportStateId: "current",
				ImportStatePersist: true,
			},
			{
				Config: bundleHCL(b4),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(bundleAddr, "licence_epoch", "4"),
					puts(m, "/licence/bundle", 0),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// What the platform refuses, and what the provider refuses first; the
// bundle is never echoed.
func TestAccLicenceBundle_Refusals(t *testing.T) {
	m := newMock(t)
	_ = m
	other := acctest.MockBundle(3, "inst-other", "mock-valid")
	forged := acctest.MockBundle(3, acctest.MockInstanceID, "forged")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{Config: bundleHCL("not-a-bundle-value"), ExpectError: words("Not a licence bundle")},
			{Config: bundleHCL(other), ExpectError: words("instance_mismatch")},
			{Config: bundleHCL(forged), ExpectError: words("bundle_invalid")},
		},
	})
}

func TestAccLicenceDataSources(t *testing.T) {
	m := newMock(t)
	b := validBundle(2)
	m.InstallLicence(b)
	cfg := providerBlock(false) + `
data "ataila_licence" "this" {}
data "ataila_licence_socket_facts" "this" {}
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.ataila_licence.this", "state", "ACTIVE"),
					resource.TestCheckResourceAttr("data.ataila_licence.this", "tier", "sp"),
					resource.TestCheckResourceAttr("data.ataila_licence.this", "modules.#", "2"),
					resource.TestCheckResourceAttr("data.ataila_licence.this", "document_digest", digestOf(t, b)),
					resource.TestCheckResourceAttr("data.ataila_licence.this", "serial_masked", "false"),
					resource.TestCheckResourceAttr("data.ataila_licence.this", "sockets.licensed", "8"),
					resource.TestCheckResourceAttr("data.ataila_licence.this", "sp_mode_enabled", "true"),
					resource.TestCheckResourceAttrSet("data.ataila_licence.this", "valid_until"),
					resource.TestCheckNoResourceAttr("data.ataila_licence.this", "bundle"),
					resource.TestCheckResourceAttr("data.ataila_licence_socket_facts.this", "total", "3"),
					resource.TestCheckResourceAttr("data.ataila_licence_socket_facts.this", "facts.#", "2"),
					resource.TestCheckResourceAttr("data.ataila_licence_socket_facts.this", "facts.0.node_name", "node-a"),
					resource.TestCheckResourceAttr("data.ataila_licence_socket_facts.this", "chain.intact", "true"),
				),
			},
			{
				// A read-only token sees the serial masked.
				PreConfig: func() { m.SetScopes("licence-read-global") },
				Config:    cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.ataila_licence.this", "serial_masked", "true"),
					resource.TestCheckResourceAttr("data.ataila_licence.this", "serial", "****-3333"),
				),
			},
		},
	})
}

// ── brand ────────────────────────────────────────────────────────────────────

func brandHCL(extra ...string) string {
	return providerBlock(false) + fmt.Sprintf(`
resource "ataila_brand" "this" {
  product_name = "Example Cloud"
  brand_color  = "#1E88E5"
  page_title   = "Example Cloud portal"
%s
}
`, indent(extra))
}

// ifMatches are the If-Match headers of every PUT /brand.
func ifMatches(m *acctest.MockAPI) []string {
	var out []string
	for _, r := range m.Requests() {
		if r.Method == "PUT" && r.Path == "/brand" {
			out = append(out, r.Header.Get("If-Match"))
		}
	}
	return out
}

func expectIfMatch(m *acctest.MockAPI, want ...string) resource.TestCheckFunc {
	return check(func() error {
		if got := ifMatches(m); fmt.Sprint(got) != fmt.Sprint(want) {
			return fmt.Errorf("If-Match headers %q, want %q", got, want)
		}
		return nil
	})
}

func TestAccBrand_Lifecycle(t *testing.T) {
	m := newMock(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{
				Config: brandHCL(),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(brandAddr, "id", "current"),
					resource.TestCheckResourceAttr(brandAddr, "brand_color", "#1E88E5"),
					resource.TestCheckResourceAttr(brandAddr, "product_name_accent", ""),
					resource.TestCheckResourceAttr(brandAddr, "logo_size", "regular"),
					resource.TestCheckResourceAttr(brandAddr, "version", "2"),
					resource.TestCheckNoResourceAttr(brandAddr, "attribution"),
					resource.TestCheckNoResourceAttr(brandAddr, "first_party"),
					check(func() error {
						if b := m.Brand(); b["brand_color"] != "#1e88e5" || b["product_name_accent"] != "" {
							return fmt.Errorf("mock brand %v", b)
						}
						return nil
					}),
					expectIfMatch(m, `"1"`),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				Config: brandHCL(`product_name_accent = "Cloud"`, `logo_offset_x = -4`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(brandAddr, "version", "3"),
					expectIfMatch(m, `"1"`, `"2"`),
				),
			},
			{
				// Changed in the portal: the refresh sees it and the write names the new version.
				PreConfig: func() { m.SetBrandField("product_name", "Someone Else") },
				Config:    brandHCL(`product_name_accent = "Cloud"`, `logo_offset_x = -4`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(brandAddr, "product_name", "Example Cloud"),
					expectIfMatch(m, `"1"`, `"2"`, `"4"`),
				),
			},
			{
				// Changed in the portal after the read: 412, reported, not retried.
				PreConfig:   func() { m.ChangeBrandAfterNextRead() },
				Config:      brandHCL(`product_name_accent = "Cloud"`, `logo_offset_x = 4`),
				ExpectError: words("The brand changed outside Terraform since the last read .* Run plan again"),
			},
			{
				Config: brandHCL(`product_name_accent = "Cloud"`, `logo_offset_x = 4`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(brandAddr, "page_title", "Example Cloud portal"),
					check(func() error {
						// One PUT refused (412), then one accepted: never a silent retry.
						if n := len(ifMatches(m)); n != 5 {
							return fmt.Errorf("%d PUT /brand, want 5", n)
						}
						return nil
					}),
				),
			},
			{
				// An import has no configured spelling of the colour: it reads the stored lower case.
				ResourceName: brandAddr, ImportState: true, ImportStateId: "current", ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"brand_color"},
			},
			{
				Config:      brandHCL(`product_name_accent = "Sky"`),
				ExpectError: words("Not part of the product name"),
			},
			{
				Config: providerBlock(false),
				Check: check(func() error {
					if b := m.Brand(); b["product_name"] != "Example Cloud" {
						return fmt.Errorf("destroy changed the brand: %v", b)
					}
					return nil
				}),
			},
		},
	})
}

// An imported brand whose colour the configuration writes in upper case
// plans nothing.
func TestAccBrand_ImportNoDiff(t *testing.T) {
	m := newMock(t)
	for k, v := range map[string]any{"product_name": "Example Cloud", "product_name_accent": "",
		"brand_color": "#1e88e5", "page_title": "Example Cloud portal"} {
		m.SetBrandField(k, v)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{Config: brandHCL(), ResourceName: brandAddr, ImportState: true, ImportStateId: "current", ImportStatePersist: true},
			{Config: brandHCL(), PlanOnly: true},
		},
	})
}

// ── brand assets ─────────────────────────────────────────────────────────────

// pngBytes is a minimal PNG header of the given width; salt varies the bytes.
func pngBytes(width uint32, salt byte) []byte {
	b := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR")
	b = binary.BigEndian.AppendUint32(b, width)
	b = binary.BigEndian.AppendUint32(b, width/2)
	return append(b, 8, 6, 0, 0, 0, salt, salt, salt, salt)
}

func sha(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func assetHCL(path string, extra ...string) string {
	return fmt.Sprintf(`
resource "ataila_brand_asset" "logo" {
  kind   = "logo"
  source = %q
%s
}
`, filepath.ToSlash(path), indent(extra))
}

func TestAccBrandAsset_Lifecycle(t *testing.T) {
	m := newMock(t)
	factories, rec := recordingProvider()
	dir := t.TempDir()
	logo, logo2 := filepath.Join(dir, "logo.png"), filepath.Join(dir, "logo-copy.png")
	v1, v2 := pngBytes(400, 1), pngBytes(400, 2)
	write := func(p string, b []byte) {
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(logo, v1)
	write(logo2, v1)
	var first, second string
	withBrand := func(path string) string {
		return brandHCL(`logo_asset_id = ataila_brand_asset.logo.id`) + assetHCL(path)
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: withBrand(logo),
				Check: resource.ComposeAggregateTestCheckFunc(
					stateAttr(assetAddr, "id", &first),
					resource.TestCheckResourceAttr(assetAddr, "sha256", sha(v1)),
					resource.TestCheckResourceAttr(assetAddr, "filename", "logo.png"),
					resource.TestCheckResourceAttr(assetAddr, "mime", "image/png"),
					resource.TestCheckResourceAttr(assetAddr, "width", "400"),
					resource.TestCheckResourceAttr(assetAddr, "url", "/api/brand/assets/"+sha(v1)),
					resource.TestCheckResourceAttrPair(brandAddr, "logo_asset_id", assetAddr, "id"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// Another path to the same bytes: nothing is uploaded or replaced.
				Config: withBrand(logo2),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(assetAddr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPtr(assetAddr, "id", &first),
					check(func() error {
						if n := m.Calls("POST", "/brand/assets"); n != 1 {
							return fmt.Errorf("%d uploads, want 1", n)
						}
						return nil
					}),
				),
			},
			{
				// A changed file replaces the asset; the old one stays on the platform.
				PreConfig: func() { write(logo2, v2) },
				Config:    withBrand(logo2),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(assetAddr, plancheck.ResourceActionDestroyBeforeCreate),
						plancheck.ExpectResourceAction(brandAddr, plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					stateAttr(assetAddr, "id", &second),
					resource.TestCheckResourceAttr(assetAddr, "sha256", sha(v2)),
					resource.TestCheckResourceAttrPair(brandAddr, "logo_asset_id", assetAddr, "id"),
					check(func() error {
						if first == second || m.BrandAssetCount() != 2 {
							return fmt.Errorf("first %s second %s, %d assets", first, second, m.BrandAssetCount())
						}
						return nil
					}),
				),
			},
			{ResourceName: assetAddr, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"source"}},
			{
				// The same bytes again (base64 this time) adopt the stored asset.
				Config: brandHCL() + fmt.Sprintf(`
resource "ataila_brand_asset" "logo" {
  kind           = "logo"
  content_base64 = %q
}
`, base64.StdEncoding.EncodeToString(v1)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPtr(assetAddr, "id", &first),
					rec.expectWarning("Existing brand asset adopted"),
				),
			},
			{
				// The same bytes as the other kind are refused.
				Config: brandHCL() + fmt.Sprintf(`
resource "ataila_brand_asset" "logo" {
  kind           = "logo"
  content_base64 = %q
}
resource "ataila_brand_asset" "icon" {
  kind           = "favicon"
  content_base64 = %q
}
`, base64.StdEncoding.EncodeToString(v1), base64.StdEncoding.EncodeToString(v1)),
				ExpectError: words("The same file is stored as another kind"),
			},
			{
				// A logo asset where a favicon belongs: the platform validates the reference.
				Config: brandHCL(`favicon_asset_id = ataila_brand_asset.logo.id`) + fmt.Sprintf(`
resource "ataila_brand_asset" "logo" {
  kind           = "logo"
  content_base64 = %q
}
`, base64.StdEncoding.EncodeToString(v1)),
				ExpectError: words("asset_wrong_kind"),
			},
			{
				// Destroy forgets: every asset stays.
				Config: providerBlock(false),
				Check: check(func() error {
					if m.BrandAssetCount() != 2 {
						return fmt.Errorf("%d assets after destroy, want 2", m.BrandAssetCount())
					}
					return nil
				}),
			},
		},
	})
}

func TestAccBrandAsset_Refusals(t *testing.T) {
	m := newMock(t)
	m.SetBrandStorage(false)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{
				Config: providerBlock(false) + fmt.Sprintf(`
resource "ataila_brand_asset" "logo" {
  kind           = "logo"
  content_base64 = %q
}
`, base64.StdEncoding.EncodeToString(pngBytes(400, 9))),
				ExpectError: words("The platform cannot store brand assets"),
				Check:       nil,
			},
			{
				Config: providerBlock(false) + fmt.Sprintf(`
resource "ataila_brand_asset" "logo" {
  kind           = "logo"
  content_base64 = %q
}
`, base64.StdEncoding.EncodeToString(pngBytes(50, 9))),
				PreConfig:   func() { m.SetBrandStorage(true) },
				ExpectError: words("asset_invalid"),
			},
			{
				Config: providerBlock(false) + `
resource "ataila_brand_asset" "logo" {
  kind = "logo"
}
`,
				ExpectError: words("Missing Attribute Configuration"),
			},
		},
		CheckDestroy: func(*terraform.State) error {
			// A final 503 is not retried.
			if n := m.Calls("POST", "/brand/assets"); n != 2 {
				return fmt.Errorf("%d uploads, want 2", n)
			}
			return nil
		},
	})
}

func TestAccBrandDataSources(t *testing.T) {
	m := newMock(t)
	png := pngBytes(300, 3)
	cfg := providerBlock(false) + fmt.Sprintf(`
resource "ataila_brand_asset" "logo" {
  kind           = "logo"
  content_base64 = %q
}
data "ataila_brand" "this" {}
data "ataila_brand_asset" "by_id" {
  id = ataila_brand_asset.logo.id
}
data "ataila_brand_asset" "by_sha" {
  sha256     = %q
  depends_on = [ataila_brand_asset.logo]
}
`, base64.StdEncoding.EncodeToString(png), sha(png))
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config: cfg,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.ataila_brand.this", "product_name", "Example Cloud"),
				resource.TestCheckResourceAttr("data.ataila_brand.this", "version", "1"),
				resource.TestCheckResourceAttr("data.ataila_brand.this", "attribution", "Powered by the mock platform"),
				resource.TestCheckResourceAttr("data.ataila_brand.this", "first_party", "false"),
				resource.TestCheckResourceAttrPair("data.ataila_brand_asset.by_id", "sha256", assetAddr, "sha256"),
				resource.TestCheckResourceAttrPair("data.ataila_brand_asset.by_sha", "id", assetAddr, "id"),
				resource.TestCheckResourceAttr("data.ataila_brand_asset.by_sha", "width", "300"),
				check(func() error {
					if m.BrandAssetCount() != 1 {
						return fmt.Errorf("%d assets", m.BrandAssetCount())
					}
					return nil
				}),
			),
		}},
	})
}
