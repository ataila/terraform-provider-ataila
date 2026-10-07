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

	"github.com/hashicorp/terraform-plugin-framework-validators/float64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
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
	_ resource.ResourceWithConfigure    = (*gatewayKeyResource)(nil)
	_ resource.ResourceWithModifyPlan   = (*gatewayKeyResource)(nil)
	_ resource.ResourceWithImportState  = (*gatewayKeyResource)(nil)
	_ resource.ResourceWithUpgradeState = (*gatewayKeyResource)(nil)
)

// AliasImportPrefix imports a gateway key by its alias.
const AliasImportPrefix = "alias:"

// NewGatewayKeyResource is the factory for ataila_ai_gateway_key.
func NewGatewayKeyResource() resource.Resource { return &gatewayKeyResource{} }

type gatewayKeyResource struct {
	data *ProviderData
}

var keyFrozen = frozenKey{object: "AI gateway key", why: "The key's alias is built from its tenant, env, app " +
	"and feature; another alias is another key, and replacing this one would delete it in the gateway, " +
	"irreversibly, breaking every client that uses it."}

type gatewayKeyModel struct {
	ID              types.String   `tfsdk:"id"`
	OrganizationID  types.String   `tfsdk:"organization_id"`
	Env             types.String   `tfsdk:"env"`
	App             types.String   `tfsdk:"app"`
	Feature         types.String   `tfsdk:"feature"`
	Models          types.Set      `tfsdk:"models"`
	ProjectID       types.String   `tfsdk:"project_id"`
	RpmLimit        types.Int64    `tfsdk:"rpm_limit"`
	TpmLimit        types.Int64    `tfsdk:"tpm_limit"`
	SoftBudgetUSD   types.Float64  `tfsdk:"soft_budget_usd"`
	BudgetDuration  types.String   `tfsdk:"budget_duration"`
	ExposeSecret    types.Bool     `tfsdk:"expose_secret"`
	RotationTrigger types.Map      `tfsdk:"rotation_trigger"`
	KeyAlias        types.String   `tfsdk:"key_alias"`
	SecretPath      types.String   `tfsdk:"secret_path"`
	SecretField     types.String   `tfsdk:"secret_field"`
	Origin          types.String   `tfsdk:"origin"`
	TokenHashPrefix types.String   `tfsdk:"token_hash_prefix"`
	SpendUSD        types.Float64  `tfsdk:"spend_usd"`
	Live            types.String   `tfsdk:"live"`
	CreatedBy       types.String   `tfsdk:"created_by"`
	CreatedAt       TimestampValue `tfsdk:"created_at"`
	UpdatedAt       TimestampValue `tfsdk:"updated_at"`
	RotatedAt       TimestampValue `tfsdk:"rotated_at"`
	Secret          types.String   `tfsdk:"secret"`
	Warnings        types.List     `tfsdk:"warnings"`
}

func int64Ptr(p *int64) types.Int64 {
	if p == nil {
		return types.Int64Null()
	}
	return types.Int64Value(*p)
}

func float64Ptr(p *float64) types.Float64 {
	if p == nil {
		return types.Float64Null()
	}
	return types.Float64Value(*p)
}

// fromAPI fills everything the API reports. It never touches the inputs the
// API does not echo (expose_secret, rotation_trigger) nor secret: the value
// is set by the caller from a create or rotation answer only.
func (m *gatewayKeyModel) fromAPI(k *client.GatewayKeyData) {
	m.ID = types.StringValue(k.Id)
	m.OrganizationID = types.StringValue(k.OrganizationId)
	m.Env = types.StringValue(k.Env)
	m.App = types.StringValue(k.App)
	m.Feature = stringOrNull(k.Feature)
	models := make([]attr.Value, 0, len(k.Models))
	for _, name := range k.Models {
		models = append(models, types.StringValue(name))
	}
	m.Models = types.SetValueMust(types.StringType, models)
	m.ProjectID = stringOrNull(k.ProjectId)
	m.RpmLimit = int64Ptr(k.RpmLimit)
	m.TpmLimit = int64Ptr(k.TpmLimit)
	m.SoftBudgetUSD = float64Ptr(k.SoftBudgetUsd)
	m.BudgetDuration = stringOrNull(k.BudgetDuration)
	m.KeyAlias = types.StringValue(k.KeyAlias)
	m.SecretPath = stringOrNull(k.SecretPath)
	m.SecretField = stringOrNull(k.SecretField)
	m.Origin = types.StringValue(k.Origin)
	m.TokenHashPrefix = stringOrNull(k.TokenHashPrefix)
	m.SpendUSD = float64Ptr(k.SpendUsd)
	m.Live = types.StringValue(k.Live)
	m.CreatedBy = stringOrNull(k.CreatedBy)
	m.CreatedAt = NewTimestamp(k.CreatedAt)
	m.UpdatedAt = NewTimestamp(k.UpdatedAt)
	m.RotatedAt = NewTimestampPointer(k.RotatedAt)
}

func (m *gatewayKeyModel) ident() string {
	if a := m.KeyAlias.ValueString(); a != "" {
		return fmt.Sprintf("%s (id %s)", a, m.ID.ValueString())
	}
	return "with id " + m.ID.ValueString()
}

var (
	rxUUID     = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	rxSegment  = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	rxDuration = regexp.MustCompile(`^[1-9][0-9]{0,3}(s|m|h|d|mo)$`)
)

func (r *gatewayKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ai_gateway_key"
}

func (r *gatewayKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	segment := []validator.String{stringvalidator.LengthBetween(1, 40), stringvalidator.RegexMatches(rxSegment,
		"must be lower-case letters and digits in words joined by single hyphens")}
	computed := func(desc string, mods ...planmodifier.String) schema.StringAttribute {
		return schema.StringAttribute{MarkdownDescription: desc, Computed: true, PlanModifiers: mods}
	}
	resp.Schema = schema.Schema{
		Version: 1,
		MarkdownDescription: "A virtual key of the platform's AI gateway, for one tenant, env and app. " +
			"**Needs a platform with an AI gateway**: where none is configured, every call answers " +
			"`gateway_not_configured` and the provider reports it without retrying.\n\n" +
			"The key's value is always kept in the platform's secrets store, at `secret_path`. It is returned once, in the create or " +
			"rotation that produced it, and only with `expose_secret = true`; the provider then keeps it in " +
			"`secret` (sensitive) and never reads it again, so it never shows as a difference. A change to " +
			"`rotation_trigger` rotates the key: a new value under the same alias, the old one stops working " +
			"at once. An adopted key (`origin = \"adopted\"`) cannot be rotated here.\n\n" +
			"**Frozen keys** (`organization_id`, `env`, `app`, `feature`) make up the alias; a change fails the " +
			"plan. **Destroy deletes the key in the gateway, irreversibly** (every client using it fails at " +
			"once); it needs the admin permission, not `allow_destroy`. Budgets are soft: the gateway alerts " +
			"and never blocks.",
		Attributes: map[string]schema.Attribute{
			"id": computed("Key id (a UUID) in the platform's registry.", keep...),
			"organization_id": schema.StringAttribute{
				MarkdownDescription: "Id of the tenant that owns the key. **Frozen.**", Required: true,
				Validators:    []validator.String{stringvalidator.RegexMatches(rxUUID, "must be a tenant id (a UUID)")},
				PlanModifiers: []planmodifier.String{keyFrozen.forString()},
			},
			"env": schema.StringAttribute{
				MarkdownDescription: "`dev`, `uat` or `prod`. **Frozen.**", Required: true,
				Validators:    []validator.String{stringvalidator.OneOf("dev", "uat", "prod")},
				PlanModifiers: []planmodifier.String{keyFrozen.forString()},
			},
			"app": schema.StringAttribute{
				MarkdownDescription: "The client application, 1-40 characters, lower-case words joined by hyphens. **Frozen.**",
				Required:            true, Validators: segment,
				PlanModifiers: []planmodifier.String{keyFrozen.forString()},
			},
			"feature": schema.StringAttribute{
				MarkdownDescription: "Optional feature of the app, same rule; the alias's last part. **Frozen.**",
				Optional:            true, Validators: segment,
				PlanModifiers: []planmodifier.String{keyFrozen.forString()},
			},
			"models": schema.SetAttribute{
				MarkdownDescription: "Serving-tier names the key may call (`ataila_ai_serving_tiers`). A tier that " +
					"serves nothing right now is accepted with a warning.",
				ElementType: types.StringType, Required: true,
				Validators: []validator.Set{setvalidator.SizeBetween(1, 50),
					setvalidator.ValueStringsAre(stringvalidator.RegexMatches(rxSegment, "must be a tier name"))},
			},
			"project_id": schema.StringAttribute{
				MarkdownDescription: "Optional project of that tenant the key serves.", Optional: true,
				Validators: []validator.String{stringvalidator.RegexMatches(rxIntID, "must be a project id")},
			},
			"rpm_limit": schema.Int64Attribute{
				MarkdownDescription: "Requests per minute.", Optional: true,
				Validators: []validator.Int64{int64validator.Between(1, 1_000_000_000)},
			},
			"tpm_limit": schema.Int64Attribute{
				MarkdownDescription: "Tokens per minute.", Optional: true,
				Validators: []validator.Int64{int64validator.Between(1, 2_000_000_000)},
			},
			"soft_budget_usd": schema.Float64Attribute{
				MarkdownDescription: "SOFT budget in US dollars: the gateway alerts when it is reached and never blocks.",
				Optional:            true,
				Validators:          []validator.Float64{float64validator.Between(0.000001, 1_000_000_000)},
			},
			"budget_duration": schema.StringAttribute{
				MarkdownDescription: "The soft budget's period, for example `30d` (`s`, `m`, `h`, `d` or `mo`).",
				Optional:            true,
				Validators: []validator.String{stringvalidator.RegexMatches(rxDuration,
					"must be a number and s, m, h, d or mo")},
			},
			"expose_secret": schema.BoolAttribute{
				MarkdownDescription: "Return the key's value in the create or rotation answer, into `secret`. Not " +
					"sent anywhere else; changing it alone changes nothing until the next create or rotation.",
				Optional: true,
			},
			"rotation_trigger": schema.MapAttribute{
				MarkdownDescription: "Any map; a change rotates the key (for example `{ at = \"2026-10\" }`).",
				ElementType:         types.StringType, Optional: true,
			},
			"key_alias": computed("`<tenant-slug>-<env>-<app>[-<feature>]`; an adopted key keeps its own.", keep...),
			"secret_path": computed("Where the secrets store keeps the key's value. Null for an adopted key "+
				"whose location was never recorded.", keep...),
			"secret_field": computed("The field at `secret_path` that holds the value.", keep...),
			"origin":       computed("`api` (created here) or `adopted` (existed in the gateway first).", keep...),
			"token_hash_prefix": computed("The first 12 characters of the gateway's SHA-256 of the key, to find it " +
				"in the gateway's own records. Never the value."),
			"spend_usd": schema.Float64Attribute{
				MarkdownDescription: "Spend the gateway has recorded for the key (US dollars), as of the last read.",
				Computed:            true,
			},
			"live": computed("`present`: the gateway has the key and the limits are its live values; `missing`: " +
				"the gateway lost it (re-create it with `-replace`); `not_read`: right after a change."),
			"created_by": computed("Id of the principal that created the key.", keep...),
			"created_at": schema.StringAttribute{MarkdownDescription: "RFC 3339 in UTC, compared as an instant.",
				CustomType: TimestampType{}, Computed: true, PlanModifiers: keep},
			"updated_at": schema.StringAttribute{MarkdownDescription: "RFC 3339 in UTC, compared as an instant.",
				CustomType: TimestampType{}, Computed: true},
			"rotated_at": schema.StringAttribute{MarkdownDescription: "The last rotation, or null.",
				CustomType: TimestampType{}, Computed: true, PlanModifiers: keep},
			"secret": schema.StringAttribute{
				MarkdownDescription: "The key's value (`sk-…`), only from a create or rotation made with " +
					"`expose_secret = true`; null otherwise. Sensitive. The API never returns it again: a read keeps " +
					"what the state has.",
				Computed: true, Sensitive: true, PlanModifiers: keep,
			},
			"warnings": warningsSchema("What did not go as planned in the last create, change or rotation made " +
				"through this resource (for example a tier that serves nothing)."),
		},
	}
}

func (r *gatewayKeyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *gatewayKeyResource) configured(diags interface{ AddError(string, string) }) bool {
	if r.data == nil {
		diags.AddError("Provider not configured", "The ataila provider was not configured.")
		return false
	}
	return true
}

// ModifyPlan plans a rotation: when rotation_trigger changes, the value,
// its hash and the rotation time are unknown until the rotation runs.
func (r *gatewayKeyResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var plan, state gatewayKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || plan.RotationTrigger.Equal(state.RotationTrigger) {
		return
	}
	if state.Origin.ValueString() == "adopted" {
		resp.Diagnostics.AddAttributeError(path.Root("rotation_trigger"), "An adopted key cannot be rotated here",
			"The key existed in the gateway before the platform registered it; its value lives in its consumer's "+
				"own secret, so a new value would break that consumer. Rotate it where the consumer is deployed, "+
				"and leave rotation_trigger unchanged.")
		return
	}
	if plan.ExposeSecret.ValueBool() {
		plan.Secret = types.StringUnknown()
	} else {
		plan.Secret = types.StringNull()
	}
	plan.TokenHashPrefix = types.StringUnknown()
	plan.RotatedAt = TimestampValue{StringValue: types.StringUnknown()}
	plan.UpdatedAt = TimestampValue{StringValue: types.StringUnknown()}
	plan.Live = types.StringUnknown()
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

// gatewayKeyDiag explains a failed key call: the gateway's absence first,
// then the key's own refusals with what to do.
func gatewayKeyDiag(doing string, err error) (summary, detail string) {
	if d := gatewayUnavailable(doing, err); d != nil {
		return d.Summary(), d.Detail()
	}
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		hint := map[string]string{
			client.CodeKeyAdopted: "The key was adopted: its value lives in its consumer's own secret, so a new " +
				"value would break that consumer. Rotate it where the consumer is deployed.",
			client.CodeKeyMissingOnGateway: "The gateway no longer has this key. Re-create it with " +
				"`tofu apply -replace=<address>` / `terraform apply -replace=<address>`.",
			"key_alias_taken": "A live key already has this alias. Import it (alias:<key_alias>) instead of " +
				"creating it.",
			"key_alias_on_gateway": "The gateway already has a key with this alias that the platform has not " +
				"registered; adopt it in the portal, then import it.",
			"unknown_tier": "`models` names a tier the catalogue does not have (ataila_ai_serving_tiers lists them).",
		}[apiErr.Code()]
		d := apiError(doing, err)
		if hint != "" {
			return d.Summary(), hint + "\n\n" + d.Detail()
		}
		return d.Summary(), d.Detail()
	}
	d := apiError(doing, err)
	return d.Summary(), d.Detail()
}

func setStrings(ctx context.Context, s types.Set) []string {
	var out []string
	_ = s.ElementsAs(ctx, &out, false)
	sort.Strings(out)
	return out
}

func (r *gatewayKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan gatewayKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{
		"organization_id": plan.OrganizationID.ValueString(),
		"env":             plan.Env.ValueString(),
		"app":             plan.App.ValueString(),
		"models":          setStrings(ctx, plan.Models),
		"expose_secret":   plan.ExposeSecret.ValueBool(),
	}
	if v := optString(plan.Feature); v != nil {
		body["feature"] = *v
	}
	if v := optString(plan.ProjectID); v != nil {
		body["project_id"] = *v
	}
	if !plan.RpmLimit.IsNull() {
		body["rpm_limit"] = plan.RpmLimit.ValueInt64()
	}
	if !plan.TpmLimit.IsNull() {
		body["tpm_limit"] = plan.TpmLimit.ValueInt64()
	}
	if !plan.SoftBudgetUSD.IsNull() {
		body["soft_budget_usd"] = plan.SoftBudgetUSD.ValueFloat64()
	}
	if v := optString(plan.BudgetDuration); v != nil {
		body["budget_duration"] = *v
	}
	doing := "creating the AI gateway key " + strings.Join([]string{plan.Env.ValueString(), plan.App.ValueString()}, "-")
	k, err := r.data.API.CreateGatewayKey(ctx, body)
	if err != nil {
		summary, detail := gatewayKeyDiag(doing, err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	addWarnings(&resp.Diagnostics, doing, &k.Warnings)
	plan.fromAPI(k)
	plan.Secret = stringOrNull(k.Secret)
	r.refresh(ctx, &plan)
	plan.Warnings = warningsValue(k.Warnings)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *gatewayKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var state gatewayKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	k, err := r.data.API.GetGatewayKey(ctx, state.ID.ValueString())
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		summary, detail := gatewayKeyDiag("reading the AI gateway key "+state.ident(), err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	if k.Live == "missing" {
		resp.Diagnostics.AddWarning("The AI gateway lost this key",
			fmt.Sprintf("The gateway no longer has the key %s, so nobody can use it; the platform still lists it. "+
				"Re-create it under the same alias with `tofu apply -replace=ataila_ai_gateway_key.<name>` / "+
				"`terraform apply -replace=ataila_ai_gateway_key.<name>`.", state.ident()))
	}
	secret := state.Secret
	state.fromAPI(k)
	state.Secret = secret // never re-read: the API returns it once
	state.Warnings = keepWarnings(state.Warnings)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *gatewayKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan, state gatewayKeyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	for _, f := range []struct {
		name        string
		state, plan tfValue
	}{{"organization_id", state.OrganizationID, plan.OrganizationID}, {"env", state.Env, plan.Env},
		{"app", state.App, plan.App}, {"feature", state.Feature, plan.Feature}} {
		keyFrozen.check(path.Root(f.name), f.state, f.plan, true, &resp.Diagnostics)
	}
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	patch := client.Patch{}
	if !plan.Models.Equal(state.Models) {
		patch["models"] = setStrings(ctx, plan.Models)
	}
	if !plan.ProjectID.Equal(state.ProjectID) {
		patch["project_id"] = nullable(plan.ProjectID)
	}
	if !plan.RpmLimit.Equal(state.RpmLimit) {
		patch["rpm_limit"] = nullableInt(plan.RpmLimit)
	}
	if !plan.TpmLimit.Equal(state.TpmLimit) {
		patch["tpm_limit"] = nullableInt(plan.TpmLimit)
	}
	if !plan.SoftBudgetUSD.Equal(state.SoftBudgetUSD) {
		if plan.SoftBudgetUSD.IsNull() {
			patch["soft_budget_usd"] = nil
		} else {
			patch["soft_budget_usd"] = plan.SoftBudgetUSD.ValueFloat64()
		}
	}
	if !plan.BudgetDuration.Equal(state.BudgetDuration) {
		patch["budget_duration"] = nullable(plan.BudgetDuration)
	}

	next := state
	next.ExposeSecret, next.RotationTrigger = plan.ExposeSecret, state.RotationTrigger
	var warnings []client.ApiWarning
	if len(patch) > 0 {
		k, err := r.data.API.UpdateGatewayKey(ctx, id, patch)
		if err != nil {
			summary, detail := gatewayKeyDiag("changing the AI gateway key "+state.ident(), err)
			resp.Diagnostics.AddError(summary, detail)
			return
		}
		next.fromAPI(k)
		warnings = append(warnings, k.Warnings...)
	}
	if !plan.RotationTrigger.Equal(state.RotationTrigger) {
		k, err := r.data.API.RotateGatewayKey(ctx, id, plan.ExposeSecret.ValueBool())
		if err != nil {
			summary, detail := gatewayKeyDiag("rotating the AI gateway key "+state.ident(), err)
			resp.Diagnostics.AddError(summary, detail)
			// The change above stands; the old trigger stays, so the next apply rotates again.
			if len(patch) > 0 {
				next.Warnings = warningsValue(warnings)
				resp.Diagnostics.Append(resp.State.Set(ctx, &next)...)
			}
			return
		}
		next.fromAPI(k)
		next.Secret = stringOrNull(k.Secret)
		next.RotationTrigger = plan.RotationTrigger
		warnings = append(warnings, k.Warnings...)
	}
	if len(patch) == 0 && plan.RotationTrigger.Equal(state.RotationTrigger) {
		// Only expose_secret changed: nothing to send.
		next.Warnings = keepWarnings(state.Warnings)
	} else {
		next.Warnings = warningsValue(warnings)
	}
	addWarnings(&resp.Diagnostics, "changing the AI gateway key "+state.ident(), &warnings)
	r.refresh(ctx, &next)
	resp.Diagnostics.Append(resp.State.Set(ctx, &next)...)
}

// refresh re-reads a key after a write so that the state holds the gateway's
// live values and spend (a write answers with registry values). A failed
// read changes nothing: the write stands, and the next refresh reads again.
func (r *gatewayKeyResource) refresh(ctx context.Context, m *gatewayKeyModel) {
	k, err := r.data.API.GetGatewayKey(ctx, m.ID.ValueString())
	if err != nil || k.Live != "present" {
		return
	}
	secret := m.Secret
	m.fromAPI(k)
	m.Secret = secret
}

func nullableInt(v types.Int64) any {
	if v.IsNull() {
		return nil
	}
	return v.ValueInt64()
}

func (r *gatewayKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var state gatewayKeyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.data.API.DeleteGatewayKey(ctx, state.ID.ValueString())
	if err != nil && !isNotFound(err) {
		summary, detail := gatewayKeyDiag("deleting the AI gateway key "+state.ident(), err)
		resp.Diagnostics.AddError(summary, detail)
	}
}

func (r *gatewayKeyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	id := strings.TrimSpace(req.ID)
	if alias, ok := strings.CutPrefix(id, AliasImportPrefix); ok {
		found, err := r.data.API.ListGatewayKeys(ctx, client.GatewayKeyFilter{KeyAlias: alias})
		if err != nil {
			summary, detail := gatewayKeyDiag("looking up the AI gateway key "+alias, err)
			resp.Diagnostics.AddError(summary, detail)
			return
		}
		if len(found) != 1 {
			resp.Diagnostics.AddError("Cannot import the AI gateway key",
				fmt.Sprintf("%d live keys have the alias %q; the import needs exactly one.", len(found), alias))
			return
		}
		id = found[0].Id
	}
	if id == "" {
		resp.Diagnostics.AddError("Cannot import the AI gateway key", "Give the key id, or alias:<key_alias>.")
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("secret"), types.StringNull())...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("warnings"), warningsValue(nil))...)
}

// UpgradeState renames the attributes 0.7.0 renamed (schema version 0 to 1).
func (r *gatewayKeyResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return renameUpgraders(gatewayKeyRenamesV1)
}
