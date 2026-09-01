# terraform-provider-openwrt

Terraform provider scaffold for OpenWrt network orchestration.

## Current scope

This repository contains a provider baseline with working orchestration primitives:

- Provider runtime and schema
- OpenWrt LuCI RPC client abstraction
- Initial resource surface:
  - `openwrt_segment`
  - `openwrt_network`
  - `openwrt_dhcp_pool`
  - `openwrt_dhcp_host`
  - `openwrt_firewall_rule`
  - `openwrt_wireguard_interface`
  - `openwrt_wireguard_peer`
- CI and development scaffolding

Both resources perform managed-block updates in OpenWrt config files (`/etc/config/network` and `/etc/config/dhcp`), commit package changes, and restart the affected service.

## Development

```bash
go mod tidy
go test ./...
go build ./...
```

## Pre-commit

```bash
pip install pre-commit
pre-commit install
pre-commit run --all-files
```

## License

MIT. See [LICENSE](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/LICENSE).
