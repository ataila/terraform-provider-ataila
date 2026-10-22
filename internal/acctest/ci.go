// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package acctest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// GitLab CI workload identity (platform 1.0.395): POST /auth/ci-token takes a
// GitLab CI job's ID token and answers a one-hour `ataila_cit_` token of the
// service account an operator linked to the job's project. The mock follows
// the platform's order (routers/auth.py): the ID token (RS256 signature by a
// trusted issuer, `aud` = this platform, not expired), then the link, the
// protected ref, and the scopes. An exchanged token authenticates like the
// mock's own token (same principal, same permissions), but GET /whoami says
// `ci`, and the mock can expire it to prove the provider renews it.

// FakeGitLab is a GitLab instance's ID token signer: an issuer URL and an RSA
// key that the mock trusts once TrustGitLab is called.
type FakeGitLab struct {
	Issuer string
	key    *rsa.PrivateKey
	kid    string
}

// NewFakeGitLab makes an issuer with its own 2048-bit RSA key.
func NewFakeGitLab(t testing.TB, issuer string) *FakeGitLab {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating the fake GitLab's key: %v", err)
	}
	return &FakeGitLab{Issuer: strings.TrimRight(issuer, "/"), key: key, kid: "fake-gitlab-1"}
}

// IDTokenClaims are what GitLab puts in a job's ID token (the subset the
// platform reads), as strings like GitLab sends them.
type IDTokenClaims struct {
	Audience     string
	ProjectPath  string
	Ref          string
	RefProtected bool
	PipelineID   string
	JobID        string
	// ExpiresIn is the token's lifetime from now; zero means five minutes, and
	// a negative value makes an expired token.
	ExpiresIn time.Duration
}

// IDToken signs an RS256 ID token for the claims.
func (g *FakeGitLab) IDToken(c IDTokenClaims) string {
	now := time.Now()
	life := c.ExpiresIn
	if life == 0 {
		life = 5 * time.Minute
	}
	ref := c.Ref
	if ref == "" {
		ref = "main"
	}
	claims := map[string]any{
		"iss": g.Issuer, "aud": c.Audience, "iat": now.Unix(), "nbf": now.Add(-5 * time.Second).Unix(),
		"exp":          now.Add(life).Unix(),
		"sub":          "project_path:" + c.ProjectPath + ":ref_type:branch:ref:" + ref,
		"project_path": c.ProjectPath, "ref": ref, "ref_type": "branch",
		"ref_protected": fmt.Sprintf("%t", c.RefProtected),
		"pipeline_id":   orDefault(c.PipelineID, "1001"), "job_id": orDefault(c.JobID, "5005"),
	}
	return g.sign(map[string]any{"alg": "RS256", "typ": "JWT", "kid": g.kid}, claims)
}

func (g *FakeGitLab) sign(header, claims map[string]any) string {
	input := b64JSON(header) + "." + b64JSON(claims)
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, g.key, crypto.SHA256, sum[:])
	if err != nil {
		panic(fmt.Sprintf("signing the fake ID token: %v", err))
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func b64JSON(v any) string {
	b, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(b)
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// CILink is an operator's link of a GitLab project to the mock's service
// account, as the portal makes it.
type CILink struct {
	Issuer      string
	ProjectPath string
	Scopes      []string
	// AllowDestroy is the exchanged tokens' destroy flag.
	AllowDestroy bool
	// AnyRef lets pipelines of unprotected refs exchange (the portal's
	// "protected branches and tags only", unticked).
	AnyRef bool
}

type ciState struct {
	issuers map[string]*rsa.PublicKey
	links   map[string]CILink // issuer + "\x00" + lower(project path)
	tokens  map[string]*ciToken
	issued  int
}

type ciToken struct {
	id, prefix string
	link       CILink
	expired    bool
}

func newCIState() *ciState {
	return &ciState{issuers: map[string]*rsa.PublicKey{}, links: map[string]CILink{}, tokens: map[string]*ciToken{}}
}

func linkKey(issuer, project string) string {
	return strings.TrimRight(issuer, "/") + "\x00" + strings.ToLower(project)
}

// TrustGitLab makes the mock accept ID tokens the fake GitLab signs.
func (m *MockAPI) TrustGitLab(g *FakeGitLab) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ci.issuers[g.Issuer] = &g.key.PublicKey
}

// LinkCIProject links a GitLab project to the mock's service account.
func (m *MockAPI) LinkCIProject(link CILink) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ci.links[linkKey(link.Issuer, link.ProjectPath)] = link
}

// CIExchanges counts the tokens the mock has issued.
func (m *MockAPI) CIExchanges() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ci.issued
}

// ExpireCITokens makes every token issued so far answer 401 token_expired,
// as an hour-old one does.
func (m *MockAPI) ExpireCITokens() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.ci.tokens {
		t.expired = true
	}
}

// exchangeAudience is the `aud` the mock expects: its own portal address, as
// the platform's default (portal_public_url).
func (m *MockAPI) exchangeAudience() string { return strings.TrimRight(m.srv.URL, "/") }

// ciExchange is POST /auth/ci-token.
func (m *MockAPI) ciExchange(c *call) reply {
	if c.r.Method != http.MethodPost {
		return c.methodNotAllowed()
	}
	var body struct {
		IDToken string `json:"id_token"`
	}
	if err := json.Unmarshal(c.body, &body); err != nil || body.IDToken == "" {
		return c.problem(http.StatusUnprocessableEntity, "validation_failed", "id_token is required.", nil)
	}
	unauthorized := func(code, detail string) reply {
		rep := c.problem(http.StatusUnauthorized, code, detail, nil)
		rep.headers = map[string]string{"WWW-Authenticate": "Bearer"}
		return rep
	}
	claims, code, detail := m.verifyIDToken(body.IDToken)
	if code != "" {
		return unauthorized(code, detail)
	}
	iss, _ := claims["iss"].(string)
	project, _ := claims["project_path"].(string)
	if project == "" {
		return unauthorized("ci_token_invalid", "The ID token names no project_path.")
	}
	link, linked := m.ci.links[linkKey(iss, project)]
	if !linked {
		return c.problem(http.StatusForbidden, "ci_project_not_linked",
			fmt.Sprintf("No service account is linked to the GitLab project %q.", project), nil)
	}
	if protected, _ := claims["ref_protected"].(string); !link.AnyRef && protected != "true" {
		return c.problem(http.StatusForbidden, "ci_ref_not_protected",
			"This project's link accepts pipelines of protected branches and tags only.", nil)
	}
	scopes := intersect(link.Scopes, m.whoami["scopes"].([]string))
	if len(scopes) == 0 {
		return c.problem(http.StatusForbidden, "ci_link_scopes_not_held",
			"The service account no longer holds any of the link's scopes.", nil)
	}

	m.ci.issued++
	prefix := fmt.Sprintf("ci%06d", m.ci.issued)
	token := fmt.Sprintf("ataila_cit_%s_mock-exchanged-not-a-secret", prefix)
	m.ci.tokens[token] = &ciToken{id: fmt.Sprintf("00000000-0000-4000-8000-%012d", m.ci.issued),
		prefix: prefix, link: link}
	principal, _ := m.whoami["principal"].(map[string]any)
	return ok(http.StatusCreated, map[string]any{
		"token":                token,
		"token_id":             m.ci.tokens[token].id,
		"expires_at":           time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		"service_account_id":   principal["id"],
		"service_account_name": principal["name"],
		"project_path":         project,
		"scopes":               scopes,
		"allow_destroy":        link.AllowDestroy,
	})
}

// verifyIDToken checks an ID token as the platform does
// (services/ci_identity.verify_exchange_token): the issuer is trusted before
// the signature is checked, then RS256, `aud`, expiry. It returns the claims,
// or the refusal's code and detail.
func (m *MockAPI) verifyIDToken(token string) (map[string]any, string, string) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, "ci_token_invalid", "Not a JWT."
	}
	var header, claims map[string]any
	if !decodePart(parts[0], &header) || !decodePart(parts[1], &claims) {
		return nil, "ci_token_invalid", "Not a JWT."
	}
	iss, _ := claims["iss"].(string)
	key, trusted := m.ci.issuers[strings.TrimRight(iss, "/")]
	if !trusted {
		return nil, "ci_issuer_not_allowed", fmt.Sprintf("The issuer %q is not trusted here.", iss)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if alg, _ := header["alg"].(string); alg != "RS256" || err != nil ||
		rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig) != nil {
		return nil, "ci_token_invalid", "The ID token's signature does not verify."
	}
	if aud, _ := claims["aud"].(string); strings.TrimRight(aud, "/") != m.exchangeAudience() {
		return nil, "ci_audience_mismatch", fmt.Sprintf("The ID token's aud is not %q.", m.exchangeAudience())
	}
	if exp, _ := claims["exp"].(float64); int64(exp) <= time.Now().Unix() {
		return nil, "ci_token_expired", "The ID token has expired."
	}
	return claims, "", ""
}

func decodePart(s string, into *map[string]any) bool {
	b, err := base64.RawURLEncoding.DecodeString(s)
	return err == nil && json.Unmarshal(b, into) == nil
}

func intersect(want, held []string) []string {
	have := map[string]bool{}
	for _, s := range held {
		have[s] = true
	}
	var out []string
	for _, s := range want {
		if have[s] {
			out = append(out, s)
		}
	}
	return out
}

// authenticate checks the bearer: the mock's own token, or a live CI token it
// issued (noted on the call, for GET /whoami).
func (m *MockAPI) authenticate(c *call) (reply, bool) {
	bearer := strings.TrimPrefix(c.r.Header.Get("Authorization"), "Bearer ")
	if bearer == MockToken {
		return reply{}, true
	}
	if t, found := m.ci.tokens[bearer]; found {
		if t.expired {
			rep := c.problem(http.StatusUnauthorized, "token_expired", "The API token has expired.", nil)
			rep.headers = map[string]string{"WWW-Authenticate": "Bearer"}
			return rep, false
		}
		c.ci = t
		return reply{}, true
	}
	return c.problem(http.StatusUnauthorized, "not_authenticated", "A valid API token is required.", nil), false
}

// ciWhoami is GET /whoami for a CI token: the mock's principal, `ci`, and the
// link's scopes the principal holds.
func (m *MockAPI) ciWhoami(t *ciToken) map[string]any {
	scopes := intersect(t.link.Scopes, m.whoami["scopes"].([]string))
	return map[string]any{
		"principal":  m.whoami["principal"],
		"auth_kind":  "ci",
		"scopes":     scopes,
		"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		"token": map[string]any{
			"id": t.id, "name": "ci " + t.link.ProjectPath, "prefix": t.prefix,
			"granted_scopes": t.link.Scopes, "allow_destroy": t.link.AllowDestroy,
		},
		"scope":     "global",
		"tenant_id": nil,
	}
}
