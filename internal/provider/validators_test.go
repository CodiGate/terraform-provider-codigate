package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func TestNotEmptySet(t *testing.T) {
	cases := map[string]struct {
		v       types.Set
		wantErr bool
	}{
		"null":    {types.SetNull(types.StringType), false},
		"unknown": {types.SetUnknown(types.StringType), false},
		"empty":   {types.SetValueMust(types.StringType, []attr.Value{}), true},
		"one":     {types.SetValueMust(types.StringType, []attr.Value{types.StringValue("/healthz")}), false},
	}
	for name, c := range cases {
		resp := &validator.SetResponse{}
		notEmptySet{}.ValidateSet(context.Background(), validator.SetRequest{ConfigValue: c.v}, resp)
		if got := resp.Diagnostics.HasError(); got != c.wantErr {
			t.Errorf("%s: error = %v, want %v", name, got, c.wantErr)
		}
	}
}

func TestScopePath(t *testing.T) {
	cases := map[string]bool{
		"/api/":              true,
		"/api/v1/search":     true,
		"/webhooks/stripe-1": true,
		"/a:b@c":             true,
		"/":                  false,
		"api":                false,
		"/API":               false,
		"//api":              false,
		"/a//b":              false,
		"/a/./b":             false,
		"/a/../b":            false,
		"/a b":               false,
		"/a?b":               false,
		"/a%20":              false,
		"/a*":                false,
		"/a;b":               false,
		"/a'b":               false,
		"/.codigate/x":       false,
	}
	for p, ok := range cases {
		if got := scopePathMsg(p) == ""; got != ok {
			t.Errorf("%q: valid = %v, want %v (%s)", p, got, ok, scopePathMsg(p))
		}
	}
}

func TestAlertURL(t *testing.T) {
	cases := map[string]bool{
		"https://hooks.example.com/codigate":   true,
		"https://ntfy.sh/acme-alerts":          true,
		"https://example.com:8443/x?token=abc": true,
		"":                                     false,
		" https://example.com":                 false,
		"https://example.com ":                 false,
		"http://example.com":                   false,
		"HTTPS://example.com":                  false,
		"https://":                             false,
		"https:///path":                        false,
		"https://exa mple.com":                 false,
		"ftp://example.com":                    false,
		"https://example.com/" + strings.Repeat("a", 1981): false,
		"https://example.com/" + strings.Repeat("a", 1980): true,
	}
	for u, ok := range cases {
		if got := alertURLMsg(u) == ""; got != ok {
			t.Errorf("%q: valid = %v, want %v (%s)", u, got, ok, alertURLMsg(u))
		}
	}
}
