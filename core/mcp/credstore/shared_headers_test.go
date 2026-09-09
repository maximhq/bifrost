package credstore

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestSharedHeadersResolverAdminConnectionHeaders_StickyReturnsError
// confirms AdminConnectionHeaders still refuses to resolve anything for a
// sticky client (the default when NeedsSessionStickiness/ConnectionType
// aren't set) — a sticky client holds a persistent connection discovered
// via connectToMCPClient, and should never reach this method at all.
func TestSharedHeadersResolverAdminConnectionHeaders_StickyReturnsError(t *testing.T) {
	resolver := &sharedHeadersResolver{}
	config := &schemas.MCPClientConfig{ID: "client-1", Name: "Test Client"}

	headers, err := resolver.AdminConnectionHeaders(context.Background(), config)
	if err == nil {
		t.Fatal("expected a non-nil error for a sticky client")
	}
	if headers != nil {
		t.Errorf("expected nil headers alongside the error, got %v", headers)
	}
}

// TestSharedHeadersResolverAdminConnectionHeaders_PerCallDelegatesToConnectionHeaders
// pins the fix for the shared-headers-per-call tool-discovery bug: a client
// running per-call (needs_session_stickiness nil/false on an http
// connection) must resolve the same Authorization header ConnectionHeaders
// would — there is no separate "admin" credential for this auth type — so
// the periodic connection checker's per-call discovery cycle
// (MCPManager.performAdminToolDiscovery) can actually populate its tool map.
func TestSharedHeadersResolverAdminConnectionHeaders_PerCallDelegatesToConnectionHeaders(t *testing.T) {
	resolver := &sharedHeadersResolver{}
	config := &schemas.MCPClientConfig{
		ID:             "client-1",
		Name:           "Test Client",
		ConnectionType: schemas.MCPConnectionTypeHTTP,
		Headers:        map[string]schemas.SecretVar{"Authorization": *schemas.NewSecretVar("Bearer static-token")},
		// NeedsSessionStickiness left nil: the default per-call value.
	}

	headers, err := resolver.AdminConnectionHeaders(context.Background(), config)
	if err != nil {
		t.Fatalf("expected no error for a per-call client, got: %v", err)
	}
	if got := headers.Get("Authorization"); got != "Bearer static-token" {
		t.Errorf("expected the resolved Authorization header, got %q", got)
	}
}

// TestSharedHeadersResolverAdminConnectionHeaders_PerCallNoAuthorizationHeaderIsFine
// covers the case CodeRabbit-adjacent reasoning flagged: a shared client
// with no Authorization header configured at all (e.g. auth is carried
// entirely via other static headers, or none) must resolve to empty
// headers, not an error — VerifyHeadersConnection's separate "userHeaders
// required" guard is what needs relaxing for this, not this method.
func TestSharedHeadersResolverAdminConnectionHeaders_PerCallNoAuthorizationHeaderIsFine(t *testing.T) {
	resolver := &sharedHeadersResolver{}
	config := &schemas.MCPClientConfig{
		ID:             "client-1",
		Name:           "Test Client",
		ConnectionType: schemas.MCPConnectionTypeHTTP,
	}

	headers, err := resolver.AdminConnectionHeaders(context.Background(), config)
	if err != nil {
		t.Fatalf("expected no error for a per-call client with no Authorization header, got: %v", err)
	}
	if got := headers.Get("Authorization"); got != "" {
		t.Errorf("expected no Authorization header, got %q", got)
	}
}
