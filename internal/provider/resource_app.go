package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/codigate/terraform-provider-codigate/internal/client"
)

var (
	_ resource.ResourceWithImportState    = (*appResource)(nil)
	_ resource.ResourceWithValidateConfig = (*appResource)(nil)
)

type appResource struct{ resourceBase }

type policyModel struct {
	Everyone     types.Bool `tfsdk:"everyone"`
	Emails       types.Set  `tfsdk:"emails"`
	EmailDomains types.Set  `tfsdk:"email_domains"`
	Groups       types.Set  `tfsdk:"groups"`
}

type appModel struct {
	ID                 types.String `tfsdk:"id"`
	Name               types.String `tfsdk:"name"`
	Hostname           types.String `tfsdk:"hostname"`
	AgentID            types.String `tfsdk:"agent_id"`
	Enabled            types.Bool   `tfsdk:"enabled"`
	Access             types.String `tfsdk:"access"`
	IdentityProviderID types.String `tfsdk:"identity_provider_id"`
	Policy             *policyModel `tfsdk:"policy"`
	ServiceTokenIDs    types.Set    `tfsdk:"service_token_ids"`
	BypassPaths        types.Set    `tfsdk:"bypass_paths"`
	CreatedAt          types.String `tfsdk:"created_at"`
}

func newAppResource() resource.Resource { return &appResource{} }

func (r *appResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app"
}

func (r *appResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "An app: a hostname Codigate serves through an agent, with who may reach it. Import by id (app_...) or by hostname.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "App id.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Display name (at most 100 characters).",
				Validators:  []validator.String{stringCheck{desc: "1-100 characters", check: lengthCheck(1, 100)}},
			},
			"hostname": schema.StringAttribute{
				Required:    true,
				Description: "Public hostname, lowercase without a trailing dot. It must be under the workspace's app domain or a verified custom domain.",
				Validators:  []validator.String{lowercaseHostname},
			},
			"agent_id": schema.StringAttribute{
				Required:    true,
				Description: "Id of the agent that reaches the origin.",
			},
			"enabled": schema.BoolAttribute{
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(true),
				Description: "Whether the app serves traffic. Defaults to true.",
			},
			"access": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString("protected"),
				Description: "protected (sign-in through identity_provider_id and policy) or public (no sign-in; paid plans with a verified custom domain). Defaults to protected.",
				Validators:  []validator.String{oneOf("protected", "public")},
			},
			"identity_provider_id": schema.StringAttribute{
				Optional:    true,
				Description: "Identity provider users sign in with. Required together with policy for protected access; an app without one is not live yet.",
			},
			"policy": schema.SingleNestedAttribute{
				Optional:    true,
				Description: "Who may sign in. At least one rule must allow someone.",
				Attributes: map[string]schema.Attribute{
					"everyone": schema.BoolAttribute{
						Optional:    true,
						Computed:    true,
						Default:     booldefault.StaticBool(false),
						Description: "Allow everyone who can sign in at the identity provider.",
					},
					"emails": schema.SetAttribute{
						ElementType: types.StringType,
						Optional:    true,
						Description: "Allowed email addresses (lowercase).",
						Validators:  []validator.Set{lowercaseSet},
					},
					"email_domains": schema.SetAttribute{
						ElementType: types.StringType,
						Optional:    true,
						Description: "Allowed email domains (lowercase), for example example.com.",
						Validators:  []validator.Set{lowercaseSet},
					},
					"groups": schema.SetAttribute{
						ElementType: types.StringType,
						Optional:    true,
						Description: "Allowed groups from the identity provider's groups claim (OIDC only).",
					},
				},
			},
			"service_token_ids": schema.SetAttribute{
				ElementType: types.StringType,
				Optional:    true,
				Description: "Service tokens that may call the app without signing in (protected access only).",
			},
			"bypass_paths": schema.SetAttribute{
				ElementType: types.StringType,
				Optional:    true,
				Validators:  []validator.Set{notEmptySet{}},
				Description: "Path prefixes served without sign-in, for example /healthz (at most 20, protected access only; leave it out for none; paid plans with a verified custom domain covering the hostname).",
			},
			"created_at": schema.StringAttribute{
				Computed:      true,
				Description:   "Creation time (RFC 3339).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *appResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg appModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if cfg.Access.ValueString() == "public" {
		for _, a := range []struct {
			name string
			set  bool
		}{
			{"identity_provider_id", !cfg.IdentityProviderID.IsNull()},
			{"policy", cfg.Policy != nil},
			{"service_token_ids", !cfg.ServiceTokenIDs.IsNull()},
			{"bypass_paths", !cfg.BypassPaths.IsNull()},
		} {
			if a.set {
				resp.Diagnostics.AddAttributeError(path.Root(a.name), "Not used for public apps", a.name+" only applies to protected access; a public app has no sign-in.")
			}
		}
		return
	}
	if !cfg.IdentityProviderID.IsNull() && cfg.Policy == nil {
		resp.Diagnostics.AddAttributeError(path.Root("policy"), "Missing policy", "A protected app with an identity provider needs a policy saying who may sign in.")
	}
	if cfg.IdentityProviderID.IsNull() && cfg.Policy != nil {
		resp.Diagnostics.AddAttributeError(path.Root("identity_provider_id"), "Missing identity provider", "A policy needs identity_provider_id.")
	}
}

func (m *appModel) fill(a *client.App) {
	m.ID = types.StringValue(a.ID)
	m.Name = types.StringValue(a.Name)
	m.Hostname = types.StringValue(a.Hostname)
	m.AgentID = strPtrValue(a.AgentID)
	m.Enabled = types.BoolValue(a.Enabled)
	m.Access = types.StringValue(a.Access)
	m.IdentityProviderID = strPtrValue(a.IdpID)
	m.CreatedAt = strPtrValue(a.CreatedAt)
	if a.Policy == nil {
		m.Policy = nil
	} else {
		prior := m.Policy
		if prior == nil {
			prior = &policyModel{Emails: types.SetNull(types.StringType), EmailDomains: types.SetNull(types.StringType), Groups: types.SetNull(types.StringType)}
		}
		m.Policy = &policyModel{
			Everyone:     types.BoolValue(a.Policy.Everyone),
			Emails:       emptyIsNull(prior.Emails, a.Policy.Emails),
			EmailDomains: emptyIsNull(prior.EmailDomains, a.Policy.EmailDomains),
			Groups:       emptyIsNull(prior.Groups, a.Policy.Groups),
		}
	}
	m.ServiceTokenIDs = emptyIsNull(m.ServiceTokenIDs, a.ServiceTokens)
	m.BypassPaths = emptyIsNull(m.BypassPaths, a.BypassPaths)
}

// accessBody is the PUT /apps/{id}/access body for the planned access; ok is false when there is nothing to send.
func (m *appModel) accessBody(ctx context.Context, diags *diag.Diagnostics) (client.AppAccess, bool) {
	if m.Access.ValueString() == "public" {
		// The API takes absent or array service_tokens and bypass_paths, never null.
		return client.AppAccess{Access: "public", ServiceTokens: []string{}, BypassPaths: []string{}}, true
	}
	if m.IdentityProviderID.IsNull() {
		return client.AppAccess{}, false
	}
	body := client.AppAccess{
		Access:        "protected",
		IdpID:         optString(m.IdentityProviderID),
		ServiceTokens: nonEmpty(setStrings(ctx, m.ServiceTokenIDs, diags)),
		BypassPaths:   nonEmpty(setStrings(ctx, m.BypassPaths, diags)),
	}
	if m.Policy != nil {
		body.Policy = &client.Policy{
			Everyone:     m.Policy.Everyone.ValueBool(),
			Emails:       nonEmpty(setStrings(ctx, m.Policy.Emails, diags)),
			EmailDomains: nonEmpty(setStrings(ctx, m.Policy.EmailDomains, diags)),
			Groups:       nonEmpty(setStrings(ctx, m.Policy.Groups, diags)),
		}
	}
	return body, true
}

func (r *appResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan appModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	a, err := r.client.CreateApp(ctx, plan.Name.ValueString(), plan.Hostname.ValueString(), plan.AgentID.ValueString())
	if err != nil {
		apiError(&resp.Diagnostics, "create app", err)
		return
	}
	// Keep the app in state from here on, so a failing access or enabled call does not orphan it.
	plan.ID = types.StringValue(a.ID)
	if body, ok := plan.accessBody(ctx, &resp.Diagnostics); ok && !resp.Diagnostics.HasError() {
		if a, err = r.client.SetAppAccess(ctx, a.ID, body); err != nil {
			r.saveAfterFailure(ctx, &plan, resp, "set app access", err)
			return
		}
	}
	if !plan.Enabled.IsNull() && plan.Enabled.ValueBool() != a.Enabled {
		if a, err = r.client.SetAppEnabled(ctx, plan.ID.ValueString(), plan.Enabled.ValueBool()); err != nil {
			r.saveAfterFailure(ctx, &plan, resp, "set app enabled", err)
			return
		}
	}
	plan.fill(a)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *appResource) saveAfterFailure(ctx context.Context, plan *appModel, resp *resource.CreateResponse, action string, err error) {
	apiError(&resp.Diagnostics, action, err)
	if fresh, gerr := r.client.App(ctx, plan.ID.ValueString()); gerr == nil {
		plan.fill(fresh)
		resp.Diagnostics.Append(resp.State.Set(ctx, plan)...)
	}
}

func (r *appResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state appModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	a, err := r.client.App(ctx, state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read app", err)
		return
	}
	state.fill(a)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *appResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state appModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	plan.ID = state.ID

	changes := map[string]any{}
	if !plan.Name.Equal(state.Name) {
		changes["name"] = plan.Name.ValueString()
	}
	if !plan.Hostname.Equal(state.Hostname) {
		changes["hostname"] = plan.Hostname.ValueString()
	}
	if !plan.AgentID.Equal(state.AgentID) {
		changes["agent_id"] = plan.AgentID.ValueString()
	}
	if len(changes) > 0 {
		if _, err := r.client.UpdateApp(ctx, id, changes); err != nil {
			apiError(&resp.Diagnostics, "update app", err)
			return
		}
	}

	accessChanged := !plan.Access.Equal(state.Access) || !plan.IdentityProviderID.Equal(state.IdentityProviderID) ||
		!policyEqual(plan.Policy, state.Policy) || !plan.ServiceTokenIDs.Equal(state.ServiceTokenIDs) || !plan.BypassPaths.Equal(state.BypassPaths)
	if accessChanged {
		body, ok := plan.accessBody(ctx, &resp.Diagnostics)
		if resp.Diagnostics.HasError() {
			return
		}
		if !ok {
			resp.Diagnostics.AddAttributeError(path.Root("identity_provider_id"), "Cannot remove the identity provider",
				"The API cannot take the identity provider off a protected app. Set another identity provider, make the app public, or replace the app.")
			return
		}
		if _, err := r.client.SetAppAccess(ctx, id, body); err != nil {
			apiError(&resp.Diagnostics, "set app access", err)
			return
		}
	}
	if !plan.Enabled.Equal(state.Enabled) {
		if _, err := r.client.SetAppEnabled(ctx, id, plan.Enabled.ValueBool()); err != nil {
			apiError(&resp.Diagnostics, "set app enabled", err)
			return
		}
	}
	a, err := r.client.App(ctx, id)
	if err != nil {
		apiError(&resp.Diagnostics, "read app", err)
		return
	}
	plan.fill(a)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func policyEqual(a, b *policyModel) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Everyone.Equal(b.Everyone) && a.Emails.Equal(b.Emails) && a.EmailDomains.Equal(b.EmailDomains) && a.Groups.Equal(b.Groups)
}

func (r *appResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state appModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteApp(ctx, state.ID.ValueString()); err != nil && !client.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete app", err)
	}
}

// ImportState accepts an app id or a hostname.
func (r *appResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := req.ID
	if strings.Contains(id, ".") {
		a, err := r.client.AppByHostname(ctx, id)
		if err != nil {
			apiError(&resp.Diagnostics, "find app by hostname", err)
			return
		}
		id = a.ID
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}
