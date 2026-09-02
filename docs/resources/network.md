# openwrt_network

`openwrt_network` describes an OpenWrt network interface and optional VLAN metadata.

## Example Usage

```hcl
resource "openwrt_network" "runner_vlan" {
  name             = "runner"
  device           = "br-lan.100"
  proto            = "static"
  cidr             = "192.0.2.1/24"
  gateway          = "192.0.2.254"
  dns              = ["1.1.1.1", "1.0.0.1"]
  vlan_id          = 20
  parent_interface = "br-lan"
  zone             = "runner"
}
```

## Argument Reference

- `name` (Required) Interface name and resource key.
- `device` (Required) OpenWrt device (for example `br-lan.100`).
- `proto` (Required) Interface protocol (`static`, `dhcp`, etc).
- `cidr` (Optional) Interface CIDR for static addressing (for example `192.0.2.1/24`).
- `gateway` (Optional) Gateway address.
- `ip6assign` (Optional) IPv6 prefix assignment length (for example `60`).
- `delegate` (Optional) Whether to delegate IPv6 prefixes (`true`/`false`).
- `dns` (Optional) List of resolver IPs.
- `mtu` (Optional) MTU value.
- `vlan_id` (Optional) VLAN id in range `1-4094`.
- `parent_interface` (Optional) Required when `vlan_id` is set.
- `zone` (Optional) Zone metadata value (currently informational in this resource path).

## Attributes Reference

- `id` Resource id (computed as `name`).

## Behavior

This resource manages an isolated block in `/etc/config/network`, commits the `network` package, and restarts the `network` service.

## Import

```bash
terraform import openwrt_network.runner runner
```

## Notes

- Validation enforces non-empty `name`, `device`, and `proto`.
- When `cidr` is supplied, it must be valid CIDR notation.
- `zone`, `vlan_id`, and `parent_interface` are accepted for model consistency, but this resource currently writes interface options only.
