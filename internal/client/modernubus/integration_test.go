//go:build integration

package modernubus

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestIntegrationReadOnlySystemBoardAndUCIGet(t *testing.T) {
	cfg, ok := integrationConfigFromEnv(t)
	if !ok {
		return
	}

	client := NewClient(Config{
		Remote:   cfg.Remote,
		User:     cfg.User,
		Password: cfg.Password,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var board map[string]any
	if err := client.Call(ctx, "system", "board", map[string]any{}, &board); err != nil {
		t.Fatalf("system.board failed: %v", err)
	}
	if len(board) == 0 {
		t.Fatal("system.board returned empty object")
	}
	if _, ok := board["model"]; !ok {
		t.Fatal("system.board missing model field")
	}

	resp, err := client.UCIGet(ctx, UCIGetRequest{Config: cfg.ReadPackage})
	if err != nil {
		t.Fatalf("uci.get(%s) failed: %v", cfg.ReadPackage, err)
	}
	if !resp.PackageExists {
		t.Fatalf("expected package %q to be readable", cfg.ReadPackage)
	}
}

func TestIntegrationPhase2AStagedAddChangesAndRevert(t *testing.T) {
	cfg, ok := integrationConfigFromEnv(t)
	if !ok {
		return
	}

	beforeHTTP := httpStatusCode(t, cfg.Remote+"/cgi-bin/luci/admin/ubus")

	fixturePath := "/etc/config/tf_probe_cap"
	fixtureCreated := false
	if out, err := runSSHCommand(cfg, "test -e "+fixturePath+" && echo EXISTS || echo ABSENT"); err != nil {
		t.Fatalf("fixture presence check failed: %v", err)
	} else if strings.Contains(out, "EXISTS") {
		t.Fatalf("fixture %s already exists unexpectedly; aborting per gate", fixturePath)
	}

	if _, err := runSSHCommand(cfg, "touch "+fixturePath+" && chmod 600 "+fixturePath); err != nil {
		t.Fatalf("fixture create failed: %v", err)
	}
	fixtureCreated = true

	clientA := NewClient(Config{
		Remote:   cfg.Remote,
		User:     cfg.User,
		Password: cfg.Password,
	})
	clientB := NewClient(Config{
		Remote:   cfg.Remote,
		User:     cfg.User,
		Password: cfg.Password,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	cleanup := func() {
		_, _ = clientA.UCIRevert(context.Background(), UCIRevertRequest{Config: "tf_probe_cap"})
		if fixtureCreated {
			if out, err := runSSHCommand(cfg, "if grep -q '^config ' "+fixturePath+"; then echo NOT_EMPTY; else echo EMPTY; fi"); err == nil && strings.Contains(out, "EMPTY") {
				_, _ = runSSHCommand(cfg, "rm -f "+fixturePath)
			}
		}
	}
	defer cleanup()

	pkg, err := clientA.UCIGet(ctx, UCIGetRequest{Config: "tf_probe_cap"})
	if err != nil {
		t.Fatalf("session A uci.get package failed: %v", err)
	}
	if !pkg.EmptyPackage {
		t.Fatalf("expected empty tf_probe_cap package at start")
	}

	addResp, err := clientA.UCIAdd(ctx, UCIAddRequest{
		Config: "tf_probe_cap",
		Type:   "meta",
		Name:   "provider_probe",
		Values: map[string]any{"marker": "phase-2a"},
	})
	if err != nil {
		t.Fatalf("session A uci.add failed: %v", err)
	}
	if addResp.Section != "provider_probe" {
		t.Fatalf("expected provider_probe section, got %q", addResp.Section)
	}

	changesResp, err := clientA.UCIChanges(ctx, UCIChangesRequest{Config: "tf_probe_cap"})
	if err != nil {
		t.Fatalf("session A uci.changes failed: %v", err)
	}
	if !changesContainExpectedDelta(changesResp) {
		t.Fatalf("session A uci.changes missing expected delta")
	}

	sectionA, err := clientA.UCIGet(ctx, UCIGetRequest{Config: "tf_probe_cap", Section: "provider_probe"})
	if err != nil {
		t.Fatalf("session A section get failed: %v", err)
	}
	markerA, ok := sectionA.Values["marker"].String()
	if !ok || markerA != "phase-2a" {
		t.Fatalf("session A expected marker phase-2a in staged delta")
	}

	sectionB, err := clientB.UCIGet(ctx, UCIGetRequest{Config: "tf_probe_cap", Section: "provider_probe"})
	if err != nil {
		t.Fatalf("session B section get failed: %v", err)
	}
	if sectionB.SectionExists || len(sectionB.Values) != 0 {
		t.Fatalf("session B unexpectedly sees uncommitted staged section")
	}

	if _, err := clientA.UCIRevert(ctx, UCIRevertRequest{Config: "tf_probe_cap"}); err != nil {
		t.Fatalf("session A revert failed: %v", err)
	}

	changesAfter, err := clientA.UCIChanges(ctx, UCIChangesRequest{Config: "tf_probe_cap"})
	if err != nil {
		t.Fatalf("session A changes after revert failed: %v", err)
	}
	if len(changesAfter.Changes) != 0 {
		t.Fatalf("expected empty changes after revert, got %d", len(changesAfter.Changes))
	}

	sectionAAfter, err := clientA.UCIGet(ctx, UCIGetRequest{Config: "tf_probe_cap", Section: "provider_probe"})
	if err != nil {
		t.Fatalf("session A section get after revert failed: %v", err)
	}
	if sectionAAfter.SectionExists || len(sectionAAfter.Values) != 0 {
		t.Fatalf("session A section still visible after revert")
	}

	pkgB, err := clientB.UCIGet(ctx, UCIGetRequest{Config: "tf_probe_cap"})
	if err != nil {
		t.Fatalf("session B package get after revert failed: %v", err)
	}
	if !pkgB.EmptyPackage {
		t.Fatalf("session B package not empty after revert")
	}

	emptyCheck, err := runSSHCommand(cfg, "if grep -q '^config ' "+fixturePath+"; then echo NOT_EMPTY; else echo EMPTY; fi")
	if err != nil {
		t.Fatalf("fixture empty-check failed: %v", err)
	}
	if !strings.Contains(emptyCheck, "EMPTY") {
		t.Fatalf("fixture contains sections unexpectedly")
	}

	if _, err := runSSHCommand(cfg, "rm -f "+fixturePath); err != nil {
		t.Fatalf("fixture remove failed: %v", err)
	}
	fixtureCreated = false

	afterHTTP := httpStatusCode(t, cfg.Remote+"/cgi-bin/luci/admin/ubus")
	if beforeHTTP != 200 || afterHTTP != 200 {
		t.Fatalf("router connectivity check failed before=%d after=%d", beforeHTTP, afterHTTP)
	}
}

type integrationConfig struct {
	Remote      string
	User        string
	Password    string
	ReadPackage string
	SSHPassword string
}

func integrationConfigFromEnv(t *testing.T) (integrationConfig, bool) {
	t.Helper()
	if os.Getenv("OPENWRT_INTEGRATION") != "1" {
		t.Skip("set OPENWRT_INTEGRATION=1 to run integration tests")
		return integrationConfig{}, false
	}

	cfg := integrationConfig{
		Remote:      os.Getenv("OPENWRT_REMOTE"),
		User:        os.Getenv("OPENWRT_USER"),
		Password:    os.Getenv("OPENWRT_PASSWORD"),
		ReadPackage: os.Getenv("OPENWRT_INTEGRATION_UCI_PACKAGE"),
		SSHPassword: os.Getenv("OPENWRT_SSH_PASSWORD"),
	}
	if cfg.Remote == "" || cfg.User == "" || cfg.Password == "" || cfg.ReadPackage == "" {
		t.Skip("integration config missing: OPENWRT_REMOTE, OPENWRT_USER, OPENWRT_PASSWORD, OPENWRT_INTEGRATION_UCI_PACKAGE")
		return integrationConfig{}, false
	}
	if cfg.SSHPassword == "" {
		cfg.SSHPassword = cfg.Password
	}
	return cfg, true
}

func runSSHCommand(cfg integrationConfig, command string) (string, error) {
	u, err := url.Parse(cfg.Remote)
	if err != nil || u.Hostname() == "" {
		return "", fmt.Errorf("invalid OPENWRT_REMOTE for ssh host parsing")
	}
	host := u.Hostname()
	pw := strings.ReplaceAll(cfg.SSHPassword, "\\", "\\\\")
	pw = strings.ReplaceAll(pw, "\"", "\\\"")
	cmd := strings.ReplaceAll(command, "\"", "\\\"")

	script := strings.Join([]string{
		"set timeout 20",
		"log_user 0",
		fmt.Sprintf("spawn ssh -o StrictHostKeyChecking=accept-new -o ConnectTimeout=10 %s@%s \"%s\"", cfg.User, host, cmd),
		"expect {",
		"  -re \"(?i)yes/no\" { send \"yes\\r\"; exp_continue }",
		fmt.Sprintf("  -re \"(?i)password:\" { send \"%s\\r\" }", pw),
		"  timeout { puts \"__TIMEOUT__\"; exit 124 }",
		"}",
		"expect eof",
		"set out $expect_out(buffer)",
		"puts $out",
	}, "\n")

	execCmd := exec.Command("expect", "-c", script)
	output, runErr := execCmd.CombinedOutput()
	out := strings.TrimSpace(string(output))
	if runErr != nil {
		return out, fmt.Errorf("ssh command failed")
	}
	return out, nil
}

func changesContainExpectedDelta(resp UCIChangesResponse) bool {
	hasType := false
	hasMarker := false
	for _, change := range resp.Changes {
		op, ok := change.StringAt(0)
		if !ok || op != "set" {
			continue
		}
		section, ok := change.StringAt(1)
		if !ok || section != "provider_probe" {
			continue
		}
		third, ok := change.StringAt(2)
		if !ok {
			continue
		}
		if third == "meta" {
			hasType = true
			continue
		}
		if third == "marker" {
			if val, ok := change.StringAt(3); ok && val == "phase-2a" {
				hasMarker = true
			}
		}
	}
	return hasType && hasMarker
}

func httpStatusCode(t *testing.T, rawURL string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatalf("create connectivity request: %v", err)
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("connectivity request failed: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}
