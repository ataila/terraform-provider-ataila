// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package acctest

import (
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// Service accounts (platform 1.0.397): /service-accounts[/{id}] over the same
// people as /users (kind "service"), as the platform's facade is over the
// same rows. Assumed from routers/service_accounts.py:
//   - keys service-accounts-read-global / -admin-global; DELETE and PATCH
//     is_active false are destroy-gated by the token's flag;
//   - the name rule `^[a-z][a-z0-9-]{1,46}[a-z0-9]$` (422
//     service_account_name_invalid), unique by its synthetic address (409
//     service_account_name_taken, a deactivated account keeps it);
//   - roles: never admin, founder or ssh-console (403
//     role_not_allowed_for_service_account), a catalogue key (422
//     unknown_role), a grantable one when added (400 role_not_grantable),
//     and a token adds or removes only keys it holds (403
//     role_not_held_by_token), never on its own account (403
//     token_cannot_change_own_roles); an empty set is allowed;
//   - an owner customer that must exist (422 customer_not_found); null clears it;
//   - nothing issues a token; ci_links are read-only (SetServiceAccountCILinks).

var rxSAName = regexp.MustCompile(`^[a-z][a-z0-9-]{1,46}[a-z0-9]$`)

// SetServiceAccountCILinks sets the GitLab CI links an operator made in the
// portal for a service account (read-only over the API).
func (m *MockAPI) SetServiceAccountCILinks(id string, links ...map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.saLinks == nil {
		m.saLinks = map[string][]map[string]any{}
	}
	m.saLinks[id] = links
}

// ServiceAccountState is the account as the platform holds it: active, its
// owner customer id ("" for none) and its roles, sorted.
func (m *MockAPI) ServiceAccountState(id string) (active bool, owner string, roles []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.users.people[id]
	if p == nil {
		return false, "", nil
	}
	if p.owner != nil {
		owner = strconv.Itoa(*p.owner)
	}
	return p.isActive, owner, sortedRoles(p)
}

func sortedRoles(p *mockPerson) []string {
	out := make([]string, 0, len(p.roles))
	for r := range p.roles {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

func (m *MockAPI) saRead(c *call) *reply {
	return m.require(c, "service-accounts-read-global", "service-accounts-admin-global")
}

func (m *MockAPI) saWrite(c *call) *reply { return m.require(c, "service-accounts-admin-global") }

func (m *MockAPI) saDestroyRefused(c *call) *reply {
	if !m.tokenAllowsDestroy() {
		r := c.problem(http.StatusForbidden, "destroy_not_allowed",
			"This API token was not created with allow_destroy.", nil)
		return &r
	}
	return nil
}

func (m *MockAPI) routeServiceAccounts(c *call) (reply, bool) {
	parts := strings.Split(strings.Trim(c.path, "/"), "/")
	if parts[0] != "service-accounts" {
		return reply{}, false
	}
	switch {
	case len(parts) == 1:
		switch c.r.Method {
		case http.MethodGet:
			return m.saList(c), true
		case http.MethodPost:
			return m.saCreate(c), true
		}
	case len(parts) == 2:
		switch c.r.Method {
		case http.MethodGet:
			return m.saGet(c, parts[1]), true
		case http.MethodPatch:
			return m.saUpdate(c, parts[1]), true
		case http.MethodDelete:
			return m.saDelete(c, parts[1]), true
		}
	default:
		return c.problem(http.StatusNotFound, "not_found", "", nil), true
	}
	return c.methodNotAllowed(), true
}

func (m *MockAPI) saWire(p *mockPerson) map[string]any {
	var owner, ownerName any
	if p.owner != nil {
		owner = strconv.Itoa(*p.owner)
		if cu := m.tenancy.customers[*p.owner]; cu != nil {
			ownerName = cu.shortName
		}
	}
	links := m.saLinks[p.id]
	if links == nil {
		links = []map[string]any{}
	}
	return map[string]any{
		"id": p.id, "name": p.firstName, "email": p.email, "is_active": p.isActive,
		"owner_customer_id": owner, "owner_customer_name": ownerName, "roles": sortedRoles(p),
		"active_tokens": 0, "ci_links": links, "created_at": p.createdAt, "updated_at": p.updatedAt,
		"warnings": []any{},
	}
}

func (m *MockAPI) loadServiceAccount(c *call, raw string) (*mockPerson, *reply) {
	id, valid := uuidID(raw)
	p := m.users.people[id]
	if !valid || p == nil || p.kind != "service" {
		r := c.problem(http.StatusNotFound, "service_account_not_found", "No such service account.", nil)
		return nil, &r
	}
	return p, nil
}

func (m *MockAPI) saList(c *call) reply {
	if r := m.saRead(c); r != nil {
		return *r
	}
	q := c.r.URL.Query()
	var list []*mockPerson
	for _, p := range m.users.people {
		if p.kind != "service" {
			continue
		}
		if o := q.Get("owner_customer_id"); o != "" && (p.owner == nil || strconv.Itoa(*p.owner) != o) {
			continue
		}
		if a := q.Get("is_active"); a != "" && strconv.FormatBool(p.isActive) != a {
			continue
		}
		list = append(list, p)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].firstName < list[j].firstName })
	items := make([]map[string]any, 0, len(list))
	for _, p := range list {
		items = append(items, m.saWire(p))
	}
	return ok(http.StatusOK, map[string]any{"items": items, "next_cursor": nil})
}

func (m *MockAPI) saGet(c *call, raw string) reply {
	if r := m.saRead(c); r != nil {
		return *r
	}
	p, bad := m.loadServiceAccount(c, raw)
	if bad != nil {
		return *bad
	}
	return ok(http.StatusOK, m.saWire(p))
}

func (m *MockAPI) saNameTaken(name, except string) bool {
	return m.emailTaken(name+"@service-account.invalid", except)
}

// saOwner validates an owner_customer_id member; ok false means refused.
func (m *MockAPI) saOwner(c *call, raw *string) (*int, *reply) {
	if raw == nil {
		return nil, nil
	}
	cid, valid := intID(*raw)
	if !valid || m.tenancy.customers[cid] == nil {
		r := c.problem(http.StatusUnprocessableEntity, "customer_not_found",
			"owner_customer_id names no customer.", map[string]any{"field": "owner_customer_id"})
		return nil, &r
	}
	return &cid, nil
}

// saRoles checks a requested role set against the account's current one.
func (m *MockAPI) saRoles(c *call, id string, current map[string]string, requested []string) *reply {
	refuse := func(status int, code, detail string) *reply {
		r := c.problem(status, code, detail, nil)
		return &r
	}
	want := map[string]bool{}
	for _, r := range requested {
		want[r] = true
	}
	catalogue := m.roleCatalogue()
	held := m.scopes()
	for _, r := range requested {
		switch {
		case r == "admin" || privilegedRoles[r]:
			return refuse(http.StatusForbidden, "role_not_allowed_for_service_account",
				"A service account cannot hold "+r)
		case !catalogue[r]:
			return refuse(http.StatusUnprocessableEntity, "unknown_role", "Unknown role(s): "+r)
		}
	}
	changed := false
	for r := range want {
		if _, had := current[r]; !had {
			changed = true
			if !m.grantable(r) {
				return refuse(http.StatusBadRequest, "role_not_grantable", "Not grantable: "+r)
			}
			if m.isToken() && !held[r] {
				return refuse(http.StatusForbidden, "role_not_held_by_token",
					"An API token can grant only a role within its own scopes; it does not carry "+r)
			}
		}
	}
	for r := range current {
		if !want[r] {
			changed = true
			if m.isToken() && !held[r] {
				return refuse(http.StatusForbidden, "role_not_held_by_token",
					"An API token can revoke only a role within its own scopes; it does not carry "+r)
			}
		}
	}
	if changed && m.isToken() && id == m.principalID() {
		return refuse(http.StatusForbidden, "token_cannot_change_own_roles",
			"An API token cannot change the roles of its own account")
	}
	return nil
}

func stringsField(v *validation, body map[string]any, field string) ([]string, bool) {
	raw, present := body[field]
	if !present {
		return nil, false
	}
	list, isList := raw.([]any)
	if !isList {
		v.fail(field, "Input should be a valid list")
		return nil, true
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		s, isString := e.(string)
		if !isString || !rxRoleName.MatchString(s) {
			v.fail(field, "malformed role name")
			continue
		}
		out = append(out, s)
	}
	return out, true
}

func (m *MockAPI) saCreate(c *call) reply {
	if r := m.saWrite(c); r != nil {
		return *r
	}
	v := &validation{}
	body := decodeBody(c, v, "name", "owner_customer_id", "roles")
	name, _ := strField(v, body, "name", true, false, length(3, 48))
	owner, _ := strField(v, body, "owner_customer_id", false, true, nil)
	roles, _ := stringsField(v, body, "roles")
	if r := v.reply(c); r != nil {
		return *r
	}
	if !rxSAName.MatchString(*name) {
		return c.problem(http.StatusUnprocessableEntity, "service_account_name_invalid",
			"3-48 characters: lowercase letters, digits and '-', starting with a letter.",
			map[string]any{"field": "name"})
	}
	if m.saNameTaken(*name, "") {
		return c.problem(http.StatusConflict, "service_account_name_taken", "A service account named "+*name+" exists.", nil)
	}
	cid, bad := m.saOwner(c, owner)
	if bad != nil {
		return *bad
	}
	if r := m.saRoles(c, "", map[string]string{}, roles); r != nil {
		return *r
	}
	ts := now()
	p := &mockPerson{id: uuid.NewString(), email: *name + "@service-account.invalid", firstName: *name,
		username: *name, locale: "en", kind: "service", isActive: true, authMode: "local", adUsername: *name,
		roles: map[string]string{}, ssoSync: "unlinked", createdAt: ts, updatedAt: ts, owner: cid}
	for _, r := range roles {
		p.roles[r] = ts
	}
	m.users.people[p.id] = p
	m.tenancy.users[p.id] = p.email
	return ok(http.StatusCreated, m.saWire(p))
}

func (m *MockAPI) saUpdate(c *call, raw string) reply {
	if r := m.saWrite(c); r != nil {
		return *r
	}
	if r := patchMediaType(c); r != nil {
		return *r
	}
	v := &validation{}
	body := decodeBody(c, v, "name", "owner_customer_id", "roles", "is_active")
	name, _ := strField(v, body, "name", false, false, length(3, 48))
	owner, ownerSent := strField(v, body, "owner_customer_id", false, true, nil)
	roles, rolesSent := stringsField(v, body, "roles")
	active, _ := boolField(v, body, "is_active", false)
	if r := v.reply(c); r != nil {
		return *r
	}
	p, bad := m.loadServiceAccount(c, raw)
	if bad != nil {
		return *bad
	}
	if name != nil && *name != p.firstName {
		if !rxSAName.MatchString(*name) {
			return c.problem(http.StatusUnprocessableEntity, "service_account_name_invalid",
				"3-48 characters: lowercase letters, digits and '-', starting with a letter.",
				map[string]any{"field": "name"})
		}
		if m.saNameTaken(*name, p.id) {
			return c.problem(http.StatusConflict, "service_account_name_taken", "A service account named "+*name+" exists.", nil)
		}
	}
	var cid *int
	if ownerSent {
		if cid, bad = m.saOwner(c, owner); bad != nil {
			return *bad
		}
	}
	if active != nil && !*active && p.isActive {
		if r := m.saDestroyRefused(c); r != nil {
			return *r
		}
	}
	if rolesSent {
		if r := m.saRoles(c, p.id, p.roles, roles); r != nil {
			return *r
		}
	}
	ts := now()
	if name != nil && *name != p.firstName {
		p.firstName, p.username, p.adUsername = *name, *name, *name
		p.email = *name + "@service-account.invalid"
		m.tenancy.users[p.id] = p.email
	}
	if ownerSent {
		p.owner = cid
	}
	if rolesSent {
		next := map[string]string{}
		for _, r := range roles {
			if at, had := p.roles[r]; had {
				next[r] = at
			} else {
				next[r] = ts
			}
		}
		p.roles = next
	}
	if active != nil {
		p.isActive = *active
	}
	p.updatedAt = ts
	return ok(http.StatusOK, m.saWire(p))
}

func (m *MockAPI) saDelete(c *call, raw string) reply {
	if r := m.saWrite(c); r != nil {
		return *r
	}
	if r := m.saDestroyRefused(c); r != nil {
		return *r
	}
	p, bad := m.loadServiceAccount(c, raw)
	if bad != nil {
		return *bad
	}
	if p.isActive {
		p.isActive = false
		p.updatedAt = now()
	}
	return reply{status: http.StatusNoContent}
}
