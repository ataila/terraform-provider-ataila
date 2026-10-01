// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"strings"
	"testing"

	"github.com/ataila/terraform-provider-ataila/internal/acctest"
)

func TestUserCalls(t *testing.T) {
	m, api := mockAPI(t, 2)
	ctx := context.Background()
	m.FailProvisioningStep("gitlab_user")
	git := true
	u, err := api.CreateUser(ctx, UserCreate{Email: "Nora.Uj@Example.COM", FirstName: "Nóra", LastName: strPtr("Új"), NeedsGitAccess: &git})
	if err != nil {
		t.Fatal(err)
	}
	if *u.Email != "nora.uj@example.com" || *u.Username != "nora.uj" || *u.AdUsername != "nora.uj" ||
		u.Roles[0] != "user" || !u.SsoLinked || string(*u.ProvisioningStatus) != "error" {
		t.Errorf("created %+v", u)
	}
	if u.Warnings == nil || (*u.Warnings)[0].Code != "provisioning_gitlab_user_failed" {
		t.Errorf("warnings %+v", u.Warnings)
	}

	// Lists page to the end; filters apply; people only by default.
	m.AddServiceAccount("robot")
	people, err := api.ListUsers(ctx, UserFilter{})
	if err != nil {
		t.Fatal(err)
	}
	all, _ := api.ListUsers(ctx, UserFilter{Kind: "all"})
	if len(all) != len(people)+2 { // the service account and the token's own principal
		t.Errorf("people %d, all %d", len(people), len(all))
	}
	byMail, err := api.ListUsers(ctx, UserFilter{Email: "NORA.UJ@example.com"})
	if err != nil || len(byMail) != 1 || byMail[0].Id != u.Id {
		t.Errorf("by e-mail: %+v %v", byMail, err)
	}

	// A frozen username once the SSO account exists; last_name null clears.
	_, err = api.UpdateUser(ctx, u.Id, Patch{"username": "other"})
	if e := apiErr(t, err); e.StatusCode != 422 || e.Code() != CodeImmutableField {
		t.Errorf("username after SSO link: %v", err)
	}
	upd, err := api.UpdateUser(ctx, u.Id, Patch{"last_name": nil, "locale": "en"})
	if err != nil || upd.LastName != nil || upd.Locale != "en" || upd.Name != "Nóra" {
		t.Errorf("clear last_name: %+v %v", upd, err)
	}

	// Deactivation: the token's destroy flag, then the platform's refusals.
	err = api.DeactivateUser(ctx, u.Id)
	if e := apiErr(t, err); e.StatusCode != 403 || e.Code() != CodeDestroyNotAllowed {
		t.Errorf("deactivate without the flag: %v", err)
	}
	_, err = api.UpdateUser(ctx, u.Id, Patch{"is_active": false})
	if e := apiErr(t, err); e.Code() != CodeDestroyNotAllowed {
		t.Errorf("PATCH is_active false without the flag: %v", err)
	}
	m.SetTokenAllowDestroy(true)
	if err := api.DeactivateUser(ctx, u.Id); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if gone, err := api.GetUser(ctx, u.Id); err != nil || gone.IsActive {
		t.Fatalf("after deactivate: %+v %v", gone, err)
	}
	_, err = api.CreateUser(ctx, UserCreate{Email: "nora.uj@example.com", FirstName: "Nora"})
	if e := apiErr(t, err); e.StatusCode != 409 || e.Code() != "email_taken" {
		t.Errorf("re-create a deactivated address: %v", err)
	}
	err = api.DeactivateUser(ctx, "00000000-0000-4000-8000-00000000ad01")
	if e := apiErr(t, err); e.StatusCode != 409 || e.Code() != "last_active_admin" {
		t.Errorf("last admin: %v", err)
	}
}

func TestRoleGrantCalls(t *testing.T) {
	m, api := mockAPI(t, 0)
	ctx := context.Background()
	id := m.AddPerson("grant.me@example.com", "Grant", "Me")

	g, granted, err := api.GrantRole(ctx, id, "users-read-global")
	if err != nil || !granted || g.GrantedAt == nil {
		t.Fatalf("grant: %+v %v %v", g, granted, err)
	}
	if _, granted, err = api.GrantRole(ctx, id, "users-read-global"); err != nil || granted {
		t.Errorf("second grant: %v %v", granted, err)
	}
	for role, code := range map[string]string{
		"admin":                  "role_not_manageable_by_token",
		"api-tokens-read-global": "role_not_held_by_token",
		"users-read-tenant":      "role_not_grantable",
		"no-such-role":           "unknown_role",
		"Bad Role":               "unknown_role",
	} {
		_, _, err := api.GrantRole(ctx, id, role)
		if e := apiErr(t, err); e.Code() != code {
			t.Errorf("grant %s: %v, want %s", role, err, code)
		}
	}
	_, _, err = api.GrantRole(ctx, "00000000-0000-4000-8000-000000000001", "users-read-global")
	if e := apiErr(t, err); e.Code() != "service_account_managed_elsewhere" {
		t.Errorf("grant to the token's own (service) account: %v", err)
	}
	if err := api.RevokeRole(ctx, id, "users-read-global"); err != nil {
		t.Fatal(err)
	}
	if _, err := api.GetRoleGrant(ctx, id, "users-read-global"); apiErr(t, err).Code() != "role_grant_not_found" {
		t.Errorf("revoked grant still reads: %v", err)
	}
	if e := apiErr(t, api.RevokeRole(ctx, id, "user")); e.Code() != "last_role" {
		t.Errorf("last role: %v", e)
	}

	perms, err := api.ListPermissions(ctx, "users")
	if err != nil || len(perms) != 4 {
		t.Fatalf("users permissions: %d %v", len(perms), err)
	}
	for _, p := range perms {
		if p.Grantable != (p.Scope == "global") || p.Mintable != p.Grantable {
			t.Errorf("%s grantable %v mintable %v", p.Key, p.Grantable, p.Mintable)
		}
	}
}

func newKey(org string, extra map[string]any) map[string]any {
	body := map[string]any{"organization_id": org, "env": "prod", "app": "chatbot", "models": []string{"general"}}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

func TestGatewayKeyCalls(t *testing.T) {
	m, api := mockAPI(t, 0)
	ctx := context.Background()
	c, err := api.CreateCustomer(ctx, newCustomer("GATE", "gate"))
	if err != nil {
		t.Fatal(err)
	}
	org := c.PrimaryTenantId

	k, err := api.CreateGatewayKey(ctx, newKey(org, map[string]any{
		"expose_secret": true, "soft_budget_usd": 0.1, "rpm_limit": 60, "models": []string{"general", "code-max"}}))
	if err != nil {
		t.Fatal(err)
	}
	if k.KeyAlias != "gate-prod-chatbot" || k.Secret == nil || *k.Secret != m.SecretValue(k.KeyAlias) ||
		*k.SoftBudgetUsd != 0.1 || k.Live != "not_read" || k.SpendUsd != nil {
		t.Errorf("created %+v", k)
	}
	if len(k.Warnings) != 1 || k.Warnings[0].Code != "tier_not_serving" {
		t.Errorf("warnings %+v", k.Warnings)
	}
	got, err := api.GetGatewayKey(ctx, k.Id)
	if err != nil || got.Secret != nil || got.SpendUsd == nil {
		t.Errorf("read: %+v %v", got, err)
	}
	m.LoseGatewayKey(k.KeyAlias)
	lost, err := api.GetGatewayKey(ctx, k.Id)
	if err != nil || lost.Live != "missing" || lost.Warnings[0].Code != CodeKeyMissingOnGateway {
		t.Errorf("lost key: %+v %v", lost, err)
	}
	_, err = api.RotateGatewayKey(ctx, k.Id, true)
	if e := apiErr(t, err); e.Code() != CodeKeyMissingOnGateway {
		t.Errorf("rotating a lost key: %v", err)
	}
	if err := api.DeleteGatewayKey(ctx, k.Id); err != nil {
		t.Fatalf("deleting a lost key: %v", err)
	}

	k2, err := api.CreateGatewayKey(ctx, newKey(org, map[string]any{"feature": "summaries"}))
	if err != nil || k2.Secret != nil {
		t.Fatalf("create without expose: %+v %v", k2, err)
	}
	before := m.SecretValue(k2.KeyAlias)
	rot, err := api.RotateGatewayKey(ctx, k2.Id, true)
	if err != nil || rot.Secret == nil || *rot.Secret == before || rot.RotatedAt == nil {
		t.Errorf("rotate: %+v %v", rot, err)
	}
	found, err := api.ListGatewayKeys(ctx, GatewayKeyFilter{KeyAlias: "gate-prod-chatbot-summaries"})
	if err != nil || len(found) != 1 || found[0].Id != k2.Id || found[0].Live != "not_read" {
		t.Errorf("list by alias: %+v %v", found, err)
	}
	_, err = api.UpdateGatewayKey(ctx, k2.Id, Patch{"app": "other"})
	if e := apiErr(t, err); e.Code() != CodeImmutableField {
		t.Errorf("frozen app: %v", err)
	}

	adopted := m.AddAdoptedKey(org, "gate-prod-legacy", "general")
	_, err = api.RotateGatewayKey(ctx, adopted, false)
	if e := apiErr(t, err); e.StatusCode != 409 || e.Code() != CodeKeyAdopted {
		t.Errorf("rotating an adopted key: %v", err)
	}
	_, err = api.CreateGatewayKey(ctx, newKey(org, map[string]any{"models": []string{"nope"}}))
	if e := apiErr(t, err); e.Code() != "unknown_tier" || !strings.Contains(e.Detail(), "tiers: code, code-max, general") {
		t.Errorf("unknown tier: %v\n%s", err, e.Detail())
	}
}

// A retried create whose first answer was lost is replayed without the value.
func TestGatewayKeyReplayNeverCarriesTheSecret(t *testing.T) {
	m, api := mockAPI(t, 0)
	ctx := context.Background()
	c, _ := api.CreateCustomer(ctx, newCustomer("REPLAY", "replay"))
	m.InjectFaults("/ai/gateway/keys", acctest.Fault{Status: 502, Method: "POST", AfterHandling: true, RetryAfter: "0"})
	k, err := api.CreateGatewayKey(ctx, newKey(c.PrimaryTenantId, map[string]any{"expose_secret": true}))
	if err != nil {
		t.Fatal(err)
	}
	if k.Secret != nil || len(k.Warnings) == 0 || k.Warnings[len(k.Warnings)-1].Code != "secret_not_replayed" {
		t.Errorf("replayed create: secret %v warnings %+v", k.Secret, k.Warnings)
	}
	if m.SecretValue(k.KeyAlias) == "" {
		t.Error("the value must be in the secrets store")
	}
}

// A platform without a gateway: final 503 naming the code, never retried;
// the registry reads keep working.
func TestNoGatewayIsFinal(t *testing.T) {
	for _, state := range []string{"not_configured", "unreachable"} {
		m, api := mockAPI(t, 0)
		ctx := context.Background()
		m.SetGateway(state)
		_, err := api.GetGateway(ctx)
		e := apiErr(t, err)
		if e.StatusCode != 503 || e.Code() != "gateway_"+state || !e.IsFinalUnavailable() || e.Attempts != 1 {
			t.Errorf("%s: %v (attempts %d)", state, err, e.Attempts)
		}
		if n := m.Calls("GET", "/ai/gateway"); n != 1 {
			t.Errorf("%s: GET /ai/gateway sent %d times", state, n)
		}
		if _, err := api.ListGatewayKeys(ctx, GatewayKeyFilter{}); err != nil {
			t.Errorf("%s: the key list must work without a gateway: %v", state, err)
		}
		if tiers, err := api.ListServingTiers(ctx); err != nil || len(tiers) != 3 {
			t.Errorf("%s: tiers: %d %v", state, len(tiers), err)
		}
	}
}

func TestServingTierCalls(t *testing.T) {
	_, api := mockAPI(t, 1)
	ctx := context.Background()
	tiers, err := api.ListServingTiers(ctx)
	if err != nil || len(tiers) != 3 || tiers[0].Key != "code" {
		t.Fatalf("tiers: %+v %v", tiers, err)
	}
	pin := "model-general"
	got, err := api.PutServingTier(ctx, "code", &pin, true, false)
	if err != nil || got.Source != "pin" || *got.ResolvedModel != pin {
		t.Errorf("pin: %+v %v", got, err)
	}
	off := "model-offline"
	_, err = api.PutServingTier(ctx, "code", &off, true, false)
	if e := apiErr(t, err); e.Code() != CodeModelNotLoaded || !strings.Contains(e.Detail(), "candidates:") {
		t.Errorf("unloaded pin: %v\n%s", err, e.Detail())
	}
	got, err = api.PutServingTier(ctx, "code", &off, true, true)
	if err != nil || got.Source != "pin-offline" || got.Warnings == nil || (*got.Warnings)[0].Code != CodeModelNotLoaded {
		t.Errorf("unloaded pin allowed: %+v %v", got, err)
	}
	got, err = api.PutServingTier(ctx, "code", nil, true, false)
	if err != nil || got.PinnedModel != nil || got.Source != "auto" {
		t.Errorf("back to auto: %+v %v", got, err)
	}
	_, err = api.PutServingTier(ctx, "nope", nil, true, false)
	if e := apiErr(t, err); !e.IsNotFound() || e.Code() != "tier_not_found" {
		t.Errorf("no such tier: %v", err)
	}
}

func strPtr(s string) *string { return &s }
