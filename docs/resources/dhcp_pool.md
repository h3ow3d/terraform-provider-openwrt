# openwrt_dhcp_pool

Manages a DHCP scope (`config dhcp`) in `/etc/config/dhcp` through HTTP ubus UCI operations.

## Example Usage

```hcl
resource "openwrt_dhcp_pool" "segment_a" {
  name      = "segment-a"
  interface = "br-lan.20"
  start     = 100
  limit     = 50

  # Optional values shown explicitly.
  leasetime = "12h"
  force     = true
  dhcpv6    = "disabled"
  ra        = "disabled"
}
```

## Argument Reference

- `name` (Required) Stable resource identity used to derive the provider-owned UCI section key.
- `interface` (Required) OpenWrt interface name the pool serves.
- `start` (Required) Start offset in the subnet.
- `limit` (Required) Pool size.
- `leasetime` (Optional, Computed) Lease duration. Defaults to `12h`.
- `force` (Optional, Computed) Whether dnsmasq serves DHCP on this network regardless of interface state. Defaults to `true`.
- `dhcpv6` (Optional, Computed) DHCPv6 mode. Defaults to `disabled`.
- `ra` (Optional, Computed) Router-advertisement mode. Defaults to `disabled`.

## Attributes Reference

- `id` Canonical resource name.

## Behavior

- Uses a deterministic provider-owned named UCI section.
- Applies staged changes with rollback enabled and confirms after health and read-back checks.
- Reads live UCI state for drift and absence detection.
- Changing `name` requires replacement; all other fields update in place.

## Import

Import using the canonical resource name:

```bash
terraform import openwrt_dhcp_pool.segment_a segment-a
```
