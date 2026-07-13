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
type fakeMCPHeadersProvider struct {
	frontendURL string
}

func (f *fakeMCPHeadersProvider) GetCredentialByMode(ctx context.Context, mode schemas.MCPAuthMode, identity, mcpClientID string) (*schemas.MCPHeadersUserCredential, error) {
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
