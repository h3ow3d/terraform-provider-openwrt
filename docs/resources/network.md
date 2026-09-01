# openwrt_network

`openwrt_network` describes an OpenWrt network interface and optional VLAN metadata.

## Example

```hcl
resource "openwrt_network" "runner_vlan" {
  name             = "runner"
  device           = "br-lan.20"
  proto            = "static"
  cidr             = "192.168.20.1/24"
  gateway          = "192.168.20.1"
  dns              = ["1.1.1.1", "1.0.0.1"]
  vlan_id          = 20
  parent_interface = "br-lan"
  zone             = "runner"
}
```

## Behavior

This resource manages an isolated block in `/etc/config/network`, commits the `network` package, and restarts the `network` service.
