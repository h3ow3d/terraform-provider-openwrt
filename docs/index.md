# openwrt Provider

OpenWrt provider scaffold for network orchestration over LuCI RPC.

## Provider Configuration

```hcl
provider "openwrt" {
  remote   = "http://192.168.20.1/cgi-bin/luci/rpc"
  user     = "root"
  password = var.openwrt_password
}
```

Environment variable alternatives:

- `OPENWRT_REMOTE`
- `OPENWRT_USER`
- `OPENWRT_PASSWORD`

