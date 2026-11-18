// Command terraform-provider-codigate is the Terraform provider for Codigate.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/codigate/terraform-provider-codigate/internal/provider"
)

// Registry docs in docs/ come from the schema descriptions and examples/:
//go:generate go run github.com/hashicorp/terraform-plugin-docs/cmd/tfplugindocs@v0.25.0 generate --provider-name codigate

// version is set by goreleaser.
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run with support for debuggers like delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/codigate/codigate",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err.Error())
	}
}
