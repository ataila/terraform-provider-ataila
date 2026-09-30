// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
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
	_ resource.ResourceWithConfigure      = (*promotionResource)(nil)
	_ resource.ResourceWithImportState    = (*promotionResource)(nil)
	_ resource.ResourceWithValidateConfig = (*promotionResource)(nil)
	_ resource.ResourceWithConfigure      = (*prodLockResource)(nil)
	_ resource.ResourceWithImportState    = (*prodLockResource)(nil)
	_ resource.ResourceWithValidateConfig = (*prodLockResource)(nil)
)

// defaultPromotionTimeout is how long create waits by default.
const defaultPromotionTimeout = 60 * time.Minute

var (
	promotionFrozen = frozenKey{object: "release promotion", why: "A promotion is the record of one request; " +
		"destroying it only removes it from the state, and a replacement would book another deployment. " +
		"Declare a new ataila_release_promotion for another promotion."}
	prodLockFrozen = frozenKey{object: "PROD data lock", why: "The lock belongs to one project. Declare a " +
		"separate ataila_project_prod_lock for another project."}
)

var rxReleaseVersion = stringvalidator.RegexMatches(
	regexpMust(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,63}$`), "must be an image tag: letters, digits, '_', '.' and '-', at most 64 characters, not starting with '.' or '-'")

// ── ataila_release_promotion ─────────────────────────────────────────────────

// NewReleasePromotionResource is the factory for ataila_release_promotion.
func NewReleasePromotionResource() resource.Resource { return &promotionResource{} }

type promotionResource struct {
	data *ProviderData
}

type promotionModel struct {
	ID               types.String   `tfsdk:"id"`
	ProjectID        types.String   `tfsdk:"project_id"`
	Component        types.String   `tfsdk:"component"`
	TargetEnv        types.String   `tfsdk:"target_env"`
	Version          types.String   `tfsdk:"version"`
	WaitForApproval  types.Bool     `tfsdk:"wait_for_approval"`
	OperationID      types.String   `tfsdk:"operation_id"`
	Status           types.String   `tfsdk:"status"`
	PortalStatus     types.String   `tfsdk:"portal_status"`
	SourceEnv        types.String   `tfsdk:"source_env"`
	RequestedBy      types.String   `tfsdk:"requested_by"`
	RequestedAt      TimestampValue `tfsdk:"requested_at"`
	RequestedVia     types.String   `tfsdk:"requested_via"`
	RequestedTokenID types.String   `tfsdk:"requested_token_id"`
	DecidedBy        types.String   `tfsdk:"decided_by"`
	DecidedAt        TimestampValue `tfsdk:"decided_at"`
	ApprovalReason   types.String   `tfsdk:"approval_reason"`
	StartedAt        TimestampValue `tfsdk:"started_at"`
	CompletedAt      TimestampValue `tfsdk:"completed_at"`
	ErrorReason      types.String   `tfsdk:"error_reason"`
	PipelineURL      types.String   `tfsdk:"pipeline_url"`
	Timeouts         timeouts.Value `tfsdk:"timeouts"`
}

func (m *promotionModel) fromAPI(o *client.ReleaseOperation) {
	m.ID = types.StringValue(o.Id)
	m.ProjectID = types.StringValue(o.ProjectId)
	if o.Component != nil {
		m.Component = types.StringValue(string(*o.Component))
	}
	m.TargetEnv = types.StringValue(string(o.TargetEnv))
	m.Version = stringOrNull(o.Version)
	if m.WaitForApproval.IsNull() || m.WaitForApproval.IsUnknown() {
		m.WaitForApproval = types.BoolValue(false)
	}
	m.OperationID = types.StringValue(o.OperationId)
	m.Status = types.StringValue(string(o.Status))
	m.PortalStatus = types.StringValue(string(o.PortalStatus))
	m.SourceEnv = types.StringValue(string(o.SourceEnv))
	m.RequestedBy = types.StringValue(o.RequestedBy)
	m.RequestedAt = NewTimestamp(o.RequestedAt)
	if o.RequestedVia != nil {
		m.RequestedVia = types.StringValue(string(*o.RequestedVia))
	} else {
		m.RequestedVia = types.StringNull()
	}
	m.RequestedTokenID = stringOrNull(o.RequestedTokenId)
	m.DecidedBy = stringOrNull(o.DecidedBy)
	m.DecidedAt = NewTimestampPointer(o.DecidedAt)
	m.ApprovalReason = stringOrNull(o.ApprovalReason)
	m.StartedAt = NewTimestampPointer(o.StartedAt)
	m.CompletedAt = NewTimestampPointer(o.CompletedAt)
	m.ErrorReason = stringOrNull(o.ErrorReason)
	m.PipelineURL = stringOrNull(o.PipelineUrl)
}

func (m *promotionModel) ident() string {
	return fmt.Sprintf("of %s into %s on project %s", m.Component.ValueString(), m.TargetEnv.ValueString(),
		m.ProjectID.ValueString())
}

func (r *promotionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_release_promotion"
}

func (r *promotionResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	c := func(doc string) schema.StringAttribute {
		return schema.StringAttribute{MarkdownDescription: doc, Computed: true}
	}
	ts := func(doc string) schema.StringAttribute {
		return schema.StringAttribute{MarkdownDescription: doc + " RFC 3339 in UTC.", CustomType: TimestampType{}, Computed: true}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A request to promote one component of a Kubernetes project into one environment " +
			"(the platform's Release Manager). It records the request; it never decides a go-live.\n\n" +
			"* `dev` deploys a named build: `version` is required, and the platform cannot check that the build " +
			"exists before it dispatches.\n" +
			"* `uat` and `prod` promote what the environment below **last reported** running (`dev` for `uat`, " +
			"`uat` for `prod`): leave `version` out to take it, or give it and it must be that version.\n" +
			"* `prod` is a **request that waits for a person** to approve it in the portal. With " +
			"`wait_for_approval = false` (the default) create ends at once with `status = awaiting_approval` and " +
			"a warning; with `true` it waits for the decision, up to `timeouts.create`.\n\n" +
			"Create waits until the operation succeeds. A failed operation (rejected, failed, or timed out on the " +
			"platform) fails the apply and taints the resource, so the next apply requests again. On a platform " +
			"that fakes dispatch (`dryrun`, or `simulate`) nothing is executed and the operation never " +
			"completes: create then ends with the status it saw and a warning naming the mode. When " +
			"`timeouts.create` passes, create ends the same way, with a warning: the operation goes on, and a " +
			"refresh follows it. Tainting it would book a second deployment.\n\n" +
			"All arguments are **frozen**: a change fails the plan; declare a new resource for a new promotion. " +
			"**Destroy only removes the resource from the state**: the history stays, and a pending PROD request " +
			"stays pending in the portal. Import by the release operation's id (a data copy is refused).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The release operation's id.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"project_id": schema.StringAttribute{
				MarkdownDescription: "Id of the project (Kubernetes projects only). **Frozen.**",
				Required:            true,
				Validators:          []validator.String{stringvalidator.RegexMatches(rxIntID, "must be a project id")},
				PlanModifiers:       []planmodifier.String{promotionFrozen.forString()},
			},
			"component": schema.StringAttribute{
				MarkdownDescription: "`app-api` or `www`. **Frozen.**",
				Required:            true,
				Validators:          []validator.String{stringvalidator.OneOf("app-api", "www")},
				PlanModifiers:       []planmodifier.String{promotionFrozen.forString()},
			},
			"target_env": schema.StringAttribute{
				MarkdownDescription: "`dev`, `uat` or `prod`. **Frozen.**",
				Required:            true,
				Validators:          []validator.String{stringvalidator.OneOf("dev", "uat", "prod")},
				PlanModifiers:       []planmodifier.String{promotionFrozen.forString()},
			},
			"version": schema.StringAttribute{
				MarkdownDescription: "The version (image tag). Required for `dev`. For `uat` and `prod`, left " +
					"out means the version the source environment last reported. **Frozen.**",
				Optional:   true,
				Computed:   true,
				Validators: []validator.String{rxReleaseVersion},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(),
					promotionFrozen.forString()},
			},
			"wait_for_approval": schema.BoolAttribute{
				MarkdownDescription: "For `prod`: wait for a person to approve or reject the request, up to " +
					"`timeouts.create`. Default `false`: create ends once the request is booked.",
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
			},
			"operation_id": schema.StringAttribute{
				MarkdownDescription: "`release:<id>`, the operation to poll.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"status": c("`awaiting_approval` (a PROD request waiting for a person), `pending` (approved, not " +
				"picked up), `running`, `succeeded` or `failed` (also when rejected, or given up on by the platform)."),
			"portal_status":      c("The Release Manager's own status: `pending`, `approved`, `rejected`, `running`, `succeeded` or `failed`."),
			"source_env":         c("The environment promoted from (`sandbox` for `dev`)."),
			"requested_by":       c("Who asked: the token's principal."),
			"requested_at":       ts("When it was requested."),
			"requested_via":      c("`session`, `pat` or `service_account`."),
			"requested_token_id": c("The API token used."),
			"decided_by":         c("Who approved or rejected it."),
			"decided_at":         ts("When it was decided."),
			"approval_reason":    c("The reason and the approver's note, as recorded; addresses and digests masked."),
			"started_at":         ts("When it started running."),
			"completed_at":       ts("When it ended."),
			"error_reason":       c("Why it failed or was rejected; addresses and digests masked."),
			"pipeline_url":       c("The pipeline carrying it out, once known."),
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true}),
		},
	}
}

func (r *promotionResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var target, version types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("target_env"), &target)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("version"), &version)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if target.ValueString() == "dev" && version.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("version"), "A promotion into dev needs a version",
			"A promotion into dev deploys a build, which must be named: set version to the build's image tag. "+
				"The platform cannot check that the build exists before it dispatches.")
	}
}

func (r *promotionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *promotionResource) configured(diags interface{ AddError(string, string) }) bool {
	if r.data == nil {
		diags.AddError("Provider not configured", "The ataila provider was not configured.")
		return false
	}
	return true
}

func (r *promotionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan promotionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	wait, d := plan.Timeouts.Create(ctx, defaultPromotionTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := client.ReleasePromotionCreate{
		Component: client.ReleasePromotionCreateComponent(plan.Component.ValueString()),
		TargetEnv: client.ReleasePromotionCreateTargetEnv(plan.TargetEnv.ValueString()),
		Version:   optString(plan.Version),
	}
	op, err := r.data.API.RequestPromotion(ctx, plan.ProjectID.ValueString(), body)
	if err != nil {
		resp.Diagnostics.Append(promotionError(plan.ident(), err))
		return
	}
	native, _ := client.ReleaseOperationID(op.Id)
	if native == "" && op.ResourceId != nil {
		native = *op.ResourceId
	}

	waitCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	waitApproval := plan.WaitForApproval.ValueBool()
	last, pollErr := pollOperation(waitCtx, r.data.API, op.Id, op, func(o *client.Operation) bool {
		if o.Status == client.OperationStatusAwaitingApproval {
			return !waitApproval
		}
		return fakesDispatch(operationDispatch(o))
	})

	readCtx, readCancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer readCancel()
	ro, err := r.data.API.GetReleaseOperation(readCtx, native)
	if err != nil {
		resp.Diagnostics.Append(apiError("reading release operation "+native, err))
		return
	}
	plan.fromAPI(ro)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)

	switch {
	case errors.Is(pollErr, context.DeadlineExceeded):
		resp.Diagnostics.AddWarning("The promotion did not finish in time",
			fmt.Sprintf("Release operation %s %s was %s after %s. It goes on on the platform; the state holds "+
				"what was seen, and a refresh follows it. The resource is not tainted, because the next apply "+
				"would then request a second deployment.", native, plan.ident(), plan.Status.ValueString(), wait))
	case pollErr != nil:
		resp.Diagnostics.Append(apiError(fmt.Sprintf("waiting for release operation %s %s", native, plan.ident()), pollErr))
	case last.Status == client.OperationStatusFailed:
		resp.Diagnostics.AddError(fmt.Sprintf("The promotion failed (%s)", operationErrorCode(last)),
			fmt.Sprintf("Release operation %s %s ended failed.\n\n%s\n\nThe resource is tainted: the next apply "+
				"requests the promotion again.", native, plan.ident(), operationErrorText(last)))
	case last.Status == client.OperationStatusAwaitingApproval:
		resp.Diagnostics.AddWarning("A person must approve the promotion in the portal",
			fmt.Sprintf("Release operation %s %s is a PROD request: it waits (awaiting_approval) until a person "+
				"approves or rejects it in the portal's Release Manager. The API cannot approve it. Set "+
				"wait_for_approval = true to wait for the decision.", native, plan.ident()))
	case last.Status != client.OperationStatusSucceeded:
		mode := operationDispatch(last)
		resp.Diagnostics.AddWarning(fmt.Sprintf("The platform fakes dispatch (%s): the promotion will not run", mode),
			fmt.Sprintf("Release operation %s %s is %s. This platform's dispatch mode is %s: nothing is executed, "+
				"so the operation will not complete (the platform marks a build promotion failed after a while "+
				"without a report). That is how every non-live platform behaves, so the resource is created.",
				native, plan.ident(), last.Status, mode))
	}
}

func promotionError(ident string, err error) diag.Diagnostic {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == 422 {
		hint := map[string]string{
			client.CodeVMProjectsUnsupported: "Releases of VM projects cannot be requested through the API: " +
				"their deploy ships the head of main and ignores the requested version. Use the portal's Release Manager.",
			client.CodeVersionNotAtSource: "UAT and PROD take only the version the environment below last reported. " +
				"Leave version out to take it, or promote into the environment below first.",
			"component_not_enabled": "The project does not have this component.",
			"version_required":      "A promotion into dev must name its version.",
		}[apiErr.Code()]
		if hint != "" {
			return diag.NewErrorDiagnostic(apiErr.Summary(),
				fmt.Sprintf("While requesting the promotion %s.\n\n%s\n\n%s", ident, hint, apiErr.Detail()))
		}
	}
	return apiError("requesting the promotion "+ident, err)
}

func (r *promotionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var state promotionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	o, err := r.data.API.GetReleaseOperation(ctx, state.ID.ValueString())
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.Append(apiError("reading release operation "+state.ID.ValueString(), err))
		return
	}
	state.fromAPI(o)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update changes only wait_for_approval or the timeouts: nothing is sent.
func (r *promotionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan, state promotionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	for _, f := range []struct {
		name        string
		state, plan types.String
	}{{"project_id", state.ProjectID, plan.ProjectID}, {"component", state.Component, plan.Component},
		{"target_env", state.TargetEnv, plan.TargetEnv}, {"version", state.Version, plan.Version}} {
		promotionFrozen.check(path.Root(f.name), f.state, f.plan, true, &resp.Diagnostics)
	}
	if resp.Diagnostics.HasError() {
		return
	}
	o, err := r.data.API.GetReleaseOperation(ctx, state.ID.ValueString())
	if err != nil {
		resp.Diagnostics.Append(apiError("reading release operation "+state.ID.ValueString(), err))
		return
	}
	plan.fromAPI(o)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete forgets the promotion: its history stays on the platform.
func (r *promotionResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}

func (r *promotionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	id := strings.TrimSpace(req.ID)
	if native, ok := client.ReleaseOperationID(id); ok {
		id = native
	}
	if !rxIntID.MatchString(id) {
		resp.Diagnostics.AddError("Cannot import the promotion",
			fmt.Sprintf("The import id is the release operation's id (a number, or release:<id>), got %q.", req.ID))
		return
	}
	o, err := r.data.API.GetReleaseOperation(ctx, id)
	if err != nil {
		resp.Diagnostics.Append(apiError("reading release operation "+id+" to import it", err))
		return
	}
	if o.Operation != client.PromoteBuild {
		resp.Diagnostics.AddError("Only promotions can be imported",
			fmt.Sprintf("Release operation %s is a %s, booked in the portal. The API neither creates nor manages data "+
				"copies; read it with the ataila_release_operation data source.", id, o.Operation))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

// ── ataila_project_prod_lock ─────────────────────────────────────────────────

// NewProjectProdLockResource is the factory for ataila_project_prod_lock.
func NewProjectProdLockResource() resource.Resource { return &prodLockResource{} }

type prodLockResource struct {
	data *ProviderData
}

type prodLockModel struct {
	ID            types.String   `tfsdk:"id"`
	ProjectID     types.String   `tfsdk:"project_id"`
	Locked        types.Bool     `tfsdk:"locked"`
	ConfirmUnlock types.String   `tfsdk:"confirm_unlock"`
	LockedAt      TimestampValue `tfsdk:"locked_at"`
	LockedBy      types.String   `tfsdk:"locked_by"`
}

func (m *prodLockModel) fromAPI(l *client.ProdLock) {
	m.ID = types.StringValue(l.ProjectId)
	m.ProjectID = types.StringValue(l.ProjectId)
	m.Locked = types.BoolValue(l.Locked)
	m.LockedAt = NewTimestampPointer(l.LockedAt)
	m.LockedBy = stringOrNull(l.LockedBy)
}

func (r *prodLockResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project_prod_lock"
}

func (r *prodLockResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The PROD **data** lock of a project: while it is set, the portal refuses every data " +
			"copy into PROD, so PROD stays the source of truth for its data. It does not block code promotion.\n\n" +
			"Unlocking (`locked = false`) needs `confirm_unlock` set to the project's short name; it is sent only " +
			"to unlock. **Destroy only removes the resource from the state**: the lock stays as it is. Import by " +
			"project id.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The project id.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"project_id": schema.StringAttribute{
				MarkdownDescription: "Id of the project. **Frozen.**",
				Required:            true,
				Validators:          []validator.String{stringvalidator.RegexMatches(rxIntID, "must be a project id")},
				PlanModifiers:       []planmodifier.String{prodLockFrozen.forString()},
			},
			"locked": schema.BoolAttribute{
				MarkdownDescription: "`true` locks PROD data; `false` unlocks it (needs `confirm_unlock`).",
				Required:            true,
			},
			"confirm_unlock": schema.StringAttribute{
				MarkdownDescription: "The project's short name, exactly: required when `locked = false`, sent only then.",
				Optional:            true,
				Validators:          []validator.String{stringvalidator.LengthAtMost(64)},
			},
			"locked_at": schema.StringAttribute{
				MarkdownDescription: "When it was locked: RFC 3339 in UTC.", CustomType: TimestampType{}, Computed: true,
			},
			"locked_by": schema.StringAttribute{MarkdownDescription: "Who locked it.", Computed: true},
		},
	}
}

func (r *prodLockResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var locked types.Bool
	var confirm types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("locked"), &locked)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("confirm_unlock"), &confirm)...)
	if resp.Diagnostics.HasError() || locked.IsUnknown() || locked.IsNull() {
		return
	}
	if !locked.ValueBool() && confirm.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("confirm_unlock"), "Unlocking needs a confirmation",
			"Unlocking PROD data (locked = false) needs confirm_unlock set to the project's short name, exactly.")
	}
}

func (r *prodLockResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *prodLockResource) put(ctx context.Context, plan *prodLockModel, diags *diag.Diagnostics) bool {
	if r.data == nil {
		diags.AddError("Provider not configured", "The ataila provider was not configured.")
		return false
	}
	l, err := r.data.API.PutProdLock(ctx, plan.ProjectID.ValueString(), plan.Locked.ValueBool(), plan.ConfirmUnlock.ValueString())
	if err != nil {
		if client.IsCode(err, 422, client.CodeUnlockNotConfirmed) {
			diags.AddAttributeError(path.Root("confirm_unlock"), "The unlock was not confirmed",
				"confirm_unlock must be the project's short name, exactly. The lock is unchanged.")
			return false
		}
		diags.Append(apiError("setting the PROD data lock of project "+plan.ProjectID.ValueString(), err))
		return false
	}
	plan.fromAPI(l)
	return true
}

func (r *prodLockResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan prodLockModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || !r.put(ctx, &plan, &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *prodLockResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if r.data == nil {
		resp.Diagnostics.AddError("Provider not configured", "The ataila provider was not configured.")
		return
	}
	var state prodLockModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	l, err := r.data.API.GetProdLock(ctx, state.ProjectID.ValueString())
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.Append(apiError("reading the PROD data lock of project "+state.ProjectID.ValueString(), err))
		return
	}
	state.fromAPI(l)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *prodLockResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state prodLockModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	prodLockFrozen.check(path.Root("project_id"), state.ProjectID, plan.ProjectID, true, &resp.Diagnostics)
	if resp.Diagnostics.HasError() || !r.put(ctx, &plan, &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete forgets the resource: the lock stays as it is.
func (r *prodLockResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}

func (r *prodLockResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := strings.TrimSpace(req.ID)
	if !rxIntID.MatchString(id) {
		resp.Diagnostics.AddError("Cannot import the PROD data lock",
			fmt.Sprintf("The import id is the project id, got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), id)...)
}

// ── data sources ─────────────────────────────────────────────────────────────

// NewReleaseStateDataSource is the factory for the ataila_release_state data
// source.
func NewReleaseStateDataSource() datasource.DataSource { return &releaseStateDataSource{} }

type releaseStateDataSource struct {
	data *ProviderData
}

var reportedVersionTypes = map[string]attr.Type{"env": types.StringType, "component": types.StringType,
	"last_reported_version": types.StringType, "last_reported_at": types.StringType,
	"last_reported_by": types.StringType, "source_env": types.StringType}

type releaseStateModel struct {
	ProjectID            types.String   `tfsdk:"project_id"`
	DeploymentBackend    types.String   `tfsdk:"deployment_backend"`
	Versions             types.List     `tfsdk:"versions"`
	ProdDataLocked       types.Bool     `tfsdk:"prod_data_locked"`
	ProdDataLockedAt     TimestampValue `tfsdk:"prod_data_locked_at"`
	ProdDataLockedBy     types.String   `tfsdk:"prod_data_locked_by"`
	PriorProdDataCopies  types.Int64    `tfsdk:"prior_prod_data_copies"`
	PendingOperationIDs  types.List     `tfsdk:"pending_operation_ids"`
	InFlightOperationIDs types.List     `tfsdk:"in_flight_operation_ids"`
}

func (d *releaseStateDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_release_state"
}

func (d *releaseStateDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = dschema.Schema{
		MarkdownDescription: "A project's release state: per environment and component the version a release " +
			"pipeline **last reported** (recorded when a deploy reports success, not probed live), the PROD " +
			"data lock, and the operations still open.",
		Attributes: map[string]dschema.Attribute{
			"project_id": dschema.StringAttribute{
				MarkdownDescription: "Id of the project.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.RegexMatches(rxIntID, "must be a project id")},
			},
			"deployment_backend": dschema.StringAttribute{MarkdownDescription: "`k8s` or `vm`; only `k8s` projects take promotions through the API.", Computed: true},
			"versions": dschema.ListAttribute{
				MarkdownDescription: "`env`, `component`, `last_reported_version`, `last_reported_at` (RFC 3339), " +
					"`last_reported_by` and `source_env`.",
				Computed:    true,
				ElementType: types.ObjectType{AttrTypes: reportedVersionTypes},
			},
			"prod_data_locked":        dschema.BoolAttribute{MarkdownDescription: "The PROD data lock.", Computed: true},
			"prod_data_locked_at":     dschema.StringAttribute{MarkdownDescription: "When it was locked.", CustomType: TimestampType{}, Computed: true},
			"prod_data_locked_by":     dschema.StringAttribute{MarkdownDescription: "Who locked it.", Computed: true},
			"prior_prod_data_copies":  dschema.Int64Attribute{MarkdownDescription: "Succeeded data copies into PROD, all time.", Computed: true},
			"pending_operation_ids":   dschema.ListAttribute{MarkdownDescription: "`release:<id>` of every operation awaiting approval.", Computed: true, ElementType: types.StringType},
			"in_flight_operation_ids": dschema.ListAttribute{MarkdownDescription: "`release:<id>` of every operation approved or running.", Computed: true, ElementType: types.StringType},
		},
	}
}

func (d *releaseStateDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *releaseStateDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.data == nil {
		resp.Diagnostics.AddError("Provider not configured", "The ataila provider was not configured.")
		return
	}
	var cfg releaseStateModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	s, err := d.data.API.GetReleaseState(ctx, cfg.ProjectID.ValueString())
	if err != nil {
		summary, detail := apiErrorText("the release state of project "+cfg.ProjectID.ValueString(), err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	elems := make([]attr.Value, 0, len(s.Versions))
	for _, v := range s.Versions {
		var src types.String
		if v.SourceEnv != nil {
			src = types.StringValue(string(*v.SourceEnv))
		} else {
			src = types.StringNull()
		}
		elems = append(elems, types.ObjectValueMust(reportedVersionTypes, map[string]attr.Value{
			"env": types.StringValue(string(v.Env)), "component": types.StringValue(string(v.Component)),
			"last_reported_version": types.StringValue(v.LastReportedVersion),
			"last_reported_at":      types.StringValue(FormatTimestamp(v.LastReportedAt)),
			"last_reported_by":      types.StringValue(v.LastReportedBy), "source_env": src,
		}))
	}
	cfg.DeploymentBackend = types.StringValue(string(s.DeploymentBackend))
	cfg.Versions = types.ListValueMust(types.ObjectType{AttrTypes: reportedVersionTypes}, elems)
	cfg.ProdDataLocked = types.BoolValue(s.ProdDataLocked)
	cfg.ProdDataLockedAt = NewTimestampPointer(s.ProdDataLockedAt)
	cfg.ProdDataLockedBy = stringOrNull(s.ProdDataLockedBy)
	cfg.PriorProdDataCopies = types.Int64Value(int64(s.PriorProdDataCopies))
	cfg.PendingOperationIDs = stringList(ctx, s.PendingOperationIds, &resp.Diagnostics)
	cfg.InFlightOperationIDs = stringList(ctx, s.InFlightOperationIds, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &cfg)...)
}

// NewReleaseOperationDataSource is the factory for the
// ataila_release_operation data source.
func NewReleaseOperationDataSource() datasource.DataSource { return &releaseOperationDataSource{} }

type releaseOperationDataSource struct {
	data *ProviderData
}

type releaseOperationModel struct {
	ID               types.String   `tfsdk:"id"`
	OperationID      types.String   `tfsdk:"operation_id"`
	ProjectID        types.String   `tfsdk:"project_id"`
	Operation        types.String   `tfsdk:"operation"`
	Component        types.String   `tfsdk:"component"`
	SourceEnv        types.String   `tfsdk:"source_env"`
	TargetEnv        types.String   `tfsdk:"target_env"`
	Version          types.String   `tfsdk:"version"`
	Status           types.String   `tfsdk:"status"`
	PortalStatus     types.String   `tfsdk:"portal_status"`
	RequestedBy      types.String   `tfsdk:"requested_by"`
	RequestedAt      TimestampValue `tfsdk:"requested_at"`
	RequestedVia     types.String   `tfsdk:"requested_via"`
	RequestedTokenID types.String   `tfsdk:"requested_token_id"`
	DecidedBy        types.String   `tfsdk:"decided_by"`
	DecidedAt        TimestampValue `tfsdk:"decided_at"`
	ApprovalReason   types.String   `tfsdk:"approval_reason"`
	StartedAt        TimestampValue `tfsdk:"started_at"`
	CompletedAt      TimestampValue `tfsdk:"completed_at"`
	ErrorReason      types.String   `tfsdk:"error_reason"`
	PipelineURL      types.String   `tfsdk:"pipeline_url"`
}

func (d *releaseOperationDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_release_operation"
}

func (d *releaseOperationDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	s := func(doc string) dschema.StringAttribute {
		return dschema.StringAttribute{MarkdownDescription: doc, Computed: true}
	}
	ts := func(doc string) dschema.StringAttribute {
		return dschema.StringAttribute{MarkdownDescription: doc + " RFC 3339 in UTC.", CustomType: TimestampType{}, Computed: true}
	}
	resp.Schema = dschema.Schema{
		MarkdownDescription: "One release operation: a promotion, or a data copy booked in the portal.",
		Attributes: map[string]dschema.Attribute{
			"id": dschema.StringAttribute{
				MarkdownDescription: "The release operation's id.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.RegexMatches(rxIntID, "must be a release operation id")},
			},
			"operation_id":       s("`release:<id>`."),
			"project_id":         s("The project."),
			"operation":          s("`promote_build` or `copy_data`."),
			"component":          s("`app-api` or `www`; null for a data copy."),
			"source_env":         s("The environment it comes from."),
			"target_env":         s("The environment it goes to."),
			"version":            s("The version promoted."),
			"status":             s("`awaiting_approval`, `pending`, `running`, `succeeded` or `failed`."),
			"portal_status":      s("The Release Manager's own status."),
			"requested_by":       s("Who asked."),
			"requested_at":       ts("When it was requested."),
			"requested_via":      s("`session`, `pat` or `service_account`; null when booked in the portal or by a pipeline."),
			"requested_token_id": s("The API token used, when one was."),
			"decided_by":         s("Who approved or rejected it."),
			"decided_at":         ts("When it was decided."),
			"approval_reason":    s("The reason and the approver's note; addresses and digests masked."),
			"started_at":         ts("When it started."),
			"completed_at":       ts("When it ended."),
			"error_reason":       s("Why it failed or was rejected; addresses and digests masked."),
			"pipeline_url":       s("The pipeline carrying it out, once known."),
		},
	}
}

func (d *releaseOperationDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *releaseOperationDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.data == nil {
		resp.Diagnostics.AddError("Provider not configured", "The ataila provider was not configured.")
		return
	}
	var cfg releaseOperationModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	o, err := d.data.API.GetReleaseOperation(ctx, cfg.ID.ValueString())
	if err != nil {
		summary, detail := apiErrorText("release operation "+cfg.ID.ValueString(), err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	var p promotionModel
	p.fromAPI(o)
	out := releaseOperationModel{ID: p.ID, OperationID: p.OperationID, ProjectID: p.ProjectID,
		Operation: types.StringValue(string(o.Operation)), Component: types.StringNull(), SourceEnv: p.SourceEnv,
		TargetEnv: p.TargetEnv, Version: p.Version, Status: p.Status, PortalStatus: p.PortalStatus,
		RequestedBy: p.RequestedBy, RequestedAt: p.RequestedAt, RequestedVia: p.RequestedVia,
		RequestedTokenID: p.RequestedTokenID, DecidedBy: p.DecidedBy, DecidedAt: p.DecidedAt,
		ApprovalReason: p.ApprovalReason, StartedAt: p.StartedAt, CompletedAt: p.CompletedAt,
		ErrorReason: p.ErrorReason, PipelineURL: p.PipelineURL}
	if o.Component != nil {
		out.Component = types.StringValue(string(*o.Component))
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &out)...)
}

func regexpMust(expr string) *regexp.Regexp { return regexp.MustCompile(expr) }
