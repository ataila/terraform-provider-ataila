// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"fmt"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/ataila/terraform-provider-ataila/internal/acctest"
)

const customerAddr = "ataila_customer.test"

// archived checks, after the final destroy, that the customer was archived
// (not deleted: the platform keeps it).
func archived(m *acctest.MockAPI, id *string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		c, ok := m.Customer(*id)
		if !ok || c["status"] != "archived" {
			return fmt.Errorf("customer %s after destroy: %v", *id, c)
		}
		return nil
	}
}

// Create with an allocated index, read back without a diff, update in place,
// clear a nullable field, import by id and by short name, then destroy with
// both switches on, which archives.
func TestAccCustomerResource_Lifecycle(t *testing.T) {
	m := newMock(t)
	contact := m.AddUser("ops@example.com")
	var id, primary string

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		CheckDestroy:             archived(m, &id),
		Steps: []resource.TestStep{
			{
				Config: providerBlock(false) + customerHCL("EXAMPLE", "example"),
				Check: resource.ComposeAggregateTestCheckFunc(
					stateAttr(customerAddr, "id", &id),
					stateAttr(customerAddr, "primary_tenant_id", &primary),
					resource.TestCheckResourceAttr(customerAddr, "customer_index", "2"),
					resource.TestCheckResourceAttr(customerAddr, "short_name", "EXAMPLE"),
					resource.TestCheckResourceAttr(customerAddr, "edition", "sp"),
					resource.TestCheckResourceAttr(customerAddr, "billing_tier", "INTERNAL"),
					resource.TestCheckResourceAttr(customerAddr, "default_email_tier", "3"),
					resource.TestCheckResourceAttr(customerAddr, "status", "active"),
					resource.TestCheckNoResourceAttr(customerAddr, "notes"),
					resource.TestCheckResourceAttrSet(customerAddr, "created_at"),
					// The primary contact became a member of the primary tenant.
					check(func() error {
						if role := m.MembershipRole(primary, contact); role != "member" {
							return fmt.Errorf("primary contact membership = %q", role)
						}
						return nil
					}),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				Config: providerBlock(false) + customerHCL("EXAMPLE", "example",
					`notes        = "Signed in September."`,
					`status       = "suspended"`,
					`billing_tier = "PAYING"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(customerAddr, plancheck.ResourceActionUpdate),
					},
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPtr(customerAddr, "id", &id),
					resource.TestCheckResourceAttr(customerAddr, "notes", "Signed in September."),
					mockField(m.Customer, &id, "status", "suspended"),
					mockField(m.Customer, &id, "billing_tier", "PAYING"),
					check(func() error {
						for _, r := range m.Requests() {
							if r.Method == "PATCH" && r.Header.Get("Content-Type") != "application/merge-patch+json" {
								return fmt.Errorf("PATCH %s sent as %q", r.Path, r.Header.Get("Content-Type"))
							}
						}
						return nil
					}),
				),
			},
			{
				// Removing notes from the configuration clears them (PATCH null).
				Config: providerBlock(false) + customerHCL("EXAMPLE", "example",
					`status       = "suspended"`, `billing_tier = "PAYING"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(customerAddr, "notes"),
					mockField(m.Customer, &id, "notes", nil),
				),
			},
			{
				ResourceName:      customerAddr,
				ImportState:       true,
				ImportStateVerify: true,
			},
			{
				ResourceName:      customerAddr,
				ImportState:       true,
				ImportStateId:     "short_name:EXAMPLE",
				ImportStateVerify: true,
			},
			{
				ResourceName:  customerAddr,
				ImportState:   true,
				ImportStateId: "short_name:NOSUCH",
				ExpectError:   words(`0 customers have short_name "NOSUCH"`),
			},
			{
				// Both destroy switches on for the final destroy.
				PreConfig: allowDestroyEverywhere(m),
				Config: providerBlock(true) + customerHCL("EXAMPLE", "example",
					`status       = "suspended"`, `billing_tier = "PAYING"`),
			},
		},
	})
}

// An explicit index and every optional attribute, including a status the
// create itself cannot set; the configured spelling of the e-mail domain is
// kept although the platform stores it in lower case.
func TestAccCustomerResource_AllArguments(t *testing.T) {
	m := newMock(t)
	var id string
	cfg := providerBlock(true) + `
resource "ataila_customer" "test" {
  customer_index        = 42
  short_name            = "SAMPLE"
  long_name             = "Sample Industries"
  gitlab_group          = "sample"
  edition               = "enterprise"
  primary_contact_email = "Billing@Sample.EXAMPLE"
  primary_contact_name  = "Billing Team"
  default_email_tier    = 1
  billing_tier          = "PAYING"
  status                = "suspended"
  notes                 = "Pilot."
}
`
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		PreCheck:                 allowDestroyEverywhere(m),
		CheckDestroy:             archived(m, &id),
		Steps: []resource.TestStep{{
			Config: cfg,
			Check: resource.ComposeAggregateTestCheckFunc(
				stateAttr(customerAddr, "id", &id),
				resource.TestCheckResourceAttr(customerAddr, "customer_index", "42"),
				resource.TestCheckResourceAttr(customerAddr, "edition", "enterprise"),
				resource.TestCheckResourceAttr(customerAddr, "status", "suspended"),
				resource.TestCheckResourceAttr(customerAddr, "default_email_tier", "1"),
				resource.TestCheckResourceAttr(customerAddr, "primary_contact_email", "Billing@Sample.EXAMPLE"),
				mockField(m.Customer, &id, "primary_contact_email", "Billing@sample.example"),
				mockField(m.Customer, &id, "status", "suspended"),
				// Created suspended in one call.
				check(func() error {
					if n := m.Calls("PATCH", "/customers"); n != 0 {
						return fmt.Errorf("a suspended create sent %d PATCH requests", n)
					}
					return nil
				}),
			),
		}},
	})
}

// Changing a frozen key fails the plan, names the key, and sends nothing.
func TestAccCustomerResource_FrozenKeysFailThePlan(t *testing.T) {
	m := newMock(t)
	var id string
	var mutations int
	base := func(extra ...string) string {
		return providerBlock(false) + customerHCL("EXAMPLE", "example", extra...)
	}
	frozenStep := func(cfg, key string) resource.TestStep {
		return resource.TestStep{
			Config:      cfg,
			PlanOnly:    true,
			ExpectError: words("Cannot change " + key + " of an existing customer .* only archives it"),
		}
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		CheckDestroy:             archived(m, &id),
		Steps: []resource.TestStep{
			{
				Config: base(),
				Check: resource.ComposeAggregateTestCheckFunc(
					stateAttr(customerAddr, "id", &id),
					check(func() error { mutations = m.Mutations(); return nil }),
				),
			},
			frozenStep(providerBlock(false)+customerHCL("EXAMPLX", "example"), "short_name"),
			frozenStep(providerBlock(false)+customerHCL("EXAMPLE", "example-two"), "gitlab_group"),
			frozenStep(base(`edition = "enterprise"`), "edition"),
			frozenStep(base(`customer_index = 7`), "customer_index"),
			{
				// Not only plan: apply stops at the same place.
				Config:      providerBlock(false) + customerHCL("EXAMPLX", "example"),
				ExpectError: words("Cannot change short_name of an existing customer"),
			},
			{
				Config:   base(`customer_index = 2`), // the current value is fine
				PlanOnly: true,
			},
			{
				PreConfig: func() {
					if got := m.Mutations(); got != mutations {
						t.Errorf("a refused frozen-key change sent %d mutating requests", got-mutations)
					}
					m.SetTokenAllowDestroy(true)
				},
				Config: providerBlock(true) + customerHCL("EXAMPLE", "example"),
			},
		},
	})
}

// A change made in the portal shows in the next plan and is set back; an
// archive made in the portal fails the plan with the way out.
func TestAccCustomerResource_Drift(t *testing.T) {
	m := newMock(t)
	var id string
	cfg := providerBlock(false) + customerHCL("DRIFT", "drift", `notes = "Keep."`)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		CheckDestroy:             archived(m, &id),
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check:  stateAttr(customerAddr, "id", &id),
			},
			{
				PreConfig: func() {
					m.SetCustomerField(id, "long_name", "Renamed In The Portal")
					m.SetCustomerField(id, "notes", nil)
				},
				Config:             cfg,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(customerAddr, plancheck.ResourceActionUpdate),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					mockField(m.Customer, &id, "long_name", "Example Holdings Ltd"),
					mockField(m.Customer, &id, "notes", "Keep."),
				),
			},
			{
				PreConfig:   func() { m.SetCustomerField(id, "status", "archived") },
				Config:      cfg,
				PlanOnly:    true,
				ExpectError: words("The customer is archived .* terraform state rm ataila_customer"),
			},
			{
				// Destroying an archived customer succeeds and changes nothing.
				PreConfig: allowDestroyEverywhere(m),
				Config:    providerBlock(true),
			},
		},
	})
}

// Destroy is refused before any API call while the provider's allow_destroy
// is off (the default), with both switches and the state-removal way out.
func TestAccCustomerResource_DestroyRefusedByDefault(t *testing.T) {
	m := newMock(t)
	var id string
	cfg := `provider "ataila" {}` + "\n" + customerHCL("KEEP", "keep")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		CheckDestroy:             archived(m, &id),
		Steps: []resource.TestStep{
			{Config: cfg, Check: stateAttr(customerAddr, "id", &id)},
			{
				Config:  cfg,
				Destroy: true,
				ExpectError: words("Destroying a customer is not allowed .* allow_destroy = true .* destroy allowed " +
					".* tofu state rm ataila_customer.<name> .* terraform state rm ataila_customer.<name>"),
			},
			{
				// Removing the resource from the configuration is a destroy too.
				Config:      `provider "ataila" {}`,
				ExpectError: words("Destroying a customer is not allowed"),
			},
			{
				PreConfig: func() {
					if n := m.Calls("DELETE", "/customers"); n != 0 {
						t.Errorf("%d DELETE requests were sent although destroy is off", n)
					}
					m.SetTokenAllowDestroy(true)
				},
				Config: providerBlock(true) + customerHCL("KEEP", "keep"),
			},
		},
	})
}

// With the provider's switch on, the platform still refuses a token minted
// without allow_destroy, and the diagnostic says which switch is off.
func TestAccCustomerResource_DestroyRefusedByToken(t *testing.T) {
	m := newMock(t)
	var id string
	cfg := providerBlock(true) + customerHCL("TOKEN", "token")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		CheckDestroy:             archived(m, &id),
		Steps: []resource.TestStep{
			{Config: cfg, Check: stateAttr(customerAddr, "id", &id)},
			{
				Config:      cfg,
				Destroy:     true,
				ExpectError: words("The token was not created with allow_destroy .* code: destroy_not_allowed"),
			},
			{
				PreConfig: func() {
					if n := m.Calls("DELETE", "/customers"); n != 1 {
						t.Errorf("DELETE sent %d times, want 1", n)
					}
					m.SetTokenAllowDestroy(true)
				},
				Config: cfg,
			},
		},
	})
}

// The platform refuses to archive a customer with projects; the diagnostic
// carries the code and the count it returned.
func TestAccCustomerResource_DestroyRefusedWithProjects(t *testing.T) {
	m := newMock(t)
	var id, primary string
	cfg := providerBlock(true) + customerHCL("BUSY", "busy")
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		PreCheck:                 allowDestroyEverywhere(m),
		CheckDestroy:             archived(m, &id),
		Steps: []resource.TestStep{
			{Config: cfg, Check: resource.ComposeAggregateTestCheckFunc(
				stateAttr(customerAddr, "id", &id),
				stateAttr(customerAddr, "primary_tenant_id", &primary),
			)},
			{
				PreConfig: func() { m.SetTenantBlockers(primary, 2, 0, 0, 0) },
				Config:    cfg,
				Destroy:   true,
				ExpectError: words("The platform refused to archive the customer (customer_has_projects) " +
					".* blockers: projects=2"),
			},
			{
				PreConfig: func() { m.SetTenantBlockers(primary, 0, 0, 0, 0) },
				Config:    cfg,
			},
		},
	})
}

// A warning from the platform reaches the operator as a warning; the create
// succeeds.
func TestAccCustomerResource_WarningsSurface(t *testing.T) {
	m := newMock(t)
	m.SetGitLabOutcome("partial", "The group exists; its owner could not be added.")
	factories, rec := recordingProvider()
	var id string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		PreCheck:                 allowDestroyEverywhere(m),
		CheckDestroy:             archived(m, &id),
		Steps: []resource.TestStep{{
			Config: providerBlock(true) + customerHCL("WARN", "warn"),
			Check: resource.ComposeAggregateTestCheckFunc(
				stateAttr(customerAddr, "id", &id),
				rec.expectWarning("ATAILA API warning: gitlab_group_not_ready", "its owner could not be added"),
			),
		}},
	})
}

// A create whose answer is lost on the way back is retried with the same
// Idempotency-Key and replayed by the platform: one customer, not two.
func TestAccCustomerResource_CreateSurvivesALostAnswer(t *testing.T) {
	m := newMock(t)
	m.InjectFaults("/customers", acctest.Fault{Status: 502, Method: "POST", AfterHandling: true, RetryAfter: "0"})
	var id string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		PreCheck:                 allowDestroyEverywhere(m),
		CheckDestroy:             archived(m, &id),
		Steps: []resource.TestStep{{
			Config: providerBlock(true) + customerHCL("ONCE", "once"),
			Check: resource.ComposeAggregateTestCheckFunc(
				stateAttr(customerAddr, "id", &id),
				check(func() error {
					if n := m.Calls("POST", "/customers"); n != 2 {
						return fmt.Errorf("POST /customers sent %d times, want 2", n)
					}
					if _, second := m.Customer("2"); second {
						return fmt.Errorf("a second customer was created")
					}
					return nil
				}),
			),
		}},
	})
}

// A create whose first attempt is still running when the retry arrives: the
// platform answers 429 idempotency_request_in_progress with Retry-After, the
// provider waits and retries, and the first attempt's answer is replayed.
func TestAccCustomerResource_CreateRetriedWhileTheFirstAttemptRuns(t *testing.T) {
	m := newMock(t)
	m.InjectFaults("/customers", acctest.Fault{Status: 504, Method: "POST", StillRunning: 1})
	var id string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		PreCheck:                 allowDestroyEverywhere(m),
		CheckDestroy:             archived(m, &id),
		Steps: []resource.TestStep{{
			Config: providerBlock(true) + customerHCL("SLOW", "slow"),
			Check: resource.ComposeAggregateTestCheckFunc(
				stateAttr(customerAddr, "id", &id),
				check(func() error {
					if n := m.Calls("POST", "/customers"); n != 3 {
						return fmt.Errorf("POST /customers sent %d times, want 3 (504, 429, replay)", n)
					}
					if _, second := m.Customer("2"); second {
						return fmt.Errorf("a second customer was created")
					}
					return nil
				}),
			),
		}},
	})
}

// Values the platform would refuse or rewrite are refused at plan time: a
// GitLab group longer than a tenant slug may be, an address in the
// "Name <address>" form, a customer id with a leading zero.
func TestAccCustomerResource_InputRules(t *testing.T) {
	newMock(t)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		Steps: []resource.TestStep{
			{
				Config:      providerBlock(false) + customerHCL("LONG", "a-group-name-of-thirty-one-char"),
				PlanOnly:    true,
				ExpectError: words("1-29 lower-case letters, digits or hyphens"),
			},
			{
				Config: providerBlock(false) + `
resource "ataila_customer" "test" {
  short_name            = "NAMED"
  long_name             = "Named Address Ltd"
  gitlab_group          = "named"
  primary_contact_email = "Ops Desk <ops@example.com>"
  primary_contact_name  = "Ops Desk"
}
`,
				PlanOnly:    true,
				ExpectError: words("must be an e-mail address"),
			},
			{
				Config: providerBlock(false) + `
data "ataila_customer" "zero" {
  id = "007"
}
`,
				PlanOnly:    true,
				ExpectError: words("must be a customer id"),
			},
		},
	})
}
