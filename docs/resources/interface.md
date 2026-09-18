# openwrt_interface

Manages a named non-WireGuard `config interface` section in `/etc/config/network` through HTTP ubus UCI operations.

## Example Usage

```hcl
resource "openwrt_interface" "runner" {
  name      = "vlan20"
  device    = "br-vlan20"
  proto     = "static"
  cidr      = "192.168.20.1/24"
  gateway   = "192.168.20.254"
  mtu       = 1500
  delegate  = false
  ip6assign = 60
  dns       = ["192.168.20.1"]
}
```

## Argument Reference

- `name` (Required) UCI interface section key. Must match `^[a-z0-9_]+$`.
- `device` (Required) Network device assigned to the interface (for example `br-vlan20`).
- `proto` (Optional, Computed) Interface protocol. Defaults to `static`.
- `cidr` (Optional) IPv4 CIDR for static interface addressing.
- `gateway` (Optional) IPv4/IPv6 gateway.
- `mtu` (Optional) Interface MTU between `576` and `9200`.
- `delegate` (Optional, Computed) IPv6 prefix delegation toggle. Defaults to `false`.
- `ip6assign` (Optional) IPv6 delegated prefix size between `0` and `128`.
- `dns` (Optional) DNS server list attached to the interface.

## Attributes Reference

- `id` Canonical interface name.

## Behavior

- Uses a named `config interface` section in the `network` package.
- Applies staged changes with rollback enabled and confirms after health and read-back checks.
- Reads live UCI state for drift and absence detection.
- Converts `cidr` to UCI `ipaddr` and `netmask` options.
- Clears optional options (`gateway`, `mtu`, `ip6assign`, `dns`) when removed from configuration.
- Changing `name` requires replacement; all other fields update in place.

## Import

Import using the interface name:

```bash
terraform import openwrt_interface.runner vlan20
```
