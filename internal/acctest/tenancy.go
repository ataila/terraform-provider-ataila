// Copyright (c) 2026 ATAILA Kft.
// SPDX-License-Identifier: MPL-2.0

package acctest

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// The tenancy endpoints of /api/v1: customers, tenants (organizations) and
// tenant memberships, with the platform's rules:
//
//   - creating a customer creates its primary tenant too (slug = gitlab_group,
//     name = long_name, the hub router as default router) and makes the
//     primary contact, when a user with that address exists, a `member` of it;
//     a token that creates something is never made its owner;
//   - customer_index is allocated as one above the highest ever used, never
//     below 2, up to 999;
//   - frozen keys (customer: customer_index, short_name, gitlab_group,
//     edition; tenant: customer_id, slug) refuse a DIFFERENT value with 422
//     immutable_field and accept the current one;
//   - DELETE of a customer archives it; archived customers keep every key and
//     cannot be changed; DELETE of a tenant succeeds only for an empty tenant
//     that is no customer's primary;
//   - lists are cursor-paged (customers by id, tenants by slug, memberships by
//     user id), page size 1-200, default 50;
//   - a customer is created `active` or, when asked, `suspended`; `archived`
//     is refused (422);
//   - every destroy refusal is a 409 with a `blockers` object;
//   - integer ids follow one rule (^[1-9][0-9]{0,8}$); anything else in a
//     path is a 404; gitlab_group follows the tenant slug rule (2-30);
//   - timestamps are RFC 3339 in UTC with a Z; a tenant's updated_at equals
//     created_at until its first change;
//   - e-mail addresses come back with the domain lower-cased;
//   - PATCH is JSON Merge Patch (application/merge-patch+json or JSON);
//   - a membership may read `developer`, a PUT accepts only the other four;
//   - a legacy tenant (AddLegacyTenant) has customer_id null.

// Routers the mock knows; hubRouterID is the default router of every new
// customer's primary tenant.
const hubRouterID = 3

var knownRouters = map[int]bool{1: true, 2: true, 3: true}

// GitLab outcomes of a customer create, and the warning each one adds.
var gitlabWarnings = map[string]string{
	"partial": "gitlab_group_not_ready",
	"skipped": "gitlab_not_configured",
	"error":   "gitlab_group_failed",
}

var (
	rxShortName   = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,15}$`)
	rxGitlabGroup = regexp.MustCompile(`^[a-z][a-z0-9-]{1,29}$`)
	rxSlug        = regexp.MustCompile(`^[a-z][a-z0-9-]{1,29}$`)
	rxIntID       = regexp.MustCompile(`^[1-9][0-9]{0,8}$`)
	// Assumed: a simplified stand-in for pydantic's EmailStr.
	rxEmail = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
)

type mockCustomer struct {
	id, index                   int
	shortName, longName, group  string
	edition, email, contactName string
	emailTier                   int
	billingTier, status         string
	notes                       *string
	primaryTenantID, createdAt  string
}

type mockTenant struct {
	id                   string
	customerID           *int
	slug, name           string
	description          *string
	defaultRouterID      *int
	createdAt, updatedAt string
	// What keeps a tenant from being deleted.
	projects, contracts, helpdeskRecords, attributedResources int
}

type mockMembership struct {
	role, createdAt string
}

type tenancyState struct {
	nextCustomerID int
	customers      map[int]*mockCustomer
	tenants        map[string]*mockTenant
	memberships    map[string]map[string]*mockMembership // tenant id -> user id -> membership
	users          map[string]string                     // user id -> e-mail
	gitlabOutcome  string
	gitlabMessage  string
}

func newTenancyState() *tenancyState {
	return &tenancyState{
		nextCustomerID: 1,
		customers:      map[int]*mockCustomer{},
		tenants:        map[string]*mockTenant{},
		memberships:    map[string]map[string]*mockMembership{},
		users:          map[string]string{},
		gitlabOutcome:  "ok",
	}
}

func now() string { return wireTime(time.Now()) }

// wireTime renders a time as the platform does: RFC 3339 in UTC with a Z,
// microseconds only when there are any.
func wireTime(t time.Time) string {
	t = t.UTC()
	s := t.Format("2006-01-02T15:04:05")
	if us := t.Nanosecond() / 1000; us != 0 {
		s += fmt.Sprintf(".%06d", us)
	}
	return s + "Z"
}

// ── test helpers: out-of-band changes, as an operator in the portal makes them

// AddUser registers a platform user by e-mail (first name = the address's
// local part) and returns its id.
func (m *MockAPI) AddUser(email string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	local := email
	if at := strings.Index(email, "@"); at > 0 {
		local = email[:at]
	}
	return m.addPerson(email, local, "", "human").id
}

// SetGitLabOutcome sets what the GitLab step of the next customer creates
// reports: "ok" (no warning), "partial", "skipped" or "error".
func (m *MockAPI) SetGitLabOutcome(outcome, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tenancy.gitlabOutcome, m.tenancy.gitlabMessage = outcome, message
}

// Customer is the wire form of a customer, as GET would answer it.
func (m *MockAPI) Customer(id string) (map[string]any, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, err := strconv.Atoi(id)
	if err != nil {
		return nil, false
	}
	c, found := m.tenancy.customers[n]
	if !found {
		return nil, false
	}
	return m.tenancy.customerWire(c, nil), true
}

// Tenant is the wire form of a tenant, as GET would answer it.
func (m *MockAPI) Tenant(id string) (map[string]any, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, found := m.tenancy.tenants[id]
	if !found {
		return nil, false
	}
	return m.tenancy.tenantWire(t), true
}

// MembershipRole is the role of a membership, or "" when there is none.
func (m *MockAPI) MembershipRole(tenantID, userID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if mm := m.tenancy.memberships[tenantID][userID]; mm != nil {
		return mm.role
	}
	return ""
}

// SetCustomerField changes a customer as the portal would, bypassing the
// API's rules: long_name, primary_contact_email, primary_contact_name,
// billing_tier, status (including "archived") or notes (nil clears).
func (m *MockAPI) SetCustomerField(id, field string, value any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, _ := strconv.Atoi(id)
	c := m.tenancy.customers[n]
	if c == nil {
		panic("SetCustomerField: no customer " + id)
	}
	switch field {
	case "long_name":
		c.longName = value.(string)
	case "primary_contact_email":
		c.email = value.(string)
	case "primary_contact_name":
		c.contactName = value.(string)
	case "billing_tier":
		c.billingTier = value.(string)
	case "status":
		c.status = value.(string)
	case "notes":
		if value == nil {
			c.notes = nil
		} else {
			s := value.(string)
			c.notes = &s
		}
	default:
		panic("SetCustomerField: unsupported field " + field)
	}
}

// SetTenantField changes a tenant as the portal would: name, description
// (nil clears) or default_router_id (an int, or nil to clear).
func (m *MockAPI) SetTenantField(id, field string, value any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.tenancy.tenants[id]
	if t == nil {
		panic("SetTenantField: no tenant " + id)
	}
	switch field {
	case "name":
		t.name = value.(string)
	case "description":
		if value == nil {
			t.description = nil
		} else {
			s := value.(string)
			t.description = &s
		}
	case "default_router_id":
		if value == nil {
			t.defaultRouterID = nil
		} else {
			n := value.(int)
			t.defaultRouterID = &n
		}
	default:
		panic("SetTenantField: unsupported field " + field)
	}
	t.updatedAt = now()
}

// AddLegacyTenant adds a tenant that no customer owns (customer_id null), as
// older platforms hold, and returns its id.
func (m *MockAPI) AddLegacyTenant(slug, name string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.tenancy.newTenant(nil, slug, name, nil, nil).id
}

// RemoveTenant deletes a tenant and its memberships out of band.
func (m *MockAPI) RemoveTenant(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.tenancy.tenants, id)
	delete(m.tenancy.memberships, id)
}

// SetMembership sets (role != "") or removes (role == "") a membership out of band.
func (m *MockAPI) SetMembership(tenantID, userID, role string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if role == "" {
		delete(m.tenancy.memberships[tenantID], userID)
		return
	}
	m.tenancy.putMembership(tenantID, userID, role)
}

// SetTenantBlockers sets what keeps a tenant from being deleted: projects
// (which also keep its customer from being archived), contracts, helpdesk
// records and attributed resources.
func (m *MockAPI) SetTenantBlockers(id string, projects, contracts, helpdesk, attributed int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.tenancy.tenants[id]
	if t == nil {
		panic("SetTenantBlockers: no tenant " + id)
	}
	t.projects, t.contracts, t.helpdeskRecords, t.attributedResources = projects, contracts, helpdesk, attributed
}

// SeedTenants adds n tenants to a customer out of band, slugs prefix-000 … and
// returns their ids.
func (m *MockAPI) SeedTenants(customerID, prefix string, n int) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	cid, _ := strconv.Atoi(customerID)
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		t := m.tenancy.newTenant(&cid, fmt.Sprintf("%s-%03d", prefix, i), fmt.Sprintf("Seeded %d", i), nil, nil)
		ids = append(ids, t.id)
	}
	return ids
}

// ── routing

func (m *MockAPI) routeTenancy(c *call) reply {
	parts := strings.Split(strings.Trim(c.path, "/"), "/")
	method := c.r.Method
	switch {
	case len(parts) == 1 && parts[0] == "customers":
		switch method {
		case http.MethodGet:
			return m.customersList(c)
		case http.MethodPost:
			return m.customersCreate(c)
		}
	case len(parts) == 2 && parts[0] == "customers":
		switch method {
		case http.MethodGet:
			return m.customersGet(c, parts[1])
		case http.MethodPatch:
			return m.customersUpdate(c, parts[1])
		case http.MethodDelete:
			return m.customersDelete(c, parts[1])
		}
	case len(parts) == 1 && parts[0] == "tenants":
		switch method {
		case http.MethodGet:
			return m.tenantsList(c)
		case http.MethodPost:
			return m.tenantsCreate(c)
		}
	case len(parts) == 2 && parts[0] == "tenants":
		switch method {
		case http.MethodGet:
			return m.tenantsGet(c, parts[1])
		case http.MethodPatch:
			return m.tenantsUpdate(c, parts[1])
		case http.MethodDelete:
			return m.tenantsDelete(c, parts[1])
		}
	case len(parts) == 3 && parts[0] == "tenants" && parts[2] == "memberships":
		if method == http.MethodGet {
			return m.membershipsList(c, parts[1])
		}
	case len(parts) == 4 && parts[0] == "tenants" && parts[2] == "memberships":
		switch method {
		case http.MethodGet:
			return m.membershipsGet(c, parts[1], parts[3])
		case http.MethodPut:
			return m.membershipsPut(c, parts[1], parts[3])
		case http.MethodDelete:
			return m.membershipsDelete(c, parts[1], parts[3])
		}
	default:
		return c.problem(http.StatusNotFound, "not_found", "", nil)
	}
	return c.methodNotAllowed()
}

// ── authorisation: READ, WRITE and DESTROY as the tenancy routers declare them

func (m *MockAPI) scopes() map[string]bool {
	out := map[string]bool{}
	switch s := m.whoami["scopes"].(type) {
	case []string:
		for _, v := range s {
			out[v] = true
		}
	case []any:
		for _, v := range s {
			out[fmt.Sprint(v)] = true
		}
	}
	return out
}

func (m *MockAPI) require(c *call, perms ...string) *reply {
	have := m.scopes()
	for _, p := range perms {
		if have[p] {
			return nil
		}
	}
	r := c.problem(http.StatusForbidden, "forbidden", "Requires one of: "+strings.Join(perms, ", "), nil)
	return &r
}

func (m *MockAPI) canRead(c *call) *reply {
	return m.require(c, "tenancy-read-global", "tenancy-admin-global")
}

func (m *MockAPI) canWrite(c *call) *reply { return m.require(c, "tenancy-admin-global") }

func (m *MockAPI) canDestroy(c *call) *reply {
	if r := m.canWrite(c); r != nil {
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

// ── validation: 422 validation_failed with an "errors" list, as FastAPI does

type validation struct {
	errs []map[string]any
}

func (v *validation) fail(field, msg string) {
	v.errs = append(v.errs, map[string]any{"loc": []any{"body", field}, "msg": msg, "type": "value_error"})
}

func (v *validation) reply(c *call) *reply {
	if len(v.errs) == 0 {
		return nil
	}
	r := c.problem(http.StatusUnprocessableEntity, "validation_failed",
		"The request does not match the schema.", map[string]any{"errors": v.errs})
	return &r
}

// decodeBody reads a JSON object and refuses members outside allowed
// (extra="forbid" on every v1 request model).
func decodeBody(c *call, v *validation, allowed ...string) map[string]any {
	body := map[string]any{}
	if err := json.Unmarshal(c.body, &body); err != nil {
		v.fail("", "Input should be a valid JSON object")
		return body
	}
	ok := map[string]bool{}
	for _, a := range allowed {
		ok[a] = true
	}
	for k := range body {
		if !ok[k] {
			v.fail(k, "Extra inputs are not permitted")
		}
	}
	return body
}

func strField(v *validation, body map[string]any, field string, required, nullable bool, check func(string) string) (*string, bool) {
	raw, present := body[field]
	if !present {
		if required {
			v.fail(field, "Field required")
		}
		return nil, false
	}
	if raw == nil {
		if !nullable {
			v.fail(field, "Input should be a valid string")
		}
		return nil, true
	}
	s, isString := raw.(string)
	if !isString {
		v.fail(field, "Input should be a valid string")
		return nil, true
	}
	if check != nil {
		if msg := check(s); msg != "" {
			v.fail(field, msg)
		}
	}
	return &s, true
}

func intField(v *validation, body map[string]any, field string, nullable bool, check func(int) string) (*int, bool) {
	raw, present := body[field]
	if !present {
		return nil, false
	}
	if raw == nil {
		if !nullable {
			v.fail(field, "Input should be a valid integer")
		}
		return nil, true
	}
	f, isNumber := raw.(float64)
	if !isNumber || f != float64(int(f)) {
		v.fail(field, "Input should be a valid integer")
		return nil, true
	}
	n := int(f)
	if check != nil {
		if msg := check(n); msg != "" {
			v.fail(field, msg)
		}
	}
	return &n, true
}

func length(min, max int) func(string) string {
	return func(s string) string {
		if n := len([]rune(s)); n < min || n > max {
			return fmt.Sprintf("String should have between %d and %d characters", min, max)
		}
		return ""
	}
}

func pattern(rx *regexp.Regexp) func(string) string {
	return func(s string) string {
		if !rx.MatchString(s) {
			return "String should match pattern '" + rx.String() + "'"
		}
		return ""
	}
}

func oneOf(values ...string) func(string) string {
	return func(s string) string {
		for _, v := range values {
			if s == v {
				return ""
			}
		}
		return "Input should be " + strings.Join(values, " or ")
	}
}

func longName(s string) string {
	if msg := length(3, 80)(s); msg != "" {
		return msg
	}
	for _, r := range s {
		if r == '"' || r == '\\' || r == '\'' || r < 0x20 || r == 0x7f {
			return "long_name must not contain quotes, backslashes or control characters"
		}
	}
	return ""
}

func email(s string) string {
	if !rxEmail.MatchString(bareAddress(s)) {
		return "value is not a valid email address"
	}
	return ""
}

var rxNamedAddress = regexp.MustCompile(`^[^<>]*<([^<>]+)>$`)

// bareAddress drops surrounding whitespace and reduces "Name <address>" to
// the address.
func bareAddress(s string) string {
	s = strings.TrimSpace(s)
	if m := rxNamedAddress.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(m[1])
	}
	return s
}

// normalizeEmail is what the platform stores and returns: the bare address,
// the local part as sent, the domain lower-cased. (The platform also converts
// an internationalised domain to its canonical form; the mock only
// lower-cases.)
func normalizeEmail(s string) string {
	s = bareAddress(s)
	at := strings.LastIndex(s, "@")
	if at < 0 {
		return s
	}
	return s[:at+1] + strings.ToLower(s[at+1:])
}

// patchMediaType accepts a PATCH body sent as JSON Merge Patch or plain JSON,
// the two media types the contract documents. Assumed: anything else is
// refused as an invalid body, as FastAPI does when it cannot read JSON.
func patchMediaType(c *call) *reply {
	mt, _, _ := mime.ParseMediaType(c.r.Header.Get("Content-Type"))
	if mt == "application/json" || mt == "application/merge-patch+json" {
		return nil
	}
	v := &validation{}
	v.fail("", "Input should be a valid dictionary or object to extract fields from")
	return v.reply(c)
}

// ── paging

type pageParams struct {
	limit int
	after map[string]any
}

func (c *call) pageParams() (pageParams, *reply) {
	p := pageParams{limit: 50}
	q := c.r.URL.Query()
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > 200 {
			r := c.problem(http.StatusUnprocessableEntity, "validation_failed", "The request does not match the schema.",
				map[string]any{"errors": []any{map[string]any{"loc": []any{"query", "limit"},
					"msg": "Input should be between 1 and 200", "type": "value_error"}}})
			return p, &r
		}
		p.limit = n
	}
	if cur := q.Get("cursor"); cur != "" {
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(cur, "="))
		if err == nil {
			err = json.Unmarshal(raw, &p.after)
		}
		if err != nil || p.after == nil {
			r := c.problem(http.StatusBadRequest, "invalid_cursor", "The cursor is not valid.", nil)
			return p, &r
		}
	}
	return p, nil
}

func cursorOf(key map[string]any) string {
	b, _ := json.Marshal(key)
	return base64.RawURLEncoding.EncodeToString(b)
}

func page[T any](items []T, limit int, key func(T) map[string]any) map[string]any {
	out := map[string]any{"items": []T{}, "next_cursor": nil}
	if len(items) > limit {
		out["items"] = items[:limit]
		out["next_cursor"] = cursorOf(key(items[limit-1]))
	} else if len(items) > 0 {
		out["items"] = items
	}
	return out
}

func invalidCursor(c *call) reply {
	return c.problem(http.StatusBadRequest, "invalid_cursor", "The cursor is not valid.", nil)
}

// ── ids: a path id that cannot name a row is a 404 of the resource

// intID applies the one integer-id rule of paths, filters and bodies:
// 1 to 999999999, no sign, no leading zero. Anything else names no row.
func intID(raw string) (int, bool) {
	if !rxIntID.MatchString(raw) {
		return 0, false
	}
	n, _ := strconv.Atoi(raw)
	return n, true
}

func uuidID(raw string) (string, bool) {
	u, err := uuid.Parse(raw)
	if err != nil {
		return "", false
	}
	return u.String(), true
}

// ── customers

func (s *tenancyState) customerWire(c *mockCustomer, warnings []map[string]any) map[string]any {
	if warnings == nil {
		warnings = []map[string]any{}
	}
	var notes any
	if c.notes != nil {
		notes = *c.notes
	}
	return map[string]any{
		"id":                    strconv.Itoa(c.id),
		"customer_index":        c.index,
		"short_name":            c.shortName,
		"long_name":             c.longName,
		"gitlab_group":          c.group,
		"edition":               c.edition,
		"primary_tenant_id":     c.primaryTenantID,
		"primary_contact_email": c.email,
		"primary_contact_name":  c.contactName,
		"default_email_tier":    c.emailTier,
		"billing_tier":          c.billingTier,
		"status":                c.status,
		"notes":                 notes,
		"created_at":            c.createdAt,
		"gitlab_status":         nil,
		"warnings":              warnings,
	}
}

func (s *tenancyState) customerProjects(id int) int {
	n := 0
	for _, t := range s.tenants {
		if t.customerID != nil && *t.customerID == id {
			n += t.projects
		}
	}
	return n
}

func (m *MockAPI) customersList(c *call) reply {
	if r := m.canRead(c); r != nil {
		return *r
	}
	p, bad := c.pageParams()
	if bad != nil {
		return *bad
	}
	q := c.r.URL.Query()
	status := q.Get("status")
	if status != "" && oneOf("active", "suspended", "archived")(status) != "" {
		return c.problem(http.StatusUnprocessableEntity, "validation_failed", "The request does not match the schema.",
			map[string]any{"errors": []any{map[string]any{"loc": []any{"query", "status"}, "msg": "invalid status", "type": "enum"}}})
	}
	after := 0
	if p.after != nil {
		f, isNumber := p.after["id"].(float64)
		if !isNumber {
			return invalidCursor(c)
		}
		after = int(f)
	}
	ids := make([]int, 0, len(m.tenancy.customers))
	for id := range m.tenancy.customers {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	var items []map[string]any
	for _, id := range ids {
		cu := m.tenancy.customers[id]
		if id <= after || (q.Has("short_name") && cu.shortName != q.Get("short_name")) ||
			(q.Has("gitlab_group") && cu.group != q.Get("gitlab_group")) || (status != "" && cu.status != status) {
			continue
		}
		items = append(items, m.tenancy.customerWire(cu, nil))
		if len(items) > p.limit {
			break
		}
	}
	return ok(http.StatusOK, page(items, p.limit, func(it map[string]any) map[string]any {
		n, _ := strconv.Atoi(it["id"].(string))
		return map[string]any{"id": n}
	}))
}

func (m *MockAPI) customersCreate(c *call) reply {
	if r := m.canWrite(c); r != nil {
		return *r
	}
	v := &validation{}
	body := decodeBody(c, v, "customer_index", "short_name", "long_name", "gitlab_group", "primary_contact_email",
		"primary_contact_name", "default_email_tier", "billing_tier", "notes", "edition", "status")
	index, _ := intField(v, body, "customer_index", true, func(n int) string {
		if n < 1 || n > 999 {
			return "Input should be between 1 and 999"
		}
		return ""
	})
	shortName, _ := strField(v, body, "short_name", true, false, pattern(rxShortName))
	long, _ := strField(v, body, "long_name", true, false, longName)
	group, _ := strField(v, body, "gitlab_group", true, false, pattern(rxGitlabGroup))
	mail, _ := strField(v, body, "primary_contact_email", true, false, email)
	contact, _ := strField(v, body, "primary_contact_name", true, false, length(2, 80))
	tier, _ := intField(v, body, "default_email_tier", false, func(n int) string {
		if n < 1 || n > 3 {
			return "Input should be 1, 2 or 3"
		}
		return ""
	})
	billing, _ := strField(v, body, "billing_tier", false, false, oneOf("INTERNAL", "PAYING"))
	notes, _ := strField(v, body, "notes", false, true, length(0, 2000))
	edition, _ := strField(v, body, "edition", false, false, oneOf("sp", "enterprise"))
	createStatus, _ := strField(v, body, "status", false, false, oneOf("active", "suspended"))
	if r := v.reply(c); r != nil {
		return *r
	}
	s := m.tenancy

	// ADR-029 A5: no customer slug may be a hyphen-prefix of another, archived ones included.
	for _, other := range s.customers {
		a, b := *group, other.group
		if a != b && (strings.HasPrefix(b, a+"-") || strings.HasPrefix(a, b+"-")) {
			return c.problem(http.StatusConflict, "gitlab_group_collision",
				fmt.Sprintf("Customer slugs collide for gitlab-access roles: '%s' and '%s'.", a, b), nil)
		}
	}
	idx := 0
	if index != nil {
		idx = *index
	} else {
		idx = 2
		for _, other := range s.customers {
			if other.index+1 > idx {
				idx = other.index + 1
			}
		}
		if idx > 999 {
			return c.problem(http.StatusConflict, "customer_index_exhausted",
				"No customer_index is free (the range ends at 999).", nil)
		}
	}
	taken := func(code string) reply {
		return c.problem(http.StatusConflict, code,
			"A customer with this key already exists (archived customers keep their keys).", nil)
	}
	for _, t := range s.tenants {
		if t.slug == *group {
			return taken("gitlab_group_taken")
		}
	}
	for _, other := range s.customers {
		switch {
		case other.index == idx:
			return taken("customer_index_taken")
		case other.shortName == *shortName:
			return taken("short_name_taken")
		case other.group == *group:
			return taken("gitlab_group_taken")
		}
	}

	cu := &mockCustomer{
		id: s.nextCustomerID, index: idx, shortName: *shortName, longName: *long, group: *group,
		edition: "sp", email: normalizeEmail(*mail), contactName: *contact, emailTier: 3,
		billingTier: "INTERNAL", status: "active", notes: notes, createdAt: now(),
	}
	s.nextCustomerID++
	if tier != nil {
		cu.emailTier = *tier
	}
	if billing != nil {
		cu.billingTier = *billing
	}
	if edition != nil {
		cu.edition = *edition
	}
	if createStatus != nil {
		cu.status = *createStatus
	}
	hub := hubRouterID
	primary := s.newTenant(&cu.id, cu.group, cu.longName, nil, &hub)
	cu.primaryTenantID = primary.id
	s.customers[cu.id] = cu
	for uid, addr := range s.users {
		if strings.EqualFold(addr, cu.email) {
			s.putMembership(primary.id, uid, "member")
		}
	}

	var warnings []map[string]any
	if code, found := gitlabWarnings[s.gitlabOutcome]; found {
		warnings = append(warnings, map[string]any{"code": code, "message": s.gitlabMessage})
	}
	return ok(http.StatusCreated, s.customerWire(cu, warnings))
}

func (m *MockAPI) loadCustomer(c *call, raw string) (*mockCustomer, *reply) {
	id, valid := intID(raw)
	cu := m.tenancy.customers[id]
	if !valid || cu == nil {
		r := c.problem(http.StatusNotFound, "customer_not_found", "No such customer.", nil)
		return nil, &r
	}
	return cu, nil
}

func (m *MockAPI) customersGet(c *call, raw string) reply {
	if r := m.canRead(c); r != nil {
		return *r
	}
	include := c.r.URL.Query().Get("include")
	if c.r.URL.Query().Has("include") && include != "gitlab_status" {
		return c.problem(http.StatusUnprocessableEntity, "validation_failed", "The request does not match the schema.",
			map[string]any{"errors": []any{map[string]any{"loc": []any{"query", "include"}, "msg": "Input should be 'gitlab_status'", "type": "literal_error"}}})
	}
	cu, bad := m.loadCustomer(c, raw)
	if bad != nil {
		return *bad
	}
	var warnings []map[string]any
	wire := m.tenancy.customerWire(cu, nil)
	if include == "gitlab_status" {
		switch m.tenancy.gitlabOutcome {
		case "ok":
			wire["gitlab_status"] = map[string]any{"exists": true, "full_path": cu.group,
				"web_url": "https://gitlab.example.com/" + cu.group}
		case "skipped":
			warnings = append(warnings, map[string]any{"code": "gitlab_not_configured", "message": m.tenancy.gitlabMessage})
		default:
			warnings = append(warnings, map[string]any{"code": "gitlab_unreachable", "message": m.tenancy.gitlabMessage})
		}
		if warnings != nil {
			wire["warnings"] = warnings
		}
	}
	return ok(http.StatusOK, wire)
}

// frozenDiffers is resource.check_frozen's comparison: both sides as strings,
// null as null.
func frozenDiffers(sent any, current string, currentNull bool) bool {
	if sent == nil {
		return !currentNull
	}
	if currentNull {
		return true
	}
	switch t := sent.(type) {
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64) != current
	default:
		return fmt.Sprint(t) != current
	}
}

func (m *MockAPI) customersUpdate(c *call, raw string) reply {
	if r := m.canWrite(c); r != nil {
		return *r
	}
	if r := patchMediaType(c); r != nil {
		return *r
	}
	v := &validation{}
	body := decodeBody(c, v, "long_name", "primary_contact_email", "primary_contact_name", "default_email_tier",
		"billing_tier", "status", "notes", "customer_index", "short_name", "gitlab_group", "edition")
	long, _ := strField(v, body, "long_name", false, false, longName)
	mail, _ := strField(v, body, "primary_contact_email", false, false, email)
	contact, _ := strField(v, body, "primary_contact_name", false, false, length(2, 80))
	tier, _ := intField(v, body, "default_email_tier", false, func(n int) string {
		if n < 1 || n > 3 {
			return "Input should be 1, 2 or 3"
		}
		return ""
	})
	billing, _ := strField(v, body, "billing_tier", false, false, oneOf("INTERNAL", "PAYING"))
	status, _ := strField(v, body, "status", false, false, oneOf("active", "suspended"))
	notes, notesSent := strField(v, body, "notes", false, true, length(0, 2000))
	intField(v, body, "customer_index", true, nil)
	strField(v, body, "short_name", false, true, nil)
	strField(v, body, "gitlab_group", false, true, nil)
	strField(v, body, "edition", false, true, oneOf("sp", "enterprise"))
	if r := v.reply(c); r != nil {
		return *r
	}
	cu, bad := m.loadCustomer(c, raw)
	if bad != nil {
		return *bad
	}
	for _, f := range []struct {
		name, current string
	}{
		{"customer_index", strconv.Itoa(cu.index)}, {"short_name", cu.shortName},
		{"gitlab_group", cu.group}, {"edition", cu.edition},
	} {
		if sent, present := body[f.name]; present && frozenDiffers(sent, f.current, false) {
			return c.problem(http.StatusUnprocessableEntity, "immutable_field",
				f.name+" cannot be changed after create.", map[string]any{"field": f.name})
		}
	}
	if cu.status == "archived" {
		return c.problem(http.StatusConflict, "customer_archived", "An archived customer cannot be changed.", nil)
	}
	if long != nil {
		cu.longName = *long
	}
	if mail != nil {
		cu.email = normalizeEmail(*mail)
	}
	if contact != nil {
		cu.contactName = *contact
	}
	if tier != nil {
		cu.emailTier = *tier
	}
	if billing != nil {
		cu.billingTier = *billing
	}
	if status != nil {
		cu.status = *status
	}
	if notesSent {
		cu.notes = notes
	}
	return ok(http.StatusOK, m.tenancy.customerWire(cu, nil))
}

func (m *MockAPI) customersDelete(c *call, raw string) reply {
	if r := m.canDestroy(c); r != nil {
		return *r
	}
	cu, bad := m.loadCustomer(c, raw)
	if bad != nil {
		return *bad
	}
	if cu.status == "archived" {
		return reply{status: http.StatusNoContent}
	}
	if n := m.tenancy.customerProjects(cu.id); n > 0 {
		return c.problem(http.StatusConflict, "customer_has_projects",
			fmt.Sprintf("The customer still has %d project(s); every project row counts, retired ones included.", n),
			map[string]any{"blockers": map[string]any{"projects": n}})
	}
	cu.status = "archived"
	return reply{status: http.StatusNoContent}
}

// ── tenants

func (s *tenancyState) newTenant(customerID *int, slug, name string, description *string, router *int) *mockTenant {
	ts := now()
	t := &mockTenant{id: uuid.NewString(), customerID: customerID, slug: slug, name: name,
		description: description, defaultRouterID: router, createdAt: ts, updatedAt: ts}
	s.tenants[t.id] = t
	return t
}

func (s *tenancyState) isPrimary(t *mockTenant) bool {
	for _, c := range s.customers {
		if c.primaryTenantID == t.id {
			return true
		}
	}
	return false
}

func (s *tenancyState) tenantWire(t *mockTenant) map[string]any {
	var customerID, description, router any
	if t.customerID != nil {
		customerID = strconv.Itoa(*t.customerID)
	}
	if t.description != nil {
		description = *t.description
	}
	if t.defaultRouterID != nil {
		router = strconv.Itoa(*t.defaultRouterID)
	}
	return map[string]any{
		"id":                t.id,
		"customer_id":       customerID,
		"slug":              t.slug,
		"name":              t.name,
		"description":       description,
		"default_router_id": router,
		"is_primary":        s.isPrimary(t),
		"project_count":     t.projects,
		"member_count":      len(s.memberships[t.id]),
		"created_at":        t.createdAt,
		"updated_at":        t.updatedAt,
	}
}

func (m *MockAPI) tenantsList(c *call) reply {
	if r := m.canRead(c); r != nil {
		return *r
	}
	q := c.r.URL.Query()
	if q.Has("customer_id") && !rxIntID.MatchString(q.Get("customer_id")) {
		return c.problem(http.StatusUnprocessableEntity, "validation_failed", "The request does not match the schema.",
			map[string]any{"errors": []any{map[string]any{"loc": []any{"query", "customer_id"},
				"msg": "String should match pattern '^[1-9][0-9]{0,8}$'", "type": "string_pattern_mismatch"}}})
	}
	p, bad := c.pageParams()
	if bad != nil {
		return *bad
	}
	after := ""
	if p.after != nil {
		s, isString := p.after["slug"].(string)
		if !isString || s == "" {
			return invalidCursor(c)
		}
		after = s
	}
	all := make([]*mockTenant, 0, len(m.tenancy.tenants))
	for _, t := range m.tenancy.tenants {
		all = append(all, t)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].slug < all[j].slug })
	var items []map[string]any
	for _, t := range all {
		if t.slug <= after && after != "" {
			continue
		}
		if q.Has("customer_id") && (t.customerID == nil || strconv.Itoa(*t.customerID) != q.Get("customer_id")) {
			continue
		}
		if q.Has("slug") && t.slug != q.Get("slug") {
			continue
		}
		items = append(items, m.tenancy.tenantWire(t))
		if len(items) > p.limit {
			break
		}
	}
	return ok(http.StatusOK, page(items, p.limit, func(it map[string]any) map[string]any {
		return map[string]any{"slug": it["slug"]}
	}))
}

func routerRefused(c *call) reply {
	return c.problem(http.StatusUnprocessableEntity, "default_router_not_found", "default_router_id names no router.",
		map[string]any{"field": "default_router_id"})
}

func (m *MockAPI) tenantsCreate(c *call) reply {
	if r := m.canWrite(c); r != nil {
		return *r
	}
	v := &validation{}
	body := decodeBody(c, v, "customer_id", "name", "slug", "description", "default_router_id")
	customerID, _ := strField(v, body, "customer_id", true, false, pattern(rxIntID))
	name, _ := strField(v, body, "name", true, false, length(2, 120))
	slug, _ := strField(v, body, "slug", true, false, pattern(rxSlug))
	description, _ := strField(v, body, "description", false, true, length(0, 2000))
	router, _ := strField(v, body, "default_router_id", false, true, pattern(rxIntID))
	if r := v.reply(c); r != nil {
		return *r
	}
	s := m.tenancy
	cid, _ := strconv.Atoi(*customerID)
	cu := s.customers[cid]
	if cu == nil {
		return c.problem(http.StatusUnprocessableEntity, "customer_not_found", "customer_id names no customer.",
			map[string]any{"field": "customer_id"})
	}
	if cu.status == "archived" {
		return c.problem(http.StatusConflict, "customer_archived", "An archived customer cannot get a new tenant.", nil)
	}
	for _, t := range s.tenants {
		if t.slug == *slug {
			return c.problem(http.StatusConflict, "tenant_slug_taken", fmt.Sprintf("The slug '%s' is taken.", *slug), nil)
		}
	}
	var routerID *int
	if router != nil {
		n, _ := strconv.Atoi(*router)
		if !knownRouters[n] {
			return routerRefused(c)
		}
		routerID = &n
	}
	t := s.newTenant(&cid, *slug, *name, description, routerID)
	return ok(http.StatusCreated, s.tenantWire(t))
}

func (m *MockAPI) loadTenant(c *call, raw string) (*mockTenant, *reply) {
	id, valid := uuidID(raw)
	t := m.tenancy.tenants[id]
	if !valid || t == nil {
		r := c.problem(http.StatusNotFound, "tenant_not_found", "No such tenant.", nil)
		return nil, &r
	}
	return t, nil
}

func (m *MockAPI) tenantsGet(c *call, raw string) reply {
	if r := m.canRead(c); r != nil {
		return *r
	}
	t, bad := m.loadTenant(c, raw)
	if bad != nil {
		return *bad
	}
	return ok(http.StatusOK, m.tenancy.tenantWire(t))
}

func (m *MockAPI) tenantsUpdate(c *call, raw string) reply {
	if r := m.canWrite(c); r != nil {
		return *r
	}
	if r := patchMediaType(c); r != nil {
		return *r
	}
	v := &validation{}
	body := decodeBody(c, v, "name", "description", "default_router_id", "customer_id", "slug")
	name, _ := strField(v, body, "name", false, false, length(2, 120))
	description, descriptionSent := strField(v, body, "description", false, true, length(0, 2000))
	router, routerSent := strField(v, body, "default_router_id", false, true, pattern(rxIntID))
	strField(v, body, "customer_id", false, true, nil)
	strField(v, body, "slug", false, true, nil)
	if r := v.reply(c); r != nil {
		return *r
	}
	t, bad := m.loadTenant(c, raw)
	if bad != nil {
		return *bad
	}
	currentCustomer := ""
	if t.customerID != nil {
		currentCustomer = strconv.Itoa(*t.customerID)
	}
	if sent, present := body["customer_id"]; present && frozenDiffers(sent, currentCustomer, t.customerID == nil) {
		return c.problem(http.StatusUnprocessableEntity, "immutable_field", "customer_id cannot be changed after create.",
			map[string]any{"field": "customer_id"})
	}
	if sent, present := body["slug"]; present && frozenDiffers(sent, t.slug, false) {
		return c.problem(http.StatusUnprocessableEntity, "immutable_field", "slug cannot be changed after create.",
			map[string]any{"field": "slug"})
	}
	changed := false
	if routerSent {
		if router == nil {
			t.defaultRouterID = nil
		} else {
			n, _ := strconv.Atoi(*router)
			if !knownRouters[n] {
				return routerRefused(c)
			}
			t.defaultRouterID = &n
		}
		changed = true
	}
	if name != nil {
		t.name = *name
		changed = true
	}
	if descriptionSent {
		t.description = description
		changed = true
	}
	if changed {
		t.updatedAt = now()
	}
	return ok(http.StatusOK, m.tenancy.tenantWire(t))
}

func (m *MockAPI) tenantsDelete(c *call, raw string) reply {
	if r := m.canDestroy(c); r != nil {
		return *r
	}
	t, bad := m.loadTenant(c, raw)
	if bad != nil {
		return *bad
	}
	if m.tenancy.isPrimary(t) {
		return c.problem(http.StatusConflict, "tenant_is_primary",
			"A customer's primary tenant cannot be deleted; archive the customer.", nil)
	}
	blockers := map[string]any{}
	codes := []struct {
		name, code string
		n          int
	}{
		{"projects", "tenant_has_projects", t.projects},
		{"contracts", "tenant_has_contracts", t.contracts},
		{"helpdesk_records", "tenant_has_helpdesk_records", t.helpdeskRecords},
		{"attributed_resources", "tenant_has_attributed_resources", t.attributedResources},
		{"ai_gateway_keys", "tenant_has_ai_gateway_keys", m.gateway.liveKeysOf(t.id)},
	}
	first := ""
	for _, b := range codes {
		if b.n > 0 {
			blockers[b.name] = b.n
			if first == "" {
				first = b.code
			}
		}
	}
	if first != "" {
		return c.problem(http.StatusConflict, first, "The tenant is not empty.", map[string]any{"blockers": blockers})
	}
	delete(m.tenancy.memberships, t.id)
	delete(m.tenancy.tenants, t.id)
	return reply{status: http.StatusNoContent}
}

// ── memberships

func (s *tenancyState) putMembership(tenantID, userID, role string) bool {
	if s.memberships[tenantID] == nil {
		s.memberships[tenantID] = map[string]*mockMembership{}
	}
	if mm := s.memberships[tenantID][userID]; mm != nil {
		mm.role = role
		return false
	}
	s.memberships[tenantID][userID] = &mockMembership{role: role, createdAt: now()}
	return true
}

func membershipWire(tenantID, userID string, mm *mockMembership) map[string]any {
	return map[string]any{"tenant_id": tenantID, "user_id": userID, "role": mm.role, "created_at": mm.createdAt}
}

func (m *MockAPI) membershipsList(c *call, rawTenant string) reply {
	if r := m.canRead(c); r != nil {
		return *r
	}
	p, bad := c.pageParams()
	if bad != nil {
		return *bad
	}
	t, bad := m.loadTenant(c, rawTenant)
	if bad != nil {
		return *bad
	}
	after := ""
	if p.after != nil {
		s, isString := p.after["user_id"].(string)
		if !isString || s == "" {
			return invalidCursor(c)
		}
		if after, isString = uuidID(s); !isString {
			return invalidCursor(c)
		}
	}
	users := make([]string, 0, len(m.tenancy.memberships[t.id]))
	for uid := range m.tenancy.memberships[t.id] {
		users = append(users, uid)
	}
	sort.Strings(users)
	var items []map[string]any
	for _, uid := range users {
		if after != "" && uid <= after {
			continue
		}
		items = append(items, membershipWire(t.id, uid, m.tenancy.memberships[t.id][uid]))
		if len(items) > p.limit {
			break
		}
	}
	return ok(http.StatusOK, page(items, p.limit, func(it map[string]any) map[string]any {
		return map[string]any{"user_id": it["user_id"]}
	}))
}

func (m *MockAPI) membershipsGet(c *call, rawTenant, rawUser string) reply {
	if r := m.canRead(c); r != nil {
		return *r
	}
	t, bad := m.loadTenant(c, rawTenant)
	if bad != nil {
		return *bad
	}
	uid, valid := uuidID(rawUser)
	mm := m.tenancy.memberships[t.id][uid]
	if !valid || mm == nil {
		return c.problem(http.StatusNotFound, "membership_not_found", "No such membership.", nil)
	}
	return ok(http.StatusOK, membershipWire(t.id, uid, mm))
}

func (m *MockAPI) membershipsPut(c *call, rawTenant, rawUser string) reply {
	if r := m.canWrite(c); r != nil {
		return *r
	}
	v := &validation{}
	body := decodeBody(c, v, "role")
	role, _ := strField(v, body, "role", true, false, oneOf("owner", "admin", "member", "viewer"))
	if r := v.reply(c); r != nil {
		return *r
	}
	t, bad := m.loadTenant(c, rawTenant)
	if bad != nil {
		return *bad
	}
	uid, valid := uuidID(rawUser)
	if _, exists := m.tenancy.users[uid]; !valid || !exists {
		return c.problem(http.StatusNotFound, "user_not_found", "No such user.", nil)
	}
	created := m.tenancy.putMembership(t.id, uid, *role)
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	return ok(status, membershipWire(t.id, uid, m.tenancy.memberships[t.id][uid]))
}

func (m *MockAPI) membershipsDelete(c *call, rawTenant, rawUser string) reply {
	if r := m.canWrite(c); r != nil {
		return *r
	}
	t, bad := m.loadTenant(c, rawTenant)
	if bad != nil {
		return *bad
	}
	uid, valid := uuidID(rawUser)
	if !valid || m.tenancy.memberships[t.id][uid] == nil {
		return c.problem(http.StatusNotFound, "membership_not_found", "No such membership.", nil)
	}
	delete(m.tenancy.memberships[t.id], uid)
	return reply{status: http.StatusNoContent}
}
