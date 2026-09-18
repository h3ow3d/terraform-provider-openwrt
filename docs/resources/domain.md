# openwrt_domain

Manages a static DNS record (`config domain`) in `/etc/config/dhcp`.

## Example Usage

```hcl
resource "openwrt_domain" "application" {
  name = "app.example.invalid"
  ip   = "192.0.2.10"
}
```

## Argument Reference

- `name` (Required) Domain name/FQDN. Canonicalized to lowercase without a trailing dot.
- `ip` (Required) Destination IP (IPv4 or IPv6).

## Attributes Reference

- `id` Resource id (canonical domain name).

## Behavior

- Manages a deterministic named `config domain` section in `dhcp` package through HTTP ubus UCI methods.
- Section identity uses a bounded readable prefix plus deterministic hash derived from canonical domain name.
- Applies staged changes with `uci.apply` (`rollback=true`) and confirms only after health/read-back checks succeed.
- Uses live UCI reads for drift/absence detection (`result:[0]` with no payload is treated as absent section).
- Changing `name` requires replacement; changing `ip` updates in place.

## Import

```bash
terraform import openwrt_domain.application app.example.invalid
```
