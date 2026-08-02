package credstore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// fakeOAuth2Provider implements schemas.OAuth2Provider. Only
// GetUserAccessTokenByMode and InitiateUserOAuthFlow need real behavior for
// the ConnectionHeaders re-auth path exercised here; the rest are unused.
type fakeOAuth2Provider struct {
	authorizeURL string
}

func (f *fakeOAuth2Provider) GetAccessToken(ctx context.Context, oauthConfigID string) (string, error) {
	return "", errors.New("not implemented")
}

func (f *fakeOAuth2Provider) RefreshAccessToken(ctx context.Context, oauthConfigID string) error {
	return errors.New("not implemented")
}

func (f *fakeOAuth2Provider) ValidateToken(ctx context.Context, oauthConfigID string) (bool, error) {
	return false, errors.New("not implemented")
}

func (f *fakeOAuth2Provider) RevokeToken(ctx context.Context, oauthConfigID string) error {
	return errors.New("not implemented")
}

func (f *fakeOAuth2Provider) GetUserAccessTokenByMode(ctx context.Context, mode schemas.MCPAuthMode, identity, mcpClientID string) (string, error) {
	return "", schemas.ErrOAuth2TokenNotFound
}

func (f *fakeOAuth2Provider) InitiateUserOAuthFlow(ctx context.Context, oauthConfigID string, mcpClientID string, redirectURI string, flowMode schemas.MCPAuthMode) (*schemas.OAuth2FlowInitiation, string, error) {
	return &schemas.OAuth2FlowInitiation{AuthorizeURL: f.authorizeURL}, "flow-session-id", nil
}

func (f *fakeOAuth2Provider) CompleteUserOAuthFlow(ctx context.Context, state string, code string) (string, error) {
	return "", errors.New("not implemented")
}

func (f *fakeOAuth2Provider) RefreshUserAccessToken(ctx context.Context, tokenID string) error {
	return errors.New("not implemented")
}

func newTestOAuthContext() *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyMCPSessionID, "sess-1")
	ctx.SetValue(schemas.BifrostContextKeyMCPCallbackBaseURL, "https://host")
	return ctx
}

func TestPerUserOAuthResolverConnectionHeadersTempTokenReminder(t *testing.T) {
	oauthConfigID := "oauth-config-1"
	config := &schemas.MCPClientConfig{
		ID:            "client-1",
		Name:          "Test Client",
		OauthConfigID: &oauthConfigID,
	}

	tests := []struct {
		name         string
		authorizeURL string
		wantReminder bool
	}{
		{
			name:         "authorize URL with temp-token fragment includes reminder",
			authorizeURL: "https://host/workspace/mcp-sessions/auth?flow=abc123#t=xyz",
			wantReminder: true,
		},
		{
			name:         "authorize URL without fragment omits reminder",
			authorizeURL: "https://host/workspace/mcp-sessions/auth?flow=abc123",
			wantReminder: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := &perUserOAuthResolver{provider: &fakeOAuth2Provider{authorizeURL: tt.authorizeURL}}
			ctx := newTestOAuthContext()

			_, err := resolver.ConnectionHeaders(ctx, config)
			if err == nil {
				t.Fatalf("expected an MCPAuthRequiredError, got nil")
			}
			authErr, ok := err.(*schemas.MCPAuthRequiredError)
			if !ok {
				t.Fatalf("expected *schemas.MCPAuthRequiredError, got %T: %v", err, err)
			}

			gotReminder := strings.Contains(authErr.Message, schemas.MCPAuthTempTokenReminder)
			if gotReminder != tt.wantReminder {
				t.Errorf("Message = %q, contains reminder = %v, want %v", authErr.Message, gotReminder, tt.wantReminder)
			}
		})
	}
}
