# openwrt Provider

The `openwrt` provider manages OpenWrt router configuration over the LuCI ubus JSON-RPC API (`/cgi-bin/luci/admin/ubus`).

## Example Usage

```hcl
terraform {
  required_providers {
    openwrt = {
      source  = "h3ow3d/openwrt"
      version = "0.1.0"
    }
  }
}

provider "openwrt" {
  remote   = "http://192.0.2.1"
  user     = "root"
  password = var.openwrt_password
}
```

## Provider Arguments

- `remote` (Optional) LuCI base URL for the router, for example `http://192.0.2.1`.  
  Can be set with `OPENWRT_REMOTE`.
- `user` (Optional) OpenWrt admin username.  
  Can be set with `OPENWRT_USER`.
- `password` (Optional, Sensitive) OpenWrt admin password.  
  Can be set with `OPENWRT_PASSWORD`.

All three values are required either in configuration or env vars.

## Operational Behavior

- Resources use HTTP ubus UCI operations through the LuCI JSON-RPC endpoint.
- The provider does not use SSH, direct config-file writes, `file.exec`, or service restarts.

## Current Limitations

- `openwrt_domain` and `openwrt_dhcp_host` are currently available. Other resource types will return as they are migrated to HTTP ubus UCI operations.
- Migration from pre-existing unmanaged config should be done carefully and incrementally.

## Resources

- `openwrt_domain`
- `openwrt_dhcp_host`
