// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ataila/terraform-provider-ataila/internal/acctest"
)

// Environment variables naming the two CLIs for TestCrossCLIState.
const (
	envCrossTerraform = "ATAILA_CROSS_CLI_TERRAFORM"
	envCrossTofu      = "ATAILA_CROSS_CLI_TOFU"
)

// The provider address each CLI writes into a state for source "ataila/ataila".
const (
	terraformAddr = terraformHost + "/ataila/ataila"
	tofuAddr      = tofuHost + "/ataila/ataila"
)

// TestCrossCLIState proves what happens when one working directory, one
// configuration (`source = "ataila/ataila"`) and one state file are used by
// both CLIs, in both orders, with the provider installed as a user installs it
// (each CLI knows it under its own registry's address only):
//
//   - OpenTofu reads a state Terraform wrote, unaided: it maps
//     registry.terraform.io/ataila/ataila to its own registry by itself.
//   - Terraform does NOT read a state OpenTofu wrote: the state names
//     registry.opentofu.org/ataila/ataila, which Terraform does not know.
//     After `terraform state replace-provider` it reads it without a diff.
//
// Each order ends with the second CLI changing something and the first one
// reading that back without a diff. The README section "Switching between
// OpenTofu and Terraform" documents the commands.
//
// It needs both CLIs: set ATAILA_CROSS_CLI_TERRAFORM and ATAILA_CROSS_CLI_TOFU.
func TestCrossCLIState(t *testing.T) {
	terraformBin, tofuBin := os.Getenv(envCrossTerraform), os.Getenv(envCrossTofu)
	if terraformBin == "" || tofuBin == "" {
		t.Skipf("set %s and %s to run the cross-CLI state check", envCrossTerraform, envCrossTofu)
	}
	providerDir := buildProvider(t)

	type pair struct{ terraform, tofu *cli }
	setup := func(t *testing.T) (pair, func(string)) {
		m := newMock(t)
		m.SetTokenAllowDestroy(true)
		m.ProvisionInstantly(true)
		user := m.AddUser("dana@example.com")
		_, projectTenant := m.AddCustomer("SHOPS", "shops")
		m.SetMembership(projectTenant, user, "member")
		dir := t.TempDir()
		write := func(description string) {
			t.Helper()
			cfg := crossConfig(description, user) + crossProjectConfig(projectTenant, user) + crossBrandConfig()
			if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(cfg), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return pair{
			terraform: newCLI(t, terraformBin, terraformHost, dir, providerDir),
			tofu:      newCLI(t, tofuBin, tofuHost, dir, providerDir),
		}, write
	}
	expectProviders := func(t *testing.T, dir, want, after string) {
		t.Helper()
		got := stateProviders(t, dir)
		if len(got) != 1 || got[0] != fmt.Sprintf("%q", "provider[\""+want+"\"]") {
			t.Fatalf("after %s the state names %v, want only %s", after, got, want)
		}
	}
	// terraformRefuses shows that Terraform cannot use a state OpenTofu wrote.
	terraformRefuses := func(t *testing.T, tf *cli) {
		t.Helper()
		out, code := tf.run("plan", "-input=false", "-no-color", "-detailed-exitcode")
		if code != 1 || !strings.Contains(out, tofuAddr) {
			t.Fatalf("terraform plan on an OpenTofu state: exit %d, want 1 naming %s:\n%s", code, tofuAddr, out)
		}
		t.Logf("terraform refuses as expected: %s", errorBlock(out))
	}
	replaceForTerraform := func(tf *cli) {
		tf.must("state", "replace-provider", "-auto-approve", tofuAddr, terraformAddr)
	}

	t.Run("terraform-first", func(t *testing.T) {
		p, write := setup(t)
		write("Written by Terraform.")
		p.terraform.apply()
		expectProviders(t, p.terraform.dir, terraformAddr, "terraform apply")

		// OpenTofu reads Terraform's state unaided.
		p.tofu.noDiff("OpenTofu reading the state Terraform wrote")

		write("Changed by OpenTofu.")
		p.tofu.apply()
		expectProviders(t, p.tofu.dir, tofuAddr, "tofu apply")

		// Back to Terraform: refused until the address is mapped.
		terraformRefuses(t, p.terraform)
		replaceForTerraform(p.terraform)
		p.terraform.noDiff("Terraform reading the state OpenTofu wrote, after replace-provider")
		p.terraform.must("destroy", "-input=false", "-no-color", "-auto-approve")
	})

	t.Run("opentofu-first", func(t *testing.T) {
		p, write := setup(t)
		write("Written by OpenTofu.")
		p.tofu.apply()
		expectProviders(t, p.tofu.dir, tofuAddr, "tofu apply")

		// Terraform cannot read it until the address is mapped.
		terraformRefuses(t, p.terraform)
		replaceForTerraform(p.terraform)
		expectProviders(t, p.terraform.dir, terraformAddr, "terraform state replace-provider")
		p.terraform.noDiff("Terraform reading the state OpenTofu wrote, after replace-provider")

		write("Changed by Terraform.")
		p.terraform.apply()

		// OpenTofu reads it back unaided; its optional replace-provider works too.
		p.tofu.noDiff("OpenTofu reading the state Terraform wrote")
		p.tofu.must("state", "replace-provider", "-auto-approve", terraformAddr, tofuAddr)
		expectProviders(t, p.tofu.dir, tofuAddr, "tofu state replace-provider")
		p.tofu.noDiff("OpenTofu after its own replace-provider")
		p.tofu.must("destroy", "-input=false", "-no-color", "-auto-approve")
	})
}

func crossConfig(description, user string) string {
	return fmt.Sprintf(`
terraform {
  required_providers {
    ataila = {
      source = "ataila/ataila"
    }
  }
}

provider "ataila" {
  allow_destroy = true
}

resource "ataila_customer" "c" {
  short_name            = "CROSS"
  long_name             = "Cross Check Ltd"
  gitlab_group          = "cross"
  primary_contact_email = "Ops@Cross.EXAMPLE"
  primary_contact_name  = "Ops Desk"
  status                = "suspended"
}

resource "ataila_tenant" "t" {
  customer_id = ataila_customer.c.id
  slug        = "cross-builds"
  name        = "Builds"
  description = %q
}

resource "ataila_tenant_membership" "m" {
  tenant_id = ataila_tenant.t.id
  user_id   = %q
  role      = "admin"
}

data "ataila_tenants" "all" {
  customer_id = ataila_customer.c.id
  depends_on  = [ataila_tenant.t]
}

output "tenant_count" {
  value = length(data.ataila_tenants.all.tenants)
}

output "tenant_created_at" {
  value = ataila_tenant.t.created_at
}
`, description, user)
}

// crossProjectConfig adds a project, its provisioning, a member, a release
// promotion, its PROD data lock and an AI model. The primary domain is
// written in mixed case, which the platform lower-cases, and a benchmark with
// a trailing zero, which it drops.
func crossProjectConfig(tenantID, user string) string {
	return fmt.Sprintf(`
resource "ataila_project" "p" {
  tenant_id        = %q
  short_name       = "cross"
  gitlab_repo_slug = "cross-app"
  primary_domain   = "Cross.Example.com"
  long_name        = "Cross Shop"
  enable_ai        = true
}

resource "ataila_project_provisioning" "p" {
  project_id = ataila_project.p.id
}

resource "ataila_project_member" "m" {
  project_id = ataila_project.p.id
  user_id    = %q
  role       = "viewer"
}

output "project_frontend" {
  value = ataila_project.p.urls.frontend
}

resource "ataila_release_promotion" "r" {
  project_id = ataila_project.p.id
  component  = "app-api"
  target_env = "dev"
  version    = "1.0.0"
}

resource "ataila_project_prod_lock" "l" {
  project_id = ataila_project.p.id
  locked     = true
}

resource "ataila_ai_model" "m" {
  repo       = "example-lab/cross-model"
  benchmarks = { score = "80.50" }
}
`, tenantID, user)
}

// crossBrandConfig adds the licence bundle, the brand (its colour in upper
// case, which the platform lower-cases) and a brand asset.
func crossBrandConfig() string {
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR\x00\x00\x01\x90\x00\x00\x00\xc8\x08\x06\x00\x00\x00")
	return fmt.Sprintf(`
resource "ataila_licence_bundle" "l" {
  bundle = %q
}

resource "ataila_brand_asset" "a" {
  kind           = "logo"
  content_base64 = %q
}

resource "ataila_brand" "b" {
  product_name  = "Cross Cloud"
  brand_color   = "#ABCDEF"
  page_title    = "Cross"
  logo_asset_id = ataila_brand_asset.a.id
}
`, acctest.MockBundle(3, acctest.MockInstanceID, "mock-valid"), base64.StdEncoding.EncodeToString(png))
}

// errorBlock is the CLI's first error, its title and text on one line.
func errorBlock(out string) string {
	i := strings.Index(out, "Error:")
	if i < 0 {
		return "(no error in the output)"
	}
	return strings.Join(strings.Fields(out[i:min(len(out), i+400)]), " ")
}
