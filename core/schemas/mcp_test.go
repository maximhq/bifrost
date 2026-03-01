package schemas

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestMCPToolManagerConfig_UnmarshalJSON(t *testing.T) {
	tests := []struct {
		name           string
		input          string
		expectDuration time.Duration
		expectDepth    int
		wantErr        bool
		errContains    string
	}{
		{
			name:           "string duration",
			input:          `{"tool_execution_timeout":"30s","max_agent_depth":10}`,
			expectDuration: 30 * time.Second,
			expectDepth:    10,
		},
		{
			name:           "numeric value treated as nanoseconds",
			input:          `{"tool_execution_timeout":30,"max_agent_depth":10}`,
			expectDuration: 30 * time.Nanosecond,
			expectDepth:    10,
		},
		{
			name:           "numeric workaround nanoseconds equals 30s",
			input:          `{"tool_execution_timeout":30000000000,"max_agent_depth":10}`,
			expectDuration: 30 * time.Second,
			expectDepth:    10,
		},
		{
			name:        "invalid duration string",
			input:       `{"tool_execution_timeout":"30seconds","max_agent_depth":10}`,
			wantErr:     true,
			errContains: "invalid tool_execution_timeout",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg MCPToolManagerConfig
			err := json.Unmarshal([]byte(tt.input), &cfg)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Fatalf("expected error containing %q, got %q", tt.errContains, err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.ToolExecutionTimeout != tt.expectDuration {
				t.Errorf("expected timeout %v, got %v", tt.expectDuration, cfg.ToolExecutionTimeout)
			}
			if cfg.MaxAgentDepth != tt.expectDepth {
				t.Errorf("expected max agent depth %d, got %d", tt.expectDepth, cfg.MaxAgentDepth)
			}
		})
	}
}
