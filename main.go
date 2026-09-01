package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/h3ow3d/terraform-provider-openwrt/internal/provider"
)

var version = "dev"

func main() {
	var debug bool

	flag.BoolVar(&debug, "debug", false, "set to true to run the provider with support for debuggers like delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.terraform.io/h3ow3d/openwrt",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err.Error())
	}
}
