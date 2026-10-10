//go:build !tinygo && !wasm

package schemas

import (
	"encoding/json"
	"testing"
)

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

func TestMCPConnectionTypeOpenAPI_OTelNetworkTransport(t *testing.T) {
	if got := MCPConnectionTypeOpenAPI.OTelNetworkTransport(); got != "tcp" {
		t.Fatalf("openapi tools reach their upstream over HTTP; OTelNetworkTransport() = %q, want tcp", got)
	}
	if got := MCPConnectionTypeInProcess.OTelNetworkTransport(); got != "" {
		t.Fatalf("inprocess must stay attribute-less, got %q", got)
	}
}

func TestMCPOpenAPIConfig_HasSpecSource(t *testing.T) {
	str := func(s string) *string { return &s }
	for _, tc := range []struct {
		name string
		cfg  *MCPOpenAPIConfig
		want bool
	}{
		{name: "nil", cfg: nil},
		{name: "empty", cfg: &MCPOpenAPIConfig{}},
		{name: "whitespace only", cfg: &MCPOpenAPIConfig{Spec: "  \n", SpecURL: str(" "), SpecFile: str("")}},
		{name: "inline", cfg: &MCPOpenAPIConfig{Spec: "openapi: 3.0.0"}, want: true},
		{name: "url", cfg: &MCPOpenAPIConfig{SpecURL: str("https://example.com/openapi.json")}, want: true},
		{name: "file", cfg: &MCPOpenAPIConfig{SpecFile: str("specs/petstore.yaml")}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cfg.HasSpecSource(); got != tc.want {
				t.Fatalf("HasSpecSource() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestMCPClientConfig_OpenAPIConfigRoundTrips pins the wire shape of openapi_config
// through MCPClientConfig's custom (Un)MarshalJSON: nested credentials keep their
// SecretVar form and the server-computed metadata survives unchanged.
func TestMCPClientConfig_OpenAPIConfigRoundTrips(t *testing.T) {
	in := `{
		"name": "petstore",
		"connection_type": "openapi",
		"auth_type": "none",
		"openapi_config": {
			"spec": "openapi: 3.0.3\ninfo: {title: Petstore, version: 1.0.0}\npaths: {}",
			"spec_url": "https://petstore.example.com/openapi.yaml",
			"base_url": "https://petstore.example.com/v1",
			"security_credentials": {
				"ApiKeyAuth": {"value": "env.PETSTORE_KEY"},
				"BasicAuth": {"username": "svc", "password": "env.PETSTORE_PASSWORD"}
			},
			"include_deprecated": true,
			"max_response_bytes": 1024,
			"spec_size": 71,
			"spec_hash": "abc",
			"spec_title": "Petstore",
			"openapi_version": "3.0.3",
			"operation_count": 0
		}
	}`
	var cfg MCPClientConfig
	if err := json.Unmarshal([]byte(in), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.ConnectionType != MCPConnectionTypeOpenAPI {
		t.Fatalf("connection_type = %q", cfg.ConnectionType)
	}
	oc := cfg.OpenAPIConfig
	if oc == nil {
		t.Fatal("openapi_config was dropped")
	}
	if oc.SpecURL == nil || *oc.SpecURL != "https://petstore.example.com/openapi.yaml" || oc.BaseURL == nil || *oc.BaseURL != "https://petstore.example.com/v1" {
		t.Fatalf("spec_url/base_url not preserved: %+v", oc)
	}
	if !oc.IncludeDeprecated || oc.MaxResponseBytes != 1024 || oc.SpecSize != 71 || oc.SpecHash != "abc" || oc.SpecTitle != "Petstore" || oc.OpenAPIVersion != "3.0.3" {
		t.Fatalf("scalar fields not preserved: %+v", oc)
	}
	if got := oc.SecurityCredentials["ApiKeyAuth"].Value.GetValue(); got != "env.PETSTORE_KEY" && !oc.SecurityCredentials["ApiKeyAuth"].Value.IsFromSecret() {
		t.Fatalf("ApiKeyAuth value lost: %q", got)
	}
	if oc.SecurityCredentials["BasicAuth"].Username.GetValue() != "svc" {
		t.Fatalf("BasicAuth username lost: %+v", oc.SecurityCredentials["BasicAuth"])
	}

	out, err := json.Marshal(&cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back MCPClientConfig
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("re-unmarshal: %v", err)
	}
	if back.OpenAPIConfig == nil || back.OpenAPIConfig.Spec != oc.Spec || len(back.OpenAPIConfig.SecurityCredentials) != 2 {
		t.Fatalf("round trip lost data: %s", out)
	}
}
