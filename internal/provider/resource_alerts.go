package provider

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/codigate/terraform-provider-codigate/internal/client"
)

var (
	_ resource.ResourceWithImportState = (*alertsResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*alertsResource)(nil)
)

const alertsID = "workspace"

type alertsResource struct{ resourceBase }

type alertsModel struct {
	ID            types.String `tfsdk:"id"`
	WebhookURL    types.String `tfsdk:"webhook_url"`
	NtfyURL       types.String `tfsdk:"ntfy_url"`
	SigningSecret types.String `tfsdk:"signing_secret"`
	HasSecret     types.Bool   `tfsdk:"has_secret"`
	Allowed       types.Bool   `tfsdk:"allowed"`
}

func newAlertsResource() resource.Resource { return &alertsResource{} }

func (r *alertsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_workspace_alerts"
}

// alertURLMsg mirrors the API's rules for webhook_url and ntfy_url (SetAlertChannels::urlRules):
// an https URL with a host, at most 2000 characters. The API trims spaces and treats "" as
// "clear", so both are rejected here: leave the attribute out instead.
func alertURLMsg(s string) string {
	switch {
	case s == "":
		return "Must not be empty; leave the attribute out to turn the channel off."
	case strings.TrimSpace(s) != s:
		return "Must not start or end with spaces."
	case len([]rune(s)) > client.AlertURLMax:
		return fmt.Sprintf("Must be at most %d characters long.", client.AlertURLMax)
	case !strings.HasPrefix(s, "https://"):
		return "Must be an https:// URL."
	case strings.ContainsAny(s, " \t\r\n"):
		return "Must be a valid URL (no spaces)."
	}
	if u, err := url.Parse(s); err != nil || u.Hostname() == "" {
		return "Must be a valid https URL with a host name."
	}
	return ""
}

var alertURL = stringCheck{desc: "an https:// URL, at most 2000 characters", check: alertURLMsg}

func (r *alertsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Extra channels for the workspace's origin health alerts: a signed webhook and an ntfy topic. " +
			"Email to the admins needs no setup. One per workspace; destroying it turns both channels off. " +
			"Adding a channel needs the Starter plan or higher; removing one works on every plan.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: `Always "workspace".`, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"webhook_url": schema.StringAttribute{
				Optional: true, Sensitive: true,
				Description: "Webhook that receives each alert as JSON, signed like log export (X-Codigate-Signature header). https:// only, at most 2000 characters; the host must be public. Leave out for no webhook.",
				Validators:  []validator.String{alertURL},
			},
			"ntfy_url": schema.StringAttribute{
				Optional: true, Sensitive: true,
				Description: "ntfy topic URL, for example https://ntfy.sh/<topic>. https:// only, at most 2000 characters; the host must be public. Leave out for no ntfy alerts.",
				Validators:  []validator.String{alertURL},
			},
			"signing_secret": schema.StringAttribute{
				Computed: true, Sensitive: true,
				Description: "Secret that signs each webhook request. Only known when Terraform added the webhook (the API returns it once); null after an import. It stays the same when the webhook URL changes and is removed with the webhook.",
			},
			"has_secret": schema.BoolAttribute{Computed: true, Description: "Whether the workspace has a webhook signing secret."},
			"allowed":    schema.BoolAttribute{Computed: true, Description: "Whether the workspace's plan allows webhook and ntfy alerts."},
		},
	}
}

// ModifyPlan predicts signing_secret and has_secret: a new webhook gets a new secret, a changed
// URL keeps it, no webhook means no secret.
func (r *alertsResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan alertsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var state *alertsModel
	if !req.State.Raw.IsNull() {
		state = &alertsModel{}
		resp.Diagnostics.Append(req.State.Get(ctx, state)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	plan.Allowed = types.BoolUnknown()
	if state != nil && !state.Allowed.IsNull() {
		plan.Allowed = state.Allowed
	}
	switch {
	case plan.WebhookURL.IsUnknown():
		plan.SigningSecret, plan.HasSecret = types.StringUnknown(), types.BoolUnknown()
	case plan.WebhookURL.IsNull():
		plan.SigningSecret, plan.HasSecret = types.StringNull(), types.BoolValue(false)
	case state != nil && state.HasSecret.ValueBool():
		plan.SigningSecret, plan.HasSecret = state.SigningSecret, types.BoolValue(true)
	default:
		plan.SigningSecret, plan.HasSecret = types.StringUnknown(), types.BoolValue(true)
	}
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

func (m *alertsModel) fill(a *client.Alerts) {
	m.ID = types.StringValue(alertsID)
	m.WebhookURL = strPtrValue(a.WebhookURL)
	m.NtfyURL = strPtrValue(a.NtfyURL)
	m.HasSecret = types.BoolValue(a.HasSecret)
	m.Allowed = types.BoolValue(a.Allowed)
	switch {
	case a.SigningSecret != nil:
		m.SigningSecret = types.StringValue(*a.SigningSecret)
	case !a.HasSecret || m.SigningSecret.IsUnknown():
		m.SigningSecret = types.StringNull()
	}
}

func (r *alertsResource) write(ctx context.Context, plan *alertsModel, diags *diag.Diagnostics) {
	a, err := r.client.SetAlerts(ctx, optString(plan.WebhookURL), optString(plan.NtfyURL))
	if err != nil {
		apiError(diags, "set alert channels", err)
		return
	}
	plan.fill(a)
}

func (r *alertsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan alertsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.write(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *alertsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state alertsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	a, err := r.client.Alerts(ctx)
	if err != nil {
		apiError(&resp.Diagnostics, "read alert channels", err)
		return
	}
	state.fill(a)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *alertsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan alertsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	r.write(ctx, &plan, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete turns both channels off (the API default); email alerts to the admins stay.
func (r *alertsResource) Delete(ctx context.Context, _ resource.DeleteRequest, resp *resource.DeleteResponse) {
	if _, err := r.client.SetAlerts(ctx, nil, nil); err != nil && !client.IsNotFound(err) {
		apiError(&resp.Diagnostics, "turn alert channels off", err)
	}
}

func (r *alertsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if req.ID != alertsID {
		resp.Diagnostics.AddError("Unexpected import id", `Import the workspace alert channels with the id "workspace".`)
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), alertsID)...)
}
