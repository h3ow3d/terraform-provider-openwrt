terraform {
  required_providers {
    openwrt = {
      source = "h3ow3d/openwrt"
    }
  }
}

provider "openwrt" {
  remote   = "http://192.0.2.1"
  user     = "root"
  password = "replace-me"
}

resource "openwrt_network" "runner_vlan" {
  name             = "runner"
  device           = "br-lan.20"
  proto            = "static"
  cidr             = "192.0.2.1/24"
  dns              = ["1.1.1.1", "1.0.0.1"]
  zone             = "runner"
  vlan_id          = 20
  parent_interface = "br-lan"
}

resource "openwrt_device" "runner_bridge" {
  name  = "br-vlan20"
  type  = "bridge"
  ports = ["lan2"]
}

resource "openwrt_segment" "vault_vlan" {
  name              = "vault"
  device            = "br-lan.30"
  cidr              = "198.51.100.1/24"
  dhcp_start        = 100
  dhcp_limit        = 100
  dhcp_leasetime    = "12h"
  allow_wan_forward = false
}

resource "openwrt_dhcp_host" "host_a" {
  name     = "host-a"
  mac      = "02:11:22:33:44:55"
  ip       = "192.0.2.10"
  hostname = "host-a"
}

resource "openwrt_domain" "grafana_runner" {
  name = "grafana.runner.ophomelab.internal"
  ip   = "192.0.2.10"
}

resource "openwrt_firewall_rule" "allow_ssh_from_management" {
  name      = "allow-ssh-from-mgmt"
  src       = "management"
  dest      = "runner"
  proto     = "tcp"
  dest_port = "22"
  target    = "ACCEPT"
}
