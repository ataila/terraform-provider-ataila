// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package acctest

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// The Kubernetes namespace quota of a project (gap 4 slice 4): `k8s_quota` on
// the project read, PATCH / DELETE /projects/{id}/k8s-quota/{env}. The mock
// keeps one override map per project and environment, merges it over a tier
// default like the platform's compiler, and applies the two rules a
// configuration can trip over: a GPU key above 0 needs a cluster that runs the
// GPU queue (422 quota_refused), and the guaranteed floors of one cluster may
// not exceed its tenant cards (409 quota_conflict). Both are the platform's
// codes; the texts are the mock's own.

var quotaKeys = []string{"req_cpu", "req_mem", "lim_cpu", "lim_mem", "pvc", "storage", "pods",
	"gpu_exclusive", "gpu_shared", "gpu_borrow", "fair_weight"}

var quotaGPUKeys = map[string]bool{"gpu_exclusive": true, "gpu_shared": true, "gpu_borrow": true}

var quotaTierDefaults = map[string]map[string]string{
	"prod": {"req_cpu": "8", "req_mem": "16Gi", "lim_cpu": "16", "lim_mem": "32Gi", "pvc": "20",
		"storage": "200Gi", "pods": "30", "gpu_exclusive": "0", "gpu_shared": "0", "gpu_borrow": "0", "fair_weight": "1"},
	"nonprod": {"req_cpu": "4", "req_mem": "8Gi", "lim_cpu": "8", "lim_mem": "16Gi", "pvc": "12",
		"storage": "100Gi", "pods": "20", "gpu_exclusive": "0", "gpu_shared": "0", "gpu_borrow": "0", "fair_weight": "1"},
}

// quotaClusters is the cluster of each environment, the platform's default map.
var quotaClusters = map[string]string{"dev": "nonprod-k8s", "uat": "nonprod-k8s", "prod": "prod-k8s"}

var (
	rxQuotaCPU    = regexp.MustCompile(`^(?:(?:0|[1-9]\d*)(?:\.\d+)?|(?:0|[1-9]\d*)m)$`)
	rxQuotaBytes  = regexp.MustCompile(`^(?:0|[1-9]\d*)(?:\.\d+)?(?:Ki|Mi|Gi|Ti|k|M|G|T)$`)
	rxQuotaCount  = regexp.MustCompile(`^\d+$`)
	rxQuotaWeight = regexp.MustCompile(`^(?:0|[1-9]\d*)(?:\.\d+)?$`)
)

func quotaTier(env string) string {
	if env == "prod" {
		return "prod"
	}
	return "nonprod"
}

// quotaValueError is the platform's own message shape for a bad quantity.
func quotaValueError(key, value string) string {
	var ok bool
	var hint string
	switch key {
	case "req_cpu", "lim_cpu":
		ok, hint = rxQuotaCPU.MatchString(value), "a CPU quantity such as 8, 0.5 or 500m"
	case "req_mem", "lim_mem", "storage":
		ok, hint = rxQuotaBytes.MatchString(value), "a quantity with a unit (Ki, Mi, Gi, Ti, k, M, G, T) such as 16Gi"
	case "fair_weight":
		ok, hint = rxQuotaWeight.MatchString(value), "a non-negative decimal fair-share weight such as 1 or 0.5"
	default:
		ok, hint = rxQuotaCount.MatchString(value), "a whole number"
	}
	if ok {
		return ""
	}
	return fmt.Sprintf("k8s quota %s: %q is not %s.", key, value, hint)
}

// SetGPUQueueCluster makes a cluster run the GPU queue with the given tenant
// cards (0 cards: the queue runs, every floor is refused).
func (m *MockAPI) SetGPUQueueCluster(name string, cards int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.projects.gpuQueueCards == nil {
		m.projects.gpuQueueCards = map[string]int{}
	}
	m.projects.gpuQueueCards[name] = cards
}

// ServeProjectQuota false makes the mock a platform release older than
// k8s_quota: projects carry no such member and the quota routes are 404
// not_found, as on such a platform.
func (m *MockAPI) ServeProjectQuota(on bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.projects.quotaOff = !on
}

// FailReadAfterNextQuota makes the project read that follows the next quota
// PATCH answered 200 fail (a 403 problem): the read-back after a quota change
// is lost, while the change itself is stored.
func (m *MockAPI) FailReadAfterNextQuota() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.projects.readFailsNext = true
}

// SetProjectQuota sets a project's overrides for one environment out of band,
// as the portal's quota editor would have: a holder of a GPU floor, say.
func (m *MockAPI) SetProjectQuota(id, env string, values map[string]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, _ := strconv.Atoi(id)
	p := m.projects.projects[n]
	if p == nil {
		return
	}
	if p.quota == nil {
		p.quota = map[string]map[string]string{}
	}
	copied := map[string]string{}
	for k, v := range values {
		copied[k] = v
	}
	p.quota[env] = copied
}

// ProjectQuota returns a project's stored overrides for one environment (the
// keys the configuration set), or nil when the environment is on the default.
func (m *MockAPI) ProjectQuota(id, env string) map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, _ := strconv.Atoi(id)
	p := m.projects.projects[n]
	if p == nil || p.quota == nil || p.quota[env] == nil {
		return nil
	}
	out := map[string]string{}
	for k, v := range p.quota[env] {
		out[k] = v
	}
	return out
}

// ProjectQuotaReason returns the reason the last successful quota PATCH of one
// environment carried ("" when none did).
func (m *MockAPI) ProjectQuotaReason(id, env string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, _ := strconv.Atoi(id)
	if p := m.projects.projects[n]; p != nil {
		return p.quotaReason[env]
	}
	return ""
}

// QuotaPatches returns how many quota PATCHes the mock has answered with 200.
func (m *MockAPI) QuotaPatches() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.projects.quotaPatches
}

func (p *mockProject) quotaEffective(env string) map[string]string {
	out := map[string]string{}
	for k, v := range quotaTierDefaults[quotaTier(env)] {
		out[k] = v
	}
	for k, v := range p.quota[env] {
		out[k] = v
	}
	return out
}

func (m *MockAPI) quotaEnvWire(p *mockProject, env string) map[string]any {
	eff := p.quotaEffective(env)
	cluster := quotaClusters[env]
	_, queue := m.projects.gpuQueueCards[cluster]
	keys := []string{}
	for _, k := range quotaKeys {
		if _, ok := p.quota[env][k]; ok {
			keys = append(keys, k)
		}
	}
	gpu := false
	for k := range quotaGPUKeys {
		if n, _ := strconv.Atoi(eff[k]); n > 0 {
			gpu = true
		}
	}
	out := map[string]any{
		"env": env, "namespace": p.short + "-" + env, "cluster": cluster, "tier": quotaTier(env),
		"gpu_queue": queue, "gpu_enabled": gpu, "overridden": len(keys) > 0, "overridden_keys": keys,
	}
	for _, k := range quotaKeys {
		out[k] = eff[k]
	}
	return out
}

// projectQuota is the `k8s_quota` member of a project: null for a VM project.
func (m *MockAPI) projectQuota(p *mockProject) any {
	if p.backend != "k8s" {
		return nil
	}
	out := map[string]any{}
	for _, env := range []string{"dev", "uat", "prod"} {
		out[env] = m.quotaEnvWire(p, env)
	}
	return out
}

func (m *MockAPI) quotaWrite(c *call) *reply { return m.require(c, "k8s-gpu-admin-global") }

func quotaEnv(c *call, env string) *reply {
	if env == "dev" || env == "uat" || env == "prod" {
		return nil
	}
	r := c.problem(http.StatusUnprocessableEntity, "validation_failed",
		"Input should be 'dev', 'uat' or 'prod'", map[string]any{"field": "env"})
	return &r
}

func quotaProject(c *call, p *mockProject) *reply {
	if r := notPlatform(c, p); r != nil {
		return r
	}
	if p.backend != "k8s" {
		r := c.problem(http.StatusUnprocessableEntity, "vm_projects_unsupported",
			fmt.Sprintf("Project %s is not a Kubernetes project (deployment_backend=%s): the namespace quota "+
				"applies to Kubernetes projects only.", p.short, p.backend), map[string]any{"field": "deployment_backend"})
		return &r
	}
	return nil
}

// quotaGuards are the compiler's and the cohort's rules on the merged quota.
func (m *MockAPI) quotaGuards(c *call, p *mockProject, env string, merged map[string]string) *reply {
	cluster := quotaClusters[env]
	cards, queue := m.projects.gpuQueueCards[cluster]
	gpu := false
	for k := range quotaGPUKeys {
		if n, _ := strconv.Atoi(merged[k]); n > 0 {
			gpu = true
		}
	}
	if gpu && !queue {
		r := c.problem(http.StatusUnprocessableEntity, "quota_refused",
			fmt.Sprintf("Cluster %s (%s-%s) runs no Kueue, so it takes no GPU quota: gpu_exclusive, gpu_shared "+
				"and gpu_borrow must stay 0 there.", cluster, p.short, env), nil)
		return &r
	}
	if n, _ := strconv.Atoi(merged["gpu_shared"]); n > 0 {
		r := c.problem(http.StatusUnprocessableEntity, "quota_refused",
			fmt.Sprintf("k8s quota %s: gpu_shared = %d on cluster %s, which has no gpu-shared flavor.", env, n, cluster), nil)
		return &r
	}
	floor, _ := strconv.Atoi(merged["gpu_exclusive"])
	current, _ := strconv.Atoi(p.quotaEffective(env)["gpu_exclusive"])
	if floor > current {
		committed := 0
		for _, other := range m.projects.projects {
			if other.backend != "k8s" {
				continue
			}
			for e, cl := range quotaClusters {
				if cl != cluster || (other.id == p.id && e == env) {
					continue
				}
				n, _ := strconv.Atoi(other.quotaEffective(e)["gpu_exclusive"])
				committed += n
			}
		}
		if committed+floor > cards {
			r := c.problem(http.StatusConflict, "quota_conflict",
				fmt.Sprintf("Cluster %s has %d tenant GPU card(s); the other ClusterQueues in its cohort already "+
					"hold %d as guaranteed floor. %s-%s asking for %d would commit %d > %d.",
					cluster, cards, committed, p.short, env, floor, committed+floor, cards), nil)
			return &r
		}
	}
	return nil
}

// quotaUpdated is the PATCH answer: the environment's quota plus what the
// change set in motion. The mock marks the GitOps pair stale when it has run
// (the platform then starts a walk over it) and reports the status the
// provider reads.
func (m *MockAPI) quotaUpdated(p *mockProject, env string) map[string]any {
	out := m.quotaEnvWire(p, env)
	stale := []string{}
	for _, key := range []string{"k8s:render-helm-values", "k8s:argo-sync-wait"} {
		if st := p.stages[key]; st != nil && st.status == "success" {
			p.stale[key] = true
			stale = append(stale, key)
		}
	}
	out["stale_stages"] = stale
	out["operation_id"] = nil
	out["dispatch_status"] = "not_provisioned"
	if len(stale) == 2 {
		out["dispatch_status"] = "started"
		out["operation_id"] = fmt.Sprintf("provision:%d", m.projects.nextOrch)
	} else if len(stale) == 1 {
		out["dispatch_status"] = "partial"
	}
	out["warnings"] = []any{}
	return out
}

func (m *MockAPI) projectQuotaUpdate(c *call, raw, env string) reply {
	if r := m.quotaWrite(c); r != nil {
		return *r
	}
	if r := patchMediaType(c); r != nil {
		return *r
	}
	if r := quotaEnv(c, env); r != nil {
		return *r
	}
	v := &validation{}
	body := decodeBody(c, v, append(append([]string{}, quotaKeys...), "reason")...)
	reason, _ := body["reason"].(string)
	if _, present := body["reason"]; !present {
		v.fail("reason", "Field required")
	} else if strings.TrimSpace(reason) == "" {
		v.fail("reason", "reason is required: say why this quota differs from the tier default")
	}
	changes := map[string]*string{}
	for _, k := range quotaKeys {
		rawV, present := body[k]
		if !present {
			continue
		}
		switch t := rawV.(type) {
		case nil:
			changes[k] = nil
		case string:
			s := strings.TrimSpace(t)
			changes[k] = &s
		case float64:
			s := strconv.FormatFloat(t, 'f', -1, 64)
			changes[k] = &s
		default:
			v.fail(k, "a quota value is a quantity string or a number")
		}
	}
	if r := v.reply(c); r != nil {
		return *r
	}
	p, bad := m.loadProject(c, raw)
	if bad != nil {
		return *bad
	}
	if r := quotaProject(c, p); r != nil {
		return *r
	}
	if len(changes) == 0 {
		return c.problem(http.StatusUnprocessableEntity, "validation_failed",
			"Name at least one quota key to change: "+strings.Join(quotaKeys, ", ")+".", nil)
	}
	for k, val := range changes {
		if val == nil {
			continue
		}
		if msg := quotaValueError(k, *val); msg != "" {
			return c.problem(http.StatusUnprocessableEntity, "quota_refused", msg, nil)
		}
	}
	if p.quota == nil {
		p.quota = map[string]map[string]string{}
	}
	wanted := map[string]string{}
	for k, val := range p.quota[env] {
		wanted[k] = val
	}
	for k, val := range changes {
		if val == nil {
			delete(wanted, k)
			continue
		}
		wanted[k] = *val
		if rxQuotaCount.MatchString(*val) && k != "req_cpu" && k != "lim_cpu" && k != "fair_weight" {
			n, _ := strconv.Atoi(*val)
			wanted[k] = strconv.Itoa(n) // the compiler's canonical count
		}
	}
	merged := map[string]string{}
	for k, val := range quotaTierDefaults[quotaTier(env)] {
		merged[k] = val
	}
	for k, val := range wanted {
		merged[k] = val
	}
	if r := m.quotaGuards(c, p, env, merged); r != nil {
		return *r
	}
	if p.status == "retired" {
		return c.problem(http.StatusConflict, "quota_conflict",
			fmt.Sprintf("Project %s is retired: only its GPU quota can be released.", p.short), nil)
	}
	if len(wanted) == 0 {
		delete(p.quota, env)
	} else {
		p.quota[env] = wanted
	}
	if p.quotaReason == nil {
		p.quotaReason = map[string]string{}
	}
	p.quotaReason[env] = strings.TrimSpace(reason)
	m.projects.quotaPatches++
	if m.projects.readFailsNext {
		m.projects.readFailsNext = false
		path := fmt.Sprintf("/projects/%d", p.id)
		m.faults[path] = append(m.faults[path], Fault{Status: http.StatusForbidden, Code: "forbidden",
			Method: http.MethodGet})
	}
	return ok(http.StatusOK, m.quotaUpdated(p, env))
}

func (m *MockAPI) projectQuotaReset(c *call, raw, env string) reply {
	if r := m.quotaWrite(c); r != nil {
		return *r
	}
	if r := quotaEnv(c, env); r != nil {
		return *r
	}
	p, bad := m.loadProject(c, raw)
	if bad != nil {
		return *bad
	}
	if r := quotaProject(c, p); r != nil {
		return *r
	}
	if p.quota == nil || p.quota[env] == nil {
		return c.problem(http.StatusNotFound, "quota_override_not_found",
			fmt.Sprintf("%s-%s has no quota override; it is on the tier default.", p.short, env), nil)
	}
	delete(p.quota, env)
	m.quotaUpdated(p, env)
	return reply{status: http.StatusNoContent}
}
