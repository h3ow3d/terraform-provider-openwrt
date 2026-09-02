# openwrt_dhcp_host

`openwrt_dhcp_host` manages a DHCP host reservation in OpenWrt.

## Example Usage

```hcl
resource "openwrt_dhcp_host" "host_a" {
  name     = "host-a"
  mac      = "02:11:22:33:44:55"
  ip       = "192.0.2.10"
  hostname = "host-a"
}
```

## Argument Reference

- `name` (Required) Reservation name.
- `mac` (Required) MAC address (`aa:bb:cc:dd:ee:ff`).
- `ip` (Required) Reserved IPv4 address.
- `duid` (Optional) DHCPv6 DUID.
- `hostname` (Optional) DNS name for this host. If set, provider writes this to `option name`.
- `dns` (Optional) Whether to publish local DNS record (`option dns`). Defaults to `true`.

## Attributes Reference

- `id` Resource id (computed as `name`).

## Behavior

This resource manages an isolated block in `/etc/config/dhcp`, commits the `dhcp` package, and restarts `dnsmasq`.

## Import

```bash
terraform import openwrt_dhcp_host.host_a host-a
```

## Notes

- MAC format and IPv4 format are validated before apply.
