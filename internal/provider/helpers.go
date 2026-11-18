package provider

import (
	"context"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/codigate/terraform-provider-codigate/internal/client"
)

// clientFrom pulls the configured client out of ProviderData, reporting a wrong type.
func clientFrom(data any, diags *diag.Diagnostics) *client.Client {
	if data == nil {
		return nil
	}
	c, ok := data.(*client.Client)
	if !ok {
		diags.AddError("Unexpected provider data", fmt.Sprintf("Expected *client.Client, got %T. This is a bug in the provider.", data))
		return nil
	}
	return c
}

type resourceBase struct {
	client *client.Client
}

func (b *resourceBase) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if c := clientFrom(req.ProviderData, &resp.Diagnostics); c != nil {
		b.client = c
	}
}

type dataSourceBase struct {
	client *client.Client
}

func (b *dataSourceBase) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if c := clientFrom(req.ProviderData, &resp.Diagnostics); c != nil {
		b.client = c
	}
}

func apiError(diags *diag.Diagnostics, action string, err error) {
	diags.AddError("Codigate API error: "+action, err.Error())
}

func strPtrValue(s *string) types.String {
	if s == nil {
		return types.StringNull()
	}
	return types.StringValue(*s)
}

func optString(v types.String) *string {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	s := v.ValueString()
	return &s
}

func stringSet(values []string) types.Set {
	elems := make([]attr.Value, 0, len(values))
	for _, v := range values {
		elems = append(elems, types.StringValue(v))
	}
	return types.SetValueMust(types.StringType, elems)
}

func stringList(values []string) types.List {
	elems := make([]attr.Value, 0, len(values))
	for _, v := range values {
		elems = append(elems, types.StringValue(v))
	}
	return types.ListValueMust(types.StringType, elems)
}

// setStrings returns the set's strings sorted; nil for a null or unknown set.
func setStrings(ctx context.Context, s types.Set, diags *diag.Diagnostics) []string {
	if s.IsNull() || s.IsUnknown() {
		return nil
	}
	var out []string
	diags.Append(s.ElementsAs(ctx, &out, false)...)
	sort.Strings(out)
	return out
}

func listStrings(ctx context.Context, l types.List, diags *diag.Diagnostics) []string {
	if l.IsNull() || l.IsUnknown() {
		return nil
	}
	var out []string
	diags.Append(l.ElementsAs(ctx, &out, false)...)
	return out
}

// emptyIsNull keeps a null set in state when the API returns an empty list and the config left it out.
func emptyIsNull(prior types.Set, values []string) types.Set {
	if len(values) == 0 && prior.IsNull() {
		return types.SetNull(types.StringType)
	}
	return stringSet(values)
}

func nonEmpty(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

// stringCheck is a string validator from a function returning an error message ("" when fine).
type stringCheck struct {
	desc  string
	check func(string) string
}

func (v stringCheck) Description(context.Context) string         { return v.desc }
func (v stringCheck) MarkdownDescription(context.Context) string { return v.desc }
func (v stringCheck) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if msg := v.check(req.ConfigValue.ValueString()); msg != "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid value", msg)
	}
}

// setCheck validates every known string in a set.
type setCheck struct {
	desc  string
	check func(string) string
}

func (v setCheck) Description(context.Context) string         { return v.desc }
func (v setCheck) MarkdownDescription(context.Context) string { return v.desc }
func (v setCheck) ValidateSet(ctx context.Context, req validator.SetRequest, resp *validator.SetResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	for _, e := range req.ConfigValue.Elements() {
		s, ok := e.(types.String)
		if !ok || s.IsNull() || s.IsUnknown() {
			continue
		}
		if msg := v.check(s.ValueString()); msg != "" {
			resp.Diagnostics.AddAttributeError(req.Path, "Invalid value", msg)
		}
	}
}

// notEmptySet rejects an empty set: the API stores "none" as absent, so [] would come back as null
// after an import and show a diff. Leave the attribute out instead.
type notEmptySet struct{}

func (notEmptySet) Description(context.Context) string { return "not empty (leave it out for none)" }
func (notEmptySet) MarkdownDescription(context.Context) string {
	return "not empty (leave it out for none)"
}
func (notEmptySet) ValidateSet(ctx context.Context, req validator.SetRequest, resp *validator.SetResponse) {
	if !req.ConfigValue.IsNull() && !req.ConfigValue.IsUnknown() && len(req.ConfigValue.Elements()) == 0 {
		resp.Diagnostics.AddAttributeError(req.Path, "Empty set", "Leave the attribute out instead of setting it to [].")
	}
}

func oneOf(values ...string) stringCheck {
	return stringCheck{
		desc: "one of " + strings.Join(values, ", "),
		check: func(s string) string {
			for _, v := range values {
				if s == v {
					return ""
				}
			}
			return fmt.Sprintf("%q must be one of: %s.", s, strings.Join(values, ", "))
		},
	}
}

var hostnameRe = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,61}[a-z0-9]$`)

// lowercaseHostname rejects hostnames the API would rewrite, so the plan matches what is stored.
var lowercaseHostname = stringCheck{
	desc: "a lowercase hostname without a trailing dot",
	check: func(s string) string {
		if s != client.NormalizeHostname(s) {
			return fmt.Sprintf("Write %q as %q: hostnames are stored lowercase without a trailing dot.", s, client.NormalizeHostname(s))
		}
		if !hostnameRe.MatchString(s) {
			return fmt.Sprintf("%q is not a valid hostname.", s)
		}
		return ""
	},
}

// canonicalIP accepts an IP or CIDR only in the canonical form the API stores.
var canonicalIP = setCheck{
	desc: "IP addresses or CIDR ranges in canonical form",
	check: func(s string) string {
		if strings.Contains(s, "/") {
			p, err := netip.ParsePrefix(s)
			if err != nil {
				return fmt.Sprintf("%q is not an IP address or CIDR range.", s)
			}
			if c := p.Masked().String(); c != s {
				return fmt.Sprintf("Write %q as %q.", s, c)
			}
			return ""
		}
		a, err := netip.ParseAddr(s)
		if err != nil {
			return fmt.Sprintf("%q is not an IP address or CIDR range.", s)
		}
		if c := a.Unmap().String(); c != s {
			return fmt.Sprintf("Write %q as %q.", s, c)
		}
		return ""
	},
}

var countryRe = regexp.MustCompile(`^[A-Z]{2}$`)

var countryCode = setCheck{
	desc: "uppercase ISO 3166-1 alpha-2 country codes",
	check: func(s string) string {
		if !countryRe.MatchString(s) {
			return fmt.Sprintf("%q is not an uppercase two-letter country code (for example NL).", s)
		}
		return ""
	},
}

var lowercaseSet = setCheck{
	desc: "lowercase values",
	check: func(s string) string {
		if s != strings.ToLower(strings.TrimSpace(s)) {
			return fmt.Sprintf("Write %q as %q: the API stores it lowercase.", s, strings.ToLower(strings.TrimSpace(s)))
		}
		return ""
	},
}

var accentRe = regexp.MustCompile(`^#[0-9a-f]{6}$`)

var accentColor = stringCheck{
	desc: "a lowercase #rrggbb colour",
	check: func(s string) string {
		if !accentRe.MatchString(s) {
			return fmt.Sprintf("%q must be a lowercase #rrggbb colour.", s)
		}
		return ""
	},
}

func lengthCheck(min, max int) func(string) string {
	return func(s string) string {
		if n := len([]rune(s)); n < min || n > max {
			return fmt.Sprintf("Must be %d to %d characters long.", min, max)
		}
		return ""
	}
}

func int64Set(values []int64) types.Set {
	elems := make([]attr.Value, 0, len(values))
	for _, v := range values {
		elems = append(elems, types.Int64Value(v))
	}
	return types.SetValueMust(types.Int64Type, elems)
}

// emptyListIsNull is emptyIsNull for lists.
func emptyListIsNull(prior types.List, values []string) types.List {
	if len(values) == 0 && prior.IsNull() {
		return types.ListNull(types.StringType)
	}
	return stringList(values)
}

// importID returns id when it has the prefix, otherwise the id that lookup finds (by name).
func importID(id, prefix string, lookup func() (string, error)) (string, error) {
	if strings.HasPrefix(id, prefix) {
		return id, nil
	}
	return lookup()
}

type int64Range struct{ min, max int64 }

func int64Between(min, max int64) int64Range { return int64Range{min, max} }

func (v int64Range) Description(context.Context) string {
	return fmt.Sprintf("between %d and %d", v.min, v.max)
}
func (v int64Range) MarkdownDescription(ctx context.Context) string { return v.Description(ctx) }
func (v int64Range) ValidateInt64(ctx context.Context, req validator.Int64Request, resp *validator.Int64Response) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if n := req.ConfigValue.ValueInt64(); n < v.min || n > v.max {
		resp.Diagnostics.AddAttributeError(req.Path, "Value out of range", fmt.Sprintf("Must be %s, got %d.", v.Description(ctx), n))
	}
}
