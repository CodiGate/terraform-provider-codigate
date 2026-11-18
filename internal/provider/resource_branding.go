package provider

import (
	"context"
	"encoding/base64"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/codigate/terraform-provider-codigate/internal/client"
)

var _ resource.ResourceWithImportState = (*brandingResource)(nil)

const brandingID = "workspace"

type brandingResource struct{ resourceBase }

type brandingModel struct {
	ID      types.String `tfsdk:"id"`
	Accent  types.String `tfsdk:"accent"`
	Logo    types.String `tfsdk:"logo"`
	LogoURL types.String `tfsdk:"logo_data_url"`
}

func newBrandingResource() resource.Resource { return &brandingResource{} }

func (r *brandingResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_workspace_branding"
}

func (r *brandingResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Logo and accent colour on the workspace's login pages. One per workspace; destroying it removes the branding. Paid plans.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: `Always "workspace".`, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"accent": schema.StringAttribute{
				Optional: true, Description: "Accent colour, lowercase #rrggbb. Leave out for the default.",
				Validators: []validator.String{accentColor},
			},
			"logo": schema.StringAttribute{
				Optional: true, Description: `Logo image (PNG, JPEG, WebP or SVG, at most 256 KB), base64. Use filebase64("logo.svg").`,
				Validators: []validator.String{stringCheck{desc: "base64", check: func(s string) string {
					if _, err := base64.StdEncoding.DecodeString(s); err != nil {
						return "The logo must be base64, for example filebase64(\"logo.png\")."
					}
					return ""
				}}},
			},
			"logo_data_url": schema.StringAttribute{Computed: true, Description: "The stored logo as a data: URL."},
		},
	}
}

func (m *brandingModel) fill(b *client.Branding) {
	m.ID = types.StringValue(brandingID)
	m.Accent = strPtrValue(b.Accent)
	m.LogoURL = strPtrValue(b.Logo)
	if b.Logo == nil {
		m.Logo = types.StringNull()
		return
	}
	stored := (*b.Logo)[strings.Index(*b.Logo, ",")+1:]
	if m.Logo.IsNull() || m.Logo.IsUnknown() || m.Logo.ValueString() != stored {
		m.Logo = types.StringValue(stored)
	}
}

func (r *brandingResource) write(ctx context.Context, plan *brandingModel, hadLogo bool) (*client.Branding, error) {
	body := map[string]any{"accent": optString(plan.Accent)}
	if s := optString(plan.Logo); s != nil {
		body["logo"] = *s
	} else if hadLogo {
		body["remove_logo"] = true
	}
	return r.client.SetBranding(ctx, body)
}

func (r *brandingResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan brandingModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	b, err := r.write(ctx, &plan, true)
	if err != nil {
		apiError(&resp.Diagnostics, "set branding", err)
		return
	}
	plan.fill(b)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *brandingResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state brandingModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	b, err := r.client.Branding(ctx)
	if err != nil {
		apiError(&resp.Diagnostics, "read branding", err)
		return
	}
	state.fill(b)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *brandingResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state brandingModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	b, err := r.write(ctx, &plan, !state.Logo.IsNull())
	if err != nil {
		apiError(&resp.Diagnostics, "set branding", err)
		return
	}
	plan.fill(b)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *brandingResource) Delete(ctx context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	if err := r.client.DeleteBranding(ctx); err != nil && !client.IsNotFound(err) {
		apiError(&resp.Diagnostics, "remove branding", err)
	}
}

func (r *brandingResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if req.ID != brandingID {
		resp.Diagnostics.AddError("Unexpected import id", `Import the workspace branding with the id "workspace".`)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), brandingID)...)
}
