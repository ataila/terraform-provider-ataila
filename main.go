// Copyright (c) 2026 Macskásy Attila
// SPDX-License-Identifier: MPL-2.0

// Command terraform-provider-ataila is the ATAILA Cloud Platform provider for
// OpenTofu and Terraform.
package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/ataila/terraform-provider-ataila/internal/provider"
)

// Documentation: `go generate ./...` renders docs/ from the schema, examples/
// and templates/.
//go:generate go tool tfplugindocs generate --provider-name ataila --rendered-provider-name ATAILA

// version is set by the release build (-X main.version=...).
var version = "0.3.0"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run the provider with support for debuggers such as delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address:         "registry.terraform.io/ataila/ataila",
		Debug:           debug,
		ProtocolVersion: 6,
	})
	if err != nil {
		log.Fatal(err.Error())
	}
}
