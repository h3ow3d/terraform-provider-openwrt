# openwrt_wireguard_peer

Manages a WireGuard peer section (`config wireguard_<interface>`) in `/etc/config/network`.

## Example Usage

```hcl
resource "openwrt_wireguard_peer" "runner_worker_1" {
  name                 = "runner-worker-1"
  interface            = openwrt_wireguard_interface.wg0.name
  public_key           = var.worker_public_key
  allowed_ips          = ["203.0.113.2/32"]
  endpoint_host        = "runner-worker-1.example.net"
  endpoint_port        = 51820
  persistent_keepalive = 25
}
```

## Argument Reference

- `name` (Required) Peer display name.
- `interface` (Required) WireGuard interface name this peer belongs to.
- `public_key` (Required) Base64 WireGuard public key.
- `preshared_key` (Optional, Sensitive) Peer preshared key.
- `allowed_ips` (Optional) List of allowed IP CIDRs for this peer.
- `endpoint_host` (Optional) Endpoint host/IP.
- `endpoint_port` (Optional) Endpoint UDP port.
- `persistent_keepalive` (Optional) Keepalive interval in seconds.

## Attributes Reference

- `id` Resource id (computed as `<interface>/<name>`).

## Behavior

- Writes a managed `config wireguard_<interface>` block to `/etc/config/network`.
- Sets `option description` to `name` and uses that value in managed block keys.
- Runs `uci commit network`.
- Restarts `network`.

## Import

```bash
terraform import openwrt_wireguard_peer.runner_worker_1 wg0/runner-worker-1
```
