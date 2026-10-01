// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/ataila/terraform-provider-ataila/internal/client"
)

var (
	_ datasource.DataSourceWithConfigure        = (*projectDataSource)(nil)
	_ datasource.DataSourceWithConfigValidators = (*projectDataSource)(nil)
	_ datasource.DataSourceWithConfigure        = (*projectsDataSource)(nil)
	_ datasource.DataSourceWithConfigure        = (*projectStagesDataSource)(nil)
)

// ── ataila_project ───────────────────────────────────────────────────────────

// NewProjectDataSource is the factory for the ataila_project data source.
func NewProjectDataSource() datasource.DataSource { return &projectDataSource{} }

type projectDataSource struct {
	data *ProviderData
}

func (d *projectDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}

func projectDataAttributes() map[string]schema.Attribute {
	c := func(name string) schema.StringAttribute {
		return schema.StringAttribute{MarkdownDescription: projectDocs[name], Computed: true}
	}
	attrs := map[string]schema.Attribute{
		"id": schema.StringAttribute{
			MarkdownDescription: "Project id. Give `id` or `short_name`.",
			Optional:            true,
			Computed:            true,
			Validators:          []validator.String{stringvalidator.RegexMatches(rxIntID, "must be a project id")},
		},
		"short_name": schema.StringAttribute{
			MarkdownDescription: "Short name of the project. Give `id` or `short_name`.",
			Optional:            true,
			Computed:            true,
		},
		"tenant_id":          c("tenant_id"),
		"customer_id":        c("customer_id"),
		"project_index":      schema.Int64Attribute{MarkdownDescription: projectDocs["project_index"], Computed: true},
		"gitlab_repo_slug":   c("gitlab_repo_slug"),
		"primary_domain":     c("primary_domain"),
		"deployment_backend": c("deployment_backend"),
		"network_only":       schema.BoolAttribute{MarkdownDescription: projectDocs["network_only"], Computed: true},
		"status":             c("status"),
		"is_self":            schema.BoolAttribute{MarkdownDescription: projectDocs["is_self"], Computed: true},
		"registered_by":      c("registered_by"),
		"created_at": schema.StringAttribute{
			MarkdownDescription: projectDocs["created_at"], CustomType: TimestampType{}, Computed: true,
		},
		"stale_stages": schema.ListAttribute{MarkdownDescription: projectDocs["stale_stages"], Computed: true, ElementType: types.StringType},
		"urls": schema.SingleNestedAttribute{
			MarkdownDescription: projectDocs["urls"],
			Computed:            true,
			Attributes: map[string]schema.Attribute{
				"static":   schema.StringAttribute{Computed: true, MarkdownDescription: "The web site (`www.`)."},
				"frontend": schema.StringAttribute{Computed: true, MarkdownDescription: "The application front end (`app.`)."},
				"backend":  schema.StringAttribute{Computed: true, MarkdownDescription: "The application API (`api.`)."},
				"ai":       schema.StringAttribute{Computed: true, MarkdownDescription: "The AI endpoint (`ai.`)."},
			},
		},
		"harbor_namespace":      c("harbor_namespace"),
		"gitlab_repositories":   schema.ListAttribute{MarkdownDescription: projectDocs["gitlab_repositories"], Computed: true, ElementType: types.ObjectType{AttrTypes: projectRepoTypes}},
		"kubernetes_namespaces": schema.ListAttribute{MarkdownDescription: projectDocs["kubernetes_namespaces"], Computed: true, ElementType: types.ObjectType{AttrTypes: projectNSTypes}},
		"vault_paths":           schema.ListAttribute{MarkdownDescription: projectDocs["vault_paths"], Computed: true, ElementType: types.ObjectType{AttrTypes: projectVaultTypes}},
	}
	for _, s := range projectSettings {
		switch s.kind {
		case kindBool:
			attrs[s.name] = schema.BoolAttribute{MarkdownDescription: s.doc, Computed: true}
		case kindInt:
			attrs[s.name] = schema.Int64Attribute{MarkdownDescription: s.doc, Computed: true}
		default:
			attrs[s.name] = schema.StringAttribute{MarkdownDescription: s.doc, Computed: true}
		}
	}
	return attrs
}

func (d *projectDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "One project, by `id` or by `short_name`, with its settings, its outputs and " +
			"its stale provisioning stages. The platform's own projects (`is_self`) can be read too.",
		Attributes: projectDataAttributes(),
	}
}

func (d *projectDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("short_name")),
	}
}

func (d *projectDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *projectDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.data == nil {
		resp.Diagnostics.AddError("Provider not configured", "The ataila provider was not configured.")
		return
	}
	var cfg types.Object
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	given := cfg.Attributes()
	id := strAttr(given, "id")
	if id == "" {
		short := strAttr(given, "short_name")
		found, err := d.data.API.ListProjects(ctx, client.ProjectFilter{ShortName: short})
		if err != nil {
			summary, detail := apiErrorText("the projects (GET /projects)", err)
			resp.Diagnostics.AddError(summary, detail)
			return
		}
		if len(found) != 1 {
			resp.Diagnostics.AddError("Project not found",
				fmt.Sprintf("%d projects have short_name %q; exactly one is needed.", len(found), short))
			return
		}
		id = found[0].String("id")
	}
	p, err := d.data.API.GetProject(ctx, id)
	if err != nil {
		summary, detail := apiErrorText("the project with id "+id, err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	prov, err := d.data.API.GetProvisioning(ctx, id)
	if err != nil {
		summary, detail := apiErrorText("the provisioning of the project with id "+id, err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	values := projectValues(p, nil, prov.StaleStages)
	resp.Diagnostics.Append(resp.State.Set(ctx, objectFrom(ctx, cfg.Type(ctx).(basetypes.ObjectType), values, &resp.Diagnostics))...)
}

// ── ataila_projects ──────────────────────────────────────────────────────────

// NewProjectsDataSource is the factory for the ataila_projects data source.
func NewProjectsDataSource() datasource.DataSource { return &projectsDataSource{} }

type projectsDataSource struct {
	data *ProviderData
}

type projectSummaryModel struct {
	ID                types.String   `tfsdk:"id"`
	TenantID          types.String   `tfsdk:"tenant_id"`
	CustomerID        types.String   `tfsdk:"customer_id"`
	ProjectIndex      types.Int64    `tfsdk:"project_index"`
	ShortName         types.String   `tfsdk:"short_name"`
	LongName          types.String   `tfsdk:"long_name"`
	PrimaryDomain     types.String   `tfsdk:"primary_domain"`
	GitlabRepoSlug    types.String   `tfsdk:"gitlab_repo_slug"`
	DeploymentBackend types.String   `tfsdk:"deployment_backend"`
	NetworkOnly       types.Bool     `tfsdk:"network_only"`
	Status            types.String   `tfsdk:"status"`
	IsSelf            types.Bool     `tfsdk:"is_self"`
	CreatedAt         TimestampValue `tfsdk:"created_at"`
}

type projectsModel struct {
	TenantID   types.String          `tfsdk:"tenant_id"`
	CustomerID types.String          `tfsdk:"customer_id"`
	Status     types.String          `tfsdk:"status"`
	ShortName  types.String          `tfsdk:"short_name"`
	Projects   []projectSummaryModel `tfsdk:"projects"`
}

func (d *projectsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_projects"
}

func (d *projectsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	c := func(name, doc string) schema.StringAttribute {
		if doc == "" {
			doc = projectDocs[name]
		}
		return schema.StringAttribute{MarkdownDescription: doc, Computed: true}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "The projects on the platform, optionally filtered, in id order: summaries " +
			"without settings or outputs (read one with the `ataila_project` data source for those). The " +
			"provider reads every page of the list.",
		Attributes: map[string]schema.Attribute{
			"tenant_id": schema.StringAttribute{
				MarkdownDescription: "Only the projects of this tenant.",
				Optional:            true,
				Validators:          []validator.String{stringvalidator.RegexMatches(rxUUID, "must be a tenant id (a UUID)")},
			},
			"customer_id": schema.StringAttribute{
				MarkdownDescription: "Only the projects of this customer.",
				Optional:            true,
				Validators:          []validator.String{stringvalidator.RegexMatches(rxIntID, "must be a customer id")},
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "Only the projects with this status: `planned`, `provisioning`, `active`, " +
					"`paused` or `retired`.",
				Optional:   true,
				Validators: []validator.String{stringvalidator.OneOf("planned", "provisioning", "active", "paused", "retired")},
			},
			"short_name": schema.StringAttribute{
				MarkdownDescription: "Only the project with this short name.",
				Optional:            true,
			},
			"projects": schema.ListNestedAttribute{
				MarkdownDescription: "The projects, in id order.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"id":                 c("id", ""),
					"tenant_id":          c("tenant_id", "Id of the owning tenant."),
					"customer_id":        c("customer_id", ""),
					"project_index":      schema.Int64Attribute{MarkdownDescription: "The project's index, 1-99.", Computed: true},
					"short_name":         c("short_name", "Short name."),
					"long_name":          c("long_name", "Display name."),
					"primary_domain":     c("primary_domain", "Primary domain."),
					"gitlab_repo_slug":   c("gitlab_repo_slug", "Application repository name."),
					"deployment_backend": c("deployment_backend", "`k8s` or `vm`."),
					"network_only":       schema.BoolAttribute{MarkdownDescription: "A network zone only.", Computed: true},
					"status":             c("status", ""),
					"is_self":            schema.BoolAttribute{MarkdownDescription: projectDocs["is_self"], Computed: true},
					"created_at": schema.StringAttribute{
						MarkdownDescription: projectDocs["created_at"], CustomType: TimestampType{}, Computed: true,
					},
				}},
			},
		},
	}
}

func (d *projectsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *projectsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.data == nil {
		resp.Diagnostics.AddError("Provider not configured", "The ataila provider was not configured.")
		return
	}
	var cfg projectsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	found, err := d.data.API.ListProjects(ctx, client.ProjectFilter{
		TenantID: cfg.TenantID.ValueString(), CustomerID: cfg.CustomerID.ValueString(),
		Status: cfg.Status.ValueString(), ShortName: cfg.ShortName.ValueString(),
	})
	if err != nil {
		summary, detail := apiErrorText("the projects (GET /projects)", err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	cfg.Projects = make([]projectSummaryModel, len(found))
	for i, p := range found {
		idx, _ := p["project_index"].(float64)
		cfg.Projects[i] = projectSummaryModel{
			ID: types.StringValue(p.String("id")), TenantID: types.StringValue(p.String("tenant_id")),
			CustomerID: nullIfEmpty(p.String("customer_id")), ProjectIndex: types.Int64Value(int64(idx)),
			ShortName: types.StringValue(p.String("short_name")), LongName: types.StringValue(p.String("long_name")),
			PrimaryDomain:     types.StringValue(p.String("primary_domain")),
			GitlabRepoSlug:    types.StringValue(p.String("gitlab_repo_slug")),
			DeploymentBackend: types.StringValue(p.String("deployment_backend")),
			NetworkOnly:       types.BoolValue(p.Bool("network_only")), Status: types.StringValue(p.String("status")),
			IsSelf: types.BoolValue(p.Bool("is_self")), CreatedAt: timestampFromString(p.String("created_at")),
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}

// ── ataila_project_stages ────────────────────────────────────────────────────

// NewProjectStagesDataSource is the factory for the ataila_project_stages
// data source.
func NewProjectStagesDataSource() datasource.DataSource { return &projectStagesDataSource{} }

type projectStagesDataSource struct {
	data *ProviderData
}

var stageGridTypes = map[string]attr.Type{
	"key": types.StringType, "title": types.StringType, "deps": types.ListType{ElemType: types.StringType},
	"deferred": types.BoolType, "status": types.StringType, "blocked": types.BoolType, "stale": types.BoolType,
	"simulated": types.BoolType, "started_at": types.StringType, "finished_at": types.StringType,
	"last_run_id": types.StringType, "last_run_at": types.StringType,
	"error_message": types.StringType, "verify_state": types.StringType, "verify_summary": types.StringType,
}

type projectStagesModel struct {
	ProjectID types.String `tfsdk:"project_id"`
	State     types.String `tfsdk:"state"`
	Percent   types.Int64  `tfsdk:"percent"`
	Stages    types.List   `tfsdk:"stages"`
}

func (d *projectStagesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project_stages"
}

func (d *projectStagesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A project's provisioning stages in apply order, with their dependencies and " +
			"latest runs. Read from the platform's database only.",
		Attributes: map[string]schema.Attribute{
			"project_id": schema.StringAttribute{
				MarkdownDescription: "Id of the project.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.RegexMatches(rxIntID, "must be a project id")},
			},
			"state": schema.StringAttribute{
				MarkdownDescription: "`not_started`, `provisioning`, `complete`, `attention` or `needs_action`.",
				Computed:            true,
			},
			"percent": schema.Int64Attribute{MarkdownDescription: "Share of the stages done, 0-100.", Computed: true},
			"stages": schema.ListAttribute{
				MarkdownDescription: "The stages: `key`, `title`, `deps` (the stages it needs), `deferred` (never " +
					"applied automatically), `status`, `blocked` (pending while a dependency is not done), " +
					"`stale`, `simulated`, `started_at` and `finished_at` (RFC 3339, of the latest run), " +
					"`last_run_id` and `last_run_at` (that run in the portal's run history; null when it never ran), " +
					"`error_message`, `verify_state` and `verify_summary`.",
				Computed:    true,
				ElementType: types.ObjectType{AttrTypes: stageGridTypes},
			},
		},
	}
}

func (d *projectStagesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *projectStagesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.data == nil {
		resp.Diagnostics.AddError("Provider not configured", "The ataila provider was not configured.")
		return
	}
	var cfg projectStagesModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	grid, err := d.data.API.GetStages(ctx, cfg.ProjectID.ValueString())
	if err != nil {
		summary, detail := apiErrorText("the stages of the project "+cfg.ProjectID.ValueString(), err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	elems := make([]attr.Value, 0, len(grid.Stages))
	for _, s := range grid.Stages {
		deps := make([]attr.Value, 0, len(s.Deps))
		for _, dep := range s.Deps {
			deps = append(deps, types.StringValue(dep))
		}
		elems = append(elems, types.ObjectValueMust(stageGridTypes, map[string]attr.Value{
			"key": types.StringValue(s.Key), "title": types.StringValue(s.Title),
			"deps": types.ListValueMust(types.StringType, deps), "deferred": types.BoolValue(s.Deferred),
			"status": types.StringValue(string(s.Status)), "blocked": types.BoolValue(s.Blocked),
			"stale": types.BoolValue(s.Stale), "simulated": types.BoolValue(s.Simulated != nil && *s.Simulated),
			"started_at": timestampString(s.StartedAt), "finished_at": timestampString(s.FinishedAt),
			"last_run_id": stringOrNull(s.LastRunId), "last_run_at": timestampString(s.LastRunAt),
			"error_message": stringOrNull(s.ErrorMessage), "verify_state": stringOrNull(s.VerifyState),
			"verify_summary": stringOrNull(s.VerifySummary),
		}))
	}
	cfg.State = types.StringValue(string(grid.State))
	cfg.Percent = types.Int64Value(int64(grid.Percent))
	cfg.Stages = types.ListValueMust(types.ObjectType{AttrTypes: stageGridTypes}, elems)
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
