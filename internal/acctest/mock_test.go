// Copyright (c) 2026 ATAILA Kft.
// SPDX-License-Identifier: MPL-2.0

package acctest

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/ataila/terraform-provider-ataila/internal/client"
)

// The mock's answers must decode with the client generated from the pinned
// contract; otherwise the acceptance tests would prove the wrong thing.
func TestMockSpeaksTheContract(t *testing.T) {
	m := NewMockAPI(t)
	api, err := client.New(client.Config{Endpoint: m.URL(), Token: MockToken, CACertPEM: []byte(m.CACertPEM())})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	meta, err := api.Meta(ctx)
	if err != nil {
		t.Fatalf("meta: %v", err)
	}
	if err := client.CheckAPIVersion(meta.ApiVersion); err != nil {
		t.Errorf("mock API version: %v", err)
	}
	who, err := api.Whoami(ctx)
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	if who.Token == nil || who.Principal.Kind != "service" {
		t.Errorf("whoami = %+v", who)
	}

	_, err = api.Operation(ctx, "nope")
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || !apiErr.IsNotFound() || apiErr.Code() != "operation_not_found" || apiErr.RequestID() == "" {
		t.Errorf("unknown operation: %v", err)
	}
}

func TestMockOrderOfChecks(t *testing.T) {
	m := NewMockAPI(t)
	ctx := context.Background()
	bad, err := client.New(client.Config{Endpoint: m.URL(), Token: "wrong", CACertPEM: []byte(m.CACertPEM())})
	if err != nil {
		t.Fatal(err)
	}
	var apiErr *client.APIError

	if _, err := bad.Meta(ctx); !errors.As(err, &apiErr) || apiErr.StatusCode != 401 {
		t.Errorf("wrong token: %v", err)
	}

	m.RefuseLicence("/meta", "licence_locked", "Install a licence.")
	if _, err := bad.Meta(ctx); !errors.As(err, &apiErr) || !apiErr.IsLicenceRefusal() {
		t.Errorf("the licence gate must answer before authentication: %v", err)
	}

	m.SwitchOff()
	if _, err := bad.Meta(ctx); !errors.As(err, &apiErr) || apiErr.StatusCode != 404 {
		t.Errorf("a switched-off API must answer 404 first: %v", err)
	}
}

// The mock's idempotency follows the platform's: same key and same request
// replay the stored answer, the same key on another request is refused.
func TestMockIdempotency(t *testing.T) {
	m := NewMockAPI(t)
	hc := m.srv.Client()
	post := func(key, body string) (*http.Response, map[string]any) {
		req, _ := http.NewRequest(http.MethodPost, m.URL()+"/api/v1/customers", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+MockToken)
		req.Header.Set("Idempotency-Key", key)
		resp, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out := map[string]any{}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp, out
	}
	body := `{"short_name":"IDEM","long_name":"Idempotent Ltd","gitlab_group":"idem",` +
		`"primary_contact_email":"ops@example.com","primary_contact_name":"Ops Desk"}`

	first, a := post("k-1", body)
	again, b := post("k-1", body)
	if first.StatusCode != 201 || again.StatusCode != 201 || a["id"] != b["id"] {
		t.Fatalf("replay: %d %v / %d %v", first.StatusCode, a["id"], again.StatusCode, b["id"])
	}
	if first.Header.Get("Idempotent-Replayed") != "" || again.Header.Get("Idempotent-Replayed") != "true" {
		t.Errorf("Idempotent-Replayed: %q then %q", first.Header.Get("Idempotent-Replayed"), again.Header.Get("Idempotent-Replayed"))
	}
	reused, p := post("k-1", strings.Replace(body, "IDEM", "OTHER", 1))
	if reused.StatusCode != 409 || p["code"] != "idempotency_key_reused" {
		t.Errorf("reused key: %d %v", reused.StatusCode, p["code"])
	}
	// Without a key the same body is a second create, which the unique keys refuse.
	dup, p := post("k-2", body)
	if dup.StatusCode != 409 || p["code"] != "gitlab_group_taken" {
		t.Errorf("second create: %d %v", dup.StatusCode, p["code"])
	}
}

// Invalid bodies are refused with the FastAPI-shaped 422.
func TestMockValidation(t *testing.T) {
	m := NewMockAPI(t)
	req, _ := http.NewRequest(http.MethodPost, m.URL()+"/api/v1/tenants",
		strings.NewReader(`{"customer_id":"x","name":"a","slug":"Bad","colour":"red"}`))
	req.Header.Set("Authorization", "Bearer "+MockToken)
	resp, err := m.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	errs, _ := out["errors"].([]any)
	if resp.StatusCode != 422 || out["code"] != "validation_failed" || len(errs) != 4 {
		t.Errorf("got %d %v with %d errors", resp.StatusCode, out["code"], len(errs))
	}
}

// An address comes back as the platform stores it: the bare address, the
// local part as sent, the domain lower-cased.
func TestMockNormalisesAddresses(t *testing.T) {
	for in, want := range map[string]string{
		"Pat@Example.COM":             "Pat@example.com",
		"  Pat@Example.COM ":          "Pat@example.com",
		"Pat Doe <Pat@Example.COM>":   "Pat@example.com",
		"ops.desk+tf@Sub.Example.Org": "ops.desk+tf@sub.example.org",
	} {
		if got := normalizeEmail(in); got != want {
			t.Errorf("normalizeEmail(%q) = %q, want %q", in, got, want)
		}
	}
}
