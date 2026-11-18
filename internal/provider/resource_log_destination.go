package provider

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/codigate/terraform-provider-codigate/internal/client"
)

var (
	_ resource.ResourceWithImportState    = (*logDestinationResource)(nil)
	_ resource.ResourceWithValidateConfig = (*logDestinationResource)(nil)
)

type logDestinationResource struct{ resourceBase }

type logDestinationModel struct {
	ID            types.String `tfsdk:"id"`
	Type          types.String `tfsdk:"type"`
	Name          types.String `tfsdk:"name"`
	Streams       types.Set    `tfsdk:"streams"`
	Enabled       types.Bool   `tfsdk:"enabled"`
	URL           types.String `tfsdk:"url"`
	AuthHeader    types.String `tfsdk:"auth_header"`
	Host          types.String `tfsdk:"host"`
	Port          types.Int64  `tfsdk:"port"`
	AppName       types.String `tfsdk:"app_name"`
	Endpoint      types.String `tfsdk:"endpoint"`
	Region        types.String `tfsdk:"region"`
	Bucket        types.String `tfsdk:"bucket"`
	Prefix        types.String `tfsdk:"prefix"`
	AccessKey     types.String `tfsdk:"access_key"`
	SecretKey     types.String `tfsdk:"secret_key"`
	CaPEM         types.String `tfsdk:"ca_pem"`
	CaSHA256      types.String `tfsdk:"ca_sha256"`
	SigningSecret types.String `tfsdk:"signing_secret"`
	Status        types.String `tfsdk:"status"`
}

// logFields lists the type-specific attributes and which type they belong to.
var logFields = map[string]string{
	"url": "https", "auth_header": "https",
	"host": "syslog", "port": "syslog", "app_name": "syslog",
	"endpoint": "s3", "region": "s3", "bucket": "s3", "prefix": "s3", "access_key": "s3", "secret_key": "s3",
}

func newLogDestinationResource() resource.Resource { return &logDestinationResource{} }

func (r *logDestinationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_log_destination"
}

func (r *logDestinationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	opt := func(desc string) schema.StringAttribute {
		return schema.StringAttribute{Optional: true, Description: desc}
	}
	resp.Schema = schema.Schema{
		Description: "Streams access and audit logs to an HTTPS webhook, a syslog server (TLS) or S3-compatible storage. Paid plans.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, Description: "Destination id.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"type": schema.StringAttribute{
				Required: true, Description: "https, syslog or s3. Changing it replaces the destination.",
				Validators:    []validator.String{oneOf("https", "syslog", "s3")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{Required: true, Description: "Name (at most 100 characters).", Validators: []validator.String{stringCheck{desc: "1-100 characters", check: lengthCheck(1, 100)}}},
			"streams": schema.SetAttribute{
				ElementType: types.StringType, Required: true, Description: "Which logs to send: access, audit or both.",
				Validators: []validator.Set{setCheck{desc: "access or audit", check: func(s string) string {
					if s != "access" && s != "audit" {
						return fmt.Sprintf("%q is not access or audit", s)
					}
					return ""
				}}},
			},
			"enabled":     schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), Description: "Whether logs are sent (default true)."},
			"url":         opt("https: webhook URL (https://...)."),
			"auth_header": schema.StringAttribute{Optional: true, Sensitive: true, Description: `https: extra header on one line, "Header-Name: value". Never returned by the API.`},
			"host":        opt("syslog: host name."),
			"port": schema.Int64Attribute{
				Optional: true, Computed: true, Description: "syslog: TCP port for syslog over TLS (default 6514).",
				Validators:    []validator.Int64{int64Between(1, 65535)},
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			},
			"app_name":   opt("syslog: APP-NAME field (default codigate)."),
			"endpoint":   opt("s3: endpoint URL (https://...)."),
			"region":     opt("s3: region."),
			"bucket":     opt("s3: bucket."),
			"prefix":     opt("s3: key prefix."),
			"access_key": opt("s3: access key id."),
			"secret_key": schema.StringAttribute{Optional: true, Sensitive: true, Description: "s3: secret access key. Never returned by the API."},
			"ca_pem":     opt("Private CA (PEM) to trust for the destination's TLS certificate."),
			"ca_sha256":  schema.StringAttribute{Computed: true, Description: "SHA-256 of the stored CA."},
			"signing_secret": schema.StringAttribute{
				Computed: true, Sensitive: true, Description: "https: secret that signs each request (Codigate-Signature). Only known after create.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"status": schema.StringAttribute{Computed: true, Description: "Delivery status."},
		},
	}
}

func (r *logDestinationResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg logDestinationModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || cfg.Type.IsUnknown() || cfg.Type.IsNull() {
		return
	}
	typ := cfg.Type.ValueString()
	set := cfg.fields()
	for name, owner := range logFields {
		if owner != typ && set[name] {
			resp.Diagnostics.AddAttributeError(path.Root(name), "Not used for "+typ, fmt.Sprintf("%s only applies to type %s.", name, owner))
		}
	}
	required := map[string][]string{"https": {"url"}, "syslog": {"host"}, "s3": {"endpoint", "region", "bucket", "access_key", "secret_key"}}
	for _, name := range required[typ] {
		if !set[name] {
			resp.Diagnostics.AddAttributeError(path.Root(name), "Missing "+name, fmt.Sprintf("A %s destination needs %s.", typ, name))
		}
	}
}

// fields reports which type-specific attributes are set (unknown counts as set).
func (m *logDestinationModel) fields() map[string]bool {
	return map[string]bool{
		"url": !m.URL.IsNull(), "auth_header": !m.AuthHeader.IsNull(),
		"host": !m.Host.IsNull(), "port": !m.Port.IsNull(), "app_name": !m.AppName.IsNull(),
		"endpoint": !m.Endpoint.IsNull(), "region": !m.Region.IsNull(), "bucket": !m.Bucket.IsNull(),
		"prefix": !m.Prefix.IsNull(), "access_key": !m.AccessKey.IsNull(), "secret_key": !m.SecretKey.IsNull(),
	}
}

func (m *logDestinationModel) body(ctx context.Context, diags *diag.Diagnostics) map[string]any {
	b := map[string]any{
		"name":    m.Name.ValueString(),
		"streams": setStrings(ctx, m.Streams, diags),
		"enabled": m.Enabled.ValueBool(),
	}
	switch m.Type.ValueString() {
	case "https":
		b["url"] = m.URL.ValueString()
	case "syslog":
		b["host"] = m.Host.ValueString()
		if !m.Port.IsNull() && !m.Port.IsUnknown() {
			b["port"] = m.Port.ValueInt64()
		}
		b["app_name"] = optString(m.AppName)
	case "s3":
		b["endpoint"] = m.Endpoint.ValueString()
		b["region"] = m.Region.ValueString()
		b["bucket"] = m.Bucket.ValueString()
		b["prefix"] = optString(m.Prefix)
		b["access_key"] = m.AccessKey.ValueString()
	}
	return b
}

func configString(cfg map[string]any, key string) types.String {
	if v, ok := cfg[key]; ok && v != nil {
		return types.StringValue(fmt.Sprint(v))
	}
	return types.StringNull()
}

func pemHash(pem string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(pem)))
	return hex.EncodeToString(sum[:])
}

func (m *logDestinationModel) fill(d *client.LogDestination) {
	m.ID = types.StringValue(d.ID)
	m.Type = types.StringValue(d.Type)
	m.Name = types.StringValue(d.Name)
	m.Streams = stringSet(d.Streams)
	m.Enabled = types.BoolValue(d.Enabled)
	m.Status = types.StringValue(d.Status)
	m.CaSHA256 = strPtrValue(d.CaSHA256)
	m.URL, m.Host, m.AppName = configString(d.Config, "url"), configString(d.Config, "host"), configString(d.Config, "app_name")
	m.Endpoint, m.Region, m.Bucket = configString(d.Config, "endpoint"), configString(d.Config, "region"), configString(d.Config, "bucket")
	m.Prefix, m.AccessKey = configString(d.Config, "prefix"), configString(d.Config, "access_key")
	m.Port = types.Int64Null()
	if p, ok := d.Config["port"].(float64); ok {
		m.Port = types.Int64Value(int64(p))
	}
	// Secrets are never returned: keep what Terraform set while the API says one is stored.
	if !d.HasAuthHeader || m.AuthHeader.IsUnknown() {
		m.AuthHeader = types.StringNull()
	}
	if !d.HasSecretKey || m.SecretKey.IsUnknown() {
		m.SecretKey = types.StringNull()
	}
	if d.CaSHA256 == nil || m.CaPEM.IsNull() || m.CaPEM.IsUnknown() || pemHash(m.CaPEM.ValueString()) != *d.CaSHA256 {
		m.CaPEM = types.StringNull()
	}
	if d.SigningSecret != nil {
		m.SigningSecret = types.StringValue(*d.SigningSecret)
	} else if m.SigningSecret.IsUnknown() {
		m.SigningSecret = types.StringNull()
	}
}

func (r *logDestinationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan logDestinationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	b := plan.body(ctx, &resp.Diagnostics)
	b["type"] = plan.Type.ValueString()
	if s := optString(plan.AuthHeader); s != nil {
		b["auth_header"] = *s
	}
	if s := optString(plan.SecretKey); s != nil {
		b["secret_key"] = *s
	}
	if s := optString(plan.CaPEM); s != nil {
		b["ca_pem"] = *s
	}
	d, err := r.client.CreateLogDestination(ctx, b)
	if err != nil {
		apiError(&resp.Diagnostics, "create log destination", err)
		return
	}
	plan.fill(d)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *logDestinationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state logDestinationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	d, err := r.client.LogDestination(ctx, state.ID.ValueString())
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiError(&resp.Diagnostics, "read log destination", err)
		return
	}
	state.fill(d)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *logDestinationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state logDestinationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	b := plan.body(ctx, &resp.Diagnostics)
	switch {
	case plan.AuthHeader.IsNull() && !state.AuthHeader.IsNull():
		b["clear_auth_header"] = true
	case !plan.AuthHeader.IsNull() && !plan.AuthHeader.Equal(state.AuthHeader):
		b["auth_header"] = plan.AuthHeader.ValueString()
	}
	if !plan.SecretKey.IsNull() && !plan.SecretKey.Equal(state.SecretKey) {
		b["secret_key"] = plan.SecretKey.ValueString()
	}
	switch {
	case plan.CaPEM.IsNull() && !state.CaSHA256.IsNull():
		b["ca_pem"] = ""
	case !plan.CaPEM.IsNull() && !plan.CaPEM.Equal(state.CaPEM):
		b["ca_pem"] = plan.CaPEM.ValueString()
	}
	plan.SigningSecret = state.SigningSecret
	d, err := r.client.UpdateLogDestination(ctx, plan.ID.ValueString(), b)
	if err != nil {
		apiError(&resp.Diagnostics, "update log destination", err)
		return
	}
	plan.fill(d)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *logDestinationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state logDestinationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteLogDestination(ctx, state.ID.ValueString()); err != nil && !client.IsNotFound(err) {
		apiError(&resp.Diagnostics, "delete log destination", err)
	}
}

func (r *logDestinationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
