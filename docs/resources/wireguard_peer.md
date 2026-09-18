# openwrt_wireguard_peer

Manages a provider-owned WireGuard peer section in `/etc/config/network` through HTTP ubus UCI operations.

## Example Usage

```hcl
resource "openwrt_wireguard_peer" "actions" {
  name                 = "actions"
  interface            = "wg_runner"
  public_key           = "base64-test-public-key="
  preshared_key        = "base64-test-preshared-key="
  allowed_ips          = ["10.1.0.1/32"]
  endpoint_host        = "vpn.example.net"
  endpoint_port        = 51820
  persistent_keepalive = 25
  description          = "GitHub Actions tunnel peer"
}
```

## Argument Reference

- `name` (Required) Stable peer identity used to derive a provider-owned section key.
- `interface` (Required) WireGuard interface name. Must match `^[a-z0-9_]+$`.
- `public_key` (Required) Peer public key.
- `allowed_ips` (Required) Allowed CIDR list for the peer.
- `preshared_key` (Optional, Sensitive) Optional pre-shared key.
- `endpoint_host` (Optional) Remote endpoint hostname or IP.
- `endpoint_port` (Optional) Remote endpoint port.
- `persistent_keepalive` (Optional) Persistent keepalive interval in seconds.
- `description` (Optional) Human-readable peer description.

## Attributes Reference

- `id` Canonical peer name.

## Behavior

- Uses a deterministic provider-owned named section with type `wireguard_<interface>`.
- Applies staged changes with rollback enabled and confirms after health and read-back checks.
- Reads live UCI state for drift and absence detection.
- Clears optional options when removed from configuration.
- Changing `name` or `interface` requires replacement; all other fields update in place.

## Import

Import using `<interface>/<name>`:

```bash
terraform import openwrt_wireguard_peer.actions wg_runner/actions
```
