// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/ataila/terraform-provider-ataila/internal/client"
)

// A project has many settings of three kinds. They are described once, here,
// and the resource, the data source, the request bodies and the state are all
// built from this table.

type settingKind int

const (
	kindString settingKind = iota
	kindBool
	kindInt
)

type projectSetting struct {
	name     string
	kind     settingKind
	enum     []string // allowed strings
	ints     []int64  // allowed integers (nil: 0 to max)
	max      int64
	nullable bool // null clears it; Optional only
	doc      string
}

var projectSettings = []projectSetting{
	{name: "long_name", kind: kindString, doc: "Display name, 2-60 characters. Quotes, apostrophes, backslashes " +
		"and control characters are refused because the name is copied into generated project files."},
	{name: "description", kind: kindString, nullable: true, doc: "Free-text description, up to 2000 " +
		"characters. Leaving it out clears it."},
	{name: "frontend_variant", kind: kindString, enum: []string{"react", "angular", "vue", "nuxt4"},
		doc: "Front-end framework of the application skeleton. Platform default `react`."},
	{name: "has_mobile", kind: kindBool, doc: "Include a mobile application. Platform default `false`."},
	{name: "enable_static_site", kind: kindBool, doc: "A static web site. Platform default `true`; always " +
		"`false` for a `network_only` project."},
	{name: "enable_fullstack_app", kind: kindBool, doc: "A full-stack application. Platform default `true`; " +
		"always `false` for a `network_only` project."},
	{name: "frontend_exposure", kind: kindString, enum: []string{"INTERNAL_ONLY", "PUBLIC"},
		doc: "Who can reach the front end. Platform default `PUBLIC`."},
	{name: "api_exposure", kind: kindString, enum: []string{"INTERNAL_ONLY", "PUBLIC"},
		doc: "Who can reach the API. Platform default `INTERNAL_ONLY`."},
	{name: "app_gateway", kind: kindString, enum: []string{"shared", "dedicated"},
		doc: "`shared` uses the environment's application gateway, `dedicated` gets its own. Platform " +
			"default `shared`. A read may also return `ataila`, the platform's own project's value (platform " +
			"1.0.206 and later); it cannot be set."},
	{name: "enable_ai", kind: kindBool, doc: "An AI endpoint for the project. Platform default `false`."},
	{name: "enable_web_www", kind: kindBool, doc: "A `www` web site repository. Platform default `true`."},
	{name: "www_template", kind: kindString, enum: []string{"template-www", "template-blog"},
		doc: "Template of the web site. Platform default `template-www`."},
	{name: "enable_uat_app_public", kind: kindBool, doc: "Publish the UAT application. Platform default `false`."},
	{name: "enable_uat_www_public", kind: kindBool, doc: "Publish the UAT web site. Platform default `false`."},
	{name: "enable_object_storage", kind: kindBool, doc: "Object storage. Platform default `true`."},
	{name: "enable_cache", kind: kindBool, doc: "A cache. Platform default `false`."},
	{name: "enable_dr_db_replica", kind: kindBool, doc: "A disaster-recovery database replica. Platform " +
		"default `true`."},
	{name: "prod_object_storage_node_count", kind: kindInt, ints: []int64{2, 4}, doc: "Object storage nodes in " +
		"production: 2 or 4. Platform default 2."},
	{name: "prod_object_storage_disks_per_vm", kind: kindInt, ints: []int64{1, 2}, doc: "Object storage disks per " +
		"node in production: 1 or 2. Platform default 2."},
	{name: "enable_dr_object_storage_mirror", kind: kindBool, doc: "Mirror object storage to the disaster-recovery " +
		"site. Platform default `true`."},
	{name: "enable_nas_object_storage_replication", kind: kindBool, doc: "Replicate object storage to the " +
		"backup NAS. Platform default `false`."},
	{name: "enable_mssql", kind: kindBool, doc: "SQL Server. Needs `deployment_backend = \"vm\"`. Platform " +
		"default `false`."},
	{name: "mssql_edition", kind: kindString, enum: []string{"express", "standard", "enterprise"},
		doc: "SQL Server edition. Platform default `express`."},
	{name: "enable_iis", kind: kindBool, doc: "IIS/.NET hosting. Needs `deployment_backend = \"vm\"`. " +
		"Platform default `false`."},
	{name: "windows_vm_count_prod", kind: kindInt, max: 10, doc: "Windows machines in production, 0-10. " +
		"More than 0 needs `deployment_backend = \"vm\"`. Platform default 0."},
	{name: "windows_vm_count_uat", kind: kindInt, max: 10, doc: "Windows machines in UAT, 0-10. More than " +
		"0 needs `deployment_backend = \"vm\"`. Platform default 0."},
	{name: "windows_vm_count_dev", kind: kindInt, max: 10, doc: "Windows machines in development, 0-10. " +
		"More than 0 needs `deployment_backend = \"vm\"`. Platform default 0."},
	{name: "github_user", kind: kindString, nullable: true, doc: "A GitHub user linked to the project. " +
		"Leaving it out clears it."},
	{name: "github_repo_url", kind: kindString, nullable: true, doc: "A GitHub repository linked to the " +
		"project. Leaving it out clears it."},
	{name: "import_existing_repo", kind: kindBool, doc: "The GitLab repository already holds code: " +
		"provisioning does not seed it from the template. Platform default `false`."},
	{name: "allow_public_https_egress", kind: kindBool, doc: "Kubernetes projects: allow outbound HTTPS " +
		"to the internet. Platform default `false`."},
}

var (
	rxProjectShort = regexp.MustCompile(`^[a-z][a-z0-9]{1,10}$`)
	rxRepoSlug     = regexp.MustCompile(`^[a-z][a-z0-9-]{1,40}$`)
	rxFQDN         = regexp.MustCompile(`^([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}$`)
	// An IPv4 literal anywhere in a string.
	rxIPv4 = regexp.MustCompile(`(^|[^0-9.])([0-9]{1,3}\.){3}[0-9]{1,3}($|[^0-9.])`)
)

// Documentation of the attributes that are not settings, shared by the
// resource and the data sources.
var projectDocs = map[string]string{
	"id":        "Project id, assigned by the platform.",
	"tenant_id": "Id of the tenant that owns the project. **Frozen.**",
	"customer_id": "Id of the customer of the owning tenant (null for a tenant without a customer, which " +
		"only the platform's own projects have).",
	"project_index": "The project's index, 1-99, unique on the platform; it numbers the project's " +
		"networks. Omit it and the platform allocates one above the highest in use (never below 4). **Frozen.**",
	"short_name": "Short name, `^[a-z][a-z0-9]{1,10}$` (2-11 characters), unique on the platform. **Frozen.**",
	"gitlab_repo_slug": "Name of the application repository in the customer's GitLab group, " +
		"`^[a-z][a-z0-9-]{1,40}$`, unique within the customer. **Frozen.**",
	"primary_domain": "The project's primary domain, a fully qualified domain name, unique on the platform. " +
		"The platform stores it in lower case; the provider keeps the spelling of the configuration. **Frozen.**",
	"deployment_backend": "`k8s` (the platform's default) or `vm`. **Frozen.**",
	"network_only": "Register the network zone only: no application and no web site. Forces " +
		"`enable_static_site` and `enable_fullstack_app` off. Platform default `false`. **Frozen.**",
	"status": "`planned` until provisioning starts, then `provisioning`, `active` once every stage is " +
		"done, `paused`, or `retired`. Read-only.",
	"is_self":       "One of the platform's own projects: readable, never writable through the API.",
	"registered_by": "Id of the user or service account that created the project, when known.",
	"created_at":    "When the project was created: RFC 3339 in UTC, compared as an instant.",
	"stale_stages": "Provisioning stages done against an older version of the project's settings, in " +
		"apply order. The next provisioning run (`ataila_project_provisioning`) re-applies exactly these.",
	"urls":                     "The project's public addresses, each null when the project has none: `static` (the web site), `frontend` (the application), `backend` (its API) and `ai` (its AI endpoint).",
	"image_registry_namespace": "The project's container registry namespace.",
	"gitlab_repositories": "The project's GitLab repositories: `path` (`<group>/<repository>`), `kind` " +
		"(`app` or `www`) and `primary`.",
	"kubernetes_namespaces": "The project's Kubernetes namespaces, by `env`.",
	"secret_paths": "Where the project's development and UAT secrets live in the secrets store, by `env` (`dev` or " +
		"`uat`). Production paths are never listed, nor the paths of a `vm` backend project.",
	"warnings": "What did not go as planned in the last change made through the provider (for example " +
		"stale stages that could not be recorded). Empty when everything went as planned.",
	"k8s_quota": quotaAttrDoc,
}

var (
	projectURLTypes    = map[string]attr.Type{"static": types.StringType, "frontend": types.StringType, "backend": types.StringType, "ai": types.StringType}
	projectRepoTypes   = map[string]attr.Type{"path": types.StringType, "kind": types.StringType, "primary": types.BoolType}
	projectNSTypes     = map[string]attr.Type{"env": types.StringType, "namespace": types.StringType}
	projectSecretTypes = map[string]attr.Type{"env": types.StringType, "path": types.StringType}
)

// projectValues turns an API project into attribute values. prior holds the
// values the configuration or state had (for spellings kept as configured);
// stale is the provisioning's stale stages (nil: none known).
func projectValues(p client.ProjectData, prior map[string]attr.Value, stale []string) map[string]attr.Value {
	v := map[string]attr.Value{}
	for _, s := range projectSettings {
		raw := p[s.name]
		switch s.kind {
		case kindString:
			if str, ok := raw.(string); ok {
				v[s.name] = types.StringValue(str)
			} else {
				v[s.name] = types.StringNull()
			}
		case kindBool:
			b, _ := raw.(bool)
			v[s.name] = types.BoolValue(b)
		case kindInt:
			f, _ := raw.(float64)
			v[s.name] = types.Int64Value(int64(f))
		}
	}
	keepFold := func(name string) {
		got := p.String(name)
		if pv, ok := prior[name].(types.String); ok && !pv.IsNull() && !pv.IsUnknown() &&
			strings.EqualFold(strings.TrimSpace(pv.ValueString()), got) {
			v[name] = pv
			return
		}
		v[name] = types.StringValue(got)
	}
	v["id"] = types.StringValue(p.String("id"))
	keepFold("tenant_id")
	keepFold("primary_domain")
	v["customer_id"] = nullIfEmpty(p.String("customer_id"))
	idx, _ := p["project_index"].(float64)
	v["project_index"] = types.Int64Value(int64(idx))
	v["short_name"] = types.StringValue(p.String("short_name"))
	v["gitlab_repo_slug"] = types.StringValue(p.String("gitlab_repo_slug"))
	v["deployment_backend"] = types.StringValue(p.String("deployment_backend"))
	v["network_only"] = types.BoolValue(p.Bool("network_only"))
	v["status"] = types.StringValue(p.String("status"))
	v["is_self"] = types.BoolValue(p.Bool("is_self"))
	v["registered_by"] = nullIfEmpty(p.String("registered_by"))
	v["created_at"] = timestampFromString(p.String("created_at"))

	if stale == nil {
		stale = []string{}
	}
	staleElems := make([]attr.Value, 0, len(stale))
	for _, s := range stale {
		staleElems = append(staleElems, types.StringValue(s))
	}
	v["stale_stages"] = types.ListValueMust(types.StringType, staleElems)

	outputs, _ := p["outputs"].(map[string]any)
	urls, _ := outputs["urls"].(map[string]any)
	if urls == nil {
		v["urls"] = types.ObjectNull(projectURLTypes)
	} else {
		u := map[string]attr.Value{}
		for k := range projectURLTypes {
			s, _ := urls[k].(string)
			u[k] = nullIfEmpty(s)
		}
		v["urls"] = types.ObjectValueMust(projectURLTypes, u)
	}
	hn, _ := outputs["image_registry_namespace"].(string)
	v["image_registry_namespace"] = nullIfEmpty(hn)
	v["gitlab_repositories"] = objectList(outputs["gitlab_repositories"], projectRepoTypes)
	v["kubernetes_namespaces"] = objectList(outputs["kubernetes_namespaces"], projectNSTypes)
	v["secret_paths"] = objectList(outputs["secret_paths"], projectSecretTypes)
	// The resource's shape (with `reason`); the data source replaces it with its own.
	v["k8s_quota"] = quotaValue(p, prior["k8s_quota"], true)
	return v
}

// objectList converts a JSON list of objects to a list attribute of the given
// object type (strings and booleans only).
func objectList(raw any, types_ map[string]attr.Type) types.List {
	list, _ := raw.([]any)
	elems := make([]attr.Value, 0, len(list))
	for _, item := range list {
		m, _ := item.(map[string]any)
		vals := map[string]attr.Value{}
		for k, t := range types_ {
			if t.Equal(types.BoolType) {
				b, _ := m[k].(bool)
				vals[k] = types.BoolValue(b)
				continue
			}
			s, _ := m[k].(string)
			vals[k] = nullIfEmpty(s)
		}
		elems = append(elems, types.ObjectValueMust(types_, vals))
	}
	return types.ListValueMust(types.ObjectType{AttrTypes: types_}, elems)
}

func nullIfEmpty(s string) types.String {
	if s == "" {
		return types.StringNull()
	}
	return types.StringValue(s)
}

func timestampFromString(s string) TimestampValue {
	if s == "" {
		return TimestampNull()
	}
	if t, err := ParseTimestamp(s); err == nil {
		return NewTimestamp(t)
	}
	return TimestampValue{StringValue: basetypes.NewStringValue(s)}
}

// projectWarnings is the warnings attribute from a project answer.
func projectWarnings(p client.ProjectData) (types.List, *[]client.ApiWarning) {
	list, _ := p["warnings"].([]any)
	ws := make([]client.ApiWarning, 0, len(list))
	for _, item := range list {
		m, _ := item.(map[string]any)
		code, _ := m["code"].(string)
		msg, _ := m["message"].(string)
		ws = append(ws, client.ApiWarning{Code: code, Message: msg})
	}
	return warningsValue(ws), &ws
}

// objectFrom builds an object of the given type from values, filling the
// attributes values lacks with nulls of their type.
func objectFrom(ctx context.Context, t basetypes.ObjectType, values map[string]attr.Value, diags *diag.Diagnostics) types.Object {
	full := map[string]attr.Value{}
	for name, at := range t.AttrTypes {
		if v, ok := values[name]; ok {
			full[name] = v
			continue
		}
		nv, err := nullOf(ctx, at)
		if err != nil {
			diags.AddError("Internal error", err.Error())
			return types.ObjectNull(t.AttrTypes)
		}
		full[name] = nv
	}
	obj, d := types.ObjectValue(t.AttrTypes, full)
	diags.Append(d...)
	return obj
}

func nullOf(ctx context.Context, t attr.Type) (attr.Value, error) {
	return t.ValueFromTerraform(ctx, tftypes.NewValue(t.TerraformType(ctx), nil))
}

// settingBodyValue is the JSON value of a setting's known, non-null value.
func settingBodyValue(s projectSetting, v attr.Value) (any, bool) {
	if v == nil || v.IsNull() || v.IsUnknown() {
		return nil, false
	}
	switch s.kind {
	case kindString:
		return v.(types.String).ValueString(), true
	case kindBool:
		return v.(types.Bool).ValueBool(), true
	default:
		return v.(types.Int64).ValueInt64(), true
	}
}

// hasIPv4 reports whether s holds an IPv4 literal.
func hasIPv4(s string) bool { return rxIPv4.MatchString(s) }
