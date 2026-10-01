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
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ataila/terraform-provider-ataila/internal/client"
)

// ── specs ────────────────────────────────────────────────────────────────────

const liveDoc = " Live (monitoring); null when `monitoring_reachable` is false."

var (
	nodeModelSpec = []fieldSpec{
		{name: "model", kind: fString}, {name: "served_name", kind: fString}, {name: "engine", kind: fString},
		{name: "port", kind: fInt}, {name: "tensor_parallel", kind: fInt}, {name: "max_model_len", kind: fInt},
		{name: "cluster", kind: fString}, {name: "tiers", kind: fStrings},
	}
	aiNodeSpec = []fieldSpec{
		{name: "hostname", kind: fString, doc: "The node's hostname."},
		{name: "site", kind: fString, doc: "Its site."},
		{name: "mgmt_ip", kind: fString, doc: "Its management address."},
		{name: "gpu_class", kind: fString, doc: "For example `rtx-3090` or `gb10`."},
		{name: "specs_summary", kind: fString, doc: "A summary of its hardware."},
		{name: "services", kind: fStrings, doc: "The services it runs."},
		{name: "is_virtual", kind: fBool, doc: "A GPU virtual machine on a hybrid host."},
		{name: "parent_host", kind: fString, doc: "The host of a virtual node."},
		{name: "vmid", kind: fInt, doc: "The virtual machine id of a virtual node."},
		{name: "monitoring_reachable", kind: fBool, doc: "False: monitoring could not be read, and every live field is null. The read does not fail."},
		{name: "status", kind: fString, doc: "`serving`, `loaded_idle`, `idle`, `offline`, `standby` or `powered_off`." + liveDoc},
		{name: "online", kind: fBool, doc: "Online." + liveDoc},
		{name: "role", kind: fString, doc: "Its role." + liveDoc},
		{name: "cluster", kind: fObject, doc: "The DGX cluster it belongs to: `name`, `role`." + liveDoc,
			sub: []fieldSpec{{name: "name", kind: fString}, {name: "role", kind: fString}}},
		{name: "uptime_seconds", kind: fFloat, doc: "Uptime." + liveDoc},
		{name: "cpu_util_pct", kind: fFloat, doc: "CPU use." + liveDoc},
		{name: "load1", kind: fFloat, doc: "Load average." + liveDoc},
		{name: "mem_used_pct", kind: fFloat, doc: "Memory use." + liveDoc},
		{name: "disk_used_pct", kind: fFloat, doc: "Disk use." + liveDoc},
		{name: "gpu_count", kind: fInt, doc: "GPUs." + liveDoc},
		{name: "gpu_util_avg_pct", kind: fFloat, doc: "Average GPU use." + liveDoc},
		{name: "vram_used_bytes", kind: fFloat, doc: "VRAM used." + liveDoc},
		{name: "vram_total_bytes", kind: fFloat, doc: "VRAM in total." + liveDoc},
		{name: "gpu_temp_max_c", kind: fFloat, doc: "Hottest GPU." + liveDoc},
		{name: "throttle_active", kind: fBool, doc: "A GPU is throttling." + liveDoc},
		{name: "collector_stale", kind: fBool, doc: "The node's collector is stale." + liveDoc},
		{name: "models", kind: fObjects, doc: "The loaded models: " + fieldNames(nodeModelSpec) + "." + liveDoc, sub: nodeModelSpec},
	}
	clusterMemberSpec = []fieldSpec{{name: "hostname", kind: fString}, {name: "role", kind: fString},
		{name: "crosslink_ip", kind: fString}, {name: "mgmt_ip", kind: fString}}
	clusterSpec = []fieldSpec{
		{name: "id", kind: fString}, {name: "name", kind: fString}, {name: "topology", kind: fString},
		{name: "interconnect", kind: fString}, {name: "members", kind: fObjects, sub: clusterMemberSpec},
		{name: "crosslink_subnet", kind: fString}, {name: "status", kind: fString},
		{name: "serve", kind: fObject, sub: []fieldSpec{{name: "recipe", kind: fString}, {name: "model", kind: fString},
			{name: "served_at", kind: fTime}}},
		{name: "error_message", kind: fString}, {name: "notes", kind: fString},
		{name: "created_at", kind: fTime}, {name: "updated_at", kind: fTime},
	}
	launchSpec = []fieldSpec{{name: "key", kind: fString}, {name: "host", kind: fString}, {name: "label", kind: fString},
		{name: "model", kind: fString}, {name: "engine", kind: fString}, {name: "port", kind: fInt},
		{name: "enabled", kind: fBool}}
	loadTargetSpec = []fieldSpec{
		{name: "hostname", kind: fString}, {name: "online", kind: fBool}, {name: "status", kind: fString},
		{name: "gpu_count", kind: fInt}, {name: "tensor_parallel", kind: fInt}, {name: "per_gpu_gb", kind: fInt},
		{name: "vram_total_gb", kind: fInt}, {name: "usable_vram_gb", kind: fFloat},
		{name: "loaded_models", kind: fStrings}, {name: "loadable", kind: fBool}, {name: "engine", kind: fString},
		{name: "is_cluster", kind: fBool}, {name: "members", kind: fStrings},
	}
	mountSpec = []fieldSpec{{name: "name", kind: fString}, {name: "mount", kind: fString}, {name: "kind", kind: fString},
		{name: "total_gb", kind: fFloat}, {name: "free_gb", kind: fFloat}, {name: "captured_at", kind: fTime}}
	cachedSpec    = []fieldSpec{{name: "name", kind: fString}, {name: "repo", kind: fString}, {name: "size_gb", kind: fFloat}, {name: "cached_at", kind: fTime}}
	nodeStoreSpec = append(append([]fieldSpec{}, mountSpec...), fieldSpec{name: "cached", kind: fObjects, sub: cachedSpec})
	storageSpec   = []fieldSpec{
		{name: "shares", kind: fStrings, doc: "The central-store shares the store actions can use."},
		{name: "nas", kind: fObjects, doc: "Free space per central-store share, as last scanned: " + fieldNames(mountSpec) + ".", sub: mountSpec},
		{name: "nodes", kind: fObjects, doc: "Each node's local disk, as last scanned, with the models cached on it (`cached`: " +
			fieldNames(cachedSpec) + "): " + fieldNames(mountSpec) + ".", sub: nodeStoreSpec},
		{name: "captured_at", kind: fTime, doc: "The newest scan; null when nothing was ever scanned."},
	}
)

// setSpecState writes config-held attributes plus spec values into a data
// source's state.
func setSpecState(ctx context.Context, state *tfsdk.State, values map[string]attr.Value, diags *diag.Diagnostics) {
	for name, v := range values {
		diags.Append(state.SetAttribute(ctx, path.Root(name), v)...)
	}
}

type aiDataSource struct {
	data *ProviderData
}

func (d *aiDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *aiDataSource) ready(diags *diag.Diagnostics) bool {
	if d.data == nil {
		diags.AddError("Provider not configured", "The ataila provider was not configured.")
		return false
	}
	return true
}

func listValue(specs []fieldSpec, items []client.Record) types.List {
	ot := types.ObjectType{AttrTypes: specTypes(specs)}
	elems := make([]attr.Value, 0, len(items))
	for _, it := range items {
		elems = append(elems, types.ObjectValueMust(ot.AttrTypes, specValues(it, specs)))
	}
	return types.ListValueMust(ot, elems)
}

// ── ataila_ai_nodes / ataila_ai_node ─────────────────────────────────────────

// NewAINodesDataSource is the factory for ataila_ai_nodes.
func NewAINodesDataSource() datasource.DataSource { return &aiNodesDataSource{} }

type aiNodesDataSource struct{ aiDataSource }

func (d *aiNodesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ai_nodes"
}

func (d *aiNodesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dschema.Schema{
		MarkdownDescription: "The AI fleet (AI Center, read-only). Roster fields are always there; live fields " +
			"are null when monitoring cannot be read, and then `monitoring_reachable` is false. The read never " +
			"fails because of monitoring. Needs `ai-center-read-global`.",
		Attributes: map[string]dschema.Attribute{
			"monitoring_reachable": dschema.BoolAttribute{
				MarkdownDescription: "False: monitoring could not be read and every live field is null.", Computed: true},
			"nodes": dataNestedList("The nodes, by hostname.", specTypes(aiNodeSpec), specDoc(aiNodeSpec, "AiNode")),
		},
	}
}

func (d *aiNodesDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	nodes, reachable, err := d.data.API.ListAINodes(ctx)
	if err != nil {
		summary, detail := apiErrorText("the AI nodes (GET /ai/nodes)", err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	setSpecState(ctx, &resp.State, map[string]attr.Value{
		"monitoring_reachable": types.BoolValue(reachable), "nodes": listValue(aiNodeSpec, nodes),
	}, &resp.Diagnostics)
}

// NewAINodeDataSource is the factory for ataila_ai_node.
func NewAINodeDataSource() datasource.DataSource { return &aiNodeDataSource{} }

type aiNodeDataSource struct{ aiDataSource }

func (d *aiNodeDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ai_node"
}

func (d *aiNodeDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := specDataAttributes(aiNodeSpec, "AiNode")
	attrs["hostname"] = dschema.StringAttribute{MarkdownDescription: "The node's hostname.", Required: true}
	resp.Schema = dschema.Schema{
		MarkdownDescription: "One AI node (AI Center, read-only), by hostname. Live fields are null when monitoring " +
			"cannot be read (`monitoring_reachable` false); the read does not fail because of it.",
		Attributes: attrs,
	}
}

func (d *aiNodeDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	var host types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("hostname"), &host)...)
	if resp.Diagnostics.HasError() {
		return
	}
	n, err := d.data.API.GetAINode(ctx, host.ValueString())
	if err != nil {
		summary, detail := apiErrorText("the AI node "+host.ValueString(), err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	setSpecState(ctx, &resp.State, specValues(n, aiNodeSpec), &resp.Diagnostics)
}

// ── ataila_dgx_clusters ──────────────────────────────────────────────────────

// NewDGXClustersDataSource is the factory for ataila_dgx_clusters.
func NewDGXClustersDataSource() datasource.DataSource { return &dgxClustersDataSource{} }

type dgxClustersDataSource struct{ aiDataSource }

func (d *dgxClustersDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dgx_clusters"
}

func (d *dgxClustersDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dschema.Schema{
		MarkdownDescription: "The DGX clusters as recorded (AI Center, read-only). The read does not converge a " +
			"cluster that is forming or breaking.",
		Attributes: map[string]dschema.Attribute{
			"name": dschema.StringAttribute{MarkdownDescription: "Only the cluster with this name.", Optional: true,
				Validators: []validator.String{stringvalidator.LengthAtMost(40)}},
			"clusters": dataNestedList("The clusters, by name.", specTypes(clusterSpec), specDoc(clusterSpec, "DgxCluster")),
		},
	}
}

func (d *dgxClustersDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	var name types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("name"), &name)...)
	items, err := d.data.API.ListDGXClusters(ctx, name.ValueString())
	if err != nil {
		summary, detail := apiErrorText("the DGX clusters (GET /ai/clusters)", err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	setSpecState(ctx, &resp.State, map[string]attr.Value{"name": name, "clusters": listValue(clusterSpec, items)}, &resp.Diagnostics)
}

// ── ataila_ai_model_launch_catalog ───────────────────────────────────────────

// NewLaunchCatalogDataSource is the factory for ataila_ai_model_launch_catalog.
func NewLaunchCatalogDataSource() datasource.DataSource { return &launchCatalogDataSource{} }

type launchCatalogDataSource struct{ aiDataSource }

func (d *launchCatalogDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ai_model_launch_catalog"
}

func (d *launchCatalogDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dschema.Schema{
		MarkdownDescription: "The launch catalogue (AI Center, read-only): the models each node can launch. The " +
			"load and unload commands are never returned.",
		Attributes: map[string]dschema.Attribute{
			"host":    dschema.StringAttribute{MarkdownDescription: "Only the entries of this host.", Optional: true},
			"enabled": dschema.BoolAttribute{MarkdownDescription: "Only enabled (or disabled) entries.", Optional: true},
			"entries": dataNestedList("The entries, by key.", specTypes(launchSpec), specDoc(launchSpec, "LaunchCatalogEntry")),
		},
	}
}

func (d *launchCatalogDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	var host types.String
	var enabled types.Bool
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("host"), &host)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("enabled"), &enabled)...)
	var en *bool
	if !enabled.IsNull() {
		v := enabled.ValueBool()
		en = &v
	}
	items, err := d.data.API.ListLaunchCatalog(ctx, host.ValueString(), en)
	if err != nil {
		summary, detail := apiErrorText("the launch catalogue (GET /ai/catalog)", err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	setSpecState(ctx, &resp.State, map[string]attr.Value{"host": host, "enabled": enabled,
		"entries": listValue(launchSpec, items)}, &resp.Diagnostics)
}

// ── ataila_ai_model_storage / ataila_ai_load_targets ─────────────────────────

// NewModelStorageDataSource is the factory for ataila_ai_model_storage.
func NewModelStorageDataSource() datasource.DataSource { return &modelStorageDataSource{} }

type modelStorageDataSource struct{ aiDataSource }

func (d *modelStorageDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ai_model_storage"
}

func (d *modelStorageDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dschema.Schema{
		MarkdownDescription: "Model storage as last scanned: the central-store shares and each node's local disk " +
			"with the models cached on it. Nothing is scanned by the read. Needs `ai-models-read-global`.",
		Attributes: specDataAttributes(storageSpec, "Storage"),
	}
}

func (d *modelStorageDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	s, err := d.data.API.GetModelStorage(ctx)
	if err != nil {
		summary, detail := apiErrorText("the model storage (GET /ai-models/storage)", err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	setSpecState(ctx, &resp.State, specValues(s, storageSpec), &resp.Diagnostics)
}

// NewLoadTargetsDataSource is the factory for ataila_ai_load_targets.
func NewLoadTargetsDataSource() datasource.DataSource { return &loadTargetsDataSource{} }

type loadTargetsDataSource struct{ aiDataSource }

func (d *loadTargetsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ai_load_targets"
}

func (d *loadTargetsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dschema.Schema{
		MarkdownDescription: "The nodes and DGX clusters a model can be served on, with their VRAM budget: live " +
			"values when monitoring answers, static fallbacks otherwise.",
		Attributes: map[string]dschema.Attribute{
			"targets": dataNestedList("The targets, by hostname. `loadable` is false for fit-only targets (DGX "+
				"clusters, Kubernetes nodes).", specTypes(loadTargetSpec), specDoc(loadTargetSpec, "LoadTarget")),
		},
	}
}

func (d *loadTargetsDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	items, err := d.data.API.ListLoadTargets(ctx)
	if err != nil {
		summary, detail := apiErrorText("the load targets (GET /ai-models/load-targets)", err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	setSpecState(ctx, &resp.State, map[string]attr.Value{"targets": listValue(loadTargetSpec, items)}, &resp.Diagnostics)
}

// ── ataila_ai_model / ataila_ai_models ───────────────────────────────────────

// NewAIModelDataSource is the factory for the ataila_ai_model data source.
func NewAIModelDataSource() datasource.DataSource { return &aiModelDataSource{} }

type aiModelDataSource struct{ aiDataSource }

func (d *aiModelDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ai_model"
}

func (d *aiModelDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attrs := specDataAttributes(aiModelSpec, "AiModel")
	attrs["id"] = dschema.StringAttribute{MarkdownDescription: "The model's id. Give `id` or `repo`.", Optional: true, Computed: true,
		Validators: []validator.String{stringvalidator.RegexMatches(rxIntID, "must be a model id")}}
	attrs["repo"] = dschema.StringAttribute{MarkdownDescription: "The Hugging Face repo id. Give `id` or `repo`.", Optional: true, Computed: true}
	resp.Schema = dschema.Schema{
		MarkdownDescription: "One AI model of the catalogue, by id or by repo. Needs `ai-models-read-global`.",
		Attributes:          attrs,
	}
}

func (d *aiModelDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("repo"))}
}

func (d *aiModelDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	var id, repo types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("id"), &id)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("repo"), &repo)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var m client.Record
	if !id.IsNull() {
		got, err := d.data.API.GetAIModel(ctx, id.ValueString())
		if err != nil {
			summary, detail := apiErrorText("the AI model "+id.ValueString(), err)
			resp.Diagnostics.AddError(summary, detail)
			return
		}
		m = got
	} else {
		found, err := d.data.API.ListAIModels(ctx, repo.ValueString(), "", "", "")
		if err != nil {
			summary, detail := apiErrorText("the AI models (GET /ai-models)", err)
			resp.Diagnostics.AddError(summary, detail)
			return
		}
		if len(found) != 1 {
			resp.Diagnostics.AddError("AI model not found",
				fmt.Sprintf("%d models have repo %q; exactly one is needed.", len(found), repo.ValueString()))
			return
		}
		m = found[0]
	}
	setSpecState(ctx, &resp.State, specValues(m, aiModelSpec), &resp.Diagnostics)
}

// NewAIModelsDataSource is the factory for ataila_ai_models.
func NewAIModelsDataSource() datasource.DataSource { return &aiModelsDataSource{} }

type aiModelsDataSource struct{ aiDataSource }

func (d *aiModelsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ai_models"
}

func (d *aiModelsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	filter := func(doc string) dschema.StringAttribute {
		return dschema.StringAttribute{MarkdownDescription: doc, Optional: true}
	}
	resp.Schema = dschema.Schema{
		MarkdownDescription: "The AI model catalogue, optionally filtered, in id order. The provider reads every page.",
		Attributes: map[string]dschema.Attribute{
			"repo":         filter("Only the model with this repo id."),
			"status":       filter("Only models with this status: `planned`, `pulling`, `owned` or `serving`."),
			"category":     filter("Only models of this category."),
			"gateway_tier": filter("Only models of this gateway tier."),
			"models": dataNestedList("The models, with the attributes of the `ataila_ai_model` data source.",
				specTypes(aiModelSpec), specDoc(aiModelSpec, "AiModel")),
		},
	}
}

func (d *aiModelsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if !d.ready(&resp.Diagnostics) {
		return
	}
	f := map[string]types.String{}
	for _, name := range []string{"repo", "status", "category", "gateway_tier"} {
		var v types.String
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root(name), &v)...)
		f[name] = v
	}
	if resp.Diagnostics.HasError() {
		return
	}
	items, err := d.data.API.ListAIModels(ctx, f["repo"].ValueString(), f["status"].ValueString(),
		f["category"].ValueString(), f["gateway_tier"].ValueString())
	if err != nil {
		summary, detail := apiErrorText("the AI models (GET /ai-models)", err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	values := map[string]attr.Value{"models": listValue(aiModelSpec, items)}
	for k, v := range f {
		values[k] = v
	}
	setSpecState(ctx, &resp.State, values, &resp.Diagnostics)
}
