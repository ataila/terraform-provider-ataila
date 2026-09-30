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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ataila/terraform-provider-ataila/internal/client"
)

var (
	_ resource.ResourceWithConfigure   = (*projectMemberResource)(nil)
	_ resource.ResourceWithImportState = (*projectMemberResource)(nil)
)

// NewProjectMemberResource is the factory for ataila_project_member.
func NewProjectMemberResource() resource.Resource { return &projectMemberResource{} }

type projectMemberResource struct {
	data *ProviderData
}

type projectMemberModel struct {
	ID         types.String   `tfsdk:"id"`
	ProjectID  types.String   `tfsdk:"project_id"`
	UserID     types.String   `tfsdk:"user_id"`
	Role       types.String   `tfsdk:"role"`
	GitlabRole types.String   `tfsdk:"gitlab_role"`
	CreatedAt  TimestampValue `tfsdk:"created_at"`
}

func (m *projectMemberModel) fromAPI(pm *client.ProjectMember) {
	m.ID = types.StringValue(pm.ProjectId + "/" + pm.UserId)
	m.ProjectID = types.StringValue(pm.ProjectId)
	m.UserID = keepFoldString(m.UserID, pm.UserId)
	m.Role = types.StringValue(string(pm.Role))
	if pm.GitlabRole == nil {
		m.GitlabRole = types.StringNull()
	} else {
		m.GitlabRole = types.StringValue(string(*pm.GitlabRole))
	}
	m.CreatedAt = NewTimestampPointer(pm.CreatedAt)
}

// keepFoldString keeps the configured spelling of a value the platform
// stores in lower case.
func keepFoldString(prior types.String, stored string) types.String {
	if !prior.IsNull() && !prior.IsUnknown() && strings.EqualFold(prior.ValueString(), stored) {
		return prior
	}
	return types.StringValue(stored)
}

func (m *projectMemberModel) ident() string {
	return fmt.Sprintf("of user %s in project %s", m.UserID.ValueString(), m.ProjectID.ValueString())
}

func (r *projectMemberResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project_member"
}

func (r *projectMemberResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A user's membership in a project, with its role.\n\n" +
			"Only platform staff and members of a tenant of the project's customer can be project members. " +
			"Creating a membership that already exists adopts it and sets its role. Changing `project_id` " +
			"or `user_id` replaces the membership; changing `role` or `gitlab_role` updates it in place. " +
			"Destroying removes the membership and needs no `allow_destroy`; a member of a retired project " +
			"can still be removed, but a retired project takes no new members and no role changes.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "`<project_id>/<user_id>`.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"project_id": schema.StringAttribute{
				MarkdownDescription: "Id of the project. Changing it replaces the membership.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.RegexMatches(rxIntID, "must be a project id")},
				PlanModifiers:       replace,
			},
			"user_id": schema.StringAttribute{
				MarkdownDescription: "Id of the platform user. Changing it replaces the membership.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.RegexMatches(rxUUID, "must be a user id (a UUID)")},
				PlanModifiers:       append([]planmodifier.String{sameFold{}}, replace...),
			},
			"role": schema.StringAttribute{
				MarkdownDescription: "`owner`, `admin`, `developer` (the default), `member` or `viewer`.",
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("developer"),
				Validators:          []validator.String{stringvalidator.OneOf("owner", "admin", "developer", "member", "viewer")},
			},
			"gitlab_role": schema.StringAttribute{
				MarkdownDescription: "`guest`, `reporter`, `developer` or `maintainer` (platform staff only). " +
					"**Recorded, not enforced:** the platform stores it, but nothing creates the GitLab user or " +
					"its GitLab membership from it yet.",
				Optional:   true,
				Validators: []validator.String{stringvalidator.OneOf("guest", "reporter", "developer", "maintainer")},
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

func (r *projectMemberResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *projectMemberResource) configured(diags interface{ AddError(string, string) }) bool {
	if r.data == nil {
		diags.AddError("Provider not configured", "The ataila provider was not configured.")
		return false
	}
	return true
}

func (r *projectMemberResource) put(ctx context.Context, plan *projectMemberModel, doing string, diags *diag.Diagnostics) bool {
	pm, created, err := r.data.API.PutProjectMember(ctx, plan.ProjectID.ValueString(), plan.UserID.ValueString(),
		plan.Role.ValueString(), optString(plan.GitlabRole))
	if err != nil {
		diags.Append(projectMemberError(doing+" "+plan.ident(), err))
		return false
	}
	if doing == "adding the membership" && !created {
		diags.AddWarning("Existing project membership adopted",
			fmt.Sprintf("User %s already was a member of project %s. The provider now manages that membership "+
				"and set its role to %s. Destroying the resource removes the membership.",
				plan.UserID.ValueString(), plan.ProjectID.ValueString(), plan.Role.ValueString()))
	}
	plan.fromAPI(pm)
	return true
}

func projectMemberError(doing string, err error) diag.Diagnostic {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		hint := map[string]string{
			client.CodeProjectRetired: "The project is retired: it takes no new members and no role " +
				"changes. Members can still be removed.",
			"member_not_eligible": "Only platform staff and active members of a tenant of the project's " +
				"customer can be project members. Add the user to one of the customer's tenants first " +
				"(ataila_tenant_membership).",
			"gitlab_role_above_cap": "gitlab_role maintainer is for platform staff only; customers' users " +
				"get at most developer.",
			client.CodePlatformReadOnly: "The platform's own projects are read-only through the API.",
		}[apiErr.Code()]
		if hint != "" {
			return diag.NewErrorDiagnostic(apiErr.Summary(),
				fmt.Sprintf("While %s.\n\n%s\n\n%s", doing, hint, apiErr.Detail()))
		}
	}
	return apiError(doing, err)
}

func (r *projectMemberResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan projectMemberModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || !r.put(ctx, &plan, "adding the membership", &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *projectMemberResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var state projectMemberModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pm, err := r.data.API.GetProjectMember(ctx, state.ProjectID.ValueString(), state.UserID.ValueString())
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.Append(apiError("reading the membership "+state.ident(), err))
		return
	}
	state.fromAPI(pm)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *projectMemberResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan projectMemberModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || !r.put(ctx, &plan, "changing the membership", &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *projectMemberResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var state projectMemberModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.data.API.DeleteProjectMember(ctx, state.ProjectID.ValueString(), state.UserID.ValueString())
	if err != nil && !isNotFound(err) {
		resp.Diagnostics.Append(projectMemberError("removing the membership "+state.ident(), err))
	}
}

func (r *projectMemberResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	projectID, userID, ok := strings.Cut(strings.TrimSpace(req.ID), "/")
	if !ok || !rxIntID.MatchString(projectID) || !rxUUID.MatchString(userID) {
		resp.Diagnostics.AddError("Cannot import the project membership",
			fmt.Sprintf("The import id must be <project_id>/<user_id>, got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), projectID+"/"+userID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), projectID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("user_id"), userID)...)
}
