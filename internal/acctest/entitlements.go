// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package acctest

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// Service entitlements and grants (platform 1.0.392 on), with the platform's
// rules:
//
//   - reads take entitlements-read-global or -admin-global, writes the admin
//     key, both DELETEs additionally a token created with allow_destroy;
//   - the API never approves: `enabled` on a service that requires approval
//     and has none is stored `pending_approval`, with the warning
//     approval_required. ApproveEntitlement is the operator's portal approval,
//     and a later PUT keeps it;
//   - DELETE of an entitlement disables it and revokes every grant under it;
//     DELETE of a grant revokes it (again: 204). Nothing is removed;
//   - a grant needs the entitlement (409 entitlement_not_held) and a free seat
//     (409 seats_exhausted); PUT of a revoked grant revives it (201).

const entitlementsAt = "2026-10-22T08:00:00Z"

type mockService struct {
	key, name, category, enforcement, url string
	requiresApproval, active              bool
	order                                 int
}

type mockEntitlement struct {
	state                string
	seats                *int
	dedicatedURL, notes  *string
	approved             bool
	createdAt, updatedAt string
}

type mockGrant struct {
	level     string
	notes     *string
	revokedAt *string
}

type entitlementsState struct {
	services     map[string]*mockService
	entitlements map[int]map[string]*mockEntitlement      // customer -> service
	grants       map[string]map[int]map[string]*mockGrant // user -> customer -> service
}

func newEntitlementsState() *entitlementsState {
	s := &entitlementsState{
		services:     map[string]*mockService{},
		entitlements: map[int]map[string]*mockEntitlement{},
		grants:       map[string]map[int]map[string]*mockGrant{},
	}
	for i, sv := range []mockService{
		{key: "coder", name: "Coder", category: "shared", enforcement: "claim", active: true},
		{key: "gitlab", name: "GitLab", category: "shared", enforcement: "projection", active: true},
		{key: "n8n", name: "n8n", category: "dedicated", enforcement: "role", active: true, requiresApproval: true},
		{key: "old-thing", name: "Old thing", category: "shared", enforcement: "manual"},
	} {
		sv := sv
		sv.order = (i + 1) * 10
		sv.url = "https://" + sv.key + ".example.com"
		s.services[sv.key] = &sv
	}
	return s
}

// ApproveEntitlement is an operator approving a customer's entitlement in the
// portal: a pending one becomes enabled.
func (m *MockAPI) ApproveEntitlement(customerID, serviceKey string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cid, _ := strconv.Atoi(customerID)
	if e := m.entitlements.entitlements[cid][serviceKey]; e != nil {
		e.approved = true
		if e.state == "pending_approval" {
			e.state = "enabled"
		}
	}
}

// RevokeGrantOutOfBand revokes a grant in the portal.
func (m *MockAPI) RevokeGrantOutOfBand(userID, customerID, serviceKey string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cid, _ := strconv.Atoi(customerID)
	if g := m.entitlements.grants[userID][cid][serviceKey]; g != nil && g.revokedAt == nil {
		at := entitlementsAt
		g.revokedAt = &at
	}
}

// EntitlementState is the stored state of a customer's entitlement ("" when
// the customer never held the service).
func (m *MockAPI) EntitlementState(customerID, serviceKey string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	cid, _ := strconv.Atoi(customerID)
	if e := m.entitlements.entitlements[cid][serviceKey]; e != nil {
		return e.state
	}
	return ""
}

// GrantRevoked reports whether a grant exists and is revoked.
func (m *MockAPI) GrantRevoked(userID, customerID, serviceKey string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	cid, _ := strconv.Atoi(customerID)
	g := m.entitlements.grants[userID][cid][serviceKey]
	return g != nil && g.revokedAt != nil
}

func (m *MockAPI) entitlementsRead(c *call) *reply {
	return m.require(c, "entitlements-read-global", "entitlements-admin-global")
}

func (m *MockAPI) entitlementsWrite(c *call) *reply {
	return m.require(c, "entitlements-admin-global")
}

func (m *MockAPI) entitlementsDestroy(c *call) *reply {
	if r := m.entitlementsWrite(c); r != nil {
		return r
	}
	if tok, isToken := m.whoami["token"].(map[string]any); isToken && tok != nil {
		if allow, _ := tok["allow_destroy"].(bool); !allow {
			r := c.problem(http.StatusForbidden, "destroy_not_allowed",
				"This API token was not created with allow_destroy.", nil)
			return &r
		}
	}
	return nil
}

func (m *MockAPI) routeEntitlements(c *call) (reply, bool) {
	parts := strings.Split(strings.Trim(c.path, "/"), "/")
	method := c.r.Method
	switch {
	case c.path == "/services":
		if method != http.MethodGet {
			return c.methodNotAllowed(), true
		}
		return m.servicesList(c), true
	case len(parts) == 3 && parts[0] == "customers" && parts[2] == "services":
		if method != http.MethodGet {
			return c.methodNotAllowed(), true
		}
		return m.customerServicesList(c, parts[1]), true
	case len(parts) == 4 && parts[0] == "customers" && parts[2] == "services":
		switch method {
		case http.MethodGet:
			return m.customerServicesGet(c, parts[1], parts[3]), true
		case http.MethodPut:
			return m.customerServicesPut(c, parts[1], parts[3]), true
		case http.MethodDelete:
			return m.customerServicesDelete(c, parts[1], parts[3]), true
		}
		return c.methodNotAllowed(), true
	case len(parts) == 5 && parts[0] == "users" && parts[2] == "service-grants":
		switch method {
		case http.MethodGet:
			return m.grantGet(c, parts[1], parts[3], parts[4]), true
		case http.MethodPut:
			return m.grantPut(c, parts[1], parts[3], parts[4]), true
		case http.MethodDelete:
			return m.grantDelete(c, parts[1], parts[3], parts[4]), true
		}
		return c.methodNotAllowed(), true
	}
	return reply{}, false
}

func serviceWire(s *mockService) map[string]any {
	return map[string]any{
		"key": s.key, "name": s.name, "description": "The " + s.name + " service", "category": s.category,
		"enforcement": s.enforcement, "url": s.url, "requires_approval": s.requiresApproval,
		"active": s.active, "sort_order": s.order,
	}
}

func (m *MockAPI) servicesList(c *call) reply {
	if r := m.entitlementsRead(c); r != nil {
		return *r
	}
	all := c.r.URL.Query().Get("include_inactive") == "true"
	var list []*mockService
	for _, s := range m.entitlements.services {
		if all || s.active {
			list = append(list, s)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].order < list[j].order })
	items := make([]map[string]any, 0, len(list))
	for _, s := range list {
		items = append(items, serviceWire(s))
	}
	return ok(http.StatusOK, map[string]any{"items": items, "next_cursor": nil})
}

func (m *MockAPI) seatsUsed(cid int, key string) int {
	n := 0
	for _, byCustomer := range m.entitlements.grants {
		if g := byCustomer[cid][key]; g != nil && g.revokedAt == nil {
			n++
		}
	}
	return n
}

func (m *MockAPI) entitlementWire(cid int, key string, e *mockEntitlement) map[string]any {
	s := m.entitlements.services[key]
	url := s.url
	if e.dedicatedURL != nil {
		url = *e.dedicatedURL
	}
	var approvedAt any
	if e.approved {
		approvedAt = entitlementsAt
	}
	return map[string]any{
		"customer_id": strconv.Itoa(cid), "service_key": key, "service_name": s.name, "state": e.state,
		"seats": e.seats, "seats_used": m.seatsUsed(cid, key), "dedicated_url": e.dedicatedURL, "url": url,
		"approved": e.approved, "approved_at": approvedAt, "notes": e.notes,
		"created_at": e.createdAt, "updated_at": e.updatedAt,
	}
}

func (m *MockAPI) loadEntitlementCustomer(c *call, raw string) (int, *reply) {
	cid, valid := intID(raw)
	if !valid || m.tenancy.customers[cid] == nil {
		r := c.problem(http.StatusNotFound, "customer_not_found", "No such customer.", nil)
		return 0, &r
	}
	return cid, nil
}

func (m *MockAPI) loadService(c *call, key string) *reply {
	if m.entitlements.services[key] == nil {
		r := c.problem(http.StatusNotFound, "service_not_found", "No such service.", nil)
		return &r
	}
	return nil
}

func (m *MockAPI) customerServicesList(c *call, raw string) reply {
	if r := m.entitlementsRead(c); r != nil {
		return *r
	}
	cid, bad := m.loadEntitlementCustomer(c, raw)
	if bad != nil {
		return *bad
	}
	var keys []string
	for k := range m.entitlements.entitlements[cid] {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		return m.entitlements.services[keys[i]].order < m.entitlements.services[keys[j]].order
	})
	items := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		items = append(items, m.entitlementWire(cid, k, m.entitlements.entitlements[cid][k]))
	}
	return ok(http.StatusOK, map[string]any{"items": items, "next_cursor": nil})
}

func (m *MockAPI) customerServicesGet(c *call, raw, key string) reply {
	if r := m.entitlementsRead(c); r != nil {
		return *r
	}
	cid, bad := m.loadEntitlementCustomer(c, raw)
	if bad != nil {
		return *bad
	}
	if r := m.loadService(c, key); r != nil {
		return *r
	}
	e := m.entitlements.entitlements[cid][key]
	if e == nil {
		return c.problem(http.StatusNotFound, "entitlement_not_found", "The customer does not hold this service.", nil)
	}
	return ok(http.StatusOK, m.entitlementWire(cid, key, e))
}

func (m *MockAPI) customerServicesPut(c *call, raw, key string) reply {
	if r := m.entitlementsWrite(c); r != nil {
		return *r
	}
	v := &validation{}
	body := decodeBody(c, v, "state", "seats", "dedicated_url", "notes")
	state, _ := strField(v, body, "state", false, false, oneOf("enabled", "disabled"))
	seats, _ := intField(v, body, "seats", true, func(n int) string {
		if n < 1 {
			return "Input should be greater than 0"
		}
		return ""
	})
	dedicated, _ := strField(v, body, "dedicated_url", false, true, nil)
	notes, _ := strField(v, body, "notes", false, true, nil)
	if r := v.reply(c); r != nil {
		return *r
	}
	cid, bad := m.loadEntitlementCustomer(c, raw)
	if bad != nil {
		return *bad
	}
	if r := m.loadService(c, key); r != nil {
		return *r
	}
	want := "enabled"
	if state != nil {
		want = *state
	}
	if m.entitlements.entitlements[cid] == nil {
		m.entitlements.entitlements[cid] = map[string]*mockEntitlement{}
	}
	e := m.entitlements.entitlements[cid][key]
	status := http.StatusOK
	if e == nil {
		e = &mockEntitlement{createdAt: entitlementsAt}
		m.entitlements.entitlements[cid][key] = e
		status = http.StatusCreated
	}
	e.state, e.seats, e.dedicatedURL, e.notes, e.updatedAt = want, seats, dedicated, notes, entitlementsAt
	var warnings []map[string]any
	if want == "enabled" && !e.approved && m.entitlements.services[key].requiresApproval {
		e.state = "pending_approval"
		warnings = append(warnings, map[string]any{"code": "approval_required",
			"message": "The service needs an operator's approval in the portal."})
	}
	out := m.entitlementWire(cid, key, e)
	out["warnings"] = warnings
	if warnings == nil {
		out["warnings"] = []map[string]any{}
	}
	return ok(status, out)
}

func (m *MockAPI) customerServicesDelete(c *call, raw, key string) reply {
	if r := m.entitlementsDestroy(c); r != nil {
		return *r
	}
	cid, bad := m.loadEntitlementCustomer(c, raw)
	if bad != nil {
		return *bad
	}
	if r := m.loadService(c, key); r != nil {
		return *r
	}
	e := m.entitlements.entitlements[cid][key]
	if e == nil {
		return c.problem(http.StatusNotFound, "entitlement_not_found", "The customer does not hold this service.", nil)
	}
	e.state = "disabled"
	for _, byCustomer := range m.entitlements.grants {
		if g := byCustomer[cid][key]; g != nil && g.revokedAt == nil {
			at := entitlementsAt
			g.revokedAt = &at
		}
	}
	return reply{status: http.StatusNoContent}
}

func (m *MockAPI) grantWire(uid string, cid int, key string, g *mockGrant) map[string]any {
	e := m.entitlements.entitlements[cid][key]
	s := m.entitlements.services[key]
	var blocked any
	switch {
	case g.revokedAt != nil:
		blocked = "grant_revoked"
	case !s.active:
		blocked = "service_retired"
	case e.state != "enabled":
		blocked = "customer_" + e.state
	}
	return map[string]any{
		"user_id": uid, "user_email": m.tenancy.users[uid], "customer_id": strconv.Itoa(cid), "service_key": key,
		"grant_level": g.level, "effective": blocked == nil, "blocked_by": blocked,
		"granted_by": "00000000-0000-4000-8000-000000000001", "granted_at": entitlementsAt,
		"revoked_at": g.revokedAt, "notes": g.notes,
	}
}

func (m *MockAPI) grantGet(c *call, uid, rawCustomer, key string) reply {
	if r := m.entitlementsRead(c); r != nil {
		return *r
	}
	if _, exists := m.tenancy.users[uid]; !exists {
		return c.problem(http.StatusNotFound, "user_not_found", "No such user.", nil)
	}
	cid, _ := intID(rawCustomer)
	g := m.entitlements.grants[uid][cid][key]
	if g == nil {
		return c.problem(http.StatusNotFound, "grant_not_found", "No such grant.", nil)
	}
	return ok(http.StatusOK, m.grantWire(uid, cid, key, g))
}

func (m *MockAPI) grantPut(c *call, uid, rawCustomer, key string) reply {
	if r := m.entitlementsWrite(c); r != nil {
		return *r
	}
	v := &validation{}
	body := decodeBody(c, v, "grant_level", "notes")
	level, _ := strField(v, body, "grant_level", false, false, oneOf("login", "user", "admin", "audit"))
	notes, _ := strField(v, body, "notes", false, true, nil)
	if r := v.reply(c); r != nil {
		return *r
	}
	if _, exists := m.tenancy.users[uid]; !exists {
		return c.problem(http.StatusNotFound, "user_not_found", "No such user.", nil)
	}
	cid, bad := m.loadEntitlementCustomer(c, rawCustomer)
	if bad != nil {
		return *bad
	}
	if r := m.loadService(c, key); r != nil {
		return *r
	}
	e := m.entitlements.entitlements[cid][key]
	if e == nil {
		return c.problem(http.StatusConflict, "entitlement_not_held",
			"The customer does not hold this service; set it first.", nil)
	}
	g := m.entitlements.grants[uid][cid][key]
	revived := g == nil || g.revokedAt != nil
	if revived && e.seats != nil && m.seatsUsed(cid, key) >= *e.seats {
		return c.problem(http.StatusConflict, "seats_exhausted",
			"All "+strconv.Itoa(*e.seats)+" seats of this service are taken under the customer.",
			map[string]any{"seats": *e.seats})
	}
	if m.entitlements.grants[uid] == nil {
		m.entitlements.grants[uid] = map[int]map[string]*mockGrant{}
	}
	if m.entitlements.grants[uid][cid] == nil {
		m.entitlements.grants[uid][cid] = map[string]*mockGrant{}
	}
	want := "user"
	if level != nil {
		want = *level
	}
	g = &mockGrant{level: want, notes: notes}
	m.entitlements.grants[uid][cid][key] = g
	status := http.StatusOK
	if revived {
		status = http.StatusCreated
	}
	out := m.grantWire(uid, cid, key, g)
	warnings := []map[string]any{}
	if out["effective"] == false {
		warnings = append(warnings, map[string]any{"code": "grant_not_effective",
			"message": "The grant was made but is not in force: " + out["blocked_by"].(string) + "."})
	}
	out["warnings"] = warnings
	return ok(status, out)
}

func (m *MockAPI) grantDelete(c *call, uid, rawCustomer, key string) reply {
	if r := m.entitlementsDestroy(c); r != nil {
		return *r
	}
	if _, exists := m.tenancy.users[uid]; !exists {
		return c.problem(http.StatusNotFound, "user_not_found", "No such user.", nil)
	}
	cid, _ := intID(rawCustomer)
	g := m.entitlements.grants[uid][cid][key]
	if g == nil {
		return c.problem(http.StatusNotFound, "grant_not_found", "No such grant.", nil)
	}
	if g.revokedAt == nil {
		at := entitlementsAt
		g.revokedAt = &at
	}
	return reply{status: http.StatusNoContent}
}
