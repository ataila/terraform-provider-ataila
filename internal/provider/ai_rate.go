// Copyright (c) 2026 ATAILA Kft.
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ataila/terraform-provider-ataila/internal/client"
)

// The AI rate card (the platform's list rate per serving tier) and a tenant's
// rate plan (its own rates instead of the list), EUR per one million input and
// output tokens. Both are time-ranged on the platform: a change closes the
// rate in force the day before and starts the new one, so usage keeps the
// price of its day. The same rate again changes nothing (the API is
// idempotent), which is what makes a second apply a no-op.

var (
	_ resource.ResourceWithConfigure     = (*aiRateCardResource)(nil)
	_ resource.ResourceWithModifyPlan    = (*aiRateCardResource)(nil)
	_ resource.ResourceWithImportState   = (*aiRateCardResource)(nil)
	_ resource.ResourceWithConfigure     = (*aiRatePlanResource)(nil)
	_ resource.ResourceWithModifyPlan    = (*aiRatePlanResource)(nil)
	_ resource.ResourceWithImportState   = (*aiRatePlanResource)(nil)
	_ datasource.DataSourceWithConfigure = (*aiRateCardDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*aiRatePlanDataSource)(nil)
)

var (
	// rxTier is a serving tier as the platform names it.
	rxTier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	// rxDay is a billing day.
	rxDay = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
)

// rateMax is the largest rate the platform takes, EUR per 1M tokens.
const rateMax = 1000000

// rateEpsilon: the platform keeps six decimal places of a rate.
const rateEpsilon = 0.0000005

const (
	rateInDoc   = "EUR per one million input (prompt) tokens, `0` to `1000000`; the platform keeps six decimal places."
	rateOutDoc  = "EUR per one million output (completion) tokens, `0` to `1000000`; the platform keeps six decimal places."
	rateNoteDoc = "A note, 1-500 characters. Terraform owns it: leaving it out clears the note of the rate it " +
		"manages."
)

func sameRate(a, b float64) bool { return math.Abs(a-b) < rateEpsilon }

// keepRate is the prior value when the platform's is the same rate.
func keepRate(prior types.Float64, platform float64) types.Float64 {
	if !prior.IsNull() && !prior.IsUnknown() && sameRate(prior.ValueFloat64(), platform) {
		return prior
	}
	return types.Float64Value(platform)
}

// noteValue is a stored note as the state holds it: "" (cleared) is null.
func noteValue(n *string) types.String {
	if n == nil || *n == "" {
		return types.StringNull()
	}
	return types.StringValue(*n)
}

// noteToSend is what a configured note sends: Terraform owns the note, so a
// note left out clears it ("").
func noteToSend(v types.String) *string {
	s := ""
	if !v.IsNull() && !v.IsUnknown() {
		s = v.ValueString()
	}
	return &s
}

// ownedRate is the rate a resource manages for one tier: the scheduled change
// when there is one (the configuration's rate starts then), else the rate in
// force today. A rate in force today that already ends (valid_to set) with
// nothing scheduled after it is on its way out: none.
func ownedRate(current, upcoming *client.AIRate) *client.AIRate {
	if upcoming != nil {
		return upcoming
	}
	if current != nil && current.ValidTo != nil && *current.ValidTo != "" {
		return nil
	}
	return current
}

// rateWriteError explains a refused rate change.
func rateWriteError(doing string, err error) diag.Diagnostic {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code() {
		case "invalid_valid_from":
			return diag.NewErrorDiagnostic("valid_from is out of range",
				fmt.Sprintf("While %s: the platform takes a valid_from from 2020-01-01 to three years ahead.\n\n%s",
					doing, apiErr.Detail()))
		case "rate_changed_concurrently":
			return diag.NewErrorDiagnostic("Another change to the same rate landed at the same moment",
				fmt.Sprintf("While %s. Run the apply again: it reads the rate first.\n\n%s", doing, apiErr.Detail()))
		}
	}
	return aiBillingReadError(doing, err)
}

// ── ataila_ai_rate_card ──────────────────────────────────────────────────────

// NewAIRateCardResource is the factory for the ataila_ai_rate_card resource.
func NewAIRateCardResource() resource.Resource { return &aiRateCardResource{} }

type aiRateCardResource struct {
	data *ProviderData
}

type aiRateCardModel struct {
	ID        types.String  `tfsdk:"id"`
	Tier      types.String  `tfsdk:"tier"`
	EurIn     types.Float64 `tfsdk:"eur_per_1m_input"`
	EurOut    types.Float64 `tfsdk:"eur_per_1m_output"`
	ValidFrom types.String  `tfsdk:"valid_from"`
	ValidTo   types.String  `tfsdk:"valid_to"`
	Note      types.String  `tfsdk:"note"`
	Warnings  types.List    `tfsdk:"warnings"`
}

const rateCardResourceDoc = "The **list rate** of one serving tier of the AI gateway: what a tenant without its " +
	"own rate (`ataila_ai_rate_plan`) pays, EUR per one million input and output tokens. Usage is rated when " +
	"read, each day at the rate in force that day; a tier without a rate is not priced at all (never 0).\n\n" +
	"A change closes the rate in force the day before `valid_from` and starts the new one, so earlier usage " +
	"keeps its price. Leave `valid_from` out for \"from today\"; a past day re-prices the usage since then; a " +
	"future day schedules the change (the platform keeps one scheduled change per tier). The same rate again " +
	"changes nothing on the platform, so a second apply is a no-op.\n\n" +
	"**Destroying it only forgets it**: a list rate is never removed (the platform has no delete); the tier " +
	"keeps its last rate. Changing `tier` manages another tier. Needs a token holding `ai-gateway-admin-global` " +
	"and platform release " + client.ReleaseAIBilling + " or later (refused at plan time before it)."

func (r *aiRateCardResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ai_rate_card"
}

func (r *aiRateCardResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: rateCardResourceDoc,
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The tier.", Computed: true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"tier": schema.StringAttribute{
				MarkdownDescription: "The serving tier, for example `general`, `code` or `embed` " +
					"(`ataila_ai_serving_tiers`, or `ataila_ai_rate_card` for every tier the platform knows). " +
					"Changing it manages another tier; this one keeps its rate.",
				Required:      true,
				Validators:    []validator.String{stringvalidator.RegexMatches(rxTier, "must be a serving tier name")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"eur_per_1m_input": schema.Float64Attribute{
				MarkdownDescription: rateInDoc, Required: true,
				Validators: []validator.Float64{float64validator.Between(0, rateMax)},
			},
			"eur_per_1m_output": schema.Float64Attribute{
				MarkdownDescription: rateOutDoc, Required: true,
				Validators: []validator.Float64{float64validator.Between(0, rateMax)},
			},
			"valid_from": schema.StringAttribute{
				MarkdownDescription: "The first billing day (`YYYY-MM-DD`, the platform's billing time zone) the " +
					"rate applies. Left out: today, when the rate or note changes; the attribute then reads the " +
					"day the platform recorded.",
				Optional:   true,
				Computed:   true,
				Validators: []validator.String{stringvalidator.RegexMatches(rxDay, "must be YYYY-MM-DD")},
			},
			"valid_to": schema.StringAttribute{
				MarkdownDescription: "The last day of the rate when another one is already scheduled after it; " +
					"null while it is open.",
				Computed: true,
			},
			"note": schema.StringAttribute{
				MarkdownDescription: rateNoteDoc, Optional: true,
				Validators: []validator.String{stringvalidator.LengthBetween(1, 500)},
			},
			"warnings": warningsSchema("What did not go as planned in the last change made through this " +
				"resource (for example the change was made but its audit row was not written)."),
		},
	}
}

func (r *aiRateCardResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *aiRateCardResource) configured(diags *diag.Diagnostics) bool {
	if r.data == nil {
		diags.AddError("Provider not configured", "The ataila provider was not configured.")
		return false
	}
	return true
}

// ModifyPlan refuses the resource on a platform without rates, and plans
// `valid_from` when the configuration leaves it out: unchanged when nothing
// else changes, unknown (today, as the platform records it) when it does.
func (r *aiRateCardResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return // destroy forgets
	}
	if d := r.data.featureRefused(client.FeatureAIRates, path.Root("tier"), "ataila_ai_rate_card"); d != nil {
		resp.Diagnostics.Append(d)
		return
	}
	var cfgFrom types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("valid_from"), &cfgFrom)...)
	if resp.Diagnostics.HasError() || !cfgFrom.IsNull() {
		return
	}
	if req.State.Raw.IsNull() {
		return // create: computed
	}
	var plan, state aiRateCardModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	changed := plan.EurIn.IsUnknown() || plan.EurOut.IsUnknown() || plan.Note.IsUnknown() ||
		!plan.EurIn.Equal(state.EurIn) || !plan.EurOut.Equal(state.EurOut) || !plan.Note.Equal(state.Note)
	from, to := state.ValidFrom, state.ValidTo
	if changed {
		from, to = types.StringUnknown(), types.StringUnknown()
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("valid_from"), from)...)
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("valid_to"), to)...)
}

// fromRate fills the model from the rate the resource manages; the prior
// values keep their spelling where the platform holds the same.
func (m *aiRateCardModel) fromRate(rate *client.AIRate) {
	m.ID = types.StringValue(rate.Tier)
	m.Tier = types.StringValue(rate.Tier)
	m.EurIn = keepRate(m.EurIn, rate.EurPer1mInput)
	m.EurOut = keepRate(m.EurOut, rate.EurPer1mOutput)
	m.ValidFrom = types.StringValue(rate.ValidFrom)
	m.ValidTo = stringOrNull(rate.ValidTo)
	m.Note = noteValue(rate.Note)
}

func (r *aiRateCardResource) put(ctx context.Context, cfgFrom types.String, plan *aiRateCardModel, diags *diag.Diagnostics) bool {
	tier := plan.Tier.ValueString()
	doing := "setting the list rate of the tier " + tier
	from := ""
	if !cfgFrom.IsNull() && !cfgFrom.IsUnknown() {
		from = cfgFrom.ValueString()
	}
	out, err := r.data.API.PutAIRateCardTier(ctx, tier, plan.EurIn.ValueFloat64(), plan.EurOut.ValueFloat64(),
		from, noteToSend(plan.Note))
	if err != nil {
		diags.Append(rateWriteError(doing, err))
		return false
	}
	addWarnings(diags, doing, &out.Warnings)
	owned := ownedRate(out.Current, out.Upcoming)
	if owned == nil {
		diags.AddError("The platform did not keep the rate",
			fmt.Sprintf("While %s: the platform answered without a rate for the tier. Run the apply again.", doing))
		return false
	}
	plan.fromRate(owned)
	// A configured start is what the platform applied the rate from, whether it
	// started a row that day or the same rate already covered it.
	if from != "" {
		plan.ValidFrom = types.StringValue(from)
	}
	plan.Warnings = warningsValue(out.Warnings)
	return true
}

func (r *aiRateCardResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan aiRateCardModel
	var cfgFrom types.String
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("valid_from"), &cfgFrom)...)
	if resp.Diagnostics.HasError() || !r.put(ctx, cfgFrom, &plan, &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *aiRateCardResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var state aiRateCardModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tier := state.Tier.ValueString()
	t, err := r.data.API.GetAIRateCardTier(ctx, tier)
	if err != nil {
		resp.Diagnostics.Append(aiBillingReadError("reading the list rate of the tier "+tier, err))
		return
	}
	var owned *client.AIRate
	if t != nil {
		owned = ownedRate(t.Current, t.Upcoming)
	}
	if owned == nil {
		resp.State.RemoveResource(ctx) // no rate any more: the next plan sets it again
		return
	}
	priorFrom, priorIn, priorOut := state.ValidFrom, state.EurIn, state.EurOut
	state.fromRate(owned)
	// The same rate as the state's: the day it was recorded from stays (a
	// configured start the platform covered with a row that starts on another
	// day is the same price). A different rate is drift, and reads as it is.
	if !priorFrom.IsNull() && !priorFrom.IsUnknown() && !priorIn.IsNull() && !priorOut.IsNull() &&
		sameRate(priorIn.ValueFloat64(), owned.EurPer1mInput) && sameRate(priorOut.ValueFloat64(), owned.EurPer1mOutput) {
		state.ValidFrom = priorFrom
	}
	state.Warnings = keepWarnings(state.Warnings)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *aiRateCardResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan aiRateCardModel
	var cfgFrom types.String
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("valid_from"), &cfgFrom)...)
	if resp.Diagnostics.HasError() {
		return
	}
	planned := plan.ValidFrom
	if !r.put(ctx, cfgFrom, &plan, &resp.Diagnostics) {
		return
	}
	// Nothing to change but the note's spelling: the planned day stays.
	if !planned.IsUnknown() && !planned.IsNull() && cfgFrom.IsNull() {
		plan.ValidFrom = planned
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete forgets the tier's rate: the platform keeps it (a list rate is never
// removed).
func (r *aiRateCardResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}

func (r *aiRateCardResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	tier := strings.TrimSpace(req.ID)
	if !rxTier.MatchString(tier) {
		resp.Diagnostics.AddError("Cannot import the rate", fmt.Sprintf("Give the tier, for example general, not %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("tier"), tier)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), tier)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("warnings"), warningsValue(nil))...)
}

// ── ataila_ai_rate_plan ──────────────────────────────────────────────────────

// NewAIRatePlanResource is the factory for the ataila_ai_rate_plan resource.
func NewAIRatePlanResource() resource.Resource { return &aiRatePlanResource{} }

type aiRatePlanResource struct {
	data *ProviderData
}

type aiRatePlanModel struct {
	ID               types.String `tfsdk:"id"`
	TenantID         types.String `tfsdk:"tenant_id"`
	TenantName       types.String `tfsdk:"tenant_name"`
	Rates            types.Map    `tfsdk:"rates"`
	ValidFrom        types.String `tfsdk:"valid_from"`
	ContractIncluded types.Bool   `tfsdk:"contract_included"`
	Effective        types.List   `tfsdk:"effective"`
	Warnings         types.List   `tfsdk:"warnings"`
}

var planRateTypes = map[string]attr.Type{
	"eur_per_1m_input":  types.Float64Type,
	"eur_per_1m_output": types.Float64Type,
	"note":              types.StringType,
}

var effectiveRateSpec = []fieldSpec{
	{name: "tier", kind: fString}, {name: "basis", kind: fString},
	{name: "eur_per_1m_input", kind: fFloat}, {name: "eur_per_1m_output", kind: fFloat},
	{name: "valid_from", kind: fString},
	{name: "list_eur_per_1m_input", kind: fFloat}, {name: "list_eur_per_1m_output", kind: fFloat},
}

const ratePlanResourceDoc = "A tenant's **whole** AI rate plan: its own price per serving tier instead of the " +
	"list rate (`ataila_ai_rate_card`), EUR per one million input and output tokens.\n\n" +
	"**This resource owns the tenant's entire plan.** Every apply replaces the plan on the platform with " +
	"exactly the `rates` in the configuration (`PUT /tenants/{id}/ai-rate-plan`): a tier that has a rate in the " +
	"tenant's plan but is **not** in `rates` **goes back to the list rate** from `valid_from` (today when left " +
	"out). The plan shows such a tier as removed from `rates`, and warns. Earlier usage keeps the price of its " +
	"day: rates are time-ranged, never rewritten. The same plan again changes nothing on the platform, so a " +
	"second apply is a no-op.\n\n" +
	"**Destroy ends the plan today** (`DELETE`): every tier goes back to the list rate; the history stays. It is " +
	"not gated by `allow_destroy`: it changes a price, it removes nothing. Changing `tenant_id` manages another " +
	"tenant's plan (this one is ended). Needs a token holding `orders-admin-global` or " +
	"`ai-gateway-admin-global` and platform release " + client.ReleaseAIBilling + " or later (refused at plan " +
	"time before it)."

func (r *aiRatePlanResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ai_rate_plan"
}

func (r *aiRatePlanResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: ratePlanResourceDoc,
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The tenant's id: one plan per tenant.", Computed: true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"tenant_id": schema.StringAttribute{
				MarkdownDescription: "The tenant whose plan this is (`id` of an `ataila_tenant`). Changing it " +
					"manages another tenant's plan (this one is ended).",
				Required:      true,
				Validators:    []validator.String{stringvalidator.RegexMatches(rxUUID, "must be a tenant id")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"tenant_name": schema.StringAttribute{
				MarkdownDescription: "The tenant's display name.", Computed: true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"rates": schema.MapNestedAttribute{
				MarkdownDescription: "The tenant's rates, by serving tier. The WHOLE plan: a tier left out costs " +
					"the list rate after the apply. `{}` ends the plan.",
				Required: true,
				Validators: []validator.Map{mapvalidator.KeysAre(stringvalidator.RegexMatches(rxTier,
					"must be a serving tier name"))},
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"eur_per_1m_input": schema.Float64Attribute{
						MarkdownDescription: rateInDoc, Required: true,
						Validators: []validator.Float64{float64validator.Between(0, rateMax)},
					},
					"eur_per_1m_output": schema.Float64Attribute{
						MarkdownDescription: rateOutDoc, Required: true,
						Validators: []validator.Float64{float64validator.Between(0, rateMax)},
					},
					"note": schema.StringAttribute{
						MarkdownDescription: rateNoteDoc, Optional: true,
						Validators: []validator.String{stringvalidator.LengthBetween(1, 500)},
					},
				}},
			},
			"valid_from": schema.StringAttribute{
				MarkdownDescription: "The first billing day (`YYYY-MM-DD`) a change of the plan applies; today " +
					"when left out. A past day re-prices the usage since then (rating is at read time): set it to " +
					"the first of the month to price the whole month at the plan. Sent with each change; not read " +
					"back.",
				Optional:   true,
				Validators: []validator.String{stringvalidator.RegexMatches(rxDay, "must be YYYY-MM-DD")},
			},
			"contract_included": schema.BoolAttribute{
				MarkdownDescription: "True while an active contract includes the tenant's metered AI: it is " +
					"priced at 0 EUR, whatever the plan says.",
				Computed: true,
			},
			"effective": resourceNestedList("What each tier costs the tenant today and why: `basis` `plan` "+
				"(its own rate), `list`, `contract_included` or `none` (not priced), with the list rate beside it.",
				specTypes(effectiveRateSpec), specDoc(effectiveRateSpec, "AiEffectiveRate"), nil),
			"warnings": warningsSchema("What did not go as planned in the last change made through this " +
				"resource (for example the change was made but its audit row was not written)."),
		},
	}
}

func (r *aiRatePlanResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *aiRatePlanResource) configured(diags *diag.Diagnostics) bool {
	if r.data == nil {
		diags.AddError("Provider not configured", "The ataila provider was not configured.")
		return false
	}
	return true
}

func (r *aiRatePlanResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return // destroy: the plan ends (documented)
	}
	if d := r.data.featureRefused(client.FeatureAIRates, path.Root("rates"), "ataila_ai_rate_plan"); d != nil {
		resp.Diagnostics.Append(d)
		return
	}
	if req.State.Raw.IsNull() {
		return
	}
	var plan, state aiRatePlanModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || plan.Rates.IsUnknown() || !plan.TenantID.Equal(state.TenantID) {
		return
	}
	if gone := removedDimensions(state.Rates, plan.Rates); len(gone) > 0 {
		resp.Diagnostics.AddAttributeWarning(path.Root("rates"),
			"The apply returns tiers to the list rate: "+strings.Join(gone, ", "),
			fmt.Sprintf("The tenant %s has its own rate for %s, and the configuration does not: this resource owns "+
				"the whole plan, so the apply ends those rates and the tenant pays the list rate for them. Add the "+
				"tier to rates to keep its rate.", state.TenantID.ValueString(), strings.Join(gone, ", ")))
	}
}

// planRatesValue is `rates` from the platform's plan: per tier the rate it
// manages (a scheduled change, else the rate in force today; a rate already
// ending with nothing after it is leaving the plan). prior keeps spellings.
func planRatesValue(p *client.AIRatePlan, prior types.Map) types.Map {
	priorElems := map[string]attr.Value{}
	if !prior.IsNull() && !prior.IsUnknown() {
		priorElems = prior.Elements()
	}
	current := map[string]*client.AIRate{}
	upcoming := map[string]*client.AIRate{}
	for i := range p.Rates {
		current[p.Rates[i].Tier] = &p.Rates[i]
	}
	for i := range p.Upcoming {
		upcoming[p.Upcoming[i].Tier] = &p.Upcoming[i]
	}
	tiers := map[string]bool{}
	for t := range current {
		tiers[t] = true
	}
	for t := range upcoming {
		tiers[t] = true
	}
	elems := map[string]attr.Value{}
	for t := range tiers {
		owned := ownedRate(current[t], upcoming[t])
		if owned == nil {
			continue
		}
		pin, pout := types.Float64Null(), types.Float64Null()
		if po, ok := priorElems[t].(types.Object); ok && !po.IsNull() && !po.IsUnknown() {
			pin, _ = po.Attributes()["eur_per_1m_input"].(types.Float64)
			pout, _ = po.Attributes()["eur_per_1m_output"].(types.Float64)
		}
		elems[t] = types.ObjectValueMust(planRateTypes, map[string]attr.Value{
			"eur_per_1m_input":  keepRate(pin, owned.EurPer1mInput),
			"eur_per_1m_output": keepRate(pout, owned.EurPer1mOutput),
			"note":              noteValue(owned.Note),
		})
	}
	return types.MapValueMust(types.ObjectType{AttrTypes: planRateTypes}, elems)
}

func (m *aiRatePlanModel) fromAPI(p *client.AIRatePlan, prior types.Map) {
	m.ID = types.StringValue(p.TenantID)
	if m.TenantID.IsNull() || m.TenantID.IsUnknown() || !strings.EqualFold(m.TenantID.ValueString(), p.TenantID) {
		m.TenantID = types.StringValue(p.TenantID)
	}
	m.TenantName = stringOrNull(p.TenantName)
	m.Rates = planRatesValue(p, prior)
	m.ContractIncluded = types.BoolValue(p.ContractIncluded)
	m.Effective = listValue(effectiveRateSpec, p.Effective)
}

// planRatesFromModel is the PUT body's rates, by tier name.
func planRatesFromModel(m types.Map) []client.AIRatePlanRate {
	elems := m.Elements()
	tiers := make([]string, 0, len(elems))
	for t := range elems {
		tiers = append(tiers, t)
	}
	sort.Strings(tiers)
	out := []client.AIRatePlanRate{}
	for _, t := range tiers {
		o, ok := elems[t].(types.Object)
		if !ok || o.IsNull() || o.IsUnknown() {
			continue
		}
		a := o.Attributes()
		rate := client.AIRatePlanRate{Tier: t}
		if f, ok := a["eur_per_1m_input"].(types.Float64); ok {
			rate.EurPer1mInput = f.ValueFloat64()
		}
		if f, ok := a["eur_per_1m_output"].(types.Float64); ok {
			rate.EurPer1mOutput = f.ValueFloat64()
		}
		note, _ := a["note"].(types.String)
		rate.Note = noteToSend(note)
		out = append(out, rate)
	}
	return out
}

func (r *aiRatePlanResource) put(ctx context.Context, plan *aiRatePlanModel, diags *diag.Diagnostics) bool {
	tenantID := plan.TenantID.ValueString()
	doing := "replacing the AI rate plan of the tenant " + tenantID
	from := ""
	if !plan.ValidFrom.IsNull() && !plan.ValidFrom.IsUnknown() {
		from = plan.ValidFrom.ValueString()
	}
	out, err := r.data.API.PutAIRatePlan(ctx, tenantID, planRatesFromModel(plan.Rates), from)
	if err != nil {
		diags.Append(rateWriteError(doing, err))
		return false
	}
	addWarnings(diags, doing, &out.Warnings)
	plan.fromAPI(out, plan.Rates)
	plan.Warnings = warningsValue(out.Warnings)
	return true
}

func (r *aiRatePlanResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan aiRatePlanModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || !r.put(ctx, &plan, &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *aiRatePlanResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var state aiRatePlanModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := state.TenantID.ValueString()
	p, err := r.data.API.GetAIRatePlan(ctx, tenantID)
	if err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && apiErr.Code() == client.CodeTenantNotFound {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.Append(aiBillingReadError("reading the AI rate plan of the tenant "+tenantID, err))
		return
	}
	state.fromAPI(p, state.Rates)
	state.Warnings = keepWarnings(state.Warnings)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *aiRatePlanResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan aiRatePlanModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || !r.put(ctx, &plan, &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete ends the tenant's plan today: every tier goes back to the list rate.
func (r *aiRatePlanResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var state aiRatePlanModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	tenantID := state.TenantID.ValueString()
	if err := r.data.API.DeleteAIRatePlan(ctx, tenantID); err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && apiErr.Code() == client.CodeTenantNotFound {
			return // the tenant is gone, and its plan with it
		}
		resp.Diagnostics.Append(rateWriteError("ending the AI rate plan of the tenant "+tenantID, err))
	}
}

func (r *aiRatePlanResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := strings.TrimSpace(req.ID)
	if !rxUUID.MatchString(id) {
		resp.Diagnostics.AddError("Cannot import the rate plan", fmt.Sprintf("Give the tenant's id (a UUID), not %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("tenant_id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("warnings"), warningsValue(nil))...)
}

// ── data sources ─────────────────────────────────────────────────────────────

var (
	aiRateSpec = []fieldSpec{
		{name: "tier", kind: fString}, {name: "eur_per_1m_input", kind: fFloat},
		{name: "eur_per_1m_output", kind: fFloat}, {name: "valid_from", kind: fString},
		{name: "valid_to", kind: fString}, {name: "note", kind: fString}, {name: "set_by", kind: fString},
		{name: "set_at", kind: fTime},
	}
	aiRateCardTierSpec = []fieldSpec{
		{name: "tier", kind: fString}, {name: "seedable", kind: fBool},
		{name: "current", kind: fObject, sub: aiRateSpec}, {name: "upcoming", kind: fObject, sub: aiRateSpec},
	}
	aiRateCardSpec = []fieldSpec{
		{name: "as_of", kind: fString}, {name: "currency", kind: fString}, {name: "unit", kind: fString},
		{name: "tiers", kind: fObjects, sub: aiRateCardTierSpec},
		{name: "history", kind: fObjects, sub: aiRateSpec},
	}
	aiRatePlanSpec = []fieldSpec{
		{name: "tenant_name", kind: fString}, {name: "as_of", kind: fString},
		{name: "currency", kind: fString}, {name: "unit", kind: fString},
		{name: "contract_included", kind: fBool},
		{name: "contract", kind: fObject, sub: []fieldSpec{{name: "id", kind: fString},
			{name: "status", kind: fString}, {name: "start_date", kind: fString}, {name: "end_date", kind: fString}}},
		{name: "rates", kind: fObjects, sub: aiRateSpec},
		{name: "upcoming", kind: fObjects, sub: aiRateSpec},
		{name: "effective", kind: fObjects, sub: effectiveRateSpec},
		{name: "history", kind: fObjects, sub: aiRateSpec},
	}
)

// NewAIRateCardDataSource is the factory for the ataila_ai_rate_card data source.
func NewAIRateCardDataSource() datasource.DataSource { return &aiRateCardDataSource{} }

type aiRateCardDataSource struct{ aiDataSource }

func (d *aiRateCardDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ai_rate_card"
}

func (d *aiRateCardDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dschema.Schema{
		MarkdownDescription: "The platform's AI rate card: the list rate per serving tier, EUR per one million " +
			"input and output tokens — in force today (`current`), the next scheduled change (`upcoming`) and the " +
			"history. Tiers the platform knows appear even without a rate (`current` null: their usage is not " +
			"priced). Needs a token holding `ai-gateway-read-global` (or `ai-gateway-admin-global`) and platform " +
			"release " + client.ReleaseAIBilling + " or later.",
		Attributes: specDataAttributes(aiRateCardSpec, "AiRateCard"),
	}
}

func (d *aiRateCardDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	if dg := d.data.featureRefused(client.FeatureAIRates, path.Root("tiers"), "The ataila_ai_rate_card data source"); dg != nil {
		resp.Diagnostics.Append(dg)
		return
	}
	rec, err := d.data.API.GetAIRateCardRecord(ctx)
	if err != nil {
		resp.Diagnostics.Append(aiBillingReadError("reading the AI rate card", err))
		return
	}
	setSpecState(ctx, &resp.State, specValues(rec, aiRateCardSpec), &resp.Diagnostics)
}

// NewAIRatePlanDataSource is the factory for the ataila_ai_rate_plan data source.
func NewAIRatePlanDataSource() datasource.DataSource { return &aiRatePlanDataSource{} }

type aiRatePlanDataSource struct{ aiDataSource }

func (d *aiRatePlanDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ai_rate_plan"
}

func (d *aiRatePlanDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := specDataAttributes(aiRatePlanSpec, "TenantAiRatePlan")
	attrs["tenant_id"] = dschema.StringAttribute{
		MarkdownDescription: "The tenant (`id` of an `ataila_tenant`).",
		Required:            true,
		Validators:          []validator.String{stringvalidator.RegexMatches(rxUUID, "must be a tenant id")},
	}
	resp.Schema = dschema.Schema{
		MarkdownDescription: "A tenant's AI rates: its own rates in force today (`rates`; empty when it pays the " +
			"list rate), scheduled changes, the effective rate per tier with its basis next to the list rate, " +
			"whether a contract includes metered AI, and the history. Needs a token holding " +
			"`orders-read-global` or `ai-gateway-read-global` (or an admin key) and platform release " +
			client.ReleaseAIBilling + " or later.",
		Attributes: attrs,
	}
}

func (d *aiRatePlanDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	var tenantID types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("tenant_id"), &tenantID)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if dg := d.data.featureRefused(client.FeatureAIRates, path.Root("tenant_id"), "The ataila_ai_rate_plan data source"); dg != nil {
		resp.Diagnostics.Append(dg)
		return
	}
	rec, err := d.data.API.GetAIRatePlanRecord(ctx, tenantID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(aiBillingReadError("reading the AI rate plan of the tenant "+tenantID.ValueString(), err))
		return
	}
	values := specValues(rec, aiRatePlanSpec)
	values["tenant_id"] = tenantID
	setSpecState(ctx, &resp.State, values, &resp.Diagnostics)
}
