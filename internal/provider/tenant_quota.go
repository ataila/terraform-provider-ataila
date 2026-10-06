// Copyright (c) 2026 ATAILA Kft.
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/float64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/mapvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
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

// A tenant's quota set: per dimension a limit and a policy, set by the
// operator, against which the platform decides every order the tenant places
// (within the limit: approved by itself; over it: an operator's approval card;
// `hard_cap`: refused). The API replaces the WHOLE set with every PUT, and the
// resource owns the whole set the same way.

var (
	_ resource.ResourceWithConfigure     = (*tenantQuotaResource)(nil)
	_ resource.ResourceWithModifyPlan    = (*tenantQuotaResource)(nil)
	_ resource.ResourceWithImportState   = (*tenantQuotaResource)(nil)
	_ datasource.DataSourceWithConfigure = (*tenantQuotaDataSource)(nil)
)

// quotaDimensions are the platform's quota dimensions, in its order.
var quotaDimensions = []string{"vcpu", "ram_gb", "disk_gb", "desktops", "vms", "ai_tpm",
	"ai_budget_eur_month", "gpu"}

// quotaPolicies are the policies a limit may have.
var quotaPolicies = []string{"auto", "approve_always", "hard_cap"}

// quotaLimitMax is the largest limit the platform stores (NUMERIC(14,4)).
const quotaLimitMax = 9999999999

// quotaLimitEpsilon: the platform keeps four decimal places, so a configured
// limit and the stored one are the same limit when they differ by less than
// half of the fourth place.
const quotaLimitEpsilon = 0.00005

// NewTenantQuotaResource is the factory for ataila_tenant_quota.
func NewTenantQuotaResource() resource.Resource { return &tenantQuotaResource{} }

// NewTenantQuotaDataSource is the factory for the ataila_tenant_quota data source.
func NewTenantQuotaDataSource() datasource.DataSource { return &tenantQuotaDataSource{} }

type tenantQuotaResource struct {
	data *ProviderData
}

type tenantQuotaModel struct {
	ID         types.String `tfsdk:"id"`
	TenantID   types.String `tfsdk:"tenant_id"`
	TenantName types.String `tfsdk:"tenant_name"`
	Quota      types.Map    `tfsdk:"quota"`
	Usage      types.List   `tfsdk:"usage"`
	Notes      types.List   `tfsdk:"notes"`
	Warnings   types.List   `tfsdk:"warnings"`
}

// quotaEntryTypes is one entry of the resource's `quota` map.
var quotaEntryTypes = map[string]attr.Type{
	"limit":  types.Float64Type,
	"policy": types.StringType,
	"note":   types.StringType,
}

// quotaDataEntryTypes is one entry of the data source's `quota` map.
var quotaDataEntryTypes = map[string]attr.Type{
	"limit":  types.Float64Type,
	"policy": types.StringType,
	"note":   types.StringType,
	"set_by": types.StringType,
	"set_at": types.StringType,
}

// quotaUsageTypes is one element of `usage`.
var quotaUsageTypes = map[string]attr.Type{
	"dimension": types.StringType,
	"limit":     types.Float64Type,
	"policy":    types.StringType,
	"allocated": types.Float64Type,
	"reserved":  types.Float64Type,
	"remaining": types.Float64Type,
}

var tenantQuotaDocs = map[string]string{
	"id":          "The tenant's id: one quota set per tenant.",
	"tenant_id":   "The tenant whose quota set this is (`id` of an `ataila_tenant`, or a customer's `primary_tenant_id`).",
	"tenant_name": "The tenant's display name.",
	"limit": "The limit, `0` to `9999999999`, in the dimension's unit (`ai_tpm`: tokens per minute; " +
		"`ai_budget_eur_month`: EUR per month; `vcpu`, `ram_gb`, `disk_gb`, `desktops`, `vms`, `gpu`: counts " +
		"and gigabytes). The platform keeps four decimal places. `0` allows nothing.",
	"policy": "`auto` (an order within the limit is approved by itself, over it goes to an operator's approval " +
		"card), `approve_always` (every order touching the dimension goes to a card) or `hard_cap` (an order " +
		"over the limit is refused when it is placed).",
	"note":   "A note for operators, up to 500 characters.",
	"set_by": "The user who last changed the limit (`id` of an `ataila_user`); null when that user no longer exists.",
	"set_at": "When the limit was last changed: RFC 3339 in UTC.",
	"usage": "Every dimension, with or without a limit, next to what the tenant holds: `limit` and `policy` " +
		"(null without a limit), `allocated` (what the tenant holds now; null when the platform cannot count " +
		"it, and then every order touching it goes to a card), `reserved` (what approved orders not yet " +
		"delivered hold) and `remaining`. Read from the platform at every refresh; it moves as the tenant " +
		"orders and uses capacity.",
	"notes": "Caveats about the counts in `usage`, for a person: why a figure is missing or estimated.",
}

const quotaDimensionsDoc = "`vcpu`, `ram_gb`, `disk_gb`, `desktops`, `vms`, `ai_tpm` (AI gateway tokens per " +
	"minute: the sum of the tenant's key limits), `ai_budget_eur_month` (the tenant's monthly AI budget in EUR) " +
	"and `gpu` (dedicated GPU cards; shown, never ordered)"

const tenantQuotaResourceDoc = "A tenant's **whole** quota set: per dimension a limit and its policy, against " +
	"which the platform decides every order the tenant places from the catalogue. Dimensions: " +
	quotaDimensionsDoc + ".\n\n" +
	"**This resource owns the tenant's entire quota set.** Every apply replaces the set on the platform with " +
	"exactly the `quota` entries in the configuration (`PUT /tenants/{id}/quotas`): a dimension that has a " +
	"limit on the platform but is **not** in `quota` **loses its limit** — no limit, and every order touching " +
	"that dimension then goes to an operator's approval card (never to automatic approval). The plan shows " +
	"such a dimension as removed from `quota`, and warns. Do not manage one tenant's quotas both here and on " +
	"the portal's quotas page: the next apply puts back what the configuration says.\n\n" +
	"**Destroy removes every limit** of the tenant (an empty set: every order goes to an approval card). It " +
	"is not gated by `allow_destroy`: the platform treats it as a quota change, not a removal. To stop " +
	"managing the set without changing it, remove the resource from the state instead (`tofu state rm` / " +
	"`terraform state rm`, or a `removed` block with `destroy = false`).\n\n" +
	"Changing `tenant_id` manages another tenant's set: the old tenant's limits are removed, the new tenant's " +
	"are set. Needs a token holding `orders-admin-global` (reading needs `orders-read-global`). Needs platform " +
	"release " + client.ReleaseQuotasOrders + " or later: on an older platform the configuration is refused at " +
	"plan time, before any request. A quota is never raised by approving an order; it changes here and on the " +
	"quotas page only."

func (r *tenantQuotaResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_tenant_quota"
}

func (r *tenantQuotaResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	d := tenantQuotaDocs
	resp.Schema = schema.Schema{
		MarkdownDescription: tenantQuotaResourceDoc,
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: d["id"], Computed: true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"tenant_id": schema.StringAttribute{
				MarkdownDescription: d["tenant_id"] + " Changing it manages another tenant's set (this one's " +
					"limits are removed).",
				Required:      true,
				Validators:    []validator.String{stringvalidator.RegexMatches(rxUUID, "must be a tenant id")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"tenant_name": schema.StringAttribute{
				MarkdownDescription: d["tenant_name"], Computed: true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"quota": schema.MapNestedAttribute{
				MarkdownDescription: "The limits, by dimension (" + quotaDimensionsDoc + "). The WHOLE set: a " +
					"dimension left out has no limit on the platform after the apply. `{}` removes every limit.",
				Required:   true,
				Validators: []validator.Map{mapvalidator.KeysAre(stringvalidator.OneOf(quotaDimensions...))},
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"limit": schema.Float64Attribute{
						MarkdownDescription: d["limit"],
						Required:            true,
						Validators:          []validator.Float64{float64validator.Between(0, quotaLimitMax)},
					},
					"policy": schema.StringAttribute{
						MarkdownDescription: d["policy"] + " Defaults to `auto`.",
						Optional:            true,
						Computed:            true,
						Default:             stringdefault.StaticString("auto"),
						Validators:          []validator.String{stringvalidator.OneOf(quotaPolicies...)},
					},
					"note": schema.StringAttribute{
						MarkdownDescription: d["note"],
						Optional:            true,
						Validators:          []validator.String{stringvalidator.LengthAtMost(500)},
					},
				}},
			},
			"usage": resourceNestedList(d["usage"], quotaUsageTypes, quotaUsageDoc, nil),
			"notes": schema.ListAttribute{MarkdownDescription: d["notes"], Computed: true,
				ElementType: types.StringType},
			"warnings": warningsSchema("What did not go as planned in the last change made through this " +
				"resource (for example the change was made but its audit row was not written)."),
		},
	}
}

func quotaUsageDoc(p []string) string {
	if len(p) == 1 && p[0] == "dimension" {
		return "The dimension: " + quotaDimensionsDoc + "."
	}
	return client.Describe("TenantQuotaUsage", p...)
}

func (r *tenantQuotaResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *tenantQuotaResource) configured(diags *diag.Diagnostics) bool {
	if r.data == nil {
		diags.AddError("Provider not configured", "The ataila provider was not configured.")
		return false
	}
	return true
}

// ModifyPlan refuses the resource on a platform that does not serve tenant
// quotas, before any request, and warns about every dimension whose limit the
// apply removes.
func (r *tenantQuotaResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return // destroy: the tenant's limits are removed (documented); nothing to check
	}
	if d := r.data.featureRefused(client.FeatureTenantQuota, path.Root("quota"), "ataila_tenant_quota"); d != nil {
		resp.Diagnostics.Append(d)
		return
	}
	if req.State.Raw.IsNull() {
		return
	}
	var plan, state tenantQuotaModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || plan.Quota.IsUnknown() || !plan.TenantID.Equal(state.TenantID) {
		return
	}
	if gone := removedDimensions(state.Quota, plan.Quota); len(gone) > 0 {
		resp.Diagnostics.AddAttributeWarning(path.Root("quota"),
			"The apply removes limits: "+strings.Join(gone, ", "),
			fmt.Sprintf("The tenant %s has a limit on %s on the platform, and the configuration does not: this "+
				"resource owns the whole quota set, so the apply removes it. Without a limit, every order touching "+
				"the dimension goes to an operator's approval card. Add the dimension to quota to keep its limit.",
				state.TenantID.ValueString(), strings.Join(gone, ", ")))
	}
}

// removedDimensions are the keys of state that plan does not have, sorted.
func removedDimensions(state, plan types.Map) []string {
	if state.IsNull() || state.IsUnknown() || plan.IsNull() || plan.IsUnknown() {
		return nil
	}
	have := plan.Elements()
	var gone []string
	for k := range state.Elements() {
		if _, ok := have[k]; !ok {
			gone = append(gone, k)
		}
	}
	sort.Strings(gone)
	return gone
}

// quotaLimitsFromPlan turns the planned `quota` map into the PUT body, in the
// platform's dimension order.
func quotaLimitsFromPlan(m types.Map) []client.QuotaLimit {
	out := []client.QuotaLimit{}
	elems := m.Elements()
	for _, dim := range quotaDimensions {
		v, ok := elems[dim].(types.Object)
		if !ok || v.IsNull() || v.IsUnknown() {
			continue
		}
		a := v.Attributes()
		l := client.QuotaLimit{Dimension: dim, Policy: "auto"}
		if f, ok := a["limit"].(types.Float64); ok {
			l.Limit = f.ValueFloat64()
		}
		if p, ok := knownString(a["policy"]); ok && p != "" {
			l.Policy = p
		}
		if n, ok := a["note"].(types.String); ok && !n.IsNull() && !n.IsUnknown() {
			s := n.ValueString()
			l.Note = &s
		}
		out = append(out, l)
	}
	return out
}

// sameLimit reports whether two limits are one to the platform, which keeps
// four decimal places.
func sameLimit(a, b float64) bool { return math.Abs(a-b) < quotaLimitEpsilon }

// quotaMapValue is the resource's `quota` from the platform's rows. prior is
// the map the plan or state had: a limit the platform rounded keeps the
// prior's spelling, so the state holds what the configuration says.
func quotaMapValue(rows []client.TenantQuotaRow, prior types.Map) types.Map {
	priorElems := map[string]attr.Value{}
	if !prior.IsNull() && !prior.IsUnknown() {
		priorElems = prior.Elements()
	}
	elems := make(map[string]attr.Value, len(rows))
	for _, r := range rows {
		limit := types.Float64Value(r.Limit)
		if po, ok := priorElems[r.Dimension].(types.Object); ok && !po.IsNull() && !po.IsUnknown() {
			if pl, ok := po.Attributes()["limit"].(types.Float64); ok && !pl.IsNull() && !pl.IsUnknown() &&
				sameLimit(pl.ValueFloat64(), r.Limit) {
				limit = pl
			}
		}
		elems[r.Dimension] = types.ObjectValueMust(quotaEntryTypes, map[string]attr.Value{
			"limit": limit, "policy": types.StringValue(r.Policy), "note": stringOrNull(r.Note),
		})
	}
	return types.MapValueMust(types.ObjectType{AttrTypes: quotaEntryTypes}, elems)
}

// quotaDataMapValue is the data source's `quota`.
func quotaDataMapValue(rows []client.TenantQuotaRow) types.Map {
	elems := make(map[string]attr.Value, len(rows))
	for _, r := range rows {
		setAt := types.StringNull()
		if r.SetAt != nil {
			setAt = types.StringValue(FormatTimestamp(*r.SetAt))
		}
		elems[r.Dimension] = types.ObjectValueMust(quotaDataEntryTypes, map[string]attr.Value{
			"limit": types.Float64Value(r.Limit), "policy": types.StringValue(r.Policy),
			"note": stringOrNull(r.Note), "set_by": stringOrNull(r.SetBy), "set_at": setAt,
		})
	}
	return types.MapValueMust(types.ObjectType{AttrTypes: quotaDataEntryTypes}, elems)
}

func float64OrNull(p *float64) types.Float64 {
	if p == nil {
		return types.Float64Null()
	}
	return types.Float64Value(*p)
}

// quotaUsageValue is `usage`, in the platform's order.
func quotaUsageValue(rows []client.TenantQuotaUsageRow) types.List {
	ot := types.ObjectType{AttrTypes: quotaUsageTypes}
	elems := make([]attr.Value, 0, len(rows))
	for _, u := range rows {
		elems = append(elems, types.ObjectValueMust(quotaUsageTypes, map[string]attr.Value{
			"dimension": types.StringValue(u.Dimension), "limit": float64OrNull(u.Limit),
			"policy": stringOrNull(u.Policy), "allocated": float64OrNull(u.Allocated),
			"reserved": types.Float64Value(u.Reserved), "remaining": float64OrNull(u.Remaining),
		}))
	}
	return types.ListValueMust(ot, elems)
}

func quotaNotesValue(notes []string) types.List {
	elems := make([]attr.Value, 0, len(notes))
	for _, n := range notes {
		elems = append(elems, types.StringValue(n))
	}
	return types.ListValueMust(types.StringType, elems)
}

// fromAPI fills the model from the platform's answer; prior is the `quota`
// the plan or state had.
func (m *tenantQuotaModel) fromAPI(q *client.TenantQuotaSet, prior types.Map) {
	m.ID = types.StringValue(q.TenantID)
	// The platform answers the id in lower case; a configuration that spells it
	// otherwise keeps its spelling (it is the same tenant).
	if m.TenantID.IsNull() || m.TenantID.IsUnknown() || !strings.EqualFold(m.TenantID.ValueString(), q.TenantID) {
		m.TenantID = types.StringValue(q.TenantID)
	}
	m.TenantName = stringOrNull(q.TenantName)
	m.Quota = quotaMapValue(q.Quotas, prior)
	m.Usage = quotaUsageValue(q.Usage)
	m.Notes = quotaNotesValue(q.Notes)
}

// quotaRequestError explains a refused quota request.
func quotaRequestError(doing, tenantID string, err error) diag.Diagnostic {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		return apiError(doing, err)
	}
	switch {
	case apiErr.Code() == client.CodeTenantNotFound:
		return diag.NewErrorDiagnostic("No such tenant: "+tenantID,
			fmt.Sprintf("While %s: the platform has no tenant with this id. Use the id of an ataila_tenant, or "+
				"a customer's primary_tenant_id.\n\n%s", doing, apiErr.Detail()))
	case (apiErr.StatusCode == 404 && (apiErr.Code() == "" || apiErr.Code() == "not_found")) || apiErr.StatusCode == 405:
		return diag.NewErrorDiagnostic("This platform does not serve tenant quotas",
			fmt.Sprintf("While %s: the platform answered HTTP %d. Tenant quotas need platform release %s or "+
				"later.\n\n%s", doing, apiErr.StatusCode, client.ReleaseQuotasOrders, apiErr.Detail()))
	}
	return apiError(doing, err)
}

func (r *tenantQuotaResource) put(ctx context.Context, plan *tenantQuotaModel, diags *diag.Diagnostics) bool {
	tenantID := plan.TenantID.ValueString()
	doing := "replacing the quota set of the tenant " + tenantID
	q, err := r.data.API.PutTenantQuotas(ctx, tenantID, quotaLimitsFromPlan(plan.Quota))
	if err != nil {
		diags.Append(quotaRequestError(doing, tenantID, err))
		return false
	}
	addWarnings(diags, doing, &q.Warnings)
	plan.fromAPI(q, plan.Quota)
	plan.Warnings = warningsValue(q.Warnings)
	return true
}

func (r *tenantQuotaResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan tenantQuotaModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || !r.put(ctx, &plan, &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *tenantQuotaResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var state tenantQuotaModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := state.TenantID.ValueString()
	q, err := r.data.API.GetTenantQuotas(ctx, tenantID)
	if isNotFound(err) {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && apiErr.Code() == client.CodeTenantNotFound {
			resp.State.RemoveResource(ctx)
			return
		}
	}
	if err != nil {
		resp.Diagnostics.Append(quotaRequestError("reading the quota set of the tenant "+tenantID, tenantID, err))
		return
	}
	state.fromAPI(q, state.Quota)
	state.Warnings = keepWarnings(state.Warnings)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *tenantQuotaResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan tenantQuotaModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || !r.put(ctx, &plan, &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes every limit of the tenant (an empty set).
func (r *tenantQuotaResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var state tenantQuotaModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := state.TenantID.ValueString()
	doing := "removing every limit of the tenant " + tenantID
	q, err := r.data.API.PutTenantQuotas(ctx, tenantID, nil)
	if err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && apiErr.Code() == client.CodeTenantNotFound {
			return // the tenant is gone, and its quotas with it
		}
		resp.Diagnostics.Append(quotaRequestError(doing, tenantID, err))
		return
	}
	addWarnings(&resp.Diagnostics, doing, &q.Warnings)
}

func (r *tenantQuotaResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := strings.TrimSpace(req.ID)
	if !rxUUID.MatchString(id) {
		resp.Diagnostics.AddError("Cannot import the tenant quota set",
			fmt.Sprintf("Give the tenant's id (a UUID), not %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("tenant_id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("warnings"), warningsValue(nil))...)
}

// ── data source ──────────────────────────────────────────────────────────────

type tenantQuotaDataSource struct {
	data *ProviderData
}

type tenantQuotaDataModel struct {
	TenantID   types.String `tfsdk:"tenant_id"`
	TenantName types.String `tfsdk:"tenant_name"`
	Quota      types.Map    `tfsdk:"quota"`
	Usage      types.List   `tfsdk:"usage"`
	Notes      types.List   `tfsdk:"notes"`
}

func (d *tenantQuotaDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_tenant_quota"
}

func (d *tenantQuotaDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	doc := tenantQuotaDocs
	resp.Schema = dschema.Schema{
		MarkdownDescription: "A tenant's quota set: the limits the operator set, per dimension (" +
			quotaDimensionsDoc + "), and for every dimension what the tenant holds against them. Needs a token " +
			"holding `orders-read-global` (or `orders-admin-global`) and platform release " +
			client.ReleaseQuotasOrders + " or later.",
		Attributes: map[string]dschema.Attribute{
			"tenant_id": dschema.StringAttribute{
				MarkdownDescription: doc["tenant_id"],
				Required:            true,
				Validators:          []validator.String{stringvalidator.RegexMatches(rxUUID, "must be a tenant id")},
			},
			"tenant_name": dschema.StringAttribute{MarkdownDescription: doc["tenant_name"], Computed: true},
			"quota": dschema.MapNestedAttribute{
				MarkdownDescription: "The limits, by dimension; a dimension without a limit is not listed.",
				Computed:            true,
				NestedObject: dschema.NestedAttributeObject{Attributes: dataNestedAttrs(quotaDataEntryTypes,
					func(p []string) string { return doc[p[len(p)-1]] }, nil)},
			},
			"usage": dataNestedList(doc["usage"], quotaUsageTypes, quotaUsageDoc),
			"notes": dschema.ListAttribute{MarkdownDescription: doc["notes"], Computed: true,
				ElementType: types.StringType},
		},
	}
}

func (d *tenantQuotaDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *tenantQuotaDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.data == nil {
		resp.Diagnostics.AddError("Provider not configured", "The ataila provider was not configured.")
		return
	}
	var cfg tenantQuotaDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if diagnostic := d.data.featureRefused(client.FeatureTenantQuota, path.Root("tenant_id"), "The ataila_tenant_quota data source"); diagnostic != nil {
		resp.Diagnostics.Append(diagnostic)
		return
	}
	tenantID := cfg.TenantID.ValueString()
	q, err := d.data.API.GetTenantQuotas(ctx, tenantID)
	if err != nil {
		resp.Diagnostics.Append(quotaRequestError("reading the quota set of the tenant "+tenantID, tenantID, err))
		return
	}
	state := tenantQuotaDataModel{
		TenantID: types.StringValue(q.TenantID), TenantName: stringOrNull(q.TenantName),
		Quota: quotaDataMapValue(q.Quotas), Usage: quotaUsageValue(q.Usage), Notes: quotaNotesValue(q.Notes),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
