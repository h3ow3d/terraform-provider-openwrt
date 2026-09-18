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

resource "openwrt_dhcp_host" "probe" {
  name     = "tf-provider-host-probe"
  hostname = "tf-provider-host-probe.invalid"
  ip       = "192.0.2.10"
  dns      = true
}

resource "openwrt_dhcp_pool" "probe" {
  name      = "tf-provider-pool-probe"
  interface = "lan"
  start     = 100
  limit     = 50
  leasetime = "12h"
  force     = true
  dhcpv6    = "disabled"
  ra        = "disabled"
}
