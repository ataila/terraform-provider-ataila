// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ataila/terraform-provider-ataila/internal/client"
)

var _ datasource.DataSourceWithConfigure = (*auditEventsDataSource)(nil)

// defaultAuditLimit is how many events a read keeps when `limit` is left out.
const defaultAuditLimit = 100

var rxRFC3339 = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$`)

var auditEventTypes = map[string]attr.Type{
	"id": types.StringType, "occurred_at": types.StringType, "actor": types.StringType, "action": types.StringType,
	"entity_type": types.StringType, "entity_id": types.StringType, "auth_kind": types.StringType,
	"token_id": types.StringType, "request_id": types.StringType, "source_ip": types.StringType,
	"tenant_id": types.StringType, "before_state": types.StringType, "after_state": types.StringType,
}

// NewAuditEventsDataSource is the factory for ataila_audit_events.
func NewAuditEventsDataSource() datasource.DataSource { return &auditEventsDataSource{} }

type auditEventsDataSource struct {
	data *ProviderData
}

type auditEventsModel struct {
	Action        types.String `tfsdk:"action"`
	Actor         types.String `tfsdk:"actor"`
	AuthKind      types.String `tfsdk:"auth_kind"`
	TokenID       types.String `tfsdk:"token_id"`
	RequestID     types.String `tfsdk:"request_id"`
	EntityType    types.String `tfsdk:"entity_type"`
	EntityID      types.String `tfsdk:"entity_id"`
	TenantID      types.String `tfsdk:"tenant_id"`
	OccurredFrom  types.String `tfsdk:"occurred_from"`
	OccurredTo    types.String `tfsdk:"occurred_to"`
	Limit         types.Int64  `tfsdk:"limit"`
	IncludeStates types.Bool   `tfsdk:"include_states"`
	Events        types.List   `tfsdk:"events"`
	More          types.Bool   `tfsdk:"more"`
}

func (d *auditEventsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_audit_events"
}

func (d *auditEventsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	filter := func(desc string, v ...validator.String) dschema.StringAttribute {
		return dschema.StringAttribute{MarkdownDescription: desc, Optional: true, Validators: v}
	}
	out := func(desc string) dschema.StringAttribute {
		return dschema.StringAttribute{MarkdownDescription: desc, Computed: true}
	}
	instant := stringvalidator.RegexMatches(rxRFC3339, "must be an RFC 3339 time such as 2026-10-23T00:00:00Z")
	resp.Schema = dschema.Schema{
		MarkdownDescription: "The platform's audit log: every recorded change, newest first, narrowed by the " +
			"filters below. The log grows with every change, so a read keeps at most `limit` events (100 when " +
			"left out); `more` says whether further events matched. The entity before and after each change are " +
			"left out unless `include_states` is true, which keeps the state small and keeps what the writers " +
			"recorded (names and addresses among it) out of the state file by default.\n\n" +
			"Needs `audit-read-global` (or an admin key). A tenant-scoped token reads the events of its own " +
			"tenant only. Every read is evaluated at plan time: two plans can show different events.",
		Attributes: map[string]dschema.Attribute{
			"action": filter("Exact action, or a prefix when it ends in `*` (`user.*`).", stringvalidator.LengthBetween(1, 200)),
			"actor": filter("Exact actor: a person's e-mail address (compared lower-cased), `system` or `pipeline:<run id>`.",
				stringvalidator.LengthBetween(1, 320)),
			"auth_kind": filter("How the actor authenticated: `session`, `pat`, `service_account` or `ci`.",
				stringvalidator.OneOf("session", "pat", "service_account", "ci")),
			"token_id": filter("Events made with this API token (its id).",
				stringvalidator.RegexMatches(rxUUID, "must be a token id")),
			"request_id":  filter("Events of one request (`X-Request-ID`).", stringvalidator.LengthBetween(1, 200)),
			"entity_type": filter("Exact entity type, e.g. `users`.", stringvalidator.LengthBetween(1, 200)),
			"entity_id":   filter("Exact entity id, as the writer spelled it.", stringvalidator.LengthBetween(1, 500)),
			"tenant_id": filter("Events attributed to this tenant.",
				stringvalidator.RegexMatches(rxUUID, "must be a tenant id")),
			"occurred_from": filter("Events at or after this time (RFC 3339).", instant),
			"occurred_to":   filter("Events before this time (RFC 3339, exclusive).", instant),
			"limit": dschema.Int64Attribute{
				MarkdownDescription: "The most events to keep, newest first: 1-1000; 100 when left out.",
				Optional:            true,
				Validators:          []validator.Int64{int64validator.Between(1, 1000)},
			},
			"include_states": dschema.BoolAttribute{
				MarkdownDescription: "Also keep each event's `before_state` and `after_state` (JSON); false when left out.",
				Optional:            true,
			},
			"events": dschema.ListNestedAttribute{
				MarkdownDescription: "The matching events, newest first.",
				Computed:            true,
				NestedObject: dschema.NestedAttributeObject{Attributes: map[string]dschema.Attribute{
					"id":           out("The event's id: a positive integer as a string."),
					"occurred_at":  out("When the change was recorded (RFC 3339, UTC)."),
					"actor":        out("Who made the change: an e-mail address as it was then, `system` or `pipeline:<run id>`."),
					"action":       out("What was done, e.g. `user.create`."),
					"entity_type":  out("The kind of thing changed."),
					"entity_id":    out("Which one, as the writer spelled its id; null when none was recorded."),
					"auth_kind":    out("`session`, `pat`, `service_account` or `ci`; null when not recorded."),
					"token_id":     out("The API token used, for `pat` and `service_account`."),
					"request_id":   out("The request's `X-Request-ID`, when recorded."),
					"source_ip":    out("The client address as the platform saw it, when recorded."),
					"tenant_id":    out("The tenant the event is attributed to, when its writer recorded one."),
					"before_state": out("The entity before the change, as JSON; null unless `include_states` (and on a create)."),
					"after_state":  out("The entity after the change, as JSON; null unless `include_states` (and on a delete)."),
				}},
			},
			"more": dschema.BoolAttribute{
				MarkdownDescription: "True when more events matched than `limit` kept.",
				Computed:            true,
			},
		},
	}
}

func (d *auditEventsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

// jsonOrNull renders a recorded state as compact JSON; null when absent.
func jsonOrNull(v any) types.String {
	if v == nil {
		return types.StringNull()
	}
	b, err := json.Marshal(v)
	if err != nil {
		return types.StringNull()
	}
	return types.StringValue(string(b))
}

func instantOrNil(v types.String, at path.Path, diags interface {
	AddAttributeError(path.Path, string, string)
}) *time.Time {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	t, err := time.Parse(time.RFC3339, v.ValueString())
	if err != nil {
		diags.AddAttributeError(at, "Not an RFC 3339 time", err.Error())
		return nil
	}
	return &t
}

func (d *auditEventsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.data == nil {
		resp.Diagnostics.AddError("Provider not configured", "The ataila provider was not configured.")
		return
	}
	var m auditEventsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	f := client.AuditFilter{
		Action: m.Action.ValueString(), Actor: m.Actor.ValueString(), AuthKind: m.AuthKind.ValueString(),
		TokenID: m.TokenID.ValueString(), RequestID: m.RequestID.ValueString(),
		EntityType: m.EntityType.ValueString(), EntityID: m.EntityID.ValueString(),
		TenantID: m.TenantID.ValueString(),
		From:     instantOrNil(m.OccurredFrom, path.Root("occurred_from"), &resp.Diagnostics),
		To:       instantOrNil(m.OccurredTo, path.Root("occurred_to"), &resp.Diagnostics),
	}
	if resp.Diagnostics.HasError() {
		return
	}
	limit := defaultAuditLimit
	if !m.Limit.IsNull() {
		limit = int(m.Limit.ValueInt64())
	}
	states := m.IncludeStates.ValueBool()
	events, more, err := d.data.API.ListAuditEvents(ctx, f, limit)
	if err != nil {
		resp.Diagnostics.Append(apiError("reading the audit log", err))
		return
	}
	elems := make([]attr.Value, 0, len(events))
	for _, e := range events {
		before, after := types.StringNull(), types.StringNull()
		if states {
			before, after = jsonOrNull(e.BeforeState), jsonOrNull(e.AfterState)
		}
		elems = append(elems, types.ObjectValueMust(auditEventTypes, map[string]attr.Value{
			"id": types.StringValue(e.Id), "occurred_at": types.StringValue(FormatTimestamp(e.OccurredAt)),
			"actor": types.StringValue(e.Actor), "action": types.StringValue(e.Action),
			"entity_type": types.StringValue(e.EntityType), "entity_id": stringOrNull(e.EntityId),
			"auth_kind": stringOrNull(e.AuthKind), "token_id": stringOrNull(e.TokenId),
			"request_id": stringOrNull(e.RequestId), "source_ip": stringOrNull(e.SourceIp),
			"tenant_id": stringOrNull(e.TenantId), "before_state": before, "after_state": after,
		}))
	}
	m.Events = types.ListValueMust(types.ObjectType{AttrTypes: auditEventTypes}, elems)
	m.More = types.BoolValue(more)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	if more {
		resp.Diagnostics.AddWarning("More audit events matched",
			fmt.Sprintf("The audit log holds more events matching these filters than limit = %d kept (the newest "+
				"%d are here). Narrow the filters or raise limit (at most 1000).", limit, len(events)))
	}
}
