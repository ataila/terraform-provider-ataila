// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/ataila/terraform-provider-ataila/internal/acctest"
	"github.com/ataila/terraform-provider-ataila/internal/client"
)

// feedbackMock is the release origin with three items, and a token holding
// feedback-admin-global.
func feedbackMock(t *testing.T) *acctest.MockAPI {
	t.Helper()
	m := newMock(t)
	m.SetScopes(append(acctest.DefaultScopes(), "feedback-admin-global")...)
	m.SetMeta(func(meta map[string]any) { meta["platform_version"] = client.ReleaseServiceAccounts })
	m.AddFeedbackItem(41, "Export fails on large projects", "triaged", "blocker_bug", "EXAMPLE")
	m.AddFeedbackItem(42, "Dark theme for the portal", "scheduled", "new_feature", "EXAMPLE")
	m.AddFeedbackItem(43, "Rename a tenant", "new", "change", "OTHER")
	return m
}

// briefNeverFetched: GET /feedback/{n}/brief moves an item to in_progress, so
// a read-only data source must never call it.
func briefNeverFetched(m *acctest.MockAPI) resource.TestCheckFunc {
	return func(*terraform.State) error {
		if n := m.FeedbackBriefs(); n != 0 {
			return fmt.Errorf("the provider fetched a brief %d times; that moves the item", n)
		}
		return nil
	}
}

func TestAccFeedbackItems(t *testing.T) {
	m := feedbackMock(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config: providerBlock(false) + `
data "ataila_feedback_items" "all" {}

data "ataila_feedback_items" "example_bugs" {
  customer = "example"
  kind     = "blocker_bug"
}
`,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.ataila_feedback_items.all", "items.#", "3"),
				resource.TestCheckResourceAttr("data.ataila_feedback_items.all", "items.0.number", "43"),
				resource.TestCheckResourceAttr("data.ataila_feedback_items.example_bugs", "items.#", "1"),
				resource.TestCheckResourceAttr("data.ataila_feedback_items.example_bugs", "items.0.title",
					"Export fails on large projects"),
				resource.TestCheckResourceAttr("data.ataila_feedback_items.example_bugs", "items.0.severity", "major"),
				resource.TestCheckResourceAttr("data.ataila_feedback_items.example_bugs", "items.0.expected_week", "2026-W44"),
				briefNeverFetched(m),
			),
		}},
	})
}

func TestAccFeedbackItem(t *testing.T) {
	m := feedbackMock(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{
				Config: providerBlock(false) + `
data "ataila_feedback_item" "bug" {
  number = 41
}
`,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.ataila_feedback_item.bug", "status", "triaged"),
					resource.TestCheckResourceAttr("data.ataila_feedback_item.bug", "body", "Steps to reproduce: …"),
					resource.TestCheckResourceAttr("data.ataila_feedback_item.bug", "page.route", "/projects/12"),
					resource.TestCheckResourceAttr("data.ataila_feedback_item.bug", "context.release_seq", "13"),
					resource.TestCheckResourceAttr("data.ataila_feedback_item.bug", "comments.#", "1"),
					resource.TestCheckResourceAttr("data.ataila_feedback_item.bug", "comments.0.internal", "true"),
					resource.TestCheckResourceAttr("data.ataila_feedback_item.bug", "week_history.0.previous_week", "2026-W43"),
					briefNeverFetched(m),
				),
			},
			{
				Config: providerBlock(false) + `
data "ataila_feedback_item" "none" {
  number = 999
}
`,
				ExpectError: words("feedback_item_not_found"),
			},
		},
	})
}

// Off the release origin the backlog is not there: the error says where it is.
func TestAccFeedbackItems_NotTheReleaseOrigin(t *testing.T) {
	m := feedbackMock(t)
	m.SetFeedbackOrigin(false)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config:      providerBlock(false) + "data \"ataila_feedback_items\" \"all\" {}\n",
			ExpectError: words("This platform does not hold the feedback backlog .* issues releases"),
		}},
	})
}

// Without feedback-admin-global the platform refuses, and the error names the key.
func TestAccFeedbackItems_NeedsTheAdminKey(t *testing.T) {
	m := feedbackMock(t)
	m.SetScopes(acctest.DefaultScopes()...)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config:      providerBlock(false) + "data \"ataila_feedback_items\" \"all\" {}\n",
			ExpectError: words("feedback-admin-global"),
		}},
	})
}

// On a platform before the backlog reads, nothing is sent.
func TestAccFeedbackItems_OlderPlatform(t *testing.T) {
	m := feedbackMock(t)
	m.SetMeta(func(meta map[string]any) { meta["platform_version"] = "1.0.237" })
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config:      providerBlock(false) + "data \"ataila_feedback_items\" \"all\" {}\n",
			ExpectError: words("needs platform release " + client.ReleaseFeedback),
		}},
	})
	if n := m.Hits("/feedback"); n != 0 {
		t.Errorf("%d reads were sent to a platform that does not serve them", n)
	}
}
