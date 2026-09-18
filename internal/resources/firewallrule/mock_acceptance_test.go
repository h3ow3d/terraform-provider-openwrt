package firewallrule

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
	mockAddress      = "openwrt_firewall_rule.probe"
	mockName         = "allow-dns"
	mockProviderHost = "registry.terraform.io/h3ow3d/openwrt"
)

func TestAccOpenWRTFirewallRuleMockLifecycle(t *testing.T) {
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
	writeConfig(t, workspace, mock.URL(), "wan", "53")
	runCommand(t, workspace, env, tofuExe, "validate", "-no-color")

	createPlan := filepath.Join(workspace, "create.tfplan")
	requirePlanSummary(t, runCommand(t, workspace, env, tofuExe, "plan", "-parallelism=1", "-input=false", "-no-color", "-out", createPlan), "Plan: 1 to add, 0 to change, 0 to destroy.")
	runCommand(t, workspace, env, tofuExe, "apply", "-parallelism=1", "-input=false", "-no-color", createPlan)
	assertRuleSection(t, mock, true)
	assertNoChanges(t, workspace, env, tofuExe)

	writeConfig(t, workspace, mock.URL(), "", "")
	updatePlan := filepath.Join(workspace, "update.tfplan")
	requirePlanSummary(t, runCommand(t, workspace, env, tofuExe, "plan", "-parallelism=1", "-input=false", "-no-color", "-out", updatePlan), "Plan: 0 to add, 1 to change, 0 to destroy.")
	runCommand(t, workspace, env, tofuExe, "apply", "-parallelism=1", "-input=false", "-no-color", updatePlan)
	assertRuleSection(t, mock, false)

	section := sectionNameForRule(mockName)
	if err := mock.InjectOutOfBandOption("firewall", section, "target", "DROP"); err != nil {
		t.Fatalf("inject drift: %v", err)
	}
	driftPlan := filepath.Join(workspace, "drift.tfplan")
	driftOutput := runCommand(t, workspace, env, tofuExe, "plan", "-parallelism=1", "-input=false", "-no-color", "-out", driftPlan)
	requirePlanSummary(t, driftOutput, "Plan: 0 to add, 1 to change, 0 to destroy.")
	if !strings.Contains(driftOutput, "DROP") || !strings.Contains(driftOutput, "ACCEPT") {
		t.Fatalf("plan did not expose remote drift:\n%s", driftOutput)
	}
	runCommand(t, workspace, env, tofuExe, "apply", "-parallelism=1", "-input=false", "-no-color", driftPlan)
	assertRuleSection(t, mock, false)

	runCommand(t, workspace, env, tofuExe, "state", "rm", mockAddress)
	runCommand(t, workspace, env, tofuExe, "import", "-parallelism=1", mockAddress, mockName)
	assertNoChanges(t, workspace, env, tofuExe)

	destroyPlan := filepath.Join(workspace, "destroy.tfplan")
	requirePlanSummary(t, runCommand(t, workspace, env, tofuExe, "plan", "-destroy", "-parallelism=1", "-input=false", "-no-color", "-out", destroyPlan), "Plan: 0 to add, 0 to change, 1 to destroy.")
	runCommand(t, workspace, env, tofuExe, "apply", "-parallelism=1", "-input=false", "-no-color", destroyPlan)
	if _, exists := mock.Section("firewall", section); exists {
		t.Fatal("managed firewall rule remains after destroy")
	}
	assertRequestScope(t, mock, section)
}

func writeConfig(t *testing.T, workspace, remote, dest, destPort string) {
	t.Helper()
	destBlock := ""
	if dest != "" {
		destBlock = fmt.Sprintf("\n  dest      = %q", dest)
	}
	destPortBlock := ""
	if destPort != "" {
		destPortBlock = fmt.Sprintf("\n  dest_port = %q", destPort)
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

resource "openwrt_firewall_rule" "probe" {
  name    = %q
  src     = "runner"
  target  = "ACCEPT"
  proto   = "udp"
  enabled = true%s%s
}
`, mockProviderHost, remote, mockName, destBlock, destPortBlock)
	writeFile(t, filepath.Join(workspace, "main.tf"), configuration, 0o600)
}

func assertRuleSection(t *testing.T, mock *ubusmock.Server, expectOptional bool) {
	t.Helper()
	section, exists := mock.Section("firewall", sectionNameForRule(mockName))
	if !exists {
		t.Fatal("managed firewall rule section not found")
	}
	if section[".type"] != ruleSectionType || section["name"] != mockName || section["src"] != "runner" || section["target"] != "ACCEPT" {
		t.Fatalf("unexpected firewall rule section: %#v", section)
	}
	_, hasDest := section["dest"]
	_, hasDestPort := section["dest_port"]
	if expectOptional && (!hasDest || !hasDestPort) {
		t.Fatalf("expected optional values to be present: %#v", section)
	}
	if !expectOptional && (hasDest || hasDestPort) {
		t.Fatalf("expected optional values to be absent: %#v", section)
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
			if config, ok := record.Args["config"].(string); ok && config != "firewall" {
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
