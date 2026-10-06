// Copyright (c) 2026 ATAILA Kft.
// SPDX-License-Identifier: MPL-2.0

package acctest

import (
	"encoding/json"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Tenant quotas, the order catalogue and tenant orders, with the platform's
// rules:
//
//   - GET and PUT /tenants/{id}/quotas need orders-read-global or
//     orders-admin-global (PUT: orders-admin-global); a tenant that does not
//     exist (or a path id that is not a UUID) is 404 tenant_not_found;
//   - a PUT replaces the WHOLE set: a dimension left out loses its limit; a
//     limit is kept to four decimal places; a limit whose value, policy and
//     note are unchanged keeps its set_by and set_at; a changed one is set by
//     the token's principal;
//   - the body is validated as the platform's model: known dimension and
//     policy, 0 <= limit <= 9999999999, a note up to 500 characters, each
//     dimension once, no other member (422 validation_failed);
//   - `usage` lists every dimension in the platform's order with its limit,
//     what is allocated (SetTenantAllocation; 0 unless set), reserved and
//     remaining;
//   - GET /catalogue lists the enabled items by sort_order, then key, paged;
//   - GET /tenants/{id}/orders lists one tenant's orders newest first, paged,
//     `status` repeatable; GET /orders/{id} adds the timeline (`events`);
//   - orders have no write at all (the portal approves them);
//   - ServeQuotasOrders(false) makes all of it answer 404 not_found, as a
//     platform older than these endpoints does.

var mockQuotaDimensions = []string{"vcpu", "ram_gb", "disk_gb", "desktops", "vms", "ai_tpm",
	"ai_budget_eur_month", "gpu"}

var mockQuotaPolicies = map[string]bool{"auto": true, "approve_always": true, "hard_cap": true}

var mockOrderStatuses = map[string]bool{"submitted": true, "auto_approved": true, "awaiting_approval": true,
	"approved": true, "rejected": true, "dispatched": true, "running": true, "delivered": true, "partial": true,
	"failed": true, "cancelled": true}

type mockQuota struct {
	limit        float64
	policy       string
	note         *string
	setBy, setAt string
}

// MockOrder is an order the test puts on the platform (as a tenant's order
// in the portal would).
type MockOrder struct {
	ID       string
	Item     string         // catalogue item key; default "ai-gateway-key"
	Status   string         // default "delivered"
	Spec     map[string]any // default an AI key spec
	Dispatch map[string]any // default {}
	Events   []map[string]any
	at       time.Time
	tenantID string
}

type ordersState struct {
	quotas    map[string]map[string]*mockQuota // tenant -> dimension -> limit
	allocated map[string]map[string]any        // tenant -> dimension -> number or nil
	notes     map[string][]string
	items     []map[string]any
	orders    map[string]*MockOrder
	seq       int
	off       bool
}

func newOrdersState() *ordersState {
	return &ordersState{
		quotas:    map[string]map[string]*mockQuota{},
		allocated: map[string]map[string]any{},
		notes:     map[string][]string{},
		orders:    map[string]*MockOrder{},
		items: []map[string]any{
			catalogueItem("vdi-desktop", "vdi-desktop", "Developer desktop", 10,
				map[string]any{"unit_keys": map[string]any{"vcpu_month": "vcpu"}}),
			catalogueItem("project-vm", "project-vm", "Virtual machine in a project", 20,
				map[string]any{"unit_keys": map[string]any{"vcpu_month": "vcpu"}}),
			catalogueItem("ai-gateway-key", "ai-gateway-key", "AI gateway key", 30, nil),
		},
	}
}

func catalogueItem(key, kind, name string, sortOrder int, price map[string]any) map[string]any {
	var hint any
	if price != nil {
		hint = price
	}
	return map[string]any{
		"key": key, "kind": kind, "name": name, "edition": "sp", "requires_approval": false,
		"spec_schema": map[string]any{"type": "object", "required": []any{"env"},
			"properties": map[string]any{"env": map[string]any{"type": "string", "enum": []any{"dev", "uat", "prod"}}}},
		"quota_dimensions": map[string]any{"ai_tpm": map[string]any{"field": "tpm"}},
		"price_hint":       hint, "sort_order": sortOrder, "enabled": true,
	}
}

// ── test helpers ─────────────────────────────────────────────────────────────

// ServeQuotasOrders switches the quota, catalogue and order endpoints on (the
// default) or off (404, as an older platform answers).
func (m *MockAPI) ServeQuotasOrders(on bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.orders.off = !on
}

// SetTenantQuota sets one limit out of band, as an operator on the portal's
// quotas page would.
func (m *MockAPI) SetTenantQuota(tenantID, dimension string, limit float64, policy string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.orders.quotas[tenantID] == nil {
		m.orders.quotas[tenantID] = map[string]*mockQuota{}
	}
	m.orders.quotas[tenantID][dimension] = &mockQuota{limit: limit, policy: policy, setAt: now()}
}

// TenantQuotaSet is the tenant's stored set: dimension -> "limit/policy".
func (m *MockAPI) TenantQuotaSet(tenantID string) map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]string{}
	for d, q := range m.orders.quotas[tenantID] {
		out[d] = jsonNumber(q.limit) + "/" + q.policy
	}
	return out
}

// SetTenantAllocation sets what the tenant holds of a dimension; nil: the
// platform cannot count it (and a note says so).
func (m *MockAPI) SetTenantAllocation(tenantID, dimension string, value *float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.orders.allocated[tenantID] == nil {
		m.orders.allocated[tenantID] = map[string]any{}
	}
	if value == nil {
		m.orders.allocated[tenantID][dimension] = nil
		m.orders.notes[tenantID] = append(m.orders.notes[tenantID],
			"The "+dimension+" allocation cannot be counted.")
		return
	}
	m.orders.allocated[tenantID][dimension] = *value
}

// AddOrder puts an order of the tenant on the platform and returns its id.
// Each order is a minute newer than the one added before it.
func (m *MockAPI) AddOrder(tenantID string, o MockOrder) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.orders.seq++
	if o.ID == "" {
		o.ID = uuid.NewString()
	}
	if o.Item == "" {
		o.Item = "ai-gateway-key"
	}
	if o.Status == "" {
		o.Status = "delivered"
	}
	if o.Spec == nil {
		o.Spec = map[string]any{"env": "dev", "app": "chat", "models": []any{"general"}, "tpm": 50000}
	}
	if o.Dispatch == nil {
		o.Dispatch = map[string]any{}
	}
	o.tenantID = tenantID
	o.at = time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC).Add(time.Duration(m.orders.seq) * time.Minute)
	if o.Events == nil {
		o.Events = []map[string]any{
			{"id": "1", "at": wireTime(o.at), "actor": "owner@example.com", "event": "submitted", "detail": map[string]any{}},
			{"id": "2", "at": wireTime(o.at), "actor": "system:quota-engine", "event": "auto_approved",
				"detail": map[string]any{"result": "within"}},
		}
	}
	m.orders.orders[o.ID] = &o
	return o.ID
}

func jsonNumber(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

// ── routing ──────────────────────────────────────────────────────────────────

// routeOrders answers the quota, catalogue and order paths, and reports
// whether the path was one of them.
func (m *MockAPI) routeOrders(c *call) (reply, bool) {
	parts := strings.Split(strings.Trim(c.path, "/"), "/")
	switch {
	case len(parts) == 3 && parts[0] == "tenants" && (parts[2] == "quotas" || parts[2] == "orders"):
	case len(parts) == 1 && parts[0] == "catalogue":
	case len(parts) == 2 && parts[0] == "orders":
	default:
		return reply{}, false
	}
	if m.orders.off {
		return c.problem(http.StatusNotFound, "not_found", "", nil), true
	}
	method := c.r.Method
	switch {
	case parts[0] == "tenants" && parts[2] == "quotas" && method == http.MethodGet:
		return m.quotasGet(c, parts[1]), true
	case parts[0] == "tenants" && parts[2] == "quotas" && method == http.MethodPut:
		return m.quotasPut(c, parts[1]), true
	case parts[0] == "tenants" && parts[2] == "orders" && method == http.MethodGet:
		return m.tenantOrdersList(c, parts[1]), true
	case parts[0] == "catalogue" && method == http.MethodGet:
		return m.catalogueList(c), true
	case parts[0] == "orders" && method == http.MethodGet:
		return m.orderGet(c, parts[1]), true
	}
	return c.methodNotAllowed(), true
}

func (m *MockAPI) canReadOrders(c *call) *reply {
	return m.require(c, "orders-read-global", "orders-admin-global")
}

// ── quotas ───────────────────────────────────────────────────────────────────

func (m *MockAPI) quotasWire(t *mockTenant) map[string]any {
	set := m.orders.quotas[t.id]
	dims := make([]string, 0, len(set))
	for d := range set {
		dims = append(dims, d)
	}
	sort.Strings(dims)
	quotas := []any{}
	for _, d := range dims {
		q := set[d]
		var note, setBy any
		if q.note != nil {
			note = *q.note
		}
		if q.setBy != "" {
			setBy = q.setBy
		}
		quotas = append(quotas, map[string]any{"dimension": d, "limit": q.limit, "policy": q.policy,
			"note": note, "set_by": setBy, "set_at": q.setAt})
	}
	usage := []any{}
	for _, d := range mockQuotaDimensions {
		var alloc any = 0.0
		if a, found := m.orders.allocated[t.id][d]; found {
			alloc = a
		}
		row := map[string]any{"dimension": d, "limit": nil, "policy": nil, "allocated": alloc,
			"reserved": 0.0, "remaining": nil}
		if q, found := set[d]; found {
			row["limit"], row["policy"] = q.limit, q.policy
			if a, isNum := alloc.(float64); isNum {
				row["remaining"] = math.Round((q.limit-a)*10000) / 10000
			}
		}
		usage = append(usage, row)
	}
	notes := append([]string{}, m.orders.notes[t.id]...)
	return map[string]any{"tenant_id": t.id, "tenant_name": t.name, "quotas": quotas, "usage": usage,
		"notes": notes, "warnings": []any{}}
}

func (m *MockAPI) quotasGet(c *call, raw string) reply {
	if r := m.canReadOrders(c); r != nil {
		return *r
	}
	t, bad := m.loadTenant(c, raw)
	if bad != nil {
		return *bad
	}
	return ok(http.StatusOK, m.quotasWire(t))
}

type quotaEntry struct {
	dim, policy string
	limit       float64
	note        *string
}

// quotaEntries validates a PUT body as the platform's model does.
func quotaEntries(c *call) ([]quotaEntry, *reply) {
	v := &validation{}
	body := decodeBody(c, v, "quotas")
	list, isList := body["quotas"].([]any)
	if _, present := body["quotas"]; !present {
		v.fail("quotas", "Field required")
	} else if !isList {
		v.fail("quotas", "Input should be a valid list")
	}
	var entries []quotaEntry
	seen := map[string]bool{}
	for _, item := range list {
		e, isObj := item.(map[string]any)
		if !isObj {
			v.fail("quotas", "Input should be a valid object")
			continue
		}
		for k := range e {
			if k != "dimension" && k != "limit" && k != "policy" && k != "note" {
				v.fail(k, "Extra inputs are not permitted")
			}
		}
		dim, _ := e["dimension"].(string)
		known := false
		for _, d := range mockQuotaDimensions {
			known = known || d == dim
		}
		if !known {
			v.fail("dimension", "Input should be one of the quota dimensions")
		}
		if seen[dim] {
			v.fail("quotas", "Value error, each dimension may appear once")
		}
		seen[dim] = true
		limit, isNum := e["limit"].(float64)
		if !isNum || limit < 0 || limit > 9999999999 {
			v.fail("limit", "Input should be between 0 and 9999999999")
		}
		policy := "auto"
		if p, present := e["policy"]; present {
			ps, _ := p.(string)
			if !mockQuotaPolicies[ps] {
				v.fail("policy", "Input should be 'auto', 'approve_always' or 'hard_cap'")
			}
			policy = ps
		}
		var note *string
		if n, isStr := e["note"].(string); isStr {
			if len([]rune(n)) > 500 {
				v.fail("note", "String should have at most 500 characters")
			}
			note = &n
		}
		entries = append(entries, quotaEntry{dim: dim, policy: policy, limit: limit, note: note})
	}
	return entries, v.reply(c)
}

func (m *MockAPI) quotasPut(c *call, raw string) reply {
	if r := m.require(c, "orders-admin-global"); r != nil {
		return *r
	}
	entries, bad := quotaEntries(c)
	if bad != nil {
		return *bad
	}
	t, badTenant := m.loadTenant(c, raw)
	if badTenant != nil {
		return *badTenant
	}
	principal, _ := m.whoami["principal"].(map[string]any)
	setBy, _ := principal["id"].(string)
	before := m.orders.quotas[t.id]
	after := map[string]*mockQuota{}
	for _, e := range entries {
		limit := math.Round(e.limit*10000) / 10000
		old := before[e.dim]
		if old != nil && old.limit == limit && old.policy == e.policy && sameNote(old.note, e.note) {
			after[e.dim] = old
			continue
		}
		after[e.dim] = &mockQuota{limit: limit, policy: e.policy, note: e.note, setBy: setBy, setAt: now()}
	}
	m.orders.quotas[t.id] = after
	return ok(http.StatusOK, m.quotasWire(t))
}

func sameNote(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// ── the catalogue ────────────────────────────────────────────────────────────

func (m *MockAPI) catalogueList(c *call) reply {
	if r := m.canReadOrders(c); r != nil {
		return *r
	}
	p, bad := c.pageParams()
	if bad != nil {
		return *bad
	}
	var items []map[string]any
	for _, it := range m.orders.items {
		if it["enabled"] == true {
			wire := map[string]any{}
			for k, v := range it {
				if k != "enabled" {
					wire[k] = v
				}
			}
			items = append(items, wire)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i]["sort_order"].(int), items[j]["sort_order"].(int)
		if a != b {
			return a < b
		}
		return items[i]["key"].(string) < items[j]["key"].(string)
	})
	if p.after != nil {
		key, _ := p.after["key"].(string)
		sortAfter, _ := p.after["sort_order"].(float64)
		var rest []map[string]any
		for _, it := range items {
			s := float64(it["sort_order"].(int))
			if s > sortAfter || (s == sortAfter && it["key"].(string) > key) {
				rest = append(rest, it)
			}
		}
		items = rest
	}
	return ok(http.StatusOK, page(items, p.limit, func(it map[string]any) map[string]any {
		return map[string]any{"sort_order": it["sort_order"], "key": it["key"]}
	}))
}

// ── orders ───────────────────────────────────────────────────────────────────

func (m *MockAPI) orderWire(o *MockOrder, withEvents bool) map[string]any {
	name := ""
	if t := m.tenancy.tenants[o.tenantID]; t != nil {
		name = t.name
	}
	kinds := map[string][2]string{"ai-gateway-key": {"ai-gateway-key", "AI gateway key"},
		"vdi-desktop": {"vdi-desktop", "Developer desktop"}, "project-vm": {"project-vm", "Virtual machine in a project"}}
	k := kinds[o.Item]
	w := map[string]any{
		"id": o.ID, "tenant_id": o.tenantID, "tenant_name": name, "customer_id": "1", "project_id": nil,
		"catalogue_item_key": o.Item, "catalogue_item_kind": k[0], "catalogue_item_name": k[1],
		"spec": o.Spec, "status": o.Status,
		"quota_check": map[string]any{"result": "within", "decision": "auto_approved", "reasons": []any{},
			"over_dimensions": []any{}, "refused_dimensions": []any{},
			"dimensions": []any{map[string]any{"dimension": "ai_tpm", "limit": 200000.0, "policy": "auto",
				"allocated": 0.0, "reserved": 0.0, "requested": 50000.0, "total": 50000.0,
				"remaining": 150000.0, "result": "within", "forces_card": false, "reason": nil}}},
		"overage": nil, "requested_by": "00000000-0000-4000-8000-0000000000b1",
		"approved_by": nil, "approved_at": nil, "approval_reason": nil,
		"rejected_by": nil, "rejected_at": nil, "rejection_reason": nil,
		"dispatch": o.Dispatch, "operation_id": "order:" + o.ID,
		"created_at": wireTime(o.at), "updated_at": wireTime(o.at.Add(time.Minute)),
	}
	if withEvents {
		events := make([]any, 0, len(o.Events))
		for _, e := range o.Events {
			events = append(events, e)
		}
		w["events"] = events
	}
	return w
}

func (m *MockAPI) tenantOrdersList(c *call, raw string) reply {
	if r := m.canReadOrders(c); r != nil {
		return *r
	}
	p, bad := c.pageParams()
	if bad != nil {
		return *bad
	}
	statuses := c.r.URL.Query()["status"]
	for _, s := range statuses {
		if !mockOrderStatuses[s] {
			return c.problem(http.StatusUnprocessableEntity, "validation_failed", "The request does not match the schema.",
				map[string]any{"errors": []any{map[string]any{"loc": []any{"query", "status"},
					"msg": "Input should be an order status", "type": "literal_error"}}})
		}
	}
	t, badTenant := m.loadTenant(c, raw)
	if badTenant != nil {
		return *badTenant
	}
	var list []*MockOrder
	for _, o := range m.orders.orders {
		if o.tenantID != t.id {
			continue
		}
		match := len(statuses) == 0
		for _, s := range statuses {
			match = match || s == o.Status
		}
		if match {
			list = append(list, o)
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if !list[i].at.Equal(list[j].at) {
			return list[i].at.After(list[j].at)
		}
		return list[i].ID > list[j].ID
	})
	if p.after != nil {
		at, _ := p.after["created_at"].(string)
		id, _ := p.after["id"].(string)
		cut, err := time.Parse(time.RFC3339Nano, at)
		if err != nil || id == "" {
			return invalidCursor(c)
		}
		var rest []*MockOrder
		for _, o := range list {
			if o.at.Before(cut) || (o.at.Equal(cut) && o.ID < id) {
				rest = append(rest, o)
			}
		}
		list = rest
	}
	wires := make([]map[string]any, 0, len(list))
	for _, o := range list {
		wires = append(wires, m.orderWire(o, false))
	}
	return ok(http.StatusOK, page(wires, p.limit, func(w map[string]any) map[string]any {
		return map[string]any{"created_at": w["created_at"], "id": w["id"]}
	}))
}

func (m *MockAPI) orderGet(c *call, raw string) reply {
	if r := m.canReadOrders(c); r != nil {
		return *r
	}
	id, valid := uuidID(raw)
	o := m.orders.orders[id]
	if !valid || o == nil {
		return c.problem(http.StatusNotFound, "order_not_found", "No such order.", nil)
	}
	return ok(http.StatusOK, m.orderWire(o, true))
}
