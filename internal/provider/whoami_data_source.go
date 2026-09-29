// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ datasource.DataSourceWithConfigure = (*whoamiDataSource)(nil)

// NewWhoamiDataSource is the factory for ataila_whoami.
func NewWhoamiDataSource() datasource.DataSource { return &whoamiDataSource{} }

type whoamiDataSource struct {
	data *ProviderData
}

type whoamiModel struct {
	Principal types.Object `tfsdk:"principal"`
	AuthKind  types.String `tfsdk:"auth_kind"`
	Scopes    types.List   `tfsdk:"scopes"`
	ExpiresAt types.String `tfsdk:"expires_at"`
	Token     types.Object `tfsdk:"token"`
}

type principalModel struct {
	ID    types.String `tfsdk:"id"`
	Email types.String `tfsdk:"email"`
	Name  types.String `tfsdk:"name"`
	Kind  types.String `tfsdk:"kind"`
}

type tokenModel struct {
	ID            types.String `tfsdk:"id"`
	Name          types.String `tfsdk:"name"`
	Prefix        types.String `tfsdk:"prefix"`
	GrantedScopes types.List   `tfsdk:"granted_scopes"`
	AllowDestroy  types.Bool   `tfsdk:"allow_destroy"`
}

var principalAttrTypes = map[string]attr.Type{
	"id":    types.StringType,
	"email": types.StringType,
	"name":  types.StringType,
	"kind":  types.StringType,
}

var tokenAttrTypes = map[string]attr.Type{
	"id":             types.StringType,
	"name":           types.StringType,
	"prefix":         types.StringType,
	"granted_scopes": types.ListType{ElemType: types.StringType},
	"allow_destroy":  types.BoolType,
}

func (d *whoamiDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_whoami"
}

func (d *whoamiDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Who the provider is acting as: the principal behind the token, how it " +
			"authenticated, what it may do right now and when its credential expires.",
		Attributes: map[string]schema.Attribute{
			"principal": schema.SingleNestedAttribute{
				MarkdownDescription: "The calling principal.",
				Computed:            true,
				Attributes: map[string]schema.Attribute{
					"id": schema.StringAttribute{
						MarkdownDescription: "User id.",
						Computed:            true,
					},
					"email": schema.StringAttribute{
						MarkdownDescription: "E-mail address; a service account has a synthetic one.",
						Computed:            true,
					},
					"name": schema.StringAttribute{
						MarkdownDescription: "Display name.",
						Computed:            true,
					},
					"kind": schema.StringAttribute{
						MarkdownDescription: "`human` or `service`.",
						Computed:            true,
					},
				},
			},
			"auth_kind": schema.StringAttribute{
				MarkdownDescription: "How the caller authenticated: `pat` (personal token), " +
					"`service_account` or `session`.",
				Computed: true,
			},
			"scopes": schema.ListAttribute{
				MarkdownDescription: "Effective permission keys: for a token, the scopes it was minted with " +
					"that the principal still holds right now.",
				ElementType: types.StringType,
				Computed:    true,
			},
			"expires_at": schema.StringAttribute{
				MarkdownDescription: "When the credential expires (RFC 3339), when the platform reports it.",
				Computed:            true,
			},
			"token": schema.SingleNestedAttribute{
				MarkdownDescription: "The API token in use; null for a portal session.",
				Computed:            true,
				Attributes: map[string]schema.Attribute{
					"id": schema.StringAttribute{
						MarkdownDescription: "Token id.",
						Computed:            true,
					},
					"name": schema.StringAttribute{
						MarkdownDescription: "Token name.",
						Computed:            true,
					},
					"prefix": schema.StringAttribute{
						MarkdownDescription: "The token's public prefix, as the portal shows it.",
						Computed:            true,
					},
					"granted_scopes": schema.ListAttribute{
						MarkdownDescription: "Scopes the token was minted with.",
						ElementType:         types.StringType,
						Computed:            true,
					},
					"allow_destroy": schema.BoolAttribute{
						MarkdownDescription: "Whether the token was minted with destroy allowed.",
						Computed:            true,
					},
				},
			},
		},
	}
}

func (d *whoamiDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *whoamiDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.data == nil {
		resp.Diagnostics.AddError("Provider not configured", "The ataila provider was not configured.")
		return
	}
	who, err := d.data.API.Whoami(ctx)
	if err != nil {
		summary, detail := apiErrorText("the calling principal (GET /whoami)", err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}

	principal, diags := types.ObjectValueFrom(ctx, principalAttrTypes, principalModel{
		ID:    types.StringValue(who.Principal.Id),
		Email: types.StringValue(who.Principal.Email),
		Name:  types.StringValue(who.Principal.Name),
		Kind:  types.StringValue(who.Principal.Kind),
	})
	resp.Diagnostics.Append(diags...)

	token := types.ObjectNull(tokenAttrTypes)
	if who.Token != nil {
		token, diags = types.ObjectValueFrom(ctx, tokenAttrTypes, tokenModel{
			ID:            types.StringValue(who.Token.Id),
			Name:          types.StringValue(who.Token.Name),
			Prefix:        types.StringValue(who.Token.Prefix),
			GrantedScopes: stringList(ctx, who.Token.GrantedScopes, &resp.Diagnostics),
			AllowDestroy:  types.BoolValue(who.Token.AllowDestroy),
		})
		resp.Diagnostics.Append(diags...)
	}

	state := whoamiModel{
		Principal: principal,
		AuthKind:  types.StringValue(who.AuthKind),
		Scopes:    stringList(ctx, who.Scopes, &resp.Diagnostics),
		ExpiresAt: stringOrNull(who.ExpiresAt),
		Token:     token,
	}
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
