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

// The projects endpoints of /api/v1: project records, their provisioning and
// their members, with the platform's rules:
//
//   - a project is a record: creating or changing one provisions nothing;
//     project_index is allocated (one above the highest, never below 4, up to
//     99) when omitted; project_index, short_name, gitlab_repo_slug,
//     tenant_id, deployment_backend, network_only and primary_domain are
//     frozen; status is read-only;
//   - the platform's own projects (is_self) are readable, never writable
//     (409 platform_project_read_only); a retired project refuses changes
//     (409 project_retired); DELETE retires, destroy-gated; a retired
//     project keeps its index, short name and domain;
//   - a change marks the provisioning stages it affects STALE, but only
//     stages already done, and returns them as stale_stages;
//   - POST /provisioning starts an orchestration (apply-pending when stages
//     are stale, else apply-all) and answers 202 with an operation
//     provision:<n>; a second start while one runs is 409
//     orchestration_in_progress naming its operation_id;
//   - the mock's stage engine advances ONE stage per poll of the operation
//     (the platform's advances in time). dispatch_mode live completes stages,
//     simulate completes them as simulated (converged, never provisioned),
//     dryrun never completes one;
//   - members: only platform staff and members of a tenant of the project's
//     customer (409 member_not_eligible); gitlab_role maintainer only for
//     staff (422 gitlab_role_above_cap); removal is allowed on a retired
//     project.

var (
	rxProjectShort = regexp.MustCompile(`^[a-z][a-z0-9]{1,10}$`)
	rxRepoSlug     = regexp.MustCompile(`^[a-z][a-z0-9-]{1,40}$`)
	rxFQDN         = regexp.MustCompile(`^([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}$`)
)

// The k8s stage catalogue, in apply order; the last two are deferred (never
// applied automatically).
var k8sStages = []struct {
	key, title string
	deps       []string
	deferred   bool
}{
	{"prep:reserve-ipam", "Reserve IPAM", nil, false},
	{"harbor:create-namespace", "Create Harbor namespace", []string{"prep:reserve-ipam"}, false},
	{"minio:provision-k8s", "Provision object storage", []string{"prep:reserve-ipam"}, false},
	{"gitlab:create-project", "Create GitLab project", []string{"prep:reserve-ipam", "harbor:create-namespace"}, false},
	{"vault:create-paths", "Create Vault paths", []string{"gitlab:create-project"}, false},
	{"dns:add-domain", "Add DNS records", []string{"vault:create-paths"}, false},
	{"gitlab:populate-repo", "Populate repo", []string{"gitlab:create-project", "harbor:create-namespace"}, false},
	{"k8s:render-helm-values", "Render Helm values (GitOps)", []string{"vault:create-paths", "harbor:create-namespace", "gitlab:populate-repo"}, false},
	{"k8s:argo-sync-wait", "ArgoCD sync + wait", []string{"k8s:render-helm-values"}, false},
	{"db:migrate", "Apply DB migrations", []string{"k8s:argo-sync-wait"}, false},
	{"traefik:register-routes", "Register Traefik routes", []string{"k8s:argo-sync-wait"}, false},
	{"synology:create-namespace", "Create Synology namespace", []string{"prep:reserve-ipam"}, false},
	{"backup:register", "Register backup", []string{"k8s:argo-sync-wait"}, false},
	{"mail:provision-tier3", "Provision mail (tier-3)", []string{"dns:add-domain"}, true},
	{"monitoring:register", "Register monitoring", []string{"k8s:argo-sync-wait"}, true},
}

// Which stages a change of a setting leaves stale (when done). Assumed: the
// platform derives this from a manifest diff; the mock uses a fixed map.
var staleByField = map[string][]string{
	"long_name": {"gitlab:populate-repo"}, "description": {"gitlab:populate-repo"},
	"frontend_variant": {"gitlab:populate-repo"}, "www_template": {"gitlab:populate-repo"},
	"enable_ai":         {"k8s:render-helm-values", "traefik:register-routes"},
	"frontend_exposure": {"traefik:register-routes"}, "api_exposure": {"traefik:register-routes"},
	"enable_object_storage": {"k8s:render-helm-values"}, "enable_cache": {"k8s:render-helm-values"},
}

type settingSpec struct {
	name     string
	kind     string // "string", "bool", "int"
	def      any
	enum     []string
	nullable bool
}

// projectSettings are the settings a project's owner may set, with the
// platform's defaults.
var projectSettings = []settingSpec{
	{"long_name", "string", nil, nil, false},
	{"description", "string", nil, nil, true},
	{"frontend_variant", "string", "react", []string{"react", "angular", "vue", "nuxt4"}, false},
	{"has_mobile", "bool", false, nil, false},
	{"enable_static_site", "bool", true, nil, false},
	{"enable_fullstack_app", "bool", true, nil, false},
	{"frontend_exposure", "string", "PUBLIC", []string{"INTERNAL_ONLY", "PUBLIC"}, false},
	{"api_exposure", "string", "INTERNAL_ONLY", []string{"INTERNAL_ONLY", "PUBLIC"}, false},
	{"app_gateway", "string", "shared", []string{"shared", "dedicated"}, false},
	{"enable_ai", "bool", false, nil, false},
	{"enable_web_www", "bool", true, nil, false},
	{"www_template", "string", "template-www", []string{"template-www", "template-blog"}, false},
	{"enable_uat_app_public", "bool", false, nil, false},
	{"enable_uat_www_public", "bool", false, nil, false},
	{"enable_object_storage", "bool", true, nil, false},
	{"enable_cache", "bool", false, nil, false},
	{"enable_dr_db_replica", "bool", true, nil, false},
	{"prod_object_storage_node_count", "int", 2, []string{"2", "4"}, false},
	{"prod_object_storage_disks_per_vm", "int", 2, []string{"1", "2"}, false},
	{"enable_dr_object_storage_mirror", "bool", true, nil, false},
	{"enable_nas_object_storage_replication", "bool", false, nil, false},
	{"enable_mssql", "bool", false, nil, false},
	{"mssql_edition", "string", "express", []string{"express", "standard", "enterprise"}, false},
	{"enable_iis", "bool", false, nil, false},
	{"windows_vm_count_prod", "int", 0, nil, false},
	{"windows_vm_count_uat", "int", 0, nil, false},
	{"windows_vm_count_dev", "int", 0, nil, false},
	{"github_user", "string", nil, nil, true},
	{"github_repo_url", "string", nil, nil, true},
	{"import_existing_repo", "bool", false, nil, false},
	{"allow_public_https_egress", "bool", false, nil, false},
}

var projectFrozen = []string{"project_index", "short_name", "gitlab_repo_slug", "tenant_id",
	"deployment_backend", "network_only", "primary_domain"}

type mockStage struct {
	status, started, finished, errMsg string
	simulated                         bool
	runID                             string
}

type mockProjectMember struct {
	role       string
	gitlabRole *string
	createdAt  string
}

type mockProject struct {
	id, index, customerID         int
	tenant, short, slug, domain   string
	backend, status, registeredBy string
	networkOnly, isSelf           bool
	settings                      map[string]any
	createdAt                     string
	stages                        map[string]*mockStage
	stale                         map[string]bool
	members                       map[string]*mockProjectMember
	quota                         map[string]map[string]string // env -> the keys set (k8s_quota)
	quotaReason                   map[string]string            // env -> the reason of the last PATCH
}

type mockOrch struct {
	id, project                int
	kind, status, current      string
	queue                      []string
	started, updated, finished string
	errMsg                     string
}

type projectsState struct {
	projects      map[int]*mockProject
	orchs         map[int]*mockOrch
	nextID        int
	nextOrch      int
	dispatchMode  string
	failStage     map[string]bool
	manualStage   map[string]int // polls a stage waits for an operator; <0 for good
	instant       bool           // a start runs every stage at once
	nextRun       int            // stage run ids
	gpuQueueCards map[string]int // clusters that run the GPU queue -> tenant cards
	quotaPatches  int            // quota PATCHes answered 200
	quotaOff      bool           // a platform older than k8s_quota: no member, no route
	readFailsNext bool           // the project read after the next quota PATCH fails
}

func newProjectsState() *projectsState {
	return &projectsState{projects: map[int]*mockProject{}, orchs: map[int]*mockOrch{}, nextID: 1, nextOrch: 1,
		dispatchMode: "live", failStage: map[string]bool{}, manualStage: map[string]int{}}
}

// ── test helpers ─────────────────────────────────────────────────────────────

// SetDispatchMode sets how stages run: "live", "simulate" or "dryrun".
//
// It is the platform's one DISPATCH_MODE: /meta reports it, provisioning
// follows it (simulate marks stages done), and releases and store runs fake
// their dispatch under anything but live.
func (m *MockAPI) SetDispatchMode(mode string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.projects.dispatchMode = mode
	other := "live"
	if mode != "live" {
		other = "dryrun"
	}
	m.releases.dispatch, m.ai.dispatch = other, other
	m.meta["dispatch_mode_effective"] = mode
	m.meta["simulate_stage_seconds"] = nil
	if mode == "simulate" {
		m.meta["simulate_stage_seconds"] = 3.0
	}
}

// ProvisionInstantly makes a provisioning start run every stage before it
// answers, for tests that run the built provider with its real poll interval.
func (m *MockAPI) ProvisionInstantly(instant bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.projects.instant = instant
}

// FailStage makes a stage fail when an orchestration reaches it.
func (m *MockAPI) FailStage(key string, fail bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.projects.failStage[key] = fail
}

// ManualStage makes a stage wait for an operator when an orchestration
// reaches it: for the given number of polls of the operation, or for good
// when polls is negative (until ResolveManual).
func (m *MockAPI) ManualStage(key string, polls int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if polls == 0 {
		delete(m.projects.manualStage, key)
		return
	}
	m.projects.manualStage[key] = polls
}

// ResolveManual lets the stages of a project that wait for an operator
// complete at the next poll.
func (m *MockAPI) ResolveManual(projectID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, _ := strconv.Atoi(projectID)
	for _, o := range m.projects.orchs {
		if o.project == id && o.status == "paused" {
			m.projects.manualStage[o.current] = 1
		}
	}
}

// AddCustomer registers a customer with its primary tenant out of band and
// returns the customer's id and the primary tenant's id.
func (m *MockAPI) AddCustomer(short, group string) (customerID, tenantID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.tenancy
	idx := 2
	for _, c := range s.customers {
		if c.index >= idx {
			idx = c.index + 1
		}
	}
	cu := &mockCustomer{id: s.nextCustomerID, index: idx, shortName: short, longName: short + " Ltd", group: group,
		edition: "sp", email: "ops@example.com", contactName: "Ops Desk", emailTier: 3, billingTier: "INTERNAL",
		status: "active", createdAt: now()}
	s.nextCustomerID++
	hub := hubRouterID
	primary := s.newTenant(&cu.id, cu.group, cu.longName, nil, &hub)
	cu.primaryTenantID = primary.id
	s.customers[cu.id] = cu
	return strconv.Itoa(cu.id), primary.id
}

// MarkPlatformProject makes a project one of the platform's own (is_self),
// or not.
func (m *MockAPI) MarkPlatformProject(id string, self bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, _ := strconv.Atoi(id)
	m.projects.projects[n].isSelf = self
}

// Retire retires a project out of band.
func (m *MockAPI) Retire(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, _ := strconv.Atoi(id)
	m.projects.projects[n].status = "retired"
}

// AddPlatformProject registers one of the platform's own (is_self) projects
// under a tenant and returns its id.
func (m *MockAPI) AddPlatformProject(tenantID, short string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.newProject(tenantID, short, short, short+".example.com", "k8s", false, nil, 0)
	p.isSelf = true
	return strconv.Itoa(p.id)
}

// AddTestProject registers a project of a tenant out of band ("k8s" or "vm"
// backend) and returns its id.
func (m *MockAPI) AddTestProject(tenantID, short, backend string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.newProject(tenantID, short, short, short+".example.com", backend, false, nil, 0)
	return strconv.Itoa(p.id)
}

// Project is the wire form of a project.
func (m *MockAPI) Project(id string) (map[string]any, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, _ := strconv.Atoi(id)
	p := m.projects.projects[n]
	if p == nil {
		return nil, false
	}
	return m.projectWire(p, nil), true
}

// SetProjectSetting changes a project's setting out of band.
func (m *MockAPI) SetProjectSetting(id, name string, value any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, _ := strconv.Atoi(id)
	m.projects.projects[n].settings[name] = value
}

// MarkStale marks done stages of a project stale out of band (a change in the
// portal).
func (m *MockAPI) MarkStale(id string, keys ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, _ := strconv.Atoi(id)
	p := m.projects.projects[n]
	for _, k := range keys {
		if st := p.stages[k]; st != nil && st.status == "success" {
			p.stale[k] = true
		}
	}
}

// ProjectMemberRole is a project member's role ("" when none).
func (m *MockAPI) ProjectMemberRole(projectID, userID string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, _ := strconv.Atoi(projectID)
	if mm := m.projects.projects[n].members[userID]; mm != nil {
		return mm.role
	}
	return ""
}

// SetPersonInternal marks a person as platform staff.
func (m *MockAPI) SetPersonInternal(id string, internal bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.users.people[id].isInternal = internal
}

// ── records ──────────────────────────────────────────────────────────────────

func (m *MockAPI) newProject(tenant, short, slug, domain, backend string, networkOnly bool, settings map[string]any, index int) *mockProject {
	s := m.projects
	if index == 0 {
		index = 4
		for _, p := range s.projects {
			if p.index+1 > index {
				index = p.index + 1
			}
		}
	}
	full := map[string]any{}
	for _, spec := range projectSettings {
		full[spec.name] = spec.def
	}
	full["long_name"] = short
	for k, v := range settings {
		full[k] = v
	}
	if networkOnly {
		full["enable_static_site"], full["enable_fullstack_app"] = false, false
	}
	cid := 0
	if t := m.tenancy.tenants[tenant]; t != nil && t.customerID != nil {
		cid = *t.customerID
	}
	p := &mockProject{id: s.nextID, index: index, customerID: cid, tenant: tenant, short: short, slug: slug,
		domain: strings.ToLower(domain), backend: backend, networkOnly: networkOnly, status: "planned",
		registeredBy: m.principalID(), settings: full, createdAt: now(), stages: map[string]*mockStage{},
		stale: map[string]bool{}, members: map[string]*mockProjectMember{}}
	for _, st := range k8sStages {
		p.stages[st.key] = &mockStage{status: "pending"}
	}
	s.nextID++
	s.projects[p.id] = p
	if t := m.tenancy.tenants[tenant]; t != nil {
		t.projects++ // a project, retired or not, keeps its tenant and customer
	}
	return p
}

func (m *MockAPI) customerGroup(p *mockProject) string {
	if c := m.tenancy.customers[p.customerID]; c != nil {
		return c.group
	}
	return "group"
}

func (m *MockAPI) projectOutputs(p *mockProject) map[string]any {
	on := func(k string) bool { b, _ := p.settings[k].(bool); return b }
	url := func(prefix string, cond bool) any {
		if !cond {
			return nil
		}
		return "https://" + prefix + "." + p.domain
	}
	group := m.customerGroup(p)
	repos := []map[string]any{}
	namespaces := []map[string]any{}
	secrets := []map[string]any{}
	if !p.networkOnly {
		repos = append(repos, map[string]any{"path": group + "/" + p.slug, "kind": "app", "primary": true})
		if on("enable_web_www") {
			repos = append(repos, map[string]any{"path": group + "/" + p.slug + "-www", "kind": "www", "primary": false})
		}
		if p.backend == "k8s" {
			for _, env := range []string{"dev", "uat", "prod"} {
				namespaces = append(namespaces, map[string]any{"env": env, "namespace": p.short + "-" + env})
			}
			for _, env := range []string{"dev", "uat"} {
				// An invented path shape.
				secrets = append(secrets, map[string]any{"env": env, "path": "projects/" + group + "/" + p.short + "/" + env})
			}
		}
	}
	return map[string]any{
		"urls": map[string]any{
			"static": url("www", !p.networkOnly && on("enable_static_site")), "frontend": url("app", !p.networkOnly && on("enable_fullstack_app")),
			"backend": url("api", !p.networkOnly && on("enable_fullstack_app")), "ai": url("ai", !p.networkOnly && on("enable_ai")),
		},
		"image_registry_namespace": group + "-" + p.short, "gitlab_repositories": repos,
		"kubernetes_namespaces": namespaces, "secret_paths": secrets,
	}
}

func (m *MockAPI) projectWire(p *mockProject, extra map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range p.settings {
		out[k] = v
	}
	var customer any
	if p.customerID != 0 {
		customer = strconv.Itoa(p.customerID)
	}
	var registered any
	if p.registeredBy != "" {
		registered = p.registeredBy
	}
	for k, v := range map[string]any{
		"id": strconv.Itoa(p.id), "tenant_id": p.tenant, "customer_id": customer, "project_index": p.index,
		"short_name": p.short, "gitlab_repo_slug": p.slug, "primary_domain": p.domain,
		"deployment_backend": p.backend, "network_only": p.networkOnly, "status": p.status, "is_self": p.isSelf,
		"registered_by": registered, "created_at": p.createdAt, "outputs": m.projectOutputs(p), "warnings": []any{},
	} {
		out[k] = v
	}
	if !m.projects.quotaOff {
		out["k8s_quota"] = m.projectQuota(p)
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func (m *MockAPI) projectSummary(p *mockProject) map[string]any {
	w := m.projectWire(p, nil)
	out := map[string]any{}
	for _, k := range []string{"id", "tenant_id", "customer_id", "project_index", "short_name", "long_name",
		"primary_domain", "gitlab_repo_slug", "deployment_backend", "network_only", "status", "is_self", "created_at"} {
		out[k] = w[k]
	}
	return out
}

// ── routing ──────────────────────────────────────────────────────────────────

func (m *MockAPI) routeProjects(c *call) reply {
	parts := strings.Split(strings.Trim(c.path, "/"), "/")
	method := c.r.Method
	switch {
	case len(parts) == 1:
		switch method {
		case http.MethodGet:
			return m.projectsList(c)
		case http.MethodPost:
			return m.projectsCreate(c)
		}
	case len(parts) == 2:
		switch method {
		case http.MethodGet:
			return m.projectsGet(c, parts[1])
		case http.MethodPatch:
			return m.projectsUpdate(c, parts[1])
		case http.MethodDelete:
			return m.projectsDelete(c, parts[1])
		}
	case len(parts) == 3 && parts[2] == "provisioning":
		switch method {
		case http.MethodGet:
			return m.provisioningGet(c, parts[1])
		case http.MethodPost:
			return m.provisioningStart(c, parts[1])
		}
	case len(parts) == 3 && parts[2] == "stages" && method == http.MethodGet:
		return m.stagesGet(c, parts[1])
	case len(parts) == 3 && parts[2] == "members" && method == http.MethodGet:
		return m.projectMembersList(c, parts[1])
	case len(parts) == 3 && (parts[2] == "release-promotions" || parts[2] == "release-state" ||
		parts[2] == "release-operations" || parts[2] == "prod-lock"):
		return m.routeProjectReleases(c, parts[1], parts[2])
	case len(parts) == 4 && parts[2] == "members":
		switch method {
		case http.MethodGet:
			return m.projectMemberGet(c, parts[1], parts[3])
		case http.MethodPut:
			return m.projectMemberPut(c, parts[1], parts[3])
		case http.MethodDelete:
			return m.projectMemberDelete(c, parts[1], parts[3])
		}
	case len(parts) == 4 && parts[2] == "k8s-quota" && !m.projects.quotaOff:
		switch method {
		case http.MethodPatch:
			return m.projectQuotaUpdate(c, parts[1], parts[3])
		case http.MethodDelete:
			return m.projectQuotaReset(c, parts[1], parts[3])
		}
	default:
		return c.problem(http.StatusNotFound, "not_found", "", nil)
	}
	return c.methodNotAllowed()
}

func (m *MockAPI) projectsRead(c *call) *reply {
	return m.require(c, "projects-read-global", "projects-admin-global")
}
func (m *MockAPI) projectsWrite(c *call) *reply { return m.require(c, "projects-admin-global") }

func (m *MockAPI) loadProject(c *call, raw string) (*mockProject, *reply) {
	id, valid := intID(raw)
	p := m.projects.projects[id]
	if !valid || p == nil {
		r := c.problem(http.StatusNotFound, "project_not_found", "No such project.", nil)
		return nil, &r
	}
	return p, nil
}

// projectLongName is the long_name rule of projects: 2 to 60 characters, no
// quote, apostrophe, backslash or control character.
func projectLongName(s string) string {
	if msg := length(2, 60)(s); msg != "" {
		return msg
	}
	for _, r := range s {
		if r == '"' || r == '\\' || r == '\'' || r < 0x20 || r == 0x7f {
			return "long_name must not contain quotes, backslashes or control characters"
		}
	}
	return ""
}

func notPlatform(c *call, p *mockProject) *reply {
	if p.isSelf {
		r := c.problem(http.StatusConflict, "platform_project_read_only",
			"This is one of ATAILA's own platform projects; it is read-only through the API.", nil)
		return &r
	}
	return nil
}

func writableProject(c *call, p *mockProject) *reply {
	if r := notPlatform(c, p); r != nil {
		return r
	}
	if p.status == "retired" {
		r := c.problem(http.StatusConflict, "project_retired", "The project is retired.", nil)
		return &r
	}
	return nil
}

// settingsFields validates the settings members of a body.
func settingsFields(v *validation, body map[string]any, create bool) map[string]any {
	out := map[string]any{}
	for _, spec := range projectSettings {
		raw, present := body[spec.name]
		if !present {
			if create && spec.name == "long_name" {
				v.fail("long_name", "Field required")
			}
			continue
		}
		if raw == nil {
			if spec.nullable {
				out[spec.name] = nil
			} else {
				v.fail(spec.name, spec.name+" cannot be null; omit it to leave it unchanged")
			}
			continue
		}
		switch spec.kind {
		case "string":
			s, isString := raw.(string)
			switch {
			case !isString:
				v.fail(spec.name, "Input should be a valid string")
			case spec.name == "long_name" && projectLongName(s) != "":
				v.fail(spec.name, projectLongName(s))
			case spec.name == "description" && len([]rune(s)) > 2000:
				v.fail(spec.name, "String should have at most 2000 characters")
			case spec.enum != nil && oneOf(spec.enum...)(s) != "":
				v.fail(spec.name, oneOf(spec.enum...)(s))
			default:
				out[spec.name] = s
			}
		case "bool":
			if b, isBool := raw.(bool); isBool {
				out[spec.name] = b
			} else {
				v.fail(spec.name, "Input should be a valid boolean")
			}
		case "int":
			f, isNumber := raw.(float64)
			n := int(f)
			switch {
			case !isNumber || f != float64(n):
				v.fail(spec.name, "Input should be a valid integer")
			case spec.enum != nil && oneOf(spec.enum...)(strconv.Itoa(n)) != "":
				v.fail(spec.name, "Input should be "+strings.Join(spec.enum, " or "))
			case spec.enum == nil && (n < 0 || n > 10):
				v.fail(spec.name, "Input should be between 0 and 10")
			default:
				out[spec.name] = n
			}
		}
	}
	return out
}

// crossField is the create rules on a merged state: Windows workloads need
// the VM backend.
func crossField(c *call, backend string, settings map[string]any) *reply {
	on := func(k string) bool { b, _ := settings[k].(bool); return b }
	count := func(k string) int { n, _ := settings[k].(int); return n }
	windows := on("enable_mssql") || on("enable_iis") ||
		count("windows_vm_count_prod")+count("windows_vm_count_uat")+count("windows_vm_count_dev") > 0
	if windows && backend == "k8s" {
		r := c.problem(http.StatusUnprocessableEntity, "windows_requires_vm_backend",
			"SQL Server, IIS and Windows machines need deployment_backend vm.", nil)
		return &r
	}
	return nil
}

func (m *MockAPI) projectsList(c *call) reply {
	if r := m.projectsRead(c); r != nil {
		return *r
	}
	p, bad := c.pageParams()
	if bad != nil {
		return *bad
	}
	q := c.r.URL.Query()
	after := 0
	if p.after != nil {
		f, isNumber := p.after["id"].(float64)
		if !isNumber {
			return invalidCursor(c)
		}
		after = int(f)
	}
	ids := make([]int, 0, len(m.projects.projects))
	for id := range m.projects.projects {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	var items []map[string]any
	for _, id := range ids {
		pr := m.projects.projects[id]
		switch {
		case id <= after,
			q.Has("tenant_id") && pr.tenant != strings.ToLower(q.Get("tenant_id")),
			q.Has("customer_id") && strconv.Itoa(pr.customerID) != q.Get("customer_id"),
			q.Has("status") && pr.status != q.Get("status"),
			q.Has("short_name") && pr.short != q.Get("short_name"):
			continue
		}
		items = append(items, m.projectSummary(pr))
		if len(items) > p.limit {
			break
		}
	}
	return ok(http.StatusOK, page(items, p.limit, func(it map[string]any) map[string]any {
		n, _ := strconv.Atoi(it["id"].(string))
		return map[string]any{"id": n}
	}))
}

func (m *MockAPI) projectsCreate(c *call) reply {
	if r := m.projectsWrite(c); r != nil {
		return *r
	}
	v := &validation{}
	allowed := []string{"tenant_id", "project_index", "short_name", "gitlab_repo_slug", "primary_domain",
		"deployment_backend", "network_only"}
	for _, s := range projectSettings {
		allowed = append(allowed, s.name)
	}
	body := decodeBody(c, v, allowed...)
	tenant, _ := strField(v, body, "tenant_id", true, false, pattern(rxUUID))
	index, _ := intField(v, body, "project_index", true, func(n int) string {
		if n < 1 || n > 99 {
			return "Input should be between 1 and 99"
		}
		return ""
	})
	short, _ := strField(v, body, "short_name", true, false, pattern(rxProjectShort))
	slug, _ := strField(v, body, "gitlab_repo_slug", true, false, pattern(rxRepoSlug))
	domain, _ := strField(v, body, "primary_domain", true, false, func(s string) string {
		if !rxFQDN.MatchString(strings.ToLower(strings.TrimSpace(s))) || len(s) > 253 {
			return "must be a valid FQDN"
		}
		return ""
	})
	backend, _ := strField(v, body, "deployment_backend", false, false, oneOf("vm", "k8s"))
	networkOnly, _ := boolField(v, body, "network_only", false)
	settings := settingsFields(v, body, true)
	if r := v.reply(c); r != nil {
		return *r
	}
	tid := strings.ToLower(*tenant)
	t := m.tenancy.tenants[tid]
	if t == nil || t.customerID == nil {
		return c.problem(http.StatusUnprocessableEntity, "tenant_not_found", "tenant_id names no tenant of a customer.",
			map[string]any{"field": "tenant_id"})
	}
	if cu := m.tenancy.customers[*t.customerID]; cu != nil && cu.status == "archived" {
		return c.problem(http.StatusConflict, "customer_archived", "An archived customer cannot get a new project.", nil)
	}
	be := "k8s"
	if backend != nil {
		be = *backend
	}
	merged := map[string]any{}
	for _, spec := range projectSettings {
		merged[spec.name] = spec.def
	}
	for k, val := range settings {
		merged[k] = val
	}
	if r := crossField(c, be, merged); r != nil {
		return *r
	}
	// Reservations are checked in a fixed order; the first conflict is the
	// code, and every one is listed.
	d := strings.ToLower(strings.TrimSpace(*domain))
	ids := make([]int, 0, len(m.projects.projects))
	for id := range m.projects.projects {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	var conflicts []map[string]any
	add := func(key string, value any, holder int) {
		conflicts = append(conflicts, map[string]any{"key": key, "value": value, "project_id": strconv.Itoa(holder)})
	}
	for _, check := range []string{"short_name", "project_index", "primary_domain", "gitlab_repo_slug"} {
		for _, id := range ids {
			other := m.projects.projects[id]
			switch {
			case check == "short_name" && other.short == *short:
				add(check, *short, id)
			case check == "project_index" && index != nil && other.index == *index:
				add(check, *index, id)
			case check == "primary_domain" && other.domain == d:
				add(check, d, id)
			case check == "gitlab_repo_slug" && other.customerID == *t.customerID && other.slug == *slug:
				add(check, *slug, id)
			}
		}
	}
	if len(conflicts) > 0 {
		key := conflicts[0]["key"].(string)
		return c.problem(http.StatusConflict, key+"_taken",
			fmt.Sprintf("%s %v is already held by project %s.", key, conflicts[0]["value"], conflicts[0]["project_id"]),
			map[string]any{"field": key, "conflicts": conflicts})
	}
	idx := 0
	if index != nil {
		idx = *index
	}
	no := networkOnly != nil && *networkOnly
	p := m.newProject(tid, *short, *slug, d, be, no, settings, idx)
	if p.index > 99 {
		delete(m.projects.projects, p.id)
		return c.problem(http.StatusConflict, "project_index_exhausted", "No project_index is free.", nil)
	}
	return ok(http.StatusCreated, m.projectWire(p, nil))
}

func (m *MockAPI) projectsGet(c *call, raw string) reply {
	if r := m.projectsRead(c); r != nil {
		return *r
	}
	p, bad := m.loadProject(c, raw)
	if bad != nil {
		return *bad
	}
	return ok(http.StatusOK, m.projectWire(p, nil))
}

func (m *MockAPI) projectsUpdate(c *call, raw string) reply {
	if r := m.projectsWrite(c); r != nil {
		return *r
	}
	if r := patchMediaType(c); r != nil {
		return *r
	}
	v := &validation{}
	allowed := append([]string{}, projectFrozen...)
	for _, s := range projectSettings {
		allowed = append(allowed, s.name)
	}
	body := decodeBody(c, v, allowed...)
	changes := settingsFields(v, body, false)
	if r := v.reply(c); r != nil {
		return *r
	}
	p, bad := m.loadProject(c, raw)
	if bad != nil {
		return *bad
	}
	if r := writableProject(c, p); r != nil {
		return *r
	}
	current := map[string]string{"project_index": strconv.Itoa(p.index), "short_name": p.short,
		"gitlab_repo_slug": p.slug, "tenant_id": p.tenant, "deployment_backend": p.backend,
		"network_only": strconv.FormatBool(p.networkOnly), "primary_domain": p.domain}
	for _, f := range projectFrozen {
		if sent, present := body[f]; present && frozenDiffers(sent, current[f], false) {
			return c.problem(http.StatusUnprocessableEntity, "immutable_field", f+" cannot be changed after create.",
				map[string]any{"field": f})
		}
	}
	if len(changes) == 0 {
		return ok(http.StatusOK, m.projectWire(p, map[string]any{"stale_stages": []string{}}))
	}
	merged := map[string]any{}
	for k, val := range p.settings {
		merged[k] = val
	}
	for k, val := range changes {
		merged[k] = val
	}
	if r := crossField(c, p.backend, merged); r != nil {
		return *r
	}
	implied := map[string]bool{}
	for k, val := range changes {
		if fmt.Sprint(p.settings[k]) != fmt.Sprint(val) {
			for _, stage := range staleByField[k] {
				implied[stage] = true
			}
		}
	}
	p.settings = merged
	stale := []string{}
	for _, st := range k8sStages {
		if implied[st.key] && p.stages[st.key].status == "success" {
			p.stale[st.key] = true
			stale = append(stale, st.key)
		}
	}
	return ok(http.StatusOK, m.projectWire(p, map[string]any{"stale_stages": stale}))
}

func (m *MockAPI) projectsDelete(c *call, raw string) reply {
	if r := m.require(c, "projects-admin-global"); r != nil {
		return *r
	}
	if m.isToken() && !m.tokenAllowsDestroy() {
		return c.problem(http.StatusForbidden, "destroy_not_allowed", "This API token was not created with allow_destroy.", nil)
	}
	p, bad := m.loadProject(c, raw)
	if bad != nil {
		return *bad
	}
	if r := notPlatform(c, p); r != nil {
		return *r
	}
	if p.status == "retired" {
		return reply{status: http.StatusNoContent}
	}
	if o := m.activeOrch(p.id); o != nil {
		return m.inProgress(c, o)
	}
	p.status = "retired"
	return reply{status: http.StatusNoContent}
}

// ── provisioning ─────────────────────────────────────────────────────────────

func (m *MockAPI) activeOrch(project int) *mockOrch {
	for _, o := range m.projects.orchs {
		if o.project == project && (o.status == "running" || o.status == "paused") {
			return o
		}
	}
	return nil
}

func (m *MockAPI) inProgress(c *call, o *mockOrch) reply {
	return c.problem(http.StatusConflict, "orchestration_in_progress",
		"An "+o.kind+" orchestration is already running on this project; poll its operation.",
		map[string]any{"operation_id": fmt.Sprintf("provision:%d", o.id)})
}

func (m *MockAPI) provisioningView(p *mockProject) map[string]any {
	total, done := 0, 0
	var stages []map[string]any
	running, failed, needs, stale := []string{}, []string{}, []string{}, []string{}
	simulated := false
	for _, st := range k8sStages {
		if st.deferred {
			continue
		}
		s := p.stages[st.key]
		total++
		if s.status == "success" {
			done++
		}
		switch s.status {
		case "running":
			running = append(running, st.key)
		case "failed":
			failed = append(failed, st.key)
		case "manual":
			needs = append(needs, st.key)
		}
		if p.stale[st.key] {
			stale = append(stale, st.key)
		}
		simulated = simulated || s.simulated
		var runAt any
		if s.runID != "" {
			runAt = s.started
		}
		stages = append(stages, map[string]any{"key": st.key, "status": s.status, "stale": p.stale[st.key],
			"simulated": s.simulated, "last_run_id": nullStr(s.runID), "last_run_at": runAt})
	}
	state := "provisioning"
	switch {
	case len(failed) > 0:
		state = "attention"
	case len(needs) > 0:
		state = "needs_action"
	case done == total:
		state = "complete"
	case done == 0 && m.activeOrch(p.id) == nil && len(running) == 0:
		state = "not_started"
	}
	converged := done == total && len(stale) == 0
	var orch any
	if o := m.activeOrch(p.id); o != nil {
		orch = map[string]any{"operation_id": fmt.Sprintf("provision:%d", o.id), "kind": o.kind,
			"status": m.opStatus(o, p), "current_stage": nullStr(o.current), "started_at": o.started,
			"updated_at": o.updated, "stale": false}
	}
	var message any
	switch m.projects.dispatchMode {
	case "dryrun":
		message = "Pipeline dispatch on this platform is dry-run: nothing is executed and provisioning never completes."
	case "simulate":
		message = "Pipeline dispatch on this platform is SIMULATED: each stage is marked done without running."
	}
	return map[string]any{
		"project_id": strconv.Itoa(p.id), "state": state, "converged": converged,
		"provisioned": converged && !simulated, "simulated": simulated, "percent": done * 100 / total,
		"total": total, "done": done, "stages": stages, "running_stages": running, "failed_stages": failed,
		"needs_action_stages": needs, "stale_stages": stale, "orchestration": orch,
		"dispatch_mode": m.projects.dispatchMode, "message": message,
	}
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (m *MockAPI) opStatus(o *mockOrch, p *mockProject) string {
	switch o.status {
	case "complete":
		return "succeeded"
	case "failed":
		return "failed"
	case "paused":
		return "awaiting_operator"
	}
	if o.current != "" {
		if st := p.stages[o.current]; st != nil && st.status == "manual" {
			return "awaiting_operator"
		}
		return "running"
	}
	return "pending"
}

func (m *MockAPI) operationWire(o *mockOrch) map[string]any {
	p := m.projects.projects[o.project]
	status := m.opStatus(o, p)
	simulated := false
	for _, s := range p.stages {
		simulated = simulated || s.simulated
	}
	var errV any
	if status == "failed" {
		errV = map[string]any{"code": "stage_failed", "stage": o.current, "message": o.errMsg}
	}
	msg := o.kind + " of project " + p.short
	if status == "awaiting_operator" {
		msg += "; stage " + o.current + " needs an operator in the portal"
	}
	return map[string]any{
		"id": fmt.Sprintf("provision:%d", o.id), "kind": "provision", "status": status,
		"resource_type": "project", "resource_id": strconv.Itoa(p.id), "created_at": o.started,
		"updated_at": o.updated, "message": msg, "error": errV, "stage": nullStr(o.current),
		"dispatch_mode": m.projects.dispatchMode, "simulated": simulated,
	}
}

// advance moves an orchestration by one stage: the stage it is on finishes
// (as the dispatch mode and the injected faults say) and the next begins.
func (m *MockAPI) advance(o *mockOrch) {
	p := m.projects.projects[o.project]
	ts := now()
	if o.status == "paused" {
		left := m.projects.manualStage[o.current]
		if left < 0 {
			return
		}
		if left--; left > 0 {
			m.projects.manualStage[o.current] = left
			return
		}
		// The operator completed the stage.
		delete(m.projects.manualStage, o.current)
		o.status = "running"
	} else if o.status != "running" {
		return
	}
	o.updated = ts
	if o.current != "" {
		st := p.stages[o.current]
		switch {
		case m.projects.dispatchMode == "dryrun":
			return // a fake pipeline id: the stage never finishes
		case m.projects.failStage[o.current]:
			st.status, st.finished, st.errMsg = "failed", ts, "injected failure"
			o.status, o.finished, o.errMsg = "failed", ts, "stage "+o.current+" failed"
			return
		case st.status != "manual" && m.projects.manualStage[o.current] != 0:
			st.status = "manual"
			o.status = "paused"
			return
		}
		st.status, st.finished, st.simulated = "success", ts, m.projects.dispatchMode == "simulate"
		delete(p.stale, o.current)
		o.queue = o.queue[1:]
		o.current = ""
	}
	if len(o.queue) == 0 {
		o.status, o.finished = "complete", ts
		done := true
		for _, st := range k8sStages {
			done = done && (st.deferred || p.stages[st.key].status == "success")
		}
		if done && m.projects.dispatchMode == "live" {
			p.status = "active"
		}
		return
	}
	o.current = o.queue[0]
	p.stages[o.current].status, p.stages[o.current].started = "running", ts
	m.projects.nextRun++
	p.stages[o.current].runID = strconv.Itoa(m.projects.nextRun)
}

func (m *MockAPI) provisioningGet(c *call, raw string) reply {
	if r := m.projectsRead(c); r != nil {
		return *r
	}
	p, bad := m.loadProject(c, raw)
	if bad != nil {
		return *bad
	}
	return ok(http.StatusOK, m.provisioningView(p))
}

func (m *MockAPI) provisioningStart(c *call, raw string) reply {
	if r := m.projectsWrite(c); r != nil {
		return *r
	}
	p, bad := m.loadProject(c, raw)
	if bad != nil {
		return *bad
	}
	if r := writableProject(c, p); r != nil {
		return *r
	}
	if o := m.activeOrch(p.id); o != nil {
		return m.inProgress(c, o)
	}
	kind := "apply-all"
	var queue []string
	for _, st := range k8sStages {
		if st.deferred {
			continue
		}
		if p.stale[st.key] {
			kind = "apply-pending"
		}
	}
	for _, st := range k8sStages {
		if st.deferred {
			continue
		}
		s := p.stages[st.key]
		if (kind == "apply-pending" && p.stale[st.key]) || (kind == "apply-all" && s.status != "success") {
			queue = append(queue, st.key)
		}
	}
	ts := now()
	o := &mockOrch{id: m.projects.nextOrch, project: p.id, kind: kind, status: "running", queue: queue,
		started: ts, updated: ts}
	m.projects.nextOrch++
	m.projects.orchs[o.id] = o
	if m.projects.dispatchMode == "live" && p.status == "planned" {
		p.status = "provisioning"
	}
	m.advance(o) // the first stage starts at once
	for m.projects.instant && o.status == "running" {
		m.advance(o)
	}
	rep := ok(http.StatusAccepted, m.operationWire(o))
	rep.headers = map[string]string{"Location": fmt.Sprintf("/api/v1/operations/provision:%d", o.id)}
	return rep
}

// provisionOperation answers GET /operations/provision:<n>, and moves the
// orchestration on by one stage first (the mock's clock).
func (m *MockAPI) provisionOperation(c *call, native string) reply {
	if r := m.projectsRead(c); r != nil {
		return c.problem(http.StatusNotFound, "operation_not_found", "No such operation.", nil)
	}
	n, err := strconv.Atoi(native)
	o := m.projects.orchs[n]
	if err != nil || o == nil {
		return c.problem(http.StatusNotFound, "operation_not_found", "No such operation.", nil)
	}
	m.advance(o)
	return ok(http.StatusOK, m.operationWire(o))
}

func (m *MockAPI) stagesGet(c *call, raw string) reply {
	if r := m.projectsRead(c); r != nil {
		return *r
	}
	p, bad := m.loadProject(c, raw)
	if bad != nil {
		return *bad
	}
	view := m.provisioningView(p)
	var items []map[string]any
	for _, st := range k8sStages {
		s := p.stages[st.key]
		blocked := false
		if s.status == "pending" {
			for _, d := range st.deps {
				blocked = blocked || p.stages[d].status != "success"
			}
		}
		deps := st.deps
		if deps == nil {
			deps = []string{}
		}
		items = append(items, map[string]any{
			"key": st.key, "title": st.title, "deps": deps, "deferred": st.deferred, "status": s.status,
			"blocked": blocked, "stale": p.stale[st.key], "simulated": s.simulated, "started_at": nullStr(s.started),
			"finished_at": nullStr(s.finished), "error_message": nullStr(s.errMsg), "verify_state": nil, "verify_summary": nil,
			"last_run_id": nullStr(s.runID), "last_run_at": nullStr(s.started),
		})
	}
	return ok(http.StatusOK, map[string]any{"project_id": strconv.Itoa(p.id), "state": view["state"],
		"percent": view["percent"], "stages": items})
}

// ── members ──────────────────────────────────────────────────────────────────

func projectMemberWire(p *mockProject, uid string, mm *mockProjectMember) map[string]any {
	var gl any
	if mm.gitlabRole != nil {
		gl = *mm.gitlabRole
	}
	return map[string]any{"project_id": strconv.Itoa(p.id), "user_id": uid, "role": mm.role,
		"gitlab_role": gl, "created_at": mm.createdAt}
}

func (m *MockAPI) projectMembersList(c *call, raw string) reply {
	if r := m.projectsRead(c); r != nil {
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
	ids := make([]string, 0, len(p.members))
	for id := range p.members {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var items []map[string]any
	for _, id := range ids {
		items = append(items, projectMemberWire(p, id, p.members[id]))
	}
	return ok(http.StatusOK, page(items, pp.limit, func(it map[string]any) map[string]any {
		return map[string]any{"user_id": it["user_id"]}
	}))
}

func (m *MockAPI) projectMemberGet(c *call, raw, rawUser string) reply {
	if r := m.projectsRead(c); r != nil {
		return *r
	}
	p, bad := m.loadProject(c, raw)
	if bad != nil {
		return *bad
	}
	uid, valid := uuidID(rawUser)
	mm := p.members[uid]
	if !valid || mm == nil {
		return c.problem(http.StatusNotFound, "member_not_found", "No such member.", nil)
	}
	return ok(http.StatusOK, projectMemberWire(p, uid, mm))
}

func (m *MockAPI) eligible(p *mockProject, uid string) bool {
	person := m.users.people[uid]
	if person == nil || !person.isActive {
		return false
	}
	if person.isInternal {
		return true
	}
	for tid, members := range m.tenancy.memberships {
		t := m.tenancy.tenants[tid]
		if _, in := members[uid]; in && t != nil && t.customerID != nil && *t.customerID == p.customerID {
			return true
		}
	}
	return false
}

func (m *MockAPI) projectMemberPut(c *call, raw, rawUser string) reply {
	if r := m.projectsWrite(c); r != nil {
		return *r
	}
	v := &validation{}
	body := decodeBody(c, v, "role", "gitlab_role")
	role, _ := strField(v, body, "role", false, false, oneOf("owner", "admin", "developer", "member", "viewer"))
	gitlabRole, _ := strField(v, body, "gitlab_role", false, true, oneOf("guest", "reporter", "developer", "maintainer"))
	if r := v.reply(c); r != nil {
		return *r
	}
	p, bad := m.loadProject(c, raw)
	if bad != nil {
		return *bad
	}
	if r := writableProject(c, p); r != nil {
		return *r
	}
	uid, valid := uuidID(rawUser)
	if !valid || m.users.people[uid] == nil {
		return c.problem(http.StatusNotFound, "user_not_found", "No such user.", nil)
	}
	if !m.eligible(p, uid) {
		return c.problem(http.StatusConflict, "member_not_eligible",
			"Only ATAILA staff and active members of a tenant of the project's customer can be project members.", nil)
	}
	if gitlabRole != nil && *gitlabRole == "maintainer" && !m.users.people[uid].isInternal {
		return c.problem(http.StatusUnprocessableEntity, "gitlab_role_above_cap",
			"gitlab_role 'maintainer' is above the customer cap of 'developer'.", map[string]any{"field": "gitlab_role"})
	}
	r := "developer"
	if role != nil {
		r = *role
	}
	status := http.StatusOK
	mm := p.members[uid]
	if mm == nil {
		mm = &mockProjectMember{createdAt: now()}
		p.members[uid] = mm
		status = http.StatusCreated
	}
	mm.role, mm.gitlabRole = r, gitlabRole
	return ok(status, projectMemberWire(p, uid, mm))
}

func (m *MockAPI) projectMemberDelete(c *call, raw, rawUser string) reply {
	if r := m.projectsWrite(c); r != nil {
		return *r
	}
	p, bad := m.loadProject(c, raw)
	if bad != nil {
		return *bad
	}
	if r := notPlatform(c, p); r != nil {
		return *r
	}
	uid, valid := uuidID(rawUser)
	if !valid || p.members[uid] == nil {
		return c.problem(http.StatusNotFound, "member_not_found", "No such member.", nil)
	}
	delete(p.members, uid)
	return reply{status: http.StatusNoContent}
}
