// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/resourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
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

	"github.com/ataila/terraform-provider-ataila/internal/client"
)

var (
	_ resource.ResourceWithConfigure        = (*brandResource)(nil)
	_ resource.ResourceWithImportState      = (*brandResource)(nil)
	_ resource.ResourceWithValidateConfig   = (*brandResource)(nil)
	_ resource.ResourceWithModifyPlan       = (*brandResource)(nil)
	_ resource.ResourceWithConfigure        = (*brandAssetResource)(nil)
	_ resource.ResourceWithModifyPlan       = (*brandAssetResource)(nil)
	_ resource.ResourceWithImportState      = (*brandAssetResource)(nil)
	_ resource.ResourceWithConfigValidators = (*brandAssetResource)(nil)
)

var rxHexColour = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// ── ataila_brand ─────────────────────────────────────────────────────────────

// NewBrandResource is the factory for ataila_brand.
func NewBrandResource() resource.Resource { return &brandResource{} }

type brandResource struct {
	data *ProviderData
}

type brandModel struct {
	ID                types.String   `tfsdk:"id"`
	ProductName       types.String   `tfsdk:"product_name"`
	ProductNameAccent types.String   `tfsdk:"product_name_accent"`
	BrandColor        types.String   `tfsdk:"brand_color"`
	PageTitle         types.String   `tfsdk:"page_title"`
	LogoSize          types.String   `tfsdk:"logo_size"`
	LogoOffsetX       types.Int64    `tfsdk:"logo_offset_x"`
	LogoAssetID       types.String   `tfsdk:"logo_asset_id"`
	FaviconAssetID    types.String   `tfsdk:"favicon_asset_id"`
	Version           types.Int64    `tfsdk:"version"`
	UpdatedAt         TimestampValue `tfsdk:"updated_at"`
}

// fromAPI fills the model; the colour keeps the configured spelling when it
// is the same colour, and asset ids their configured case.
func (m *brandModel) fromAPI(b *client.Brand) {
	m.ID = types.StringValue(SingletonImportID)
	m.ProductName = types.StringValue(b.ProductName)
	m.ProductNameAccent = types.StringValue(b.ProductNameAccent)
	m.BrandColor = keepFoldString(m.BrandColor, b.BrandColor)
	m.PageTitle = types.StringValue(b.PageTitle)
	m.LogoSize = types.StringValue(string(b.LogoSize))
	m.LogoOffsetX = types.Int64Value(int64(b.LogoOffsetX))
	m.LogoAssetID = keepFoldPtr(m.LogoAssetID, b.LogoAssetId)
	m.FaviconAssetID = keepFoldPtr(m.FaviconAssetID, b.FaviconAssetId)
	m.Version = types.Int64Value(int64(b.Version))
	m.UpdatedAt = NewTimestamp(b.UpdatedAt)
}

func keepFoldPtr(prior types.String, stored *string) types.String {
	if stored == nil {
		return types.StringNull()
	}
	return keepFoldString(prior, *stored)
}

func (m *brandModel) body() client.BrandPut {
	return client.BrandPut{
		ProductName:       m.ProductName.ValueString(),
		ProductNameAccent: m.ProductNameAccent.ValueString(),
		BrandColor:        m.BrandColor.ValueString(),
		PageTitle:         m.PageTitle.ValueString(),
		LogoSize:          client.BrandPutLogoSize(m.LogoSize.ValueString()),
		LogoOffsetX:       int(m.LogoOffsetX.ValueInt64()),
		LogoAssetId:       optString(m.LogoAssetID),
		FaviconAssetId:    optString(m.FaviconAssetID),
	}
}

func (r *brandResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_brand"
}

func (r *brandResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "The platform's brand (a singleton): the product name, colour, page title and " +
			"logo shown to every tenant and user, the sign-in page included.\n\n" +
			"The brand always exists: **create** replaces its fields with the configuration's, **update** does " +
			"the same, and **destroy only removes it from the state**. Every write names the brand's " +
			"`version` as last read (If-Match): when the brand changed outside the configuration after that " +
			"read, the platform refuses (412) and the provider reports it; run plan again. It never retries.\n\n" +
			"Fields left out take the defaults below, not the brand's current values: the resource manages " +
			"all of them. The attribution line and whether the portal is the vendor's own are read-only on " +
			"the platform and not attributes here. The sidebar tagline, the sign-in texts and the footer " +
			"links are not managed by the API and keep their values.\n\n" +
			"Needs the permission `brand-center-admin-global`. Import with the id `current`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Always `current`.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"product_name": schema.StringAttribute{
				MarkdownDescription: "The wordmark in the sidebar and on the sign-in page, 2-32 characters.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.LengthBetween(2, 32)},
			},
			"product_name_accent": schema.StringAttribute{
				MarkdownDescription: "A part of `product_name` shown in `brand_color`; empty (the default) " +
					"colours the whole name. At most 32 characters.",
				Optional: true, Computed: true, Default: stringdefault.StaticString(""),
				Validators: []validator.String{stringvalidator.LengthAtMost(32)},
			},
			"brand_color": schema.StringAttribute{
				MarkdownDescription: "Six-digit hex colour, for example `#2196f3`. The platform stores it in " +
					"lower case; the provider keeps the configuration's spelling.",
				Required:      true,
				Validators:    []validator.String{stringvalidator.RegexMatches(rxHexColour, "must be a six-digit hex colour such as #2196f3")},
				PlanModifiers: []planmodifier.String{sameFold{}},
			},
			"page_title": schema.StringAttribute{
				MarkdownDescription: "The browser-tab title, 1-60 characters.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 60)},
			},
			"logo_size": schema.StringAttribute{
				MarkdownDescription: "The sidebar logo size: `compact`, `medium`, `regular` (the default) or `large`.",
				Optional:            true, Computed: true, Default: stringdefault.StaticString("regular"),
				Validators: []validator.String{stringvalidator.OneOf("compact", "medium", "regular", "large")},
			},
			"logo_offset_x": schema.Int64Attribute{
				MarkdownDescription: "Horizontal nudge of the logo in pixels, -40 to 40; default 0.",
				Optional:            true, Computed: true, Default: int64default.StaticInt64(0),
				Validators: []validator.Int64{int64validator.Between(-40, 40)},
			},
			"logo_asset_id": schema.StringAttribute{
				MarkdownDescription: "A brand asset of kind `logo` (`ataila_brand_asset`); left out, the " +
					"built-in logo is shown.",
				Optional:   true,
				Validators: []validator.String{stringvalidator.RegexMatches(rxUUID, "must be a brand asset id")},
			},
			"favicon_asset_id": schema.StringAttribute{
				MarkdownDescription: "A brand asset of kind `favicon`; left out, the built-in one is kept.",
				Optional:            true,
				Validators:          []validator.String{stringvalidator.RegexMatches(rxUUID, "must be a brand asset id")},
			},
			"version": schema.Int64Attribute{
				MarkdownDescription: "The brand's version, bumped by every change; the next write names it.",
				Computed:            true,
			},
			"updated_at": schema.StringAttribute{
				MarkdownDescription: "When the brand last changed: RFC 3339 in UTC, compared as an instant.",
				CustomType:          TimestampType{}, Computed: true,
			},
		},
	}
}

func (r *brandResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var name, accent types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("product_name"), &name)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("product_name_accent"), &accent)...)
	if resp.Diagnostics.HasError() || name.IsUnknown() || accent.IsUnknown() || accent.ValueString() == "" {
		return
	}
	if !strings.Contains(name.ValueString(), accent.ValueString()) {
		resp.Diagnostics.AddAttributeError(path.Root("product_name_accent"), "Not part of the product name",
			fmt.Sprintf("product_name_accent %q must be a part of product_name %q; otherwise it would not be shown.",
				accent.ValueString(), name.ValueString()))
	}
}

// ModifyPlan keeps the state when no managed field differs (a colour written
// in another letter case), so that the version stays known and nothing is
// written.
func (r *brandResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() || req.Plan.Raw.Equal(req.State.Raw) {
		return
	}
	var plan, state brandModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	same := plan.ProductName.Equal(state.ProductName) && plan.ProductNameAccent.Equal(state.ProductNameAccent) &&
		plan.BrandColor.Equal(state.BrandColor) && plan.PageTitle.Equal(state.PageTitle) &&
		plan.LogoSize.Equal(state.LogoSize) && plan.LogoOffsetX.Equal(state.LogoOffsetX) &&
		sameFoldValue(plan.LogoAssetID, state.LogoAssetID) && sameFoldValue(plan.FaviconAssetID, state.FaviconAssetID)
	if same {
		resp.Plan.Raw = req.State.Raw.Copy()
	}
}

// sameFoldValue compares two strings case-insensitively, nulls as nulls.
func sameFoldValue(a, b types.String) bool {
	if a.IsUnknown() || b.IsUnknown() || a.IsNull() || b.IsNull() {
		return a.Equal(b)
	}
	return strings.EqualFold(a.ValueString(), b.ValueString())
}

func (r *brandResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *brandResource) configured(diags interface{ AddError(string, string) }) bool {
	if r.data == nil {
		diags.AddError("Provider not configured", "The ataila provider was not configured.")
		return false
	}
	return true
}

// put replaces the brand, conditional on version.
func (r *brandResource) put(ctx context.Context, plan *brandModel, version int, diags *diag.Diagnostics) bool {
	b, err := r.data.API.PutBrand(ctx, plan.body(), version)
	if err != nil {
		var apiErr *client.APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 412 {
			current, _ := apiErr.Extra("current_version")
			diags.AddError("The brand changed outside Terraform since the last read",
				fmt.Sprintf("The brand was changed outside this configuration after the provider read it "+
					"(it read version %d; the platform is at version %v), so the platform refused to replace it. "+
					"Run plan again to see the change, then apply. The provider does not retry.\n\n%s",
					version, current, apiErr.Detail()))
			return false
		}
		diags.Append(apiError("replacing the brand", err))
		return false
	}
	plan.fromAPI(b)
	return true
}

func (r *brandResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan brandModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	current, err := r.data.API.GetBrand(ctx)
	if err != nil {
		resp.Diagnostics.Append(apiError("reading the brand", err))
		return
	}
	if !r.put(ctx, &plan, current.Version, &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *brandResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var state brandModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	b, err := r.data.API.GetBrand(ctx)
	if err != nil {
		resp.Diagnostics.Append(apiError("reading the brand", err))
		return
	}
	state.fromAPI(b)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *brandResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan, state brandModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !r.put(ctx, &plan, int(state.Version.ValueInt64()), &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete forgets the resource: the brand cannot be removed.
func (r *brandResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {}

func (r *brandResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if strings.TrimSpace(req.ID) != SingletonImportID {
		resp.Diagnostics.AddError("Cannot import the brand",
			fmt.Sprintf("The brand is a singleton; its import id is %q, got %q.", SingletonImportID, req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), SingletonImportID)...)
}

// ── ataila_brand_asset ───────────────────────────────────────────────────────

// NewBrandAssetResource is the factory for ataila_brand_asset.
func NewBrandAssetResource() resource.Resource { return &brandAssetResource{} }

type brandAssetResource struct {
	data *ProviderData
}

type brandAssetModel struct {
	ID            types.String   `tfsdk:"id"`
	Kind          types.String   `tfsdk:"kind"`
	Source        types.String   `tfsdk:"source"`
	ContentBase64 types.String   `tfsdk:"content_base64"`
	SHA256        types.String   `tfsdk:"sha256"`
	Filename      types.String   `tfsdk:"filename"`
	Mime          types.String   `tfsdk:"mime"`
	Width         types.Int64    `tfsdk:"width"`
	Height        types.Int64    `tfsdk:"height"`
	Bytes         types.Int64    `tfsdk:"bytes"`
	URL           types.String   `tfsdk:"url"`
	UploadedAt    TimestampValue `tfsdk:"uploaded_at"`
}

func (m *brandAssetModel) fromAPI(a *client.BrandAsset) {
	m.ID = types.StringValue(a.Id)
	m.Kind = types.StringValue(string(a.Kind))
	m.SHA256 = types.StringValue(a.Sha256)
	m.Filename = types.StringValue(a.Filename)
	m.Mime = types.StringValue(a.Mime)
	m.Width = int64OrNull(a.Width)
	m.Height = int64OrNull(a.Height)
	m.Bytes = types.Int64Value(int64(a.Bytes))
	m.URL = types.StringValue(a.Url)
	m.UploadedAt = NewTimestamp(a.UploadedAt)
}

// content is the asset's bytes from source or content_base64.
func (m *brandAssetModel) content() ([]byte, string, error) {
	if !m.Source.IsNull() {
		b, err := os.ReadFile(m.Source.ValueString())
		if err != nil {
			return nil, "", fmt.Errorf("reading %s: %w", m.Source.ValueString(), err)
		}
		return b, filepath.Base(m.Source.ValueString()), nil
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(m.ContentBase64.ValueString()))
	if err != nil {
		return nil, "", errors.New("content_base64 is not valid standard base64")
	}
	return b, "", nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (r *brandAssetResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_brand_asset"
}

func (r *brandAssetResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	keepInt := []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A brand asset: a logo or a favicon stored by the platform, content-addressed by " +
			"its sha256. Give the file with `source` (a path) or `content_base64`.\n\n" +
			"Uploading bytes the platform already holds as the same kind adopts that asset (a warning says " +
			"so); the same bytes as the other kind are refused. A **changed file replaces the resource**: the " +
			"new content is uploaded as a new asset, and the old one stays. **Destroy only removes the " +
			"resource from the state**: the platform has no delete, and the file stays served.\n\n" +
			"**Everything uploaded is readable without signing in** at its `url` (the sign-in page shows it): " +
			"upload nothing that is not public. Logos: PNG, WebP or SVG, 120-2000 px wide; favicons: PNG, SVG " +
			"or ICO; at most 512 KB; SVG without active content.\n\n" +
			"Needs the permission `brand-center-admin-global`. Import by asset id.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{MarkdownDescription: "The asset's id, assigned by the platform.", Computed: true, PlanModifiers: keep},
			"kind": schema.StringAttribute{
				MarkdownDescription: "`logo` or `favicon`. Changing it replaces the resource.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.OneOf("logo", "favicon")},
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"source": schema.StringAttribute{
				MarkdownDescription: "Path of the file to upload. Its content is read at plan time; the file's " +
					"name is sent as the asset's file name. A change of path alone changes nothing.",
				Optional: true,
			},
			"content_base64": schema.StringAttribute{
				MarkdownDescription: "The file as standard base64, instead of `source`.",
				Optional:            true,
			},
			"sha256": schema.StringAttribute{
				MarkdownDescription: "sha256 (hex) of the content, computed at plan time. A different value " +
					"replaces the resource.",
				Computed: true,
			},
			"filename": schema.StringAttribute{MarkdownDescription: "The file name the platform recorded.", Computed: true, PlanModifiers: keep},
			"mime":     schema.StringAttribute{MarkdownDescription: "The media type the platform detected.", Computed: true, PlanModifiers: keep},
			"width":    schema.Int64Attribute{MarkdownDescription: "Width in pixels, when known.", Computed: true, PlanModifiers: keepInt},
			"height":   schema.Int64Attribute{MarkdownDescription: "Height in pixels, when known.", Computed: true, PlanModifiers: keepInt},
			"bytes":    schema.Int64Attribute{MarkdownDescription: "Size in bytes.", Computed: true, PlanModifiers: keepInt},
			"url": schema.StringAttribute{
				MarkdownDescription: "Where the platform serves the file (a path on the portal), readable without signing in.",
				Computed:            true, PlanModifiers: keep,
			},
			"uploaded_at": schema.StringAttribute{
				MarkdownDescription: "When the asset was first uploaded: RFC 3339 in UTC.",
				CustomType:          TimestampType{}, Computed: true, PlanModifiers: keep,
			},
		},
	}
}

func (r *brandAssetResource) ConfigValidators(context.Context) []resource.ConfigValidator {
	return []resource.ConfigValidator{
		resourcevalidator.ExactlyOneOf(path.MatchRoot("source"), path.MatchRoot("content_base64")),
	}
}

func (r *brandAssetResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *brandAssetResource) configured(diags interface{ AddError(string, string) }) bool {
	if r.data == nil {
		diags.AddError("Provider not configured", "The ataila provider was not configured.")
		return false
	}
	return true
}

// ModifyPlan computes the content's sha256 and replaces the resource when it
// changed.
func (r *brandAssetResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan brandAssetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var state *brandAssetModel
	if !req.State.Raw.IsNull() {
		state = &brandAssetModel{}
		resp.Diagnostics.Append(req.State.Get(ctx, state)...)
	}
	if plan.Source.IsUnknown() || plan.ContentBase64.IsUnknown() {
		plan.SHA256 = types.StringUnknown()
		if state != nil {
			resp.RequiresReplace = append(resp.RequiresReplace, path.Root("sha256"))
		}
		resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
		return
	}
	if plan.Source.IsNull() && plan.ContentBase64.IsNull() {
		return // the config validator reports it
	}
	b, _, err := plan.content()
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the brand asset's content", err.Error())
		return
	}
	plan.SHA256 = types.StringValue(sha256Hex(b))
	if state != nil && !state.SHA256.Equal(plan.SHA256) {
		resp.RequiresReplace = append(resp.RequiresReplace, path.Root("sha256"))
		for _, v := range []*types.String{&plan.ID, &plan.Filename, &plan.Mime, &plan.URL} {
			*v = types.StringUnknown()
		}
		for _, v := range []*types.Int64{&plan.Width, &plan.Height, &plan.Bytes} {
			*v = types.Int64Unknown()
		}
		plan.UploadedAt = TimestampValue{StringValue: types.StringUnknown()}
	}
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

func (r *brandAssetResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan brandAssetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	b, name, err := plan.content()
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the brand asset's content", err.Error())
		return
	}
	a, created, err := r.data.API.UploadBrandAsset(ctx, plan.Kind.ValueString(), b, name)
	if err != nil {
		resp.Diagnostics.Append(brandAssetError(plan.Kind.ValueString(), err))
		return
	}
	if !created {
		resp.Diagnostics.AddWarning("Existing brand asset adopted",
			fmt.Sprintf("The platform already held these bytes as a %s (asset %s, uploaded %s); the resource now "+
				"refers to it. Destroying the resource only forgets it.", a.Kind, a.Id, FormatTimestamp(a.UploadedAt)))
	}
	plan.fromAPI(a)
	if plan.SHA256.ValueString() != sha256Hex(b) {
		resp.Diagnostics.AddError("The platform stored different content",
			fmt.Sprintf("The platform reports sha256 %s for the upload, but the content's sha256 is %s.",
				plan.SHA256.ValueString(), sha256Hex(b)))
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func brandAssetError(kind string, err error) diag.Diagnostic {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.StatusCode == 409 && apiErr.Code() == client.CodeAssetKindConflict:
			id, _ := apiErr.Extra("existing_asset_id")
			other, _ := apiErr.Extra("existing_kind")
			return diag.NewErrorDiagnostic("The same file is stored as another kind",
				fmt.Sprintf("The platform already holds these bytes as a %v (asset %v), so it refuses them as a %s. "+
					"Use a different file, or import that asset with kind = %q.\n\n%s", other, id, kind, other, apiErr.Detail()))
		case apiErr.StatusCode == 503 && apiErr.Code() == client.CodeStorageUnavailable:
			return diag.NewErrorDiagnostic("The platform cannot store brand assets",
				"Object storage is not configured on this platform, so no new asset can be stored. The provider "+
					"does not retry.\n\n"+apiErr.Detail())
		}
	}
	return apiError("uploading the brand asset", err)
}

func (r *brandAssetResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var state brandAssetModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	a, err := r.data.API.GetBrandAsset(ctx, state.ID.ValueString())
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.Append(apiError("reading the brand asset "+state.ID.ValueString(), err))
		return
	}
	state.fromAPI(a)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update stores a new path or encoding of the same content: nothing changes
// on the platform (a different content is a replacement).
func (r *brandAssetResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state brandAssetModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	b, _, err := plan.content()
	if err != nil {
		resp.Diagnostics.AddError("Cannot read the brand asset's content", err.Error())
		return
	}
	if sha256Hex(b) != state.SHA256.ValueString() {
		resp.Diagnostics.AddError("The file changed after the plan",
			"The content read now differs from the content planned. Run plan again.")
		return
	}
	state.Source, state.ContentBase64 = plan.Source, plan.ContentBase64
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Delete forgets the resource: the platform has no delete for assets.
func (r *brandAssetResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}

func (r *brandAssetResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := strings.TrimSpace(req.ID)
	if !rxUUID.MatchString(id) {
		resp.Diagnostics.AddError("Cannot import the brand asset",
			fmt.Sprintf("The import id is the asset's id (a UUID), got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), strings.ToLower(id))...)
}

// ── data sources ─────────────────────────────────────────────────────────────

// NewBrandDataSource is the factory for the ataila_brand data source.
func NewBrandDataSource() datasource.DataSource { return &brandDataSource{} }

type brandDataSource struct {
	data *ProviderData
}

func (d *brandDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_brand"
}

func (d *brandDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	s := func(doc string) dschema.StringAttribute {
		return dschema.StringAttribute{MarkdownDescription: doc, Computed: true}
	}
	resp.Schema = dschema.Schema{
		MarkdownDescription: "The platform's brand. Needs `brand-center-read-global` or `brand-center-admin-global`.",
		Attributes: map[string]dschema.Attribute{
			"id":                  s("Always `current`."),
			"product_name":        s("The wordmark."),
			"product_name_accent": s("The part of the name shown in `brand_color`."),
			"brand_color":         s("Six-digit hex colour, lower case."),
			"page_title":          s("The browser-tab title."),
			"logo_size":           s("`compact`, `medium`, `regular` or `large`."),
			"logo_offset_x":       dschema.Int64Attribute{MarkdownDescription: "Horizontal nudge of the logo in pixels.", Computed: true},
			"logo_asset_id":       s("The logo asset; null for the built-in logo."),
			"favicon_asset_id":    s("The favicon asset; null for the built-in one."),
			"version":             dschema.Int64Attribute{MarkdownDescription: "The brand's version.", Computed: true},
			"updated_at": dschema.StringAttribute{
				MarkdownDescription: "When the brand last changed: RFC 3339 in UTC.", CustomType: TimestampType{}, Computed: true,
			},
		},
	}
}

func (d *brandDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *brandDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.data == nil {
		resp.Diagnostics.AddError("Provider not configured", "The ataila provider was not configured.")
		return
	}
	b, err := d.data.API.GetBrand(ctx)
	if err != nil {
		summary, detail := apiErrorText("the brand (GET /brand)", err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	var m brandModel
	m.fromAPI(b)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

// NewBrandAssetDataSource is the factory for the ataila_brand_asset data
// source.
func NewBrandAssetDataSource() datasource.DataSource { return &brandAssetDataSource{} }

type brandAssetDataSource struct {
	data *ProviderData
}

type brandAssetDataModel struct {
	ID         types.String   `tfsdk:"id"`
	Kind       types.String   `tfsdk:"kind"`
	SHA256     types.String   `tfsdk:"sha256"`
	Filename   types.String   `tfsdk:"filename"`
	Mime       types.String   `tfsdk:"mime"`
	Width      types.Int64    `tfsdk:"width"`
	Height     types.Int64    `tfsdk:"height"`
	Bytes      types.Int64    `tfsdk:"bytes"`
	URL        types.String   `tfsdk:"url"`
	UploadedAt TimestampValue `tfsdk:"uploaded_at"`
}

func (d *brandAssetDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_brand_asset"
}

func (d *brandAssetDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	s := func(doc string) dschema.StringAttribute {
		return dschema.StringAttribute{MarkdownDescription: doc, Computed: true}
	}
	i := func(doc string) dschema.Int64Attribute {
		return dschema.Int64Attribute{MarkdownDescription: doc, Computed: true}
	}
	resp.Schema = dschema.Schema{
		MarkdownDescription: "One brand asset, by `id` or by `sha256` of its content.",
		Attributes: map[string]dschema.Attribute{
			"id": dschema.StringAttribute{
				MarkdownDescription: "The asset's id. Give `id` or `sha256`.", Optional: true, Computed: true,
				Validators: []validator.String{stringvalidator.RegexMatches(rxUUID, "must be a brand asset id")},
			},
			"sha256": dschema.StringAttribute{
				MarkdownDescription: "sha256 (lower-case hex) of the content. Give `id` or `sha256`.", Optional: true, Computed: true,
				Validators: []validator.String{stringvalidator.RegexMatches(regexp.MustCompile(`^[0-9a-f]{64}$`), "must be a lower-case hex sha256")},
			},
			"kind":     s("`logo` or `favicon`."),
			"filename": s("The file name recorded at upload."),
			"mime":     s("The media type."),
			"width":    i("Width in pixels, when known."),
			"height":   i("Height in pixels, when known."),
			"bytes":    i("Size in bytes."),
			"url":      s("Where the platform serves the file, readable without signing in."),
			"uploaded_at": dschema.StringAttribute{
				MarkdownDescription: "When the asset was uploaded: RFC 3339 in UTC.", CustomType: TimestampType{}, Computed: true,
			},
		},
	}
}

func (d *brandAssetDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("sha256"))}
}

func (d *brandAssetDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *brandAssetDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.data == nil {
		resp.Diagnostics.AddError("Provider not configured", "The ataila provider was not configured.")
		return
	}
	var cfg brandAssetDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var a *client.BrandAsset
	if !cfg.ID.IsNull() {
		got, err := d.data.API.GetBrandAsset(ctx, cfg.ID.ValueString())
		if err != nil {
			summary, detail := apiErrorText("the brand asset "+cfg.ID.ValueString(), err)
			resp.Diagnostics.AddError(summary, detail)
			return
		}
		a = got
	} else {
		found, err := d.data.API.ListBrandAssets(ctx, "", cfg.SHA256.ValueString())
		if err != nil {
			summary, detail := apiErrorText("the brand assets (GET /brand/assets)", err)
			resp.Diagnostics.AddError(summary, detail)
			return
		}
		if len(found) != 1 {
			resp.Diagnostics.AddError("Brand asset not found",
				fmt.Sprintf("%d brand assets have sha256 %s; exactly one is needed.", len(found), cfg.SHA256.ValueString()))
			return
		}
		a = &found[0]
	}
	var m brandAssetModel
	m.fromAPI(a)
	out := brandAssetDataModel{ID: m.ID, Kind: m.Kind, SHA256: m.SHA256, Filename: m.Filename, Mime: m.Mime,
		Width: m.Width, Height: m.Height, Bytes: m.Bytes, URL: m.URL, UploadedAt: m.UploadedAt}
	resp.Diagnostics.Append(resp.State.Set(ctx, &out)...)
}
