package provider

import (
	"context"

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
	_ resource.ResourceWithImportState = (*agentResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*agentResource)(nil)
)

type agentResource struct{ resourceBase }

type agentModel struct {
	ID            types.String `tfsdk:"id"`
	Name          types.String `tfsdk:"name"`
	Token         types.String `tfsdk:"token"`
	RotateTrigger types.String `tfsdk:"rotate_trigger"`
	CreatedAt     types.String `tfsdk:"created_at"`
}

func newAgentResource() resource.Resource { return &agentResource{} }

func (r *agentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_agent"
}

func (r *agentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A Codigate agent: the outbound connector that runs next to your apps. The agent token is only known to Terraform when it creates or rotates the agent.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Agent id.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:    true,
				Description: "Agent name (at most 100 characters).",
				Validators:  []validator.String{stringCheck{desc: "1-100 characters", check: lengthCheck(1, 100)}},
			},
			"token": schema.StringAttribute{
				Computed:      true,
				Sensitive:     true,
				Description:   "Agent token (cga_...), set as CODIGATE_TOKEN on the agent. Only known after create or rotate; null after import.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"rotate_trigger": schema.StringAttribute{
				Optional:    true,
				Description: "Change this value (for example a date) to rotate the agent token in place. The old token stops working immediately. After terraform import the first apply only records the configured value; it does not rotate.",
			},
			"created_at": schema.StringAttribute{
				Computed:      true,
				Description:   "Creation time (RFC 3339).",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
		},
	}
}

func (r *agentResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var plan, state agentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if rotates(plan, state) {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("token"), types.StringUnknown())...)
	}
}

// rotates reports whether the plan rotates the token. An imported agent has neither a trigger nor
// a token in state; its first plan adopts the configured trigger without rotating, so importing a
// running agent doesn't disconnect it. Rotate it afterwards by changing the trigger again.
func rotates(plan, state agentModel) bool {
	if plan.RotateTrigger.Equal(state.RotateTrigger) {
		return false
	}
	return !(state.RotateTrigger.IsNull() && state.Token.IsNull())
}

func (m *agentModel) fill(a *client.Agent) {
	m.ID = types.StringValue(a.ID)
	m.Name = types.StringValue(a.Name)
	m.CreatedAt = strPtrValue(a.CreatedAt)
}

func (r *agentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan agentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	a, token, err := r.client.CreateAgent(ctx, plan.Name.ValueString())
	if err != nil {
		apiError(&resp.Diagnostics, "create agent", err)
		return
	}
	plan.fill(a)
	plan.Token = types.StringValue(token)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *agentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state agentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	a, err := r.client.Agent(ctx, state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read agent", err)
		return
	}
	state.fill(a)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *agentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state agentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	a, err := r.client.Agent(ctx, id)
	if !plan.Name.Equal(state.Name) {
		a, err = r.client.RenameAgent(ctx, id, plan.Name.ValueString())
	}
	if err != nil {
		apiError(&resp.Diagnostics, "update agent", err)
		return
	}
	plan.Token = state.Token
	if rotates(plan, state) {
		var token string
		a, token, err = r.client.RotateAgent(ctx, id)
		if err != nil {
			apiError(&resp.Diagnostics, "rotate agent token", err)
			return
		}
		plan.Token = types.StringValue(token)
	}
	plan.fill(a)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *agentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state agentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteAgent(ctx, state.ID.ValueString()); err != nil && !client.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete agent", err)
	}
}

func (r *agentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("token"), types.StringNull())...)
}
