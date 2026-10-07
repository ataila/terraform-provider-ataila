// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ datasource.DataSourceWithConfigure = (*metaDataSource)(nil)

// NewMetaDataSource is the factory for ataila_meta.
func NewMetaDataSource() datasource.DataSource { return &metaDataSource{} }

type metaDataSource struct {
	data *ProviderData
}

type metaModel struct {
	APIVersion      types.String  `tfsdk:"api_version"`
	PlatformVersion types.String  `tfsdk:"platform_version"`
	Tier            types.String  `tfsdk:"tier"`
	TenancyMode     types.String  `tfsdk:"tenancy_mode"`
	Modules         types.List    `tfsdk:"modules"`
	Licence         types.Object  `tfsdk:"licence"`
	DispatchMode    types.String  `tfsdk:"dispatch_mode_effective"`
	SimulateSeconds types.Float64 `tfsdk:"simulate_stage_seconds"`
}

type licenceModel struct {
	State         types.String `tfsdk:"state"`
	StateReason   types.String `tfsdk:"state_reason"`
	DaysRemaining types.Int64  `tfsdk:"days_remaining"`
}

var licenceAttrTypes = map[string]attr.Type{
	"state":          types.StringType,
	"state_reason":   types.StringType,
	"days_remaining": types.Int64Type,
}

func (d *metaDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_meta"
}

func (d *metaDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "What the platform behind the endpoint is: its API version, its build, " +
			"its licence tier and state, the modules the licence includes, and what pipeline dispatch does " +
			"there.",
		Attributes: map[string]schema.Attribute{
			"api_version": schema.StringAttribute{
				MarkdownDescription: "Semantic version of the API, for example `1.0.0`.",
				Computed:            true,
			},
			"platform_version": schema.StringAttribute{
				MarkdownDescription: "Version of the platform build serving the API.",
				Computed:            true,
			},
			"tier": schema.StringAttribute{
				MarkdownDescription: "Product tier of the installed licence; empty when unlicensed.",
				Computed:            true,
			},
			"tenancy_mode": schema.StringAttribute{
				MarkdownDescription: "`single` or `multi`; empty when unlicensed.",
				Computed:            true,
			},
			"modules": schema.ListAttribute{
				MarkdownDescription: "Modules the licence includes.",
				ElementType:         types.StringType,
				Computed:            true,
			},
			"dispatch_mode_effective": schema.StringAttribute{
				MarkdownDescription: "What pipeline dispatch does on this platform: `live` (work runs), `dryrun` " +
					"(nothing is executed; provisioning and releases never complete) or `simulate` (provisioning " +
					"stages are marked done without running; every other dispatch behaves as `dryrun`).",
				Computed: true,
			},
			"simulate_stage_seconds": schema.Float64Attribute{
				MarkdownDescription: "Under `simulate`, how long a simulated provisioning stage takes; null otherwise.",
				Computed:            true,
			},
			"licence": schema.SingleNestedAttribute{
				MarkdownDescription: "State of the platform's licence.",
				Computed:            true,
				Attributes: map[string]schema.Attribute{
					"state": schema.StringAttribute{
						MarkdownDescription: "Licence state, for example `ACTIVE` or `UNLICENSED`.",
						Computed:            true,
					},
					"state_reason": schema.StringAttribute{
						MarkdownDescription: "Why the licence is in this state, when the platform says.",
						Computed:            true,
					},
					"days_remaining": schema.Int64Attribute{
						MarkdownDescription: "Days until the licence expires; null when it does not apply.",
						Computed:            true,
					},
				},
			},
		},
	}
}

func (d *metaDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *metaDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.data == nil {
		resp.Diagnostics.AddError("Provider not configured", "The ataila provider was not configured.")
		return
	}
	meta, err := d.data.API.Meta(ctx)
	if err != nil {
		summary, detail := apiErrorText("the platform facts (GET /meta)", err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}

	lic := licenceModel{
		State:         types.StringValue(meta.Licence.State),
		StateReason:   stringOrEmpty(meta.Licence.StateReason),
		DaysRemaining: types.Int64Null(),
	}
	if meta.Licence.DaysRemaining != nil {
		lic.DaysRemaining = types.Int64Value(int64(*meta.Licence.DaysRemaining))
	}
	licence, diags := types.ObjectValueFrom(ctx, licenceAttrTypes, lic)
	resp.Diagnostics.Append(diags...)

	var modules []string
	if meta.Modules != nil {
		modules = *meta.Modules
	}
	state := metaModel{
		APIVersion:      types.StringValue(meta.ApiVersion),
		PlatformVersion: types.StringValue(meta.PlatformVersion),
		Tier:            stringOrEmpty(meta.Tier),
		TenancyMode:     stringOrEmpty(meta.TenancyMode),
		Modules:         stringList(ctx, modules, &resp.Diagnostics),
		Licence:         licence,
		DispatchMode:    types.StringValue(string(meta.DispatchModeEffective)),
		SimulateSeconds: types.Float64PointerValue(meta.SimulateStageSeconds),
	}
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
