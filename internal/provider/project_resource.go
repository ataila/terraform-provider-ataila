// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/ataila/terraform-provider-ataila/internal/client"
)

var (
	_ resource.ResourceWithConfigure      = (*projectResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*projectResource)(nil)
	_ resource.ResourceWithImportState    = (*projectResource)(nil)
	_ resource.ResourceWithUpgradeState   = (*projectResource)(nil)
	_ resource.ResourceWithValidateConfig = (*projectResource)(nil)
)

// projectFrozenKeys are the keys set when a project is created.
var projectFrozenKeys = []string{"tenant_id", "project_index", "short_name", "gitlab_repo_slug",
	"primary_domain", "deployment_backend", "network_only"}

// NewProjectResource is the factory for ataila_project.
func NewProjectResource() resource.Resource { return &projectResource{} }

type projectResource struct {
	data *ProviderData
}

func (r *projectResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}

func projectSettingResourceAttribute(s projectSetting) schema.Attribute {
	switch s.kind {
	case kindBool:
		return schema.BoolAttribute{
			MarkdownDescription: s.doc + " Leave it out to keep the current value.",
			Optional:            true,
			Computed:            true,
			PlanModifiers:       []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
		}
	case kindInt:
		var v validator.Int64
		if s.ints != nil {
			v = int64validator.OneOf(s.ints...)
		} else {
			v = int64validator.Between(0, s.max)
		}
		return schema.Int64Attribute{
			MarkdownDescription: s.doc + " Leave it out to keep the current value.",
			Optional:            true,
			Computed:            true,
			Validators:          []validator.Int64{v},
			PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
		}
	}
	switch {
	case s.name == "long_name":
		return schema.StringAttribute{
			MarkdownDescription: s.doc,
			Required:            true,
			Validators: []validator.String{
				stringvalidator.LengthBetween(2, 60),
				stringvalidator.RegexMatches(rxLongName, "must not contain quotes, apostrophes, backslashes or control characters"),
			},
		}
	case s.nullable:
		var vs []validator.String
		if s.name == "description" {
			vs = append(vs, stringvalidator.LengthAtMost(2000))
		}
		return schema.StringAttribute{MarkdownDescription: s.doc, Optional: true, Validators: vs}
	}
	return schema.StringAttribute{
		MarkdownDescription: s.doc + " Leave it out to keep the current value.",
		Optional:            true,
		Computed:            true,
		Validators:          []validator.String{stringvalidator.OneOf(s.enum...)},
		PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
	}
}

func (r *projectResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	d := projectDocs
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	attrs := map[string]schema.Attribute{
		"id": schema.StringAttribute{MarkdownDescription: d["id"], Computed: true, PlanModifiers: keep},
		"tenant_id": schema.StringAttribute{
			MarkdownDescription: d["tenant_id"],
			Required:            true,
			Validators:          []validator.String{stringvalidator.RegexMatches(rxUUID, "must be a tenant id (a UUID)")},
			PlanModifiers:       []planmodifier.String{sameFold{}, projectFrozen.forString()},
		},
		"customer_id": schema.StringAttribute{MarkdownDescription: d["customer_id"], Computed: true, PlanModifiers: keep},
		"project_index": schema.Int64Attribute{
			MarkdownDescription: d["project_index"],
			Optional:            true,
			Computed:            true,
			Validators:          []validator.Int64{int64validator.Between(1, 99)},
			PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown(), projectFrozen.forInt64()},
		},
		"short_name": schema.StringAttribute{
			MarkdownDescription: d["short_name"],
			Required:            true,
			Validators: []validator.String{stringvalidator.RegexMatches(rxProjectShort,
				"must be a lower-case letter followed by 1-10 lower-case letters or digits")},
			PlanModifiers: []planmodifier.String{projectFrozen.forString()},
		},
		"gitlab_repo_slug": schema.StringAttribute{
			MarkdownDescription: d["gitlab_repo_slug"],
			Required:            true,
			Validators: []validator.String{stringvalidator.RegexMatches(rxRepoSlug,
				"must be a lower-case letter followed by 1-40 lower-case letters, digits or hyphens")},
			PlanModifiers: []planmodifier.String{projectFrozen.forString()},
		},
		"primary_domain": schema.StringAttribute{
			MarkdownDescription: d["primary_domain"],
			Required:            true,
			Validators: []validator.String{
				stringvalidator.LengthAtMost(253),
				stringvalidator.RegexMatches(rxFQDN, "must be a fully qualified domain name, without spaces"),
			},
			PlanModifiers: []planmodifier.String{sameFold{}, projectFrozen.forString()},
		},
		"deployment_backend": schema.StringAttribute{
			MarkdownDescription: d["deployment_backend"],
			Optional:            true,
			Computed:            true,
			Validators:          []validator.String{stringvalidator.OneOf("k8s", "vm")},
			PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown(), projectFrozen.forString()},
		},
		"network_only": schema.BoolAttribute{
			MarkdownDescription: d["network_only"],
			Optional:            true,
			Computed:            true,
			PlanModifiers:       []planmodifier.Bool{boolplanmodifier.UseStateForUnknown(), projectFrozen.forBool()},
		},
		"status":        schema.StringAttribute{MarkdownDescription: d["status"], Computed: true, PlanModifiers: keep},
		"is_self":       schema.BoolAttribute{MarkdownDescription: d["is_self"], Computed: true, PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}},
		"registered_by": schema.StringAttribute{MarkdownDescription: d["registered_by"], Computed: true, PlanModifiers: keep},
		"created_at": schema.StringAttribute{
			MarkdownDescription: d["created_at"], CustomType: TimestampType{}, Computed: true, PlanModifiers: keep,
		},
		"stale_stages": schema.ListAttribute{MarkdownDescription: d["stale_stages"], Computed: true, ElementType: types.StringType},
		"urls": schema.SingleNestedAttribute{
			MarkdownDescription: d["urls"],
			Computed:            true,
			Attributes: map[string]schema.Attribute{
				"static":   schema.StringAttribute{Computed: true, MarkdownDescription: "The web site (`www.`)."},
				"frontend": schema.StringAttribute{Computed: true, MarkdownDescription: "The application front end (`app.`)."},
				"backend":  schema.StringAttribute{Computed: true, MarkdownDescription: "The application API (`api.`)."},
				"ai":       schema.StringAttribute{Computed: true, MarkdownDescription: "The AI endpoint (`ai.`)."},
			},
		},
		"image_registry_namespace": schema.StringAttribute{MarkdownDescription: d["image_registry_namespace"], Computed: true},
		"gitlab_repositories":      resourceNestedList(d["gitlab_repositories"], projectRepoTypes, contractDoc("GitLabRepository"), nil),
		"kubernetes_namespaces":    resourceNestedList(d["kubernetes_namespaces"], projectNSTypes, contractDoc("KubernetesNamespace"), nil),
		"secret_paths":             resourceNestedList(d["secret_paths"], projectSecretTypes, contractDoc("SecretPath"), nil),
		"warnings":                 resourceNestedList(d["warnings"], warningAttrTypes, contractDoc("ApiWarning"), nil),
		"k8s_quota":                projectQuotaResourceAttribute(),
	}
	for _, s := range projectSettings {
		attrs[s.name] = projectSettingResourceAttribute(s)
	}
	resp.Schema = schema.Schema{
		Version: 1,
		MarkdownDescription: "A project: an application (or a network zone) of a tenant, with its settings.\n\n" +
			"Creating a project **records** it; it provisions nothing and stays `planned`. Provisioning is " +
			"a separate resource, `ataila_project_provisioning`. Changing a setting changes the record and " +
			"marks the provisioning stages it affects stale (`stale_stages`); the next provisioning run " +
			"re-applies them.\n\n" +
			"**Frozen keys** (`tenant_id`, `project_index`, `short_name`, `gitlab_repo_slug`, " +
			"`primary_domain`, `deployment_backend`, `network_only`) are set at create and never change. " +
			"Changing one **fails the plan**; the provider never replaces a project.\n\n" +
			"Settings left out of the configuration get the platform's default at create and keep their " +
			"current value afterwards. `description`, `github_user` and `github_repo_url` are the exception: " +
			"leaving them out clears them.\n\n" +
			"A Kubernetes project's namespace quota, GPU scheduling included, is `k8s_quota`: set the keys of " +
			"an environment and the provider sends them to the platform's quota editor after the project is " +
			"created or changed (one request per environment; the platform re-applies the project's GitOps " +
			"stages itself).\n\n" +
			"The platform's own projects (`is_self`) cannot be imported or changed through the API; the " +
			"provider refuses at plan time.\n\n" +
			"**Destroy retires** the project and needs `allow_destroy = true` on the provider **and** a " +
			"token minted with destroy allowed. Retiring touches nothing on the substrate; a retired " +
			"project keeps its `project_index`, `short_name` and `primary_domain` reserved for good. " +
			"Destroying a project that is already retired only removes it from the state.",
		Attributes: attrs,
	}
}

// sameFold keeps the state's value when the configuration differs from it
// only in letter case (the platform stores these in lower case).
type sameFold struct{}

func (sameFold) Description(context.Context) string {
	return "A value that differs from the state only in letter case is no change."
}
func (m sameFold) MarkdownDescription(ctx context.Context) string { return m.Description(ctx) }
func (sameFold) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.StateValue.IsNull() || req.StateValue.IsUnknown() || resp.PlanValue.IsNull() || resp.PlanValue.IsUnknown() {
		return
	}
	if strings.EqualFold(req.StateValue.ValueString(), resp.PlanValue.ValueString()) {
		resp.PlanValue = req.StateValue
	}
}

func (r *projectResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *projectResource) configured(diags interface{ AddError(string, string) }) bool {
	if r.data == nil {
		diags.AddError("Provider not configured", "The ataila provider was not configured.")
		return false
	}
	return true
}

// ValidateConfig refuses the combinations the platform would refuse or
// silently override.
func (r *projectResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var networkOnly, static, fullstack, mssql, iis types.Bool
	var backend types.String
	var counts [3]types.Int64
	for name, target := range map[string]any{"network_only": &networkOnly, "enable_static_site": &static,
		"enable_fullstack_app": &fullstack, "enable_mssql": &mssql, "enable_iis": &iis,
		"deployment_backend": &backend, "windows_vm_count_prod": &counts[0], "windows_vm_count_uat": &counts[1],
		"windows_vm_count_dev": &counts[2]} {
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root(name), target)...)
	}
	if resp.Diagnostics.HasError() {
		return
	}
	if networkOnly.ValueBool() {
		for name, v := range map[string]types.Bool{"enable_static_site": static, "enable_fullstack_app": fullstack} {
			if v.ValueBool() {
				resp.Diagnostics.AddAttributeError(path.Root(name), "Not possible on a network-only project",
					fmt.Sprintf("network_only = true registers the network zone only; the platform forces %s "+
						"off. Leave %s out or set it to false.", name, name))
			}
		}
	}
	if backend.IsUnknown() {
		return
	}
	var quota types.Object
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("k8s_quota"), &quota)...)
	if backend.ValueString() == "vm" && !quota.IsNull() && !quota.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("k8s_quota"), "Only a Kubernetes project has a namespace quota",
			"k8s_quota applies to deployment_backend = \"k8s\" projects only: a VM project has no namespaces. "+
				"Remove k8s_quota, or create the project with the k8s backend.")
	}
	windows := []string{}
	if mssql.ValueBool() {
		windows = append(windows, "enable_mssql")
	}
	if iis.ValueBool() {
		windows = append(windows, "enable_iis")
	}
	for i, name := range []string{"windows_vm_count_prod", "windows_vm_count_uat", "windows_vm_count_dev"} {
		if counts[i].ValueInt64() > 0 {
			windows = append(windows, name)
		}
	}
	if len(windows) > 0 && backend.ValueString() != "vm" {
		resp.Diagnostics.AddAttributeError(path.Root("deployment_backend"), "Windows workloads need the vm backend",
			fmt.Sprintf("%s need deployment_backend = \"vm\" (the platform's default is k8s). Set "+
				"deployment_backend = \"vm\" when creating the project; it cannot change later.",
				strings.Join(windows, ", ")))
	}
}

func stateObject(ctx context.Context, get func(context.Context, any) diag.Diagnostics) (types.Object, map[string]attr.Value, diag.Diagnostics) {
	var obj types.Object
	d := get(ctx, &obj)
	if obj.IsNull() || obj.IsUnknown() {
		return obj, map[string]attr.Value{}, d
	}
	return obj, obj.Attributes(), d
}

func strAttr(v map[string]attr.Value, name string) string {
	if s, ok := v[name].(types.String); ok {
		return s.ValueString()
	}
	if s, ok := v[name].(TimestampValue); ok {
		return s.ValueString()
	}
	return ""
}

func boolAttr(v map[string]attr.Value, name string) bool {
	b, _ := v[name].(types.Bool)
	return b.ValueBool()
}

func projectIdent(v map[string]attr.Value) string {
	if s := strAttr(v, "short_name"); s != "" {
		return fmt.Sprintf("%s (id %s)", s, strAttr(v, "id"))
	}
	return "with id " + strAttr(v, "id")
}

func (r *projectResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	// k8s_quota came with platform release 1.0.203; on an older platform a
	// configuration that sets it is refused here, on create and on update.
	if !req.Plan.Raw.IsNull() {
		var quota types.Object
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("k8s_quota"), &quota)...)
		if !quota.IsNull() {
			if d := r.data.featureRefused(client.FeatureK8sQuota, path.Root("k8s_quota"), "k8s_quota"); d != nil {
				resp.Diagnostics.Append(d)
				return
			}
		}
	}
	if req.State.Raw.IsNull() {
		return // create
	}
	_, state, d := stateObject(ctx, req.State.Get)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ident := projectIdent(state)
	retired := strAttr(state, "status") == "retired"
	if req.Plan.Raw.IsNull() {
		switch {
		case boolAttr(state, "is_self"):
			resp.Diagnostics.Append(platformProjectRefused(ident, "destroy"))
		case retired:
			// Nothing to do on the platform: destroy only removes it from the state.
		case r.data != nil && !r.data.AllowDestroy:
			resp.Diagnostics.Append(destroyRefused("ataila_project", "project", ident))
		}
		return
	}
	_, plan, d := stateObject(ctx, req.Plan.Get)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	changed := false
	for _, name := range projectFrozenKeys {
		changed = changed || !plan[name].Equal(state[name])
	}
	for _, s := range projectSettings {
		changed = changed || !plan[s.name].Equal(state[s.name])
	}
	if quotaMissing(plan["k8s_quota"], state["k8s_quota"]) && !retired && !boolAttr(state, "is_self") {
		resp.Diagnostics.AddAttributeError(path.Root("k8s_quota"), "The platform reports no namespace quota for this project",
			fmt.Sprintf("The project %s has no k8s_quota in the platform's answer, so it cannot be changed: either the "+
				"project is not a Kubernetes project with a compiled manifest, or this platform release predates "+
				"k8s_quota on the API (it needs a later release). Remove k8s_quota from the configuration.", ident))
		return
	}
	changed = changed || quotaChanged(plan["k8s_quota"], state["k8s_quota"])
	if !changed {
		// Nothing the configuration sets differs (a spelling the platform
		// normalises, for example): no update, and the outputs stay known.
		// The quota keeps what the configuration writes (a respelt count, a
		// reason with no override to carry it); the update then only records
		// it in the state.
		if !req.Plan.Raw.Equal(req.State.Raw) {
			resp.Plan.Raw = req.State.Raw.Copy()
			resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("k8s_quota"),
				quotaPlanned(plan["k8s_quota"], state["k8s_quota"]))...)
		}
		return
	}
	switch {
	case boolAttr(state, "is_self"):
		resp.Diagnostics.Append(platformProjectRefused(ident, "change"))
	case retired:
		resp.Diagnostics.AddError("The project is retired",
			fmt.Sprintf("The project %s is retired. A retired project can never be changed again, and its "+
				"project_index, short_name and primary_domain stay reserved.\n\n"+
				"Set the configuration back to the values in the state, or remove the project from the "+
				"configuration and the state:\n"+
				"  tofu state rm ataila_project.<name>       (OpenTofu)\n"+
				"  terraform state rm ataila_project.<name>  (Terraform)", ident))
	}
}

func platformProjectRefused(ident, what string) diag.Diagnostic {
	return diag.NewErrorDiagnostic("The platform's own projects are read-only",
		fmt.Sprintf("The project %s is one of the platform's own projects (is_self = true). They can be "+
			"read, never changed, provisioned or retired through the API, so the provider refuses to %s it.\n\n"+
			"Read it with the ataila_project data source instead, and remove the resource from the state:\n"+
			"  tofu state rm ataila_project.<name>       (OpenTofu)\n"+
			"  terraform state rm ataila_project.<name>  (Terraform)", ident, what))
}

func (r *projectResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	planObj, plan, d := stateObject(ctx, req.Plan.Get)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{
		"tenant_id":        strAttr(plan, "tenant_id"),
		"short_name":       strAttr(plan, "short_name"),
		"gitlab_repo_slug": strAttr(plan, "gitlab_repo_slug"),
		"primary_domain":   strAttr(plan, "primary_domain"),
	}
	if v, ok := plan["project_index"].(types.Int64); ok && !v.IsNull() && !v.IsUnknown() {
		body["project_index"] = v.ValueInt64()
	}
	if v, ok := plan["deployment_backend"].(types.String); ok && !v.IsNull() && !v.IsUnknown() {
		body["deployment_backend"] = v.ValueString()
	}
	if v, ok := plan["network_only"].(types.Bool); ok && !v.IsNull() && !v.IsUnknown() {
		body["network_only"] = v.ValueBool()
	}
	for _, s := range projectSettings {
		if val, ok := settingBodyValue(s, plan[s.name]); ok {
			body[s.name] = val
		}
	}
	short := strAttr(plan, "short_name")
	p, err := r.data.API.CreateProject(ctx, body)
	if err != nil {
		resp.Diagnostics.Append(projectCreateError(short, err))
		return
	}
	_, ws := projectWarnings(p)
	addWarnings(&resp.Diagnostics, "creating the project "+short, ws)
	// The quota, when the configuration sets one: the create takes none, so
	// it follows as the quota editor's own requests. A refusal here leaves the
	// project created; the state records it so that the next apply retries
	// the quota alone.
	quotaPlan := quotaPrior(plan["k8s_quota"], nil, nil)
	if patches := quotaPatches(plan["k8s_quota"], nil); len(patches) > 0 {
		id := p.String("id")
		ident := fmt.Sprintf("%s (id %s)", short, id)
		qws, done, ok := applyQuota(ctx, r.data.API, id, ident, patches, &resp.Diagnostics)
		*ws = append(*ws, qws...)
		quotaPlan = quotaPrior(plan["k8s_quota"], nil, quotaUnapplied(patches, done))
		if !ok {
			// The project exists; only its quota was refused. The error above
			// taints the resource, and a replacement would RETIRE the project
			// (its names stay reserved for good) — so say how to keep it.
			resp.Diagnostics.AddError("The project was created, its quota was refused",
				fmt.Sprintf("The project %s is created and recorded in the state, but the quota above was "+
					"refused, so the resource is tainted. Do NOT let the next apply replace it (that retires the "+
					"project): clear the taint, fix k8s_quota, and apply again:\n"+
					"  tofu untaint ataila_project.<name>       (OpenTofu)\n"+
					"  terraform untaint ataila_project.<name>  (Terraform)", ident))
		}
		// Read back what the quota requests changed. Should that read fail,
		// the create's own answer still goes into the state, with each
		// environment the requests changed as the platform answered it: the
		// project exists, and the next refresh reads the rest.
		if again, err := r.data.API.GetProject(ctx, id); err != nil {
			resp.Diagnostics.Append(apiError("reading the project "+ident+" after setting its quota", err))
			p = withQuotaAnswers(p, done)
		} else {
			p = again
		}
	}
	prior := map[string]attr.Value{}
	for k, v := range plan {
		prior[k] = v
	}
	prior["k8s_quota"] = quotaPlan
	values := projectValues(p, prior, nil)
	values["warnings"] = warningsValue(*ws)
	resp.Diagnostics.Append(resp.State.Set(ctx, objectFrom(ctx, planObj.Type(ctx).(basetypes.ObjectType), values, &resp.Diagnostics))...)
}

func (r *projectResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	stateObj, state, d := stateObject(ctx, req.State.Get)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := strAttr(state, "id")
	p, err := r.data.API.GetProject(ctx, id)
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.Append(apiError("reading the project "+projectIdent(state), err))
		return
	}
	prov, err := r.data.API.GetProvisioning(ctx, id)
	if err != nil {
		resp.Diagnostics.Append(apiError("reading the provisioning of the project "+projectIdent(state), err))
		return
	}
	values := projectValues(p, state, prov.StaleStages)
	prior, _ := state["warnings"].(types.List)
	values["warnings"] = keepWarnings(prior)
	resp.Diagnostics.Append(resp.State.Set(ctx, objectFrom(ctx, stateObj.Type(ctx).(basetypes.ObjectType), values, &resp.Diagnostics))...)
}

func (r *projectResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
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
	ident := projectIdent(state)

	// Frozen keys again: a value unknown at plan time is known now. Nothing
	// has been sent yet.
	for _, name := range projectFrozenKeys {
		pv, sv := plan[name].(tfValue), state[name].(tfValue)
		if name == "tenant_id" || name == "primary_domain" {
			if strings.EqualFold(strAttr(plan, name), strAttr(state, name)) {
				continue
			}
		}
		projectFrozen.check(path.Root(name), sv, pv, true, &resp.Diagnostics)
	}
	if resp.Diagnostics.HasError() {
		return
	}

	patch := client.Patch{}
	for _, s := range projectSettings {
		pv, sv := plan[s.name], state[s.name]
		if pv == nil || pv.IsUnknown() || pv.Equal(sv) {
			continue
		}
		if pv.IsNull() {
			if s.nullable {
				patch[s.name] = nil
			}
			continue
		}
		val, _ := settingBodyValue(s, pv)
		patch[s.name] = val
	}

	id := strAttr(state, "id")
	var (
		p   client.ProjectData
		err error
	)
	if len(patch) == 0 {
		p, err = r.data.API.GetProject(ctx, id)
	} else {
		p, err = r.data.API.UpdateProject(ctx, id, patch)
	}
	if err != nil {
		resp.Diagnostics.Append(projectWriteError("changing the project "+ident, ident, err))
		return
	}
	warnings, ws := projectWarnings(p)
	addWarnings(&resp.Diagnostics, "changing the project "+ident, ws)
	// The quota: one request per environment the plan needs, after the
	// project's own change (the settings never depend on the quota, the quota
	// never on the settings). A refusal fails the apply with the platform's
	// message, and the state still records what did change — the project's own
	// change and the environments before the refused one — read back from the
	// platform, so the next plan shows only what is left. Every read after a
	// change that went through only adds an error when it fails: the state is
	// then built from the answers already in hand (the settings answer, the
	// quota answers), never left as it was before the change.
	quotaPlan := quotaPrior(plan["k8s_quota"], state["k8s_quota"], nil)
	if patches := quotaPatches(plan["k8s_quota"], state["k8s_quota"]); len(patches) > 0 {
		qws, done, _ := applyQuota(ctx, r.data.API, id, ident, patches, &resp.Diagnostics)
		*ws = append(*ws, qws...)
		warnings = warningsValue(*ws)
		quotaPlan = quotaPrior(plan["k8s_quota"], state["k8s_quota"], quotaUnapplied(patches, done))
		if again, err := r.data.API.GetProject(ctx, id); err != nil {
			resp.Diagnostics.Append(apiError("reading the project "+ident+" after changing its quota", err))
			p = withQuotaAnswers(p, done)
		} else {
			p = again
		}
	}
	prior := map[string]attr.Value{}
	for k, v := range plan {
		prior[k] = v
	}
	prior["k8s_quota"] = quotaPlan
	prov, err := r.data.API.GetProvisioning(ctx, id)
	var stale []string
	if err != nil {
		resp.Diagnostics.Append(apiError("reading the provisioning of the project "+ident, err))
	} else {
		stale = prov.StaleStages
	}
	values := projectValues(p, prior, stale)
	if err != nil {
		values["stale_stages"] = state["stale_stages"] // unknown now: the last one read
	}
	values["warnings"] = warnings
	resp.Diagnostics.Append(resp.State.Set(ctx, objectFrom(ctx, planObj.Type(ctx).(basetypes.ObjectType), values, &resp.Diagnostics))...)
}

// projectCreateError explains a create refused because values are taken: the
// platform lists every conflict (the first one is the code).
func projectCreateError(short string, err error) diag.Diagnostic {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 409 {
		return apiError("creating the project "+short, err)
	}
	raw, _ := apiErr.Extra("conflicts")
	list, _ := raw.([]any)
	if len(list) == 0 {
		return apiError("creating the project "+short, err)
	}
	var lines []string
	for _, item := range list {
		c, _ := item.(map[string]any)
		line := fmt.Sprintf("  - %v %q", c["key"], fmt.Sprint(c["value"]))
		if id, ok := c["project_id"]; ok && id != nil {
			line += fmt.Sprintf(" is held by project %v", id)
		}
		lines = append(lines, line)
	}
	return diag.NewErrorDiagnostic(fmt.Sprintf("The project's reserved values are taken (%s)", apiErr.Code()),
		fmt.Sprintf("While creating the project %s the platform found these values held already:\n%s\n\n"+
			"A retired project keeps its project_index, short_name and primary_domain reserved for good. "+
			"Choose other values, or import the project that holds them.\n\n%s",
			short, strings.Join(lines, "\n"), apiErr.Detail()))
}

// projectWriteError explains the refusals a change of a project can meet.
func projectWriteError(doing, ident string, err error) diag.Diagnostic {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == 409 {
		switch apiErr.Code() {
		case client.CodePlatformReadOnly:
			return platformProjectRefused(ident, "change")
		case client.CodeProjectRetired:
			return diag.NewErrorDiagnostic("The project is retired",
				fmt.Sprintf("The project %s is retired and can never be changed again. Remove it from the "+
					"configuration and the state (tofu state rm / terraform state rm).\n\n%s", ident, apiErr.Detail()))
		}
	}
	return apiError(doing, err)
}

func (r *projectResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	_, state, d := stateObject(ctx, req.State.Get)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	ident := projectIdent(state)
	switch {
	case boolAttr(state, "is_self"):
		resp.Diagnostics.Append(platformProjectRefused(ident, "destroy"))
		return
	case strAttr(state, "status") == "retired":
		return
	case !r.data.AllowDestroy:
		resp.Diagnostics.Append(destroyRefused("ataila_project", "project", ident))
		return
	}
	err := r.data.API.RetireProject(ctx, strAttr(state, "id"))
	if err != nil && !isNotFound(err) {
		resp.Diagnostics.Append(destroyError("ataila_project", "project", ident, err))
	}
}

func (r *projectResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	id := strings.TrimSpace(req.ID)
	if short, ok := strings.CutPrefix(id, ShortNameImportPrefix); ok {
		found, err := r.data.API.ListProjects(ctx, client.ProjectFilter{ShortName: short})
		if err != nil {
			resp.Diagnostics.Append(apiError("looking up the project with short_name "+short, err))
			return
		}
		if len(found) != 1 {
			resp.Diagnostics.AddError("Cannot import the project",
				fmt.Sprintf("%d projects have short_name %q; the import needs exactly one.", len(found), short))
			return
		}
		id = found[0].String("id")
	}
	if !rxIntID.MatchString(id) {
		resp.Diagnostics.AddError("Cannot import the project",
			fmt.Sprintf("%q is not a project id (1 to 999999999, no leading zero). Give the id, or "+
				"short_name:<short_name>.", id))
		return
	}
	p, err := r.data.API.GetProject(ctx, id)
	if err != nil {
		resp.Diagnostics.Append(apiError("reading the project "+id+" to import it", err))
		return
	}
	if p.Bool("is_self") {
		resp.Diagnostics.Append(diag.NewErrorDiagnostic("The platform's own projects cannot be imported",
			fmt.Sprintf("The project %s (id %s) is one of the platform's own projects (is_self = true). They "+
				"are read-only through the API, so the provider does not manage them. Read it with the "+
				"ataila_project data source instead.", p.String("short_name"), id)))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

// UpgradeState renames the attributes 0.7.0 renamed (schema version 0 to 1).
func (r *projectResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return renameUpgraders(projectRenamesV1)
}
