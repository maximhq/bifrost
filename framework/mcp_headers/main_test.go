package mcp_headers

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/temptoken"
)

// testConfigStore is a minimal in-memory implementation of configstore.ConfigStore
// for use in mcp_headers tests. Embeds the interface so unneeded methods panic if
// called. Mirrors framework/oauth2/sync_test.go's testConfigStore.
type testConfigStore struct {
	configstore.ConfigStore

	mu           sync.Mutex
	clientConfig *configstore.ClientConfig
	headerFlows  map[string]*tables.TableMCPPerUserHeaderFlow
	tempTokens   map[string]*tables.TempToken
}

func newTestConfigStore() *testConfigStore {
	return &testConfigStore{
		headerFlows: make(map[string]*tables.TableMCPPerUserHeaderFlow),
		tempTokens:  make(map[string]*tables.TempToken),
	}
}

func (s *testConfigStore) GetClientConfig(_ context.Context) (*configstore.ClientConfig, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.clientConfig == nil {
		return nil, nil
	}
	return bifrost.Ptr(*s.clientConfig), nil
}

func (s *testConfigStore) GetMCPPerUserHeaderFlowByModeIdentityAndMCPClient(_ context.Context, mode schemas.MCPAuthMode, identity, mcpClientID string) (*tables.TableMCPPerUserHeaderFlow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, flow := range s.headerFlows {
		if flow.FlowMode != string(mode) || flow.MCPClientID != mcpClientID {
			continue
		}
		switch mode {
		case schemas.MCPAuthModeUser:
			if flow.UserID != nil && *flow.UserID == identity {
				return bifrost.Ptr(*flow), nil
			}
		case schemas.MCPAuthModeVK:
			if flow.VirtualKeyID != nil && *flow.VirtualKeyID == identity {
				return bifrost.Ptr(*flow), nil
			}
		case schemas.MCPAuthModeSession:
			if flow.SessionID == identity {
				return bifrost.Ptr(*flow), nil
			}
		}
	}
	return nil, nil
}

func (s *testConfigStore) CreateMCPPerUserHeaderFlow(_ context.Context, flow *tables.TableMCPPerUserHeaderFlow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.headerFlows[flow.ID] = bifrost.Ptr(*flow)
	return nil
}

func (s *testConfigStore) UpdateMCPPerUserHeaderFlow(_ context.Context, flow *tables.TableMCPPerUserHeaderFlow) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.headerFlows[flow.ID] = bifrost.Ptr(*flow)
	return nil
}

func (s *testConfigStore) CreateTempToken(_ context.Context, token *tables.TempToken, _ ...*gorm.DB) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tempTokens[token.ID] = bifrost.Ptr(*token)
	return nil
}

func newTestProvider(store *testConfigStore) *Provider {
	return NewProvider(store, bifrost.NewDefaultLogger(schemas.LogLevelError))
}

// newTempTokenService wires a temptoken.Service with the mcp_headers_auth scope
// registered. Mirrors what handlers.RegisterTempTokenScopes does at server
// startup, but kept local so this package has no dependency on the handlers
// layer.
func newTempTokenService(t *testing.T, store *testConfigStore) *temptoken.Service {
	t.Helper()
	svc := temptoken.NewService(store, temptoken.NewRegistry())
	require.NoError(t, svc.Registry().Register(temptoken.Scope{
		Name: temptoken.MCPHeadersAuthScopeName,
		AllowedRoutes: []temptoken.RoutePattern{
			{Method: "GET", Path: "/api/mcp/per-user-headers/flows/{id}"},
			{Method: "PUT", Path: "/api/mcp/per-user-headers/flows/{id}"},
		},
		ResourceIDInPath: "{id}",
		MaxTTL:           SubmissionFlowTTL,
	}))
	return svc
}

func TestMCPTempTokenAuthEnabled(t *testing.T) {
	store := newTestConfigStore()
	provider := newTestProvider(store)

	assert.False(t, provider.mcpTempTokenAuthEnabled(context.Background()))

	store.clientConfig = &configstore.ClientConfig{}
	assert.False(t, provider.mcpTempTokenAuthEnabled(context.Background()))

	store.clientConfig.MCPEnableTempTokenAuth = true
	assert.True(t, provider.mcpTempTokenAuthEnabled(context.Background()))
}

func TestInitiateUserSubmissionFlow_TempTokenGatedByClientConfig(t *testing.T) {
	// Verifies that the MCPEnableTempTokenAuth client-config toggle gates the
	// URL fragment on the submit URL — the same behavior the OAuth surface
	// already had and that headers must mirror for the UI switch to control
	// both kinds uniformly.
	const (
		mcpClientID = "test-mcp-client"
		identity    = "test-user"
		baseURL     = "http://localhost:8080"
	)

	t.Run("toggle disabled: URL has no fragment", func(t *testing.T) {
		store := newTestConfigStore()
		store.clientConfig = &configstore.ClientConfig{MCPEnableTempTokenAuth: false}
		provider := newTestProvider(store)
		provider.SetTempTokenService(newTempTokenService(t, store))

		initiation, err := provider.InitiateUserSubmissionFlow(context.Background(), schemas.MCPAuthModeUser, identity, mcpClientID, baseURL)
		require.NoError(t, err)
		assert.NotContains(t, initiation.FrontendURL, "#t=", "temp-token fragment must not be appended when toggle is off")
		assert.Empty(t, store.tempTokens, "Mint must not be called when toggle is off")
	})

	t.Run("toggle enabled (VK mode): URL carries fragment", func(t *testing.T) {
		// VK/session-mode flows are intentionally shareable and continue to mint
		// when the toggle is on; user-mode is gated separately (see subtest below).
		store := newTestConfigStore()
		store.clientConfig = &configstore.ClientConfig{MCPEnableTempTokenAuth: true}
		provider := newTestProvider(store)
		provider.SetTempTokenService(newTempTokenService(t, store))

		initiation, err := provider.InitiateUserSubmissionFlow(context.Background(), schemas.MCPAuthModeVK, identity, mcpClientID, baseURL)
		require.NoError(t, err)
		assert.Contains(t, initiation.FrontendURL, "#t=", "temp-token fragment must be appended when toggle is on")
		require.Len(t, store.tempTokens, 1, "Mint must be called exactly once when toggle is on")
		for _, tok := range store.tempTokens {
			assert.Equal(t, temptoken.MCPHeadersAuthScopeName, tok.Scope)
			assert.Equal(t, initiation.FlowID, tok.ResourceID, "minted token must be bound to the flow row's ID")
		}
	})

	t.Run("toggle enabled (user mode): mint is skipped", func(t *testing.T) {
		// User-mode flows must never mint a temp token even when the toggle is on:
		// the handler-side identity gate requires caller user_id to match
		// flow.UserID, and the temp-token middleware bypasses cookie resolution,
		// which would 403 even legitimate users.
		store := newTestConfigStore()
		store.clientConfig = &configstore.ClientConfig{MCPEnableTempTokenAuth: true}
		provider := newTestProvider(store)
		provider.SetTempTokenService(newTempTokenService(t, store))

		initiation, err := provider.InitiateUserSubmissionFlow(context.Background(), schemas.MCPAuthModeUser, identity, mcpClientID, baseURL)
		require.NoError(t, err)
		assert.NotContains(t, initiation.FrontendURL, "#t=", "user-mode flows must not carry a temp-token fragment")
		assert.Empty(t, store.tempTokens, "Mint must not be called for user-mode flows")
	})

	t.Run("no temp-token service installed: URL has no fragment", func(t *testing.T) {
		store := newTestConfigStore()
		store.clientConfig = &configstore.ClientConfig{MCPEnableTempTokenAuth: true}
		provider := newTestProvider(store) // SetTempTokenService not called

		initiation, err := provider.InitiateUserSubmissionFlow(context.Background(), schemas.MCPAuthModeUser, identity, mcpClientID, baseURL)
		require.NoError(t, err)
		assert.NotContains(t, initiation.FrontendURL, "#t=", "no fragment when temp-token service is unavailable")
	})

	t.Run("toggle enabled but client-config read fails: URL has no fragment", func(t *testing.T) {
		// Same shape as oauth2.mcpTempTokenAuthEnabled: a configstore error is
		// treated as 'disabled' (fail-closed) rather than panicking the flow.
		store := &errClientConfigStore{testConfigStore: newTestConfigStore()}
		provider := newTestProvider(store.testConfigStore)
		provider.configStore = store // swap in the wrapper so GetClientConfig errors
		provider.SetTempTokenService(newTempTokenService(t, store.testConfigStore))

		initiation, err := provider.InitiateUserSubmissionFlow(context.Background(), schemas.MCPAuthModeUser, identity, mcpClientID, baseURL)
		require.NoError(t, err, "flow init must still succeed even when client-config read fails")
		assert.NotContains(t, initiation.FrontendURL, "#t=", "no fragment when client-config read errors")
	})
}

// errClientConfigStore returns an error from GetClientConfig but otherwise
// delegates to the embedded testConfigStore. Used to verify mcpTempTokenAuthEnabled
// fails closed on read errors.
type errClientConfigStore struct {
	*testConfigStore
}

func (s *errClientConfigStore) GetClientConfig(_ context.Context) (*configstore.ClientConfig, error) {
	return nil, assertErr("synthetic client-config read failure")
}

type assertErr string

func (e assertErr) Error() string { return string(e) }

// sanity: the auth-page URL always carries the flow ID + kind=headers query
// param, regardless of the temp-token toggle. Guards against URL-shape
// regressions that would break the dashboard auth landing page.
func TestInitiateUserSubmissionFlow_URLShape(t *testing.T) {
	store := newTestConfigStore()
	provider := newTestProvider(store)

	initiation, err := provider.InitiateUserSubmissionFlow(context.Background(), schemas.MCPAuthModeUser, "u", "client", "http://localhost:8080/")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(initiation.FrontendURL, "http://localhost:8080/workspace/mcp-sessions/auth?flow="))
	assert.Contains(t, initiation.FrontendURL, "&kind=headers")
	assert.WithinDuration(t, time.Now().Add(SubmissionFlowTTL), initiation.ExpiresAt, time.Second)
}
