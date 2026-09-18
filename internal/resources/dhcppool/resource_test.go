package dhcppool

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

type fakePoolClient struct {
	rpcURL string
	values map[string]modernubus.UCIGetResponse

	addReqs    []modernubus.UCIAddRequest
	setReqs    []modernubus.UCISetRequest
	deleteReqs []modernubus.UCIDeleteRequest
	applyReqs  []modernubus.UCIApplyRequest
	confirmCnt int
}

func (f *fakePoolClient) CurrentRPCURL() (string, error) { return f.rpcURL, nil }
func (f *fakePoolClient) RunMutationTransaction(ctx context.Context, _ time.Duration, fn func(context.Context) error) error {
	return fn(ctx)
}
func (f *fakePoolClient) UCIGet(_ context.Context, req modernubus.UCIGetRequest) (modernubus.UCIGetResponse, error) {
	if response, ok := f.values[req.Section]; ok {
		return response, nil
	}
	return modernubus.UCIGetResponse{PackageExists: true, Values: map[string]modernubus.UCIValue{}}, nil
}
func (f *fakePoolClient) UCIAdd(_ context.Context, req modernubus.UCIAddRequest) (modernubus.UCIAddResponse, error) {
	f.addReqs = append(f.addReqs, req)
	f.setResponse(req.Name, req.Values)
	return modernubus.UCIAddResponse{Section: req.Name}, nil
}
func (f *fakePoolClient) UCISet(_ context.Context, req modernubus.UCISetRequest) (modernubus.UCISetResponse, error) {
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
func (f *fakePoolClient) UCIDelete(_ context.Context, req modernubus.UCIDeleteRequest) (modernubus.UCIDeleteResponse, error) {
	f.deleteReqs = append(f.deleteReqs, req)
	delete(f.values, req.Section)
	return modernubus.UCIDeleteResponse{}, nil
}
func (f *fakePoolClient) UCIApply(_ context.Context, req modernubus.UCIApplyRequest) (modernubus.UCIApplyResponse, error) {
	f.applyReqs = append(f.applyReqs, req)
	return modernubus.UCIApplyResponse{}, nil
}
func (f *fakePoolClient) UCIConfirm(_ context.Context, _ modernubus.UCIConfirmRequest) (modernubus.UCIConfirmResponse, error) {
	f.confirmCnt++
	return modernubus.UCIConfirmResponse{}, nil
}
func (f *fakePoolClient) setResponse(section string, raw map[string]any) {
	if f.values == nil {
		f.values = map[string]modernubus.UCIGetResponse{}
	}
	values := map[string]modernubus.UCIValue{}
	for key, value := range raw {
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

func TestNormalizePlanDefaultsAndValidation(t *testing.T) {
	plan := ResourceModel{
		Name:      types.StringValue("Runner"),
		Interface: types.StringValue("br-lan.20"),
		Start:     types.Int64Value(100),
		Limit:     types.Int64Value(50),
	}
	applyDefaults(&plan)
	var diagnostics diag.Diagnostics
	normalized, ok := normalizePlan(plan, &diagnostics)
	if !ok || diagnostics.HasError() {
		t.Fatalf("expected valid plan: %v", diagnostics)
	}
	if normalized.Name.ValueString() != "runner" || normalized.LeaseTime.ValueString() != "12h" || !normalized.Force.ValueBool() {
		t.Fatalf("unexpected defaults: %#v", normalized)
	}
}

func TestSectionNameForPoolStableAndBounded(t *testing.T) {
	first := sectionNameForPool("Runner")
	second := sectionNameForPool("runner")
	if first != second || !strings.HasPrefix(first, sectionPrefix) {
		t.Fatalf("unstable section identity: %q %q", first, second)
	}
	if len(first) > len(sectionPrefix)+sectionReadableMax+1+sectionHashHexLen {
		t.Fatalf("section identity exceeds bound: %q", first)
	}
}

func TestCreateUpdateReadDeleteLifecycleHelpers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	client := &fakePoolClient{rpcURL: server.URL, values: map[string]modernubus.UCIGetResponse{}}
	desired := normalizedPool(t, "runner", "br-lan.20", 100, 50, "12h", true, "disabled", "disabled")
	if err := createPool(context.Background(), client, desired, 10); err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if len(client.addReqs) != 1 || client.addReqs[0].Type != poolSectionType {
		t.Fatalf("unexpected add request: %#v", client.addReqs)
	}
	updated := normalizedPool(t, "runner", "br-lan.20", 120, 50, "6h", false, "server", "server")
	if err := updatePool(context.Background(), client, updated, 10); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if len(client.setReqs) != 1 {
		t.Fatalf("expected one set request")
	}
	live, exists, err := readLivePool(context.Background(), client, "runner")
	if err != nil || !exists {
		t.Fatalf("expected live pool: exists=%v err=%v", exists, err)
	}
	if live.Start.ValueInt64() != 120 || live.LeaseTime.ValueString() != "6h" || live.Force.ValueBool() {
		t.Fatalf("unexpected read-back model: %#v", live)
	}
	if err := deletePool(context.Background(), client, "runner", 10); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if len(client.deleteReqs) != 1 {
		t.Fatalf("expected one section delete")
	}
}

func normalizedPool(t *testing.T, name, iface string, start, limit int64, leasetime string, force bool, dhcpv6, ra string) ResourceModel {
	t.Helper()
	plan := ResourceModel{
		Name:      types.StringValue(name),
		Interface: types.StringValue(iface),
		Start:     types.Int64Value(start),
		Limit:     types.Int64Value(limit),
		LeaseTime: types.StringValue(leasetime),
		Force:     types.BoolValue(force),
		DHCPv6:    types.StringValue(dhcpv6),
		RA:        types.StringValue(ra),
	}
	var diagnostics diag.Diagnostics
	normalized, ok := normalizePlan(plan, &diagnostics)
	if !ok {
		t.Fatalf("normalize failed: %v", diagnostics)
	}
	return normalized
}
