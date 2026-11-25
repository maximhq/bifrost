package credstore

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestClientCredentialsResolver: the shared token rides the Authorization
// header, stickiness is honored as for shared oauth, and the store routes the
// auth type to it.
func TestClientCredentialsResolver(t *testing.T) {
	store := NewCredStore(&fakeSharedOAuthProvider{accessToken: "cc-token"}, nil, nil)
	config := &schemas.MCPClientConfig{ID: "client-1", Name: "cc", AuthType: schemas.MCPAuthTypeClientCredentials, ConnectionType: schemas.MCPConnectionTypeHTTP}

	headers, err := store.ConnectionHeaders(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), config)
	if err != nil {
		t.Fatalf("ConnectionHeaders: %v", err)
	}
	if got := headers.Get("Authorization"); got != "Bearer cc-token" {
		t.Fatalf("expected the shared bearer token, got %q", got)
	}
	// A shared credential: per-call only by default (stickiness unset), never forced.
	if !store.RequiresPerCallConnection(config) {
		t.Fatal("an http client with stickiness unset runs per-call")
	}
	sticky := *config
	sticky.NeedsSessionStickiness = schemas.Ptr(true)
	if store.RequiresPerCallConnection(&sticky) {
		t.Fatal("client_credentials must honor needs_session_stickiness, like oauth")
	}

	// Per-call (stickiness unset on http): admin discovery uses the same token.
	adminHeaders, err := store.AdminConnectionHeaders(context.Background(), config)
	if err != nil {
		t.Fatalf("AdminConnectionHeaders: %v", err)
	}
	if got := adminHeaders.Get("Authorization"); got != "Bearer cc-token" {
		t.Fatalf("expected the shared bearer token for admin discovery, got %q", got)
	}

	if _, err := (&clientCredentialsResolver{}).ConnectionHeaders(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), config); err == nil {
		t.Fatal("expected an error without an OAuth provider")
	}
}
