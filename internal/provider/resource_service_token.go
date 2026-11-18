package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/codigate/terraform-provider-codigate/internal/client"
)

var (
	_ resource.ResourceWithImportState    = (*serviceTokenResource)(nil)
	_ resource.ResourceWithValidateConfig = (*serviceTokenResource)(nil)
)

type serviceTokenResource struct{ resourceBase }

type serviceTokenModel struct {
	ID                   types.String `tfsdk:"id"`
	Name                 types.String `tfsdk:"name"`
	Type                 types.String `tfsdk:"type"`
	ExpiresInDays        types.Int64  `tfsdk:"expires_in_days"`
	Certificate          types.String `tfsdk:"certificate"`
	Secret               types.String `tfsdk:"secret"`
	GeneratedCertificate types.String `tfsdk:"generated_certificate"`
	PrivateKey           types.String `tfsdk:"private_key"`
	PKCS12               types.String `tfsdk:"pkcs12"`
	PKCS12Password       types.String `tfsdk:"pkcs12_password"`
	CertSHA256           types.String `tfsdk:"cert_sha256"`
	CertExpiresAt        types.String `tfsdk:"cert_expires_at"`
	ExpiresAt            types.String `tfsdk:"expires_at"`
	CreatedAt            types.String `tfsdk:"created_at"`
}

func newServiceTokenResource() resource.Resource { return &serviceTokenResource{} }

func (r *serviceTokenResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_service_token"
}

// replaceUnlessImported forces replacement on a change, except when the prior value is unknown
// to Terraform because the token was imported (the API does not return these inputs).
func replaceUnlessImported() planmodifier.String {
	return stringplanmodifier.RequiresReplaceIf(func(_ context.Context, req planmodifier.StringRequest, resp *stringplanmodifier.RequiresReplaceIfFuncResponse) {
		resp.RequiresReplace = !req.StateValue.IsNull()
	}, "Replaces the token unless it was imported.", "Replaces the token unless it was imported.")
}

func (r *serviceTokenResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	once := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Computed: true, Sensitive: true, Description: desc, PlanModifiers: keep}
	}
	resp.Schema = schema.Schema{
		Description: "A service token that lets machines reach protected apps without a login. Attach it to an app with codigate_app.service_tokens. Tokens cannot be changed: every change creates a new token. Secrets and generated keys are only known to Terraform when it creates the token.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "Token id (svc_...), sent as Codigate-Client-Id.", PlanModifiers: keep},
			"name": schema.StringAttribute{
				Required: true, Description: "Token name (at most 100 characters).",
				Validators:    []validator.String{stringCheck{desc: "1-100 characters", check: lengthCheck(1, 100)}},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"type": schema.StringAttribute{
				Optional: true, Computed: true, Default: stringdefault.StaticString("secret"),
				Description:   "secret (default): a client secret sent as Codigate-Client-Secret. certificate: a client certificate (mTLS).",
				Validators:    []validator.String{oneOf("secret", "certificate")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"expires_in_days": schema.Int64Attribute{
				Optional: true, Description: "Days until the token expires (1-3650). Leave out for no expiry.",
				Validators: []validator.Int64{int64Between(1, 3650)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplaceIf(func(_ context.Context, req planmodifier.Int64Request, resp *int64planmodifier.RequiresReplaceIfFuncResponse) {
					resp.RequiresReplace = !req.StateValue.IsNull()
				}, "Replaces the token unless it was imported.", "Replaces the token unless it was imported.")},
			},
			"certificate": schema.StringAttribute{
				Optional: true, Description: "For type certificate: your own client certificate (PEM). Leave out to have Codigate generate a key and certificate.",
				PlanModifiers: []planmodifier.String{replaceUnlessImported()},
			},
			"secret":                once("Client secret (cgs_...) for type secret."),
			"generated_certificate": once("For type certificate: the client certificate (PEM)."),
			"private_key":           once("For a generated certificate: the private key (PEM)."),
			"pkcs12":                once("For a generated certificate: certificate and key as PKCS#12, base64."),
			"pkcs12_password":       once("Password of pkcs12."),
			"cert_sha256":           schema.StringAttribute{Computed: true, Description: "SHA-256 fingerprint of the certificate.", PlanModifiers: keep},
			"cert_expires_at":       schema.StringAttribute{Computed: true, Description: "Certificate expiry (RFC 3339).", PlanModifiers: keep},
			"expires_at":            schema.StringAttribute{Computed: true, Description: "Token expiry (RFC 3339), null for none.", PlanModifiers: keep},
			"created_at":            schema.StringAttribute{Computed: true, Description: "Creation time (RFC 3339).", PlanModifiers: keep},
		},
	}
}

func (r *serviceTokenResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg serviceTokenModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || cfg.Type.IsUnknown() || cfg.Certificate.IsNull() {
		return
	}
	if cfg.Type.ValueString() != "certificate" {
		resp.Diagnostics.AddAttributeError(path.Root("certificate"), "Certificate needs type certificate", `Set type = "certificate" to use a client certificate.`)
	}
}

func (m *serviceTokenModel) fill(t *client.ServiceToken) {
	m.ID = types.StringValue(t.ID)
	m.Name = types.StringValue(t.Name)
	m.Type = types.StringValue(t.Type)
	m.CertSHA256 = strPtrValue(t.CertSHA256)
	m.CertExpiresAt = strPtrValue(t.CertExpiresAt)
	m.ExpiresAt = strPtrValue(t.ExpiresAt)
	m.CreatedAt = strPtrValue(t.CreatedAt)
	for _, f := range []*types.String{&m.Secret, &m.GeneratedCertificate, &m.PrivateKey, &m.PKCS12, &m.PKCS12Password} {
		if f.IsUnknown() {
			*f = types.StringNull()
		}
	}
}

func (r *serviceTokenResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan serviceTokenModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := map[string]any{"name": plan.Name.ValueString(), "type": plan.Type.ValueString()}
	if !plan.ExpiresInDays.IsNull() {
		body["expires_in_days"] = plan.ExpiresInDays.ValueInt64()
	}
	if s := optString(plan.Certificate); s != nil {
		body["certificate"] = *s
	}
	t, err := r.client.CreateServiceToken(ctx, body)
	if err != nil {
		apiError(&resp.Diagnostics, "create service token", err)
		return
	}
	plan.Secret = strPtrValue(t.Secret)
	plan.GeneratedCertificate = strPtrValue(t.Certificate)
	plan.PrivateKey = strPtrValue(t.PrivateKey)
	plan.PKCS12 = strPtrValue(t.PKCS12)
	plan.PKCS12Password = strPtrValue(t.PKCS12Password)
	plan.fill(t)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *serviceTokenResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state serviceTokenModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	t, err := r.client.ServiceToken(ctx, state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read service token", err)
		return
	}
	state.fill(t)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update only runs after an import, to record inputs the API does not return; tokens are never changed in place.
func (r *serviceTokenResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan serviceTokenModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *serviceTokenResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state serviceTokenModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteServiceToken(ctx, state.ID.ValueString()); err != nil && !client.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete service token", err)
	}
}

func (r *serviceTokenResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
