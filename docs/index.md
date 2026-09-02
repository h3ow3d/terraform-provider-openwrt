# openwrt Provider

The `openwrt` provider manages OpenWrt router configuration over the LuCI JSON-RPC API (`/cgi-bin/luci/rpc/*`).

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

- Resources write only provider-managed blocks to OpenWrt config files under `/etc/config`.
- On apply, resources commit the affected UCI package and restart the relevant service.
- If a commit or restart fails, the provider attempts to roll back changed files.

## Current Limitations

- Resource `Read` operations are lightweight and do not yet perform full remote drift reconciliation.
- Migration from pre-existing unmanaged config should be done carefully and incrementally.

## Resources

- `openwrt_device`
- `openwrt_domain`
- `openwrt_segment`
- `openwrt_network`
- `openwrt_dhcp_pool`
- `openwrt_dhcp_host`
- `openwrt_firewall_rule`
- `openwrt_wireguard_interface`
- `openwrt_wireguard_peer`
