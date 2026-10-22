// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ataila/terraform-provider-ataila/internal/client"
)

// Service accounts (platform 1.0.397 on; PLAN §10 Wave 2 R2): an automation
// identity — a name, an owner customer and roles. The resource NEVER issues a
// token: a service account's token is minted in the portal or exchanged from a
// GitLab CI ID token (ci_jwt), and its GitLab CI links are read-only here.

var (
	_ resource.ResourceWithConfigure            = (*serviceAccountResource)(nil)
	_ resource.ResourceWithModifyPlan           = (*serviceAccountResource)(nil)
	_ resource.ResourceWithImportState          = (*serviceAccountResource)(nil)
	_ datasource.DataSourceWithConfigure        = (*serviceAccountDataSource)(nil)
	_ datasource.DataSourceWithConfigValidators = (*serviceAccountDataSource)(nil)
)

// NameImportPrefix imports a service account by its name.
const NameImportPrefix = "name:"

// rxServiceAccountName is the platform's rule (api_tokens/service.py _SA_NAME).
var rxServiceAccountName = regexp.MustCompile(`^[a-z][a-z0-9-]{1,46}[a-z0-9]$`)

var ciLinkTypes = map[string]attr.Type{
	"id":                  types.StringType,
	"issuer":              types.StringType,
	"project_path":        types.StringType,
	"scopes":              types.ListType{ElemType: types.StringType},
	"allow_destroy":       types.BoolType,
	"protected_refs_only": types.BoolType,
}

// ciLinkDataTypes add what changes with every exchange, which a data source
// may show and a resource's state should not.
var ciLinkDataTypes = func() map[string]attr.Type {
	m := map[string]attr.Type{"live_tokens": types.Int64Type}
	for k, v := range ciLinkTypes {
		m[k] = v
	}
	return m
}()

var serviceAccountDocs = map[string]string{
	"id":   "The account's user id (a UUID), assigned by the platform: `principal.id` in `ataila_whoami` for its tokens.",
	"name": "3-48 characters: lower-case letters, digits and `-`, starting with a letter, not ending in `-`. Unique among service accounts; it also names the account's synthetic e-mail address. Changing it renames the account.",
	"owner_customer_id": "Id of the customer this automation identity belongs to; omit it (or set null) for the " +
		"platform operator's own.",
	"owner_customer_name": "The owner customer's short name; null without an owner.",
	"roles": "The permission keys the account holds, the whole set; its tokens carry at most these. Omit it to " +
		"leave the roles to the portal. Never `admin` or a privileged role. A key newly added must be grantable and " +
		"carried by the provider's own token (a token never hands out more than it holds), and no token changes its " +
		"own account's roles.",
	"is_active": "`true` (the default). `false` deactivates the account, exactly like destroying the resource, and " +
		"needs the same two switches; every token of it stops at once. Setting it back to `true` re-activates it " +
		"with its tokens, roles and links.",
	"email":         "The account's synthetic e-mail address, which receives no mail.",
	"active_tokens": "How many of its tokens are live now (never a token itself).",
	"ci_links": "The GitLab projects whose pipelines may exchange their ID token for a one-hour token of this " +
		"account (`ci_jwt`), by issuer and path. Read-only: an operator links and unlinks a project in the portal.",
	"created_at": "When the account was created: RFC 3339 in UTC, compared as an instant.",
	"updated_at": "When it last changed: RFC 3339 in UTC, compared as an instant.",
	"warnings": "What did not go as planned in the last create or change made through this resource. Also " +
		"reported as warnings when it happens.",
}

var ciLinkDocs = map[string]string{
	"id":                  "The link's id.",
	"issuer":              "The GitLab whose ID tokens the link accepts (its address).",
	"project_path":        "The GitLab project, as its ID tokens name it (`group/project`).",
	"scopes":              "The scopes every exchanged token gets, less any the account no longer holds.",
	"allow_destroy":       "Exchanged tokens may archive, delete, deactivate and retire.",
	"protected_refs_only": "Only pipelines of protected branches and tags may exchange.",
	"live_tokens":         "Tokens exchanged through this link that still work.",
}

func ciLinksValue(links []client.ServiceAccountCiLink, live bool) types.List {
	t := ciLinkTypes
	if live {
		t = ciLinkDataTypes
	}
	elems := make([]attr.Value, 0, len(links))
	for _, l := range links {
		scopes := make([]attr.Value, 0, len(l.Scopes))
		for _, s := range l.Scopes {
			scopes = append(scopes, types.StringValue(s))
		}
		v := map[string]attr.Value{
			"id": types.StringValue(l.Id), "issuer": types.StringValue(l.Issuer),
			"project_path":        types.StringValue(l.ProjectPath),
			"scopes":              types.ListValueMust(types.StringType, scopes),
			"allow_destroy":       types.BoolValue(l.AllowDestroy),
			"protected_refs_only": types.BoolValue(l.ProtectedRefsOnly),
		}
		if live {
			v["live_tokens"] = types.Int64Value(int64(l.LiveTokens))
		}
		elems = append(elems, types.ObjectValueMust(t, v))
	}
	return types.ListValueMust(types.ObjectType{AttrTypes: t}, elems)
}

func rolesSet(roles []string) types.Set {
	sorted := append([]string{}, roles...)
	sort.Strings(sorted)
	elems := make([]attr.Value, 0, len(sorted))
	for _, r := range sorted {
		elems = append(elems, types.StringValue(r))
	}
	return types.SetValueMust(types.StringType, elems)
}

// ── ataila_service_account (resource) ────────────────────────────────────────

// NewServiceAccountResource is the factory for ataila_service_account.
func NewServiceAccountResource() resource.Resource { return &serviceAccountResource{} }

type serviceAccountResource struct {
	data *ProviderData
}

type serviceAccountModel struct {
	ID                types.String   `tfsdk:"id"`
	Name              types.String   `tfsdk:"name"`
	OwnerCustomerID   types.String   `tfsdk:"owner_customer_id"`
	OwnerCustomerName types.String   `tfsdk:"owner_customer_name"`
	Roles             types.Set      `tfsdk:"roles"`
	IsActive          types.Bool     `tfsdk:"is_active"`
	Email             types.String   `tfsdk:"email"`
	CILinks           types.List     `tfsdk:"ci_links"`
	CreatedAt         TimestampValue `tfsdk:"created_at"`
	UpdatedAt         TimestampValue `tfsdk:"updated_at"`
	Warnings          types.List     `tfsdk:"warnings"`
}

func (m *serviceAccountModel) fromAPI(sa *client.ServiceAccount) {
	m.ID = types.StringValue(sa.Id)
	m.Name = types.StringValue(sa.Name)
	m.OwnerCustomerID = stringOrNull(sa.OwnerCustomerId)
	m.OwnerCustomerName = stringOrNull(sa.OwnerCustomerName)
	m.Roles = rolesSet(sa.Roles)
	m.IsActive = types.BoolValue(sa.IsActive)
	m.Email = types.StringValue(sa.Email)
	m.CILinks = ciLinksValue(sa.CiLinks, false)
	m.CreatedAt = NewTimestamp(sa.CreatedAt)
	m.UpdatedAt = NewTimestampPointer(sa.UpdatedAt)
}

func (m *serviceAccountModel) ident() string {
	if n := m.Name.ValueString(); n != "" {
		return fmt.Sprintf("%s (id %s)", n, m.ID.ValueString())
	}
	return "with id " + m.ID.ValueString()
}

func (r *serviceAccountResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service_account"
}

func (r *serviceAccountResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	d := serviceAccountDocs
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A service account: a non-human identity for automation, **identity only** — a name, an " +
			"owner customer and roles. It cannot sign in, and this resource **never issues a token**: a token for it " +
			"is minted in the portal, or a GitLab pipeline exchanges its ID token for a one-hour one (`ci_jwt`) once " +
			"an operator has linked the project in the portal; `ci_links` shows those links, read-only.\n\n" +
			"Needs a token holding `service-accounts-admin-global` and platform release " +
			client.ReleaseServiceAccounts + " or later.\n\n" +
			"**Destroy deactivates** the account (every token of it stops at once) and needs `allow_destroy = true` " +
			"on the provider **and** a token minted with destroy allowed. A deactivated account keeps its name: " +
			"creating the same name again is refused; import it (`name:<name>`) and set `is_active = true` instead.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{MarkdownDescription: d["id"], Computed: true, PlanModifiers: keep},
			"name": schema.StringAttribute{
				MarkdownDescription: d["name"], Required: true,
				Validators: []validator.String{stringvalidator.RegexMatches(rxServiceAccountName,
					"must be 3-48 lower-case letters, digits and '-', starting with a letter, not ending in '-'")},
			},
			"owner_customer_id": schema.StringAttribute{
				MarkdownDescription: d["owner_customer_id"], Optional: true,
				Validators: []validator.String{stringvalidator.RegexMatches(rxIntID,
					"must be a customer id (1 to 999999999, no leading zero)")},
			},
			"owner_customer_name": schema.StringAttribute{MarkdownDescription: d["owner_customer_name"], Computed: true},
			"roles": schema.SetAttribute{
				MarkdownDescription: d["roles"], ElementType: types.StringType, Optional: true, Computed: true,
				PlanModifiers: []planmodifier.Set{setplanmodifier.UseStateForUnknown()},
				Validators: []validator.Set{setvalidator.ValueStringsAre(stringvalidator.RegexMatches(rxRoleName,
					"must be a permission key"))},
			},
			"is_active": schema.BoolAttribute{
				MarkdownDescription: d["is_active"], Optional: true, Computed: true,
				Default: booldefault.StaticBool(true),
			},
			"email":    schema.StringAttribute{MarkdownDescription: d["email"], Computed: true},
			"ci_links": resourceNestedList(d["ci_links"], ciLinkTypes, func(p []string) string { return ciLinkDocs[p[len(p)-1]] }, nil),
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

func (r *serviceAccountResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *serviceAccountResource) configured(diags interface{ AddError(string, string) }) bool {
	if r.data == nil {
		diags.AddError("Provider not configured", "The ataila provider was not configured.")
		return false
	}
	return true
}

func (r *serviceAccountResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	var state, plan serviceAccountModel
	if !req.State.Raw.IsNull() {
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	}
	if !req.Plan.Raw.IsNull() {
		resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	}
	if resp.Diagnostics.HasError() {
		return
	}
	if req.Plan.Raw.IsNull() { // destroy
		if r.data != nil && !r.data.AllowDestroy {
			resp.Diagnostics.Append(destroyRefused("ataila_service_account", "service account", state.ident()))
		}
		return
	}
	if d := r.data.featureRefused(client.FeatureServiceAccounts, path.Root("name"),
		"ataila_service_account"); d != nil {
		resp.Diagnostics.Append(d)
		return
	}
	switch {
	case req.State.Raw.IsNull(): // create
		if !plan.IsActive.IsUnknown() && !plan.IsActive.ValueBool() {
			resp.Diagnostics.AddAttributeError(path.Root("is_active"), "A new service account is created active",
				"is_active = false on an account that does not exist yet would create and deactivate it at once. "+
					"Create it with is_active = true (the default); to manage an existing deactivated account, import it.")
		}
	default: // update
		if !plan.IsActive.IsUnknown() && !plan.IsActive.ValueBool() && state.IsActive.ValueBool() &&
			r.data != nil && !r.data.AllowDestroy {
			d := destroyRefused("ataila_service_account", "service account", state.ident())
			resp.Diagnostics.AddAttributeError(path.Root("is_active"),
				"Setting is_active = false deactivates the service account", "Deactivating is a destroy.\n\n"+d.Detail())
		}
	}
}

func (r *serviceAccountResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan serviceAccountModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := client.ServiceAccountCreate{Name: plan.Name.ValueString(), OwnerCustomerId: optString(plan.OwnerCustomerID)}
	if !plan.Roles.IsUnknown() && !plan.Roles.IsNull() {
		roles := setStrings(ctx, plan.Roles)
		body.Roles = &roles
	}
	sa, err := r.data.API.CreateServiceAccount(ctx, body)
	if err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && apiErr.Code() == "service_account_name_taken" {
			resp.Diagnostics.AddError("A service account with this name exists",
				fmt.Sprintf("%s is taken, possibly by a deactivated account (deactivated accounts keep their name). "+
					"To manage that account, import it (tofu import / terraform import ataila_service_account.<name> "+
					"%s%s) and set is_active = true.\n\n%s",
					plan.Name.ValueString(), NameImportPrefix, plan.Name.ValueString(), apiErr.Detail()))
			return
		}
		resp.Diagnostics.Append(apiError("creating the service account "+plan.Name.ValueString(), err))
		return
	}
	addWarnings(&resp.Diagnostics, "creating the service account "+plan.Name.ValueString(), sa.Warnings)
	plan.fromAPI(sa)
	plan.Warnings = warningsValue(warningsOf(sa.Warnings))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *serviceAccountResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var state serviceAccountModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	sa, err := r.data.API.GetServiceAccount(ctx, state.ID.ValueString())
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.Append(apiError("reading the service account "+state.ident(), err))
		return
	}
	state.fromAPI(sa)
	state.Warnings = keepWarnings(state.Warnings)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *serviceAccountResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan, state serviceAccountModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	patch := client.Patch{}
	if !plan.Name.Equal(state.Name) {
		patch["name"] = plan.Name.ValueString()
	}
	if !plan.OwnerCustomerID.Equal(state.OwnerCustomerID) {
		patch["owner_customer_id"] = nullable(plan.OwnerCustomerID)
	}
	if !plan.Roles.IsUnknown() && !plan.Roles.Equal(state.Roles) {
		patch["roles"] = setStrings(ctx, plan.Roles)
	}
	deactivating := !plan.IsActive.ValueBool() && state.IsActive.ValueBool()
	if !plan.IsActive.Equal(state.IsActive) {
		if deactivating && !r.data.AllowDestroy {
			resp.Diagnostics.Append(destroyRefused("ataila_service_account", "service account", state.ident()))
			return
		}
		patch["is_active"] = plan.IsActive.ValueBool()
	}
	var (
		sa  *client.ServiceAccount
		err error
	)
	if len(patch) == 0 {
		sa, err = r.data.API.GetServiceAccount(ctx, state.ID.ValueString())
	} else {
		sa, err = r.data.API.UpdateServiceAccount(ctx, state.ID.ValueString(), patch)
	}
	if err != nil {
		if deactivating {
			resp.Diagnostics.Append(destroyError("ataila_service_account", "service account", state.ident(), err))
			return
		}
		resp.Diagnostics.Append(apiError("changing the service account "+state.ident(), err))
		return
	}
	addWarnings(&resp.Diagnostics, "changing the service account "+state.ident(), sa.Warnings)
	plan.fromAPI(sa)
	plan.Warnings = warningsValue(warningsOf(sa.Warnings))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *serviceAccountResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var state serviceAccountModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !r.data.AllowDestroy {
		resp.Diagnostics.Append(destroyRefused("ataila_service_account", "service account", state.ident()))
		return
	}
	err := r.data.API.DeactivateServiceAccount(ctx, state.ID.ValueString())
	if err != nil && !isNotFound(err) {
		resp.Diagnostics.Append(destroyError("ataila_service_account", "service account", state.ident(), err))
	}
}

func (r *serviceAccountResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	sa, diags := findServiceAccount(ctx, r.data.API, strings.TrimSpace(req.ID), "Cannot import the service account")
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), sa.Id)...)
}

// findServiceAccount looks an account up by id, or by name with the `name:`
// prefix (or a bare name, which is never a UUID).
func findServiceAccount(ctx context.Context, api *client.API, ref, summary string) (*client.ServiceAccount, diag.Diagnostics) {
	var diags diag.Diagnostics
	name, byName := strings.CutPrefix(ref, NameImportPrefix)
	// An id is a UUID, which the name rule would also accept when it starts
	// with a letter: a UUID is always an id.
	if !byName && !rxUUID.MatchString(ref) && rxServiceAccountName.MatchString(ref) {
		name, byName = ref, true
	}
	if ref == "" || (byName && name == "") {
		diags.AddError(summary, "Give the service account id, or name:<name>.")
		return nil, diags
	}
	if !byName {
		sa, err := api.GetServiceAccount(ctx, ref)
		if isNotFound(err) {
			diags.AddError(summary, fmt.Sprintf("No service account has the id %q.", ref))
			return nil, diags
		}
		if err != nil {
			diags.Append(apiError("reading the service account "+ref, err))
			return nil, diags
		}
		return sa, diags
	}
	all, err := api.ListServiceAccounts(ctx)
	if err != nil {
		diags.Append(apiError("listing the service accounts", err))
		return nil, diags
	}
	for i := range all {
		if all[i].Name == name {
			return &all[i], diags
		}
	}
	diags.AddError(summary, fmt.Sprintf("No service account is named %q.", name))
	return nil, diags
}

// ── ataila_service_account (data source) ─────────────────────────────────────

// NewServiceAccountDataSource is the factory for the ataila_service_account data source.
func NewServiceAccountDataSource() datasource.DataSource { return &serviceAccountDataSource{} }

type serviceAccountDataSource struct {
	data *ProviderData
}

type serviceAccountDataModel struct {
	ID                types.String   `tfsdk:"id"`
	Name              types.String   `tfsdk:"name"`
	OwnerCustomerID   types.String   `tfsdk:"owner_customer_id"`
	OwnerCustomerName types.String   `tfsdk:"owner_customer_name"`
	Roles             types.Set      `tfsdk:"roles"`
	IsActive          types.Bool     `tfsdk:"is_active"`
	Email             types.String   `tfsdk:"email"`
	ActiveTokens      types.Int64    `tfsdk:"active_tokens"`
	CILinks           types.List     `tfsdk:"ci_links"`
	CreatedAt         TimestampValue `tfsdk:"created_at"`
	UpdatedAt         TimestampValue `tfsdk:"updated_at"`
}

func (d *serviceAccountDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service_account"
}

func (d *serviceAccountDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	doc := serviceAccountDocs
	computed := func(name string) dschema.StringAttribute {
		return dschema.StringAttribute{MarkdownDescription: doc[name], Computed: true}
	}
	resp.Schema = dschema.Schema{
		MarkdownDescription: "One service account, by `id` or by `name`: its owner, roles, how many tokens are " +
			"live, and the GitLab projects linked to it. Never a token. Needs a token holding " +
			"`service-accounts-read-global` (or the admin key) and platform release " + client.ReleaseServiceAccounts +
			" or later.",
		Attributes: map[string]dschema.Attribute{
			"id": dschema.StringAttribute{MarkdownDescription: doc["id"] + " Set this or `name`.",
				Optional: true, Computed: true},
			"name": dschema.StringAttribute{MarkdownDescription: "The account's name. Set this or `id`.",
				Optional: true, Computed: true},
			"owner_customer_id":   computed("owner_customer_id"),
			"owner_customer_name": computed("owner_customer_name"),
			"roles": dschema.SetAttribute{MarkdownDescription: "The permission keys the account holds.",
				ElementType: types.StringType, Computed: true},
			"is_active":     dschema.BoolAttribute{MarkdownDescription: "False when the account is deactivated.", Computed: true},
			"email":         computed("email"),
			"active_tokens": dschema.Int64Attribute{MarkdownDescription: doc["active_tokens"], Computed: true},
			"ci_links": dataNestedList(doc["ci_links"], ciLinkDataTypes,
				func(p []string) string { return ciLinkDocs[p[len(p)-1]] }),
			"created_at": dschema.StringAttribute{MarkdownDescription: doc["created_at"], CustomType: TimestampType{},
				Computed: true},
			"updated_at": dschema.StringAttribute{MarkdownDescription: doc["updated_at"], CustomType: TimestampType{},
				Computed: true},
		},
	}
}

func (d *serviceAccountDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name")),
	}
}

func (d *serviceAccountDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *serviceAccountDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.data == nil {
		resp.Diagnostics.AddError("Provider not configured", "The ataila provider was not configured.")
		return
	}
	if diagnostic := d.data.featureRefused(client.FeatureServiceAccounts, path.Root("name"),
		"The ataila_service_account data source"); diagnostic != nil {
		resp.Diagnostics.Append(diagnostic)
		return
	}
	var cfg serviceAccountDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	ref := cfg.ID.ValueString()
	if ref == "" {
		ref = NameImportPrefix + cfg.Name.ValueString()
	}
	sa, diags := findServiceAccount(ctx, d.data.API, ref, "Cannot read the service account")
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	cfg.ID = types.StringValue(sa.Id)
	cfg.Name = types.StringValue(sa.Name)
	cfg.OwnerCustomerID = stringOrNull(sa.OwnerCustomerId)
	cfg.OwnerCustomerName = stringOrNull(sa.OwnerCustomerName)
	cfg.Roles = rolesSet(sa.Roles)
	cfg.IsActive = types.BoolValue(sa.IsActive)
	cfg.Email = types.StringValue(sa.Email)
	cfg.ActiveTokens = types.Int64Value(int64(sa.ActiveTokens))
	cfg.CILinks = ciLinksValue(sa.CiLinks, true)
	cfg.CreatedAt = NewTimestamp(sa.CreatedAt)
	cfg.UpdatedAt = NewTimestampPointer(sa.UpdatedAt)
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
