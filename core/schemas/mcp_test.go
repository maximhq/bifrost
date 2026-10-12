//go:build !tinygo && !wasm

package schemas

import "testing"

func TestMCPAuthURLHasTempTokenFragment(t *testing.T) {
	tests := []struct {
		name    string
		authURL string
		want    bool
	}{
		{
			name:    "url with temp-token fragment",
			authURL: "https://host/workspace/mcp-sessions/auth?flow=abc123#t=xyz",
			want:    true,
		},
		{
			name:    "url with no fragment",
			authURL: "https://host/workspace/mcp-sessions/auth?flow=abc123",
			want:    false,
		},
		{
			name:    "url with a different fragment",
			authURL: "https://host/workspace/mcp-sessions/auth?flow=abc123#nope",
			want:    false,
		},
		{
			name:    "empty string",
			authURL: "",
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MCPAuthURLHasTempTokenFragment(tt.authURL); got != tt.want {
				t.Errorf("MCPAuthURLHasTempTokenFragment(%q) = %v, want %v", tt.authURL, got, tt.want)
			}
		})
	}
}

func TestMCPCodeModeLimitsWithDefaults(t *testing.T) {
	got := MCPCodeModeLimits{MaxSteps: 5, MaxToolCalls: 500}.WithDefaults()
	want := MCPCodeModeLimits{
		MaxSourceBytes:  DefaultCodeModeMaxSourceBytes,
		MaxSteps:        5,
		MaxMemoryBytes:  DefaultCodeModeMaxMemoryBytes,
		MaxLogBytes:     DefaultCodeModeMaxLogBytes,
		MaxToolCalls:    500,
		MaxValueBytes:   DefaultCodeModeMaxValueBytes,
		MaxNestingDepth: DefaultCodeModeMaxNestingDepth,
	}
	if got != want {
		t.Fatalf("WithDefaults() = %+v, want %+v", got, want)
	}
}

func TestMCPCodeModeLimitsValidate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		limits  MCPCodeModeLimits
		wantErr bool
	}{
		{name: "zero uses defaults", limits: MCPCodeModeLimits{}},
		{name: "large limits", limits: MCPCodeModeLimits{MaxSteps: 1 << 40, MaxMemoryBytes: 1 << 34, MaxToolCalls: 100000, MaxValueBytes: 1 << 30, MaxNestingDepth: MaxCodeModeNestingDepth}},
		{name: "negative steps", limits: MCPCodeModeLimits{MaxSteps: -1}, wantErr: true},
		{name: "negative source", limits: MCPCodeModeLimits{MaxSourceBytes: -1}, wantErr: true},
		{name: "negative memory", limits: MCPCodeModeLimits{MaxMemoryBytes: -1}, wantErr: true},
		{name: "negative logs", limits: MCPCodeModeLimits{MaxLogBytes: -1}, wantErr: true},
		{name: "negative tool calls", limits: MCPCodeModeLimits{MaxToolCalls: -1}, wantErr: true},
		{name: "negative value", limits: MCPCodeModeLimits{MaxValueBytes: -1}, wantErr: true},
		{name: "negative depth", limits: MCPCodeModeLimits{MaxNestingDepth: -1}, wantErr: true},
		{name: "value below minimum", limits: MCPCodeModeLimits{MaxValueBytes: MinCodeModeValueBytes - 1}, wantErr: true},
		{name: "depth above recursion bound", limits: MCPCodeModeLimits{MaxNestingDepth: MaxCodeModeNestingDepth + 1}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.limits.Validate(); (err != nil) != tc.wantErr {
				t.Fatalf("Validate() err=%v, wantErr=%v", err, tc.wantErr)
			}
		})
	}
}
