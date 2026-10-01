// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// envPreviousProviderDir names a directory holding the provider binary of the
// previous release (CI builds it from the newest earlier v* tag).
const envPreviousProviderDir = "ATAILA_PREVIOUS_PROVIDER_DIR"

// TestCrossCLIStateUpgrade proves that a state the previous release wrote
// (schema version 0, with the attribute names of then) plans clean under this
// release, in both CLIs and both orders: the resources whose attributes were
// renamed upgrade their state by renaming, and nothing else changes.
//
// It needs both CLIs and the previous release's binary: set
// ATAILA_CROSS_CLI_TERRAFORM, ATAILA_CROSS_CLI_TOFU and
// ATAILA_PREVIOUS_PROVIDER_DIR.
func TestCrossCLIStateUpgrade(t *testing.T) {
	terraformBin, tofuBin := os.Getenv(envCrossTerraform), os.Getenv(envCrossTofu)
	previousDir := os.Getenv(envPreviousProviderDir)
	if terraformBin == "" || tofuBin == "" || previousDir == "" {
		t.Skipf("set %s, %s and %s to run the state upgrade check", envCrossTerraform, envCrossTofu,
			envPreviousProviderDir)
	}
	name := "terraform-provider-ataila"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if _, err := os.Stat(filepath.Join(previousDir, name)); err != nil {
		t.Fatalf("%s=%s holds no %s: %v", envPreviousProviderDir, previousDir, name, err)
	}
	currentDir := buildProvider(t)

	type clis struct{ oldTF, newTF, oldTofu, newTofu *cli }
	setup := func(t *testing.T) (clis, string) {
		m := newMock(t)
		m.SetTokenAllowDestroy(true)
		_, tenant := m.AddCustomer("UPGR", "upgr")
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(upgradeConfig(tenant)), 0o600); err != nil {
			t.Fatal(err)
		}
		return clis{
			oldTF:   newCLI(t, terraformBin, terraformHost, dir, previousDir),
			newTF:   newCLI(t, terraformBin, terraformHost, dir, currentDir),
			oldTofu: newCLI(t, tofuBin, tofuHost, dir, previousDir),
			newTofu: newCLI(t, tofuBin, tofuHost, dir, currentDir),
		}, dir
	}

	t.Run("terraform-first", func(t *testing.T) {
		c, dir := setup(t)
		c.oldTF.apply()
		expectSchemaVersions(t, dir, 0, "the previous release's apply")
		c.newTF.noDiff("Terraform, this release, on the previous release's state")
		c.newTofu.noDiff("OpenTofu, this release, on the previous release's Terraform state")
		c.newTF.apply("-refresh-only")
		expectSchemaVersions(t, dir, 1, "this release's refresh")
		c.newTF.noDiff("Terraform after the upgrade was written")
		c.newTF.must("destroy", "-input=false", "-no-color", "-auto-approve")
	})

	t.Run("opentofu-first", func(t *testing.T) {
		c, dir := setup(t)
		c.oldTofu.apply()
		expectSchemaVersions(t, dir, 0, "the previous release's apply")
		c.newTofu.noDiff("OpenTofu, this release, on the previous release's state")
		c.newTF.must("state", "replace-provider", "-auto-approve", tofuAddr, terraformAddr)
		c.newTF.noDiff("Terraform, this release, on the previous release's OpenTofu state")
		c.newTofu.apply("-refresh-only")
		expectSchemaVersions(t, dir, 1, "this release's refresh")
		c.newTofu.must("destroy", "-input=false", "-no-color", "-auto-approve")
	})
}

// upgradeConfig declares one of each resource whose attributes were renamed,
// in a form both releases accept.
func upgradeConfig(tenant string) string {
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

resource "ataila_user" "u" {
  email      = "upgrade.check@example.com"
  first_name = "Upgrade"
}

resource "ataila_project" "p" {
  tenant_id        = %[1]q
  short_name       = "upgr"
  gitlab_repo_slug = "upgr-app"
  primary_domain   = "upgr.example.com"
  long_name        = "Upgrade Check"
}

resource "ataila_ai_gateway_key" "k" {
  organization_id = %[1]q
  env             = "dev"
  app             = "upgrade"
  models          = ["general"]
}

resource "ataila_ai_model" "m" {
  repo  = "example-lab/upgrade-model"
  notes = "written by the previous release"
}
`, tenant)
}

// expectSchemaVersions checks the schema version of the four upgraded
// resource types in the state file.
func expectSchemaVersions(t *testing.T, dir string, want int, after string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "terraform.tfstate"))
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		Resources []struct {
			Type      string `json:"type"`
			Instances []struct {
				SchemaVersion int `json:"schema_version"`
			} `json:"instances"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, r := range st.Resources {
		switch r.Type {
		case "ataila_user", "ataila_project", "ataila_ai_gateway_key", "ataila_ai_model":
			seen++
			for _, i := range r.Instances {
				if i.SchemaVersion != want {
					t.Fatalf("after %s, %s is at schema version %d, want %d", after, r.Type, i.SchemaVersion, want)
				}
			}
		}
	}
	if seen != 4 {
		t.Fatalf("after %s the state holds %d of the 4 upgraded resource types", after, seen)
	}
}
