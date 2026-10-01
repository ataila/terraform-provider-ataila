// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-timeouts/resource/timeouts"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/ataila/terraform-provider-ataila/internal/client"
)

var (
	_ resource.ResourceWithConfigure   = (*provisioningResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*provisioningResource)(nil)
	_ resource.ResourceWithImportState = (*provisioningResource)(nil)
)

// defaultProvisioningTimeout is how long create and update wait by default.
const defaultProvisioningTimeout = 60 * time.Minute

// operationPollInterval is how often the provider polls an operation. Tests
// shorten it.
var operationPollInterval = 5 * time.Second

// NewProjectProvisioningResource is the factory for
// ataila_project_provisioning.
func NewProjectProvisioningResource() resource.Resource { return &provisioningResource{} }

type provisioningResource struct {
	data *ProviderData
}

type provisioningModel struct {
	ID           types.String   `tfsdk:"id"`
	ProjectID    types.String   `tfsdk:"project_id"`
	Converged    types.Bool     `tfsdk:"converged"`
	Provisioned  types.Bool     `tfsdk:"provisioned"`
	Simulated    types.Bool     `tfsdk:"simulated"`
	DispatchMode types.String   `tfsdk:"dispatch_mode"`
	State        types.String   `tfsdk:"state"`
	Stages       types.List     `tfsdk:"stages"`
	StaleStages  types.List     `tfsdk:"stale_stages"`
	OperationID  types.String   `tfsdk:"operation_id"`
	Timeouts     timeouts.Value `tfsdk:"timeouts"`
}

var stageStateTypes = map[string]attr.Type{
	"key": types.StringType, "status": types.StringType, "stale": types.BoolType, "simulated": types.BoolType,
	"last_run_id": types.StringType, "last_run_at": types.StringType,
}

func (m *provisioningModel) fromAPI(p *client.Provisioning) {
	m.ID = types.StringValue(p.ProjectId)
	m.ProjectID = types.StringValue(p.ProjectId)
	m.Converged = types.BoolValue(p.Converged)
	m.Provisioned = types.BoolValue(p.Provisioned)
	m.Simulated = types.BoolValue(p.Simulated)
	m.DispatchMode = types.StringValue(string(p.DispatchMode))
	m.State = types.StringValue(string(p.State))
	elems := make([]attr.Value, 0, len(p.Stages))
	for _, s := range p.Stages {
		sim := s.Simulated != nil && *s.Simulated
		elems = append(elems, types.ObjectValueMust(stageStateTypes, map[string]attr.Value{
			"key": types.StringValue(s.Key), "status": types.StringValue(string(s.Status)),
			"stale": types.BoolValue(s.Stale), "simulated": types.BoolValue(sim),
			"last_run_id": stringOrNull(s.LastRunId), "last_run_at": timestampString(s.LastRunAt),
		}))
	}
	m.Stages = types.ListValueMust(types.ObjectType{AttrTypes: stageStateTypes}, elems)
	stale := make([]attr.Value, 0, len(p.StaleStages))
	for _, s := range p.StaleStages {
		stale = append(stale, types.StringValue(s))
	}
	m.StaleStages = types.ListValueMust(types.StringType, stale)
	if m.OperationID.IsUnknown() {
		m.OperationID = types.StringNull()
	}
}

func (r *provisioningResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project_provisioning"
}

func (r *provisioningResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keepBool := []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}
	keepString := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	keepList := []planmodifier.List{listplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "The provisioning of a project: the platform's stage engine building what the " +
			"project's settings describe. One per project.\n\n" +
			"**Create** starts provisioning and waits until the operation ends and the project is " +
			"**converged**: every stage done and none stale. When the project changes later (its " +
			"`stale_stages`), or a stage is undone, the next plan shows an **in-place update** of " +
			"`converged` from `false` to `true`; applying it starts provisioning again (the platform then " +
			"re-applies only the stale stages) and waits.\n\n" +
			"While a stage waits for an operator in the portal the provider keeps waiting, up to the " +
			"timeout. When provisioning is already running (started in the portal, or by an earlier run " +
			"that timed out), the provider waits for that run instead of starting another. When the " +
			"timeout passes, the resource is left with the last known stage in the error (a new resource " +
			"is marked tainted); the run goes on on the platform, and the next apply waits for it.\n\n" +
			"On a platform whose `dispatch_mode` is `dryrun` nothing is ever executed and provisioning can " +
			"never finish, so the provider fails at once. Under `simulate` stages are marked done without " +
			"running: `converged` becomes true while `provisioned` stays false.\n\n" +
			"**Destroy only removes the resource from the state.** Nothing is torn down: the API has no " +
			"way to undo provisioning.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The project id.",
				Computed:            true,
				PlanModifiers:       keepString,
			},
			"project_id": schema.StringAttribute{
				MarkdownDescription: "Id of the project to provision. **Frozen:** a provisioning never moves to " +
					"another project.",
				Required:      true,
				Validators:    []validator.String{stringvalidator.RegexMatches(rxIntID, "must be a project id")},
				PlanModifiers: []planmodifier.String{provisioningFrozen.forString()},
			},
			"converged": schema.BoolAttribute{
				MarkdownDescription: "Every stage is done and none is stale, by real or by simulated runs. " +
					"`false` in the state makes the next plan an update that provisions again.",
				Computed:      true,
				PlanModifiers: keepBool,
			},
			"provisioned": schema.BoolAttribute{
				MarkdownDescription: "Converged on real runs only (no stage simulated): the substrate exists.",
				Computed:            true,
				PlanModifiers:       keepBool,
			},
			"simulated": schema.BoolAttribute{
				MarkdownDescription: "At least one stage's latest run was simulated: marked done, never run.",
				Computed:            true,
				PlanModifiers:       keepBool,
			},
			"dispatch_mode": schema.StringAttribute{
				MarkdownDescription: "How the platform runs stages: `live`, `simulate` or `dryrun`.",
				Computed:            true,
				PlanModifiers:       keepString,
			},
			"state": schema.StringAttribute{
				MarkdownDescription: "`not_started`, `provisioning`, `complete`, `attention` (a stage failed) or " +
					"`needs_action` (a stage waits for an operator).",
				Computed:      true,
				PlanModifiers: keepString,
			},
			"stages": schema.ListAttribute{
				MarkdownDescription: "The stages in apply order: `key`, `status` (`pending`, `running`, " +
					"`success`, `failed`, `partial` or `manual`), `stale`, `simulated`, and `last_run_id` and " +
					"`last_run_at` (RFC 3339) of the stage's newest run in the portal's run history (null when it " +
					"never ran). The `ataila_project_stages` data source has more detail.",
				Computed:      true,
				ElementType:   types.ObjectType{AttrTypes: stageStateTypes},
				PlanModifiers: keepList,
			},
			"stale_stages": schema.ListAttribute{
				MarkdownDescription: "Stages done against older settings, in apply order.",
				Computed:            true,
				ElementType:         types.StringType,
				PlanModifiers:       keepList,
			},
			"operation_id": schema.StringAttribute{
				MarkdownDescription: "The last provisioning operation the provider started or waited for " +
					"(`provision:<n>`).",
				Computed:      true,
				PlanModifiers: keepString,
			},
		},
		Blocks: map[string]schema.Block{
			"timeouts": timeouts.Block(ctx, timeouts.Opts{Create: true, Update: true}),
		},
	}
}

func (r *provisioningResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *provisioningResource) configured(diags interface{ AddError(string, string) }) bool {
	if r.data == nil {
		diags.AddError("Provider not configured", "The ataila provider was not configured.")
		return false
	}
	return true
}

// ModifyPlan turns a project that is not converged into an in-place update
// whose apply provisions again.
func (r *provisioningResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var state, plan provisioningModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || state.Converged.ValueBool() {
		return
	}
	plan.Converged = types.BoolValue(true)
	plan.Provisioned = types.BoolUnknown()
	plan.Simulated = types.BoolUnknown()
	plan.State = types.StringUnknown()
	plan.Stages = types.ListUnknown(types.ObjectType{AttrTypes: stageStateTypes})
	plan.StaleStages = types.ListUnknown(types.StringType)
	plan.OperationID = types.StringUnknown()
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

func (r *provisioningResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan provisioningModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	wait, d := plan.Timeouts.Create(ctx, defaultProvisioningTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	state, ok := r.provision(ctx, plan, wait, &resp.Diagnostics)
	if ok || state != nil {
		resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
	}
}

func (r *provisioningResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan, prior provisioningModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	provisioningFrozen.check(path.Root("project_id"), prior.ProjectID, plan.ProjectID, true, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if prior.Converged.ValueBool() {
		// Only the timeouts changed: nothing to run.
		prior.Timeouts = plan.Timeouts
		resp.Diagnostics.Append(resp.State.Set(ctx, &prior)...)
		return
	}
	wait, d := plan.Timeouts.Update(ctx, defaultProvisioningTimeout)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.OperationID = prior.OperationID
	state, ok := r.provision(ctx, plan, wait, &resp.Diagnostics)
	if ok || state != nil {
		resp.Diagnostics.Append(resp.State.Set(ctx, state)...)
	}
}

// provision starts provisioning (or adopts the run holding the project) and
// waits for it. It returns the state to store and whether it converged; on a
// failure after the start it returns the last known state with the error.
func (r *provisioningResource) provision(ctx context.Context, plan provisioningModel, wait time.Duration, diags *diag.Diagnostics) (*provisioningModel, bool) {
	projectID := plan.ProjectID.ValueString()
	what := "project " + projectID
	before, err := r.data.API.GetProvisioning(ctx, projectID)
	if err != nil {
		diags.Append(apiError("reading the provisioning of the "+what, err))
		return nil, false
	}
	if before.DispatchMode == client.ProvisioningDispatchModeDryrun {
		diags.AddError("Provisioning can never finish on this platform",
			fmt.Sprintf("The platform's dispatch_mode is dryrun: pipeline dispatch is faked, no stage is ever "+
				"executed and provisioning never completes. The provider does not start it for the %s.\n\n%s",
				what, strings.TrimSpace(ptrString(before.Message))))
		return nil, false
	}

	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()

	op, running, err := r.data.API.StartProvisioning(ctx, projectID)
	if err != nil {
		diags.Append(provisioningStartError(what, err))
		return nil, false
	}
	opID := running
	if op != nil {
		opID = op.Id
	} else {
		diags.AddWarning("Provisioning was already running",
			fmt.Sprintf("An orchestration already held the %s (operation %s). The provider waited for it "+
				"instead of starting another.", what, running))
	}
	plan.OperationID = types.StringValue(opID)

	last, waitedFor, err := r.poll(ctx, opID, op)
	for _, stage := range waitedFor {
		diags.AddWarning("A stage waited for an operator",
			fmt.Sprintf("Stage %s of the %s needed a person in the portal while the provider waited "+
				"(operation %s).", stage, what, opID))
	}

	// Read the outcome with a context of its own: the wait may have used up ctx.
	readCtx, readCancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer readCancel()
	after, readErr := r.data.API.GetProvisioning(readCtx, projectID)
	state := plan
	if readErr == nil {
		state.fromAPI(after)
	} else {
		state.Converged = types.BoolValue(false)
		for _, v := range []*types.Bool{&state.Provisioned, &state.Simulated} {
			if v.IsUnknown() {
				*v = types.BoolNull()
			}
		}
		for _, v := range []*types.String{&state.State, &state.DispatchMode} {
			if v.IsUnknown() {
				*v = types.StringNull()
			}
		}
		if state.Stages.IsUnknown() {
			state.Stages = types.ListNull(types.ObjectType{AttrTypes: stageStateTypes})
		}
		if state.StaleStages.IsUnknown() {
			state.StaleStages = types.ListNull(types.StringType)
		}
		state.ID = plan.ProjectID
	}

	stage := lastStage(last, after)
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		diags.AddError("Provisioning did not finish in time",
			fmt.Sprintf("The %s was not converged after %s. Last stage: %s. Operation %s keeps running on "+
				"the platform; the next apply waits for it (raise timeouts to wait longer).",
				what, wait, stage, opID))
		return &state, false
	case err != nil:
		diags.Append(apiError(fmt.Sprintf("waiting for operation %s of the %s", opID, what), err))
		return &state, false
	case last.Status == client.OperationStatusFailed:
		diags.AddError("Provisioning failed",
			fmt.Sprintf("Operation %s of the %s failed at stage %s.\n\n%s\n\nFix the cause (the portal "+
				"shows the stage's pipeline), then apply again: the next run starts provisioning again.",
				opID, what, stage, operationErrorText(last)))
		return &state, false
	case readErr != nil:
		diags.Append(apiError("reading the provisioning of the "+what, readErr))
		return &state, false
	case !after.Converged:
		diags.AddError("Provisioning finished but the project is not converged",
			fmt.Sprintf("Operation %s of the %s succeeded, but the project is not converged: %s. Apply "+
				"again to provision the rest.", opID, what, notConvergedText(after)))
		return &state, false
	}
	if last.DispatchMode != nil && *last.DispatchMode == client.OperationDispatchModeSimulate {
		tflog.Info(ctx, "provisioning was simulated", map[string]any{"project_id": projectID})
	}
	return &state, true
}

// poll waits for an operation to end. It returns the last answer and the
// stages that waited for an operator on the way.
func (r *provisioningResource) poll(ctx context.Context, id string, first *client.Operation) (*client.Operation, []string, error) {
	last := first
	var awaiting []string
	seen := map[string]bool{}
	for {
		if last != nil {
			switch last.Status {
			case client.OperationStatusSucceeded, client.OperationStatusFailed:
				return last, awaiting, nil
			case client.OperationStatusAwaitingOperator:
				if s := ptrString(last.Stage); !seen[s] {
					seen[s] = true
					awaiting = append(awaiting, s)
					tflog.Warn(ctx, "a provisioning stage waits for an operator", map[string]any{"stage": s})
				}
			}
			if last.DispatchMode != nil && *last.DispatchMode == client.OperationDispatchModeDryrun {
				return last, awaiting, errors.New("the platform's dispatch_mode is dryrun: the operation can never finish")
			}
		}
		select {
		case <-ctx.Done():
			return last, awaiting, ctx.Err()
		case <-time.After(operationPollInterval):
		}
		op, err := r.data.API.Operation(ctx, id)
		if err != nil {
			if ctx.Err() != nil {
				return last, awaiting, ctx.Err()
			}
			return last, awaiting, err
		}
		last = op
	}
}

func lastStage(op *client.Operation, p *client.Provisioning) string {
	var parts []string
	if op != nil && ptrString(op.Stage) != "" {
		parts = append(parts, fmt.Sprintf("%s (operation %s)", ptrString(op.Stage), op.Status))
	}
	if p != nil {
		for _, s := range p.Stages {
			if s.Status == client.StageStateStatusRunning || s.Status == client.StageStateStatusManual ||
				s.Status == client.StageStateStatusFailed {
				parts = append(parts, fmt.Sprintf("%s is %s", s.Key, s.Status))
			}
		}
	}
	if len(parts) == 0 {
		return "none reported"
	}
	return strings.Join(parts, "; ")
}

func notConvergedText(p *client.Provisioning) string {
	var parts []string
	var pending []string
	for _, s := range p.Stages {
		if s.Status != client.StageStateStatusSuccess {
			pending = append(pending, s.Key+" "+string(s.Status))
		}
	}
	if len(pending) > 0 {
		parts = append(parts, "not done: "+strings.Join(pending, ", "))
	}
	if len(p.StaleStages) > 0 {
		parts = append(parts, "stale: "+strings.Join(p.StaleStages, ", "))
	}
	if len(parts) == 0 {
		return "state " + string(p.State)
	}
	return strings.Join(parts, "; ")
}

func provisioningStartError(what string, err error) diag.Diagnostic {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == 409 {
		switch apiErr.Code() {
		case client.CodePlatformReadOnly:
			return diag.NewErrorDiagnostic("The platform's own projects are read-only",
				fmt.Sprintf("The %s is one of the platform's own projects; it cannot be provisioned through "+
					"the API.\n\n%s", what, apiErr.Detail()))
		case client.CodeProjectRetired:
			return diag.NewErrorDiagnostic("The project is retired",
				fmt.Sprintf("The %s is retired; a retired project is never provisioned again. Remove the "+
					"ataila_project_provisioning from the configuration and the state.\n\n%s", what, apiErr.Detail()))
		}
	}
	return apiError("starting the provisioning of the "+what, err)
}

func ptrString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func (r *provisioningResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var state provisioningModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.data.API.GetProvisioning(ctx, state.ProjectID.ValueString())
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.Append(apiError("reading the provisioning of the project "+state.ProjectID.ValueString(), err))
		return
	}
	state.fromAPI(p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Delete forgets the resource: the API cannot undo provisioning.
func (r *provisioningResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}

func (r *provisioningResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := strings.TrimSpace(req.ID)
	if !rxIntID.MatchString(id) {
		resp.Diagnostics.AddError("Cannot import the provisioning",
			fmt.Sprintf("The import id is the project id (1 to 999999999, no leading zero), got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("project_id"), id)...)
}
