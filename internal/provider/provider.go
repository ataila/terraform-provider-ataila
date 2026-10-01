// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

// Package provider implements the ATAILA provider for OpenTofu and Terraform.
//
// It uses terraform-plugin-framework over protocol 6 only, and on purpose uses
// nothing that exists in only one of the two CLIs, or at different versions in
// each: no actions, no ephemeral resources, no write-only attributes and no
// provider functions.
package provider

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/providervalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/ataila/terraform-provider-ataila/internal/client"
)

// Environment variables read when the matching argument is not set.
const (
	EnvEndpoint = "ATAILA_ENDPOINT"
	EnvToken    = "ATAILA_TOKEN"
	EnvCACert   = "ATAILA_CA_CERT"
)

var (
	_ provider.Provider                     = (*ataProvider)(nil)
	_ provider.ProviderWithConfigValidators = (*ataProvider)(nil)
)

type ataProvider struct {
	version string
}

// New returns the provider factory for the given release version.
func New(version string) func() provider.Provider {
	return func() provider.Provider { return &ataProvider{version: version} }
}

// ProviderData is what Configure hands to every data source and resource.
type ProviderData struct {
	API *client.API
	// AllowDestroy is the provider half of the destroy double opt-in: a
	// destroy also needs a token minted with allow_destroy.
	AllowDestroy bool
	// Meta is the answer of GET /meta at configure time.
	Meta *client.Meta
}

// DispatchMode is what pipeline dispatch does on the platform, as GET /meta
// said at configure time: "live", "dryrun" or "simulate" ("" when unknown).
func (d *ProviderData) DispatchMode() string {
	if d == nil || d.Meta == nil {
		return ""
	}
	return string(d.Meta.DispatchModeEffective)
}

type providerModel struct {
	Endpoint       types.String `tfsdk:"endpoint"`
	Token          types.String `tfsdk:"token"`
	CACertFile     types.String `tfsdk:"ca_cert_file"`
	CACertPEM      types.String `tfsdk:"ca_cert_pem"`
	AllowDestroy   types.Bool   `tfsdk:"allow_destroy"`
	RequestTimeout types.String `tfsdk:"request_timeout"`
}

func (p *ataProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "ataila"
	resp.Version = p.version
}

func (p *ataProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an ATAILA Cloud Platform through its versioned API (`/api/v1`). " +
			"Works with OpenTofu 1.6 and later and Terraform 1.6 and later.",
		Attributes: map[string]schema.Attribute{
			"endpoint": schema.StringAttribute{
				MarkdownDescription: "Base URL of the platform's portal, for example `https://portal.example.com`. " +
					"The provider calls `<endpoint>/api/v1`; a trailing `/api/v1` is accepted. HTTPS is required " +
					"except for a loopback address. May also be set with the `" + EnvEndpoint + "` environment variable.",
				Optional: true,
			},
			"token": schema.StringAttribute{
				MarkdownDescription: "API token: a personal token (`ataila_pat_…`) or a service-account token " +
					"(`ataila_sat_…`), minted in the portal. May also be set with the `" + EnvToken +
					"` environment variable, which keeps it out of configuration files.",
				Optional:  true,
				Sensitive: true,
			},
			"ca_cert_file": schema.StringAttribute{
				MarkdownDescription: "Path of a PEM file holding the certificate authority that signed the " +
					"platform's certificate, trusted in addition to the system roots. Conflicts with `ca_cert_pem`. " +
					"May also be set with `" + EnvCACert + "` (a path or PEM text).",
				Optional: true,
			},
			"ca_cert_pem": schema.StringAttribute{
				MarkdownDescription: "The same certificate authority as PEM text. Conflicts with `ca_cert_file`.",
				Optional:            true,
			},
			"allow_destroy": schema.BoolAttribute{
				MarkdownDescription: "Allow this provider to destroy platform objects. Defaults to `false`. " +
					"Destroying needs this flag **and** a token minted with destroy allowed; without both, " +
					"a destroy is refused.",
				Optional: true,
			},
			"request_timeout": schema.StringAttribute{
				MarkdownDescription: "Timeout for one HTTP request, as a duration such as `30s` or `2m`. " +
					"Each retry gets its own. Defaults to `60s`.",
				Optional: true,
			},
		},
	}
}

func (p *ataProvider) ConfigValidators(_ context.Context) []provider.ConfigValidator {
	return []provider.ConfigValidator{
		providervalidator.Conflicting(path.MatchRoot("ca_cert_file"), path.MatchRoot("ca_cert_pem")),
	}
}

func (p *ataProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	for name, v := range map[string]types.String{
		"endpoint": cfg.Endpoint, "token": cfg.Token, "ca_cert_file": cfg.CACertFile,
		"ca_cert_pem": cfg.CACertPEM, "request_timeout": cfg.RequestTimeout,
	} {
		if v.IsUnknown() {
			resp.Diagnostics.AddAttributeError(path.Root(name), "Unknown value for "+name,
				"The provider cannot be configured with a value that is only known after apply. "+
					"Set "+name+" statically or through its environment variable.")
		}
	}
	if cfg.AllowDestroy.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("allow_destroy"), "Unknown value for allow_destroy",
			"allow_destroy must be known when the provider is configured.")
	}
	if resp.Diagnostics.HasError() {
		return
	}

	endpoint := firstSet(cfg.Endpoint, EnvEndpoint)
	token := firstSet(cfg.Token, EnvToken)
	if endpoint == "" {
		resp.Diagnostics.AddAttributeError(path.Root("endpoint"), "Missing ATAILA API endpoint",
			"Set endpoint in the provider block or the "+EnvEndpoint+" environment variable, "+
				"for example https://portal.example.com.")
	}
	if token == "" {
		resp.Diagnostics.AddAttributeError(path.Root("token"), "Missing ATAILA API token",
			"Set token in the provider block or, better, the "+EnvToken+" environment variable. "+
				"Mint a token in the portal under API tokens.")
	}

	caPEM, caPath, err := caCert(cfg)
	if err != nil {
		resp.Diagnostics.AddAttributeError(caPath, "Cannot read the CA certificate", err.Error())
	}

	timeout := client.DefaultRequestTimeout
	if s := strings.TrimSpace(cfg.RequestTimeout.ValueString()); s != "" {
		d, err := time.ParseDuration(s)
		if err != nil || d <= 0 {
			resp.Diagnostics.AddAttributeError(path.Root("request_timeout"), "Invalid request_timeout",
				fmt.Sprintf("%q is not a positive duration such as 30s or 2m.", s))
		}
		timeout = d
	}
	if resp.Diagnostics.HasError() {
		return
	}

	api, err := client.New(client.Config{
		Endpoint:       endpoint,
		Token:          token,
		CACertPEM:      caPEM,
		RequestTimeout: timeout,
		UserAgent:      client.UserAgent(p.version),
	})
	if err != nil {
		resp.Diagnostics.AddError("Invalid ATAILA provider configuration", err.Error())
		return
	}

	meta, err := api.Meta(ctx)
	if err != nil {
		summary, detail := configureError(api.BaseURL(), err)
		resp.Diagnostics.AddError(summary, detail)
		return
	}
	if err := client.CheckAPIVersion(meta.ApiVersion); err != nil {
		resp.Diagnostics.AddError("Unsupported ATAILA API version", upperFirst(err.Error())+".")
		return
	}
	tflog.Debug(ctx, "configured ATAILA provider", map[string]interface{}{
		"api_version":      meta.ApiVersion,
		"platform_version": meta.PlatformVersion,
	})

	data := &ProviderData{API: api, AllowDestroy: cfg.AllowDestroy.ValueBool(), Meta: meta}
	resp.DataSourceData = data
	resp.ResourceData = data
}

func (p *ataProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewMetaDataSource,
		NewWhoamiDataSource,
		NewCustomerDataSource,
		NewTenantDataSource,
		NewTenantsDataSource,
		NewUserDataSource,
		NewUsersDataSource,
		NewPermissionCatalogDataSource,
		NewServingTiersDataSource,
		NewGatewayDataSource,
		NewProjectDataSource,
		NewProjectsDataSource,
		NewProjectStagesDataSource,
		NewLicenceDataSource,
		NewLicenceSocketFactsDataSource,
		NewBrandDataSource,
		NewBrandAssetDataSource,
		NewReleaseStateDataSource,
		NewReleaseOperationDataSource,
		NewAIModelDataSource,
		NewAIModelsDataSource,
		NewModelStorageDataSource,
		NewLoadTargetsDataSource,
		NewAINodesDataSource,
		NewAINodeDataSource,
		NewDGXClustersDataSource,
		NewLaunchCatalogDataSource,
	}
}

func (p *ataProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewCustomerResource,
		NewTenantResource,
		NewTenantMembershipResource,
		NewUserResource,
		NewUserRoleGrantResource,
		NewGatewayKeyResource,
		NewServingTierResource,
		NewProjectResource,
		NewProjectProvisioningResource,
		NewProjectMemberResource,
		NewLicenceBundleResource,
		NewBrandResource,
		NewBrandAssetResource,
		NewReleasePromotionResource,
		NewProjectProdLockResource,
		NewAIModelResource,
		NewAIModelNodeCacheResource,
	}
}

// firstSet returns the configured value, else the environment variable.
func firstSet(v types.String, env string) string {
	if s := strings.TrimSpace(v.ValueString()); s != "" {
		return s
	}
	return strings.TrimSpace(os.Getenv(env))
}

// caCert resolves ca_cert_pem, then ca_cert_file, then ATAILA_CA_CERT.
func caCert(cfg providerModel) ([]byte, path.Path, error) {
	if s := strings.TrimSpace(cfg.CACertPEM.ValueString()); s != "" {
		if !strings.Contains(s, "-----BEGIN") {
			return nil, path.Root("ca_cert_pem"), errors.New("ca_cert_pem does not hold PEM text")
		}
		return []byte(s), path.Root("ca_cert_pem"), nil
	}
	if f := strings.TrimSpace(cfg.CACertFile.ValueString()); f != "" {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, path.Root("ca_cert_file"), fmt.Errorf("reading %s: %w", f, err)
		}
		return b, path.Root("ca_cert_file"), nil
	}
	b, err := client.LoadCACert(os.Getenv(EnvCACert))
	if err != nil {
		return nil, path.Root("ca_cert_file"), fmt.Errorf("%s: %w", EnvCACert, err)
	}
	return b, path.Root("ca_cert_file"), nil
}

// configureError explains a failed GET /meta, the first call the provider makes.
func configureError(base string, err error) (string, string) {
	var apiErr *client.APIError
	if !errors.As(err, &apiErr) {
		return "Cannot reach the ATAILA API",
			fmt.Sprintf("GET %s/meta failed: %v\n\nCheck the endpoint, the network path and, for a private "+
				"certificate authority, ca_cert_file or ca_cert_pem.", base, err)
	}
	switch {
	case apiErr.IsLicenceRefusal():
		return apiErr.Summary(), apiErr.Detail()
	case apiErr.StatusCode == 401:
		return "The ATAILA API rejected the token",
			"The token is missing, malformed, expired or revoked. Mint a new one in the portal.\n\n" + apiErr.Detail()
	case apiErr.StatusCode == 404:
		return "No ATAILA API at this endpoint",
			fmt.Sprintf("GET %s/meta answered 404. Either the endpoint is wrong, or the platform's public API "+
				"is switched off: an administrator switches it on in the portal's platform settings.\n\n%s",
				base, apiErr.Detail())
	case client.Retryable(apiErr.StatusCode):
		return "The ATAILA API is unavailable",
			"The API kept answering that it cannot serve right now, and the provider gave up retrying. " +
				"Try again shortly.\n\n" + apiErr.Detail()
	}
	return apiErr.Summary(), apiErr.Detail()
}

// apiErrorText is the diagnostic for a failed read; an *APIError carries its
// own summary and detail (problem title, detail, code and request id).
func apiErrorText(what string, err error) (string, string) {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		return apiErr.Summary(), fmt.Sprintf("While reading %s.\n\n%s", what, apiErr.Detail())
	}
	return "Cannot reach the ATAILA API", fmt.Sprintf("While reading %s: %v", what, err)
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
