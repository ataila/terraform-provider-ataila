// Copyright (c) 2026 Macskásy Attila
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

const userAddr = "ataila_user.test"

// seededAdmin is the one active admin every mock starts with.
const seededAdmin = "00000000-0000-4000-8000-00000000ad01"

func userHCL(email string, extra ...string) string {
	return fmt.Sprintf(`
resource "ataila_user" "test" {
  email      = %q
  first_name = "Dana"
%s
}
`, email, indent(extra))
}

// deactivated checks, after the final destroy, that the person was
// deactivated (never deleted).
func deactivated(m *acctest.MockAPI, id *string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		p, ok := m.Person(*id)
		if !ok || p["is_active"] != false {
			return fmt.Errorf("user %s after destroy: %v", *id, p)
		}
		return nil
	}
}

func TestAccUserResource_Lifecycle(t *testing.T) {
	m := newMock(t)
	var id string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		CheckDestroy:             deactivated(m, &id),
		Steps: []resource.TestStep{
			{
				Config: providerBlock(false) + userHCL("dana.tester@example.com", `last_name  = "Tester"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					stateAttr(userAddr, "id", &id),
					resource.TestCheckResourceAttr(userAddr, "username", "dana.tester"),
					resource.TestCheckResourceAttr(userAddr, "ad_username", "dana.tester"),
					resource.TestCheckResourceAttr(userAddr, "name", "Dana Tester"),
					resource.TestCheckResourceAttr(userAddr, "locale", "hu"),
					resource.TestCheckResourceAttr(userAddr, "is_active", "true"),
					resource.TestCheckResourceAttr(userAddr, "kind", "human"),
					resource.TestCheckResourceAttr(userAddr, "roles.#", "1"),
					resource.TestCheckResourceAttr(userAddr, "roles.0", "user"),
					resource.TestCheckResourceAttr(userAddr, "keycloak_linked", "true"),
					resource.TestCheckResourceAttr(userAddr, "provisioning_status", "partial"),
					resource.TestCheckResourceAttr(userAddr, "warnings.#", "0"),
					resource.TestCheckResourceAttrSet(userAddr, "created_at"),
				),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPostRefresh: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				Config: providerBlock(false) + userHCL("dana.tester@example.com", `last_name  = "Tester-Smith"`, `locale     = "en"`),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(userAddr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrPtr(userAddr, "id", &id),
					mockField(m.Person, &id, "last_name", "Tester-Smith"),
					mockField(m.Person, &id, "locale", "en"),
					// A change later does not touch the handles derived at create.
					resource.TestCheckResourceAttr(userAddr, "username", "dana.tester"),
				),
			},
			{
				// Removing last_name clears it.
				Config: providerBlock(false) + userHCL("dana.tester@example.com", `locale     = "en"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr(userAddr, "last_name"),
					mockField(m.Person, &id, "last_name", nil),
					resource.TestCheckResourceAttr(userAddr, "name", "Dana"),
				),
			},
			{ResourceName: userAddr, ImportState: true, ImportStateVerify: true},
			{ResourceName: userAddr, ImportState: true, ImportStateId: "email:Dana.Tester@example.com", ImportStateVerify: true},
			{ResourceName: userAddr, ImportState: true, ImportStateId: "username:dana.tester", ImportStateVerify: true},
			{
				ResourceName: userAddr, ImportState: true, ImportStateId: "email:nobody@example.com",
				ExpectError: words("0 users have e-mail address nobody@example.com"),
			},
			{PreConfig: allowDestroyEverywhere(m), Config: providerBlock(true) + userHCL("dana.tester@example.com", `locale     = "en"`)},
		},
	})
}

// On a platform without SSO the SSO step is skipped: partial, with a
// keycloak_not_configured warning; deactivation answers 204.
func TestAccUserResource_NoKeycloak(t *testing.T) {
	m := newMock(t)
	m.SetKeycloak(false)
	factories, rec := recordingProvider()
	var id string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		PreCheck:                 allowDestroyEverywhere(m),
		CheckDestroy:             deactivated(m, &id),
		Steps: []resource.TestStep{{
			Config: providerBlock(true) + userHCL("dana.nosso@example.com"),
			Check: resource.ComposeAggregateTestCheckFunc(
				stateAttr(userAddr, "id", &id),
				resource.TestCheckResourceAttr(userAddr, "provisioning_status", "partial"),
				resource.TestCheckResourceAttr(userAddr, "warnings.0.code", "keycloak_not_configured"),
				rec.expectWarning("keycloak_not_configured"),
			),
		}},
	})
}

// The configured spelling of an address is kept although the platform
// stores it lower-cased; a create reports provisioning warnings as warnings.
func TestAccUserResource_EmailCaseAndWarnings(t *testing.T) {
	m := newMock(t)
	m.FailProvisioningStep("gitlab_user")
	factories, rec := recordingProvider()
	var id string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: factories,
		PreCheck:                 allowDestroyEverywhere(m),
		CheckDestroy:             deactivated(m, &id),
		Steps: []resource.TestStep{{
			Config: providerBlock(true) + userHCL("Dana.Mixed@Example.COM", `needs_git_access = true`),
			Check: resource.ComposeAggregateTestCheckFunc(
				stateAttr(userAddr, "id", &id),
				resource.TestCheckResourceAttr(userAddr, "email", "Dana.Mixed@Example.COM"),
				mockField(m.Person, &id, "email", "dana.mixed@example.com"),
				resource.TestCheckResourceAttr(userAddr, "provisioning_status", "error"),
				resource.TestCheckResourceAttr(userAddr, "warnings.0.code", "provisioning_gitlab_user_failed"),
				rec.expectWarning("ATAILA API warning: provisioning_gitlab_user_failed"),
			),
		}},
	})
}

// username and ad_username are set at create only: a change fails the plan
// and sends nothing.
func TestAccUserResource_FrozenHandles(t *testing.T) {
	m := newMock(t)
	var mutations int
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		PreCheck:                 allowDestroyEverywhere(m),
		Steps: []resource.TestStep{
			{
				Config: providerBlock(true) + userHCL("frozen@example.com", `username   = "dtest"`, `ad_username = "d.test"`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr(userAddr, "username", "dtest"),
					resource.TestCheckResourceAttr(userAddr, "ad_username", "d.test"),
					check(func() error { mutations = m.Mutations(); return nil }),
				),
			},
			{
				Config:      providerBlock(true) + userHCL("frozen@example.com", `username   = "dtest2"`, `ad_username = "d.test"`),
				PlanOnly:    true,
				ExpectError: words("Cannot change username of an existing user .* SSO and directory account names"),
			},
			{
				Config:      providerBlock(true) + userHCL("frozen@example.com", `username   = "dtest"`, `ad_username = "d.test2"`),
				PlanOnly:    true,
				ExpectError: words("Cannot change ad_username of an existing user"),
			},
			{
				// Leaving them out keeps them.
				PreConfig: func() {
					if got := m.Mutations(); got != mutations {
						t.Errorf("refused changes sent %d mutating requests", got-mutations)
					}
				},
				Config:   providerBlock(true) + userHCL("frozen@example.com"),
				PlanOnly: true,
			},
		},
	})
}

// Changes made in the portal show in the next plan; a person deactivated in
// the portal is re-activated by the configuration's is_active = true.
func TestAccUserResource_Drift(t *testing.T) {
	m := newMock(t)
	var id string
	cfg := providerBlock(false) + userHCL("drift@example.com", `last_name  = "Drift"`)
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		CheckDestroy:             deactivated(m, &id),
		Steps: []resource.TestStep{
			{Config: cfg, Check: stateAttr(userAddr, "id", &id)},
			{
				PreConfig: func() {
					m.SetPersonField(id, "last_name", "Changed")
					m.SetPersonField(id, "is_active", false)
				},
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction(userAddr, plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					mockField(m.Person, &id, "last_name", "Drift"),
					mockField(m.Person, &id, "is_active", true),
				),
			},
			{PreConfig: allowDestroyEverywhere(m), Config: providerBlock(true) + userHCL("drift@example.com", `last_name  = "Drift"`)},
		},
	})
}

// Deactivation (destroy, or is_active = false) needs both switches: refused
// at plan time by the provider's, then by the platform for the token's.
func TestAccUserResource_DeactivationGate(t *testing.T) {
	m := newMock(t)
	var id string
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		CheckDestroy:             deactivated(m, &id),
		Steps: []resource.TestStep{
			{Config: `provider "ataila" {}` + userHCL("gate@example.com"), Check: stateAttr(userAddr, "id", &id)},
			{
				Config:  `provider "ataila" {}` + userHCL("gate@example.com"),
				Destroy: true,
				ExpectError: words("Destroying a user is not allowed .* destroy means deactivate .* " +
					"tofu state rm ataila_user.<name>"),
			},
			{
				Config:      `provider "ataila" {}` + userHCL("gate@example.com", `is_active  = false`),
				PlanOnly:    true,
				ExpectError: words("Setting is_active = false deactivates the user"),
			},
			{
				// The provider allows it; the token was minted without allow_destroy.
				Config:      providerBlock(true) + userHCL("gate@example.com", `is_active  = false`),
				ExpectError: words("The token was not created with allow_destroy"),
			},
			{
				PreConfig: func() {
					if n := m.Calls("DELETE", "/users"); n != 0 {
						t.Errorf("%d DELETE requests although destroy was refused", n)
					}
					m.SetTokenAllowDestroy(true)
				},
				Config: providerBlock(true) + userHCL("gate@example.com", `is_active  = false`),
				Check:  mockField(m.Person, &id, "is_active", false),
			},
			{
				// And back: is_active = true re-activates.
				Config: providerBlock(true) + userHCL("gate@example.com"),
				Check:  mockField(m.Person, &id, "is_active", true),
			},
		},
	})
}

// The platform refuses to deactivate the last active admin; a deactivated
// person's address cannot be created again, and importing them is the way
// back; a service account cannot be imported.
func TestAccUserResource_PlatformRefusals(t *testing.T) {
	m := newMock(t)
	former := m.AddPerson("former@example.com", "Former", "Person")
	m.SetPersonField(former, "is_active", false)
	m.AddServiceAccount("robot")
	adminHCL := `
resource "ataila_user" "admin" {
  email      = "platform.admin@example.com"
  first_name = "Platform"
  last_name  = "Admin"
  locale     = "en"
}
`
	formerHCL := func(extra string) string {
		return `
resource "ataila_user" "former" {
  email      = "former@example.com"
  first_name = "Former"
  last_name  = "Person"
` + extra + `
}
`
	}
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: protoV6,
		PreCheck:                 allowDestroyEverywhere(m),
		CheckDestroy: func(*terraform.State) error {
			if p, _ := m.Person(former); p["is_active"] != false {
				return fmt.Errorf("former person after destroy: %v", p)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				// Creating a deactivated person's address again.
				Config:      providerBlock(true) + formerHCL(""),
				ExpectError: words("A user with this e-mail address exists .* import them"),
			},
			{
				Config: providerBlock(true) + formerHCL(""), ResourceName: "ataila_user.former",
				ImportState: true, ImportStateId: "email:former@example.com", ImportStatePersist: true,
			},
			{
				// is_active defaults to true: the import is re-activated.
				Config: providerBlock(true) + formerHCL(""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("ataila_user.former", plancheck.ResourceActionUpdate)},
				},
				Check: mockField(m.Person, &[]string{former}[0], "is_active", true),
			},
			{
				Config: providerBlock(true) + formerHCL("") + adminHCL, ResourceName: "ataila_user.admin",
				ImportState: true, ImportStateId: seededAdmin, ImportStatePersist: true,
			},
			{
				Config:      providerBlock(true) + formerHCL(""),
				ExpectError: words("The platform refused to deactivate the user (last_active_admin) .* keeps at least one active admin"),
			},
			{
				// Another admin first: then the deactivation goes through.
				PreConfig: func() { m.SetPersonRole(former, "admin", true) },
				Config:    providerBlock(true) + formerHCL(""),
				Check: func(*terraform.State) error {
					if p, _ := m.Person(seededAdmin); p["is_active"] != false {
						return fmt.Errorf("admin after destroy: %v", p)
					}
					return nil
				},
			},
			{
				Config: providerBlock(true) + formerHCL("") + `
resource "ataila_user" "robot" {
  email      = "robot@service-account.invalid"
  first_name = "robot"
}
`,
				ResourceName: "ataila_user.robot", ImportState: true, ImportStateId: "email:robot@service-account.invalid",
				ExpectError: words("Cannot import a service account"),
			},
			{
				// Clean-up: another active admin, so that the final destroy may deactivate this one.
				PreConfig: func() { m.SetPersonRole(m.AddPerson("keeper@example.com", "Keeper", ""), "admin", true) },
				Config:    providerBlock(true) + formerHCL(""),
			},
		},
	})
}
