// Copyright (c) 2026 ATAILA Kft.
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/mod/semver"
)

func TestNormalizeEndpoint(t *testing.T) {
	ok := map[string]string{
		"https://portal.example.com":             "https://portal.example.com/api/v1",
		"https://portal.example.com/":            "https://portal.example.com/api/v1",
		"https://portal.example.com/api/v1":      "https://portal.example.com/api/v1",
		"https://portal.example.com/api/v1/":     "https://portal.example.com/api/v1",
		"https://example.com/portal":             "https://example.com/portal/api/v1",
		"https://portal.example.com:8443":        "https://portal.example.com:8443/api/v1",
		"http://127.0.0.1:8000":                  "http://127.0.0.1:8000/api/v1",
		"http://localhost:8000/":                 "http://localhost:8000/api/v1",
		"  https://portal.example.com/api/v1  ":  "https://portal.example.com/api/v1",
		"https://user:pw@portal.example.com/x/y": "https://portal.example.com/x/y/api/v1",
	}
	for in, want := range ok {
		got, err := NormalizeEndpoint(in)
		if err != nil || got != want {
			t.Errorf("NormalizeEndpoint(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{
		"", "portal.example.com", "http://portal.example.com", "ftp://portal.example.com",
		"https://portal.example.com/?x=1", "https://portal.example.com/#top",
	} {
		if got, err := NormalizeEndpoint(bad); err == nil {
			t.Errorf("NormalizeEndpoint(%q) = %q, want an error", bad, got)
		}
	}
}

func TestNewRequiresAToken(t *testing.T) {
	if _, err := New(Config{Endpoint: "https://portal.example.com", Token: "  "}); err == nil {
		t.Fatal("New accepted an empty token")
	}
}

// tlsServer serves /api/v1/meta over TLS with a certificate no system trusts.
func tlsServer(t *testing.T) (*httptest.Server, []byte) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, metaBody)
	}))
	t.Cleanup(srv.Close)
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	return srv, caPEM
}

func TestPrivateCAIsTrustedOnlyWhenGiven(t *testing.T) {
	srv, caPEM := tlsServer(t)
	ctx := context.Background()

	without, err := New(Config{Endpoint: srv.URL, Token: testToken, MaxRetries: -1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := without.Meta(ctx); err == nil {
		t.Fatal("an untrusted certificate was accepted without a CA")
	}

	with, err := New(Config{Endpoint: srv.URL, Token: testToken, CACertPEM: caPEM})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := with.Meta(ctx); err != nil {
		t.Fatalf("with the CA: %v", err)
	}
}

func TestLoadCACertFromPEMOrFile(t *testing.T) {
	srv, caPEM := tlsServer(t)
	file := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(file, caPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"pem": string(caPEM), "file": file} {
		t.Run(name, func(t *testing.T) {
			pemBytes, err := LoadCACert(value)
			if err != nil {
				t.Fatal(err)
			}
			api, err := New(Config{Endpoint: srv.URL, Token: testToken, CACertPEM: pemBytes})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := api.Meta(context.Background()); err != nil {
				t.Fatalf("Meta: %v", err)
			}
		})
	}
	if _, err := LoadCACert(filepath.Join(t.TempDir(), "missing.pem")); err == nil {
		t.Error("a missing file was accepted")
	}
	if b, err := LoadCACert("  "); err != nil || b != nil {
		t.Errorf("blank value: %q, %v", b, err)
	}
}

func TestInvalidCAIsRefused(t *testing.T) {
	_, err := New(Config{Endpoint: "https://portal.example.com", Token: testToken, CACertPEM: []byte("not a certificate")})
	if err == nil || !strings.Contains(err.Error(), "no PEM-encoded certificate") {
		t.Fatalf("want a CA error, got %v", err)
	}
}

func TestTLSConfigFloorIsTLS12(t *testing.T) {
	cfg, err := TLSConfig(nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MinVersion != 0x0303 {
		t.Errorf("MinVersion = %#x", cfg.MinVersion)
	}
}

func TestWhoamiDecodes(t *testing.T) {
	s := &scripted{replies: []reply{{status: 200, body: `{"principal":{"id":"u1","email":"bot@service-account.invalid","name":"bot","kind":"service"},` +
		`"auth_kind":"service_account","scopes":["a-global","b-global"],"expires_at":"2027-01-01T00:00:00Z",` +
		`"token":{"id":"t1","name":"ci","prefix":"abcd1234","granted_scopes":["a-global"],"allow_destroy":false}}`}}}
	api, _ := newTestAPI(t, s)
	w, err := api.Whoami(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if w.Principal.Kind != "service" || w.AuthKind != "service_account" || len(w.Scopes) != 2 ||
		w.Token == nil || w.Token.Prefix != "abcd1234" || w.ExpiresAt == nil {
		t.Errorf("decoded %+v", w)
	}
}

func TestUnexpectedSuccessBodyIsAnError(t *testing.T) {
	s := &scripted{replies: []reply{{status: 200, headers: map[string]string{"Content-Type": "text/html"}, body: "<html></html>"}}}
	api, _ := newTestAPI(t, s)
	if _, err := api.Meta(context.Background()); err == nil || !strings.Contains(err.Error(), "unexpected response") {
		t.Fatalf("want an unexpected-response error, got %v", err)
	}
}

func TestCheckAPIVersion(t *testing.T) {
	for _, v := range []string{"1.0.0", "1.4.2", "v1.0.0", "1.0.1-rc.1"} {
		if err := CheckAPIVersion(v); err != nil {
			t.Errorf("CheckAPIVersion(%q) = %v", v, err)
		}
	}
	for v, want := range map[string]string{
		"2.0.0":  "speaks API v1 only",
		"0.9.0":  "speaks API v1 only",
		"banana": "not a semantic version",
		"":       "not a semantic version",
	} {
		err := CheckAPIVersion(v)
		var ve *VersionError
		if !errors.As(err, &ve) || !strings.Contains(err.Error(), want) {
			t.Errorf("CheckAPIVersion(%q) = %v, want %q", v, err, want)
		}
	}
	if err := checkAPIVersion("1.2.0", "1.3.0"); err == nil || !strings.Contains(err.Error(), "older than 1.3.0") {
		t.Errorf("an API older than the minimum was accepted: %v", err)
	}
	if err := checkAPIVersion("1.3.0", "1.3.0"); err != nil {
		t.Errorf("the minimum itself was refused: %v", err)
	}
}

func TestPlatformServes(t *testing.T) {
	if FeatureMinimum(FeatureK8sQuota) != "1.0.203" {
		t.Fatalf("k8s_quota came with 1.0.203, not %s", FeatureMinimum(FeatureK8sQuota))
	}
	for v, want := range map[string]bool{
		"1.0.187": false, "1.0.202": false, "1.0.203-rc.1": false, "dev": false, "": false,
		"1.0.203": true, "v1.0.206": true, "1.1.0": true,
	} {
		if got := PlatformServes(v, FeatureK8sQuota); got != want {
			t.Errorf("PlatformServes(%q, k8s_quota) = %v, want %v", v, got, want)
		}
	}
	if PlatformServes("9.9.9", "no-such-feature") {
		t.Error("an unknown feature is served")
	}
}

// Tenant quotas, the catalogue and the order reads came with one platform
// release, ReleaseQuotasOrders: served from it on, refused just before it.
func TestPlatformServesQuotasAndOrders(t *testing.T) {
	parts := strings.Split(ReleaseQuotasOrders, ".")
	n, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil || n == 0 || !semver.IsValid("v"+ReleaseQuotasOrders) {
		t.Fatalf("ReleaseQuotasOrders %q is not a release with a patch number above 0", ReleaseQuotasOrders)
	}
	parts[len(parts)-1] = strconv.Itoa(n - 1)
	below := strings.Join(parts, ".")
	for _, f := range []string{FeatureTenantQuota, FeatureCatalogue, FeatureOrders} {
		if FeatureMinimum(f) != ReleaseQuotasOrders {
			t.Errorf("%s came with %s, not %s", f, FeatureMinimum(f), ReleaseQuotasOrders)
		}
		if !PlatformServes(ReleaseQuotasOrders, f) || PlatformServes(below, f) || PlatformServes(MinimumPlatformVersion, f) {
			t.Errorf("%s: served by %s and not by %s (nor %s) expected", f, ReleaseQuotasOrders, below,
				MinimumPlatformVersion)
		}
	}
}

func TestCheckPlatformVersion(t *testing.T) {
	if MinimumPlatformVersion != "1.0.187" {
		t.Fatalf("MinimumPlatformVersion = %s; the vendored contract is that of 1.0.187", MinimumPlatformVersion)
	}
	for _, v := range []string{"1.0.187", "1.0.196", "v1.0.188", "1.1.0", "1.0.188-rc.1", "2.0.0"} {
		if err := CheckPlatformVersion(v); err != nil {
			t.Errorf("CheckPlatformVersion(%q) = %v", v, err)
		}
	}
	for v, want := range map[string]string{
		"1.0.176":      "older than 1.0.187",
		"1.0.155":      "older than 1.0.187",
		"1.0.187-rc.1": "older than 1.0.187",
		"0.9.0":        "older than 1.0.187",
		"dev":          "not a semantic version",
		"":             "not a semantic version",
	} {
		err := CheckPlatformVersion(v)
		var ve *VersionError
		if !errors.As(err, &ve) || !strings.Contains(err.Error(), want) {
			t.Errorf("CheckPlatformVersion(%q) = %v, want %q", v, err, want)
		}
	}
	if err := CheckPlatformVersion("1.0.176"); !strings.Contains(err.Error(), "Idempotency-Key") ||
		!strings.Contains(err.Error(), "Upgrade the platform to 1.0.187") {
		t.Errorf("the refusal does not say why or what to do: %v", err)
	}
}
