// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/ataila/terraform-provider-ataila/internal/acctest"
	"github.com/ataila/terraform-provider-ataila/internal/client"
)

// GitLab CI workload identity (platform 1.0.395 on): a pipeline gives the
// provider its GitLab ID token (ci_jwt / ATAILA_CI_JWT) instead of an API
// token, and the provider exchanges it at POST /auth/ci-token. The ID tokens
// here are real RS256 JWTs signed by a fake GitLab the mock trusts.

const (
	ciIssuer  = "https://gitlab.example.test"
	ciProject = "infra/terraform-estate"
)

// ciMock trusts a fake GitLab and links ciProject to its service account with
// one scope; the environment carries no API token.
func ciMock(t *testing.T, link acctest.CILink) (*acctest.MockAPI, *acctest.FakeGitLab) {
	t.Helper()
	m := acctest.NewMockAPI(t)
	m.UseEnv(t)
	t.Setenv("ATAILA_TOKEN", "")
	t.Setenv("ATAILA_CI_JWT", "")
	g := acctest.NewFakeGitLab(t, ciIssuer)
	m.TrustGitLab(g)
	if link.ProjectPath == "" {
		link.ProjectPath = ciProject
	}
	if link.Issuer == "" {
		link.Issuer = ciIssuer
	}
	if link.Scopes == nil {
		link.Scopes = []string{"users-read-global"}
	}
	m.LinkCIProject(link)
	return m, g
}

// jobIDToken is the ID token a protected pipeline of ciProject gets, with its
// `aud` set to the platform's address.
func jobIDToken(m *acctest.MockAPI, g *acctest.FakeGitLab, change func(*acctest.IDTokenClaims)) string {
	c := acctest.IDTokenClaims{Audience: m.URL(), ProjectPath: ciProject, RefProtected: true}
	if change != nil {
		change(&c)
	}
	return g.IDToken(c)
}

// everyRequestUsedAnExchangedToken: the exchange sent no Authorization header
// and every other request a `cit` token.
func everyRequestUsedAnExchangedToken(m *acctest.MockAPI) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if m.CIExchanges() == 0 {
			return fmt.Errorf("the provider never exchanged the ID token")
		}
		for _, r := range m.Requests() {
			auth := r.Header.Get("Authorization")
			if r.Path == "/auth/ci-token" {
				if auth != "" || r.Header.Get("Idempotency-Key") != "" {
					return fmt.Errorf("the exchange sent Authorization %q, Idempotency-Key %q",
						auth, r.Header.Get("Idempotency-Key"))
				}
				continue
			}
			if !strings.HasPrefix(auth, "Bearer ataila_cit_") {
				return fmt.Errorf("%s %s: Authorization %q is not an exchanged token", r.Method, r.Path, auth)
			}
		}
		return nil
	}
}

// The pipeline's way: ATAILA_CI_JWT holds the job's ID token, and the provider
// acts as the linked service account with the link's scopes, as `ci`.
func TestAccProviderCIToken_ExchangesTheJobsIDToken(t *testing.T) {
	m, g := ciMock(t, acctest.CILink{})
	t.Setenv("ATAILA_CI_JWT", jobIDToken(m, g, nil))
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config: whoamiConfig,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.ataila_whoami.me", "auth_kind", "ci"),
				resource.TestCheckResourceAttr("data.ataila_whoami.me", "principal.kind", "service"),
				resource.TestCheckResourceAttr("data.ataila_whoami.me", "scopes.#", "1"),
				resource.TestCheckResourceAttr("data.ataila_whoami.me", "scopes.0", "users-read-global"),
				resource.TestMatchResourceAttr("data.ataila_whoami.me", "token.prefix", regexp.MustCompile(`^ci\d{6}$`)),
				everyRequestUsedAnExchangedToken(m),
			),
		}},
	})
}

// ci_jwt set in the provider block works the same way.
func TestAccProviderCIToken_InTheProviderBlock(t *testing.T) {
	m, g := ciMock(t, acctest.CILink{AllowDestroy: true})
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf("provider \"ataila\" {\n  ci_jwt = %q\n}\n\ndata \"ataila_whoami\" \"me\" {}\n",
				jobIDToken(m, g, nil)),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.ataila_whoami.me", "auth_kind", "ci"),
				resource.TestCheckResourceAttr("data.ataila_whoami.me", "token.allow_destroy", "true"),
				everyRequestUsedAnExchangedToken(m),
			),
		}},
	})
}

// A job that holds both variables keeps using its API token: ATAILA_TOKEN
// comes before ATAILA_CI_JWT, as it did before ci_jwt existed.
func TestAccProviderCIToken_AnAPITokenComesFirst(t *testing.T) {
	m, g := ciMock(t, acctest.CILink{})
	t.Setenv("ATAILA_TOKEN", acctest.MockToken)
	t.Setenv("ATAILA_CI_JWT", jobIDToken(m, g, nil))
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config: whoamiConfig,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.ataila_whoami.me", "auth_kind", "service_account"),
				func(*terraform.State) error {
					if n := m.CIExchanges(); n != 0 {
						return fmt.Errorf("the provider exchanged the ID token %d times while it had a token", n)
					}
					return nil
				},
			),
		}},
	})
}

func TestAccProviderCIToken_TokenAndCIJWTConflict(t *testing.T) {
	m, g := ciMock(t, acctest.CILink{})
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf("provider \"ataila\" {\n  token  = %q\n  ci_jwt = %q\n}\n\ndata \"ataila_meta\" \"this\" {}\n",
				acctest.MockToken, jobIDToken(m, g, nil)),
			ExpectError: words("Invalid Attribute Combination"),
		}},
	})
}

func TestAccProviderCIToken_MissingCredentialNamesBothWays(t *testing.T) {
	ciMock(t, acctest.CILink{})
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config:      metaConfig,
			ExpectError: words("Missing ATAILA API token .* ATAILA_CI_JWT"),
		}},
	})
}

// Every refusal of the exchange stops the run at configure time, before any
// other call, with what to do about it.
func TestAccProviderCIToken_Refusals(t *testing.T) {
	stranger := acctest.NewFakeGitLab(t, "https://gitlab.stranger.example")
	cases := []struct {
		name   string
		link   acctest.CILink
		token  func(m *acctest.MockAPI, g *acctest.FakeGitLab) string
		expect string
	}{
		{"unlinked project", acctest.CILink{ProjectPath: "infra/another"},
			func(m *acctest.MockAPI, g *acctest.FakeGitLab) string { return jobIDToken(m, g, nil) },
			"refused the GitLab CI ID token .* GitLab CI links .* ci_project_not_linked"},
		{"wrong audience", acctest.CILink{},
			func(m *acctest.MockAPI, g *acctest.FakeGitLab) string {
				return jobIDToken(m, g, func(c *acctest.IDTokenClaims) { c.Audience = "https://portal.example.com" })
			},
			"aud .* The provider talks to"},
		{"expired ID token", acctest.CILink{},
			func(m *acctest.MockAPI, g *acctest.FakeGitLab) string {
				return jobIDToken(m, g, func(c *acctest.IDTokenClaims) { c.ExpiresIn = -time.Minute })
			},
			"The ID token has expired"},
		{"untrusted GitLab", acctest.CILink{},
			func(m *acctest.MockAPI, _ *acctest.FakeGitLab) string { return jobIDToken(m, stranger, nil) },
			"does not trust the GitLab"},
		{"unprotected ref", acctest.CILink{},
			func(m *acctest.MockAPI, g *acctest.FakeGitLab) string {
				return jobIDToken(m, g, func(c *acctest.IDTokenClaims) { c.Ref, c.RefProtected = "feature/x", false })
			},
			"protected branches and tags only"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, g := ciMock(t, tc.link)
			t.Setenv("ATAILA_CI_JWT", tc.token(m, g))
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: protoV6,
				Steps: []resource.TestStep{{
					Config:      metaConfig,
					ExpectError: words(tc.expect),
				}},
			})
			for _, r := range m.Requests() {
				if r.Path != "/auth/ci-token" {
					t.Errorf("after a refused exchange the provider still called %s %s", r.Method, r.Path)
				}
			}
		})
	}
}

// An unprotected ref is fine when the operator's link says so.
func TestAccProviderCIToken_AnyRefWhenTheLinkAllowsIt(t *testing.T) {
	m, g := ciMock(t, acctest.CILink{AnyRef: true})
	t.Setenv("ATAILA_CI_JWT", jobIDToken(m, g, func(c *acctest.IDTokenClaims) { c.RefProtected = false }))
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config: whoamiConfig,
			Check:  resource.TestCheckResourceAttr("data.ataila_whoami.me", "auth_kind", "ci"),
		}},
	})
}

// A platform before 1.0.395 has no exchange and answers 404: the provider
// names the release that brought it, and the way out.
func TestAccProviderCIToken_OlderPlatform(t *testing.T) {
	m, g := ciMock(t, acctest.CILink{})
	t.Setenv("ATAILA_CI_JWT", jobIDToken(m, g, nil))
	for range 4 {
		m.InjectFaults("/auth/ci-token", acctest.Fault{Status: http.StatusNotFound, Code: "not_found"})
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config:      metaConfig,
			ExpectError: words("came with platform release " + client.ReleaseCIExchange + " .* ATAILA_TOKEN"),
		}},
	})
}

// A rate-limited exchange is retried after Retry-After, like any request.
func TestAccProviderCIToken_ARateLimitedExchangeIsRetried(t *testing.T) {
	m, g := ciMock(t, acctest.CILink{})
	t.Setenv("ATAILA_CI_JWT", jobIDToken(m, g, nil))
	m.InjectFaults("/auth/ci-token", acctest.Fault{Status: http.StatusTooManyRequests, Code: "rate_limited", RetryAfter: "1"})
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config: whoamiConfig,
			Check:  resource.TestCheckResourceAttr("data.ataila_whoami.me", "auth_kind", "ci"),
		}},
	})
}
