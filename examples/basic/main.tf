terraform {
  required_providers {
    openwrt = {
      source  = "registry.terraform.io/h3ow3d/openwrt"
      version = "0.2.3"
    }
  }
}

provider "openwrt" {
}

resource "openwrt_device" "probe" {
  name  = "br-vlan20"
  type  = "bridge"
  ports = ["lan2"]
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

resource "openwrt_network" "probe" {
  name      = "vlan30"
  device    = "br-vlan20"
  proto     = "static"
  cidr      = "192.168.30.1/24"
  delegate  = false
  ip6assign = 60
  dns       = ["192.168.30.1"]
}

resource "openwrt_firewall_rule" "probe" {
  name      = "allow-dns"
  src       = "runner"
  dest      = "wan"
  target    = "ACCEPT"
  proto     = "udp"
  dest_port = "53"
  family    = "ipv4"
}

resource "openwrt_interface" "probe" {
  name      = "vlan20"
  device    = "br-vlan20"
  proto     = "static"
  cidr      = "192.168.20.1/24"
  delegate  = false
  ip6assign = 60
  dns       = ["192.168.20.1"]
}

resource "openwrt_wireguard_interface" "probe" {
  name        = "wg_runner"
  private_key = "base64-test-private-key="
  listen_port = 51820
  addresses   = ["10.42.0.1/24"]
}

resource "openwrt_wireguard_peer" "probe" {
  name                 = "actions"
  interface            = openwrt_wireguard_interface.probe.name
  public_key           = "base64-test-public-key="
  allowed_ips          = ["10.1.0.1/32"]
  endpoint_host        = "vpn.example.net"
  endpoint_port        = 51820
  persistent_keepalive = 25
}
