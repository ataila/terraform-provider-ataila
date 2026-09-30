// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package client

import (
	"context"
	"encoding/json"
	"testing"
)

func TestProjectCalls(t *testing.T) {
	m, api := mockAPI(t, 1)
	ctx := context.Background()
	_, tenant := m.AddCustomer("EXAMPLE", "example")

	p, err := api.CreateProject(ctx, map[string]any{"tenant_id": tenant, "short_name": "shop",
		"gitlab_repo_slug": "shop-app", "primary_domain": "Shop.Example.com", "long_name": "Example Shop"})
	if err != nil {
		t.Fatal(err)
	}
	id := p.String("id")
	if p.String("status") != "planned" || p["project_index"] != float64(4) || p.String("primary_domain") != "shop.example.com" {
		t.Errorf("created %v", p)
	}
	if _, err := api.CreateProject(ctx, map[string]any{"tenant_id": tenant, "short_name": "blog",
		"gitlab_repo_slug": "blog", "primary_domain": "blog.example.com", "long_name": "Example Blog"}); err != nil {
		t.Fatal(err)
	}
	// Every page is read (page size 1).
	all, err := api.ListProjects(ctx, ProjectFilter{TenantID: tenant})
	if err != nil || len(all) != 2 {
		t.Fatalf("list: %d %v", len(all), err)
	}
	one, err := api.ListProjects(ctx, ProjectFilter{ShortName: "blog"})
	if err != nil || len(one) != 1 || one[0].String("short_name") != "blog" {
		t.Fatalf("filtered: %v %v", one, err)
	}

	// Start, and a second start adopts the running operation.
	op, running, err := api.StartProvisioning(ctx, id)
	if err != nil || op == nil || running != "" || op.Id != "provision:1" {
		t.Fatalf("start: %+v %q %v", op, running, err)
	}
	op2, running, err := api.StartProvisioning(ctx, id)
	if err != nil || op2 != nil || running != "provision:1" {
		t.Fatalf("second start: %+v %q %v", op2, running, err)
	}
	for i := 0; i < 20; i++ {
		if op, err = api.Operation(ctx, "provision:1"); err != nil {
			t.Fatal(err)
		}
	}
	if op.Status != OperationStatusSucceeded {
		t.Fatalf("operation %+v", op)
	}
	prov, err := api.GetProvisioning(ctx, id)
	if err != nil || !prov.Converged || !prov.Provisioned || prov.DispatchMode != ProvisioningDispatchModeLive {
		t.Fatalf("provisioning %+v %v", prov, err)
	}

	// A change of a done stage's input marks it stale.
	upd, err := api.UpdateProject(ctx, id, Patch{"long_name": "Example Shop 2", "description": nil})
	if err != nil || len(upd.Strings("stale_stages")) != 1 || upd.Strings("stale_stages")[0] != "gitlab:populate-repo" {
		t.Fatalf("update: %v %v", upd["stale_stages"], err)
	}
	grid, err := api.GetStages(ctx, id)
	if err != nil || len(grid.Stages) != 15 {
		t.Fatalf("stages: %v", err)
	}

	// Retired: changes and starts are refused, a second retire is a 204.
	m.SetTokenAllowDestroy(true)
	if err := api.RetireProject(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := api.RetireProject(ctx, id); err != nil {
		t.Fatal(err)
	}
	_, err = api.UpdateProject(ctx, id, Patch{"long_name": "x2"})
	if e := apiErr(t, err); e.Code() != CodeProjectRetired {
		t.Errorf("update of a retired project: %v", err)
	}
	_, _, err = api.StartProvisioning(ctx, id)
	if e := apiErr(t, err); e.Code() != CodeProjectRetired {
		t.Errorf("start on a retired project: %v", err)
	}
}

// A member's gitlab_role is sent as null when unset, and the body is exactly
// role and gitlab_role.
func TestProjectMemberBody(t *testing.T) {
	m, api := mockAPI(t, 0)
	ctx := context.Background()
	_, tenant := m.AddCustomer("EXAMPLE", "example")
	p, err := api.CreateProject(ctx, map[string]any{"tenant_id": tenant, "short_name": "shop",
		"gitlab_repo_slug": "shop-app", "primary_domain": "shop.example.com", "long_name": "Example Shop"})
	if err != nil {
		t.Fatal(err)
	}
	user := m.AddUser("dev@example.com")
	m.SetMembership(tenant, user, "member")
	pm, created, err := api.PutProjectMember(ctx, p.String("id"), user, "viewer", nil)
	if err != nil || !created || pm.Role != "viewer" || pm.GitlabRole != nil {
		t.Fatalf("put: %+v %v %v", pm, created, err)
	}
	reqs := m.Requests()
	var body map[string]any
	if err := json.Unmarshal(reqs[len(reqs)-1].Body, &body); err != nil {
		t.Fatal(err)
	}
	if v, present := body["gitlab_role"]; !present || v != nil || len(body) != 2 {
		t.Errorf("body %v", body)
	}
	role := "reporter"
	if _, created, err = api.PutProjectMember(ctx, p.String("id"), user, "viewer", &role); err != nil || created {
		t.Fatalf("second put: %v %v", created, err)
	}
	if err := api.DeleteProjectMember(ctx, p.String("id"), user); err != nil {
		t.Fatal(err)
	}
	if _, err := api.GetProjectMember(ctx, p.String("id"), user); err == nil || !apiErr(t, err).IsNotFound() {
		t.Errorf("after delete: %v", err)
	}
}
