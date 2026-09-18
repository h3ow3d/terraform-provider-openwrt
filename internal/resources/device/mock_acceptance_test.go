package device

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
	mockAddress      = "openwrt_device.probe"
	mockName         = "br-vlan20"
	mockProviderHost = "registry.terraform.io/h3ow3d/openwrt"
)

func TestAccOpenWRTDeviceMockLifecycle(t *testing.T) {
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
	writeConfig(t, workspace, mock.URL(), []string{"lan1", "lan2"})
	runCommand(t, workspace, env, tofuExe, "init", "-input=false", "-no-color")
	runCommand(t, workspace, env, tofuExe, "validate", "-no-color")

	createPlan := filepath.Join(workspace, "create.tfplan")
	requirePlanSummary(t, runCommand(t, workspace, env, tofuExe, "plan", "-parallelism=1", "-input=false", "-no-color", "-out", createPlan), "Plan: 1 to add, 0 to change, 0 to destroy.")
	runCommand(t, workspace, env, tofuExe, "apply", "-parallelism=1", "-input=false", "-no-color", createPlan)
	assertDeviceSection(t, mock, []string{"lan1", "lan2"})
	assertNoChanges(t, workspace, env, tofuExe)

	writeConfig(t, workspace, mock.URL(), nil)
	updatePlan := filepath.Join(workspace, "update.tfplan")
	requirePlanSummary(t, runCommand(t, workspace, env, tofuExe, "plan", "-parallelism=1", "-input=false", "-no-color", "-out", updatePlan), "Plan: 0 to add, 1 to change, 0 to destroy.")
	runCommand(t, workspace, env, tofuExe, "apply", "-parallelism=1", "-input=false", "-no-color", updatePlan)
	assertDeviceSection(t, mock, nil)

	section := sectionNameForDevice(mockName)
	if err := mock.InjectOutOfBandOption("network", section, "type", "8021q"); err != nil {
		t.Fatalf("inject drift: %v", err)
	}
	driftPlan := filepath.Join(workspace, "drift.tfplan")
	driftOutput := runCommand(t, workspace, env, tofuExe, "plan", "-parallelism=1", "-input=false", "-no-color", "-out", driftPlan)
	requirePlanSummary(t, driftOutput, "Plan: 0 to add, 1 to change, 0 to destroy.")
	if !strings.Contains(driftOutput, "8021q") || !strings.Contains(driftOutput, "bridge") {
		t.Fatalf("plan did not expose remote drift:\n%s", driftOutput)
	}
	runCommand(t, workspace, env, tofuExe, "apply", "-parallelism=1", "-input=false", "-no-color", driftPlan)
	assertDeviceSection(t, mock, nil)

	runCommand(t, workspace, env, tofuExe, "state", "rm", mockAddress)
	runCommand(t, workspace, env, tofuExe, "import", "-parallelism=1", mockAddress, mockName)
	assertNoChanges(t, workspace, env, tofuExe)

	destroyPlan := filepath.Join(workspace, "destroy.tfplan")
	requirePlanSummary(t, runCommand(t, workspace, env, tofuExe, "plan", "-destroy", "-parallelism=1", "-input=false", "-no-color", "-out", destroyPlan), "Plan: 0 to add, 0 to change, 1 to destroy.")
	runCommand(t, workspace, env, tofuExe, "apply", "-parallelism=1", "-input=false", "-no-color", destroyPlan)
	if _, exists := mock.Section("network", section); exists {
		t.Fatal("managed network device remains after destroy")
	}
	assertRequestScope(t, mock, section)
}

func writeConfig(t *testing.T, workspace, remote string, ports []string) {
	t.Helper()
	portsBlock := ""
	if len(ports) > 0 {
		quoted := make([]string, 0, len(ports))
		for _, port := range ports {
			quoted = append(quoted, fmt.Sprintf("%q", port))
		}
		portsBlock = fmt.Sprintf("\n  ports = [%s]", strings.Join(quoted, ", "))
	}
	configuration := fmt.Sprintf(`terraform {
  required_providers {
    openwrt = {
      source = %q
    }
  }
}

provider "openwrt" {
  remote   = %q
  user     = "dummy"
  password = "dummy-pass"
}

resource "openwrt_device" "probe" {
  name = %q
  type = "bridge"%s
}
`, mockProviderHost, remote, mockName, portsBlock)
	writeFile(t, filepath.Join(workspace, "main.tf"), configuration, 0o600)
}

func assertDeviceSection(t *testing.T, mock *ubusmock.Server, ports []string) {
	t.Helper()
	section, exists := mock.Section("network", sectionNameForDevice(mockName))
	if !exists {
		t.Fatal("managed network device section not found")
	}
	if section[".type"] != deviceSectionType || section["name"] != mockName || section["type"] != "bridge" {
		t.Fatalf("unexpected network device section: %#v", section)
	}
	value, hasPorts := section["ports"]
	if len(ports) == 0 {
		if hasPorts {
			t.Fatalf("expected ports option to be absent: %#v", section)
		}
		return
	}
	if !hasPorts {
		t.Fatalf("expected ports option: %#v", section)
	}
	decoded, ok := value.([]any)
	if !ok {
		t.Fatalf("unexpected ports type %T", value)
	}
	if len(decoded) != len(ports) {
		t.Fatalf("unexpected ports length: %#v", decoded)
	}
	for index, port := range ports {
		parsed, ok := decoded[index].(string)
		if !ok || parsed != port {
			t.Fatalf("unexpected ports value: %#v", decoded)
		}
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
