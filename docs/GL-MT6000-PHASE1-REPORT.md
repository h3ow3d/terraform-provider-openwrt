# GL-MT6000 Phase 1 report: read-only modern HTTP ubus client

## Scope and boundary confirmation

Phase 1 only was implemented.

- Added a new client package alongside legacy client.
- Did not change [provider.go](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/internal/provider/provider.go) wiring.
- Did not modify any resource files.
- Did not modify or remove legacy [luci client](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/internal/client/luci/client.go).
- Did not add any UCI mutation methods to the new client.
- Performed read-only live integration checks only.

## Package and file layout chosen

- New package: [internal/client/modernubus/](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/internal/client/modernubus/)
  - [client.go](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/internal/client/modernubus/client.go)
  - [client_test.go](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/internal/client/modernubus/client_test.go)
  - [integration_test.go](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/internal/client/modernubus/integration_test.go) (opt-in via build tag + env flag)

## Public client API introduced

In [client.go](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/internal/client/modernubus/client.go):

- `type Client`
- `func NewClient(Config) *Client`
- `func (c *Client) Call(ctx, object, method, args, into) error`
- `func (c *Client) UCIGet(ctx, UCIGetRequest) (UCIGetResponse, error)`
- `func (c *Client) SessionInfo() SessionInfo`
- `type Status` + mappings for `0/2/4/6/9`
- typed error types: `TransportError`, `HTTPStatusError`, `MalformedResponseError`, `RPCError`, `StatusError`, `AuthenticationError`, `PermissionDeniedError`

`UCIGetResponse` supports:

- package lookup
- named section lookup
- option lookup
- typed string/list option access via `UCIValue.String()` and `UCIValue.List()`
- metadata fields `.name`, `.type`, `.anonymous`
- firmware semantics:
  - empty package `values: []`
  - missing section as `result: [0]` with no payload
  - ubus failure status via typed errors

## Session-renewal design

- Session is established through `session.login` on `/cgi-bin/luci/admin/ubus`.
- Captures `ubus_rpc_session`, firmware timeout seconds, and computed expiry time.
- Proactive renewal uses `SessionSkew` before expiry.
- Session state is protected by a mutex.
- Renewal logic is concurrency-safe: concurrent callers reuse one valid token and avoid login storms.
- HTTP 403 on call path clears token and performs one re-auth attempt.

## Typed error model

Error classes distinguish:

- HTTP transport failures (`TransportError`)
- non-success HTTP status (`HTTPStatusError`)
- malformed JSON/response shape (`MalformedResponseError`)
- JSON-RPC error (`RPCError`)
- ubus status result (`StatusError`)
- authentication failures (`AuthenticationError`)
- permission denial (`PermissionDeniedError`)

Sensitive values are not included in error strings (passwords/session IDs are not exposed).

## Unit tests (httptest) and results

File: [client_test.go](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/internal/client/modernubus/client_test.go)

Tests added and passing:

- `TestCallUsesModernEndpointAndRPCShape`
- `TestLoginFailureReturnsAuthenticationError`
- `TestFirmwareTimeoutParsingAndExpiryCapture`
- `TestSessionReuseBeforeExpiry`
- `TestProactiveRenewalNearExpiry`
- `TestConcurrentCallersSingleRenewal`
- `TestGenericAuthenticatedCallSuccess`
- `TestUCIGetPackageResponse`
- `TestUCIGetNamedSectionWithStringAndListAndMetadata`
- `TestUCIGetEmptyPackageValuesArray`
- `TestUCIGetMissingSectionResultZeroNoPayload`
- `TestUbusStatusMappings` (+ subtests for `OK`, `INVALID_ARGUMENT`, `NOT_FOUND`, `PERMISSION_DENIED`, `UNKNOWN_ERROR`)
- `TestPermissionDeniedFromJSONRPCError`
- `TestNonSuccessHTTPStatusError`
- `TestMalformedResponseError`
- `TestContextCancellationOrTimeout`
- `TestCredentialAndTokenRedactionFromErrors`

## Live integration test (opt-in) and results

File: [integration_test.go](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/internal/client/modernubus/integration_test.go)

Properties:

- requires build tag `integration`
- requires `OPENWRT_INTEGRATION=1`
- reads all connection data from env only
- no defaults for router address/credentials/package
- clean skip when env is absent
- performs only read-only:
  - authenticated `system.board`
  - authenticated `uci.get` for operator-provided package

Executed command result:

- `go test -tags=integration -run TestIntegrationReadOnlySystemBoardAndUCIGet -v ./internal/client/modernubus` -> **PASS**

Sanitized live call outcomes:

- `session.login` -> success (`result[0]=0`)
- `system.board` -> success (`result[0]=0`)
- `uci.get` on configured readable package (`network`) -> success (`result[0]=0`)

## Verification gate results

- `gofmt` on new files: **PASS**
- `go vet ./...`: **PASS**
- `go test ./...`: **PASS**
- `go test -race ./...`: **PASS**
- opt-in live read-only integration test: **PASS**

## Router state and connectivity before/after

- No router mutation performed in Phase 1.
- Endpoint connectivity check before test: HTTP `200`
- Endpoint connectivity check after test: HTTP `200`

## Git diff summary

Changed files for Phase 1 implementation:

- [client.go](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/internal/client/modernubus/client.go)
- [client_test.go](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/internal/client/modernubus/client_test.go)
- [integration_test.go](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/internal/client/modernubus/integration_test.go)
- [GL-MT6000-PHASE1-REPORT.md](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/docs/GL-MT6000-PHASE1-REPORT.md)

Confirmed unchanged:

- [provider.go](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/internal/provider/provider.go)
- [resources/](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/internal/resources/)
- legacy [luci client](/Users/samholden/Git/_ophomelab/_providers/terraform-provider-openwrt/internal/client/luci/client.go)

## Known limitations (intentional for Phase 1)

- No UCI mutation methods in new client yet.
- Provider wiring still uses legacy client.
- No acceptance Terraform resource tests yet.

## Rollback instructions

If Phase 1 needs rollback:

1. `git revert <phase-1-commit-sha>`
2. Re-run:
   - `go vet ./...`
   - `go test ./...`
   - `go test -race ./...`

## Proposed Phase 1 commit message

`feat(modernubus): add read-only authenticated HTTP ubus client with typed errors and tests`

## Recommendation

Phase 1 gates passed.  
Recommend operator review and explicit approval before starting Phase 2.
