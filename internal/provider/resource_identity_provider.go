package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/codigate/terraform-provider-codigate/internal/client"
)

var (
	_ resource.ResourceWithImportState    = (*identityProviderResource)(nil)
	_ resource.ResourceWithValidateConfig = (*identityProviderResource)(nil)
)

type identityProviderResource struct{ resourceBase }

type identityProviderModel struct {
	ID              types.String `tfsdk:"id"`
	Name            types.String `tfsdk:"name"`
	Type            types.String `tfsdk:"type"`
	Issuer          types.String `tfsdk:"issuer"`
	ClientID        types.String `tfsdk:"client_id"`
	ClientSecret    types.String `tfsdk:"client_secret"`
	Scopes          types.List   `tfsdk:"scopes"`
	GroupsClaim     types.String `tfsdk:"groups_claim"`
	HasClientSecret types.Bool   `tfsdk:"has_client_secret"`
	RedirectURI     types.String `tfsdk:"redirect_uri"`
}

func newIdentityProviderResource() resource.Resource { return &identityProviderResource{} }

func (r *identityProviderResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_identity_provider"
}

func (r *identityProviderResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "An identity provider that protected apps sign people in with: your own OIDC provider, or type email for one-time codes by email.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Identity provider id.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Name shown on the sign-in page (at most 100 characters).",
				Validators:  []validator.String{stringCheck{desc: "1-100 characters", check: lengthCheck(1, 100)}},
			},
			"type": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString("oidc"),
				Description:   "oidc (default) or email. Changing it replaces the identity provider.",
				Validators:    []validator.String{oneOf("oidc", "email")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"issuer": schema.StringAttribute{
				Optional:    true,
				Description: "OIDC issuer URL (https, no query or fragment). Required for oidc.",
			},
			"client_id": schema.StringAttribute{
				Optional:    true,
				Description: "OIDC client id. Required for oidc.",
			},
			"client_secret": schema.StringAttribute{
				Optional: true, Sensitive: true,
				Description: "OIDC client secret. Leave out for a public client (PKCE only). The API never returns it, so Terraform only notices a change made in Terraform.",
			},
			"scopes": schema.ListAttribute{
				ElementType: types.StringType, Optional: true,
				Description: "Extra scopes to request besides openid, email and profile (at most 20).",
			},
			"groups_claim": schema.StringAttribute{
				Optional: true, Computed: true,
				Description:   "ID token claim with the user's groups. Defaults to groups for oidc.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"has_client_secret": schema.BoolAttribute{
				Computed:    true,
				Description: "Whether a client secret is stored.",
			},
			"redirect_uri": schema.StringAttribute{
				Computed:      true,
				Description:   "Redirect URI to register at the OIDC provider (null for email).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *identityProviderResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg identityProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || cfg.Type.IsUnknown() {
		return
	}
	if cfg.Type.ValueString() == "email" {
		for name, v := range map[string]bool{
			"issuer": !cfg.Issuer.IsNull(), "client_id": !cfg.ClientID.IsNull(), "client_secret": !cfg.ClientSecret.IsNull(),
			"scopes": !cfg.Scopes.IsNull(), "groups_claim": !cfg.GroupsClaim.IsNull(),
		} {
			if v {
				resp.Diagnostics.AddAttributeError(path.Root(name), "Not used for email", name+" only applies to type oidc.")
			}
		}
		return
	}
	if cfg.Issuer.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("issuer"), "Missing issuer", "An oidc identity provider needs issuer.")
	}
	if cfg.ClientID.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("client_id"), "Missing client_id", "An oidc identity provider needs client_id.")
	}
}

func (m *identityProviderModel) isEmail() bool { return m.Type.ValueString() == "email" }

func (m *identityProviderModel) fill(idp *client.IdentityProvider) {
	m.ID = types.StringValue(idp.ID)
	m.Name = types.StringValue(idp.Name)
	m.Type = types.StringValue(idp.Type)
	m.HasClientSecret = types.BoolValue(idp.HasClientSecret)
	if idp.Type == "email" {
		m.Issuer, m.ClientID, m.ClientSecret = types.StringNull(), types.StringNull(), types.StringNull()
		m.Scopes, m.GroupsClaim, m.RedirectURI = types.ListNull(types.StringType), types.StringNull(), types.StringNull()
		return
	}
	// The API trims a trailing slash from the issuer; keep the configured form when that is the only difference.
	if m.Issuer.IsNull() || m.Issuer.IsUnknown() || strings.TrimRight(m.Issuer.ValueString(), "/") != idp.Issuer {
		m.Issuer = types.StringValue(idp.Issuer)
	}
	m.ClientID = types.StringValue(idp.ClientID)
	if !idp.HasClientSecret {
		m.ClientSecret = types.StringNull()
	} else if m.ClientSecret.IsUnknown() {
		m.ClientSecret = types.StringNull()
	}
	m.Scopes = emptyListIsNull(m.Scopes, idp.Scopes)
	m.GroupsClaim = types.StringValue(idp.GroupsClaim)
	m.RedirectURI = types.StringValue(idp.RedirectURI)
}

func (r *identityProviderResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan identityProviderModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"name": plan.Name.ValueString(), "type": plan.Type.ValueString()}
	if !plan.isEmail() {
		body["issuer"] = plan.Issuer.ValueString()
		body["client_id"] = plan.ClientID.ValueString()
		body["client_secret"] = optString(plan.ClientSecret)
		body["scopes"] = nonEmpty(listStrings(ctx, plan.Scopes, &resp.Diagnostics))
		if s := optString(plan.GroupsClaim); s != nil {
			body["groups_claim"] = *s
		}
	}
	idp, err := r.client.CreateIdentityProvider(ctx, body)
	if err != nil {
		apiError(&resp.Diagnostics, "create identity provider", err)
		return
	}
	plan.fill(idp)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *identityProviderResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state identityProviderModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	idp, err := r.client.IdentityProvider(ctx, state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read identity provider", err)
		return
	}
	state.fill(idp)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *identityProviderResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state identityProviderModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"name": plan.Name.ValueString()}
	if !plan.isEmail() {
		body["issuer"] = plan.Issuer.ValueString()
		body["client_id"] = plan.ClientID.ValueString()
		body["scopes"] = nonEmpty(listStrings(ctx, plan.Scopes, &resp.Diagnostics))
		if s := optString(plan.GroupsClaim); s != nil {
			body["groups_claim"] = *s
		}
		switch {
		case plan.ClientSecret.IsNull() && !state.ClientSecret.IsNull():
			body["remove_client_secret"] = true
		case !plan.ClientSecret.IsNull() && !plan.ClientSecret.Equal(state.ClientSecret):
			body["client_secret"] = plan.ClientSecret.ValueString()
		}
	}
	idp, err := r.client.UpdateIdentityProvider(ctx, plan.ID.ValueString(), body)
	if err != nil {
		apiError(&resp.Diagnostics, "update identity provider", err)
		return
	}
	plan.fill(idp)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *identityProviderResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state identityProviderModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteIdentityProvider(ctx, state.ID.ValueString()); err != nil && !client.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete identity provider", err)
	}
}

func (r *identityProviderResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
