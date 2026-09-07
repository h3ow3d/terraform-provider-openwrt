package modernubus

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestUCISetRequestAndResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Params[1].(string) == "session" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"ubus_rpc_session":"tokA","timeout":300}]}`))
			return
		}
		if req.Params[1].(string) != "uci" || req.Params[2].(string) != "set" {
			t.Fatalf("unexpected method")
		}
		args := req.Params[3].(map[string]any)
		if args["config"] != "dhcp" || args["section"] != "tf_domain_test" {
			t.Fatalf("unexpected set args")
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0]}`))
	}))
	defer server.Close()

	client := NewClient(Config{Remote: server.URL, User: "root", Password: "secret"})
	_, err := client.UCISet(context.Background(), UCISetRequest{
		Config:  "dhcp",
		Section: "tf_domain_test",
		Values:  map[string]any{"name": "a", "ip": "192.0.2.1"},
	})
	if err != nil {
		t.Fatalf("uci.set failed: %v", err)
	}
}

func TestUCIDeleteRequestAndResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Params[1].(string) == "session" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"ubus_rpc_session":"tokA","timeout":300}]}`))
			return
		}
		if req.Params[2].(string) != "delete" {
			t.Fatalf("unexpected method")
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0]}`))
	}))
	defer server.Close()

	client := NewClient(Config{Remote: server.URL, User: "root", Password: "secret"})
	_, err := client.UCIDelete(context.Background(), UCIDeleteRequest{Config: "dhcp", Section: "tf_domain_test"})
	if err != nil {
		t.Fatalf("uci.delete failed: %v", err)
	}
}

func TestUCIApplyRollbackTimeoutRequest(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Params[1].(string) == "session" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"ubus_rpc_session":"tokA","timeout":300}]}`))
			return
		}
		if req.Params[2].(string) != "apply" {
			t.Fatalf("unexpected method")
		}
		args := req.Params[3].(map[string]any)
		if args["rollback"] != true || int64(args["timeout"].(float64)) != 10 {
			t.Fatalf("unexpected apply args")
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0]}`))
	}))
	defer server.Close()

	client := NewClient(Config{Remote: server.URL, User: "root", Password: "secret"})
	_, err := client.UCIApply(context.Background(), UCIApplyRequest{Rollback: true, Timeout: 10})
	if err != nil {
		t.Fatalf("uci.apply failed: %v", err)
	}
}

func TestUCIConfirmUsesSameSessionWhenPinned(t *testing.T) {
	t.Parallel()
	var sessions []string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		object := req.Params[1].(string)
		method := req.Params[2].(string)
		if object == "session" && method == "login" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"ubus_rpc_session":"tokA","timeout":300}]}`))
			return
		}
		mu.Lock()
		sessions = append(sessions, req.Params[0].(string))
		mu.Unlock()
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0]}`))
	}))
	defer server.Close()

	client := NewClient(Config{Remote: server.URL, User: "root", Password: "secret"})
	err := client.RunMutationTransaction(context.Background(), 0, func(txCtx context.Context) error {
		if _, err := client.UCISet(txCtx, UCISetRequest{Config: "dhcp", Section: "tf_domain_test", Values: map[string]any{"ip": "192.0.2.1"}}); err != nil {
			return err
		}
		_, err := client.UCIConfirm(txCtx, UCIConfirmRequest{})
		return err
	})
	if err != nil {
		t.Fatalf("transaction failed: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sessions) != 2 || sessions[0] != "tokA" || sessions[1] != "tokA" {
		t.Fatalf("session not pinned: %#v", sessions)
	}
}

func TestNoMutationRetryAfterTransportFailure(t *testing.T) {
	t.Parallel()
	var setCalls int
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Params[1].(string) == "session" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"ubus_rpc_session":"tokA","timeout":300}]}`))
			return
		}
		mu.Lock()
		setCalls++
		mu.Unlock()
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatalf("hijack unsupported")
		}
		conn, _, _ := hj.Hijack()
		_ = conn.Close()
	}))
	defer server.Close()

	client := NewClient(Config{Remote: server.URL, User: "root", Password: "secret"})
	_, err := client.UCISet(context.Background(), UCISetRequest{Config: "dhcp", Section: "tf_domain_test", Values: map[string]any{"ip": "192.0.2.1"}})
	if err == nil {
		t.Fatal("expected transport error")
	}
	var transportErr *TransportError
	if !errors.As(err, &transportErr) {
		t.Fatalf("expected transport error, got %T", err)
	}
	mu.Lock()
	gotSetCalls := setCalls
	mu.Unlock()
	if gotSetCalls != 1 {
		t.Fatalf("expected exactly one mutation attempt, got %d", gotSetCalls)
	}
}

func TestRunMutationTransactionSerializes(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Params[1].(string) == "session" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"ubus_rpc_session":"tokA","timeout":300}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0]}`))
	}))
	defer server.Close()

	client := NewClient(Config{Remote: server.URL, User: "root", Password: "secret"})
	start := make(chan struct{})
	release := make(chan struct{})
	order := make(chan string, 2)

	go func() {
		_ = client.RunMutationTransaction(context.Background(), 0, func(txCtx context.Context) error {
			order <- "first-enter"
			close(start)
			<-release
			_, err := client.UCIConfirm(txCtx, UCIConfirmRequest{})
			return err
		})
		order <- "first-exit"
	}()

	<-start
	go func() {
		_ = client.RunMutationTransaction(context.Background(), 0, func(txCtx context.Context) error {
			order <- "second-enter"
			_, err := client.UCIConfirm(txCtx, UCIConfirmRequest{})
			return err
		})
		order <- "second-exit"
	}()

	time.Sleep(30 * time.Millisecond)
	close(release)

	got := []string{<-order, <-order, <-order, <-order}
	expected := []string{"first-enter", "first-exit", "second-enter", "second-exit"}
	for i := range expected {
		if got[i] != expected[i] {
			t.Fatalf("unexpected order: got=%v want=%v", got, expected)
		}
	}
}

func TestEnsureSessionLifetimeRenewsBeforeTransaction(t *testing.T) {
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

	var logins int
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Params[1].(string) == "session" {
			mu.Lock()
			logins++
			current := logins
			mu.Unlock()
			token := "tokA"
			if current > 1 {
				token = "tokB"
			}
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"ubus_rpc_session":"` + token + `","timeout":10}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0]}`))
	}))
	defer server.Close()

	client := NewClient(Config{Remote: server.URL, User: "root", Password: "secret", Now: clock, SessionSkew: time.Second})
	if err := client.Call(context.Background(), "system", "board", nil, nil); err != nil {
		t.Fatalf("seed call failed: %v", err)
	}
	advance(8 * time.Second)
	if err := client.RunMutationTransaction(context.Background(), 5*time.Second, func(context.Context) error { return nil }); err != nil {
		t.Fatalf("transaction setup failed: %v", err)
	}
	mu.Lock()
	gotLogins := logins
	mu.Unlock()
	if gotLogins != 2 {
		t.Fatalf("expected renewal before transaction, got %d logins", gotLogins)
	}
}

func TestMissingSectionReadHandling(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Params[1].(string) == "session" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"ubus_rpc_session":"tokA","timeout":300}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0]}`))
	}))
	defer server.Close()
	client := NewClient(Config{Remote: server.URL, User: "root", Password: "secret"})
	resp, err := client.UCIGet(context.Background(), UCIGetRequest{Config: "dhcp", Section: "missing"})
	if err != nil {
		t.Fatalf("uci.get failed: %v", err)
	}
	if resp.SectionExists {
		t.Fatalf("expected missing section semantics")
	}
}

func TestIdempotentDelete(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Params[1].(string) == "session" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"ubus_rpc_session":"tokA","timeout":300}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[4]}`))
	}))
	defer server.Close()
	client := NewClient(Config{Remote: server.URL, User: "root", Password: "secret"})
	if _, err := client.UCIDelete(context.Background(), UCIDeleteRequest{Config: "dhcp", Section: "missing"}); err != nil {
		t.Fatalf("delete should be idempotent on not found: %v", err)
	}
}

func TestContextCancellationForMutation(t *testing.T) {
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
		HTTPClient: &http.Client{Timeout: 25 * time.Millisecond},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Millisecond)
	defer cancel()
	_, err := client.UCISet(ctx, UCISetRequest{Config: "dhcp", Section: "x"})
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestConcurrentMutationsRaceFree(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Params[1].(string) == "session" {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{"ubus_rpc_session":"tokA","timeout":300}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0]}`))
	}))
	defer server.Close()
	client := NewClient(Config{Remote: server.URL, User: "root", Password: "secret"})
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_ = client.RunMutationTransaction(context.Background(), 0, func(txCtx context.Context) error {
				_, err := client.UCISet(txCtx, UCISetRequest{Config: "dhcp", Section: "s", Values: map[string]any{"k": idx}})
				return err
			})
		}(i)
	}
	wg.Wait()
}

func TestMutationTransportErrorDoesNotLeakValues(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	_ = ln.Close()
	client := NewClient(Config{
		Remote:   "http://" + ln.Addr().String(),
		User:     "root",
		Password: "super-secret-password",
	})
	_, err = client.UCISet(context.Background(), UCISetRequest{
		Config:  "dhcp",
		Section: "x",
		Values:  map[string]any{"marker": "super-secret-value"},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if strings.Contains(msg, "super-secret-password") || strings.Contains(msg, "super-secret-value") {
		t.Fatalf("sensitive value leaked in error")
	}
}
