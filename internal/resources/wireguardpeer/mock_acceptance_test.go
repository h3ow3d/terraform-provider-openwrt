package wireguardpeer

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/h3ow3d/terraform-provider-openwrt/internal/testutil/ubusmock"
)

const (
	mockAddress      = "openwrt_wireguard_peer.probe"
	mockName         = "actions"
	mockProviderHost = "registry.terraform.io/h3ow3d/openwrt"
)

func TestAccOpenWRTWireGuardPeerMockLifecycle(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" || os.Getenv("OPENWRT_ACC_TARGET") != "mock" {
		t.Skip("set TF_ACC=1 OPENWRT_ACC_TARGET=mock to run mock acceptance")
	}

	mock := ubusmock.NewServer()
	defer mock.Close()
	if !ubusmock.IsLoopbackURL(mock.URL()) {
		t.Fatalf("mock URL must be loopback: %s", mock.URL())
	}

	repoRoot := findRepoRoot(t)
	goExe := findExecutable(t, "go", "/opt/homebrew/bin/go")
	tofuExe := findExecutable(t, "tofu", "/opt/homebrew/bin/tofu")
	tempRoot := t.TempDir()
	providerDir := filepath.Join(tempRoot, "provider")
	if err := os.MkdirAll(providerDir, 0o755); err != nil {
		t.Fatalf("create provider directory: %v", err)
	}
	runCommand(t, repoRoot, nil, goExe, "build", "-o", filepath.Join(providerDir, "terraform-provider-openwrt"), ".")

	cliConfig := filepath.Join(tempRoot, "tofurc")
	writeFile(t, cliConfig, fmt.Sprintf(`provider_installation {
  dev_overrides {
    %q = %q
  }
  direct {}
}
`, mockProviderHost, providerDir), 0o600)
	workspace := filepath.Join(tempRoot, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	env := tofuEnvironment(cliConfig)
	writeConfig(t, workspace, mock.URL(), []string{"10.1.0.1/32"}, "host.example.net")
	runCommand(t, workspace, env, tofuExe, "validate", "-no-color")

	createPlan := filepath.Join(workspace, "create.tfplan")
	requirePlanSummary(t, runCommand(t, workspace, env, tofuExe, "plan", "-parallelism=1", "-input=false", "-no-color", "-out", createPlan), "Plan: 1 to add, 0 to change, 0 to destroy.")
	runCommand(t, workspace, env, tofuExe, "apply", "-parallelism=1", "-input=false", "-no-color", createPlan)
	assertPeerSection(t, mock, true)
	assertNoChanges(t, workspace, env, tofuExe)

	writeConfig(t, workspace, mock.URL(), []string{"10.1.0.1/32"}, "")
	updatePlan := filepath.Join(workspace, "update.tfplan")
	requirePlanSummary(t, runCommand(t, workspace, env, tofuExe, "plan", "-parallelism=1", "-input=false", "-no-color", "-out", updatePlan), "Plan: 0 to add, 1 to change, 0 to destroy.")
	runCommand(t, workspace, env, tofuExe, "apply", "-parallelism=1", "-input=false", "-no-color", updatePlan)
	assertPeerSection(t, mock, false)

	section := sectionNameForPeer("wg_runner", mockName)
	if err := mock.InjectOutOfBandOption("network", section, "endpoint_host", "drifted.example.net"); err != nil {
		t.Fatalf("inject drift: %v", err)
	}
	driftPlan := filepath.Join(workspace, "drift.tfplan")
	driftOutput := runCommand(t, workspace, env, tofuExe, "plan", "-parallelism=1", "-input=false", "-no-color", "-out", driftPlan)
	requirePlanSummary(t, driftOutput, "Plan: 0 to add, 1 to change, 0 to destroy.")
	if !strings.Contains(driftOutput, "drifted.example.net") || !strings.Contains(driftOutput, "-> null") {
		t.Fatalf("plan did not expose remote drift:\n%s", driftOutput)
	}
	runCommand(t, workspace, env, tofuExe, "apply", "-parallelism=1", "-input=false", "-no-color", driftPlan)
	assertPeerSection(t, mock, false)

	runCommand(t, workspace, env, tofuExe, "state", "rm", mockAddress)
	runCommand(t, workspace, env, tofuExe, "import", "-parallelism=1", mockAddress, "wg_runner/"+mockName)
	assertNoChanges(t, workspace, env, tofuExe)

	destroyPlan := filepath.Join(workspace, "destroy.tfplan")
	requirePlanSummary(t, runCommand(t, workspace, env, tofuExe, "plan", "-destroy", "-parallelism=1", "-input=false", "-no-color", "-out", destroyPlan), "Plan: 0 to add, 0 to change, 1 to destroy.")
	runCommand(t, workspace, env, tofuExe, "apply", "-parallelism=1", "-input=false", "-no-color", destroyPlan)
	if _, exists := mock.Section("network", section); exists {
		t.Fatal("managed WireGuard peer remains after destroy")
	}
	assertRequestScope(t, mock, section)
}

func writeConfig(t *testing.T, workspace, remote string, allowedIPs []string, endpointHost string) {
	t.Helper()
	allowed := make([]string, 0, len(allowedIPs))
	for _, ip := range allowedIPs {
		allowed = append(allowed, fmt.Sprintf("%q", ip))
	}
	endpointBlock := ""
	if endpointHost != "" {
		endpointBlock = fmt.Sprintf("\n  endpoint_host = %q", endpointHost)
	}
	configuration := fmt.Sprintf(`terraform {
  required_providers {
    openwrt = {
      source = %q
			version = "0.2.3"
    }
  }
}

provider "openwrt" {
  remote   = %q
  user     = "dummy"
  password = "dummy-pass"
}

resource "openwrt_wireguard_peer" "probe" {
  name                 = %q
  interface            = "wg_runner"
  public_key           = "base64-test-public-key="
  allowed_ips          = [%s]
  endpoint_port        = 51820
  persistent_keepalive = 25%s
}
`, mockProviderHost, remote, mockName, strings.Join(allowed, ", "), endpointBlock)
	writeFile(t, filepath.Join(workspace, "main.tf"), configuration, 0o600)
}

func assertPeerSection(t *testing.T, mock *ubusmock.Server, expectEndpointHost bool) {
	t.Helper()
	section, exists := mock.Section("network", sectionNameForPeer("wg_runner", mockName))
	if !exists {
		t.Fatal("managed WireGuard peer section not found")
	}
	if section[".type"] != peerSectionType("wg_runner") || section["public_key"] != "base64-test-public-key=" {
		t.Fatalf("unexpected WireGuard peer section: %#v", section)
	}
	if _, ok := section["allowed_ips"]; !ok {
		t.Fatalf("expected allowed_ips option: %#v", section)
	}
	_, hasEndpointHost := section["endpoint_host"]
	if hasEndpointHost != expectEndpointHost {
		t.Fatalf("unexpected endpoint_host presence (want=%v): %#v", expectEndpointHost, section)
	}
}

func assertRequestScope(t *testing.T, mock *ubusmock.Server, expectedSection string) {
	t.Helper()
	for _, record := range mock.RequestHistory() {
		if record.Object != "uci" {
			continue
		}
		switch record.Method {
		case "get", "add", "set", "delete":
			if config, ok := record.Args["config"].(string); ok && config != "network" {
				t.Fatalf("request targeted unexpected config %q", config)
			}
			if section, ok := record.Args["section"].(string); ok && section != "" && section != expectedSection {
				t.Fatalf("request targeted unexpected section %q", section)
			}
			if name, ok := record.Args["name"].(string); ok && name != "" && name != expectedSection {
				t.Fatalf("request added unexpected section %q", name)
			}
		case "apply", "confirm":
		default:
			t.Fatalf("unexpected UCI method %q", record.Method)
		}
	}
}

func assertNoChanges(t *testing.T, workspace string, env []string, tofu string) {
	t.Helper()
	output := runCommand(t, workspace, env, tofu, "plan", "-parallelism=1", "-input=false", "-no-color")
	if !strings.Contains(output, "No changes.") {
		t.Fatalf("expected no changes:\n%s", output)
	}
}

func requirePlanSummary(t *testing.T, output, expected string) {
	t.Helper()
	if !strings.Contains(output, expected) {
		t.Fatalf("expected %q:\n%s", expected, output)
	}
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	current := workingDirectory
	for range 8 {
		if _, err := os.Stat(filepath.Join(current, "go.mod")); err == nil {
			return current
		}
		next := filepath.Dir(current)
		if next == current {
			break
		}
		current = next
	}
	t.Fatalf("could not find repository root from %s", workingDirectory)
	return ""
}

func findExecutable(t *testing.T, name, fallback string) string {
	t.Helper()
	if executable, err := exec.LookPath(name); err == nil {
		return executable
	}
	if _, err := os.Stat(fallback); err == nil {
		return fallback
	}
	t.Fatalf("required executable %q not found", name)
	return ""
}

func tofuEnvironment(cliConfig string) []string {
	environment := []string{}
	for _, variable := range os.Environ() {
		key := strings.SplitN(variable, "=", 2)[0]
		if strings.HasPrefix(key, "OPENWRT_") || strings.HasPrefix(key, "TF_LOG") || strings.HasPrefix(key, "SSH_") {
			continue
		}
		environment = append(environment, variable)
	}
	return append(environment, "TF_CLI_CONFIG_FILE="+cliConfig, "CHECKPOINT_DISABLE=1", "TF_IN_AUTOMATION=1")
}

func runCommand(t *testing.T, directory string, environment []string, executable string, args ...string) string {
	t.Helper()
	command := exec.Command(executable, args...)
	command.Dir = directory
	if environment != nil {
		command.Env = environment
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("command failed: %s %s\n%s", executable, strings.Join(args, " "), output)
	}
	return string(output)
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
