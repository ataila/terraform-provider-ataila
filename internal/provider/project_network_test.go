// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/ataila/terraform-provider-ataila/internal/acctest"
	"github.com/ataila/terraform-provider-ataila/internal/client"
)

const networkAddr = "data.ataila_project_network.p"

func networkProject(t *testing.T, backend string) (*acctest.MockAPI, string) {
	t.Helper()
	m := newMock(t)
	m.SetMeta(func(meta map[string]any) { meta["platform_version"] = client.ReleaseProjectNetwork })
	m.SetScopes(append(acctest.DefaultScopes(), "networking-read-global")...)
	_, tenant := m.AddCustomer("EXAMPLE", "example")
	return m, m.AddTestProject(tenant, "shop", backend)
}

func networkHCL(pid string) string {
	return providerBlock(false) + fmt.Sprintf("data \"ataila_project_network\" \"p\" {\n  project_id = %q\n}\n", pid)
}

func TestAccProjectNetwork_vmProject(t *testing.T) {
	m, pid := networkProject(t, "vm")
	m.SetProjectNetwork(pid, map[string]any{
		"subnets": []any{map[string]any{"cidr": "192.0.2.0/24", "site": "north", "environments": []any{"dev", "uat"}}},
		"vlans": []any{map[string]any{"vlan_id": 304, "name": "p-shop", "cidr": "192.0.2.0/24",
			"gateway": "192.0.2.254", "site": "north"}},
		"security_groups": []any{map[string]any{"name": "shop-db", "environment": "prod", "cluster": "pve-a",
			"comment": "databases", "rules": []any{map[string]any{"position": 0, "direction": "in",
				"action": "ACCEPT", "source": "+shop-app", "destination": nil, "protocol": "tcp", "port": "5432",
				"comment": "app to db"}}}},
		"vms": []any{map[string]any{"name": "prod-shop-db-01", "environment": "prod", "group": "shop-db",
			"enabled": true, "applied": true}},
		"gateway_allows": []any{map[string]any{"chain": "forward", "action": "accept", "source": nil,
			"destination": "192.0.2.10", "protocol": "tcp", "ports": "443", "comment": "public HTTPS"}},
	})
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config: networkHCL(pid),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(networkAddr, "deployment_backend", "vm"),
				resource.TestCheckResourceAttr(networkAddr, "network_mode", "vlan"),
				resource.TestCheckResourceAttr(networkAddr, "subnets.0.environments.#", "2"),
				resource.TestCheckResourceAttr(networkAddr, "vlans.0.vlan_id", "304"),
				resource.TestCheckResourceAttr(networkAddr, "vlans.0.gateway", "192.0.2.254"),
				resource.TestCheckResourceAttr(networkAddr, "security_groups.0.rules.0.port", "5432"),
				resource.TestCheckNoResourceAttr(networkAddr, "security_groups.0.rules.0.destination"),
				resource.TestCheckResourceAttr(networkAddr, "vms.0.applied", "true"),
				resource.TestCheckResourceAttr(networkAddr, "gateway_allows.0.ports", "443"),
				resource.TestCheckNoResourceAttr(networkAddr, "gateway_allows.0.source"),
				resource.TestCheckNoResourceAttr(networkAddr, "kubernetes.%"),
				resource.TestCheckNoResourceAttr(networkAddr, "vpc.%"),
			),
		}},
	})
}

func TestAccProjectNetwork_kubernetesAndVpc(t *testing.T) {
	m, pid := networkProject(t, "k8s")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{
				Config: networkHCL(pid),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(networkAddr, "deployment_backend", "k8s"),
					resource.TestCheckResourceAttr(networkAddr, "kubernetes.namespace", "shop-prod"),
					resource.TestCheckResourceAttr(networkAddr, "kubernetes.policies.0.name", "default-deny"),
					resource.TestCheckResourceAttr(networkAddr, "vlans.#", "0"),
				),
			},
			{
				PreConfig: func() {
					m.SetProjectNetwork(pid, map[string]any{"network_mode": "vpc", "vpc": map[string]any{
						"name": "vpc-shop", "site": "north", "pool_cidr": "198.51.100.0/22",
						"vnets": []any{map[string]any{"id": "vn-shop-prod", "environment": "prod", "vni": 10304,
							"cidr": "198.51.100.0/26", "gateway": "198.51.100.62", "applied": true,
							"gateway_applied": false, "vms": []any{map[string]any{"name": "prod-shop-app-01",
								"environment": "prod", "address": "198.51.100.10"}}}}}})
				},
				Config: networkHCL(pid),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(networkAddr, "network_mode", "vpc"),
					resource.TestCheckResourceAttr(networkAddr, "vpc.vnets.0.vni", "10304"),
					resource.TestCheckResourceAttr(networkAddr, "vpc.vnets.0.gateway_applied", "false"),
					resource.TestCheckResourceAttr(networkAddr, "vpc.vnets.0.vms.0.address", "198.51.100.10"),
				),
			},
		},
	})
}

func TestAccProjectNetwork_refusals(t *testing.T) {
	m, pid := networkProject(t, "vm")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{
				PreConfig:   func() { m.SetScopes(acctest.DefaultScopes()...) },
				Config:      networkHCL(pid),
				ExpectError: words("networking-read-global"),
			},
			{
				PreConfig: func() {
					m.SetScopes(append(acctest.DefaultScopes(), "networking-read-global")...)
					m.SetMeta(func(meta map[string]any) { meta["platform_version"] = client.ReleaseFleet })
				},
				Config:      networkHCL(pid),
				ExpectError: words("needs platform release " + client.ReleaseProjectNetwork),
			},
			{
				PreConfig: func() {
					m.SetMeta(func(meta map[string]any) { meta["platform_version"] = client.ReleaseProjectNetwork })
				},
				Config:      networkHCL("999"),
				ExpectError: words("project_not_found"),
			},
		},
	})
}
