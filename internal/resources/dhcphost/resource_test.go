package dhcphost

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/h3ow3d/terraform-provider-openwrt/internal/client/modernubus"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type fakeHostClient struct {
	rpcURL string
	values map[string]modernubus.UCIGetResponse

	addReqs    []modernubus.UCIAddRequest
	setReqs    []modernubus.UCISetRequest
	deleteReqs []modernubus.UCIDeleteRequest
	applyReqs  []modernubus.UCIApplyRequest
	confirmCnt int
}

func (f *fakeHostClient) CurrentRPCURL() (string, error) { return f.rpcURL, nil }
func (f *fakeHostClient) RunMutationTransaction(ctx context.Context, _ time.Duration, fn func(context.Context) error) error {
	return fn(ctx)
}
func (f *fakeHostClient) UCIGet(_ context.Context, req modernubus.UCIGetRequest) (modernubus.UCIGetResponse, error) {
	if response, ok := f.values[req.Section]; ok {
		return response, nil
	}
	return modernubus.UCIGetResponse{PackageExists: true, Values: map[string]modernubus.UCIValue{}}, nil
}
func (f *fakeHostClient) UCIAdd(_ context.Context, req modernubus.UCIAddRequest) (modernubus.UCIAddResponse, error) {
	f.addReqs = append(f.addReqs, req)
	f.setResponse(req.Name, req.Values)
	return modernubus.UCIAddResponse{Section: req.Name}, nil
}
func (f *fakeHostClient) UCISet(_ context.Context, req modernubus.UCISetRequest) (modernubus.UCISetResponse, error) {
	f.setReqs = append(f.setReqs, req)
	current := f.values[req.Section]
	if current.Values == nil {
		current.Values = map[string]modernubus.UCIValue{}
	}
	for key, value := range req.Values {
		current.Values[key] = modernubus.NewUCIValue(value)
	}
	current.PackageExists = true
	current.SectionExists = true
	f.values[req.Section] = current
	return modernubus.UCISetResponse{}, nil
}
func (f *fakeHostClient) UCIDelete(_ context.Context, req modernubus.UCIDeleteRequest) (modernubus.UCIDeleteResponse, error) {
	f.deleteReqs = append(f.deleteReqs, req)
	if req.Option != "" {
		current := f.values[req.Section]
		delete(current.Values, req.Option)
		f.values[req.Section] = current
	} else {
		delete(f.values, req.Section)
	}
	return modernubus.UCIDeleteResponse{}, nil
}
func (f *fakeHostClient) UCIApply(_ context.Context, req modernubus.UCIApplyRequest) (modernubus.UCIApplyResponse, error) {
	f.applyReqs = append(f.applyReqs, req)
	return modernubus.UCIApplyResponse{}, nil
}
func (f *fakeHostClient) UCIConfirm(_ context.Context, _ modernubus.UCIConfirmRequest) (modernubus.UCIConfirmResponse, error) {
	f.confirmCnt++
	return modernubus.UCIConfirmResponse{}, nil
}
func (f *fakeHostClient) setResponse(section string, raw map[string]any) {
	if f.values == nil {
		f.values = map[string]modernubus.UCIGetResponse{}
	}
	values := map[string]modernubus.UCIValue{}
	for key, value := range raw {
		values[key] = modernubus.NewUCIValue(value)
	}
	f.values[section] = modernubus.UCIGetResponse{PackageExists: true, SectionExists: true, Values: values}
}

func TestSchemaSupportsDNSOnlyHosts(t *testing.T) {
	var response resource.SchemaResponse
	(&Resource{}).Schema(context.Background(), resource.SchemaRequest{}, &response)
	mac, ok := response.Schema.Attributes["mac"].(schema.StringAttribute)
	if !ok || !mac.Optional || mac.Required {
		t.Fatalf("mac must be optional for DNS-only host records")
	}
	name := response.Schema.Attributes["name"].(schema.StringAttribute)
	if len(name.PlanModifiers) == 0 {
		t.Fatalf("name must require replacement")
	}
}

func TestNormalizePlanSupportsDNSOnlyHost(t *testing.T) {
	plan := ResourceModel{
		Name: types.StringValue("PVE01_VM"),
		IP:   types.StringValue("192.168.30.10"),
		DNS:  types.BoolValue(true),
	}
	var diagnostics diag.Diagnostics
	normalized, ok := normalizePlan(plan, &diagnostics)
	if !ok || diagnostics.HasError() {
		t.Fatalf("expected valid DNS-only host: %v", diagnostics)
	}
	if normalized.Name.ValueString() != "pve01_vm" || !normalized.MAC.IsNull() {
		t.Fatalf("unexpected normalized identity: %#v", normalized)
	}
	values := hostValues(normalized)
	if values["name"] != "pve01_vm" || values["ip"] != "192.168.30.10" || values["dns"] != "1" {
		t.Fatalf("unexpected UCI values: %#v", values)
	}
	if _, exists := values["mac"]; exists {
		t.Fatal("DNS-only host must not write an empty mac option")
	}
}

func TestNormalizePlanCollapsesDefaultHostname(t *testing.T) {
	plan := ResourceModel{
		Name:     types.StringValue("server.example.invalid"),
		Hostname: types.StringValue("SERVER.EXAMPLE.INVALID."),
		IP:       types.StringValue("192.0.2.20"),
		DNS:      types.BoolValue(true),
	}
	var diagnostics diag.Diagnostics
	normalized, ok := normalizePlan(plan, &diagnostics)
	if !ok || diagnostics.HasError() {
		t.Fatalf("expected valid host: %v", diagnostics)
	}
	if !normalized.Hostname.IsNull() {
		t.Fatalf("hostname equal to name should use default representation")
	}
}

func TestSectionNameForHostIsStableAndBounded(t *testing.T) {
	first := sectionNameForHost("PVE01_VM")
	second := sectionNameForHost("pve01_vm")
	if first != second || !strings.HasPrefix(first, sectionPrefix) {
		t.Fatalf("unstable section identity: %q %q", first, second)
	}
	if len(first) > len(sectionPrefix)+sectionReadableMax+1+sectionHashHexLen {
		t.Fatalf("section identity exceeds bound: %q", first)
	}
}

func TestCreateHostWritesNamedHostSection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	client := &fakeHostClient{rpcURL: server.URL, values: map[string]modernubus.UCIGetResponse{}}
	desired := normalizedHost(t, "pve01_vm", "192.168.30.10", "pve01.vm.ophomelab.internal", "", "", true)
	if err := createHost(context.Background(), client, desired, 10); err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if len(client.addReqs) != 1 {
		t.Fatalf("expected one add, got %d", len(client.addReqs))
	}
	request := client.addReqs[0]
	if request.Config != dhcpPackage || request.Type != hostSectionType || request.Name != sectionNameForHost("pve01_vm") {
		t.Fatalf("unexpected add request: %#v", request)
	}
	if request.Values["name"] != "pve01.vm.ophomelab.internal" || request.Values["ip"] != "192.168.30.10" {
		t.Fatalf("unexpected host values: %#v", request.Values)
	}
	if len(client.applyReqs) != 1 || client.confirmCnt != 1 {
		t.Fatalf("expected one apply and confirm")
	}
}

func TestUpdateHostRemovesClearedOptionalOptions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	name := "pve01_vm"
	section := sectionNameForHost(name)
	client := &fakeHostClient{rpcURL: server.URL, values: map[string]modernubus.UCIGetResponse{}}
	client.setResponse(section, map[string]any{
		"name": "pve01.vm.ophomelab.internal",
		"ip":   "192.168.30.10",
		"dns":  "1",
		"mac":  "02:11:22:33:44:55",
		"duid": "0001000123456789",
	})
	desired := normalizedHost(t, name, "192.168.30.11", "pve01.vm.ophomelab.internal", "", "", true)
	if err := updateHost(context.Background(), client, desired, 10); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if len(client.deleteReqs) != 2 || client.deleteReqs[0].Option != "mac" || client.deleteReqs[1].Option != "duid" {
		t.Fatalf("expected targeted option deletes: %#v", client.deleteReqs)
	}
	if len(client.setReqs) != 1 || client.setReqs[0].Section != section {
		t.Fatalf("expected one set on stable section")
	}
}

func TestReadLiveHostReturnsRemoteDrift(t *testing.T) {
	name := "pve01_vm"
	client := &fakeHostClient{values: map[string]modernubus.UCIGetResponse{}}
	client.setResponse(sectionNameForHost(name), map[string]any{
		"name": "pve01.vm.ophomelab.internal",
		"ip":   "192.168.30.99",
		"dns":  "0",
	})
	live, exists, err := readLiveHost(context.Background(), client, name)
	if err != nil || !exists {
		t.Fatalf("expected live host: exists=%v err=%v", exists, err)
	}
	if live.IP.ValueString() != "192.168.30.99" || live.DNS.ValueBool() || live.Hostname.ValueString() != "pve01.vm.ophomelab.internal" {
		t.Fatalf("remote drift not represented: %#v", live)
	}
}

func TestDeleteHostIsTargetedAndIdempotent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	name := "pve01_vm"
	section := sectionNameForHost(name)
	client := &fakeHostClient{rpcURL: server.URL, values: map[string]modernubus.UCIGetResponse{}}
	client.setResponse(section, map[string]any{"name": name, "ip": "192.168.30.10", "dns": "1"})
	client.setResponse("unrelated", map[string]any{"name": "keep", "ip": "192.0.2.5"})
	if err := deleteHost(context.Background(), client, name, 10); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if len(client.deleteReqs) != 1 || client.deleteReqs[0].Section != section || client.deleteReqs[0].Option != "" {
		t.Fatalf("unexpected delete requests: %#v", client.deleteReqs)
	}
	if _, exists := client.values["unrelated"]; !exists {
		t.Fatal("unrelated section was modified")
	}
	missing := &fakeHostClient{rpcURL: server.URL, values: map[string]modernubus.UCIGetResponse{}}
	if err := deleteHost(context.Background(), missing, name, 10); err != nil || len(missing.deleteReqs) != 0 {
		t.Fatalf("idempotent delete failed: err=%v requests=%#v", err, missing.deleteReqs)
	}
}

func normalizedHost(t *testing.T, name, ip, hostname, mac, duid string, dns bool) ResourceModel {
	t.Helper()
	plan := ResourceModel{
		Name:     types.StringValue(name),
		MAC:      nullableString(mac),
		IP:       types.StringValue(ip),
		DUID:     nullableString(duid),
		Hostname: nullableString(hostname),
		DNS:      types.BoolValue(dns),
	}
	var diagnostics diag.Diagnostics
	normalized, ok := normalizePlan(plan, &diagnostics)
	if !ok {
		t.Fatalf("normalize failed: %v", diagnostics)
	}
	return normalized
}
