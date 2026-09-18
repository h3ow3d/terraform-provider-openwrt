package wireguardinterface

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

type fakeWGInterfaceClient struct {
	rpcURL string
	values map[string]modernubus.UCIGetResponse

	addReqs    []modernubus.UCIAddRequest
	setReqs    []modernubus.UCISetRequest
	deleteReqs []modernubus.UCIDeleteRequest
	applyReqs  []modernubus.UCIApplyRequest
	confirmCnt int
}

func (f *fakeWGInterfaceClient) CurrentRPCURL() (string, error) { return f.rpcURL, nil }
func (f *fakeWGInterfaceClient) RunMutationTransaction(ctx context.Context, _ time.Duration, fn func(context.Context) error) error {
	return fn(ctx)
}
func (f *fakeWGInterfaceClient) UCIGet(_ context.Context, req modernubus.UCIGetRequest) (modernubus.UCIGetResponse, error) {
	if response, ok := f.values[req.Section]; ok {
		return response, nil
	}
	return modernubus.UCIGetResponse{PackageExists: true, Values: map[string]modernubus.UCIValue{}}, nil
}
func (f *fakeWGInterfaceClient) UCIAdd(_ context.Context, req modernubus.UCIAddRequest) (modernubus.UCIAddResponse, error) {
	f.addReqs = append(f.addReqs, req)
	f.setResponse(req.Name, req.Values)
	return modernubus.UCIAddResponse{Section: req.Name}, nil
}
func (f *fakeWGInterfaceClient) UCISet(_ context.Context, req modernubus.UCISetRequest) (modernubus.UCISetResponse, error) {
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
func (f *fakeWGInterfaceClient) UCIDelete(_ context.Context, req modernubus.UCIDeleteRequest) (modernubus.UCIDeleteResponse, error) {
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
func (f *fakeWGInterfaceClient) UCIApply(_ context.Context, req modernubus.UCIApplyRequest) (modernubus.UCIApplyResponse, error) {
	f.applyReqs = append(f.applyReqs, req)
	return modernubus.UCIApplyResponse{}, nil
}
func (f *fakeWGInterfaceClient) UCIConfirm(_ context.Context, _ modernubus.UCIConfirmRequest) (modernubus.UCIConfirmResponse, error) {
	f.confirmCnt++
	return modernubus.UCIConfirmResponse{}, nil
}
func (f *fakeWGInterfaceClient) setResponse(section string, raw map[string]any) {
	if f.values == nil {
		f.values = map[string]modernubus.UCIGetResponse{}
	}
	values := map[string]modernubus.UCIValue{}
	for key, value := range raw {
		if key == "addresses" {
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

func TestNormalizePlanValidationAndAddressNormalization(t *testing.T) {
	addresses, diagnostics := types.ListValueFrom(context.Background(), types.StringType, []string{"10.42.0.1/24", " 10.42.0.1/24 ", "fd00:42::1/64"})
	if diagnostics.HasError() {
		t.Fatalf("addresses build failed: %v", diagnostics)
	}
	plan := ResourceModel{
		Name:       types.StringValue("WG_RUNNER"),
		PrivateKey: types.StringValue("base64-test-private-key="),
		ListenPort: types.Int64Value(51820),
		Addresses:  addresses,
	}
	var diags diag.Diagnostics
	normalized, ok := normalizePlan(context.Background(), plan, &diags)
	if !ok || diags.HasError() {
		t.Fatalf("expected valid plan: %v", diags)
	}
	if normalized.Name.ValueString() != "wg_runner" {
		t.Fatalf("unexpected canonical name: %s", normalized.Name.ValueString())
	}
	var normalizedAddresses []string
	diags.Append(normalized.Addresses.ElementsAs(context.Background(), &normalizedAddresses, false)...)
	if diags.HasError() {
		t.Fatalf("addresses extraction failed: %v", diags)
	}
	if len(normalizedAddresses) != 2 {
		t.Fatalf("unexpected normalized addresses: %#v", normalizedAddresses)
	}
}

func TestCreateUpdateReadDeleteLifecycleHelpers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	client := &fakeWGInterfaceClient{rpcURL: server.URL, values: map[string]modernubus.UCIGetResponse{}}
	desired := normalizedInterface(t, "wg_runner", "base64-test-private-key=", 51820, []string{"10.42.0.1/24"})
	if err := createInterface(context.Background(), client, desired, 10); err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if len(client.addReqs) != 1 || client.addReqs[0].Type != interfaceSectionType {
		t.Fatalf("unexpected add request: %#v", client.addReqs)
	}
	updated := normalizedInterface(t, "wg_runner", "base64-test-private-key=", 0, nil)
	if err := updateInterface(context.Background(), client, updated, 10); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if len(client.deleteReqs) < 2 {
		t.Fatalf("expected option deletes for cleared values: %#v", client.deleteReqs)
	}
	live, exists, err := readLiveInterface(context.Background(), client, "wg_runner")
	if err != nil || !exists {
		t.Fatalf("expected live interface: exists=%v err=%v", exists, err)
	}
	if !live.ListenPort.IsNull() || !live.Addresses.IsNull() {
		t.Fatalf("expected cleared optional values in read model: %#v", live)
	}
	if err := deleteInterface(context.Background(), client, "wg_runner", 10); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
}

func TestCanonicalNameValidation(t *testing.T) {
	if _, err := canonicalName(" "); err == nil {
		t.Fatal("expected empty-name validation error")
	}
	if _, err := canonicalName("wg-runner"); err == nil {
		t.Fatal("expected unsupported character validation error")
	}
	name, err := canonicalName("WG_RUNNER")
	if err != nil || name != "wg_runner" {
		t.Fatalf("unexpected canonical output: %q err=%v", name, err)
	}
}

func normalizedInterface(t *testing.T, name, privateKey string, listenPort int64, addresses []string) ResourceModel {
	t.Helper()
	plan := ResourceModel{Name: types.StringValue(name), PrivateKey: types.StringValue(privateKey)}
	if listenPort > 0 {
		plan.ListenPort = types.Int64Value(listenPort)
	} else {
		plan.ListenPort = types.Int64Null()
	}
	if addresses != nil {
		listValue, diagnostics := types.ListValueFrom(context.Background(), types.StringType, addresses)
		if diagnostics.HasError() {
			t.Fatalf("addresses build failed: %v", diagnostics)
		}
		plan.Addresses = listValue
	} else {
		plan.Addresses = types.ListNull(types.StringType)
	}
	var diags diag.Diagnostics
	normalized, ok := normalizePlan(context.Background(), plan, &diags)
	if !ok {
		t.Fatalf("normalize failed: %v", diags)
	}
	return normalized
}
