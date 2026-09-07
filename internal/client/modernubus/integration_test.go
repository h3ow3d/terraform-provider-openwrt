//go:build integration

package modernubus

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestIntegrationReadOnlySystemBoardAndUCIGet(t *testing.T) {
	if os.Getenv("OPENWRT_INTEGRATION") != "1" {
		t.Skip("set OPENWRT_INTEGRATION=1 to run live integration test")
	}

	remote := os.Getenv("OPENWRT_REMOTE")
	user := os.Getenv("OPENWRT_USER")
	password := os.Getenv("OPENWRT_PASSWORD")
	packageName := os.Getenv("OPENWRT_INTEGRATION_UCI_PACKAGE")
	if remote == "" || user == "" || password == "" || packageName == "" {
		t.Skip("integration config missing: OPENWRT_REMOTE, OPENWRT_USER, OPENWRT_PASSWORD, OPENWRT_INTEGRATION_UCI_PACKAGE")
	}

	client := NewClient(Config{
		Remote:   remote,
		User:     user,
		Password: password,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var board map[string]any
	if err := client.Call(ctx, "system", "board", map[string]any{}, &board); err != nil {
		t.Fatalf("system.board failed: %v", err)
	}
	if len(board) == 0 {
		t.Fatal("system.board returned empty object")
	}
	if _, ok := board["model"]; !ok {
		t.Fatal("system.board missing model field")
	}

	resp, err := client.UCIGet(ctx, UCIGetRequest{Config: packageName})
	if err != nil {
		t.Fatalf("uci.get(%s) failed: %v", packageName, err)
	}
	if !resp.PackageExists {
		t.Fatalf("expected package %q to be readable", packageName)
	}
}
