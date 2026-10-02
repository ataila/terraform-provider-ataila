// Copyright (c) 2026 ATAILA Kft.
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ataila/terraform-provider-ataila/internal/client"
)

var (
	_ datasource.DataSourceWithConfigure        = (*userDataSource)(nil)
	_ datasource.DataSourceWithConfigValidators = (*userDataSource)(nil)
	_ datasource.DataSourceWithConfigure        = (*usersDataSource)(nil)
	_ datasource.DataSourceWithConfigure        = (*permissionCatalogDataSource)(nil)
)

// NewUserDataSource is the factory for the ataila_user data source.
func NewUserDataSource() datasource.DataSource { return &userDataSource{} }

// NewUsersDataSource is the factory for the ataila_users data source.
func NewUsersDataSource() datasource.DataSource { return &usersDataSource{} }

// NewPermissionCatalogDataSource is the factory for ataila_permission_catalog.
func NewPermissionCatalogDataSource() datasource.DataSource { return &permissionCatalogDataSource{} }

// findUser resolves an id, `email:<address>` or `username:<name>` to one
// user (service accounts included).
func findUser(ctx context.Context, api *client.API, ref, summary string) (*client.User, diag.Diagnostics) {
	var diags diag.Diagnostics
	filter := client.UserFilter{Kind: "all"}
	what := ""
	switch {
	case strings.HasPrefix(ref, EmailImportPrefix):
		filter.Email = strings.TrimPrefix(ref, EmailImportPrefix)
		what = "e-mail address " + filter.Email
	case strings.HasPrefix(ref, UsernameImportPrefix):
		filter.Username = strings.TrimPrefix(ref, UsernameImportPrefix)
		what = "username " + filter.Username
	default:
		if ref == "" {
			diags.AddError(summary, "Give the user id, email:<address> or username:<name>.")
			return nil, diags
		}
		u, err := api.GetUser(ctx, ref)
		if isNotFound(err) {
			diags.AddError(summary, fmt.Sprintf("No user has id %q.", ref))
			return nil, diags
		}
		if err != nil {
			diags.Append(apiError("reading the user "+ref, err))
			return nil, diags
		}
		return u, diags
	}
	found, err := api.ListUsers(ctx, filter)
	if err != nil {
		diags.Append(apiError("looking up the user with "+what, err))
		return nil, diags
	}
	if len(found) != 1 {
		diags.AddError(summary, fmt.Sprintf("%d users have %s; exactly one is needed.", len(found), what))
		return nil, diags
	}
	return &found[0], diags
}

// userDataAttributes are one user's attributes in a data source; lookups
// replaces the ones taken as input.
func userDataAttributes(lookups map[string]schema.StringAttribute) map[string]schema.Attribute {
	d := userDocs
	str := func(name string) schema.Attribute {
		if a, ok := lookups[name]; ok {
			return a
		}
		return schema.StringAttribute{MarkdownDescription: d[name], Computed: true}
	}
	boolean := func(name string) schema.Attribute {
		return schema.BoolAttribute{MarkdownDescription: d[name], Computed: true}
	}
	ts := func(name string) schema.Attribute {
		return schema.StringAttribute{MarkdownDescription: d[name], CustomType: TimestampType{}, Computed: true}
	}
	return map[string]schema.Attribute{
		"id": str("id"), "email": str("email"), "first_name": str("first_name"), "last_name": str("last_name"),
		"locale": str("locale"), "needs_git_access": boolean("needs_git_access"), "is_active": boolean("is_active"),
		"username": str("username"), "ad_username": str("ad_username"), "name": str("name"), "kind": str("kind"),
		"is_internal": boolean("is_internal"), "auth_mode": str("auth_mode"),
		"roles":      schema.ListAttribute{MarkdownDescription: d["roles"], ElementType: types.StringType, Computed: true},
		"sso_linked": boolean("sso_linked"), "gitlab_linked": boolean("gitlab_linked"),
		"sso_sync_status": str("sso_sync_status"), "provisioning_status": str("provisioning_status"),
		"created_at": ts("created_at"), "updated_at": ts("updated_at"),
	}
}

type userDataSource struct {
	data *ProviderData
}

func (d *userDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user"
}

func (d *userDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	lookup := func(name, how string) schema.StringAttribute {
		return schema.StringAttribute{MarkdownDescription: userDocs[name] + " " + how, Optional: true, Computed: true}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "One user, looked up by exactly one of `id`, `email` (case-insensitive) or `username`. " +
			"Service accounts and deactivated people are found too; check `kind` and `is_active`.",
		Attributes: userDataAttributes(map[string]schema.StringAttribute{
			"id":       lookup("id", "Set it to look the user up by id."),
			"email":    lookup("email", "Set it to look the user up by e-mail address."),
			"username": lookup("username", "Set it to look the user up by username."),
		}),
	}
}

func (d *userDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("email"), path.MatchRoot("username")),
	}
}

func (d *userDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *userDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.data == nil {
		resp.Diagnostics.AddError("Provider not configured", "The ataila provider was not configured.")
		return
	}
	var cfg userCore
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ref := cfg.ID.ValueString()
	switch {
	case !cfg.Email.IsNull():
		ref = EmailImportPrefix + cfg.Email.ValueString()
	case !cfg.Username.IsNull():
		ref = UsernameImportPrefix + cfg.Username.ValueString()
	}
	u, diags := findUser(ctx, d.data.API, ref, "User not found")
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	var state userCore
	state.fromAPI(u, cfg.Email)
	// A lookup attribute keeps the configuration's spelling of the same value.
	if !cfg.Username.IsNull() && strings.EqualFold(cfg.Username.ValueString(), state.Username.ValueString()) {
		state.Username = cfg.Username
	}
	if !cfg.ID.IsNull() && strings.EqualFold(cfg.ID.ValueString(), state.ID.ValueString()) {
		state.ID = cfg.ID
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

type usersDataSource struct {
	data *ProviderData
}

type usersModel struct {
	Email      types.String `tfsdk:"email"`
	Username   types.String `tfsdk:"username"`
	Kind       types.String `tfsdk:"kind"`
	IsActive   types.Bool   `tfsdk:"is_active"`
	TenantID   types.String `tfsdk:"tenant_id"`
	CustomerID types.String `tfsdk:"customer_id"`
	Role       types.String `tfsdk:"role"`
	Users      []userCore   `tfsdk:"users"`
}

func (d *usersDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_users"
}

func (d *usersDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	filter := func(desc string, v ...validator.String) schema.StringAttribute {
		return schema.StringAttribute{MarkdownDescription: desc, Optional: true, Validators: v}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Users, filtered; every page is read. People only unless `kind` says otherwise.",
		Attributes: map[string]schema.Attribute{
			"email":    filter("Only this e-mail address (case-insensitive)."),
			"username": filter("Only this username (case-insensitive)."),
			"kind": filter("`human` (the default), `service` or `all`.",
				stringvalidator.OneOf("human", "service", "all")),
			"is_active": schema.BoolAttribute{MarkdownDescription: "Only active (`true`) or deactivated (`false`) users.", Optional: true},
			"tenant_id": filter("Only members of this tenant."),
			"customer_id": filter("Only members of one of this customer's tenants.",
				stringvalidator.RegexMatches(rxIntID, "must be a customer id")),
			"role": filter("Only holders of this role."),
			"users": schema.ListNestedAttribute{
				MarkdownDescription: "The users, ordered by id.",
				Computed:            true,
				NestedObject:        schema.NestedAttributeObject{Attributes: userDataAttributes(nil)},
			},
		},
	}
}

func (d *usersDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *usersDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.data == nil {
		resp.Diagnostics.AddError("Provider not configured", "The ataila provider was not configured.")
		return
	}
	var cfg usersModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	f := client.UserFilter{Email: cfg.Email.ValueString(), Username: cfg.Username.ValueString(),
		Kind: cfg.Kind.ValueString(), TenantID: cfg.TenantID.ValueString(),
		CustomerID: cfg.CustomerID.ValueString(), Role: cfg.Role.ValueString()}
	if !cfg.IsActive.IsNull() {
		v := cfg.IsActive.ValueBool()
		f.IsActive = &v
	}
	found, err := d.data.API.ListUsers(ctx, f)
	if err != nil {
		summary, detail := apiErrorText("the users (GET /users)", err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	cfg.Users = make([]userCore, len(found))
	for i := range found {
		cfg.Users[i].fromAPI(&found[i], types.StringNull())
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}

type permissionCatalogDataSource struct {
	data *ProviderData
}

type permissionModel struct {
	Key         types.String `tfsdk:"key"`
	Feature     types.String `tfsdk:"feature"`
	Level       types.String `tfsdk:"level"`
	Scope       types.String `tfsdk:"scope"`
	Category    types.String `tfsdk:"category"`
	Label       types.String `tfsdk:"label"`
	Description types.String `tfsdk:"description"`
	Grantable   types.Bool   `tfsdk:"grantable"`
	Mintable    types.Bool   `tfsdk:"mintable"`
}

type permissionCatalogModel struct {
	Feature     types.String      `tfsdk:"feature"`
	Permissions []permissionModel `tfsdk:"permissions"`
}

func (d *permissionCatalogDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_permission_catalog"
}

func (d *permissionCatalogDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	str := func(desc string) schema.Attribute {
		return schema.StringAttribute{MarkdownDescription: desc, Computed: true}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "Every fine-grained permission key the platform honours, `<feature>-<level>-<scope>`. " +
			"The roles catalogue also holds role names that are not permission keys (`user`, `admin`, …).",
		Attributes: map[string]schema.Attribute{
			"feature": schema.StringAttribute{MarkdownDescription: "Only this feature's keys, for example `users`.", Optional: true},
			"permissions": schema.ListNestedAttribute{
				MarkdownDescription: "The keys, ordered by key.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"key":         str("The permission key, for example `users-read-global`."),
					"feature":     str("The feature it belongs to."),
					"level":       str("`read` or `admin`."),
					"scope":       str("`global` or `tenant`."),
					"category":    str("The group the portal shows it in."),
					"label":       str("Display name."),
					"description": str("What the key allows."),
					"grantable": schema.BoolAttribute{MarkdownDescription: "May be granted to a person " +
						"(`ataila_user_role_grant`). A key that is not grantable is still honoured for those who hold it.", Computed: true},
					"mintable": schema.BoolAttribute{MarkdownDescription: "May be carried by an API token.", Computed: true},
				}},
			},
		},
	}
}

func (d *permissionCatalogDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *permissionCatalogDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.data == nil {
		resp.Diagnostics.AddError("Provider not configured", "The ataila provider was not configured.")
		return
	}
	var cfg permissionCatalogModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	found, err := d.data.API.ListPermissions(ctx, cfg.Feature.ValueString())
	if err != nil {
		summary, detail := apiErrorText("the permission catalogue (GET /permissions)", err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	cfg.Permissions = make([]permissionModel, len(found))
	for i, p := range found {
		cfg.Permissions[i] = permissionModel{
			Key: types.StringValue(p.Key), Feature: types.StringValue(p.Feature),
			Level: types.StringValue(string(p.Level)), Scope: types.StringValue(string(p.Scope)),
			Category: types.StringValue(p.Category), Label: types.StringValue(p.Label),
			Description: types.StringValue(p.Description), Grantable: types.BoolValue(p.Grantable),
			Mintable: types.BoolValue(p.Mintable),
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
