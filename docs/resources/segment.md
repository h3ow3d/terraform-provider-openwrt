# openwrt_segment

Orchestrates a complete routed segment across:

- `/etc/config/network` interface
- `/etc/config/dhcp` pool
- `/etc/config/firewall` zone
- optional zone forwarding to `wan`

Each apply is done through managed blocks with commit + service restart.

## Example Usage

```hcl
resource "openwrt_segment" "runner" {
  name              = "runner"
  device            = "br-lan.100"
  cidr              = "198.51.100.1/24"
  dhcp_start        = 100
  dhcp_limit        = 100
  dhcp_leasetime    = "12h"
  allow_wan_forward = false
}
```

## Argument Reference

- `name` (Required) Segment name (used for interface and DHCP section names by default).
- `device` (Required) Interface device (for example `br-lan.100`).
- `cidr` (Required) Interface CIDR.
- `dhcp_start` (Required) DHCP start offset.
- `dhcp_limit` (Required) DHCP range size.
- `proto` (Optional) Interface protocol. Defaults to `static`.
- `zone` (Optional) Firewall zone name. Defaults to `name`.
- `dns` (Optional) DNS servers list.
- `gateway` (Optional) Gateway address.
- `ip6assign` (Optional) IPv6 prefix assignment length. Defaults to `60`.
- `delegate` (Optional) Whether to delegate IPv6 prefixes. Defaults to `false`.
- `dhcp_leasetime` (Optional) Lease duration. Defaults to `12h`.
- `dhcp_force` (Optional) DHCP force mode (`option force`). Defaults to `true`.
- `dhcpv6` (Optional) DHCPv6 mode. Defaults to `disabled`.
- `ra` (Optional) Router advertisement mode. Defaults to `disabled`.
- `allow_wan_forward` (Optional) Adds forwarding rule from segment zone to `wan`. Defaults to `false`.
- `firewall_input` (Optional) Zone input policy. Defaults to `REJECT`.
- `firewall_output` (Optional) Zone output policy. Defaults to `ACCEPT`.
- `firewall_forward` (Optional) Zone forward policy. Defaults to `REJECT`.
- `vlan_id` (Optional) VLAN id (`1-4094`).
- `parent_interface` (Optional) Required when `vlan_id` is set.

## Attributes Reference

- `id` Resource id (computed as `name`).

## Behavior

Creates/updates multiple managed blocks in one operation:

- `/etc/config/network` interface block
- `/etc/config/dhcp` pool block
- `/etc/config/firewall` zone block
- optional `/etc/config/firewall` forwarding block (`zone -> wan`)

Then commits and restarts affected services.

## Notes

- `vlan_id` and `parent_interface` are currently validated/stored for forward compatibility, but are not yet rendered into a dedicated VLAN UCI section by this resource.
- `Read` currently trusts Terraform state and does not yet perform full remote drift detection.

## Import

```bash
terraform import openwrt_segment.runner runner
```
