// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

// Package acctest holds what the acceptance tests share: an in-process mock of
// the platform's /api/v1, served over TLS with its own certificate authority,
// and small helpers around it.
//
// The mock follows the real API's order of checks: the on/off switch (404
// when off), the licence gate (403 problem whose code starts with
// "licence_"), authentication (401), idempotent replay of a POST, then the
// route, whose own order is permission, destroy flag, body validation, the
// object's existence, then the operation's rules. Every error is an RFC 9457
// problem document with a code and a request_id.
//
// The tenancy endpoints (customers, tenants, memberships) are in tenancy.go.
// Where the mock's behaviour had to be assumed rather than read from the
// platform's code, the comment says so.
package acctest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
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
	// Method limits the fault to one HTTP method; empty matches any.
	Method string
	// AfterHandling runs the request first (a create is committed and its
	// idempotent answer stored) and then answers with the fault: a response
	// lost on the way back, as a proxy's 502 after the API did the work.
	AfterHandling bool
	// StillRunning models a slow POST: the request's Idempotency-Key is
	// claimed, the fault is answered (a proxy's 504, say) while the API keeps
	// working, and the next StillRunning requests with that key are answered
	// 429 idempotency_request_in_progress with Retry-After, as the platform
	// does. The request after those finds the work done and gets it replayed.
	StillRunning int
}

// InProgressRetryAfter is the Retry-After of a 429
// idempotency_request_in_progress, as the platform sends it.
const InProgressRetryAfter = "2"

// Request is what the mock saw.
type Request struct {
	Method string
	Path   string
	Query  string
	Header http.Header
	Body   []byte
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

	idempotent map[string]storedReply
	tenancy    *tenancyState
	users      *usersState
	gateway    *gatewayState
	projects   *projectsState
	licence    *licenceState
	brand      *brandState
	releases   *releasesState
	ai         *aiState
}

type storedReply struct {
	hash   string
	status int
	body   []byte
	// running counts the 429s still to answer while a claimed request is
	// "still running"; pending marks such a claim.
	running int
	pending bool
}

// reply is a handler's answer before it is written, so that idempotency and
// injected faults can wrap every route the same way.
type reply struct {
	status  int
	body    any // nil for no body
	problem bool
	headers map[string]string
	// replay, when set, is what an idempotent replay answers instead of body
	// (a key's value is never stored for a replay).
	replay any
}

// call is one request as the handlers see it.
type call struct {
	r         *http.Request
	path      string // below /api/v1, without the query
	body      []byte
	requestID string
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
		idempotent: map[string]storedReply{},
		tenancy:    newTenancyState(),
		gateway:    newGatewayState(),
		projects:   newProjectsState(),
		licence:    newLicenceState(),
		brand:      newBrandState(),
		releases:   newReleasesState(),
		ai:         newAIState(),
	}
	principal, _ := m.whoami["principal"].(map[string]any)
	m.users = newUsersState(principal, m.whoami["scopes"].([]string))
	m.srv = httptest.NewTLSServer(http.HandlerFunc(m.serve))
	t.Cleanup(m.srv.Close)
	return m
}

// DefaultMeta is the GET /meta answer of a licensed multi-tenant platform.
func DefaultMeta() map[string]any {
	return map[string]any{
		"api_version":             "1.0.0",
		"platform_version":        "1.0.0",
		"tier":                    "standard",
		"tenancy_mode":            "multi",
		"modules":                 []string{"ai-gateway", "sp-mode"},
		"dispatch_mode_effective": "live",
		"simulate_stage_seconds":  nil,
		"licence": map[string]any{
			"state":          "ACTIVE",
			"state_reason":   "",
			"days_remaining": 120,
		},
	}
}

// DefaultWhoami is the GET /whoami answer for a service-account token that
// may administer tenancy and was minted without allow_destroy.
func DefaultWhoami() map[string]any {
	return map[string]any{
		"principal": map[string]any{
			"id":    "00000000-0000-4000-8000-000000000001",
			"email": "ci-bot@service-account.invalid",
			"name":  "ci-bot",
			"kind":  "service",
		},
		"auth_kind":  "service_account",
		"scopes":     defaultScopes(),
		"expires_at": "2027-01-01T00:00:00Z",
		"token": map[string]any{
			"id":             "00000000-0000-4000-8000-0000000000aa",
			"name":           "ci",
			"prefix":         "mocktokn",
			"granted_scopes": defaultScopes(),
			"allow_destroy":  false,
		},
	}
}

func defaultScopes() []string {
	return []string{"ai-center-admin-global", "ai-center-read-global", "ai-gateway-admin-global",
		"ai-gateway-read-global", "ai-models-admin-global", "ai-models-read-global", "brand-center-admin-global",
		"brand-center-read-global", "licence-admin-global", "licence-read-global", "projects-admin-global",
		"projects-read-global", "release-manager-admin-global", "release-manager-read-global",
		"tenancy-admin-global", "tenancy-read-global", "users-admin-global", "users-read-global"}
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

// SetWhoami replaces the GET /whoami answer. Its "scopes" are the token's
// effective permissions and its token's "allow_destroy" is the destroy flag
// the mock enforces.
func (m *MockAPI) SetWhoami(whoami map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.whoami = whoami
}

// SetTokenAllowDestroy sets the token's own allow_destroy flag.
func (m *MockAPI) SetTokenAllowDestroy(allow bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if tok, ok := m.whoami["token"].(map[string]any); ok {
		tok["allow_destroy"] = allow
	}
}

// SetScopes sets the token's effective scopes.
func (m *MockAPI) SetScopes(scopes ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.whoami["scopes"] = append([]string{}, scopes...)
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

// InjectFaults queues error answers for a path; each is used once, by the
// first request whose method it matches.
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

// Calls counts the requests with this method whose path starts with prefix.
func (m *MockAPI) Calls(method, prefix string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, r := range m.requests {
		if r.Method == method && strings.HasPrefix(r.Path, prefix) {
			n++
		}
	}
	return n
}

// Mutations counts the requests that could change something (anything but GET).
func (m *MockAPI) Mutations() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, r := range m.requests {
		if r.Method != http.MethodGet {
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
	c := &call{r: r, path: strings.TrimPrefix(r.URL.Path, apiPrefix), requestID: fmt.Sprintf("mock-%04d", m.seq)}
	c.body, _ = io.ReadAll(r.Body)
	m.requests = append(m.requests, Request{
		Method: r.Method, Path: c.path, Query: r.URL.RawQuery, Header: r.Header.Clone(), Body: c.body,
	})
	w.Header().Set("X-Request-ID", c.requestID)

	if !strings.HasPrefix(r.URL.Path, apiPrefix+"/") || m.switchedOff {
		write(w, c.problem(http.StatusNotFound, "not_found", "", nil))
		return
	}
	if refusal, ok := m.refusals[c.path]; ok {
		write(w, c.problem(http.StatusForbidden, refusal["code"].(string), refusal["remedy"].(string), refusal))
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+MockToken {
		write(w, c.problem(http.StatusUnauthorized, "not_authenticated", "A valid API token is required.", nil))
		return
	}

	fault, hasFault := m.takeFault(c.path, r.Method)
	if key := r.Header.Get("Idempotency-Key"); hasFault && fault.StillRunning > 0 && r.Method == http.MethodPost && key != "" {
		m.idempotent[key] = storedReply{hash: requestHash(c), running: fault.StillRunning, pending: true}
		write(w, c.faultReply(fault))
		return
	}
	if hasFault && !fault.AfterHandling {
		write(w, c.faultReply(fault))
		return
	}

	var rep reply
	if key := r.Header.Get("Idempotency-Key"); r.Method == http.MethodPost && key != "" {
		rep = m.idempotently(c, key)
	} else {
		rep = m.route(c)
	}
	if hasFault {
		rep = c.faultReply(fault)
	}
	write(w, rep)
}

func (m *MockAPI) takeFault(path, method string) (Fault, bool) {
	queue := m.faults[path]
	for i, f := range queue {
		if f.Method == "" || f.Method == method {
			m.faults[path] = append(queue[:i:i], queue[i+1:]...)
			return f, true
		}
	}
	return Fault{}, false
}

// idempotently runs a POST under its Idempotency-Key as the real API does:
// the same key and the same request replay the stored answer (marked
// Idempotent-Replayed); the same key with a different request is 409
// idempotency_key_reused. Answers of 500 and above are not stored.
func (m *MockAPI) idempotently(c *call, key string) reply {
	hash := requestHash(c)
	stored, ok := m.idempotent[key]
	if ok && stored.hash != hash {
		return c.problem(http.StatusConflict, "idempotency_key_reused",
			"This Idempotency-Key was used for a different request.", nil)
	}
	if ok && stored.pending {
		if stored.running > 0 {
			stored.running--
			m.idempotent[key] = stored
			rep := c.problem(http.StatusTooManyRequests, "idempotency_request_in_progress",
				"A request with this Idempotency-Key is still running; retry after the indicated delay.", nil)
			rep.headers = map[string]string{"Retry-After": InProgressRetryAfter}
			return rep
		}
		// The claimed request has finished: its answer is stored, and replayed.
		rep := m.route(c)
		if rep.replay != nil {
			rep.body, rep.replay = rep.replay, nil
		}
		b, _ := json.Marshal(rep.body)
		m.idempotent[key] = storedReply{hash: hash, status: rep.status, body: b}
		rep.headers = map[string]string{"Idempotent-Replayed": "true"}
		return rep
	}
	if ok {
		return reply{status: stored.status, body: json.RawMessage(stored.body),
			problem: stored.status >= 400, headers: map[string]string{"Idempotent-Replayed": "true"}}
	}
	rep := m.route(c)
	if rep.status < 500 {
		stored := rep.body
		if rep.replay != nil {
			stored = rep.replay
		}
		b, _ := json.Marshal(stored)
		m.idempotent[key] = storedReply{hash: hash, status: rep.status, body: b}
	}
	return rep
}

// requestHash identifies a request for idempotency: method, path, query, body.
func requestHash(c *call) string {
	sum := sha256.New()
	for _, part := range [][]byte{[]byte(c.r.Method), []byte(c.path), []byte(c.r.URL.RawQuery), c.body} {
		fmt.Fprintf(sum, "%d:", len(part))
		sum.Write(part)
	}
	return hex.EncodeToString(sum.Sum(nil))
}

func (m *MockAPI) route(c *call) reply {
	switch {
	case c.path == "/meta":
		if c.r.Method != http.MethodGet {
			return c.methodNotAllowed()
		}
		return ok(http.StatusOK, m.meta)
	case c.path == "/whoami":
		if c.r.Method != http.MethodGet {
			return c.methodNotAllowed()
		}
		return ok(http.StatusOK, m.whoami)
	case strings.HasPrefix(c.path, "/operations/"):
		if c.r.Method != http.MethodGet {
			return c.methodNotAllowed()
		}
		if native, isProvision := strings.CutPrefix(c.path, "/operations/provision:"); isProvision {
			return m.provisionOperation(c, native)
		}
		if native, isRelease := strings.CutPrefix(c.path, "/operations/release:"); isRelease {
			return m.releaseOperation(c, native)
		}
		if native, isRun := strings.CutPrefix(c.path, "/operations/model-store-run:"); isRun {
			return m.storeRunOperation(c, native)
		}
		if op, found := m.operations[strings.TrimPrefix(c.path, "/operations/")]; found {
			return ok(http.StatusOK, op)
		}
		return c.problem(http.StatusNotFound, "operation_not_found", "No such operation.", nil)
	case c.path == "/customers" || strings.HasPrefix(c.path, "/customers/"),
		c.path == "/tenants" || strings.HasPrefix(c.path, "/tenants/"):
		return m.routeTenancy(c)
	case c.path == "/users" || strings.HasPrefix(c.path, "/users/"), c.path == "/permissions":
		return m.routeUsers(c)
	case c.path == "/ai/gateway" || strings.HasPrefix(c.path, "/ai/gateway/"):
		return m.routeGateway(c)
	case c.path == "/projects" || strings.HasPrefix(c.path, "/projects/"):
		return m.routeProjects(c)
	case c.path == "/licence" || strings.HasPrefix(c.path, "/licence/"):
		return m.routeLicence(c)
	case c.path == "/brand" || strings.HasPrefix(c.path, "/brand/"):
		return m.routeBrand(c)
	case strings.HasPrefix(c.path, "/release-operations/"):
		if c.r.Method != http.MethodGet {
			return c.methodNotAllowed()
		}
		return m.releaseOperationGet(c, strings.TrimPrefix(c.path, "/release-operations/"))
	case c.path == "/ai-models" || strings.HasPrefix(c.path, "/ai-models/"):
		return m.routeAIModels(c)
	case c.path == "/ai/nodes" || strings.HasPrefix(c.path, "/ai/nodes/"), c.path == "/ai/clusters", c.path == "/ai/catalog":
		return m.routeAICenter(c)
	}
	return c.problem(http.StatusNotFound, "not_found", "", nil)
}

func ok(status int, body any) reply { return reply{status: status, body: body} }

func (c *call) problem(status int, code, detail string, extra map[string]any) reply {
	body := map[string]any{}
	for k, v := range extra {
		body[k] = v
	}
	body["type"] = "urn:ataila:api:problem:" + code
	body["title"] = http.StatusText(status)
	body["status"] = status
	body["detail"] = detail
	body["code"] = code
	body["instance"] = c.r.URL.Path
	body["request_id"] = c.requestID
	return reply{status: status, body: body, problem: true}
}

func (c *call) methodNotAllowed() reply {
	return c.problem(http.StatusMethodNotAllowed, "method_not_allowed", "", nil)
}

func (c *call) faultReply(f Fault) reply {
	code := f.Code
	if code == "" {
		code = "injected"
	}
	rep := c.problem(f.Status, code, "Injected by the test.", nil)
	if f.RetryAfter != "" {
		rep.headers = map[string]string{"Retry-After": f.RetryAfter}
	}
	return rep
}

func write(w http.ResponseWriter, rep reply) {
	for k, v := range rep.headers {
		w.Header().Set(k, v)
	}
	if rep.body == nil {
		w.WriteHeader(rep.status)
		return
	}
	ct := "application/json"
	if rep.problem {
		ct = "application/problem+json"
	}
	var buf bytes.Buffer
	_ = json.NewEncoder(&buf).Encode(rep.body)
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(rep.status)
	_, _ = w.Write(buf.Bytes())
}
