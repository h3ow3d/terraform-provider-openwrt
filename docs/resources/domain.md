# openwrt_domain

Manages a static DNS record (`config domain`) in `/etc/config/dhcp`.

## Example Usage

```hcl
resource "openwrt_domain" "grafana_runner" {
  name = "monitoring.segment-a.example.internal"
  ip   = "192.0.2.10"
}
```

## Argument Reference

- `name` (Required) Domain name/FQDN.
- `ip` (Required) Destination IP (IPv4 or IPv6).

## Attributes Reference

- `id` Resource id (computed as `name`).

## Behavior

- Manages a deterministic named `config domain` section in `dhcp` package through HTTP ubus UCI methods.
- Applies staged changes with `uci.apply` (`rollback=true`) and confirms only after health/read-back checks succeed.
- Uses live UCI reads for drift/absence detection (`result:[0]` with no payload is treated as absent section).

## Import

```bash
terraform import openwrt_domain.grafana_runner monitoring.segment-a.example.internal
```
