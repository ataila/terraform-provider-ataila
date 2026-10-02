// Copyright (c) 2026 ATAILA Kft.
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

// The AI model catalogue, node caches, store runs and AI Center of /api/v1,
// with the platform's rules:
//
//   - a model is a catalogue row, created `planned`; weights arrive only
//     through the store actions. `repo` is frozen; `status`, `location`,
//     `offline_ready` and `nas_volume` are read-only (422
//     read_only_field when a PATCH changes them, and create refuses them);
//   - delete removes the row only, and is refused (409, with `blockers`)
//     while a store run is active, a node holds a cache, or a central copy
//     exists (location synology/both, or status owned/serving);
//   - caching needs a central copy (409 no_central_copy) and a roster node
//     (422 unknown_node); one store run per model (409 run_in_progress);
//     uncaching is refused while the model is loaded (409
//     model_loaded_on_node) and while monitoring cannot be read (503
//     loaded_state_unknown);
//   - the mock moves a store run one step per poll of its operation; under
//     dispatch mode dryrun a run never completes (the platform then fails it);
//   - AI Center reads never fail: without monitoring they answer the roster
//     with monitoring_reachable false.

var rxHFRepo = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)

// The metadata a client may write, with its kind ("s" string, "f" number,
// "b" bool, "m" object).
var modelFields = map[string]string{
	"display_name": "s", "org": "s", "vendor": "s", "vendor_country": "s", "params": "s", "param_count_b": "f",
	"architecture": "s", "quant": "s", "size_gb": "f", "published": "s", "license": "s", "gateway_tier": "s",
	"category": "s", "serving_node": "s", "gated": "b", "summary": "s", "description": "s",
	"context_window": "s", "min_target": "s", "strong_axis": "s", "frontier_equiv": "s", "benchmarks": "m",
	"model_card_url": "s", "notes": "s",
}

var modelReadOnly = []string{"status", "location", "offline_ready", "nas_volume"}

type mockModel struct {
	id                 int
	repo               string
	fields             map[string]any
	status             string
	location, volume   any
	createdAt, updated string
}

type mockRun struct {
	id, model        int
	node, action     string
	status, dispatch string
	started, ended   string
}

type aiState struct {
	models     map[int]*mockModel
	nextModel  int
	caches     map[int]map[string]string // model -> node -> state
	runs       map[int]*mockRun
	nextRun    int
	nodes      []string
	loaded     map[string][]string // node -> repos loaded
	monitoring bool
	dispatch   string
	failNext   bool
	clusters   []map[string]any
	catalog    []map[string]any
}

func newAIState() *aiState {
	return &aiState{models: map[int]*mockModel{}, nextModel: 1, caches: map[int]map[string]string{},
		runs: map[int]*mockRun{}, nextRun: 1, loaded: map[string][]string{}, dispatch: "live"}
}

// ── test helpers ─────────────────────────────────────────────────────────────

// AddAINode adds a node to the AI roster.
func (m *MockAPI) AddAINode(hostname string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ai.nodes = append(m.ai.nodes, hostname)
	sort.Strings(m.ai.nodes)
}

// SetMonitoring sets whether monitoring can be read.
func (m *MockAPI) SetMonitoring(reachable bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ai.monitoring = reachable
}

// SetStoreDispatch sets how store runs alone are dispatched ("live" or
// "dryrun"), leaving /meta as it is: the provider's check after a 202.
func (m *MockAPI) SetStoreDispatch(mode string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ai.dispatch = mode
}

// FailNextStoreRun makes the next store run that runs fail.
func (m *MockAPI) FailNextStoreRun() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ai.failNext = true
}

// GiveCentralCopy records a central copy of a model (as a pull would).
func (m *MockAPI) GiveCentralCopy(modelID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, _ := strconv.Atoi(modelID)
	md := m.ai.models[n]
	md.location, md.volume, md.status = "synology", "models-2", "owned"
}

// DropCentralCopy removes the central copy record.
func (m *MockAPI) DropCentralCopy(modelID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, _ := strconv.Atoi(modelID)
	md := m.ai.models[n]
	md.location, md.volume, md.status = nil, nil, "planned"
}

// LoadOnNode marks a model as loaded on a node.
func (m *MockAPI) LoadOnNode(node, repo string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ai.loaded[node] = append(m.ai.loaded[node], repo)
}

// AIModel is the wire form of a model.
func (m *MockAPI) AIModel(id string) (map[string]any, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, _ := strconv.Atoi(id)
	md := m.ai.models[n]
	if md == nil {
		return nil, false
	}
	return m.modelWire(md), true
}

// NodeCacheState is a model's cache state on a node ("" when none).
func (m *MockAPI) NodeCacheState(modelID, node string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, _ := strconv.Atoi(modelID)
	return m.ai.caches[n][node]
}

// SetNodeCache records a cache out of band ("" removes it).
func (m *MockAPI) SetNodeCache(modelID, node, state string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, _ := strconv.Atoi(modelID)
	if state == "" {
		delete(m.ai.caches[n], node)
		return
	}
	if m.ai.caches[n] == nil {
		m.ai.caches[n] = map[string]string{}
	}
	m.ai.caches[n][node] = state
}

// AddDGXCluster and AddLaunchEntry seed AI Center.
func (m *MockAPI) AddDGXCluster(name string, members ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var ms []map[string]any
	for i, h := range members {
		role := "worker"
		if i == 0 {
			role = "head"
		}
		ms = append(ms, map[string]any{"hostname": h, "role": role, "crosslink_ip": nil, "mgmt_ip": nil})
	}
	m.ai.clusters = append(m.ai.clusters, map[string]any{"id": strconv.Itoa(len(m.ai.clusters) + 1), "name": name,
		"topology": "pair", "interconnect": "qsfp", "members": ms, "crosslink_subnet": nil, "status": "active",
		"serve":         map[string]any{"recipe": "recipe-a", "model": "example/model-a", "served_at": nil},
		"error_message": nil, "notes": nil, "created_at": now(), "updated_at": now()})
}

// AddLaunchEntry adds a launch catalogue entry.
func (m *MockAPI) AddLaunchEntry(key, host, model string, enabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ai.catalog = append(m.ai.catalog, map[string]any{"key": key, "host": host, "label": "Label " + key,
		"model": model, "engine": "vllm", "port": 8000, "enabled": enabled})
}

// ── models ───────────────────────────────────────────────────────────────────

func (m *MockAPI) modelWire(md *mockModel) map[string]any {
	out := map[string]any{}
	for k := range modelFields {
		out[k] = nil
	}
	out["gated"] = false
	for k, v := range md.fields {
		out[k] = v
	}
	central := md.location == "synology" || md.location == "both"
	var caches []map[string]any
	nodes := make([]string, 0)
	for n := range m.ai.caches[md.id] {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)
	for _, n := range nodes {
		caches = append(caches, map[string]any{"node": n, "state": m.ai.caches[md.id][n], "size_gb": 12.5})
	}
	if caches == nil {
		caches = []map[string]any{}
	}
	var path any
	if central {
		path = "models/" + md.repo
	}
	for k, v := range map[string]any{
		"id": strconv.Itoa(md.id), "repo": md.repo, "status": md.status, "location": md.location,
		"offline_ready": len(nodes) > 0, "nas_volume": md.volume, "nas_path": path, "dgx_recipe": nil,
		"node_caches": caches, "created_at": md.createdAt, "updated_at": md.updated,
	} {
		out[k] = v
	}
	return out
}

func (m *MockAPI) aiRead(c *call) *reply {
	return m.require(c, "ai-models-read-global", "ai-models-admin-global")
}
func (m *MockAPI) aiWrite(c *call) *reply { return m.require(c, "ai-models-admin-global") }

func (m *MockAPI) routeAIModels(c *call) reply {
	parts := strings.Split(strings.Trim(c.path, "/"), "/")
	method := c.r.Method
	switch {
	case len(parts) == 1 && method == http.MethodGet:
		return m.modelsList(c)
	case len(parts) == 1 && method == http.MethodPost:
		return m.modelsCreate(c)
	case len(parts) == 2 && parts[1] == "storage" && method == http.MethodGet:
		if r := m.aiRead(c); r != nil {
			return *r
		}
		return ok(http.StatusOK, map[string]any{"shares": []string{"models-2", "models-3"},
			"nas": []any{}, "nodes": []any{}, "captured_at": nil})
	case len(parts) == 2 && parts[1] == "load-targets" && method == http.MethodGet:
		if r := m.aiRead(c); r != nil {
			return *r
		}
		items := []map[string]any{
			{"hostname": "ai-a", "online": false, "status": "unknown", "gpu_count": 2, "tensor_parallel": 2,
				"per_gpu_gb": 24, "vram_total_gb": 48, "usable_vram_gb": 43.2, "loaded_models": []any{},
				"loadable": true, "engine": nil, "is_cluster": false, "members": nil},
			{"hostname": "dgx-pair", "online": false, "status": "unknown", "gpu_count": 2, "tensor_parallel": 2,
				"per_gpu_gb": 128, "vram_total_gb": 256, "usable_vram_gb": 230.4, "loaded_models": []any{},
				"loadable": false, "engine": "spark-vllm", "is_cluster": true, "members": []any{"dgx-1", "dgx-2"}},
		}
		return ok(http.StatusOK, page(items, 50, func(it map[string]any) map[string]any { return nil }))
	case len(parts) == 3 && parts[1] == "runs" && method == http.MethodGet:
		return c.problem(http.StatusNotFound, "run_not_found", "No such run.", nil)
	case len(parts) == 2 && method == http.MethodGet:
		return m.modelsGet(c, parts[1])
	case len(parts) == 2 && method == http.MethodPatch:
		return m.modelsUpdate(c, parts[1])
	case len(parts) == 2 && method == http.MethodDelete:
		return m.modelsDelete(c, parts[1])
	case len(parts) == 4 && parts[2] == "node-caches":
		switch method {
		case http.MethodGet:
			return m.cacheGet(c, parts[1], parts[3])
		case http.MethodPut:
			return m.cachePut(c, parts[1], parts[3])
		case http.MethodDelete:
			return m.cacheDelete(c, parts[1], parts[3])
		}
	}
	if len(parts) <= 4 {
		return c.methodNotAllowed()
	}
	return c.problem(http.StatusNotFound, "not_found", "", nil)
}

func (m *MockAPI) loadModel(c *call, raw string) (*mockModel, *reply) {
	n, valid := intID(raw)
	md := m.ai.models[n]
	if !valid || md == nil {
		r := c.problem(http.StatusNotFound, "model_not_found", "No such model.", nil)
		return nil, &r
	}
	return md, nil
}

// modelFieldValues validates the metadata members of a body.
func modelFieldValues(v *validation, body map[string]any, patch bool) map[string]any {
	out := map[string]any{}
	for k, kind := range modelFields {
		raw, present := body[k]
		if !present {
			continue
		}
		if raw == nil {
			if patch && k != "display_name" && k != "gated" {
				out[k] = nil
			} else if patch {
				v.fail(k, k+" cannot be null; omit a field to leave it unchanged")
			}
			continue
		}
		okType := false
		switch kind {
		case "s":
			_, okType = raw.(string)
		case "f":
			_, okType = raw.(float64)
		case "b":
			_, okType = raw.(bool)
		case "m":
			_, okType = raw.(map[string]any)
		}
		if !okType {
			v.fail(k, "Input has the wrong type")
			continue
		}
		out[k] = raw
	}
	return out
}

func (m *MockAPI) modelsList(c *call) reply {
	if r := m.aiRead(c); r != nil {
		return *r
	}
	p, bad := c.pageParams()
	if bad != nil {
		return *bad
	}
	after := 0
	if p.after != nil {
		f, _ := p.after["id"].(float64)
		after = int(f)
	}
	q := c.r.URL.Query()
	ids := make([]int, 0)
	for id := range m.ai.models {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	var items []map[string]any
	for _, id := range ids {
		md := m.ai.models[id]
		w := m.modelWire(md)
		if id <= after || (q.Has("repo") && md.repo != q.Get("repo")) || (q.Has("status") && md.status != q.Get("status")) ||
			(q.Has("category") && w["category"] != q.Get("category")) ||
			(q.Has("gateway_tier") && w["gateway_tier"] != q.Get("gateway_tier")) {
			continue
		}
		items = append(items, w)
		if len(items) > p.limit {
			break
		}
	}
	return ok(http.StatusOK, page(items, p.limit, func(it map[string]any) map[string]any {
		n, _ := strconv.Atoi(it["id"].(string))
		return map[string]any{"id": n}
	}))
}

func (m *MockAPI) modelsCreate(c *call) reply {
	if r := m.aiWrite(c); r != nil {
		return *r
	}
	v := &validation{}
	allowed := []string{"repo"}
	for k := range modelFields {
		allowed = append(allowed, k)
	}
	body := decodeBody(c, v, allowed...)
	repo, _ := strField(v, body, "repo", true, false, func(s string) string {
		s = strings.TrimSpace(s)
		if !rxHFRepo.MatchString(s) || strings.Contains(s, "..") || len(s) > 96 {
			return "repo must be a Hugging Face repo id 'org/name' (letters, digits, '.', '_' and '-' only)"
		}
		return ""
	})
	fields := modelFieldValues(v, body, false)
	if r := v.reply(c); r != nil {
		return *r
	}
	r := strings.TrimSpace(*repo)
	for _, md := range m.ai.models {
		if md.repo == r {
			return c.problem(http.StatusConflict, "repo_taken", r+" is already in the catalogue.", nil)
		}
	}
	if _, set := fields["display_name"]; !set {
		fields["display_name"] = r[strings.LastIndex(r, "/")+1:]
	}
	ts := now()
	md := &mockModel{id: m.ai.nextModel, repo: r, fields: fields, status: "planned", createdAt: ts, updated: ts}
	m.ai.nextModel++
	m.ai.models[md.id] = md
	return ok(http.StatusCreated, m.modelWire(md))
}

func (m *MockAPI) modelsGet(c *call, raw string) reply {
	if r := m.aiRead(c); r != nil {
		return *r
	}
	md, bad := m.loadModel(c, raw)
	if bad != nil {
		return *bad
	}
	return ok(http.StatusOK, m.modelWire(md))
}

func (m *MockAPI) modelsUpdate(c *call, raw string) reply {
	if r := m.aiWrite(c); r != nil {
		return *r
	}
	if r := patchMediaType(c); r != nil {
		return *r
	}
	v := &validation{}
	allowed := append([]string{"repo"}, modelReadOnly...)
	for k := range modelFields {
		allowed = append(allowed, k)
	}
	body := decodeBody(c, v, allowed...)
	fields := modelFieldValues(v, body, true)
	if r := v.reply(c); r != nil {
		return *r
	}
	md, bad := m.loadModel(c, raw)
	if bad != nil {
		return *bad
	}
	if sent, present := body["repo"]; present && sent != md.repo {
		return c.problem(http.StatusUnprocessableEntity, "immutable_field", "repo cannot be changed after create.",
			map[string]any{"field": "repo"})
	}
	w := m.modelWire(md)
	for _, f := range modelReadOnly {
		if sent, present := body[f]; present && fmt.Sprint(sent) != fmt.Sprint(w[f]) {
			return c.problem(http.StatusUnprocessableEntity, "read_only_field",
				f+" is set by the store actions and cannot be changed here.", map[string]any{"field": f})
		}
	}
	changed := false
	for k, val := range fields {
		if fmt.Sprint(md.fields[k]) != fmt.Sprint(val) {
			changed = true
		}
		if val == nil {
			delete(md.fields, k)
		} else {
			md.fields[k] = val
		}
	}
	if changed {
		md.updated = now()
	}
	return ok(http.StatusOK, m.modelWire(md))
}

func (m *MockAPI) activeRun(model int) *mockRun {
	for _, r := range m.ai.runs {
		if r.model == model && (r.status == "pending" || r.status == "running") {
			return r
		}
	}
	return nil
}

func (m *MockAPI) modelsDelete(c *call, raw string) reply {
	if r := m.aiWrite(c); r != nil {
		return *r
	}
	md, bad := m.loadModel(c, raw)
	if bad != nil {
		return *bad
	}
	blockers := map[string]any{}
	if m.activeRun(md.id) != nil {
		blockers["active_runs"] = 1
	}
	if n := len(m.ai.caches[md.id]); n > 0 {
		blockers["node_caches"] = n
	}
	if md.location == "synology" || md.location == "both" || md.status == "owned" || md.status == "serving" {
		blockers["central_copy"] = 1
	}
	for _, key := range []string{"active_runs", "node_caches", "central_copy"} {
		if _, blocked := blockers[key]; blocked {
			code := map[string]string{"active_runs": "model_has_active_run", "node_caches": "model_has_node_caches",
				"central_copy": "model_has_central_copy"}[key]
			return c.problem(http.StatusConflict, code, "The model still has weights or a store run; the catalogue row stays.",
				map[string]any{"blockers": blockers})
		}
	}
	delete(m.ai.models, md.id)
	return reply{status: http.StatusNoContent}
}

// ── node caches and store runs ───────────────────────────────────────────────

func cacheWire(model int, node, state string) map[string]any {
	return map[string]any{"id": fmt.Sprintf("%d:%s", model, node), "model_id": strconv.Itoa(model), "node": node,
		"state": state, "size_gb": 12.5, "path": "/models/cache/" + node, "updated_at": now()}
}

func (m *MockAPI) cacheGet(c *call, raw, node string) reply {
	if r := m.aiRead(c); r != nil {
		return *r
	}
	md, bad := m.loadModel(c, raw)
	if bad != nil {
		return *bad
	}
	state := m.ai.caches[md.id][node]
	if state == "" {
		return c.problem(http.StatusNotFound, "node_cache_not_found", "The model has no cache on that node.", nil)
	}
	return ok(http.StatusOK, cacheWire(md.id, node, state))
}

func (m *MockAPI) knownNode(node string) bool {
	for _, n := range m.ai.nodes {
		if n == node {
			return true
		}
	}
	return false
}

func (m *MockAPI) startRun(c *call, md *mockModel, node, action string) reply {
	if r := m.activeRun(md.id); r != nil {
		return c.problem(http.StatusConflict, "run_in_progress",
			fmt.Sprintf("Store run %d (%s) of this model is still %s; wait for it to finish.", r.id, r.action, r.status),
			map[string]any{"operation_id": fmt.Sprintf("model-store-run:%d", r.id)})
	}
	run := &mockRun{id: m.ai.nextRun, model: md.id, node: node, action: action, status: "pending",
		dispatch: m.ai.dispatch, started: now()}
	m.ai.nextRun++
	m.ai.runs[run.id] = run
	rep := ok(http.StatusAccepted, m.runOperation(run))
	rep.headers = map[string]string{"Location": fmt.Sprintf("/api/v1/operations/model-store-run:%d", run.id)}
	if run.dispatch == "dryrun" {
		// The platform's poller fails a run that was never dispatched.
		run.status, run.ended = "failed", now()
	}
	return rep
}

func (m *MockAPI) cachePut(c *call, raw, node string) reply {
	if r := m.aiWrite(c); r != nil {
		return *r
	}
	md, bad := m.loadModel(c, raw)
	if bad != nil {
		return *bad
	}
	if m.ai.caches[md.id][node] == "cached" {
		return ok(http.StatusOK, cacheWire(md.id, node, "cached"))
	}
	if md.location != "synology" && md.location != "both" {
		return c.problem(http.StatusConflict, "no_central_copy", "The model has no copy on the central store to cache from.", nil)
	}
	if !m.knownNode(node) {
		return c.problem(http.StatusUnprocessableEntity, "unknown_node", "'"+node+"' is not an AI node.",
			map[string]any{"field": "node"})
	}
	return m.startRun(c, md, node, "cache")
}

func (m *MockAPI) cacheDelete(c *call, raw, node string) reply {
	if r := m.aiWrite(c); r != nil {
		return *r
	}
	md, bad := m.loadModel(c, raw)
	if bad != nil {
		return *bad
	}
	if m.ai.caches[md.id][node] == "" {
		return c.problem(http.StatusNotFound, "node_cache_not_found", "The model has no cache on that node.", nil)
	}
	if !m.knownNode(node) {
		return c.problem(http.StatusUnprocessableEntity, "unknown_node", "'"+node+"' is not an AI node.",
			map[string]any{"field": "node"})
	}
	if !m.ai.monitoring {
		rep := c.problem(http.StatusServiceUnavailable, "loaded_state_unknown",
			"Monitoring cannot be read, so whether the model is loaded on the node is unknown; nothing was dispatched.", nil)
		rep.headers = map[string]string{"Retry-After": "30"}
		return rep
	}
	for _, repo := range m.ai.loaded[node] {
		if repo == md.repo {
			return c.problem(http.StatusConflict, "model_loaded_on_node", md.repo+" is loaded on "+node+"; unload it first.", nil)
		}
	}
	return m.startRun(c, md, node, "uncache")
}

func (m *MockAPI) runOperation(r *mockRun) map[string]any {
	status := map[string]string{"pending": "pending", "running": "running", "success": "succeeded",
		"failed": "failed", "timeout": "failed"}[r.status]
	var errV any
	if status == "failed" {
		errV = map[string]any{"code": "run_" + r.status, "detail": "the runner job failed"}
	}
	return map[string]any{
		"id": fmt.Sprintf("model-store-run:%d", r.id), "kind": "model-store-run", "status": status,
		"resource_type": "ai_model_node_cache", "resource_id": fmt.Sprintf("%d:%s", r.model, r.node),
		"created_at": r.started, "updated_at": now(), "message": r.action + " on " + r.node, "error": errV,
		"dispatch_mode": r.dispatch,
	}
}

func (m *MockAPI) advanceRun(r *mockRun) {
	if r.dispatch == "dryrun" {
		return
	}
	switch r.status {
	case "pending":
		r.status = "running"
		if r.action == "cache" {
			if m.ai.caches[r.model] == nil {
				m.ai.caches[r.model] = map[string]string{}
			}
			m.ai.caches[r.model][r.node] = "copying"
		}
	case "running":
		if m.ai.failNext {
			m.ai.failNext = false
			r.status, r.ended = "failed", now()
			if r.action == "cache" {
				delete(m.ai.caches[r.model], r.node) // a failed copy leaves nothing
			}
			return
		}
		r.status, r.ended = "success", now()
		if r.action == "cache" {
			m.ai.caches[r.model][r.node] = "cached"
		} else {
			delete(m.ai.caches[r.model], r.node)
		}
	}
}

// storeRunOperation answers GET /operations/model-store-run:<n>.
func (m *MockAPI) storeRunOperation(c *call, native string) reply {
	if r := m.aiRead(c); r != nil {
		return c.problem(http.StatusNotFound, "operation_not_found", "No such operation.", nil)
	}
	n, err := strconv.Atoi(native)
	r := m.ai.runs[n]
	if err != nil || r == nil {
		return c.problem(http.StatusNotFound, "operation_not_found", "No such operation.", nil)
	}
	m.advanceRun(r)
	return ok(http.StatusOK, m.runOperation(r))
}

// ── AI Center ────────────────────────────────────────────────────────────────

func (m *MockAPI) nodeWire(h string) map[string]any {
	out := map[string]any{"hostname": h, "site": "site-a", "mgmt_ip": "192.0.2.10", "gpu_class": "rtx-3090",
		"specs_summary": "2x RTX 3090", "services": []string{"vllm"}, "is_virtual": false, "parent_host": nil,
		"vmid": nil, "monitoring_reachable": m.ai.monitoring}
	for _, k := range []string{"status", "online", "role", "cluster", "uptime_seconds", "cpu_util_pct", "load1",
		"mem_used_pct", "disk_used_pct", "gpu_count", "gpu_util_avg_pct", "vram_used_bytes", "vram_total_bytes",
		"gpu_temp_max_c", "throttle_active", "collector_stale", "models"} {
		out[k] = nil
	}
	if m.ai.monitoring {
		models := []map[string]any{}
		for _, repo := range m.ai.loaded[h] {
			models = append(models, map[string]any{"model": repo, "served_name": repo, "engine": "vllm", "port": 8000,
				"tensor_parallel": 2, "max_model_len": 32768, "cluster": nil, "tiers": []string{"general"}})
		}
		for k, v := range map[string]any{"status": "idle", "online": true, "role": "serving", "gpu_count": 2,
			"gpu_util_avg_pct": 12.5, "vram_used_bytes": 1.5e10, "vram_total_bytes": 5.1e10, "models": models,
			"throttle_active": false, "collector_stale": false} {
			out[k] = v
		}
	}
	return out
}

func (m *MockAPI) routeAICenter(c *call) reply {
	if c.r.Method != http.MethodGet {
		return c.methodNotAllowed()
	}
	if r := m.require(c, "ai-center-read-global", "ai-center-admin-global"); r != nil {
		return *r
	}
	parts := strings.Split(strings.Trim(c.path, "/"), "/")
	p, bad := c.pageParams()
	if bad != nil {
		return *bad
	}
	q := c.r.URL.Query()
	switch {
	case len(parts) == 2 && parts[1] == "nodes":
		after, _ := p.after["hostname"].(string)
		var items []map[string]any
		for _, h := range m.ai.nodes {
			if h > after {
				items = append(items, m.nodeWire(h))
			}
			if len(items) > p.limit {
				break
			}
		}
		out := page(items, p.limit, func(it map[string]any) map[string]any { return map[string]any{"hostname": it["hostname"]} })
		out["monitoring_reachable"] = m.ai.monitoring
		return ok(http.StatusOK, out)
	case len(parts) == 3 && parts[1] == "nodes":
		if !m.knownNode(parts[2]) {
			return c.problem(http.StatusNotFound, "node_not_found", "Not an AI node.", nil)
		}
		return ok(http.StatusOK, m.nodeWire(parts[2]))
	case len(parts) == 2 && parts[1] == "clusters":
		var items []map[string]any
		for _, cl := range m.ai.clusters {
			if !q.Has("name") || cl["name"] == q.Get("name") {
				items = append(items, cl)
			}
		}
		return ok(http.StatusOK, page(items, 1000, nil))
	case len(parts) == 2 && parts[1] == "catalog":
		var items []map[string]any
		for _, e := range m.ai.catalog {
			if (!q.Has("host") || e["host"] == q.Get("host")) && (!q.Has("enabled") || fmt.Sprint(e["enabled"]) == q.Get("enabled")) {
				items = append(items, e)
			}
		}
		return ok(http.StatusOK, page(items, 1000, nil))
	}
	return c.problem(http.StatusNotFound, "not_found", "", nil)
}
