# openwrt_firewall_rule

Manages a provider-owned firewall `config rule` section in `/etc/config/firewall` through HTTP ubus UCI operations.

## Example Usage

```hcl
resource "openwrt_firewall_rule" "allow_dns" {
  name      = "allow-dns"
  src       = "runner"
  dest      = "wan"
  target    = "ACCEPT"
  proto     = "udp"
  dest_port = "53"
  family    = "ipv4"
  enabled   = true
}
```

## Argument Reference

- `name` (Required) Stable rule identity used to derive a provider-owned UCI section key.
- `src` (Required) Source firewall zone.
- `dest` (Optional) Destination firewall zone.
- `target` (Required) Rule action, for example `ACCEPT`, `REJECT`, or `DROP`.
- `proto` (Optional, Computed) Protocol selector. Defaults to `all`.
- `family` (Optional) Address family (`ipv4`, `ipv6`, or `any`).
- `src_port` (Optional) Source port expression.
- `dest_port` (Optional) Destination port expression.
- `enabled` (Optional, Computed) Rule enabled state. Defaults to `true`.

## Attributes Reference

- `id` Canonical rule name.

## Behavior

- Uses a deterministic provider-owned named UCI section.
- Applies staged changes with rollback enabled and confirms after health and read-back checks.
- Reads live UCI state for drift and absence detection.
- Clears optional options when removed from configuration.
- Changing `name` requires replacement; all other fields update in place.

## Import

Import using the canonical rule name:

```bash
terraform import openwrt_firewall_rule.allow_dns allow-dns
```
