package provider

import (
	"context"
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
