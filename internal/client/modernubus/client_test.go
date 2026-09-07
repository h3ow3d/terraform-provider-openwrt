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
			if loginArgs["username"] != "root" {
				t.Fatalf("expected root username")
			}
			if loginArgs["password"] != "secret" {
				t.Fatalf("expected password in request body")
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

	client := NewClient(Config{
		Remote:   server.URL,
		User:     "root",
		Password: "secret",
	})

	var board map[string]any
	if err := client.Call(context.Background(), "system", "board", map[string]any{}, &board); err != nil {
		t.Fatalf("Call failed: %v", err)
	}

	mu.Lock()
	gotLogin := loginSeen
	gotCall := callSeen
	gotSession := callSession
	mu.Unlock()

	if !gotLogin || !gotCall {
		t.Fatalf("expected login and call to be observed")
	}
	if gotSession != "tokA" {
		t.Fatalf("expected authenticated session token in call, got %q", gotSession)
	}
}

func TestLoginFailureReturnsAuthenticationError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[6,{}]}`))
	}))
	defer server.Close()

	client := NewClient(Config{
		Remote:   server.URL,
		User:     "root",
		Password: "secret",
	})

	err := client.Call(context.Background(), "system", "board", nil, nil)
	if err == nil {
		t.Fatal("expected auth error")
	}
	if !IsAuthenticationError(err) {
		t.Fatalf("expected authentication error, got %T", err)
	}
	if !IsPermissionDenied(err) {
		t.Fatalf("expected permission denied classification, got %T", err)
	}
}

func TestFirmwareTimeoutParsingAndExpiryCapture(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	clock := func() time.Time { return now }

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRPCRequest(t, r)
		object := req.Params[1].(string)
		method := req.Params[2].(string)
		if object == "session" && method == "login" {
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
		Now:         clock,
		SessionSkew: time.Second,
	})
	if err := client.Call(context.Background(), "system", "board", nil, nil); err != nil {
		t.Fatalf("call failed: %v", err)
	}
	info := client.SessionInfo()
	if !info.HasSession {
		t.Fatal("expected session to exist")
	}
	if info.TimeoutSec != 15 {
		t.Fatalf("expected timeout 15, got %d", info.TimeoutSec)
	}
	want := now.Add(15 * time.Second)
	if !info.ExpiresAt.Equal(want) {
		t.Fatalf("expected expiresAt %v, got %v", want, info.ExpiresAt)
	}
}

func TestSessionReuseBeforeExpiry(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	loginCount := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRPCRequest(t, r)
		object := req.Params[1].(string)
		method := req.Params[2].(string)
		if object == "session" && method == "login" {
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
	if err := client.Call(context.Background(), "system", "board", nil, nil); err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	if err := client.Call(context.Background(), "system", "board", nil, nil); err != nil {
		t.Fatalf("second call failed: %v", err)
	}

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
	nextToken := "tokA"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRPCRequest(t, r)
		object := req.Params[1].(string)
		method := req.Params[2].(string)
		if object == "session" && method == "login" {
			mu.Lock()
			loginCount++
			token := nextToken
			if loginCount == 1 {
				nextToken = "tokB"
			}
			mu.Unlock()
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

	if err := client.Call(context.Background(), "system", "board", nil, nil); err != nil {
		t.Fatalf("first call failed: %v", err)
	}
	advance(9 * time.Second)
	if err := client.Call(context.Background(), "system", "board", nil, nil); err != nil {
		t.Fatalf("second call failed: %v", err)
	}

	mu.Lock()
	got := loginCount
	mu.Unlock()
	if got != 2 {
		t.Fatalf("expected proactive renewal login count 2, got %d", got)
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
		object := req.Params[1].(string)
		method := req.Params[2].(string)
		if object == "session" && method == "login" {
			mu.Lock()
			loginCount++
			seq := loginCount
			mu.Unlock()
			token := "tokA"
			if seq > 1 {
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
	errCh := make(chan error, 20)
	for i := 0; i < 20; i++ {
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
		t.Fatalf("expected one renewal for all concurrent callers (total logins 2), got %d", got)
	}
}

func TestGenericAuthenticatedCallSuccess(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRPCRequest(t, r)
		object := req.Params[1].(string)
		method := req.Params[2].(string)
		if object == "session" && method == "login" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"ubus_rpc_session":"tokA","timeout":300}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"hostname":"GL-MT6000"}]}`))
	}))
	defer server.Close()

	client := NewClient(Config{Remote: server.URL, User: "root", Password: "secret"})
	var out map[string]any
	if err := client.Call(context.Background(), "system", "board", nil, &out); err != nil {
		t.Fatalf("generic call failed: %v", err)
	}
	if out["hostname"] != "GL-MT6000" {
		t.Fatalf("unexpected output: %v", out)
	}
}

func TestUCIGetPackageResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRPCRequest(t, r)
		if req.Params[1].(string) == "session" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"ubus_rpc_session":"tokA","timeout":300}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"values":{"lan":{"proto":"static"}}}]}`))
	}))
	defer server.Close()

	client := NewClient(Config{Remote: server.URL, User: "root", Password: "secret"})
	resp, err := client.UCIGet(context.Background(), UCIGetRequest{Config: "network"})
	if err != nil {
		t.Fatalf("uci get failed: %v", err)
	}
	if !resp.PackageExists || !resp.SectionExists || !resp.OptionExists {
		t.Fatalf("unexpected existence flags: %+v", resp)
	}
}

func TestUCIGetNamedSectionWithStringAndListAndMetadata(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRPCRequest(t, r)
		if req.Params[1].(string) == "session" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"ubus_rpc_session":"tokA","timeout":300}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"values":{".name":"probe",".type":"meta",".anonymous":false,"marker":"controlled-test","ports":["lan1:t","lan2:u*"]}}]}`))
	}))
	defer server.Close()

	client := NewClient(Config{Remote: server.URL, User: "root", Password: "secret"})
	resp, err := client.UCIGet(context.Background(), UCIGetRequest{Config: "tf_probe_cap", Section: "probe"})
	if err != nil {
		t.Fatalf("uci get failed: %v", err)
	}
	if !resp.SectionExists {
		t.Fatal("expected section to exist")
	}
	if resp.MetadataName != "probe" || resp.MetadataType != "meta" {
		t.Fatalf("metadata mismatch: %+v", resp)
	}
	if resp.MetadataIsAnon == nil || *resp.MetadataIsAnon {
		t.Fatalf("metadata anonymous mismatch: %+v", resp.MetadataIsAnon)
	}

	marker, ok := resp.Values["marker"].String()
	if !ok || marker != "controlled-test" {
		t.Fatalf("unexpected marker: %q (%v)", marker, ok)
	}
	ports, ok := resp.Values["ports"].List()
	if !ok || len(ports) != 2 {
		t.Fatalf("unexpected ports: %v", ports)
	}
}

func TestUCIGetEmptyPackageValuesArray(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRPCRequest(t, r)
		if req.Params[1].(string) == "session" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"ubus_rpc_session":"tokA","timeout":300}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"values":[]}]}`))
	}))
	defer server.Close()

	client := NewClient(Config{Remote: server.URL, User: "root", Password: "secret"})
	resp, err := client.UCIGet(context.Background(), UCIGetRequest{Config: "tf_probe_cap"})
	if err != nil {
		t.Fatalf("uci get failed: %v", err)
	}
	if !resp.EmptyPackage || !resp.PackageExists {
		t.Fatalf("expected empty package response, got %+v", resp)
	}
}

func TestUCIGetMissingSectionResultZeroNoPayload(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRPCRequest(t, r)
		if req.Params[1].(string) == "session" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"ubus_rpc_session":"tokA","timeout":300}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0]}`))
	}))
	defer server.Close()

	client := NewClient(Config{Remote: server.URL, User: "root", Password: "secret"})
	resp, err := client.UCIGet(context.Background(), UCIGetRequest{Config: "tf_probe_cap", Section: "missing"})
	if err != nil {
		t.Fatalf("uci get failed: %v", err)
	}
	if resp.SectionExists {
		t.Fatalf("expected missing section semantics, got %+v", resp)
	}
}

func TestUbusStatusMappings(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		status int
	}{
		{name: "OK", status: 0},
		{name: "INVALID_ARGUMENT", status: 2},
		{name: "NOT_FOUND", status: 4},
		{name: "PERMISSION_DENIED", status: 6},
		{name: "UNKNOWN_ERROR", status: 9},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ParseStatus(tc.status)
			if got != Status(tc.status) {
				t.Fatalf("status mapping mismatch: got %v want %d", got, tc.status)
			}
		})
	}
}

func TestPermissionDeniedFromJSONRPCError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRPCRequest(t, r)
		if req.Params[1].(string) == "session" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"ubus_rpc_session":"tokA","timeout":300}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32002,"message":"Access denied"}}`))
	}))
	defer server.Close()

	client := NewClient(Config{Remote: server.URL, User: "root", Password: "secret"})
	err := client.Call(context.Background(), "service", "event", map[string]any{"type": "config.change"}, nil)
	if err == nil {
		t.Fatal("expected permission denied error")
	}
	if !IsPermissionDenied(err) {
		t.Fatalf("expected permission denied error type, got %T", err)
	}
}

func TestNonSuccessHTTPStatusError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()

	client := NewClient(Config{Remote: server.URL, User: "root", Password: "secret"})
	err := client.Call(context.Background(), "system", "board", nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var authErr *AuthenticationError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected authentication error, got %T", err)
	}
	var httpErr *HTTPStatusError
	if !errors.As(err, &httpErr) {
		t.Fatalf("expected HTTPStatusError in chain, got %T", err)
	}
}

func TestMalformedResponseError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{not-json`))
	}))
	defer server.Close()

	client := NewClient(Config{Remote: server.URL, User: "root", Password: "secret"})
	err := client.Call(context.Background(), "system", "board", nil, nil)
	if err == nil {
		t.Fatal("expected malformed response error")
	}
	var authErr *AuthenticationError
	if !errors.As(err, &authErr) {
		t.Fatalf("expected authentication error, got %T", err)
	}
	var malformed *MalformedResponseError
	if !errors.As(err, &malformed) {
		t.Fatalf("expected malformed response error in chain, got %T", err)
	}
}

func TestContextCancellationOrTimeout(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
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

	err := client.Call(ctx, "system", "board", nil, nil)
	if err == nil {
		t.Fatal("expected timeout/cancellation error")
	}
	var transportErr *TransportError
	var authErr *AuthenticationError
	if !errors.As(err, &transportErr) && !errors.As(err, &authErr) {
		t.Fatalf("expected transport/auth cancellation chain, got %T", err)
	}
}

func TestCredentialAndTokenRedactionFromErrors(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := decodeRPCRequest(t, r)
		if req.Params[1].(string) == "session" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"ubus_rpc_session":"token-secret-value","timeout":300}]}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	client := NewClient(Config{
		Remote:   server.URL,
		User:     "root",
		Password: "super-secret-password",
	})

	err := client.Call(context.Background(), "system", "board", nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if containsAny(msg, "super-secret-password", "token-secret-value") {
		t.Fatalf("error leaked sensitive value: %q", msg)
	}
}

func containsAny(s string, values ...string) bool {
	for _, v := range values {
		if v != "" && strings.Contains(s, v) {
			return true
		}
	}
	return false
}
