# openwrt_device

Manages an OpenWrt `config device` section in `/etc/config/network`.

## Example Usage

```hcl
resource "openwrt_device" "runner_bridge" {
  name  = "br-vlan20"
  type  = "bridge"
  ports = ["lan2"]
}
```

## Argument Reference

- `name` (Required) Device section name and `option name` value.
- `type` (Required) Device type (for example `bridge`).
- `ports` (Optional) Port member list for bridge-like devices.

## Attributes Reference

- `id` Resource id (computed as `name`).

## Behavior

- Writes a managed `config device` block to `/etc/config/network`.
- Runs `uci commit network`.
- Restarts `network`.

## Import

```bash
terraform import openwrt_device.runner_bridge br-vlan20
```
