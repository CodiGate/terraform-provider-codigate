package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/codigate/terraform-provider-codigate/internal/client"
)

var (
	_ resource.ResourceWithImportState = (*domainResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*domainResource)(nil)
)

type domainResource struct{ resourceBase }

type domainModel struct {
	ID             types.String `tfsdk:"id"`
	Name           types.String `tfsdk:"name"`
	Verify         types.Bool   `tfsdk:"verify"`
	Verified       types.Bool   `tfsdk:"verified"`
	VerifiedAt     types.String `tfsdk:"verified_at"`
	ExpiresAt      types.String `tfsdk:"expires_at"`
	ChallengeType  types.String `tfsdk:"challenge_type"`
	ChallengeName  types.String `tfsdk:"challenge_name"`
	ChallengeHost  types.String `tfsdk:"challenge_host"`
	ChallengeValue types.String `tfsdk:"challenge_value"`
}

func newDomainResource() resource.Resource { return &domainResource{} }

func (r *domainResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_domain"
}

func (r *domainResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		Description: "A custom domain claimed by the workspace. Publish the TXT record from challenge_name/challenge_value, then set verify = true (or verify in the dashboard). Apps on the domain and its subdomains need it verified.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "Domain id.", PlanModifiers: keep},
			"name": schema.StringAttribute{
				Required:      true,
				Description:   "Domain name, for example example.com. Changing it replaces the domain.",
				Validators:    []validator.String{lowercaseHostname},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"verify": schema.BoolAttribute{
				Optional: true, Computed: true, Default: booldefault.StaticBool(false),
				Description: "Check the TXT record on every apply while the domain is unverified. Apply succeeds with a warning when the record is not found yet.",
			},
			"verified":        schema.BoolAttribute{Computed: true, Description: "Whether the domain is verified."},
			"verified_at":     schema.StringAttribute{Computed: true, Description: "Verification time (RFC 3339)."},
			"expires_at":      schema.StringAttribute{Computed: true, Description: "When an unverified claim is dropped (null once verified)."},
			"challenge_type":  schema.StringAttribute{Computed: true, Description: "DNS record type to publish (TXT).", PlanModifiers: keep},
			"challenge_name":  schema.StringAttribute{Computed: true, Description: "Full name of the TXT record.", PlanModifiers: keep},
			"challenge_host":  schema.StringAttribute{Computed: true, Description: "Name of the TXT record relative to the domain.", PlanModifiers: keep},
			"challenge_value": schema.StringAttribute{Computed: true, Description: "Value of the TXT record.", PlanModifiers: keep},
		},
	}
}

// ModifyPlan keeps verified and its time stable unless an apply will try to verify.
func (r *domainResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var plan, state domainModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if state.Verified.ValueBool() || !plan.Verify.ValueBool() {
		plan.Verified, plan.VerifiedAt, plan.ExpiresAt = state.Verified, state.VerifiedAt, state.ExpiresAt
	} else {
		plan.Verified, plan.VerifiedAt, plan.ExpiresAt = types.BoolUnknown(), types.StringUnknown(), types.StringUnknown()
	}
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

func (m *domainModel) fill(d *client.Domain) {
	m.ID = types.StringValue(d.ID)
	m.Name = types.StringValue(d.Name)
	m.Verified = types.BoolValue(d.Verified)
	m.VerifiedAt = strPtrValue(d.VerifiedAt)
	m.ExpiresAt = strPtrValue(d.ExpiresAt)
	m.ChallengeType = types.StringValue(d.Challenge.Type)
	m.ChallengeName = types.StringValue(d.Challenge.Name)
	m.ChallengeHost = types.StringValue(d.Challenge.Host)
	m.ChallengeValue = types.StringValue(d.Challenge.Value)
	if m.Verify.IsNull() || m.Verify.IsUnknown() {
		m.Verify = types.BoolValue(false)
	}
}

func (r *domainResource) verify(ctx context.Context, m *domainModel, d *client.Domain, warn func(string, string), errf func(string, error)) *client.Domain {
	if d.Verified || !m.Verify.ValueBool() {
		return d
	}
	v, err := r.client.VerifyDomain(ctx, d.ID)
	if client.IsValidation(err) {
		// The API answers 422 while the TXT record is missing or wrong: keep the claim and retry next apply.
		warn("Domain not verified yet", strings.TrimPrefix(err.Error(), "codigate API: ")+"\nPublish a TXT record "+d.Challenge.Name+" with value "+d.Challenge.Value+", then apply again.")
		return d
	}
	if err != nil {
		errf("verify domain", err)
		return d
	}
	if !v.Verified {
		warn("Domain not verified yet", "Publish a TXT record "+v.Challenge.Name+" with value "+v.Challenge.Value+", then apply again.")
	}
	return v
}

func (r *domainResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan domainModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, err := r.client.CreateDomain(ctx, plan.Name.ValueString())
	if err != nil {
		apiError(&resp.Diagnostics, "add domain", err)
		return
	}
	d = r.verify(ctx, &plan, d, resp.Diagnostics.AddWarning, func(a string, e error) { apiError(&resp.Diagnostics, a, e) })
	plan.fill(d)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *domainResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state domainModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, err := r.client.Domain(ctx, state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read domain", err)
		return
	}
	state.fill(d)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *domainResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan domainModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, err := r.client.Domain(ctx, plan.ID.ValueString())
	if err != nil {
		apiError(&resp.Diagnostics, "read domain", err)
		return
	}
	d = r.verify(ctx, &plan, d, resp.Diagnostics.AddWarning, func(a string, e error) { apiError(&resp.Diagnostics, a, e) })
	plan.fill(d)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *domainResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state domainModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteDomain(ctx, state.ID.ValueString()); err != nil && !client.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete domain", err)
	}
}

// ImportState accepts a domain id (dom_...) or the domain name.
func (r *domainResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id, err := importID(req.ID, "dom_", func() (string, error) {
		ds, err := r.client.Domains(ctx)
		if err != nil {
			return "", err
		}
		name := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(req.ID)), ".")
		for _, d := range ds {
			if d.Name == name {
				return d.ID, nil
			}
		}
		return "", &client.Error{Status: 404, Message: "no domain named " + name}
	})
	if err != nil {
		apiError(&resp.Diagnostics, "import domain", err)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}
