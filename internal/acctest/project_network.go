// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package acctest

import (
	"net/http"
	"strconv"
)

// A project's network (platform 1.0.401), as routers/network.py serves it:
// read only, networking-read-global. A project without a network set here has
// none applied yet: empty lists, and for a Kubernetes project its default-deny
// policy.

// SetProjectNetwork sets the network the mock reports for a project; members
// left out of network keep the defaults.
func (m *MockAPI) SetProjectNetwork(projectID string, network map[string]any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.networks == nil {
		m.networks = map[string]map[string]any{}
	}
	m.networks[projectID] = network
}

func (m *MockAPI) projectNetworkGet(c *call, raw string) reply {
	if c.r.Method != http.MethodGet {
		return c.methodNotAllowed()
	}
	if r := m.require(c, "networking-read-global"); r != nil {
		return *r
	}
	p, bad := m.loadProject(c, raw)
	if bad != nil {
		return *bad
	}
	id := strconv.Itoa(p.id)
	var k8s any
	if p.backend == "k8s" {
		ns := p.short + "-prod"
		k8s = map[string]any{"namespace": ns, "policies": []any{map[string]any{
			"namespace": ns, "name": "default-deny", "detail": "all ingress and egress denied unless allowed below",
			"egress": []any{}}}}
	}
	out := map[string]any{"project_id": id, "deployment_backend": p.backend, "network_mode": "vlan",
		"subnets": []any{}, "vlans": []any{}, "security_groups": []any{}, "vms": []any{},
		"gateway_allows": []any{}, "kubernetes": k8s, "vpc": nil}
	for k, v := range m.networks[id] {
		out[k] = v
	}
	return ok(http.StatusOK, out)
}
