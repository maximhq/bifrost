package credstore

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestNoneResolverAdminConnectionHeadersReturnsError confirms noneResolver's
// stub AdminConnectionHeaders returns a plain (non-nil, non-panicking) error
// rather than ever being reachable in production — auth_type "none" has no
// separate admin credential, so the periodic tool syncer's per-user
// discovery path (see ClientToolSyncer.performSync) never dispatches here.
func TestNoneResolverAdminConnectionHeadersReturnsError(t *testing.T) {
	resolver := &noneResolver{}
	config := &schemas.MCPClientConfig{ID: "client-1", Name: "Test Client"}

	headers, err := resolver.AdminConnectionHeaders(context.Background(), config)
	if err == nil {
		t.Fatal("expected a non-nil error for auth_type \"none\"")
	}
	if headers != nil {
		t.Errorf("expected nil headers alongside the error, got %v", headers)
	}
}
