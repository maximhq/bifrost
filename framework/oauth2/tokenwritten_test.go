package oauth2

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

// Two providers over one store stand in for two processes sharing a token
// store; each test wires the writer's OnTokenWritten callback to the reader's
// local EvictUserTokenByID, the way a deployment would forward it.

// tokenRecorder collects the IDs passed to an OnTokenWritten callback.
type tokenRecorder struct {
	mu  sync.Mutex
	ids []string
}

func (r *tokenRecorder) record(_ context.Context, tokenID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids = append(r.ids, tokenID)
}

func (r *tokenRecorder) got() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ids...)
}

// countingTokenServer answers every token request, refresh or code exchange,
// with a fresh pair: access-1/refresh-1, access-2/refresh-2, ...
func countingTokenServer(t *testing.T) *httptest.Server {
	t.Helper()
	var n atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		i := n.Add(1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"access_token":  fmt.Sprintf("access-%d", i),
			"refresh_token": fmt.Sprintf("refresh-%d", i),
			"token_type":    "bearer",
			"expires_in":    3600,
		})
	}))
	t.Cleanup(server.Close)
	return server
}

// seedSessionTokenFor stores a live session-mode token "tok-1" (access-0) for
// sess-1 on mcp-1 whose template config points at tokenURL.
func seedSessionTokenFor(store *testConfigStore, tokenURL string) {
	store.oauthConfigs["cfg-1"] = &tables.TableOauthConfig{
		ID:          "cfg-1",
		ClientID:    schemas.NewSecretVar("client-id"),
		TokenURL:    tokenURL,
		RedirectURI: "http://localhost/callback",
		Scopes:      `["read"]`,
		Status:      "authorized",
	}
	seedUserToken(store, "tok-1", "cfg-1", "mcp-1", "sess-1", "access-0", bifrost.Ptr(time.Now().Add(time.Hour)))
}

// sessionAccessToken is the lookup a tool call for sess-1 on mcp-1 makes.
func sessionAccessToken(p *OAuth2Provider) (string, error) {
	return p.GetUserAccessTokenByMode(context.Background(), schemas.MCPAuthModeSession, "sess-1", "mcp-1")
}

// forceRefreshSession is what a tool call does after the upstream rejects
// sess-1's access token.
func forceRefreshSession(p *OAuth2Provider) error {
	oauthConfigID := "cfg-1"
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyMCPSessionID, "sess-1")
	return p.ForceRefreshAccessToken(ctx, &schemas.MCPClientConfig{
		ID:            "mcp-1",
		Name:          "Test Client",
		AuthType:      schemas.MCPAuthTypePerUserOauth,
		OauthConfigID: &oauthConfigID,
	})
}

// TestOnTokenWrittenReachesAnotherProcessAfterRefresh: without the callback
// the reader keeps serving the token the writer just replaced; with it, the
// reader serves the refreshed token.
func TestOnTokenWrittenReachesAnotherProcessAfterRefresh(t *testing.T) {
	store := newTestConfigStore()
	seedSessionTokenFor(store, countingTokenServer(t).URL+"/token")
	writer, reader := newTestProvider(store), newTestProvider(store)

	access, err := sessionAccessToken(reader)
	require.NoError(t, err)
	require.Equal(t, "access-0", access)

	require.NoError(t, forceRefreshSession(writer))
	access, err = sessionAccessToken(reader)
	require.NoError(t, err)
	require.Equal(t, "access-0", access, "with no callback the reader keeps the token the writer replaced")

	writer.SetOnTokenWritten(func(_ context.Context, tokenID string) {
		reader.EvictUserTokenByID(tokenID)
	})
	require.NoError(t, forceRefreshSession(writer))
	access, err = sessionAccessToken(reader)
	require.NoError(t, err)
	assert.Equal(t, "access-2", access)
}

// TestOnTokenWrittenAfterRefreshRejected: a permanently rejected refresh marks
// the row needs_reauth; the reader then reports re-authentication instead of
// serving the dead token.
func TestOnTokenWrittenAfterRefreshRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{"error": "invalid_grant"})
	}))
	t.Cleanup(server.Close)
	store := newTestConfigStore()
	seedSessionTokenFor(store, server.URL+"/token")
	writer, reader := newTestProvider(store), newTestProvider(store)
	rec := &tokenRecorder{}
	writer.SetOnTokenWritten(func(ctx context.Context, tokenID string) {
		rec.record(ctx, tokenID)
		reader.EvictUserTokenByID(tokenID)
	})

	_, err := sessionAccessToken(reader)
	require.NoError(t, err)

	require.ErrorIs(t, forceRefreshSession(writer), schemas.ErrOAuth2TokenExpired)
	assert.Equal(t, []string{"tok-1"}, rec.got())
	_, err = sessionAccessToken(reader)
	assert.ErrorIs(t, err, schemas.ErrOAuth2TokenNotFound)
}

// TestOnTokenWrittenSkipsLostRefreshRace: when another process already stored
// a newer refresh result, this refresh writes nothing and announces nothing;
// the process that won announces its own write.
func TestOnTokenWrittenSkipsLostRefreshRace(t *testing.T) {
	store := newTestConfigStore()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		store.mu.Lock()
		store.oauthTokens["tok-1"].RefreshToken = "stored-by-another-process"
		store.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"access_token": "discarded", "refresh_token": "discarded", "token_type": "bearer", "expires_in": 3600})
	}))
	t.Cleanup(server.Close)
	seedSessionTokenFor(store, server.URL+"/token")
	p := newTestProvider(store)
	rec := &tokenRecorder{}
	p.SetOnTokenWritten(rec.record)

	require.NoError(t, p.RefreshAccessToken(context.Background(), "tok-1"))
	assert.Empty(t, rec.got())
}

// reauthStore adds the flow methods CompleteUserOAuthFlow needs and upserts
// session-mode tokens by binding, as the real store does.
type reauthStore struct {
	*testConfigStore
	flow *tables.TableMCPOauthFlow
}

func (s *reauthStore) ClaimOauthUserSessionByState(_ context.Context, state string) (*tables.TableMCPOauthFlow, error) {
	if s.flow == nil || s.flow.State != state {
		return nil, nil
	}
	flow := s.flow
	s.flow = nil
	return flow, nil
}

func (s *reauthStore) DeleteOauthUserSession(context.Context, string) error {
	return nil
}

func (s *reauthStore) CreateOauthToken(_ context.Context, token *tables.TableMCPOauthToken, _ ...*gorm.DB) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.oauthTokens {
		if existing.AuthMode == token.AuthMode && existing.MCPClientID == token.MCPClientID && existing.SessionID == token.SessionID {
			token.ID = existing.ID
			break
		}
	}
	s.oauthTokens[token.ID] = bifrost.Ptr(*token)
	return nil
}

// TestOnTokenWrittenReachesAnotherProcessAfterReauthorization: re-authorizing
// an existing binding replaces its row in place, and the reader switches to
// the new credential.
func TestOnTokenWrittenReachesAnotherProcessAfterReauthorization(t *testing.T) {
	store := &reauthStore{testConfigStore: newTestConfigStore()}
	seedSessionTokenFor(store.testConfigStore, countingTokenServer(t).URL+"/token")
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	writer, reader := NewOAuth2Provider(store, logger), NewOAuth2Provider(store, logger)
	writer.SetOnTokenWritten(func(_ context.Context, tokenID string) {
		reader.EvictUserTokenByID(tokenID)
	})

	access, err := sessionAccessToken(reader)
	require.NoError(t, err)
	require.Equal(t, "access-0", access)

	store.flow = &tables.TableMCPOauthFlow{
		ID:            "flow-1",
		MCPClientID:   "mcp-1",
		OauthConfigID: "cfg-1",
		State:         "state-1",
		CodeVerifier:  "verifier",
		SessionID:     "sess-1",
		FlowMode:      string(schemas.MCPAuthModeSession),
		Status:        "claiming",
		ExpiresAt:     time.Now().Add(10 * time.Minute),
	}
	_, err = writer.CompleteUserOAuthFlow(context.Background(), "state-1", "code-1")
	require.NoError(t, err)

	access, err = sessionAccessToken(reader)
	require.NoError(t, err)
	assert.Equal(t, "access-1", access)
}

// TestOnTokenWrittenAfterAdminExchangeWrites: retaining and renewing a
// token-exchange admin credential both announce the row.
func TestOnTokenWrittenAfterAdminExchangeWrites(t *testing.T) {
	var hits atomic.Int64
	var form url.Values
	server := tokenEndpointStub(t, &hits, &form, "minted-1", 3600)
	defer server.Close()

	store := newTestConfigStore()
	config := exchangeTestClientConfig()
	store.mcpClients = map[string]*schemas.MCPClientConfig{config.ID: config}
	p := NewOAuth2Provider(store, nil)
	p.retryBaseDelay = time.Millisecond
	p.SetTokenExchangeIdPResolver(&fakeIdPResolver{
		available: true,
		idp: &schemas.TokenExchangeIdP{
			TokenEndpoint: server.URL,
			GrantShape:    schemas.TokenExchangeGrantRFC8693,
		},
	})
	rec := &tokenRecorder{}
	p.SetOnTokenWritten(rec.record)
	ctx := context.Background()

	require.NoError(t, p.RetainExchangeAdminCredential(ctx, config, &schemas.OAuth2TokenExchangeResponse{
		AccessToken:  "retained-1",
		RefreshToken: "refresh-1",
		TokenType:    "Bearer",
		ExpiresIn:    3600,
	}))
	row, err := store.GetAdminOauthTokenByMCPClientID(ctx, config.ID)
	require.NoError(t, err)
	require.NotNil(t, row)

	store.mu.Lock()
	store.oauthTokens[row.ID].ExpiresAt = bifrost.Ptr(time.Now().Add(-time.Minute))
	store.mu.Unlock()
	token, err := p.GetAdminAccessToken(ctx, config.ID)
	require.NoError(t, err)
	require.Equal(t, "minted-1", token)

	assert.Equal(t, []string{row.ID, row.ID}, rec.got())
}

// TestEvictUserTokenByIDDoesNotCallOnTokenWritten: applying another process's
// eviction must not announce it again, or processes would echo it back and
// forth.
func TestEvictUserTokenByIDDoesNotCallOnTokenWritten(t *testing.T) {
	p := newTestProvider(newTestConfigStore())
	rec := &tokenRecorder{}
	p.SetOnTokenWritten(rec.record)

	p.EvictUserTokenByID("tok-1")

	assert.Empty(t, rec.got())
}
