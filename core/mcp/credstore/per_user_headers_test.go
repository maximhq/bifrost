package credstore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// fakeMCPHeadersProvider implements schemas.MCPHeadersProvider. Only
// GetCredentialByMode and InitiateUserSubmissionFlow need real behavior for
// the ConnectionHeaders re-auth path exercised here; the rest are unused.
// GetCredentialByMode is configurable via credential/credentialErr for the
// AdminConnectionHeaders tests below — neither field set (the zero value)
// preserves the original ErrHeadersCredentialNotFound stub behavior.
type fakeMCPHeadersProvider struct {
	frontendURL string

	credential    *schemas.MCPHeadersUserCredential
	credentialErr error
}

func (f *fakeMCPHeadersProvider) GetCredentialByMode(ctx context.Context, mode schemas.MCPAuthMode, identity, mcpClientID string) (*schemas.MCPHeadersUserCredential, error) {
	if f.credentialErr != nil {
		return nil, f.credentialErr
	}
	if f.credential != nil {
		return f.credential, nil
	}
	return nil, schemas.ErrHeadersCredentialNotFound
}

func (f *fakeMCPHeadersProvider) UpsertCredential(ctx context.Context, cred *schemas.MCPHeadersUserCredential) error {
	return errors.New("not implemented")
}

func (f *fakeMCPHeadersProvider) DeleteCredential(ctx context.Context, id string) error {
	return errors.New("not implemented")
}

func (f *fakeMCPHeadersProvider) InitiateUserSubmissionFlow(ctx context.Context, mode schemas.MCPAuthMode, identity, mcpClientID, baseURL string) (*schemas.MCPHeadersFlowInitiation, error) {
	return &schemas.MCPHeadersFlowInitiation{FlowID: "flow-1", FrontendURL: f.frontendURL}, nil
}

func newTestHeadersContext() *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyMCPSessionID, "sess-1")
	ctx.SetValue(schemas.BifrostContextKeyMCPCallbackBaseURL, "https://host")
	return ctx
}

func TestPerUserHeadersResolverConnectionHeadersTempTokenReminder(t *testing.T) {
	config := &schemas.MCPClientConfig{
		ID:                "client-1",
		Name:              "Test Client",
		PerUserHeaderKeys: []string{"authorization"},
	}

	tests := []struct {
		name         string
		frontendURL  string
		wantReminder bool
	}{
		{
			name:         "frontend URL with temp-token fragment includes reminder",
			frontendURL:  "https://host/workspace/mcp-sessions/auth?flow=abc123#t=xyz",
			wantReminder: true,
		},
		{
			name:         "frontend URL without fragment omits reminder",
			frontendURL:  "https://host/workspace/mcp-sessions/auth?flow=abc123",
			wantReminder: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := &perUserHeadersResolver{provider: &fakeMCPHeadersProvider{frontendURL: tt.frontendURL}}
			ctx := newTestHeadersContext()

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

// TestPerUserHeadersResolverAdminConnectionHeaders covers
// perUserHeadersResolver.AdminConnectionHeaders: the retained admin header
// credential lookup used by the periodic tool syncer's per-user path (see
// ClientToolSyncer.performSync / MCPManager.performAdminToolDiscovery), as
// distinct from ConnectionHeaders' real-caller path exercised above.
func TestPerUserHeadersResolverAdminConnectionHeaders(t *testing.T) {
	config := &schemas.MCPClientConfig{
		ID:                "client-1",
		Name:              "Test Client",
		PerUserHeaderKeys: []string{"authorization", "x-tenant-id"},
	}

	t.Run("success delegates to provider and returns filtered header values", func(t *testing.T) {
		provider := &fakeMCPHeadersProvider{credential: &schemas.MCPHeadersUserCredential{
			MCPClientID: config.ID,
			AuthMode:    schemas.MCPAuthModeAdmin,
			Headers: map[string]string{
				"authorization": "Bearer admin-value",
				"x-tenant-id":   "tenant-42",
				"x-extra":       "should-not-appear", // not declared in PerUserHeaderKeys
			},
			Status: schemas.MCPHeadersUserCredentialStatusActive,
		}}
		resolver := &perUserHeadersResolver{provider: provider}

		headers, err := resolver.AdminConnectionHeaders(context.Background(), config)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got, want := headers.Get("Authorization"), "Bearer admin-value"; got != want {
			t.Errorf("Authorization = %q, want %q", got, want)
		}
		if got, want := headers.Get("X-Tenant-Id"), "tenant-42"; got != want {
			t.Errorf("X-Tenant-Id = %q, want %q", got, want)
		}
		if got := headers.Get("X-Extra"); got != "" {
			t.Errorf("X-Extra leaked into output: %q, want empty (not declared in PerUserHeaderKeys)", got)
		}
	})

	t.Run("nil provider returns an error", func(t *testing.T) {
		resolver := &perUserHeadersResolver{provider: nil}

		_, err := resolver.AdminConnectionHeaders(context.Background(), config)
		if err == nil {
			t.Fatal("expected an error when provider is nil")
		}
		if !strings.Contains(err.Error(), "MCPHeadersProvider") {
			t.Errorf("error = %q, expected to mention the missing MCPHeadersProvider", err.Error())
		}
	})

	t.Run("provider error propagates", func(t *testing.T) {
		providerErr := errors.New("credential store unavailable")
		resolver := &perUserHeadersResolver{provider: &fakeMCPHeadersProvider{credentialErr: providerErr}}

		_, err := resolver.AdminConnectionHeaders(context.Background(), config)
		if err == nil {
			t.Fatal("expected an error when the provider fails")
		}
		if !errors.Is(err, providerErr) {
			t.Errorf("expected error to wrap the provider error, got: %v", err)
		}
	})

	t.Run("missing required header key returns an error", func(t *testing.T) {
		provider := &fakeMCPHeadersProvider{credential: &schemas.MCPHeadersUserCredential{
			MCPClientID: config.ID,
			AuthMode:    schemas.MCPAuthModeAdmin,
			Headers: map[string]string{
				"authorization": "Bearer admin-value",
				// x-tenant-id intentionally absent — required by config.PerUserHeaderKeys
			},
			Status: schemas.MCPHeadersUserCredentialStatusNeedsUpdate,
		}}
		resolver := &perUserHeadersResolver{provider: provider}

		_, err := resolver.AdminConnectionHeaders(context.Background(), config)
		if err == nil {
			t.Fatal("expected an error when a required header key is missing from the stored credential")
		}
		if !strings.Contains(err.Error(), "missing required keys") {
			t.Errorf("error = %q, expected to mention missing required keys", err.Error())
		}
		if !strings.Contains(err.Error(), "x-tenant-id") {
			t.Errorf("error = %q, expected to name the missing key x-tenant-id", err.Error())
		}
	})
}
