// Copyright (c) 2026 ATAILA Kft.
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ataila/terraform-provider-ataila/internal/client"
)

// The order catalogue and a tenant's orders, READ ONLY: an order is placed by
// the tenant in the portal and approved, rejected or re-run by an operator in
// the portal (there is no ataila_order resource, by design). The free-form
// members — an item's order form, an order's spec, quota check and delivery
// facts — are JSON text (`*_json`), for `jsondecode()`.

var (
	_ datasource.DataSourceWithConfigure = (*catalogueItemsDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*ordersDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*orderDataSource)(nil)
)

// orderStatuses are an order's states, in the platform's order.
var orderStatuses = []string{"submitted", "auto_approved", "awaiting_approval", "approved", "rejected",
	"dispatched", "running", "delivered", "partial", "failed", "cancelled"}

// NewCatalogueItemsDataSource is the factory for ataila_catalogue_items.
func NewCatalogueItemsDataSource() datasource.DataSource { return &catalogueItemsDataSource{} }

// NewOrdersDataSource is the factory for ataila_orders.
func NewOrdersDataSource() datasource.DataSource { return &ordersDataSource{} }

// NewOrderDataSource is the factory for ataila_order.
func NewOrderDataSource() datasource.DataSource { return &orderDataSource{} }

// jsonText is a JSON value as compact text with sorted object keys, so the
// same value always reads the same; null for an absent or JSON-null value.
func jsonText(raw json.RawMessage) types.String {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return types.StringNull()
	}
	var v any
	if err := json.Unmarshal(trimmed, &v); err != nil {
		return types.StringValue(string(trimmed))
	}
	b, err := json.Marshal(v)
	if err != nil {
		return types.StringValue(string(trimmed))
	}
	return types.StringValue(string(b))
}

// jsonMember is one string member of a JSON object, or null.
func jsonMember(raw json.RawMessage, name string) types.String {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return types.StringNull()
	}
	if s, ok := m[name].(string); ok && s != "" {
		return types.StringValue(s)
	}
	return types.StringNull()
}

// ── the catalogue ────────────────────────────────────────────────────────────

var catalogueItemTypes = map[string]attr.Type{
	"key":                   types.StringType,
	"kind":                  types.StringType,
	"name":                  types.StringType,
	"edition":               types.StringType,
	"requires_approval":     types.BoolType,
	"spec_schema_json":      types.StringType,
	"quota_dimensions_json": types.StringType,
	"price_hint_json":       types.StringType,
	"sort_order":            types.Int64Type,
}

var catalogueItemDocs = map[string]string{
	"key":               "The item's key, for example `ai-gateway-key`.",
	"kind":              client.Describe("CatalogueItem", "kind"),
	"name":              "Its display name.",
	"edition":           client.Describe("CatalogueItem", "edition"),
	"requires_approval": client.Describe("CatalogueItem", "requires_approval"),
	"spec_schema_json": "The order form as JSON text: a JSON Schema object (`type`, `properties`, `required`, " +
		"defaults and limits) an order's spec must satisfy. Read it with `jsondecode()`.",
	"quota_dimensions_json": "As JSON text: how an order's spec maps onto quota dimensions, per dimension " +
		"`{\"const\": n}`, `{\"field\": \"<spec field>\"}` or `{\"field\": …, \"map\": {…}}`.",
	"price_hint_json": "As JSON text: which cost units price an order of the item (`unit_keys`); null when " +
		"the item has no price basis. The EUR estimate itself is the tenant's, in the portal.",
	"sort_order": "Display order, ascending.",
}

type catalogueItemsDataSource struct {
	data *ProviderData
}

type catalogueItemsModel struct {
	Items types.List `tfsdk:"items"`
}

func (d *catalogueItemsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_catalogue_items"
}

func (d *catalogueItemsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dschema.Schema{
		MarkdownDescription: "What tenants can order on the platform: every enabled catalogue item, in display " +
			"order, with its order form, how an order of it counts against the tenant's quotas " +
			"(`ataila_tenant_quota`), and whether every order of it needs an operator's approval. This is the " +
			"operator's view; a tenant orders, and sees its price estimate, in the portal. Needs a token holding " +
			"`orders-read-global` (or `orders-admin-global`) and platform release " + client.ReleaseQuotasOrders +
			" or later.",
		Attributes: map[string]dschema.Attribute{
			"items": dataNestedList("The enabled items, by `sort_order`, then `key`.", catalogueItemTypes,
				func(p []string) string { return catalogueItemDocs[p[len(p)-1]] }),
		},
	}
}

func (d *catalogueItemsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *catalogueItemsDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.data == nil {
		resp.Diagnostics.AddError("Provider not configured", "The ataila provider was not configured.")
		return
	}
	if diagnostic := d.data.featureRefused(client.FeatureCatalogue, path.Root("items"), "The ataila_catalogue_items data source"); diagnostic != nil {
		resp.Diagnostics.Append(diagnostic)
		return
	}
	items, err := d.data.API.ListCatalogueItems(ctx)
	if err != nil {
		resp.Diagnostics.Append(ordersReadError("reading the catalogue (GET /catalogue)", err))
		return
	}
	ot := types.ObjectType{AttrTypes: catalogueItemTypes}
	elems := make([]attr.Value, 0, len(items))
	for _, it := range items {
		elems = append(elems, types.ObjectValueMust(catalogueItemTypes, map[string]attr.Value{
			"key": types.StringValue(it.Key), "kind": types.StringValue(it.Kind), "name": types.StringValue(it.Name),
			"edition": types.StringValue(it.Edition), "requires_approval": types.BoolValue(it.RequiresApproval),
			"spec_schema_json": jsonText(it.SpecSchema), "quota_dimensions_json": jsonText(it.QuotaDimensions),
			"price_hint_json": jsonText(it.PriceHint), "sort_order": types.Int64Value(it.SortOrder),
		}))
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &catalogueItemsModel{Items: types.ListValueMust(ot, elems)})...)
}

// ── orders ───────────────────────────────────────────────────────────────────

// orderTypes are the attributes of one order (the ataila_order data source
// adds `events`).
var orderTypes = map[string]attr.Type{
	"id":                  types.StringType,
	"tenant_id":           types.StringType,
	"tenant_name":         types.StringType,
	"customer_id":         types.StringType,
	"project_id":          types.StringType,
	"catalogue_item_key":  types.StringType,
	"catalogue_item_kind": types.StringType,
	"catalogue_item_name": types.StringType,
	"status":              types.StringType,
	"quota_result":        types.StringType,
	"quota_decision":      types.StringType,
	"requested_by":        types.StringType,
	"approved_by":         types.StringType,
	"approved_at":         types.StringType,
	"approval_reason":     types.StringType,
	"rejected_by":         types.StringType,
	"rejected_at":         types.StringType,
	"rejection_reason":    types.StringType,
	"key_id":              types.StringType,
	"key_alias":           types.StringType,
	"operation_id":        types.StringType,
	"spec_json":           types.StringType,
	"quota_check_json":    types.StringType,
	"overage_json":        types.StringType,
	"dispatch_json":       types.StringType,
	"created_at":          types.StringType,
	"updated_at":          types.StringType,
}

var orderEventTypes = map[string]attr.Type{
	"id":          types.StringType,
	"at":          types.StringType,
	"actor":       types.StringType,
	"event":       types.StringType,
	"detail_json": types.StringType,
}

var orderDocs = map[string]string{
	"id":                  "The order's id (a UUID).",
	"tenant_id":           "The tenant that placed it.",
	"tenant_name":         "The tenant's display name.",
	"customer_id":         "The tenant's customer when the order was placed; null when that customer is gone.",
	"project_id":          "The project the order is for, when its item takes one.",
	"catalogue_item_key":  "The catalogue item ordered (`key` in `ataila_catalogue_items`).",
	"catalogue_item_kind": client.Describe("CatalogueItem", "kind"),
	"catalogue_item_name": "The item's display name.",
	"status":              client.Describe("Order", "status"),
	"quota_result": "The quota check's result: `within`, `over` (the order went to a card) or " +
		"`refused_hard_cap`.",
	"quota_decision":   "The quota check's decision: `auto_approved`, `awaiting_approval` or `refused`.",
	"requested_by":     "The member who placed it (`id` of an `ataila_user`); null when that user no longer exists.",
	"approved_by":      "The operator who approved it; null for an automatic approval or none.",
	"approved_at":      "When it was approved: RFC 3339 in UTC; null if not.",
	"approval_reason":  "The operator's reason for approving it.",
	"rejected_by":      "The operator who rejected it; null if not.",
	"rejected_at":      "When it was rejected: RFC 3339 in UTC; null if not.",
	"rejection_reason": "The operator's reason for rejecting it.",
	"key_id": "For an AI gateway key order: the id of the key it delivered (`id` of an " +
		"`ataila_ai_gateway_key`, which can import it); null otherwise or before delivery.",
	"key_alias": "For an AI gateway key order: the alias of the key it delivered. Never the key's value: " +
		"an order does not hold it.",
	"operation_id":     "The order as an operation, `order:<id>`.",
	"spec_json":        "The order form as submitted, as JSON text.",
	"quota_check_json": "The quota check as recorded on the order, as JSON text: `result`, `decision`, `reasons` and, per dimension, `limit`, `policy`, `allocated`, `reserved`, `requested`, `total`, `remaining` and `result`.",
	"overage_json":     "A one-off overage an operator approved, as JSON text; null when none. It never raised the tenant's quota.",
	"dispatch_json":    "What delivering the order produced, as JSON text (`run_kind`, `run_id`, `key_id`, `key_alias`, `outcome`…); `{}` before delivery starts. Secret-looking members are null.",
	"created_at":       "When it was placed: RFC 3339 in UTC.",
	"updated_at":       "Its last change: RFC 3339 in UTC.",
	"events":           "The order's timeline, oldest first: `id`, `at`, `actor` (an e-mail address, or `system:<part>`), `event` and `detail_json`.",
	"detail_json":      "The entry's facts, as JSON text. Secret-looking members are null.",
	"at":               "When it happened: RFC 3339 in UTC.",
	"actor":            "Who: an e-mail address, or `system:<part>` when the platform did it.",
	"event":            client.Describe("OrderEvent", "event"),
}

func orderDoc(p []string) string {
	if len(p) == 2 && p[0] == "events" && p[1] == "id" {
		return "The entry's id; ids grow with time."
	}
	return orderDocs[p[len(p)-1]]
}

// orderValues are the attributes of one order.
func orderValues(o *client.OrderData) map[string]attr.Value {
	var qc struct {
		Result   string `json:"result"`
		Decision string `json:"decision"`
	}
	_ = json.Unmarshal(o.QuotaCheck, &qc)
	nullIfBlank := func(s string) types.String {
		if s == "" {
			return types.StringNull()
		}
		return types.StringValue(s)
	}
	dispatch := jsonText(o.Dispatch)
	if dispatch.IsNull() {
		dispatch = types.StringValue("{}")
	}
	return map[string]attr.Value{
		"id": types.StringValue(o.ID), "tenant_id": types.StringValue(o.TenantID),
		"tenant_name": stringOrNull(o.TenantName), "customer_id": stringOrNull(o.CustomerID),
		"project_id": stringOrNull(o.ProjectID), "catalogue_item_key": types.StringValue(o.CatalogueItemKey),
		"catalogue_item_kind": stringOrNull(o.CatalogueItemKind), "catalogue_item_name": stringOrNull(o.CatalogueItemName),
		"status": types.StringValue(o.Status), "quota_result": nullIfBlank(qc.Result),
		"quota_decision": nullIfBlank(qc.Decision), "requested_by": stringOrNull(o.RequestedBy),
		"approved_by": stringOrNull(o.ApprovedBy), "approved_at": timestampString(o.ApprovedAt),
		"approval_reason": stringOrNull(o.ApprovalReason), "rejected_by": stringOrNull(o.RejectedBy),
		"rejected_at": timestampString(o.RejectedAt), "rejection_reason": stringOrNull(o.RejectionReason),
		"key_id": jsonMember(o.Dispatch, "key_id"), "key_alias": jsonMember(o.Dispatch, "key_alias"),
		"operation_id": types.StringValue(o.OperationID), "spec_json": jsonText(o.Spec),
		"quota_check_json": jsonText(o.QuotaCheck), "overage_json": jsonText(o.Overage),
		"dispatch_json": dispatch, "created_at": types.StringValue(FormatTimestamp(o.CreatedAt)),
		"updated_at": types.StringValue(FormatTimestamp(o.UpdatedAt)),
	}
}

func orderEventsValue(events []client.OrderEventData) types.List {
	ot := types.ObjectType{AttrTypes: orderEventTypes}
	elems := make([]attr.Value, 0, len(events))
	for _, e := range events {
		detail := jsonText(e.Detail)
		if detail.IsNull() {
			detail = types.StringValue("{}")
		}
		elems = append(elems, types.ObjectValueMust(orderEventTypes, map[string]attr.Value{
			"id": types.StringValue(e.ID), "at": types.StringValue(FormatTimestamp(e.At)),
			"actor": types.StringValue(e.Actor), "event": types.StringValue(e.Event), "detail_json": detail,
		}))
	}
	return types.ListValueMust(ot, elems)
}

// ordersReadError explains a failed read of the catalogue or of orders.
func ordersReadError(doing string, err error) diag.Diagnostic {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.Code() == client.CodeTenantNotFound:
			return diag.NewErrorDiagnostic("No such tenant",
				fmt.Sprintf("While %s: the platform has no tenant with this id.\n\n%s", doing, apiErr.Detail()))
		case apiErr.Code() == client.CodeOrderNotFound:
			return diag.NewErrorDiagnostic("No such order",
				fmt.Sprintf("While %s: the platform has no order with this id.\n\n%s", doing, apiErr.Detail()))
		case (apiErr.StatusCode == 404 && (apiErr.Code() == "" || apiErr.Code() == "not_found")) || apiErr.StatusCode == 405:
			return diag.NewErrorDiagnostic("This platform does not serve orders",
				fmt.Sprintf("While %s: the platform answered HTTP %d. The catalogue and the order reads need "+
					"platform release %s or later.\n\n%s", doing, apiErr.StatusCode, client.ReleaseQuotasOrders,
					apiErr.Detail()))
		}
	}
	return apiError(doing, err)
}

type ordersDataSource struct {
	data *ProviderData
}

type ordersModel struct {
	TenantID types.String `tfsdk:"tenant_id"`
	Status   types.String `tfsdk:"status"`
	Orders   types.List   `tfsdk:"orders"`
}

func (d *ordersDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_orders"
}

func (d *ordersDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dschema.Schema{
		MarkdownDescription: "A tenant's orders, newest first (all pages), each with its quota check, its " +
			"decision and what delivering it produced. **Read only**: a tenant orders in the portal, and an " +
			"operator approves, rejects or re-runs an order in the portal; there is no order resource. Needs a " +
			"token holding `orders-read-global` (or `orders-admin-global`) and platform release " +
			client.ReleaseQuotasOrders + " or later.",
		Attributes: map[string]dschema.Attribute{
			"tenant_id": dschema.StringAttribute{
				MarkdownDescription: "The tenant whose orders to read.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.RegexMatches(rxUUID, "must be a tenant id")},
			},
			"status": dschema.StringAttribute{
				MarkdownDescription: "Only orders in this state: " + backtickList(orderStatuses) + ".",
				Optional:            true,
				Validators:          []validator.String{stringvalidator.OneOf(orderStatuses...)},
			},
			"orders": dataNestedList("The orders, newest first.", orderTypes, orderDoc),
		},
	}
}

func (d *ordersDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *ordersDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.data == nil {
		resp.Diagnostics.AddError("Provider not configured", "The ataila provider was not configured.")
		return
	}
	var cfg ordersModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if diagnostic := d.data.featureRefused(client.FeatureOrders, path.Root("tenant_id"), "The ataila_orders data source"); diagnostic != nil {
		resp.Diagnostics.Append(diagnostic)
		return
	}
	var statuses []string
	if s := cfg.Status.ValueString(); s != "" {
		statuses = []string{s}
	}
	tenantID := cfg.TenantID.ValueString()
	found, err := d.data.API.ListTenantOrders(ctx, tenantID, statuses)
	if err != nil {
		resp.Diagnostics.Append(ordersReadError("reading the orders of the tenant "+tenantID, err))
		return
	}
	ot := types.ObjectType{AttrTypes: orderTypes}
	elems := make([]attr.Value, 0, len(found))
	for i := range found {
		elems = append(elems, types.ObjectValueMust(orderTypes, orderValues(&found[i])))
	}
	cfg.Orders = types.ListValueMust(ot, elems)
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}

type orderDataSource struct {
	data *ProviderData
}

func (d *orderDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_order"
}

// orderDataTypes are the ataila_order data source's attributes.
func orderDataTypes() map[string]attr.Type {
	t := make(map[string]attr.Type, len(orderTypes)+1)
	for k, v := range orderTypes {
		t[k] = v
	}
	t["events"] = types.ListType{ElemType: types.ObjectType{AttrTypes: orderEventTypes}}
	return t
}

func (d *orderDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	t := orderDataTypes()
	delete(t, "id")
	attrs := dataNestedAttrs(t, orderDoc, nil)
	attrs["id"] = dschema.StringAttribute{
		MarkdownDescription: "The order's id (a UUID).",
		Required:            true,
		Validators:          []validator.String{stringvalidator.RegexMatches(rxUUID, "must be an order id")},
	}
	resp.Schema = dschema.Schema{
		MarkdownDescription: "One order, by id, with its timeline: who placed it, how the quota decided, who " +
			"approved or rejected it and why, and each step of its delivery. **Read only** (approval stays in " +
			"the portal). For an AI gateway key order, `key_id` names the key it delivered, never its value. " +
			"Needs a token holding `orders-read-global` (or `orders-admin-global`) and platform release " +
			client.ReleaseQuotasOrders + " or later.",
		Attributes: attrs,
	}
}

func (d *orderDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *orderDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.data == nil {
		resp.Diagnostics.AddError("Provider not configured", "The ataila provider was not configured.")
		return
	}
	var id types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("id"), &id)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if diagnostic := d.data.featureRefused(client.FeatureOrders, path.Root("id"), "The ataila_order data source"); diagnostic != nil {
		resp.Diagnostics.Append(diagnostic)
		return
	}
	o, err := d.data.API.GetOrder(ctx, id.ValueString())
	if err != nil {
		resp.Diagnostics.Append(ordersReadError("reading the order "+id.ValueString(), err))
		return
	}
	v := orderValues(o)
	// The configuration's spelling of the id (the platform answers in lower case).
	v["id"] = id
	v["events"] = orderEventsValue(o.Events)
	obj := types.ObjectValueMust(orderDataTypes(), v)
	for name, value := range obj.Attributes() {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(name), value)...)
	}
}

// backtickList is "`a`, `b` or `c`".
func backtickList(items []string) string {
	s := ""
	for i, it := range items {
		switch {
		case i == 0:
		case i == len(items)-1:
			s += " or "
		default:
			s += ", "
		}
		s += "`" + it + "`"
	}
	return s
}
