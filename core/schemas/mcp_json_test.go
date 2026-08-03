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


// TestMCPClientConfigMarshalToolSyncIntervalEmitsNanoseconds pins the wire unit of
// tool_sync_interval. MarshalJSON overrides tool_execution_timeout into a duration
// string but lets tool_sync_interval fall through to time.Duration's default
// nanosecond encoding, so the two fields leave in different units. Callers that echo
// this value back into a PUT (which reads minutes) corrupt it — see issue #5026.
// Change this test deliberately, together with the UI readers, never incidentally.
func TestMCPClientConfigMarshalToolSyncIntervalEmitsNanoseconds(t *testing.T) {
	cfg := MCPClientConfig{
		Name:                 "demo",
		ConnectionType:       MCPConnectionTypeHTTP,
		ToolSyncInterval:     10 * time.Minute,
		ToolExecutionTimeout: 30 * time.Second,
	}
	raw, err := sonic.Marshal(cfg)
	if err != nil {
		t.Fatalf("unexpected marshal error: %v", err)
	}
	if !strings.Contains(string(raw), `"tool_sync_interval":600000000000`) {
		t.Fatalf("expected tool_sync_interval as nanoseconds, got: %s", raw)
	}
	if !strings.Contains(string(raw), `"tool_execution_timeout":"30s"`) {
		t.Fatalf("expected tool_execution_timeout as duration string, got: %s", raw)
	}
}

// TestMCPClientConfigToolSyncIntervalRoundTrips guards the marshal/unmarshal pair
// against drifting apart: UnmarshalJSON reads bare integers as nanoseconds, which is
// only correct while MarshalJSON keeps emitting them.
func TestMCPClientConfigToolSyncIntervalRoundTrips(t *testing.T) {
	for _, interval := range []time.Duration{0, time.Minute, 10 * time.Minute, time.Hour} {
		cfg := MCPClientConfig{Name: "demo", ConnectionType: MCPConnectionTypeHTTP, ToolSyncInterval: interval}
		raw, err := sonic.Marshal(cfg)
		if err != nil {
			t.Fatalf("unexpected marshal error for %v: %v", interval, err)
		}
		var got MCPClientConfig
		if err := sonic.Unmarshal(raw, &got); err != nil {
			t.Fatalf("unexpected unmarshal error for %v: %v", interval, err)
		}
		if got.ToolSyncInterval != interval {
			t.Fatalf("round-trip changed tool_sync_interval: want %v, got %v (wire: %s)", interval, got.ToolSyncInterval, raw)
		}
	}
}
