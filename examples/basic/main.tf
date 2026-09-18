terraform {
  required_providers {
    openwrt = {
      source = "registry.terraform.io/h3ow3d/openwrt"
    }
  }
}

provider "openwrt" {
}

resource "openwrt_domain" "probe" {
  name = "tf-provider-probe.invalid"
  ip   = "192.0.2.1"
}
