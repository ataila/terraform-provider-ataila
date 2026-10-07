// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ataila/terraform-provider-ataila/internal/client"
)

var (
	_ datasource.DataSourceWithConfigure        = (*tenantDataSource)(nil)
	_ datasource.DataSourceWithConfigValidators = (*tenantDataSource)(nil)
	_ datasource.DataSourceWithConfigure        = (*tenantsDataSource)(nil)
)

// NewTenantDataSource is the factory for the ataila_tenant data source.
func NewTenantDataSource() datasource.DataSource { return &tenantDataSource{} }

// NewTenantsDataSource is the factory for the ataila_tenants data source.
func NewTenantsDataSource() datasource.DataSource { return &tenantsDataSource{} }

// tenantDataAttributes are the attributes of one tenant as the data sources
// return it; lookups replaces the ones a data source takes as input.
func tenantDataAttributes(lookups map[string]schema.StringAttribute) map[string]schema.Attribute {
	d := tenantDocs
	str := func(name string) schema.Attribute {
		if a, ok := lookups[name]; ok {
			return a
		}
		return schema.StringAttribute{MarkdownDescription: d[name], Computed: true}
	}
	return map[string]schema.Attribute{
		"id":                str("id"),
		"customer_id":       str("customer_id"),
		"slug":              str("slug"),
		"name":              str("name"),
		"description":       str("description"),
		"default_router_id": str("default_router_id"),
		"is_primary":        schema.BoolAttribute{MarkdownDescription: d["is_primary"], Computed: true},
		"project_count":     schema.Int64Attribute{MarkdownDescription: d["project_count"], Computed: true},
		"member_count":      schema.Int64Attribute{MarkdownDescription: d["member_count"], Computed: true},
		"created_at":        schema.StringAttribute{MarkdownDescription: d["created_at"], CustomType: TimestampType{}, Computed: true},
		"updated_at":        schema.StringAttribute{MarkdownDescription: d["updated_at"], CustomType: TimestampType{}, Computed: true},
	}
}

type tenantDataSource struct {
	data *ProviderData
}

func (d *tenantDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_tenant"
}

func (d *tenantDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "One tenant, looked up by exactly one of `id` or `slug`.",
		Attributes: tenantDataAttributes(map[string]schema.StringAttribute{
			"id": {
				MarkdownDescription: tenantDocs["id"] + " Set it to look the tenant up by id.",
				Optional:            true, Computed: true,
			},
			"slug": {
				MarkdownDescription: tenantDocs["slug"] + " Set it to look the tenant up by slug.",
				Optional:            true, Computed: true,
			},
		}),
	}
}

func (d *tenantDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("slug")),
	}
}

func (d *tenantDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *tenantDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.data == nil {
		resp.Diagnostics.AddError("Provider not configured", "The ataila provider was not configured.")
		return
	}
	var cfg tenantModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var t *client.Tenant
	if !cfg.ID.IsNull() {
		got, err := d.data.API.GetTenant(ctx, cfg.ID.ValueString())
		if err != nil {
			summary, detail := apiErrorText("the tenant with id "+cfg.ID.ValueString(), err)
			resp.Diagnostics.AddError(summary, detail)
			return
		}
		t = got
	} else {
		found, err := d.data.API.ListTenants(ctx, client.TenantFilter{Slug: cfg.Slug.ValueString()})
		if err != nil {
			summary, detail := apiErrorText("the tenants (GET /tenants)", err)
			resp.Diagnostics.AddError(summary, detail)
			return
		}
		if len(found) != 1 {
			resp.Diagnostics.AddError("Tenant not found",
				fmt.Sprintf("%d tenants have slug %q; exactly one is needed.", len(found), cfg.Slug.ValueString()))
			return
		}
		t = &found[0]
	}
	var state tenantModel
	state.fromAPI(t)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

type tenantsDataSource struct {
	data *ProviderData
}

type tenantsModel struct {
	CustomerID types.String  `tfsdk:"customer_id"`
	Tenants    []tenantModel `tfsdk:"tenants"`
}

func (d *tenantsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_tenants"
}

func (d *tenantsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Every tenant on the platform, or every tenant of one customer, in slug order. " +
			"The provider reads every page of the list.",
		Attributes: map[string]schema.Attribute{
			"customer_id": schema.StringAttribute{
				MarkdownDescription: "Only this customer's tenants. Omit it for all tenants, legacy tenants " +
					"without a customer included.",
				Optional:   true,
				Validators: []validator.String{stringvalidator.RegexMatches(rxIntID, "must be a customer id")},
			},
			"tenants": schema.ListNestedAttribute{
				MarkdownDescription: "The tenants, ordered by slug.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: tenantDataAttributes(nil),
				},
			},
		},
	}
}

func (d *tenantsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *tenantsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.data == nil {
		resp.Diagnostics.AddError("Provider not configured", "The ataila provider was not configured.")
		return
	}
	var cfg tenantsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	found, err := d.data.API.ListTenants(ctx, client.TenantFilter{CustomerID: cfg.CustomerID.ValueString()})
	if err != nil {
		summary, detail := apiErrorText("the tenants (GET /tenants)", err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	state := tenantsModel{CustomerID: cfg.CustomerID, Tenants: make([]tenantModel, len(found))}
	for i := range found {
		state.Tenants[i].fromAPI(&found[i])
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
