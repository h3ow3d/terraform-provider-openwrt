package firewallrule

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

type fakeFirewallRuleClient struct {
	rpcURL string
	values map[string]modernubus.UCIGetResponse

	addReqs    []modernubus.UCIAddRequest
	setReqs    []modernubus.UCISetRequest
	deleteReqs []modernubus.UCIDeleteRequest
	applyReqs  []modernubus.UCIApplyRequest
	confirmCnt int
}

func (f *fakeFirewallRuleClient) CurrentRPCURL() (string, error) { return f.rpcURL, nil }
func (f *fakeFirewallRuleClient) RunMutationTransaction(ctx context.Context, _ time.Duration, fn func(context.Context) error) error {
	return fn(ctx)
}
func (f *fakeFirewallRuleClient) UCIGet(_ context.Context, req modernubus.UCIGetRequest) (modernubus.UCIGetResponse, error) {
	if response, ok := f.values[req.Section]; ok {
		return response, nil
	}
	return modernubus.UCIGetResponse{PackageExists: true, Values: map[string]modernubus.UCIValue{}}, nil
}
func (f *fakeFirewallRuleClient) UCIAdd(_ context.Context, req modernubus.UCIAddRequest) (modernubus.UCIAddResponse, error) {
	f.addReqs = append(f.addReqs, req)
	f.setResponse(req.Name, req.Values)
	return modernubus.UCIAddResponse{Section: req.Name}, nil
}
func (f *fakeFirewallRuleClient) UCISet(_ context.Context, req modernubus.UCISetRequest) (modernubus.UCISetResponse, error) {
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
func (f *fakeFirewallRuleClient) UCIDelete(_ context.Context, req modernubus.UCIDeleteRequest) (modernubus.UCIDeleteResponse, error) {
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
func (f *fakeFirewallRuleClient) UCIApply(_ context.Context, req modernubus.UCIApplyRequest) (modernubus.UCIApplyResponse, error) {
	f.applyReqs = append(f.applyReqs, req)
	return modernubus.UCIApplyResponse{}, nil
}
func (f *fakeFirewallRuleClient) UCIConfirm(_ context.Context, _ modernubus.UCIConfirmRequest) (modernubus.UCIConfirmResponse, error) {
	f.confirmCnt++
	return modernubus.UCIConfirmResponse{}, nil
}
func (f *fakeFirewallRuleClient) setResponse(section string, raw map[string]any) {
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

func TestNormalizePlanValidationAndDefaults(t *testing.T) {
	plan := ResourceModel{
		Name:     types.StringValue("allow-dns"),
		Src:      types.StringValue("runner"),
		Dest:     types.StringValue("wan"),
		Target:   types.StringValue("accept"),
		DestPort: types.StringValue("53"),
	}
	var diags diag.Diagnostics
	normalized, ok := normalizePlan(plan, &diags)
	if !ok || diags.HasError() {
		t.Fatalf("expected valid plan: %v", diags)
	}
	if normalized.Proto.ValueString() != "all" {
		t.Fatalf("unexpected default proto: %s", normalized.Proto.ValueString())
	}
	if !normalized.Enabled.ValueBool() {
		t.Fatal("expected enabled default true")
	}
}

func TestCreateUpdateReadDeleteLifecycleHelpers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	client := &fakeFirewallRuleClient{rpcURL: server.URL, values: map[string]modernubus.UCIGetResponse{}}
	desired := normalizedRule(t, "allow-dns", "runner", "wan", "ACCEPT", "udp", "ipv4", "53")
	if err := createRule(context.Background(), client, desired, 10); err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if len(client.addReqs) != 1 || client.addReqs[0].Type != ruleSectionType {
		t.Fatalf("unexpected add request: %#v", client.addReqs)
	}
	updated := normalizedRule(t, "allow-dns", "runner", "", "ACCEPT", "all", "", "")
	if err := updateRule(context.Background(), client, updated, 10); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if len(client.deleteReqs) < 3 {
		t.Fatalf("expected option deletes for cleared values: %#v", client.deleteReqs)
	}
	live, exists, err := readLiveRule(context.Background(), client, "allow-dns")
	if err != nil || !exists {
		t.Fatalf("expected live rule: exists=%v err=%v", exists, err)
	}
	if !live.Dest.IsNull() || !live.DestPort.IsNull() || !live.Family.IsNull() {
		t.Fatalf("expected cleared optional values in read model: %#v", live)
	}
	if err := deleteRule(context.Background(), client, "allow-dns", 10); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
}

func TestCanonicalNameValidation(t *testing.T) {
	if _, err := canonicalName(" "); err == nil {
		t.Fatal("expected empty-name validation error")
	}
	if _, err := canonicalName("allow dns"); err == nil {
		t.Fatal("expected unsupported character validation error")
	}
	name, err := canonicalName("ALLOW-DNS")
	if err != nil || name != "allow-dns" {
		t.Fatalf("unexpected canonical output: %q err=%v", name, err)
	}
}

func normalizedRule(t *testing.T, name, src, dest, target, proto, family, destPort string) ResourceModel {
	t.Helper()
	plan := ResourceModel{
		Name:    types.StringValue(name),
		Src:     types.StringValue(src),
		Target:  types.StringValue(target),
		Proto:   types.StringValue(proto),
		Enabled: types.BoolValue(true),
	}
	if dest != "" {
		plan.Dest = types.StringValue(dest)
	} else {
		plan.Dest = types.StringNull()
	}
	if family != "" {
		plan.Family = types.StringValue(family)
	} else {
		plan.Family = types.StringNull()
	}
	if destPort != "" {
		plan.DestPort = types.StringValue(destPort)
	} else {
		plan.DestPort = types.StringNull()
	}
	plan.SrcPort = types.StringNull()
	var diags diag.Diagnostics
	normalized, ok := normalizePlan(plan, &diags)
	if !ok {
		t.Fatalf("normalize failed: %v", diags)
	}
	return normalized
}
