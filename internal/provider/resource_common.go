// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"golang.org/x/net/idna"

	"github.com/ataila/terraform-provider-ataila/internal/client"
)

// ── frozen keys (F13) ────────────────────────────────────────────────────────
//
// A frozen key is set when the object is created and never changes. A change
// to one fails the PLAN with an explanation. It must never become a replace:
// destroying a customer only archives it and an archived customer keeps its
// keys, so the re-create of a replace could never succeed; replacing a tenant
// would delete it, which the platform allows only for an empty tenant.

type frozenKey struct {
	object string // "customer", "tenant"
	why    string // why the provider does not replace the object instead
}

var (
	customerFrozen = frozenKey{object: "customer", why: "Destroying a customer only archives it, and an " +
		"archived customer keeps its keys, so a replacement with these keys could never be created."}
	tenantFrozen = frozenKey{object: "tenant", why: "Replacing a tenant would delete it, which the platform " +
		"allows only for an empty tenant that is not a customer's primary, and would drop its memberships."}
	projectFrozen = frozenKey{object: "project", why: "Destroying a project only retires it, and a retired " +
		"project keeps its project_index, short_name and primary_domain reserved for good, so a replacement " +
		"with these keys could never be created."}
	provisioningFrozen = frozenKey{object: "project provisioning", why: "A provisioning belongs to one " +
		"project. Declare a separate ataila_project_provisioning for the other project."}
)

func (f frozenKey) forString() planmodifier.String { return frozenString{f} }
func (f frozenKey) forInt64() planmodifier.Int64   { return frozenInt64{f} }
func (f frozenKey) forBool() planmodifier.Bool     { return frozenBool{f} }

func (f frozenKey) Description(context.Context) string {
	return "Frozen: set when the " + f.object + " is created and never changed. A change fails the plan; " +
		"the provider never replaces the " + f.object + " to apply it."
}

func (f frozenKey) MarkdownDescription(ctx context.Context) string { return f.Description(ctx) }

// check is the plan-time rule: on an update (state and plan both present), a
// known planned value that differs from the state is an error. Unknown values
// are checked again in Update, before any API call.
func (f frozenKey) check(p path.Path, state, plan tfValue, isUpdate bool, diags *diag.Diagnostics) {
	if !isUpdate || plan.IsUnknown() || plan.Equal(state) {
		return
	}
	diags.Append(f.diagnostic(p, state, plan))
}

func (f frozenKey) diagnostic(p path.Path, state, plan tfValue) diag.Diagnostic {
	name := p.String()
	return diag.NewAttributeErrorDiagnostic(p,
		fmt.Sprintf("Cannot change %s of an existing %s", name, f.object),
		fmt.Sprintf("%s is fixed when the %s is created and can never change. It is %s; the configuration "+
			"asks for %s.\n\n"+
			"The provider refuses the change instead of replacing the %s. %s\n\n"+
			"To keep managing this %s, set %s back to %s. To manage a different %s, declare a new resource "+
			"for it; this one stays until it is destroyed or removed from the state.",
			name, f.object, showValue(state), showValue(plan), f.object, f.why, f.object, name,
			showValue(state), f.object))
}

// tfValue is what types.String and types.Int64 have in common here.
type tfValue interface {
	attr.Value
	IsNull() bool
	IsUnknown() bool
}

func showValue(v tfValue) string {
	if v.IsNull() {
		return "unset"
	}
	return v.String()
}

type frozenString struct{ frozenKey }

func (m frozenString) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	isUpdate := !req.State.Raw.IsNull() && !req.Plan.Raw.IsNull()
	m.check(req.Path, req.StateValue, resp.PlanValue, isUpdate, &resp.Diagnostics)
}

type frozenInt64 struct{ frozenKey }

func (m frozenInt64) PlanModifyInt64(_ context.Context, req planmodifier.Int64Request, resp *planmodifier.Int64Response) {
	isUpdate := !req.State.Raw.IsNull() && !req.Plan.Raw.IsNull()
	m.check(req.Path, req.StateValue, resp.PlanValue, isUpdate, &resp.Diagnostics)
}

type frozenBool struct{ frozenKey }

func (m frozenBool) PlanModifyBool(_ context.Context, req planmodifier.BoolRequest, resp *planmodifier.BoolResponse) {
	isUpdate := !req.State.Raw.IsNull() && !req.Plan.Raw.IsNull()
	m.check(req.Path, req.StateValue, resp.PlanValue, isUpdate, &resp.Diagnostics)
}

// ── destroy (D7, F8) ─────────────────────────────────────────────────────────

// destroyVerb says what destroy does to each destroy-gated object.
var destroyVerb = map[string]string{
	"customer": "archive",
	"tenant":   "delete",
	"user":     "deactivate",
	"project":  "retire",
}

// destroyRefused is the diagnostic when the provider's allow_destroy is off.
// It is raised at plan time and again in Delete, before any API call.
func destroyRefused(resourceType, object, ident string) diag.Diagnostic {
	what := map[string]string{
		"customer": "For a customer, destroy means archive: the customer, its keys, its tenants and its GitLab " +
			"group stay, and an archived customer can never be changed or re-created.",
		"tenant": "For a tenant, destroy means delete, which the platform allows only for an empty tenant " +
			"that is not a customer's primary.",
		"user": "For a user, destroy means deactivate: the person stays, cannot sign in, and keeps their " +
			"e-mail address, so the same address cannot be created again (import it and set is_active = true " +
			"instead). Setting is_active = false is a deactivation too, behind the same switches.",
		"project": "For a project, destroy means retire: its status becomes retired and nothing on the " +
			"substrate is touched (no machine, DNS record, secret or repository is removed). A retired " +
			"project keeps its project_index, short_name and primary_domain reserved for good, so they " +
			"cannot be used again.",
	}[object]
	return diag.NewErrorDiagnostic(
		fmt.Sprintf("Destroying %s %s is not allowed", article(object), object),
		fmt.Sprintf("The provider would have to %s the %s %s, and destroying is switched off.\n\n"+
			"Destroy needs two switches, and both must be on:\n"+
			"  1. allow_destroy = true in the provider \"ataila\" block (it is false by default);\n"+
			"  2. an API token minted in the portal with destroy allowed. The platform refuses a destroy "+
			"from any other token.\n\n%s\n\n"+
			"To stop managing it without destroying it, remove it from the state instead:\n"+
			"  tofu state rm %s.<name>       (OpenTofu)\n"+
			"  terraform state rm %s.<name>  (Terraform)\n"+
			"OpenTofu 1.7 and Terraform 1.7 or later can do the same in configuration with a removed block "+
			"whose lifecycle sets destroy = false.",
			destroyVerb[object], object, ident, what, resourceType, resourceType))
}

// destroyError turns a failed DELETE into a diagnostic: the token's own flag,
// the platform's refusals (409) with what blocks them, anything else.
func destroyError(resourceType, object, ident string, err error) diag.Diagnostic {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		return diag.NewErrorDiagnostic("Cannot reach the ATAILA API",
			fmt.Sprintf("While trying to %s the %s %s: %v", destroyVerb[object], object, ident, err))
	}
	switch {
	case apiErr.StatusCode == 403 && apiErr.Code() == client.CodeDestroyNotAllowed:
		return diag.NewErrorDiagnostic("The token was not created with allow_destroy",
			fmt.Sprintf("The provider's allow_destroy is true, but the platform refused to %s the %s %s: "+
				"the API token in use was minted without destroy allowed. A destroy needs both switches.\n\n"+
				"Mint a token with destroy allowed in the portal and use it for this run, or remove the %s "+
				"from the state instead (tofu state rm / terraform state rm %s.<name>).\n\n%s",
				destroyVerb[object], object, ident, object, resourceType, apiErr.Detail()))
	case apiErr.StatusCode == 409:
		hint := map[string]string{
			"customer_has_projects": "Every project of the customer counts, retired ones included. Remove " +
				"the projects first, or remove the customer from the state.",
			"tenant_is_primary": "A customer's primary tenant is never deleted: it goes with its customer, " +
				"which is archived. Remove the tenant from the state instead.",
			"cannot_deactivate_self": "The token's own account cannot be deactivated with that token. Remove " +
				"the user from the state, or deactivate them in the portal.",
			"last_active_admin": "The platform keeps at least one active admin. Make another person admin " +
				"first, or remove this user from the state.",
			"service_account_managed_elsewhere": "Service accounts are managed on the portal's service " +
				"accounts page, not through this resource. Remove it from the state.",
			client.CodeOrchestrationInProgress: "A provisioning run holds the project. Wait for it to " +
				"finish (the operation_id below names it), then destroy again.",
			client.CodePlatformReadOnly: "This is one of the platform's own projects; it is read-only " +
				"through the API. Remove it from the state instead.",
		}[apiErr.Code()]
		if hint == "" && strings.HasPrefix(apiErr.Code(), "tenant_has_") {
			hint = "Only an empty tenant can be deleted; blockers counts what still hangs off it. Move or " +
				"remove those first, or remove the tenant from the state."
		}
		detail := apiErr.Detail()
		if hint != "" {
			detail = hint + "\n\n" + detail
		}
		return diag.NewErrorDiagnostic(
			fmt.Sprintf("The platform refused to %s the %s (%s)", destroyVerb[object], object, apiErr.Code()),
			detail)
	}
	return diag.NewErrorDiagnostic(apiErr.Summary(),
		fmt.Sprintf("While trying to %s the %s %s.\n\n%s", destroyVerb[object], object, ident, apiErr.Detail()))
}

func article(noun string) string {
	if strings.HasPrefix(noun, "user") {
		return "a"
	}
	if strings.ContainsAny(noun[:1], "aeiou") {
		return "an"
	}
	return "a"
}

// ── API errors and warnings ──────────────────────────────────────────────────

// warningAttrTypes is one element of a `warnings` attribute.
var warningAttrTypes = map[string]attr.Type{"code": types.StringType, "message": types.StringType}

// warningsValue is the `warnings` attribute: what did not go as planned in
// the last request that changed the object.
func warningsValue(ws []client.ApiWarning) types.List {
	elems := make([]attr.Value, 0, len(ws))
	for _, w := range ws {
		elems = append(elems, types.ObjectValueMust(warningAttrTypes, map[string]attr.Value{
			"code": types.StringValue(w.Code), "message": types.StringValue(w.Message)}))
	}
	return types.ListValueMust(types.ObjectType{AttrTypes: warningAttrTypes}, elems)
}

// keepWarnings is the `warnings` attribute on a read: a read changes
// nothing, so the last change's warnings stay; none yet is an empty list.
func keepWarnings(prior types.List) types.List {
	if prior.IsNull() || prior.IsUnknown() {
		return warningsValue(nil)
	}
	return prior
}

// gatewayUnavailable explains a final 503 from the AI gateway routes, or
// returns nil for any other error.
func gatewayUnavailable(doing string, err error) diag.Diagnostic {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) || !apiErr.IsFinalUnavailable() {
		return nil
	}
	why := map[string]string{
		client.CodeGatewayNotConfigured: "This platform has no AI gateway configured. The AI gateway resources " +
			"and data sources need a platform with a gateway.",
		client.CodeGatewayUnreachable: "The platform's AI gateway (or the secrets store it keeps key values in) cannot be " +
			"reached right now. Try again when it is back; the provider does not retry this.",
		client.CodeSecretStoreWriteFailed: "The secrets store did not store the key's value. " +
			"key_removed_from_gateway says whether the minted key was removed again.",
	}[apiErr.Code()]
	return diag.NewErrorDiagnostic(fmt.Sprintf("The AI gateway is not available (%s)", apiErr.Code()),
		fmt.Sprintf("While %s.\n\n%s\n\n%s", doing, why, apiErr.Detail()))
}

// apiError is the diagnostic for any other failed call.
func apiError(doing string, err error) diag.Diagnostic {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		return diag.NewErrorDiagnostic(apiErr.Summary(), fmt.Sprintf("While %s.\n\n%s", doing, apiErr.Detail()))
	}
	return diag.NewErrorDiagnostic("Cannot reach the ATAILA API", fmt.Sprintf("While %s: %v", doing, err))
}

func isNotFound(err error) bool {
	var apiErr *client.APIError
	return errors.As(err, &apiErr) && apiErr.IsNotFound()
}

// addWarnings reports the API's warnings on a request that succeeded. They
// are never errors: the change was made.
func addWarnings(diags *diag.Diagnostics, doing string, warnings *[]client.ApiWarning) {
	if warnings == nil {
		return
	}
	for _, w := range *warnings {
		msg := strings.TrimSpace(w.Message)
		if msg == "" {
			msg = "(no message)"
		}
		diags.AddWarning("ATAILA API warning: "+w.Code,
			fmt.Sprintf("%s\n\nThis did not go as planned while %s. The request itself succeeded and its "+
				"result is in the state. (code: %s)", msg, doing, w.Code))
	}
}

// ── small conversions ────────────────────────────────────────────────────────

// sameEmail compares two addresses as the platform stores them: the local
// part exactly, the domain case-folded and in its canonical (Unicode) form, so
// Pat@Example.COM, Pat@example.com and an internationalised domain written in
// either form are the same address.
func sameEmail(a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	ia, ib := strings.LastIndex(a, "@"), strings.LastIndex(b, "@")
	if ia < 0 || ib < 0 {
		return a == b
	}
	return a[:ia] == b[:ib] && strings.EqualFold(canonicalDomain(a[ia+1:]), canonicalDomain(b[ib+1:]))
}

func canonicalDomain(d string) string {
	if u, err := idna.Lookup.ToUnicode(strings.ToLower(d)); err == nil {
		return u
	}
	return d
}

// keepEmail keeps the configured spelling of an address the platform stored
// normalised, so the state matches the configuration.
func keepEmail(prior types.String, stored string) types.String {
	if !prior.IsNull() && !prior.IsUnknown() && sameEmail(prior.ValueString(), stored) {
		return prior
	}
	return types.StringValue(stored)
}

func int64OrNull(p *int) types.Int64 {
	if p == nil {
		return types.Int64Null()
	}
	return types.Int64Value(int64(*p))
}

// optString is the pointer a request body wants for an optional attribute.
func optString(v types.String) *string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	s := v.ValueString()
	return &s
}

// nullable is the PATCH value of an optional attribute: nil (JSON null)
// clears it.
func nullable(v types.String) any {
	if v.IsNull() {
		return nil
	}
	return v.ValueString()
}
