// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
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
	_ resource.ResourceWithConfigure   = (*membershipResource)(nil)
	_ resource.ResourceWithImportState = (*membershipResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*membershipResource)(nil)
)

// legacyRole can be read on old memberships but no longer set.
const legacyRole = "developer"

// NewTenantMembershipResource is the factory for ataila_tenant_membership.
func NewTenantMembershipResource() resource.Resource { return &membershipResource{} }

type membershipResource struct {
	data *ProviderData
}

type membershipModel struct {
	ID        types.String   `tfsdk:"id"`
	TenantID  types.String   `tfsdk:"tenant_id"`
	UserID    types.String   `tfsdk:"user_id"`
	Role      types.String   `tfsdk:"role"`
	CreatedAt TimestampValue `tfsdk:"created_at"`
}

func (m *membershipModel) fromAPI(mm *client.Membership) {
	m.ID = types.StringValue(mm.TenantId + "/" + mm.UserId)
	m.TenantID = types.StringValue(mm.TenantId)
	m.UserID = types.StringValue(mm.UserId)
	m.Role = types.StringValue(string(mm.Role))
	m.CreatedAt = NewTimestampPointer(mm.CreatedAt)
}

func (m *membershipModel) ident() string {
	return fmt.Sprintf("of user %s in tenant %s", m.UserID.ValueString(), m.TenantID.ValueString())
}

func (r *membershipResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_tenant_membership"
}

func (r *membershipResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A user's membership in a tenant, with its role. The platform mirrors it into " +
			"the identity provider's tenant groups.\n\n" +
			"Creating a membership that already exists adopts it and sets its role (a warning says so). " +
			"Changing `tenant_id` or `user_id` replaces the membership, which is safe; changing `role` " +
			"updates it in place. Destroying removes the membership and needs no `allow_destroy`.\n\n" +
			"A legacy membership may hold the role `developer`, which can be read but no longer set. " +
			"Import it and leave `role` out of the configuration (or set it to `developer`) and it shows " +
			"no difference; planning to set `developer` anywhere else fails the plan.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "`<tenant_id>/<user_id>`.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"tenant_id": schema.StringAttribute{
				MarkdownDescription: "Id of the tenant. Changing it replaces the membership.",
				Required:            true,
				PlanModifiers:       replace,
			},
			"user_id": schema.StringAttribute{
				MarkdownDescription: "Id of the platform user. Changing it replaces the membership.",
				Required:            true,
				PlanModifiers:       replace,
			},
			"role": schema.StringAttribute{
				MarkdownDescription: "`owner`, `admin`, `member` or `viewer`. Required to create a membership. " +
					"Leave it out to keep an existing membership's role as it is, for example a legacy " +
					"`developer`, which can be read but no longer set.",
				Optional:      true,
				Computed:      true,
				Validators:    []validator.String{stringvalidator.OneOf("owner", "admin", "member", "viewer", legacyRole)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "When the membership was created: RFC 3339 in UTC, compared as an instant.",
				CustomType:          TimestampType{},
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *membershipResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *membershipResource) configured(diags interface{ AddError(string, string) }) bool {
	if r.data == nil {
		diags.AddError("Provider not configured", "The ataila provider was not configured.")
		return false
	}
	return true
}

// ModifyPlan checks the role at plan time: a new membership needs one, and
// `developer` can only be kept, never set.
func (r *membershipResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return // destroy: memberships are not destroy-gated
	}
	var plan membershipModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	var configRole types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("role"), &configRole)...)
	if resp.Diagnostics.HasError() || configRole.IsUnknown() {
		return
	}
	creating := req.State.Raw.IsNull() || len(resp.RequiresReplace) > 0
	stateRole := ""
	if !req.State.Raw.IsNull() {
		var state membershipModel
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		stateRole = state.Role.ValueString()
	}
	switch {
	case creating && configRole.IsNull():
		resp.Diagnostics.AddAttributeError(path.Root("role"), "A new membership needs a role",
			"Set role to owner, admin, member or viewer. Leaving role out is only for keeping the role of a "+
				"membership that already exists (for example one imported with the legacy role developer).")
	case plan.Role.ValueString() == legacyRole && (creating || stateRole != legacyRole):
		resp.Diagnostics.AddAttributeError(path.Root("role"), "The role developer can no longer be set",
			"developer is a legacy role: the platform still reports it on old memberships but accepts only "+
				"owner, admin, member or viewer when a role is set. Choose one of those. A membership that "+
				"already holds developer keeps it as long as the configuration leaves role out or says developer.")
	}
}

func (r *membershipResource) put(ctx context.Context, plan *membershipModel, adopt bool, diags interface {
	AddWarning(string, string)
}) (*client.Membership, error) {
	mm, created, err := r.data.API.PutMembership(ctx, plan.TenantID.ValueString(), plan.UserID.ValueString(),
		plan.Role.ValueString())
	if err == nil && adopt && !created {
		diags.AddWarning("Existing membership adopted",
			fmt.Sprintf("User %s already was a member of tenant %s. The provider now manages that membership "+
				"and set its role to %s. Destroying the resource removes the membership.",
				plan.UserID.ValueString(), plan.TenantID.ValueString(), plan.Role.ValueString()))
	}
	return mm, err
}

func (r *membershipResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan membershipModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	mm, err := r.put(ctx, &plan, true, &resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.Append(apiError("adding the membership "+plan.ident(), err))
		return
	}
	plan.fromAPI(mm)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *membershipResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var state membershipModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	mm, err := r.data.API.GetMembership(ctx, state.TenantID.ValueString(), state.UserID.ValueString())
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.Append(apiError("reading the membership "+state.ident(), err))
		return
	}
	state.fromAPI(mm)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *membershipResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan membershipModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	mm, err := r.put(ctx, &plan, false, &resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.Append(apiError("changing the membership "+plan.ident(), err))
		return
	}
	plan.fromAPI(mm)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *membershipResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var state membershipModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.data.API.DeleteMembership(ctx, state.TenantID.ValueString(), state.UserID.ValueString())
	if err != nil && !isNotFound(err) {
		resp.Diagnostics.Append(apiError("removing the membership "+state.ident(), err))
	}
}

func (r *membershipResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	tenantID, userID, ok := strings.Cut(strings.TrimSpace(req.ID), "/")
	if !ok || tenantID == "" || userID == "" || strings.Contains(userID, "/") {
		resp.Diagnostics.AddError("Cannot import the membership",
			fmt.Sprintf("The import id must be <tenant_id>/<user_id>, got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), tenantID+"/"+userID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("tenant_id"), tenantID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("user_id"), userID)...)
}
