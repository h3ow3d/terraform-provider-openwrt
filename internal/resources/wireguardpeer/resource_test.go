package wireguardpeer

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

type fakeWireGuardPeerClient struct {
	rpcURL string
	values map[string]modernubus.UCIGetResponse

	addReqs    []modernubus.UCIAddRequest
	setReqs    []modernubus.UCISetRequest
	deleteReqs []modernubus.UCIDeleteRequest
	applyReqs  []modernubus.UCIApplyRequest
	confirmCnt int
}

func (f *fakeWireGuardPeerClient) CurrentRPCURL() (string, error) { return f.rpcURL, nil }
func (f *fakeWireGuardPeerClient) RunMutationTransaction(ctx context.Context, _ time.Duration, fn func(context.Context) error) error {
	return fn(ctx)
}
func (f *fakeWireGuardPeerClient) UCIGet(_ context.Context, req modernubus.UCIGetRequest) (modernubus.UCIGetResponse, error) {
	if response, ok := f.values[req.Section]; ok {
		return response, nil
	}
	return modernubus.UCIGetResponse{PackageExists: true, Values: map[string]modernubus.UCIValue{}}, nil
}
func (f *fakeWireGuardPeerClient) UCIAdd(_ context.Context, req modernubus.UCIAddRequest) (modernubus.UCIAddResponse, error) {
	f.addReqs = append(f.addReqs, req)
	f.setResponse(req.Name, req.Type, req.Values)
	return modernubus.UCIAddResponse{Section: req.Name}, nil
}
func (f *fakeWireGuardPeerClient) UCISet(_ context.Context, req modernubus.UCISetRequest) (modernubus.UCISetResponse, error) {
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
func (f *fakeWireGuardPeerClient) UCIDelete(_ context.Context, req modernubus.UCIDeleteRequest) (modernubus.UCIDeleteResponse, error) {
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
func (f *fakeWireGuardPeerClient) UCIApply(_ context.Context, req modernubus.UCIApplyRequest) (modernubus.UCIApplyResponse, error) {
	f.applyReqs = append(f.applyReqs, req)
	return modernubus.UCIApplyResponse{}, nil
}
func (f *fakeWireGuardPeerClient) UCIConfirm(_ context.Context, _ modernubus.UCIConfirmRequest) (modernubus.UCIConfirmResponse, error) {
	f.confirmCnt++
	return modernubus.UCIConfirmResponse{}, nil
}
func (f *fakeWireGuardPeerClient) setResponse(section, sectionType string, raw map[string]any) {
	if f.values == nil {
		f.values = map[string]modernubus.UCIGetResponse{}
	}
	values := map[string]modernubus.UCIValue{".type": modernubus.NewUCIValue(sectionType)}
	for key, value := range raw {
		if key == "allowed_ips" {
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

func TestNormalizePlanValidationAndListNormalization(t *testing.T) {
	allowed, diagnostics := types.ListValueFrom(context.Background(), types.StringType, []string{"10.1.0.0/24", " 10.1.0.0/24 "})
	if diagnostics.HasError() {
		t.Fatalf("allowed_ips build failed: %v", diagnostics)
	}
	plan := ResourceModel{
		Name:       types.StringValue("actions"),
		Interface:  types.StringValue("WG_RUNNER"),
		PublicKey:  types.StringValue("base64-test-public-key="),
		AllowedIPs: allowed,
	}
	var diags diag.Diagnostics
	normalized, ok := normalizePlan(context.Background(), plan, &diags)
	if !ok || diags.HasError() {
		t.Fatalf("expected valid plan: %v", diags)
	}
	if normalized.Interface.ValueString() != "wg_runner" {
		t.Fatalf("unexpected canonical interface: %s", normalized.Interface.ValueString())
	}
	var normalizedAllowed []string
	diags.Append(normalized.AllowedIPs.ElementsAs(context.Background(), &normalizedAllowed, false)...)
	if diags.HasError() {
		t.Fatalf("allowed_ips extraction failed: %v", diags)
	}
	if len(normalizedAllowed) != 1 {
		t.Fatalf("unexpected normalized allowed_ips: %#v", normalizedAllowed)
	}
}

func TestCreateUpdateReadDeleteLifecycleHelpers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	client := &fakeWireGuardPeerClient{rpcURL: server.URL, values: map[string]modernubus.UCIGetResponse{}}
	desired := normalizedPeer(t, "actions", "wg_runner", "base64-test-public-key=", []string{"10.1.0.0/24"}, "host.example.net")
	if err := createPeer(context.Background(), client, desired, 10); err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if len(client.addReqs) != 1 || client.addReqs[0].Type != "wireguard_wg_runner" {
		t.Fatalf("unexpected add request: %#v", client.addReqs)
	}
	updated := normalizedPeer(t, "actions", "wg_runner", "base64-test-public-key=", []string{"10.1.0.0/24"}, "")
	if err := updatePeer(context.Background(), client, updated, 10); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if len(client.deleteReqs) < 1 {
		t.Fatalf("expected option deletes for cleared values: %#v", client.deleteReqs)
	}
	live, exists, err := readLivePeer(context.Background(), client, "actions", "wg_runner")
	if err != nil || !exists {
		t.Fatalf("expected live peer: exists=%v err=%v", exists, err)
	}
	if !live.EndpointHost.IsNull() {
		t.Fatalf("expected cleared optional values in read model: %#v", live)
	}
	if err := deletePeer(context.Background(), client, "actions", "wg_runner", 10); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
}

func TestCanonicalNameValidation(t *testing.T) {
	if _, err := canonicalName(" "); err == nil {
		t.Fatal("expected empty-name validation error")
	}
	if _, err := canonicalInterface("wg-runner"); err == nil {
		t.Fatal("expected unsupported interface character validation error")
	}
}

func normalizedPeer(t *testing.T, name, iface, publicKey string, allowed []string, endpointHost string) ResourceModel {
	t.Helper()
	allowedValue, diagnostics := types.ListValueFrom(context.Background(), types.StringType, allowed)
	if diagnostics.HasError() {
		t.Fatalf("allowed_ips build failed: %v", diagnostics)
	}
	plan := ResourceModel{
		Name:                types.StringValue(name),
		Interface:           types.StringValue(iface),
		PublicKey:           types.StringValue(publicKey),
		AllowedIPs:          allowedValue,
		PersistentKeepalive: types.Int64Null(),
		EndpointPort:        types.Int64Null(),
		PresharedKey:        types.StringNull(),
		Description:         types.StringNull(),
	}
	if endpointHost != "" {
		plan.EndpointHost = types.StringValue(endpointHost)
	} else {
		plan.EndpointHost = types.StringNull()
	}
	var diags diag.Diagnostics
	normalized, ok := normalizePlan(context.Background(), plan, &diags)
	if !ok {
		t.Fatalf("normalize failed: %v", diags)
	}
	return normalized
}
