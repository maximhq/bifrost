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

func TestMCPClientConfigUnmarshalToolExecutionTimeoutString(t *testing.T) {
	raw := []byte(`{"name":"demo","connection_type":"http","tool_execution_timeout":"45s"}`)
	var cfg MCPClientConfig
	if err := sonic.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}
	if cfg.ToolExecutionTimeout != 45*time.Second {
		t.Fatalf("expected 45s, got %v", cfg.ToolExecutionTimeout)
	}
}

func TestMCPClientConfigUnmarshalToolExecutionTimeoutInteger(t *testing.T) {
	raw := []byte(`{"name":"demo","connection_type":"http","tool_execution_timeout":60}`)
	var cfg MCPClientConfig
	if err := sonic.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}
	if cfg.ToolExecutionTimeout != 60*time.Second {
		t.Fatalf("expected 60s, got %v", cfg.ToolExecutionTimeout)
	}
}

func TestMCPClientConfigUnmarshalToolExecutionTimeoutNotSet(t *testing.T) {
	raw := []byte(`{"name":"demo","connection_type":"http"}`)
	var cfg MCPClientConfig
	if err := sonic.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}
	if cfg.ToolExecutionTimeout != 0 {
		t.Fatalf("expected 0 (use global), got %v", cfg.ToolExecutionTimeout)
	}
}

func TestMCPClientConfigUnmarshalToolExecutionTimeoutExplicitZero(t *testing.T) {
	raw := []byte(`{"name":"demo","connection_type":"http","tool_execution_timeout":0}`)
	var cfg MCPClientConfig
	if err := sonic.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}
	if cfg.ToolExecutionTimeout != 0 {
		t.Fatalf("expected 0 (use global), got %v", cfg.ToolExecutionTimeout)
	}
}

func TestMCPClientConfigUnmarshalToolExecutionTimeoutInvalidString(t *testing.T) {
	raw := []byte(`{"name":"demo","connection_type":"http","tool_execution_timeout":"not-a-duration"}`)
	var cfg MCPClientConfig
	if err := sonic.Unmarshal(raw, &cfg); err == nil {
		t.Fatal("expected unmarshal error for invalid duration, got nil")
	}
}

func TestMCPClientConfigUnmarshalToolExecutionTimeoutNegativeInteger(t *testing.T) {
	raw := []byte(`{"name":"demo","connection_type":"http","tool_execution_timeout":-30}`)
	var cfg MCPClientConfig
	if err := sonic.Unmarshal(raw, &cfg); err == nil {
		t.Fatal("expected unmarshal error for negative timeout, got nil")
	}
}

func TestMCPClientConfigUnmarshalToolExecutionTimeoutNegativeString(t *testing.T) {
	raw := []byte(`{"name":"demo","connection_type":"http","tool_execution_timeout":"-30s"}`)
	var cfg MCPClientConfig
	if err := sonic.Unmarshal(raw, &cfg); err == nil {
		t.Fatal("expected unmarshal error for negative duration string, got nil")
	}
}

func TestMCPClientConfigMarshalEmitsDurationStrings(t *testing.T) {
	cfg := MCPClientConfig{
		Name:                 "demo",
		ConnectionType:       "http",
		ToolSyncInterval:     10 * time.Minute,
		ToolExecutionTimeout: 45 * time.Second,
	}
	data, err := sonic.Marshal(cfg)
	if err != nil {
		t.Fatalf("unexpected marshal error: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, `"tool_sync_interval":"10m0s"`) {
		t.Fatalf("expected tool_sync_interval as duration string, got: %s", s)
	}
	if !strings.Contains(s, `"tool_execution_timeout":"45s"`) {
		t.Fatalf("expected tool_execution_timeout as duration string, got: %s", s)
	}
}

func TestMCPClientConfigMarshalOmitsZeroDurations(t *testing.T) {
	cfg := MCPClientConfig{Name: "demo", ConnectionType: "http"}
	data, err := sonic.Marshal(cfg)
	if err != nil {
		t.Fatalf("unexpected marshal error: %v", err)
	}
	s := string(data)
	if strings.Contains(s, "tool_sync_interval") {
		t.Fatalf("expected tool_sync_interval omitted when zero, got: %s", s)
	}
	if strings.Contains(s, "tool_execution_timeout") {
		t.Fatalf("expected tool_execution_timeout omitted when zero, got: %s", s)
	}
}

func TestMCPClientConfigDurationFieldsRoundTrip(t *testing.T) {
	for _, syncInterval := range []time.Duration{10 * time.Minute, 30 * time.Second, -time.Minute} {
		cfg := MCPClientConfig{
			Name:                 "demo",
			ConnectionType:       "http",
			ToolSyncInterval:     syncInterval,
			ToolExecutionTimeout: 90 * time.Second,
		}
		data, err := sonic.Marshal(cfg)
		if err != nil {
			t.Fatalf("unexpected marshal error: %v", err)
		}
		var decoded MCPClientConfig
		if err := sonic.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("unexpected unmarshal error: %v", err)
		}
		if decoded.ToolSyncInterval != syncInterval {
			t.Fatalf("tool_sync_interval did not round-trip: sent %v, got %v", syncInterval, decoded.ToolSyncInterval)
		}
		if decoded.ToolExecutionTimeout != 90*time.Second {
			t.Fatalf("tool_execution_timeout did not round-trip: got %v", decoded.ToolExecutionTimeout)
		}
	}
}

func TestMCPConfigMarshalToolSyncIntervalRoundTrip(t *testing.T) {
	cfg := MCPConfig{ToolSyncInterval: 10 * time.Minute}
	data, err := sonic.Marshal(cfg)
	if err != nil {
		t.Fatalf("unexpected marshal error: %v", err)
	}
	if !strings.Contains(string(data), `"tool_sync_interval":"10m0s"`) {
		t.Fatalf("expected tool_sync_interval as duration string, got: %s", string(data))
	}
	var decoded MCPConfig
	if err := sonic.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unexpected unmarshal error: %v", err)
	}
	if decoded.ToolSyncInterval != 10*time.Minute {
		t.Fatalf("tool_sync_interval did not round-trip: got %v", decoded.ToolSyncInterval)
	}
}