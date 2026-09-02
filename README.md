# terraform-provider-openwrt

Terraform/OpenTofu provider for managing OpenWrt network configuration over LuCI RPC.

Registry source:

```hcl
source = "h3ow3d/openwrt"
```

## Quick start

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

Provider args can also be set with environment variables:

- `OPENWRT_REMOTE`
- `OPENWRT_USER`
- `OPENWRT_PASSWORD`

## Resource scope

- `openwrt_device`
- `openwrt_domain`
- `openwrt_segment`
- `openwrt_network`
- `openwrt_dhcp_pool`
- `openwrt_dhcp_host`
- `openwrt_firewall_rule`
- `openwrt_wireguard_interface`
- `openwrt_wireguard_peer`

## How apply works

Resources write provider-managed blocks in `/etc/config/*` files, then:

1. run `uci commit` for affected package(s)
2. restart affected service(s)

If apply fails during commit/restart, the provider attempts rollback of modified files.

## Known limitations

- Read operations are intentionally lightweight and currently do not perform full remote drift reconciliation.
- Some schema fields are currently forward-compatibility fields and not yet fully rendered into dedicated UCI structures (called out in per-resource docs).
- Migrate existing unmanaged OpenWrt configs gradually and validate each step.

## Documentation

- Provider docs: [docs/index.md](docs/index.md)
- Resource docs: [docs/resources/](docs/resources/)

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

- Dependabot config: [.github/dependabot.yml](.github/dependabot.yml)
- Auto-merge workflow: [.github/workflows/dependabot-automerge.yml](.github/workflows/dependabot-automerge.yml)
  - only Dependabot PRs
  - only patch/minor updates
  - blocks workflow-file changes
  - requires successful `test` check before approve + auto-merge

## License

MIT. See [LICENSE](LICENSE).

## Publishing to Terraform Registry

1. Create a GPG key for provider signing.
2. Add `GPG_PRIVATE_KEY` and `GPG_PASSPHRASE` GitHub repository secrets.
3. Create a Terraform Registry provider in namespace `h3ow3d`, type `openwrt`, from this repository.
4. Push a semantic tag (for example `v0.1.0`); the release workflow at [release.yml](.github/workflows/release.yml) publishes release artifacts.
