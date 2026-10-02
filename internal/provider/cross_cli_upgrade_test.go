// Copyright (c) 2026 ATAILA Kft.
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"

	"github.com/ataila/terraform-provider-ataila/internal/provider"
)

// envPreviousProviderDir names a directory holding the provider binary of the
// previous release (CI builds it from the newest earlier v* tag).
const envPreviousProviderDir = "ATAILA_PREVIOUS_PROVIDER_DIR"

// TestCrossCLIStateUpgrade proves that a state the previous release wrote
// plans clean under this release, in both CLIs and both orders, and that a
// refresh by this release records this release's schema versions. From 0.6.x
// (schema version 0, the attribute names of then) that is the rename-only
// upgrade to version 1 of the four resources whose attributes 0.7.0 renamed;
// from a release with the same schema versions it is a plain cross-version
// check. The schema versions the previous release writes are read from its
// state, not assumed.
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
	current := currentSchemaVersions(t)

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
		logUpgrade(t, stateSchemaVersions(t, dir, "the previous release's apply"), current)
		c.newTF.noDiff("Terraform, this release, on the previous release's state")
		c.newTofu.noDiff("OpenTofu, this release, on the previous release's Terraform state")
		c.newTF.apply("-refresh-only")
		expectSchemaVersions(t, dir, current, "this release's refresh")
		c.newTF.noDiff("Terraform after the upgrade was written")
		c.newTF.must("destroy", "-input=false", "-no-color", "-auto-approve")
	})

	t.Run("opentofu-first", func(t *testing.T) {
		c, dir := setup(t)
		c.oldTofu.apply()
		logUpgrade(t, stateSchemaVersions(t, dir, "the previous release's apply"), current)
		c.newTofu.noDiff("OpenTofu, this release, on the previous release's state")
		c.newTF.must("state", "replace-provider", "-auto-approve", tofuAddr, terraformAddr)
		c.newTF.noDiff("Terraform, this release, on the previous release's OpenTofu state")
		c.newTofu.apply("-refresh-only")
		expectSchemaVersions(t, dir, current, "this release's refresh")
		c.newTofu.must("destroy", "-input=false", "-no-color", "-auto-approve")
	})
}

// upgradedTypes are the resource types upgradeConfig declares.
var upgradedTypes = []string{"ataila_user", "ataila_project", "ataila_ai_gateway_key", "ataila_ai_model"}

// currentSchemaVersions returns this release's schema version of each of
// upgradedTypes.
func currentSchemaVersions(t *testing.T) map[string]int64 {
	t.Helper()
	server, err := providerserver.NewProtocol6WithError(provider.New(testVersion)())()
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := server.GetProviderSchema(context.Background(), &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	for _, typ := range upgradedTypes {
		s, ok := schemas.ResourceSchemas[typ]
		if !ok {
			t.Fatalf("this release has no resource %s", typ)
		}
		out[typ] = s.Version
	}
	return out
}

// logUpgrade says which schema versions the check upgrades from and to, and
// fails when the previous release wrote a newer version than this one knows.
func logUpgrade(t *testing.T, previous, current map[string]int64) {
	t.Helper()
	for _, typ := range upgradedTypes {
		switch {
		case previous[typ] > current[typ]:
			t.Fatalf("the previous release wrote %s at schema version %d, newer than this release's %d",
				typ, previous[typ], current[typ])
		case previous[typ] < current[typ]:
			t.Logf("%s: upgrading schema version %d to %d", typ, previous[typ], current[typ])
		default:
			t.Logf("%s: schema version %d in both releases", typ, current[typ])
		}
	}
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

// stateSchemaVersions reads the schema version of each of upgradedTypes from
// the state file; every type must be there, each instance at one version.
func stateSchemaVersions(t *testing.T, dir, after string) map[string]int64 {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "terraform.tfstate"))
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		Resources []struct {
			Type      string `json:"type"`
			Instances []struct {
				SchemaVersion int64 `json:"schema_version"`
			} `json:"instances"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	for _, r := range st.Resources {
		for _, typ := range upgradedTypes {
			if r.Type != typ {
				continue
			}
			for _, inst := range r.Instances {
				if v, seen := out[typ]; seen && v != inst.SchemaVersion {
					t.Fatalf("after %s, the instances of %s are at different schema versions", after, typ)
				}
				out[typ] = inst.SchemaVersion
			}
		}
	}
	if len(out) != len(upgradedTypes) {
		t.Fatalf("after %s the state holds %d of the %d upgraded resource types", after, len(out), len(upgradedTypes))
	}
	return out
}

// expectSchemaVersions checks that the state holds each of upgradedTypes at
// the wanted schema version.
func expectSchemaVersions(t *testing.T, dir string, want map[string]int64, after string) {
	t.Helper()
	got := stateSchemaVersions(t, dir, after)
	for _, typ := range upgradedTypes {
		if got[typ] != want[typ] {
			t.Fatalf("after %s, %s is at schema version %d, want %d", after, typ, got[typ], want[typ])
		}
	}
}
