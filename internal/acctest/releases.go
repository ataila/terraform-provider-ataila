// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package acctest

import (
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// The release endpoints of /api/v1, with the platform's rules:
//
//   - a promotion is a REQUEST: into prod it waits (awaiting_approval) until a
//     person decides in the portal (ApproveRelease / RejectRelease here); into
//     dev and uat it starts at once. The API cannot approve.
//   - Kubernetes projects only (422 vm_projects_unsupported); the component
//     must exist (422 component_not_enabled); dev needs a version (422
//     version_required); uat and prod take what the environment below last
//     reported (422 version_not_at_source).
//   - the mock moves a started promotion one step per poll of its operation
//     (approved → running → succeeded); under dispatch mode dryrun it never
//     completes, as on every estate that fakes dispatch.
//   - the PROD data lock: unlocking needs the project's short name.

var rxReleaseVersion = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,63}$`)

var releaseSource = map[string]string{"dev": "sandbox", "uat": "dev", "prod": "uat"}

type mockReleaseOp struct {
	id, project                      int
	operation, component             string
	source, target, version          string
	status                           string // the portal's: pending approved running succeeded failed rejected
	requestedBy, requestedAt         string
	requestedVia, requestedToken     string
	decidedBy, decidedAt, reason     string
	startedAt, completedAt, errorMsg string
	reaped                           bool
	pipeline                         string
}

type mockLock struct{ at, by string }

type releasesState struct {
	ops      map[int]*mockReleaseOp
	next     int
	reported map[int]map[string]map[string]string // project -> env -> component -> version
	locks    map[int]mockLock
	dispatch string // "live" or "dryrun"
	failNext bool
	// A person decides a waiting PROD request after this many polls of its
	// operation (0: nobody decides).
	decideAfter int
	approve     bool
}

func newReleasesState() *releasesState {
	return &releasesState{ops: map[int]*mockReleaseOp{}, next: 1, reported: map[int]map[string]map[string]string{},
		locks: map[int]mockLock{}, dispatch: "live"}
}

// ── test helpers ─────────────────────────────────────────────────────────────

// SetReleaseDispatch sets how releases alone are dispatched ("live" or
// "dryrun"), leaving /meta as it is: the provider's check after a 202.
func (m *MockAPI) SetReleaseDispatch(mode string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.releases.dispatch = mode
}

// FailNextRelease makes the next release that runs fail.
func (m *MockAPI) FailNextRelease() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.releases.failNext = true
}

// ReportVersion records what an environment runs, as a release pipeline does.
func (m *MockAPI) ReportVersion(projectID, env, component, version string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, _ := strconv.Atoi(projectID)
	m.report(n, env, component, version)
}

func (m *MockAPI) report(project int, env, component, version string) {
	r := m.releases.reported
	if r[project] == nil {
		r[project] = map[string]map[string]string{}
	}
	if r[project][env] == nil {
		r[project][env] = map[string]string{}
	}
	r[project][env][component] = version
}

// ApproveRelease approves a request in the portal (it then runs).
func (m *MockAPI) ApproveRelease(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, _ := strconv.Atoi(id)
	o := m.releases.ops[n]
	o.status, o.decidedBy, o.decidedAt = "approved", "approver@example.com", now()
}

// RejectRelease rejects a request in the portal.
func (m *MockAPI) RejectRelease(id, note string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, _ := strconv.Atoi(id)
	o := m.releases.ops[n]
	o.status, o.decidedBy, o.decidedAt, o.completedAt = "rejected", "approver@example.com", now(), now()
	o.reason = "Please promote. (requested through the API)\n--\n" + note
}

// DecideAfter makes a person approve (or reject) the next waiting PROD
// request after that many polls of its operation.
func (m *MockAPI) DecideAfter(polls int, approve bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.releases.decideAfter, m.releases.approve = polls, approve
}

// AddDataCopy books a data copy in the portal and returns its id.
func (m *MockAPI) AddDataCopy(projectID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, _ := strconv.Atoi(projectID)
	o := &mockReleaseOp{id: m.releases.next, project: n, operation: "copy_data", source: "prod", target: "uat",
		status: "succeeded", requestedBy: "operator@example.com", requestedAt: now(), completedAt: now()}
	m.releases.next++
	m.releases.ops[o.id] = o
	return strconv.Itoa(o.id)
}

// ReleaseOperation is the wire form of a release operation.
func (m *MockAPI) ReleaseOperation(id string) (map[string]any, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, _ := strconv.Atoi(id)
	o := m.releases.ops[n]
	if o == nil {
		return nil, false
	}
	return releaseOpWire(o), true
}

// ReleaseOperationCount is how many release operations exist.
func (m *MockAPI) ReleaseOperationCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.releases.ops)
}

// ── wire ─────────────────────────────────────────────────────────────────────

var releaseWireStatus = map[string]string{"pending": "awaiting_approval", "approved": "pending",
	"running": "running", "succeeded": "succeeded", "failed": "failed", "rejected": "failed"}

func releaseOpWire(o *mockReleaseOp) map[string]any {
	var errReason any
	switch o.status {
	case "rejected":
		parts := strings.Split(o.reason, "\n--\n")
		errReason = strings.TrimSpace(parts[len(parts)-1])
	case "failed":
		errReason = o.errorMsg
	}
	var component any
	if o.component != "" {
		component = o.component
	}
	return map[string]any{
		"id": strconv.Itoa(o.id), "operation_id": fmt.Sprintf("release:%d", o.id),
		"project_id": strconv.Itoa(o.project), "operation": o.operation, "component": component,
		"source_env": o.source, "target_env": o.target, "version": nullStr(o.version),
		"status": releaseWireStatus[o.status], "portal_status": o.status, "requested_by": o.requestedBy,
		"requested_at": o.requestedAt, "requested_via": nullStr(o.requestedVia),
		"requested_token_id": nullStr(o.requestedToken), "decided_by": nullStr(o.decidedBy),
		"decided_at": nullStr(o.decidedAt), "approval_reason": nullStr(o.reason),
		"started_at": nullStr(o.startedAt), "completed_at": nullStr(o.completedAt),
		"error_reason": errReason, "pipeline_url": nullStr(o.pipeline),
	}
}

func (m *MockAPI) releaseOperationWire(o *mockReleaseOp) map[string]any {
	status := releaseWireStatus[o.status]
	var errV any
	switch o.status {
	case "rejected":
		errV = map[string]any{"code": "rejected", "message": releaseOpWire(o)["error_reason"]}
	case "failed":
		code := "release_failed"
		if o.reaped {
			code = "operation_timed_out"
		}
		errV = map[string]any{"code": code, "message": o.errorMsg}
	}
	msg := map[string]string{"awaiting_approval": "Waiting for a person to approve it in the portal's Release Manager.",
		"pending": "Approved; waiting to be picked up.", "running": "Running."}[status]
	if m.releases.dispatch == "dryrun" && (status == "pending" || status == "running") {
		msg = "Dry run: this platform fakes the dispatch and nothing is executed, so the operation will not complete."
	}
	updated := o.requestedAt
	for _, t := range []string{o.completedAt, o.startedAt, o.decidedAt} {
		if t != "" {
			updated = t
			break
		}
	}
	return map[string]any{
		"id": fmt.Sprintf("release:%d", o.id), "kind": "release", "status": status,
		"resource_type": "release_operation", "resource_id": strconv.Itoa(o.id), "created_at": o.requestedAt,
		"updated_at": updated, "message": nullStr(msg), "error": errV, "dispatch_mode": m.releases.dispatch,
		"pipeline_url": nullStr(o.pipeline),
	}
}

// advanceRelease moves a started operation by one step.
func (m *MockAPI) advanceRelease(o *mockReleaseOp) {
	if o.operation != "promote_build" {
		return
	}
	ts := now()
	switch o.status {
	case "pending":
		if m.releases.decideAfter > 0 {
			m.releases.decideAfter--
			if m.releases.decideAfter == 0 {
				o.decidedBy, o.decidedAt = "approver@example.com", ts
				if m.releases.approve {
					o.status = "approved"
				} else {
					o.status, o.completedAt = "rejected", ts
					o.reason += "\n--\nNot this week."
				}
			}
		}
	case "approved":
		o.status, o.startedAt = "running", ts
		if m.releases.dispatch == "dryrun" {
			o.pipeline = "https://gitlab.example.com/pipelines/900000001"
		} else {
			o.pipeline = fmt.Sprintf("https://gitlab.example.com/pipelines/%d", 1000+o.id)
		}
	case "running":
		if m.releases.dispatch == "dryrun" {
			return
		}
		if m.releases.failNext {
			m.releases.failNext = false
			o.status, o.completedAt, o.errorMsg = "failed", ts, "deploy job failed"
			return
		}
		o.status, o.completedAt = "succeeded", ts
		m.report(o.project, o.target, o.component, o.version)
	}
}

// ── routes ───────────────────────────────────────────────────────────────────

func (m *MockAPI) releasesRead(c *call) *reply {
	return m.require(c, "release-manager-read-global", "release-manager-admin-global",
		"release-manager-read-tenant", "release-manager-admin-tenant")
}

func (m *MockAPI) releasesWrite(c *call) *reply {
	return m.require(c, "release-manager-admin-global", "release-manager-admin-tenant")
}

func (m *MockAPI) routeProjectReleases(c *call, raw, what string) reply {
	method := c.r.Method
	switch {
	case what == "release-promotions" && method == http.MethodPost:
		return m.releasePromote(c, raw)
	case what == "release-state" && method == http.MethodGet:
		return m.releaseStateGet(c, raw)
	case what == "release-operations" && method == http.MethodGet:
		return m.releaseOpsList(c, raw)
	case what == "prod-lock" && method == http.MethodGet:
		return m.prodLockGet(c, raw)
	case what == "prod-lock" && method == http.MethodPut:
		return m.prodLockPut(c, raw)
	}
	return c.methodNotAllowed()
}

func (m *MockAPI) releasePromote(c *call, raw string) reply {
	if r := m.releasesWrite(c); r != nil {
		return *r
	}
	v := &validation{}
	body := decodeBody(c, v, "component", "target_env", "version")
	component, _ := strField(v, body, "component", true, false, oneOf("app-api", "www"))
	target, _ := strField(v, body, "target_env", true, false, oneOf("dev", "uat", "prod"))
	version, _ := strField(v, body, "version", false, true, pattern(rxReleaseVersion))
	if r := v.reply(c); r != nil {
		return *r
	}
	p, bad := m.loadProject(c, raw)
	if bad != nil {
		return *bad
	}
	if p.backend != "k8s" {
		return c.problem(http.StatusUnprocessableEntity, "vm_projects_unsupported",
			p.short+" is a VM project. Releases of VM projects cannot be requested through the API.", nil)
	}
	flag := map[string]string{"app-api": "enable_fullstack_app", "www": "enable_static_site"}[*component]
	if on, _ := p.settings[flag].(bool); !on {
		return c.problem(http.StatusUnprocessableEntity, "component_not_enabled",
			fmt.Sprintf("The project has no %s component (%s is off).", *component, flag),
			map[string]any{"component": *component})
	}
	source := releaseSource[*target]
	resolved := ""
	if *target == "dev" {
		if version == nil {
			return c.problem(http.StatusUnprocessableEntity, "version_required",
				"A promotion into dev deploys a build and must name its version.", nil)
		}
		resolved = *version
	} else {
		at := m.releases.reported[p.id][source][*component]
		if at == "" {
			return c.problem(http.StatusUnprocessableEntity, "version_not_at_source",
				fmt.Sprintf("%s reports no %s version, so there is nothing tested to promote into %s.",
					source, *component, *target), map[string]any{"source_env": source, "reported_version": nil})
		}
		if version != nil && *version != at {
			return c.problem(http.StatusUnprocessableEntity, "version_not_at_source",
				fmt.Sprintf("%s is not what %s runs: %s last reported %s.", *version, source, source, at),
				map[string]any{"source_env": source, "reported_version": at})
		}
		resolved = at
	}
	principal, _ := m.whoami["principal"].(map[string]any)
	email, _ := principal["email"].(string)
	via, tokenID := "session", ""
	if kind, _ := m.whoami["auth_kind"].(string); kind == "pat" || kind == "service_account" {
		via = kind
	}
	if tok, isToken := m.whoami["token"].(map[string]any); isToken && tok != nil {
		tokenID, _ = tok["id"].(string)
	}
	o := &mockReleaseOp{id: m.releases.next, project: p.id, operation: "promote_build", component: *component,
		source: source, target: *target, version: resolved, status: "approved", requestedBy: email,
		requestedAt: now(), requestedVia: via, requestedToken: tokenID}
	if *target == "prod" {
		o.status = "pending"
		o.reason = "Requested through the API."
	}
	m.releases.next++
	m.releases.ops[o.id] = o
	if o.status == "approved" {
		m.advanceRelease(o) // dev and uat start at once
	}
	rep := ok(http.StatusAccepted, m.releaseOperationWire(o))
	rep.headers = map[string]string{"Location": fmt.Sprintf("/api/v1/operations/release:%d", o.id)}
	return rep
}

// releaseOperation answers GET /operations/release:<n>, moving it on first.
func (m *MockAPI) releaseOperation(c *call, native string) reply {
	if r := m.releasesRead(c); r != nil {
		return c.problem(http.StatusNotFound, "operation_not_found", "No such operation.", nil)
	}
	n, err := strconv.Atoi(native)
	o := m.releases.ops[n]
	if err != nil || o == nil {
		return c.problem(http.StatusNotFound, "operation_not_found", "No such operation.", nil)
	}
	m.advanceRelease(o)
	return ok(http.StatusOK, m.releaseOperationWire(o))
}

func (m *MockAPI) releaseOperationGet(c *call, raw string) reply {
	if r := m.releasesRead(c); r != nil {
		return *r
	}
	n, valid := intID(raw)
	o := m.releases.ops[n]
	if !valid || o == nil {
		return c.problem(http.StatusNotFound, "release_operation_not_found", "No such release operation.", nil)
	}
	return ok(http.StatusOK, releaseOpWire(o))
}

func (m *MockAPI) releaseStateGet(c *call, raw string) reply {
	if r := m.releasesRead(c); r != nil {
		return *r
	}
	p, bad := m.loadProject(c, raw)
	if bad != nil {
		return *bad
	}
	versions := []map[string]any{}
	envs := make([]string, 0)
	for env := range m.releases.reported[p.id] {
		envs = append(envs, env)
	}
	sort.Strings(envs)
	for _, env := range envs {
		comps := make([]string, 0)
		for comp := range m.releases.reported[p.id][env] {
			comps = append(comps, comp)
		}
		sort.Strings(comps)
		for _, comp := range comps {
			var src any
			if s, known := releaseSource[env]; known {
				src = s
			}
			versions = append(versions, map[string]any{"env": env, "component": comp,
				"last_reported_version": m.releases.reported[p.id][env][comp], "last_reported_at": now(),
				"last_reported_by": "pipeline:4242", "source_env": src})
		}
	}
	pending, inFlight := []string{}, []string{}
	ids := make([]int, 0)
	for id, o := range m.releases.ops {
		if o.project == p.id {
			ids = append(ids, id)
		}
	}
	sort.Ints(ids)
	for _, id := range ids {
		switch m.releases.ops[id].status {
		case "pending":
			pending = append(pending, fmt.Sprintf("release:%d", id))
		case "approved", "running":
			inFlight = append(inFlight, fmt.Sprintf("release:%d", id))
		}
	}
	lock := m.releases.locks[p.id]
	return ok(http.StatusOK, map[string]any{
		"project_id": strconv.Itoa(p.id), "deployment_backend": p.backend, "versions": versions,
		"prod_data_locked": lock.at != "", "prod_data_locked_at": nullStr(lock.at),
		"prod_data_locked_by": nullStr(lock.by), "prior_prod_data_copies": 0,
		"pending_operation_ids": pending, "in_flight_operation_ids": inFlight,
	})
}

func (m *MockAPI) releaseOpsList(c *call, raw string) reply {
	if r := m.releasesRead(c); r != nil {
		return *r
	}
	pp, bad := c.pageParams()
	if bad != nil {
		return *bad
	}
	p, bad := m.loadProject(c, raw)
	if bad != nil {
		return *bad
	}
	before := 1 << 30
	if pp.after != nil {
		f, _ := pp.after["id"].(float64)
		before = int(f)
	}
	ids := make([]int, 0)
	for id, o := range m.releases.ops {
		if o.project == p.id && id < before {
			ids = append(ids, id)
		}
	}
	sort.Sort(sort.Reverse(sort.IntSlice(ids)))
	var items []map[string]any
	for _, id := range ids {
		items = append(items, releaseOpWire(m.releases.ops[id]))
		if len(items) > pp.limit {
			break
		}
	}
	return ok(http.StatusOK, page(items, pp.limit, func(it map[string]any) map[string]any {
		n, _ := strconv.Atoi(it["id"].(string))
		return map[string]any{"id": n}
	}))
}

func (m *MockAPI) prodLockWire(p *mockProject) map[string]any {
	lock := m.releases.locks[p.id]
	return map[string]any{"project_id": strconv.Itoa(p.id), "locked": lock.at != "",
		"locked_at": nullStr(lock.at), "locked_by": nullStr(lock.by)}
}

func (m *MockAPI) prodLockGet(c *call, raw string) reply {
	if r := m.releasesRead(c); r != nil {
		return *r
	}
	p, bad := m.loadProject(c, raw)
	if bad != nil {
		return *bad
	}
	return ok(http.StatusOK, m.prodLockWire(p))
}

func (m *MockAPI) prodLockPut(c *call, raw string) reply {
	if r := m.releasesWrite(c); r != nil {
		return *r
	}
	v := &validation{}
	body := decodeBody(c, v, "locked", "confirm_unlock")
	locked, _ := boolField(v, body, "locked", false)
	if _, present := body["locked"]; !present {
		v.fail("locked", "Field required")
	}
	confirm, _ := strField(v, body, "confirm_unlock", false, true, length(0, 64))
	if r := v.reply(c); r != nil {
		return *r
	}
	p, bad := m.loadProject(c, raw)
	if bad != nil {
		return *bad
	}
	if !*locked && (confirm == nil || *confirm != p.short) {
		return c.problem(http.StatusUnprocessableEntity, "unlock_not_confirmed",
			"Unlocking PROD data needs confirm_unlock equal to the project's short name.", nil)
	}
	isLocked := m.releases.locks[p.id].at != ""
	if isLocked != *locked {
		if *locked {
			principal, _ := m.whoami["principal"].(map[string]any)
			email, _ := principal["email"].(string)
			m.releases.locks[p.id] = mockLock{at: now(), by: email}
		} else {
			delete(m.releases.locks, p.id)
		}
	}
	return ok(http.StatusOK, m.prodLockWire(p))
}
