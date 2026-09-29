// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ataila/terraform-provider-ataila/internal/client"
)

var (
	_ datasource.DataSourceWithConfigure        = (*customerDataSource)(nil)
	_ datasource.DataSourceWithConfigValidators = (*customerDataSource)(nil)
)

// NewCustomerDataSource is the factory for the ataila_customer data source.
func NewCustomerDataSource() datasource.DataSource { return &customerDataSource{} }

type customerDataSource struct {
	data *ProviderData
}

func (d *customerDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_customer"
}

func (d *customerDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	lookup := func(name, how string) schema.StringAttribute {
		return schema.StringAttribute{
			MarkdownDescription: customerDocs[name] + " " + how,
			Optional:            true,
			Computed:            true,
		}
	}
	computed := func(name string) schema.StringAttribute {
		return schema.StringAttribute{MarkdownDescription: customerDocs[name], Computed: true}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "One customer, looked up by exactly one of `id`, `short_name` or `gitlab_group`. " +
			"Archived customers are found too; check `status`.",
		Attributes: map[string]schema.Attribute{
			"id":           lookup("id", "Set it to look the customer up by id."),
			"short_name":   lookup("short_name", "Set it to look the customer up by short name."),
			"gitlab_group": lookup("gitlab_group", "Set it to look the customer up by GitLab group."),
			"customer_index": schema.Int64Attribute{
				MarkdownDescription: customerDocs["customer_index"], Computed: true,
			},
			"long_name":             computed("long_name"),
			"edition":               computed("edition"),
			"primary_contact_email": computed("primary_contact_email"),
			"primary_contact_name":  computed("primary_contact_name"),
			"default_email_tier": schema.Int64Attribute{
				MarkdownDescription: customerDocs["default_email_tier"], Computed: true,
			},
			"billing_tier": computed("billing_tier"),
			"status": schema.StringAttribute{
				MarkdownDescription: "`active`, `suspended` or `archived`.", Computed: true,
			},
			"notes":             computed("notes"),
			"primary_tenant_id": computed("primary_tenant_id"),
			"created_at":        computed("created_at"),
		},
	}
}

func (d *customerDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("short_name"), path.MatchRoot("gitlab_group")),
	}
}

func (d *customerDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *customerDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.data == nil {
		resp.Diagnostics.AddError("Provider not configured", "The ataila provider was not configured.")
		return
	}
	var cfg customerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var c *client.Customer
	switch {
	case !cfg.ID.IsNull():
		got, err := d.data.API.GetCustomer(ctx, cfg.ID.ValueString())
		if err != nil {
			summary, detail := apiErrorText("the customer with id "+cfg.ID.ValueString(), err)
			resp.Diagnostics.AddError(summary, detail)
			return
		}
		c = got
	default:
		filter, what := client.CustomerFilter{ShortName: cfg.ShortName.ValueString()}, "short_name"
		value := cfg.ShortName.ValueString()
		if !cfg.GitlabGroup.IsNull() {
			filter, what, value = client.CustomerFilter{GitlabGroup: cfg.GitlabGroup.ValueString()}, "gitlab_group",
				cfg.GitlabGroup.ValueString()
		}
		found, err := d.data.API.ListCustomers(ctx, filter)
		if err != nil {
			summary, detail := apiErrorText("the customers (GET /customers)", err)
			resp.Diagnostics.AddError(summary, detail)
			return
		}
		if len(found) != 1 {
			resp.Diagnostics.AddError("Customer not found",
				fmt.Sprintf("%d customers have %s %q; exactly one is needed.", len(found), what, value))
			return
		}
		c = &found[0]
	}

	var state customerModel
	state.fromAPI(c, types.StringNull())
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
