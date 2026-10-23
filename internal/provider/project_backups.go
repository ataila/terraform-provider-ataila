// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"

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

var (
	_ datasource.DataSourceWithConfigure = (*backupSetsDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*backupExportsDataSource)(nil)
)

// defaultBackupSetLimit is how many sets a read keeps when `limit` is left out.
const defaultBackupSetLimit = 100

var (
	backupSetTypes = map[string]attr.Type{
		"id": types.StringType, "environment": types.StringType, "tier": types.StringType,
		"components": types.ListType{ElemType: types.StringType}, "status": types.StringType,
		"attempt": types.Int64Type, "bytes": types.Int64Type, "has_checksums": types.BoolType,
		"created_at": types.StringType, "started_at": types.StringType, "finished_at": types.StringType,
		"pruned_at": types.StringType, "error": types.StringType,
	}
	backupExportTypes = map[string]attr.Type{
		"id": types.StringType, "status": types.StringType, "environments": types.ListType{ElemType: types.StringType},
		"database": types.StringType, "files": types.BoolType,
		"source_set_ids": types.ListType{ElemType: types.StringType},
		"contents":       types.ListType{ElemType: types.StringType}, "bytes": types.Int64Type,
		"sha256": types.StringType, "expires_at": types.StringType, "created_at": types.StringType,
		"started_at": types.StringType, "finished_at": types.StringType, "error": types.StringType,
	}
)

func backupProjectAttr() dschema.StringAttribute {
	return dschema.StringAttribute{
		MarkdownDescription: "Id of the project.",
		Required:            true,
		Validators:          []validator.String{stringvalidator.RegexMatches(rxIntID, "must be a project id")},
	}
}

// ── ataila_project_backup_sets ───────────────────────────────────────────────

// NewBackupSetsDataSource is the factory for ataila_project_backup_sets.
func NewBackupSetsDataSource() datasource.DataSource { return &backupSetsDataSource{} }

type backupSetsDataSource struct {
	data *ProviderData
}

type backupSetsModel struct {
	ProjectID   types.String `tfsdk:"project_id"`
	Environment types.String `tfsdk:"environment"`
	Tier        types.String `tfsdk:"tier"`
	Status      types.String `tfsdk:"status"`
	Limit       types.Int64  `tfsdk:"limit"`
	Sets        types.List   `tfsdk:"sets"`
	More        types.Bool   `tfsdk:"more"`
}

func (d *backupSetsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project_backup_sets"
}

func (d *backupSetsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	out := func(desc string) dschema.StringAttribute {
		return dschema.StringAttribute{MarkdownDescription: desc, Computed: true}
	}
	resp.Schema = dschema.Schema{
		MarkdownDescription: "A project's backup sets, newest first: what each run copied of one environment. A " +
			"project keeps many sets, so a read keeps at most `limit` (100 when left out); `more` says whether " +
			"further sets matched. Where a set is stored and its manifest stay in the portal.\n\n" +
			"Needs `project-backup-read-global` (or the admin key); a tenant-scoped token reads its own tenant's " +
			"projects. Platform release 1.0.399 or later.",
		Attributes: map[string]dschema.Attribute{
			"project_id": backupProjectAttr(),
			"environment": dschema.StringAttribute{
				MarkdownDescription: "Only this environment's sets: `dev`, `uat` or `prod`.",
				Optional:            true,
				Validators:          []validator.String{stringvalidator.OneOf("dev", "uat", "prod")},
			},
			"tier": dschema.StringAttribute{
				MarkdownDescription: "Only the sets of this tier: `daily`, `weekly`, `monthly`, `yearly` or `manual` " +
					"(Back up now).",
				Optional:   true,
				Validators: []validator.String{stringvalidator.OneOf("daily", "weekly", "monthly", "yearly", "manual")},
			},
			"status": dschema.StringAttribute{
				MarkdownDescription: "Only the sets in this status: `queued`, `running`, `success`, `failed` or " +
					"`pruned`.",
				Optional: true,
				Validators: []validator.String{
					stringvalidator.OneOf("queued", "running", "success", "failed", "pruned")},
			},
			"limit": dschema.Int64Attribute{
				MarkdownDescription: "The most sets to keep, newest first: 1-1000; 100 when left out.",
				Optional:            true,
				Validators:          []validator.Int64{int64validator.Between(1, 1000)},
			},
			"sets": dschema.ListNestedAttribute{
				MarkdownDescription: "The matching sets, newest first.",
				Computed:            true,
				NestedObject: dschema.NestedAttributeObject{Attributes: map[string]dschema.Attribute{
					"id":          out("The set's id."),
					"environment": out("`dev`, `uat` or `prod`."),
					"tier":        out("The tier that made it, or `manual`."),
					"components": dschema.ListAttribute{MarkdownDescription: "What it carries: `code`, " +
						"`database`, `files`.", ElementType: types.StringType, Computed: true},
					"status": out("`queued`, `running`, `success`, `failed`, or `pruned` (a success removed by " +
						"retention)."),
					"attempt": dschema.Int64Attribute{MarkdownDescription: "The run's attempt number: above 1 " +
						"after a retry.", Computed: true},
					"bytes": dschema.Int64Attribute{MarkdownDescription: "The set's size in bytes, once it " +
						"finished.", Computed: true},
					"has_checksums": dschema.BoolAttribute{MarkdownDescription: "True when the set was completed " +
						"with its checksum list, written last: a set without one is not complete.", Computed: true},
					"created_at":  out("When the set was requested (RFC 3339, UTC)."),
					"started_at":  out("When its run started."),
					"finished_at": out("When its run finished."),
					"pruned_at":   out("When retention removed it."),
					"error":       out("Why it failed, as the run reported it."),
				}},
			},
			"more": dschema.BoolAttribute{
				MarkdownDescription: "True when more sets matched than `limit` kept.",
				Computed:            true,
			},
		},
	}
}

func (d *backupSetsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *backupSetsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.data == nil {
		resp.Diagnostics.AddError("Provider not configured", "The ataila provider was not configured.")
		return
	}
	var m backupSetsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if dg := d.data.featureRefused(client.FeatureBackupPlans, path.Root("project_id"),
		"The ataila_project_backup_sets data source"); dg != nil {
		resp.Diagnostics.Append(dg)
		return
	}
	limit := defaultBackupSetLimit
	if !m.Limit.IsNull() {
		limit = int(m.Limit.ValueInt64())
	}
	pid := m.ProjectID.ValueString()
	sets, more, err := d.data.API.ListBackupSets(ctx, pid, client.BackupSetFilter{
		Environment: m.Environment.ValueString(), Tier: m.Tier.ValueString(), Status: m.Status.ValueString()}, limit)
	if err != nil {
		resp.Diagnostics.Append(apiError(fmt.Sprintf("reading the backup sets of project %s", pid), err))
		return
	}
	elems := make([]attr.Value, 0, len(sets))
	for _, s := range sets {
		elems = append(elems, types.ObjectValueMust(backupSetTypes, map[string]attr.Value{
			"id": types.StringValue(s.Id), "environment": types.StringValue(s.Environment),
			"tier": types.StringValue(s.Tier), "components": stringsValue(s.Components),
			"status": types.StringValue(s.Status), "attempt": types.Int64Value(int64(s.Attempt)),
			"bytes": int64OrNull(s.Bytes), "has_checksums": types.BoolValue(s.HasChecksums),
			"created_at": types.StringValue(FormatTimestamp(s.CreatedAt)), "started_at": timestampString(s.StartedAt),
			"finished_at": timestampString(s.FinishedAt), "pruned_at": timestampString(s.PrunedAt),
			"error": stringOrNull(s.Error),
		}))
	}
	m.Sets = types.ListValueMust(types.ObjectType{AttrTypes: backupSetTypes}, elems)
	m.More = types.BoolValue(more)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	if more {
		resp.Diagnostics.AddWarning("More backup sets matched",
			fmt.Sprintf("Project %s has more sets matching these filters than limit = %d kept (the newest %d are "+
				"here). Narrow the filters or raise limit (at most 1000).", pid, limit, len(sets)))
	}
}

// ── ataila_project_backup_exports ────────────────────────────────────────────

// NewBackupExportsDataSource is the factory for ataila_project_backup_exports.
func NewBackupExportsDataSource() datasource.DataSource { return &backupExportsDataSource{} }

type backupExportsDataSource struct {
	data *ProviderData
}

type backupExportsModel struct {
	ProjectID types.String `tfsdk:"project_id"`
	Status    types.String `tfsdk:"status"`
	Exports   types.List   `tfsdk:"exports"`
}

func (d *backupExportsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project_backup_exports"
}

func (d *backupExportsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	out := func(desc string) dschema.StringAttribute {
		return dschema.StringAttribute{MarkdownDescription: desc, Computed: true}
	}
	list := func(desc string) dschema.ListAttribute {
		return dschema.ListAttribute{MarkdownDescription: desc, ElementType: types.StringType, Computed: true}
	}
	resp.Schema = dschema.Schema{
		MarkdownDescription: "A project's exports, newest first: downloadable packages built from its sets. An " +
			"export is requested, and its package downloaded, in the portal only; nothing here carries a download " +
			"link. Exports expire within days, so the list stays short.\n\n" +
			"Needs `project-backup-read-global` (or the admin key); a tenant-scoped token reads its own tenant's " +
			"projects. Platform release 1.0.399 or later.",
		Attributes: map[string]dschema.Attribute{
			"project_id": backupProjectAttr(),
			"status": dschema.StringAttribute{
				MarkdownDescription: "Only the exports in this status: `queued`, `running`, `success`, `failed` or " +
					"`expired`.",
				Optional: true,
				Validators: []validator.String{
					stringvalidator.OneOf("queued", "running", "success", "failed", "expired")},
			},
			"exports": dschema.ListNestedAttribute{
				MarkdownDescription: "The matching exports, newest first.",
				Computed:            true,
				NestedObject: dschema.NestedAttributeObject{Attributes: map[string]dschema.Attribute{
					"id":           out("The export's id."),
					"status":       out("`queued`, `running`, `success`, `failed`, or `expired`."),
					"environments": list("The environments it carries."),
					"database": out("What it carries of each database: `schema`, `data` (schema and data) or " +
						"`both` (those and a full dump)."),
					"files": dschema.BoolAttribute{MarkdownDescription: "Whether it carries the object storage " +
						"buckets.", Computed: true},
					"source_set_ids": list("The sets it was built from."),
					"contents":       list("What is inside the package, one line per part."),
					"bytes": dschema.Int64Attribute{MarkdownDescription: "The package's size in bytes, once it " +
						"finished.", Computed: true},
					"sha256":      out("The package's SHA-256, once it finished."),
					"expires_at":  out("When the download window closes (RFC 3339, UTC)."),
					"created_at":  out("When the export was requested."),
					"started_at":  out("When its run started."),
					"finished_at": out("When its run finished."),
					"error":       out("Why it failed, as the run reported it."),
				}},
			},
		},
	}
}

func (d *backupExportsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *backupExportsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.data == nil {
		resp.Diagnostics.AddError("Provider not configured", "The ataila provider was not configured.")
		return
	}
	var m backupExportsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if dg := d.data.featureRefused(client.FeatureBackupPlans, path.Root("project_id"),
		"The ataila_project_backup_exports data source"); dg != nil {
		resp.Diagnostics.Append(dg)
		return
	}
	pid := m.ProjectID.ValueString()
	exports, err := d.data.API.ListBackupExports(ctx, pid, m.Status.ValueString())
	if err != nil {
		resp.Diagnostics.Append(apiError(fmt.Sprintf("reading the exports of project %s", pid), err))
		return
	}
	elems := make([]attr.Value, 0, len(exports))
	for _, e := range exports {
		elems = append(elems, types.ObjectValueMust(backupExportTypes, map[string]attr.Value{
			"id": types.StringValue(e.Id), "status": types.StringValue(e.Status),
			"environments": stringsValue(e.Environments), "database": types.StringValue(e.Database),
			"files": types.BoolValue(e.Files), "source_set_ids": stringsValue(e.SourceSetIds),
			"contents": stringsValue(e.Contents), "bytes": int64OrNull(e.Bytes), "sha256": stringOrNull(e.Sha256),
			"expires_at": timestampString(e.ExpiresAt), "created_at": types.StringValue(FormatTimestamp(e.CreatedAt)),
			"started_at": timestampString(e.StartedAt), "finished_at": timestampString(e.FinishedAt),
			"error": stringOrNull(e.Error),
		}))
	}
	m.Exports = types.ListValueMust(types.ObjectType{AttrTypes: backupExportTypes}, elems)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
