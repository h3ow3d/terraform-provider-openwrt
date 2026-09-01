# openwrt_dhcp_host

`openwrt_dhcp_host` manages a DHCP host reservation in OpenWrt.

## Example

```hcl
resource "openwrt_dhcp_host" "runner_control_node" {
  name     = "runner-control-node"
  mac      = "dc:a6:32:12:34:56"
  ip       = "192.168.20.10"
  hostname = "runner-control-node"
}
```

## Behavior

This resource manages an isolated block in `/etc/config/dhcp`, commits the `dhcp` package, and restarts `dnsmasq`.
