package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/codigate/terraform-provider-codigate/internal/client"
)

var (
	_ resource.ResourceWithImportState    = (*edgeRulesResource)(nil)
	_ resource.ResourceWithValidateConfig = (*edgeRulesResource)(nil)
)

type edgeRulesResource struct{ resourceBase }

type pathRuleModel struct {
	Path   types.String `tfsdk:"path"`
	Action types.String `tfsdk:"action"`
}

type banModel struct {
	AfterRateLimits   types.Int64 `tfsdk:"after_rate_limits"`
	AfterFailedLogins types.Int64 `tfsdk:"after_failed_logins"`
	WindowSeconds     types.Int64 `tfsdk:"window_seconds"`
	DurationSeconds   types.Int64 `tfsdk:"duration_seconds"`
}

type challengeModel struct {
	Mode          types.String `tfsdk:"mode"`
	Difficulty    types.String `tfsdk:"difficulty"`
	CookieMinutes types.Int64  `tfsdk:"cookie_minutes"`
	AutoPerMinute types.Int64  `tfsdk:"auto_per_minute"`
	SkipPaths     types.Set    `tfsdk:"skip_paths"`
}

type wafPathExclusionModel struct {
	Path  types.String `tfsdk:"path"`
	Rules types.Set    `tfsdk:"rules"`
}

type wafModel struct {
	Mode           types.String            `tfsdk:"mode"`
	Exclusions     types.Set               `tfsdk:"exclusions"`
	PathExclusions []wafPathExclusionModel `tfsdk:"path_exclusions"`
}

type edgeRulesModel struct {
	ID                 types.String    `tfsdk:"id"`
	AppID              types.String    `tfsdk:"app_id"`
	AllowIPs           types.Set       `tfsdk:"allow_ips"`
	DenyIPs            types.Set       `tfsdk:"deny_ips"`
	AllowCountries     types.Set       `tfsdk:"allow_countries"`
	DenyCountries      types.Set       `tfsdk:"deny_countries"`
	PathRules          []pathRuleModel `tfsdk:"path_rules"`
	RateLimitPerMinute types.Int64     `tfsdk:"rate_limit_per_minute"`
	Ban                *banModel       `tfsdk:"ban"`
	Challenge          *challengeModel `tfsdk:"challenge"`
	Waf                *wafModel       `tfsdk:"waf"`
	AdvancedAllowed    types.Bool      `tfsdk:"advanced_allowed"`
	Inactive           types.List      `tfsdk:"inactive"`
}

func newEdgeRulesResource() resource.Resource { return &edgeRulesResource{} }

func (r *edgeRulesResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_app_edge_rules"
}

func (r *edgeRulesResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "All edge rules of one app: IP and country lists, path rules, rate limit, automatic bans, the browser challenge and the WAF. " +
			"This resource owns the app's complete rule set: anything not in the configuration is cleared, and destroying it clears all rules. " +
			"Country lists, rate limit, ban, challenge and WAF need a paid plan; on Free they are stored but listed in inactive. Import by app id or hostname.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				Description:   "Same as app_id.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"app_id": schema.StringAttribute{
				Required:      true,
				Description:   "Id of the app.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"allow_ips": schema.SetAttribute{
				ElementType: types.StringType, Optional: true,
				Description: "Only these IP addresses or CIDR ranges may connect (canonical form, at most 100).",
				Validators:  []validator.Set{canonicalIP},
			},
			"deny_ips": schema.SetAttribute{
				ElementType: types.StringType, Optional: true,
				Description: "These IP addresses or CIDR ranges are refused (canonical form, at most 100).",
				Validators:  []validator.Set{canonicalIP},
			},
			"allow_countries": schema.SetAttribute{
				ElementType: types.StringType, Optional: true,
				Description: "Only visitors from these countries (uppercase ISO codes) may connect. Paid plans.",
				Validators:  []validator.Set{countryCode},
			},
			"deny_countries": schema.SetAttribute{
				ElementType: types.StringType, Optional: true,
				Description: "Visitors from these countries are refused. Paid plans.",
				Validators:  []validator.Set{countryCode},
			},
			"path_rules": schema.ListNestedAttribute{
				Optional:    true,
				Description: "Per-path actions, checked in order (at most 50).",
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"path": schema.StringAttribute{
						Required: true, Description: "Path prefix, lowercase, starting with / (not / itself).",
						Validators: []validator.String{stringCheck{desc: "a lowercase path", check: func(s string) string {
							if !strings.HasPrefix(s, "/") || s == "/" || s != strings.ToLower(s) {
								return "Use a lowercase path prefix that starts with / and is not / itself."
							}
							return ""
						}}},
					},
					"action": schema.StringAttribute{
						Required: true, Description: "block (refuse) or login (always require sign-in, also for public apps).",
						Validators: []validator.String{oneOf("block", "login")},
					},
				}},
			},
			"rate_limit_per_minute": schema.Int64Attribute{
				Optional:    true,
				Description: "Requests per minute per IP address (1-100000). Paid plans.",
			},
			"ban": schema.SingleNestedAttribute{
				Optional:    true,
				Description: "Ban an IP address for a while after repeated rate limits or failed sign-ins. Paid plans.",
				Attributes: map[string]schema.Attribute{
					"after_rate_limits": schema.Int64Attribute{
						Optional: true, Computed: true, Default: int64default.StaticInt64(0),
						Description: "Ban after this many rate-limited requests within the window (0 = off).",
					},
					"after_failed_logins": schema.Int64Attribute{
						Optional: true, Computed: true, Default: int64default.StaticInt64(0),
						Description: "Ban after this many failed sign-ins within the window (0 = off).",
					},
					"window_seconds":   schema.Int64Attribute{Required: true, Description: "Counting window in seconds (1-3600)."},
					"duration_seconds": schema.Int64Attribute{Required: true, Description: "Ban length in seconds (1-86400)."},
				},
			},
			"challenge": schema.SingleNestedAttribute{
				Optional:    true,
				Description: "Browser challenge (proof of work) before the app. Paid plans.",
				Attributes: map[string]schema.Attribute{
					"mode": schema.StringAttribute{
						Required: true, Description: "on (always) or auto (when an IP sends more than auto_per_minute requests).",
						Validators: []validator.String{oneOf("on", "auto")},
					},
					"difficulty": schema.StringAttribute{
						Optional: true, Computed: true, Default: stringdefault.StaticString("medium"),
						Description: "low, medium or high. Defaults to medium.",
						Validators:  []validator.String{oneOf("low", "medium", "high")},
					},
					"cookie_minutes": schema.Int64Attribute{
						Optional: true, Computed: true, Default: int64default.StaticInt64(30),
						Description: "How long a solved challenge lasts (5-1440 minutes). Defaults to 30.",
						Validators:  []validator.Int64{int64Between(5, 1440)},
					},
					"auto_per_minute": schema.Int64Attribute{
						Optional: true, Description: "Requests per minute that turn the challenge on (mode auto only, 10-1000000).",
						Validators: []validator.Int64{int64Between(10, 1000000)},
					},
					"skip_paths": schema.SetAttribute{
						ElementType: types.StringType, Optional: true,
						Description: "Path prefixes where no challenge is shown, for example /api/ or /webhooks/ (lowercase, not /, at most 50). " +
							"Other edge rules, the WAF and the app's access settings still apply there.",
						Validators: []validator.Set{scopePathSet, setSize{0, client.MaxChallengeSkipPath}},
					},
				},
			},
			"waf": schema.SingleNestedAttribute{
				Optional:    true,
				Description: "Web application firewall (OWASP CRS). Leave out to turn it off. Paid plans.",
				Attributes: map[string]schema.Attribute{
					"mode": schema.StringAttribute{
						Required: true, Description: "detect (log only) or block.",
						Validators: []validator.String{oneOf("detect", "block")},
					},
					"exclusions": schema.SetAttribute{
						ElementType: types.Int64Type, Optional: true,
						Description: "CRS rule ids to skip on the whole app, for example 942100 (at most 100).",
						Validators: []validator.Set{
							int64SetRange{min: 1, max: client.MaxWafRuleID},
							setSize{0, client.MaxWafExclusions},
						},
					},
					"path_exclusions": schema.ListNestedAttribute{
						Optional: true,
						Description: "CRS attack rules to skip only under a path prefix, for example rule 942100 under /api/search " +
							"(at most 50 paths, each path once).",
						Validators: []validator.List{listSize{client.MaxWafPathExclusions}},
						NestedObject: schema.NestedAttributeObject{
							Attributes: map[string]schema.Attribute{
								"path": schema.StringAttribute{
									Required: true, Description: "Lowercase path prefix, not /.",
									Validators: []validator.String{scopePath},
								},
								"rules": schema.SetAttribute{
									ElementType: types.Int64Type, Required: true,
									Description: "CRS attack rule ids (910000-948999) to skip under the path, 1 to 20.",
									Validators: []validator.Set{
										int64SetRange{min: client.MinWafAttackRule, max: client.MaxWafAttackRule, hint: " (a CRS attack rule such as 942100)"},
										setSize{1, client.MaxWafPathRules},
									},
								},
							},
						},
					},
				},
			},
			"advanced_allowed": schema.BoolAttribute{
				Computed:    true,
				Description: "Whether the workspace plan enforces the paid parts.",
			},
			"inactive": schema.ListAttribute{
				ElementType: types.StringType, Computed: true,
				Description: "Stored parts the edge does not enforce on the current plan.",
			},
		},
	}
}

func (r *edgeRulesResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg edgeRulesModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if b := cfg.Ban; b != nil && b.AfterRateLimits.ValueInt64() == 0 && b.AfterFailedLogins.ValueInt64() == 0 &&
		!b.AfterRateLimits.IsUnknown() && !b.AfterFailedLogins.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("ban"), "Ban never triggers", "Set after_rate_limits or after_failed_logins above 0, or leave ban out.")
	}
	if c := cfg.Challenge; c != nil && !c.Mode.IsUnknown() {
		if c.Mode.ValueString() == "auto" && c.AutoPerMinute.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("challenge").AtName("auto_per_minute"), "Missing auto_per_minute", "Challenge mode auto needs auto_per_minute.")
		}
		if c.Mode.ValueString() == "on" && !c.AutoPerMinute.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root("challenge").AtName("auto_per_minute"), "Not used", "auto_per_minute only applies to challenge mode auto.")
		}
	}
	if w := cfg.Waf; w != nil {
		seenPath := map[string]bool{}
		for i, e := range w.PathExclusions {
			if e.Path.IsUnknown() || e.Path.IsNull() {
				continue
			}
			if seenPath[e.Path.ValueString()] {
				resp.Diagnostics.AddAttributeError(path.Root("waf").AtName("path_exclusions").AtListIndex(i).AtName("path"),
					"Duplicate path", "List each path once, with all its rules.")
			}
			seenPath[e.Path.ValueString()] = true
		}
	}
	seen := map[string]bool{}
	for i, p := range cfg.PathRules {
		if p.Path.IsUnknown() {
			continue
		}
		if seen[p.Path.ValueString()] {
			resp.Diagnostics.AddAttributeError(path.Root("path_rules").AtListIndex(i).AtName("path"), "Duplicate path", "Each path can have one rule.")
		}
		seen[p.Path.ValueString()] = true
	}
}

func (m *edgeRulesModel) rules(ctx context.Context, diags *diag.Diagnostics) client.EdgeRules {
	out := client.EdgeRules{
		AllowIPs:       nonEmpty(setStrings(ctx, m.AllowIPs, diags)),
		DenyIPs:        nonEmpty(setStrings(ctx, m.DenyIPs, diags)),
		AllowCountries: nonEmpty(setStrings(ctx, m.AllowCountries, diags)),
		DenyCountries:  nonEmpty(setStrings(ctx, m.DenyCountries, diags)),
		Paths:          []client.PathRule{},
	}
	for _, p := range m.PathRules {
		out.Paths = append(out.Paths, client.PathRule{Path: p.Path.ValueString(), Action: p.Action.ValueString()})
	}
	if !m.RateLimitPerMinute.IsNull() {
		out.RateLimit = &client.RateLimit{PerMinute: m.RateLimitPerMinute.ValueInt64()}
	}
	if b := m.Ban; b != nil {
		out.Ban = &client.Ban{
			AfterRateLimits: b.AfterRateLimits.ValueInt64(), AfterFailedLogins: b.AfterFailedLogins.ValueInt64(),
			WindowSeconds: b.WindowSeconds.ValueInt64(), DurationSeconds: b.DurationSeconds.ValueInt64(),
		}
	}
	if c := m.Challenge; c != nil {
		out.Challenge = &client.Challenge{Mode: c.Mode.ValueString(), Difficulty: c.Difficulty.ValueString(), CookieMinutes: c.CookieMinutes.ValueInt64()}
		if !c.AutoPerMinute.IsNull() {
			v := c.AutoPerMinute.ValueInt64()
			out.Challenge.AutoPerMinute = &v
		}
		out.Challenge.SkipPaths = nonEmpty(setStrings(ctx, c.SkipPaths, diags))
	}
	if w := m.Waf; w != nil {
		out.Waf = &client.Waf{Mode: w.Mode.ValueString(), Exclusions: []int64{}, PathExclusions: []client.WafPathExclusion{}}
		if !w.Exclusions.IsNull() && !w.Exclusions.IsUnknown() {
			diags.Append(w.Exclusions.ElementsAs(ctx, &out.Waf.Exclusions, false)...)
		}
		for _, e := range w.PathExclusions {
			x := client.WafPathExclusion{Path: e.Path.ValueString(), Rules: []int64{}}
			diags.Append(e.Rules.ElementsAs(ctx, &x.Rules, false)...)
			out.Waf.PathExclusions = append(out.Waf.PathExclusions, x)
		}
	}
	return out
}

func (m *edgeRulesModel) fill(v *client.EdgeRulesView) {
	r := v.Rules
	m.ID = types.StringValue(v.AppID)
	m.AppID = types.StringValue(v.AppID)
	m.AllowIPs = emptyIsNull(m.AllowIPs, r.AllowIPs)
	m.DenyIPs = emptyIsNull(m.DenyIPs, r.DenyIPs)
	m.AllowCountries = emptyIsNull(m.AllowCountries, r.AllowCountries)
	m.DenyCountries = emptyIsNull(m.DenyCountries, r.DenyCountries)
	if len(r.Paths) == 0 && m.PathRules == nil {
		m.PathRules = nil
	} else {
		m.PathRules = make([]pathRuleModel, 0, len(r.Paths))
		for _, p := range r.Paths {
			m.PathRules = append(m.PathRules, pathRuleModel{Path: types.StringValue(p.Path), Action: types.StringValue(p.Action)})
		}
	}
	m.RateLimitPerMinute = types.Int64Null()
	if r.RateLimit != nil {
		m.RateLimitPerMinute = types.Int64Value(r.RateLimit.PerMinute)
	}
	m.Ban = nil
	if b := r.Ban; b != nil {
		m.Ban = &banModel{
			AfterRateLimits: types.Int64Value(b.AfterRateLimits), AfterFailedLogins: types.Int64Value(b.AfterFailedLogins),
			WindowSeconds: types.Int64Value(b.WindowSeconds), DurationSeconds: types.Int64Value(b.DurationSeconds),
		}
	}
	priorSkip := types.SetNull(types.StringType)
	if m.Challenge != nil {
		priorSkip = m.Challenge.SkipPaths
	}
	m.Challenge = nil
	if c := r.Challenge; c != nil {
		m.Challenge = &challengeModel{
			Mode: types.StringValue(c.Mode), Difficulty: types.StringValue(c.Difficulty),
			CookieMinutes: types.Int64Value(c.CookieMinutes), AutoPerMinute: types.Int64Null(),
			SkipPaths: emptyIsNull(priorSkip, c.SkipPaths),
		}
		if c.AutoPerMinute != nil {
			m.Challenge.AutoPerMinute = types.Int64Value(*c.AutoPerMinute)
		}
	}
	priorWaf := m.Waf
	m.Waf = nil
	if w := r.Waf; w != nil && w.Mode != "" && w.Mode != "off" {
		m.Waf = &wafModel{Mode: types.StringValue(w.Mode), Exclusions: types.SetNull(types.Int64Type)}
		if len(w.Exclusions) > 0 || (priorWaf != nil && !priorWaf.Exclusions.IsNull()) {
			m.Waf.Exclusions = int64Set(w.Exclusions)
		}
		// An empty list in the config stays [] in state; left out it stays null.
		if len(w.PathExclusions) > 0 || (priorWaf != nil && priorWaf.PathExclusions != nil) {
			m.Waf.PathExclusions = make([]wafPathExclusionModel, 0, len(w.PathExclusions))
			for _, e := range w.PathExclusions {
				m.Waf.PathExclusions = append(m.Waf.PathExclusions, wafPathExclusionModel{
					Path: types.StringValue(e.Path), Rules: int64Set(e.Rules),
				})
			}
		}
	}
	m.AdvancedAllowed = types.BoolValue(v.AdvancedAllowed)
	m.Inactive = stringList(nonEmpty(v.Inactive))
}

func (r *edgeRulesResource) apply(ctx context.Context, plan *edgeRulesModel, diags *diag.Diagnostics) bool {
	rules := plan.rules(ctx, diags)
	if diags.HasError() {
		return false
	}
	v, err := r.client.SetEdgeRules(ctx, plan.AppID.ValueString(), rules)
	if err != nil {
		apiError(diags, "set edge rules", err)
		return false
	}
	plan.fill(v)
	return true
}

func (r *edgeRulesResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan edgeRulesModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.apply(ctx, &plan, &resp.Diagnostics) {
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
}

func (r *edgeRulesResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state edgeRulesModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	v, err := r.client.EdgeRules(ctx, state.AppID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read edge rules", err)
		return
	}
	state.fill(v)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *edgeRulesResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan edgeRulesModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if r.apply(ctx, &plan, &resp.Diagnostics) {
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}
}

func (r *edgeRulesResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state edgeRulesModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if _, err := r.client.SetEdgeRules(ctx, state.AppID.ValueString(), client.EdgeRules{}); err != nil && !client.IsNotFound(err) {
		apiError(&resp.Diagnostics, "clear edge rules", err)
	}
}

func (r *edgeRulesResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
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
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("app_id"), id)...)
}
