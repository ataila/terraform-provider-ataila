// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package acctest

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// The fleet inventory (platform 1.0.400), as routers/fleet.py serves it: read
// only; VMs and certificates in id order with filters and cursor pages; VM
// nodes in id order; the clusters in one page by name. infrastructure-read-global
// reads the first three, certificates-read-global the certificates. Tenant
// narrowing is the platform's (tests/test_v1_fleet.py), not modelled here.

type fleetState struct {
	vms, nodes, certs []map[string]any
	clusters          []map[string]any
}

const fleetStamp = "2026-10-01T02:00:00Z"

// AddVM records a project's VM and returns its id.
func (m *MockAPI) AddVM(projectID, env, role, status string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := strconv.Itoa(len(m.fleet.vms) + 1)
	m.fleet.vms = append(m.fleet.vms, map[string]any{
		"id": id, "project_id": projectID, "name": fmt.Sprintf("%s-p%s-%s-%s", env, projectID, role, id),
		"environment": env, "role": role, "status": status, "cpu": 4, "ram_gb": 8, "disk_gb": 64,
		"disk_class": "N", "pending_hardware": false, "created_at": fleetStamp,
	})
	return id
}

// AddVMNode records an infrastructure VM node and returns its id.
func (m *MockAPI) AddVMNode(name, role string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := strconv.Itoa(len(m.fleet.nodes) + 1)
	m.fleet.nodes = append(m.fleet.nodes, map[string]any{
		"id": id, "name": name, "role": role, "environment": "sec", "status": "running",
		"os": "Ubuntu Server 24.04 LTS", "cpu": 2, "ram_gb": 4, "disk_gb": 32, "description": nil,
		"created_at": fleetStamp, "updated_at": fleetStamp,
	})
	return id
}

// AddK8sCluster records a Kubernetes cluster.
func (m *MockAPI) AddK8sCluster(name string, enabled bool, defaultFor ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	envs := make([]any, 0, len(defaultFor))
	for _, e := range defaultFor {
		envs = append(envs, e)
	}
	m.fleet.clusters = append(m.fleet.clusters, map[string]any{
		"name": name, "enabled": enabled, "single_node": !enabled, "tenant_gpu_cards": 0,
		"default_for_envs": envs, "whole_project": false, "is_default_home": len(defaultFor) > 0 && defaultFor[0] == "prod",
		"targetable": enabled,
	})
}

// AddCertificate records a certificate (projectID "" = a domain no project owns)
// and returns its id.
func (m *MockAPI) AddCertificate(projectID, fqdn, status string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	id := strconv.Itoa(len(m.fleet.certs) + 1)
	var pid any
	if projectID != "" {
		pid = projectID
	}
	m.fleet.certs = append(m.fleet.certs, map[string]any{
		"id": id, "project_id": pid, "fqdn": fqdn, "issuer": "letsencrypt", "challenge_type": "dns-01",
		"issued_at": fleetStamp, "expires_at": "2026-12-30T02:00:00Z", "auto_rotate": true,
		"last_renewal_attempt": nil, "status": status, "created_at": fleetStamp,
	})
	return id
}

func (m *MockAPI) routeFleet(c *call) (reply, bool) {
	top := strings.SplitN(strings.TrimPrefix(c.path, "/"), "/", 2)[0]
	key := map[string]string{"vms": "infrastructure-read-global", "vm-nodes": "infrastructure-read-global",
		"kubernetes-clusters": "infrastructure-read-global", "certificates": "certificates-read-global"}[top]
	if key == "" {
		return reply{}, false
	}
	if c.r.Method != http.MethodGet {
		return c.methodNotAllowed(), true
	}
	if r := m.require(c, key); r != nil {
		return *r, true
	}
	q := c.r.URL.Query()
	var items []map[string]any
	filters := map[string]string{}
	switch top {
	case "vms":
		items = m.fleet.vms
		filters = map[string]string{"project_id": q.Get("project_id"), "environment": q.Get("environment"),
			"role": q.Get("role"), "status": q.Get("status")}
	case "vm-nodes":
		items = m.fleet.nodes
	case "certificates":
		items = m.fleet.certs
		filters = map[string]string{"project_id": q.Get("project_id"), "status": q.Get("status")}
	case "kubernetes-clusters":
		clusters := append([]map[string]any(nil), m.fleet.clusters...)
		sort.Slice(clusters, func(i, j int) bool { return clusters[i]["name"].(string) < clusters[j]["name"].(string) })
		page := make([]any, 0, len(clusters))
		for _, cl := range clusters {
			page = append(page, cl)
		}
		return ok(http.StatusOK, map[string]any{"items": page, "next_cursor": nil}), true
	}
	limit := 50
	if l := q.Get("limit"); l != "" {
		limit, _ = strconv.Atoi(l)
	}
	if limit < 1 || limit > 200 {
		return c.problem(http.StatusUnprocessableEntity, "validation_failed", "limit must be 1-200.", nil), true
	}
	after := 0
	if cur := q.Get("cursor"); cur != "" {
		n, err := strconv.Atoi(strings.TrimPrefix(cur, top+":"))
		if err != nil || !strings.HasPrefix(cur, top+":") {
			return c.problem(http.StatusBadRequest, "invalid_cursor", "The cursor is not valid.", nil), true
		}
		after = n
	}
	var matched []any
	more := false
	for _, it := range items {
		if n, _ := strconv.Atoi(it["id"].(string)); n <= after {
			continue
		}
		keep := true
		for k, want := range filters {
			if want != "" && fmt.Sprint(it[k]) != want {
				keep = false
			}
		}
		if !keep {
			continue
		}
		if len(matched) == limit {
			more = true
			break
		}
		matched = append(matched, it)
	}
	var next any
	if more {
		next = fmt.Sprintf("%s:%s", top, matched[len(matched)-1].(map[string]any)["id"])
	}
	if matched == nil {
		matched = []any{}
	}
	return ok(http.StatusOK, map[string]any{"items": matched, "next_cursor": next}), true
}
