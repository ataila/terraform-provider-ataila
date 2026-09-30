// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ataila/terraform-provider-ataila/internal/client"
)

var (
	_ resource.ResourceWithConfigure   = (*roleGrantResource)(nil)
	_ resource.ResourceWithImportState = (*roleGrantResource)(nil)
)

// NewUserRoleGrantResource is the factory for ataila_user_role_grant.
func NewUserRoleGrantResource() resource.Resource { return &roleGrantResource{} }

type roleGrantResource struct {
	data *ProviderData
}

type roleGrantModel struct {
	ID        types.String   `tfsdk:"id"`
	UserID    types.String   `tfsdk:"user_id"`
	Role      types.String   `tfsdk:"role"`
	GrantedAt TimestampValue `tfsdk:"granted_at"`
}

func (m *roleGrantModel) fromAPI(g *client.RoleGrant) {
	m.ID = types.StringValue(g.UserId + "/" + g.Role)
	m.UserID = types.StringValue(g.UserId)
	m.Role = types.StringValue(g.Role)
	m.GrantedAt = NewTimestampPointer(g.GrantedAt)
}

func (m *roleGrantModel) ident() string {
	return fmt.Sprintf("%s of user %s", m.Role.ValueString(), m.UserID.ValueString())
}

func (r *roleGrantResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user_role_grant"
}

func (r *roleGrantResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "One role held by one user. Grants are additive: this resource adds or removes its " +
			"own role and leaves the others alone. Changing `user_id` or `role` replaces the grant, which is safe. " +
			"Destroying removes the role and needs no `allow_destroy`.\n\n" +
			"Granting a role the user already holds adopts it (a warning says so). An API token can never grant " +
			"or revoke `admin`, `founder` or `ssh-console`, never change its own account's roles, and grants or " +
			"revokes only roles it carries itself; each is a clear error, never retried. The platform also keeps " +
			"at least one role per user and one active admin.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "`<user_id>/<role>`.", Computed: true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"user_id": schema.StringAttribute{
				MarkdownDescription: "Id of the user. Changing it replaces the grant.", Required: true,
				PlanModifiers: replace,
			},
			"role": schema.StringAttribute{
				MarkdownDescription: "A role name from the roles catalogue: a permission key (see " +
					"`ataila_permission_catalog`, `grantable`) or a role such as `user`. Changing it replaces the grant.",
				Required:      true,
				Validators:    []validator.String{stringvalidator.RegexMatches(rxRoleName, "must be a role name")},
				PlanModifiers: replace,
			},
			"granted_at": schema.StringAttribute{
				MarkdownDescription: "When the role was granted, when the platform knows it: RFC 3339 in UTC.",
				CustomType:          TimestampType{}, Computed: true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *roleGrantResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *roleGrantResource) configured(diags interface{ AddError(string, string) }) bool {
	if r.data == nil {
		diags.AddError("Provider not configured", "The ataila provider was not configured.")
		return false
	}
	return true
}

// grantError explains a refused grant or revoke; the platform's role rules
// are final, never retried.
func grantError(doing, role string, err error) diag.Diagnostic {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		return apiError(doing, err)
	}
	text := map[string][2]string{
		"role_not_manageable_by_token": {"An API token cannot grant or revoke " + role,
			"admin, founder and ssh-console are never granted or revoked by an API token. A person who holds " +
				"the role grants it in the portal."},
		"role_not_held_by_token": {"The token does not carry the role " + role,
			"An API token grants or revokes only roles within its own scopes. Use a token minted with " + role +
				" (a key its owner holds), or grant it in the portal."},
		"token_cannot_change_own_roles": {"A token cannot change its own account's roles",
			"The user is the token's own principal. Change their roles in the portal or with another principal's token."},
		"role_not_grantable": {"The role " + role + " is not grantable",
			"The platform honours this key for those who hold it but does not grant it any more " +
				"(ataila_permission_catalog shows grantable = false)."},
		"unknown_role":                        {"No such role: " + role, "The role is not in the platform's roles catalogue."},
		"privileged_role_requires_holding_it": {"Granting " + role + " requires holding it", ""},
		"cannot_grant_to_self":                {"Cannot grant " + role + " to yourself", ""},
		"last_role":                           {"A user keeps at least one role", "Grant the user another role before removing this one."},
		"last_active_admin":                   {"The last active admin keeps admin", "Make another person admin first."},
		"cannot_remove_own_admin":             {"Cannot remove your own admin role", ""},
		"service_account_managed_elsewhere": {"The user is a service account",
			"Service accounts' roles are managed on the portal's service accounts page."},
	}[apiErr.Code()]
	if text[0] == "" {
		return apiError(doing, err)
	}
	detail := apiErr.Detail()
	if text[1] != "" {
		detail = text[1] + "\n\n" + detail
	}
	return diag.NewErrorDiagnostic(text[0], "While "+doing+".\n\n"+detail)
}

func (r *roleGrantResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan roleGrantModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	g, granted, err := r.data.API.GrantRole(ctx, plan.UserID.ValueString(), plan.Role.ValueString())
	if err != nil {
		resp.Diagnostics.Append(grantError("granting "+plan.ident(), plan.Role.ValueString(), err))
		return
	}
	if !granted {
		resp.Diagnostics.AddWarning("Existing role grant adopted",
			fmt.Sprintf("User %s already held %s. The provider now manages that grant; destroying the resource "+
				"removes the role.", plan.UserID.ValueString(), plan.Role.ValueString()))
	}
	addWarnings(&resp.Diagnostics, "granting "+plan.ident(), g.Warnings)
	plan.fromAPI(g)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *roleGrantResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var state roleGrantModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	g, err := r.data.API.GetRoleGrant(ctx, state.UserID.ValueString(), state.Role.ValueString())
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.Append(apiError("reading "+state.ident(), err))
		return
	}
	state.fromAPI(g)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is never called with a change: both inputs force replacement.
func (r *roleGrantResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan roleGrantModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *roleGrantResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var state roleGrantModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.data.API.RevokeRole(ctx, state.UserID.ValueString(), state.Role.ValueString())
	if err != nil && !isNotFound(err) {
		resp.Diagnostics.Append(grantError("removing "+state.ident(), state.Role.ValueString(), err))
	}
}

func (r *roleGrantResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	userID, role, ok := strings.Cut(strings.TrimSpace(req.ID), "/")
	if !ok || userID == "" || role == "" || strings.Contains(role, "/") {
		resp.Diagnostics.AddError("Cannot import the role grant",
			fmt.Sprintf("The import id must be <user_id>/<role>, got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), userID+"/"+role)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("user_id"), userID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("role"), role)...)
}
