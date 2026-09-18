package device

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

type fakeDeviceClient struct {
	rpcURL string
	values map[string]modernubus.UCIGetResponse

	addReqs    []modernubus.UCIAddRequest
	setReqs    []modernubus.UCISetRequest
	deleteReqs []modernubus.UCIDeleteRequest
	applyReqs  []modernubus.UCIApplyRequest
	confirmCnt int
}

func (f *fakeDeviceClient) CurrentRPCURL() (string, error) { return f.rpcURL, nil }
func (f *fakeDeviceClient) RunMutationTransaction(ctx context.Context, _ time.Duration, fn func(context.Context) error) error {
	return fn(ctx)
}
func (f *fakeDeviceClient) UCIGet(_ context.Context, req modernubus.UCIGetRequest) (modernubus.UCIGetResponse, error) {
	if response, ok := f.values[req.Section]; ok {
		return response, nil
	}
	return modernubus.UCIGetResponse{PackageExists: true, Values: map[string]modernubus.UCIValue{}}, nil
}
func (f *fakeDeviceClient) UCIAdd(_ context.Context, req modernubus.UCIAddRequest) (modernubus.UCIAddResponse, error) {
	f.addReqs = append(f.addReqs, req)
	f.setResponse(req.Name, req.Values)
	return modernubus.UCIAddResponse{Section: req.Name}, nil
}
func (f *fakeDeviceClient) UCISet(_ context.Context, req modernubus.UCISetRequest) (modernubus.UCISetResponse, error) {
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
func (f *fakeDeviceClient) UCIDelete(_ context.Context, req modernubus.UCIDeleteRequest) (modernubus.UCIDeleteResponse, error) {
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
func (f *fakeDeviceClient) UCIApply(_ context.Context, req modernubus.UCIApplyRequest) (modernubus.UCIApplyResponse, error) {
	f.applyReqs = append(f.applyReqs, req)
	return modernubus.UCIApplyResponse{}, nil
}
func (f *fakeDeviceClient) UCIConfirm(_ context.Context, _ modernubus.UCIConfirmRequest) (modernubus.UCIConfirmResponse, error) {
	f.confirmCnt++
	return modernubus.UCIConfirmResponse{}, nil
}
func (f *fakeDeviceClient) setResponse(section string, raw map[string]any) {
	if f.values == nil {
		f.values = map[string]modernubus.UCIGetResponse{}
	}
	values := map[string]modernubus.UCIValue{}
	for key, value := range raw {
		if key == "ports" {
			switch typed := value.(type) {
			case []string:
				converted := make([]any, 0, len(typed))
				for _, item := range typed {
					converted = append(converted, item)
				}
				value = converted
			}
		}
		values[key] = modernubus.NewUCIValue(value)
	}
	f.values[section] = modernubus.UCIGetResponse{PackageExists: true, SectionExists: true, Values: values}
}

func TestSchemaNameRequiresReplacement(t *testing.T) {
	var response resource.SchemaResponse
	(&Resource{}).Schema(context.Background(), resource.SchemaRequest{}, &response)
	name := response.Schema.Attributes["name"].(schema.StringAttribute)
	if len(name.PlanModifiers) == 0 {
		t.Fatal("name must require replacement")
	}
}

func TestNormalizePlanValidationAndPortNormalization(t *testing.T) {
	ports, diagnostics := types.ListValueFrom(context.Background(), types.StringType, []string{"lan2", " lan1 ", "", "lan2"})
	if diagnostics.HasError() {
		t.Fatalf("ports build failed: %v", diagnostics)
	}
	plan := ResourceModel{
		Name:  types.StringValue("Br-Vlan20"),
		Type:  types.StringValue("bridge"),
		Ports: ports,
	}
	var diags diag.Diagnostics
	normalized, ok := normalizePlan(context.Background(), plan, &diags)
	if !ok || diags.HasError() {
		t.Fatalf("expected valid plan: %v", diags)
	}
	if normalized.Name.ValueString() != "br-vlan20" {
		t.Fatalf("unexpected canonical name: %s", normalized.Name.ValueString())
	}
	var normalizedPorts []string
	diags.Append(normalized.Ports.ElementsAs(context.Background(), &normalizedPorts, false)...)
	if diags.HasError() {
		t.Fatalf("ports extraction failed: %v", diags)
	}
	if len(normalizedPorts) != 2 || normalizedPorts[0] != "lan1" || normalizedPorts[1] != "lan2" {
		t.Fatalf("unexpected normalized ports: %#v", normalizedPorts)
	}
}

func TestCreateUpdateReadDeleteLifecycleHelpers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	client := &fakeDeviceClient{rpcURL: server.URL, values: map[string]modernubus.UCIGetResponse{}}
	desired := normalizedDevice(t, "br-vlan20", "bridge", []string{"lan1", "lan2"})
	if err := createDevice(context.Background(), client, desired, 10); err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if len(client.addReqs) != 1 || client.addReqs[0].Type != deviceSectionType {
		t.Fatalf("unexpected add request: %#v", client.addReqs)
	}
	updated := normalizedDevice(t, "br-vlan20", "bridge", nil)
	if err := updateDevice(context.Background(), client, updated, 10); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if len(client.deleteReqs) == 0 || client.deleteReqs[0].Option != "ports" {
		t.Fatalf("expected ports option delete: %#v", client.deleteReqs)
	}
	live, exists, err := readLiveDevice(context.Background(), client, "br-vlan20")
	if err != nil || !exists {
		t.Fatalf("expected live device: exists=%v err=%v", exists, err)
	}
	if !live.Ports.IsNull() {
		t.Fatalf("expected cleared ports in read model: %#v", live)
	}
	if err := deleteDevice(context.Background(), client, "br-vlan20", 10); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if len(client.deleteReqs) < 2 {
		t.Fatalf("expected section delete after option delete: %#v", client.deleteReqs)
	}
}

func TestReadDeviceParsesPortsListAndFallbackName(t *testing.T) {
	client := &fakeDeviceClient{values: map[string]modernubus.UCIGetResponse{}}
	client.setResponse("br-vlan20", map[string]any{
		"type":  "bridge",
		"ports": []string{"lan2", "lan1"},
	})
	live, exists, err := readLiveDevice(context.Background(), client, "br-vlan20")
	if err != nil || !exists {
		t.Fatalf("expected live device: exists=%v err=%v", exists, err)
	}
	if live.Name.ValueString() != "br-vlan20" {
		t.Fatalf("expected fallback name from section key, got %q", live.Name.ValueString())
	}
	var ports []string
	if diags := live.Ports.ElementsAs(context.Background(), &ports, false); diags.HasError() {
		t.Fatalf("ports decode failed: %v", diags)
	}
	if len(ports) != 2 || ports[0] != "lan1" || ports[1] != "lan2" {
		t.Fatalf("unexpected ports ordering: %#v", ports)
	}
}

func TestCanonicalNameValidation(t *testing.T) {
	if _, err := canonicalName(" "); err == nil {
		t.Fatal("expected empty-name validation error")
	}
	if _, err := canonicalName("br vlan20"); err == nil {
		t.Fatal("expected unsupported character validation error")
	}
	name, err := canonicalName("Br.VLAN-20")
	if err != nil || !strings.EqualFold(name, "br.vlan-20") {
		t.Fatalf("unexpected canonical output: %q err=%v", name, err)
	}
}

func normalizedDevice(t *testing.T, name, deviceType string, ports []string) ResourceModel {
	t.Helper()
	plan := ResourceModel{Name: types.StringValue(name), Type: types.StringValue(deviceType)}
	if ports != nil {
		listValue, diagnostics := types.ListValueFrom(context.Background(), types.StringType, ports)
		if diagnostics.HasError() {
			t.Fatalf("ports build failed: %v", diagnostics)
		}
		plan.Ports = listValue
	} else {
		plan.Ports = types.ListNull(types.StringType)
	}
	var diags diag.Diagnostics
	normalized, ok := normalizePlan(context.Background(), plan, &diags)
	if !ok {
		t.Fatalf("normalize failed: %v", diags)
	}
	return normalized
}
