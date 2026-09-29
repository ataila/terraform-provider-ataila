// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/go-hclog"
	"github.com/hashicorp/go-plugin"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6/tf6server"

	"github.com/ataila/terraform-provider-ataila/internal/provider"
)

// Environment variables naming the two CLIs for TestCrossCLIState.
const (
	envCrossTerraform = "ATAILA_CROSS_CLI_TERRAFORM"
	envCrossTofu      = "ATAILA_CROSS_CLI_TOFU"
)

// TestCrossCLIState proves that state written by one CLI is read by the
// other without a diff (PLAN §7, step 6): Terraform applies, OpenTofu plans
// with -detailed-exitcode and must find nothing to do; then OpenTofu applies
// a change and Terraform must find nothing to do. Both CLIs run against the
// same working directory and the same state file, with configuration that
// says only `source = "ataila/ataila"`, as a user's does.
//
// It needs both CLIs, so it is not an acceptance test of the per-CLI matrix:
// set ATAILA_CROSS_CLI_TERRAFORM and ATAILA_CROSS_CLI_TOFU to their paths.
func TestCrossCLIState(t *testing.T) {
	terraformBin, tofuBin := os.Getenv(envCrossTerraform), os.Getenv(envCrossTofu)
	if terraformBin == "" || tofuBin == "" {
		t.Skipf("set %s and %s to run the cross-CLI state check", envCrossTerraform, envCrossTofu)
	}
	m := newMock(t)
	m.SetTokenAllowDestroy(true)
	user := m.AddUser("dana@example.com")

	dir := t.TempDir()
	cliConfig := filepath.Join(dir, "empty.rc")
	if err := os.WriteFile(cliConfig, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	writeConfig := func(description string) {
		t.Helper()
		cfg := fmt.Sprintf(`
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
  notes                 = "Written by one CLI, read by the other."
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
`, description, user)
		if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(cfg), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// run executes one CLI command against a fresh in-process provider server.
	run := func(bin string, args ...string) (string, int) {
		t.Helper()
		name := filepath.Base(bin)
		// Each CLI gets the provider only under its own registry's address,
		// as an installed provider would be; a state that names the other
		// registry's address must still work.
		host := "registry.terraform.io"
		if bin == tofuBin {
			host = "registry.opentofu.org"
		}
		reattach, stop := serveProvider(t, host)
		defer stop()
		cmd := exec.Command(bin, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"TF_REATTACH_PROVIDERS="+reattach,
			"TF_IN_AUTOMATION=1",
			"CHECKPOINT_DISABLE=1",
			"TF_CLI_CONFIG_FILE="+cliConfig,
			"TOFU_CLI_CONFIG_FILE="+cliConfig,
			// Each CLI keeps its own working data; the state file is shared.
			"TF_DATA_DIR="+filepath.Join(dir, ".data-"+strings.TrimSuffix(name, ".exe")),
		)
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		code := 0
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else if err != nil {
			t.Fatalf("%s %s: %v", name, strings.Join(args, " "), err)
		}
		t.Logf("%s %s: exit %d", name, strings.Join(args, " "), code)
		return out.String(), code
	}
	must := func(bin string, args ...string) {
		t.Helper()
		if out, code := run(bin, args...); code != 0 {
			t.Fatalf("%s %s failed (exit %d):\n%s", filepath.Base(bin), strings.Join(args, " "), code, out)
		}
	}
	// noDiff runs plan -detailed-exitcode: 0 is "no changes", 2 is a diff.
	noDiff := func(bin, wroteBy string) {
		t.Helper()
		out, code := run(bin, "plan", "-input=false", "-no-color", "-detailed-exitcode")
		if code != 0 {
			t.Fatalf("%s found a difference in the state %s wrote (exit %d):\n%s",
				filepath.Base(bin), wroteBy, code, out)
		}
	}

	writeConfig("First written by Terraform.")
	must(terraformBin, "init", "-input=false", "-no-color")
	must(terraformBin, "apply", "-input=false", "-no-color", "-auto-approve")

	must(tofuBin, "init", "-input=false", "-no-color")
	noDiff(tofuBin, "Terraform")

	writeConfig("Then changed by OpenTofu.")
	must(tofuBin, "apply", "-input=false", "-no-color", "-auto-approve")
	// As on a fresh machine: init against the state the other CLI wrote.
	must(terraformBin, "init", "-input=false", "-no-color")
	noDiff(terraformBin, "OpenTofu")

	tenants, _ := m.Tenant(tenantIDFromState(t, dir))
	if tenants == nil || tenants["description"] != "Then changed by OpenTofu." {
		t.Errorf("the change made by OpenTofu did not reach the platform: %v", tenants)
	}

	must(terraformBin, "destroy", "-input=false", "-no-color", "-auto-approve")
	if c, _ := m.Customer("1"); c["status"] != "archived" {
		t.Errorf("customer after destroy: %v", c)
	}
}

// tenantIDFromState reads ataila_tenant.t's id from the shared state file.
func tenantIDFromState(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "terraform.tfstate"))
	if err != nil {
		t.Fatal(err)
	}
	var st struct {
		Resources []struct {
			Type      string `json:"type"`
			Provider  string `json:"provider"`
			Instances []struct {
				Attributes map[string]any `json:"attributes"`
			} `json:"instances"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	for _, r := range st.Resources {
		if r.Type == "ataila_tenant" && len(r.Instances) == 1 {
			t.Logf("state records the provider as %s", r.Provider)
			return fmt.Sprint(r.Instances[0].Attributes["id"])
		}
	}
	t.Fatal("no ataila_tenant in the state")
	return ""
}

// serveProvider starts the provider in-process in debug mode and returns the
// TF_REATTACH_PROVIDERS value for the provider under the given registry host,
// and a stop function.
func serveProvider(t *testing.T, host string) (string, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cfgCh := make(chan *plugin.ReattachConfig, 1)
	closeCh := make(chan struct{})
	factory := func() tfprotov6.ProviderServer {
		s, err := providerserver.NewProtocol6WithError(provider.New(testVersion)())()
		if err != nil {
			panic(err)
		}
		return s
	}
	go func() {
		_ = tf6server.Serve(host+"/ataila/ataila", factory,
			tf6server.WithDebug(ctx, cfgCh, closeCh),
			tf6server.WithGoPluginLogger(hclog.NewNullLogger()),
			tf6server.WithLoggingSink(t),
			tf6server.WithoutLogStderrOverride())
	}()
	var cfg *plugin.ReattachConfig
	select {
	case cfg = <-cfgCh:
	case <-time.After(10 * time.Second):
		cancel()
		t.Fatal("the provider server did not start")
	}
	entry := map[string]any{
		"Protocol":        string(cfg.Protocol),
		"ProtocolVersion": cfg.ProtocolVersion,
		"Pid":             cfg.Pid,
		"Test":            true,
		"Addr":            map[string]string{"Network": cfg.Addr.Network(), "String": cfg.Addr.String()},
	}
	b, _ := json.Marshal(map[string]any{
		"registry.terraform.io/ataila/ataila": entry,
		"registry.opentofu.org/ataila/ataila": entry,
	})
	return string(b), func() {
		cancel()
		select {
		case <-closeCh:
		case <-time.After(10 * time.Second):
		}
	}
}
