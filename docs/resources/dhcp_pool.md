# openwrt_dhcp_pool

Manages a DHCP scope in `/etc/config/dhcp` for a target interface.

## Example Usage

```hcl
resource "openwrt_dhcp_pool" "runner_pool" {
  name      = "runner"
  interface = "runner"
  start     = 100
  limit     = 100
  leasetime = "12h"
}
```

## Argument Reference

- `name` (Required) DHCP section name.
- `interface` (Required) OpenWrt network interface name this pool serves.
- `start` (Required) Start offset in DHCP range.
- `limit` (Required) Number of leases in the pool.
- `leasetime` (Optional) Lease duration. Defaults to `12h`.

## Attributes Reference

- `id` Resource id (computed as `name`).

## Behavior

- Writes a managed `config dhcp` block to `/etc/config/dhcp`.
- Runs `uci commit dhcp`.
- Restarts `dnsmasq`.

## Import

```bash
terraform import openwrt_dhcp_pool.runner_pool runner
```
