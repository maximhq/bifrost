package credstore

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestNoneResolverAdminConnectionHeaders_StickyReturnsError confirms
// AdminConnectionHeaders still refuses to resolve anything for a sticky
// client (the default when NeedsSessionStickiness/ConnectionType aren't
// set) — a sticky client holds a persistent connection discovered via
// connectToMCPClient, and should never reach this method at all.
func TestNoneResolverAdminConnectionHeaders_StickyReturnsError(t *testing.T) {
	resolver := &noneResolver{}
	config := &schemas.MCPClientConfig{ID: "client-1", Name: "Test Client"}

	headers, err := resolver.AdminConnectionHeaders(context.Background(), config)
	if err == nil {
		t.Fatal("expected a non-nil error for a sticky client")
	}
	if headers != nil {
		t.Errorf("expected nil headers alongside the error, got %v", headers)
	}
}

// TestNoneResolverAdminConnectionHeaders_PerCallDelegatesToConnectionHeaders
// pins the fix for the none-auth-per-call tool-discovery bug: a client
// running per-call (needs_session_stickiness nil/false on an http
// connection) must resolve successfully (empty headers, same as
// ConnectionHeaders) rather than erroring — so the periodic connection
// checker's per-call discovery cycle
// (MCPManager.performAdminToolDiscovery) can actually run for it.
func TestNoneResolverAdminConnectionHeaders_PerCallDelegatesToConnectionHeaders(t *testing.T) {
	resolver := &noneResolver{}
	config := &schemas.MCPClientConfig{
		ID:             "client-1",
		Name:           "Test Client",
		ConnectionType: schemas.MCPConnectionTypeHTTP,
		// NeedsSessionStickiness left nil: the default per-call value.
	}

	headers, err := resolver.AdminConnectionHeaders(context.Background(), config)
	if err != nil {
		t.Fatalf("expected no error for a per-call client, got: %v", err)
	}
	if len(headers) != 0 {
		t.Errorf("expected empty headers, got %v", headers)
	}
}
