package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Test doubles and helpers shared by the performAdminToolDiscovery tests
// below and the ClientToolSyncer.performSync tests in toolsync_test.go.
// =============================================================================

// fakeAdminCredStore is a schemas.MCPCredentialStore test double whose
// AdminConnectionHeaders return value (and call count) is fully
// configurable. The other methods are unused by performAdminToolDiscovery /
// performSync but must exist to satisfy the interface.
type fakeAdminCredStore struct {
	mu      sync.Mutex
	headers http.Header
	err     error
	calls   int
}

func (f *fakeAdminCredStore) ConnectionHeaders(_ *schemas.BifrostContext, _ *schemas.MCPClientConfig) (http.Header, error) {
	return http.Header{}, nil
}

func (f *fakeAdminCredStore) RequestHeaders(_ *schemas.BifrostContext, _ *schemas.MCPClientConfig) (http.Header, error) {
	return http.Header{}, nil
}

func (f *fakeAdminCredStore) RequiresPerCallConnection(_ *schemas.MCPClientConfig) bool {
	return true
}

func (f *fakeAdminCredStore) ForceRefresh(_ *schemas.BifrostContext, _ *schemas.MCPClientConfig) error {
	return nil
}

func (f *fakeAdminCredStore) AdminConnectionHeaders(_ context.Context, _ *schemas.MCPClientConfig) (http.Header, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.headers, nil
}

func (f *fakeAdminCredStore) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// adminDiscoveryRecorder captures the inbound headers of every HTTP request
// the fake upstream MCP server (below) receives, so tests can assert on
// exactly what performAdminToolDiscovery put on the wire.
type adminDiscoveryRecorder struct {
	mu      sync.Mutex
	headers []http.Header
	methods []string
}

func (r *adminDiscoveryRecorder) add(method string, h http.Header) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.headers = append(r.headers, h.Clone())
	r.methods = append(r.methods, method)
}

// headerValues returns every recorded value for name, one per POST (JSON-RPC)
// request received. An ephemeral admin-discovery connection issues at least
// an "initialize" and a "tools/list" call over the same transport, both
// carrying whatever headers transport.WithHTTPHeaders configured, so a
// non-empty caller-supplied header should show up on every entry. DELETE
// requests (session termination on client.Close(), issued by the underlying
// MCP client library without the custom headers) are excluded — they carry
// no application-level data and aren't part of what this test verifies.
func (r *adminDiscoveryRecorder) headerValues(name string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for i, h := range r.headers {
		if r.methods[i] == http.MethodDelete {
			continue
		}
		out = append(out, h.Get(name))
	}
	return out
}

// buildAdminDiscoveryHTTPServer starts a real streamable-HTTP MCP server
// (one "echo" tool) behind an httptest server whose middleware records the
// inbound headers of every request. Used to drive
// VerifyPerUserOAuthConnection / VerifyHeadersConnection (and therefore
// performAdminToolDiscovery) through a real connect-discover-close cycle
// instead of mocking them out, so the header plumbing is verified at the
// wire rather than assumed.
func buildAdminDiscoveryHTTPServer(t *testing.T) (*httptest.Server, *adminDiscoveryRecorder) {
	t.Helper()

	s := server.NewMCPServer("test-admin-discovery", "1.0.0", server.WithToolCapabilities(true))
	echoTool := mcpgo.NewTool("echo",
		mcpgo.WithDescription("Echo tool"),
		mcpgo.WithString("message", mcpgo.Required(), mcpgo.Description("message")),
	)
	s.AddTool(echoTool, func(_ context.Context, req mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
		msg, _ := req.GetArguments()["message"].(string)
		return mcpgo.NewToolResultText(msg), nil
	})

	streamable := server.NewStreamableHTTPServer(s)
	rec := &adminDiscoveryRecorder{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r.Method, r.Header)
		streamable.ServeHTTP(w, r)
	})

	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	return ts, rec
}

// =============================================================================
// MCPManager.performAdminToolDiscovery
// =============================================================================

// TestPerformAdminToolDiscovery_PerUserOAuth_ExtractsBearerTokenAndDispatches
// confirms the per_user_oauth branch trims the "Bearer " prefix off the
// resolved Authorization header and reaches VerifyPerUserOAuthConnection —
// verified at the wire (the fake upstream sees the exact same bearer token),
// not just by dispatch happening.
func TestPerformAdminToolDiscovery_PerUserOAuth_ExtractsBearerTokenAndDispatches(t *testing.T) {
	ts, rec := buildAdminDiscoveryHTTPServer(t)

	cred := &fakeAdminCredStore{headers: http.Header{"Authorization": []string{"Bearer admin-secret-token"}}}
	m := &MCPManager{credStore: cred, logger: &MockLogger{}}

	config := &schemas.MCPClientConfig{
		ID:               "client-1",
		Name:             "oauth-client",
		AuthType:         schemas.MCPAuthTypePerUserOauth,
		ConnectionType:   schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(ts.URL),
	}

	tools, mapping, err := m.performAdminToolDiscovery(context.Background(), config)
	require.NoError(t, err)
	require.Contains(t, tools, "oauth-client-echo", "discovered tool should be present, prefixed by client name")
	require.Equal(t, "echo", mapping["echo"])
	require.Equal(t, 1, cred.callCount())

	authVals := rec.headerValues("Authorization")
	require.NotEmpty(t, authVals, "upstream should have received at least one request carrying Authorization")
	for _, v := range authVals {
		require.Equal(t, "Bearer admin-secret-token", v, "the exact resolved bearer token must reach the upstream unmodified")
	}
}

// TestPerformAdminToolDiscovery_PerUserOAuth_EmptyBearerToken_ErrorsWithoutDialing
// pins the empty-token guard: if the resolved Authorization header trims
// down to an empty token, performAdminToolDiscovery must fail fast with its
// own error instead of ever attempting a connection.
func TestPerformAdminToolDiscovery_PerUserOAuth_EmptyBearerToken_ErrorsWithoutDialing(t *testing.T) {
	cred := &fakeAdminCredStore{headers: http.Header{}} // no Authorization header at all
	m := &MCPManager{credStore: cred, logger: &MockLogger{}}

	config := &schemas.MCPClientConfig{
		ID:       "client-1b",
		Name:     "oauth-client",
		AuthType: schemas.MCPAuthTypePerUserOauth,
		// Deliberately no ConnectionString: if the code tried to dial, it
		// would fail on this instead, masking whether the empty-token guard
		// ran first. Its absence here proves the guard short-circuits.
	}

	tools, mapping, err := m.performAdminToolDiscovery(context.Background(), config)
	require.Error(t, err)
	require.Nil(t, tools)
	require.Nil(t, mapping)
	require.Contains(t, err.Error(), "admin credential resolved no access token")
}

// TestPerformAdminToolDiscovery_PerUserHeaders_ConvertsHeadersAndDispatches
// confirms the per_user_headers branch converts the resolved http.Header
// into a map[string]string correctly (every declared value present, none
// dropped or duplicated) and reaches VerifyHeadersConnection — verified at
// the wire.
func TestPerformAdminToolDiscovery_PerUserHeaders_ConvertsHeadersAndDispatches(t *testing.T) {
	ts, rec := buildAdminDiscoveryHTTPServer(t)

	cred := &fakeAdminCredStore{headers: http.Header{
		"X-Api-Key":   []string{"admin-key-1"},
		"X-Tenant-Id": []string{"tenant-42"},
	}}
	m := &MCPManager{credStore: cred, logger: &MockLogger{}}

	config := &schemas.MCPClientConfig{
		ID:                "client-2",
		Name:              "headers-client",
		AuthType:          schemas.MCPAuthTypePerUserHeaders,
		ConnectionType:    schemas.MCPConnectionTypeHTTP,
		ConnectionString:  schemas.NewSecretVar(ts.URL),
		PerUserHeaderKeys: []string{"x-api-key", "x-tenant-id"},
	}

	tools, mapping, err := m.performAdminToolDiscovery(context.Background(), config)
	require.NoError(t, err)
	require.Contains(t, tools, "headers-client-echo")
	require.Equal(t, "echo", mapping["echo"])
	require.Equal(t, 1, cred.callCount())

	apiKeyVals := rec.headerValues("X-Api-Key")
	require.NotEmpty(t, apiKeyVals)
	for _, v := range apiKeyVals {
		require.Equal(t, "admin-key-1", v)
	}

	tenantVals := rec.headerValues("X-Tenant-Id")
	require.NotEmpty(t, tenantVals)
	for _, v := range tenantVals {
		require.Equal(t, "tenant-42", v)
	}
}

// TestPerformAdminToolDiscovery_UnsupportedAuthType_ReturnsError confirms a
// genuinely unknown auth type falls into the default branch and errors
// instead of attempting discovery.
func TestPerformAdminToolDiscovery_UnsupportedAuthType_ReturnsError(t *testing.T) {
	cred := &fakeAdminCredStore{headers: http.Header{"Authorization": []string{"Bearer x"}}}
	m := &MCPManager{credStore: cred, logger: &MockLogger{}}

	config := &schemas.MCPClientConfig{
		ID:       "client-3",
		Name:     "shared-client",
		AuthType: schemas.MCPAuthType("something_unknown"),
	}

	tools, mapping, err := m.performAdminToolDiscovery(context.Background(), config)
	require.Error(t, err)
	require.Nil(t, tools)
	require.Nil(t, mapping)
	require.True(t, strings.Contains(err.Error(), "admin tool discovery not supported for auth_type"))
}

// TestPerformAdminToolDiscovery_SharedOAuth_PerCall_ExtractsBearerTokenAndDispatches
// pins the fix for the shared-OAuth-per-call tool-discovery bug: auth_type
// "oauth" now routes through the same bearer-based dispatch as
// per_user_oauth/token_exchange, since sharedOAuthResolver.AdminConnectionHeaders
// delegates to ConnectionHeaders for a per-call client (see credstore's
// shared_oauth.go) instead of erroring.
func TestPerformAdminToolDiscovery_SharedOAuth_PerCall_ExtractsBearerTokenAndDispatches(t *testing.T) {
	ts, rec := buildAdminDiscoveryHTTPServer(t)

	cred := &fakeAdminCredStore{headers: http.Header{"Authorization": []string{"Bearer shared-secret-token"}}}
	m := &MCPManager{credStore: cred, logger: &MockLogger{}}

	config := &schemas.MCPClientConfig{
		ID:               "client-5",
		Name:             "shared-oauth-client",
		AuthType:         schemas.MCPAuthTypeOauth,
		ConnectionType:   schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(ts.URL),
		// NeedsSessionStickiness left nil: the default per-call value.
	}

	tools, mapping, err := m.performAdminToolDiscovery(context.Background(), config)
	require.NoError(t, err)
	require.Contains(t, tools, "shared-oauth-client-echo")
	require.Equal(t, "echo", mapping["echo"])
	require.Equal(t, 1, cred.callCount())

	authVals := rec.headerValues("Authorization")
	require.NotEmpty(t, authVals)
	for _, v := range authVals {
		require.Equal(t, "Bearer shared-secret-token", v)
	}
}

// TestPerformAdminToolDiscovery_SharedHeadersAndNone_PerCall_ConvertsHeadersAndDispatches
// is the shared headers/none counterpart, table-driven since both route
// through the same headers-map dispatch branch as per_user_headers.
func TestPerformAdminToolDiscovery_SharedHeadersAndNone_PerCall_ConvertsHeadersAndDispatches(t *testing.T) {
	for _, authType := range []schemas.MCPAuthType{schemas.MCPAuthTypeHeaders, schemas.MCPAuthTypeNone} {
		t.Run(string(authType), func(t *testing.T) {
			ts, rec := buildAdminDiscoveryHTTPServer(t)

			cred := &fakeAdminCredStore{headers: http.Header{"X-Api-Key": []string{"shared-key-1"}}}
			m := &MCPManager{credStore: cred, logger: &MockLogger{}}

			config := &schemas.MCPClientConfig{
				ID:               "client-6",
				Name:             "shared-headers-client",
				AuthType:         authType,
				ConnectionType:   schemas.MCPConnectionTypeHTTP,
				ConnectionString: schemas.NewSecretVar(ts.URL),
			}

			tools, mapping, err := m.performAdminToolDiscovery(context.Background(), config)
			require.NoError(t, err)
			require.Contains(t, tools, "shared-headers-client-echo")
			require.Equal(t, "echo", mapping["echo"])
			require.Equal(t, 1, cred.callCount())

			apiKeyVals := rec.headerValues("X-Api-Key")
			require.NotEmpty(t, apiKeyVals)
			for _, v := range apiKeyVals {
				require.Equal(t, "shared-key-1", v)
			}
		})
	}
}

// TestPerformAdminToolDiscovery_SharedHeaders_PerCall_EmptyHeadersStillDispatches
// pins the VerifyHeadersConnection guard relaxation: a shared headers/none
// client with NO resolved headers at all (auth carried entirely by other
// static config headers, or none) must still attempt discovery rather than
// erroring on "user headers are required" — that guard is per_user_headers
// specific.
func TestPerformAdminToolDiscovery_SharedHeaders_PerCall_EmptyHeadersStillDispatches(t *testing.T) {
	ts, _ := buildAdminDiscoveryHTTPServer(t)

	cred := &fakeAdminCredStore{headers: http.Header{}} // resolves to nothing
	m := &MCPManager{credStore: cred, logger: &MockLogger{}}

	config := &schemas.MCPClientConfig{
		ID:               "client-7",
		Name:             "shared-none-client",
		AuthType:         schemas.MCPAuthTypeNone,
		ConnectionType:   schemas.MCPConnectionTypeHTTP,
		ConnectionString: schemas.NewSecretVar(ts.URL),
	}

	tools, _, err := m.performAdminToolDiscovery(context.Background(), config)
	require.NoError(t, err)
	require.Contains(t, tools, "shared-none-client-echo")
}

// TestPerformAdminToolDiscovery_CredStoreError_Propagates confirms a
// credStore.AdminConnectionHeaders failure is wrapped and returned as-is,
// without attempting any connection.
func TestPerformAdminToolDiscovery_CredStoreError_Propagates(t *testing.T) {
	credErr := errors.New("admin credential store unavailable")
	cred := &fakeAdminCredStore{err: credErr}
	m := &MCPManager{credStore: cred, logger: &MockLogger{}}

	config := &schemas.MCPClientConfig{
		ID:       "client-4",
		Name:     "oauth-client",
		AuthType: schemas.MCPAuthTypePerUserOauth,
	}

	tools, mapping, err := m.performAdminToolDiscovery(context.Background(), config)
	require.Error(t, err)
	require.Nil(t, tools)
	require.Nil(t, mapping)
	require.True(t, errors.Is(err, credErr), "expected the credStore error to be wrapped (%%w), got: %v", err)
	require.Contains(t, err.Error(), "failed to resolve admin credential")
}
