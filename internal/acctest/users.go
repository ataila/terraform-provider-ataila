// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package acctest

import (
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"
)

// The users endpoints of /api/v1: people, their role grants and the
// permission catalogue, with the platform's rules:
//
//   - a create takes no password and returns none; the person gets the role
//     `user` and is provisioned (SSO account; a GitLab account only with
//     needs_git_access): provisioning_status `partial` without GitLab, `ok`
//     with it, `error` with a provisioning_<step>_failed warning when a step
//     fails, and the answer is 201 either way;
//   - e-mail addresses are stored entirely lower-cased; a deactivated person
//     keeps theirs, so re-creating the address is 409 email_taken;
//   - username and ad_username are derived as firstname.lastname (folded to
//     ASCII) when omitted; ad_username is frozen, username is frozen once the
//     person has an SSO account (keycloak_linked);
//   - DELETE (and PATCH is_active false) DEACTIVATES: 200 with the person;
//     refused for the caller's own account, the last active admin and service
//     accounts; destroy-gated by the token's flag;
//   - role grants: 201 granted / 200 already held; a token never touches
//     admin, founder or ssh-console, never its own account, and only roles it
//     holds itself; the last role and the last admin's admin stay;
//   - service accounts are left out of lists by default and every write on
//     one is 409.

// seededAdminID is the one active admin every mock starts with.
const seededAdminID = "00000000-0000-4000-8000-00000000ad01"

var (
	rxUsername = regexp.MustCompile(`^[a-z0-9._-]{1,20}$`)
	rxRoleName = regexp.MustCompile(`^[a-z][a-z0-9._-]{1,62}$`)
)

// Roles an API token can never grant or revoke, and roles only their
// holders may grant.
var (
	tokenUnmanageable = map[string]bool{"admin": true, "founder": true, "ssh-console": true}
	privilegedRoles   = map[string]bool{"founder": true, "ssh-console": true}
)

type mockPerson struct {
	id, email, username, firstName string
	lastName                       *string
	locale, kind                   string
	isActive, isInternal           bool
	authMode                       string
	adUsername                     string
	needsGit                       bool
	roles                          map[string]string // role -> granted_at
	keycloakLinked, gitlabLinked   bool
	kcSync                         string
	provStatus                     *string
	createdAt, updatedAt           string
}

type mockPermission struct {
	key, feature, level, scope, category, label, description string
	grantable                                                bool
}

type usersState struct {
	people        map[string]*mockPerson
	failStep      string
	deactivateOut []map[string]any
	permissions   []mockPermission
}

// The catalogue the mock serves: four Platform API and AI features, each
// read and admin, each in both scopes. The -tenant twins are honoured but
// not grantable (their tenant scoping is not enforced yet).
func defaultPermissions() []mockPermission {
	type feat struct{ feature, label, category string }
	var out []mockPermission
	for _, f := range []feat{
		{"ai-gateway", "AI gateway", "AI"},
		{"api-tokens", "API tokens and service accounts", "Platform API"},
		{"tenancy", "Customers and tenants", "Platform API"},
		{"users", "Users and role grants", "Platform API"},
	} {
		for _, level := range []string{"read", "admin"} {
			for _, scope := range []string{"global", "tenant"} {
				out = append(out, mockPermission{
					key: f.feature + "-" + level + "-" + scope, feature: f.feature, level: level, scope: scope,
					category: f.category, label: f.label + " (" + level + ", " + scope + ")",
					description: "Mock description of " + f.feature + " " + level + ".",
					grantable:   scope == "global",
				})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
	return out
}

func newUsersState(principal map[string]any, scopes []string) *usersState {
	s := &usersState{people: map[string]*mockPerson{}, permissions: defaultPermissions()}
	ts := now()
	admin := &mockPerson{id: seededAdminID, email: "platform.admin@example.com", username: "platform.admin",
		firstName: "Platform", lastName: strPtr("Admin"), locale: "en", kind: "human", isActive: true,
		isInternal: true, authMode: "both", adUsername: "platform.admin",
		roles:          map[string]string{"admin": ts, "user": ts},
		keycloakLinked: true, kcSync: "ok", provStatus: strPtr("ok"), createdAt: ts, updatedAt: ts}
	s.people[admin.id] = admin
	if id, _ := principal["id"].(string); id != "" {
		roles := map[string]string{"user": ts}
		for _, sc := range scopes {
			roles[sc] = ts
		}
		kind, _ := principal["kind"].(string)
		email, _ := principal["email"].(string)
		name, _ := principal["name"].(string)
		s.people[id] = &mockPerson{id: id, email: email, username: name, firstName: name, locale: "en",
			kind: kind, isActive: true, authMode: "local", adUsername: name, roles: roles,
			kcSync: "unlinked", createdAt: ts, updatedAt: ts}
	}
	return s
}

func strPtr(s string) *string { return &s }

func sameStr(a, b *string) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

// ── test helpers ─────────────────────────────────────────────────────────────

// AddPerson registers an active person with the role `user` and returns
// their id (AddUser registers one by e-mail alone).
func (m *MockAPI) AddPerson(email, first, last string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.addPerson(email, first, last, "human").id
}

// AddServiceAccount registers a service account and returns its id.
func (m *MockAPI) AddServiceAccount(name string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.addPerson(name+"@service-account.invalid", name, "", "service").id
}

func (m *MockAPI) addPerson(email, first, last, kind string) *mockPerson {
	ts := now()
	p := &mockPerson{id: uuid.NewString(), email: strings.ToLower(email), firstName: first, locale: "hu",
		kind: kind, isActive: true, authMode: "sso", roles: map[string]string{"user": ts},
		kcSync: "unlinked", createdAt: ts, updatedAt: ts}
	if last != "" {
		p.lastName = &last
	}
	p.username = deriveHandle(first, last, "")
	p.adUsername = p.username
	m.users.people[p.id] = p
	m.tenancy.users[p.id] = p.email
	return p
}

// Person is the wire form of a person, as GET would answer it.
func (m *MockAPI) Person(id string) (map[string]any, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.users.people[id]
	if p == nil {
		return nil, false
	}
	return p.wire(nil), true
}

// SetPersonField changes a person as the portal would: first_name,
// last_name (nil clears), locale, is_active, needs_git_access.
func (m *MockAPI) SetPersonField(id, field string, value any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.users.people[id]
	if p == nil {
		panic("SetPersonField: no person " + id)
	}
	switch field {
	case "first_name":
		p.firstName = value.(string)
	case "last_name":
		if value == nil {
			p.lastName = nil
		} else {
			p.lastName = strPtr(value.(string))
		}
	case "locale":
		p.locale = value.(string)
	case "is_active":
		p.isActive = value.(bool)
	case "needs_git_access":
		p.needsGit = value.(bool)
	default:
		panic("SetPersonField: unsupported field " + field)
	}
	p.updatedAt = now()
}

// SetPersonRole grants (held) or removes a role out of band.
func (m *MockAPI) SetPersonRole(id, role string, held bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if held {
		m.users.people[id].roles[role] = now()
	} else {
		delete(m.users.people[id].roles, role)
	}
}

// FailProvisioningStep makes the named provisioning step (for example
// "gitlab_user") fail on the next creates.
func (m *MockAPI) FailProvisioningStep(step string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.users.failStep = step
}

// SetDeactivationWarning adds a warning to every deactivation (a downstream
// step that failed, such as gitlab_block_failed).
func (m *MockAPI) SetDeactivationWarning(code, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.users.deactivateOut = append(m.users.deactivateOut, map[string]any{"code": code, "message": message})
}

// ── wire forms ───────────────────────────────────────────────────────────────

func (p *mockPerson) name() string {
	parts := []string{p.firstName}
	if p.lastName != nil && *p.lastName != "" {
		parts = append(parts, *p.lastName)
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

func (p *mockPerson) wire(warnings []map[string]any) map[string]any {
	if warnings == nil {
		warnings = []map[string]any{}
	}
	roles := make([]string, 0, len(p.roles))
	for r := range p.roles {
		roles = append(roles, r)
	}
	sort.Strings(roles)
	var last, prov, mode any
	if p.lastName != nil {
		last = *p.lastName
	}
	if p.provStatus != nil {
		prov = *p.provStatus
	}
	if p.authMode != "" {
		mode = p.authMode
	}
	return map[string]any{
		"id": p.id, "email": p.email, "username": p.username, "first_name": p.firstName,
		"last_name": last, "name": p.name(), "locale": p.locale, "kind": p.kind,
		"is_active": p.isActive, "is_internal": p.isInternal, "auth_mode": mode,
		"ad_username": p.adUsername, "needs_git_access": p.needsGit, "roles": roles,
		"keycloak_linked": p.keycloakLinked, "gitlab_linked": p.gitlabLinked,
		"kc_sync_status": p.kcSync, "provisioning_status": prov,
		"created_at": p.createdAt, "updated_at": p.updatedAt, "warnings": warnings,
	}
}

// ── name derivation (services/ad_naming.py) ─────────────────────────────────

var foldAccents = strings.NewReplacer(
	"á", "a", "à", "a", "â", "a", "ä", "a", "ã", "a", "å", "a",
	"é", "e", "è", "e", "ê", "e", "ë", "e",
	"í", "i", "ì", "i", "î", "i", "ï", "i",
	"ó", "o", "ò", "o", "ô", "o", "ö", "o", "ő", "o", "õ", "o",
	"ú", "u", "ù", "u", "û", "u", "ü", "u", "ű", "u",
	"ç", "c", "ñ", "n", "ß", "ss",
)

// fold strips diacritics and lower-cases. Assumed: a Latin subset of the
// platform's Unicode folding.
func fold(s string) string { return foldAccents.Replace(strings.ToLower(strings.TrimSpace(s))) }

var rxNotHandle = regexp.MustCompile(`[^a-z0-9-]+`)

// deriveHandle is the Windows account name: the override when given, else
// firstname.lastname folded to ASCII.
func deriveHandle(first, last, override string) string {
	if strings.TrimSpace(override) != "" {
		return fold(override)
	}
	var parts []string
	for _, p := range []string{first, last} {
		if f := rxNotHandle.ReplaceAllString(fold(p), ""); f != "" {
			parts = append(parts, f)
		}
	}
	return strings.Join(parts, ".")
}

func handleProblem(sam string) string {
	switch {
	case sam == "":
		return "Windows username is empty"
	case len(sam) > 20:
		return fmt.Sprintf("Windows username '%s' is %d characters; Active Directory allows at most 20.", sam, len(sam))
	case strings.HasSuffix(sam, "."):
		return fmt.Sprintf("Windows username '%s' must not end with a period", sam)
	}
	return ""
}

func handle(s string) string {
	if !rxUsername.MatchString(s) {
		return "String should match pattern '" + rxUsername.String() + "'"
	}
	if strings.HasSuffix(s, ".") {
		return "must not end with '.'"
	}
	return ""
}

// ── routing ──────────────────────────────────────────────────────────────────

func (m *MockAPI) routeUsers(c *call) reply {
	parts := strings.Split(strings.Trim(c.path, "/"), "/")
	method := c.r.Method
	switch {
	case len(parts) == 1 && parts[0] == "permissions" && method == http.MethodGet:
		return m.permissionsList(c)
	case len(parts) == 1 && parts[0] == "users":
		switch method {
		case http.MethodGet:
			return m.usersList(c)
		case http.MethodPost:
			return m.usersCreate(c)
		}
	case len(parts) == 2 && parts[0] == "users":
		switch method {
		case http.MethodGet:
			return m.usersGet(c, parts[1])
		case http.MethodPatch:
			return m.usersUpdate(c, parts[1])
		case http.MethodDelete:
			return m.usersDelete(c, parts[1])
		}
	case len(parts) == 3 && parts[0] == "users" && parts[2] == "roles" && method == http.MethodGet:
		return m.rolesList(c, parts[1])
	case len(parts) == 4 && parts[0] == "users" && parts[2] == "roles":
		switch method {
		case http.MethodGet:
			return m.rolesGet(c, parts[1], parts[3])
		case http.MethodPut:
			return m.rolesPut(c, parts[1], parts[3])
		case http.MethodDelete:
			return m.rolesDelete(c, parts[1], parts[3])
		}
	default:
		return c.problem(http.StatusNotFound, "not_found", "", nil)
	}
	return c.methodNotAllowed()
}

func (m *MockAPI) usersRead(c *call) *reply {
	return m.require(c, "users-read-global", "users-admin-global")
}
func (m *MockAPI) usersWrite(c *call) *reply { return m.require(c, "users-admin-global") }

func (m *MockAPI) tokenAllowsDestroy() bool {
	tok, isToken := m.whoami["token"].(map[string]any)
	if !isToken || tok == nil {
		return true
	}
	allow, _ := tok["allow_destroy"].(bool)
	return allow
}

func (m *MockAPI) isToken() bool {
	tok, isToken := m.whoami["token"].(map[string]any)
	return isToken && tok != nil
}

func (m *MockAPI) principalID() string {
	p, _ := m.whoami["principal"].(map[string]any)
	id, _ := p["id"].(string)
	return id
}

func (m *MockAPI) loadPerson(c *call, raw string) (*mockPerson, *reply) {
	id, valid := uuidID(raw)
	p := m.users.people[id]
	if !valid || p == nil {
		r := c.problem(http.StatusNotFound, "user_not_found", "No such user.", nil)
		return nil, &r
	}
	return p, nil
}

func refuseService(c *call, p *mockPerson) *reply {
	if p.kind == "service" {
		r := c.problem(http.StatusConflict, "service_account_managed_elsewhere",
			"Service accounts are managed on the portal's API tokens and service accounts pages, not here.", nil)
		return &r
	}
	return nil
}

func (m *MockAPI) activeAdmins() int {
	n := 0
	for _, p := range m.users.people {
		if _, admin := p.roles["admin"]; admin && p.isActive {
			n++
		}
	}
	return n
}

// deactivate is user_admin.deactivate: the refusals, then is_active false.
func (m *MockAPI) deactivate(c *call, p *mockPerson) *reply {
	refuse := func(code, detail string) *reply {
		r := c.problem(http.StatusConflict, code, detail, nil)
		return &r
	}
	if p.kind == "service" {
		return refuse("service_account_managed_elsewhere", "Service accounts are managed on the portal's API tokens and service accounts pages, not here.")
	}
	if p.id == m.principalID() {
		return refuse("cannot_deactivate_self", "You cannot deactivate your own account.")
	}
	if !p.isActive {
		return nil
	}
	if _, admin := p.roles["admin"]; admin && m.activeAdmins() <= 1 {
		return refuse("last_active_admin", "Refusing to deactivate the last active admin")
	}
	p.isActive = false
	p.updatedAt = now()
	return nil
}

// ── users ────────────────────────────────────────────────────────────────────

func (m *MockAPI) usersList(c *call) reply {
	if r := m.usersRead(c); r != nil {
		return *r
	}
	p, bad := c.pageParams()
	if bad != nil {
		return *bad
	}
	q := c.r.URL.Query()
	queryFail := func(field, msg string) reply {
		return c.problem(http.StatusUnprocessableEntity, "validation_failed", "The request does not match the schema.",
			map[string]any{"errors": []any{map[string]any{"loc": []any{"query", field}, "msg": msg, "type": "value_error"}}})
	}
	kind := "human"
	if q.Has("kind") {
		kind = q.Get("kind")
		if oneOf("human", "service", "all")(kind) != "" {
			return queryFail("kind", "Input should be 'human', 'service' or 'all'")
		}
	}
	var active *bool
	if q.Has("is_active") {
		switch strings.ToLower(q.Get("is_active")) {
		case "true", "1":
			v := true
			active = &v
		case "false", "0":
			v := false
			active = &v
		default:
			return queryFail("is_active", "Input should be a valid boolean")
		}
	}
	tenantFilter := ""
	if q.Has("tenant_id") {
		id, valid := uuidID(q.Get("tenant_id"))
		if !valid {
			return queryFail("tenant_id", "Input should be a valid UUID")
		}
		tenantFilter = id
	}
	if q.Has("customer_id") && !rxIntID.MatchString(q.Get("customer_id")) {
		return queryFail("customer_id", "String should match pattern '^[1-9][0-9]{0,8}$'")
	}
	after := ""
	if p.after != nil {
		s, isString := p.after["id"].(string)
		if !isString || s == "" {
			return invalidCursor(c)
		}
		id, valid := uuidID(s)
		if !valid {
			return invalidCursor(c)
		}
		after = id
	}
	memberOf := func(pid string, tenants func(string) bool) bool {
		for tid, members := range m.tenancy.memberships {
			if _, in := members[pid]; in && tenants(tid) {
				return true
			}
		}
		return false
	}
	ids := make([]string, 0, len(m.users.people))
	for id := range m.users.people {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var items []map[string]any
	for _, id := range ids {
		pp := m.users.people[id]
		if after != "" && id <= after {
			continue
		}
		if kind != "all" && pp.kind != kind {
			continue
		}
		if q.Has("email") && pp.email != strings.ToLower(strings.TrimSpace(q.Get("email"))) {
			continue
		}
		if q.Has("username") && !strings.EqualFold(pp.username, q.Get("username")) {
			continue
		}
		if active != nil && pp.isActive != *active {
			continue
		}
		if tenantFilter != "" && !memberOf(id, func(t string) bool { return t == tenantFilter }) {
			continue
		}
		if q.Has("customer_id") && !memberOf(id, func(t string) bool {
			tn := m.tenancy.tenants[t]
			return tn != nil && tn.customerID != nil && fmt.Sprint(*tn.customerID) == q.Get("customer_id")
		}) {
			continue
		}
		if q.Has("role") {
			if _, holds := pp.roles[q.Get("role")]; !holds {
				continue
			}
		}
		items = append(items, pp.wire(nil))
		if len(items) > p.limit {
			break
		}
	}
	return ok(http.StatusOK, page(items, p.limit, func(it map[string]any) map[string]any {
		return map[string]any{"id": it["id"]}
	}))
}

func (m *MockAPI) usersGet(c *call, raw string) reply {
	if r := m.usersRead(c); r != nil {
		return *r
	}
	p, bad := m.loadPerson(c, raw)
	if bad != nil {
		return *bad
	}
	return ok(http.StatusOK, p.wire(nil))
}

func (m *MockAPI) emailTaken(email, except string) bool {
	for id, p := range m.users.people {
		if id != except && p.email == email {
			return true
		}
	}
	return false
}

func (m *MockAPI) usernameTaken(name, except string) bool {
	for id, p := range m.users.people {
		if id != except && strings.EqualFold(p.username, name) {
			return true
		}
	}
	return false
}

func boolField(v *validation, body map[string]any, field string, nullable bool) (*bool, bool) {
	raw, present := body[field]
	if !present {
		return nil, false
	}
	if raw == nil {
		if !nullable {
			v.fail(field, "Input should be a valid boolean")
		}
		return nil, true
	}
	b, isBool := raw.(bool)
	if !isBool {
		v.fail(field, "Input should be a valid boolean")
		return nil, true
	}
	return &b, true
}

func nonBlank(max int) func(string) string {
	return func(s string) string {
		if strings.TrimSpace(s) == "" {
			return "must not be blank"
		}
		return length(1, max)(s)
	}
}

func (m *MockAPI) usersCreate(c *call) reply {
	if r := m.usersWrite(c); r != nil {
		return *r
	}
	v := &validation{}
	body := decodeBody(c, v, "email", "first_name", "last_name", "locale", "needs_git_access", "username", "ad_username")
	mail, _ := strField(v, body, "email", true, false, email)
	first, _ := strField(v, body, "first_name", true, false, nonBlank(100))
	last, _ := strField(v, body, "last_name", false, true, length(0, 100))
	locale, _ := strField(v, body, "locale", false, false, oneOf("en", "hu"))
	git, _ := boolField(v, body, "needs_git_access", false)
	username, _ := strField(v, body, "username", false, true, handle)
	adName, _ := strField(v, body, "ad_username", false, true, handle)
	if r := v.reply(c); r != nil {
		return *r
	}
	addr := strings.ToLower(normalizeEmail(*mail))
	firstName := strings.TrimSpace(*first)
	lastName := ""
	if last != nil {
		lastName = strings.TrimSpace(*last)
	}
	override := ""
	switch {
	case adName != nil:
		override = *adName
	case username != nil:
		override = *username
	}
	sam := deriveHandle(firstName, lastName, override)
	if msg := handleProblem(sam); msg != "" {
		field := "first_name"
		if adName != nil {
			field = "ad_username"
		} else if username != nil {
			field = "username"
		}
		return c.problem(http.StatusUnprocessableEntity, "invalid_directory_name", msg, map[string]any{"field": field})
	}
	handleName := sam
	if username != nil {
		handleName = *username
	}
	if m.emailTaken(addr, "") {
		return c.problem(http.StatusConflict, "email_taken", "A user with this e-mail address exists (deactivated users keep theirs).", nil)
	}
	if m.usernameTaken(handleName, "") {
		return c.problem(http.StatusConflict, "username_taken",
			fmt.Sprintf("The username '%s' is taken; send another `username` (or `ad_username`).", handleName), nil)
	}
	ts := now()
	p := &mockPerson{id: uuid.NewString(), email: addr, username: handleName, firstName: firstName,
		locale: "hu", kind: "human", isActive: true, authMode: "sso", adUsername: sam,
		roles: map[string]string{"user": ts}, keycloakLinked: true, kcSync: "ok", createdAt: ts, updatedAt: ts}
	if lastName != "" {
		p.lastName = &lastName
	}
	if locale != nil {
		p.locale = *locale
	}
	if git != nil {
		p.needsGit = *git
	}
	warnings := m.provision(p)
	m.users.people[p.id] = p
	m.tenancy.users[p.id] = p.email
	return ok(http.StatusCreated, p.wire(warnings))
}

// provision is the portal's provisioning run: the SSO account always, a
// GitLab account when wanted; a failing step is a warning.
func (m *MockAPI) provision(p *mockPerson) []map[string]any {
	var warnings []map[string]any
	status := "partial"
	if p.needsGit {
		status = "ok"
		if m.users.failStep == "gitlab_user" {
			status = "error"
			warnings = append(warnings, map[string]any{"code": "provisioning_gitlab_user_failed",
				"message": "HTTP 503 from GitLab (injected by the test)"})
		} else {
			p.gitlabLinked = true
		}
	}
	if m.users.failStep != "" && m.users.failStep != "gitlab_user" {
		status = "error"
		warnings = append(warnings, map[string]any{"code": "provisioning_" + m.users.failStep + "_failed",
			"message": "Injected by the test."})
	}
	p.provStatus = &status
	return warnings
}

func (m *MockAPI) usersUpdate(c *call, raw string) reply {
	if r := m.usersWrite(c); r != nil {
		return *r
	}
	if r := patchMediaType(c); r != nil {
		return *r
	}
	v := &validation{}
	body := decodeBody(c, v, "first_name", "last_name", "email", "locale", "is_active", "needs_git_access", "username", "ad_username")
	first, _ := strField(v, body, "first_name", false, false, nonBlank(100))
	last, lastSent := strField(v, body, "last_name", false, true, length(1, 100))
	mail, _ := strField(v, body, "email", false, false, email)
	locale, _ := strField(v, body, "locale", false, false, oneOf("en", "hu"))
	active, _ := boolField(v, body, "is_active", false)
	git, _ := boolField(v, body, "needs_git_access", false)
	username, _ := strField(v, body, "username", false, false, handle)
	strField(v, body, "ad_username", false, true, nil)
	if r := v.reply(c); r != nil {
		return *r
	}
	p, bad := m.loadPerson(c, raw)
	if bad != nil {
		return *bad
	}
	if r := refuseService(c, p); r != nil {
		return *r
	}
	if sent, present := body["ad_username"]; present && frozenDiffers(sent, p.adUsername, false) {
		return c.problem(http.StatusUnprocessableEntity, "immutable_field", "ad_username cannot be changed after create.",
			map[string]any{"field": "ad_username"})
	}
	if username != nil && *username != strings.ToLower(p.username) {
		if p.keycloakLinked {
			return c.problem(http.StatusUnprocessableEntity, "immutable_field",
				"username cannot be changed once the person has an SSO account: it is their Keycloak and directory account name.",
				map[string]any{"field": "username"})
		}
		if m.usernameTaken(*username, p.id) {
			return c.problem(http.StatusConflict, "username_taken", "The username is taken.", nil)
		}
	} else {
		username = nil
	}
	var newEmail *string
	if mail != nil {
		addr := strings.ToLower(normalizeEmail(*mail))
		if addr != p.email {
			if m.emailTaken(addr, p.id) {
				return c.problem(http.StatusConflict, "email_taken", "A user with this e-mail address exists.", nil)
			}
			newEmail = &addr
		}
	}
	deactivating := active != nil && !*active && p.isActive
	activating := active != nil && *active && !p.isActive
	if deactivating && m.isToken() && !m.tokenAllowsDestroy() {
		return c.problem(http.StatusForbidden, "destroy_not_allowed",
			"Deactivating is a destroy: this API token was not created with allow_destroy.", nil)
	}
	var warnings []map[string]any
	changed := false
	if deactivating {
		if r := m.deactivate(c, p); r != nil {
			return *r
		}
		warnings = append(warnings, m.users.deactivateOut...)
		changed = true
	}
	if activating {
		p.isActive = true
		changed = true
	}
	if first != nil && strings.TrimSpace(*first) != p.firstName {
		p.firstName = strings.TrimSpace(*first)
		changed = true
	}
	if lastSent {
		var nl *string
		if last != nil && strings.TrimSpace(*last) != "" {
			nl = strPtr(strings.TrimSpace(*last))
		}
		if !sameStr(nl, p.lastName) {
			p.lastName = nl
			changed = true
		}
	}
	if locale != nil && *locale != p.locale {
		p.locale = *locale
		changed = true
	}
	if username != nil {
		p.username = *username
		changed = true
	}
	if newEmail != nil {
		p.email = *newEmail
		m.tenancy.users[p.id] = p.email
		warnings = append(warnings, map[string]any{"code": "email_keyed_grants_affected",
			"message": "Access granted elsewhere by e-mail address still names the old address and may no longer apply."})
		changed = true
	}
	if git != nil && *git != p.needsGit {
		p.needsGit = *git
		if *git {
			warnings = append(warnings, m.provision(p)...)
		}
		changed = true
	}
	if changed {
		p.updatedAt = now()
	}
	return ok(http.StatusOK, p.wire(warnings))
}

func (m *MockAPI) usersDelete(c *call, raw string) reply {
	if r := m.usersWrite(c); r != nil {
		return *r
	}
	if m.isToken() && !m.tokenAllowsDestroy() {
		return c.problem(http.StatusForbidden, "destroy_not_allowed", "This API token was not created with allow_destroy.", nil)
	}
	p, bad := m.loadPerson(c, raw)
	if bad != nil {
		return *bad
	}
	if r := m.deactivate(c, p); r != nil {
		return *r
	}
	return ok(http.StatusOK, p.wire(m.users.deactivateOut))
}

// ── role grants ──────────────────────────────────────────────────────────────

func (m *MockAPI) roleCatalogue() map[string]bool {
	out := map[string]bool{"user": true, "admin": true, "founder": true, "ssh-console": true}
	for _, p := range m.users.permissions {
		out[p.key] = true
	}
	return out
}

func (m *MockAPI) grantable(role string) bool {
	for _, p := range m.users.permissions {
		if p.key == role {
			return p.grantable
		}
	}
	return true // role names that are not permission keys (user, admin, ...)
}

func grantWire(uid, role, at string) map[string]any {
	var granted any
	if at != "" {
		granted = at
	}
	return map[string]any{"user_id": uid, "role": role, "granted_at": granted, "warnings": []any{}}
}

func (m *MockAPI) rolesList(c *call, raw string) reply {
	if r := m.usersRead(c); r != nil {
		return *r
	}
	pp, bad := c.pageParams()
	if bad != nil {
		return *bad
	}
	p, bad := m.loadPerson(c, raw)
	if bad != nil {
		return *bad
	}
	after := ""
	if pp.after != nil {
		s, isString := pp.after["role"].(string)
		if !isString || s == "" {
			return invalidCursor(c)
		}
		after = s
	}
	roles := make([]string, 0, len(p.roles))
	for r := range p.roles {
		roles = append(roles, r)
	}
	sort.Strings(roles)
	var items []map[string]any
	for _, r := range roles {
		if after != "" && r <= after {
			continue
		}
		items = append(items, grantWire(p.id, r, p.roles[r]))
		if len(items) > pp.limit {
			break
		}
	}
	return ok(http.StatusOK, page(items, pp.limit, func(it map[string]any) map[string]any {
		return map[string]any{"role": it["role"]}
	}))
}

func (m *MockAPI) rolesGet(c *call, raw, role string) reply {
	if r := m.usersRead(c); r != nil {
		return *r
	}
	p, bad := m.loadPerson(c, raw)
	if bad != nil {
		return *bad
	}
	at, held := p.roles[role]
	if !rxRoleName.MatchString(role) || !held {
		return c.problem(http.StatusNotFound, "role_grant_not_found", "The user does not hold this role.", nil)
	}
	return ok(http.StatusOK, grantWire(p.id, role, at))
}

// roleChange is user_admin.apply_role_change for one role added or removed;
// it returns whether the set changed, or the refusal.
func (m *MockAPI) roleChange(c *call, p *mockPerson, role string, add bool) (bool, *reply) {
	refuse := func(status int, code, detail string) (bool, *reply) {
		r := c.problem(status, code, detail, nil)
		return false, &r
	}
	_, held := p.roles[role]
	if add && !m.roleCatalogue()[role] {
		return refuse(http.StatusUnprocessableEntity, "unknown_role", "Unknown role(s): "+role)
	}
	if !add && held && len(p.roles) == 1 {
		return refuse(http.StatusConflict, "last_role", "A user must keep at least one role")
	}
	touched := (add && !held) || (!add && held)
	if add && !held && !m.grantable(role) {
		return refuse(http.StatusBadRequest, "role_not_grantable",
			"Not grantable — the -tenant scope is not enforced yet: "+role)
	}
	isSelf := p.id == m.principalID()
	if m.isToken() {
		if isSelf {
			return refuse(http.StatusForbidden, "token_cannot_change_own_roles",
				"An API token cannot change the roles of its own account")
		}
		if touched && tokenUnmanageable[role] {
			return refuse(http.StatusForbidden, "role_not_manageable_by_token", "An API token can never grant or revoke "+role)
		}
		if touched && !m.scopes()[role] {
			return refuse(http.StatusForbidden, "role_not_held_by_token",
				"An API token can grant or revoke only a role within its own scopes; it does not carry "+role)
		}
	}
	if add && !held && privilegedRoles[role] {
		if isSelf {
			return refuse(http.StatusForbidden, "cannot_grant_to_self", "Cannot grant "+role+" to yourself")
		}
		if !m.scopes()[role] {
			return refuse(http.StatusForbidden, "privileged_role_requires_holding_it", "Granting "+role+" requires holding it")
		}
	}
	if !add && held && role == "admin" {
		if isSelf {
			return refuse(http.StatusForbidden, "cannot_remove_own_admin", "Cannot remove your own admin role")
		}
		if m.activeAdmins() <= 1 {
			return refuse(http.StatusConflict, "last_active_admin", "Refusing to remove the last active admin")
		}
	}
	if !touched {
		return false, nil
	}
	if add {
		p.roles[role] = now()
	} else {
		delete(p.roles, role)
	}
	return true, nil
}

func (m *MockAPI) rolesPut(c *call, raw, role string) reply {
	if r := m.usersWrite(c); r != nil {
		return *r
	}
	p, bad := m.loadPerson(c, raw)
	if bad != nil {
		return *bad
	}
	if r := refuseService(c, p); r != nil {
		return *r
	}
	if !rxRoleName.MatchString(role) {
		return c.problem(http.StatusUnprocessableEntity, "unknown_role", "Unknown role(s): "+role, nil)
	}
	changed, refused := m.roleChange(c, p, role, true)
	if refused != nil {
		return *refused
	}
	status := http.StatusOK
	if changed {
		status = http.StatusCreated
	}
	return ok(status, grantWire(p.id, role, p.roles[role]))
}

func (m *MockAPI) rolesDelete(c *call, raw, role string) reply {
	if r := m.usersWrite(c); r != nil {
		return *r
	}
	p, bad := m.loadPerson(c, raw)
	if bad != nil {
		return *bad
	}
	if r := refuseService(c, p); r != nil {
		return *r
	}
	if !rxRoleName.MatchString(role) {
		return c.problem(http.StatusNotFound, "role_grant_not_found", "The user does not hold this role.", nil)
	}
	changed, refused := m.roleChange(c, p, role, false)
	if refused != nil {
		return *refused
	}
	if !changed {
		return c.problem(http.StatusNotFound, "role_grant_not_found", "The user does not hold this role.", nil)
	}
	return reply{status: http.StatusNoContent}
}

// ── the permission catalogue ─────────────────────────────────────────────────

func (m *MockAPI) permissionsList(c *call) reply {
	if r := m.usersRead(c); r != nil {
		return *r
	}
	pp, bad := c.pageParams()
	if bad != nil {
		return *bad
	}
	after := ""
	if pp.after != nil {
		s, isString := pp.after["key"].(string)
		if !isString || s == "" {
			return invalidCursor(c)
		}
		after = s
	}
	q := c.r.URL.Query()
	var items []map[string]any
	for _, p := range m.users.permissions {
		if (q.Has("feature") && p.feature != q.Get("feature")) || (after != "" && p.key <= after) {
			continue
		}
		mintable := p.grantable && p.scope == "global"
		items = append(items, map[string]any{
			"key": p.key, "feature": p.feature, "level": p.level, "scope": p.scope, "category": p.category,
			"label": p.label, "description": p.description, "grantable": p.grantable, "mintable": mintable,
		})
		if len(items) > pp.limit {
			break
		}
	}
	return ok(http.StatusOK, page(items, pp.limit, func(it map[string]any) map[string]any {
		return map[string]any{"key": it["key"]}
	}))
}
