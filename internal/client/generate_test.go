// Copyright (c) 2026 ATAILA Kft.
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestGeneratedClientIsCurrent regenerates the client from api/openapi-v1.json
// exactly as `go generate` does and fails when the result differs from the
// committed client.gen.go: the contract and the code must move together.
func TestGeneratedClientIsCurrent(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("the go command is needed to regenerate the client: %v", err)
	}
	out := filepath.Join(t.TempDir(), "client.gen.go")
	cmd := exec.Command(goBin, "tool", "oapi-codegen",
		"-config", "oapi-codegen.yaml", "-o", out, "../../api/openapi-v1.json")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("oapi-codegen failed: %v\n%s", err, b)
	}

	fresh, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := os.ReadFile("client.gen.go")
	if err != nil {
		t.Fatal(err)
	}
	norm := func(b []byte) []byte { return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")) }
	if !bytes.Equal(norm(fresh), norm(committed)) {
		t.Fatal("internal/client/client.gen.go is out of step with api/openapi-v1.json: " +
			"run `go generate ./internal/client` and commit the result")
	}
}

// TestDescriptionsAreCurrent does the same for descriptions.gen.go.
func TestDescriptionsAreCurrent(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("the go command is needed to regenerate the descriptions: %v", err)
	}
	out := filepath.Join(t.TempDir(), "descriptions.gen.go")
	cmd := exec.Command(goBin, "run", "../../scripts/gendesc", "-in", "../../api/openapi-v1.json", "-out", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("gendesc failed: %v\n%s", err, b)
	}
	fresh, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	committed, err := os.ReadFile("descriptions.gen.go")
	if err != nil {
		t.Fatal(err)
	}
	norm := func(b []byte) []byte { return bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n")) }
	if !bytes.Equal(norm(fresh), norm(committed)) {
		t.Fatal("internal/client/descriptions.gen.go is out of step with api/openapi-v1.json: " +
			"run `go generate ./internal/client` and commit the result")
	}
}

func TestDescribe(t *testing.T) {
	for _, c := range []struct {
		schema string
		path   []string
		want   bool
	}{
		{"AiNode", []string{"hostname"}, true},
		{"AiNode", []string{"cluster", "role"}, true},          // through a nullable reference
		{"Storage", []string{"nodes", "cached", "repo"}, true}, // through two lists
		{"ProjectOutputs", []string{"secret_paths", "path"}, true},
		{"AiNode", []string{"no_such_member"}, false},
		{"AiNode", []string{"hostname", "deeper"}, false}, // a string has no members
		{"NoSuchSchema", []string{"id"}, false},
		{"AiNode", nil, false},
	} {
		if got := Describe(c.schema, c.path...); (got != "") != c.want {
			t.Errorf("Describe(%s, %v) = %q", c.schema, c.path, got)
		}
	}
}
