// Package provider is the Codigate Terraform provider (Terraform Plugin Framework).
package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/codigate/terraform-provider-codigate/internal/client"
)

// DefaultAPIURL is the hosted Codigate workspace API.
const DefaultAPIURL = "https://app.codigate.nl/api/v1"

var _ provider.Provider = (*codigateProvider)(nil)

type codigateProvider struct {
	version string
}

type providerModel struct {
	APIURL types.String `tfsdk:"api_url"`
	Token  types.String `tfsdk:"token"`
}

// New returns the provider factory for providerserver.
func New(version string) func() provider.Provider {
	return func() provider.Provider { return &codigateProvider{version: version} }
}

func (p *codigateProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "codigate"
	resp.Version = p.version
}

func (p *codigateProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manage a Codigate workspace (agents, apps, access, edge rules, identity providers, domains, service tokens, log export, branding) through the workspace API /api/v1.",
		Attributes: map[string]schema.Attribute{
			"api_url": schema.StringAttribute{
				Optional:    true,
				Description: "Base URL of the workspace API. Defaults to the CODIGATE_API_URL environment variable, then " + DefaultAPIURL + ".",
			},
			"token": schema.StringAttribute{
				Optional:    true,
				Sensitive:   true,
				Description: "Workspace API token (Settings → API tokens). A viewer token can only read; managing resources needs an admin token. Defaults to the CODIGATE_API_TOKEN environment variable.",
			},
		},
	}
}

func (p *codigateProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if cfg.APIURL.IsUnknown() || cfg.Token.IsUnknown() {
		// Values come from resources that are not created yet; Terraform calls Configure again at apply.
		return
	}
	apiURL := cfg.APIURL.ValueString()
	if apiURL == "" {
		apiURL = os.Getenv("CODIGATE_API_URL")
	}
	if apiURL == "" {
		apiURL = DefaultAPIURL
	}
	token := cfg.Token.ValueString()
	if token == "" {
		token = os.Getenv("CODIGATE_API_TOKEN")
	}
	if token == "" {
		resp.Diagnostics.AddAttributeError(path.Root("token"), "Missing Codigate API token",
			"Set the provider's token argument or the CODIGATE_API_TOKEN environment variable to a workspace API token.")
		return
	}
	c := client.New(apiURL, token, "terraform-provider-codigate/"+p.version)
	resp.DataSourceData = c
	resp.ResourceData = c
}

func (p *codigateProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		newAgentResource,
		newAppResource,
		newEdgeRulesResource,
		newIdentityProviderResource,
		newDomainResource,
		newServiceTokenResource,
		newLogDestinationResource,
		newBrandingResource,
	}
}

func (p *codigateProvider) DataSources(context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		newWorkspaceDataSource,
		newAgentDataSource,
		newAppDataSource,
	}
}
