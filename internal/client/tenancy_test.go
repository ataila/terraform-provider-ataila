// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ataila/terraform-provider-ataila/internal/acctest"
)

func mockAPI(t *testing.T, pageSize int) (*acctest.MockAPI, *API) {
	t.Helper()
	m := acctest.NewMockAPI(t)
	api, err := New(Config{
		Endpoint: m.URL(), Token: acctest.MockToken, CACertPEM: []byte(m.CACertPEM()),
		PageSize: pageSize, BackoffBase: 1, BackoffMax: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m, api
}

func newCustomer(short, group string) CustomerCreate {
	return CustomerCreate{
		ShortName: short, LongName: "Example " + short, GitlabGroup: group,
		PrimaryContactEmail: "ops@example.com", PrimaryContactName: "Ops Desk",
	}
}

func apiErr(t *testing.T, err error) *APIError {
	t.Helper()
	var e *APIError
	if !errors.As(err, &e) {
		t.Fatalf("want *APIError, got %v", err)
	}
	return e
}

func TestCustomerCalls(t *testing.T) {
	m, api := mockAPI(t, 0)
	ctx := context.Background()
	m.SetGitLabOutcome("partial", "group created, owner not added")

	c, err := api.CreateCustomer(ctx, newCustomer("EXAMPLE", "example"))
	if err != nil {
		t.Fatal(err)
	}
	if c.CustomerIndex != 2 || c.Status != CustomerStatusActive || c.Edition != CustomerEditionSp || c.PrimaryTenantId == "" {
		t.Errorf("created %+v", c)
	}
	if c.Warnings == nil || len(*c.Warnings) != 1 || (*c.Warnings)[0].Code != "gitlab_group_not_ready" {
		t.Errorf("warnings = %+v", c.Warnings)
	}

	got, err := api.GetCustomer(ctx, c.Id)
	if err != nil || got.ShortName != "EXAMPLE" {
		t.Fatalf("get: %+v %v", got, err)
	}
	list, err := api.ListCustomers(ctx, CustomerFilter{ShortName: "EXAMPLE"})
	if err != nil || len(list) != 1 || list[0].Id != c.Id {
		t.Fatalf("list by short_name: %+v %v", list, err)
	}
	if list, _ := api.ListCustomers(ctx, CustomerFilter{GitlabGroup: "nope"}); len(list) != 0 {
		t.Errorf("unknown group listed %+v", list)
	}

	// A PATCH changes only what it names; nil sends null, which clears notes.
	upd, err := api.UpdateCustomer(ctx, c.Id, Patch{"notes": "first", "status": "suspended"})
	if err != nil || upd.Notes == nil || *upd.Notes != "first" || upd.Status != CustomerStatusSuspended {
		t.Fatalf("update: %+v %v", upd, err)
	}
	upd, err = api.UpdateCustomer(ctx, c.Id, Patch{"notes": nil})
	if err != nil || upd.Notes != nil || upd.Status != CustomerStatusSuspended {
		t.Fatalf("clearing notes: %+v %v", upd, err)
	}

	// A frozen key is refused, and the refusal names the field.
	_, err = api.UpdateCustomer(ctx, c.Id, Patch{"short_name": "OTHER"})
	e := apiErr(t, err)
	if e.StatusCode != 422 || e.Code() != CodeImmutableField || !strings.Contains(e.Detail(), "field: short_name") {
		t.Errorf("frozen key: %v\n%s", err, e.Detail())
	}
	// The current value of a frozen key is accepted.
	if _, err := api.UpdateCustomer(ctx, c.Id, Patch{"short_name": "EXAMPLE", "customer_index": 2}); err != nil {
		t.Errorf("sending the current frozen values: %v", err)
	}

	// Archive without the token's destroy flag, then with it.
	e = apiErr(t, api.ArchiveCustomer(ctx, c.Id))
	if e.StatusCode != 403 || e.Code() != CodeDestroyNotAllowed {
		t.Errorf("archive without the token flag: %v", e)
	}
	m.SetTokenAllowDestroy(true)
	if err := api.ArchiveCustomer(ctx, c.Id); err != nil {
		t.Fatal(err)
	}
	if err := api.ArchiveCustomer(ctx, c.Id); err != nil {
		t.Errorf("archiving twice must succeed: %v", err)
	}
	_, err = api.UpdateCustomer(ctx, c.Id, Patch{"long_name": "Renamed"})
	if e := apiErr(t, err); e.StatusCode != 409 || e.Code() != CodeCustomerArchived {
		t.Errorf("changing an archived customer: %v", err)
	}
	// Archived customers keep their keys.
	_, err = api.CreateCustomer(ctx, newCustomer("EXAMPLE", "sample"))
	if e := apiErr(t, err); e.StatusCode != 409 || e.Code() != "short_name_taken" {
		t.Errorf("re-using an archived short name: %v", err)
	}
	_, err = api.GetCustomer(ctx, "not-a-number")
	if e := apiErr(t, err); !e.IsNotFound() || e.Code() != "customer_not_found" {
		t.Errorf("bad id: %v", err)
	}
}

func TestCustomerArchiveRefusedWithProjects(t *testing.T) {
	m, api := mockAPI(t, 0)
	ctx := context.Background()
	m.SetTokenAllowDestroy(true)
	c, err := api.CreateCustomer(ctx, newCustomer("BUSY", "busy"))
	if err != nil {
		t.Fatal(err)
	}
	m.SetTenantBlockers(c.PrimaryTenantId, 3, 0, 0, 0)
	e := apiErr(t, api.ArchiveCustomer(ctx, c.Id))
	if e.StatusCode != 409 || e.Code() != "customer_has_projects" || e.Blockers()["projects"] != 3 ||
		!strings.Contains(e.Detail(), "blockers: projects=3") {
		t.Errorf("%v\n%s", e, e.Detail())
	}
}

// Lists are read to the end, one page at a time.
func TestListsPageThroughEverything(t *testing.T) {
	m, api := mockAPI(t, 2)
	ctx := context.Background()
	c, err := api.CreateCustomer(ctx, newCustomer("PAGED", "paged"))
	if err != nil {
		t.Fatal(err)
	}
	m.SeedTenants(c.Id, "paged-t", 6)
	other, err := api.CreateCustomer(ctx, newCustomer("ELSE", "else"))
	if err != nil {
		t.Fatal(err)
	}

	before := m.Calls("GET", "/tenants")
	all, err := api.ListTenants(ctx, TenantFilter{CustomerID: c.Id})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 7 {
		t.Fatalf("got %d tenants, want 7 (primary + 6)", len(all))
	}
	if pages := m.Calls("GET", "/tenants") - before; pages != 4 {
		t.Errorf("read %d pages, want 4 of 2", pages)
	}
	seen := map[string]bool{}
	for i, tn := range all {
		if seen[tn.Id] {
			t.Errorf("tenant %s listed twice", tn.Slug)
		}
		seen[tn.Id] = true
		if i > 0 && all[i-1].Slug >= tn.Slug {
			t.Errorf("not in slug order: %s before %s", all[i-1].Slug, tn.Slug)
		}
		if tn.CustomerId == nil || *tn.CustomerId != c.Id {
			t.Errorf("tenant %s of another customer", tn.Slug)
		}
	}
	bySlug, err := api.ListTenants(ctx, TenantFilter{Slug: "else"})
	if err != nil || len(bySlug) != 1 || bySlug[0].Id != other.PrimaryTenantId || !bySlug[0].IsPrimary {
		t.Errorf("by slug: %+v %v", bySlug, err)
	}

	customers, err := api.ListCustomers(ctx, CustomerFilter{})
	if err != nil || len(customers) != 2 {
		t.Errorf("customers: %d %v", len(customers), err)
	}
}

func TestTenantAndMembershipCalls(t *testing.T) {
	m, api := mockAPI(t, 0)
	ctx := context.Background()
	c, err := api.CreateCustomer(ctx, newCustomer("TEN", "ten"))
	if err != nil {
		t.Fatal(err)
	}
	desc := "Build farm"
	tn, err := api.CreateTenant(ctx, TenantCreate{CustomerId: c.Id, Name: "Builds", Slug: "ten-builds", Description: &desc})
	if err != nil {
		t.Fatal(err)
	}
	if tn.IsPrimary || tn.MemberCount != 0 || tn.Description == nil || *tn.Description != desc {
		t.Errorf("created %+v", tn)
	}
	_, err = api.CreateTenant(ctx, TenantCreate{CustomerId: c.Id, Name: "Again", Slug: "ten-builds"})
	if e := apiErr(t, err); e.Code() != "tenant_slug_taken" {
		t.Errorf("duplicate slug: %v", err)
	}

	upd, err := api.UpdateTenant(ctx, tn.Id, Patch{"description": nil, "default_router_id": "2"})
	if err != nil || upd.Description != nil || upd.DefaultRouterId == nil || *upd.DefaultRouterId != "2" {
		t.Fatalf("update: %+v %v", upd, err)
	}
	_, err = api.UpdateTenant(ctx, tn.Id, Patch{"slug": "renamed"})
	if e := apiErr(t, err); e.StatusCode != 422 || e.Code() != CodeImmutableField {
		t.Errorf("frozen slug: %v", err)
	}

	user := m.AddUser("dev@example.com")
	mm, created, err := api.PutMembership(ctx, tn.Id, user, "viewer")
	if err != nil || !created || mm.Role != MembershipRoleViewer {
		t.Fatalf("first put: %+v %v %v", mm, created, err)
	}
	mm, created, err = api.PutMembership(ctx, tn.Id, user, "admin")
	if err != nil || created || mm.Role != MembershipRoleAdmin {
		t.Fatalf("second put: %+v %v %v", mm, created, err)
	}
	if got, err := api.GetMembership(ctx, tn.Id, user); err != nil || got.Role != MembershipRoleAdmin {
		t.Errorf("get membership: %+v %v", got, err)
	}
	_, _, err = api.PutMembership(ctx, tn.Id, "00000000-0000-4000-8000-00000000dead", "member")
	if e := apiErr(t, err); !e.IsNotFound() || e.Code() != "user_not_found" {
		t.Errorf("unknown user: %v", err)
	}

	m.SetTokenAllowDestroy(true)
	e := apiErr(t, api.DeleteTenant(ctx, c.PrimaryTenantId))
	if e.StatusCode != 409 || e.Code() != "tenant_is_primary" {
		t.Errorf("deleting the primary tenant: %v", e)
	}
	m.SetTenantBlockers(tn.Id, 0, 1, 2, 0)
	e = apiErr(t, api.DeleteTenant(ctx, tn.Id))
	if e.Code() != "tenant_has_contracts" || e.Blockers()["contracts"] != 1 || e.Blockers()["helpdesk_records"] != 2 {
		t.Errorf("blocked delete: %v %v", e, e.Blockers())
	}
	m.SetTenantBlockers(tn.Id, 0, 0, 0, 0)
	if err := api.DeleteTenant(ctx, tn.Id); err != nil {
		t.Fatal(err)
	}
	if _, err := api.GetMembership(ctx, tn.Id, user); !apiErr(t, err).IsNotFound() {
		t.Errorf("memberships must go with their tenant: %v", err)
	}
	if err := api.DeleteMembership(ctx, c.PrimaryTenantId, user); apiErr(t, err).Code() != "membership_not_found" {
		t.Errorf("deleting a missing membership: %v", err)
	}
}

// A create whose answer was lost is retried with the same Idempotency-Key and
// the API replays the first answer: exactly one customer exists.
func TestCreateReplayedAfterALostAnswer(t *testing.T) {
	m, api := mockAPI(t, 0)
	ctx := context.Background()
	m.InjectFaults("/customers", acctest.Fault{Status: 502, Code: "bad_gateway", Method: "POST", AfterHandling: true, RetryAfter: "0"})

	c, err := api.CreateCustomer(ctx, newCustomer("ONCE", "once"))
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, r := range m.Requests() {
		if r.Method == "POST" && r.Path == "/customers" {
			keys = append(keys, r.Header.Get(IdempotencyHeader))
		}
	}
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
		t.Fatalf("POST /customers keys = %q, want two identical", keys)
	}
	all, err := api.ListCustomers(ctx, CustomerFilter{})
	if err != nil || len(all) != 1 || all[0].Id != c.Id {
		t.Errorf("customers after a replayed create: %+v %v", all, err)
	}
}

// A retry that arrives while the first attempt is still running is answered
// 429 idempotency_request_in_progress with Retry-After, and retried like any
// 429 until the first attempt's answer is replayed: one customer.
func TestCreateRetriedWhileTheFirstAttemptRuns(t *testing.T) {
	m, api := mockAPI(t, 0)
	ctx := context.Background()
	m.InjectFaults("/customers", acctest.Fault{Status: 504, Code: "gateway_timeout", Method: "POST", StillRunning: 1})

	start := time.Now()
	c, err := api.CreateCustomer(ctx, newCustomer("SLOW", "slow"))
	if err != nil {
		t.Fatal(err)
	}
	if waited := time.Since(start); waited < 2*time.Second {
		t.Errorf("the 429's Retry-After (2s) was not honoured: %v", waited)
	}
	var keys []string
	for _, r := range m.Requests() {
		if r.Method == "POST" && r.Path == "/customers" {
			keys = append(keys, r.Header.Get(IdempotencyHeader))
		}
	}
	if len(keys) != 3 || keys[0] != keys[1] || keys[1] != keys[2] {
		t.Errorf("POST /customers keys = %q, want three identical (504, 429, replay)", keys)
	}
	if all, _ := api.ListCustomers(ctx, CustomerFilter{}); len(all) != 1 || all[0].Id != c.Id {
		t.Errorf("customers: %+v", all)
	}
}

// What the contract says a client sees: status on create, RFC 3339 times,
// updated_at equal to created_at on a new tenant, the normalised e-mail, the
// one integer-id rule, merge-patch bodies, a legacy tenant without customer.
func TestContractDetails(t *testing.T) {
	m, api := mockAPI(t, 0)
	ctx := context.Background()
	body := newCustomer("DETAIL", "detail")
	suspended := CustomerCreateStatusSuspended
	body.Status = &suspended
	body.PrimaryContactEmail = "Pat@Example.COM"
	c, err := api.CreateCustomer(ctx, body)
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != CustomerStatusSuspended || c.PrimaryContactEmail != "Pat@example.com" {
		t.Errorf("created: status %s, e-mail %s", c.Status, c.PrimaryContactEmail)
	}
	if c.CreatedAt.IsZero() || c.CreatedAt.Location() != time.UTC {
		t.Errorf("created_at %v", c.CreatedAt)
	}
	if n := m.Calls("PATCH", "/customers"); n != 0 {
		t.Errorf("a suspended create took %d PATCH calls", n)
	}

	tn, err := api.CreateTenant(ctx, TenantCreate{CustomerId: c.Id, Name: "Lab", Slug: "detail-lab"})
	if err != nil {
		t.Fatal(err)
	}
	if !tn.UpdatedAt.Equal(tn.CreatedAt) {
		t.Errorf("new tenant: updated_at %v, created_at %v", tn.UpdatedAt, tn.CreatedAt)
	}
	if _, err := api.UpdateTenant(ctx, tn.Id, Patch{"name": "Laboratory"}); err != nil {
		t.Fatal(err)
	}
	for _, r := range m.Requests() {
		if r.Method == "PATCH" && r.Header.Get("Content-Type") != MergePatchContentType {
			t.Errorf("PATCH %s sent as %q", r.Path, r.Header.Get("Content-Type"))
		}
	}

	for _, bad := range []string{"007", "+1", "1234567890", "0"} {
		if _, err := api.GetCustomer(ctx, bad); !apiErr(t, err).IsNotFound() {
			t.Errorf("GET /customers/%s: %v, want 404", bad, err)
		}
	}

	legacy := m.AddLegacyTenant("old-shop", "Old Shop")
	got, err := api.GetTenant(ctx, legacy)
	if err != nil || got.CustomerId != nil {
		t.Errorf("legacy tenant: %+v %v", got, err)
	}
}
