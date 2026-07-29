package credstore

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestSharedHeadersResolverAdminConnectionHeadersReturnsError confirms
// sharedHeadersResolver's stub AdminConnectionHeaders returns a plain
// (non-nil, non-panicking) error — auth_type "headers" has no separate admin
// credential distinct from the shared one, so the periodic tool syncer's
// per-user discovery path (see ClientToolSyncer.performSync) never
// dispatches here.
func TestSharedHeadersResolverAdminConnectionHeadersReturnsError(t *testing.T) {
	resolver := &sharedHeadersResolver{}
	config := &schemas.MCPClientConfig{ID: "client-1", Name: "Test Client"}

	headers, err := resolver.AdminConnectionHeaders(context.Background(), config)
	if err == nil {
		t.Fatal("expected a non-nil error for auth_type \"headers\"")
	}
	if headers != nil {
		t.Errorf("expected nil headers alongside the error, got %v", headers)
	}
}
