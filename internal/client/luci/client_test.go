package luci

import (
	"strings"
	"testing"
)

func TestUpsertManagedBlock(t *testing.T) {
	original := "config interface 'lan'\n\toption proto 'static'\n"
	got := upsertManagedBlock(original, markerKey("network", "runner"), "config interface 'runner'\n\toption proto 'static'")
	if !strings.Contains(got, "BEGIN terraform-provider-openwrt network:runner") {
		t.Fatalf("expected managed marker block, got: %s", got)
	}

	updated := upsertManagedBlock(got, markerKey("network", "runner"), "config interface 'runner'\n\toption proto 'dhcp'")
	if !strings.Contains(updated, "option proto 'dhcp'") {
		t.Fatalf("expected updated managed block")
	}
}

func TestRemoveManagedBlock(t *testing.T) {
	content := "config interface 'lan'\n\toption proto 'static'\n\n# BEGIN terraform-provider-openwrt network:runner\nconfig interface 'runner'\n\toption proto 'static'\n# END terraform-provider-openwrt network:runner\n"
	got := removeManagedBlock(content, markerKey("network", "runner"))
	if strings.Contains(got, "network:runner") {
		t.Fatalf("expected marker block to be removed")
	}
	if !strings.Contains(got, "config interface 'lan'") {
		t.Fatalf("expected remaining config content")
	}
}
