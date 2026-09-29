// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

// Package acctest holds what the acceptance tests share: an in-process mock of
// the platform's /api/v1, served over TLS with its own certificate authority,
// and small helpers around it.
//
// The mock follows the real API's order of checks: the on/off switch (404
// when off), the licence gate (403 problem whose code starts with
// "licence_"), authentication (401), then the route. Every error is an RFC
// 9457 problem document with a code and a request_id.
package acctest

import (
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// MockToken is the only bearer token the mock accepts. It is not a secret:
// nothing but the in-process mock ever sees it.
const MockToken = "acc-test-token-not-a-secret"

const apiPrefix = "/api/v1"

// Fault is one injected error answer.
type Fault struct {
	Status     int
	Code       string
	RetryAfter string
}

// Request is what the mock saw.
type Request struct {
	Method string
	Path   string
	Header http.Header
}

// MockAPI is an in-process /api/v1.
type MockAPI struct {
	srv *httptest.Server

	mu          sync.Mutex
	meta        map[string]any
	whoami      map[string]any
	operations  map[string]map[string]any
	switchedOff bool
	refusals    map[string]map[string]any
	faults      map[string][]Fault
	requests    []Request
	seq         int
}

// NewMockAPI starts a mock that stops when the test ends.
func NewMockAPI(t testing.TB) *MockAPI {
	t.Helper()
	m := &MockAPI{
		meta:       DefaultMeta(),
		whoami:     DefaultWhoami(),
		operations: map[string]map[string]any{},
		refusals:   map[string]map[string]any{},
		faults:     map[string][]Fault{},
	}
	m.srv = httptest.NewTLSServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.srv.Close)
	return m
}

// DefaultMeta is the GET /meta answer of a licensed multi-tenant platform.
func DefaultMeta() map[string]any {
	return map[string]any{
		"api_version":      "1.0.0",
		"platform_version": "1.0.0",
		"tier":             "standard",
		"tenancy_mode":     "multi",
		"modules":          []string{"ai-gateway", "sp-mode"},
		"licence": map[string]any{
			"state":          "ACTIVE",
			"state_reason":   "",
			"days_remaining": 120,
		},
	}
}

// DefaultWhoami is the GET /whoami answer for a service-account token.
func DefaultWhoami() map[string]any {
	return map[string]any{
		"principal": map[string]any{
			"id":    "00000000-0000-4000-8000-000000000001",
			"email": "ci-bot@service-account.invalid",
			"name":  "ci-bot",
			"kind":  "service",
		},
		"auth_kind":  "service_account",
		"scopes":     []string{"customers-global", "tenants-global"},
		"expires_at": "2027-01-01T00:00:00Z",
		"token": map[string]any{
			"id":             "00000000-0000-4000-8000-0000000000aa",
			"name":           "ci",
			"prefix":         "mocktokn",
			"granted_scopes": []string{"customers-global", "tenants-global"},
			"allow_destroy":  false,
		},
	}
}

// URL is the portal base URL (without /api/v1).
func (m *MockAPI) URL() string { return m.srv.URL }

// CACertPEM is the certificate that signs the mock's TLS certificate.
func (m *MockAPI) CACertPEM() string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: m.srv.Certificate().Raw}))
}

// UseEnv points the provider at the mock through its environment variables.
func (m *MockAPI) UseEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ATAILA_ENDPOINT", m.URL())
	t.Setenv("ATAILA_TOKEN", MockToken)
	t.Setenv("ATAILA_CA_CERT", m.CACertPEM())
}

// SetMeta changes the GET /meta answer.
func (m *MockAPI) SetMeta(change func(meta map[string]any)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	change(m.meta)
}

// SetWhoami replaces the GET /whoami answer.
func (m *MockAPI) SetWhoami(whoami map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.whoami = whoami
}

// SwitchOff makes every path answer 404, as the real API does when switched off.
func (m *MockAPI) SwitchOff() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.switchedOff = true
}

// RefuseLicence makes the licence gate refuse a path (for example "/whoami").
func (m *MockAPI) RefuseLicence(path, code, remedy string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refusals[path] = map[string]any{
		"code":         code,
		"state":        "LOCKED",
		"state_reason": "expired",
		"remedy":       remedy,
		"remedy_url":   "/licence",
	}
}

// InjectFaults queues error answers for a path; each is used once.
func (m *MockAPI) InjectFaults(path string, faults ...Fault) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.faults[path] = append(m.faults[path], faults...)
}

// Hits counts the requests the mock received for a path (below /api/v1).
func (m *MockAPI) Hits(path string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, r := range m.requests {
		if r.Path == path {
			n++
		}
	}
	return n
}

// Requests returns a copy of every request received.
func (m *MockAPI) Requests() []Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Request(nil), m.requests...)
}

func (m *MockAPI) serve(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.seq++
	requestID := fmt.Sprintf("mock-%04d", m.seq)
	w.Header().Set("X-Request-ID", requestID)

	path := strings.TrimPrefix(r.URL.Path, apiPrefix)
	m.requests = append(m.requests, Request{Method: r.Method, Path: path, Header: r.Header.Clone()})

	problem := func(status int, code, detail string, extra map[string]any, headers map[string]string) {
		body := map[string]any{}
		for k, v := range extra {
			body[k] = v
		}
		body["type"] = "urn:ataila:api:problem:" + code
		body["title"] = http.StatusText(status)
		body["status"] = status
		body["detail"] = detail
		body["code"] = code
		body["instance"] = r.URL.Path
		body["request_id"] = requestID
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		writeJSON(w, status, "application/problem+json", body)
	}

	if !strings.HasPrefix(r.URL.Path, apiPrefix+"/") {
		problem(http.StatusNotFound, "not_found", "", nil, nil)
		return
	}
	if m.switchedOff {
		problem(http.StatusNotFound, "not_found", "", nil, nil)
		return
	}
	if refusal, ok := m.refusals[path]; ok {
		problem(http.StatusForbidden, refusal["code"].(string), refusal["remedy"].(string), refusal, nil)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+MockToken {
		problem(http.StatusUnauthorized, "not_authenticated", "A valid API token is required.", nil, nil)
		return
	}
	if queue := m.faults[path]; len(queue) > 0 {
		f := queue[0]
		m.faults[path] = queue[1:]
		var headers map[string]string
		if f.RetryAfter != "" {
			headers = map[string]string{"Retry-After": f.RetryAfter}
		}
		code := f.Code
		if code == "" {
			code = "injected"
		}
		problem(f.Status, code, "Injected by the test.", nil, headers)
		return
	}
	if r.Method != http.MethodGet {
		problem(http.StatusMethodNotAllowed, "method_not_allowed", "", nil, nil)
		return
	}

	switch {
	case path == "/meta":
		writeJSON(w, http.StatusOK, "application/json", m.meta)
	case path == "/whoami":
		writeJSON(w, http.StatusOK, "application/json", m.whoami)
	case strings.HasPrefix(path, "/operations/"):
		if op, ok := m.operations[strings.TrimPrefix(path, "/operations/")]; ok {
			writeJSON(w, http.StatusOK, "application/json", op)
			return
		}
		problem(http.StatusNotFound, "operation_not_found", "No such operation.", nil, nil)
	default:
		problem(http.StatusNotFound, "not_found", "", nil, nil)
	}
}

func writeJSON(w http.ResponseWriter, status int, contentType string, body any) {
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
