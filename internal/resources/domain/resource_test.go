package domain

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/h3ow3d/terraform-provider-openwrt/internal/client/modernubus"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type fakeDomainClient struct {
	rpcURL string

	getResponses map[string]modernubus.UCIGetResponse

	calls []string

	addReqs    []modernubus.UCIAddRequest
	setReqs    []modernubus.UCISetRequest
	deleteReqs []modernubus.UCIDeleteRequest
	applyReqs  []modernubus.UCIApplyRequest
	confirmCnt int

	failApply error
}

func (f *fakeDomainClient) CurrentRPCURL() (string, error) {
	return f.rpcURL, nil
}

func (f *fakeDomainClient) EnsureSessionLifetime(ctx context.Context, minLifetime time.Duration) error {
	return nil
}

func (f *fakeDomainClient) RunMutationTransaction(ctx context.Context, minLifetime time.Duration, fn func(context.Context) error) error {
	f.calls = append(f.calls, "tx.begin")
	err := fn(ctx)
	f.calls = append(f.calls, "tx.end")
	return err
}

func (f *fakeDomainClient) UCIGet(ctx context.Context, req modernubus.UCIGetRequest) (modernubus.UCIGetResponse, error) {
	f.calls = append(f.calls, "get")
	if resp, ok := f.getResponses[req.Section]; ok {
		return resp, nil
	}
	if req.Section == "" {
		return modernubus.UCIGetResponse{PackageExists: true, SectionExists: true, EmptyPackage: true, Values: map[string]modernubus.UCIValue{}}, nil
	}
	return modernubus.UCIGetResponse{PackageExists: true, SectionExists: false, Values: map[string]modernubus.UCIValue{}}, nil
}

func (f *fakeDomainClient) UCIAdd(ctx context.Context, req modernubus.UCIAddRequest) (modernubus.UCIAddResponse, error) {
	f.calls = append(f.calls, "add")
	f.addReqs = append(f.addReqs, req)
	if f.getResponses == nil {
		f.getResponses = map[string]modernubus.UCIGetResponse{}
	}
	values := map[string]modernubus.UCIValue{}
	for k, v := range req.Values {
		values[k] = modernubus.NewUCIValue(v)
	}
	f.getResponses[req.Name] = modernubus.UCIGetResponse{
		PackageExists: true,
		SectionExists: true,
		Values:        values,
	}
	return modernubus.UCIAddResponse{Section: req.Name}, nil
}

func (f *fakeDomainClient) UCISet(ctx context.Context, req modernubus.UCISetRequest) (modernubus.UCISetResponse, error) {
	f.calls = append(f.calls, "set")
	f.setReqs = append(f.setReqs, req)
	if f.getResponses == nil {
		f.getResponses = map[string]modernubus.UCIGetResponse{}
	}
	current := f.getResponses[req.Section]
	if current.Values == nil {
		current.Values = map[string]modernubus.UCIValue{}
	}
	for k, v := range req.Values {
		current.Values[k] = modernubus.NewUCIValue(v)
	}
	current.PackageExists = true
	current.SectionExists = true
	f.getResponses[req.Section] = current
	return modernubus.UCISetResponse{}, nil
}

func (f *fakeDomainClient) UCIDelete(ctx context.Context, req modernubus.UCIDeleteRequest) (modernubus.UCIDeleteResponse, error) {
	f.calls = append(f.calls, "delete")
	f.deleteReqs = append(f.deleteReqs, req)
	if f.getResponses != nil {
		delete(f.getResponses, req.Section)
	}
	return modernubus.UCIDeleteResponse{}, nil
}

func (f *fakeDomainClient) UCIApply(ctx context.Context, req modernubus.UCIApplyRequest) (modernubus.UCIApplyResponse, error) {
	f.calls = append(f.calls, "apply")
	f.applyReqs = append(f.applyReqs, req)
	if f.failApply != nil {
		return modernubus.UCIApplyResponse{}, f.failApply
	}
	return modernubus.UCIApplyResponse{}, nil
}

func (f *fakeDomainClient) UCIConfirm(ctx context.Context, req modernubus.UCIConfirmRequest) (modernubus.UCIConfirmResponse, error) {
	f.calls = append(f.calls, "confirm")
	f.confirmCnt++
	return modernubus.UCIConfirmResponse{}, nil
}

func TestDomainCreateMappingUsesAdd(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()

	client := &fakeDomainClient{
		rpcURL: server.URL,
		getResponses: map[string]modernubus.UCIGetResponse{
			sectionNameForDomain("tf-provider-probe.invalid"): {PackageExists: true, SectionExists: false, Values: map[string]modernubus.UCIValue{}},
		},
	}
	plan := ResourceModel{
		Name: types.StringValue("tf-provider-probe.invalid"),
		IP:   types.StringValue("192.0.2.1"),
	}
	if err := upsertDomain(context.Background(), client, plan, 10); err != nil {
		t.Fatalf("upsert failed: %v", err)
	}
	if len(client.addReqs) != 1 || len(client.setReqs) != 0 {
		t.Fatalf("expected add-only path")
	}
	req := client.addReqs[0]
	if req.Config != "dhcp" || req.Type != "domain" {
		t.Fatalf("unexpected add mapping")
	}
	if req.Name != sectionNameForDomain("tf-provider-probe.invalid") {
		t.Fatalf("unexpected section name")
	}
}

func TestDomainUpdateMappingUsesSet(t *testing.T) {
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
					"name": mustValue("tf-provider-probe.invalid"),
					"ip":   mustValue("192.0.2.10"),
				},
			},
		},
	}
	plan := ResourceModel{Name: types.StringValue("tf-provider-probe.invalid"), IP: types.StringValue("192.0.2.1")}
	if err := upsertDomain(context.Background(), client, plan, 10); err != nil {
		t.Fatalf("upsert failed: %v", err)
	}
	if len(client.setReqs) != 1 || len(client.addReqs) != 0 {
		t.Fatalf("expected set-only path")
	}
}

func TestDomainReadFromLiveUCIResponse(t *testing.T) {
	section := sectionNameForDomain("tf-provider-probe.invalid")
	client := &fakeDomainClient{
		getResponses: map[string]modernubus.UCIGetResponse{
			section: {
				PackageExists: true,
				SectionExists: true,
				Values: map[string]modernubus.UCIValue{
					"name": mustValue("tf-provider-probe.invalid"),
					"ip":   mustValue("192.0.2.1"),
				},
			},
		},
	}
	model, exists, err := readLiveDomain(context.Background(), client, "tf-provider-probe.invalid")
	if err != nil || !exists {
		t.Fatalf("expected existing live domain, err=%v exists=%v", err, exists)
	}
	if model.Name.ValueString() != "tf-provider-probe.invalid" || model.IP.ValueString() != "192.0.2.1" {
		t.Fatalf("unexpected model: %+v", model)
	}
}

func TestDomainDeleteMappingAndIdempotency(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()

	section := sectionNameForDomain("tf-provider-probe.invalid")
	clientExisting := &fakeDomainClient{
		rpcURL: server.URL,
		getResponses: map[string]modernubus.UCIGetResponse{
			section: {PackageExists: true, SectionExists: true, Values: map[string]modernubus.UCIValue{"name": mustValue("tf-provider-probe.invalid"), "ip": mustValue("192.0.2.1")}},
		},
	}
	if err := deleteDomain(context.Background(), clientExisting, "tf-provider-probe.invalid", 10); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	if len(clientExisting.deleteReqs) != 1 {
		t.Fatalf("expected one delete call")
	}

	clientMissing := &fakeDomainClient{
		rpcURL: server.URL,
		getResponses: map[string]modernubus.UCIGetResponse{
			section: {PackageExists: true, SectionExists: false, Values: map[string]modernubus.UCIValue{}},
		},
	}
	if err := deleteDomain(context.Background(), clientMissing, "tf-provider-probe.invalid", 10); err != nil {
		t.Fatalf("idempotent delete should succeed: %v", err)
	}
	if len(clientMissing.deleteReqs) != 0 {
		t.Fatalf("unexpected delete call for absent section")
	}
}

func TestUnrelatedUCISectionsPreserved(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()

	client := &fakeDomainClient{
		rpcURL: server.URL,
		getResponses: map[string]modernubus.UCIGetResponse{
			sectionNameForDomain("tf-provider-probe.invalid"): {PackageExists: true, SectionExists: false, Values: map[string]modernubus.UCIValue{}},
		},
	}
	plan := ResourceModel{Name: types.StringValue("tf-provider-probe.invalid"), IP: types.StringValue("192.0.2.1")}
	if err := upsertDomain(context.Background(), client, plan, 10); err != nil {
		t.Fatalf("upsert failed: %v", err)
	}
	if len(client.addReqs) != 1 {
		t.Fatalf("expected one add")
	}
	if client.addReqs[0].Config != "dhcp" {
		t.Fatalf("unexpected package write")
	}
}

func TestApplyFailureDoesNotCallConfirm(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	client := &fakeDomainClient{
		rpcURL: server.URL,
		getResponses: map[string]modernubus.UCIGetResponse{
			sectionNameForDomain("tf-provider-probe.invalid"): {PackageExists: true, SectionExists: false, Values: map[string]modernubus.UCIValue{}},
		},
		failApply: errors.New("apply failed"),
	}
	plan := ResourceModel{Name: types.StringValue("tf-provider-probe.invalid"), IP: types.StringValue("192.0.2.1")}
	err := upsertDomain(context.Background(), client, plan, 10)
	if err == nil {
		t.Fatal("expected apply failure")
	}
	if client.confirmCnt != 0 {
		t.Fatalf("confirm must not be called on apply failure")
	}
}

func TestFailedPostApplyHealthCheckDoesNotCallConfirm(t *testing.T) {
	client := &fakeDomainClient{
		rpcURL: "http://127.0.0.1:1",
		getResponses: map[string]modernubus.UCIGetResponse{
			sectionNameForDomain("tf-provider-probe.invalid"): {PackageExists: true, SectionExists: false, Values: map[string]modernubus.UCIValue{}},
		},
	}
	plan := ResourceModel{Name: types.StringValue("tf-provider-probe.invalid"), IP: types.StringValue("192.0.2.1")}
	err := upsertDomain(context.Background(), client, plan, 10)
	if err == nil {
		t.Fatal("expected health-check failure")
	}
	if client.confirmCnt != 0 {
		t.Fatalf("confirm must not be called on health-check failure")
	}
}

func TestSuccessfulApplyReadHealthCheckCallsConfirmOnce(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()

	section := sectionNameForDomain("tf-provider-probe.invalid")
	client := &fakeDomainClient{
		rpcURL: server.URL,
		getResponses: map[string]modernubus.UCIGetResponse{
			section: {PackageExists: true, SectionExists: false, Values: map[string]modernubus.UCIValue{}},
		},
	}
	plan := ResourceModel{Name: types.StringValue("tf-provider-probe.invalid"), IP: types.StringValue("192.0.2.1")}

	if err := upsertDomain(context.Background(), client, plan, 10); err != nil {
		t.Fatalf("upsert failed: %v", err)
	}
	if client.confirmCnt != 1 {
		t.Fatalf("expected confirm once, got %d", client.confirmCnt)
	}
}

func mustValue(v string) modernubus.UCIValue {
	return modernubus.NewUCIValue(v)
}
