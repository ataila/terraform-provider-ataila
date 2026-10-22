// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"regexp"
	"strconv"

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

// The Feedback & Roadmap backlog (WAVE-2-PLAN C3; platform 1.0.260 on), read
// only. It answers on the platform that issues releases, to a token holding
// feedback-admin-global. GET /feedback/{n}/brief is never read: the platform
// records that fetch and moves the item to in_progress, which a plan must not.

var (
	_ datasource.DataSourceWithConfigure = (*feedbackItemsDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*feedbackItemDataSource)(nil)
)

const feedbackWhere = "Answers only on the platform that issues releases (elsewhere `feedback_unavailable`), to a " +
	"token holding `feedback-admin-global`, from platform release " + client.ReleaseFeedback + " on. Read-only: the " +
	"provider never fetches an item's brief, which would move the item to `in_progress`."

var rxISOWeek = regexp.MustCompile(`^[0-9]{4}-W[0-9]{2}$`)

var feedbackSummaryTypes = map[string]attr.Type{
	"number": types.Int64Type, "title": types.StringType, "public_title": types.StringType,
	"kind": types.StringType, "severity": types.StringType, "status": types.StringType,
	"status_reason": types.StringType, "customer_key": types.StringType, "customer_label": types.StringType,
	"source": types.StringType, "lang": types.StringType, "summary_en": types.StringType,
	"votes": types.Int64Type, "expected_week": types.StringType, "planned_release_id": types.StringType,
	"delivered_release_id": types.StringType, "delivered_release_seq": types.Int64Type,
	"delivered_build": types.StringType, "duplicate_of_number": types.Int64Type,
	"created_at": types.StringType, "updated_at": types.StringType,
}

var feedbackCommentTypes = map[string]attr.Type{
	"author_name": types.StringType, "body": types.StringType, "created_at": types.StringType,
	"internal": types.BoolType, "lang": types.StringType, "side": types.StringType,
}

var feedbackWeekMoveTypes = map[string]attr.Type{
	"at": types.StringType, "previous_week": types.StringType, "week": types.StringType, "reason": types.StringType,
}

var feedbackPageTypes = map[string]attr.Type{
	"key": types.StringType, "route": types.StringType, "title": types.StringType,
}

var feedbackContextTypes = map[string]attr.Type{
	"browser": types.StringType, "build": types.StringType, "edition": types.StringType, "locale": types.StringType,
	"release_id": types.StringType, "release_seq": types.Int64Type, "viewport": types.StringType,
}

// feedbackDoc describes a member from the contract's own text.
func feedbackDoc(schema string) nestedDoc {
	return func(p []string) string {
		name := p[len(p)-1]
		switch name {
		case "created_at", "updated_at", "at", "ready_at", "delivered_at", "released_at", "confirmed_at",
			"auto_close_at":
			return joinDoc(client.Describe(schema, name), "RFC 3339 in UTC.")
		}
		if len(p) > 1 {
			switch p[0] {
			case "comments":
				return client.Describe("FeedbackComment", name)
			case "week_history":
				return client.Describe("FeedbackWeekMove", name)
			case "page":
				return client.Describe("FeedbackPage", name)
			case "context":
				return client.Describe("FeedbackContext", name)
			}
		}
		return client.Describe(schema, name)
	}
}

func feedbackSummaryValue(s client.FeedbackSummary) attr.Value {
	return types.ObjectValueMust(feedbackSummaryTypes, map[string]attr.Value{
		"number": types.Int64Value(int64(s.Number)), "title": types.StringValue(s.Title),
		"public_title": stringOrNull(s.PublicTitle), "kind": types.StringValue(s.Kind),
		"severity": stringOrNull(s.Severity), "status": types.StringValue(s.Status),
		"status_reason": stringOrNull(s.StatusReason), "customer_key": types.StringValue(s.CustomerKey),
		"customer_label": types.StringValue(s.CustomerLabel), "source": types.StringValue(s.Source),
		"lang": types.StringValue(s.Lang), "summary_en": stringOrNull(s.SummaryEn),
		"votes": types.Int64Value(int64(s.Votes)), "expected_week": stringOrNull(s.ExpectedWeek),
		"planned_release_id":    stringOrNull(s.PlannedReleaseId),
		"delivered_release_id":  stringOrNull(s.DeliveredReleaseId),
		"delivered_release_seq": int64OrNull(s.DeliveredReleaseSeq), "delivered_build": stringOrNull(s.DeliveredBuild),
		"duplicate_of_number": int64OrNull(s.DuplicateOfNumber),
		"created_at":          types.StringValue(FormatTimestamp(s.CreatedAt)),
		"updated_at":          types.StringValue(FormatTimestamp(s.UpdatedAt)),
	})
}

// feedbackError explains the two refusals an operator meets first.
func feedbackError(doing string, err error) diag.Diagnostic {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && apiErr.Code() == "feedback_unavailable" {
		return diag.NewErrorDiagnostic("This platform does not hold the feedback backlog",
			"Only the platform that issues releases holds it; point the provider at that one.\n\n"+apiErr.Detail())
	}
	return apiError(doing, err)
}

// ── ataila_feedback_items ────────────────────────────────────────────────────

// NewFeedbackItemsDataSource is the factory for ataila_feedback_items.
func NewFeedbackItemsDataSource() datasource.DataSource { return &feedbackItemsDataSource{} }

type feedbackItemsDataSource struct{ data *ProviderData }

type feedbackItemsModel struct {
	Status   types.String `tfsdk:"status"`
	Kind     types.String `tfsdk:"kind"`
	Customer types.String `tfsdk:"customer"`
	Week     types.String `tfsdk:"week"`
	Source   types.String `tfsdk:"source"`
	Items    types.List   `tfsdk:"items"`
}

func (d *feedbackItemsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_feedback_items"
}

func (d *feedbackItemsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dschema.Schema{
		MarkdownDescription: "The Feedback & Roadmap backlog: every item filed, newest first, as the backlog lists " +
			"it. The filters combine. " + feedbackWhere,
		Attributes: map[string]dschema.Attribute{
			"status": dschema.StringAttribute{MarkdownDescription: "Only items in this status.", Optional: true,
				Validators: []validator.String{stringvalidator.OneOf("new", "triaged", "scheduled", "in_progress",
					"ready", "delivered", "closed", "rejected", "duplicate", "withdrawn")}},
			"kind": dschema.StringAttribute{MarkdownDescription: "Only items of this kind.", Optional: true,
				Validators: []validator.String{stringvalidator.OneOf("blocker_bug", "change", "new_feature")}},
			"customer": dschema.StringAttribute{MarkdownDescription: "Only this customer's items (its key).",
				Optional: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 200)}},
			"week": dschema.StringAttribute{MarkdownDescription: "Only items planned for this ISO week, e.g. `2026-W43`.",
				Optional: true, Validators: []validator.String{stringvalidator.RegexMatches(rxISOWeek, "must be an ISO week like 2026-W43")}},
			"source": dschema.StringAttribute{MarkdownDescription: "Only items from this source (`internal`, `island` " +
				"or `demo`); without it, demo items are left out.", Optional: true,
				Validators: []validator.String{stringvalidator.OneOf("internal", "island", "demo")}},
			"items": dataNestedList("The items, as the backlog lists them.", feedbackSummaryTypes,
				feedbackDoc("FeedbackSummary")),
		},
	}
}

func (d *feedbackItemsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *feedbackItemsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.data == nil {
		resp.Diagnostics.AddError("Provider not configured", "The ataila provider was not configured.")
		return
	}
	if diagnostic := d.data.featureRefused(client.FeatureFeedback, path.Root("items"),
		"The ataila_feedback_items data source"); diagnostic != nil {
		resp.Diagnostics.Append(diagnostic)
		return
	}
	var cfg feedbackItemsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	items, err := d.data.API.ListFeedback(ctx, client.FeedbackFilter{
		Status: cfg.Status.ValueString(), Kind: cfg.Kind.ValueString(), Customer: cfg.Customer.ValueString(),
		Week: cfg.Week.ValueString(), Source: cfg.Source.ValueString(),
	})
	if err != nil {
		resp.Diagnostics.Append(feedbackError("reading the feedback backlog (GET /feedback)", err))
		return
	}
	elems := make([]attr.Value, 0, len(items))
	for _, s := range items {
		elems = append(elems, feedbackSummaryValue(s))
	}
	cfg.Items = types.ListValueMust(types.ObjectType{AttrTypes: feedbackSummaryTypes}, elems)
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}

// ── ataila_feedback_item ─────────────────────────────────────────────────────

// NewFeedbackItemDataSource is the factory for ataila_feedback_item.
func NewFeedbackItemDataSource() datasource.DataSource { return &feedbackItemDataSource{} }

type feedbackItemDataSource struct{ data *ProviderData }

// feedbackItemTypes is everything of one item: the summary's members and the rest.
var feedbackItemTypes = func() map[string]attr.Type {
	m := map[string]attr.Type{
		"body": types.StringType, "acceptance_criteria": types.StringType, "submitter_name": types.StringType,
		"ticket_url": types.StringType, "ready_at": types.StringType, "delivered_at": types.StringType,
		"released_at": types.StringType, "confirmed_at": types.StringType, "auto_close_at": types.StringType,
		"page":         types.ObjectType{AttrTypes: feedbackPageTypes},
		"context":      types.ObjectType{AttrTypes: feedbackContextTypes},
		"comments":     types.ListType{ElemType: types.ObjectType{AttrTypes: feedbackCommentTypes}},
		"week_history": types.ListType{ElemType: types.ObjectType{AttrTypes: feedbackWeekMoveTypes}},
	}
	for k, v := range feedbackSummaryTypes {
		m[k] = v
	}
	return m
}()

func (d *feedbackItemDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_feedback_item"
}

func (d *feedbackItemDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := dataNestedAttrs(feedbackItemTypes, feedbackDoc("FeedbackItem"), nil)
	attrs["number"] = dschema.Int64Attribute{MarkdownDescription: "The item's number, `N` of `#N`.", Required: true}
	resp.Schema = dschema.Schema{
		MarkdownDescription: "One Feedback & Roadmap item in full: the report, the screen it was filed from, what " +
			"the reporter's installation ran, the thread (internal notes included) and every move of its planned " +
			"week. " + feedbackWhere,
		Attributes: attrs,
	}
}

func (d *feedbackItemDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *feedbackItemDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.data == nil {
		resp.Diagnostics.AddError("Provider not configured", "The ataila provider was not configured.")
		return
	}
	if diagnostic := d.data.featureRefused(client.FeatureFeedback, path.Root("number"),
		"The ataila_feedback_item data source"); diagnostic != nil {
		resp.Diagnostics.Append(diagnostic)
		return
	}
	var number types.Int64
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("number"), &number)...)
	if resp.Diagnostics.HasError() {
		return
	}
	n := int(number.ValueInt64())
	it, err := d.data.API.GetFeedback(ctx, n)
	if err != nil {
		resp.Diagnostics.Append(feedbackError("reading feedback item #"+strconv.Itoa(n), err))
		return
	}
	values := map[string]attr.Value{}
	summary := feedbackSummaryValue(client.FeedbackSummary{
		CreatedAt: it.CreatedAt, CustomerKey: it.CustomerKey, CustomerLabel: it.CustomerLabel,
		DeliveredBuild: it.DeliveredBuild, DeliveredReleaseId: it.DeliveredReleaseId,
		DeliveredReleaseSeq: it.DeliveredReleaseSeq, DuplicateOfNumber: it.DuplicateOfNumber,
		ExpectedWeek: it.ExpectedWeek, Kind: it.Kind, Lang: it.Lang, Number: it.Number,
		PlannedReleaseId: it.PlannedReleaseId, PublicTitle: it.PublicTitle, Severity: it.Severity,
		Source: it.Source, Status: it.Status, StatusReason: it.StatusReason, SummaryEn: it.SummaryEn,
		Title: it.Title, UpdatedAt: it.UpdatedAt, Votes: it.Votes,
	}).(types.Object)
	for k, v := range summary.Attributes() {
		values[k] = v
	}
	values["body"] = types.StringValue(it.Body)
	values["acceptance_criteria"] = stringOrNull(it.AcceptanceCriteria)
	values["submitter_name"] = stringOrNull(it.SubmitterName)
	values["ticket_url"] = stringOrNull(it.TicketUrl)
	values["ready_at"] = timestampString(it.ReadyAt)
	values["delivered_at"] = timestampString(it.DeliveredAt)
	values["released_at"] = timestampString(it.ReleasedAt)
	values["confirmed_at"] = timestampString(it.ConfirmedAt)
	values["auto_close_at"] = timestampString(it.AutoCloseAt)
	values["page"] = types.ObjectValueMust(feedbackPageTypes, map[string]attr.Value{
		"key": stringOrNull(it.Page.Key), "route": stringOrNull(it.Page.Route), "title": stringOrNull(it.Page.Title),
	})
	values["context"] = types.ObjectValueMust(feedbackContextTypes, map[string]attr.Value{
		"browser": stringOrNull(it.Context.Browser), "build": stringOrNull(it.Context.Build),
		"edition": stringOrNull(it.Context.Edition), "locale": stringOrNull(it.Context.Locale),
		"release_id": stringOrNull(it.Context.ReleaseId), "release_seq": int64OrNull(it.Context.ReleaseSeq),
		"viewport": stringOrNull(it.Context.Viewport),
	})
	comments := make([]attr.Value, 0, len(it.Comments))
	for _, c := range it.Comments {
		comments = append(comments, types.ObjectValueMust(feedbackCommentTypes, map[string]attr.Value{
			"author_name": types.StringValue(c.AuthorName), "body": types.StringValue(c.Body),
			"created_at": types.StringValue(FormatTimestamp(c.CreatedAt)), "internal": types.BoolValue(c.Internal),
			"lang": types.StringValue(c.Lang), "side": types.StringValue(c.Side),
		}))
	}
	values["comments"] = types.ListValueMust(types.ObjectType{AttrTypes: feedbackCommentTypes}, comments)
	moves := make([]attr.Value, 0, len(it.WeekHistory))
	for _, w := range it.WeekHistory {
		moves = append(moves, types.ObjectValueMust(feedbackWeekMoveTypes, map[string]attr.Value{
			"at": timestampString(w.At), "previous_week": stringOrNull(w.PreviousWeek),
			"week": stringOrNull(w.Week), "reason": stringOrNull(w.Reason),
		}))
	}
	values["week_history"] = types.ListValueMust(types.ObjectType{AttrTypes: feedbackWeekMoveTypes}, moves)
	for name, v := range values {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(name), v)...)
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("number"), number)...)
}
