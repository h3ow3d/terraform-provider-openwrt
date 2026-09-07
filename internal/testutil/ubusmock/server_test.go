package ubusmock

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/h3ow3d/terraform-provider-openwrt/internal/client/modernubus"
)

func newClient(url string) *modernubus.Client {
	return modernubus.NewClient(modernubus.Config{
		Remote:     url,
		User:       "dummy",
		Password:   "dummy-pass",
		HTTPClient: &http.Client{Timeout: 5 * time.Second},
	})
}

func TestLoopbackURL(t *testing.T) {
	mock := NewServer()
	defer mock.Close()
	if !IsLoopbackURL(mock.URL()) {
		t.Fatalf("expected loopback url, got %s", mock.URL())
	}
}

func TestAuthenticationAndSafeReadReauthentication(t *testing.T) {
	mock := NewServer()
	defer mock.Close()
	client := newClient(mock.URL())
	ctx := context.Background()

	if _, err := client.UCIGet(ctx, modernubus.UCIGetRequest{Config: "dhcp"}); err != nil {
		t.Fatalf("first read failed: %v", err)
	}
	mock.ExpireAllSessions()
	if _, err := client.UCIGet(ctx, modernubus.UCIGetRequest{Config: "dhcp"}); err != nil {
		t.Fatalf("read should reauthenticate after forced expiry: %v", err)
	}
}

func TestStagedSessionIsolationAndRevert(t *testing.T) {
	mock := NewServer()
	defer mock.Close()
	ctx := context.Background()
	a := newClient(mock.URL())
	b := newClient(mock.URL())

	if _, err := a.UCIAdd(ctx, modernubus.UCIAddRequest{
		Config: "dhcp",
		Type:   "domain",
		Name:   "tmp_a",
		Values: map[string]any{"name": "a.invalid", "ip": "192.0.2.1"},
	}); err != nil {
		t.Fatalf("add failed: %v", err)
	}
	seenA, err := a.UCIGet(ctx, modernubus.UCIGetRequest{Config: "dhcp", Section: "tmp_a"})
	if err != nil || !seenA.SectionExists {
		t.Fatalf("session A should see staged section: exists=%v err=%v", seenA.SectionExists, err)
	}
	seenB, err := b.UCIGet(ctx, modernubus.UCIGetRequest{Config: "dhcp", Section: "tmp_a"})
	if err != nil {
		t.Fatalf("session B get failed: %v", err)
	}
	if seenB.SectionExists {
		t.Fatalf("session B unexpectedly saw staged section")
	}

	if _, err := a.UCIRevert(ctx, modernubus.UCIRevertRequest{Config: "dhcp"}); err != nil {
		t.Fatalf("revert failed: %v", err)
	}
	after, err := a.UCIGet(ctx, modernubus.UCIGetRequest{Config: "dhcp", Section: "tmp_a"})
	if err != nil {
		t.Fatalf("post-revert get failed: %v", err)
	}
	if after.SectionExists {
		t.Fatalf("section should be absent after revert")
	}
}

func TestAddSetDeleteChangesAndApplyConfirm(t *testing.T) {
	mock := NewServer()
	defer mock.Close()
	client := newClient(mock.URL())
	ctx := context.Background()

	if _, err := client.UCIAdd(ctx, modernubus.UCIAddRequest{
		Config: "dhcp",
		Type:   "domain",
		Name:   "tmp_apply",
		Values: map[string]any{"name": "tmp.invalid", "ip": "192.0.2.1"},
	}); err != nil {
		t.Fatalf("add failed: %v", err)
	}
	if _, err := client.UCISet(ctx, modernubus.UCISetRequest{
		Config:  "dhcp",
		Section: "tmp_apply",
		Values:  map[string]any{"ip": "192.0.2.2"},
	}); err != nil {
		t.Fatalf("set failed: %v", err)
	}
	changes, err := client.UCIChanges(ctx, modernubus.UCIChangesRequest{Config: "dhcp"})
	if err != nil {
		t.Fatalf("changes failed: %v", err)
	}
	if len(changes.Changes) == 0 {
		t.Fatalf("expected staged changes")
	}

	err = client.RunMutationTransaction(ctx, 5*time.Second, func(tx context.Context) error {
		if _, err := client.UCIApply(tx, modernubus.UCIApplyRequest{Rollback: true, Timeout: 10}); err != nil {
			return err
		}
		_, err := client.UCIConfirm(tx, modernubus.UCIConfirmRequest{})
		return err
	})
	if err != nil {
		t.Fatalf("apply/confirm tx failed: %v", err)
	}

	got, err := client.UCIGet(ctx, modernubus.UCIGetRequest{Config: "dhcp", Section: "tmp_apply"})
	if err != nil {
		t.Fatalf("get after confirm failed: %v", err)
	}
	ip, _ := got.Values["ip"].String()
	if ip != "192.0.2.2" {
		t.Fatalf("expected committed ip 192.0.2.2, got %q", ip)
	}
}

func TestRollbackWithoutConfirm(t *testing.T) {
	mock := NewServer()
	defer mock.Close()
	client := newClient(mock.URL())
	ctx := context.Background()

	if _, err := client.UCIAdd(ctx, modernubus.UCIAddRequest{
		Config: "dhcp",
		Type:   "domain",
		Name:   "tmp_rollback",
		Values: map[string]any{"name": "rb.invalid", "ip": "192.0.2.1"},
	}); err != nil {
		t.Fatalf("add failed: %v", err)
	}
	if _, err := client.UCIApply(ctx, modernubus.UCIApplyRequest{Rollback: true, Timeout: 10}); err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	mock.TriggerRollbackNow()

	got, err := client.UCIGet(ctx, modernubus.UCIGetRequest{Config: "dhcp", Section: "tmp_rollback"})
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}
	if got.SectionExists {
		t.Fatalf("expected section absent after forced rollback")
	}
}

func TestMissingSectionReturnsEmptySuccess(t *testing.T) {
	mock := NewServer()
	defer mock.Close()
	client := newClient(mock.URL())
	got, err := client.UCIGet(context.Background(), modernubus.UCIGetRequest{
		Config:  "dhcp",
		Section: "does_not_exist",
	})
	if err != nil {
		t.Fatalf("unexpected get error: %v", err)
	}
	if got.SectionExists {
		t.Fatalf("missing section should be non-existent")
	}
}

func TestTypedErrorMappingAndAccessDenied(t *testing.T) {
	mock := NewServer()
	defer mock.Close()
	client := newClient(mock.URL())
	ctx := context.Background()

	mock.SetOneShotFailure("uci", "set", OneShotFailure{Kind: FailureUBus, UbusStatus: 2})
	_, err := client.UCISet(ctx, modernubus.UCISetRequest{Config: "dhcp", Section: "missing", Values: map[string]any{"ip": "x"}})
	var statusErr *modernubus.StatusError
	if !errors.As(err, &statusErr) || statusErr.Status != modernubus.StatusInvalidArgument {
		t.Fatalf("expected status invalid argument, got %v", err)
	}

	mock.SetOneShotFailure("uci", "get", OneShotFailure{Kind: FailureJSONRPC, RPCCode: -32002, RPCMessage: "Access denied"})
	_, err = client.UCIGet(ctx, modernubus.UCIGetRequest{Config: "dhcp"})
	var denied *modernubus.PermissionDeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("expected permission denied, got %v", err)
	}
}

func TestMutationTransportFailuresAreNotRetried(t *testing.T) {
	mock := NewServer()
	defer mock.Close()
	client := newClient(mock.URL())
	ctx := context.Background()

	if _, err := client.UCIAdd(ctx, modernubus.UCIAddRequest{
		Config: "dhcp",
		Type:   "domain",
		Name:   "tmp_transport",
		Values: map[string]any{"name": "transport.invalid", "ip": "192.0.2.1"},
	}); err != nil {
		t.Fatalf("seed add failed: %v", err)
	}
	if _, err := client.UCIApply(ctx, modernubus.UCIApplyRequest{Rollback: true, Timeout: 10}); err != nil {
		t.Fatalf("seed apply failed: %v", err)
	}
	if _, err := client.UCIConfirm(ctx, modernubus.UCIConfirmRequest{}); err != nil {
		t.Fatalf("seed confirm failed: %v", err)
	}

	mock.SetOneShotFailure("uci", "set", OneShotFailure{Kind: FailureTransport})
	_, err := client.UCISet(ctx, modernubus.UCISetRequest{
		Config:  "dhcp",
		Section: "tmp_transport",
		Values:  map[string]any{"ip": "192.0.2.2"},
	})
	if err == nil {
		t.Fatalf("expected transport error")
	}
	var transportErr *modernubus.TransportError
	if !errors.As(err, &transportErr) {
		t.Fatalf("expected transport error type, got %T", err)
	}

	records := mock.RequestHistory()
	count := 0
	for _, record := range records {
		if record.Object == "uci" && record.Method == "set" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected one set call, got %d", count)
	}
}

func TestErrorMessagesRedactCredentialsAndSessions(t *testing.T) {
	mock := NewServer()
	defer mock.Close()
	client := modernubus.NewClient(modernubus.Config{
		Remote:   mock.URL(),
		User:     "redact-user",
		Password: "super-secret-password",
		HTTPClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	})
	mock.SetOneShotFailure("uci", "get", OneShotFailure{Kind: FailureMalformed})
	_, err := client.UCIGet(context.Background(), modernubus.UCIGetRequest{Config: "dhcp"})
	if err == nil {
		t.Fatalf("expected malformed error")
	}
	msg := err.Error()
	if strings.Contains(msg, "super-secret-password") {
		t.Fatalf("error leaked password: %q", msg)
	}
	if strings.Contains(strings.ToLower(msg), "sess-") {
		t.Fatalf("error leaked session token: %q", msg)
	}
}

func TestSentinelSectionRemainsUnchanged(t *testing.T) {
	mock := NewServer()
	defer mock.Close()
	client := newClient(mock.URL())
	ctx := context.Background()

	initial, ok := mock.Section("dhcp", sentinelSectionName)
	if !ok {
		t.Fatalf("missing sentinel")
	}

	if _, err := client.UCIAdd(ctx, modernubus.UCIAddRequest{
		Config: "dhcp",
		Type:   "domain",
		Name:   "tmp_keep",
		Values: map[string]any{"name": "keep.invalid", "ip": "192.0.2.5"},
	}); err != nil {
		t.Fatalf("add failed: %v", err)
	}
	if _, err := client.UCIApply(ctx, modernubus.UCIApplyRequest{Rollback: true, Timeout: 10}); err != nil {
		t.Fatalf("apply failed: %v", err)
	}
	if _, err := client.UCIConfirm(ctx, modernubus.UCIConfirmRequest{}); err != nil {
		t.Fatalf("confirm failed: %v", err)
	}

	after, ok := mock.Section("dhcp", sentinelSectionName)
	if !ok {
		t.Fatalf("sentinel disappeared")
	}
	if after["name"] != initial["name"] || after["ip"] != initial["ip"] {
		t.Fatalf("sentinel changed: before=%v after=%v", initial, after)
	}
}
