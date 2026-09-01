# openwrt_wireguard_interface

Manages a WireGuard interface section in `/etc/config/network`.

## Example Usage

```hcl
resource "openwrt_wireguard_interface" "wg0" {
  name         = "wg0"
  private_key  = var.wg_private_key
  listen_port  = 51820
  addresses    = ["203.0.113.1/24"]
}
```

## Argument Reference

- `name` (Required) Interface name.
- `private_key` (Required, Sensitive) Base64 WireGuard private key.
- `listen_port` (Optional) UDP listen port.
- `addresses` (Optional) List of CIDRs to assign to the interface.

## Attributes Reference

- `id` Resource id (computed as `name`).

## Behavior

- Writes managed `config interface` block with `proto 'wireguard'` in `/etc/config/network`.
- Runs `uci commit network`.
- Restarts `network`.

## Import

```bash
terraform import openwrt_wireguard_interface.wg0 wg0
```
