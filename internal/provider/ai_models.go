// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/float64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/float64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/ataila/terraform-provider-ataila/internal/client"
)

var (
	_ resource.ResourceWithConfigure    = (*aiModelResource)(nil)
	_ resource.ResourceWithImportState  = (*aiModelResource)(nil)
	_ resource.ResourceWithUpgradeState = (*aiModelResource)(nil)
	_ resource.ResourceWithModifyPlan   = (*aiModelResource)(nil)
	_ resource.ResourceWithConfigure    = (*nodeCacheResource)(nil)
	_ resource.ResourceWithImportState  = (*nodeCacheResource)(nil)
)

// RepoImportPrefix imports an AI model by its repo id.
const RepoImportPrefix = "repo:"

var (
	aiModelFrozen = frozenKey{object: "AI model", why: "A model's repo names its weights. Destroying the " +
		"resource removes the catalogue row, which the platform refuses while weights exist, so a " +
		"replacement could leave two rows or none."}
	nodeCacheFrozen = frozenKey{object: "node cache", why: "A replacement would remove a cached copy of the " +
		"weights and copy them again. Declare a separate ataila_ai_model_node_cache for another model or node."}

	// The Hugging Face repo id rule: org/name, each part starting with a letter
	// or digit, letters, digits, '.', '_' and '-' only, no "..", at most 96.
	rxHFRepo   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)
	rxNodeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,79}$`)
)

// The metadata of a model a client may write, with its maximum length (0:
// not a string).
type modelField struct {
	name string
	kind fieldKind
	max  int
	doc  string
}

var aiModelFields = []modelField{
	{"display_name", fString, 255, "Display name; the platform's default is the repo's last part."},
	{"org", fString, 120, "The Hugging Face organisation."},
	{"vendor", fString, 200, "The lab that built the model (the organisation may be a quantizer)."},
	{"vendor_country", fString, 40, "The vendor's country."},
	{"params", fString, 60, "Parameters in human form, for example `480B (35B active)`."},
	{"param_count_b", fFloat, 0, "Parameters in billions."},
	{"architecture", fString, 40, "The architecture."},
	{"quant", fString, 40, "The quantization."},
	{"size_gb", fFloat, 0, "Size of the weights in GB."},
	{"published", fString, 20, "When it was published."},
	{"license", fString, 80, "The licence."},
	{"gateway_tier", fString, 40, "The AI gateway tier it serves."},
	{"category", fString, 24, "The category."},
	{"serving_node", fString, 80, "The node that serves it."},
	{"gated", fBool, 0, "The Hugging Face repo needs an accept-click to pull. Platform default `false`."},
	{"summary", fString, 2000, "A summary."},
	{"description", fString, 20000, "A description."},
	{"context_window", fString, 20, "The context window."},
	{"min_target", fString, 40, "The smallest target it fits."},
	{"strong_axis", fString, 40, "What it is strong at."},
	{"frontier_equiv", fString, 120, "The frontier model it compares with."},
	{"benchmarks", fStringMap, 0, "Benchmark results by name. Values that read as numbers are sent as numbers."},
	{"model_card_url", fString, 300, "The model card."},
	{"notes", fString, 20000, "Notes."},
}

var nodeCacheRefSpec = []fieldSpec{{name: "node", kind: fString}, {name: "state", kind: fString}, {name: "size_gb", kind: fFloat}}

// aiModelComputed are the members the platform sets.
var aiModelComputed = []fieldSpec{
	{name: "id", kind: fString, doc: "The model's id, assigned by the platform."},
	{name: "status", kind: fString, doc: "**Read-only**: `planned`, `pulling`, `owned` or `serving`, set by the store actions."},
	{name: "location", kind: fString, doc: "**Read-only**: `synology` (the central store on the NAS), `local` (node caches only), `both`, or null."},
	{name: "offline_ready", kind: fBool, doc: "**Read-only**: a node holds a cached copy."},
	{name: "nas_volume", kind: fString, doc: "**Read-only**: the central-store share holding the weights; null without a central copy."},
	{name: "nas_path", kind: fString, doc: "Where the central copy is; null without one."},
	{name: "dgx_recipe", kind: fString, doc: "The recipe a DGX cluster serves it with, if any."},
	{name: "node_caches", kind: fObjects, doc: "The node caches: `node`, `state`, `size_gb`.", sub: nodeCacheRefSpec},
	{name: "created_at", kind: fTime, doc: "When the row was created."},
	{name: "updated_at", kind: fTime, doc: "When it last changed."},
}

// aiModelSpec is every member, for the data sources.
var aiModelSpec = func() []fieldSpec {
	out := []fieldSpec{{name: "repo", kind: fString, doc: "The Hugging Face repo id."}}
	for _, f := range aiModelFields {
		out = append(out, fieldSpec{name: f.name, kind: f.kind, doc: f.doc})
	}
	return append(out, aiModelComputed...)
}()

// ── ataila_ai_model ──────────────────────────────────────────────────────────

// NewAIModelResource is the factory for ataila_ai_model.
func NewAIModelResource() resource.Resource { return &aiModelResource{} }

type aiModelResource struct {
	data *ProviderData
}

func (r *aiModelResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ai_model"
}

func (r *aiModelResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := map[string]schema.Attribute{
		"repo": schema.StringAttribute{
			MarkdownDescription: "The Hugging Face repo id `org/name`: each part starts with a letter or digit and holds " +
				"only letters, digits, `.`, `_` and `-` (no `..`), at most 96 characters. Unique. **Frozen.**",
			Required: true,
			Validators: []validator.String{stringvalidator.LengthAtMost(96),
				stringvalidator.RegexMatches(rxHFRepo, "must be a Hugging Face repo id org/name"), noDoubleDot{}},
			PlanModifiers: []planmodifier.String{aiModelFrozen.forString()},
		},
	}
	keepS := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	for _, f := range aiModelFields {
		doc := f.doc + " Left out: the current value is kept."
		switch f.kind {
		case fFloat:
			attrs[f.name] = schema.Float64Attribute{MarkdownDescription: doc, Optional: true, Computed: true,
				Validators:    []validator.Float64{float64validator.AtLeast(0)},
				PlanModifiers: []planmodifier.Float64{float64planmodifier.UseStateForUnknown()}}
		case fBool:
			attrs[f.name] = schema.BoolAttribute{MarkdownDescription: doc, Optional: true, Computed: true,
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}}
		case fStringMap:
			attrs[f.name] = schema.MapAttribute{MarkdownDescription: doc, Optional: true, Computed: true,
				ElementType: types.StringType, PlanModifiers: []planmodifier.Map{mapplanmodifier.UseStateForUnknown()}}
		default:
			min := 0
			if f.name == "display_name" {
				min = 1
			}
			attrs[f.name] = schema.StringAttribute{MarkdownDescription: doc, Optional: true, Computed: true,
				Validators: []validator.String{stringvalidator.LengthBetween(min, f.max)}, PlanModifiers: keepS}
		}
	}
	for _, f := range aiModelComputed {
		switch f.kind {
		case fBool:
			attrs[f.name] = schema.BoolAttribute{MarkdownDescription: f.doc, Computed: true}
		case fObjects:
			attrs[f.name] = schema.ListAttribute{MarkdownDescription: f.doc, Computed: true,
				ElementType: types.ObjectType{AttrTypes: specTypes(f.sub)}}
		case fTime:
			attrs[f.name] = schema.StringAttribute{MarkdownDescription: f.doc + " RFC 3339 in UTC.", Computed: true,
				CustomType: TimestampType{}}
		default:
			attrs[f.name] = schema.StringAttribute{MarkdownDescription: f.doc, Computed: true}
		}
	}
	attrs["id"] = schema.StringAttribute{MarkdownDescription: "The model's id, assigned by the platform.", Computed: true, PlanModifiers: keepS}
	attrs["created_at"] = schema.StringAttribute{MarkdownDescription: "When the row was created: RFC 3339 in UTC.",
		Computed: true, CustomType: TimestampType{}, PlanModifiers: keepS}
	resp.Schema = schema.Schema{
		Version: 1,
		MarkdownDescription: "An AI model of the catalogue: a **row**, created `planned`. Weights arrive only " +
			"through the platform's store actions; pulling them is not part of the API.\n\n" +
			"`repo` is **frozen**: a change fails the plan. `status`, `location`, `offline_ready` and " +
			"`nas_volume` are **read-only**: the store actions set them, and a configuration that sets them " +
			"fails the plan. The metadata is editable; a field left out of the configuration keeps its current " +
			"value (to clear one, set it to an empty string or clear it in the portal).\n\n" +
			"**Destroy removes the catalogue row and nothing else**; no disk is touched. It is **not " +
			"destroy-gated** (`allow_destroy` does not apply), because it never destroys weights: the platform " +
			"refuses it while the row is the record of weights (a central copy), while a node holds a cache, " +
			"and while a store run is active, and the provider reports which. Import by id or `repo:<repo>`.",
		Attributes: attrs,
	}
}

// noDoubleDot refuses ".." in a repo id.
type noDoubleDot struct{}

func (noDoubleDot) Description(context.Context) string               { return "must not contain .." }
func (v noDoubleDot) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }
func (noDoubleDot) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if strings.Contains(req.ConfigValue.ValueString(), "..") {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid repo id", "A repo id must not contain \"..\".")
	}
}

func (r *aiModelResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *aiModelResource) configured(diags interface{ AddError(string, string) }) bool {
	if r.data == nil {
		diags.AddError("Provider not configured", "The ataila provider was not configured.")
		return false
	}
	return true
}

// modelValues converts an API model to the resource's attribute values.
func modelValues(m client.Record) map[string]attr.Value {
	v := map[string]attr.Value{"repo": types.StringValue(m.String("repo"))}
	for _, f := range aiModelFields {
		v[f.name] = specValue(fieldSpec{name: f.name, kind: f.kind}, m[f.name])
	}
	for _, f := range aiModelComputed {
		if f.kind == fTime {
			v[f.name] = timestampFromString(m.String(f.name))
			continue
		}
		v[f.name] = specValue(f, m[f.name])
	}
	return v
}

// fieldJSON is the request value of a configured metadata attribute.
func fieldJSON(ctx context.Context, f modelField, v attr.Value) any {
	switch f.kind {
	case fFloat:
		return v.(types.Float64).ValueFloat64()
	case fBool:
		return v.(types.Bool).ValueBool()
	case fStringMap:
		out := map[string]any{}
		var m map[string]string
		_ = v.(types.Map).ElementsAs(ctx, &m, false)
		for k, s := range m {
			if n, err := strconv.ParseFloat(s, 64); err == nil {
				out[k] = n
			} else {
				out[k] = s
			}
		}
		return out
	}
	return v.(types.String).ValueString()
}

// ModifyPlan keeps the plan empty when nothing configurable changed.
func (r *aiModelResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() || req.Plan.Raw.Equal(req.State.Raw) {
		return
	}
	_, plan, d := stateObject(ctx, req.Plan.Get)
	resp.Diagnostics.Append(d...)
	_, state, d := stateObject(ctx, req.State.Get)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Benchmarks the platform stores with other number spellings ("70.10"
	// as 70.1) are no change.
	if pb, ok := plan["benchmarks"].(types.Map); ok {
		if sb, ok := state["benchmarks"].(types.Map); ok && !pb.Equal(sb) && sameBenchmarks(ctx, pb, sb) {
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("benchmarks"), sb)...)
			plan["benchmarks"] = sb
		}
	}
	if !plan["repo"].Equal(state["repo"]) {
		return
	}
	for _, f := range aiModelFields {
		if !plan[f.name].Equal(state[f.name]) {
			return
		}
	}
	resp.Plan.Raw = req.State.Raw.Copy()
}

// setState stores a model. prior holds the planned or stored values: a
// benchmarks map the platform returned with other number spellings ("83.10"
// as 83.1) keeps the prior spelling.
func (r *aiModelResource) setState(ctx context.Context, set func(context.Context, any) diag.Diagnostics, schemaType basetypes.ObjectType, m client.Record, prior map[string]attr.Value, diags *diag.Diagnostics) {
	values := modelValues(m)
	if p, ok := prior["benchmarks"].(types.Map); ok && sameBenchmarks(ctx, p, values["benchmarks"].(types.Map)) {
		values["benchmarks"] = p
	}
	diags.Append(set(ctx, objectFrom(ctx, schemaType, values, diags))...)
}

// sameBenchmarks compares two benchmark maps, numbers by value.
func sameBenchmarks(ctx context.Context, a, b types.Map) bool {
	if a.IsNull() || a.IsUnknown() || b.IsNull() || b.IsUnknown() {
		return false
	}
	var ma, mb map[string]string
	if a.ElementsAs(ctx, &ma, false).HasError() || b.ElementsAs(ctx, &mb, false).HasError() || len(ma) != len(mb) {
		return false
	}
	for k, va := range ma {
		vb, ok := mb[k]
		if !ok {
			return false
		}
		fa, errA := strconv.ParseFloat(va, 64)
		fb, errB := strconv.ParseFloat(vb, 64)
		if va != vb && (errA != nil || errB != nil || fa != fb) {
			return false
		}
	}
	return true
}

func (r *aiModelResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	planObj, plan, d := stateObject(ctx, req.Plan.Get)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	repo := strAttr(plan, "repo")
	body := map[string]any{"repo": repo}
	for _, f := range aiModelFields {
		if v := plan[f.name]; v != nil && !v.IsNull() && !v.IsUnknown() {
			body[f.name] = fieldJSON(ctx, f, v)
		}
	}
	m, err := r.data.API.CreateAIModel(ctx, body)
	if err != nil {
		if client.IsCode(err, 409, client.CodeRepoTaken) {
			resp.Diagnostics.AddAttributeError(path.Root("repo"), "The repo is already in the catalogue",
				fmt.Sprintf("%s is already in the catalogue. Import it instead:\n"+
					"  tofu import <address> repo:%s       (OpenTofu)\n"+
					"  terraform import <address> repo:%s  (Terraform)", repo, repo, repo))
			return
		}
		resp.Diagnostics.Append(apiError("adding the AI model "+repo, err))
		return
	}
	r.setState(ctx, resp.State.Set, planObj.Type(ctx).(basetypes.ObjectType), m, plan, &resp.Diagnostics)
}

func (r *aiModelResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	stateObj, state, d := stateObject(ctx, req.State.Get)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	m, err := r.data.API.GetAIModel(ctx, strAttr(state, "id"))
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.Append(apiError("reading the AI model "+strAttr(state, "repo"), err))
		return
	}
	r.setState(ctx, resp.State.Set, stateObj.Type(ctx).(basetypes.ObjectType), m, state, &resp.Diagnostics)
}

func (r *aiModelResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	planObj, plan, d := stateObject(ctx, req.Plan.Get)
	resp.Diagnostics.Append(d...)
	_, state, d := stateObject(ctx, req.State.Get)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	aiModelFrozen.check(path.Root("repo"), state["repo"].(types.String), plan["repo"].(types.String), true, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	patch := client.Patch{}
	for _, f := range aiModelFields {
		pv := plan[f.name]
		if pv == nil || pv.IsUnknown() || pv.IsNull() || pv.Equal(state[f.name]) {
			continue
		}
		patch[f.name] = fieldJSON(ctx, f, pv)
	}
	id := strAttr(state, "id")
	var (
		m   client.Record
		err error
	)
	if len(patch) == 0 {
		m, err = r.data.API.GetAIModel(ctx, id)
	} else {
		m, err = r.data.API.UpdateAIModel(ctx, id, patch)
	}
	if err != nil {
		resp.Diagnostics.Append(apiError("changing the AI model "+strAttr(state, "repo"), err))
		return
	}
	r.setState(ctx, resp.State.Set, planObj.Type(ctx).(basetypes.ObjectType), m, plan, &resp.Diagnostics)
}

func (r *aiModelResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	_, state, d := stateObject(ctx, req.State.Get)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	repo := strAttr(state, "repo")
	err := r.data.API.DeleteAIModel(ctx, strAttr(state, "id"))
	if err == nil || isNotFound(err) {
		return
	}
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == 409 {
		remedy := map[string]string{
			"model_has_central_copy": "The row is the record of weights on the central store (or the model is " +
				"owned or serving), and the API cannot remove a central copy. Remove the weights in the portal " +
				"first, or keep the row and remove the resource from the state:\n" +
				"  tofu state rm <address>       (OpenTofu)\n  terraform state rm <address>  (Terraform)",
			"model_has_node_caches": "A node holds a cached copy. Remove the node caches first (destroy their " +
				"ataila_ai_model_node_cache resources, or uncache in the portal), then destroy again.",
			"model_has_active_run": "A store run of the model is pending or running. Wait for it to finish, then " +
				"destroy again.",
		}[apiErr.Code()]
		if remedy != "" {
			resp.Diagnostics.AddError(fmt.Sprintf("The platform keeps the AI model %s (%s)", repo, apiErr.Code()),
				fmt.Sprintf("Destroying removes only the catalogue row, and the platform refuses while weights or work "+
					"exist. The provider does not retry.\n\n%s\n\n%s", remedy, apiErr.Detail()))
			return
		}
	}
	resp.Diagnostics.Append(apiError("removing the AI model "+repo, err))
}

func (r *aiModelResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	id := strings.TrimSpace(req.ID)
	if repo, ok := strings.CutPrefix(id, RepoImportPrefix); ok {
		found, err := r.data.API.ListAIModels(ctx, repo, "", "", "")
		if err != nil {
			resp.Diagnostics.Append(apiError("looking up the AI model with repo "+repo, err))
			return
		}
		if len(found) != 1 {
			resp.Diagnostics.AddError("Cannot import the AI model",
				fmt.Sprintf("%d models have repo %q; the import needs exactly one.", len(found), repo))
			return
		}
		id = found[0].String("id")
	}
	if !rxIntID.MatchString(id) {
		resp.Diagnostics.AddError("Cannot import the AI model",
			fmt.Sprintf("%q is not a model id. Give the id, or repo:<org/name>.", id))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

// ── ataila_ai_model_node_cache ───────────────────────────────────────────────

// Default waits of a node cache: copying weights can take long.
const (
	defaultCacheCreateTimeout = 180 * time.Minute
	defaultCacheDeleteTimeout = 30 * time.Minute
)

// NewAIModelNodeCacheResource is the factory for ataila_ai_model_node_cache.
func NewAIModelNodeCacheResource() resource.Resource { return &nodeCacheResource{} }

type nodeCacheResource struct {
	data *ProviderData
}

type nodeCacheModel struct {
	ID          types.String   `tfsdk:"id"`
	ModelID     types.String   `tfsdk:"model_id"`
	Node        types.String   `tfsdk:"node"`
	State       types.String   `tfsdk:"state"`
	SizeGB      types.Float64  `tfsdk:"size_gb"`
	Path        types.String   `tfsdk:"path"`
	UpdatedAt   TimestampValue `tfsdk:"updated_at"`
	OperationID types.String   `tfsdk:"operation_id"`
	Timeouts    timeouts.Value `tfsdk:"timeouts"`
}

func (m *nodeCacheModel) fromAPI(c client.Record) {
	m.ModelID = types.StringValue(c.String("model_id"))
	m.Node = types.StringValue(c.String("node"))
	m.ID = types.StringValue(m.ModelID.ValueString() + "/" + m.Node.ValueString())
	m.State = types.StringValue(c.String("state"))
	if f, ok := c.Float("size_gb"); ok {
		m.SizeGB = types.Float64Value(f)
	} else {
		m.SizeGB = types.Float64Null()
	}
	m.Path = nullIfEmpty(c.String("path"))
	m.UpdatedAt = timestampFromString(c.String("updated_at"))
	if m.OperationID.IsUnknown() {
		m.OperationID = types.StringNull()
	}
}

func (m *nodeCacheModel) ident() string {
	return fmt.Sprintf("of model %s on node %s", m.ModelID.ValueString(), m.Node.ValueString())
}

func (r *nodeCacheResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ai_model_node_cache"
}

func (r *nodeCacheResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A cached copy of a model's weights on an AI node's local disk (fast, offline-ready " +
			"serving), copied from the central store.\n\n" +
			"**Create** starts the copy and waits until its store run succeeds, up to `timeouts.create` (default " +
			"3 hours). A node that already holds a cached copy is adopted. The model needs a central copy. " +
			"**Destroy** removes the node's copy (the central copy is untouched) and waits, up to " +
			"`timeouts.delete` (default 30 minutes); it is refused while the model is loaded on the node, and " +
			"while monitoring cannot say whether it is.\n\n" +
			"These move real weights, so on a platform that fakes dispatch (`dryrun` or `simulate`) the provider " +
			"fails at once, naming the mode: nothing would ever be copied. A run still going when the timeout " +
			"passes is reported with its operation; nothing is stored, and the next apply adopts the copy once " +
			"the run has finished (while it runs, the platform answers `run_in_progress`).\n\n" +
			"`model_id` and `node` are **frozen**. Import `<model_id>/<node>`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{MarkdownDescription: "`<model_id>/<node>`.", Computed: true, PlanModifiers: keep},
			"model_id": schema.StringAttribute{
				MarkdownDescription: "Id of the model. **Frozen.**",
				Required:            true,
				Validators:          []validator.String{stringvalidator.RegexMatches(rxIntID, "must be a model id")},
				PlanModifiers:       []planmodifier.String{nodeCacheFrozen.forString()},
			},
			"node": schema.StringAttribute{
				MarkdownDescription: "The AI node's hostname. **Frozen.**",
				Required:            true,
				Validators:          []validator.String{stringvalidator.RegexMatches(rxNodeName, "must be a node hostname")},
				PlanModifiers:       []planmodifier.String{nodeCacheFrozen.forString()},
			},
			"state":        schema.StringAttribute{MarkdownDescription: "`cached` once the copy is complete.", Computed: true, PlanModifiers: keep},
			"size_gb":      schema.Float64Attribute{MarkdownDescription: "Size of the copy in GB.", Computed: true, PlanModifiers: []planmodifier.Float64{float64planmodifier.UseStateForUnknown()}},
			"path":         schema.StringAttribute{MarkdownDescription: "Where the copy is on the node.", Computed: true, PlanModifiers: keep},
			"updated_at":   schema.StringAttribute{MarkdownDescription: "When it last changed: RFC 3339 in UTC.", Computed: true, CustomType: TimestampType{}, PlanModifiers: keep},
			"operation_id": schema.StringAttribute{MarkdownDescription: "The store run that made the copy (`model-store-run:<id>`); null when adopted.", Computed: true, PlanModifiers: keep},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Delete: true}),
		},
	}
}

func (r *nodeCacheResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *nodeCacheResource) configured(diags interface{ AddError(string, string) }) bool {
	if r.data == nil {
		diags.AddError("Provider not configured", "The ataila provider was not configured.")
		return false
	}
	return true
}

func nodeCacheError(doing string, err error) diag.Diagnostic {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		hint := map[string]string{
			client.CodeNoCentralCopy: "The model has no copy on the central store to cache from; the API cannot pull one.",
			client.CodeUnknownNode:   "The node is not an AI node of the platform's roster.",
			client.CodeRunInProgress: "A store run of this model is still going (operation_id names it). Wait for it " +
				"to finish, then apply again; the provider does not retry.",
			client.CodeLoadedStateUnknown: "Monitoring cannot be read, so whether the model is loaded on the node is " +
				"unknown, and the platform refuses to remove the copy. Nothing was dispatched; the provider does not retry.",
			client.CodeModelLoadedOnNode: "The model is loaded on the node; unload it first.",
		}[apiErr.Code()]
		if hint != "" {
			return diag.NewErrorDiagnostic(apiErr.Summary(), fmt.Sprintf("While %s.\n\n%s\n\n%s", doing, hint, apiErr.Detail()))
		}
	}
	return apiError(doing, err)
}

// dispatchesLive checks, before any write, that the platform carries store
// runs out: under a faked dispatch nothing would ever be copied or removed.
func (r *nodeCacheResource) dispatchesLive(doing string, diags *diag.Diagnostics) bool {
	mode := r.data.DispatchMode()
	if !fakesDispatch(mode) {
		return true
	}
	diags.AddError(fmt.Sprintf("The platform fakes dispatch (%s): the store run would never run", mode),
		fmt.Sprintf("While %s: the platform's dispatch mode is %s (GET /meta), so a store run would hold a fake "+
			"pipeline and never complete. Weights are real infrastructure, so there is nothing to adopt: this "+
			"works only on a platform that dispatches live. Nothing was sent.", doing, mode))
	return false
}

// runStore waits for a store run and reports how it ended.
func (r *nodeCacheResource) runStore(ctx context.Context, op *client.Operation, wait time.Duration, doing string, diags *diag.Diagnostics) bool {
	if mode := operationDispatch(op); fakesDispatch(mode) {
		diags.AddError(fmt.Sprintf("The platform fakes dispatch (%s): the store run will never run", mode),
			fmt.Sprintf("While %s: the platform answered with operation %s, but its dispatch mode is %s, so "+
				"nothing reaches the runner and the run never completes (the platform then marks it failed). "+
				"Weights are real infrastructure, so there is nothing to adopt: this works only on a platform that "+
				"dispatches live.", doing, op.Id, mode))
		return false
	}
	waitCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	last, err := pollOperation(waitCtx, r.data.API, op.Id, op, nil)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		diags.AddError("The store run did not finish in time",
			fmt.Sprintf("While %s: operation %s was still %s after %s. It goes on on the platform; apply again "+
				"after it has finished (raise the timeout to wait longer).", doing, op.Id, last.Status, wait))
		return false
	case err != nil:
		diags.Append(apiError(fmt.Sprintf("waiting for operation %s while %s", op.Id, doing), err))
		return false
	case last.Status == client.OperationStatusFailed:
		diags.AddError(fmt.Sprintf("The store run failed (%s)", operationErrorCode(last)),
			fmt.Sprintf("While %s: operation %s failed.\n\n%s", doing, op.Id, operationErrorText(last)))
		return false
	}
	return true
}

func (r *nodeCacheResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan nodeCacheModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	wait, d := plan.Timeouts.Create(ctx, defaultCacheCreateTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	doing := "caching the model " + plan.ident()
	if !r.dispatchesLive(doing, &resp.Diagnostics) {
		return
	}
	existing, op, err := r.data.API.CacheModel(ctx, plan.ModelID.ValueString(), plan.Node.ValueString())
	if err != nil {
		resp.Diagnostics.Append(nodeCacheError(doing, err))
		return
	}
	if op != nil {
		if !r.runStore(ctx, op, wait, doing, &resp.Diagnostics) {
			return
		}
		plan.OperationID = types.StringValue(op.Id)
		existing, err = r.data.API.GetNodeCache(ctx, plan.ModelID.ValueString(), plan.Node.ValueString())
		if err != nil {
			resp.Diagnostics.Append(apiError("reading the node cache "+plan.ident(), err))
			return
		}
	} else {
		plan.OperationID = types.StringNull()
		resp.Diagnostics.AddWarning("Existing node cache adopted",
			fmt.Sprintf("The node already held a cached copy %s; nothing was copied. Destroying the resource "+
				"removes the copy.", plan.ident()))
	}
	plan.fromAPI(existing)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *nodeCacheResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var state nodeCacheModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	c, err := r.data.API.GetNodeCache(ctx, state.ModelID.ValueString(), state.Node.ValueString())
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.Append(apiError("reading the node cache "+state.ident(), err))
		return
	}
	state.fromAPI(c)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update changes only the timeouts.
func (r *nodeCacheResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state nodeCacheModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	nodeCacheFrozen.check(path.Root("model_id"), state.ModelID, plan.ModelID, true, &resp.Diagnostics)
	nodeCacheFrozen.check(path.Root("node"), state.Node, plan.Node, true, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	state.Timeouts = plan.Timeouts
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *nodeCacheResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var state nodeCacheModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	wait, d := state.Timeouts.Delete(ctx, defaultCacheDeleteTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	doing := "removing the node cache " + state.ident()
	if !r.dispatchesLive(doing, &resp.Diagnostics) {
		return
	}
	op, err := r.data.API.UncacheModel(ctx, state.ModelID.ValueString(), state.Node.ValueString())
	if isNotFound(err) {
		return
	}
	if err != nil {
		resp.Diagnostics.Append(nodeCacheError(doing, err))
		return
	}
	r.runStore(ctx, op, wait, doing, &resp.Diagnostics)
}

func (r *nodeCacheResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	modelID, node, ok := strings.Cut(strings.TrimSpace(req.ID), "/")
	if !ok || !rxIntID.MatchString(modelID) || !rxNodeName.MatchString(node) {
		resp.Diagnostics.AddError("Cannot import the node cache",
			fmt.Sprintf("The import id must be <model_id>/<node>, got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), modelID+"/"+node)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("model_id"), modelID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("node"), node)...)
}

// UpgradeState renames the attributes 0.7.0 renamed (schema version 0 to 1).
func (r *aiModelResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return renameUpgraders(aiModelRenamesV1)
}
