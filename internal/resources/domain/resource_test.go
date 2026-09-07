package domain

import (
	"context"
	"errors"
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

type fakeDomainClient struct {
	rpcURL string

	getResponses map[string]modernubus.UCIGetResponse

	addReqs    []modernubus.UCIAddRequest
	setReqs    []modernubus.UCISetRequest
	deleteReqs []modernubus.UCIDeleteRequest
	applyReqs  []modernubus.UCIApplyRequest
	confirmCnt int

	runTxMinLifetime []time.Duration
	failApply        error
}

func (f *fakeDomainClient) CurrentRPCURL() (string, error) { return f.rpcURL, nil }
func (f *fakeDomainClient) EnsureSessionLifetime(ctx context.Context, minLifetime time.Duration) error {
	return nil
}

func (f *fakeDomainClient) RunMutationTransaction(ctx context.Context, minLifetime time.Duration, fn func(context.Context) error) error {
	f.runTxMinLifetime = append(f.runTxMinLifetime, minLifetime)
	return fn(ctx)
}

func (f *fakeDomainClient) UCIGet(ctx context.Context, req modernubus.UCIGetRequest) (modernubus.UCIGetResponse, error) {
	if resp, ok := f.getResponses[req.Section]; ok {
		return resp, nil
	}
	return modernubus.UCIGetResponse{PackageExists: true, SectionExists: false, Values: map[string]modernubus.UCIValue{}}, nil
}

func (f *fakeDomainClient) UCIAdd(ctx context.Context, req modernubus.UCIAddRequest) (modernubus.UCIAddResponse, error) {
	f.addReqs = append(f.addReqs, req)
	if f.getResponses == nil {
		f.getResponses = map[string]modernubus.UCIGetResponse{}
	}
	values := map[string]modernubus.UCIValue{}
	for k, v := range req.Values {
		values[k] = modernubus.NewUCIValue(v)
	}
	f.getResponses[req.Name] = modernubus.UCIGetResponse{PackageExists: true, SectionExists: true, Values: values}
	return modernubus.UCIAddResponse{Section: req.Name}, nil
}

func (f *fakeDomainClient) UCISet(ctx context.Context, req modernubus.UCISetRequest) (modernubus.UCISetResponse, error) {
	f.setReqs = append(f.setReqs, req)
	cur := f.getResponses[req.Section]
	if cur.Values == nil {
		cur.Values = map[string]modernubus.UCIValue{}
	}
	for k, v := range req.Values {
		cur.Values[k] = modernubus.NewUCIValue(v)
	}
	cur.PackageExists = true
	cur.SectionExists = true
	f.getResponses[req.Section] = cur
	return modernubus.UCISetResponse{}, nil
}

func (f *fakeDomainClient) UCIDelete(ctx context.Context, req modernubus.UCIDeleteRequest) (modernubus.UCIDeleteResponse, error) {
	f.deleteReqs = append(f.deleteReqs, req)
	delete(f.getResponses, req.Section)
	return modernubus.UCIDeleteResponse{}, nil
}

func (f *fakeDomainClient) UCIApply(ctx context.Context, req modernubus.UCIApplyRequest) (modernubus.UCIApplyResponse, error) {
	f.applyReqs = append(f.applyReqs, req)
	if f.failApply != nil {
		return modernubus.UCIApplyResponse{}, f.failApply
	}
	return modernubus.UCIApplyResponse{}, nil
}

func (f *fakeDomainClient) UCIConfirm(ctx context.Context, req modernubus.UCIConfirmRequest) (modernubus.UCIConfirmResponse, error) {
	f.confirmCnt++
	return modernubus.UCIConfirmResponse{}, nil
}

func TestSectionNameForDomain_DeterministicAndCanonical(t *testing.T) {
	a := sectionNameForDomain("Example.INVALID")
	b := sectionNameForDomain("example.invalid.")
	if a != b {
		t.Fatalf("expected canonical equivalents to share identity: %q vs %q", a, b)
	}
	if !strings.HasPrefix(a, sectionPrefix) {
		t.Fatalf("expected %q prefix in %q", sectionPrefix, a)
	}
}

func TestSectionNameForDomain_MaxLengthBounded(t *testing.T) {
	long := strings.Repeat("a", 120) + "." + strings.Repeat("b", 120) + ".invalid"
	section := sectionNameForDomain(long)
	if len(section) > len(sectionPrefix)+sectionReadableMax+1+sectionHashHexLen {
		t.Fatalf("section name exceeds bounded length: %d", len(section))
	}
}

func TestSectionNameForDomain_CollisionResistance(t *testing.T) {
	// Same readable normalization ("a_b_example_invalid"), different canonical input.
	one := sectionNameForDomain("a-b.example.invalid")
	two := sectionNameForDomain("a.b.example.invalid")
	if one == two {
		t.Fatalf("expected unique names for distinct canonical domains")
	}
}

func TestSchema_NameRequiresReplacement(t *testing.T) {
	var resp resource.SchemaResponse
	(&Resource{}).Schema(context.Background(), resource.SchemaRequest{}, &resp)
	nameAttr := resp.Schema.Attributes["name"]
	typed, ok := nameAttr.(schema.StringAttribute)
	if !ok {
		t.Fatalf("name attribute type mismatch")
	}
	if len(typed.PlanModifiers) == 0 {
		t.Fatalf("expected name to include RequiresReplace plan modifier")
	}
}

func TestCreateDomain_CollisionReturnsImportDiagnostic(t *testing.T) {
	section := sectionNameForDomain("tf-provider-probe.invalid")
	client := &fakeDomainClient{
		getResponses: map[string]modernubus.UCIGetResponse{
			section: {PackageExists: true, SectionExists: true},
		},
	}
	err := createDomain(context.Background(), client, "tf-provider-probe.invalid", "192.0.2.1", 10)
	if err == nil {
		t.Fatal("expected collision error")
	}
	if !strings.Contains(err.Error(), "import this resource") {
		t.Fatalf("expected import guidance, got: %v", err)
	}
	if len(client.addReqs) != 0 {
		t.Fatalf("must not overwrite existing section")
	}
}

func TestCreateDomain_UsesDeterministicNamedSection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()

	section := sectionNameForDomain("tf-provider-probe.invalid")
	client := &fakeDomainClient{
		rpcURL: server.URL,
		getResponses: map[string]modernubus.UCIGetResponse{
			section: {PackageExists: true, SectionExists: false},
		},
	}
	if err := createDomain(context.Background(), client, "tf-provider-probe.invalid", "192.0.2.1", 10); err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if len(client.addReqs) != 1 {
		t.Fatalf("expected exactly one add")
	}
	if client.addReqs[0].Name != section {
		t.Fatalf("unexpected section name: %q", client.addReqs[0].Name)
	}
}

func TestUpdateDomainIP_UsesSetOnSameSection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()

	section := sectionNameForDomain("tf-provider-probe.invalid")
	client := &fakeDomainClient{
		rpcURL: server.URL,
		getResponses: map[string]modernubus.UCIGetResponse{
			section: {
				PackageExists: true,
				SectionExists: true,
				Values: map[string]modernubus.UCIValue{
					"name": modernubus.NewUCIValue("tf-provider-probe.invalid"),
					"ip":   modernubus.NewUCIValue("192.0.2.1"),
				},
			},
		},
	}
	if err := updateDomainIP(context.Background(), client, "tf-provider-probe.invalid", "192.0.2.2", 10); err != nil {
		t.Fatalf("update failed: %v", err)
	}
	if len(client.setReqs) != 1 {
		t.Fatalf("expected one set request")
	}
	if client.setReqs[0].Section != section {
		t.Fatalf("unexpected section update target")
	}
}

func TestReadLiveDomain_MissingSectionReturnsAbsent(t *testing.T) {
	section := sectionNameForDomain("tf-provider-probe.invalid")
	client := &fakeDomainClient{
		getResponses: map[string]modernubus.UCIGetResponse{
			section: {PackageExists: true, SectionExists: false},
		},
	}
	_, exists, err := readLiveDomain(context.Background(), client, "tf-provider-probe.invalid")
	if err != nil || exists {
		t.Fatalf("expected absent section, exists=%v err=%v", exists, err)
	}
}

func TestReadLiveDomain_ReturnsRemoteDrift(t *testing.T) {
	section := sectionNameForDomain("tf-provider-probe.invalid")
	client := &fakeDomainClient{
		getResponses: map[string]modernubus.UCIGetResponse{
			section: {
				PackageExists: true,
				SectionExists: true,
				Values: map[string]modernubus.UCIValue{
					"name": modernubus.NewUCIValue("tf-provider-probe.invalid"),
					"ip":   modernubus.NewUCIValue("192.0.2.3"),
				},
			},
		},
	}
	model, exists, err := readLiveDomain(context.Background(), client, "tf-provider-probe.invalid")
	if err != nil || !exists {
		t.Fatalf("expected existing section, exists=%v err=%v", exists, err)
	}
	if model.IP.ValueString() != "192.0.2.3" {
		t.Fatalf("expected drifted remote IP in state read-back")
	}
}

func TestDeleteDomain_IdempotentAndTargeted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()

	section := sectionNameForDomain("tf-provider-probe.invalid")
	clientExisting := &fakeDomainClient{
		rpcURL: server.URL,
		getResponses: map[string]modernubus.UCIGetResponse{
			section: {
				PackageExists: true,
				SectionExists: true,
				Values: map[string]modernubus.UCIValue{
					"name": modernubus.NewUCIValue("tf-provider-probe.invalid"),
					"ip":   modernubus.NewUCIValue("192.0.2.2"),
				},
			},
			"unrelated": {
				PackageExists: true,
				SectionExists: true,
				Values: map[string]modernubus.UCIValue{
					"name": modernubus.NewUCIValue("keep"),
					"ip":   modernubus.NewUCIValue("192.0.2.9"),
				},
			},
		},
	}
	if err := deleteDomain(context.Background(), clientExisting, "tf-provider-probe.invalid", 10); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if len(clientExisting.deleteReqs) != 1 || clientExisting.deleteReqs[0].Section != section {
		t.Fatalf("expected one delete against managed section only")
	}
	if _, ok := clientExisting.getResponses["unrelated"]; !ok {
		t.Fatalf("unrelated section should remain untouched")
	}

	clientMissing := &fakeDomainClient{
		rpcURL: server.URL,
		getResponses: map[string]modernubus.UCIGetResponse{
			section: {PackageExists: true, SectionExists: false},
		},
	}
	if err := deleteDomain(context.Background(), clientMissing, "tf-provider-probe.invalid", 10); err != nil {
		t.Fatalf("idempotent delete failed: %v", err)
	}
	if len(clientMissing.deleteReqs) != 0 {
		t.Fatalf("must not send delete for absent section")
	}
}

func TestApplyFailureDoesNotConfirm(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()

	section := sectionNameForDomain("tf-provider-probe.invalid")
	client := &fakeDomainClient{
		rpcURL: server.URL,
		getResponses: map[string]modernubus.UCIGetResponse{
			section: {PackageExists: true, SectionExists: false},
		},
		failApply: errors.New("apply failed"),
	}
	err := createDomain(context.Background(), client, "tf-provider-probe.invalid", "192.0.2.1", 10)
	if err == nil {
		t.Fatal("expected apply failure")
	}
	if client.confirmCnt != 0 {
		t.Fatalf("confirm must not be called on apply failure")
	}
}

func TestFailedHealthCheckDoesNotConfirm(t *testing.T) {
	section := sectionNameForDomain("tf-provider-probe.invalid")
	client := &fakeDomainClient{
		rpcURL: "http://127.0.0.1:1",
		getResponses: map[string]modernubus.UCIGetResponse{
			section: {PackageExists: true, SectionExists: false},
		},
	}
	err := createDomain(context.Background(), client, "tf-provider-probe.invalid", "192.0.2.1", 10)
	if err == nil {
		t.Fatal("expected health-check failure")
	}
	if client.confirmCnt != 0 {
		t.Fatalf("confirm must not be called on health-check failure")
	}
}

func TestSuccessfulCreateApplyReadHealthConfirm(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()

	section := sectionNameForDomain("tf-provider-probe.invalid")
	client := &fakeDomainClient{
		rpcURL: server.URL,
		getResponses: map[string]modernubus.UCIGetResponse{
			section: {PackageExists: true, SectionExists: false},
		},
	}
	if err := createDomain(context.Background(), client, "tf-provider-probe.invalid", "192.0.2.1", 10); err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if len(client.applyReqs) != 1 || client.confirmCnt != 1 {
		t.Fatalf("expected one apply and one confirm")
	}
}

func TestImportIdentifierParsing(t *testing.T) {
	ok, err := parseImportIdentifier("Example.INVALID.")
	if err != nil {
		t.Fatalf("unexpected parse failure: %v", err)
	}
	if ok != "example.invalid" {
		t.Fatalf("unexpected canonical import id: %q", ok)
	}

	bad := []string{"", "bad domain", "bad/domain", "bad..domain"}
	for _, candidate := range bad {
		if _, err := parseImportIdentifier(candidate); err == nil {
			t.Fatalf("expected malformed import id to fail: %q", candidate)
		}
	}
}

func TestValidatePlanCanonicalizesAndValidates(t *testing.T) {
	plan := ResourceModel{
		Name: types.StringValue("Example.INVALID."),
		IP:   types.StringValue("192.0.2.1"),
	}
	var diags diag.Diagnostics
	canonical, ok := validatePlan(plan, &diags)
	if !ok || diags.HasError() {
		t.Fatalf("expected valid plan: %v", diags)
	}
	if canonical != "example.invalid" {
		t.Fatalf("unexpected canonical name: %q", canonical)
	}
}
