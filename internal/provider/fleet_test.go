// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/ataila/terraform-provider-ataila/internal/acctest"
	"github.com/ataila/terraform-provider-ataila/internal/client"
)

// fleetMock serves the fleet inventory (platform 1.0.400 on) to a token
// holding both fleet read keys.
func fleetMock(t *testing.T) *acctest.MockAPI {
	t.Helper()
	m := newMock(t)
	m.SetMeta(func(meta map[string]any) { meta["platform_version"] = client.ReleaseFleet })
	m.SetScopes(append(acctest.DefaultScopes(), "infrastructure-read-global", "certificates-read-global")...)
	return m
}

const fleetHCL = `
data "ataila_vms" "all" {}

data "ataila_vms" "shop_prod_db" {
  project_id  = "7"
  environment = "prod"
  role        = "db"
}

data "ataila_vm_nodes" "all" {}

data "ataila_k8s_clusters" "all" {}

data "ataila_certificates" "all" {}

data "ataila_certificates" "expiring" {
  status = "expiring"
}
`

func TestAccFleet_reads(t *testing.T) {
	m := fleetMock(t)
	for i := 0; i < 205; i++ { // more than one page of VMs
		m.AddVM("9", "dev", "docker-apps", "running")
	}
	m.AddVM("9", "prod", "db", "running") // the project filter, not the others, leaves it out
	m.AddVM("7", "prod", "db", "running")
	m.AddVM("7", "prod", "minio", "running")
	m.AddVMNode("sec-gw-01", "gateway")
	m.AddK8sCluster("prod-k8s", true, "prod")
	m.AddK8sCluster("lab-k8s", false)
	m.AddCertificate("7", "shop.example.com", "valid")
	m.AddCertificate("", "*.example.net", "expiring")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config: providerBlock(false) + fleetHCL,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.ataila_vms.all", "vms.#", "208"),
				resource.TestCheckResourceAttr("data.ataila_vms.all", "vms.207.role", "minio"),
				resource.TestCheckResourceAttr("data.ataila_vms.shop_prod_db", "vms.#", "1"),
				resource.TestCheckResourceAttr("data.ataila_vms.shop_prod_db", "vms.0.name", "prod-p7-db-207"),
				resource.TestCheckResourceAttr("data.ataila_vms.shop_prod_db", "vms.0.ram_gb", "8"),
				resource.TestCheckResourceAttr("data.ataila_vms.shop_prod_db", "vms.0.disk_class", "N"),
				resource.TestCheckResourceAttr("data.ataila_vm_nodes.all", "nodes.#", "1"),
				resource.TestCheckResourceAttr("data.ataila_vm_nodes.all", "nodes.0.name", "sec-gw-01"),
				resource.TestCheckNoResourceAttr("data.ataila_vm_nodes.all", "nodes.0.description"),
				resource.TestCheckResourceAttr("data.ataila_k8s_clusters.all", "clusters.#", "2"),
				resource.TestCheckResourceAttr("data.ataila_k8s_clusters.all", "clusters.0.name", "lab-k8s"),
				resource.TestCheckResourceAttr("data.ataila_k8s_clusters.all", "clusters.1.default_for_envs.0", "prod"),
				resource.TestCheckResourceAttr("data.ataila_k8s_clusters.all", "clusters.1.is_default_home", "true"),
				resource.TestCheckResourceAttr("data.ataila_certificates.all", "certificates.#", "2"),
				resource.TestCheckResourceAttr("data.ataila_certificates.all", "certificates.0.project_id", "7"),
				resource.TestCheckNoResourceAttr("data.ataila_certificates.all", "certificates.1.project_id"),
				resource.TestCheckResourceAttr("data.ataila_certificates.expiring", "certificates.#", "1"),
				resource.TestCheckResourceAttr("data.ataila_certificates.expiring", "certificates.0.fqdn", "*.example.net"),
			),
		}},
	})
}

func TestAccFleet_needsItsKeys(t *testing.T) {
	m := newMock(t) // DefaultScopes holds neither fleet key
	m.SetMeta(func(meta map[string]any) { meta["platform_version"] = client.ReleaseFleet })
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{Config: providerBlock(false) + `data "ataila_vms" "all" {}`, ExpectError: words("infrastructure-read-global")},
			{Config: providerBlock(false) + `data "ataila_certificates" "all" {}`, ExpectError: words("certificates-read-global")},
		},
	})
}

func TestAccFleet_olderPlatformRefused(t *testing.T) {
	m := fleetMock(t)
	m.SetMeta(func(meta map[string]any) { meta["platform_version"] = client.ReleaseBackupPlans })
	refused := words("needs platform release " + client.ReleaseFleet)
	steps := []resource.TestStep{}
	for _, hcl := range []string{`data "ataila_vms" "x" {}`, `data "ataila_vm_nodes" "x" {}`,
		`data "ataila_k8s_clusters" "x" {}`, `data "ataila_certificates" "x" {}`} {
		steps = append(steps, resource.TestStep{Config: providerBlock(false) + hcl, ExpectError: refused})
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: protoV6, Steps: steps})
}

func TestAccFleet_validation(t *testing.T) {
	fleetMock(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{Config: providerBlock(false) + `data "ataila_vms" "x" { environment = "staging" }`,
				ExpectError: words("value must be one of")},
			{Config: providerBlock(false) + `data "ataila_certificates" "x" { project_id = "07" }`,
				ExpectError: words("must be a project id")},
		},
	})
}
