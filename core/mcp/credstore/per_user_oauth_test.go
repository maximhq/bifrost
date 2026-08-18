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
// the ConnectionHeaders re-auth path exercised here, plus GetAdminAccessToken
// (configurable via adminAccessToken/adminAccessTokenErr) for the
// AdminConnectionHeaders tests below; the rest are unused.
type fakeOAuth2Provider struct {
	authorizeURL string

	adminAccessToken    string
	adminAccessTokenErr error
}

func (f *fakeOAuth2Provider) GetAccessToken(ctx context.Context, oauthConfigID string) (string, error) {
	return "", errors.New("not implemented")
}

// GetAdminAccessToken returns adminAccessTokenErr if set, else
// adminAccessToken. Neither field set (the zero value) preserves the
// original "not implemented" stub behavior for callers that don't care.
func (f *fakeOAuth2Provider) GetAdminAccessToken(ctx context.Context, oauthConfigID string) (string, error) {
	if f.adminAccessTokenErr != nil {
		return "", f.adminAccessTokenErr
	}
	if f.adminAccessToken != "" {
		return f.adminAccessToken, nil
	}
	return "", errors.New("not implemented")
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

func (f *fakeOAuth2Provider) RefreshAccessToken(ctx context.Context, tokenID string) error {
	return errors.New("not implemented")
}

func (f *fakeOAuth2Provider) ForceRefreshAccessToken(ctx *schemas.BifrostContext, config *schemas.MCPClientConfig) error {
	return errors.New("not implemented")
}

func (f *fakeOAuth2Provider) TokenExchangeAvailable() bool { return false }

func (f *fakeOAuth2Provider) GetExchangedAccessToken(ctx *schemas.BifrostContext, config *schemas.MCPClientConfig) (string, error) {
	return "", errors.New("not implemented")
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

// TestPerUserOAuthResolverAdminConnectionHeaders covers
// perUserOAuthResolver.AdminConnectionHeaders: the retained admin
// bootstrap-token lookup used by the periodic tool syncer's per-user path
// (see ClientToolSyncer.performSync / MCPManager.performAdminToolDiscovery),
// as distinct from ConnectionHeaders' real-caller path exercised above.
func TestPerUserOAuthResolverAdminConnectionHeaders(t *testing.T) {
	oauthConfigID := "oauth-config-1"
	baseConfig := &schemas.MCPClientConfig{
		ID:            "client-1",
		Name:          "Test Client",
		OauthConfigID: &oauthConfigID,
	}

	t.Run("success delegates to provider and returns a Bearer header", func(t *testing.T) {
		resolver := &perUserOAuthResolver{provider: &fakeOAuth2Provider{adminAccessToken: "admin-access-token-123"}}

		headers, err := resolver.AdminConnectionHeaders(context.Background(), baseConfig)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got, want := headers.Get("Authorization"), "Bearer admin-access-token-123"; got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
	})

	t.Run("nil provider returns an error", func(t *testing.T) {
		resolver := &perUserOAuthResolver{provider: nil}

		_, err := resolver.AdminConnectionHeaders(context.Background(), baseConfig)
		if err == nil {
			t.Fatal("expected an error when provider is nil")
		}
		if !strings.Contains(err.Error(), "OAuth2Provider") {
			t.Errorf("error = %q, expected to mention the missing OAuth2Provider", err.Error())
		}
	})

	t.Run("missing OauthConfigID returns an error", func(t *testing.T) {
		resolver := &perUserOAuthResolver{provider: &fakeOAuth2Provider{adminAccessToken: "unused"}}
		config := &schemas.MCPClientConfig{ID: "client-2", Name: "No Oauth Config"}

		_, err := resolver.AdminConnectionHeaders(context.Background(), config)
		if err == nil {
			t.Fatal("expected an error when OauthConfigID is nil")
		}
		if !strings.Contains(err.Error(), "no linked oauth config") {
			t.Errorf("error = %q, expected to mention the missing linked oauth config", err.Error())
		}
	})

	t.Run("empty OauthConfigID returns an error", func(t *testing.T) {
		empty := ""
		resolver := &perUserOAuthResolver{provider: &fakeOAuth2Provider{adminAccessToken: "unused"}}
		config := &schemas.MCPClientConfig{ID: "client-3", Name: "Empty Oauth Config", OauthConfigID: &empty}

		_, err := resolver.AdminConnectionHeaders(context.Background(), config)
		if err == nil {
			t.Fatal("expected an error when OauthConfigID is empty")
		}
		if !strings.Contains(err.Error(), "no linked oauth config") {
			t.Errorf("error = %q, expected to mention the missing linked oauth config", err.Error())
		}
	})

	t.Run("provider error propagates", func(t *testing.T) {
		providerErr := errors.New("admin token store unavailable")
		resolver := &perUserOAuthResolver{provider: &fakeOAuth2Provider{adminAccessTokenErr: providerErr}}

		_, err := resolver.AdminConnectionHeaders(context.Background(), baseConfig)
		if err == nil {
			t.Fatal("expected an error when the provider fails")
		}
		if !errors.Is(err, providerErr) {
			t.Errorf("expected error to wrap the provider error, got: %v", err)
		}
	})
}
