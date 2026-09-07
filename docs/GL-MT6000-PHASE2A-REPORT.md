# GL-MT6000 Phase 2A report: staged UCI add/changes/revert

## Scope and boundary confirmation

Phase 2A only was implemented in [modernubus/](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/internal/client/modernubus/).

- No changes to [provider.go](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/internal/provider/provider.go)
- No changes to [resources/](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/internal/resources/)
- No changes to legacy [luci client](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/internal/client/luci/client.go)
- No `uci.set/delete/commit/apply/confirm/rollback` methods added to modernubus in this phase
- No service operations/file APIs were used

## Files changed

- [client.go](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/internal/client/modernubus/client.go)
- [client_test.go](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/internal/client/modernubus/client_test.go)
- [integration_test.go](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/internal/client/modernubus/integration_test.go)
- [GL-MT6000-PHASE2A-REPORT.md](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/docs/GL-MT6000-PHASE2A-REPORT.md)

## Client API added

In [client.go](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/internal/client/modernubus/client.go):

- `func (c *Client) UCIAdd(ctx context.Context, req UCIAddRequest) (UCIAddResponse, error)`
- `func (c *Client) UCIChanges(ctx context.Context, req UCIChangesRequest) (UCIChangesResponse, error)`
- `func (c *Client) UCIRevert(ctx context.Context, req UCIRevertRequest) (UCIRevertResponse, error)`

## Request/response types added

- `UCIAddRequest` / `UCIAddResponse`
- `UCIChangesRequest` / `UCIChangesResponse` / `UCIChange`
- `UCIRevertRequest` / `UCIRevertResponse`
- `ValidationError` for required-field checks

Behavior:

- validates required add fields (`config`, `type`)
- validates required `config` for `changes` and `revert`
- returns section name from `uci.add`
- parses variable-shaped `uci.changes` rows safely
- reuses Phase 1 authenticated session management and typed errors
- does not retry a mutation on transport failure

## Unit tests and results

All unit tests in [client_test.go](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/internal/client/modernubus/client_test.go) passed, including:

- `TestUCIAddRequestShapeAndNamedResponse`
- `TestUCIAddAnonymousSectionResponse`
- `TestMissingRequiredArguments`
- `TestUCIChangesParsingAndEmptyResponse`
- `TestUCIRevertSuccess`
- `TestUBUSFailureForAddChangesRevert`
- `TestJSONRPCFailureForAddChangesRevert`
- `TestUCIAddContextCancellation`
- `TestSensitiveValueRedaction`
- `TestSessionReuseAcrossAddGetChangesRevert`
- plus existing Phase 1 transport/session/error tests

Verification commands:

- `gofmt` (changed files): PASS
- `go vet ./...`: PASS
- `go test ./...`: PASS
- `go test -race ./...`: PASS

## Sanitized live call results (opt-in integration)

Integration tests (build tag `integration`) in [integration_test.go](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/internal/client/modernubus/integration_test.go):

- `TestIntegrationReadOnlySystemBoardAndUCIGet`: PASS
- `TestIntegrationPhase2AStagedAddChangesAndRevert`: PASS

Sanitized outcome summary:

- session A:
  - `uci.get(tf_probe_cap)` -> success (`status 0` implied by typed call success), empty package
  - `uci.add(config=tf_probe_cap,type=meta,name=provider_probe,values.marker=phase-2a)` -> success (`status 0`), `section=provider_probe`
  - `uci.changes(tf_probe_cap)` -> success (`status 0`), expected staged delta present
  - `uci.get(tf_probe_cap, section=provider_probe)` -> staged marker visible
  - `uci.revert(tf_probe_cap)` -> success (`status 0`)
  - `uci.changes(tf_probe_cap)` -> empty after revert
- session B:
  - `uci.get(tf_probe_cap, section=provider_probe)` before revert -> missing section (`status 0` with no payload semantics)
  - `uci.get(tf_probe_cap)` after revert -> empty package

No session IDs, passwords, or sensitive values were logged.

## Proof of session-isolated staging

`provider_probe` staged value was visible in session A and absent in session B before commit.  
This demonstrates session-local delta isolation.

## Proof revert removed staged change

After `uci.revert(tf_probe_cap)` in session A:

- session A `uci.changes` was empty
- session A section lookup showed absence
- session B package remained empty
- direct shell confirmed fixture file had no `config` sections

## Router state before and after

Before:

- fixture file absent was required; test aborts if present
- router endpoint reachable

After:

- no persisted `provider_probe` section remains
- no session delta remains
- fixture removed (only because this test created it and it was empty)
- no production package touched by test flow

## Connectivity result

- HTTP endpoint `/cgi-bin/luci/admin/ubus` was 200 before and after integration run.

## Cleanup result

Cleanup succeeded:

- fallback revert attempted in teardown path
- fixture emptied check passed
- fixture removed

## Known limitations

- Persistence (`commit`), apply/confirm, and ACL work are out of scope for Phase 2A.
- Provider wiring still points at legacy client.

## Rollback procedure

1. `git revert <phase-2a-commit-sha>`
2. Re-run:
   - `go vet ./...`
   - `go test ./...`
   - `go test -race ./...`

## Proposed commit message

`feat(modernubus): add staged UCI operations`

## Recommendation

Phase 2A gates passed.  
Stop here and request explicit approval before Phase 2B.
