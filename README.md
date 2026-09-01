# terraform-provider-openwrt

Terraform provider scaffold for OpenWrt network orchestration.

Terraform Registry source:

```hcl
source = "h3ow3d/openwrt"
```

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

## Dependency automation

- Dependabot config: [.github/dependabot.yml](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/.github/dependabot.yml)
- Auto-merge workflow: [.github/workflows/dependabot-automerge.yml](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/.github/workflows/dependabot-automerge.yml)
  - only Dependabot PRs
  - only patch/minor updates
  - blocks workflow-file changes
  - requires successful `test` check before approve + auto-merge

## License

MIT. See [LICENSE](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/LICENSE).

## Publishing to Terraform Registry

1. Create a GPG key for provider signing.
2. Add `GPG_PRIVATE_KEY` and `GPG_PASSPHRASE` GitHub repository secrets.
3. Create a Terraform Registry provider in namespace `h3ow3d`, type `openwrt`, from this repository.
4. Push a semantic tag (for example `v0.1.0`); the release workflow at [release.yml](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/.github/workflows/release.yml) publishes release artifacts.
