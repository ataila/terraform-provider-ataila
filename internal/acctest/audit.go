// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package acctest

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The audit log, read only, as routers/audit.py serves it: audit-read-global;
// newest first; exact filters (an action ending in `*` is a prefix); a page
// of at most 500 with an opaque cursor. The mock counts the pages it served,
// so a test can prove a read stops at its limit instead of walking the log.

type auditState struct {
	events []map[string]any // oldest first; the list answers newest first
	pages  int
}

func (m *MockAPI) audit() *auditState {
	if m.auditLog == nil {
		m.auditLog = &auditState{}
	}
	return m.auditLog
}

// AddAuditEvent records an event at the given time; empty strings are null.
func (m *MockAPI) AddAuditEvent(at time.Time, actor, action, entityType, entityID, authKind, tenantID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.audit()
	nullable := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	a.events = append(a.events, map[string]any{
		"id": strconv.Itoa(len(a.events) + 1), "occurred_at": wireTime(at.UTC()), "actor": actor,
		"action": action, "entity_type": entityType, "entity_id": nullable(entityID),
		"auth_kind": nullable(authKind), "token_id": nil, "request_id": fmt.Sprintf("req-%d", len(a.events)+1),
		"source_ip": "192.0.2.10", "tenant_id": nullable(tenantID),
		"before_state": nil, "after_state": map[string]any{"name": entityID},
	})
}

// AuditPagesRead is how many pages of GET /audit-events the mock served.
func (m *MockAPI) AuditPagesRead() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.audit().pages
}

func (m *MockAPI) routeAudit(c *call) (reply, bool) {
	if c.path != "/audit-events" && !strings.HasPrefix(c.path, "/audit-events/") {
		return reply{}, false
	}
	if c.r.Method != http.MethodGet {
		return c.methodNotAllowed(), true
	}
	if r := m.require(c, "audit-read-global"); r != nil {
		return *r, true
	}
	a := m.audit()
	if id, one := strings.CutPrefix(c.path, "/audit-events/"); one {
		for _, e := range a.events {
			if e["id"] == id {
				return ok(http.StatusOK, e), true
			}
		}
		return c.problem(http.StatusNotFound, "audit_event_not_found", "No such event.", nil), true
	}
	q := c.r.URL.Query()
	var from, to time.Time
	if v := q.Get("occurred_from"); v != "" {
		from, _ = time.Parse(time.RFC3339, v)
	}
	if v := q.Get("occurred_to"); v != "" {
		to, _ = time.Parse(time.RFC3339, v)
	}
	var matched []map[string]any
	for i := len(a.events) - 1; i >= 0; i-- { // newest first
		e := a.events[i]
		keep := true
		for _, f := range []string{"actor", "auth_kind", "token_id", "request_id", "entity_type", "entity_id", "tenant_id"} {
			if v := q.Get(f); v != "" && fmt.Sprint(e[f]) != v {
				keep = false
			}
		}
		if v := q.Get("action"); v != "" {
			action, _ := e["action"].(string)
			if prefix, isPrefix := strings.CutSuffix(v, "*"); isPrefix {
				keep = keep && strings.HasPrefix(action, prefix)
			} else {
				keep = keep && action == v
			}
		}
		at, _ := time.Parse(time.RFC3339, e["occurred_at"].(string))
		if !from.IsZero() && at.Before(from) || !to.IsZero() && !at.Before(to) {
			keep = false
		}
		if keep {
			matched = append(matched, e)
		}
	}
	limit := 50
	if v, err := strconv.Atoi(q.Get("limit")); err == nil {
		limit = v
	}
	if limit < 1 || limit > 500 {
		return c.problem(http.StatusUnprocessableEntity, "validation_failed", "limit must be 1-500.", nil), true
	}
	start := 0
	if cur := q.Get("cursor"); cur != "" {
		n, err := strconv.Atoi(strings.TrimPrefix(cur, "audit:"))
		if err != nil || n < 0 || n > len(matched) {
			return c.problem(http.StatusBadRequest, "invalid_cursor", "This cursor is not from this list.", nil), true
		}
		start = n
	}
	end := min(start+limit, len(matched))
	var next any
	if end < len(matched) {
		next = fmt.Sprintf("audit:%d", end)
	}
	a.pages++
	return ok(http.StatusOK, map[string]any{"items": matched[start:end], "next_cursor": next}), true
}
