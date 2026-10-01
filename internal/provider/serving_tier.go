// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"strings"

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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ataila/terraform-provider-ataila/internal/client"
)

var (
	_ resource.ResourceWithConfigure     = (*servingTierResource)(nil)
	_ resource.ResourceWithImportState   = (*servingTierResource)(nil)
	_ datasource.DataSourceWithConfigure = (*servingTiersDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*gatewayDataSource)(nil)
)

// NewServingTierResource is the factory for ataila_ai_serving_tier.
func NewServingTierResource() resource.Resource { return &servingTierResource{} }

// NewServingTiersDataSource is the factory for ataila_ai_serving_tiers.
func NewServingTiersDataSource() datasource.DataSource { return &servingTiersDataSource{} }

// NewGatewayDataSource is the factory for ataila_ai_gateway.
func NewGatewayDataSource() datasource.DataSource { return &gatewayDataSource{} }

// tierCore is what the tier resource and the tiers data source share.
type tierCore struct {
	Key             types.String   `tfsdk:"key"`
	PinnedModel     types.String   `tfsdk:"pinned_model"`
	Enabled         types.Bool     `tfsdk:"enabled"`
	Label           types.String   `tfsdk:"label"`
	Category        types.String   `tfsdk:"category"`
	Sort            types.Int64    `tfsdk:"sort"`
	Role            types.String   `tfsdk:"role"`
	Resolved        types.Bool     `tfsdk:"resolved"`
	ResolvedModel   types.String   `tfsdk:"resolved_model"`
	Source          types.String   `tfsdk:"source"`
	CandidateModels types.List     `tfsdk:"candidate_models"`
	CreatedAt       TimestampValue `tfsdk:"created_at"`
	UpdatedAt       TimestampValue `tfsdk:"updated_at"`
}

type tierModel struct {
	tierCore
	ID               types.String `tfsdk:"id"`
	AllowUnloadedPin types.Bool   `tfsdk:"allow_unloaded_pin"`
	Warnings         types.List   `tfsdk:"warnings"`
}

func (m *tierCore) fromAPI(t *client.ServingTier) {
	m.Key = types.StringValue(t.Key)
	m.PinnedModel = stringOrNull(t.PinnedModel)
	m.Enabled = types.BoolValue(t.Enabled)
	m.Label = types.StringValue(t.Label)
	m.Category = types.StringValue(t.Category)
	m.Sort = types.Int64Value(int64(t.Sort))
	m.Role = stringOrNull(t.Role)
	m.Resolved = types.BoolValue(t.Resolved)
	m.ResolvedModel = stringOrNull(t.ResolvedModel)
	m.Source = types.StringValue(string(t.Source))
	cands := make([]attr.Value, 0, len(t.CandidateModels))
	for _, c := range t.CandidateModels {
		cands = append(cands, types.StringValue(c))
	}
	m.CandidateModels = types.ListValueMust(types.StringType, cands)
	m.CreatedAt = NewTimestamp(t.CreatedAt)
	m.UpdatedAt = NewTimestamp(t.UpdatedAt)
}

var tierDocs = map[string]string{
	"key":              "The tier's name, for example `code`.",
	"pinned_model":     "The served model the tier is pinned to; null lets the platform auto-assign one.",
	"enabled":          "Whether clients can use the tier; `false` hides it from every client.",
	"label":            "Display name.",
	"category":         "The group the tier belongs to.",
	"sort":             "Display order.",
	"role":             "The tier's role, when it has one.",
	"resolved":         "A loaded model backs the tier right now.",
	"resolved_model":   "The served model backing it right now.",
	"source":           "`pin` (the pin is loaded and serves), `auto` (auto-assigned), `pin-offline` (pinned to a model that is not loaded, so the tier serves nothing) or `none`.",
	"candidate_models": "Every model loaded on the fleet right now: what `pinned_model` may be without `allow_unloaded_pin`.",
	"created_at":       "When the tier was created: RFC 3339 in UTC, compared as an instant.",
	"updated_at":       "The last change to the pin or the enabled flag (the creation time until the first change): RFC 3339 in UTC, compared as an instant.",
}

func (r *servingTierResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ai_serving_tier"
}

type servingTierResource struct {
	data *ProviderData
}

func (r *servingTierResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	d := tierDocs
	computed := func(name string) schema.StringAttribute {
		return schema.StringAttribute{MarkdownDescription: d[name], Computed: true}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "The pin and the enabled flag of one serving tier of the AI gateway. Tiers exist " +
			"only in the platform's catalogue: this resource manages an EXISTING tier (a key the catalogue does " +
			"not have is an error) and **destroying it only forgets it**: nothing is sent, the tier keeps its " +
			"last pin and flag. A pin changes what every client of the tier gets.\n\n" +
			"A `pinned_model` that is not loaded on the fleet is refused unless `allow_unloaded_pin = true`; the " +
			"tier then serves nothing until that model is loaded.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The tier's key.", Computed: true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"key": schema.StringAttribute{
				MarkdownDescription: d["key"] + " Changing it manages another tier (this one is forgotten).",
				Required:            true,
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 200)},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"pinned_model": schema.StringAttribute{MarkdownDescription: d["pinned_model"], Optional: true,
				Validators: []validator.String{stringvalidator.LengthBetween(1, 200)}},
			"enabled": schema.BoolAttribute{MarkdownDescription: d["enabled"] + " Defaults to `true`.",
				Optional: true, Computed: true, Default: booldefault.StaticBool(true)},
			"allow_unloaded_pin": schema.BoolAttribute{
				MarkdownDescription: "Accept a `pinned_model` that is not loaded. Sent with each change; not read back.",
				Optional:            true,
			},
			"label":          computed("label"),
			"category":       computed("category"),
			"sort":           schema.Int64Attribute{MarkdownDescription: d["sort"], Computed: true},
			"role":           computed("role"),
			"resolved":       schema.BoolAttribute{MarkdownDescription: d["resolved"], Computed: true},
			"resolved_model": computed("resolved_model"),
			"source":         computed("source"),
			"candidate_models": schema.ListAttribute{MarkdownDescription: d["candidate_models"],
				ElementType: types.StringType, Computed: true},
			"created_at": schema.StringAttribute{MarkdownDescription: d["created_at"],
				CustomType: TimestampType{}, Computed: true},
			"updated_at": schema.StringAttribute{MarkdownDescription: d["updated_at"],
				CustomType: TimestampType{}, Computed: true},
			"warnings": warningsSchema("What did not go as planned in the last change made through this " +
				"resource (for example a gateway that could not be reconciled at once)."),
		},
	}
}

func (r *servingTierResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *servingTierResource) put(ctx context.Context, plan *tierModel, diags *diag.Diagnostics) bool {
	if r.data == nil {
		diags.AddError("Provider not configured", "The ataila provider was not configured.")
		return false
	}
	key := plan.Key.ValueString()
	t, err := r.data.API.PutServingTier(ctx, key, optString(plan.PinnedModel), plan.Enabled.ValueBool(),
		plan.AllowUnloadedPin.ValueBool())
	if err != nil {
		var apiErr *client.APIError
		switch {
		case errors.As(err, &apiErr) && apiErr.Code() == "tier_not_found":
			diags.AddError("No such serving tier: "+key,
				"Tiers exist only in the platform's catalogue; this resource manages an existing one. "+
					"ataila_ai_serving_tiers lists them.\n\n"+apiErr.Detail())
		case errors.As(err, &apiErr) && apiErr.Code() == client.CodeModelNotLoaded:
			diags.AddError("The model is not loaded: "+plan.PinnedModel.ValueString(),
				"Pinning a model that is not loaded takes the tier offline. Pin one of the loaded models "+
					"(candidates below), or set allow_unloaded_pin = true to insist.\n\n"+apiErr.Detail())
		default:
			diags.Append(apiError("setting the serving tier "+key, err))
		}
		return false
	}
	w := warningsOf(t.Warnings)
	addWarnings(diags, "setting the serving tier "+key, &w)
	allow := plan.AllowUnloadedPin
	plan.fromAPI(t)
	plan.ID = plan.Key
	plan.AllowUnloadedPin = allow
	plan.Warnings = warningsValue(w)
	return true
}

func (r *servingTierResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan tierModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || !r.put(ctx, &plan, &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *servingTierResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if r.data == nil {
		resp.Diagnostics.AddError("Provider not configured", "The ataila provider was not configured.")
		return
	}
	var state tierModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	t, err := r.data.API.GetServingTier(ctx, state.Key.ValueString())
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.Append(apiError("reading the serving tier "+state.Key.ValueString(), err))
		return
	}
	state.fromAPI(t)
	state.ID = state.Key
	state.Warnings = keepWarnings(state.Warnings)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *servingTierResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan tierModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || !r.put(ctx, &plan, &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete forgets the tier: there is nothing to delete, and the tier keeps its
// last pin and flag.
func (r *servingTierResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}

func (r *servingTierResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	key := strings.TrimSpace(req.ID)
	if key == "" {
		resp.Diagnostics.AddError("Cannot import the serving tier", "Give the tier's key, for example code.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("key"), key)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), key)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("warnings"), warningsValue(nil))...)
}

// ── data sources ─────────────────────────────────────────────────────────────

type servingTiersDataSource struct {
	data *ProviderData
}

type servingTiersModel struct {
	Tiers []tierCore `tfsdk:"tiers"`
}

func (d *servingTiersDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ai_serving_tiers"
}

func (d *servingTiersDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	str := func(name string) dschema.Attribute {
		return dschema.StringAttribute{MarkdownDescription: tierDocs[name], Computed: true}
	}
	resp.Schema = dschema.Schema{
		MarkdownDescription: "Every serving tier of the AI gateway's catalogue, ordered by key, with how each " +
			"resolves right now. Read from the platform's own data: it works when the gateway is down, and is " +
			"empty on a platform whose catalogue is.",
		Attributes: map[string]dschema.Attribute{
			"tiers": dschema.ListNestedAttribute{
				MarkdownDescription: "The tiers.",
				Computed:            true,
				NestedObject: dschema.NestedAttributeObject{Attributes: map[string]dschema.Attribute{
					"key": str("key"), "pinned_model": str("pinned_model"),
					"enabled":        dschema.BoolAttribute{MarkdownDescription: tierDocs["enabled"], Computed: true},
					"label":          str("label"),
					"category":       str("category"),
					"sort":           dschema.Int64Attribute{MarkdownDescription: tierDocs["sort"], Computed: true},
					"role":           str("role"),
					"resolved":       dschema.BoolAttribute{MarkdownDescription: tierDocs["resolved"], Computed: true},
					"resolved_model": str("resolved_model"), "source": str("source"),
					"candidate_models": dschema.ListAttribute{MarkdownDescription: tierDocs["candidate_models"],
						ElementType: types.StringType, Computed: true},
					"created_at": dschema.StringAttribute{MarkdownDescription: tierDocs["created_at"],
						CustomType: TimestampType{}, Computed: true},
					"updated_at": dschema.StringAttribute{MarkdownDescription: tierDocs["updated_at"],
						CustomType: TimestampType{}, Computed: true},
				}},
			},
		},
	}
}

func (d *servingTiersDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *servingTiersDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.data == nil {
		resp.Diagnostics.AddError("Provider not configured", "The ataila provider was not configured.")
		return
	}
	found, err := d.data.API.ListServingTiers(ctx)
	if err != nil {
		summary, detail := apiErrorText("the serving tiers (GET /ai/gateway/tiers)", err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	state := servingTiersModel{Tiers: make([]tierCore, len(found))}
	for i := range found {
		state.Tiers[i].fromAPI(&found[i])
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

type gatewayDataSource struct {
	data *ProviderData
}

type gatewayModel struct {
	BaseURL types.String `tfsdk:"base_url"`
	Tiers   types.List   `tfsdk:"tiers"`
}

func (d *gatewayDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ai_gateway"
}

func (d *gatewayDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dschema.Schema{
		MarkdownDescription: "The platform's AI gateway: the OpenAI-compatible base URL clients call with " +
			"`Authorization: Bearer <key>`, and the serving-tier names. **Needs a platform with an AI " +
			"gateway**: without one the read fails with `gateway_not_configured`, never retried.",
		Attributes: map[string]dschema.Attribute{
			"base_url": dschema.StringAttribute{MarkdownDescription: "The base URL (ends in `/v1`).", Computed: true},
			"tiers": dschema.ListAttribute{MarkdownDescription: "Every serving tier name, sorted.",
				ElementType: types.StringType, Computed: true},
		},
	}
}

func (d *gatewayDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *gatewayDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.data == nil {
		resp.Diagnostics.AddError("Provider not configured", "The ataila provider was not configured.")
		return
	}
	g, err := d.data.API.GetGateway(ctx)
	if err != nil {
		if diag := gatewayUnavailable("reading the AI gateway (GET /ai/gateway)", err); diag != nil {
			resp.Diagnostics.Append(diag)
			return
		}
		summary, detail := apiErrorText("the AI gateway (GET /ai/gateway)", err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	state := gatewayModel{BaseURL: types.StringValue(g.BaseUrl), Tiers: stringList(ctx, g.Tiers, &resp.Diagnostics)}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
