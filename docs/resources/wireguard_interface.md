# openwrt_wireguard_interface

Manages a named WireGuard `config interface` section in `/etc/config/network` through HTTP ubus UCI operations.

## Example Usage

```hcl
resource "openwrt_wireguard_interface" "runner" {
  name        = "wg_runner"
  private_key = "base64-test-private-key="
  listen_port = 51820
  addresses   = ["10.42.0.1/24", "fd00:42::1/64"]
}
```

## Argument Reference

- `name` (Required) UCI interface section key. Must match `^[a-z0-9_]+$`.
- `private_key` (Required, Sensitive) WireGuard private key.
- `listen_port` (Optional) UDP listen port.
- `addresses` (Optional) Interface addresses in CIDR notation.

## Attributes Reference

- `id` Canonical interface name.

## Behavior

- Uses a named `config interface` section in the `network` package.
- Enforces `proto=wireguard`.
- Applies staged changes with rollback enabled and confirms after health and read-back checks.
- Reads live UCI state for drift and absence detection.
- Clears `listen_port` and `addresses` options when removed from configuration.
- Changing `name` requires replacement; other fields update in place.

## Import

Import using the interface name:

```bash
terraform import openwrt_wireguard_interface.runner wg_runner
```
