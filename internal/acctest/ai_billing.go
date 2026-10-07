// Copyright (c) 2026 ATAILA Kft.
// SPDX-License-Identifier: MPL-2.0

package acctest

import (
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Rated AI usage, the AI rate card and tenant rate plans, with the platform's
// rules (services/ai_rating):
//
//   - a rate runs from valid_from to valid_to (null: open); the rate in force
//     on a day is the one covering it, the latest valid_from winning;
//   - setting a rate from day E: a row starting on E is edited in place; a
//     later E while a change is already scheduled edits that change; the rate
//     already in force on E again changes nothing (a different note edits the
//     note in place; no note keeps it); otherwise the rate covering E is closed
//     the day before and a new one starts on E;
//   - a plan PUT is the WHOLE plan: a tier left out is removed from E (rows
//     starting on E or later deleted, the covering one closed the day before);
//     DELETE is a PUT of [] from today, answered 204;
//   - valid_from from 2020-01-01 to three years ahead (422 invalid_valid_from);
//     rates 0 to 1000000; a tier as the platform names it; unknown members 422;
//   - permissions: the card reads with ai-gateway-read/admin-global and is set
//     with ai-gateway-admin-global; a plan reads with the orders or ai-gateway
//     read keys and is set with orders-admin-global or ai-gateway-admin-global;
//     usage reads with any of the four;
//   - the usage answers are what the test put there (SetTenantAIUsage,
//     SetGatewayAIUsage), with the asked month;
//   - ServeAIBilling(false): every one of these paths is 404 not_found, as on
//     a platform before these endpoints.

var mockRxTier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// mockKnownTiers are the tiers the mock platform's pricebook knows.
var mockKnownTiers = []string{"general", "code", "embed"}

type mockRate struct {
	id           int
	tier, org    string
	in, out      float64
	from         string
	to, note     *string
	setBy, setAt string
}

type aiBillingState struct {
	card    []*mockRate
	plans   []*mockRate
	usage   map[string]map[string]any
	gateway map[string]any
	next    int
	off     bool
}

func newAIBillingState() *aiBillingState {
	return &aiBillingState{usage: map[string]map[string]any{}}
}

const mockDay = "2006-01-02"

// AIToday is the billing day the mock platform answers with (UTC).
func AIToday() string { return time.Now().UTC().Format(mockDay) }

func dayAdd(d string, n int) string {
	t, _ := time.Parse(mockDay, d)
	return t.AddDate(0, 0, n).Format(mockDay)
}

// ── test helpers ─────────────────────────────────────────────────────────────

// ServeAIBilling switches the usage and rate endpoints on (default) or off.
func (m *MockAPI) ServeAIBilling(on bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.aiBilling.off = !on
}

// SetTenantAIUsage is the answer of GET /tenants/{id}/ai-usage (its month is
// replaced by the asked one).
func (m *MockAPI) SetTenantAIUsage(tenantID string, payload map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.aiBilling.usage[tenantID] = payload
}

// SetGatewayAIUsage is the answer of GET /ai/gateway/usage.
func (m *MockAPI) SetGatewayAIUsage(payload map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.aiBilling.gateway = payload
}

// SetListRate sets a list rate out of band, as an operator on the portal's
// rate card would, from the given day.
func (m *MockAPI) SetListRate(tier string, in, out float64, from string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.aiBilling.card = m.aiBilling.setRate(m.aiBilling.card, "", tier, in, out, nil, from, AIToday(), "")
}

// SetPlanRate sets one tier of a tenant's plan out of band, from the given day.
func (m *MockAPI) SetPlanRate(tenantID, tier string, in, out float64, from string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.aiBilling.plans = m.aiBilling.setRate(m.aiBilling.plans, tenantID, tier, in, out, nil, from, AIToday(), "")
}

// ListRateInForce is the tier's list rate today as "in/out", "" without one.
func (m *MockAPI) ListRateInForce(tier string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	r := inForce(rowsOf(m.aiBilling.card, "", tier), AIToday())
	if r == nil {
		return ""
	}
	return jsonNumber(r.in) + "/" + jsonNumber(r.out)
}

// PlanInForce is the tenant's plan today: tier -> "in/out".
func (m *MockAPI) PlanInForce(tenantID string) map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]string{}
	for _, t := range tiersOf(m.aiBilling.plans, tenantID) {
		if r := inForce(rowsOf(m.aiBilling.plans, tenantID, t), AIToday()); r != nil {
			out[t] = jsonNumber(r.in) + "/" + jsonNumber(r.out)
		}
	}
	return out
}

// ── the rate rules ───────────────────────────────────────────────────────────

func rowsOf(rows []*mockRate, org, tier string) []*mockRate {
	var out []*mockRate
	for _, r := range rows {
		if r.org == org && r.tier == tier {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].from != out[j].from {
			return out[i].from < out[j].from
		}
		return out[i].id < out[j].id
	})
	return out
}

func tiersOf(rows []*mockRate, org string) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range rows {
		if r.org == org && !seen[r.tier] {
			seen[r.tier] = true
			out = append(out, r.tier)
		}
	}
	sort.Strings(out)
	return out
}

func inForce(rows []*mockRate, day string) *mockRate {
	var best *mockRate
	for _, r := range rows {
		if r.from <= day && (r.to == nil || *r.to >= day) {
			if best == nil || r.from > best.from || (r.from == best.from && r.id > best.id) {
				best = r
			}
		}
	}
	return best
}

func upcomingOf(rows []*mockRate, today string) *mockRate {
	for _, r := range rows {
		if r.from > today {
			return r
		}
	}
	return nil
}

func sameNotePtr(note *string, have *string) bool { return note == nil || sameNote(note, have) }

// setRate applies one tier's change from day e and returns the rows.
func (s *aiBillingState) setRate(rows []*mockRate, org, tier string, in, out float64, note *string,
	e, today, setBy string) []*mockRate {
	mine := rowsOf(rows, org, tier)
	for _, r := range mine {
		if r.from == e {
			if r.in == in && r.out == out && sameNotePtr(note, r.note) {
				return rows
			}
			r.in, r.out = in, out
			if note != nil {
				r.note = note
			}
			return rows
		}
	}
	var future []*mockRate
	for _, r := range mine {
		if r.from > today {
			future = append(future, r)
		}
	}
	if e > today && len(future) > 0 {
		head := future[0]
		head.from, head.to, head.in, head.out, head.note = e, nil, in, out, note
		drop := map[*mockRate]bool{}
		for _, r := range future[1:] {
			drop[r] = true
		}
		var kept []*mockRate
		for _, r := range rows {
			if !drop[r] {
				kept = append(kept, r)
			}
		}
		if cur := inForce(mine, today); cur != nil && cur != head {
			end := dayAdd(e, -1)
			cur.to = &end
		}
		return kept
	}
	prev := inForce(mine, e)
	if prev != nil && prev.in == in && prev.out == out {
		if !sameNotePtr(note, prev.note) {
			prev.note = note
		}
		return rows
	}
	if prev != nil {
		end := dayAdd(e, -1)
		prev.to = &end
	}
	var to *string
	for _, r := range mine {
		if r.from > e {
			end := dayAdd(r.from, -1)
			to = &end
			break
		}
	}
	s.next++
	return append(rows, &mockRate{id: s.next, tier: tier, org: org, in: in, out: out, from: e, to: to,
		note: note, setBy: setBy, setAt: now()})
}

// removeRate takes a tier off a plan from day e.
func removeRate(rows []*mockRate, org, tier, e string) []*mockRate {
	var kept []*mockRate
	for _, r := range rows {
		if r.org == org && r.tier == tier {
			if r.from >= e {
				continue
			}
			if r.to == nil || *r.to >= e {
				end := dayAdd(e, -1)
				r.to = &end
			}
		}
		kept = append(kept, r)
	}
	return kept
}

func rateWire(r *mockRate) map[string]any {
	if r == nil {
		return nil
	}
	var to, note, setBy any
	if r.to != nil {
		to = *r.to
	}
	if r.note != nil {
		note = *r.note
	}
	if r.setBy != "" {
		setBy = r.setBy
	}
	return map[string]any{"tier": r.tier, "eur_per_1m_input": r.in, "eur_per_1m_output": r.out,
		"valid_from": r.from, "valid_to": to, "note": note, "set_by": setBy, "set_at": r.setAt}
}

func historyWire(rows []*mockRate) []any {
	sorted := append([]*mockRate{}, rows...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].from != sorted[j].from {
			return sorted[i].from > sorted[j].from
		}
		return sorted[i].id > sorted[j].id
	})
	out := []any{}
	for _, r := range sorted {
		out = append(out, rateWire(r))
	}
	return out
}

func (s *aiBillingState) cardTier(tier, today string) map[string]any {
	mine := rowsOf(s.card, "", tier)
	return map[string]any{"tier": tier, "current": rateWire(inForce(mine, today)),
		"upcoming": rateWire(upcomingOf(mine, today)), "seedable": tier != "embed"}
}

// ── routing ──────────────────────────────────────────────────────────────────

// routeAIBilling answers the usage and rate paths, and reports whether the
// path was one of them.
func (m *MockAPI) routeAIBilling(c *call) (reply, bool) {
	parts := strings.Split(strings.Trim(c.path, "/"), "/")
	switch {
	case len(parts) == 3 && parts[0] == "tenants" && (parts[2] == "ai-usage" || parts[2] == "ai-rate-plan"):
	case c.path == "/ai/rate-card", len(parts) == 3 && parts[0] == "ai" && parts[1] == "rate-card":
	case c.path == "/ai/gateway/usage":
	default:
		return reply{}, false
	}
	if m.aiBilling.off {
		return c.problem(http.StatusNotFound, "not_found", "", nil), true
	}
	method := c.r.Method
	switch {
	case parts[0] == "tenants" && parts[2] == "ai-usage" && method == http.MethodGet:
		return m.tenantAIUsage(c, parts[1]), true
	case c.path == "/ai/gateway/usage" && method == http.MethodGet:
		return m.gatewayAIUsage(c), true
	case c.path == "/ai/rate-card" && method == http.MethodGet:
		return m.rateCardGet(c), true
	case len(parts) == 3 && parts[1] == "rate-card" && method == http.MethodPut:
		return m.rateCardPut(c, parts[2]), true
	case parts[0] == "tenants" && parts[2] == "ai-rate-plan":
		switch method {
		case http.MethodGet:
			return m.ratePlanGet(c, parts[1]), true
		case http.MethodPut:
			return m.ratePlanPut(c, parts[1]), true
		case http.MethodDelete:
			return m.ratePlanDelete(c, parts[1]), true
		}
	}
	return c.methodNotAllowed(), true
}

var mockRxMonth = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])$`)

func (c *call) month() (string, *reply) {
	month := c.r.URL.Query().Get("month")
	if month != "" && !mockRxMonth.MatchString(month) {
		r := c.problem(http.StatusUnprocessableEntity, "validation_failed", "The request does not match the schema.",
			map[string]any{"errors": []any{map[string]any{"loc": []any{"query", "month"},
				"msg": "String should match pattern", "type": "string_pattern_mismatch"}}})
		return "", &r
	}
	if month == "" {
		month = AIToday()[:7]
	}
	return month, nil
}

func (m *MockAPI) usageRead(c *call) *reply {
	return m.require(c, "ai-gateway-read-global", "ai-gateway-admin-global", "orders-read-global",
		"orders-admin-global")
}

func (m *MockAPI) tenantAIUsage(c *call, raw string) reply {
	if r := m.usageRead(c); r != nil {
		return *r
	}
	month, bad := c.month()
	if bad != nil {
		return *bad
	}
	t, badTenant := m.loadTenant(c, raw)
	if badTenant != nil {
		return *badTenant
	}
	out := map[string]any{}
	for k, v := range m.aiBilling.usage[t.id] {
		out[k] = v
	}
	out["tenant_id"], out["month"], out["tz"] = t.id, month, "Europe/Budapest"
	if _, ok := out["tenant_name"]; !ok {
		out["tenant_name"] = t.name
	}
	return ok(http.StatusOK, out)
}

func (m *MockAPI) gatewayAIUsage(c *call) reply {
	if r := m.usageRead(c); r != nil {
		return *r
	}
	month, bad := c.month()
	if bad != nil {
		return *bad
	}
	out := map[string]any{"tenants": []any{}, "unattributed": nil}
	for k, v := range m.aiBilling.gateway {
		out[k] = v
	}
	out["month"], out["tz"] = month, "Europe/Budapest"
	return ok(http.StatusOK, out)
}

func (m *MockAPI) rateCardGet(c *call) reply {
	if r := m.require(c, "ai-gateway-read-global", "ai-gateway-admin-global"); r != nil {
		return *r
	}
	today := AIToday()
	seen := map[string]bool{}
	var tiers []string
	for _, t := range append(append([]string{}, mockKnownTiers...), tiersOf(m.aiBilling.card, "")...) {
		if !seen[t] {
			seen[t] = true
			tiers = append(tiers, t)
		}
	}
	sort.Strings(tiers)
	list := []any{}
	for _, t := range tiers {
		list = append(list, m.aiBilling.cardTier(t, today))
	}
	return ok(http.StatusOK, map[string]any{"as_of": today, "currency": "EUR", "unit": "1M tokens",
		"tiers": list, "history": historyWire(m.aiBilling.card)})
}

// rateBody validates a rate's members as the platform's model does.
func rateBody(v *validation, e map[string]any) (in, out float64, note *string) {
	for _, f := range []string{"eur_per_1m_input", "eur_per_1m_output"} {
		n, isNum := e[f].(float64)
		if !isNum || n < 0 || n > 1000000 {
			v.fail(f, "Input should be between 0 and 1000000")
		}
		if f == "eur_per_1m_input" {
			in = n
		} else {
			out = n
		}
	}
	if n, present := e["note"]; present && n != nil {
		s, isStr := n.(string)
		if !isStr || len([]rune(s)) > 500 {
			v.fail("note", "String should have at most 500 characters")
		}
		note = &s
	}
	return in, out, note
}

// validFrom is the body's valid_from or today, and its bound check.
func validFrom(c *call, body map[string]any, today string) (string, *reply) {
	e := today
	if raw, present := body["valid_from"]; present && raw != nil {
		s, _ := raw.(string)
		if _, err := time.Parse(mockDay, s); err != nil {
			r := c.problem(http.StatusUnprocessableEntity, "validation_failed", "The request does not match the schema.", nil)
			return "", &r
		}
		e = s
	}
	if e < "2020-01-01" || e > dayAdd(today, 3*366) {
		r := c.problem(http.StatusUnprocessableEntity, "invalid_valid_from", "valid_from is out of range.", nil)
		return "", &r
	}
	return e, nil
}

func (m *MockAPI) rateCardPut(c *call, tier string) reply {
	if r := m.require(c, "ai-gateway-admin-global"); r != nil {
		return *r
	}
	v := &validation{}
	if !mockRxTier.MatchString(tier) {
		v.fail("tier", "String should match pattern")
	}
	body := decodeBody(c, v, "eur_per_1m_input", "eur_per_1m_output", "valid_from", "note")
	in, out, note := rateBody(v, body)
	if r := v.reply(c); r != nil {
		return *r
	}
	today := AIToday()
	e, bad := validFrom(c, body, today)
	if bad != nil {
		return *bad
	}
	before := len(historyWire(rowsOf(m.aiBilling.card, "", tier)))
	snapshot := historyWire(rowsOf(m.aiBilling.card, "", tier))
	m.aiBilling.card = m.aiBilling.setRate(m.aiBilling.card, "", tier, in, out, note, e, today, m.principalID())
	after := historyWire(rowsOf(m.aiBilling.card, "", tier))
	changed := before != len(after) || jsonString(snapshot) != jsonString(after)
	w := m.aiBilling.cardTier(tier, today)
	w["as_of"], w["changed"], w["history"], w["warnings"] = today, changed, after, []any{}
	return ok(http.StatusOK, w)
}

func (m *MockAPI) planWire(t *mockTenant, changed any) map[string]any {
	today := AIToday()
	org := t.id
	rates, upcoming := []any{}, []any{}
	for _, tier := range tiersOf(m.aiBilling.plans, org) {
		mine := rowsOf(m.aiBilling.plans, org, tier)
		if r := inForce(mine, today); r != nil {
			rates = append(rates, rateWire(r))
		}
		if r := upcomingOf(mine, today); r != nil {
			upcoming = append(upcoming, rateWire(r))
		}
	}
	seen := map[string]bool{}
	var tiers []string
	for _, x := range append(tiersOf(m.aiBilling.card, ""), tiersOf(m.aiBilling.plans, org)...) {
		if !seen[x] {
			seen[x] = true
			tiers = append(tiers, x)
		}
	}
	sort.Strings(tiers)
	effective := []any{}
	for _, tier := range tiers {
		lst := inForce(rowsOf(m.aiBilling.card, "", tier), today)
		own := inForce(rowsOf(m.aiBilling.plans, org, tier), today)
		e := map[string]any{"tier": tier, "basis": "none", "eur_per_1m_input": nil, "eur_per_1m_output": nil,
			"valid_from": nil, "list_eur_per_1m_input": nil, "list_eur_per_1m_output": nil}
		if lst != nil {
			e["list_eur_per_1m_input"], e["list_eur_per_1m_output"] = lst.in, lst.out
		}
		pick, basis := own, "plan"
		if pick == nil {
			pick, basis = lst, "list"
		}
		if pick != nil {
			e["basis"], e["eur_per_1m_input"], e["eur_per_1m_output"], e["valid_from"] = basis, pick.in, pick.out, pick.from
		}
		effective = append(effective, e)
	}
	var history []*mockRate
	for _, r := range m.aiBilling.plans {
		if r.org == org {
			history = append(history, r)
		}
	}
	return map[string]any{"tenant_id": org, "tenant_name": t.name, "as_of": today, "currency": "EUR",
		"unit": "1M tokens", "contract_included": false, "contract": nil, "rates": rates,
		"upcoming": upcoming, "effective": effective, "history": historyWire(history), "changed": changed,
		"warnings": []any{}}
}

func (m *MockAPI) ratePlanGet(c *call, raw string) reply {
	if r := m.require(c, "orders-read-global", "orders-admin-global", "ai-gateway-read-global",
		"ai-gateway-admin-global"); r != nil {
		return *r
	}
	t, bad := m.loadTenant(c, raw)
	if bad != nil {
		return *bad
	}
	return ok(http.StatusOK, m.planWire(t, nil))
}

type planEntry struct {
	tier    string
	in, out float64
	note    *string
}

func (m *MockAPI) replacePlan(t *mockTenant, entries []planEntry, e string) bool {
	org, today := t.id, AIToday()
	var mine []*mockRate
	for _, r := range m.aiBilling.plans {
		if r.org == org {
			mine = append(mine, r)
		}
	}
	snapshot := jsonString(historyWire(mine))
	wanted := map[string]bool{}
	for _, p := range entries {
		wanted[p.tier] = true
		m.aiBilling.plans = m.aiBilling.setRate(m.aiBilling.plans, org, p.tier, p.in, p.out, p.note, e, today,
			m.principalID())
	}
	for _, tier := range tiersOf(m.aiBilling.plans, org) {
		if !wanted[tier] {
			m.aiBilling.plans = removeRate(m.aiBilling.plans, org, tier, e)
		}
	}
	var after []*mockRate
	for _, r := range m.aiBilling.plans {
		if r.org == org {
			after = append(after, r)
		}
	}
	return snapshot != jsonString(historyWire(after))
}

func (m *MockAPI) ratePlanPut(c *call, raw string) reply {
	if r := m.require(c, "orders-admin-global", "ai-gateway-admin-global"); r != nil {
		return *r
	}
	v := &validation{}
	body := decodeBody(c, v, "rates", "valid_from")
	list, isList := body["rates"].([]any)
	if !isList {
		v.fail("rates", "Field required")
	}
	var entries []planEntry
	seen := map[string]bool{}
	for _, item := range list {
		e, isObj := item.(map[string]any)
		if !isObj {
			v.fail("rates", "Input should be a valid object")
			continue
		}
		for k := range e {
			if k != "tier" && k != "eur_per_1m_input" && k != "eur_per_1m_output" && k != "note" {
				v.fail(k, "Extra inputs are not permitted")
			}
		}
		tier, _ := e["tier"].(string)
		if !mockRxTier.MatchString(tier) {
			v.fail("tier", "String should match pattern")
		}
		if seen[tier] {
			v.fail("rates", "Value error, a tier appears twice in the plan")
		}
		seen[tier] = true
		in, out, note := rateBody(v, e)
		entries = append(entries, planEntry{tier: tier, in: in, out: out, note: note})
	}
	if r := v.reply(c); r != nil {
		return *r
	}
	t, bad := m.loadTenant(c, raw)
	if bad != nil {
		return *bad
	}
	e, badDay := validFrom(c, body, AIToday())
	if badDay != nil {
		return *badDay
	}
	changed := m.replacePlan(t, entries, e)
	return ok(http.StatusOK, m.planWire(t, changed))
}

func (m *MockAPI) ratePlanDelete(c *call, raw string) reply {
	if r := m.require(c, "orders-admin-global", "ai-gateway-admin-global"); r != nil {
		return *r
	}
	t, bad := m.loadTenant(c, raw)
	if bad != nil {
		return *bad
	}
	m.replacePlan(t, nil, AIToday())
	return reply{status: http.StatusNoContent}
}

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
