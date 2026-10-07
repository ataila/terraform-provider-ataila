// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ataila/terraform-provider-ataila/internal/client"
)

// A Kubernetes project's namespace quota: one block per environment, each the
// EFFECTIVE quota (the platform's tier default with the project's overrides
// merged over it), GPU scheduling included. The platform reads it on the
// project (`k8s_quota`) and changes it per environment
// (PATCH /projects/{id}/k8s-quota/{env}); the resource sets the eleven quota
// keys of an environment and reads the rest.

// quotaEnvs are the environments, in the platform's order.
var quotaEnvs = []string{"dev", "uat", "prod"}

// quotaKeys are the quota keys a configuration may set, in the platform's
// order; every value is a Kubernetes quantity carried as a string.
var quotaKeys = []string{"req_cpu", "req_mem", "lim_cpu", "lim_mem", "pvc", "storage", "pods",
	"gpu_exclusive", "gpu_shared", "gpu_borrow", "fair_weight"}

// quotaReadOnly are the members the platform reports and nothing sets.
var quotaReadOnly = map[string]attr.Type{
	"namespace":       types.StringType,
	"cluster":         types.StringType,
	"tier":            types.StringType,
	"gpu_queue":       types.BoolType,
	"gpu_enabled":     types.BoolType,
	"overridden":      types.BoolType,
	"overridden_keys": types.ListType{ElemType: types.StringType},
}

// defaultQuotaReason is sent when the configuration gives no `reason`.
const defaultQuotaReason = "Set through the ATAILA Terraform provider."

const quotaReasonDoc = "Why this environment's quota differs from the tier default, 1-1000 characters; the " +
	"platform records it with the environment's override (an override moves the project's billing basis). Sent " +
	"with every change of this environment's keys. A new reason alone is sent too when the environment has an " +
	"override (its overridden keys go again, unchanged, with it); without one there is nothing on the platform to " +
	"carry it, and only the state records it. Left out, the reason given last is kept; when none was ever given, `" +
	defaultQuotaReason + "` is sent and the state holds null. The platform does not report it back, so an " +
	"imported project has none."

// quotaEnvTypes are the attribute types of one environment's block. The
// resource's block also carries `reason`; the data source's does not.
func quotaEnvTypes(withReason bool) map[string]attr.Type {
	t := make(map[string]attr.Type, len(quotaReadOnly)+len(quotaKeys)+1)
	for k, v := range quotaReadOnly {
		t[k] = v
	}
	for _, k := range quotaKeys {
		t[k] = types.StringType
	}
	if withReason {
		t["reason"] = types.StringType
	}
	return t
}

// quotaType is the type of the whole `k8s_quota` attribute.
func quotaType(withReason bool) types.ObjectType {
	env := types.ObjectType{AttrTypes: quotaEnvTypes(withReason)}
	return types.ObjectType{AttrTypes: map[string]attr.Type{"dev": env, "uat": env, "prod": env}}
}

var quotaDoc = contractDoc("KubernetesQuota")

const quotaAttrDoc = "Kubernetes backend only: the namespace quota of each environment (`dev`, `uat`, `prod`) — " +
	"the tier default with the project's overrides merged over it — including GPU scheduling: `gpu_exclusive` " +
	"(whole cards guaranteed, the floor), `gpu_borrow` (idle cards the namespace may borrow, preempted when the " +
	"owner needs them back), `fair_weight` (its share among borrowers) and `gpu_shared` (time-sliced units, only " +
	"where a dedicated time-sliced node exists). Null for a VM-backend project, for a project registered " +
	"without a manifest, and on a platform before release 1.0.203."

const quotaResourceDoc = quotaAttrDoc + "\n\n" +
	"Set any of the eleven quota keys of an environment and the provider sends the ones that differ (with " +
	"`reason`) to the platform's quota editor, one request per environment, after the project is created or " +
	"changed. The platform checks and compiles the change before storing it, then re-applies the project's " +
	"GitOps stages itself. A key left out keeps its current value; setting a key to the tier default's value " +
	"keeps an override with that value (the portal's reset drops an environment's overrides altogether). A " +
	"count may be spelled with leading zeros (`\"040\"` is 40): the state keeps the configuration's spelling. A " +
	"GPU key above `0` is admitted only where the environment's cluster runs the GPU queue (`gpu_queue`), and the " +
	"guaranteed floors of every namespace on one cluster may not exceed its tenant cards; the platform refuses " +
	"otherwise and the apply fails with its message, nothing stored. Any change here needs a token holding " +
	"`k8s-gpu-admin-global`; reading needs only the project read permission. Needs platform release 1.0.203 or " +
	"later: on an older platform the attribute is null, and a configuration that sets it is refused at plan time, " +
	"on create and on update, before any request. Removing `k8s_quota` from the configuration, or destroying the " +
	"project, changes no quota: the overrides stay on the platform (a destroyed project is retired)."

func quotaEnvDoc(env string) string {
	return fmt.Sprintf("The `%s` environment's namespace quota.", env)
}

// projectQuotaResourceAttribute is the `k8s_quota` attribute of ataila_project.
func projectQuotaResourceAttribute() rschema.Attribute {
	envBlock := func(env string) rschema.Attribute {
		attrs := map[string]rschema.Attribute{}
		for name, t := range quotaReadOnly {
			if lt, ok := t.(types.ListType); ok {
				attrs[name] = rschema.ListAttribute{MarkdownDescription: quotaDoc([]string{name}), Computed: true,
					ElementType: lt.ElemType}
				continue
			}
			if t.Equal(types.BoolType) {
				attrs[name] = rschema.BoolAttribute{MarkdownDescription: quotaDoc([]string{name}), Computed: true}
				continue
			}
			attrs[name] = rschema.StringAttribute{MarkdownDescription: quotaDoc([]string{name}), Computed: true}
		}
		for _, k := range quotaKeys {
			attrs[k] = rschema.StringAttribute{
				MarkdownDescription: quotaDoc([]string{k}) + " Leave it out to keep the current value.",
				Optional:            true,
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			}
		}
		attrs["reason"] = rschema.StringAttribute{
			MarkdownDescription: quotaReasonDoc,
			Optional:            true,
			Computed:            true,
			PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		}
		return rschema.SingleNestedAttribute{
			MarkdownDescription: quotaEnvDoc(env),
			Optional:            true,
			Computed:            true,
			Attributes:          attrs,
			PlanModifiers:       []planmodifier.Object{objectplanmodifier.UseStateForUnknown()},
		}
	}
	return rschema.SingleNestedAttribute{
		MarkdownDescription: quotaResourceDoc,
		Optional:            true,
		Computed:            true,
		Attributes: map[string]rschema.Attribute{
			"dev": envBlock("dev"), "uat": envBlock("uat"), "prod": envBlock("prod"),
		},
		PlanModifiers: []planmodifier.Object{objectplanmodifier.UseStateForUnknown()},
	}
}

// projectQuotaDataAttribute is the `k8s_quota` attribute of the ataila_project
// data source: everything computed, no `reason`.
func projectQuotaDataAttribute() dschema.Attribute {
	env := types.ObjectType{AttrTypes: quotaEnvTypes(false)}
	return dschema.SingleNestedAttribute{
		MarkdownDescription: quotaAttrDoc,
		Computed:            true,
		Attributes: map[string]dschema.Attribute{
			"dev":  dschema.SingleNestedAttribute{MarkdownDescription: quotaEnvDoc("dev"), Computed: true, Attributes: dataNestedAttrs(env.AttrTypes, quotaDoc, nil)},
			"uat":  dschema.SingleNestedAttribute{MarkdownDescription: quotaEnvDoc("uat"), Computed: true, Attributes: dataNestedAttrs(env.AttrTypes, quotaDoc, nil)},
			"prod": dschema.SingleNestedAttribute{MarkdownDescription: quotaEnvDoc("prod"), Computed: true, Attributes: dataNestedAttrs(env.AttrTypes, quotaDoc, nil)},
		},
	}
}

// quotaCountKeys are the keys the platform stores as whole numbers, without
// leading zeros (the compiler's int() of the value).
var quotaCountKeys = map[string]bool{"pvc": true, "pods": true, "gpu_exclusive": true, "gpu_shared": true,
	"gpu_borrow": true}

// quotaSame reports whether two spellings of a key's value are one value to
// the platform: equal once trimmed (the platform strips), or the same whole
// number for a count key ("040" and "40").
func quotaSame(key, a, b string) bool {
	a, b = strings.TrimSpace(a), strings.TrimSpace(b)
	if a == b {
		return true
	}
	if !quotaCountKeys[key] || !allDigits(a) || !allDigits(b) {
		return false
	}
	return noLeadingZeros(a) == noLeadingZeros(b)
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func noLeadingZeros(s string) string {
	if t := strings.TrimLeft(s, "0"); t != "" {
		return t
	}
	return "0"
}

// quotaEnvValue is one environment's block from the API's entry. prior is the
// block the plan or state had: a key it spells differently from the platform
// but means the same keeps its spelling (Terraform requires the configured
// value back), and its `reason` is kept (the platform does not report it).
// nil prior: the platform's spelling, and no reason.
func quotaEnvValue(entry map[string]any, prior map[string]attr.Value, withReason bool) types.Object {
	t := quotaEnvTypes(withReason)
	v := map[string]attr.Value{}
	for _, k := range []string{"namespace", "cluster", "tier"} {
		s, _ := entry[k].(string)
		v[k] = nullIfEmpty(s)
	}
	for _, k := range []string{"gpu_queue", "gpu_enabled", "overridden"} {
		b, _ := entry[k].(bool)
		v[k] = types.BoolValue(b)
	}
	keys, _ := entry["overridden_keys"].([]any)
	elems := make([]attr.Value, 0, len(keys))
	for _, k := range keys {
		if s, ok := k.(string); ok {
			elems = append(elems, types.StringValue(s))
		}
	}
	v["overridden_keys"] = types.ListValueMust(types.StringType, elems)
	for _, k := range quotaKeys {
		s, _ := entry[k].(string)
		v[k] = nullIfEmpty(s)
		if was, ok := knownString(prior[k]); ok && s != "" && quotaSame(k, was, s) {
			v[k] = types.StringValue(was)
		}
	}
	if withReason {
		v["reason"] = types.StringNull()
		if r, ok := prior["reason"].(types.String); ok && !r.IsUnknown() {
			v["reason"] = r
		}
	}
	return types.ObjectValueMust(t, v)
}

// quotaValue is the `k8s_quota` attribute from a project answer: null when
// the project has none (a VM project, or a platform that does not serve it).
// prior is the attribute the plan or state held, for the reasons it keeps.
func quotaValue(p client.ProjectData, prior attr.Value, withReason bool) attr.Value {
	t := quotaType(withReason)
	raw, _ := p["k8s_quota"].(map[string]any)
	if raw == nil {
		return types.ObjectNull(t.AttrTypes)
	}
	priorEnvs := map[string]map[string]attr.Value{}
	if po, ok := prior.(types.Object); ok && !po.IsNull() && !po.IsUnknown() {
		for env, ev := range po.Attributes() {
			if eo, ok := ev.(types.Object); ok && !eo.IsNull() && !eo.IsUnknown() {
				priorEnvs[env] = eo.Attributes()
			}
		}
	}
	v := map[string]attr.Value{}
	for _, env := range quotaEnvs {
		entry, _ := raw[env].(map[string]any)
		if entry == nil {
			v[env] = types.ObjectNull(quotaEnvTypes(withReason))
			continue
		}
		v[env] = quotaEnvValue(entry, priorEnvs[env], withReason)
	}
	return types.ObjectValueMust(t.AttrTypes, v)
}

// quotaEnvAttrs are one environment's attributes of a `k8s_quota` value, or
// nil when the value or the environment is null or unknown.
func quotaEnvAttrs(v attr.Value, env string) map[string]attr.Value {
	o, ok := v.(types.Object)
	if !ok || o.IsNull() || o.IsUnknown() {
		return nil
	}
	e, ok := o.Attributes()[env].(types.Object)
	if !ok || e.IsNull() || e.IsUnknown() {
		return nil
	}
	return e.Attributes()
}

// knownString is a known, non-null string attribute's value.
func knownString(v attr.Value) (string, bool) {
	s, ok := v.(types.String)
	if !ok || s.IsNull() || s.IsUnknown() {
		return "", false
	}
	return s.ValueString(), true
}

// quotaPatches are the requests the plan needs, per environment: every quota
// key whose planned value is known and means something other than the state's
// (a respelling is no change), with the environment's reason. A new reason
// alone needs a request only where the environment has an override to carry
// it: its overridden keys are re-sent unchanged with it, which the platform
// stores as the same values with the new reason (the manifest does not change,
// so no provisioning walk starts). Empty when the plan needs none.
func quotaPatches(plan, state attr.Value) map[string]client.Patch {
	out := map[string]client.Patch{}
	for _, env := range quotaEnvs {
		pe := quotaEnvAttrs(plan, env)
		if pe == nil {
			continue
		}
		se := quotaEnvAttrs(state, env)
		patch := client.Patch{}
		for _, k := range quotaKeys {
			want, ok := knownString(pe[k])
			if !ok {
				continue
			}
			if have, ok := knownString(se[k]); ok && quotaSame(k, want, have) {
				continue
			}
			patch[k] = want
		}
		reason, given := knownString(pe["reason"])
		given = given && strings.TrimSpace(reason) != ""
		if len(patch) == 0 {
			if !given || !quotaReasonChanged(pe, se) {
				continue
			}
			for _, k := range quotaOverriddenKeys(se) {
				if have, ok := knownString(se[k]); ok {
					patch[k] = have
				}
			}
			if len(patch) == 0 {
				continue // no override to carry it: the state alone records the reason
			}
		}
		if !given {
			reason = defaultQuotaReason
		}
		patch["reason"] = reason
		out[env] = patch
	}
	return out
}

// quotaChanged reports whether the plan needs a quota request.
func quotaChanged(plan, state attr.Value) bool { return len(quotaPatches(plan, state)) > 0 }

// quotaReasonChanged reports whether an environment's planned reason is known
// and differs from the state's (null included).
func quotaReasonChanged(plan, state map[string]attr.Value) bool {
	p, ok := plan["reason"].(types.String)
	if !ok || p.IsUnknown() {
		return false
	}
	s, _ := state["reason"].(types.String)
	return !p.Equal(s)
}

// quotaOverriddenKeys are the keys an environment's state lists as overridden.
func quotaOverriddenKeys(env map[string]attr.Value) []string {
	l, ok := env["overridden_keys"].(types.List)
	if !ok || l.IsNull() || l.IsUnknown() {
		return nil
	}
	var keys []string
	for _, e := range l.Elements() {
		if s, ok := knownString(e); ok {
			keys = append(keys, s)
		}
	}
	return keys
}

// quotaMissing reports a configuration that sets `k8s_quota` on a project
// whose state has none: the platform does not report one for it (a release
// older than k8s_quota, or a project without a manifest), so no request can
// succeed and the plan could not be kept.
func quotaMissing(plan, state attr.Value) bool {
	p, ok := plan.(types.Object)
	if !ok || p.IsNull() || p.IsUnknown() {
		return false
	}
	s, ok := state.(types.Object)
	return !ok || s.IsNull()
}

// quotaPlanned is the state's `k8s_quota` with the plan's known keys and
// reasons laid over it: the plan of a project whose quota needs no request
// but whose configuration spells a value differently or gives a reason the
// platform has nowhere to record. The plan must keep a configured value as
// written; the platform-reported members stay as the state has them.
func quotaPlanned(plan, state attr.Value) attr.Value {
	so, ok := state.(types.Object)
	if !ok || so.IsNull() || so.IsUnknown() {
		return state
	}
	envs := map[string]attr.Value{}
	for env, sv := range so.Attributes() {
		envs[env] = sv
		pe, se := quotaEnvAttrs(plan, env), quotaEnvAttrs(state, env)
		if pe == nil || se == nil {
			continue
		}
		a := make(map[string]attr.Value, len(se))
		for k, v := range se {
			a[k] = v
		}
		for _, k := range append(append([]string{}, quotaKeys...), "reason") {
			if v, ok := pe[k].(types.String); ok && !v.IsUnknown() {
				a[k] = v
			}
		}
		envs[env] = types.ObjectValueMust(sv.(types.Object).AttributeTypes(context.Background()), a)
	}
	return types.ObjectValueMust(so.AttributeTypes(context.Background()), envs)
}

// quotaPrior is the `k8s_quota` the new state is read against: per
// environment the plan's block, except where its request was needed and did
// not go through (unapplied): there the state's block, as the platform kept
// that environment as it was, reason included (no state: none, so the
// platform's values and no reason). An unknown reason is settled to null: the
// state records the reason the configuration gave, and null where it gave
// none (the default text the platform was sent is not pretended to be
// configured, so a later plan without a reason does not differ from it).
func quotaPrior(plan, state attr.Value, unapplied map[string]bool) attr.Value {
	po, ok := plan.(types.Object)
	if !ok || po.IsNull() || po.IsUnknown() {
		return plan
	}
	envs := map[string]attr.Value{}
	for env, ev := range po.Attributes() {
		envs[env] = ev
		if unapplied[env] {
			if so, ok := state.(types.Object); ok && !so.IsNull() && !so.IsUnknown() {
				envs[env] = so.Attributes()[env]
			} else {
				envs[env] = types.ObjectUnknown(ev.(types.Object).AttributeTypes(context.Background()))
			}
			continue
		}
		eo, ok := ev.(types.Object)
		if !ok || eo.IsNull() || eo.IsUnknown() {
			continue
		}
		a := map[string]attr.Value{}
		for k, v := range eo.Attributes() {
			a[k] = v
		}
		if r, ok := a["reason"].(types.String); ok && r.IsUnknown() {
			a["reason"] = types.StringNull()
		}
		envs[env] = types.ObjectValueMust(eo.AttributeTypes(context.Background()), a)
	}
	return types.ObjectValueMust(po.AttributeTypes(context.Background()), envs)
}

// quotaUnapplied are the environments whose request was needed but did not go
// through: the refused one and every one after it.
func quotaUnapplied(patches map[string]client.Patch, done map[string]client.ProjectData) map[string]bool {
	out := map[string]bool{}
	for env := range patches {
		if _, ok := done[env]; !ok {
			out[env] = true
		}
	}
	return out
}

// withQuotaAnswers is the project answer p with each environment a quota
// request went through for replaced by the platform's answer to that request
// (the same members, the environment's new effective quota): what the state
// records when reading the project back after the requests fails. p itself is
// not modified.
func withQuotaAnswers(p client.ProjectData, done map[string]client.ProjectData) client.ProjectData {
	raw, _ := p["k8s_quota"].(map[string]any)
	if raw == nil || len(done) == 0 {
		return p
	}
	quota := make(map[string]any, len(raw))
	for env, v := range raw {
		quota[env] = v
	}
	for env, answer := range done {
		quota[env] = map[string]any(answer)
	}
	out := make(client.ProjectData, len(p))
	for k, v := range p {
		out[k] = v
	}
	out["k8s_quota"] = quota
	return out
}

// applyQuota sends the quota requests the plan needs, one PATCH per
// environment in the platform's order, and explains a refusal. It stops at the
// first refusal and returns the API's warnings and, per environment whose
// request went through, the platform's answer to it.
func applyQuota(ctx context.Context, api *client.API, id, ident string, patches map[string]client.Patch,
	diags *diag.Diagnostics) ([]client.ApiWarning, map[string]client.ProjectData, bool) {
	var warnings []client.ApiWarning
	done := map[string]client.ProjectData{}
	for _, env := range quotaEnvs {
		patch, ok := patches[env]
		if !ok {
			continue
		}
		doing := fmt.Sprintf("changing the %s namespace quota of the project %s", env, ident)
		p, err := api.UpdateProjectQuota(ctx, id, env, patch)
		if err != nil {
			diags.Append(quotaWriteError(doing, ident, env, err))
			return warnings, done, false
		}
		done[env] = p
		_, ws := projectWarnings(p)
		addWarnings(diags, doing, ws)
		warnings = append(warnings, *ws...)
		switch status := p.String("dispatch_status"); status {
		case "partial", "not_provisioned":
			diags.AddWarning("The quota is saved but not yet applied on the cluster",
				fmt.Sprintf("While %s: the platform answered dispatch_status %q. The quota is stored; it reaches the "+
					"cluster with the next full provisioning run (ataila_project_provisioning).", doing, status))
		case "busy", "already_running":
			diags.AddWarning("The quota is saved; a provisioning run already in progress will not apply it",
				fmt.Sprintf("While %s: the platform answered dispatch_status %q. The stages it needs are marked stale; "+
					"they are applied by the next provisioning run once the current one ends.", doing, status))
		}
	}
	return warnings, done, true
}

// quotaWriteError explains the refusals a quota change can meet.
func quotaWriteError(doing, ident, env string, err error) diag.Diagnostic {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		return apiError(doing, err)
	}
	switch apiErr.Code() {
	case client.CodePlatformReadOnly:
		return platformProjectRefused(ident, "change")
	case client.CodeVMProjectsUnsupported:
		return diag.NewErrorDiagnostic("This project has no namespace quota",
			fmt.Sprintf("The project %s has no namespace quota: k8s_quota applies to deployment_backend = \"k8s\" "+
				"projects with a compiled manifest. Remove k8s_quota from its configuration.\n\n%s", ident, apiErr.Detail()))
	case client.CodeQuotaRefused:
		return diag.NewErrorDiagnostic(fmt.Sprintf("The platform refused the %s quota", env),
			fmt.Sprintf("While %s, the platform's quota rules refused a value: a malformed quantity, a GPU key "+
				"above 0 on a cluster that runs no GPU queue (k8s_quota.%s.gpu_queue is false), or gpu_shared above "+
				"0 where no time-sliced node exists. Nothing was stored.\n\n%s", doing, env, apiErr.Detail()))
	case client.CodeQuotaConflict:
		return diag.NewErrorDiagnostic(fmt.Sprintf("The %s quota conflicts with the cluster's capacity", env),
			fmt.Sprintf("While %s, the platform refused the change: the guaranteed GPU floors of the namespaces on "+
				"the cluster would exceed its tenant cards, gpu_shared would rise while no cluster offers "+
				"time-sliced units, a retired project asked for more than a release, or the project changed "+
				"meanwhile (plan again). Nothing was stored.\n\n%s", doing, apiErr.Detail()))
	}
	// A route this platform does not have: 404 with the generic code (a
	// missing project is project_not_found), or 405.
	if (apiErr.StatusCode == 404 && (apiErr.Code() == "" || apiErr.Code() == "not_found")) || apiErr.StatusCode == 405 {
		return diag.NewErrorDiagnostic("This platform does not serve the namespace quota",
			fmt.Sprintf("While %s: the platform answered HTTP %d. k8s_quota needs a platform release whose API "+
				"reports k8s_quota on the project; read k8s_quota from the ataila_project data source to see "+
				"whether this platform does (it is null when not).\n\n%s",
				doing, apiErr.StatusCode, apiErr.Detail()))
	}
	return apiError(doing, err)
}
