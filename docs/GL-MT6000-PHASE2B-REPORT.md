# GL-MT6000 Phase 2B report: `openwrt_domain` vertical slice on modernubus

## Pre-implementation safety gate

### Isolation check result

`openwrt_domain` is representable as one standalone, deterministic named UCI section in package `dhcp`:

- section type: `config domain`
- section name: deterministic (`tf_domain_<normalized_domain>`)
- options: `name`, `ip`

This migration does **not** require:

- rewriting `/etc/config/dhcp`
- changing dnsmasq global options
- modifying DHCP pools
- changing network configuration
- mutating unrelated sections

## Exact production files changed

- [internal/client/modernubus/client.go](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/internal/client/modernubus/client.go)
- [internal/provider/provider.go](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/internal/provider/provider.go)
- [internal/provider/client_bundle.go](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/internal/provider/client_bundle.go)
- [internal/resources/domain/resource.go](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/internal/resources/domain/resource.go)
- [docs/resources/domain.md](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/docs/resources/domain.md)

Test/report files added:

- [internal/client/modernubus/phase2b_test.go](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/internal/client/modernubus/phase2b_test.go)
- [internal/resources/domain/resource_test.go](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/internal/resources/domain/resource_test.go)
- [docs/GL-MT6000-PHASE2B-REPORT.md](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/docs/GL-MT6000-PHASE2B-REPORT.md)

## Selected provider wiring seam

Implemented a narrow provider data adapter in [client_bundle.go](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/internal/provider/client_bundle.go):

- wraps legacy `luci.Client` (delegates all existing methods unchanged)
- also carries `*modernubus.Client` via `ModernUBUS()`

`provider.Configure` now creates both clients and injects the wrapper.  
Effect:

- all existing resources still consume `luci.Client` behavior through delegation
- only [openwrt_domain resource](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/internal/resources/domain/resource.go) consumes modernubus by asserting `ModernUBUS()`

Why safe:

- no broad per-resource wiring edits
- no legacy client behavior removal
- one-resource migration only

## Methods implemented (modernubus)

Added only methods required by this phase:

- `UCISet`
- `UCIDelete` (idempotent on `NOT_FOUND`)
- `UCIApply`
- `UCIConfirm`

Also added transaction orchestration primitives used by migrated resource:

- `RunMutationTransaction` (serialized via mutex)
- `EnsureSessionLifetime` / min-lifetime check before transaction staging
- pinned-session mutation execution during a transaction context

No file API, no service API, no shell execution.

## `openwrt_domain` behavior after migration

In [resource.go](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/internal/resources/domain/resource.go):

- `Create`/`Update`:
  - live `uci.get` on deterministic section
  - `uci.add` or `uci.set` on that section only
  - `uci.apply` with `rollback=true`, finite timeout
  - HTTP health check (`/cgi-bin/luci/admin/ubus`)
  - read-back verify (`name`, `ip`)
  - `uci.confirm` only when health and read-back succeed
- `Read`:
  - reads live UCI state (not prior TF state)
  - treats `result:[0]` with no payload as absence
- `Delete`:
  - idempotent if section absent
  - otherwise `uci.delete` + apply/health/read-back/confirm

## Unit and race test results

Validation commands:

- `go vet ./...` -> PASS
- `go test ./...` -> PASS
- `go test -race ./...` -> PASS

New modernubus tests include:

- `TestUCISetRequestAndResponse`
- `TestUCIDeleteRequestAndResponse`
- `TestUCIApplyRollbackTimeoutRequest`
- `TestUCIConfirmUsesSameSessionWhenPinned`
- `TestRunMutationTransactionSerializes`
- `TestNoMutationRetryAfterTransportFailure`
- `TestEnsureSessionLifetimeRenewsBeforeTransaction`
- `TestMissingSectionReadHandling`
- `TestIdempotentDelete`
- `TestContextCancellationForMutation`
- `TestConcurrentMutationsRaceFree`
- `TestMutationTransportErrorDoesNotLeakValues`

New domain tests include:

- `TestDomainCreateMappingUsesAdd`
- `TestDomainReadFromLiveUCIResponse`
- `TestDomainUpdateMappingUsesSet`
- `TestDomainDeleteMappingAndIdempotency`
- `TestUnrelatedUCISectionsPreserved`
- `TestApplyFailureDoesNotCallConfirm`
- `TestFailedPostApplyHealthCheckDoesNotCallConfirm`
- `TestSuccessfulApplyReadHealthCheckCallsConfirmOnce`

## Provider build result

- Local provider binary build: PASS (`go build`)
- Built binary used via temporary CLI config dev override (no global config changes)

## Sanitized OpenTofu run results

Temporary one-resource config (outside repo): `openwrt_domain` only.

- `tofu init` -> PASS
- `tofu validate` -> PASS
- `tofu plan` (parallelism 1) -> `1 to add`
- `tofu apply` (parallelism 1) -> PASS (`1 added`)
- second `tofu plan` -> `No changes`
- `tofu plan -refresh-only` -> PASS (`No changes`)
- `tofu destroy` (parallelism 1) -> PASS (`1 destroyed`)

## UCI verification

After apply, independent root UCI checks confirmed exactly the test section values:

- `name = tf-provider-probe.invalid`
- `ip = 192.0.2.1`

After destroy, section absent.

## DNS activation verification

After apply:

- `tf-provider-probe.invalid` resolved to `192.0.2.1` via router DNS.

After destroy:

- `tf-provider-probe.invalid` returned NXDOMAIN.

## Before/after semantic comparison

Baseline and post-destroy `uci export dhcp` were captured outside repo.

- baseline hash and after hash differed due formatting (blank-line placement)
- semantic comparison (ignoring blank lines) matched
- no unrelated dhcp section/value changes detected

## Connectivity results

- HTTP ubus endpoint before test: `200`
- HTTP ubus endpoint after destroy: `200`

## Cleanup result

- test resource destroyed via OpenTofu
- test UCI section absent
- test DNS mapping absent
- router remained reachable
- direct recovery command was prepared but **not required**

## Known limitations

- This phase migrates only `openwrt_domain`.
- Other resources still use legacy client behavior.
- Least-privilege tofu ACL work not started in this phase.

## Rollback instructions

1. `git revert <phase-2b-commit-sha>`
2. Re-run:
   - `go vet ./...`
   - `go test ./...`
   - `go test -race ./...`

## Recommendation

Phase 2B success criteria passed for the migrated `openwrt_domain` vertical slice.  
Stop here and request explicit approval before any ACL work or next resource migration.
