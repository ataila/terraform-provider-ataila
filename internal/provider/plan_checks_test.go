// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ataila/terraform-provider-ataila/internal/client"
)

var (
	present = tftypes.NewValue(tftypes.String, "object")
	absent  = tftypes.NewValue(tftypes.String, nil)
)

func stringReq(state, plan tftypes.Value, stateValue, planValue types.String) (planmodifier.StringRequest, *planmodifier.StringResponse) {
	req := planmodifier.StringRequest{
		Path:       path.Root("short_name"),
		State:      tfsdk.State{Raw: state},
		Plan:       tfsdk.Plan{Raw: plan},
		StateValue: stateValue,
		PlanValue:  planValue,
	}
	return req, &planmodifier.StringResponse{PlanValue: planValue}
}

func TestFrozenKeyRefusesAChangeAtPlanTime(t *testing.T) {
	req, resp := stringReq(present, present, types.StringValue("EXAMPLE"), types.StringValue("SAMPLE"))
	customerFrozen.forString().PlanModifyString(context.Background(), req, resp)

	if resp.Diagnostics.ErrorsCount() != 1 {
		t.Fatalf("want one error, got %v", resp.Diagnostics)
	}
	if resp.RequiresReplace {
		t.Error("a frozen key must never force a replacement")
	}
	d := resp.Diagnostics[0]
	if d.Summary() != "Cannot change short_name of an existing customer" {
		t.Errorf("summary %q", d.Summary())
	}
	for _, want := range []string{`It is "EXAMPLE"`, `asks for "SAMPLE"`, "only archives it", `set short_name back to "EXAMPLE"`} {
		if !strings.Contains(d.Detail(), want) {
			t.Errorf("detail lacks %q:\n%s", want, d.Detail())
		}
	}
	if withPath, ok := d.(diag.DiagnosticWithPath); !ok || !withPath.Path().Equal(path.Root("short_name")) {
		t.Error("the error must point at the attribute")
	}
}

func TestFrozenKeyAllowsWhatIsNotAChange(t *testing.T) {
	cases := map[string]struct {
		state, plan           tftypes.Value
		stateValue, planValue types.String
	}{
		"create":        {absent, present, types.StringNull(), types.StringValue("EXAMPLE")},
		"destroy":       {present, absent, types.StringValue("EXAMPLE"), types.StringNull()},
		"same value":    {present, present, types.StringValue("EXAMPLE"), types.StringValue("EXAMPLE")},
		"unknown value": {present, present, types.StringValue("EXAMPLE"), types.StringUnknown()},
	}
	for name, c := range cases {
		req, resp := stringReq(c.state, c.plan, c.stateValue, c.planValue)
		tenantFrozen.forString().PlanModifyString(context.Background(), req, resp)
		if resp.Diagnostics.HasError() || resp.RequiresReplace {
			t.Errorf("%s: %v (replace %v)", name, resp.Diagnostics, resp.RequiresReplace)
		}
	}
}

func TestFrozenInt64(t *testing.T) {
	req := planmodifier.Int64Request{
		Path: path.Root("customer_index"), State: tfsdk.State{Raw: present}, Plan: tfsdk.Plan{Raw: present},
		StateValue: types.Int64Value(7), PlanValue: types.Int64Value(8),
	}
	resp := &planmodifier.Int64Response{PlanValue: req.PlanValue}
	customerFrozen.forInt64().PlanModifyInt64(context.Background(), req, resp)
	if !resp.Diagnostics.HasError() || resp.RequiresReplace || !strings.Contains(resp.Diagnostics[0].Detail(), "It is 7") {
		t.Errorf("%v", resp.Diagnostics)
	}
	// Omitted from the configuration after create: UseStateForUnknown keeps it.
	req.PlanValue, resp.PlanValue = types.Int64Unknown(), types.Int64Unknown()
	resp.Diagnostics = nil
	customerFrozen.forInt64().PlanModifyInt64(context.Background(), req, resp)
	if resp.Diagnostics.HasError() {
		t.Errorf("unknown: %v", resp.Diagnostics)
	}
}

// replacingAttributes runs every attribute's plan modifiers with a changed
// value and returns the attributes that ask for a replacement, and how many
// modifiers ran.
func replacingAttributes(t *testing.T, r resource.Resource) (replacing []string, ran int) {
	ctx := context.Background()
	var sresp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sresp)
	for name, a := range sresp.Schema.Attributes {
		if sa, ok := a.(interface {
			StringPlanModifiers() []planmodifier.String
		}); ok {
			for _, m := range sa.StringPlanModifiers() {
				req, resp := stringReq(present, present, types.StringValue("a"), types.StringValue("b"))
				req.Path = path.Root(name)
				m.PlanModifyString(ctx, req, resp)
				ran++
				if resp.RequiresReplace {
					replacing = append(replacing, name)
				}
			}
		}
		if ia, ok := a.(interface {
			Int64PlanModifiers() []planmodifier.Int64
		}); ok {
			for _, m := range ia.Int64PlanModifiers() {
				req := planmodifier.Int64Request{Path: path.Root(name), State: tfsdk.State{Raw: present},
					Plan: tfsdk.Plan{Raw: present}, StateValue: types.Int64Value(1), PlanValue: types.Int64Value(2)}
				resp := &planmodifier.Int64Response{PlanValue: req.PlanValue}
				m.PlanModifyInt64(ctx, req, resp)
				ran++
				if resp.RequiresReplace {
					replacing = append(replacing, name)
				}
			}
		}
		if ba, ok := a.(interface {
			BoolPlanModifiers() []planmodifier.Bool
		}); ok {
			for _, m := range ba.BoolPlanModifiers() {
				req := planmodifier.BoolRequest{Path: path.Root(name), State: tfsdk.State{Raw: present},
					Plan: tfsdk.Plan{Raw: present}, StateValue: types.BoolValue(false), PlanValue: types.BoolValue(true)}
				resp := &planmodifier.BoolResponse{PlanValue: req.PlanValue}
				m.PlanModifyBool(ctx, req, resp)
				ran++
				if resp.RequiresReplace {
					replacing = append(replacing, name)
				}
			}
		}
	}
	return replacing, ran
}

// No attribute of a destroy-gated resource may force a replacement (F13).
func TestNoTenancyAttributeForcesReplacement(t *testing.T) {
	for _, r := range []resource.Resource{NewCustomerResource(), NewTenantResource(), NewUserResource(),
		NewGatewayKeyResource(), NewProjectResource(), NewProjectProvisioningResource()} {
		replacing, ran := replacingAttributes(t, r)
		if ran < 5 {
			t.Errorf("%T: only %d plan modifiers ran; the check is not looking", r, ran)
		}
		if len(replacing) > 0 {
			t.Errorf("%T: %v force replacement", r, replacing)
		}
	}
	// The detector works: a membership's tenant_id and user_id do replace.
	replacing, _ := replacingAttributes(t, NewTenantMembershipResource())
	if len(replacing) != 2 {
		t.Errorf("membership replacing attributes = %v, want tenant_id and user_id", replacing)
	}
	replacing, _ = replacingAttributes(t, NewProjectMemberResource())
	if len(replacing) != 2 {
		t.Errorf("project member replacing attributes = %v, want project_id and user_id", replacing)
	}
}

// Every frozen key of a project fails the plan on a change, and none replaces.
func TestProjectFrozenKeysFailThePlan(t *testing.T) {
	ctx := context.Background()
	var sresp resource.SchemaResponse
	NewProjectResource().Schema(ctx, resource.SchemaRequest{}, &sresp)
	for _, name := range []string{"tenant_id", "project_index", "short_name", "gitlab_repo_slug",
		"primary_domain", "deployment_backend", "network_only"} {
		var diags diag.Diagnostics
		replace := false
		switch a := sresp.Schema.Attributes[name].(type) {
		case interface{ StringPlanModifiers() []planmodifier.String }:
			for _, m := range a.StringPlanModifiers() {
				req, resp := stringReq(present, present, types.StringValue("a"), types.StringValue("b"))
				req.Path = path.Root(name)
				m.PlanModifyString(ctx, req, resp)
				diags.Append(resp.Diagnostics...)
				replace = replace || resp.RequiresReplace
			}
		case interface{ Int64PlanModifiers() []planmodifier.Int64 }:
			for _, m := range a.Int64PlanModifiers() {
				req := planmodifier.Int64Request{Path: path.Root(name), State: tfsdk.State{Raw: present},
					Plan: tfsdk.Plan{Raw: present}, StateValue: types.Int64Value(4), PlanValue: types.Int64Value(5)}
				resp := &planmodifier.Int64Response{PlanValue: req.PlanValue}
				m.PlanModifyInt64(ctx, req, resp)
				diags.Append(resp.Diagnostics...)
				replace = replace || resp.RequiresReplace
			}
		case interface{ BoolPlanModifiers() []planmodifier.Bool }:
			for _, m := range a.BoolPlanModifiers() {
				req := planmodifier.BoolRequest{Path: path.Root(name), State: tfsdk.State{Raw: present},
					Plan: tfsdk.Plan{Raw: present}, StateValue: types.BoolValue(false), PlanValue: types.BoolValue(true)}
				resp := &planmodifier.BoolResponse{PlanValue: req.PlanValue}
				m.PlanModifyBool(ctx, req, resp)
				diags.Append(resp.Diagnostics...)
				replace = replace || resp.RequiresReplace
			}
		default:
			t.Fatalf("%s: unexpected attribute type %T", name, a)
		}
		if replace || !diags.HasError() || !strings.Contains(diags[0].Summary(), "Cannot change "+name+" of an existing project") {
			t.Errorf("%s: replace=%v diags=%v", name, replace, diags)
		}
	}
}

func TestDestroyRefusedExplainsBothSwitches(t *testing.T) {
	d := destroyRefused("ataila_customer", "customer", "EXAMPLE (id 1)")
	if d.Severity() != diag.SeverityError || d.Summary() != "Destroying a customer is not allowed" {
		t.Fatalf("%s: %s", d.Severity(), d.Summary())
	}
	for _, want := range []string{
		"archive the customer EXAMPLE (id 1)", "allow_destroy = true", "token minted in the portal with destroy allowed",
		"tofu state rm ataila_customer.<name>", "terraform state rm ataila_customer.<name>", "removed block",
	} {
		if !strings.Contains(d.Detail(), want) {
			t.Errorf("detail lacks %q:\n%s", want, d.Detail())
		}
	}
}

// problemErr is what the transport returns for a problem answer.
func problemErr(t *testing.T, status int, body map[string]any) error {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", client.ProblemContentType)
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}))
	defer srv.Close()
	api, err := client.New(client.Config{Endpoint: srv.URL, Token: "t", MaxRetries: -1})
	if err != nil {
		t.Fatal(err)
	}
	return api.DeleteTenant(context.Background(), "x")
}

func TestDestroyErrors(t *testing.T) {
	err := problemErr(t, 403, map[string]any{"type": "t", "title": "Forbidden", "status": 403,
		"code": "destroy_not_allowed", "detail": "This API token was not created with allow_destroy."})
	d := destroyError("ataila_tenant", "tenant", "builds (id x)", err)
	if d.Summary() != "The token was not created with allow_destroy" || !strings.Contains(d.Detail(), "provider's allow_destroy is true") {
		t.Errorf("403: %s\n%s", d.Summary(), d.Detail())
	}

	err = problemErr(t, 409, map[string]any{"type": "t", "title": "Conflict", "status": 409,
		"code": "tenant_has_projects", "detail": "The tenant is not empty.",
		"blockers": map[string]any{"projects": 2, "contracts": 1}})
	d = destroyError("ataila_tenant", "tenant", "builds (id x)", err)
	if d.Summary() != "The platform refused to delete the tenant (tenant_has_projects)" {
		t.Errorf("409 summary: %s", d.Summary())
	}
	for _, want := range []string{"Only an empty tenant", "blockers: contracts=1, projects=2", "code: tenant_has_projects"} {
		if !strings.Contains(d.Detail(), want) {
			t.Errorf("409 detail lacks %q:\n%s", want, d.Detail())
		}
	}

	err = problemErr(t, 409, map[string]any{"type": "t", "title": "Conflict", "status": 409,
		"code": "customer_has_projects", "detail": "The customer still has 3 project(s).",
		"blockers": map[string]any{"projects": 3}})
	d = destroyError("ataila_customer", "customer", "EXAMPLE (id 1)", err)
	if !strings.Contains(d.Summary(), "archive the customer (customer_has_projects)") || !strings.Contains(d.Detail(), "blockers: projects=3") {
		t.Errorf("customer 409: %s\n%s", d.Summary(), d.Detail())
	}
}

func TestWarningsAreNeverErrors(t *testing.T) {
	var diags diag.Diagnostics
	addWarnings(&diags, "creating the customer EXAMPLE", &[]client.ApiWarning{
		{Code: "gitlab_group_not_ready", Message: "The group exists; its owner was not added."},
		{Code: "audit_not_recorded", Message: ""},
	})
	if diags.HasError() || diags.WarningsCount() != 2 {
		t.Fatalf("%v", diags)
	}
	if diags[0].Summary() != "ATAILA API warning: gitlab_group_not_ready" || !strings.Contains(diags[0].Detail(), "its owner was not added") {
		t.Errorf("%s\n%s", diags[0].Summary(), diags[0].Detail())
	}
	addWarnings(&diags, "x", nil)
	if len(diags) != 2 {
		t.Error("nil warnings added something")
	}
}

func TestEmailSpelling(t *testing.T) {
	if !sameEmail("Ops@Example.COM", "Ops@example.com") || sameEmail("ops@example.com", "Ops@example.com") {
		t.Error("the domain is case-insensitive, the local part is not")
	}
	if got := keepEmail(types.StringValue("Ops@Example.COM"), "Ops@example.com"); got.ValueString() != "Ops@Example.COM" {
		t.Errorf("configured spelling lost: %s", got)
	}
	if got := keepEmail(types.StringValue("old@example.com"), "new@example.com"); got.ValueString() != "new@example.com" {
		t.Errorf("a real change was hidden: %s", got)
	}
	if got := keepEmail(types.StringNull(), "a@example.com"); got.ValueString() != "a@example.com" {
		t.Errorf("import: %s", got)
	}
}

// Delete refuses before touching the API: this resource has no API at all,
// so any call would panic.
func TestDeleteRefusesBeforeAnyCall(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		r     resource.Resource
		model any
	}{
		{&customerResource{data: &ProviderData{}}, &customerModel{ID: types.StringValue("1"), ShortName: types.StringValue("EXAMPLE")}},
		{&tenantResource{data: &ProviderData{}}, &tenantModel{ID: types.StringValue("t"), Slug: types.StringValue("builds")}},
	} {
		var sresp resource.SchemaResponse
		tc.r.Schema(ctx, resource.SchemaRequest{}, &sresp)
		state := tfsdk.State{Schema: sresp.Schema, Raw: tftypes.NewValue(sresp.Schema.Type().TerraformType(ctx), nil)}
		if diags := state.Set(ctx, tc.model); diags.HasError() {
			t.Fatalf("%v", diags)
		}
		resp := &resource.DeleteResponse{State: state}
		tc.r.Delete(ctx, resource.DeleteRequest{State: state}, resp)
		if !resp.Diagnostics.HasError() || !strings.HasPrefix(resp.Diagnostics[0].Summary(), "Destroying a") {
			t.Errorf("%T: %v", tc.r, resp.Diagnostics)
		}
	}
}

// ModifyPlan refuses a destroy at plan time when allow_destroy is off, and
// lets it through when it is on.
func TestModifyPlanGatesDestroy(t *testing.T) {
	ctx := context.Background()
	for _, allow := range []bool{false, true} {
		r := &tenantResource{data: &ProviderData{AllowDestroy: allow}}
		var sresp resource.SchemaResponse
		r.Schema(ctx, resource.SchemaRequest{}, &sresp)
		typ := sresp.Schema.Type().TerraformType(ctx)
		state := tfsdk.State{Schema: sresp.Schema, Raw: tftypes.NewValue(typ, nil)}
		if diags := state.Set(ctx, &tenantModel{ID: types.StringValue("t"), Slug: types.StringValue("builds")}); diags.HasError() {
			t.Fatal(diags)
		}
		plan := tfsdk.Plan{Schema: sresp.Schema, Raw: tftypes.NewValue(typ, nil)}
		resp := &resource.ModifyPlanResponse{Plan: plan}
		r.ModifyPlan(ctx, resource.ModifyPlanRequest{State: state, Plan: plan}, resp)
		if resp.Diagnostics.HasError() == allow {
			t.Errorf("allow_destroy=%v: %v", allow, resp.Diagnostics)
		}
	}
}
