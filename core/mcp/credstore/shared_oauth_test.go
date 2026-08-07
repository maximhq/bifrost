package credstore

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// fakeSharedOAuthProvider implements schemas.OAuth2Provider with only
// GetAccessToken configurable — the only method sharedOAuthResolver's
// ConnectionHeaders calls. Every other method is unused by these tests.
type fakeSharedOAuthProvider struct {
	accessToken string
}

func (f *fakeSharedOAuthProvider) GetAccessToken(_ context.Context, _ string) (string, error) {
	return f.accessToken, nil
}
func (f *fakeSharedOAuthProvider) GetAdminAccessToken(_ context.Context, _ string) (string, error) {
	return "", nil
}
func (f *fakeSharedOAuthProvider) ValidateToken(_ context.Context, _ string) (bool, error) {
	return false, nil
}
func (f *fakeSharedOAuthProvider) RevokeToken(_ context.Context, _ string) error { return nil }
func (f *fakeSharedOAuthProvider) GetUserAccessTokenByMode(_ context.Context, _ schemas.MCPAuthMode, _, _ string) (string, error) {
	return "", nil
}
func (f *fakeSharedOAuthProvider) InitiateUserOAuthFlow(_ context.Context, _ string, _ string, _ string, _ schemas.MCPAuthMode) (*schemas.OAuth2FlowInitiation, string, error) {
	return nil, "", nil
}
func (f *fakeSharedOAuthProvider) CompleteUserOAuthFlow(_ context.Context, _ string, _ string) (string, error) {
	return "", nil
}
func (f *fakeSharedOAuthProvider) RefreshAccessToken(_ context.Context, _ string) error { return nil }
func (f *fakeSharedOAuthProvider) ForceRefreshAccessToken(_ *schemas.BifrostContext, _ *schemas.MCPClientConfig) error {
	return nil
}
func (f *fakeSharedOAuthProvider) TokenExchangeAvailable() bool { return false }
func (f *fakeSharedOAuthProvider) GetExchangedAccessToken(_ *schemas.BifrostContext, _ *schemas.MCPClientConfig) (string, error) {
	return "", nil
}

// TestSharedOAuthResolverAdminConnectionHeaders_StickyReturnsError confirms
// AdminConnectionHeaders still refuses to resolve anything for a sticky
// client (the default when NeedsSessionStickiness/ConnectionType aren't
// set, matching needsSessionStickiness' own default-to-sticky behavior) — a
// sticky client holds a persistent connection discovered via
// connectToMCPClient, and should never reach this method at all.
func TestSharedOAuthResolverAdminConnectionHeaders_StickyReturnsError(t *testing.T) {
	resolver := &sharedOAuthResolver{}
	config := &schemas.MCPClientConfig{ID: "client-1", Name: "Test Client"}

	headers, err := resolver.AdminConnectionHeaders(context.Background(), config)
	if err == nil {
		t.Fatal("expected a non-nil error for a sticky client")
	}
	if headers != nil {
		t.Errorf("expected nil headers alongside the error, got %v", headers)
	}
}

// TestSharedOAuthResolverAdminConnectionHeaders_PerCallDelegatesToConnectionHeaders
// pins the fix for the shared-OAuth-per-call tool-discovery bug: a client
// running per-call (needs_session_stickiness nil/false on an http
// connection) must resolve the same bearer token ConnectionHeaders would —
// there is no separate "admin" credential for this auth type — so the
// periodic connection checker's per-call discovery cycle
// (MCPManager.performAdminToolDiscovery) can actually populate its tool map.
func TestSharedOAuthResolverAdminConnectionHeaders_PerCallDelegatesToConnectionHeaders(t *testing.T) {
	resolver := &sharedOAuthResolver{provider: &fakeSharedOAuthProvider{accessToken: "shared-token-abc"}}
	oauthConfigID := "oauth-config-1"
	config := &schemas.MCPClientConfig{
		ID:             "client-1",
		Name:           "Test Client",
		ConnectionType: schemas.MCPConnectionTypeHTTP,
		OauthConfigID:  &oauthConfigID,
		// NeedsSessionStickiness left nil: the default per-call value.
	}

	headers, err := resolver.AdminConnectionHeaders(context.Background(), config)
	if err != nil {
		t.Fatalf("expected no error for a per-call client, got: %v", err)
	}
	if got := headers.Get("Authorization"); got != "Bearer shared-token-abc" {
		t.Errorf("expected the resolved bearer token, got %q", got)
	}
}
