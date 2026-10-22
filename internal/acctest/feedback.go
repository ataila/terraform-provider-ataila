// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package acctest

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// The Feedback & Roadmap backlog (platform 1.0.260), read only, as
// routers/feedback.py serves it: feedback-admin-global; 404
// feedback_unavailable off the release origin (SetFeedbackOrigin(false));
// GET /feedback/{n}/brief is a STATE CHANGE (the item moves to in_progress),
// so the mock counts it and a test proves the provider never calls it.

type feedbackState struct {
	items     map[int]map[string]any
	notOrigin bool
	briefs    int
}

// AddFeedbackItem files an item; summary members not given get defaults.
func (m *MockAPI) AddFeedbackItem(number int, title, status, kind, customer string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.feedback == nil {
		m.feedback = &feedbackState{items: map[int]map[string]any{}}
	}
	ts := "2026-10-20T09:00:00Z"
	var severity any
	if kind == "blocker_bug" {
		severity = "major"
	}
	m.feedback.items[number] = map[string]any{
		"number": number, "title": title, "public_title": nil, "kind": kind, "severity": severity,
		"status": status, "status_reason": nil, "customer_key": strings.ToLower(customer),
		"customer_label": customer, "source": "internal", "lang": "en", "summary_en": nil, "votes": 2,
		"expected_week": "2026-W44", "planned_release_id": nil, "delivered_release_id": nil,
		"delivered_release_seq": nil, "delivered_build": nil, "duplicate_of_number": nil,
		"created_at": ts, "updated_at": ts,
		"body": "Steps to reproduce: …", "acceptance_criteria": "It works.", "submitter_name": "Dana",
		"ticket_url": nil, "ready_at": nil, "delivered_at": nil, "released_at": nil, "confirmed_at": nil,
		"auto_close_at": nil,
		"page":          map[string]any{"key": "projects", "route": "/projects/12", "title": "Projects"},
		"context":       map[string]any{"build": "1.0.396", "locale": "en", "release_id": "2026.10.13", "release_seq": 13},
		"comments": []map[string]any{
			{"author_name": "Ops", "body": "Triaged.", "created_at": ts, "internal": true, "lang": "en", "side": "ataila"},
		},
		"week_history": []map[string]any{
			{"at": ts, "previous_week": "2026-W43", "week": "2026-W44", "reason": "Capacity."},
		},
	}
}

// SetFeedbackOrigin says whether this platform holds the backlog (default true).
func (m *MockAPI) SetFeedbackOrigin(origin bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.feedback == nil {
		m.feedback = &feedbackState{items: map[int]map[string]any{}}
	}
	m.feedback.notOrigin = !origin
}

// FeedbackBriefs counts GET /feedback/{n}/brief, which moves an item.
func (m *MockAPI) FeedbackBriefs() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.feedback == nil {
		return 0
	}
	return m.feedback.briefs
}

var feedbackSummaryKeys = []string{"number", "title", "public_title", "kind", "severity", "status", "status_reason",
	"customer_key", "customer_label", "source", "lang", "summary_en", "votes", "expected_week", "planned_release_id",
	"delivered_release_id", "delivered_release_seq", "delivered_build", "duplicate_of_number", "created_at", "updated_at"}

func (m *MockAPI) routeFeedback(c *call) (reply, bool) {
	parts := strings.Split(strings.Trim(c.path, "/"), "/")
	if parts[0] != "feedback" {
		return reply{}, false
	}
	if c.r.Method != http.MethodGet {
		return c.methodNotAllowed(), true
	}
	if r := m.require(c, "feedback-admin-global"); r != nil {
		return *r, true
	}
	if m.feedback == nil {
		m.feedback = &feedbackState{items: map[int]map[string]any{}}
	}
	if m.feedback.notOrigin {
		return c.problem(http.StatusNotFound, "feedback_unavailable",
			"This platform does not hold the feedback backlog.", nil), true
	}
	if len(parts) == 1 {
		return m.feedbackList(c), true
	}
	n, err := strconv.Atoi(parts[1])
	item := m.feedback.items[n]
	if err != nil || item == nil {
		return c.problem(http.StatusNotFound, "feedback_item_not_found", "No such item.", nil), true
	}
	if len(parts) == 3 && parts[2] == "brief" {
		m.feedback.briefs++
		item["status"] = "in_progress"
		return reply{status: http.StatusOK, body: "# brief"}, true
	}
	if len(parts) != 2 {
		return c.problem(http.StatusNotFound, "not_found", "", nil), true
	}
	return ok(http.StatusOK, item), true
}

func (m *MockAPI) feedbackList(c *call) reply {
	q := c.r.URL.Query()
	var numbers []int
	for n, it := range m.feedback.items {
		match := true
		for _, f := range []string{"status", "kind", "source"} {
			if v := q.Get(f); v != "" && it[f] != v {
				match = false
			}
		}
		if v := q.Get("customer"); v != "" && it["customer_key"] != v {
			match = false
		}
		if v := q.Get("week"); v != "" && it["expected_week"] != v {
			match = false
		}
		if match {
			numbers = append(numbers, n)
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(numbers)))
	items := make([]map[string]any, 0, len(numbers))
	for _, n := range numbers {
		s := map[string]any{}
		for _, k := range feedbackSummaryKeys {
			s[k] = m.feedback.items[n][k]
		}
		items = append(items, s)
	}
	return ok(http.StatusOK, map[string]any{"items": items, "next_cursor": nil})
}
