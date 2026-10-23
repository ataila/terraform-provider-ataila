// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package acctest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// A project's backups (platform 1.0.399), as routers/project_backups.py serves
// them: the plan is replaced as a whole (every member required; tiers null =
// follow the installation default) or returned to the default by DELETE; a
// plan never stored is the default at version 0 and a write stores it at 1; a
// replace that changes nothing keeps the version; switching an enabled plan off
// is destroy-class for a token; a retired project refuses a replace and ignores
// a reset. Sets and exports are read only, newest first.

var (
	rxBackupTime  = regexp.MustCompile(`^([01]\d|2[0-3]):([0-5]\d)$`)
	backupEnvs    = []string{"dev", "uat", "prod"}
	backupParts   = []string{"code", "database", "files"}
	backupWeekday = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}
)

type mockBackupPlan struct {
	enabled    bool
	envs       map[string]any
	tiers      map[string]any // the project's own; read only while overridden
	overridden bool
	version    int // 0 = never stored
	updatedAt  string
	bumpOnRead bool // the next read is followed by a change in the portal
}

type mockBackupItem struct {
	project int
	created string
	wire    map[string]any
}

type backupState struct {
	plans        map[int]*mockBackupPlan
	defaultTiers map[string]any
	sets         []*mockBackupItem
	exports      []*mockBackupItem
}

func defaultBackupEnvs() map[string]any {
	return map[string]any{
		"dev":  map[string]any{"code": true, "database": true, "files": false},
		"uat":  map[string]any{"code": false, "database": false, "files": false},
		"prod": map[string]any{"code": true, "database": true, "files": true},
	}
}

func builtinBackupTiers() map[string]any {
	return map[string]any{
		"daily":   map[string]any{"enabled": true, "keep": 7, "at": "02:00"},
		"weekly":  map[string]any{"enabled": true, "keep": 4, "day": "sun", "at": nil},
		"monthly": map[string]any{"enabled": true, "keep": 6, "day": 1, "at": nil},
		"yearly":  map[string]any{"enabled": true, "keep": 3, "at": nil},
	}
}

// deepCopy copies a JSON-shaped value; numbers come back as float64.
func deepCopy(v map[string]any) map[string]any {
	b, _ := json.Marshal(v)
	out := map[string]any{}
	_ = json.Unmarshal(b, &out)
	return out
}

func sameJSON(a, b any) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	var va, vb any
	_ = json.Unmarshal(ja, &va)
	_ = json.Unmarshal(jb, &vb)
	return reflect.DeepEqual(va, vb)
}

func (s *backupState) init() {
	if s.plans == nil {
		s.plans = map[int]*mockBackupPlan{}
		s.defaultTiers = builtinBackupTiers()
	}
}

// plan is the project's plan; a project without one has the default, unstored.
func (s *backupState) plan(pid int) *mockBackupPlan {
	s.init()
	if p := s.plans[pid]; p != nil {
		return p
	}
	return &mockBackupPlan{enabled: true, envs: defaultBackupEnvs()}
}

func (s *backupState) resolvedTiers(p *mockBackupPlan) map[string]any {
	if p.overridden {
		return deepCopy(p.tiers)
	}
	return deepCopy(s.defaultTiers)
}

func (s *backupState) wire(pid int, p *mockBackupPlan) map[string]any {
	var updated any
	if p.version > 0 {
		updated = p.updatedAt
	}
	return map[string]any{
		"project_id": strconv.Itoa(pid), "enabled": p.enabled, "environments": deepCopy(p.envs),
		"tiers_overridden": p.overridden, "tiers": s.resolvedTiers(p), "default_tiers": deepCopy(s.defaultTiers),
		"default_environments": defaultBackupEnvs(),
		"is_default":           p.enabled && sameJSON(p.envs, defaultBackupEnvs()) && !p.overridden,
		"version":              p.version, "updated_at": updated,
	}
}

// store writes a plan the way save_plan does: a new plan at version 1, a
// changed one at version + 1, an unchanged one as it is. It reports whether
// anything changed.
func (s *backupState) store(pid int, enabled bool, envs, tiers map[string]any) bool {
	cur := s.plan(pid)
	next := &mockBackupPlan{enabled: enabled, envs: deepCopy(envs), overridden: tiers != nil, version: cur.version}
	if tiers != nil {
		next.tiers = deepCopy(tiers)
	}
	if cur.version > 0 && cur.enabled == next.enabled && sameJSON(cur.envs, next.envs) &&
		cur.overridden == next.overridden && sameJSON(s.resolvedTiers(cur), s.resolvedTiers(next)) {
		return false
	}
	next.version++
	next.updatedAt = now()
	next.bumpOnRead = cur.bumpOnRead
	s.plans[pid] = next
	return true
}

// ── test helpers ──────────────────────────────────────────────────────────────

func (m *MockAPI) mockProjectID(id string) int {
	pid, err := strconv.Atoi(id)
	if err != nil {
		panic("acctest: not a project id: " + id)
	}
	return pid
}

// SeedBackupPlan stores the default plan of a project at version 1, as the
// platform does when it provisions one.
func (m *MockAPI) SeedBackupPlan(projectID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.backups.store(m.mockProjectID(projectID), true, defaultBackupEnvs(), nil)
}

// SetBackupPlanInPortal changes a project's plan as the portal would: enabled,
// one environment's database switch, and (dailyKeep > 0) its own tiers.
func (m *MockAPI) SetBackupPlanInPortal(projectID string, enabled bool, uatDatabase bool, dailyKeep int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pid := m.mockProjectID(projectID)
	envs := deepCopy(m.backups.plan(pid).envs)
	envs["uat"].(map[string]any)["database"] = uatDatabase
	var tiers map[string]any
	if dailyKeep > 0 {
		tiers = builtinBackupTiers()
		tiers["daily"].(map[string]any)["keep"] = dailyKeep
	}
	m.backups.store(pid, enabled, envs, tiers)
}

// ChangeBackupPlanAfterNextRead makes the portal change the project's plan
// right after the next read of it, between a provider's read and its write.
func (m *MockAPI) ChangeBackupPlanAfterNextRead(projectID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pid := m.mockProjectID(projectID)
	m.backups.init()
	if m.backups.plans[pid] == nil {
		m.backups.store(pid, true, defaultBackupEnvs(), nil)
	}
	m.backups.plans[pid].bumpOnRead = true
}

// SetDefaultBackupTiers changes the installation's default tiers (Settings ›
// Backups): every plan that follows the default follows it.
func (m *MockAPI) SetDefaultBackupTiers(dailyKeep int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.backups.init()
	m.backups.defaultTiers["daily"].(map[string]any)["keep"] = dailyKeep
}

// BackupPlan is the wire form of a project's plan.
func (m *MockAPI) BackupPlan(projectID string) map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	pid := m.mockProjectID(projectID)
	return m.backups.wire(pid, m.backups.plan(pid))
}

func (m *MockAPI) nextBackupStamp() string {
	n := len(m.backups.sets) + len(m.backups.exports)
	return time.Date(2026, 10, 1, 2, 0, 0, 0, time.UTC).Add(time.Duration(n) * time.Hour).Format(time.RFC3339)
}

// AddBackupSet records a set of the project and returns its id.
func (m *MockAPI) AddBackupSet(projectID, env, tier, status string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.backups.init()
	id, at := uuid.NewString(), m.nextBackupStamp()
	var finished, errText, size any
	if status == "success" || status == "failed" || status == "pruned" {
		finished = at
	}
	if status == "failed" {
		errText = "the database dump failed"
	} else if finished != nil {
		size = 1048576
	}
	m.backups.sets = append(m.backups.sets, &mockBackupItem{project: m.mockProjectID(projectID), created: at,
		wire: map[string]any{"id": id, "project_id": projectID, "environment": env, "tier": tier,
			"components": []any{"code", "database"}, "status": status, "attempt": 1, "bytes": size,
			"has_checksums": status == "success", "created_at": at, "started_at": at, "finished_at": finished,
			"pruned_at": nil, "error": errText}})
	return id
}

// AddBackupExport records an export of the project and returns its id.
func (m *MockAPI) AddBackupExport(projectID, status string, envs ...string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.backups.init()
	id, at := uuid.NewString(), m.nextBackupStamp()
	list := make([]any, 0, len(envs))
	for _, e := range envs {
		list = append(list, e)
	}
	var size, sum, expires any
	if status == "success" {
		size, sum, expires = 4096, strings.Repeat("ab", 32), "2026-10-31T00:00:00Z"
	}
	m.backups.exports = append(m.backups.exports, &mockBackupItem{project: m.mockProjectID(projectID), created: at,
		wire: map[string]any{"id": id, "project_id": projectID, "status": status, "environments": list,
			"database": "both", "files": true, "source_set_ids": []any{}, "contents": []any{"manifest.json",
				"RUNBOOK.md", "SHA256SUMS"}, "bytes": size, "sha256": sum, "expires_at": expires, "created_at": at,
			"started_at": at, "finished_at": nil, "error": nil}})
	return id
}

// ── routes ───────────────────────────────────────────────────────────────────

func (m *MockAPI) backupsRead(c *call) *reply {
	return m.require(c, "project-backup-read-global", "project-backup-admin-global")
}

func (m *MockAPI) routeProjectBackups(c *call, raw, what string) reply {
	method := c.r.Method
	switch {
	case what == "backup-plan" && method == http.MethodGet:
		return m.backupPlanGet(c, raw)
	case what == "backup-plan" && method == http.MethodPut:
		return m.backupPlanPut(c, raw)
	case what == "backup-plan" && method == http.MethodDelete:
		return m.backupPlanDelete(c, raw)
	case (what == "backup-sets" || what == "backup-exports") && method == http.MethodGet:
		return m.backupList(c, raw, what)
	}
	return c.methodNotAllowed()
}

func (m *MockAPI) backupPlanReply(c *call, pid int) reply {
	p := m.backups.plan(pid)
	rep := ok(http.StatusOK, m.backups.wire(pid, p))
	rep.headers = map[string]string{"ETag": fmt.Sprintf(`"%d"`, p.version)}
	return rep
}

func (m *MockAPI) backupPlanGet(c *call, raw string) reply {
	if r := m.backupsRead(c); r != nil {
		return *r
	}
	p, bad := m.loadProject(c, raw)
	if bad != nil {
		return *bad
	}
	rep := m.backupPlanReply(c, p.id)
	if plan := m.backups.plans[p.id]; plan != nil && plan.bumpOnRead {
		plan.bumpOnRead = false
		envs := deepCopy(plan.envs)
		envs["uat"].(map[string]any)["files"] = !envs["uat"].(map[string]any)["files"].(bool)
		var tiers map[string]any
		if plan.overridden {
			tiers = plan.tiers
		}
		m.backups.store(p.id, plan.enabled, envs, tiers)
	}
	return rep
}

// object checks that raw is an object with exactly the members named.
func backupObject(v *validation, field string, raw any, members []string) map[string]any {
	obj, isObject := raw.(map[string]any)
	if !isObject {
		v.fail(field, "Input should be a valid dictionary or object")
		return nil
	}
	for _, k := range members {
		if _, present := obj[k]; !present {
			v.fail(field+"."+k, "Field required")
		}
	}
	for k := range obj {
		if !contains(members, k) {
			v.fail(field+"."+k, "Extra inputs are not permitted")
		}
	}
	return obj
}

func backupTier(v *validation, name string, raw any) {
	members := map[string][]string{"daily": {"enabled", "keep", "at"}, "weekly": {"enabled", "keep", "day", "at"},
		"monthly": {"enabled", "keep", "day", "at"}, "yearly": {"enabled", "keep", "at"}}[name]
	field := "tiers." + name
	t := backupObject(v, field, raw, members)
	if t == nil {
		return
	}
	if _, isBool := t["enabled"].(bool); !isBool {
		v.fail(field+".enabled", "Input should be a valid boolean")
	}
	keepMax := map[string]int{"daily": 366, "weekly": 104, "monthly": 120, "yearly": 30}[name]
	if k, isNumber := t["keep"].(float64); !isNumber || k != float64(int(k)) || k < 1 || int(k) > keepMax {
		v.fail(field+".keep", fmt.Sprintf("Input should be a whole number from 1 to %d", keepMax))
	}
	at, present := t["at"]
	if s, isString := at.(string); present && (at != nil || name == "daily") && (!isString || !rxBackupTime.MatchString(s)) {
		v.fail(field+".at", "String should match pattern '"+rxBackupTime.String()+"'")
	}
	switch name {
	case "weekly":
		if d, _ := t["day"].(string); !contains(backupWeekday, d) {
			v.fail(field+".day", "Input should be 'mon', 'tue', 'wed', 'thu', 'fri', 'sat' or 'sun'")
		}
	case "monthly":
		if d, isNumber := t["day"].(float64); !isNumber || d != float64(int(d)) || d < 1 || d > 28 {
			v.fail(field+".day", "Input should be a whole number from 1 to 28")
		}
	}
}

func (m *MockAPI) backupPlanPut(c *call, raw string) reply {
	if r := m.require(c, "project-backup-admin-global"); r != nil {
		return *r
	}
	p, bad := m.loadProject(c, raw)
	if bad != nil {
		return *bad
	}
	expected := -1
	if h := strings.TrimSpace(c.r.Header.Get("If-Match")); h != "" && h != "*" {
		match := rxIfMatch.FindStringSubmatch(h)
		if match == nil {
			return c.problem(http.StatusBadRequest, "invalid_if_match",
				`If-Match must be the plan's version as an entity tag, e.g. "7".`, nil)
		}
		expected, _ = strconv.Atoi(match[2])
	}
	v := &validation{}
	body := decodeBody(c, v, "enabled", "environments", "tiers")
	for _, k := range []string{"enabled", "environments", "tiers"} {
		if _, present := body[k]; !present {
			v.fail(k, "Field required")
		}
	}
	enabled, isBool := body["enabled"].(bool)
	if _, present := body["enabled"]; present && !isBool {
		v.fail("enabled", "Input should be a valid boolean")
	}
	var envs map[string]any
	if rawEnvs, present := body["environments"]; present {
		envs = backupObject(v, "environments", rawEnvs, backupEnvs)
		for _, e := range backupEnvs {
			if comps := backupObject(v, "environments."+e, envs[e], backupParts); comps != nil {
				for _, part := range backupParts {
					if _, isBool := comps[part].(bool); !isBool {
						v.fail("environments."+e+"."+part, "Input should be a valid boolean")
					}
				}
			}
		}
	}
	var tiers map[string]any
	if rawTiers := body["tiers"]; rawTiers != nil {
		tiers = backupObject(v, "tiers", rawTiers, []string{"daily", "weekly", "monthly", "yearly"})
		for _, name := range []string{"daily", "weekly", "monthly", "yearly"} {
			if tiers != nil {
				backupTier(v, name, tiers[name])
			}
		}
	}
	if r := v.reply(c); r != nil {
		return *r
	}
	if p.status == "retired" {
		return c.problem(http.StatusConflict, "project_retired", "The project is retired.", nil)
	}
	cur := m.backups.plan(p.id)
	if expected >= 0 && expected != cur.version {
		return c.problem(http.StatusPreconditionFailed, "version_mismatch",
			fmt.Sprintf("The plan is at version %d, not %d.", cur.version, expected),
			map[string]any{"current_version": cur.version})
	}
	if !enabled && cur.enabled && m.isToken() && !m.tokenAllowsDestroy() {
		return c.problem(http.StatusForbidden, "destroy_not_allowed",
			"Switching a project's backups off is destroy-class: this API token was not created with allow_destroy.", nil)
	}
	m.backups.store(p.id, enabled, envs, tiers)
	return m.backupPlanReply(c, p.id)
}

func (m *MockAPI) backupPlanDelete(c *call, raw string) reply {
	if r := m.require(c, "project-backup-admin-global"); r != nil {
		return *r
	}
	p, bad := m.loadProject(c, raw)
	if bad != nil {
		return *bad
	}
	if p.status != "retired" {
		m.backups.store(p.id, true, defaultBackupEnvs(), nil)
	}
	return reply{status: http.StatusNoContent}
}

func (m *MockAPI) backupList(c *call, raw, what string) reply {
	if r := m.backupsRead(c); r != nil {
		return *r
	}
	p, bad := m.loadProject(c, raw)
	if bad != nil {
		return *bad
	}
	q := c.r.URL.Query()
	filters := map[string][]string{"status": {"queued", "running", "success", "failed", "pruned", "expired"}}
	items := m.backups.exports
	if what == "backup-sets" {
		items = m.backups.sets
		filters["environment"] = backupEnvs
		filters["tier"] = []string{"daily", "weekly", "monthly", "yearly", "manual"}
	}
	for name, allowed := range filters {
		if got := q.Get(name); got != "" && !contains(allowed, got) {
			return c.problem(http.StatusUnprocessableEntity, "validation_failed",
				name+" is not one of the values this list filters on.", nil)
		}
	}
	limit := 50
	if l := q.Get("limit"); l != "" {
		limit, _ = strconv.Atoi(l)
	}
	if limit < 1 || limit > 200 {
		return c.problem(http.StatusUnprocessableEntity, "validation_failed", "limit must be 1-200.", nil)
	}
	var mine []map[string]any
	for _, it := range items {
		w := it.wire
		if it.project != p.id || (q.Get("status") != "" && w["status"] != q.Get("status")) ||
			(q.Get("environment") != "" && w["environment"] != q.Get("environment")) ||
			(q.Get("tier") != "" && w["tier"] != q.Get("tier")) {
			continue
		}
		mine = append(mine, w)
	}
	sort.SliceStable(mine, func(i, j int) bool { return mine[i]["created_at"].(string) > mine[j]["created_at"].(string) })
	start := 0
	if cur := q.Get("cursor"); cur != "" {
		n, err := strconv.Atoi(strings.TrimPrefix(cur, what+":"))
		if err != nil || !strings.HasPrefix(cur, what+":") || n < 0 || n > len(mine) {
			return c.problem(http.StatusBadRequest, "invalid_cursor", "The cursor is not valid.", nil)
		}
		start = n
	}
	end := min(start+limit, len(mine))
	page := make([]any, 0, end-start)
	for _, w := range mine[start:end] {
		page = append(page, w)
	}
	var next any
	if end < len(mine) {
		next = fmt.Sprintf("%s:%d", what, end)
	}
	return ok(http.StatusOK, map[string]any{"items": page, "next_cursor": next})
}
