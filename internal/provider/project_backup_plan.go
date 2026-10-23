// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
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
	_ resource.ResourceWithConfigure   = (*backupPlanResource)(nil)
	_ resource.ResourceWithImportState = (*backupPlanResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*backupPlanResource)(nil)
)

var rxBackupTime = regexp.MustCompile(`^([01]\d|2[0-3]):([0-5]\d)$`)

var (
	backupComponentTypes = map[string]attr.Type{"code": types.BoolType, "database": types.BoolType, "files": types.BoolType}
	backupEnvTypes       = map[string]attr.Type{
		"dev":  types.ObjectType{AttrTypes: backupComponentTypes},
		"uat":  types.ObjectType{AttrTypes: backupComponentTypes},
		"prod": types.ObjectType{AttrTypes: backupComponentTypes},
	}
	backupDailyTypes  = map[string]attr.Type{"enabled": types.BoolType, "keep": types.Int64Type, "at": types.StringType}
	backupWeeklyTypes = map[string]attr.Type{"enabled": types.BoolType, "keep": types.Int64Type, "day": types.StringType,
		"at": types.StringType}
	backupMonthlyTypes = map[string]attr.Type{"enabled": types.BoolType, "keep": types.Int64Type, "day": types.Int64Type,
		"at": types.StringType}
	backupYearlyTypes = map[string]attr.Type{"enabled": types.BoolType, "keep": types.Int64Type, "at": types.StringType}
	backupTierTypes   = map[string]attr.Type{
		"daily":   types.ObjectType{AttrTypes: backupDailyTypes},
		"weekly":  types.ObjectType{AttrTypes: backupWeeklyTypes},
		"monthly": types.ObjectType{AttrTypes: backupMonthlyTypes},
		"yearly":  types.ObjectType{AttrTypes: backupYearlyTypes},
	}
)

// NewBackupPlanResource is the factory for ataila_project_backup_plan.
func NewBackupPlanResource() resource.Resource { return &backupPlanResource{} }

type backupPlanResource struct {
	data *ProviderData
}

type backupPlanModel struct {
	ID              types.String `tfsdk:"id"`
	ProjectID       types.String `tfsdk:"project_id"`
	Enabled         types.Bool   `tfsdk:"enabled"`
	Environments    types.Object `tfsdk:"environments"`
	Tiers           types.Object `tfsdk:"tiers"`
	TiersOverridden types.Bool   `tfsdk:"tiers_overridden"`
	IsDefault       types.Bool   `tfsdk:"is_default"`
	Version         types.Int64  `tfsdk:"version"`
}

// ── wire <-> Terraform values ────────────────────────────────────────────────

func backupEnvsValue(e client.BackupEnvironments) types.Object {
	comps := func(c client.BackupComponents) attr.Value {
		return types.ObjectValueMust(backupComponentTypes, map[string]attr.Value{
			"code": types.BoolValue(c.Code), "database": types.BoolValue(c.Database), "files": types.BoolValue(c.Files)})
	}
	return types.ObjectValueMust(backupEnvTypes, map[string]attr.Value{
		"dev": comps(e.Dev), "uat": comps(e.Uat), "prod": comps(e.Prod)})
}

func backupTiersValue(t client.BackupTiers) types.Object {
	return types.ObjectValueMust(backupTierTypes, map[string]attr.Value{
		"daily": types.ObjectValueMust(backupDailyTypes, map[string]attr.Value{
			"enabled": types.BoolValue(t.Daily.Enabled), "keep": types.Int64Value(int64(t.Daily.Keep)),
			"at": types.StringValue(t.Daily.At)}),
		"weekly": types.ObjectValueMust(backupWeeklyTypes, map[string]attr.Value{
			"enabled": types.BoolValue(t.Weekly.Enabled), "keep": types.Int64Value(int64(t.Weekly.Keep)),
			"day": types.StringValue(string(t.Weekly.Day)), "at": stringOrNull(t.Weekly.At)}),
		"monthly": types.ObjectValueMust(backupMonthlyTypes, map[string]attr.Value{
			"enabled": types.BoolValue(t.Monthly.Enabled), "keep": types.Int64Value(int64(t.Monthly.Keep)),
			"day": types.Int64Value(int64(t.Monthly.Day)), "at": stringOrNull(t.Monthly.At)}),
		"yearly": types.ObjectValueMust(backupYearlyTypes, map[string]attr.Value{
			"enabled": types.BoolValue(t.Yearly.Enabled), "keep": types.Int64Value(int64(t.Yearly.Keep)),
			"at": stringOrNull(t.Yearly.At)}),
	})
}

func objAttrs(o types.Object, name string) map[string]attr.Value {
	return o.Attributes()[name].(types.Object).Attributes()
}

func attrBool(m map[string]attr.Value, k string) bool     { return m[k].(types.Bool).ValueBool() }
func attrInt(m map[string]attr.Value, k string) int       { return int(m[k].(types.Int64).ValueInt64()) }
func attrString(m map[string]attr.Value, k string) string { return m[k].(types.String).ValueString() }

func attrStringPtr(m map[string]attr.Value, k string) *string {
	s := m[k].(types.String)
	if s.IsNull() || s.IsUnknown() {
		return nil
	}
	v := s.ValueString()
	return &v
}

func backupEnvsWire(o types.Object) client.BackupEnvironments {
	comps := func(name string) client.BackupComponents {
		a := objAttrs(o, name)
		return client.BackupComponents{Code: attrBool(a, "code"), Database: attrBool(a, "database"),
			Files: attrBool(a, "files")}
	}
	return client.BackupEnvironments{Dev: comps("dev"), Uat: comps("uat"), Prod: comps("prod")}
}

func backupTiersWire(o types.Object) *client.BackupTiers {
	d, w, mo, y := objAttrs(o, "daily"), objAttrs(o, "weekly"), objAttrs(o, "monthly"), objAttrs(o, "yearly")
	return &client.BackupTiers{
		Daily: client.DailyTier{Enabled: attrBool(d, "enabled"), Keep: attrInt(d, "keep"), At: attrString(d, "at")},
		Weekly: client.WeeklyTier{Enabled: attrBool(w, "enabled"), Keep: attrInt(w, "keep"),
			Day: client.WeeklyTierDay(attrString(w, "day")), At: attrStringPtr(w, "at")},
		Monthly: client.MonthlyTier{Enabled: attrBool(mo, "enabled"), Keep: attrInt(mo, "keep"),
			Day: attrInt(mo, "day"), At: attrStringPtr(mo, "at")},
		Yearly: client.YearlyTier{Enabled: attrBool(y, "enabled"), Keep: attrInt(y, "keep"), At: attrStringPtr(y, "at")},
	}
}

// fromAPI fills the model from the platform's plan. environments stays null
// while it is null here and the plan has the default environments; tiers is
// null while the plan follows the installation's default tiers.
func (m *backupPlanModel) fromAPI(p *client.BackupPlan) {
	m.ID = types.StringValue(p.ProjectId)
	m.ProjectID = types.StringValue(p.ProjectId)
	m.Enabled = types.BoolValue(p.Enabled)
	if !(m.Environments.IsNull() && p.Environments == p.DefaultEnvironments) {
		m.Environments = backupEnvsValue(p.Environments)
	}
	if p.TiersOverridden {
		m.Tiers = backupTiersValue(p.Tiers)
	} else {
		m.Tiers = types.ObjectNull(backupTierTypes)
	}
	m.TiersOverridden = types.BoolValue(p.TiersOverridden)
	m.IsDefault = types.BoolValue(p.IsDefault)
	m.Version = types.Int64Value(int64(p.Version))
}

// body is the whole plan to send: null environments are the platform's default
// ones (as current reports them), null tiers follow the installation default.
func (m *backupPlanModel) body(current *client.BackupPlan) client.BackupPlanPut {
	b := client.BackupPlanPut{Enabled: m.Enabled.ValueBool(), Environments: current.DefaultEnvironments}
	if !m.Environments.IsNull() {
		b.Environments = backupEnvsWire(m.Environments)
	}
	if !m.Tiers.IsNull() {
		b.Tiers = backupTiersWire(m.Tiers)
	}
	return b
}

// ── schema ───────────────────────────────────────────────────────────────────

func (r *backupPlanResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project_backup_plan"
}

func backupComponentsAttr(env string) schema.SingleNestedAttribute {
	flag := func(desc string) schema.BoolAttribute {
		return schema.BoolAttribute{MarkdownDescription: desc, Required: true}
	}
	return schema.SingleNestedAttribute{
		MarkdownDescription: "What " + env + "'s sets carry.",
		Required:            true,
		Attributes: map[string]schema.Attribute{
			"code": flag("The code: every repository as a git bundle (on PROD, the released tag) and the " +
				"environment's deployment files, secrets as names only."),
			"database": flag("The environment's database."),
			"files":    flag("The environment's object storage buckets."),
		},
	}
}

func backupTierAttr(desc string, keepMax int64, day schema.Attribute, atRequired bool) schema.SingleNestedAttribute {
	attrs := map[string]schema.Attribute{
		"enabled": schema.BoolAttribute{
			MarkdownDescription: "Whether the tier runs; true when left out.",
			Optional:            true, Computed: true, Default: booldefault.StaticBool(true),
		},
		"keep": schema.Int64Attribute{
			MarkdownDescription: fmt.Sprintf("How many successful sets of the tier are kept per environment: 1-%d.", keepMax),
			Required:            true,
			Validators:          []validator.Int64{int64validator.Between(1, keepMax)},
		},
	}
	timeRule := stringvalidator.RegexMatches(rxBackupTime, "must be a time HH:MM, 00:00-23:59")
	if atRequired {
		attrs["at"] = schema.StringAttribute{
			MarkdownDescription: "When it runs: HH:MM, in the platform's backup time zone.",
			Required:            true, Validators: []validator.String{timeRule},
		}
	} else {
		attrs["at"] = schema.StringAttribute{
			MarkdownDescription: "When it runs: HH:MM, in the platform's backup time zone. Left out, it runs at " +
				"the daily tier's time.",
			Optional: true, Validators: []validator.String{timeRule},
		}
	}
	if day != nil {
		attrs["day"] = day
	}
	return schema.SingleNestedAttribute{MarkdownDescription: desc, Required: true, Attributes: attrs}
}

func (r *backupPlanResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A project's backup plan: whether the project is backed up, what each environment's " +
			"sets carry, and the tiers (how often, how many kept). Every project has a plan: a new one gets the " +
			"default (enabled; DEV code and database; PROD code, database and files; UAT none; the installation's " +
			"default tiers).\n\n" +
			"**Creating** this resource takes the project's plan over and replaces it with the configuration. " +
			"Leaving `environments` out means the default environments; leaving `tiers` out follows the " +
			"installation's default tiers, which then apply as they change (Settings › Backups). Setting `tiers` " +
			"gives the project its own, even when they equal the default.\n\n" +
			"**`enabled = false`** stops every scheduled set and is destroy-class: it needs `allow_destroy = true` " +
			"on the provider **and** a token created with destroy allowed. **Destroy** returns the plan to the " +
			"default, which never switches backups off and needs neither; the plan itself is never removed. On " +
			"a retired project destroy changes nothing.\n\n" +
			"Back up now, exports and restores are requested in the portal, never here. Needs " +
			"`project-backup-admin-global` (the read key reads); platform release 1.0.399 or later.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The project's id, as `project_id`.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"project_id": schema.StringAttribute{
				MarkdownDescription: "Id of the project. Changing it replaces the resource: the old project's plan " +
					"returns to the default.",
				Required:      true,
				Validators:    []validator.String{stringvalidator.RegexMatches(rxIntID, "must be a project id")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"enabled": schema.BoolAttribute{
				MarkdownDescription: "Whether the project is backed up; true when left out. `false` stops every " +
					"scheduled set (those already made are kept until they expire) and is destroy-class.",
				Optional: true, Computed: true, Default: booldefault.StaticBool(true),
			},
			"environments": schema.SingleNestedAttribute{
				MarkdownDescription: "What each environment's sets carry. Left out: the default (DEV code and " +
					"database, PROD all three, UAT none). An environment with nothing is not backed up; one that " +
					"is not deployed is skipped whatever it says.",
				Optional: true,
				Attributes: map[string]schema.Attribute{
					"dev": backupComponentsAttr("DEV"), "uat": backupComponentsAttr("UAT"),
					"prod": backupComponentsAttr("PROD"),
				},
			},
			"tiers": schema.SingleNestedAttribute{
				MarkdownDescription: "The project's own tiers. Left out, the plan follows the installation's " +
					"default tiers. A set made by one tier counts against that tier's `keep` only.",
				Optional: true,
				Attributes: map[string]schema.Attribute{
					"daily": backupTierAttr("Every day.", 366, nil, true),
					"weekly": backupTierAttr("Once a week.", 104, schema.StringAttribute{
						MarkdownDescription: "The day of the week: `mon` … `sun`.",
						Required:            true,
						Validators: []validator.String{
							stringvalidator.OneOf("mon", "tue", "wed", "thu", "fri", "sat", "sun")},
					}, false),
					"monthly": backupTierAttr("Once a month.", 120, schema.Int64Attribute{
						MarkdownDescription: "The day of the month: 1-28, so that every month has it.",
						Required:            true,
						Validators:          []validator.Int64{int64validator.Between(1, 28)},
					}, false),
					"yearly": backupTierAttr("Once a year, on 1 January.", 30, nil, false),
				},
			},
			"tiers_overridden": schema.BoolAttribute{
				MarkdownDescription: "True when the project has its own tiers (`tiers` set).",
				Computed:            true,
			},
			"is_default": schema.BoolAttribute{
				MarkdownDescription: "True while the plan is the default: enabled, the default environments and " +
					"the installation's default tiers followed.",
				Computed: true,
			},
			"version": schema.Int64Attribute{
				MarkdownDescription: "The plan's version on the platform, bumped by every change from the portal " +
					"or the API. The provider replaces the plan only at the version it last read.",
				Computed: true,
			},
		},
	}
}

func (r *backupPlanResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *backupPlanResource) configured(diags interface{ AddError(string, string) }) bool {
	if r.data == nil {
		diags.AddError("Provider not configured", "The ataila provider was not configured.")
		return false
	}
	return true
}

func backupOffRefused(projectID string) diag.Diagnostic {
	return diag.NewAttributeErrorDiagnostic(path.Root("enabled"), "Setting enabled = false switches the project's backups off",
		fmt.Sprintf("Switching the backups of project %s off stops every scheduled set, so it is destroy-class "+
			"and needs two switches, both on:\n"+
			"  1. allow_destroy = true in the provider \"ataila\" block (it is false by default);\n"+
			"  2. an API token minted in the portal with destroy allowed. The platform refuses it from any other "+
			"token.\n\nNothing was sent.", projectID))
}

// ModifyPlan refuses an older platform, and switching backups off while the
// provider's allow_destroy is off, before anything is sent.
func (r *backupPlanResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return // destroy: back to the default, never destroy-class
	}
	if dg := r.data.featureRefused(client.FeatureBackupPlans, path.Root("project_id"),
		"The ataila_project_backup_plan resource"); dg != nil {
		resp.Diagnostics.Append(dg)
		return
	}
	var plan backupPlanModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || plan.Enabled.IsUnknown() || plan.Enabled.ValueBool() ||
		r.data == nil || r.data.AllowDestroy {
		return
	}
	if !req.State.Raw.IsNull() {
		var state backupPlanModel
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if resp.Diagnostics.HasError() || !state.Enabled.ValueBool() {
			return // already off: not a switch
		}
	}
	resp.Diagnostics.Append(backupOffRefused(plan.ProjectID.ValueString()))
}

// replace reads the plan (for its default environments and version), then
// replaces it at the version given: the state's on update, the one just read
// on create.
func (r *backupPlanResource) replace(ctx context.Context, plan *backupPlanModel, stateVersion *int, diags *diag.Diagnostics) bool {
	pid := plan.ProjectID.ValueString()
	current, err := r.data.API.GetBackupPlan(ctx, pid)
	if err != nil {
		diags.Append(apiError(fmt.Sprintf("reading the backup plan of project %s", pid), err))
		return false
	}
	version := current.Version
	if stateVersion != nil {
		version = *stateVersion
	}
	got, err := r.data.API.PutBackupPlan(ctx, pid, plan.body(current), &version)
	if err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 412 {
			now, _ := apiErr.Extra("current_version")
			diags.AddError("The backup plan changed outside Terraform since the last read",
				fmt.Sprintf("The backup plan of project %s was changed outside this configuration after the provider "+
					"read it (it read version %d; the platform is at version %v), so the platform refused to replace "+
					"it. Run plan again to see the change, then apply. The provider does not retry.\n\n%s",
					pid, version, now, apiErr.Detail()))
			return false
		}
		if errors.As(err, &apiErr) && apiErr.Code() == "destroy_not_allowed" {
			diags.Append(backupOffRefused(pid))
			return false
		}
		diags.Append(apiError(fmt.Sprintf("replacing the backup plan of project %s", pid), err))
		return false
	}
	plan.fromAPI(got)
	return true
}

func (r *backupPlanResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan backupPlanModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || !r.replace(ctx, &plan, nil, &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *backupPlanResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var state backupPlanModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pid := state.ProjectID.ValueString()
	p, err := r.data.API.GetBackupPlan(ctx, pid)
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.Append(apiError(fmt.Sprintf("reading the backup plan of project %s", pid), err))
		return
	}
	state.fromAPI(p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *backupPlanResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan, state backupPlanModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	version := int(state.Version.ValueInt64())
	if !r.replace(ctx, &plan, &version, &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete returns the plan to the default; a project already gone is not an error.
func (r *backupPlanResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var state backupPlanModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pid := state.ProjectID.ValueString()
	if err := r.data.API.ResetBackupPlan(ctx, pid); err != nil && !isNotFound(err) {
		resp.Diagnostics.Append(apiError(fmt.Sprintf("returning the backup plan of project %s to the default", pid), err))
	}
}

func (r *backupPlanResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := strings.TrimSpace(req.ID)
	if !rxIntID.MatchString(id) {
		resp.Diagnostics.AddError("Cannot import the backup plan",
			fmt.Sprintf("The import id is the project's id (1 to 999999999), got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("environments"), types.ObjectNull(backupEnvTypes))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("tiers"), types.ObjectNull(backupTierTypes))...)
}
