// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ataila/terraform-provider-ataila/internal/client"
)

// SingletonImportID imports the platform's licence bundle and its brand.
const SingletonImportID = "current"

var (
	_ resource.ResourceWithConfigure   = (*licenceBundleResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*licenceBundleResource)(nil)
	_ resource.ResourceWithImportState = (*licenceBundleResource)(nil)
)

// ── ataila_licence_bundle ────────────────────────────────────────────────────

// NewLicenceBundleResource is the factory for ataila_licence_bundle.
func NewLicenceBundleResource() resource.Resource { return &licenceBundleResource{} }

type licenceBundleResource struct {
	data *ProviderData
}

type licenceBundleModel struct {
	ID             types.String   `tfsdk:"id"`
	Bundle         types.String   `tfsdk:"bundle"`
	DocumentDigest types.String   `tfsdk:"document_digest"`
	State          types.String   `tfsdk:"state"`
	LicenceID      types.String   `tfsdk:"licence_id"`
	LicenceEpoch   types.Int64    `tfsdk:"licence_epoch"`
	Tier           types.String   `tfsdk:"tier"`
	ValidUntil     TimestampValue `tfsdk:"valid_until"`
	InstalledAt    TimestampValue `tfsdk:"installed_at"`
}

func (m *licenceBundleModel) fromAPI(l *client.Licence) {
	m.ID = types.StringValue(SingletonImportID)
	m.DocumentDigest = stringOrNull(l.DocumentDigest)
	m.State = types.StringValue(string(l.State))
	m.LicenceID = stringOrNull(l.LicenceId)
	m.LicenceEpoch = int64OrNull(l.LicenceEpoch)
	m.Tier = stringOrNull(l.Tier)
	m.ValidUntil = NewTimestampPointer(l.ValidUntil)
	m.InstalledAt = NewTimestampPointer(l.InstalledAt)
}

func (r *licenceBundleResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_licence_bundle"
}

func (r *licenceBundleResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "The licence bundle installed on the platform (a singleton).\n\n" +
			"**Create** installs the bundle, unless the platform already holds exactly this document (its " +
			"`document_digest` matches the bundle's), which is adopted as it is. The platform installs a " +
			"bundle only when its epoch is higher than the installed document's; an older one (409 " +
			"`stale_epoch`) is an error that is not retried.\n\n" +
			"**Read** reads the licence. When the installed document is not this bundle's any more (another " +
			"bundle was installed outside this configuration), the next plan shows an update of " +
			"`document_digest`; applying it installs this bundle again, which the platform refuses when the " +
			"installed one is newer. Nothing is ever replaced.\n\n" +
			"**Destroy only removes the resource from the state**: there is no way to remove a licence. " +
			"The provider never activates, signs or reveals a licence.\n\n" +
			"Needs the permission `licence-admin-global`. Import with the id `current`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Always `current`.",
				Computed:            true,
				PlanModifiers:       keep,
			},
			"bundle": schema.StringAttribute{
				MarkdownDescription: "The `acplic1.` bundle issued for this platform. **Sensitive.** Read it " +
					"from a file, for example `file(\"licence.acplic1\")`.",
				Required:  true,
				Sensitive: true,
				Validators: []validator.String{stringvalidator.LengthBetween(1, 65536),
					bundleValidator{}},
			},
			"document_digest": schema.StringAttribute{
				MarkdownDescription: "sha256 (hex) of the installed licence document. The provider plans an " +
					"update when it differs from the bundle's.",
				Computed: true,
			},
			"state": schema.StringAttribute{
				MarkdownDescription: "The licence state, for example `ACTIVE`, `EXPIRING`, `GRACE` or `LOCKED`.",
				Computed:            true,
			},
			"licence_id":    schema.StringAttribute{MarkdownDescription: "The installed licence's id.", Computed: true},
			"licence_epoch": schema.Int64Attribute{MarkdownDescription: "The installed document's epoch.", Computed: true},
			"tier":          schema.StringAttribute{MarkdownDescription: "The licensed tier.", Computed: true},
			"valid_until": schema.StringAttribute{
				MarkdownDescription: "End of the licence term: RFC 3339 in UTC, compared as an instant.",
				CustomType:          TimestampType{}, Computed: true,
			},
			"installed_at": schema.StringAttribute{
				MarkdownDescription: "When the installed document was installed.",
				CustomType:          TimestampType{}, Computed: true,
			},
		},
	}
}

// bundleValidator checks that a bundle can be decoded (not that it verifies:
// that is the platform's).
type bundleValidator struct{}

func (bundleValidator) Description(context.Context) string {
	return "must be an acplic1. bundle holding a document"
}
func (v bundleValidator) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }
func (bundleValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if _, err := client.BundleDocumentDigest(req.ConfigValue.ValueString()); err != nil {
		// Never echo the value: it is sensitive.
		resp.Diagnostics.AddAttributeError(req.Path, "Not a licence bundle",
			fmt.Sprintf("The value cannot be read as a licence bundle: %v. Give the %s string the portal or the "+
				"issuer produced, unchanged.", err, client.BundlePrefix))
	}
}

func (r *licenceBundleResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *licenceBundleResource) configured(diags interface{ AddError(string, string) }) bool {
	if r.data == nil {
		diags.AddError("Provider not configured", "The ataila provider was not configured.")
		return false
	}
	return true
}

// ModifyPlan plans an update when the installed document is not the
// configured bundle's, and none otherwise.
func (r *licenceBundleResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan licenceBundleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || plan.Bundle.IsUnknown() {
		return
	}
	digest, err := client.BundleDocumentDigest(plan.Bundle.ValueString())
	if err != nil {
		return // the validator reports it
	}
	if req.State.Raw.IsNull() {
		plan.DocumentDigest = types.StringValue(digest)
		resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
		return
	}
	var state licenceBundleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.DocumentDigest.ValueString() == digest {
		// This bundle is installed: nothing to do, even if the configured text
		// changed spelling (whitespace) or the state has no bundle (import).
		plan.DocumentDigest = state.DocumentDigest
		plan.State, plan.LicenceID, plan.LicenceEpoch = state.State, state.LicenceID, state.LicenceEpoch
		plan.Tier, plan.ValidUntil, plan.InstalledAt = state.Tier, state.ValidUntil, state.InstalledAt
	} else {
		plan.DocumentDigest = types.StringValue(digest)
		plan.State, plan.LicenceID, plan.Tier = types.StringUnknown(), types.StringUnknown(), types.StringUnknown()
		plan.LicenceEpoch = types.Int64Unknown()
		plan.ValidUntil = TimestampValue{StringValue: types.StringUnknown()}
		plan.InstalledAt = TimestampValue{StringValue: types.StringUnknown()}
	}
	plan.ID = types.StringValue(SingletonImportID)
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

// install installs the configured bundle unless it is installed already, and
// returns the licence after it.
func (r *licenceBundleResource) install(ctx context.Context, bundle string, diags *diag.Diagnostics) *client.Licence {
	digest, err := client.BundleDocumentDigest(bundle)
	if err != nil {
		diags.AddAttributeError(path.Root("bundle"), "Not a licence bundle", err.Error())
		return nil
	}
	current, err := r.data.API.GetLicence(ctx)
	if err != nil {
		diags.Append(apiError("reading the licence", err))
		return nil
	}
	if ptrString(current.DocumentDigest) == digest {
		return current // already installed: adopted
	}
	installed, err := r.data.API.PutLicenceBundle(ctx, bundle)
	if err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 409 && apiErr.Code() == client.CodeStaleEpoch {
			// The platform says whether the sent document is the installed one;
			// the digest is the cross-check (and the answer of older platforms).
			already, _ := apiErr.Extra("already_installed")
			if d, _ := apiErr.Extra("installed_document_digest"); already == true || d == digest {
				return r.readAfter(ctx, diags)
			}
			epoch, _ := apiErr.Extra("installed_epoch")
			diags.AddAttributeError(path.Root("bundle"), "The platform refused the bundle (stale_epoch)",
				fmt.Sprintf("%s\n\nThe platform installs a bundle only when its epoch is higher than the "+
					"installed document's (epoch %v). This bundle is older than, or as old as, the one installed, "+
					"and is not the same document. The provider does not retry. Use the bundle issued last, or "+
					"remove this resource from the state to leave the installed licence as it is.",
					apiErr.Detail(), epoch))
			return nil
		}
		diags.Append(apiError("installing the licence bundle", err))
		return nil
	}
	addWarnings(diags, "installing the licence bundle", installed.Warnings)
	return r.readAfter(ctx, diags)
}

func (r *licenceBundleResource) readAfter(ctx context.Context, diags *diag.Diagnostics) *client.Licence {
	l, err := r.data.API.GetLicence(ctx)
	if err != nil {
		diags.Append(apiError("reading the licence", err))
		return nil
	}
	return l
}

func (r *licenceBundleResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan licenceBundleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	l := r.install(ctx, plan.Bundle.ValueString(), &resp.Diagnostics)
	if l == nil {
		return
	}
	r.store(ctx, &plan, l, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// store fills the model from the licence and checks that the configured
// bundle is what is installed.
func (r *licenceBundleResource) store(_ context.Context, m *licenceBundleModel, l *client.Licence, diags *diag.Diagnostics) {
	want, _ := client.BundleDocumentDigest(m.Bundle.ValueString())
	m.fromAPI(l)
	if got := ptrString(l.DocumentDigest); got != want {
		diags.AddError("The bundle is not the installed document",
			fmt.Sprintf("After the install the platform reports document digest %q, not the bundle's %q. "+
				"Another bundle may have been installed at the same time. Run plan again.", got, want))
	}
}

func (r *licenceBundleResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var state licenceBundleModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	l, err := r.data.API.GetLicence(ctx)
	if err != nil {
		resp.Diagnostics.Append(apiError("reading the licence", err))
		return
	}
	state.fromAPI(l)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *licenceBundleResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan licenceBundleModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	l := r.install(ctx, plan.Bundle.ValueString(), &resp.Diagnostics)
	if l == nil {
		return
	}
	r.store(ctx, &plan, l, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete forgets the resource: a licence cannot be removed.
func (r *licenceBundleResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}

func (r *licenceBundleResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if strings.TrimSpace(req.ID) != SingletonImportID {
		resp.Diagnostics.AddError("Cannot import the licence bundle",
			fmt.Sprintf("The licence is a singleton; its import id is %q, got %q.", SingletonImportID, req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), SingletonImportID)...)
}

// ── ataila_licence ───────────────────────────────────────────────────────────

// NewLicenceDataSource is the factory for the ataila_licence data source.
func NewLicenceDataSource() datasource.DataSource { return &licenceDataSource{} }

type licenceDataSource struct {
	data *ProviderData
}

var licenceSocketTypes = map[string]attr.Type{"licensed": types.Int64Type, "uncapped": types.BoolType, "observed": types.Int64Type}

type licenceDataModel struct {
	State          types.String   `tfsdk:"state"`
	StateReason    types.String   `tfsdk:"state_reason"`
	Overlays       types.List     `tfsdk:"overlays"`
	Serial         types.String   `tfsdk:"serial"`
	SerialMasked   types.Bool     `tfsdk:"serial_masked"`
	LicenceID      types.String   `tfsdk:"licence_id"`
	LicenceEpoch   types.Int64    `tfsdk:"licence_epoch"`
	ProductCode    types.String   `tfsdk:"product_code"`
	Tier           types.String   `tfsdk:"tier"`
	LicenceClass   types.String   `tfsdk:"licence_class"`
	TenancyMode    types.String   `tfsdk:"tenancy_mode"`
	Modules        types.List     `tfsdk:"modules"`
	Sockets        types.Object   `tfsdk:"sockets"`
	ValidFrom      TimestampValue `tfsdk:"valid_from"`
	ValidUntil     TimestampValue `tfsdk:"valid_until"`
	GraceDays      types.Int64    `tfsdk:"grace_days"`
	DaysRemaining  types.Int64    `tfsdk:"days_remaining"`
	InstanceID     types.String   `tfsdk:"instance_id"`
	FQDN           types.String   `tfsdk:"fqdn"`
	BoundFQDN      types.String   `tfsdk:"bound_fqdn"`
	DocumentDigest types.String   `tfsdk:"document_digest"`
	InstalledAt    TimestampValue `tfsdk:"installed_at"`
	GrowthAllowed  types.Bool     `tfsdk:"growth_allowed"`
	WritesAllowed  types.Bool     `tfsdk:"writes_allowed"`
	SPModeEnabled  types.Bool     `tfsdk:"sp_mode_enabled"`
}

func (d *licenceDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_licence"
}

func (d *licenceDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	s := func(doc string) dschema.StringAttribute {
		return dschema.StringAttribute{MarkdownDescription: doc, Computed: true}
	}
	b := func(doc string) dschema.BoolAttribute {
		return dschema.BoolAttribute{MarkdownDescription: doc, Computed: true}
	}
	i := func(doc string) dschema.Int64Attribute {
		return dschema.Int64Attribute{MarkdownDescription: doc, Computed: true}
	}
	ts := func(doc string) dschema.StringAttribute {
		return dschema.StringAttribute{MarkdownDescription: doc + " RFC 3339 in UTC.", CustomType: TimestampType{}, Computed: true}
	}
	resp.Schema = dschema.Schema{
		MarkdownDescription: "The platform's licence, as the licence gate sees it. The installed bundle is " +
			"never read into the state. Needs `licence-read-global` or `licence-admin-global`.",
		Attributes: map[string]dschema.Attribute{
			"state": s("`UNLICENSED`, `PENDING_ACTIVATION`, `INVALID`, `DOMAIN_MISMATCH`, `ACTIVE`, " +
				"`EXPIRING`, `GRACE`, `LOCKED` or `REVOKED`."),
			"state_reason": s("Why the licence is in this state; empty when `ACTIVE`."),
			"overlays": dschema.ListAttribute{MarkdownDescription: "Conditions shown next to the state: " +
				"`clock_skew`, `over_deployed`.", Computed: true, ElementType: types.StringType},
			"serial": dschema.StringAttribute{MarkdownDescription: "The licence serial: in full for a token " +
				"holding `licence-admin-global`, masked to its last group otherwise. **Sensitive.**",
				Computed: true, Sensitive: true},
			"serial_masked": b("True when `serial` is masked for this token."),
			"licence_id":    s("The installed licence's id."),
			"licence_epoch": i("The installed document's epoch."),
			"product_code":  s("The licensed product."),
			"tier":          s("The licensed tier."),
			"licence_class": s("The licence class."),
			"tenancy_mode":  s("`single` or `multi`."),
			"modules":       dschema.ListAttribute{MarkdownDescription: "Entitled modules; empty unless the state gives full function.", Computed: true, ElementType: types.StringType},
			"sockets": dschema.SingleNestedAttribute{
				MarkdownDescription: "`licensed` (null when uncapped or unlicensed), `uncapped`, and `observed` " +
					"(what the latest census measured; null before the first).",
				Computed: true,
				Attributes: map[string]dschema.Attribute{
					"licensed": i("Sockets the licence allows."), "uncapped": b("No socket limit."),
					"observed": i("Sockets the latest census measured."),
				},
			},
			"valid_from":      ts("Start of the term."),
			"valid_until":     ts("End of the term."),
			"grace_days":      i("Days of grace after the term."),
			"days_remaining":  i("Whole days until `valid_until`; negative in grace."),
			"instance_id":     s("This platform's instance id, which a licence document binds."),
			"fqdn":            s("The name the platform answers on, as the licence engine derives it."),
			"bound_fqdn":      s("The name the installed licence is bound to."),
			"document_digest": s("sha256 (hex) of the installed licence document."),
			"installed_at":    ts("When the installed document was installed."),
			"growth_allowed":  b("The licence lets the platform grow (projects, AI, customers and tenants)."),
			"writes_allowed":  b("The licence lets ordinary changes through; false only when read-only."),
			"sp_mode_enabled": b("The licence lets the service-provider plane (customers and tenants) change."),
		},
	}
}

func (d *licenceDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *licenceDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.data == nil {
		resp.Diagnostics.AddError("Provider not configured", "The ataila provider was not configured.")
		return
	}
	l, err := d.data.API.GetLicence(ctx)
	if err != nil {
		summary, detail := apiErrorText("the licence (GET /licence)", err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	m := licenceDataModel{
		State: types.StringValue(string(l.State)), StateReason: types.StringValue(l.StateReason),
		Overlays: stringList(ctx, l.Overlays, &resp.Diagnostics), Serial: stringOrNull(l.Serial),
		SerialMasked: types.BoolValue(l.SerialMasked), LicenceID: stringOrNull(l.LicenceId),
		LicenceEpoch: int64OrNull(l.LicenceEpoch), ProductCode: stringOrNull(l.ProductCode),
		Tier: stringOrNull(l.Tier), LicenceClass: stringOrNull(l.LicenceClass),
		TenancyMode: stringOrNull(l.TenancyMode), Modules: stringList(ctx, l.Modules, &resp.Diagnostics),
		Sockets: types.ObjectValueMust(licenceSocketTypes, map[string]attr.Value{
			"licensed": int64OrNull(l.Sockets.Licensed), "uncapped": types.BoolValue(l.Sockets.Uncapped),
			"observed": int64OrNull(l.Sockets.Observed)}),
		ValidFrom: NewTimestampPointer(l.ValidFrom), ValidUntil: NewTimestampPointer(l.ValidUntil),
		GraceDays: int64OrNull(l.GraceDays), DaysRemaining: int64OrNull(l.DaysRemaining),
		InstanceID: stringOrNull(l.InstanceId), FQDN: stringOrNull(l.Fqdn), BoundFQDN: stringOrNull(l.BoundFqdn),
		DocumentDigest: stringOrNull(l.DocumentDigest), InstalledAt: NewTimestampPointer(l.InstalledAt),
		GrowthAllowed: types.BoolValue(l.GrowthAllowed), WritesAllowed: types.BoolValue(l.WritesAllowed),
		SPModeEnabled: types.BoolValue(l.SpModeEnabled),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// ── ataila_licence_socket_facts ──────────────────────────────────────────────

// NewLicenceSocketFactsDataSource is the factory for the
// ataila_licence_socket_facts data source.
func NewLicenceSocketFactsDataSource() datasource.DataSource { return &socketFactsDataSource{} }

type socketFactsDataSource struct {
	data *ProviderData
}

var (
	socketFactTypes = map[string]attr.Type{"node_name": types.StringType, "cluster": types.StringType,
		"sockets": types.Int64Type, "cores_per_socket": types.Int64Type, "source": types.StringType,
		"measured_at": types.StringType}
	socketChainTypes = map[string]attr.Type{"rows": types.Int64Type, "intact": types.BoolType,
		"first_broken_row": types.StringType}
)

type socketFactsModel struct {
	Facts types.List   `tfsdk:"facts"`
	Total types.Int64  `tfsdk:"total"`
	Chain types.Object `tfsdk:"chain"`
}

func (d *socketFactsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_licence_socket_facts"
}

func (d *socketFactsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dschema.Schema{
		MarkdownDescription: "The socket census the licence counts: the latest measurement per node, and " +
			"whether the census hash chain is intact. Read-only; the census runs on its own schedule.",
		Attributes: map[string]dschema.Attribute{
			"facts": dataNestedList("The latest measurement per node.", socketFactTypes, contractDoc("SocketFact")),
			"total": dschema.Int64Attribute{MarkdownDescription: "Sum of `sockets`; null before the first census.", Computed: true},
			"chain": dschema.SingleNestedAttribute{
				MarkdownDescription: "The census hash chain: `rows`, `intact`, and `first_broken_row` when not intact.",
				Computed:            true,
				Attributes: map[string]dschema.Attribute{
					"rows":             dschema.Int64Attribute{Computed: true, MarkdownDescription: "Rows in the chain."},
					"intact":           dschema.BoolAttribute{Computed: true, MarkdownDescription: "Every row's hash matches."},
					"first_broken_row": dschema.StringAttribute{Computed: true, MarkdownDescription: "The first row that does not match."},
				},
			},
		},
	}
}

func (d *socketFactsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *socketFactsDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.data == nil {
		resp.Diagnostics.AddError("Provider not configured", "The ataila provider was not configured.")
		return
	}
	f, err := d.data.API.GetSocketFacts(ctx)
	if err != nil {
		summary, detail := apiErrorText("the socket census (GET /licence/socket-facts)", err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	elems := make([]attr.Value, 0, len(f.Facts))
	for _, x := range f.Facts {
		elems = append(elems, types.ObjectValueMust(socketFactTypes, map[string]attr.Value{
			"node_name": types.StringValue(x.NodeName), "cluster": stringOrNull(x.Cluster),
			"sockets": types.Int64Value(int64(x.Sockets)), "cores_per_socket": int64OrNull(x.CoresPerSocket),
			"source": types.StringValue(x.Source), "measured_at": timestampString(x.MeasuredAt),
		}))
	}
	m := socketFactsModel{
		Facts: types.ListValueMust(types.ObjectType{AttrTypes: socketFactTypes}, elems),
		Total: int64OrNull(f.Total),
		Chain: types.ObjectValueMust(socketChainTypes, map[string]attr.Value{
			"rows": types.Int64Value(int64(f.Chain.Rows)), "intact": types.BoolValue(f.Chain.Intact),
			"first_broken_row": stringOrNull(f.Chain.FirstBrokenRow)}),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
