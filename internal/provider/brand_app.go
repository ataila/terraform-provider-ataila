// Copyright (c) 2026 Macskásy Attila (ATAILA)
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/ataila/terraform-provider-ataila/internal/client"
)

var (
	_ resource.ResourceWithConfigure      = (*brandAppResource)(nil)
	_ resource.ResourceWithImportState    = (*brandAppResource)(nil)
	_ resource.ResourceWithValidateConfig = (*brandAppResource)(nil)
	_ resource.ResourceWithModifyPlan     = (*brandAppResource)(nil)
)

var (
	rxAppSlug       = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)
	rxLowerHex      = regexp.MustCompile(`^#[0-9a-f]{6}$`)
	rxHTTPS         = regexp.MustCompile(`^https://`)
	footerLinkTypes = map[string]attr.Type{"label": types.StringType, "url": types.StringType}
	footerLinkType  = types.ObjectType{AttrTypes: footerLinkTypes}
)

// ── ataila_brand_app ─────────────────────────────────────────────────────────

// NewBrandAppResource is the factory for ataila_brand_app.
func NewBrandAppResource() resource.Resource { return &brandAppResource{} }

type brandAppResource struct {
	data *ProviderData
}

type brandAppModel struct {
	ID                types.String   `tfsdk:"id"`
	AppSlug           types.String   `tfsdk:"app_slug"`
	DisplayName       types.String   `tfsdk:"display_name"`
	DisplayNameAccent types.String   `tfsdk:"display_name_accent"`
	PrimaryColor      types.String   `tfsdk:"primary_color"`
	OnPrimaryColor    types.String   `tfsdk:"on_primary_color"`
	SecondaryColor    types.String   `tfsdk:"secondary_color"`
	FontPreset        types.String   `tfsdk:"font_preset"`
	HeaderLogoAssetID types.String   `tfsdk:"header_logo_asset_id"`
	HeaderLogoSize    types.String   `tfsdk:"header_logo_size"`
	FaviconAssetID    types.String   `tfsdk:"favicon_asset_id"`
	OGImageAssetID    types.String   `tfsdk:"og_image_asset_id"`
	FooterTextEN      types.String   `tfsdk:"footer_text_en"`
	FooterTextHU      types.String   `tfsdk:"footer_text_hu"`
	FooterLinks       types.List     `tfsdk:"footer_links"`
	CoBrandLineEN     types.String   `tfsdk:"co_brand_line_en"`
	CoBrandLineHU     types.String   `tfsdk:"co_brand_line_hu"`
	DefaultLanguage   types.String   `tfsdk:"default_language"`
	LegalPrivacyURL   types.String   `tfsdk:"legal_privacy_url"`
	LegalImprintURL   types.String   `tfsdk:"legal_imprint_url"`
	Version           types.Int64    `tfsdk:"version"`
	UpdatedAt         TimestampValue `tfsdk:"updated_at"`
}

// footerLinksValue is the profile's links as the attribute: null inherits,
// an empty list is "no links".
func footerLinksValue(links *[]client.FooterLink) types.List {
	if links == nil {
		return types.ListNull(footerLinkType)
	}
	elems := make([]attr.Value, 0, len(*links))
	for _, l := range *links {
		elems = append(elems, types.ObjectValueMust(footerLinkTypes, map[string]attr.Value{
			"label": types.StringValue(l.Label), "url": types.StringValue(l.Url),
		}))
	}
	return types.ListValueMust(footerLinkType, elems)
}

func enumString[T ~string](p *T) types.String {
	if p == nil {
		return types.StringNull()
	}
	return types.StringValue(string(*p))
}

func enumPtr[T ~string](v types.String) *T {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	t := T(v.ValueString())
	return &t
}

func (m *brandAppModel) fromAPI(p *client.BrandAppProfile) {
	slug := m.AppSlug.ValueString()
	if p.AppSlug != nil {
		slug = *p.AppSlug
	}
	m.ID = types.StringValue(slug)
	m.AppSlug = types.StringValue(slug)
	m.DisplayName = stringOrNull(p.DisplayName)
	m.DisplayNameAccent = stringOrNull(p.DisplayNameAccent)
	m.PrimaryColor = stringOrNull(p.PrimaryColor)
	m.OnPrimaryColor = stringOrNull(p.OnPrimaryColor)
	m.SecondaryColor = stringOrNull(p.SecondaryColor)
	m.FontPreset = enumString(p.FontPreset)
	m.HeaderLogoAssetID = keepFoldPtr(m.HeaderLogoAssetID, p.HeaderLogoAssetId)
	m.HeaderLogoSize = enumString(p.HeaderLogoSize)
	m.FaviconAssetID = keepFoldPtr(m.FaviconAssetID, p.FaviconAssetId)
	m.OGImageAssetID = keepFoldPtr(m.OGImageAssetID, p.OgImageAssetId)
	m.FooterTextEN = stringOrNull(p.FooterTextEn)
	m.FooterTextHU = stringOrNull(p.FooterTextHu)
	m.FooterLinks = footerLinksValue(p.FooterLinks)
	m.CoBrandLineEN = stringOrNull(p.CoBrandLineEn)
	m.CoBrandLineHU = stringOrNull(p.CoBrandLineHu)
	m.DefaultLanguage = enumString(p.DefaultLanguage)
	m.LegalPrivacyURL = stringOrNull(p.LegalPrivacyUrl)
	m.LegalImprintURL = stringOrNull(p.LegalImprintUrl)
	m.Version = types.Int64Value(int64(p.Version))
	m.UpdatedAt = NewTimestamp(p.UpdatedAt)
}

func (m *brandAppModel) body(ctx context.Context, diags *diag.Diagnostics) client.BrandAppPut {
	var links *[]client.FooterLink
	if !m.FooterLinks.IsNull() && !m.FooterLinks.IsUnknown() {
		var raw []struct {
			Label types.String `tfsdk:"label"`
			URL   types.String `tfsdk:"url"`
		}
		diags.Append(m.FooterLinks.ElementsAs(ctx, &raw, false)...)
		out := make([]client.FooterLink, 0, len(raw))
		for _, l := range raw {
			out = append(out, client.FooterLink{Label: l.Label.ValueString(), Url: l.URL.ValueString()})
		}
		links = &out
	}
	return client.BrandAppPut{
		DisplayName:       optString(m.DisplayName),
		DisplayNameAccent: optString(m.DisplayNameAccent),
		PrimaryColor:      optString(m.PrimaryColor),
		OnPrimaryColor:    optString(m.OnPrimaryColor),
		SecondaryColor:    optString(m.SecondaryColor),
		FontPreset:        enumPtr[client.BrandAppPutFontPreset](m.FontPreset),
		HeaderLogoAssetId: optString(m.HeaderLogoAssetID),
		HeaderLogoSize:    enumPtr[client.BrandAppPutHeaderLogoSize](m.HeaderLogoSize),
		FaviconAssetId:    optString(m.FaviconAssetID),
		OgImageAssetId:    optString(m.OGImageAssetID),
		FooterTextEn:      optString(m.FooterTextEN),
		FooterTextHu:      optString(m.FooterTextHU),
		FooterLinks:       links,
		CoBrandLineEn:     optString(m.CoBrandLineEN),
		CoBrandLineHu:     optString(m.CoBrandLineHU),
		DefaultLanguage:   enumPtr[client.BrandAppPutDefaultLanguage](m.DefaultLanguage),
		LegalPrivacyUrl:   optString(m.LegalPrivacyURL),
		LegalImprintUrl:   optString(m.LegalImprintURL),
	}
}

func (r *brandAppResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_brand_app"
}

func inheritString(desc string, v ...validator.String) schema.StringAttribute {
	return schema.StringAttribute{MarkdownDescription: desc + " Left out: inherited.", Optional: true, Validators: v}
}

func (r *brandAppResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	assetID := stringvalidator.RegexMatches(rxUUID, "must be a brand asset id")
	resp.Schema = schema.Schema{
		MarkdownDescription: "One application's Brand Center profile: the name, colours, logo, font, footer and " +
			"legal links an application built on the platform shows, overriding this platform's own brand " +
			"field by field.\n\n" +
			"Every field is optional, and a field left out is **inherited**: the application shows this " +
			"platform's brand (or the built-in default) for it. The resource manages all of them: a write " +
			"replaces the whole profile, so a field set in the portal and left out here goes back to " +
			"inherited on the next apply. Every update names the profile's `version` as last read (If-Match): " +
			"when the profile changed outside the configuration after that read, the platform refuses (412) " +
			"and the provider reports it; run plan again. It never retries.\n\n" +
			"**Create refuses an application that already has a profile** (one made in the portal, say): " +
			"import it instead, so that nothing is overwritten unseen. **Destroy deletes the profile**: the " +
			"application inherits everything again; its uploaded assets are kept.\n\n" +
			"Needs the permission `brand-center-admin-global` and platform release " + client.ReleaseBrandApps +
			" or later. Import with the application slug.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "The application slug.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"app_slug": schema.StringAttribute{
				MarkdownDescription: "The application: lower-case letters, digits and hyphens, starting with a " +
					"letter, 2-32 characters. Changing it replaces the resource.",
				Required:      true,
				Validators:    []validator.String{stringvalidator.RegexMatches(rxAppSlug, "must be lower-case letters, digits and hyphens, starting with a letter, 2-32 characters")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"display_name":         inheritString("The application's name in its header, 2-32 characters.", stringvalidator.LengthBetween(2, 32)),
			"display_name_accent":  inheritString("A part of `display_name` rendered in the primary colour; `\"\"` colours the whole name. Needs `display_name`, at most 32 characters.", stringvalidator.LengthAtMost(32)),
			"primary_color":        inheritString("Lower-case six-digit hex colour, for example `#0b6e4f`.", stringvalidator.RegexMatches(rxLowerHex, "must be a lower-case six-digit hex colour such as #0b6e4f")),
			"on_primary_color":     inheritString("Lower-case six-digit hex text colour on the primary (button text), used in place of the computed one.", stringvalidator.RegexMatches(rxLowerHex, "must be a lower-case six-digit hex colour such as #ffffff")),
			"secondary_color":      inheritString("Lower-case six-digit hex highlight colour.", stringvalidator.RegexMatches(rxLowerHex, "must be a lower-case six-digit hex colour such as #ffcf00")),
			"font_preset":          inheritString("`inter`, `montserrat` or `system`.", stringvalidator.OneOf("inter", "montserrat", "system")),
			"header_logo_asset_id": inheritString("A brand asset of kind `header_logo` or `logo` (`ataila_brand_asset`).", assetID),
			"header_logo_size":     inheritString("`compact`, `medium`, `regular` or `large`.", stringvalidator.OneOf("compact", "medium", "regular", "large")),
			"favicon_asset_id":     inheritString("A brand asset of kind `favicon`.", assetID),
			"og_image_asset_id":    inheritString("A brand asset of kind `og_image` (the social preview card, 1200 x 630).", assetID),
			"footer_text_en":       inheritString("The footer line in English, at most 160 characters.", stringvalidator.LengthAtMost(160)),
			"footer_text_hu":       inheritString("The footer line in Hungarian, at most 160 characters.", stringvalidator.LengthAtMost(160)),
			"footer_links": schema.ListNestedAttribute{
				MarkdownDescription: "Up to five footer links. `[]`: no links. Left out: inherited.",
				Optional:            true,
				Validators:          []validator.List{listvalidator.SizeAtMost(5)},
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"label": schema.StringAttribute{
						MarkdownDescription: "The link text, 1-40 characters.", Required: true,
						Validators: []validator.String{stringvalidator.LengthBetween(1, 40)},
					},
					"url": schema.StringAttribute{
						MarkdownDescription: "The address, at most 300 characters.", Required: true,
						Validators: []validator.String{stringvalidator.LengthBetween(1, 300)},
					},
				}},
			},
			"co_brand_line_en": inheritString("An optional partner line in the footer, in English, at most 120 characters.", stringvalidator.LengthAtMost(120)),
			"co_brand_line_hu": inheritString("An optional partner line in the footer, in Hungarian, at most 120 characters.", stringvalidator.LengthAtMost(120)),
			"default_language": inheritString("The language a first-time visitor sees: `en` or `hu`.", stringvalidator.OneOf("en", "hu")),
			"legal_privacy_url": inheritString("The privacy notice; starts with `https://`, at most 300 characters.",
				stringvalidator.RegexMatches(rxHTTPS, "must start with https://"), stringvalidator.LengthAtMost(300)),
			"legal_imprint_url": inheritString("The imprint (legal notice); starts with `https://`, at most 300 characters.",
				stringvalidator.RegexMatches(rxHTTPS, "must start with https://"), stringvalidator.LengthAtMost(300)),
			"version": schema.Int64Attribute{
				MarkdownDescription: "The profile's version, bumped by every change; the next write names it.",
				Computed:            true,
			},
			"updated_at": schema.StringAttribute{
				MarkdownDescription: "When the profile last changed: RFC 3339 in UTC, compared as an instant.",
				CustomType:          TimestampType{}, Computed: true,
			},
		},
	}
}

func (r *brandAppResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var name, accent types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("display_name"), &name)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("display_name_accent"), &accent)...)
	if resp.Diagnostics.HasError() || name.IsUnknown() || accent.IsUnknown() || accent.IsNull() {
		return
	}
	if name.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("display_name_accent"), "An accent needs its name",
			"display_name_accent is a part of display_name: set display_name in the same profile, or leave the accent out.")
		return
	}
	if a := accent.ValueString(); a != "" && !strings.Contains(name.ValueString(), a) {
		resp.Diagnostics.AddAttributeError(path.Root("display_name_accent"), "Not part of the display name",
			fmt.Sprintf("display_name_accent %q must be a part of display_name %q; otherwise it would not be shown.",
				a, name.ValueString()))
	}
}

// ModifyPlan refuses an older platform before anything is sent, and keeps the
// state when only the letter case of an asset id differs.
func (r *brandAppResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	if dg := r.data.featureRefused(client.FeatureBrandApps, path.Root("app_slug"), "The ataila_brand_app resource"); dg != nil {
		resp.Diagnostics.Append(dg)
		return
	}
	if req.State.Raw.IsNull() || req.Plan.Raw.Equal(req.State.Raw) {
		return
	}
	var plan, state brandAppModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	same := plan.AppSlug.Equal(state.AppSlug) && plan.DisplayName.Equal(state.DisplayName) &&
		plan.DisplayNameAccent.Equal(state.DisplayNameAccent) && plan.PrimaryColor.Equal(state.PrimaryColor) &&
		plan.OnPrimaryColor.Equal(state.OnPrimaryColor) && plan.SecondaryColor.Equal(state.SecondaryColor) &&
		plan.FontPreset.Equal(state.FontPreset) && sameFoldValue(plan.HeaderLogoAssetID, state.HeaderLogoAssetID) &&
		plan.HeaderLogoSize.Equal(state.HeaderLogoSize) && sameFoldValue(plan.FaviconAssetID, state.FaviconAssetID) &&
		sameFoldValue(plan.OGImageAssetID, state.OGImageAssetID) && plan.FooterTextEN.Equal(state.FooterTextEN) &&
		plan.FooterTextHU.Equal(state.FooterTextHU) && plan.FooterLinks.Equal(state.FooterLinks) &&
		plan.CoBrandLineEN.Equal(state.CoBrandLineEN) && plan.CoBrandLineHU.Equal(state.CoBrandLineHU) &&
		plan.DefaultLanguage.Equal(state.DefaultLanguage) && plan.LegalPrivacyURL.Equal(state.LegalPrivacyURL) &&
		plan.LegalImprintURL.Equal(state.LegalImprintURL)
	if same {
		resp.Plan.Raw = req.State.Raw.Copy()
	}
}

func (r *brandAppResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (r *brandAppResource) configured(diags interface{ AddError(string, string) }) bool {
	if r.data == nil {
		diags.AddError("Provider not configured", "The ataila provider was not configured.")
		return false
	}
	return true
}

// put replaces the profile; version nil creates it (no If-Match).
func (r *brandAppResource) put(ctx context.Context, plan *brandAppModel, version *int, diags *diag.Diagnostics) bool {
	body := plan.body(ctx, diags)
	if diags.HasError() {
		return false
	}
	slug := plan.AppSlug.ValueString()
	p, err := r.data.API.PutBrandApp(ctx, slug, body, version)
	if err != nil {
		var apiErr *client.APIError
		if version != nil && errors.As(err, &apiErr) && apiErr.StatusCode == 412 {
			current, _ := apiErr.Extra("current_version")
			diags.AddError("The brand profile changed outside Terraform since the last read",
				fmt.Sprintf("The profile of %q was changed outside this configuration after the provider read it "+
					"(it read version %d; the platform is at version %v), so the platform refused to replace it. "+
					"Run plan again to see the change, then apply. The provider does not retry.\n\n%s",
					slug, *version, current, apiErr.Detail()))
			return false
		}
		diags.Append(apiError(fmt.Sprintf("replacing the brand profile of %q", slug), err))
		return false
	}
	plan.fromAPI(p)
	return true
}

func (r *brandAppResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan brandAppModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	slug := plan.AppSlug.ValueString()
	existing, err := r.data.API.GetBrandApp(ctx, slug)
	switch {
	case err == nil:
		resp.Diagnostics.AddAttributeError(path.Root("app_slug"), "The application already has a brand profile",
			fmt.Sprintf("%q has a profile (version %d), made outside this configuration. Creating would overwrite "+
				"it unseen, so nothing was sent. Import it (terraform import <address> %s), then plan.",
				slug, existing.Version, slug))
		return
	case !isNotFound(err):
		resp.Diagnostics.Append(apiError(fmt.Sprintf("reading the brand profile of %q", slug), err))
		return
	}
	if !r.put(ctx, &plan, nil, &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *brandAppResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var state brandAppModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	slug := state.AppSlug.ValueString()
	if slug == "" {
		slug = state.ID.ValueString()
		state.AppSlug = types.StringValue(slug)
	}
	p, err := r.data.API.GetBrandApp(ctx, slug)
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.Append(apiError(fmt.Sprintf("reading the brand profile of %q", slug), err))
		return
	}
	state.fromAPI(p)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *brandAppResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var plan, state brandAppModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	version := int(state.Version.ValueInt64())
	if !r.put(ctx, &plan, &version, &resp.Diagnostics) {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete removes the profile; one already gone is not an error.
func (r *brandAppResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.configured(&resp.Diagnostics) {
		return
	}
	var state brandAppModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	slug := state.AppSlug.ValueString()
	if err := r.data.API.DeleteBrandApp(ctx, slug); err != nil && !isNotFound(err) {
		resp.Diagnostics.Append(apiError(fmt.Sprintf("deleting the brand profile of %q", slug), err))
	}
}

func (r *brandAppResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	slug := strings.TrimSpace(req.ID)
	if !rxAppSlug.MatchString(slug) {
		resp.Diagnostics.AddError("Cannot import the brand profile",
			fmt.Sprintf("The import id is the application slug (lower-case letters, digits and hyphens), got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), slug)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("app_slug"), slug)...)
}

// ── data source ataila_brand_app ─────────────────────────────────────────────

var _ datasource.DataSourceWithConfigure = (*brandAppDataSource)(nil)

// NewBrandAppDataSource is the factory for the ataila_brand_app data source.
func NewBrandAppDataSource() datasource.DataSource { return &brandAppDataSource{} }

type brandAppDataSource struct {
	data *ProviderData
}

type brandAppDataModel struct {
	AppSlug           types.String `tfsdk:"app_slug"`
	DisplayName       types.String `tfsdk:"display_name"`
	DisplayNameAccent types.String `tfsdk:"display_name_accent"`
	PrimaryColor      types.String `tfsdk:"primary_color"`
	Shades            types.Map    `tfsdk:"shades"`
	OnPrimary         types.String `tfsdk:"on_primary"`
	SecondaryColor    types.String `tfsdk:"secondary_color"`
	FontPreset        types.String `tfsdk:"font_preset"`
	FontStack         types.String `tfsdk:"font_stack"`
	HeaderLogoURL     types.String `tfsdk:"header_logo_url"`
	HeaderLogoHeight  types.Int64  `tfsdk:"header_logo_max_height"`
	HeaderLogoWidth   types.String `tfsdk:"header_logo_max_width"`
	FaviconURL        types.String `tfsdk:"favicon_url"`
	OGImageURL        types.String `tfsdk:"og_image_url"`
	FooterTextEN      types.String `tfsdk:"footer_text_en"`
	FooterTextHU      types.String `tfsdk:"footer_text_hu"`
	FooterLinks       types.List   `tfsdk:"footer_links"`
	CoBrandLineEN     types.String `tfsdk:"co_brand_line_en"`
	CoBrandLineHU     types.String `tfsdk:"co_brand_line_hu"`
	PrivacyURL        types.String `tfsdk:"legal_privacy_url"`
	ImprintURL        types.String `tfsdk:"legal_imprint_url"`
	DefaultLanguage   types.String `tfsdk:"default_language"`
	Attribution       types.String `tfsdk:"attribution"`
	FirstParty        types.Bool   `tfsdk:"first_party"`
	Source            types.Map    `tfsdk:"source"`
	Version           types.Int64  `tfsdk:"version"`
	InstallVersion    types.Int64  `tfsdk:"install_version"`
}

func (d *brandAppDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_brand_app"
}

func (d *brandAppDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	str := func(desc string) dschema.StringAttribute {
		return dschema.StringAttribute{MarkdownDescription: desc, Computed: true}
	}
	resp.Schema = dschema.Schema{
		MarkdownDescription: "What an application built on the platform shows: its Brand Center profile resolved " +
			"against this platform's brand and the built-in defaults, field by field, with where each value " +
			"came from. An application without a profile resolves too (`version` 0). Needs " +
			"`brand-center-read-global` (or the admin key) and platform release " + client.ReleaseBrandApps + " or later.",
		Attributes: map[string]dschema.Attribute{
			"app_slug": dschema.StringAttribute{
				MarkdownDescription: "The application slug.", Required: true,
				Validators: []validator.String{stringvalidator.RegexMatches(rxAppSlug, "must be lower-case letters, digits and hyphens, starting with a letter, 2-32 characters")},
			},
			"display_name":        str("The name in the header."),
			"display_name_accent": str("The part of the name rendered in the primary colour; empty colours the whole name."),
			"primary_color":       str("The primary colour, lower-case hex."),
			"shades": dschema.MapAttribute{
				MarkdownDescription: "Ten shades of the primary, keys `50` to `900`.", ElementType: types.StringType, Computed: true,
			},
			"on_primary":      str("The text colour on the primary."),
			"secondary_color": str("The highlight colour, lower-case hex."),
			"font_preset":     str("`inter`, `montserrat` or `system`."),
			"font_stack":      str("The CSS font-family value for `font_preset`."),
			"header_logo_url": str("The header logo's address; null when there is none."),
			"header_logo_max_height": dschema.Int64Attribute{
				MarkdownDescription: "The header logo box's largest height, in pixels (the logo is fitted, never stretched).", Computed: true,
			},
			"header_logo_max_width": str("The header logo box's largest width, as a CSS length such as `88%`."),
			"favicon_url":           str("The favicon's address; null when there is none."),
			"og_image_url":          str("The social preview card's address; null when there is none."),
			"footer_text_en":        str("The footer line in English."),
			"footer_text_hu":        str("The footer line in Hungarian."),
			"footer_links": dschema.ListNestedAttribute{
				MarkdownDescription: "The footer links.", Computed: true,
				NestedObject: dschema.NestedAttributeObject{Attributes: map[string]dschema.Attribute{
					"label": str("The link text."), "url": str("The address."),
				}},
			},
			"co_brand_line_en":  str("The partner line in English."),
			"co_brand_line_hu":  str("The partner line in Hungarian."),
			"legal_privacy_url": str("The privacy notice; null when there is none."),
			"legal_imprint_url": str("The imprint; null when there is none."),
			"default_language":  str("`en` or `hu`."),
			"attribution":       str("The line printed under the name, from this platform's installation; never settable."),
			"first_party": dschema.BoolAttribute{
				MarkdownDescription: "True only on the platform vendor's own installation.", Computed: true,
			},
			"source": dschema.MapAttribute{
				MarkdownDescription: "Where each writable field's value came from, keyed by the field " +
					"(`display_name`, `primary_color`, …): the profile, this platform's brand, or the default.",
				ElementType: types.StringType, Computed: true,
			},
			"version": dschema.Int64Attribute{
				MarkdownDescription: "The profile's version; 0 when the application has no profile.", Computed: true,
			},
			"install_version": dschema.Int64Attribute{
				MarkdownDescription: "The version of this platform's own brand.", Computed: true,
			},
		},
	}
}

func (d *brandAppDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.data = providerDataFrom(req.ProviderData, &resp.Diagnostics)
}

func (d *brandAppDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	if d.data == nil {
		resp.Diagnostics.AddError("Provider not configured", "The ataila provider was not configured.")
		return
	}
	var m brandAppDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if dg := d.data.featureRefused(client.FeatureBrandApps, path.Root("app_slug"), "The ataila_brand_app data source"); dg != nil {
		resp.Diagnostics.Append(dg)
		return
	}
	slug := m.AppSlug.ValueString()
	b, err := d.data.API.GetResolvedBrandApp(ctx, slug)
	if err != nil {
		resp.Diagnostics.Append(apiError(fmt.Sprintf("reading the resolved brand of %q", slug), err))
		return
	}
	shades := map[string]attr.Value{}
	for k, v := range b.Shades {
		shades[k] = types.StringValue(v)
	}
	source := map[string]attr.Value{}
	if raw, err := json.Marshal(b.Source); err == nil {
		var fields map[string]string
		if json.Unmarshal(raw, &fields) == nil {
			for k, v := range fields {
				source[k] = types.StringValue(v)
			}
		}
	}
	links := b.Footer.Links
	m.DisplayName = types.StringValue(b.DisplayName)
	m.DisplayNameAccent = types.StringValue(b.DisplayNameAccent)
	m.PrimaryColor = types.StringValue(b.PrimaryColor)
	m.Shades = types.MapValueMust(types.StringType, shades)
	m.OnPrimary = types.StringValue(b.OnPrimary)
	m.SecondaryColor = types.StringValue(b.SecondaryColor)
	m.FontPreset = types.StringValue(string(b.FontPreset))
	m.FontStack = types.StringValue(b.FontStack)
	m.HeaderLogoURL = stringOrNull(b.Assets.HeaderLogoUrl)
	m.HeaderLogoHeight = types.Int64Value(int64(b.Assets.HeaderLogoSize.MaxHeight))
	m.HeaderLogoWidth = types.StringValue(b.Assets.HeaderLogoSize.MaxWidth)
	m.FaviconURL = stringOrNull(b.Assets.FaviconUrl)
	m.OGImageURL = stringOrNull(b.Assets.OgImageUrl)
	m.FooterTextEN = types.StringValue(b.Footer.Text.En)
	m.FooterTextHU = types.StringValue(b.Footer.Text.Hu)
	m.FooterLinks = footerLinksValue(&links)
	m.CoBrandLineEN = types.StringValue(b.Footer.CoBrandLine.En)
	m.CoBrandLineHU = types.StringValue(b.Footer.CoBrandLine.Hu)
	m.PrivacyURL = stringOrNull(b.Legal.PrivacyUrl)
	m.ImprintURL = stringOrNull(b.Legal.ImprintUrl)
	m.DefaultLanguage = types.StringValue(string(b.DefaultLanguage))
	m.Attribution = types.StringValue(b.Attribution)
	m.FirstParty = types.BoolValue(b.FirstParty)
	m.Source = types.MapValueMust(types.StringType, source)
	m.Version = types.Int64Value(int64(b.Version))
	m.InstallVersion = types.Int64Value(int64(b.InstallVersion))
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
