// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ataila/terraform-provider-ataila/internal/client"
)

// Service entitlements and grants (platform 1.0.392 on). The customer HOLDS a
// service (ataila_service_entitlement), a person is GRANTED it under that
// customer (ataila_service_grant); the grant is in force only while both are.
// Approving an entitlement that needs approval stays in the portal: the API
// never approves, so neither does the provider.

var (
	_ resource.ResourceWithConfigure     = (*serviceEntitlementResource)(nil)
	_ resource.ResourceWithImportState   = (*serviceEntitlementResource)(nil)
	_ resource.ResourceWithModifyPlan    = (*serviceEntitlementResource)(nil)
	_ resource.ResourceWithConfigure     = (*serviceGrantResource)(nil)
	_ resource.ResourceWithImportState   = (*serviceGrantResource)(nil)
	_ resource.ResourceWithModifyPlan    = (*serviceGrantResource)(nil)
	_ datasource.DataSourceWithConfigure = (*servicesDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*customerEntitlementsDataSource)(nil)
)

// NewServiceEntitlementResource is the factory for ataila_service_entitlement.
func NewServiceEntitlementResource() resource.Resource { return &serviceEntitlementResource{} }

// NewServiceGrantResource is the factory for ataila_service_grant.
func NewServiceGrantResource() resource.Resource { return &serviceGrantResource{} }

// NewServicesDataSource is the factory for ataila_services.
func NewServicesDataSource() datasource.DataSource { return &servicesDataSource{} }

// NewCustomerEntitlementsDataSource is the factory for ataila_customer_entitlements.
func NewCustomerEntitlementsDataSource() datasource.DataSource {
	return &customerEntitlementsDataSource{}
}

// ── ataila_service_entitlement ───────────────────────────────────────────────

type serviceEntitlementResource struct {
	data *ProviderData
}

type serviceEntitlementModel struct {
	ID             types.String   `tfsdk:"id"`
	CustomerID     types.String   `tfsdk:"customer_id"`
	ServiceKey     types.String   `tfsdk:"service_key"`
	State          types.String   `tfsdk:"state"`
	Seats          types.Int64    `tfsdk:"seats"`
	DedicatedURL   types.String   `tfsdk:"dedicated_url"`
	Notes          types.String   `tfsdk:"notes"`
	EffectiveState types.String   `tfsdk:"effective_state"`
	Approved       types.Bool     `tfsdk:"approved"`
	ApprovedAt     TimestampValue `tfsdk:"approved_at"`
	ServiceName    types.String   `tfsdk:"service_name"`
	SeatsUsed      types.Int64    `tfsdk:"seats_used"`
	URL            types.String   `tfsdk:"url"`
	CreatedAt      TimestampValue `tfsdk:"created_at"`
	UpdatedAt      TimestampValue `tfsdk:"updated_at"`
}

// desiredState maps the stored state onto what a configuration asks for: an
// entitlement waiting for approval was asked for as enabled.
func desiredState(stored string) string {
	if stored == "disabled" {
		return "disabled"
	}
	return "enabled"
}

func (m *serviceEntitlementModel) fromAPI(e client.CustomerService) {
	m.ID = types.StringValue(e.CustomerId + "/" + e.ServiceKey)
	m.CustomerID = types.StringValue(e.CustomerId)
	m.ServiceKey = types.StringValue(e.ServiceKey)
	m.State = types.StringValue(desiredState(string(e.State)))
	m.Seats = int64OrNull(e.Seats)
	m.DedicatedURL = stringOrNull(e.DedicatedUrl)
	m.Notes = stringOrNull(e.Notes)
	m.EffectiveState = types.StringValue(string(e.State))
	m.Approved = types.BoolValue(e.Approved)
	m.ApprovedAt = NewTimestampPointer(e.ApprovedAt)
	m.ServiceName = types.StringValue(e.ServiceName)
	m.SeatsUsed = types.Int64Value(int64(e.SeatsUsed))
	m.URL = stringOrNull(e.Url)
	m.CreatedAt = NewTimestamp(e.CreatedAt)
	m.UpdatedAt = NewTimestamp(e.UpdatedAt)
}

func (m *serviceEntitlementModel) ident() string {
	return fmt.Sprintf("%s of customer %s", m.ServiceKey.ValueString(), m.CustomerID.ValueString())
}

func (r *serviceEntitlementResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service_entitlement"
}

func (r *serviceEntitlementResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A service a customer holds (an entitlement): the commercial gate a person's " +
			"`ataila_service_grant` needs. Needs a token holding `entitlements-admin-global` and platform " +
			"release " + client.ReleaseEntitlements + " or later.\n\n" +
			"**The provider never approves.** For a service that needs approval (`requires_approval` on " +
			"`ataila_services`), `state = \"enabled\"` is stored as `pending_approval` (`effective_state`) with " +
			"a warning, and no grant under it is in force until an operator approves it in the portal. The " +
			"configuration still shows no difference; an approval given in the portal is kept on every apply.\n\n" +
			"Creating an entitlement the customer already holds adopts it (a warning says so). Changing " +
			"`customer_id` or `service_key` replaces it. **Destroy withdraws the service**: the entitlement is " +
			"disabled and every grant under it is revoked, so re-creating it gives nobody access back. It is " +
			"destroy-class: it needs `allow_destroy = true` in the provider and a token created with destroy " +
			"allowed.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "`<customer_id>/<service_key>`.",
				Computed:            true,
				PlanModifiers:       keep,
			},
			"customer_id": schema.StringAttribute{
				MarkdownDescription: "Id of the customer. Changing it replaces the entitlement.",
				Required:            true,
				PlanModifiers:       replace,
			},
			"service_key": schema.StringAttribute{
				MarkdownDescription: "The service's key (`key` on `ataila_services`). Changing it replaces the " +
					"entitlement.",
				Required:      true,
				PlanModifiers: replace,
			},
			"state": schema.StringAttribute{
				MarkdownDescription: "`enabled` (the default) or `disabled`. Disabling keeps the grants under it " +
					"(none is in force while it is disabled; enabling it again brings them back); destroying " +
					"revokes them.",
				Optional:   true,
				Computed:   true,
				Default:    stringdefault.StaticString("enabled"),
				Validators: []validator.String{stringvalidator.OneOf("enabled", "disabled")},
			},
			"seats": schema.Int64Attribute{
				MarkdownDescription: "How many people may hold a grant under it at once; leave it out for no limit.",
				Optional:            true,
				Validators:          []validator.Int64{int64validator.Between(1, 100000)},
			},
			"dedicated_url": schema.StringAttribute{
				MarkdownDescription: "The customer's own instance of a `dedicated` service.",
				Optional:            true,
			},
			"notes": schema.StringAttribute{
				MarkdownDescription: "Free text.",
				Optional:            true,
			},
			"effective_state": schema.StringAttribute{
				MarkdownDescription: "What the platform stored: `enabled`, `pending_approval` (waiting for an " +
					"operator's approval in the portal) or `disabled`.",
				Computed: true,
			},
			"approved": schema.BoolAttribute{
				MarkdownDescription: "An operator approved the entitlement in the portal.",
				Computed:            true,
			},
			"approved_at": schema.StringAttribute{
				MarkdownDescription: "When it was approved: RFC 3339 in UTC; null when not.",
				CustomType:          TimestampType{},
				Computed:            true,
			},
			"service_name": schema.StringAttribute{
				MarkdownDescription: "The service's display name.",
				Computed:            true,
				PlanModifiers:       keep,
			},
			"seats_used": schema.Int64Attribute{
				MarkdownDescription: "How many people hold an unrevoked grant under it.",
				Computed:            true,
			},
			"url": schema.StringAttribute{
				MarkdownDescription: "Where the customer uses the service: `dedicated_url` when set, else the " +
					"service's own address.",
				Computed: true,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "When the customer first held the service: RFC 3339 in UTC.",
				CustomType:          TimestampType{},
				Computed:            true,
				PlanModifiers:       keep,
			},
			"updated_at": schema.StringAttribute{
				MarkdownDescription: "When the entitlement last changed: RFC 3339 in UTC.",
				CustomType:          TimestampType{},
				Computed:            true,
			},
		},
	}
}

func (r *serviceEntitlementResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *serviceEntitlementResource) configured(diags interface{ AddError(string, string) }) bool {
	if r.data == nil {
		diags.AddError("Provider not configured", "The ataila provider was not configured.")
		return false
	}
	return true
}

// ModifyPlan refuses the resource on a platform without entitlements, and a
// destroy while the provider's allow_destroy is off, both before any request.
func (r *serviceEntitlementResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		var state serviceEntitlementModel
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if !resp.Diagnostics.HasError() && r.data != nil && !r.data.AllowDestroy {
			resp.Diagnostics.Append(destroyRefused("ataila_service_entitlement", "service entitlement", state.ident()))
		}
		return
	}
	if d := r.data.featureRefused(client.FeatureEntitlements, path.Root("service_key"),
		"ataila_service_entitlement"); d != nil {
		resp.Diagnostics.Append(d)
	}
}

func (r *serviceEntitlementResource) put(ctx context.Context, plan *serviceEntitlementModel, adopt bool,
	diags interface {
		AddWarning(string, string)
	}) (*client.CustomerServiceWritten, string, error) {
	state := client.CustomerServicePutState(plan.State.ValueString())
	body := client.CustomerServicePut{State: &state, DedicatedUrl: optString(plan.DedicatedURL),
		Notes: optString(plan.Notes)}
	if !plan.Seats.IsNull() && !plan.Seats.IsUnknown() {
		n := int(plan.Seats.ValueInt64())
		body.Seats = &n
	}
	doing := "setting the service entitlement " + plan.ident()
	e, created, err := r.data.API.PutCustomerService(ctx, plan.CustomerID.ValueString(),
		plan.ServiceKey.ValueString(), body)
	if err == nil && adopt && !created {
		diags.AddWarning("Existing entitlement adopted",
			fmt.Sprintf("Customer %s already held %s. The provider now manages that entitlement and set it "+
				"as configured. Destroying the resource withdraws the service.",
				plan.CustomerID.ValueString(), plan.ServiceKey.ValueString()))
	}
	return e, doing, err
}

func entitlementFromWritten(e *client.CustomerServiceWritten) client.CustomerService {
	return client.CustomerService{
		Approved: e.Approved, ApprovedAt: e.ApprovedAt, CreatedAt: e.CreatedAt, CustomerId: e.CustomerId,
		DedicatedUrl: e.DedicatedUrl, Notes: e.Notes, Seats: e.Seats, SeatsUsed: e.SeatsUsed,
		ServiceKey: e.ServiceKey, ServiceName: e.ServiceName, State: client.CustomerServiceState(e.State),
		UpdatedAt: e.UpdatedAt, Url: e.Url,
	}
}

func (r *serviceEntitlementResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan serviceEntitlementModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	e, doing, err := r.put(ctx, &plan, true, &resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.Append(apiError(doing, err))
		return
	}
	addWarnings(&resp.Diagnostics, doing, e.Warnings)
	plan.fromAPI(entitlementFromWritten(e))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *serviceEntitlementResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var state serviceEntitlementModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	e, err := r.data.API.GetCustomerService(ctx, state.CustomerID.ValueString(), state.ServiceKey.ValueString())
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.Append(apiError("reading the service entitlement "+state.ident(), err))
		return
	}
	state.fromAPI(*e)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *serviceEntitlementResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan serviceEntitlementModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	e, doing, err := r.put(ctx, &plan, false, &resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.Append(apiError(doing, err))
		return
	}
	addWarnings(&resp.Diagnostics, doing, e.Warnings)
	plan.fromAPI(entitlementFromWritten(e))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *serviceEntitlementResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var state serviceEntitlementModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !r.data.AllowDestroy {
		resp.Diagnostics.Append(destroyRefused("ataila_service_entitlement", "service entitlement", state.ident()))
		return
	}
	err := r.data.API.DeleteCustomerService(ctx, state.CustomerID.ValueString(), state.ServiceKey.ValueString())
	if err != nil && !isNotFound(err) {
		resp.Diagnostics.Append(destroyError("ataila_service_entitlement", "service entitlement", state.ident(), err))
	}
}

func (r *serviceEntitlementResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	customerID, serviceKey, ok := strings.Cut(strings.TrimSpace(req.ID), "/")
	if !ok || !client.CustomerIDValid(customerID) || serviceKey == "" || strings.Contains(serviceKey, "/") {
		resp.Diagnostics.AddError("Cannot import the service entitlement",
			fmt.Sprintf("The import id must be <customer_id>/<service_key>, got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), customerID+"/"+serviceKey)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("customer_id"), customerID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("service_key"), serviceKey)...)
}

// ── ataila_service_grant ─────────────────────────────────────────────────────

type serviceGrantResource struct {
	data *ProviderData
}

type serviceGrantModel struct {
	ID         types.String   `tfsdk:"id"`
	UserID     types.String   `tfsdk:"user_id"`
	CustomerID types.String   `tfsdk:"customer_id"`
	ServiceKey types.String   `tfsdk:"service_key"`
	GrantLevel types.String   `tfsdk:"grant_level"`
	Notes      types.String   `tfsdk:"notes"`
	Effective  types.Bool     `tfsdk:"effective"`
	BlockedBy  types.String   `tfsdk:"blocked_by"`
	UserEmail  types.String   `tfsdk:"user_email"`
	GrantedBy  types.String   `tfsdk:"granted_by"`
	GrantedAt  TimestampValue `tfsdk:"granted_at"`
}

func (m *serviceGrantModel) fromAPI(g client.ServiceGrant) {
	m.ID = types.StringValue(g.UserId + "/" + g.CustomerId + "/" + g.ServiceKey)
	m.UserID = types.StringValue(g.UserId)
	m.CustomerID = types.StringValue(g.CustomerId)
	m.ServiceKey = types.StringValue(g.ServiceKey)
	m.GrantLevel = types.StringValue(string(g.GrantLevel))
	m.Notes = stringOrNull(g.Notes)
	m.Effective = types.BoolValue(g.Effective)
	m.BlockedBy = stringOrNull(g.BlockedBy)
	m.UserEmail = stringOrNull(g.UserEmail)
	m.GrantedBy = stringOrNull(g.GrantedBy)
	m.GrantedAt = NewTimestamp(g.GrantedAt)
}

func (m *serviceGrantModel) ident() string {
	return fmt.Sprintf("of %s to user %s under customer %s", m.ServiceKey.ValueString(),
		m.UserID.ValueString(), m.CustomerID.ValueString())
}

func (r *serviceGrantResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service_grant"
}

func (r *serviceGrantResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A person's grant of a service under one customer. It is in force (`effective`) " +
			"only while the customer's `ataila_service_entitlement` is enabled and approved, the service is " +
			"offered and the grant is not revoked; `blocked_by` says why not. The person's access in the " +
			"service follows within the platform's reconcile interval. Needs a token holding " +
			"`entitlements-admin-global` and platform release " + client.ReleaseEntitlements + " or later.\n\n" +
			"The customer must hold the service and have a free seat; the person must belong to the " +
			"customer (a tenant membership or a project role). Creating a grant the person already holds " +
			"adopts it (a warning says so); a revoked grant is revived. A grant revoked outside the " +
			"configuration is gone, and the next plan creates it again. **Destroy revokes the grant**: the " +
			"row is kept, with its revocation. It is destroy-class: it needs `allow_destroy = true` in the " +
			"provider and a token created with destroy allowed.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "`<user_id>/<customer_id>/<service_key>`.",
				Computed:            true,
				PlanModifiers:       keep,
			},
			"user_id": schema.StringAttribute{
				MarkdownDescription: "Id of the person. Changing it replaces the grant.",
				Required:            true,
				PlanModifiers:       replace,
			},
			"customer_id": schema.StringAttribute{
				MarkdownDescription: "Id of the customer the grant is held under. Changing it replaces the grant.",
				Required:            true,
				PlanModifiers:       replace,
			},
			"service_key": schema.StringAttribute{
				MarkdownDescription: "The service's key. Changing it replaces the grant.",
				Required:            true,
				PlanModifiers:       replace,
			},
			"grant_level": schema.StringAttribute{
				MarkdownDescription: "What the person may do in the service: `login`, `user` (the default), " +
					"`admin` or `audit`.",
				Optional:   true,
				Computed:   true,
				Default:    stringdefault.StaticString("user"),
				Validators: []validator.String{stringvalidator.OneOf("login", "user", "admin", "audit")},
			},
			"notes": schema.StringAttribute{
				MarkdownDescription: "Free text.",
				Optional:            true,
			},
			"effective": schema.BoolAttribute{
				MarkdownDescription: "The grant is in force right now.",
				Computed:            true,
			},
			"blocked_by": schema.StringAttribute{
				MarkdownDescription: "Why the grant is not in force (`service_retired`, " +
					"`customer_pending_approval`, `customer_disabled`); null when it is.",
				Computed: true,
			},
			"user_email": schema.StringAttribute{
				MarkdownDescription: "The person's e-mail address.",
				Computed:            true,
			},
			"granted_by": schema.StringAttribute{
				MarkdownDescription: "Who granted it (a user id), when recorded.",
				Computed:            true,
			},
			"granted_at": schema.StringAttribute{
				MarkdownDescription: "When it was granted: RFC 3339 in UTC.",
				CustomType:          TimestampType{},
				Computed:            true,
			},
		},
	}
}

func (r *serviceGrantResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *serviceGrantResource) configured(diags interface{ AddError(string, string) }) bool {
	if r.data == nil {
		diags.AddError("Provider not configured", "The ataila provider was not configured.")
		return false
	}
	return true
}

func (r *serviceGrantResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		var state serviceGrantModel
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if !resp.Diagnostics.HasError() && r.data != nil && !r.data.AllowDestroy {
			resp.Diagnostics.Append(destroyRefused("ataila_service_grant", "service grant", state.ident()))
		}
		return
	}
	if d := r.data.featureRefused(client.FeatureEntitlements, path.Root("service_key"),
		"ataila_service_grant"); d != nil {
		resp.Diagnostics.Append(d)
	}
}

func grantFromWritten(g *client.ServiceGrantWritten) client.ServiceGrant {
	return client.ServiceGrant{
		BlockedBy: g.BlockedBy, CustomerId: g.CustomerId, Effective: g.Effective,
		GrantLevel: client.ServiceGrantGrantLevel(g.GrantLevel), GrantedAt: g.GrantedAt, GrantedBy: g.GrantedBy,
		Notes: g.Notes, RevokedAt: g.RevokedAt, ServiceKey: g.ServiceKey, UserEmail: g.UserEmail, UserId: g.UserId,
	}
}

func (r *serviceGrantResource) put(ctx context.Context, plan *serviceGrantModel, adopt bool, diags interface {
	AddWarning(string, string)
}) (*client.ServiceGrantWritten, string, error) {
	level := client.ServiceGrantPutGrantLevel(plan.GrantLevel.ValueString())
	doing := "granting the service " + plan.ident()
	g, created, err := r.data.API.PutServiceGrant(ctx, plan.UserID.ValueString(), plan.CustomerID.ValueString(),
		plan.ServiceKey.ValueString(), client.ServiceGrantPut{GrantLevel: &level, Notes: optString(plan.Notes)})
	if err == nil && adopt && !created {
		diags.AddWarning("Existing grant adopted",
			fmt.Sprintf("User %s already held %s under customer %s. The provider now manages that grant and "+
				"set it as configured. Destroying the resource revokes it.", plan.UserID.ValueString(),
				plan.ServiceKey.ValueString(), plan.CustomerID.ValueString()))
	}
	return g, doing, err
}

func (r *serviceGrantResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan serviceGrantModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	g, doing, err := r.put(ctx, &plan, true, &resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.Append(apiError(doing, err))
		return
	}
	addWarnings(&resp.Diagnostics, doing, g.Warnings)
	plan.fromAPI(grantFromWritten(g))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *serviceGrantResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var state serviceGrantModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	g, err := r.data.API.GetServiceGrant(ctx, state.UserID.ValueString(), state.CustomerID.ValueString(),
		state.ServiceKey.ValueString())
	if isNotFound(err) || (err == nil && g.RevokedAt != nil) {
		resp.State.RemoveResource(ctx) // a revoked grant is gone; the next plan grants it again
		return
	}
	if err != nil {
		resp.Diagnostics.Append(apiError("reading the service grant "+state.ident(), err))
		return
	}
	state.fromAPI(*g)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *serviceGrantResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan serviceGrantModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	g, doing, err := r.put(ctx, &plan, false, &resp.Diagnostics)
	if err != nil {
		resp.Diagnostics.Append(apiError(doing, err))
		return
	}
	addWarnings(&resp.Diagnostics, doing, g.Warnings)
	plan.fromAPI(grantFromWritten(g))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *serviceGrantResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var state serviceGrantModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !r.data.AllowDestroy {
		resp.Diagnostics.Append(destroyRefused("ataila_service_grant", "service grant", state.ident()))
		return
	}
	err := r.data.API.DeleteServiceGrant(ctx, state.UserID.ValueString(), state.CustomerID.ValueString(),
		state.ServiceKey.ValueString())
	if err != nil && !isNotFound(err) {
		resp.Diagnostics.Append(destroyError("ataila_service_grant", "service grant", state.ident(), err))
	}
}

func (r *serviceGrantResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.Split(strings.TrimSpace(req.ID), "/")
	if len(parts) != 3 || parts[0] == "" || !client.CustomerIDValid(parts[1]) || parts[2] == "" {
		resp.Diagnostics.AddError("Cannot import the service grant",
			fmt.Sprintf("The import id must be <user_id>/<customer_id>/<service_key>, got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), strings.Join(parts, "/"))...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("user_id"), parts[0])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("customer_id"), parts[1])...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("service_key"), parts[2])...)
}

// ── ataila_services ──────────────────────────────────────────────────────────

var serviceTypes = map[string]attr.Type{
	"key":               types.StringType,
	"name":              types.StringType,
	"description":       types.StringType,
	"category":          types.StringType,
	"enforcement":       types.StringType,
	"url":               types.StringType,
	"requires_approval": types.BoolType,
	"active":            types.BoolType,
	"sort_order":        types.Int64Type,
}

var serviceDocs = map[string]string{
	"key":               client.Describe("Service", "key"),
	"name":              "The service's display name.",
	"description":       "What the service is, for a person.",
	"category":          client.Describe("Service", "category"),
	"enforcement":       client.Describe("Service", "enforcement"),
	"url":               client.Describe("Service", "url"),
	"requires_approval": client.Describe("Service", "requires_approval"),
	"active":            client.Describe("Service", "active"),
	"sort_order":        "Display order, ascending.",
}

type servicesDataSource struct {
	data *ProviderData
}

type servicesModel struct {
	IncludeInactive types.Bool `tfsdk:"include_inactive"`
	Services        types.List `tfsdk:"services"`
}

func (d *servicesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_services"
}

func (d *servicesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dschema.Schema{
		MarkdownDescription: "The service catalogue: every service a customer can be entitled to " +
			"(`ataila_service_entitlement`), in display order. Needs a token holding `entitlements-read-global` " +
			"(or the admin key; a tenant-scoped token may read it too) and platform release " +
			client.ReleaseEntitlements + " or later.",
		Attributes: map[string]dschema.Attribute{
			"include_inactive": dschema.BoolAttribute{
				MarkdownDescription: "Also list retired services (`active` false).",
				Optional:            true,
			},
			"services": dataNestedList("The services, by `sort_order`, then `key`.", serviceTypes,
				func(p []string) string { return serviceDocs[p[len(p)-1]] }),
		},
	}
}

func (d *servicesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *servicesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.data == nil {
		resp.Diagnostics.AddError("Provider not configured", "The ataila provider was not configured.")
		return
	}
	if diagnostic := d.data.featureRefused(client.FeatureEntitlements, path.Root("services"),
		"The ataila_services data source"); diagnostic != nil {
		resp.Diagnostics.Append(diagnostic)
		return
	}
	var cfg servicesModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	services, err := d.data.API.ListServices(ctx, cfg.IncludeInactive.ValueBool())
	if err != nil {
		resp.Diagnostics.Append(apiError("reading the service catalogue (GET /services)", err))
		return
	}
	elems := make([]attr.Value, 0, len(services))
	for _, s := range services {
		elems = append(elems, types.ObjectValueMust(serviceTypes, map[string]attr.Value{
			"key": types.StringValue(s.Key), "name": types.StringValue(s.Name),
			"description": stringOrNull(s.Description), "category": types.StringValue(s.Category),
			"enforcement": types.StringValue(s.Enforcement), "url": stringOrNull(s.Url),
			"requires_approval": types.BoolValue(s.RequiresApproval), "active": types.BoolValue(s.Active),
			"sort_order": types.Int64Value(int64(s.SortOrder)),
		}))
	}
	cfg.Services = types.ListValueMust(types.ObjectType{AttrTypes: serviceTypes}, elems)
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}

// ── ataila_customer_entitlements ─────────────────────────────────────────────

var customerEntitlementTypes = map[string]attr.Type{
	"service_key":   types.StringType,
	"service_name":  types.StringType,
	"state":         types.StringType,
	"seats":         types.Int64Type,
	"seats_used":    types.Int64Type,
	"dedicated_url": types.StringType,
	"url":           types.StringType,
	"approved":      types.BoolType,
	"approved_at":   types.StringType,
	"notes":         types.StringType,
}

var customerEntitlementDocs = map[string]string{
	"service_key":   "The service's key.",
	"service_name":  "The service's display name.",
	"state":         client.Describe("CustomerService", "state"),
	"seats":         client.Describe("CustomerService", "seats"),
	"seats_used":    client.Describe("CustomerService", "seats_used"),
	"dedicated_url": client.Describe("CustomerService", "dedicated_url"),
	"url":           client.Describe("CustomerService", "url"),
	"approved":      client.Describe("CustomerService", "approved"),
	"approved_at":   "When it was approved: RFC 3339 in UTC; null when not.",
	"notes":         "Free text.",
}

type customerEntitlementsDataSource struct {
	data *ProviderData
}

type customerEntitlementsModel struct {
	CustomerID   types.String `tfsdk:"customer_id"`
	Entitlements types.List   `tfsdk:"entitlements"`
}

func (d *customerEntitlementsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_customer_entitlements"
}

func (d *customerEntitlementsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dschema.Schema{
		MarkdownDescription: "Every service a customer holds, in any state, with its seat use. Needs a token " +
			"holding `entitlements-read-global` (or the admin key) and platform release " +
			client.ReleaseEntitlements + " or later. A tenant-scoped token reads its own customer's only.",
		Attributes: map[string]dschema.Attribute{
			"customer_id": dschema.StringAttribute{
				MarkdownDescription: "Id of the customer.",
				Required:            true,
			},
			"entitlements": dataNestedList("The customer's entitlements, in the catalogue's display order.",
				customerEntitlementTypes, func(p []string) string { return customerEntitlementDocs[p[len(p)-1]] }),
		},
	}
}

func (d *customerEntitlementsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *customerEntitlementsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.data == nil {
		resp.Diagnostics.AddError("Provider not configured", "The ataila provider was not configured.")
		return
	}
	if diagnostic := d.data.featureRefused(client.FeatureEntitlements, path.Root("customer_id"),
		"The ataila_customer_entitlements data source"); diagnostic != nil {
		resp.Diagnostics.Append(diagnostic)
		return
	}
	var cfg customerEntitlementsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := cfg.CustomerID.ValueString()
	list, err := d.data.API.ListCustomerServices(ctx, id)
	if err != nil {
		resp.Diagnostics.Append(apiError("reading the services of customer "+id, err))
		return
	}
	elems := make([]attr.Value, 0, len(list))
	for _, e := range list {
		elems = append(elems, types.ObjectValueMust(customerEntitlementTypes, map[string]attr.Value{
			"service_key": types.StringValue(e.ServiceKey), "service_name": types.StringValue(e.ServiceName),
			"state": types.StringValue(string(e.State)), "seats": int64OrNull(e.Seats),
			"seats_used": types.Int64Value(int64(e.SeatsUsed)), "dedicated_url": stringOrNull(e.DedicatedUrl),
			"url": stringOrNull(e.Url), "approved": types.BoolValue(e.Approved),
			"approved_at": timestampString(e.ApprovedAt), "notes": stringOrNull(e.Notes),
		}))
	}
	cfg.Entitlements = types.ListValueMust(types.ObjectType{AttrTypes: customerEntitlementTypes}, elems)
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}
