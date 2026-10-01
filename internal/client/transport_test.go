// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const testToken = "unit-test-token"

// scripted answers each request with the next reply and records what it saw.
type scripted struct {
	mu      sync.Mutex
	replies []reply
	seen    []*http.Request
	bodies  []string
}

type reply struct {
	status  int
	headers map[string]string
	body    string
}

func (s *scripted) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, _ := io.ReadAll(r.Body)
	s.seen = append(s.seen, r.Clone(context.Background()))
	s.bodies = append(s.bodies, string(b))
	rep := reply{status: 200, body: `{}`}
	if len(s.replies) > 0 {
		rep = s.replies[0]
		s.replies = s.replies[1:]
	}
	ct := "application/json"
	if rep.status >= 400 {
		ct = ProblemContentType
	}
	w.Header().Set("Content-Type", ct)
	for k, v := range rep.headers {
		w.Header().Set(k, v)
	}
	w.WriteHeader(rep.status)
	_, _ = io.WriteString(w, rep.body)
}

func (s *scripted) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.seen)
}

func problem(status int, code string) string {
	return fmt.Sprintf(`{"type":"urn:ataila:api:problem:%s","title":%q,"status":%d,"detail":"detail for %s","code":%q,"request_id":"req-%d"}`,
		code, http.StatusText(status), status, code, code, status)
}

const metaBody = `{"api_version":"1.0.0","platform_version":"1.0.0","tier":"standard","tenancy_mode":"multi","modules":["sp-mode"],"licence":{"state":"ACTIVE","state_reason":"","days_remaining":120}}`

// newTestAPI returns an API against a scripted server with sleeps recorded
// instead of slept.
func newTestAPI(t *testing.T, s *scripted) (*API, *[]time.Duration) {
	t.Helper()
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	api, err := New(Config{Endpoint: srv.URL, Token: testToken, UserAgent: UserAgent("9.9.9"), HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	var waits []time.Duration
	api.transport.sleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	return api, &waits
}

func TestRetryableStatuses(t *testing.T) {
	for status := 400; status < 600; status++ {
		want := status == 429 || status == 502 || status == 503 || status == 504
		if got := Retryable(status); got != want {
			t.Errorf("Retryable(%d) = %v, want %v", status, got, want)
		}
	}
}

func TestRetriesOnRetryableStatusThenSucceeds(t *testing.T) {
	for _, status := range []int{429, 502, 503, 504} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			s := &scripted{replies: []reply{
				{status: status, body: problem(status, "transient")},
				{status: status, body: problem(status, "transient")},
				{status: 200, body: metaBody},
			}}
			api, waits := newTestAPI(t, s)
			meta, err := api.Meta(context.Background())
			if err != nil {
				t.Fatalf("Meta: %v", err)
			}
			if meta.ApiVersion != "1.0.0" {
				t.Errorf("api_version = %q", meta.ApiVersion)
			}
			if s.count() != 3 {
				t.Errorf("requests = %d, want 3", s.count())
			}
			if len(*waits) != 2 {
				t.Errorf("waits = %v, want 2", *waits)
			}
		})
	}
}

func TestNeverRetriesOtherErrors(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 405, 409, 410, 412, 422, 500, 501} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			s := &scripted{replies: []reply{{status: status, body: problem(status, "final")}}}
			api, waits := newTestAPI(t, s)
			_, err := api.Meta(context.Background())
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error %v is not an *APIError", err)
			}
			if apiErr.StatusCode != status || apiErr.Attempts != 1 {
				t.Errorf("status %d attempts %d", apiErr.StatusCode, apiErr.Attempts)
			}
			if s.count() != 1 || len(*waits) != 0 {
				t.Errorf("requests = %d, waits = %v; want exactly one request", s.count(), *waits)
			}
		})
	}
}

func TestRetryAfterIsHonoured(t *testing.T) {
	s := &scripted{replies: []reply{
		{status: 429, headers: map[string]string{"Retry-After": "7"}, body: problem(429, "rate_limited")},
		{status: 503, headers: map[string]string{"Retry-After": "2"}, body: problem(503, "unavailable")},
		{status: 200, body: metaBody},
	}}
	api, waits := newTestAPI(t, s)
	if _, err := api.Meta(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{7 * time.Second, 2 * time.Second}
	if fmt.Sprint(*waits) != fmt.Sprint(want) {
		t.Errorf("waits = %v, want %v", *waits, want)
	}
}

func TestRetryAfterBeyondLimitStopsRetrying(t *testing.T) {
	s := &scripted{replies: []reply{
		{status: 429, headers: map[string]string{"Retry-After": "3600"}, body: problem(429, "rate_limited")},
		{status: 200, body: metaBody},
	}}
	api, _ := newTestAPI(t, s)
	_, err := api.Meta(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 429 {
		t.Fatalf("want the 429 back, got %v", err)
	}
	if s.count() != 1 {
		t.Errorf("requests = %d, want 1", s.count())
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	cases := map[string]struct {
		d  time.Duration
		ok bool
	}{
		"":                              {0, false},
		"0":                             {0, true},
		"12":                            {12 * time.Second, true},
		"-1":                            {0, false},
		"soon":                          {0, false},
		"Fri, 02 Jan 2026 03:04:35 GMT": {30 * time.Second, true},
		"Fri, 02 Jan 2026 03:00:00 GMT": {0, true},
	}
	for in, want := range cases {
		d, ok := parseRetryAfter(in, now)
		if d != want.d || ok != want.ok {
			t.Errorf("parseRetryAfter(%q) = %v, %v; want %v, %v", in, d, ok, want.d, want.ok)
		}
	}
}

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	tr := &transport{backoffBase: time.Second, backoffMax: 4 * time.Second}
	prevFloor := time.Duration(0)
	for attempt := 1; attempt <= 6; attempt++ {
		d, ok := tr.delay(attempt, "")
		if !ok {
			t.Fatalf("attempt %d: not ok", attempt)
		}
		floor := time.Second << (attempt - 1)
		if floor > 4*time.Second {
			floor = 4 * time.Second
		}
		if d < floor || d > floor+floor/5 {
			t.Errorf("attempt %d: delay %v outside [%v, %v]", attempt, d, floor, floor+floor/5)
		}
		if floor < prevFloor {
			t.Errorf("backoff shrank")
		}
		prevFloor = floor
	}
}

func TestGivesUpAfterMaxRetries(t *testing.T) {
	var replies []reply
	for i := 0; i < 10; i++ {
		replies = append(replies, reply{status: 503, body: problem(503, "unavailable")})
	}
	s := &scripted{replies: replies}
	api, _ := newTestAPI(t, s)
	_, err := api.Meta(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *APIError, got %v", err)
	}
	if apiErr.Attempts != DefaultMaxRetries+1 || s.count() != DefaultMaxRetries+1 {
		t.Errorf("attempts %d, requests %d; want %d", apiErr.Attempts, s.count(), DefaultMaxRetries+1)
	}
	if !strings.Contains(apiErr.Detail(), fmt.Sprintf("attempts: %d", DefaultMaxRetries+1)) {
		t.Errorf("detail does not report the attempts:\n%s", apiErr.Detail())
	}
}

func TestCancelledContextStopsRetrying(t *testing.T) {
	s := &scripted{replies: []reply{
		{status: 503, body: problem(503, "unavailable")},
		{status: 200, body: metaBody},
	}}
	api, _ := newTestAPI(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	api.transport.sleep = func(ctx context.Context, d time.Duration) error {
		cancel()
		return sleepContext(ctx, time.Hour)
	}
	_, err := api.Meta(ctx)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 503 {
		t.Fatalf("want the 503 back, got %v", err)
	}
	if s.count() != 1 {
		t.Errorf("requests = %d, want 1", s.count())
	}
}

func TestStandardHeaders(t *testing.T) {
	s := &scripted{replies: []reply{{status: 200, body: metaBody}}}
	api, _ := newTestAPI(t, s)
	if _, err := api.Meta(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := s.seen[0]
	if got := r.Header.Get("Authorization"); got != "Bearer "+testToken {
		t.Errorf("Authorization = %q", got)
	}
	if got := r.Header.Get("User-Agent"); got != "terraform-provider-ataila/9.9.9" {
		t.Errorf("User-Agent = %q", got)
	}
	if got := r.URL.Path; got != "/api/v1/meta" {
		t.Errorf("path = %q", got)
	}
	if got := r.Header.Get(IdempotencyHeader); got != "" {
		t.Errorf("a GET carried an Idempotency-Key: %q", got)
	}
}

// create sends POST /customers through the generated client, which carries
// the contract's Idempotency-Key parameter.
func create(t *testing.T, api *API, shortName string) {
	t.Helper()
	if _, err := api.CreateCustomer(context.Background(), CustomerCreate{ShortName: shortName, LongName: shortName,
		GitlabGroup: "example", PrimaryContactEmail: "it@example.com", PrimaryContactName: "IT"}); err != nil {
		t.Fatalf("POST: %v", err)
	}
}

func TestIdempotencyKeyFreshPerCreateAndStableAcrossRetries(t *testing.T) {
	s := &scripted{replies: []reply{
		{status: 503, body: problem(503, "unavailable")},
		{status: 201, body: `{}`},
		{status: 201, body: `{}`},
	}}
	api, _ := newTestAPI(t, s)
	create(t, api, "FIRST")
	create(t, api, "SECOND")

	if s.count() != 3 {
		t.Fatalf("requests = %d, want 3", s.count())
	}
	k1 := s.seen[0].Header.Get(IdempotencyHeader)
	k1retry := s.seen[1].Header.Get(IdempotencyHeader)
	k2 := s.seen[2].Header.Get(IdempotencyHeader)
	if k1 == "" || k2 == "" {
		t.Fatalf("missing Idempotency-Key: %q %q", k1, k2)
	}
	if k1 != k1retry {
		t.Errorf("a retry changed the key: %q then %q", k1, k1retry)
	}
	if k1 == k2 {
		t.Errorf("two creates shared the key %q", k1)
	}
	if s.bodies[0] != s.bodies[1] || !strings.Contains(s.bodies[1], `"short_name":"FIRST"`) {
		t.Errorf("the retried body differs: %q vs %q", s.bodies[0], s.bodies[1])
	}
}

// TestIdempotencyKeyOnEveryDeclaringOperation checks, against the vendored
// contract, that the client sends an Idempotency-Key on every operation that
// declares the header parameter, and on no other.
func TestIdempotencyKeyOnEveryDeclaringOperation(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "api", "openapi-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]struct {
			Parameters []struct {
				Name string `json:"name"`
				In   string `json:"in"`
			} `json:"parameters"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	declared := map[string]bool{} // "METHOD /path/{param}"
	for p, ops := range spec.Paths {
		for method, op := range ops {
			for _, prm := range op.Parameters {
				if prm.In == "header" && prm.Name == IdempotencyHeader {
					declared[strings.ToUpper(method)+" "+p] = true
				}
			}
		}
	}
	if len(declared) == 0 {
		t.Fatal("the contract declares no Idempotency-Key parameter")
	}

	s := &scripted{}
	api, _ := newTestAPI(t, s)
	ctx := context.Background()
	// Every client call of an operation that declares the key, plus calls
	// that must not carry one. Answers are empty: only the requests count.
	calls := map[string]func(){
		"POST /customers": func() {
			_, _ = api.CreateCustomer(ctx, CustomerCreate{PrimaryContactEmail: "it@example.com"})
		},
		"POST /tenants":         func() { _, _ = api.CreateTenant(ctx, TenantCreate{}) },
		"POST /users":           func() { _, _ = api.CreateUser(ctx, UserCreate{Email: "dana@example.com"}) },
		"POST /projects":        func() { _, _ = api.CreateProject(ctx, map[string]any{}) },
		"POST /ai-models":       func() { _, _ = api.CreateAIModel(ctx, map[string]any{}) },
		"POST /ai/gateway/keys": func() { _, _ = api.CreateGatewayKey(ctx, map[string]any{}) },
		"POST /ai/gateway/keys/{key_id}/rotations":        func() { _, _ = api.RotateGatewayKey(ctx, "k1", false) },
		"POST /brand/assets":                              func() { _, _, _ = api.UploadBrandAsset(ctx, "logo", []byte("x"), "") },
		"POST /projects/{project_id}/provisioning":        func() { _, _, _ = api.StartProvisioning(ctx, "1") },
		"POST /projects/{project_id}/release-promotions":  func() { _, _ = api.RequestPromotion(ctx, "1", ReleasePromotionCreate{}) },
		"PUT /ai-models/{model_id}/node-caches/{node}":    func() { _, _, _ = api.CacheModel(ctx, "1", "n") },
		"DELETE /ai-models/{model_id}/node-caches/{node}": func() { _, _ = api.UncacheModel(ctx, "1", "n") },
		"GET /customers/{customer_id} (no key)":           func() { _, _ = api.GetCustomer(ctx, "1") },
		"DELETE /customers/{customer_id} (no key)":        func() { _ = api.ArchiveCustomer(ctx, "1") },
		"PATCH /ai-models/{model_id} (no key)":            func() { _, _ = api.UpdateAIModel(ctx, "1", Patch{}) },
		"PUT /projects/{project_id}/prod-lock (no key)":   func() { _, _ = api.PutProdLock(ctx, "1", true, "") },
		"PUT /users/{user_id}/roles/{role} (no key)":      func() { _, _, _ = api.GrantRole(ctx, "1", "r") },
		"DELETE /ai/gateway/keys/{key_id} (no key)":       func() { _ = api.DeleteGatewayKey(ctx, "k1") },
		"PUT /tenants/{tenant_id}/memberships/{user_id} (no key)": func() {
			_, _, _ = api.PutMembership(ctx, "1", "1", "member")
		},
	}
	for name, call := range calls {
		before := s.count()
		call()
		if s.count() == before {
			t.Errorf("%s: no request was sent", name)
			continue
		}
		key := s.seen[s.count()-1].Header.Get(IdempotencyHeader)
		op, noKey := strings.CutSuffix(name, " (no key)")
		switch {
		case noKey && key != "":
			t.Errorf("%s carried an Idempotency-Key", op)
		case !noKey && key == "":
			t.Errorf("%s carried no Idempotency-Key", op)
		}
		delete(declared, name)
	}
	for op := range declared {
		t.Errorf("the contract declares an Idempotency-Key on %s, which this test does not call", op)
	}
}

func TestNewIdempotencyKeyIsUUIDv4(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		k := NewIdempotencyKey()
		if len(k) != 36 || k[14] != '4' || !strings.ContainsRune("89ab", rune(k[19])) {
			t.Fatalf("not a v4 UUID: %q", k)
		}
		if seen[k] {
			t.Fatalf("duplicate key %q", k)
		}
		seen[k] = true
	}
}
