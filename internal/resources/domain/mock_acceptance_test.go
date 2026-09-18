package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/h3ow3d/terraform-provider-openwrt/internal/testutil/ubusmock"
)

const (
	mockAccAddress      = "openwrt_domain.probe"
	mockAccInitialName  = "tf-provider-probe.invalid"
	mockAccRenamedName  = "tf-provider-probe-renamed.invalid"
	mockAccInitialIP    = "192.0.2.1"
	mockAccUpdatedIP    = "192.0.2.2"
	mockAccDriftedIP    = "192.0.2.3"
	mockAccProviderHost = "registry.terraform.io/h3ow3d/openwrt"
)

// Test harness design: use a stateful loopback ubus mock + real provider binary + real tofu CLI.
// The test builds the current provider in a temp directory, writes a temp TF_CLI_CONFIG_FILE with
// a dev override, then executes plan/apply/import/destroy through the local tofu binary.
func TestAccOpenWRTDomainMockLifecycle(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" || os.Getenv("OPENWRT_ACC_TARGET") != "mock" {
		t.Skip("set TF_ACC=1 OPENWRT_ACC_TARGET=mock to run mock acceptance")
	}

	mock := ubusmock.NewServer()
	defer mock.Close()
	if !ubusmock.IsLoopbackURL(mock.URL()) {
		t.Fatalf("mock url must be loopback: %s", mock.URL())
	}

	repoRoot := findRepoRoot(t)
	goExe := findExecutable(t, "go", "/opt/homebrew/bin/go")
	tofuExe := findExecutable(t, "tofu", "/opt/homebrew/bin/tofu")

	tempRoot := t.TempDir()
	providerDir := filepath.Join(tempRoot, "provider")
	if err := os.MkdirAll(providerDir, 0o755); err != nil {
		t.Fatalf("create provider dir: %v", err)
	}
	providerBin := filepath.Join(providerDir, "terraform-provider-openwrt")
	runCmd(t, repoRoot, nil, goExe, "build", "-o", providerBin, ".")

	tofurc := filepath.Join(tempRoot, "tofurc")
	writeFile(t, tofurc, fmt.Sprintf(`provider_installation {
  dev_overrides {
    "%s" = "%s"
  }
  direct {}
}
`, mockAccProviderHost, providerDir), 0o600)

	workspace := filepath.Join(tempRoot, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	writeWorkspaceConfig(t, workspace, mock.URL(), mockAccInitialName, mockAccInitialIP)
	env := tofuEnv(t, tofurc)

	runCmd(t, workspace, env, tofuExe, "validate", "-no-color")

	// Create
	createPlan := filepath.Join(workspace, "create.tfplan")
	out := runCmd(t, workspace, env, tofuExe, "plan", "-parallelism=1", "-input=false", "-no-color", "-out", createPlan)
	requireSummary(t, out, "Plan: 1 to add, 0 to change, 0 to destroy.")
	runCmd(t, workspace, env, tofuExe, "apply", "-parallelism=1", "-input=false", "-no-color", createPlan)
	assertStateResource(t, workspace, env, tofuExe, mockAccInitialName, mockAccInitialName, mockAccInitialIP)
	initialSection := sectionNameForDomain(mockAccInitialName)
	assertExactlyOneDomainSection(t, mock, mockAccInitialName, mockAccInitialIP)
	assertEmptyPlan(t, workspace, env, tofuExe)

	// Update
	writeWorkspaceConfig(t, workspace, mock.URL(), mockAccInitialName, mockAccUpdatedIP)
	updatePlan := filepath.Join(workspace, "update.tfplan")
	updateOut := runCmd(t, workspace, env, tofuExe, "plan", "-parallelism=1", "-input=false", "-no-color", "-out", updatePlan)
	requireSummary(t, updateOut, "Plan: 0 to add, 1 to change, 0 to destroy.")
	updateJSON := parsePlanJSON(t, runCmd(t, workspace, env, tofuExe, "show", "-json", updatePlan))
	assertUpdatePlanStableID(t, updateJSON, mockAccInitialName, mockAccInitialIP, mockAccUpdatedIP)
	runCmd(t, workspace, env, tofuExe, "apply", "-parallelism=1", "-input=false", "-no-color", updatePlan)
	assertStateResource(t, workspace, env, tofuExe, mockAccInitialName, mockAccInitialName, mockAccUpdatedIP)
	assertSectionValues(t, mock, initialSection, mockAccInitialName, mockAccUpdatedIP)
	assertExactlyOneDomainSection(t, mock, mockAccInitialName, mockAccUpdatedIP)

	// Drift repair
	if err := mock.InjectOutOfBandOption("dhcp", initialSection, "ip", mockAccDriftedIP); err != nil {
		t.Fatalf("inject drift: %v", err)
	}
	refreshOut := runCmd(t, workspace, env, tofuExe, "plan", "-refresh-only", "-parallelism=1", "-input=false", "-no-color")
	if !strings.Contains(refreshOut, mockAccDriftedIP) || !strings.Contains(refreshOut, mockAccUpdatedIP) {
		t.Fatalf("refresh-only plan did not detect drift:\n%s", refreshOut)
	}
	repairPlan := filepath.Join(workspace, "repair.tfplan")
	repairOut := runCmd(t, workspace, env, tofuExe, "plan", "-parallelism=1", "-input=false", "-no-color", "-out", repairPlan)
	requireSummary(t, repairOut, "Plan: 0 to add, 1 to change, 0 to destroy.")
	runCmd(t, workspace, env, tofuExe, "apply", "-parallelism=1", "-input=false", "-no-color", repairPlan)
	assertSectionValues(t, mock, initialSection, mockAccInitialName, mockAccUpdatedIP)
	assertEmptyPlan(t, workspace, env, tofuExe)

	// Missing-resource recreation
	if err := mock.DeleteCommittedSection("dhcp", initialSection); err != nil {
		t.Fatalf("delete committed section: %v", err)
	}
	recreatePlan := filepath.Join(workspace, "recreate.tfplan")
	recreateOut := runCmd(t, workspace, env, tofuExe, "plan", "-parallelism=1", "-input=false", "-no-color", "-out", recreatePlan)
	requireSummary(t, recreateOut, "Plan: 1 to add, 0 to change, 0 to destroy.")
	runCmd(t, workspace, env, tofuExe, "apply", "-parallelism=1", "-input=false", "-no-color", recreatePlan)
	assertSectionValues(t, mock, initialSection, mockAccInitialName, mockAccUpdatedIP)
	assertStateResource(t, workspace, env, tofuExe, mockAccInitialName, mockAccInitialName, mockAccUpdatedIP)
	assertEmptyPlan(t, workspace, env, tofuExe)

	// Name replacement
	writeWorkspaceConfig(t, workspace, mock.URL(), mockAccRenamedName, mockAccUpdatedIP)
	replacePlan := filepath.Join(workspace, "replace.tfplan")
	replaceOut := runCmd(t, workspace, env, tofuExe, "plan", "-parallelism=1", "-input=false", "-no-color", "-out", replacePlan)
	requireSummary(t, replaceOut, "Plan: 1 to add, 0 to change, 1 to destroy.")
	if !strings.Contains(replaceOut, "must be replaced") {
		t.Fatalf("expected replacement notice in plan:\n%s", replaceOut)
	}
	runCmd(t, workspace, env, tofuExe, "apply", "-parallelism=1", "-input=false", "-no-color", replacePlan)
	newSection := sectionNameForDomain(mockAccRenamedName)
	if _, ok := mock.Section("dhcp", initialSection); ok {
		t.Fatalf("old section still present after replacement")
	}
	assertSectionValues(t, mock, newSection, mockAccRenamedName, mockAccUpdatedIP)
	assertExactlyOneDomainSection(t, mock, mockAccRenamedName, mockAccUpdatedIP)
	assertStateResource(t, workspace, env, tofuExe, mockAccRenamedName, mockAccRenamedName, mockAccUpdatedIP)
	assertEmptyPlan(t, workspace, env, tofuExe)

	// Import
	backupPath := filepath.Join(workspace, "state.backup.json")
	stateBytes := runCmd(t, workspace, env, tofuExe, "state", "pull")
	writeFile(t, backupPath, stateBytes, 0o600)
	runCmd(t, workspace, env, tofuExe, "state", "rm", mockAccAddress)
	assertSectionValues(t, mock, newSection, mockAccRenamedName, mockAccUpdatedIP)
	runCmd(t, workspace, env, tofuExe, "import", "-parallelism=1", mockAccAddress, mockAccRenamedName)
	assertStateResource(t, workspace, env, tofuExe, mockAccRenamedName, mockAccRenamedName, mockAccUpdatedIP)
	assertEmptyPlan(t, workspace, env, tofuExe)

	// Destroy
	destroyPlan := filepath.Join(workspace, "destroy.tfplan")
	destroyOut := runCmd(t, workspace, env, tofuExe, "plan", "-destroy", "-parallelism=1", "-input=false", "-no-color", "-out", destroyPlan)
	requireSummary(t, destroyOut, "Plan: 0 to add, 0 to change, 1 to destroy.")
	runCmd(t, workspace, env, tofuExe, "apply", "-parallelism=1", "-input=false", "-no-color", destroyPlan)
	assertStateEmpty(t, workspace, env, tofuExe)
	if _, ok := mock.Section("dhcp", newSection); ok {
		t.Fatalf("managed section still present after destroy")
	}
	assertSentinelUnchanged(t, mock)
	assertForbiddenCallsAndScope(t, mock, initialSection, newSection)
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	cur := wd
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(cur, "go.mod")); err == nil {
			return cur
		}
		next := filepath.Dir(cur)
		if next == cur {
			break
		}
		cur = next
	}
	t.Fatalf("could not find repo root from %s", wd)
	return ""
}

func findExecutable(t *testing.T, name string, fallback string) string {
	t.Helper()
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	if fallback != "" {
		if _, err := os.Stat(fallback); err == nil {
			return fallback
		}
	}
	t.Fatalf("required executable %q not found", name)
	return ""
}

func tofuEnv(t *testing.T, tofurc string) []string {
	t.Helper()
	env := []string{}
	for _, kv := range os.Environ() {
		key := kv
		if idx := strings.IndexByte(kv, '='); idx >= 0 {
			key = kv[:idx]
		}
		if strings.HasPrefix(key, "OPENWRT_") || strings.HasPrefix(key, "TF_LOG") || strings.HasPrefix(key, "SSH_") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env,
		"TF_CLI_CONFIG_FILE="+tofurc,
		"CHECKPOINT_DISABLE=1",
		"TF_IN_AUTOMATION=1",
	)
	return env
}

func writeWorkspaceConfig(t *testing.T, workspace, remoteURL, name, ip string) {
	t.Helper()
	if !ubusmock.IsLoopbackURL(remoteURL) {
		t.Fatalf("refusing non-loopback remote %s", remoteURL)
	}
	main := fmt.Sprintf(`terraform {
  required_providers {
    openwrt = {
      source = "%s"
				version = "0.2.3"
    }
  }
}

provider "openwrt" {
  remote   = %q
  user     = "dummy"
  password = "dummy-pass"
}

resource "openwrt_domain" "probe" {
  name = %q
  ip   = %q
}
`, mockAccProviderHost, remoteURL, name, ip)
	writeFile(t, filepath.Join(workspace, "main.tf"), main, 0o600)
}

func writeFile(t *testing.T, path string, content any, mode os.FileMode) {
	t.Helper()
	var data []byte
	switch v := content.(type) {
	case string:
		data = []byte(v)
	case []byte:
		data = v
	default:
		t.Fatalf("unsupported content type %T", content)
	}
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatalf("write file %s: %v", path, err)
	}
}

func runCmd(t *testing.T, dir string, env []string, exe string, args ...string) string {
	t.Helper()
	cmd := exec.Command(exe, args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = env
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("command failed: %s %s\n%s", exe, strings.Join(args, " "), string(out))
	}
	return string(out)
}

func requireSummary(t *testing.T, output, summary string) {
	t.Helper()
	if !strings.Contains(output, summary) {
		t.Fatalf("expected summary %q in output:\n%s", summary, output)
	}
}

func assertEmptyPlan(t *testing.T, workspace string, env []string, tofuExe string) {
	t.Helper()
	out := runCmd(t, workspace, env, tofuExe, "plan", "-parallelism=1", "-input=false", "-no-color")
	if !strings.Contains(out, "No changes.") {
		t.Fatalf("expected empty plan, got:\n%s", out)
	}
}

func parsePlanJSON(t *testing.T, raw string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("parse plan json: %v", err)
	}
	return out
}

func assertUpdatePlanStableID(t *testing.T, plan map[string]any, expectedID, beforeIP, afterIP string) {
	t.Helper()
	changes, ok := plan["resource_changes"].([]any)
	if !ok || len(changes) != 1 {
		t.Fatalf("expected exactly one resource change")
	}
	change, ok := changes[0].(map[string]any)
	if !ok {
		t.Fatalf("resource change shape mismatch")
	}
	if change["address"] != mockAccAddress {
		t.Fatalf("unexpected resource address: %v", change["address"])
	}
	changeDetail, ok := change["change"].(map[string]any)
	if !ok {
		t.Fatalf("missing change detail")
	}
	actions := stringSlice(changeDetail["actions"])
	if len(actions) != 1 || actions[0] != "update" {
		t.Fatalf("expected update action, got %v", actions)
	}
	before, _ := changeDetail["before"].(map[string]any)
	after, _ := changeDetail["after"].(map[string]any)
	afterUnknown, _ := changeDetail["after_unknown"].(map[string]any)
	if before["id"] != expectedID || after["id"] != expectedID {
		t.Fatalf("id should remain stable before=%v after=%v", before["id"], after["id"])
	}
	if v, ok := afterUnknown["id"]; ok {
		b, ok := v.(bool)
		if !ok || b {
			t.Fatalf("after_unknown.id must be false/absent, got %v", v)
		}
	}
	if before["ip"] != beforeIP || after["ip"] != afterIP {
		t.Fatalf("unexpected ip delta: %v -> %v", before["ip"], after["ip"])
	}
	if before["name"] != after["name"] {
		t.Fatalf("name should not change during in-place update")
	}
}

func stringSlice(v any) []string {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, _ := item.(string)
		out = append(out, s)
	}
	return out
}

func assertStateResource(t *testing.T, workspace string, env []string, tofuExe string, expectedID, expectedName, expectedIP string) {
	t.Helper()
	pulled := runCmd(t, workspace, env, tofuExe, "state", "pull")
	var state map[string]any
	if err := json.Unmarshal([]byte(pulled), &state); err != nil {
		t.Fatalf("parse state: %v", err)
	}
	values, err := stateResourceValues(state, mockAccAddress)
	if err != nil {
		t.Fatalf("state lookup: %v\nstate:\n%s", err, pulled)
	}
	if values["id"] != expectedID || values["name"] != expectedName || values["ip"] != expectedIP {
		t.Fatalf("unexpected state values: %v", values)
	}
}

func stateResourceValues(state map[string]any, address string) (map[string]any, error) {
	values := map[string]any{}
	if root, ok := state["values"].(map[string]any); ok {
		if module, ok := root["root_module"].(map[string]any); ok {
			if v, ok := readStateModuleResource(module, address); ok {
				return v, nil
			}
		}
	}
	if resources, ok := state["resources"].([]any); ok {
		for _, raw := range resources {
			resource, _ := raw.(map[string]any)
			addr := ""
			if v, ok := resource["address"].(string); ok {
				addr = v
			} else {
				typ, _ := resource["type"].(string)
				name, _ := resource["name"].(string)
				if typ != "" && name != "" {
					addr = typ + "." + name
				}
			}
			if addr != address {
				continue
			}
			instances, _ := resource["instances"].([]any)
			if len(instances) != 1 {
				return nil, fmt.Errorf("expected one instance for %s", address)
			}
			instance, _ := instances[0].(map[string]any)
			attrs, _ := instance["attributes"].(map[string]any)
			for k, v := range attrs {
				values[k] = v
			}
			return values, nil
		}
	}
	return nil, errors.New("resource not found")
}

func readStateModuleResource(module map[string]any, address string) (map[string]any, bool) {
	resources, _ := module["resources"].([]any)
	for _, raw := range resources {
		resource, _ := raw.(map[string]any)
		if resource["address"] != address {
			continue
		}
		values, _ := resource["values"].(map[string]any)
		out := map[string]any{}
		for k, v := range values {
			out[k] = v
		}
		return out, true
	}
	return nil, false
}

func assertStateEmpty(t *testing.T, workspace string, env []string, tofuExe string) {
	t.Helper()
	out := runCmd(t, workspace, env, tofuExe, "state", "list")
	if strings.TrimSpace(out) != "" {
		t.Fatalf("expected empty state, got:\n%s", out)
	}
}

func assertSectionValues(t *testing.T, mock *ubusmock.Server, sectionName, expectedName, expectedIP string) {
	t.Helper()
	values, ok := mock.Section("dhcp", sectionName)
	if !ok {
		t.Fatalf("section %s missing", sectionName)
	}
	if values["name"] != expectedName || values["ip"] != expectedIP {
		t.Fatalf("unexpected section values: %v", values)
	}
}

func assertExactlyOneDomainSection(t *testing.T, mock *ubusmock.Server, expectedName, expectedIP string) {
	t.Helper()
	pkg := mock.PackageSnapshot("dhcp")
	count := 0
	for _, values := range pkg {
		if values["name"] == expectedName {
			if values["ip"] != expectedIP {
				t.Fatalf("section with name %s has unexpected ip %v", expectedName, values["ip"])
			}
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected exactly one managed section for %s, got %d", expectedName, count)
	}
}

func assertSentinelUnchanged(t *testing.T, mock *ubusmock.Server) {
	t.Helper()
	values, ok := mock.Section("dhcp", "sentinel_static")
	if !ok {
		t.Fatalf("sentinel missing")
	}
	if values["name"] != "sentinel.invalid" || values["ip"] != "203.0.113.99" {
		t.Fatalf("sentinel changed: %v", values)
	}
}

func assertForbiddenCallsAndScope(t *testing.T, mock *ubusmock.Server, oldSection, newSection string) {
	t.Helper()
	allowedMethods := map[string]struct{}{
		"session.login": {},
		"uci.get":       {},
		"uci.add":       {},
		"uci.set":       {},
		"uci.delete":    {},
		"uci.changes":   {},
		"uci.revert":    {},
		"uci.commit":    {},
		"uci.apply":     {},
		"uci.confirm":   {},
	}
	mutationMethods := map[string]struct{}{
		"uci.add":    {},
		"uci.set":    {},
		"uci.delete": {},
	}
	allowedSections := map[string]struct{}{
		oldSection: {},
		newSection: {},
	}

	records := mock.RequestHistory()
	slices.SortFunc(records, func(a, b ubusmock.RequestRecord) int { return a.Sequence - b.Sequence })
	if len(records) == 0 {
		t.Fatalf("expected request history")
	}
	for _, record := range records {
		key := record.Object + "." + record.Method
		if _, ok := allowedMethods[key]; !ok {
			t.Fatalf("unexpected method call: %s", key)
		}
		if strings.HasPrefix(key, "file.") || strings.HasPrefix(key, "service.") {
			t.Fatalf("forbidden call observed: %s", key)
		}
		if _, ok := mutationMethods[key]; ok {
			if section, ok := record.Args["section"].(string); ok && section != "" {
				if _, allowed := allowedSections[section]; !allowed {
					t.Fatalf("mutation touched unrelated section %s", section)
				}
			}
			if section, ok := record.Args["name"].(string); ok && section != "" {
				if _, allowed := allowedSections[section]; !allowed {
					t.Fatalf("mutation touched unrelated named section %s", section)
				}
			}
		}
	}

	pendingApply := map[string]int{}
	applies := 0
	confirms := 0
	for _, record := range records {
		key := record.Object + "." + record.Method
		switch key {
		case "uci.apply":
			pendingApply[record.Session]++
			applies++
		case "uci.confirm":
			confirms++
			if pendingApply[record.Session] == 0 {
				t.Fatalf("confirm without matching apply session")
			}
			pendingApply[record.Session]--
		}
	}
	if applies == 0 || confirms == 0 {
		t.Fatalf("expected apply/confirm calls in lifecycle")
	}
	re := regexp.MustCompile(`^sess-[0-9a-f]{8}$`)
	for session, count := range pendingApply {
		if count != 0 {
			t.Fatalf("unmatched apply/confirm for session %s", session)
		}
		if session != "00000000000000000000000000000000" && !re.MatchString(session) {
			t.Fatalf("unexpected session token format")
		}
	}
}
