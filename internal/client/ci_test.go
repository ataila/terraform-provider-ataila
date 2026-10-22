// Copyright (c) 2026 Macskásy Attila (ATAILA)
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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testIDToken = "header.claims.signature-of-a-unit-test-id-token"

func ciTokenBody(n int) string {
	return fmt.Sprintf(`{"token":"cit-%d","token_id":"t-%d","expires_at":"2026-10-22T19:00:00Z",`+
		`"service_account_id":"sa-1","service_account_name":"ci-terraform","project_path":"infra/estate",`+
		`"scopes":["users-read-global"],"allow_destroy":false}`, n, n)
}

func TestNewNeedsExactlyOneCredential(t *testing.T) {
	if _, err := New(Config{Endpoint: "https://portal.example.com"}); err == nil ||
		!strings.Contains(err.Error(), "GitLab CI ID token is required") {
		t.Errorf("no credential: %v", err)
	}
	if _, err := New(Config{Endpoint: "https://portal.example.com", Token: testToken, IDToken: testIDToken}); err == nil ||
		!strings.Contains(err.Error(), "not both") {
		t.Errorf("both credentials: %v", err)
	}
	api, err := New(Config{Endpoint: "https://portal.example.com", IDToken: testIDToken})
	if err != nil || !api.UsesCIToken() {
		t.Fatalf("an ID token alone: %v", err)
	}
	if plain, _ := New(Config{Endpoint: "https://portal.example.com", Token: testToken}); plain.UsesCIToken() {
		t.Error("a plain token is not a CI token")
	}
}

// The exchange carries the ID token in its body and no Authorization header;
// every later request carries the exchanged token.
func TestExchangeCITokenSendsNoCredentialAndUsesTheAnswer(t *testing.T) {
	s := &scripted{replies: []reply{{status: 201, body: ciTokenBody(1)}, {status: 200, body: metaBody}}}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	api, err := New(Config{Endpoint: srv.URL, IDToken: testIDToken, HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}
	ci, err := api.ExchangeCIToken(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ci.Token != "cit-1" || ci.ProjectPath != "infra/estate" || ci.ServiceAccountName != "ci-terraform" {
		t.Errorf("answer: %+v", ci)
	}
	if _, err := api.Meta(context.Background()); err != nil {
		t.Fatal(err)
	}
	exchange, meta := s.seen[0], s.seen[1]
	if exchange.Method != http.MethodPost || exchange.URL.Path != "/api/v1/auth/ci-token" {
		t.Errorf("exchange: %s %s", exchange.Method, exchange.URL.Path)
	}
	if h, ok := exchange.Header["Authorization"]; ok {
		t.Errorf("the exchange sent Authorization %q", h)
	}
	if exchange.Header.Get(IdempotencyHeader) != "" {
		t.Error("the exchange sent an Idempotency-Key; its answer is a secret and must not be stored")
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(s.bodies[0]), &body); err != nil || body["id_token"] != testIDToken || len(body) != 1 {
		t.Errorf("exchange body %q", s.bodies[0])
	}
	if got := meta.Header.Get("Authorization"); got != "Bearer cit-1" {
		t.Errorf("after the exchange: Authorization %q", got)
	}
}

func TestExchangeCITokenRetriesARateLimit(t *testing.T) {
	s := &scripted{replies: []reply{
		{status: 429, headers: map[string]string{"Retry-After": "1"}, body: problem(429, "rate_limited")},
		{status: 201, body: ciTokenBody(1)},
	}}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	api, _ := New(Config{Endpoint: srv.URL, IDToken: testIDToken, HTTPClient: srv.Client()})
	api.transport.sleep = func(context.Context, time.Duration) error { return nil }
	if _, err := api.ExchangeCIToken(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.count() != 2 || s.bodies[1] != s.bodies[0] {
		t.Errorf("want the same exchange sent twice, saw %d: %q", s.count(), s.bodies)
	}
}

func TestExchangeCITokenRefusalIsAnAPIError(t *testing.T) {
	s := &scripted{replies: []reply{{status: 403, body: problem(403, "ci_project_not_linked")}}}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	api, _ := New(Config{Endpoint: srv.URL, IDToken: testIDToken, HTTPClient: srv.Client()})
	_, err := api.ExchangeCIToken(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code() != "ci_project_not_linked" {
		t.Fatalf("got %v", err)
	}
}

// ciPlatform answers like the platform: an exchange issues the next token,
// and a request with any but the newest token is 401 token_expired.
type ciPlatform struct {
	mu        sync.Mutex
	exchanges int
	requests  atomic.Int32
}

func (p *ciPlatform) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/api/v1/auth/ci-token" {
		_, _ = io.ReadAll(r.Body)
		p.exchanges++
		w.WriteHeader(201)
		_, _ = io.WriteString(w, ciTokenBody(p.exchanges))
		return
	}
	p.requests.Add(1)
	if r.Header.Get("Authorization") != fmt.Sprintf("Bearer cit-%d", p.exchanges) {
		w.Header().Set("Content-Type", ProblemContentType)
		w.WriteHeader(401)
		_, _ = io.WriteString(w, problem(401, "token_expired"))
		return
	}
	w.WriteHeader(200)
	_, _ = io.WriteString(w, metaBody)
}

// An apply that outlasts the hour: the requests that meet the expiry renew the
// token ONCE between them and go on.
func TestAnExpiredCITokenIsRenewedOnceForConcurrentRequests(t *testing.T) {
	p := &ciPlatform{}
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	api, _ := New(Config{Endpoint: srv.URL, IDToken: testIDToken, HTTPClient: srv.Client()})
	ctx := context.Background()
	if _, err := api.ExchangeCIToken(ctx); err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.exchanges++ // the platform moves on: cit-1 has expired, cit-2 is next
	p.mu.Unlock()

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := api.Meta(ctx); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if p.exchanges != 3 {
		t.Errorf("want one renewal between the 8 requests (3 tokens issued), got %d issued", p.exchanges)
	}
	if got := api.transport.currentToken(); got != "cit-3" {
		t.Errorf("token after renewal %q", got)
	}
}

// A renewal is tried once per request: a token that expires again at once is
// an error, not a loop.
func TestARenewedTokenThatStillFailsIsAnError(t *testing.T) {
	s := &scripted{replies: []reply{
		{status: 401, body: problem(401, "token_expired")},
		{status: 201, body: ciTokenBody(2)},
		{status: 401, body: problem(401, "token_expired")},
	}}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	api, _ := New(Config{Endpoint: srv.URL, IDToken: testIDToken, HTTPClient: srv.Client()})
	api.transport.setToken("cit-1")
	_, err := api.Meta(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code() != "token_expired" {
		t.Fatalf("got %v", err)
	}
	if s.count() != 3 {
		t.Errorf("want request, exchange, request: %d requests", s.count())
	}
}

// A plain API token is never "renewed": its expiry is the operator's to fix.
func TestAnExpiredPlainTokenIsNotRenewed(t *testing.T) {
	s := &scripted{replies: []reply{{status: 401, body: problem(401, "token_expired")}}}
	api, _ := newTestAPI(t, s)
	if _, err := api.Meta(context.Background()); err == nil {
		t.Fatal("an expired token was accepted")
	}
	if s.count() != 1 {
		t.Errorf("want one request and no exchange, saw %d", s.count())
	}
}
