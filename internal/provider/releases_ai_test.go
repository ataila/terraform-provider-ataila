// Copyright (c) 2026 ATAILA Kft.
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/ataila/terraform-provider-ataila/internal/acctest"
)

const (
	promotionAddr = "ataila_release_promotion.test"
	lockAddr      = "ataila_project_prod_lock.test"
	modelAddr     = "ataila_ai_model.test"
	cacheAddr     = "ataila_ai_model_node_cache.test"
)

func promotionHCL(projectID, component, target string, extra ...string) string {
	return providerBlock(false) + fmt.Sprintf(`
resource "ataila_release_promotion" "test" {
  project_id = %q
  component  = %q
  target_env = %q
%s
}
`, projectID, component, target, indent(extra))
}

// releaseProject is a Kubernetes project on a fresh mock.
func releaseProject(t *testing.T) (*acctest.MockAPI, string) {
	t.Helper()
	fastPoll(t)
	m := newMock(t)
	_, tenant := m.AddCustomer("EXAMPLE", "example")
	return m, m.AddTestProject(tenant, "shop", "k8s")
}

// A dev promotion runs to succeeded; a uat promotion then takes the version
// dev reported; destroy only forgets; import by id.
func TestAccReleasePromotion_DevThenUAT(t *testing.T) {
	m, project := releaseProject(t)
	var id string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{
				Config: promotionHCL(project, "app-api", "dev", `version = "1.4.0"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					stateAttr(promotionAddr, "id", &id),
					resource.TestCheckResourceAttr(promotionAddr, "status", "succeeded"),
					resource.TestCheckResourceAttr(promotionAddr, "portal_status", "succeeded"),
					resource.TestCheckResourceAttr(promotionAddr, "source_env", "sandbox"),
					resource.TestCheckResourceAttr(promotionAddr, "requested_via", "service_account"),
					resource.TestCheckResourceAttrSet(promotionAddr, "requested_token_id"),
					resource.TestCheckResourceAttrSet(promotionAddr, "completed_at"),
					resource.TestCheckResourceAttrSet(promotionAddr, "pipeline_url"),
					resource.TestCheckResourceAttr(promotionAddr, "wait_for_approval", "false"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{ResourceName: promotionAddr, ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"timeouts"}},
			{
				// A frozen argument fails the plan.
				Config:      promotionHCL(project, "app-api", "dev", `version = "1.5.0"`),
				ExpectError: regexp.MustCompile(`Cannot change version of an existing release promotion`),
			},
			{
				// Another resource: uat takes what dev reported.
				Config:      promotionHCL(project, "app-api", "uat"),
				ExpectError: regexp.MustCompile(`Cannot change target_env of an existing release promotion`),
			},
			{
				Config: providerBlock(false),
				Check: check(func() error {
					if n := m.ReleaseOperationCount(); n != 1 {
						return fmt.Errorf("%d release operations, want 1 (destroy only forgets)", n)
					}
					return nil
				}),
			},
			{
				Config: promotionHCL(project, "app-api", "uat"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(promotionAddr, "version", "1.4.0"),
					resource.TestCheckResourceAttr(promotionAddr, "source_env", "dev"),
					resource.TestCheckResourceAttr(promotionAddr, "status", "succeeded"),
				),
			},
		},
	})
}

// On a platform that fakes dispatch the operation never completes: create
// succeeds with the status it saw and a warning naming the mode.
func TestAccReleasePromotion_DryrunWarns(t *testing.T) {
	m, project := releaseProject(t)
	m.SetReleaseDispatch("dryrun")
	factories, rec := recordingProvider()
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{{
			Config: promotionHCL(project, "www", "dev", `version = "0.0.0-test"`),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(promotionAddr, "status", "running"),
				resource.TestCheckResourceAttr(promotionAddr, "pipeline_url", "https://gitlab.example.com/pipelines/900000001"),
				rec.expectWarning("The platform fakes dispatch (dryrun)"),
			),
			ConfigPlanChecks: resource.ConfigPlanChecks{
				PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
			},
		}},
	})
}

// /meta says simulate before anything is booked: a warning, then the booking.
func TestAccReleasePromotion_SimulateWarnsEarly(t *testing.T) {
	m, project := releaseProject(t)
	m.SetDispatchMode("simulate")
	factories, rec := recordingProvider()
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{{
			Config: promotionHCL(project, "app-api", "dev", `version = "0.0.0-test"`),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(promotionAddr, "status", "running"),
				rec.expectWarning("The platform fakes dispatch (simulate): the promotion will not run", "(GET /meta)"),
				check(func() error {
					n := 0
					for _, w := range rec.warnings() {
						if strings.Contains(w, "fakes dispatch") {
							n++
						}
					}
					if n != 1 {
						return fmt.Errorf("%d dispatch warnings, want 1", n)
					}
					return nil
				}),
			),
		}},
	})
}

// PROD waits for a person: by default create ends at once with a warning;
// wait_for_approval waits for the decision; a rejection fails and taints;
// the timeout ends create with a warning, never a second request.
func TestAccReleasePromotion_Prod(t *testing.T) {
	m, project := releaseProject(t)
	m.ReportVersion(project, "uat", "app-api", "2.0.0")
	factories, rec := recordingProvider()
	var first string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: promotionHCL(project, "app-api", "prod"),
				Check: resource.ComposeAggregateTestCheckFunc(
					stateAttr(promotionAddr, "id", &first),
					resource.TestCheckResourceAttr(promotionAddr, "status", "awaiting_approval"),
					resource.TestCheckResourceAttr(promotionAddr, "portal_status", "pending"),
					resource.TestCheckResourceAttr(promotionAddr, "version", "2.0.0"),
					rec.expectWarning("A person must approve the promotion in the portal"),
				),
			},
			{
				// Approved in the portal: a refresh follows it, with no plan change.
				PreConfig: func() { m.ApproveRelease(first) },
				Config:    promotionHCL(project, "app-api", "prod"),
				Check:     resource.TestCheckResourceAttr(promotionAddr, "portal_status", "approved"),
			},
			{Config: providerBlock(false)},
			{
				PreConfig: func() { m.DecideAfter(2, true) },
				Config:    promotionHCL(project, "app-api", "prod", `wait_for_approval = true`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(promotionAddr, "status", "succeeded"),
					resource.TestCheckResourceAttr(promotionAddr, "decided_by", "approver@example.com"),
				),
			},
			{Config: providerBlock(false)},
			{
				PreConfig:   func() { m.DecideAfter(1, false) },
				Config:      promotionHCL(project, "app-api", "prod", `wait_for_approval = true`),
				ExpectError: words("The promotion failed (rejected) .* Not this week"),
			},
			{
				// Tainted: the next plan requests again.
				Config:             promotionHCL(project, "app-api", "prod", `wait_for_approval = true`),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{Config: providerBlock(false)},
			{
				Config: promotionHCL(project, "app-api", "prod", `wait_for_approval = true`,
					`timeouts {`, `  create = "300ms"`, `}`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(promotionAddr, "status", "awaiting_approval"),
					rec.expectWarning("The promotion did not finish in time"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

func TestAccReleasePromotion_Refusals(t *testing.T) {
	m, project := releaseProject(t)
	_, tenant := m.AddCustomer("VMS", "vms")
	vm := m.AddTestProject(tenant, "legacy", "vm")
	copyID := m.AddDataCopy(project)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{Config: promotionHCL(project, "app-api", "dev"), ExpectError: words("A promotion into dev needs a version")},
			{Config: promotionHCL(vm, "app-api", "dev", `version = "1.0.0"`), ExpectError: words("vm_projects_unsupported")},
			{Config: promotionHCL(project, "app-api", "prod"), ExpectError: words("version_not_at_source")},
			{
				PreConfig:   func() { m.ReportVersion(project, "dev", "app-api", "3.0.0") },
				Config:      promotionHCL(project, "app-api", "uat", `version = "2.9.9"`),
				ExpectError: words("2.9.9 is not what dev runs .* version_not_at_source"),
			},
			{
				Config: promotionHCL(project, "app-api", "dev", `version = "1.0.0"`), ResourceName: promotionAddr,
				ImportState: true, ImportStateId: copyID, ExpectError: words("Only promotions can be imported"),
			},
			{
				PreConfig:   func() { m.FailNextRelease() },
				Config:      promotionHCL(project, "app-api", "dev", `version = "1.0.1"`),
				ExpectError: words("The promotion failed (release_failed)"),
			},
		},
	})
}

func lockHCL(project string, extra ...string) string {
	return providerBlock(false) + fmt.Sprintf(`
resource "ataila_project_prod_lock" "test" {
  project_id = %q
%s
}
`, project, indent(extra))
}

func TestAccProjectProdLock(t *testing.T) {
	_, project := releaseProject(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{
				Config: lockHCL(project, `locked = true`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(lockAddr, "locked", "true"),
					resource.TestCheckResourceAttrSet(lockAddr, "locked_at"),
					resource.TestCheckResourceAttr(lockAddr, "locked_by", "ci-bot@service-account.invalid"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{ResourceName: lockAddr, ImportState: true, ImportStateId: project, ImportStateVerify: true},
			{Config: lockHCL(project, `locked = false`), ExpectError: words("Unlocking needs a confirmation")},
			{Config: lockHCL(project, `locked = false`, `confirm_unlock = "other"`), ExpectError: words("The unlock was not confirmed")},
			{
				Config: lockHCL(project, `locked = false`, `confirm_unlock = "shop"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(lockAddr, "locked", "false"),
					resource.TestCheckNoResourceAttr(lockAddr, "locked_at"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

func TestAccReleaseDataSources(t *testing.T) {
	m, project := releaseProject(t)
	m.ReportVersion(project, "dev", "app-api", "3.0.0")
	cfg := promotionHCL(project, "app-api", "uat") + fmt.Sprintf(`
data "ataila_release_state" "this" {
  project_id = %q
  depends_on = [ataila_release_promotion.test]
}
data "ataila_release_operation" "this" {
  id = ataila_release_promotion.test.id
}
`, project)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config: cfg,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.ataila_release_state.this", "deployment_backend", "k8s"),
				resource.TestCheckResourceAttr("data.ataila_release_state.this", "versions.#", "2"),
				resource.TestCheckResourceAttr("data.ataila_release_state.this", "versions.1.env", "uat"),
				resource.TestCheckResourceAttr("data.ataila_release_state.this", "versions.1.last_reported_version", "3.0.0"),
				resource.TestCheckResourceAttr("data.ataila_release_state.this", "prod_data_locked", "false"),
				resource.TestCheckResourceAttr("data.ataila_release_state.this", "pending_operation_ids.#", "0"),
				resource.TestCheckResourceAttr("data.ataila_release_operation.this", "operation", "promote_build"),
				resource.TestCheckResourceAttr("data.ataila_release_operation.this", "status", "succeeded"),
				resource.TestCheckResourceAttrPair("data.ataila_release_operation.this", "operation_id", promotionAddr, "operation_id"),
			),
		}},
	})
}

// ── AI models ────────────────────────────────────────────────────────────────

func modelHCL(repo string, extra ...string) string {
	return providerBlock(false) + fmt.Sprintf(`
resource "ataila_ai_model" "test" {
  repo = %q
%s
}
`, repo, indent(extra))
}

func TestAccAIModel_Lifecycle(t *testing.T) {
	m := newMock(t)
	var id string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		CheckDestroy: func(*terraform.State) error {
			if _, found := m.AIModel(id); found {
				return fmt.Errorf("model %s still in the catalogue", id)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: modelHCL("example-lab/test-model-7B", `vendor = "Example Lab"`, `param_count_b = 7.25`,
					`benchmarks = { mmlu = "71.50", note = "draft" }`, `notes = "first"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					stateAttr(modelAddr, "id", &id),
					resource.TestCheckResourceAttr(modelAddr, "display_name", "test-model-7B"),
					resource.TestCheckResourceAttr(modelAddr, "status", "planned"),
					resource.TestCheckNoResourceAttr(modelAddr, "location"),
					resource.TestCheckResourceAttr(modelAddr, "offline_ready", "false"),
					resource.TestCheckResourceAttr(modelAddr, "gated", "false"),
					resource.TestCheckResourceAttr(modelAddr, "param_count_b", "7.25"),
					resource.TestCheckResourceAttr(modelAddr, "benchmarks.mmlu", "71.50"),
					resource.TestCheckResourceAttr(modelAddr, "node_caches.#", "0"),
					mockField(m.AIModel, &id, "param_count_b", 7.25),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// Left out: the vendor is kept; notes change in place.
				Config: modelHCL("example-lab/test-model-7B", `param_count_b = 7.25`,
					`benchmarks = { mmlu = "71.50", note = "draft" }`, `notes = "second"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(modelAddr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					mockField(m.AIModel, &id, "notes", "second"),
					resource.TestCheckResourceAttr(modelAddr, "vendor", "Example Lab"),
				),
			},
			{ResourceName: modelAddr, ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"benchmarks"}},
			{ResourceName: modelAddr, ImportState: true, ImportStateId: "repo:example-lab/test-model-7B", ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"benchmarks"}},
			{
				Config:      modelHCL("example-lab/other-model"),
				ExpectError: regexp.MustCompile(`Cannot change repo of an existing AI model`),
			},
			{
				Config:      modelHCL("example-lab/test-model-7B", `status = "owned"`),
				ExpectError: regexp.MustCompile(`(?i)read-only|Invalid Configuration`),
			},
			{Config: providerBlock(false)},
		},
	})
}

// Benchmarks written with another number spelling than the platform's are
// no change (the case after an import).
func TestAccAIModel_BenchmarkSpelling(t *testing.T) {
	newMock(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{Config: modelHCL("example-lab/bench-model", `benchmarks = { mmlu = "71.5" }`)},
			{Config: modelHCL("example-lab/bench-model", `benchmarks = { mmlu = "71.50" }`), PlanOnly: true},
			{
				Config: modelHCL("example-lab/bench-model", `benchmarks = { mmlu = "72" }`),
				Check:  resource.TestCheckResourceAttr(modelAddr, "benchmarks.mmlu", "72"),
			},
		},
	})
}

// The platform keeps a row that records weights; destroy reports the remedy
// and does not retry.
func TestAccAIModel_DeleteRefused(t *testing.T) {
	m := newMock(t)
	var id string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{Config: modelHCL("example-lab/test..model"), ExpectError: words("must not contain")},
			{Config: modelHCL("example-lab/kept-model"), Check: stateAttr(modelAddr, "id", &id)},
			{
				PreConfig:   func() { m.GiveCentralCopy(id) },
				Config:      providerBlock(false),
				ExpectError: words("The platform keeps the AI model example-lab/kept-model (model_has_central_copy)"),
			},
			{
				PreConfig: func() { m.DropCentralCopy(id) },
				Config:    providerBlock(false),
				Check: check(func() error {
					if n := m.Calls("DELETE", "/ai-models/"+id); n != 2 {
						return fmt.Errorf("%d DELETEs, want 2", n)
					}
					return nil
				}),
			},
			{Config: modelHCL("example-lab/kept-model-2"), ResourceName: modelAddr, ImportState: true,
				ImportStateId: "repo:example-lab/none", ExpectError: words("0 models have repo")},
		},
	})
}

func cacheHCL(repo, node string, extra ...string) string {
	return modelHCL(repo) + fmt.Sprintf(`
resource "ataila_ai_model_node_cache" "test" {
  model_id = ataila_ai_model.test.id
  node     = %q
%s
}
`, node, indent(extra))
}

func TestAccAIModelNodeCache(t *testing.T) {
	fastPoll(t)
	m := newMock(t)
	m.AddAINode("ai-a")
	factories, rec := recordingProvider()
	var id string
	repo := "example-lab/cached-model"
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{Config: modelHCL(repo), Check: stateAttr(modelAddr, "id", &id)},
			{Config: cacheHCL(repo, "ai-a"), ExpectError: words("no_central_copy")},
			{
				PreConfig:   func() { m.GiveCentralCopy(id) },
				Config:      cacheHCL(repo, "ai-x"),
				ExpectError: words("unknown_node"),
			},
			{
				Config: cacheHCL(repo, "ai-a"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(cacheAddr, "state", "cached"),
					resource.TestCheckResourceAttr(cacheAddr, "operation_id", "model-store-run:1"),
					resource.TestCheckResourceAttr(cacheAddr, "size_gb", "12.5"),
					check(func() error {
						if s := m.NodeCacheState(id, "ai-a"); s != "cached" {
							return fmt.Errorf("mock cache state %q", s)
						}
						return nil
					}),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				ResourceName: cacheAddr, ImportState: true, ImportStateVerify: true,
				ImportStateVerifyIgnore: []string{"operation_id", "timeouts", "updated_at"},
			},
			{
				// Monitoring down: whether the model is loaded is unknown; refused, not retried.
				Config:      modelHCL(repo),
				ExpectError: words("loaded_state_unknown"),
			},
			{
				PreConfig: func() { m.SetMonitoring(true) },
				Config:    modelHCL(repo),
				Check: check(func() error {
					if s := m.NodeCacheState(id, "ai-a"); s != "" {
						return fmt.Errorf("cache still %q", s)
					}
					if n := m.Calls("DELETE", "/ai-models/"+id+"/node-caches/ai-a"); n != 2 {
						return fmt.Errorf("%d uncache calls, want 2 (a 503 loaded_state_unknown is final)", n)
					}
					return nil
				}),
			},
			{
				// /meta says the platform fakes dispatch: the provider fails before any write.
				PreConfig:   func() { m.SetDispatchMode("simulate") },
				Config:      cacheHCL(repo, "ai-a"),
				ExpectError: words("The platform fakes dispatch (simulate): the store run would never run .* Nothing was sent"),
			},
			{
				PreConfig: func() { m.SetDispatchMode("live") },
				Config:    modelHCL(repo),
				Check: check(func() error {
					if n := m.Calls("PUT", "/ai-models/"+id+"/node-caches/ai-a"); n != 2 {
						return fmt.Errorf("%d cache PUTs, want 2 (none under a faked dispatch)", n)
					}
					return nil
				}),
			},
			{
				// /meta says live but the store run comes back faked: caught after the 202.
				PreConfig:   func() { m.SetStoreDispatch("dryrun") },
				Config:      cacheHCL(repo, "ai-a"),
				ExpectError: words("The platform fakes dispatch (dryrun): the store run will never run"),
			},
			{
				PreConfig: func() {
					m.SetStoreDispatch("live")
					m.FailNextStoreRun()
				},
				Config:      cacheHCL(repo, "ai-a"),
				ExpectError: words("The store run failed (run_failed)"),
			},
			{
				// A copy the node already holds is adopted.
				PreConfig: func() { m.SetNodeCache(id, "ai-a", "cached") },
				Config:    cacheHCL(repo, "ai-a"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(cacheAddr, "state", "cached"),
					resource.TestCheckNoResourceAttr(cacheAddr, "operation_id"),
					rec.expectWarning("Existing node cache adopted"),
				),
			},
			{
				// Destroy removes the copy, then the row (once no central copy is recorded).
				PreConfig: func() { m.DropCentralCopy(id) },
				Config:    providerBlock(false),
				Check: check(func() error {
					if _, found := m.AIModel(id); found {
						return fmt.Errorf("model still there")
					}
					return nil
				}),
			},
		},
	})
}

// ── AI Center and the catalogue data sources ─────────────────────────────────

func TestAccAIDataSources_EmptyAndUnmonitored(t *testing.T) {
	newMock(t)
	cfg := providerBlock(false) + `
data "ataila_ai_nodes" "all" {}
data "ataila_dgx_clusters" "all" {}
data "ataila_ai_model_launch_catalog" "all" {}
data "ataila_ai_models" "all" {}
data "ataila_ai_model_storage" "this" {}
data "ataila_ai_load_targets" "all" {}
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{{
			Config: cfg,
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("data.ataila_ai_nodes.all", "monitoring_reachable", "false"),
				resource.TestCheckResourceAttr("data.ataila_ai_nodes.all", "nodes.#", "0"),
				resource.TestCheckResourceAttr("data.ataila_dgx_clusters.all", "clusters.#", "0"),
				resource.TestCheckResourceAttr("data.ataila_ai_model_launch_catalog.all", "entries.#", "0"),
				resource.TestCheckResourceAttr("data.ataila_ai_models.all", "models.#", "0"),
				resource.TestCheckResourceAttr("data.ataila_ai_model_storage.this", "shares.#", "2"),
				resource.TestCheckNoResourceAttr("data.ataila_ai_model_storage.this", "captured_at"),
				resource.TestCheckResourceAttr("data.ataila_ai_load_targets.all", "targets.#", "2"),
				resource.TestCheckResourceAttr("data.ataila_ai_load_targets.all", "targets.0.usable_vram_gb", "43.2"),
				resource.TestCheckResourceAttr("data.ataila_ai_load_targets.all", "targets.1.members.#", "2"),
			),
		}},
	})
}

func TestAccAIDataSources_Fleet(t *testing.T) {
	m := newMock(t)
	m.AddAINode("ai-a")
	m.AddAINode("ai-b")
	m.AddDGXCluster("pair-1", "dgx-1", "dgx-2")
	m.AddLaunchEntry("ai-a:model-a", "ai-a", "example/model-a", true)
	m.AddLaunchEntry("ai-b:model-b", "ai-b", "example/model-b", false)
	cfg := providerBlock(false) + `
resource "ataila_ai_model" "test" {
  repo     = "example-lab/listed-model"
  category = "chat"
}
data "ataila_ai_nodes" "all" {}
data "ataila_ai_node" "a" {
  hostname = "ai-a"
}
data "ataila_dgx_clusters" "pair" {
  name = "pair-1"
}
data "ataila_ai_model_launch_catalog" "enabled" {
  enabled = true
}
data "ataila_ai_model" "by_repo" {
  repo       = ataila_ai_model.test.repo
  depends_on = [ataila_ai_model.test]
}
data "ataila_ai_models" "chat" {
  category   = "chat"
  depends_on = [ataila_ai_model.test]
}
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.ataila_ai_nodes.all", "monitoring_reachable", "false"),
					resource.TestCheckResourceAttr("data.ataila_ai_nodes.all", "nodes.#", "2"),
					resource.TestCheckNoResourceAttr("data.ataila_ai_nodes.all", "nodes.0.status"),
					resource.TestCheckResourceAttr("data.ataila_ai_node.a", "monitoring_reachable", "false"),
					resource.TestCheckResourceAttr("data.ataila_ai_node.a", "gpu_class", "rtx-3090"),
					resource.TestCheckResourceAttr("data.ataila_dgx_clusters.pair", "clusters.#", "1"),
					resource.TestCheckResourceAttr("data.ataila_dgx_clusters.pair", "clusters.0.members.0.role", "head"),
					resource.TestCheckResourceAttr("data.ataila_ai_model_launch_catalog.enabled", "entries.#", "1"),
					resource.TestCheckResourceAttr("data.ataila_ai_model_launch_catalog.enabled", "entries.0.port", "8000"),
					resource.TestCheckResourceAttrPair("data.ataila_ai_model.by_repo", "id", modelAddr, "id"),
					resource.TestCheckResourceAttr("data.ataila_ai_model.by_repo", "status", "planned"),
					resource.TestCheckResourceAttr("data.ataila_ai_models.chat", "models.#", "1"),
				),
			},
			{
				PreConfig: func() {
					m.SetMonitoring(true)
					m.LoadOnNode("ai-a", "example/model-a")
				},
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.ataila_ai_nodes.all", "monitoring_reachable", "true"),
					resource.TestCheckResourceAttr("data.ataila_ai_node.a", "status", "idle"),
					resource.TestCheckResourceAttr("data.ataila_ai_node.a", "gpu_util_avg_pct", "12.5"),
					resource.TestCheckResourceAttr("data.ataila_ai_node.a", "models.0.tiers.0", "general"),
				),
			},
		},
	})
}
