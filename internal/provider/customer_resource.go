// Copyright (c) 2026 Macskásy Attila
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
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/ataila/terraform-provider-ataila/internal/client"
)

var (
	_ resource.ResourceWithConfigure   = (*customerResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*customerResource)(nil)
	_ resource.ResourceWithImportState = (*customerResource)(nil)
)

// ShortNameImportPrefix imports a customer by its short name.
const ShortNameImportPrefix = "short_name:"

// NewCustomerResource is the factory for ataila_customer.
func NewCustomerResource() resource.Resource { return &customerResource{} }

type customerResource struct {
	data *ProviderData
}

// customerModel is the state of ataila_customer and of the ataila_customer
// data source.
type customerModel struct {
	ID                  types.String   `tfsdk:"id"`
	CustomerIndex       types.Int64    `tfsdk:"customer_index"`
	ShortName           types.String   `tfsdk:"short_name"`
	LongName            types.String   `tfsdk:"long_name"`
	GitlabGroup         types.String   `tfsdk:"gitlab_group"`
	Edition             types.String   `tfsdk:"edition"`
	PrimaryContactEmail types.String   `tfsdk:"primary_contact_email"`
	PrimaryContactName  types.String   `tfsdk:"primary_contact_name"`
	DefaultEmailTier    types.Int64    `tfsdk:"default_email_tier"`
	BillingTier         types.String   `tfsdk:"billing_tier"`
	Status              types.String   `tfsdk:"status"`
	Notes               types.String   `tfsdk:"notes"`
	PrimaryTenantID     types.String   `tfsdk:"primary_tenant_id"`
	CreatedAt           TimestampValue `tfsdk:"created_at"`
}

// fromAPI fills the model from the API's answer. prior is the configured or
// stored e-mail address, kept when the platform stored the same address
// normalised.
func (m *customerModel) fromAPI(c *client.Customer, priorEmail types.String) {
	m.ID = types.StringValue(c.Id)
	m.CustomerIndex = types.Int64Value(int64(c.CustomerIndex))
	m.ShortName = types.StringValue(c.ShortName)
	m.LongName = types.StringValue(c.LongName)
	m.GitlabGroup = types.StringValue(c.GitlabGroup)
	m.Edition = types.StringValue(string(c.Edition))
	m.PrimaryContactEmail = keepEmail(priorEmail, c.PrimaryContactEmail)
	m.PrimaryContactName = types.StringValue(c.PrimaryContactName)
	m.DefaultEmailTier = types.Int64Value(int64(c.DefaultEmailTier))
	m.BillingTier = types.StringValue(string(c.BillingTier))
	m.Status = types.StringValue(string(c.Status))
	m.Notes = stringOrNull(c.Notes)
	m.PrimaryTenantID = types.StringValue(c.PrimaryTenantId)
	m.CreatedAt = NewTimestamp(c.CreatedAt)
}

func (m *customerModel) ident() string {
	if m.ShortName.ValueString() != "" {
		return fmt.Sprintf("%s (id %s)", m.ShortName.ValueString(), m.ID.ValueString())
	}
	return "with id " + m.ID.ValueString()
}

// Attribute documentation shared by the resource and the data source.
var customerDocs = map[string]string{
	"id": "Customer id, assigned by the platform.",
	"customer_index": "The customer's index, 1-999, used in derived names and address plans. " +
		"Omit it and the platform allocates the next free one (one above the highest ever used, never " +
		"below 2). **Frozen.**",
	"short_name": "Short name, `^[A-Z][A-Z0-9]{1,15}$`, for example `EXAMPLE`. **Frozen.**",
	"long_name": "Display name, 3-80 characters. Quotes, backslashes and control characters are " +
		"refused because the name is copied into generated project files.",
	"gitlab_group": "GitLab group of the customer, `^[a-z][a-z0-9-]{1,29}$` (2-30 characters); also the slug " +
		"of its primary tenant, so the tenant slug rule applies. No customer's group may be a hyphen-prefix of another's (`example` and `example-labs`). " +
		"**Frozen.**",
	"edition": "`sp` (service provider, the default) or `enterprise`. **Frozen.**",
	"primary_contact_email": "E-mail address of the primary contact. A platform user with this address " +
		"becomes a `member` of the primary tenant when the customer is created. The platform stores the " +
		"domain in lower case; the provider keeps the spelling of the configuration.",
	"primary_contact_name": "Name of the primary contact, 2-80 characters.",
	"default_email_tier":   "Default mailbox tier for the customer: 1, 2 or 3 (the default).",
	"billing_tier":         "`INTERNAL` (the default) or `PAYING`.",
	"status": "`active` (the default) or `suspended`. `archived` is what destroying a customer sets; it " +
		"cannot be configured.",
	"notes":             "Free-text notes, up to 2000 characters.",
	"primary_tenant_id": "Id of the tenant created with the customer. It can never be deleted.",
	"created_at": "When the customer was created: RFC 3339 in UTC, compared as an instant (another " +
		"representation of the same time is not a change).",
}

var (
	rxShortName   = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,15}$`)
	rxGitlabGroup = regexp.MustCompile(`^[a-z][a-z0-9-]{1,29}$`)
	rxLongName    = regexp.MustCompile(`^[^"'\\\x00-\x1f\x7f]*$`)
	// An address alone: no surrounding spaces and no "Name <address>" form,
	// which the platform would reduce to the address (a perpetual difference).
	rxEmail = regexp.MustCompile(`^[^@\s<>",;]+@[^@\s<>",;]+\.[^@\s<>",;]+$`)
)

func (r *customerResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_customer"
}

func (r *customerResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	d := customerDocs
	resp.Schema = schema.Schema{
		MarkdownDescription: "A customer: a company on the platform. Creating one also creates its primary " +
			"tenant and its GitLab group.\n\n" +
			"**Frozen keys** (`customer_index`, `short_name`, `gitlab_group`, `edition`) are set at create " +
			"and never change. Changing one in the configuration **fails the plan**; the provider never " +
			"replaces a customer, because destroying one only archives it and an archived customer keeps " +
			"its keys.\n\n" +
			"**Destroy archives** the customer and needs `allow_destroy = true` on the provider **and** a " +
			"token minted with destroy allowed. The platform refuses while the customer has projects.\n\n" +
			"Warnings from the platform (for example a GitLab group that could not be created yet) are " +
			"reported as warnings; the customer exists.\n\n" +
			"The GitLab group's live status (`gitlab_status`, which the API returns only on request) is not " +
			"an attribute: reading it asks GitLab on every refresh.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: d["id"],
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"customer_index": schema.Int64Attribute{
				MarkdownDescription: d["customer_index"],
				Optional:            true,
				Computed:            true,
				Validators:          []validator.Int64{int64validator.Between(1, 999)},
				PlanModifiers: []planmodifier.Int64{
					int64planmodifier.UseStateForUnknown(),
					customerFrozen.forInt64(),
				},
			},
			"short_name": schema.StringAttribute{
				MarkdownDescription: d["short_name"],
				Required:            true,
				Validators: []validator.String{stringvalidator.RegexMatches(rxShortName,
					"must be an upper-case letter followed by 1-15 upper-case letters or digits")},
				PlanModifiers: []planmodifier.String{customerFrozen.forString()},
			},
			"long_name": schema.StringAttribute{
				MarkdownDescription: d["long_name"],
				Required:            true,
				Validators: []validator.String{
					stringvalidator.LengthBetween(3, 80),
					stringvalidator.RegexMatches(rxLongName, "must not contain quotes, backslashes or control characters"),
				},
			},
			"gitlab_group": schema.StringAttribute{
				MarkdownDescription: d["gitlab_group"],
				Required:            true,
				Validators: []validator.String{stringvalidator.RegexMatches(rxGitlabGroup,
					"must be a lower-case letter followed by 1-29 lower-case letters, digits or hyphens (2-30 characters)")},
				PlanModifiers: []planmodifier.String{customerFrozen.forString()},
			},
			"edition": schema.StringAttribute{
				MarkdownDescription: d["edition"],
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("sp"),
				Validators:          []validator.String{stringvalidator.OneOf("sp", "enterprise")},
				PlanModifiers:       []planmodifier.String{customerFrozen.forString()},
			},
			"primary_contact_email": schema.StringAttribute{
				MarkdownDescription: d["primary_contact_email"],
				Required:            true,
				Validators:          []validator.String{stringvalidator.RegexMatches(rxEmail, "must be an e-mail address")},
			},
			"primary_contact_name": schema.StringAttribute{
				MarkdownDescription: d["primary_contact_name"],
				Required:            true,
				Validators:          []validator.String{stringvalidator.LengthBetween(2, 80)},
			},
			"default_email_tier": schema.Int64Attribute{
				MarkdownDescription: d["default_email_tier"],
				Optional:            true,
				Computed:            true,
				Default:             int64default.StaticInt64(3),
				Validators:          []validator.Int64{int64validator.OneOf(1, 2, 3)},
			},
			"billing_tier": schema.StringAttribute{
				MarkdownDescription: d["billing_tier"],
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("INTERNAL"),
				Validators:          []validator.String{stringvalidator.OneOf("INTERNAL", "PAYING")},
			},
			"status": schema.StringAttribute{
				MarkdownDescription: d["status"],
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("active"),
				Validators:          []validator.String{stringvalidator.OneOf("active", "suspended")},
			},
			"notes": schema.StringAttribute{
				MarkdownDescription: d["notes"],
				Optional:            true,
				Validators:          []validator.String{stringvalidator.LengthAtMost(2000)},
			},
			"primary_tenant_id": schema.StringAttribute{
				MarkdownDescription: d["primary_tenant_id"],
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: d["created_at"],
				CustomType:          TimestampType{},
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *customerResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *customerResource) configured(diags interface{ AddError(string, string) }) bool {
	if r.data == nil {
		diags.AddError("Provider not configured", "The ataila provider was not configured.")
		return false
	}
	return true
}

func (r *customerResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() {
		return // create
	}
	var state customerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if req.Plan.Raw.IsNull() {
		// Destroy: refused at plan time unless the provider allows it.
		if r.data != nil && !r.data.AllowDestroy {
			resp.Diagnostics.Append(destroyRefused("ataila_customer", "customer", state.ident()))
		}
		return
	}
	if state.Status.ValueString() == string(client.CustomerStatusArchived) {
		resp.Diagnostics.AddError("The customer is archived",
			fmt.Sprintf("The customer %s was archived outside this configuration. An archived customer can "+
				"never be changed, re-activated or re-created: its keys stay taken.\n\n"+
				"Remove it from the configuration and from the state:\n"+
				"  tofu state rm ataila_customer.<name>       (OpenTofu)\n"+
				"  terraform state rm ataila_customer.<name>  (Terraform)", state.ident()))
	}
}

func (r *customerResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan customerModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := client.CustomerCreate{
		ShortName:           plan.ShortName.ValueString(),
		LongName:            plan.LongName.ValueString(),
		GitlabGroup:         plan.GitlabGroup.ValueString(),
		PrimaryContactEmail: openapi_types.Email(plan.PrimaryContactEmail.ValueString()),
		PrimaryContactName:  plan.PrimaryContactName.ValueString(),
		Notes:               optString(plan.Notes),
	}
	if !plan.CustomerIndex.IsNull() && !plan.CustomerIndex.IsUnknown() {
		n := int(plan.CustomerIndex.ValueInt64())
		body.CustomerIndex = &n
	}
	if v := optString(plan.Edition); v != nil {
		e := client.CustomerCreateEdition(*v)
		body.Edition = &e
	}
	if v := optString(plan.BillingTier); v != nil {
		b := client.CustomerCreateBillingTier(*v)
		body.BillingTier = &b
	}
	if !plan.DefaultEmailTier.IsNull() && !plan.DefaultEmailTier.IsUnknown() {
		t := client.CustomerCreateDefaultEmailTier(plan.DefaultEmailTier.ValueInt64())
		body.DefaultEmailTier = &t
	}
	if v := optString(plan.Status); v != nil {
		s := client.CustomerCreateStatus(*v)
		body.Status = &s
	}

	c, err := r.data.API.CreateCustomer(ctx, body)
	if err != nil {
		resp.Diagnostics.Append(apiError("creating the customer "+body.ShortName, err))
		return
	}
	addWarnings(&resp.Diagnostics, "creating the customer "+c.ShortName, c.Warnings)

	var state customerModel
	state.fromAPI(c, plan.PrimaryContactEmail)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *customerResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var state customerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	c, err := r.data.API.GetCustomer(ctx, state.ID.ValueString())
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.Append(apiError("reading the customer "+state.ident(), err))
		return
	}
	addWarnings(&resp.Diagnostics, "reading the customer "+c.ShortName, c.Warnings)
	state.fromAPI(c, state.PrimaryContactEmail)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *customerResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan, state customerModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Frozen keys again: a value unknown at plan time is known now. Nothing
	// has been sent yet.
	customerFrozen.check(path.Root("customer_index"), state.CustomerIndex, plan.CustomerIndex, true, &resp.Diagnostics)
	customerFrozen.check(path.Root("short_name"), state.ShortName, plan.ShortName, true, &resp.Diagnostics)
	customerFrozen.check(path.Root("gitlab_group"), state.GitlabGroup, plan.GitlabGroup, true, &resp.Diagnostics)
	customerFrozen.check(path.Root("edition"), state.Edition, plan.Edition, true, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	patch := client.Patch{}
	if !plan.LongName.Equal(state.LongName) {
		patch["long_name"] = plan.LongName.ValueString()
	}
	if !sameEmail(plan.PrimaryContactEmail.ValueString(), state.PrimaryContactEmail.ValueString()) {
		patch["primary_contact_email"] = plan.PrimaryContactEmail.ValueString()
	}
	if !plan.PrimaryContactName.Equal(state.PrimaryContactName) {
		patch["primary_contact_name"] = plan.PrimaryContactName.ValueString()
	}
	if !plan.DefaultEmailTier.Equal(state.DefaultEmailTier) {
		patch["default_email_tier"] = plan.DefaultEmailTier.ValueInt64()
	}
	if !plan.BillingTier.Equal(state.BillingTier) {
		patch["billing_tier"] = plan.BillingTier.ValueString()
	}
	if !plan.Status.Equal(state.Status) {
		patch["status"] = plan.Status.ValueString()
	}
	if !plan.Notes.Equal(state.Notes) {
		patch["notes"] = nullable(plan.Notes)
	}

	var (
		c   *client.Customer
		err error
	)
	if len(patch) == 0 {
		c, err = r.data.API.GetCustomer(ctx, state.ID.ValueString())
	} else {
		c, err = r.data.API.UpdateCustomer(ctx, state.ID.ValueString(), patch)
	}
	if err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && apiErr.Code() == client.CodeCustomerArchived {
			resp.Diagnostics.AddError("The customer is archived",
				fmt.Sprintf("The customer %s is archived and can never be changed again. Remove it from the "+
					"configuration and the state (tofu state rm / terraform state rm).\n\n%s",
					state.ident(), apiErr.Detail()))
			return
		}
		resp.Diagnostics.Append(apiError("changing the customer "+state.ident(), err))
		return
	}
	addWarnings(&resp.Diagnostics, "changing the customer "+c.ShortName, c.Warnings)
	plan.fromAPI(c, plan.PrimaryContactEmail)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *customerResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var state customerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !r.data.AllowDestroy {
		resp.Diagnostics.Append(destroyRefused("ataila_customer", "customer", state.ident()))
		return
	}
	err := r.data.API.ArchiveCustomer(ctx, state.ID.ValueString())
	if err != nil && !isNotFound(err) {
		resp.Diagnostics.Append(destroyError("ataila_customer", "customer", state.ident(), err))
	}
}

func (r *customerResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	id := strings.TrimSpace(req.ID)
	if short, ok := strings.CutPrefix(id, ShortNameImportPrefix); ok {
		found, err := r.data.API.ListCustomers(ctx, client.CustomerFilter{ShortName: short})
		if err != nil {
			resp.Diagnostics.Append(apiError("looking up the customer with short_name "+short, err))
			return
		}
		if len(found) != 1 {
			resp.Diagnostics.AddError("Cannot import the customer",
				fmt.Sprintf("%d customers have short_name %q; the import needs exactly one.", len(found), short))
			return
		}
		id = found[0].Id
	}
	if !rxIntID.MatchString(id) {
		resp.Diagnostics.AddError("Cannot import the customer",
			fmt.Sprintf("%q is not a customer id (1 to 999999999, no leading zero). Give the id, or "+
				"short_name:<SHORT_NAME>.", id))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}
