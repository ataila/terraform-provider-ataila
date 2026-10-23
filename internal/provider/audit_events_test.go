// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/ataila/terraform-provider-ataila/internal/acctest"
)

// auditMock holds five events over two days and a token holding audit-read-global.
func auditMock(t *testing.T) *acctest.MockAPI {
	t.Helper()
	m := newMock(t)
	m.SetScopes(append(acctest.DefaultScopes(), "audit-read-global")...)
	day1 := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	day2 := day1.Add(24 * time.Hour)
	m.AddAuditEvent(day1, "ops@example.com", "user.create", "users", "u-1", "session", "")
	m.AddAuditEvent(day1.Add(time.Hour), "ops@example.com", "user.update", "users", "u-1", "pat", "")
	m.AddAuditEvent(day1.Add(2*time.Hour), "system", "tenant.create", "tenant", "t-1", "", "9a1b2c3d-0000-4000-8000-000000000001")
	m.AddAuditEvent(day2, "ci@example.com", "api_token.ci_exchange", "api_token", "tok-1", "ci", "")
	m.AddAuditEvent(day2.Add(time.Hour), "ops@example.com", "user.delete", "users", "u-1", "session", "")
	return m
}

func TestAccAuditEvents_filters(t *testing.T) {
	auditMock(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config: providerBlock(false) + `
data "ataila_audit_events" "all" {}

data "ataila_audit_events" "users" {
  action         = "user.*"
  include_states = true
}

data "ataila_audit_events" "day2_by_ops" {
  actor         = "ops@example.com"
  occurred_from = "2026-10-23T00:00:00Z"
}

data "ataila_audit_events" "ci" {
  auth_kind = "ci"
}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.ataila_audit_events.all", "events.#", "5"),
				resource.TestCheckResourceAttr("data.ataila_audit_events.all", "more", "false"),
				resource.TestCheckResourceAttr("data.ataila_audit_events.all", "events.0.action", "user.delete"),
				resource.TestCheckResourceAttr("data.ataila_audit_events.all", "events.0.occurred_at", "2026-10-23T10:00:00Z"),
				resource.TestCheckNoResourceAttr("data.ataila_audit_events.all", "events.0.after_state"),
				resource.TestCheckResourceAttr("data.ataila_audit_events.users", "events.#", "3"),
				resource.TestCheckResourceAttr("data.ataila_audit_events.users", "events.2.after_state", `{"name":"u-1"}`),
				resource.TestCheckResourceAttr("data.ataila_audit_events.day2_by_ops", "events.#", "1"),
				resource.TestCheckResourceAttr("data.ataila_audit_events.day2_by_ops", "events.0.action", "user.delete"),
				resource.TestCheckResourceAttr("data.ataila_audit_events.ci", "events.#", "1"),
				resource.TestCheckResourceAttr("data.ataila_audit_events.ci", "events.0.entity_type", "api_token"),
				resource.TestCheckNoResourceAttr("data.ataila_audit_events.ci", "events.0.tenant_id"),
			),
		}},
	})
}

// The log is large: a read keeps `limit` events, says there are more, and
// does not walk the rest of the log.
func TestAccAuditEvents_limitStopsTheRead(t *testing.T) {
	m := newMock(t)
	m.SetScopes(append(acctest.DefaultScopes(), "audit-read-global")...)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 1200; i++ {
		m.AddAuditEvent(start.Add(time.Duration(i)*time.Minute), "system", "project.update", "project", fmt.Sprint(i), "", "")
	}
	factories, rec := recordingProvider()
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{{
			Config: providerBlock(false) + `
data "ataila_audit_events" "default" {}

data "ataila_audit_events" "many" {
  limit = 700
}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.ataila_audit_events.default", "events.#", "100"),
				resource.TestCheckResourceAttr("data.ataila_audit_events.default", "more", "true"),
				resource.TestCheckResourceAttr("data.ataila_audit_events.default", "events.0.entity_id", "1199"),
				resource.TestCheckResourceAttr("data.ataila_audit_events.many", "events.#", "700"),
				resource.TestCheckResourceAttr("data.ataila_audit_events.many", "more", "true"),
				rec.expectWarning("More audit events matched", "limit = 100"),
				check(func() error {
					// default: one page of 100; many: 500 + 200. Per read of the two data
					// sources (plan and apply refresh them), never the 1200-event log.
					if n := m.AuditPagesRead(); n%3 != 0 || n > 3*4 {
						return fmt.Errorf("%d pages read; want 3 per read of the two data sources", n)
					}
					return nil
				}),
			),
		}},
	})
}

func TestAccAuditEvents_needsTheAuditKey(t *testing.T) {
	newMock(t) // DefaultScopes holds no audit key
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config:      providerBlock(false) + `data "ataila_audit_events" "all" {}`,
			ExpectError: words("audit-read-global"),
		}},
	})
}
