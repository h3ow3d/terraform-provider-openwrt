package ucinorm

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/h3ow3d/terraform-provider-openwrt/internal/testutil/ubusmock"
)

func TestParseDHCPPackageGetResponse_ObservedShapeParses(t *testing.T) {
	raw := readFixture(t, "flint2_dhcp_get_sanitized.json")
	snapshot, err := ParseDHCPPackageGetResponse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if len(snapshot.Sections) != 3 {
		t.Fatalf("expected 3 sections, got %d", len(snapshot.Sections))
	}
	foundManaged := false
	for _, section := range snapshot.Sections {
		if section.Key == "tfdom_tf_provider_probe_invali_6f49c8925438a00d" {
			foundManaged = true
			if section.Options["name"] != "tf-provider-probe.invalid" || section.Options["ip"] != "192.0.2.1" {
				t.Fatalf("unexpected managed values: %#v", section.Options)
			}
		}
	}
	if !foundManaged {
		t.Fatal("managed section not found in parsed snapshot")
	}
}

func TestParseDHCPPackageGetResponse_NormalizationDeterministic(t *testing.T) {
	raw := readFixture(t, "flint2_dhcp_get_sanitized.json")
	a, err := ParseDHCPPackageGetResponse(raw)
	if err != nil {
		t.Fatalf("parse A failed: %v", err)
	}

	var asMap map[string]any
	if err := json.Unmarshal(raw, &asMap); err != nil {
		t.Fatalf("fixture decode failed: %v", err)
	}
	// Rebuild with permuted object key order.
	reordered := map[string]any{
		"result":  asMap["result"],
		"jsonrpc": asMap["jsonrpc"],
		"id":      asMap["id"],
	}
	reorderedRaw, _ := json.Marshal(reordered)
	b, err := ParseDHCPPackageGetResponse(reorderedRaw)
	if err != nil {
		t.Fatalf("parse B failed: %v", err)
	}

	aj, _ := a.CanonicalJSON()
	bj, _ := b.CanonicalJSON()
	if !bytes.Equal(aj, bj) {
		t.Fatalf("normalization not deterministic")
	}
}

func TestParseDHCPPackageGetResponse_RetainsSectionOrderingMetadata(t *testing.T) {
	raw := readFixture(t, "flint2_dhcp_get_sanitized.json")
	snapshot, err := ParseDHCPPackageGetResponse(raw)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if snapshot.Sections[0].Index != 0 || snapshot.Sections[1].Index != 1 || snapshot.Sections[2].Index != 9 {
		t.Fatalf("unexpected index ordering: %#v", snapshot.Sections)
	}
}

func TestParseDHCPPackageGetResponse_RejectsMalformedVariants(t *testing.T) {
	cases := []string{
		`{"jsonrpc":"2.0","id":1}`,
		`{"jsonrpc":"2.0","id":1,"result":[2,{"values":{}}]}`,
		`{"jsonrpc":"2.0","id":1,"result":[0,{"values":[]}]}`,
		`{"jsonrpc":"2.0","id":1,"result":[0,{"values":{"x":{".name":"x",".type":"domain",".anonymous":false}}}]}`,
		`{"jsonrpc":"2.0","id":1,"result":[0,{"values":{"x":{".name":"x",".type":"domain",".anonymous":false,".index":0,"bad":{"nested":"no"}}}}]}`,
		`{"jsonrpc":"2.0","id":1,"result":[0,{"values":{"x":{".name":"y",".type":"domain",".anonymous":false,".index":0}}}]}`,
	}
	for _, raw := range cases {
		if _, err := ParseDHCPPackageGetResponse([]byte(raw)); err == nil {
			t.Fatalf("expected parse failure for: %s", raw)
		}
	}
}

func TestParseDHCPPackageGetResponse_DiagnosticsDoNotLeakSensitiveValues(t *testing.T) {
	raw := `{"jsonrpc":"2.0","id":1,"result":[0,{"values":{"x":{".name":"x",".type":"domain",".anonymous":false,".index":0,"password":"super-secret-password","bad":{"token":"super-secret-token"}}}}]}`
	_, err := ParseDHCPPackageGetResponse([]byte(raw))
	if err == nil {
		t.Fatal("expected parse failure")
	}
	msg := err.Error()
	if strings.Contains(msg, "super-secret-password") || strings.Contains(msg, "super-secret-token") {
		t.Fatalf("sensitive value leaked in error: %q", msg)
	}
}

func TestParseDHCPPackageGetResponse_MockSentinelDetectable(t *testing.T) {
	mock := ubusmock.NewServer()
	defer mock.Close()
	if !ubusmock.IsLoopbackURL(mock.URL()) {
		t.Fatalf("mock not loopback")
	}

	values := map[string]any{}
	for key, section := range mock.PackageSnapshot("dhcp") {
		values[key] = section
	}
	body, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"result":  []any{0, map[string]any{"values": values}},
	})
	snapshot, err := ParseDHCPPackageGetResponse(body)
	if err != nil {
		t.Fatalf("parse mock response failed: %v", err)
	}
	found := false
	for _, section := range snapshot.Sections {
		if section.Key == "sentinel_static" {
			found = true
			if section.Options["name"] != "sentinel.invalid" || section.Options["ip"] != "203.0.113.99" {
				t.Fatalf("unexpected sentinel values: %#v", section.Options)
			}
		}
	}
	if !found {
		t.Fatal("sentinel section not found")
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	path := filepath.Join("testdata", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	return raw
}
