// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package acctest

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// The AI gateway endpoints of /api/v1: the gateway, its serving tiers and
// virtual keys, with the platform's rules:
//
//   - a key belongs to one tenant, env and app; its alias is
//     <tenant-slug>-<env>-<app>[-<feature>], unique among live keys; those
//     four are frozen;
//   - the key's value is always stored in the secrets store and returned only in the
//     create or rotation response that produced it, only with
//     expose_secret; an idempotent replay carries no value and a
//     secret_not_replayed warning;
//   - `models` are catalogue tier names (422 unknown_tier); a tier that
//     serves nothing is accepted with a tier_not_serving warning;
//   - a read of one key shows the gateway's LIVE values and spend; `live` is
//     `missing` (with key_missing_on_gateway) when the gateway lost the key,
//     `not_read` on a list and after a PATCH;
//   - a gateway that is not configured (or unreachable) is a 503 on every
//     route that needs it; the key list and the tier routes keep working;
//   - an adopted key is never rotated (409 key_adopted); DELETE is
//     irreversible and needs the admin permission, not the destroy flag;
//   - tiers exist only by migration: PUT sets pinned_model and enabled on an
//     existing tier (404 otherwise); a pin that is not loaded is 422
//     model_not_loaded unless allow_unloaded_pin.

var (
	rxSegment  = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	rxDuration = regexp.MustCompile(`^[1-9][0-9]{0,3}(s|m|h|d|mo)$`)
	rxModel    = regexp.MustCompile(`^[A-Za-z0-9._:/@+-]{1,200}$`)
	rxUUID     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// GatewayBaseURL is what the mock's gateway reports as its base URL.
const GatewayBaseURL = "https://gateway.example.com/v1"

type mockTier struct {
	key, label, category string
	sort                 int
	role                 *string
	pinned               *string
	enabled              bool
	auto                 string // the model auto-assign picks when it is loaded
	createdAt            string
	updatedAt            *string // nil: never changed (updated_at repeats created_at)
}

type mockKey struct {
	id, org                 string
	project                 *string
	env, app                string
	feature                 *string
	alias                   string
	models                  []string
	rpm, tpm                *int64
	budget                  *float64
	duration                *string
	secretPath, secretField *string
	origin                  string
	hash, createdBy         string
	createdAt, updatedAt    string
	rotatedAt               *string
	deleted                 bool
}

// gatewayKey is the key as the gateway itself holds it.
type gatewayKey struct {
	models   []string
	rpm, tpm *int64
	budget   *float64
	duration *string
	spend    float64
	hash     string
}

type gatewayState struct {
	status     string // "ok", "not_configured", "unreachable"
	tiers      map[string]*mockTier
	candidates []string
	keys       map[string]*mockKey
	live       map[string]*gatewayKey // by alias
	secrets    map[string]string      // by alias: the stored key values
	projects   map[int]string         // project id -> tenant id
	nextProj   int
}

func newGatewayState() *gatewayState {
	return &gatewayState{
		status: "ok",
		tiers: map[string]*mockTier{
			"general":  {key: "general", label: "General", category: "chat", sort: 10, enabled: true, auto: "model-general"},
			"code":     {key: "code", label: "Code", category: "code", sort: 20, enabled: true, auto: "model-code"},
			"code-max": {key: "code-max", label: "Code (large)", category: "code", sort: 30, enabled: true, auto: "model-code-max"},
		},
		candidates: []string{"model-code", "model-general"},
		keys:       map[string]*mockKey{},
		live:       map[string]*gatewayKey{},
		secrets:    map[string]string{},
		projects:   map[int]string{},
		nextProj:   100,
	}
}

// ── test helpers ─────────────────────────────────────────────────────────────

// SetGateway sets the gateway's state: "ok", "not_configured" (a platform
// without a gateway) or "unreachable".
func (m *MockAPI) SetGateway(status string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gateway.status = status
}

// SetLoadedModels sets the models loaded on the fleet (the tier candidates).
func (m *MockAPI) SetLoadedModels(models ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gateway.candidates = append([]string{}, models...)
	sort.Strings(m.gateway.candidates)
}

// ClearTiers empties the tier catalogue, as on a platform that has none.
func (m *MockAPI) ClearTiers() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gateway.tiers = map[string]*mockTier{}
}

// SetTierOutOfBand changes a tier as the portal would: its pin (nil = auto)
// and enabled flag.
func (m *MockAPI) SetTierOutOfBand(key string, pinned *string, enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.gateway.tiers[key]
	t.pinned, t.enabled = pinned, enabled
	ts := now()
	t.updatedAt = &ts
}

// Tier is the wire form of a tier.
func (m *MockAPI) Tier(key string) (map[string]any, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t := m.gateway.tiers[key]
	if t == nil {
		return nil, false
	}
	return m.tierWire(t, nil), true
}

// AddProject registers a project of a tenant (for a key's project_id) and
// returns its id.
func (m *MockAPI) AddProject(tenantID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gateway.nextProj++
	m.gateway.projects[m.gateway.nextProj] = tenantID
	return strconv.Itoa(m.gateway.nextProj)
}

// GatewayKey is the wire form of a key as a read of it would answer (live
// values when the gateway has it).
func (m *MockAPI) GatewayKey(id string) (map[string]any, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.gateway.keys[id]
	if k == nil || k.deleted {
		return nil, false
	}
	live := m.gateway.live[k.alias]
	state := "missing"
	if live != nil {
		state = "present"
	}
	return m.keyWire(k, live, state, nil, nil), true
}

// SecretValue is the stored value of a key alias ("" when none).
func (m *MockAPI) SecretValue(alias string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.gateway.secrets[alias]
}

// OnGateway reports whether the gateway holds a key with this alias.
func (m *MockAPI) OnGateway(alias string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.gateway.live[alias] != nil
}

// LoseGatewayKey removes a key from the gateway only, as a gateway that lost it.
func (m *MockAPI) LoseGatewayKey(alias string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.gateway.live, alias)
}

// SetGatewayKeyLimits changes a key's rpm limit and spend on the gateway
// out of band.
func (m *MockAPI) SetGatewayKeyLimits(alias string, rpm int64, spend float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l := m.gateway.live[alias]
	l.rpm, l.spend = &rpm, spend
}

// AddAdoptedKey registers a key the gateway already had (origin adopted) for
// a tenant and returns its id.
func (m *MockAPI) AddAdoptedKey(tenantID, alias string, models ...string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	value := newSecret()
	ts := now()
	k := &mockKey{id: uuid.NewString(), org: tenantID, env: "prod", app: "adopted", alias: alias, models: models,
		origin: "adopted", hash: hashOf(value), createdAt: ts, updatedAt: ts}
	m.gateway.keys[k.id] = k
	m.gateway.live[alias] = &gatewayKey{models: models, hash: k.hash}
	return k.id
}

// AddUnregisteredGatewayKey puts a key on the gateway that the platform has
// not registered (an alias a create must refuse).
func (m *MockAPI) AddUnregisteredGatewayKey(alias string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.gateway.live[alias] = &gatewayKey{models: []string{"general"}, hash: hashOf(newSecret())}
}

// liveKeysOf counts a tenant's live (not deleted) keys: they keep it from
// being deleted.
func (s *gatewayState) liveKeysOf(tenantID string) int {
	n := 0
	for _, k := range s.keys {
		if !k.deleted && k.org == tenantID {
			n++
		}
	}
	return n
}

func newSecret() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return "sk-" + hex.EncodeToString(b)
}

func hashOf(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

// ── wire forms ───────────────────────────────────────────────────────────────

func (m *MockAPI) resolve(t *mockTier) (resolved bool, served *string, source string) {
	loaded := func(model string) bool {
		for _, c := range m.gateway.candidates {
			if c == model {
				return true
			}
		}
		return false
	}
	switch {
	case !t.enabled:
		// Assumed: a disabled tier resolves to nothing.
		return false, nil, "none"
	case t.pinned != nil && loaded(*t.pinned):
		return true, t.pinned, "pin"
	case t.pinned != nil:
		return false, nil, "pin-offline"
	case loaded(t.auto):
		auto := t.auto
		return true, &auto, "auto"
	}
	return false, nil, "none"
}

func (m *MockAPI) tierWire(t *mockTier, warnings []map[string]any) map[string]any {
	if warnings == nil {
		warnings = []map[string]any{}
	}
	resolved, served, source := m.resolve(t)
	var role, pinned, model, updated any
	if t.role != nil {
		role = *t.role
	}
	if t.pinned != nil {
		pinned = *t.pinned
	}
	if served != nil {
		model = *served
	}
	updated = tierCreatedAt
	if t.createdAt != "" {
		updated = t.createdAt
	}
	created := updated
	if t.updatedAt != nil {
		updated = *t.updatedAt
	}
	return map[string]any{
		"key": t.key, "label": t.label, "category": t.category, "sort": t.sort, "role": role,
		"pinned_model": pinned, "enabled": t.enabled, "resolved": resolved, "resolved_model": model,
		"source": source, "candidate_models": append([]string{}, m.gateway.candidates...),
		"created_at": created, "updated_at": updated, "warnings": warnings,
	}
}

// tierCreatedAt is when the mock's tiers were created.
const tierCreatedAt = "2026-09-01T08:00:00Z"

func (m *MockAPI) keyWire(k *mockKey, live *gatewayKey, state string, secret *string, warnings []map[string]any) map[string]any {
	if warnings == nil {
		warnings = []map[string]any{}
	}
	models, rpm, tpm, budget, duration := k.models, k.rpm, k.tpm, k.budget, k.duration
	var spend any
	if live != nil && state == "present" {
		models, rpm, tpm, budget, duration = live.models, live.rpm, live.tpm, live.budget, live.duration
		spend = live.spend
	}
	opt := func(p *string) any {
		if p == nil {
			return nil
		}
		return *p
	}
	optI := func(p *int64) any {
		if p == nil {
			return nil
		}
		return *p
	}
	var budgetV, secretV, prefix any
	if budget != nil {
		budgetV = *budget
	}
	if secret != nil {
		secretV = *secret
	}
	if k.hash != "" {
		prefix = k.hash[:12]
	}
	var createdBy any
	if k.createdBy != "" {
		createdBy = k.createdBy
	}
	return map[string]any{
		"id": k.id, "organization_id": k.org, "project_id": opt(k.project), "env": k.env, "app": k.app,
		"feature": opt(k.feature), "key_alias": k.alias, "models": append([]string{}, models...),
		"rpm_limit": optI(rpm), "tpm_limit": optI(tpm), "soft_budget_usd": budgetV,
		"budget_duration": opt(duration), "spend_usd": spend, "live": state,
		"secret_path": opt(k.secretPath), "secret_field": opt(k.secretField), "origin": k.origin,
		"token_hash_prefix": prefix, "created_by": createdBy, "created_at": k.createdAt,
		"updated_at": k.updatedAt, "rotated_at": opt(k.rotatedAt), "secret": secretV, "warnings": warnings,
	}
}

// ── routing ──────────────────────────────────────────────────────────────────

func (m *MockAPI) routeGateway(c *call) reply {
	parts := strings.Split(strings.Trim(c.path, "/"), "/")
	method := c.r.Method
	switch {
	case len(parts) == 2 && method == http.MethodGet:
		return m.gatewayGet(c)
	case len(parts) == 3 && parts[2] == "tiers" && method == http.MethodGet:
		return m.tiersList(c)
	case len(parts) == 4 && parts[2] == "tiers":
		switch method {
		case http.MethodGet:
			return m.tiersGet(c, parts[3])
		case http.MethodPut:
			return m.tiersPut(c, parts[3])
		}
	case len(parts) == 3 && parts[2] == "keys":
		switch method {
		case http.MethodGet:
			return m.keysList(c)
		case http.MethodPost:
			return m.keysCreate(c)
		}
	case len(parts) == 4 && parts[2] == "keys":
		switch method {
		case http.MethodGet:
			return m.keysGet(c, parts[3])
		case http.MethodPatch:
			return m.keysUpdate(c, parts[3])
		case http.MethodDelete:
			return m.keysDelete(c, parts[3])
		}
	case len(parts) == 5 && parts[2] == "keys" && parts[4] == "rotations" && method == http.MethodPost:
		return m.keysRotate(c, parts[3])
	default:
		return c.problem(http.StatusNotFound, "not_found", "", nil)
	}
	return c.methodNotAllowed()
}

func (m *MockAPI) gatewayRead(c *call) *reply {
	return m.require(c, "ai-gateway-read-global", "ai-gateway-admin-global")
}
func (m *MockAPI) gatewayWrite(c *call) *reply { return m.require(c, "ai-gateway-admin-global") }

// needGateway is the 503 of a route that needs the gateway.
func (m *MockAPI) needGateway(c *call) *reply {
	switch m.gateway.status {
	case "not_configured":
		r := c.problem(http.StatusServiceUnavailable, "gateway_not_configured",
			"The AI gateway is not configured on this platform.", nil)
		return &r
	case "unreachable":
		r := c.problem(http.StatusServiceUnavailable, "gateway_unreachable", "The AI gateway cannot be reached.", nil)
		r.headers = map[string]string{"Retry-After": "30"}
		return &r
	}
	return nil
}

func (m *MockAPI) gatewayGet(c *call) reply {
	if r := m.gatewayRead(c); r != nil {
		return *r
	}
	if r := m.needGateway(c); r != nil {
		return *r
	}
	tiers := make([]string, 0, len(m.gateway.tiers))
	for k := range m.gateway.tiers {
		tiers = append(tiers, k)
	}
	sort.Strings(tiers)
	return ok(http.StatusOK, map[string]any{"base_url": GatewayBaseURL, "tiers": tiers})
}

// ── tiers ────────────────────────────────────────────────────────────────────

func (m *MockAPI) tiersList(c *call) reply {
	if r := m.gatewayRead(c); r != nil {
		return *r
	}
	p, bad := c.pageParams()
	if bad != nil {
		return *bad
	}
	after := ""
	if p.after != nil {
		s, isString := p.after["key"].(string)
		if !isString || s == "" {
			return invalidCursor(c)
		}
		after = s
	}
	keys := make([]string, 0, len(m.gateway.tiers))
	for k := range m.gateway.tiers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var items []map[string]any
	for _, k := range keys {
		if after != "" && k <= after {
			continue
		}
		items = append(items, m.tierWire(m.gateway.tiers[k], nil))
		if len(items) > p.limit {
			break
		}
	}
	return ok(http.StatusOK, page(items, p.limit, func(it map[string]any) map[string]any {
		return map[string]any{"key": it["key"]}
	}))
}

func (m *MockAPI) tiersGet(c *call, key string) reply {
	if r := m.gatewayRead(c); r != nil {
		return *r
	}
	t := m.gateway.tiers[key]
	if t == nil {
		return c.problem(http.StatusNotFound, "tier_not_found", "No such tier.", nil)
	}
	return ok(http.StatusOK, m.tierWire(t, nil))
}

func (m *MockAPI) tiersPut(c *call, key string) reply {
	if r := m.gatewayWrite(c); r != nil {
		return *r
	}
	v := &validation{}
	body := decodeBody(c, v, "pinned_model", "enabled", "allow_unloaded_pin")
	pin, pinSent := strField(v, body, "pinned_model", false, true, pattern(rxModel))
	enabled, enabledSent := boolField(v, body, "enabled", false)
	allow, _ := boolField(v, body, "allow_unloaded_pin", false)
	if !pinSent && !enabledSent {
		v.fail("", "send pinned_model and/or enabled")
	}
	if r := v.reply(c); r != nil {
		return *r
	}
	t := m.gateway.tiers[key]
	if t == nil {
		return c.problem(http.StatusNotFound, "tier_not_found", "No such tier.", nil)
	}
	var warnings []map[string]any
	if pinSent && pin != nil && (t.pinned == nil || *t.pinned != *pin) {
		loaded := false
		for _, cand := range m.gateway.candidates {
			loaded = loaded || cand == *pin
		}
		if !loaded {
			if allow == nil || !*allow {
				return c.problem(http.StatusUnprocessableEntity, "model_not_loaded",
					fmt.Sprintf("'%s' is not loaded on the fleet; pinning it would take the tier offline. Set allow_unloaded_pin to insist.", *pin),
					map[string]any{"field": "pinned_model", "candidates": append([]string{}, m.gateway.candidates...)})
			}
			warnings = append(warnings, map[string]any{"code": "model_not_loaded",
				"message": *pin + " is not loaded: the tier serves nothing until it is."})
		}
	}
	if pinSent {
		t.pinned = pin
	}
	if enabledSent {
		t.enabled = *enabled
	}
	ts := now()
	t.updatedAt = &ts
	if m.gateway.status != "ok" {
		warnings = append(warnings, map[string]any{"code": "reconcile_not_applied",
			"message": "The gateway could not be reconciled now; the periodic reconcile applies the change."})
	}
	return ok(http.StatusOK, m.tierWire(t, warnings))
}

// ── keys ─────────────────────────────────────────────────────────────────────

func (m *MockAPI) keysList(c *call) reply {
	if r := m.gatewayRead(c); r != nil {
		return *r
	}
	p, bad := c.pageParams()
	if bad != nil {
		return *bad
	}
	q := c.r.URL.Query()
	after := ""
	if p.after != nil {
		s, isString := p.after["key_alias"].(string)
		if !isString || s == "" {
			return invalidCursor(c)
		}
		after = s
	}
	all := make([]*mockKey, 0, len(m.gateway.keys))
	for _, k := range m.gateway.keys {
		if !k.deleted {
			all = append(all, k)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].alias < all[j].alias })
	var items []map[string]any
	for _, k := range all {
		switch {
		case after != "" && k.alias <= after,
			q.Has("organization_id") && k.org != strings.ToLower(q.Get("organization_id")),
			q.Has("env") && k.env != q.Get("env"),
			q.Has("app") && k.app != q.Get("app"),
			q.Has("origin") && k.origin != q.Get("origin"),
			q.Has("key_alias") && k.alias != q.Get("key_alias"):
			continue
		}
		items = append(items, m.keyWire(k, nil, "not_read", nil, nil))
		if len(items) > p.limit {
			break
		}
	}
	return ok(http.StatusOK, page(items, p.limit, func(it map[string]any) map[string]any {
		return map[string]any{"key_alias": it["key_alias"]}
	}))
}

func (m *MockAPI) loadKey(c *call, raw string) (*mockKey, *reply) {
	id, valid := uuidID(raw)
	k := m.gateway.keys[id]
	if !valid || k == nil || k.deleted {
		r := c.problem(http.StatusNotFound, "key_not_found", "No such key.", nil)
		return nil, &r
	}
	return k, nil
}

// checkModels is 422 unknown_tier for a name the catalogue lacks, and a
// tier_not_serving warning for one that serves nothing now.
func (m *MockAPI) checkModels(c *call, models []string) ([]map[string]any, *reply) {
	var unknown, idle []string
	for _, name := range models {
		t := m.gateway.tiers[name]
		if t == nil {
			unknown = append(unknown, name)
			continue
		}
		if resolved, _, _ := m.resolve(t); !resolved {
			idle = append(idle, name)
		}
	}
	if len(unknown) > 0 {
		accepted := make([]string, 0, len(m.gateway.tiers))
		for k := range m.gateway.tiers {
			accepted = append(accepted, k)
		}
		sort.Strings(accepted)
		r := c.problem(http.StatusUnprocessableEntity, "unknown_tier",
			"Not in the serving-tier catalogue: "+strings.Join(unknown, ", ")+". Accepted tiers: "+strings.Join(accepted, ", ")+".",
			map[string]any{"field": "models", "unknown": unknown, "tiers": accepted})
		return nil, &r
	}
	if len(idle) == 0 {
		return nil, nil
	}
	return []map[string]any{{"code": "tier_not_serving", "message": strings.Join(idle, ", ") +
		" exist(s) but serve(s) nothing right now (disabled, or no loaded model resolves it); calls to it fail until one does."}}, nil
}

func (m *MockAPI) checkProject(c *call, project *string, org string) *reply {
	if project == nil {
		return nil
	}
	n, _ := strconv.Atoi(*project)
	owner, found := m.gateway.projects[n]
	if !found {
		r := c.problem(http.StatusUnprocessableEntity, "project_not_found", "project_id names no project.", map[string]any{"field": "project_id"})
		return &r
	}
	if owner != org {
		r := c.problem(http.StatusUnprocessableEntity, "project_not_in_organization", "The project belongs to another tenant.",
			map[string]any{"field": "project_id"})
		return &r
	}
	return nil
}

func modelsField(v *validation, body map[string]any, required bool) ([]string, bool) {
	raw, present := body["models"]
	if !present {
		if required {
			v.fail("models", "Field required")
		}
		return nil, false
	}
	list, isList := raw.([]any)
	if !isList {
		v.fail("models", "Input should be a valid list")
		return nil, true
	}
	if len(list) < 1 || len(list) > 50 {
		v.fail("models", "List should have between 1 and 50 items")
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(list))
	for _, item := range list {
		s, isString := item.(string)
		if !isString || !rxSegment.MatchString(s) {
			v.fail("models", fmt.Sprintf("%v is not a tier name", item))
			continue
		}
		if !seen[s] {
			out = append(out, s)
		}
		seen[s] = true
	}
	sort.Strings(out) // a set: sorted, each tier once
	return out, true
}

func intRange(max int) func(int) string {
	return func(n int) string {
		if n < 1 || n > max {
			return fmt.Sprintf("Input should be between 1 and %d", max)
		}
		return ""
	}
}

func numberField(v *validation, body map[string]any, field string) (*float64, bool) {
	raw, present := body[field]
	if !present {
		return nil, false
	}
	if raw == nil {
		return nil, true
	}
	f, isNumber := raw.(float64)
	if !isNumber || f <= 0 || f > 1e9 {
		v.fail(field, "Input should be greater than 0 and at most 1000000000")
		return nil, true
	}
	return &f, true
}

func ptr64(p *int) *int64 {
	if p == nil {
		return nil
	}
	n := int64(*p)
	return &n
}

// replaySafe stores, for an idempotent replay, the answer without the key's
// value and with a secret_not_replayed warning.
func (m *MockAPI) replaySafe(k *mockKey, state string, warnings []map[string]any) map[string]any {
	replay := m.keyWire(k, nil, state, nil, append(append([]map[string]any{}, warnings...), map[string]any{
		"code": "secret_not_replayed", "message": "This is a replay of an earlier request: the key's value was returned only the first time. Read it from Vault at secret_path."}))
	return replay
}

func (m *MockAPI) keysCreate(c *call) reply {
	if r := m.gatewayWrite(c); r != nil {
		return *r
	}
	if r := m.needGateway(c); r != nil {
		return *r // the gateway is checked before the body
	}
	v := &validation{}
	body := decodeBody(c, v, "organization_id", "project_id", "env", "app", "feature", "models", "rpm_limit",
		"tpm_limit", "soft_budget_usd", "budget_duration", "expose_secret")
	org, _ := strField(v, body, "organization_id", true, false, pattern(rxUUID))
	project, _ := strField(v, body, "project_id", false, true, pattern(rxIntID))
	env, _ := strField(v, body, "env", true, false, oneOf("dev", "uat", "prod"))
	app, _ := strField(v, body, "app", true, false, func(s string) string {
		if msg := length(1, 40)(s); msg != "" {
			return msg
		}
		return pattern(rxSegment)(s)
	})
	feature, _ := strField(v, body, "feature", false, true, func(s string) string {
		if msg := length(1, 40)(s); msg != "" {
			return msg
		}
		return pattern(rxSegment)(s)
	})
	models, _ := modelsField(v, body, true)
	rpm, _ := intField(v, body, "rpm_limit", true, intRange(1_000_000_000))
	tpm, _ := intField(v, body, "tpm_limit", true, intRange(2_000_000_000))
	budget, _ := numberField(v, body, "soft_budget_usd")
	duration, _ := strField(v, body, "budget_duration", false, true, pattern(rxDuration))
	expose, _ := boolField(v, body, "expose_secret", false)
	if r := v.reply(c); r != nil {
		return *r
	}
	orgID := strings.ToLower(*org)
	tenant := m.tenancy.tenants[orgID]
	if tenant == nil {
		return c.problem(http.StatusUnprocessableEntity, "organization_not_found", "organization_id names no tenant.",
			map[string]any{"field": "organization_id"})
	}
	if r := m.checkProject(c, project, orgID); r != nil {
		return *r
	}
	warnings, bad := m.checkModels(c, models)
	if bad != nil {
		return *bad
	}
	parts := []string{tenant.slug, *env, *app}
	if feature != nil {
		parts = append(parts, *feature)
	}
	alias := strings.Join(parts, "-")
	for _, k := range m.gateway.keys {
		if !k.deleted && k.alias == alias {
			return c.problem(http.StatusConflict, "key_alias_taken", "A live key is already named "+alias+".", nil)
		}
	}
	if m.gateway.live[alias] != nil {
		return c.problem(http.StatusConflict, "key_alias_on_gateway",
			"The gateway already has a key named "+alias+" that is not registered; adopt it.", nil)
	}
	value := newSecret()
	ts := now()
	path := "platform/ai-gateway/keys/" + alias // Assumed: the real mount and prefix differ.
	field := "value"
	k := &mockKey{id: uuid.NewString(), org: orgID, project: project, env: *env, app: *app, feature: feature,
		alias: alias, models: models, rpm: ptr64(rpm), tpm: ptr64(tpm), budget: budget, duration: duration,
		secretPath: &path, secretField: &field, origin: "api", hash: hashOf(value), createdBy: m.principalID(),
		createdAt: ts, updatedAt: ts}
	m.gateway.keys[k.id] = k
	m.gateway.live[alias] = &gatewayKey{models: models, rpm: k.rpm, tpm: k.tpm, budget: budget, duration: duration, hash: k.hash}
	m.gateway.secrets[alias] = value
	var secret *string
	if expose != nil && *expose {
		secret = &value
	}
	// Every write answers live not_read and spend null; a read has the live values.
	rep := ok(http.StatusCreated, m.keyWire(k, nil, "not_read", secret, warnings))
	if secret != nil {
		rep.replay = m.replaySafe(k, "not_read", warnings)
	}
	return rep
}

func (m *MockAPI) keysGet(c *call, raw string) reply {
	if r := m.gatewayRead(c); r != nil {
		return *r
	}
	k, bad := m.loadKey(c, raw)
	if bad != nil {
		return *bad
	}
	if r := m.needGateway(c); r != nil {
		return *r
	}
	live := m.gateway.live[k.alias]
	if live == nil {
		return ok(http.StatusOK, m.keyWire(k, nil, "missing", nil, []map[string]any{{
			"code": "key_missing_on_gateway", "message": "The gateway no longer has this key; it cannot be used."}}))
	}
	return ok(http.StatusOK, m.keyWire(k, live, "present", nil, nil))
}

func (m *MockAPI) keysUpdate(c *call, raw string) reply {
	if r := m.gatewayWrite(c); r != nil {
		return *r
	}
	if r := patchMediaType(c); r != nil {
		return *r
	}
	v := &validation{}
	body := decodeBody(c, v, "project_id", "models", "rpm_limit", "tpm_limit", "soft_budget_usd", "budget_duration",
		"organization_id", "env", "app", "feature")
	project, projectSent := strField(v, body, "project_id", false, true, pattern(rxIntID))
	models, modelsSent := modelsField(v, body, false)
	if raw, present := body["models"]; present && raw == nil {
		v.fail("models", "models cannot be null; omit a field to leave it unchanged")
	}
	rpm, rpmSent := intField(v, body, "rpm_limit", true, intRange(1_000_000_000))
	tpm, tpmSent := intField(v, body, "tpm_limit", true, intRange(2_000_000_000))
	budget, budgetSent := numberField(v, body, "soft_budget_usd")
	duration, durationSent := strField(v, body, "budget_duration", false, true, pattern(rxDuration))
	for _, f := range []string{"organization_id", "app", "feature"} {
		strField(v, body, f, false, true, nil)
	}
	strField(v, body, "env", false, true, oneOf("dev", "uat", "prod"))
	if r := v.reply(c); r != nil {
		return *r
	}
	k, bad := m.loadKey(c, raw)
	if bad != nil {
		return *bad
	}
	feature := ""
	if k.feature != nil {
		feature = *k.feature
	}
	for _, f := range []struct {
		name, current string
		null          bool
	}{{"organization_id", k.org, false}, {"env", k.env, false}, {"app", k.app, false}, {"feature", feature, k.feature == nil}} {
		if sent, present := body[f.name]; present && frozenDiffers(sent, f.current, f.null) {
			return c.problem(http.StatusUnprocessableEntity, "immutable_field", f.name+" cannot be changed after create.",
				map[string]any{"field": f.name})
		}
	}
	var warnings []map[string]any
	if modelsSent && models != nil {
		w, bad := m.checkModels(c, models)
		if bad != nil {
			return *bad
		}
		warnings = w
	}
	if projectSent {
		if r := m.checkProject(c, project, k.org); r != nil {
			return *r
		}
	}
	gatewayChange := modelsSent || rpmSent || tpmSent || budgetSent || durationSent
	if !gatewayChange && !projectSent {
		return ok(http.StatusOK, m.keyWire(k, nil, "not_read", nil, nil))
	}
	if gatewayChange {
		if r := m.needGateway(c); r != nil {
			return *r
		}
		if m.gateway.live[k.alias] == nil {
			return c.problem(http.StatusConflict, "key_missing_on_gateway", "The gateway no longer has this key.", nil)
		}
	}
	if projectSent {
		k.project = project
	}
	if modelsSent {
		k.models = models
	}
	if rpmSent {
		k.rpm = ptr64(rpm)
	}
	if tpmSent {
		k.tpm = ptr64(tpm)
	}
	if budgetSent {
		k.budget = budget
	}
	if durationSent {
		k.duration = duration
	}
	if gatewayChange {
		l := m.gateway.live[k.alias]
		l.models, l.rpm, l.tpm, l.budget, l.duration = k.models, k.rpm, k.tpm, k.budget, k.duration
	}
	k.updatedAt = now()
	return ok(http.StatusOK, m.keyWire(k, nil, "not_read", nil, warnings))
}

func (m *MockAPI) keysRotate(c *call, raw string) reply {
	if r := m.gatewayWrite(c); r != nil {
		return *r
	}
	v := &validation{}
	body := map[string]any{}
	if len(strings.TrimSpace(string(c.body))) > 0 {
		body = decodeBody(c, v, "expose_secret")
	}
	expose, _ := boolField(v, body, "expose_secret", false)
	if r := v.reply(c); r != nil {
		return *r
	}
	k, bad := m.loadKey(c, raw)
	if bad != nil {
		return *bad
	}
	if k.origin != "api" {
		return c.problem(http.StatusConflict, "key_adopted",
			"An adopted key's value lives in its consumer's own secret; rotate it where that consumer is deployed.", nil)
	}
	if r := m.needGateway(c); r != nil {
		return *r
	}
	live := m.gateway.live[k.alias]
	if live == nil {
		return c.problem(http.StatusConflict, "key_missing_on_gateway", "The gateway no longer has this key.", nil)
	}
	value := newSecret()
	k.hash, live.hash = hashOf(value), hashOf(value)
	ts := now()
	k.rotatedAt, k.updatedAt = &ts, ts
	m.gateway.secrets[k.alias] = value
	var secret *string
	if expose != nil && *expose {
		secret = &value
	}
	rep := ok(http.StatusOK, m.keyWire(k, nil, "not_read", secret, nil))
	if secret != nil {
		rep.replay = m.replaySafe(k, "not_read", nil)
	}
	return rep
}

func (m *MockAPI) keysDelete(c *call, raw string) reply {
	if r := m.gatewayWrite(c); r != nil {
		return *r
	}
	k, bad := m.loadKey(c, raw)
	if bad != nil {
		return *bad
	}
	if r := m.needGateway(c); r != nil {
		return *r
	}
	delete(m.gateway.live, k.alias)
	if k.origin == "api" {
		delete(m.gateway.secrets, k.alias)
	}
	k.deleted = true
	return reply{status: http.StatusNoContent}
}
