package modernubus

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

func decodeRPCRequest(t *testing.T, r *http.Request) rpcRequest {
	t.Helper()
	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	return req
}

func TestCallUsesModernEndpointAndRPCShape(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	loginSeen := false
	callSeen := false
	callSession := ""

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cgi-bin/luci/admin/ubus" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		req := decodeRPCRequest(t, r)
		if req.JSONRPC != "2.0" || req.ID != 1 || req.Method != "call" {
			t.Fatalf("unexpected rpc envelope: %+v", req)
		}
		object := req.Params[1].(string)
		method := req.Params[2].(string)
		if object == "session" && method == "login" {
			if req.Params[0].(string) != bootstrapSessionID {
				t.Fatalf("unexpected bootstrap session id")
			}
			loginArgs := req.Params[3].(map[string]any)
			if loginArgs["username"] != "root" || loginArgs["password"] != "secret" {
				t.Fatalf("unexpected login request shape")
			}
			mu.Lock()
			loginSeen = true
			mu.Unlock()
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"ubus_rpc_session":"tokA","timeout":300}]}`))
			return
		}
		if object == "system" && method == "board" {
			mu.Lock()
			callSeen = true
			callSession = req.Params[0].(string)
			mu.Unlock()
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"hostname":"GL-MT6000"}]}`))
			return
		}
		t.Fatalf("unexpected call %s.%s", object, method)
	}))
	defer server.Close()

	client := NewClient(Config{Remote: server.URL, User: "root", Password: "secret"})
	var board map[string]any
	if err := client.Call(context.Background(), "system", "board", map[string]any{}, &board); err != nil {
		t.Fatalf("Call failed: %v", err)
	}

	mu.Lock()
	gotLogin, gotCall, gotSession := loginSeen, callSeen, callSession
	mu.Unlock()
	if !gotLogin || !gotCall || gotSession != "tokA" {
		t.Fatalf("missing expected call behavior")
	}
}

func TestLoginFailureReturnsAuthenticationError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[6,{}]}`))
	}))
	defer server.Close()

	client := NewClient(Config{Remote: server.URL, User: "root", Password: "secret"})
	err := client.Call(context.Background(), "system", "board", nil, nil)
	if err == nil {
		t.Fatal("expected auth error")
	}
	if !IsAuthenticationError(err) || !IsPermissionDenied(err) {
		t.Fatalf("unexpected error classification: %T", err)
	}
}

func TestFirmwareTimeoutParsingAndExpiryCapture(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRPCRequest(t, r)
		if req.Params[1].(string) == "session" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"ubus_rpc_session":"tokA","timeout":15}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{}]}`))
	}))
	defer server.Close()

	client := NewClient(Config{
		Remote:      server.URL,
		User:        "root",
		Password:    "secret",
		Now:         func() time.Time { return now },
		SessionSkew: time.Second,
	})
	if err := client.Call(context.Background(), "system", "board", nil, nil); err != nil {
		t.Fatalf("call failed: %v", err)
	}
	info := client.SessionInfo()
	if !info.HasSession || info.TimeoutSec != 15 || !info.ExpiresAt.Equal(now.Add(15*time.Second)) {
		t.Fatalf("unexpected session info: %+v", info)
	}
}

func TestSessionReuseBeforeExpiry(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	loginCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRPCRequest(t, r)
		if req.Params[1].(string) == "session" {
			mu.Lock()
			loginCount++
			mu.Unlock()
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"ubus_rpc_session":"tokA","timeout":120}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{}]}`))
	}))
	defer server.Close()

	client := NewClient(Config{Remote: server.URL, User: "root", Password: "secret"})
	_ = client.Call(context.Background(), "system", "board", nil, nil)
	_ = client.Call(context.Background(), "system", "board", nil, nil)

	mu.Lock()
	got := loginCount
	mu.Unlock()
	if got != 1 {
		t.Fatalf("expected one login, got %d", got)
	}
}

func TestProactiveRenewalNearExpiry(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	var clockMu sync.Mutex
	clock := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	advance := func(d time.Duration) {
		clockMu.Lock()
		now = now.Add(d)
		clockMu.Unlock()
	}

	var mu sync.Mutex
	loginCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRPCRequest(t, r)
		if req.Params[1].(string) == "session" {
			mu.Lock()
			loginCount++
			n := loginCount
			mu.Unlock()
			token := "tokA"
			if n > 1 {
				token = "tokB"
			}
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"ubus_rpc_session":"` + token + `","timeout":10}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{}]}`))
	}))
	defer server.Close()

	client := NewClient(Config{
		Remote:      server.URL,
		User:        "root",
		Password:    "secret",
		Now:         clock,
		SessionSkew: 2 * time.Second,
	})
	_ = client.Call(context.Background(), "system", "board", nil, nil)
	advance(9 * time.Second)
	_ = client.Call(context.Background(), "system", "board", nil, nil)

	mu.Lock()
	got := loginCount
	mu.Unlock()
	if got != 2 {
		t.Fatalf("expected proactive renewal, got %d logins", got)
	}
}

func TestConcurrentCallersSingleRenewal(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	var clockMu sync.Mutex
	clock := func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return now
	}
	advance := func(d time.Duration) {
		clockMu.Lock()
		now = now.Add(d)
		clockMu.Unlock()
	}

	var mu sync.Mutex
	loginCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRPCRequest(t, r)
		if req.Params[1].(string) == "session" {
			mu.Lock()
			loginCount++
			n := loginCount
			mu.Unlock()
			token := "tokA"
			if n > 1 {
				token = "tokB"
			}
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"ubus_rpc_session":"` + token + `","timeout":10}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{}]}`))
	}))
	defer server.Close()

	client := NewClient(Config{
		Remote:      server.URL,
		User:        "root",
		Password:    "secret",
		Now:         clock,
		SessionSkew: time.Second,
	})

	if err := client.Call(context.Background(), "system", "board", nil, nil); err != nil {
		t.Fatalf("seed call failed: %v", err)
	}
	advance(11 * time.Second)

	var wg sync.WaitGroup
	errCh := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errCh <- client.Call(context.Background(), "system", "board", nil, nil)
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("parallel call failed: %v", err)
		}
	}

	mu.Lock()
	got := loginCount
	mu.Unlock()
	if got != 2 {
		t.Fatalf("expected total 2 logins, got %d", got)
	}
}

func TestUCIAddRequestShapeAndNamedResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRPCRequest(t, r)
		object := req.Params[1].(string)
		method := req.Params[2].(string)
		if object == "session" && method == "login" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"ubus_rpc_session":"tokA","timeout":300}]}`))
			return
		}
		if object != "uci" || method != "add" {
			t.Fatalf("unexpected call: %s.%s", object, method)
		}
		args := req.Params[3].(map[string]any)
		if args["config"] != "tf_probe_cap" || args["type"] != "meta" || args["name"] != "provider_probe" {
			t.Fatalf("unexpected args shape: %#v", args)
		}
		values := args["values"].(map[string]any)
		if values["marker"] != "phase-2a" {
			t.Fatalf("unexpected marker")
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"section":"provider_probe"}]}`))
	}))
	defer server.Close()

	client := NewClient(Config{Remote: server.URL, User: "root", Password: "secret"})
	resp, err := client.UCIAdd(context.Background(), UCIAddRequest{
		Config: "tf_probe_cap",
		Type:   "meta",
		Name:   "provider_probe",
		Values: map[string]any{"marker": "phase-2a"},
	})
	if err != nil {
		t.Fatalf("uci.add failed: %v", err)
	}
	if resp.Section != "provider_probe" {
		t.Fatalf("unexpected section: %q", resp.Section)
	}
}

func TestUCIAddAnonymousSectionResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRPCRequest(t, r)
		if req.Params[1].(string) == "session" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"ubus_rpc_session":"tokA","timeout":300}]}`))
			return
		}
		args := req.Params[3].(map[string]any)
		if _, hasName := args["name"]; hasName {
			t.Fatalf("did not expect explicit name for anonymous section")
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"section":"cfg0a11f3"}]}`))
	}))
	defer server.Close()

	client := NewClient(Config{Remote: server.URL, User: "root", Password: "secret"})
	resp, err := client.UCIAdd(context.Background(), UCIAddRequest{
		Config: "tf_probe_cap",
		Type:   "meta",
		Values: map[string]any{"marker": "phase-2a"},
	})
	if err != nil {
		t.Fatalf("uci.add failed: %v", err)
	}
	if resp.Section != "cfg0a11f3" {
		t.Fatalf("unexpected anonymous section name: %q", resp.Section)
	}
}

func TestMissingRequiredArguments(t *testing.T) {
	t.Parallel()

	client := NewClient(Config{Remote: "http://127.0.0.1", User: "root", Password: "secret"})

	_, err := client.UCIAdd(context.Background(), UCIAddRequest{Type: "meta"})
	if err == nil {
		t.Fatal("expected missing config validation error")
	}
	_, err = client.UCIAdd(context.Background(), UCIAddRequest{Config: "tf_probe_cap"})
	if err == nil {
		t.Fatal("expected missing type validation error")
	}
	_, err = client.UCIChanges(context.Background(), UCIChangesRequest{})
	if err == nil {
		t.Fatal("expected missing config validation error")
	}
	_, err = client.UCIRevert(context.Background(), UCIRevertRequest{})
	if err == nil {
		t.Fatal("expected missing config validation error")
	}
}

func TestUCIChangesParsingAndEmptyResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRPCRequest(t, r)
		if req.Params[1].(string) == "session" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"ubus_rpc_session":"tokA","timeout":300}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"changes":[["set","provider_probe","meta"],["set","provider_probe","marker","phase-2a"]]}]}`))
	}))
	defer server.Close()

	client := NewClient(Config{Remote: server.URL, User: "root", Password: "secret"})
	resp, err := client.UCIChanges(context.Background(), UCIChangesRequest{Config: "tf_probe_cap"})
	if err != nil {
		t.Fatalf("uci.changes failed: %v", err)
	}
	if len(resp.Changes) != 2 {
		t.Fatalf("expected 2 changes, got %d", len(resp.Changes))
	}
	firstOp, _ := resp.Changes[0].StringAt(0)
	if firstOp != "set" {
		t.Fatalf("unexpected first operation: %q", firstOp)
	}

	emptyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRPCRequest(t, r)
		if req.Params[1].(string) == "session" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"ubus_rpc_session":"tokB","timeout":300}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"changes":[]} ]}`))
	}))
	defer emptyServer.Close()

	client2 := NewClient(Config{Remote: emptyServer.URL, User: "root", Password: "secret"})
	emptyResp, err := client2.UCIChanges(context.Background(), UCIChangesRequest{Config: "tf_probe_cap"})
	if err != nil {
		t.Fatalf("uci.changes empty failed: %v", err)
	}
	if len(emptyResp.Changes) != 0 {
		t.Fatalf("expected no changes, got %d", len(emptyResp.Changes))
	}
}

func TestUCIRevertSuccess(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRPCRequest(t, r)
		if req.Params[1].(string) == "session" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"ubus_rpc_session":"tokA","timeout":300}]}`))
			return
		}
		if req.Params[1].(string) != "uci" || req.Params[2].(string) != "revert" {
			t.Fatalf("unexpected call")
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0]}`))
	}))
	defer server.Close()

	client := NewClient(Config{Remote: server.URL, User: "root", Password: "secret"})
	if _, err := client.UCIRevert(context.Background(), UCIRevertRequest{Config: "tf_probe_cap"}); err != nil {
		t.Fatalf("uci.revert failed: %v", err)
	}
}

func TestUBUSFailureForAddChangesRevert(t *testing.T) {
	t.Parallel()

	run := func(t *testing.T, method string, invoke func(*Client) error) {
		t.Helper()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			req := decodeRPCRequest(t, r)
			if req.Params[1].(string) == "session" {
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"ubus_rpc_session":"tokA","timeout":300}]}`))
				return
			}
			if req.Params[2].(string) != method {
				t.Fatalf("expected method %s", method)
			}
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[9]}`))
		}))
		defer server.Close()
		client := NewClient(Config{Remote: server.URL, User: "root", Password: "secret"})
		err := invoke(client)
		if err == nil {
			t.Fatalf("expected ubus failure")
		}
		var statusErr *StatusError
		if !errors.As(err, &statusErr) || statusErr.Status != StatusUnknownError {
			t.Fatalf("expected status unknown error, got %T: %v", err, err)
		}
	}

	t.Run("add", func(t *testing.T) {
		run(t, "add", func(c *Client) error {
			_, err := c.UCIAdd(context.Background(), UCIAddRequest{Config: "tf_probe_cap", Type: "meta"})
			return err
		})
	})
	t.Run("changes", func(t *testing.T) {
		run(t, "changes", func(c *Client) error {
			_, err := c.UCIChanges(context.Background(), UCIChangesRequest{Config: "tf_probe_cap"})
			return err
		})
	})
	t.Run("revert", func(t *testing.T) {
		run(t, "revert", func(c *Client) error {
			_, err := c.UCIRevert(context.Background(), UCIRevertRequest{Config: "tf_probe_cap"})
			return err
		})
	})
}

func TestJSONRPCFailureForAddChangesRevert(t *testing.T) {
	t.Parallel()

	run := func(t *testing.T, method string, invoke func(*Client) error) {
		t.Helper()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			req := decodeRPCRequest(t, r)
			if req.Params[1].(string) == "session" {
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"ubus_rpc_session":"tokA","timeout":300}]}`))
				return
			}
			if req.Params[2].(string) != method {
				t.Fatalf("expected method %s", method)
			}
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32002,"message":"Access denied"}}`))
		}))
		defer server.Close()
		client := NewClient(Config{Remote: server.URL, User: "root", Password: "secret"})
		err := invoke(client)
		if err == nil {
			t.Fatalf("expected json-rpc failure")
		}
		if !IsPermissionDenied(err) {
			t.Fatalf("expected permission denied error, got %T: %v", err, err)
		}
	}

	t.Run("add", func(t *testing.T) {
		run(t, "add", func(c *Client) error {
			_, err := c.UCIAdd(context.Background(), UCIAddRequest{Config: "tf_probe_cap", Type: "meta"})
			return err
		})
	})
	t.Run("changes", func(t *testing.T) {
		run(t, "changes", func(c *Client) error {
			_, err := c.UCIChanges(context.Background(), UCIChangesRequest{Config: "tf_probe_cap"})
			return err
		})
	})
	t.Run("revert", func(t *testing.T) {
		run(t, "revert", func(c *Client) error {
			_, err := c.UCIRevert(context.Background(), UCIRevertRequest{Config: "tf_probe_cap"})
			return err
		})
	})
}

func TestUCIAddContextCancellation(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"ubus_rpc_session":"tokA","timeout":300}]}`))
	}))
	defer server.Close()

	client := NewClient(Config{
		Remote:     server.URL,
		User:       "root",
		Password:   "secret",
		HTTPClient: &http.Client{Timeout: 20 * time.Millisecond},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	_, err := client.UCIAdd(ctx, UCIAddRequest{Config: "tf_probe_cap", Type: "meta"})
	if err == nil {
		t.Fatal("expected context cancellation/timeout")
	}
	var authErr *AuthenticationError
	var transportErr *TransportError
	if !errors.As(err, &authErr) && !errors.As(err, &transportErr) {
		t.Fatalf("expected auth/transport error, got %T: %v", err, err)
	}
}

func TestSensitiveValueRedaction(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRPCRequest(t, r)
		if req.Params[1].(string) == "session" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"ubus_rpc_session":"token-secret","timeout":300}]}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	client := NewClient(Config{Remote: server.URL, User: "root", Password: "super-secret-password"})
	_, err := client.UCIAdd(context.Background(), UCIAddRequest{
		Config: "tf_probe_cap",
		Type:   "meta",
		Values: map[string]any{"marker": "super-secret-value"},
	})
	if err == nil {
		t.Fatal("expected failure")
	}
	msg := err.Error()
	for _, secret := range []string{"super-secret-password", "token-secret", "super-secret-value"} {
		if strings.Contains(msg, secret) {
			t.Fatalf("error leaked secret value: %q", secret)
		}
	}
}

func TestSessionReuseAcrossAddGetChangesRevert(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	loginCount := 0
	callSessions := []string{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRPCRequest(t, r)
		object := req.Params[1].(string)
		method := req.Params[2].(string)
		if object == "session" && method == "login" {
			mu.Lock()
			loginCount++
			mu.Unlock()
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"ubus_rpc_session":"tokA","timeout":300}]}`))
			return
		}
		mu.Lock()
		callSessions = append(callSessions, req.Params[0].(string))
		mu.Unlock()

		switch method {
		case "add":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"section":"provider_probe"}]}`))
		case "get":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"values":{"marker":"phase-2a"}}]}`))
		case "changes":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"changes":[["set","provider_probe","meta"]]}]}`))
		case "revert":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0]}`))
		default:
			t.Fatalf("unexpected method %s", method)
		}
	}))
	defer server.Close()

	client := NewClient(Config{Remote: server.URL, User: "root", Password: "secret"})
	_, _ = client.UCIAdd(context.Background(), UCIAddRequest{Config: "tf_probe_cap", Type: "meta", Name: "provider_probe", Values: map[string]any{"marker": "phase-2a"}})
	_, _ = client.UCIGet(context.Background(), UCIGetRequest{Config: "tf_probe_cap", Section: "provider_probe"})
	_, _ = client.UCIChanges(context.Background(), UCIChangesRequest{Config: "tf_probe_cap"})
	_, _ = client.UCIRevert(context.Background(), UCIRevertRequest{Config: "tf_probe_cap"})

	mu.Lock()
	gotLogins := loginCount
	sessions := append([]string(nil), callSessions...)
	mu.Unlock()
	if gotLogins != 1 {
		t.Fatalf("expected one login, got %d", gotLogins)
	}
	if len(sessions) != 4 {
		t.Fatalf("expected 4 authenticated calls, got %d", len(sessions))
	}
	for _, s := range sessions {
		if s != "tokA" {
			t.Fatalf("expected same session token for staged operations, got %q", s)
		}
	}
}
