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
