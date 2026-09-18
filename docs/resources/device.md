# openwrt_device

Manages a named `config device` section in `/etc/config/network` through HTTP ubus UCI operations.

## Example Usage

```hcl
resource "openwrt_device" "runner_bridge" {
  name  = "br-vlan20"
  type  = "bridge"
  ports = ["lan2"]
}
```

## Argument Reference

- `name` (Required) Device identity and UCI section name.
- `type` (Required) OpenWrt device type such as `bridge`.
- `ports` (Optional) Ordered list of member ports.

## Attributes Reference

- `id` Canonical resource name.

## Behavior

- Uses a named `config device` section in `network` package.
- Applies staged changes with rollback enabled and confirms after health and read-back checks.
- Reads live UCI state for drift and absence detection.
- Removes the `ports` option when omitted from configuration.
- Changing `name` requires replacement; other fields update in place.

## Import

Import using the device name:

```bash
terraform import openwrt_device.runner_bridge br-vlan20
```
