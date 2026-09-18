# openwrt_dhcp_host

Manages a named `config host` section in `/etc/config/dhcp` through HTTP ubus UCI operations.

## Example Usage

DNS-only host record:

```hcl
resource "openwrt_dhcp_host" "server" {
  name     = "server"
  hostname = "server.example.invalid"
  ip       = "192.0.2.20"
  dns      = true
}
```

DHCP reservation:

```hcl
resource "openwrt_dhcp_host" "workstation" {
  name     = "workstation"
  hostname = "workstation.example.invalid"
  mac      = "02:11:22:33:44:55"
  ip       = "192.0.2.21"
}
```

## Argument Reference

- `name` (Required) Stable resource identity used to derive the provider-owned UCI section key.
- `ip` (Required) Reserved or published IPv4/IPv6 address.
- `hostname` (Optional) Value for the UCI `name` option. Defaults to `name`.
- `mac` (Optional) MAC address for a DHCP reservation. Omit for a DNS-only host record.
- `duid` (Optional) DHCPv6 DUID as an even-length hexadecimal string.
- `dns` (Optional, Computed) Whether dnsmasq publishes the host in DNS. Defaults to `true`.

## Attributes Reference

- `id` Canonical resource name.

## Behavior

- Uses a deterministic provider-owned named UCI section.
- Applies staged changes with rollback enabled and confirms after health and read-back checks.
- Reads live UCI state for drift and absence detection.
- Explicitly removes `mac` and `duid` options when they are cleared.
- Changing `name` requires replacement; other fields update in place.

## Import

Import using the canonical resource name:

```bash
terraform import openwrt_dhcp_host.server server
```
