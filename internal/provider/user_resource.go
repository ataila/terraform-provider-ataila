// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/ataila/terraform-provider-ataila/internal/client"
)

var (
	_ resource.ResourceWithConfigure    = (*userResource)(nil)
	_ resource.ResourceWithModifyPlan   = (*userResource)(nil)
	_ resource.ResourceWithImportState  = (*userResource)(nil)
	_ resource.ResourceWithUpgradeState = (*userResource)(nil)
)

// Import prefixes of ataila_user.
const (
	EmailImportPrefix    = "email:"
	UsernameImportPrefix = "username:"
)

// NewUserResource is the factory for ataila_user.
func NewUserResource() resource.Resource { return &userResource{} }

type userResource struct {
	data *ProviderData
}

var userFrozen = frozenKey{object: "user", why: "A person's username and Windows name are their SSO and " +
	"directory account names, which the platform does not change once the account exists; replacing the " +
	"person instead would deactivate them for good, and their e-mail address would stay taken."}

// userCore is what the resource and the data sources share.
type userCore struct {
	ID                 types.String   `tfsdk:"id"`
	Email              types.String   `tfsdk:"email"`
	FirstName          types.String   `tfsdk:"first_name"`
	LastName           types.String   `tfsdk:"last_name"`
	Locale             types.String   `tfsdk:"locale"`
	NeedsGitAccess     types.Bool     `tfsdk:"needs_git_access"`
	IsActive           types.Bool     `tfsdk:"is_active"`
	Username           types.String   `tfsdk:"username"`
	AdUsername         types.String   `tfsdk:"ad_username"`
	Name               types.String   `tfsdk:"name"`
	Kind               types.String   `tfsdk:"kind"`
	IsInternal         types.Bool     `tfsdk:"is_internal"`
	AuthMode           types.String   `tfsdk:"auth_mode"`
	Roles              types.List     `tfsdk:"roles"`
	SSOLinked          types.Bool     `tfsdk:"sso_linked"`
	GitlabLinked       types.Bool     `tfsdk:"gitlab_linked"`
	SSOSyncStatus      types.String   `tfsdk:"sso_sync_status"`
	ProvisioningStatus types.String   `tfsdk:"provisioning_status"`
	CreatedAt          TimestampValue `tfsdk:"created_at"`
	UpdatedAt          TimestampValue `tfsdk:"updated_at"`
}

type userModel struct {
	userCore
	Warnings types.List `tfsdk:"warnings"`
}

// fromAPI fills the model. priorEmail keeps the configured spelling of an
// address the platform stores lower-cased.
func (m *userCore) fromAPI(u *client.User, priorEmail types.String) {
	m.ID = types.StringValue(u.Id)
	m.Email = types.StringNull()
	if u.Email != nil {
		m.Email = types.StringValue(*u.Email)
		if !priorEmail.IsNull() && !priorEmail.IsUnknown() && strings.EqualFold(priorEmail.ValueString(), *u.Email) {
			m.Email = priorEmail
		}
	}
	m.FirstName = stringOrNull(u.FirstName)
	m.LastName = stringOrNull(u.LastName)
	m.Locale = types.StringValue(string(u.Locale))
	m.NeedsGitAccess = types.BoolValue(u.NeedsGitAccess)
	m.IsActive = types.BoolValue(u.IsActive)
	m.Username = stringOrNull(u.Username)
	m.AdUsername = stringOrNull(u.AdUsername)
	m.Name = types.StringValue(u.Name)
	m.Kind = types.StringValue(string(u.Kind))
	m.IsInternal = types.BoolValue(u.IsInternal)
	m.AuthMode = types.StringNull()
	if u.AuthMode != nil {
		m.AuthMode = types.StringValue(string(*u.AuthMode))
	}
	roles := make([]attr.Value, 0, len(u.Roles))
	for _, r := range u.Roles {
		roles = append(roles, types.StringValue(r))
	}
	m.Roles = types.ListValueMust(types.StringType, roles)
	m.SSOLinked = types.BoolValue(u.SsoLinked)
	m.GitlabLinked = types.BoolValue(u.GitlabLinked)
	m.SSOSyncStatus = types.StringValue(string(u.SsoSyncStatus))
	m.ProvisioningStatus = types.StringNull()
	if u.ProvisioningStatus != nil {
		m.ProvisioningStatus = types.StringValue(string(*u.ProvisioningStatus))
	}
	m.CreatedAt = NewTimestamp(u.CreatedAt)
	m.UpdatedAt = NewTimestamp(u.UpdatedAt)
}

func (m *userCore) ident() string {
	if e := m.Email.ValueString(); e != "" {
		return fmt.Sprintf("%s (id %s)", e, m.ID.ValueString())
	}
	return "with id " + m.ID.ValueString()
}

func warningsOf(ws *[]client.ApiWarning) []client.ApiWarning {
	if ws == nil {
		return nil
	}
	return *ws
}

var userDocs = map[string]string{
	"id": "User id (a UUID), assigned by the platform.",
	"email": "E-mail address and local sign-in name; unique, deactivated people included. The platform stores " +
		"it entirely in lower case; the provider compares it case-insensitively and keeps the configuration's " +
		"spelling.",
	"first_name": "First name, 1-100 characters.",
	"last_name":  "Last name, 1-100 characters. Removing it from the configuration clears it.",
	"locale":     "`hu` (the default) or `en`.",
	"needs_git_access": "Whether the person gets a GitLab account. Switching it on creates the account; " +
		"switching it off removes none.",
	"is_active": "`true` (the default). `false` deactivates the person, exactly like destroying the resource, " +
		"and needs the same two switches; setting it back to `true` re-activates them.",
	"username": "Sign-in handle and directory account name, `^[a-z0-9._-]{1,20}$`, not ending in `.`. Omit it " +
		"and the platform derives `firstname.lastname`. Set at create only: a change later fails the plan.",
	"ad_username": "Windows (Active Directory) account name, same rule, for when `firstname.lastname` is " +
		"too long. Set at create only: a change later fails the plan.",
	"name":                "First and last name joined.",
	"kind":                "`human`, or `service` for a service account (read-only here).",
	"is_internal":         "Whether the person is platform staff. Read-only.",
	"auth_mode":           "Which sign-in routes the person may use: `sso`, `local` or `both`. Read-only.",
	"roles":               "Every role the person holds, sorted. Manage them with `ataila_user_role_grant`.",
	"sso_linked":          "An SSO account exists for the person.",
	"gitlab_linked":       "A GitLab account exists for the person.",
	"sso_sync_status":     "The last SSO projection of the person: `unlinked`, `pending`, `ok` or `error`.",
	"provisioning_status": "The newest provisioning run: `ok`, `partial` (a step skipped: no GitLab account wanted, or a platform without SSO, which also warns `sso_not_configured`), `error`, or null.",
	"created_at":          "When the person was created: RFC 3339 in UTC, compared as an instant.",
	"updated_at":          "When the person was last changed: RFC 3339 in UTC, compared as an instant.",
	"warnings": "What did not go as planned in the last create or change made through this resource " +
		"(for example a provisioning step that failed). Also reported as warnings when it happens.",
}

var (
	rxHandle   = regexp.MustCompile(`^[a-z0-9._-]{0,19}[a-z0-9_-]$`)
	rxTrimmed  = regexp.MustCompile(`^\S(.*\S)?$`)
	rxRoleName = regexp.MustCompile(`^[a-z][a-z0-9._-]{1,62}$`)
)

func (r *userResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user"
}

func (r *userResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	d := userDocs
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	handleValidators := []validator.String{stringvalidator.RegexMatches(rxHandle,
		"must be 1-20 lower-case letters, digits, '.', '_' or '-', not ending in '.'")}
	computedStr := func(name string, mods ...planmodifier.String) schema.StringAttribute {
		return schema.StringAttribute{MarkdownDescription: d[name], Computed: true, PlanModifiers: mods}
	}
	computedBool := func(name string) schema.BoolAttribute {
		return schema.BoolAttribute{MarkdownDescription: d[name], Computed: true}
	}
	resp.Schema = schema.Schema{
		Version: 1,
		MarkdownDescription: "A person on the platform.\n\n" +
			"Creating one sends no password and gets none back: the person signs in after an operator's " +
			"password reset in the portal or a self-service reset. The platform provisions them (SSO account; a " +
			"GitLab account with `needs_git_access`); a step that fails is a warning, never an error, and " +
			"`provisioning_status` says how it went.\n\n" +
			"**Destroy deactivates** the person and needs `allow_destroy = true` on the provider **and** a token " +
			"minted with destroy allowed. The platform refuses it for the token's own account, the last active " +
			"admin and service accounts. A deactivated person keeps their e-mail address: creating the same " +
			"address again is refused (`email_taken`); import the person and set `is_active = true` instead.\n\n" +
			"`username` and `ad_username` are set at create only; a change later fails the plan. `roles` is " +
			"read-only here: grant roles with `ataila_user_role_grant`.",
		Attributes: map[string]schema.Attribute{
			"id": computedStr("id", keep...),
			"email": schema.StringAttribute{
				MarkdownDescription: d["email"], Required: true,
				Validators: []validator.String{stringvalidator.RegexMatches(rxEmail, "must be an e-mail address")},
			},
			"first_name": schema.StringAttribute{
				MarkdownDescription: d["first_name"], Required: true,
				Validators: []validator.String{stringvalidator.LengthBetween(1, 100),
					stringvalidator.RegexMatches(rxTrimmed, "must not start or end with a space")},
			},
			"last_name": schema.StringAttribute{
				MarkdownDescription: d["last_name"], Optional: true,
				Validators: []validator.String{stringvalidator.LengthBetween(1, 100),
					stringvalidator.RegexMatches(rxTrimmed, "must not start or end with a space")},
			},
			"locale": schema.StringAttribute{
				MarkdownDescription: d["locale"], Optional: true, Computed: true,
				Default:    stringdefault.StaticString("hu"),
				Validators: []validator.String{stringvalidator.OneOf("hu", "en")},
			},
			"needs_git_access": schema.BoolAttribute{
				MarkdownDescription: d["needs_git_access"], Optional: true, Computed: true,
				Default: booldefault.StaticBool(false),
			},
			"is_active": schema.BoolAttribute{
				MarkdownDescription: d["is_active"], Optional: true, Computed: true,
				Default: booldefault.StaticBool(true),
			},
			"username": schema.StringAttribute{
				MarkdownDescription: d["username"], Optional: true, Computed: true,
				Validators:    handleValidators,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), userFrozen.forString()},
			},
			"ad_username": schema.StringAttribute{
				MarkdownDescription: d["ad_username"], Optional: true, Computed: true,
				Validators:    handleValidators,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), userFrozen.forString()},
			},
			"name":        computedStr("name"),
			"kind":        computedStr("kind", keep...),
			"is_internal": computedBool("is_internal"),
			"auth_mode":   computedStr("auth_mode"),
			"roles": schema.ListAttribute{
				MarkdownDescription: d["roles"], ElementType: types.StringType, Computed: true,
			},
			"sso_linked":          computedBool("sso_linked"),
			"gitlab_linked":       computedBool("gitlab_linked"),
			"sso_sync_status":     computedStr("sso_sync_status"),
			"provisioning_status": computedStr("provisioning_status"),
			"created_at": schema.StringAttribute{
				MarkdownDescription: d["created_at"], CustomType: TimestampType{}, Computed: true, PlanModifiers: keep,
			},
			"updated_at": schema.StringAttribute{
				MarkdownDescription: d["updated_at"], CustomType: TimestampType{}, Computed: true,
			},
			"warnings": warningsSchema(d["warnings"]),
		},
	}
}

// warningsSchema is the computed `warnings` attribute of a resource.
func warningsSchema(description string) schema.ListNestedAttribute {
	return schema.ListNestedAttribute{
		MarkdownDescription: description,
		Computed:            true,
		NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"code":    schema.StringAttribute{MarkdownDescription: "Stable warning code.", Computed: true},
			"message": schema.StringAttribute{MarkdownDescription: "What happened.", Computed: true},
		}},
	}
}

func (r *userResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *userResource) configured(diags interface{ AddError(string, string) }) bool {
	if r.data == nil {
		diags.AddError("Provider not configured", "The ataila provider was not configured.")
		return false
	}
	return true
}

func (r *userResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	var state, plan userModel
	if !req.State.Raw.IsNull() {
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	}
	if !req.Plan.Raw.IsNull() {
		resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	}
	if resp.Diagnostics.HasError() {
		return
	}
	switch {
	case req.Plan.Raw.IsNull(): // destroy
		if r.data != nil && !r.data.AllowDestroy {
			resp.Diagnostics.Append(destroyRefused("ataila_user", "user", state.ident()))
		}
	case req.State.Raw.IsNull(): // create
		if !plan.IsActive.IsUnknown() && !plan.IsActive.ValueBool() {
			resp.Diagnostics.AddAttributeError(path.Root("is_active"), "A new user is created active",
				"is_active = false on a user that does not exist yet would create and deactivate them at once. "+
					"Create the person with is_active = true (the default); to manage an existing deactivated "+
					"person, import them.")
		}
	default: // update
		if !plan.IsActive.IsUnknown() && !plan.IsActive.ValueBool() && state.IsActive.ValueBool() &&
			r.data != nil && !r.data.AllowDestroy {
			d := destroyRefused("ataila_user", "user", state.ident())
			resp.Diagnostics.AddAttributeError(path.Root("is_active"), "Setting is_active = false deactivates the user",
				"Deactivating is a destroy.\n\n"+d.Detail())
		}
	}
}

func (r *userResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan userModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := client.UserCreate{
		Email:      openapi_types.Email(plan.Email.ValueString()),
		FirstName:  plan.FirstName.ValueString(),
		LastName:   optString(plan.LastName),
		Username:   optString(plan.Username),
		AdUsername: optString(plan.AdUsername),
	}
	if v := optString(plan.Locale); v != nil {
		l := client.UserCreateLocale(*v)
		body.Locale = &l
	}
	if !plan.NeedsGitAccess.IsNull() && !plan.NeedsGitAccess.IsUnknown() {
		g := plan.NeedsGitAccess.ValueBool()
		body.NeedsGitAccess = &g
	}
	u, err := r.data.API.CreateUser(ctx, body)
	if err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && apiErr.Code() == "email_taken" {
			resp.Diagnostics.AddError("A user with this e-mail address exists",
				fmt.Sprintf("%s is taken, possibly by a deactivated person (deactivated people keep their "+
					"address). To manage that person, import them (tofu import / terraform import "+
					"ataila_user.<name> email:%s) and set is_active = true.\n\n%s",
					plan.Email.ValueString(), plan.Email.ValueString(), apiErr.Detail()))
			return
		}
		resp.Diagnostics.Append(apiError("creating the user "+plan.Email.ValueString(), err))
		return
	}
	addWarnings(&resp.Diagnostics, "creating the user "+plan.Email.ValueString(), u.Warnings)
	plan.fromAPI(u, plan.Email)
	plan.Warnings = warningsValue(warningsOf(u.Warnings))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *userResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var state userModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	u, err := r.data.API.GetUser(ctx, state.ID.ValueString())
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.Append(apiError("reading the user "+state.ident(), err))
		return
	}
	state.fromAPI(u, state.Email)
	state.Warnings = keepWarnings(state.Warnings)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *userResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan, state userModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	userFrozen.check(path.Root("username"), state.Username, plan.Username, true, &resp.Diagnostics)
	userFrozen.check(path.Root("ad_username"), state.AdUsername, plan.AdUsername, true, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	patch := client.Patch{}
	if !strings.EqualFold(plan.Email.ValueString(), state.Email.ValueString()) {
		patch["email"] = plan.Email.ValueString()
	}
	if !plan.FirstName.Equal(state.FirstName) {
		patch["first_name"] = plan.FirstName.ValueString()
	}
	if !plan.LastName.Equal(state.LastName) {
		patch["last_name"] = nullable(plan.LastName)
	}
	if !plan.Locale.Equal(state.Locale) {
		patch["locale"] = plan.Locale.ValueString()
	}
	if !plan.NeedsGitAccess.Equal(state.NeedsGitAccess) {
		patch["needs_git_access"] = plan.NeedsGitAccess.ValueBool()
	}
	deactivating := !plan.IsActive.ValueBool() && state.IsActive.ValueBool()
	if !plan.IsActive.Equal(state.IsActive) {
		if deactivating && !r.data.AllowDestroy {
			resp.Diagnostics.Append(destroyRefused("ataila_user", "user", state.ident()))
			return
		}
		patch["is_active"] = plan.IsActive.ValueBool()
	}
	var (
		u   *client.User
		err error
	)
	if len(patch) == 0 {
		u, err = r.data.API.GetUser(ctx, state.ID.ValueString())
	} else {
		u, err = r.data.API.UpdateUser(ctx, state.ID.ValueString(), patch)
	}
	if err != nil {
		if deactivating {
			resp.Diagnostics.Append(destroyError("ataila_user", "user", state.ident(), err))
			return
		}
		resp.Diagnostics.Append(apiError("changing the user "+state.ident(), err))
		return
	}
	addWarnings(&resp.Diagnostics, "changing the user "+state.ident(), u.Warnings)
	plan.fromAPI(u, plan.Email)
	plan.Warnings = warningsValue(warningsOf(u.Warnings))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *userResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var state userModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !r.data.AllowDestroy {
		resp.Diagnostics.Append(destroyRefused("ataila_user", "user", state.ident()))
		return
	}
	err := r.data.API.DeactivateUser(ctx, state.ID.ValueString())
	if err != nil && !isNotFound(err) {
		resp.Diagnostics.Append(destroyError("ataila_user", "user", state.ident(), err))
	}
}

func (r *userResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	u, diags := findUser(ctx, r.data.API, strings.TrimSpace(req.ID), "Cannot import the user")
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if u.Kind == client.UserKindService {
		resp.Diagnostics.AddError("Cannot import a service account",
			fmt.Sprintf("%s is a service account. Service accounts are managed on the portal's service accounts "+
				"page; every change to one through the API is refused. Read it with the ataila_user data source.",
				u.Name))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), u.Id)...)
}

// UpgradeState renames the attributes 0.7.0 renamed (schema version 0 to 1).
func (r *userResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return renameUpgraders(userRenamesV1)
}
