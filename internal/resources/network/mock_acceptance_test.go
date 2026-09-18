package network

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
	mockAddress      = "openwrt_network.probe"
	mockName         = "vlan20"
	mockProviderHost = "registry.terraform.io/h3ow3d/openwrt"
)

func TestAccOpenWRTNetworkMockLifecycle(t *testing.T) {
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
	writeConfig(t, workspace, mock.URL(), "192.168.20.1/24", "192.168.20.254", 1500, []string{"192.168.20.1"})
	runCommand(t, workspace, env, tofuExe, "validate", "-no-color")

	createPlan := filepath.Join(workspace, "create.tfplan")
	requirePlanSummary(t, runCommand(t, workspace, env, tofuExe, "plan", "-parallelism=1", "-input=false", "-no-color", "-out", createPlan), "Plan: 1 to add, 0 to change, 0 to destroy.")
	runCommand(t, workspace, env, tofuExe, "apply", "-parallelism=1", "-input=false", "-no-color", createPlan)
	assertInterfaceSection(t, mock, true, true, true)
	assertNoChanges(t, workspace, env, tofuExe)

	writeConfig(t, workspace, mock.URL(), "192.168.20.1/24", "", 0, nil)
	updatePlan := filepath.Join(workspace, "update.tfplan")
	requirePlanSummary(t, runCommand(t, workspace, env, tofuExe, "plan", "-parallelism=1", "-input=false", "-no-color", "-out", updatePlan), "Plan: 0 to add, 1 to change, 0 to destroy.")
	runCommand(t, workspace, env, tofuExe, "apply", "-parallelism=1", "-input=false", "-no-color", updatePlan)
	assertInterfaceSection(t, mock, false, false, false)

	if err := mock.InjectOutOfBandOption("network", mockName, "gateway", "192.168.20.253"); err != nil {
		t.Fatalf("inject drift: %v", err)
	}
	driftPlan := filepath.Join(workspace, "drift.tfplan")
	driftOutput := runCommand(t, workspace, env, tofuExe, "plan", "-parallelism=1", "-input=false", "-no-color", "-out", driftPlan)
	requirePlanSummary(t, driftOutput, "Plan: 0 to add, 1 to change, 0 to destroy.")
	if !strings.Contains(driftOutput, "192.168.20.253") || !strings.Contains(driftOutput, "-> null") {
		t.Fatalf("plan did not expose remote drift:\n%s", driftOutput)
	}
	runCommand(t, workspace, env, tofuExe, "apply", "-parallelism=1", "-input=false", "-no-color", driftPlan)
	assertInterfaceSection(t, mock, false, false, false)

	runCommand(t, workspace, env, tofuExe, "state", "rm", mockAddress)
	runCommand(t, workspace, env, tofuExe, "import", "-parallelism=1", mockAddress, mockName)
	assertNoChanges(t, workspace, env, tofuExe)

	destroyPlan := filepath.Join(workspace, "destroy.tfplan")
	requirePlanSummary(t, runCommand(t, workspace, env, tofuExe, "plan", "-destroy", "-parallelism=1", "-input=false", "-no-color", "-out", destroyPlan), "Plan: 0 to add, 0 to change, 1 to destroy.")
	runCommand(t, workspace, env, tofuExe, "apply", "-parallelism=1", "-input=false", "-no-color", destroyPlan)
	if _, exists := mock.Section("network", mockName); exists {
		t.Fatal("managed interface remains after destroy")
	}
	assertRequestScope(t, mock, mockName)
}

func writeConfig(t *testing.T, workspace, remote, cidr, gateway string, mtu int64, dns []string) {
	t.Helper()
	gatewayBlock := ""
	if gateway != "" {
		gatewayBlock = fmt.Sprintf("\n  gateway   = %q", gateway)
	}
	mtuBlock := ""
	if mtu > 0 {
		mtuBlock = fmt.Sprintf("\n  mtu       = %d", mtu)
	}
	dnsBlock := ""
	if len(dns) > 0 {
		quoted := make([]string, 0, len(dns))
		for _, value := range dns {
			quoted = append(quoted, fmt.Sprintf("%q", value))
		}
		dnsBlock = fmt.Sprintf("\n  dns       = [%s]", strings.Join(quoted, ", "))
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

resource "openwrt_network" "probe" {
  name      = %q
  device    = "br-vlan20"
  proto     = "static"
  cidr      = %q
  delegate  = false
  ip6assign = 60%s%s%s
}
`, mockProviderHost, remote, mockName, cidr, gatewayBlock, mtuBlock, dnsBlock)
	writeFile(t, filepath.Join(workspace, "main.tf"), configuration, 0o600)
}

func assertInterfaceSection(t *testing.T, mock *ubusmock.Server, expectGateway, expectMTU, expectDNS bool) {
	t.Helper()
	section, exists := mock.Section("network", mockName)
	if !exists {
		t.Fatal("managed interface section not found")
	}
	if section[".type"] != interfaceSectionType || section["device"] != "br-vlan20" || section["proto"] != "static" {
		t.Fatalf("unexpected interface section: %#v", section)
	}
	if section["ipaddr"] != "192.168.20.1" || section["netmask"] != "255.255.255.0" {
		t.Fatalf("unexpected ipaddr/netmask values: %#v", section)
	}
	_, hasGateway := section["gateway"]
	if hasGateway != expectGateway {
		t.Fatalf("unexpected gateway presence (want=%v): %#v", expectGateway, section)
	}
	_, hasMTU := section["mtu"]
	if hasMTU != expectMTU {
		t.Fatalf("unexpected mtu presence (want=%v): %#v", expectMTU, section)
	}
	_, hasDNS := section["dns"]
	if hasDNS != expectDNS {
		t.Fatalf("unexpected dns presence (want=%v): %#v", expectDNS, section)
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
