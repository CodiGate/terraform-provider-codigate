package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/codigate/terraform-provider-codigate/internal/client"
)

var (
	_ datasource.DataSourceWithConfigure      = (*workspaceDataSource)(nil)
	_ datasource.DataSourceWithValidateConfig = (*agentDataSource)(nil)
	_ datasource.DataSourceWithValidateConfig = (*appDataSource)(nil)
	_ datasource.DataSourceWithValidateConfig = (*identityProviderDataSource)(nil)
)

// codigate_workspace

type workspaceDataSource struct{ dataSourceBase }

type workspaceModel struct {
	ID        types.String `tfsdk:"id"`
	Name      types.String `tfsdk:"name"`
	Slug      types.String `tfsdk:"slug"`
	AppDomain types.String `tfsdk:"app_domain"`
	Plan      types.String `tfsdk:"plan"`
	Suspended types.Bool   `tfsdk:"suspended"`
}

func newWorkspaceDataSource() datasource.DataSource { return &workspaceDataSource{} }

func (d *workspaceDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_workspace"
}

func (d *workspaceDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The workspace the API token belongs to.",
		Attributes: map[string]schema.Attribute{
			"id":         schema.StringAttribute{Computed: true, Description: "Workspace id (ws_...)."},
			"name":       schema.StringAttribute{Computed: true, Description: "Workspace name."},
			"slug":       schema.StringAttribute{Computed: true, Description: "Workspace slug."},
			"app_domain": schema.StringAttribute{Computed: true, Description: "Domain for <app>.<slug> hostnames, for example acme.codigate.app."},
			"plan":       schema.StringAttribute{Computed: true, Description: "Current plan."},
			"suspended":  schema.BoolAttribute{Computed: true, Description: "Whether the workspace is suspended."},
		},
	}
}

func (d *workspaceDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	w, err := d.client.Workspace(ctx)
	if err != nil {
		apiError(&resp.Diagnostics, "read workspace", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &workspaceModel{
		ID: types.StringValue(w.ID), Name: types.StringValue(w.Name), Slug: types.StringValue(w.Slug),
		AppDomain: types.StringValue(w.AppDomain), Plan: types.StringValue(w.Plan), Suspended: types.BoolValue(w.Suspended),
	})...)
}

// codigate_agent

type agentDataSource struct{ dataSourceBase }

type agentDataModel struct {
	ID         types.String `tfsdk:"id"`
	Name       types.String `tfsdk:"name"`
	Connected  types.Bool   `tfsdk:"connected"`
	Version    types.String `tfsdk:"version"`
	LastSeenAt types.String `tfsdk:"last_seen_at"`
	CreatedAt  types.String `tfsdk:"created_at"`
}

func newAgentDataSource() datasource.DataSource { return &agentDataSource{} }

func (d *agentDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_agent"
}

func (d *agentDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up an agent by id or by name. The token is never returned.",
		Attributes: map[string]schema.Attribute{
			"id":           schema.StringAttribute{Optional: true, Computed: true, Description: "Agent id (agt_...). Set id or name."},
			"name":         schema.StringAttribute{Optional: true, Computed: true, Description: "Agent name. Must match exactly one agent."},
			"connected":    schema.BoolAttribute{Computed: true, Description: "Whether the agent is connected now."},
			"version":      schema.StringAttribute{Computed: true, Description: "Agent version, once it has connected."},
			"last_seen_at": schema.StringAttribute{Computed: true, Description: "Last connection (RFC 3339)."},
			"created_at":   schema.StringAttribute{Computed: true, Description: "Creation time (RFC 3339)."},
		},
	}
}

func exactlyOne(a, b types.String, an, bn string, diags interface {
	AddError(string, string)
}) {
	if a.IsUnknown() || b.IsUnknown() {
		return
	}
	if a.IsNull() == b.IsNull() {
		diags.AddError("Set "+an+" or "+bn, fmt.Sprintf("Set exactly one of %s and %s.", an, bn))
	}
}

func (d *agentDataSource) ValidateConfig(ctx context.Context, req datasource.ValidateConfigRequest, resp *datasource.ValidateConfigResponse) {
	var cfg agentDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if !resp.Diagnostics.HasError() {
		exactlyOne(cfg.ID, cfg.Name, "id", "name", &resp.Diagnostics)
	}
}

func (d *agentDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg agentDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var a *client.Agent
	if !cfg.ID.IsNull() {
		var err error
		if a, err = d.client.Agent(ctx, cfg.ID.ValueString()); err != nil {
			apiError(&resp.Diagnostics, "read agent", err)
			return
		}
	} else {
		all, err := d.client.Agents(ctx)
		if err != nil {
			apiError(&resp.Diagnostics, "list agents", err)
			return
		}
		for i := range all {
			if all[i].Name == cfg.Name.ValueString() {
				if a != nil {
					resp.Diagnostics.AddAttributeError(path.Root("name"), "Ambiguous agent name", "More than one agent has this name; use id.")
					return
				}
				a = &all[i]
			}
		}
		if a == nil {
			resp.Diagnostics.AddAttributeError(path.Root("name"), "Agent not found", fmt.Sprintf("No agent is named %q.", cfg.Name.ValueString()))
			return
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &agentDataModel{
		ID: types.StringValue(a.ID), Name: types.StringValue(a.Name), Connected: types.BoolValue(a.Connected),
		Version: strPtrValue(a.Version), LastSeenAt: strPtrValue(a.LastSeenAt), CreatedAt: strPtrValue(a.CreatedAt),
	})...)
}

// codigate_app

type appDataSource struct{ dataSourceBase }

type appDataModel struct {
	ID            types.String `tfsdk:"id"`
	Hostname      types.String `tfsdk:"hostname"`
	Name          types.String `tfsdk:"name"`
	AgentID       types.String `tfsdk:"agent_id"`
	Access        types.String `tfsdk:"access"`
	IdpID         types.String `tfsdk:"identity_provider_id"`
	ServiceTokens types.Set    `tfsdk:"service_tokens"`
	BypassPaths   types.List   `tfsdk:"bypass_paths"`
	Enabled       types.Bool   `tfsdk:"enabled"`
	Live          types.Bool   `tfsdk:"live"`
	URL           types.String `tfsdk:"url"`
}

func newAppDataSource() datasource.DataSource { return &appDataSource{} }

func (d *appDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app"
}

func (d *appDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up an app by id or by hostname.",
		Attributes: map[string]schema.Attribute{
			"id":                   schema.StringAttribute{Optional: true, Computed: true, Description: "App id (app_...). Set id or hostname."},
			"hostname":             schema.StringAttribute{Optional: true, Computed: true, Description: "Public hostname of the app."},
			"name":                 schema.StringAttribute{Computed: true, Description: "App name."},
			"agent_id":             schema.StringAttribute{Computed: true, Description: "Agent that serves the app."},
			"access":               schema.StringAttribute{Computed: true, Description: "protected (sign-in required) or public."},
			"identity_provider_id": schema.StringAttribute{Computed: true, Description: "Identity provider for login apps, null for the built-in email login."},
			"service_tokens":       schema.SetAttribute{ElementType: types.StringType, Computed: true, Description: "Service token ids allowed to reach the app."},
			"bypass_paths":         schema.ListAttribute{ElementType: types.StringType, Computed: true, Description: "Paths that skip the login."},
			"enabled":              schema.BoolAttribute{Computed: true, Description: "Whether the app is enabled."},
			"live":                 schema.BoolAttribute{Computed: true, Description: "Whether the app serves traffic now."},
			"url":                  schema.StringAttribute{Computed: true, Description: "https:// URL of the app."},
		},
	}
}

func (d *appDataSource) ValidateConfig(ctx context.Context, req datasource.ValidateConfigRequest, resp *datasource.ValidateConfigResponse) {
	var cfg appDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if !resp.Diagnostics.HasError() {
		exactlyOne(cfg.ID, cfg.Hostname, "id", "hostname", &resp.Diagnostics)
	}
}

func (d *appDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg appDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var a *client.App
	var err error
	if !cfg.ID.IsNull() {
		a, err = d.client.App(ctx, cfg.ID.ValueString())
	} else {
		a, err = d.client.AppByHostname(ctx, cfg.Hostname.ValueString())
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read app", err)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &appDataModel{
		ID: types.StringValue(a.ID), Hostname: types.StringValue(a.Hostname), Name: types.StringValue(a.Name),
		AgentID: strPtrValue(a.AgentID), Access: types.StringValue(a.Access), IdpID: strPtrValue(a.IdpID),
		ServiceTokens: stringSet(a.ServiceTokens), BypassPaths: stringList(a.BypassPaths),
		Enabled: types.BoolValue(a.Enabled), Live: types.BoolValue(a.Live), URL: types.StringValue("https://" + a.Hostname),
	})...)
}

// codigate_identity_provider

type identityProviderDataSource struct{ dataSourceBase }

type identityProviderDataModel struct {
	ID              types.String `tfsdk:"id"`
	Name            types.String `tfsdk:"name"`
	Type            types.String `tfsdk:"type"`
	Issuer          types.String `tfsdk:"issuer"`
	ClientID        types.String `tfsdk:"client_id"`
	Scopes          types.List   `tfsdk:"scopes"`
	GroupsClaim     types.String `tfsdk:"groups_claim"`
	HasClientSecret types.Bool   `tfsdk:"has_client_secret"`
	RedirectURI     types.String `tfsdk:"redirect_uri"`
}

func newIdentityProviderDataSource() datasource.DataSource { return &identityProviderDataSource{} }

func (d *identityProviderDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_identity_provider"
}

func (d *identityProviderDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up an identity provider by id or by name, for example one made in the dashboard. The client secret is never returned.",
		Attributes: map[string]schema.Attribute{
			"id":                schema.StringAttribute{Optional: true, Computed: true, Description: "Identity provider id (idp_...). Set id or name."},
			"name":              schema.StringAttribute{Optional: true, Computed: true, Description: "Identity provider name. Must match exactly one identity provider."},
			"type":              schema.StringAttribute{Computed: true, Description: "oidc or email."},
			"issuer":            schema.StringAttribute{Computed: true, Description: "OIDC issuer URL (null for email)."},
			"client_id":         schema.StringAttribute{Computed: true, Description: "OIDC client id (null for email)."},
			"scopes":            schema.ListAttribute{ElementType: types.StringType, Computed: true, Description: "Extra scopes requested besides openid, email and profile."},
			"groups_claim":      schema.StringAttribute{Computed: true, Description: "ID token claim with the user's groups (null for email)."},
			"has_client_secret": schema.BoolAttribute{Computed: true, Description: "Whether a client secret is stored."},
			"redirect_uri":      schema.StringAttribute{Computed: true, Description: "Redirect URI to register at the OIDC provider (null for email)."},
		},
	}
}

func (d *identityProviderDataSource) ValidateConfig(ctx context.Context, req datasource.ValidateConfigRequest, resp *datasource.ValidateConfigResponse) {
	var cfg identityProviderDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if !resp.Diagnostics.HasError() {
		exactlyOne(cfg.ID, cfg.Name, "id", "name", &resp.Diagnostics)
	}
}

func (d *identityProviderDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg identityProviderDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var idp *client.IdentityProvider
	if !cfg.ID.IsNull() {
		var err error
		if idp, err = d.client.IdentityProvider(ctx, cfg.ID.ValueString()); err != nil {
			apiError(&resp.Diagnostics, "read identity provider", err)
			return
		}
	} else {
		all, err := d.client.IdentityProviders(ctx)
		if err != nil {
			apiError(&resp.Diagnostics, "list identity providers", err)
			return
		}
		for i := range all {
			if all[i].Name == cfg.Name.ValueString() {
				if idp != nil {
					resp.Diagnostics.AddAttributeError(path.Root("name"), "Ambiguous identity provider name", "More than one identity provider has this name; use id.")
					return
				}
				idp = &all[i]
			}
		}
		if idp == nil {
			resp.Diagnostics.AddAttributeError(path.Root("name"), "Identity provider not found", fmt.Sprintf("No identity provider is named %q.", cfg.Name.ValueString()))
			return
		}
	}
	m := identityProviderDataModel{
		ID: types.StringValue(idp.ID), Name: types.StringValue(idp.Name), Type: types.StringValue(idp.Type),
		HasClientSecret: types.BoolValue(idp.HasClientSecret),
		Issuer:          types.StringNull(), ClientID: types.StringNull(), GroupsClaim: types.StringNull(), RedirectURI: types.StringNull(),
		Scopes: types.ListNull(types.StringType),
	}
	if idp.Type != "email" {
		m.Issuer, m.ClientID = types.StringValue(idp.Issuer), types.StringValue(idp.ClientID)
		m.GroupsClaim, m.RedirectURI = types.StringValue(idp.GroupsClaim), types.StringValue(idp.RedirectURI)
		m.Scopes = stringList(idp.Scopes)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
