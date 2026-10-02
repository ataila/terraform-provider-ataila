// Copyright (c) 2026 ATAILA Kft.
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/ataila/terraform-provider-ataila/internal/acctest"
)

const whoamiConfig = `
provider "ataila" {}

data "ataila_whoami" "me" {}
`

func TestAccWhoamiDataSource(t *testing.T) {
	m := acctest.NewMockAPI(t)
	m.UseEnv(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config: whoamiConfig,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.ataila_whoami.me", "principal.kind", "service"),
				resource.TestCheckResourceAttr("data.ataila_whoami.me", "principal.name", "ci-bot"),
				resource.TestCheckResourceAttr("data.ataila_whoami.me", "principal.email", "ci-bot@service-account.invalid"),
				resource.TestCheckResourceAttr("data.ataila_whoami.me", "auth_kind", "service_account"),
				resource.TestCheckResourceAttr("data.ataila_whoami.me", "scopes.#", "18"),
				resource.TestCheckResourceAttr("data.ataila_whoami.me", "scopes.0", "ai-center-admin-global"),
				resource.TestCheckResourceAttr("data.ataila_whoami.me", "expires_at", "2027-01-01T00:00:00Z"),
				resource.TestCheckResourceAttr("data.ataila_whoami.me", "token.prefix", "mocktokn"),
				resource.TestCheckResourceAttr("data.ataila_whoami.me", "token.granted_scopes.#", "18"),
				resource.TestCheckResourceAttr("data.ataila_whoami.me", "token.allow_destroy", "false"),
			),
		}},
	})
}

// A portal session has no token object and may have no expiry.
func TestAccWhoamiDataSource_Session(t *testing.T) {
	m := acctest.NewMockAPI(t)
	m.UseEnv(t)
	m.SetWhoami(map[string]any{
		"principal": map[string]any{"id": "u-1", "email": "operator@example.com", "name": "Operator", "kind": "human"},
		"auth_kind": "session",
		"scopes":    []string{},
	})
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config: whoamiConfig,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.ataila_whoami.me", "principal.kind", "human"),
				resource.TestCheckResourceAttr("data.ataila_whoami.me", "auth_kind", "session"),
				resource.TestCheckResourceAttr("data.ataila_whoami.me", "scopes.#", "0"),
				resource.TestCheckNoResourceAttr("data.ataila_whoami.me", "expires_at"),
				resource.TestCheckNoResourceAttr("data.ataila_whoami.me", "token.prefix"),
			),
		}},
	})
}

// A licence refusal is final and the diagnostic quotes the API's remedy.
func TestAccWhoamiDataSource_LicenceRefusal(t *testing.T) {
	m := acctest.NewMockAPI(t)
	m.UseEnv(t)
	m.RefuseLicence("/whoami", "licence_locked", "Install a current licence on the Licence page.")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config:      whoamiConfig,
			ExpectError: words("Refused by the platform licence (licence_locked) .* Remedy: Install a current licence on the Licence page."),
		}},
	})
	if got := m.Hits("/whoami"); got != 1 {
		t.Errorf("GET /whoami was sent %d times; a licence refusal must not be retried", got)
	}
}

// 503 and 429 are retried (honouring Retry-After) until the API answers.
func TestAccWhoamiDataSource_RetriesTransientErrors(t *testing.T) {
	m := acctest.NewMockAPI(t)
	m.UseEnv(t)
	m.InjectFaults("/whoami",
		acctest.Fault{Status: 503, Code: "unavailable", RetryAfter: "0"},
		acctest.Fault{Status: 429, Code: "rate_limited", RetryAfter: "0"},
	)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config: whoamiConfig,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.ataila_whoami.me", "auth_kind", "service_account"),
				func(*terraform.State) error {
					if got := m.Hits("/whoami"); got < 3 {
						return fmt.Errorf("GET /whoami was sent %d times, want at least 3", got)
					}
					return nil
				},
			),
		}},
	})
}

// Any other error is mapped from the problem document into the diagnostic.
func TestAccWhoamiDataSource_ProblemDetails(t *testing.T) {
	m := acctest.NewMockAPI(t)
	m.UseEnv(t)
	m.InjectFaults("/whoami", acctest.Fault{Status: 403, Code: "forbidden"})
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config:      whoamiConfig,
			ExpectError: words("ATAILA API error: Forbidden .* code: forbidden .* request_id: mock-"),
		}},
	})
}
