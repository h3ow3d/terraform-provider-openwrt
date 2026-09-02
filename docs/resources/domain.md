# openwrt_domain

Manages a static DNS record (`config domain`) in `/etc/config/dhcp`.

## Example Usage

```hcl
resource "openwrt_domain" "grafana_runner" {
  name = "grafana.runner.ophomelab.internal"
  ip   = "192.0.2.10"
}
```

## Argument Reference

- `name` (Required) Domain name/FQDN.
- `ip` (Required) Destination IP (IPv4 or IPv6).

## Attributes Reference

- `id` Resource id (computed as `name`).

## Behavior

- Writes a managed `config domain` block to `/etc/config/dhcp`.
- Runs `uci commit dhcp`.
- Restarts `dnsmasq`.

## Import

```bash
terraform import openwrt_domain.grafana_runner grafana.runner.ophomelab.internal
```
