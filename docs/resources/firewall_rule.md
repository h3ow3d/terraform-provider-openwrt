# openwrt_firewall_rule

Manages a firewall rule in `/etc/config/firewall`.

## Example Usage

```hcl
resource "openwrt_firewall_rule" "allow_ssh_from_management" {
  name      = "allow-ssh-from-mgmt"
  src       = "management"
  dest      = "runner"
  proto     = "tcp"
  dest_port = "22"
  target    = "ACCEPT"
}
```

## Argument Reference

- `name` (Required) Rule name.
- `src` (Required) Source firewall zone.
- `dest` (Optional) Destination firewall zone.
- `proto` (Optional) Protocol value. Defaults to `tcp udp`.
- `dest_port` (Optional) Destination port(s), e.g. `22` or `80 443`.
- `target` (Optional) Rule target (`ACCEPT`, `REJECT`, `DROP`). Defaults to `ACCEPT`.
- `family` (Optional) Address family (`ipv4`, `ipv6`, or `any`).

## Attributes Reference

- `id` Resource id (computed as `name`).

## Behavior

- Writes a managed `config rule` block to `/etc/config/firewall`.
- Runs `uci commit firewall`.
- Restarts `firewall`.

## Import

```bash
terraform import openwrt_firewall_rule.allow_ssh_from_management allow-ssh-from-mgmt
```
