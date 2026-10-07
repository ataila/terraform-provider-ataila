// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// userPlan runs the user resource's ModifyPlan from state to plan (nil = none).
func userPlan(t *testing.T, allow bool, state, plan *userModel) *resource.ModifyPlanResponse {
	t.Helper()
	ctx := context.Background()
	r := &userResource{data: &ProviderData{AllowDestroy: allow}}
	var sresp resource.SchemaResponse
	r.Schema(ctx, resource.SchemaRequest{}, &sresp)
	typ := sresp.Schema.Type().TerraformType(ctx)
	mk := func(m *userModel) tftypes.Value {
		if m == nil {
			return tftypes.NewValue(typ, nil)
		}
		st := tfsdk.State{Schema: sresp.Schema, Raw: tftypes.NewValue(typ, nil)}
		m.Roles = types.ListNull(types.StringType)
		m.Warnings = warningsValue(nil)
		if d := st.Set(ctx, m); d.HasError() {
			t.Fatal(d)
		}
		return st.Raw
	}
	req := resource.ModifyPlanRequest{
		State: tfsdk.State{Schema: sresp.Schema, Raw: mk(state)},
		Plan:  tfsdk.Plan{Schema: sresp.Schema, Raw: mk(plan)},
	}
	resp := &resource.ModifyPlanResponse{Plan: req.Plan}
	r.ModifyPlan(ctx, req, resp)
	return resp
}

func person(active bool) *userModel {
	return &userModel{userCore: userCore{ID: types.StringValue("u"), Email: types.StringValue("p@example.com"),
		IsActive: types.BoolValue(active)}}
}

func TestUserDeactivationIsDestroyGatedAtPlanTime(t *testing.T) {
	cases := []struct {
		name        string
		allow       bool
		state, plan *userModel
		want        string
	}{
		{"destroy without the switch", false, person(true), nil, "Destroying a user is not allowed"},
		{"destroy with the switch", true, person(true), nil, ""},
		{"is_active false without the switch", false, person(true), person(false), "Setting is_active = false deactivates the user"},
		{"is_active false with the switch", true, person(true), person(false), ""},
		{"re-activation needs no switch", false, person(false), person(true), ""},
		{"create inactive", true, nil, person(false), "A new user is created active"},
	}
	for _, c := range cases {
		resp := userPlan(t, c.allow, c.state, c.plan)
		got := ""
		if resp.Diagnostics.HasError() {
			got = resp.Diagnostics[0].Summary()
		}
		if got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestGrantAndKeyErrorsAreExplained(t *testing.T) {
	grant := func(code string) string {
		err := problemErr(t, 403, map[string]any{"type": "t", "title": "Forbidden", "status": 403, "code": code})
		d := grantError("granting x", "admin", err)
		return d.Summary() + "\n" + d.Detail()
	}
	for code, want := range map[string]string{
		"role_not_manageable_by_token":  "An API token cannot grant or revoke admin",
		"role_not_held_by_token":        "The token does not carry the role admin",
		"token_cannot_change_own_roles": "A token cannot change its own account's roles",
		"something_else":                "ATAILA API error",
	} {
		if got := grant(code); !strings.Contains(got, want) {
			t.Errorf("%s: %s", code, got)
		}
	}
	for code, want := range map[string]string{
		"key_adopted":            "Rotate it where the consumer is deployed",
		"key_missing_on_gateway": "-replace=<address>",
		"gateway_not_configured": "The AI gateway is not available (gateway_not_configured)",
	} {
		status := 409
		if strings.HasPrefix(code, "gateway_") {
			status = 503
		}
		err := problemErr(t, status, map[string]any{"type": "t", "title": "x", "status": status, "code": code})
		summary, detail := gatewayKeyDiag("rotating k", err)
		if !strings.Contains(summary+"\n"+detail, want) {
			t.Errorf("%s: %s\n%s", code, summary, detail)
		}
	}
}
