package credstore

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// TestAWSSigV4Resolver: no header is resolved (the HTTP client signs), stickiness is
// honored as for none, ForceRefresh succeeds without network, and the store routes
// the auth type to it.
func TestAWSSigV4Resolver(t *testing.T) {
	store := NewCredStore(nil, nil, nil)
	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	config := &schemas.MCPClientConfig{
		ID: "client-1", Name: "gateway", AuthType: schemas.MCPAuthTypeAWSSigV4, ConnectionType: schemas.MCPConnectionTypeHTTP,
		AWSSigV4: &schemas.MCPAWSSigV4Config{Region: schemas.NewSecretVar("us-east-1"), RoleARN: schemas.NewSecretVar("arn:aws:iam::123456789012:role/r")},
	}

	headers, err := store.ConnectionHeaders(bfCtx, config)
	if err != nil {
		t.Fatalf("ConnectionHeaders: %v", err)
	}
	if len(headers) != 0 {
		t.Fatalf("aws_sigv4 resolves no headers, got %v", headers)
	}
	if !store.RequiresPerCallConnection(config) {
		t.Fatal("an http client with stickiness unset runs per-call")
	}
	sticky := *config
	sticky.NeedsSessionStickiness = schemas.Ptr(true)
	if store.RequiresPerCallConnection(&sticky) {
		t.Fatal("aws_sigv4 must honor needs_session_stickiness")
	}

	adminHeaders, err := store.AdminConnectionHeaders(context.Background(), config)
	if err != nil || len(adminHeaders) != 0 {
		t.Fatalf("AdminConnectionHeaders on a per-call client: %v, %v", adminHeaders, err)
	}
	if _, err := store.AdminConnectionHeaders(context.Background(), &sticky); err == nil {
		t.Fatal("expected an error for admin headers on a sticky client")
	}

	// Nothing is cached yet: invalidating is a no-op, never a network call.
	if err := store.ForceRefresh(bfCtx, config); err != nil {
		t.Fatalf("ForceRefresh: %v", err)
	}

	missing := *config
	missing.AWSSigV4 = nil
	if _, err := store.ConnectionHeaders(bfCtx, &missing); err == nil {
		t.Fatal("expected an error without an aws_sigv4 block")
	}
	if err := store.ForceRefresh(bfCtx, &missing); err == nil {
		t.Fatal("expected ForceRefresh to fail without an aws_sigv4 block")
	}
}
