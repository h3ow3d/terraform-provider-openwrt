terraform {
  required_providers {
    openwrt = {
      source = "h3ow3d/openwrt"
    }
  }
}

provider "openwrt" {
  remote   = "http://192.168.20.1/cgi-bin/luci/rpc"
  user     = "root"
  password = "replace-me"
}

resource "openwrt_network" "runner_vlan" {
  name             = "runner"
  device           = "br-lan.20"
  proto            = "static"
  cidr             = "192.168.20.1/24"
  dns              = ["1.1.1.1", "1.0.0.1"]
  zone             = "runner"
  vlan_id          = 20
  parent_interface = "br-lan"
}

resource "openwrt_segment" "vault_vlan" {
  name              = "vault"
  device            = "br-lan.30"
  cidr              = "192.168.30.1/24"
  dhcp_start        = 100
  dhcp_limit        = 100
  dhcp_leasetime    = "12h"
  allow_wan_forward = false
}

resource "openwrt_dhcp_host" "runner_control_node" {
  name     = "runner-control-node"
  mac      = "dc:a6:32:12:34:56"
  ip       = "192.168.20.10"
  hostname = "runner-control-node"
}

resource "openwrt_firewall_rule" "allow_ssh_from_management" {
  name      = "allow-ssh-from-mgmt"
  src       = "management"
  dest      = "runner"
  proto     = "tcp"
  dest_port = "22"
  target    = "ACCEPT"
}
