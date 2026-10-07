// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	fwdatasource "github.com/hashicorp/terraform-plugin-framework/datasource"
	fwprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	fwresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/ataila/terraform-provider-ataila/internal/acctest"
	"github.com/ataila/terraform-provider-ataila/internal/provider"
)

const testVersion = "0.0.0-acc"

// protoV6 serves the provider in-process; the CLI under test (terraform or
// tofu, chosen by TF_ACC_TERRAFORM_PATH) attaches to it.
var protoV6 = map[string]func() (tfprotov6.ProviderServer, error){
	"ataila": providerserver.NewProtocol6WithError(provider.New(testVersion)()),
}

// words turns "a b c" into a regexp that survives the CLI wrapping lines. A
// standalone ".*" matches anything in between.
func words(s string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString(`(?s)`)
	prev := ""
	for i, p := range strings.Fields(s) {
		switch {
		case p == ".*":
			b.WriteString(`.*`)
		case i > 0 && prev != ".*":
			b.WriteString(`\s+` + regexp.QuoteMeta(p))
		default:
			b.WriteString(regexp.QuoteMeta(p))
		}
		prev = p
	}
	return regexp.MustCompile(b.String())
}

const metaConfig = `
provider "ataila" {}

data "ataila_meta" "this" {}
`

func TestProviderSchemaIsValid(t *testing.T) {
	p := provider.New(testVersion)()
	var resp fwprovider.SchemaResponse
	p.Schema(context.Background(), fwprovider.SchemaRequest{}, &resp)
	if resp.Diagnostics.HasError() {
		t.Fatalf("schema: %v", resp.Diagnostics)
	}
	if diags := resp.Schema.ValidateImplementation(context.Background()); diags.HasError() {
		t.Fatalf("schema implementation: %v", diags)
	}
	if !resp.Schema.Attributes["token"].IsSensitive() {
		t.Error("token must be sensitive")
	}
}

// Everything in the provider block instead of the environment, the CA as PEM.
func TestAccProvider_ExplicitConfigWithCAPEM(t *testing.T) {
	m := acctest.NewMockAPI(t)
	cfg := fmt.Sprintf(`
provider "ataila" {
  endpoint        = %q
  token           = %q
  ca_cert_pem     = <<-EOT
%s
EOT
  allow_destroy   = false
  request_timeout = "15s"
}

data "ataila_meta" "this" {}
`, m.URL()+"/api/v1/", acctest.MockToken, strings.TrimSpace(m.CACertPEM()))

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config: cfg,
			Check:  resource.TestCheckResourceAttr("data.ataila_meta.this", "api_version", "1.0.0"),
		}},
	})
}

// The CA from a file, the endpoint and token from the environment.
func TestAccProvider_CACertFile(t *testing.T) {
	m := acctest.NewMockAPI(t)
	m.UseEnv(t)
	t.Setenv("ATAILA_CA_CERT", "")
	caFile := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(caFile, []byte(m.CACertPEM()), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf(`
provider "ataila" {
  ca_cert_file = %q
}

data "ataila_meta" "this" {}
`, filepath.ToSlash(caFile))

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config: cfg,
			Check:  resource.TestCheckResourceAttr("data.ataila_meta.this", "licence.state", "ACTIVE"),
		}},
	})
}

func TestAccProvider_UntrustedCertificateIsRefused(t *testing.T) {
	m := acctest.NewMockAPI(t)
	m.UseEnv(t)
	t.Setenv("ATAILA_CA_CERT", "")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config:      metaConfig,
			ExpectError: words("Cannot reach the ATAILA API"),
		}},
	})
}

func TestAccProvider_CAArgumentsConflict(t *testing.T) {
	m := acctest.NewMockAPI(t)
	m.UseEnv(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config: `
provider "ataila" {
  ca_cert_file = "ca.pem"
  ca_cert_pem  = "-----BEGIN CERTIFICATE-----"
}

data "ataila_meta" "this" {}
`,
			ExpectError: words("Invalid Attribute Combination"),
		}},
	})
}

func TestAccProvider_MissingEndpoint(t *testing.T) {
	t.Setenv("ATAILA_ENDPOINT", "")
	t.Setenv("ATAILA_TOKEN", acctest.MockToken)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config:      metaConfig,
			ExpectError: words("Missing ATAILA API endpoint"),
		}},
	})
}

func TestAccProvider_PlainHTTPIsRefused(t *testing.T) {
	t.Setenv("ATAILA_ENDPOINT", "http://portal.example.com")
	t.Setenv("ATAILA_TOKEN", acctest.MockToken)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config:      metaConfig,
			ExpectError: words("must use https"),
		}},
	})
}

func TestAccProvider_RejectedToken(t *testing.T) {
	m := acctest.NewMockAPI(t)
	m.UseEnv(t)
	t.Setenv("ATAILA_TOKEN", "some-other-token")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config:      metaConfig,
			ExpectError: words("The ATAILA API rejected the token"),
		}},
	})
}

func TestAccProvider_SwitchedOffAPI(t *testing.T) {
	m := acctest.NewMockAPI(t)
	m.UseEnv(t)
	m.SwitchOff()
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config:      metaConfig,
			ExpectError: words("public API is switched off"),
		}},
	})
}

func TestAccProvider_RefusesAnotherAPIMajor(t *testing.T) {
	m := acctest.NewMockAPI(t)
	m.UseEnv(t)
	m.SetMeta(func(meta map[string]any) { meta["api_version"] = "2.0.0" })
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config:      metaConfig,
			ExpectError: words("Unsupported ATAILA API version"),
		}},
	})
}

// The API version alone cannot tell an older platform apart (API 1.0.0 since
// platform 1.0.155): a platform before 1.0.187 is refused at Configure, before
// any read or write; 1.0.187 and later are accepted.
func TestAccProvider_PlatformRelease(t *testing.T) {
	for _, c := range []struct {
		release string
		refused bool
	}{
		{"1.0.176", true}, // renamed, but no declared Idempotency-Key or Location
		{"1.0.155", true}, // API 1.0.0 with the pre-rename names
		{"1.0.186", true},
		{"banana", true},
		{"1.0.187", false},
		{"1.0.196", false},
		{"1.1.0", false},
	} {
		t.Run(c.release, func(t *testing.T) {
			m := acctest.NewMockAPI(t)
			m.UseEnv(t)
			m.SetMeta(func(meta map[string]any) { meta["platform_version"] = c.release })
			step := resource.TestStep{Config: metaConfig}
			if c.refused {
				step.ExpectError = words("Unsupported ATAILA platform release")
			} else {
				step.Check = resource.TestCheckResourceAttr("data.ataila_meta.this", "platform_version", c.release)
			}
			resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: protoV6, Steps: []resource.TestStep{step}})
			if c.refused && m.Hits("/whoami") != 0 {
				t.Error("the provider went on after refusing the platform")
			}
		})
	}
}

func TestAccProvider_UnavailableAfterRetries(t *testing.T) {
	m := acctest.NewMockAPI(t)
	m.UseEnv(t)
	var faults []acctest.Fault
	for i := 0; i < 10; i++ {
		faults = append(faults, acctest.Fault{Status: 503, Code: "unavailable", RetryAfter: "0"})
	}
	m.InjectFaults("/meta", faults...)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config:      metaConfig,
			ExpectError: words("The ATAILA API is unavailable"),
		}},
	})
	if got := m.Hits("/meta"); got != 5 {
		t.Errorf("GET /meta was sent %d times, want 5 (one try and four retries)", got)
	}
}

// Every request identifies the provider and carries the token.
func TestAccProvider_RequestHeaders(t *testing.T) {
	m := acctest.NewMockAPI(t)
	m.UseEnv(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config: metaConfig,
			Check: func(*terraform.State) error {
				reqs := m.Requests()
				if len(reqs) == 0 {
					return fmt.Errorf("the mock saw no request")
				}
				for _, r := range reqs {
					if ua := r.Header.Get("User-Agent"); ua != "terraform-provider-ataila/"+testVersion {
						return fmt.Errorf("%s %s: User-Agent %q", r.Method, r.Path, ua)
					}
					if r.Header.Get("Authorization") != "Bearer "+acctest.MockToken {
						return fmt.Errorf("%s %s: no bearer token", r.Method, r.Path)
					}
				}
				return nil
			},
		}},
	})
}

// D38: nothing that exists in only one CLI, or at different versions in each.
func TestProviderUsesNothingOnlyOneCLIHas(t *testing.T) {
	ctx := context.Background()
	p := provider.New(testVersion)()
	if _, ok := p.(fwprovider.ProviderWithFunctions); ok {
		t.Error("provider functions are not allowed")
	}
	if _, ok := p.(fwprovider.ProviderWithEphemeralResources); ok {
		t.Error("ephemeral resources are not allowed")
	}
	if _, ok := p.(fwprovider.ProviderWithActions); ok {
		t.Error("actions are not allowed")
	}
	if _, ok := p.(fwprovider.ProviderWithListResources); ok {
		t.Error("list resources are not allowed")
	}
	for _, newResource := range p.Resources(ctx) {
		r := newResource()
		var meta fwresource.MetadataResponse
		r.Metadata(ctx, fwresource.MetadataRequest{ProviderTypeName: "ataila"}, &meta)
		var resp fwresource.SchemaResponse
		r.Schema(ctx, fwresource.SchemaRequest{}, &resp)
		if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
			t.Errorf("%s: %v", meta.TypeName, diags)
		}
		for name, a := range resp.Schema.Attributes {
			if wo, ok := a.(interface{ IsWriteOnly() bool }); ok && wo.IsWriteOnly() {
				t.Errorf("%s.%s is write-only", meta.TypeName, name)
			}
		}
	}
	for _, newDataSource := range p.DataSources(ctx) {
		d := newDataSource()
		var meta fwdatasource.MetadataResponse
		d.Metadata(ctx, fwdatasource.MetadataRequest{ProviderTypeName: "ataila"}, &meta)
		var resp fwdatasource.SchemaResponse
		d.Schema(ctx, fwdatasource.SchemaRequest{}, &resp)
		if diags := resp.Schema.ValidateImplementation(ctx); diags.HasError() {
			t.Errorf("%s: %v", meta.TypeName, diags)
		}
	}
}
