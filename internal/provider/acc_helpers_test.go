// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/ataila/terraform-provider-ataila/internal/acctest"
	"github.com/ataila/terraform-provider-ataila/internal/provider"
)

// diagRecorder keeps every diagnostic the provider returns to the CLI, so a
// test can prove that a warning reached the operator (the test framework
// itself only sees errors).
type diagRecorder struct {
	mu    sync.Mutex
	diags []*tfprotov6.Diagnostic
}

func (r *diagRecorder) add(diags []*tfprotov6.Diagnostic) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.diags = append(r.diags, diags...)
}

// warnings are the summaries of every warning recorded.
func (r *diagRecorder) warnings() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, d := range r.diags {
		if d.Severity == tfprotov6.DiagnosticSeverityWarning {
			out = append(out, d.Summary+": "+d.Detail)
		}
	}
	return out
}

// expectWarning is a check that a warning whose summary and detail contain
// every given fragment was reported.
func (r *diagRecorder) expectWarning(fragments ...string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		for _, w := range r.warnings() {
			all := true
			for _, f := range fragments {
				all = all && strings.Contains(w, f)
			}
			if all {
				return nil
			}
		}
		return fmt.Errorf("no warning with %q; warnings: %q", fragments, r.warnings())
	}
}

type recordingServer struct {
	tfprotov6.ProviderServer
	rec *diagRecorder
}

func (s recordingServer) ApplyResourceChange(ctx context.Context, req *tfprotov6.ApplyResourceChangeRequest) (*tfprotov6.ApplyResourceChangeResponse, error) {
	resp, err := s.ProviderServer.ApplyResourceChange(ctx, req)
	if resp != nil {
		s.rec.add(resp.Diagnostics)
	}
	return resp, err
}

func (s recordingServer) ReadResource(ctx context.Context, req *tfprotov6.ReadResourceRequest) (*tfprotov6.ReadResourceResponse, error) {
	resp, err := s.ProviderServer.ReadResource(ctx, req)
	if resp != nil {
		s.rec.add(resp.Diagnostics)
	}
	return resp, err
}

func (s recordingServer) PlanResourceChange(ctx context.Context, req *tfprotov6.PlanResourceChangeRequest) (*tfprotov6.PlanResourceChangeResponse, error) {
	resp, err := s.ProviderServer.PlanResourceChange(ctx, req)
	if resp != nil {
		s.rec.add(resp.Diagnostics)
	}
	return resp, err
}

// recordingProvider serves the provider like protoV6 and records its diagnostics.
func recordingProvider() (map[string]func() (tfprotov6.ProviderServer, error), *diagRecorder) {
	rec := &diagRecorder{}
	return map[string]func() (tfprotov6.ProviderServer, error){
		"ataila": func() (tfprotov6.ProviderServer, error) {
			s, err := providerserver.NewProtocol6WithError(provider.New(testVersion)())()
			if err != nil {
				return nil, err
			}
			return recordingServer{ProviderServer: s, rec: rec}, nil
		},
	}, rec
}

// providerBlock configures the provider through the environment (UseEnv)
// plus the destroy switch.
func providerBlock(allowDestroy bool) string {
	return fmt.Sprintf("provider \"ataila\" {\n  allow_destroy = %t\n}\n", allowDestroy)
}

// customerHCL is an ataila_customer named "test" with extra attribute lines.
func customerHCL(short, group string, extra ...string) string {
	return fmt.Sprintf(`
resource "ataila_customer" "test" {
  short_name            = %q
  long_name             = "Example Holdings Ltd"
  gitlab_group          = %q
  primary_contact_email = "ops@example.com"
  primary_contact_name  = "Ops Desk"
%s
}
`, short, group, indent(extra))
}

func indent(lines []string) string {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("  " + l + "\n")
	}
	return b.String()
}

// stateAttr reads an attribute from the state into *dst.
func stateAttr(name, key string, dst *string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[name]
		if !ok {
			return fmt.Errorf("%s is not in the state", name)
		}
		*dst = rs.Primary.Attributes[key]
		if *dst == "" {
			return fmt.Errorf("%s.%s is empty", name, key)
		}
		return nil
	}
}

// check wraps a plain function as a TestCheckFunc.
func check(f func() error) resource.TestCheckFunc {
	return func(*terraform.State) error { return f() }
}

// mockField checks one field of an object in the mock.
func mockField(get func(id string) (map[string]any, bool), id *string, field string, want any) resource.TestCheckFunc {
	return check(func() error {
		obj, ok := get(*id)
		if !ok {
			return fmt.Errorf("the mock has no object %s", *id)
		}
		if fmt.Sprint(obj[field]) != fmt.Sprint(want) {
			return fmt.Errorf("mock %s = %v, want %v", field, obj[field], want)
		}
		return nil
	})
}

// allowDestroyEverywhere turns both destroy switches on for the test's
// clean-up: the token's flag in the mock (the provider's is in the config).
func allowDestroyEverywhere(m *acctest.MockAPI) func() {
	return func() { m.SetTokenAllowDestroy(true) }
}

func newMock(t *testing.T) *acctest.MockAPI {
	t.Helper()
	m := acctest.NewMockAPI(t)
	m.UseEnv(t)
	return m
}
