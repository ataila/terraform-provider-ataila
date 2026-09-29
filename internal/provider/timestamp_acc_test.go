// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ataila/terraform-provider-ataila/internal/provider"
)

// TestAccTimestampRepresentation proves that a change in how a timestamp is
// written is never a change: the state's *_at values are rewritten to the
// same instants in another form (+00:00 instead of Z, six fractional digits,
// or none), and
//
//   - plan -detailed-exitcode finds nothing to do, although an output shows
//     created_at (a string comparison would report the output as changed);
//   - an update applied with -refresh=false, whose plan carries the rewritten
//     created_at, completes (a string comparison would make the provider's
//     answer an "inconsistent result").
//
// A refresh-only apply must also keep the state's representation.
//
// It runs the CLI of the acceptance matrix (TF_ACC_TERRAFORM_PATH) against the
// built provider, because the state has to be edited between commands.
func TestAccTimestampRepresentation(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance test: set TF_ACC=1 and TF_ACC_TERRAFORM_PATH")
	}
	bin := os.Getenv("TF_ACC_TERRAFORM_PATH")
	if bin == "" {
		t.Fatal("TF_ACC_TERRAFORM_PATH is not set")
	}
	host := terraformHost
	if strings.Contains(os.Getenv("TF_ACC_PROVIDER_HOST"), "opentofu") {
		host = tofuHost
	}
	m := newMock(t)
	m.SetTokenAllowDestroy(true)
	dir := t.TempDir()
	c := newCLI(t, bin, host, dir, buildProvider(t))
	write := func(name string) {
		cfg := `
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
  short_name            = "STAMP"
  long_name             = "Stamp Ltd"
  gitlab_group          = "stamp"
  primary_contact_email = "ops@stamp.example"
  primary_contact_name  = "Ops Desk"
}

resource "ataila_tenant" "t" {
  customer_id = ataila_customer.c.id
  slug        = "stamp-lab"
  name        = "` + name + `"
}

output "customer_created_at" {
  value = ataila_customer.c.created_at
}

output "tenant_times" {
  value = [ataila_tenant.t.created_at, ataila_tenant.t.updated_at]
}
`
		if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write("Lab")
	c.apply()
	rewritten := rewriteStateTimestamps(t, dir)
	if rewritten < 3 {
		t.Fatalf("only %d timestamps were rewritten", rewritten)
	}

	c.noDiff("plan after the timestamps' representation changed")

	// A refresh that reads the same instants keeps the state's representation.
	c.apply("-refresh-only")
	stateKeeps(t, dir, "+00:00")

	write("Laboratory")
	c.apply("-refresh=false")
	stateKeeps(t, dir, "+00:00")
	c.noDiff("plan after an update planned from the rewritten state")
	c.must("destroy", "-input=false", "-no-color", "-auto-approve")
}

// rewriteStateTimestamps rewrites every *_at string attribute of every
// resource and every output to the same instant written with a +00:00 offset
// and six fractional digits, and returns how many it rewrote.
func rewriteStateTimestamps(t *testing.T, dir string) int {
	t.Helper()
	file := filepath.Join(dir, "terraform.tfstate")
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	n := 0
	other := func(s string) (string, bool) {
		at, err := provider.ParseTimestamp(s)
		if err != nil {
			return s, false
		}
		out := at.UTC().Format("2006-01-02T15:04:05.000000-07:00")
		if out == s {
			return s, false
		}
		if !provider.SameInstant(s, out) {
			t.Fatalf("rewrote %s to another instant %s", s, out)
		}
		n++
		return out, true
	}
	resources, _ := st["resources"].([]any)
	for _, r := range resources {
		instances, _ := r.(map[string]any)["instances"].([]any)
		for _, inst := range instances {
			attrs, _ := inst.(map[string]any)["attributes"].(map[string]any)
			for k, v := range attrs {
				if s, ok := v.(string); ok && strings.HasSuffix(k, "_at") {
					attrs[k], _ = other(s)
				}
			}
		}
	}
	outputs, _ := st["outputs"].(map[string]any)
	for _, o := range outputs {
		om := o.(map[string]any)
		switch v := om["value"].(type) {
		case string:
			om["value"], _ = other(v)
		case []any:
			for i, e := range v {
				if s, ok := e.(string); ok {
					v[i], _ = other(s)
				}
			}
		}
	}
	out, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, out, 0o600); err != nil {
		t.Fatal(err)
	}
	return n
}

// stateKeeps checks that the state still holds the rewritten representation:
// a refresh that finds the same instant keeps what the state had.
func stateKeeps(t *testing.T, dir, fragment string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "terraform.tfstate"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), fragment) {
		t.Errorf("the state lost the %q representation", fragment)
	}
}
