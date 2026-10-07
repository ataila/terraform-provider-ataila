// Copyright (c) 2026 ATAILA Kft.
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ataila/terraform-provider-ataila/internal/client"
)

// Rated AI usage: one tenant's month (ataila_ai_usage) and every tenant's
// month (ataila_ai_gateway_usage), the platform's operator view. EUR is rated
// when read, at the tenant's own rate (ataila_ai_rate_plan), else the list
// rate (ataila_ai_rate_card); a tier without a rate is null and named in
// `unrated_tiers`, never a silent 0.

var (
	_ datasource.DataSourceWithConfigure = (*aiUsageDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*aiGatewayUsageDataSource)(nil)
)

// rxMonth is a month as the platform names it.
var rxMonth = regexp.MustCompile(`^[0-9]{4}-(0[1-9]|1[0-2])$`)

var (
	usageCountSpec = []fieldSpec{
		{name: "calls", kind: fInt}, {name: "prompt_tokens", kind: fInt},
		{name: "completion_tokens", kind: fInt}, {name: "total_tokens", kind: fInt},
		{name: "failures", kind: fInt}, {name: "cache_hits", kind: fInt},
		{name: "shadow_usd", kind: fFloat},
	}
	usageRatedSpec = []fieldSpec{
		{name: "rated_eur", kind: fFloat}, {name: "rate_basis", kind: fString},
		{name: "rated_complete", kind: fBool},
	}
	usageTierModelSpec = joinSpecs([]fieldSpec{{name: "tier", kind: fString}, {name: "model", kind: fString}},
		usageCountSpec, usageRatedSpec)
	usageKeySpec = []fieldSpec{
		{name: "key_alias", kind: fString}, {name: "env", kind: fString}, {name: "app", kind: fString},
		{name: "project_id", kind: fString}, {name: "attribution", kind: fString},
		{name: "last_seen", kind: fTime},
		{name: "totals", kind: fObject, sub: usageCountSpec},
		{name: "rated_eur", kind: fFloat},
		{name: "rows", kind: fObjects, sub: usageTierModelSpec},
	}
	usageTierSpec = joinSpecs([]fieldSpec{{name: "tier", kind: fString}}, usageCountSpec, usageRatedSpec,
		[]fieldSpec{{name: "eur_per_1m_input", kind: fFloat}, {name: "eur_per_1m_output", kind: fFloat}})
	usageDaySpec = []fieldSpec{
		{name: "day", kind: fString}, {name: "calls", kind: fInt}, {name: "total_tokens", kind: fInt},
		{name: "shadow_usd", kind: fFloat}, {name: "rated_eur", kind: fFloat},
	}
	usageForecastSpec = []fieldSpec{
		{name: "elapsed_days", kind: fInt}, {name: "days_in_month", kind: fInt},
		{name: "total_tokens", kind: fInt}, {name: "calls", kind: fInt},
		{name: "shadow_usd", kind: fFloat}, {name: "rated_eur", kind: fFloat},
	}
	usageBudgetSpec = []fieldSpec{
		{name: "limit", kind: fFloat}, {name: "policy", kind: fString}, {name: "spent", kind: fFloat},
		{name: "pct", kind: fFloat}, {name: "state", kind: fString}, {name: "forecast", kind: fFloat},
		{name: "forecast_pct", kind: fFloat}, {name: "complete", kind: fBool},
		{name: "enforced_on_gateway", kind: fBool},
	}
	tenantUsageSpec = []fieldSpec{
		{name: "tenant_name", kind: fString}, {name: "tenant_slug", kind: fString},
		{name: "tz", kind: fString},
		{name: "totals", kind: fObject, sub: usageCountSpec},
		{name: "by_key", kind: fObjects, sub: usageKeySpec},
		{name: "by_tier", kind: fObjects, sub: usageTierSpec},
		{name: "by_day", kind: fObjects, sub: usageDaySpec},
		{name: "forecast", kind: fObject, sub: usageForecastSpec},
		{name: "rated_eur", kind: fFloat}, {name: "rate_basis", kind: fString},
		{name: "rate_bases", kind: fStrings}, {name: "unrated_tiers", kind: fStrings},
		{name: "rated_complete", kind: fBool},
		{name: "budget", kind: fObject, sub: usageBudgetSpec},
		{name: "oldest_month", kind: fString},
	}
	gatewayTenantSpec = joinSpecs([]fieldSpec{
		{name: "tenant_id", kind: fString}, {name: "tenant_name", kind: fString},
		{name: "tenant_slug", kind: fString}, {name: "customer_id", kind: fString},
		{name: "customer_name", kind: fString}, {name: "keys", kind: fInt},
		{name: "key_aliases", kind: fStrings},
		{name: "attribution", kind: fStringMap, doc: "Usage rows per attribution method (`registry`, `map`, " +
			"`project`, `unattributed`); the counts as strings."},
		{name: "last_seen", kind: fTime},
	}, usageCountSpec, usageRatedSpec, []fieldSpec{
		{name: "rate_bases", kind: fStrings}, {name: "unrated_tiers", kind: fStrings},
	})
	gatewayUsageSpec = []fieldSpec{
		{name: "tz", kind: fString},
		{name: "totals", kind: fObject, sub: usageCountSpec},
		{name: "tenants", kind: fObjects, sub: gatewayTenantSpec},
		{name: "unattributed", kind: fObject, sub: gatewayTenantSpec},
		{name: "rated_eur", kind: fFloat}, {name: "rate_basis", kind: fString},
		{name: "unrated_tiers", kind: fStrings}, {name: "rated_complete", kind: fBool},
	}
)

func joinSpecs(parts ...[]fieldSpec) []fieldSpec {
	var out []fieldSpec
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

const monthDoc = "The month, `YYYY-MM`, in the platform's billing time zone. Leave it out for the current " +
	"month (the attribute then reads which month that was)."

// aiBillingReadError explains a failed read of usage or rates.
func aiBillingReadError(doing string, err error) diag.Diagnostic {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.Code() == client.CodeTenantNotFound:
			return diag.NewErrorDiagnostic("No such tenant",
				fmt.Sprintf("While %s: the platform has no tenant with this id.\n\n%s", doing, apiErr.Detail()))
		case (apiErr.StatusCode == 404 && (apiErr.Code() == "" || apiErr.Code() == "not_found")) || apiErr.StatusCode == 405:
			return diag.NewErrorDiagnostic("This platform does not serve AI usage and rates",
				fmt.Sprintf("While %s: the platform answered HTTP %d. AI usage, the rate card and rate plans need "+
					"platform release %s or later.\n\n%s", doing, apiErr.StatusCode, client.ReleaseAIBilling,
					apiErr.Detail()))
		}
	}
	return apiError(doing, err)
}

// ── ataila_ai_usage ──────────────────────────────────────────────────────────

// NewAIUsageDataSource is the factory for ataila_ai_usage.
func NewAIUsageDataSource() datasource.DataSource { return &aiUsageDataSource{} }

type aiUsageDataSource struct{ aiDataSource }

func (d *aiUsageDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ai_usage"
}

func (d *aiUsageDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := specDataAttributes(tenantUsageSpec, "TenantAiUsage")
	attrs["tenant_id"] = dschema.StringAttribute{
		MarkdownDescription: "The tenant whose usage to read (`id` of an `ataila_tenant`).",
		Required:            true,
		Validators:          []validator.String{stringvalidator.RegexMatches(rxUUID, "must be a tenant id")},
	}
	attrs["month"] = dschema.StringAttribute{
		MarkdownDescription: monthDoc, Optional: true, Computed: true,
		Validators: []validator.String{stringvalidator.RegexMatches(rxMonth, "must be YYYY-MM")},
	}
	resp.Schema = dschema.Schema{
		MarkdownDescription: "One tenant's AI gateway usage for a month, rated: totals, per key (and per tier and " +
			"model under each key), per tier with its rate, per day, the month-end forecast and the monthly AI " +
			"budget against the spend. EUR is rated when read — each day's tokens at the rate in force that day, " +
			"the tenant's own rate (`ataila_ai_rate_plan`) else the list rate (`ataila_ai_rate_card`); a tier " +
			"without a rate is null and named in `unrated_tiers`, and the totals are then a floor " +
			"(`rated_complete` false). This is the operator's view: each key's attribution and the gateway's own " +
			"shadow price (`shadow_usd`) are included. The usage ledger fills every 15 minutes, so today's " +
			"figures grow. Needs a token holding `ai-gateway-read-global` or `orders-read-global` (or an admin " +
			"key) and platform release " + client.ReleaseAIBilling + " or later.",
		Attributes: attrs,
	}
}

func (d *aiUsageDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	var tenantID, month types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("tenant_id"), &tenantID)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("month"), &month)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if dg := d.data.featureRefused(client.FeatureAIUsage, path.Root("tenant_id"), "The ataila_ai_usage data source"); dg != nil {
		resp.Diagnostics.Append(dg)
		return
	}
	doing := "reading the AI usage of the tenant " + tenantID.ValueString()
	rec, err := d.data.API.GetTenantAIUsage(ctx, tenantID.ValueString(), month.ValueString())
	if err != nil {
		resp.Diagnostics.Append(aiBillingReadError(doing, err))
		return
	}
	values := specValues(rec, tenantUsageSpec)
	values["tenant_id"] = tenantID
	m, _ := rec["month"].(string)
	values["month"] = types.StringValue(m)
	setSpecState(ctx, &resp.State, values, &resp.Diagnostics)
}

// ── ataila_ai_gateway_usage ──────────────────────────────────────────────────

// NewAIGatewayUsageDataSource is the factory for ataila_ai_gateway_usage.
func NewAIGatewayUsageDataSource() datasource.DataSource { return &aiGatewayUsageDataSource{} }

type aiGatewayUsageDataSource struct{ aiDataSource }

func (d *aiGatewayUsageDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ai_gateway_usage"
}

func (d *aiGatewayUsageDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := specDataAttributes(gatewayUsageSpec, "GatewayAiUsage")
	attrs["month"] = dschema.StringAttribute{
		MarkdownDescription: monthDoc, Optional: true, Computed: true,
		Validators: []validator.String{stringvalidator.RegexMatches(rxMonth, "must be YYYY-MM")},
	}
	resp.Schema = dschema.Schema{
		MarkdownDescription: "Every tenant's AI gateway usage for a month, heaviest first, each rated at its own " +
			"rate, and the unattributed bucket (keys no tenant could be found for, priced at the list rate for " +
			"information and billed to nobody). Needs a token holding `ai-gateway-read-global` or " +
			"`orders-read-global` (or an admin key) and platform release " + client.ReleaseAIBilling + " or later.",
		Attributes: attrs,
	}
}

func (d *aiGatewayUsageDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	var month types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("month"), &month)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if dg := d.data.featureRefused(client.FeatureAIUsage, path.Root("month"), "The ataila_ai_gateway_usage data source"); dg != nil {
		resp.Diagnostics.Append(dg)
		return
	}
	rec, err := d.data.API.GetGatewayAIUsage(ctx, month.ValueString())
	if err != nil {
		resp.Diagnostics.Append(aiBillingReadError("reading every tenant's AI usage", err))
		return
	}
	values := specValues(rec, gatewayUsageSpec)
	m, _ := rec["month"].(string)
	values["month"] = types.StringValue(m)
	setSpecState(ctx, &resp.State, values, &resp.Diagnostics)
}
