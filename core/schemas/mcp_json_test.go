package schemas

import (
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
)

func TestMCPConfigUnmarshalToolSyncIntervalString(t *testing.T) {
	raw := []byte(`{"tool_sync_interval":"10m"}`)
	var cfg MCPConfig
	if err := sonic.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}
	if cfg.ToolSyncInterval != 10*time.Minute {
		t.Fatalf("expected 10m, got %v", cfg.ToolSyncInterval)
	}
}

func TestMCPClientConfigUnmarshalToolSyncIntervalString(t *testing.T) {
	raw := []byte(`{"name":"demo","connection_type":"stdio","tool_sync_interval":"30s"}`)
	var cfg MCPClientConfig
	if err := sonic.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}
	if cfg.ToolSyncInterval != 30*time.Second {
		t.Fatalf("expected 30s, got %v", cfg.ToolSyncInterval)
	}
}

func TestMCPConfigUnmarshalToolSyncIntervalInvalidString(t *testing.T) {
	raw := []byte(`{"tool_sync_interval":"not-a-duration"}`)
	var cfg MCPConfig
	if err := sonic.Unmarshal(raw, &cfg); err == nil {
		t.Fatal("expected unmarshal error for invalid duration, got nil")
	}
}

func TestMCPConfigUnmarshalToolSyncIntervalIntegerNumber(t *testing.T) {
	raw := []byte(`{"tool_sync_interval":60000000000}`)
	var cfg MCPConfig
	if err := sonic.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}
	if cfg.ToolSyncInterval != time.Minute {
		t.Fatalf("expected 1m, got %v", cfg.ToolSyncInterval)
	}
}

func TestMCPConfigUnmarshalToolSyncIntervalRejectsFractionalNumber(t *testing.T) {
	raw := []byte(`{"tool_sync_interval":1.5}`)
	var cfg MCPConfig
	err := sonic.Unmarshal(raw, &cfg)
	if err == nil {
		t.Fatal("expected error for fractional numeric duration, got nil")
	}
	if !strings.Contains(err.Error(), "fractional numeric values are not allowed") {
		t.Fatalf("expected fractional-value error, got: %v", err)
	}
}

