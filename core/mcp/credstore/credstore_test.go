package credstore

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestRequiresPerCallConnection_OpenAPIIsAlwaysSticky pins the one override the
// openapi connection type needs in the credential store: its server is
// synthesized in-process, so even the auth types that force a fresh transport
// per call elsewhere (per_user_headers) keep the single in-process connection.
// Per-user credentials are resolved inside the synthesized tool handler from
// the call's own context instead.
func TestRequiresPerCallConnection_OpenAPIIsAlwaysSticky(t *testing.T) {
	store := NewCredStore(nil, nil, nil)
	for _, authType := range []schemas.MCPAuthType{
		"",
		schemas.MCPAuthTypeNone,
		schemas.MCPAuthTypeHeaders,
		schemas.MCPAuthTypePerUserHeaders,
	} {
		cfg := &schemas.MCPClientConfig{
			ID:                "openapi-1",
			Name:              "petstore",
			ConnectionType:    schemas.MCPConnectionTypeOpenAPI,
			AuthType:          authType,
			PerUserHeaderKeys: []string{"X-User-Token"},
		}
		if store.RequiresPerCallConnection(cfg) {
			t.Fatalf("auth_type %q: openapi clients must never be per-call", authType)
		}
	}

	// Control: the same per-user auth type on an HTTP client is per-call, so the
	// override above is doing the work rather than the resolver table.
	httpCfg := &schemas.MCPClientConfig{
		ID:                "http-1",
		Name:              "remote",
		ConnectionType:    schemas.MCPConnectionTypeHTTP,
		AuthType:          schemas.MCPAuthTypePerUserHeaders,
		PerUserHeaderKeys: []string{"X-User-Token"},
	}
	if !store.RequiresPerCallConnection(httpCfg) {
		t.Fatal("per_user_headers over HTTP must stay per-call")
	}
}
