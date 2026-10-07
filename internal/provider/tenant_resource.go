// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ataila/terraform-provider-ataila/internal/client"
)

var (
	_ resource.ResourceWithConfigure   = (*tenantResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*tenantResource)(nil)
	_ resource.ResourceWithImportState = (*tenantResource)(nil)
)

// SlugImportPrefix imports a tenant by its slug.
const SlugImportPrefix = "slug:"

// NewTenantResource is the factory for ataila_tenant.
func NewTenantResource() resource.Resource { return &tenantResource{} }

type tenantResource struct {
	data *ProviderData
}

// tenantModel is the state of ataila_tenant, of the ataila_tenant data source
// and of each element of the ataila_tenants data source.
type tenantModel struct {
	ID              types.String   `tfsdk:"id"`
	CustomerID      types.String   `tfsdk:"customer_id"`
	Slug            types.String   `tfsdk:"slug"`
	Name            types.String   `tfsdk:"name"`
	Description     types.String   `tfsdk:"description"`
	DefaultRouterID types.String   `tfsdk:"default_router_id"`
	IsPrimary       types.Bool     `tfsdk:"is_primary"`
	ProjectCount    types.Int64    `tfsdk:"project_count"`
	MemberCount     types.Int64    `tfsdk:"member_count"`
	CreatedAt       TimestampValue `tfsdk:"created_at"`
	UpdatedAt       TimestampValue `tfsdk:"updated_at"`
}

func (m *tenantModel) fromAPI(t *client.Tenant) {
	m.ID = types.StringValue(t.Id)
	m.CustomerID = stringOrNull(t.CustomerId)
	m.Slug = types.StringValue(t.Slug)
	m.Name = types.StringValue(t.Name)
	m.Description = stringOrNull(t.Description)
	m.DefaultRouterID = stringOrNull(t.DefaultRouterId)
	m.IsPrimary = types.BoolValue(t.IsPrimary)
	m.ProjectCount = types.Int64Value(int64(t.ProjectCount))
	m.MemberCount = types.Int64Value(int64(t.MemberCount))
	m.CreatedAt = NewTimestamp(t.CreatedAt)
	m.UpdatedAt = NewTimestamp(t.UpdatedAt)
}

func (m *tenantModel) ident() string {
	if m.Slug.ValueString() != "" {
		return fmt.Sprintf("%s (id %s)", m.Slug.ValueString(), m.ID.ValueString())
	}
	return "with id " + m.ID.ValueString()
}

var tenantDocs = map[string]string{
	"id": "Tenant id (a UUID), assigned by the platform.",
	"customer_id": "Id of the customer the tenant belongs to. **Frozen.** Null only for a legacy tenant " +
		"that no customer owns.",
	"slug": "Slug, `^[a-z][a-z0-9-]{1,29}$`, unique across the platform; it names the tenant's groups " +
		"and paths. **Frozen.**",
	"name":        "Display name, 2-120 characters.",
	"description": "Description, up to 2000 characters.",
	"default_router_id": "Id of the tenant's default VPN router. Omit it to leave the platform's choice " +
		"(a customer's primary tenant gets the hub router; a new tenant gets none). Once set it can be " +
		"changed but not cleared from configuration.",
	"is_primary": "Whether this is a customer's primary tenant, created with the customer. A primary " +
		"tenant is never deleted.",
	"project_count": "Projects in the tenant, every status included.",
	"member_count":  "Tenant memberships.",
	"created_at":    "When the tenant was created: RFC 3339 in UTC, compared as an instant.",
	"updated_at": "When the tenant was last changed; equal to `created_at` until the first change. " +
		"RFC 3339 in UTC, compared as an instant.",
}

var (
	rxIntID = regexp.MustCompile(`^[1-9][0-9]{0,8}$`)
	rxSlug  = regexp.MustCompile(`^[a-z][a-z0-9-]{1,29}$`)
)

func (r *tenantResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_tenant"
}

func (r *tenantResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	d := tenantDocs
	state := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A tenant of a customer. Every customer has a primary tenant, created with it " +
			"(`ataila_customer.primary_tenant_id`); this resource adds further ones.\n\n" +
			"**Frozen keys** (`customer_id`, `slug`) are set at create and never change. Changing one " +
			"**fails the plan**; the provider never replaces a tenant.\n\n" +
			"**Destroy deletes** the tenant, only when it is empty (no project of any status, no contract, " +
			"no helpdesk record, no attributed resource) and not a customer's primary. It needs " +
			"`allow_destroy = true` on the provider **and** a token minted with destroy allowed. Its " +
			"memberships go with it.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: d["id"], Computed: true, PlanModifiers: state,
			},
			"customer_id": schema.StringAttribute{
				MarkdownDescription: d["customer_id"],
				Required:            true,
				Validators:          []validator.String{stringvalidator.RegexMatches(rxIntID, "must be a customer id")},
				PlanModifiers:       []planmodifier.String{tenantFrozen.forString()},
			},
			"slug": schema.StringAttribute{
				MarkdownDescription: d["slug"],
				Required:            true,
				Validators: []validator.String{stringvalidator.RegexMatches(rxSlug,
					"must be a lower-case letter followed by 1-29 lower-case letters, digits or hyphens")},
				PlanModifiers: []planmodifier.String{tenantFrozen.forString()},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: d["name"],
				Required:            true,
				Validators:          []validator.String{stringvalidator.LengthBetween(2, 120)},
			},
			"description": schema.StringAttribute{
				MarkdownDescription: d["description"],
				Optional:            true,
				Validators:          []validator.String{stringvalidator.LengthAtMost(2000)},
			},
			"default_router_id": schema.StringAttribute{
				MarkdownDescription: d["default_router_id"],
				Optional:            true,
				Computed:            true,
				Validators:          []validator.String{stringvalidator.RegexMatches(rxIntID, "must be a router id")},
				PlanModifiers:       state,
			},
			"is_primary": schema.BoolAttribute{
				MarkdownDescription: d["is_primary"],
				Computed:            true,
				PlanModifiers:       []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"project_count": schema.Int64Attribute{MarkdownDescription: d["project_count"], Computed: true},
			"member_count":  schema.Int64Attribute{MarkdownDescription: d["member_count"], Computed: true},
			"created_at": schema.StringAttribute{
				MarkdownDescription: d["created_at"], CustomType: TimestampType{}, Computed: true, PlanModifiers: state,
			},
			"updated_at": schema.StringAttribute{
				MarkdownDescription: d["updated_at"], CustomType: TimestampType{}, Computed: true,
			},
		},
	}
}

func (r *tenantResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *tenantResource) configured(diags interface{ AddError(string, string) }) bool {
	if r.data == nil {
		diags.AddError("Provider not configured", "The ataila provider was not configured.")
		return false
	}
	return true
}

func (r *tenantResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || !req.Plan.Raw.IsNull() {
		return // create or update: the frozen keys are the attributes' own plan modifiers
	}
	var state tenantModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if !resp.Diagnostics.HasError() && r.data != nil && !r.data.AllowDestroy {
		resp.Diagnostics.Append(destroyRefused("ataila_tenant", "tenant", state.ident()))
	}
}

func (r *tenantResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan tenantModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	t, err := r.data.API.CreateTenant(ctx, client.TenantCreate{
		CustomerId:      plan.CustomerID.ValueString(),
		Name:            plan.Name.ValueString(),
		Slug:            plan.Slug.ValueString(),
		Description:     optString(plan.Description),
		DefaultRouterId: optString(plan.DefaultRouterID),
	})
	if err != nil {
		resp.Diagnostics.Append(apiError("creating the tenant "+plan.Slug.ValueString(), err))
		return
	}
	plan.fromAPI(t)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *tenantResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var state tenantModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	t, err := r.data.API.GetTenant(ctx, state.ID.ValueString())
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.Append(apiError("reading the tenant "+state.ident(), err))
		return
	}
	state.fromAPI(t)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *tenantResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan, state tenantModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantFrozen.check(path.Root("customer_id"), state.CustomerID, plan.CustomerID, true, &resp.Diagnostics)
	tenantFrozen.check(path.Root("slug"), state.Slug, plan.Slug, true, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	patch := client.Patch{}
	if !plan.Name.Equal(state.Name) {
		patch["name"] = plan.Name.ValueString()
	}
	if !plan.Description.Equal(state.Description) {
		patch["description"] = nullable(plan.Description)
	}
	if !plan.DefaultRouterID.IsUnknown() && !plan.DefaultRouterID.Equal(state.DefaultRouterID) {
		patch["default_router_id"] = nullable(plan.DefaultRouterID)
	}
	var (
		t   *client.Tenant
		err error
	)
	if len(patch) == 0 {
		t, err = r.data.API.GetTenant(ctx, state.ID.ValueString())
	} else {
		t, err = r.data.API.UpdateTenant(ctx, state.ID.ValueString(), patch)
	}
	if err != nil {
		resp.Diagnostics.Append(apiError("changing the tenant "+state.ident(), err))
		return
	}
	plan.fromAPI(t)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *tenantResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var state tenantModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !r.data.AllowDestroy {
		resp.Diagnostics.Append(destroyRefused("ataila_tenant", "tenant", state.ident()))
		return
	}
	err := r.data.API.DeleteTenant(ctx, state.ID.ValueString())
	if err != nil && !isNotFound(err) {
		resp.Diagnostics.Append(destroyError("ataila_tenant", "tenant", state.ident(), err))
	}
}

func (r *tenantResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	id := strings.TrimSpace(req.ID)
	if slug, ok := strings.CutPrefix(id, SlugImportPrefix); ok {
		found, err := r.data.API.ListTenants(ctx, client.TenantFilter{Slug: slug})
		if err != nil {
			resp.Diagnostics.Append(apiError("looking up the tenant with slug "+slug, err))
			return
		}
		if len(found) != 1 {
			resp.Diagnostics.AddError("Cannot import the tenant",
				fmt.Sprintf("%d tenants have slug %q; the import needs exactly one.", len(found), slug))
			return
		}
		id = found[0].Id
	}
	if id == "" {
		resp.Diagnostics.AddError("Cannot import the tenant", "Give the tenant id, or slug:<slug>.")
		return
	}
	// A legacy tenant that no customer owns can be read, never managed:
	// customer_id is required and frozen here.
	t, err := r.data.API.GetTenant(ctx, id)
	if err != nil {
		if isNotFound(err) {
			resp.Diagnostics.AddError("Cannot import the tenant", fmt.Sprintf("No tenant has id %q.", id))
			return
		}
		resp.Diagnostics.Append(apiError("reading the tenant "+id, err))
		return
	}
	if t.CustomerId == nil {
		resp.Diagnostics.AddError("Cannot import a tenant that belongs to no customer",
			fmt.Sprintf("The tenant %s (id %s) is a legacy tenant: no customer owns it, so its customer_id is "+
				"null. The ataila_tenant resource manages only tenants of a customer, because customer_id is "+
				"required and can never change.\n\nRead it with the ataila_tenant data source (by id or "+
				"slug) or the ataila_tenants data source instead.", t.Slug, t.Id))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), t.Id)...)
}
