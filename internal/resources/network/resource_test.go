package network

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/h3ow3d/terraform-provider-openwrt/internal/client/modernubus"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type fakeInterfaceClient struct {
	rpcURL string
	values map[string]modernubus.UCIGetResponse

	addReqs    []modernubus.UCIAddRequest
	setReqs    []modernubus.UCISetRequest
	deleteReqs []modernubus.UCIDeleteRequest
	applyReqs  []modernubus.UCIApplyRequest
	confirmCnt int
}

func (f *fakeInterfaceClient) CurrentRPCURL() (string, error) { return f.rpcURL, nil }
func (f *fakeInterfaceClient) RunMutationTransaction(ctx context.Context, _ time.Duration, fn func(context.Context) error) error {
	return fn(ctx)
}
func (f *fakeInterfaceClient) UCIGet(_ context.Context, req modernubus.UCIGetRequest) (modernubus.UCIGetResponse, error) {
	if response, ok := f.values[req.Section]; ok {
		return response, nil
	}
	return modernubus.UCIGetResponse{PackageExists: true, Values: map[string]modernubus.UCIValue{}}, nil
}
func (f *fakeInterfaceClient) UCIAdd(_ context.Context, req modernubus.UCIAddRequest) (modernubus.UCIAddResponse, error) {
	f.addReqs = append(f.addReqs, req)
	f.setResponse(req.Name, req.Values)
	return modernubus.UCIAddResponse{Section: req.Name}, nil
}
func (f *fakeInterfaceClient) UCISet(_ context.Context, req modernubus.UCISetRequest) (modernubus.UCISetResponse, error) {
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
func (f *fakeInterfaceClient) UCIDelete(_ context.Context, req modernubus.UCIDeleteRequest) (modernubus.UCIDeleteResponse, error) {
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
func (f *fakeInterfaceClient) UCIApply(_ context.Context, req modernubus.UCIApplyRequest) (modernubus.UCIApplyResponse, error) {
	f.applyReqs = append(f.applyReqs, req)
	return modernubus.UCIApplyResponse{}, nil
}
func (f *fakeInterfaceClient) UCIConfirm(_ context.Context, _ modernubus.UCIConfirmRequest) (modernubus.UCIConfirmResponse, error) {
	f.confirmCnt++
	return modernubus.UCIConfirmResponse{}, nil
}
func (f *fakeInterfaceClient) setResponse(section string, raw map[string]any) {
	if f.values == nil {
		f.values = map[string]modernubus.UCIGetResponse{}
	}
	values := map[string]modernubus.UCIValue{}
	for key, value := range raw {
		if key == "dns" {
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

func TestNormalizePlanValidationAndCIDRNormalization(t *testing.T) {
	dns, diagnostics := types.ListValueFrom(context.Background(), types.StringType, []string{"192.168.20.1", " 192.168.20.1 "})
	if diagnostics.HasError() {
		t.Fatalf("dns build failed: %v", diagnostics)
	}
	plan := ResourceModel{
		Name:      types.StringValue("VLAN20"),
		Device:    types.StringValue("br-vlan20"),
		Proto:     types.StringValue("STATIC"),
		CIDR:      types.StringValue("192.168.20.1/24"),
		Gateway:   types.StringValue("192.168.20.254"),
		MTU:       types.Int64Value(1500),
		Delegate:  types.BoolValue(false),
		IP6Assign: types.Int64Value(60),
		DNS:       dns,
	}
	var diags diag.Diagnostics
	normalized, ok := normalizePlan(context.Background(), plan, &diags)
	if !ok || diags.HasError() {
		t.Fatalf("expected valid plan: %v", diags)
	}
	if normalized.Name.ValueString() != "vlan20" {
		t.Fatalf("unexpected canonical name: %s", normalized.Name.ValueString())
	}
	if normalized.CIDR.ValueString() != "192.168.20.1/24" {
		t.Fatalf("unexpected CIDR value: %s", normalized.CIDR.ValueString())
	}
	var normalizedDNS []string
	diags.Append(normalized.DNS.ElementsAs(context.Background(), &normalizedDNS, false)...)
	if diags.HasError() {
		t.Fatalf("dns extraction failed: %v", diags)
	}
	if len(normalizedDNS) != 1 {
		t.Fatalf("unexpected normalized dns: %#v", normalizedDNS)
	}
}

func TestCreateUpdateReadDeleteLifecycleHelpers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	client := &fakeInterfaceClient{rpcURL: server.URL, values: map[string]modernubus.UCIGetResponse{}}
	desired := normalizedInterface(t, "vlan20", "br-vlan20", "192.168.20.1/24", "192.168.20.254", 1500, false, 60, []string{"192.168.20.1"})
	if err := createInterface(context.Background(), client, desired, 10); err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if len(client.addReqs) != 1 || client.addReqs[0].Type != interfaceSectionType {
		t.Fatalf("unexpected add request: %#v", client.addReqs)
	}
	updated := normalizedInterface(t, "vlan20", "br-vlan20", "192.168.20.1/24", "", 0, false, 0, nil)
	if err := updateInterface(context.Background(), client, updated, 10); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if len(client.deleteReqs) < 3 {
		t.Fatalf("expected option deletes for cleared values: %#v", client.deleteReqs)
	}
	live, exists, err := readLiveInterface(context.Background(), client, "vlan20")
	if err != nil || !exists {
		t.Fatalf("expected live interface: exists=%v err=%v", exists, err)
	}
	if !live.Gateway.IsNull() || !live.MTU.IsNull() || !live.DNS.IsNull() {
		t.Fatalf("expected cleared optional values in read model: %#v", live)
	}
	if err := deleteInterface(context.Background(), client, "vlan20", 10); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
}

func TestCanonicalNameValidation(t *testing.T) {
	if _, err := canonicalName(" "); err == nil {
		t.Fatal("expected empty-name validation error")
	}
	if _, err := canonicalName("vlan-20"); err == nil {
		t.Fatal("expected unsupported character validation error")
	}
	name, err := canonicalName("VLAN20")
	if err != nil || name != "vlan20" {
		t.Fatalf("unexpected canonical output: %q err=%v", name, err)
	}
}

func normalizedInterface(t *testing.T, name, device, cidr, gateway string, mtu int64, delegate bool, ip6assign int64, dns []string) ResourceModel {
	t.Helper()
	plan := ResourceModel{
		Name:     types.StringValue(name),
		Device:   types.StringValue(device),
		Proto:    types.StringValue("static"),
		Delegate: types.BoolValue(delegate),
	}
	if cidr != "" {
		plan.CIDR = types.StringValue(cidr)
	} else {
		plan.CIDR = types.StringNull()
	}
	if gateway != "" {
		plan.Gateway = types.StringValue(gateway)
	} else {
		plan.Gateway = types.StringNull()
	}
	if mtu > 0 {
		plan.MTU = types.Int64Value(mtu)
	} else {
		plan.MTU = types.Int64Null()
	}
	if ip6assign > 0 {
		plan.IP6Assign = types.Int64Value(ip6assign)
	} else {
		plan.IP6Assign = types.Int64Null()
	}
	if dns != nil {
		listValue, diagnostics := types.ListValueFrom(context.Background(), types.StringType, dns)
		if diagnostics.HasError() {
			t.Fatalf("dns build failed: %v", diagnostics)
		}
		plan.DNS = listValue
	} else {
		plan.DNS = types.ListNull(types.StringType)
	}
	var diags diag.Diagnostics
	normalized, ok := normalizePlan(context.Background(), plan, &diags)
	if !ok {
		t.Fatalf("normalize failed: %v", diags)
	}
	return normalized
}
