package oauth2

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

// dcrConfigStore is a gorm/sqlite-backed ConfigStore double. Unlike the
// map-based testConfigStore in sync_test.go it can serve InitiateOAuthFlow's
// ExecuteTransaction body, which issues raw tx.Create / tx.Where calls — that
// path is what performs RFC 7591 dynamic client registration, so a test that
// needs a genuinely DCR-provisioned oauth_configs row (rather than a
// hand-seeded one) has to go through it. Embeds the interface so any method
// the exercised path doesn't need panics instead of silently returning a zero
// value.
type dcrConfigStore struct {
	configstore.ConfigStore
	db *gorm.DB
}

func newDCRConfigStore(t *testing.T) *dcrConfigStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger:                                   gormlogger.Default.LogMode(gormlogger.Silent),
		DisableForeignKeyConstraintWhenMigrating: true,
	})
	require.NoError(t, err, "failed to open in-memory sqlite")
	require.NoError(t, db.AutoMigrate(
		&tables.TableOauthConfig{},
		&tables.TableMCPOauthFlow{},
		&tables.TableMCPOauthToken{},
		&tables.TableMCPClient{},
	))
	return &dcrConfigStore{db: db}
}

func (s *dcrConfigStore) ExecuteTransaction(ctx context.Context, fn func(tx *gorm.DB) error) error {
	return s.db.WithContext(ctx).Transaction(fn)
}

func (s *dcrConfigStore) GetOauthConfigByID(ctx context.Context, id string) (*tables.TableOauthConfig, error) {
	var cfg tables.TableOauthConfig
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&cfg).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &cfg, nil
}

func (s *dcrConfigStore) UpdateOauthConfig(ctx context.Context, config *tables.TableOauthConfig, tx ...*gorm.DB) error {
	db := s.db
	if len(tx) > 0 && tx[0] != nil {
		db = tx[0]
	}
	return db.WithContext(ctx).Save(config).Error
}

// RotateMCPOAuthConfig mirrors RDBConfigStore's: write every field when any of
// them differs, cascading every bound token to needs_reauth.
func (s *dcrConfigStore) RotateMCPOAuthConfig(ctx context.Context, existing *tables.TableOauthConfig, fields configstore.MCPOAuthConfigFields) (bool, error) {
	if existing == nil {
		return false, errors.New("oauth config is nil")
	}
	if !fields.DiffersFrom(existing) {
		return false, nil
	}
	existing.ClientID = fields.ClientID.Clone()
	existing.ClientSecret = fields.ClientSecret.Clone()
	existing.AuthorizeURL = fields.AuthorizeURL
	existing.TokenURL = fields.TokenURL
	if fields.RegistrationURL == "" {
		existing.RegistrationURL = nil
	} else {
		existing.RegistrationURL = bifrost.Ptr(fields.RegistrationURL)
	}
	existing.Resource = fields.Resource
	if len(fields.Scopes) == 0 {
		existing.Scopes = ""
	} else {
		scopesJSON, err := json.Marshal(fields.Scopes)
		if err != nil {
			return false, err
		}
		existing.Scopes = string(scopesJSON)
	}
	if err := s.UpdateOauthConfig(ctx, existing); err != nil {
		return false, err
	}
	if err := s.MarkTokensNeedsReauthByConfigID(ctx, existing.ID, "OAuth client credentials were rotated"); err != nil {
		return false, err
	}
	return true, nil
}

func (s *dcrConfigStore) GetOauthTokenByID(ctx context.Context, id string) (*tables.TableMCPOauthToken, error) {
	var token tables.TableMCPOauthToken
	if err := s.db.WithContext(ctx).Where("id = ?", id).First(&token).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &token, nil
}

func (s *dcrConfigStore) MarkOauthUserTokenNeedsReauthByID(ctx context.Context, tokenID, reason string) error {
	return s.db.WithContext(ctx).Model(&tables.TableMCPOauthToken{}).
		Where("id = ?", tokenID).
		Updates(map[string]any{"status": "needs_reauth", "status_reason": reason}).Error
}

func (s *dcrConfigStore) MarkTokensNeedsReauthByConfigID(ctx context.Context, oauthConfigID, reason string, tx ...*gorm.DB) error {
	db := s.db
	if len(tx) > 0 && tx[0] != nil {
		db = tx[0]
	}
	return db.WithContext(ctx).Model(&tables.TableMCPOauthToken{}).
		Where("oauth_config_id = ?", oauthConfigID).
		Updates(map[string]any{"status": "needs_reauth", "status_reason": reason}).Error
}

func (s *dcrConfigStore) RefreshOauthTokenFieldsIfActive(ctx context.Context, id, expectedPriorRefreshToken, accessToken, refreshToken string, expiresAt *time.Time, lastRefreshedAt time.Time) (bool, error) {
	res := s.db.WithContext(ctx).Model(&tables.TableMCPOauthToken{}).
		Where("id = ? AND status = ? AND refresh_token = ?", id, "active", expectedPriorRefreshToken).
		Updates(map[string]any{
			"access_token":      accessToken,
			"refresh_token":     refreshToken,
			"expires_at":        expiresAt,
			"last_refreshed_at": lastRefreshedAt,
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

func (s *dcrConfigStore) CreateOauthUserSession(ctx context.Context, flow *tables.TableMCPOauthFlow) error {
	return s.db.WithContext(ctx).Create(flow).Error
}

func (s *dcrConfigStore) UpdateOauthUserSession(ctx context.Context, flow *tables.TableMCPOauthFlow) error {
	return s.db.WithContext(ctx).Save(flow).Error
}

func (s *dcrConfigStore) GetOauthFlowByID(ctx context.Context, id string) (*tables.TableMCPOauthFlow, error) {
	var flow tables.TableMCPOauthFlow
	if err := s.db.WithContext(ctx).Where("id = ? AND flow_mode = ?", id, "admin").First(&flow).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &flow, nil
}

func (s *dcrConfigStore) GetOauthUserSessionByModeIdentityAndMCPClient(ctx context.Context, mode schemas.MCPAuthMode, identity, mcpClientID string) (*tables.TableMCPOauthFlow, error) {
	if mcpClientID == "" || mode != schemas.MCPAuthModeAdmin {
		return nil, nil
	}
	var flow tables.TableMCPOauthFlow
	err := s.db.WithContext(ctx).Where("flow_mode = ? AND mcp_client_id = ?", "admin", mcpClientID).First(&flow).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &flow, nil
}

// evictableAuthServer is an RFC 7591 authorization server whose client
// registry can be wiped, exactly as the Grafana MCP server in issue #7191 does
// when it restarts: previously registered client_ids stop being recognised and
// the token endpoint answers "invalid_client: Invalid client_id" for them.
type evictableAuthServer struct {
	mu            sync.Mutex
	registrations int
	known         map[string]bool
	// redirectURIs is what each issued client was registered with, which is
	// what a real authorization server checks an authorize request against.
	redirectURIs map[string][]string
	// refuseWith, when non-zero, makes /register answer with that status
	// instead of issuing a client.
	refuseWith int
	// reissue, when set, makes /register hand back this client_id rather than
	// minting one: a provider that recognises the registration as one it
	// already holds.
	reissue string
	server  *httptest.Server
}

func newEvictableAuthServer(t *testing.T) *evictableAuthServer {
	t.Helper()
	as := &evictableAuthServer{known: map[string]bool{}, redirectURIs: map[string][]string{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			RedirectURIs []string `json:"redirect_uris"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)

		w.Header().Set("Content-Type", "application/json")
		as.mu.Lock()
		if as.refuseWith != 0 {
			status := as.refuseWith
			as.mu.Unlock()
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": "invalid_client_metadata"})
			return
		}
		as.registrations++
		clientID := as.reissue
		if clientID == "" {
			clientID = fmt.Sprintf("dyn-client-%d", as.registrations)
		}
		as.known[clientID] = true
		as.redirectURIs[clientID] = req.RedirectURIs
		as.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"client_id": clientID})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		clientID := r.PostFormValue("client_id")
		as.mu.Lock()
		recognised := as.known[clientID]
		as.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if !recognised {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error":             "invalid_client",
				"error_description": "Invalid client_id",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "at-for-" + clientID,
			"refresh_token": "rt-for-" + clientID,
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	})
	as.server = httptest.NewServer(mux)
	t.Cleanup(as.server.Close)
	return as
}

// forgetAllClients simulates the authorization server restarting with an empty
// (in-memory) client registry.
func (as *evictableAuthServer) forgetAllClients() {
	as.mu.Lock()
	defer as.mu.Unlock()
	as.known = map[string]bool{}
}

func (as *evictableAuthServer) registrationCount() int {
	as.mu.Lock()
	defer as.mu.Unlock()
	return as.registrations
}

// redirectURIsFor reports the redirect_uris clientID was registered with.
func (as *evictableAuthServer) redirectURIsFor(clientID string) []string {
	as.mu.Lock()
	defer as.mu.Unlock()
	return as.redirectURIs[clientID]
}

// refuseRegistrations makes every later /register answer with status.
func (as *evictableAuthServer) refuseRegistrations(status int) {
	as.mu.Lock()
	defer as.mu.Unlock()
	as.refuseWith = status
}

// reissueClient makes every later /register hand back clientID.
func (as *evictableAuthServer) reissueClient(clientID string) {
	as.mu.Lock()
	defer as.mu.Unlock()
	as.reissue = clientID
}

const (
	dcrMCPClientID = "mcp-grafana"
	dcrTokenID     = "shared-token"
	dcrUserTokenID = "user-token"
	dcrRedirectURI = "http://localhost:8080/api/oauth/callback"
)

func newDCRProvider(t *testing.T) (*OAuth2Provider, *dcrConfigStore) {
	t.Helper()
	store := newDCRConfigStore(t)
	provider := NewOAuth2Provider(store, bifrost.NewDefaultLogger(schemas.LogLevelError))
	provider.retryBaseDelay = time.Millisecond
	return provider, store
}

// dcrFixture bootstraps a shared-OAuth MCP client against as, dynamically
// registering its client_id, and seeds the MCP client row plus the shared
// credential a completed consent would have produced. Returns the oauth config
// ID and the client_id the provider issued.
func dcrFixture(t *testing.T, as *evictableAuthServer, store *dcrConfigStore, provider *OAuth2Provider) (oauthConfigID, registeredClientID string) {
	t.Helper()
	ctx := context.Background()

	initiation, err := provider.InitiateOAuthFlow(ctx, &schemas.OAuth2Config{
		AuthorizeURL:    as.server.URL + "/authorize",
		TokenURL:        as.server.URL + "/token",
		RegistrationURL: bifrost.Ptr(as.server.URL + "/register"),
		RedirectURI:     dcrRedirectURI,
		ServerURL:       as.server.URL,
		Scopes:          []string{"openid"},
	})
	require.NoError(t, err)
	require.Equal(t, 1, as.registrationCount(), "bootstrap must dynamically register exactly one client")

	bootstrapped, err := store.GetOauthConfigByID(ctx, initiation.OauthConfigID)
	require.NoError(t, err)
	require.NotNil(t, bootstrapped)
	registeredClientID = bootstrapped.GetResolvedClientID()
	require.NotEmpty(t, registeredClientID)

	require.NoError(t, store.db.Create(&tables.TableMCPClient{
		ClientID:      dcrMCPClientID,
		Name:          "grafana",
		OauthConfigID: bifrost.Ptr(initiation.OauthConfigID),
	}).Error)
	require.NoError(t, store.db.Create(&tables.TableMCPOauthToken{
		ID:            dcrTokenID,
		AuthMode:      "shared",
		MCPClientID:   dcrMCPClientID,
		OauthConfigID: initiation.OauthConfigID,
		Status:        "active",
		AccessToken:   "at-for-" + registeredClientID,
		RefreshToken:  "rt-for-" + registeredClientID,
		TokenType:     "Bearer",
		ExpiresAt:     bifrost.Ptr(time.Now().Add(-time.Minute)),
	}).Error)

	return initiation.OauthConfigID, registeredClientID
}

// reauthorize runs what POST /api/mcp/client/{id}/reauthorize runs: an
// admin-mode flow against the stored oauth_configs row, then the upstream
// authorize URL the popup opens.
func reauthorize(t *testing.T, provider *OAuth2Provider, oauthConfigID string) (string, error) {
	t.Helper()
	ctx := context.Background()
	_, flowID, err := provider.InitiateUserOAuthFlow(ctx, oauthConfigID, dcrMCPClientID, dcrRedirectURI, schemas.MCPAuthModeAdmin)
	if err != nil {
		return "", err
	}
	return provider.BuildAdminUpstreamAuthorizeURL(ctx, flowID)
}

// authorizeURLClientID pulls the client_id the popup would be opened with.
func authorizeURLClientID(t *testing.T, authorizeURL string) string {
	t.Helper()
	parsed, err := url.Parse(authorizeURL)
	require.NoError(t, err)
	return parsed.Query().Get("client_id")
}

// providerAcceptsClient reports whether the authorization server would honor a
// token request from clientID, which is what decides whether the consent the
// admin is about to complete can actually be redeemed.
func providerAcceptsClient(t *testing.T, as *evictableAuthServer, clientID string) bool {
	t.Helper()
	form := url.Values{}
	form.Set("client_id", clientID)
	resp, err := http.Post(as.server.URL+"/token", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	require.NoError(t, err)
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// TestReregister_RecoversFromEvictedRegistration is the
// regression test for issue #7191.
//
// A shared-OAuth MCP client is bootstrapped against an authorization server
// that supports dynamic client registration, so its oauth_configs row holds a
// DCR-issued client_id. The server then drops its client registry (a restart).
// Bifrost's refresh is answered with 401 invalid_client, which correctly flips
// the credential to needs_reauth. Plain Reauthorize cannot recover from there,
// because it redoes consent with the very client_id the provider just said it
// has never heard of — that is the "manual re-login does not recover it" half
// of the report. Registering a replacement client first is what recovers it.
func TestReregister_RecoversFromEvictedRegistration(t *testing.T) {
	as := newEvictableAuthServer(t)
	provider, store := newDCRProvider(t)
	ctx := context.Background()

	oauthConfigID, evictedClientID := dcrFixture(t, as, store, provider)

	// The upstream server restarts and forgets every registered client.
	as.forgetAllClients()

	// The access token expires and the refresh is rejected with
	// invalid_client. This half already behaves correctly.
	require.ErrorIs(t, provider.RefreshAccessToken(ctx, dcrTokenID), schemas.ErrOAuth2TokenExpired)
	rejected, err := store.GetOauthTokenByID(ctx, dcrTokenID)
	require.NoError(t, err)
	require.Equal(t, "needs_reauth", rejected.Status)
	require.Contains(t, rejected.StatusReason, "invalid_client")

	// Plain Reauthorize hands back a URL built from the evicted client, which
	// the provider will reject at the code exchange behind it. This is the
	// dead end the issue reports, and it stays as-is.
	plainURL, err := reauthorize(t, provider, oauthConfigID)
	require.NoError(t, err)
	require.Equal(t, evictedClientID, authorizeURLClientID(t, plainURL))
	require.False(t, providerAcceptsClient(t, as, evictedClientID),
		"fixture check: the provider must still be rejecting the evicted client")

	// Reauthorize with a new client registers a replacement first.
	previousClientID, newClientID, _, err := provider.ReregisterDynamicClient(ctx, oauthConfigID, dcrRedirectURI)
	require.NoError(t, err)
	assert.Equal(t, evictedClientID, previousClientID, "must report which client_id it replaced")
	assert.NotEqual(t, evictedClientID, newClientID, "must issue a different client_id")
	assert.Equal(t, 2, as.registrationCount(), "must register exactly one replacement client")

	repaired, err := store.GetOauthConfigByID(ctx, oauthConfigID)
	require.NoError(t, err)
	assert.Equal(t, newClientID, repaired.GetResolvedClientID(),
		"the replacement must be persisted, not just returned")

	repairedURL, err := reauthorize(t, provider, oauthConfigID)
	require.NoError(t, err)
	assert.Equal(t, newClientID, authorizeURLClientID(t, repairedURL),
		"the authorize URL handed to the admin must carry the newly registered client")
	assert.True(t, providerAcceptsClient(t, as, newClientID),
		"the client in the authorize URL must be one the provider recognises, or the code exchange behind consent fails the same way")
}

// TestReauthorize_WithoutReregistering_ReusesStoredClient pins the default
// route's behavior: plain Reauthorize never touches the registration. Re-registering
// discards a credential, so it only ever happens when an admin asks for it by
// hitting the other endpoint, never as a fallback Bifrost decides on its own.
func TestReauthorize_WithoutReregistering_ReusesStoredClient(t *testing.T) {
	as := newEvictableAuthServer(t)
	provider, store := newDCRProvider(t)
	ctx := context.Background()

	oauthConfigID, registeredClientID := dcrFixture(t, as, store, provider)
	as.forgetAllClients()
	require.ErrorIs(t, provider.RefreshAccessToken(ctx, dcrTokenID), schemas.ErrOAuth2TokenExpired)

	authorizeURL, err := reauthorize(t, provider, oauthConfigID)
	require.NoError(t, err)

	assert.Equal(t, 1, as.registrationCount(), "plain reauthorize must not register anything")
	assert.Equal(t, registeredClientID, authorizeURLClientID(t, authorizeURL))

	stored, err := store.GetOauthConfigByID(ctx, oauthConfigID)
	require.NoError(t, err)
	assert.Equal(t, registeredClientID, stored.GetResolvedClientID())
}

// TestReregisterDynamicClient_CascadesEveryBoundToken pins the consequence the
// UI has to warn about: a replacement client_id invalidates every token bound
// to the config, not just the admin's, because none of them can be refreshed
// against a client the provider no longer associates them with.
func TestReregisterDynamicClient_CascadesEveryBoundToken(t *testing.T) {
	as := newEvictableAuthServer(t)
	provider, store := newDCRProvider(t)
	ctx := context.Background()

	oauthConfigID, _ := dcrFixture(t, as, store, provider)

	// An end user's own credential on the same server, still healthy.
	require.NoError(t, store.db.Create(&tables.TableMCPOauthToken{
		ID:            dcrUserTokenID,
		AuthMode:      "user",
		MCPClientID:   dcrMCPClientID,
		OauthConfigID: oauthConfigID,
		UserID:        bifrost.Ptr("user-1"),
		Status:        "active",
		AccessToken:   "user-at",
		RefreshToken:  "user-rt",
		TokenType:     "Bearer",
		ExpiresAt:     bifrost.Ptr(time.Now().Add(time.Hour)),
	}).Error)

	_, _, _, err := provider.ReregisterDynamicClient(ctx, oauthConfigID, dcrRedirectURI)
	require.NoError(t, err)

	userToken, err := store.GetOauthTokenByID(ctx, dcrUserTokenID)
	require.NoError(t, err)
	assert.Equal(t, "needs_reauth", userToken.Status,
		"an end user's credential is bound to the replaced client and must be invalidated with it")
}

// TestReregisterDynamicClient_NoRegistrationURL_Errors covers the provider that
// offers nowhere to register. Erroring is the point: silently falling back to
// the stored client_id would report a repair that did not happen.
func TestReregisterDynamicClient_NoRegistrationURL_Errors(t *testing.T) {
	as := newEvictableAuthServer(t)
	provider, store := newDCRProvider(t)
	ctx := context.Background()

	oauthConfigID, registeredClientID := dcrFixture(t, as, store, provider)

	stored, err := store.GetOauthConfigByID(ctx, oauthConfigID)
	require.NoError(t, err)
	stored.RegistrationURL = nil
	require.NoError(t, store.UpdateOauthConfig(ctx, stored))

	_, _, _, err = provider.ReregisterDynamicClient(ctx, oauthConfigID, dcrRedirectURI)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "registration endpoint")
	assert.ErrorIs(t, err, ErrDynamicRegistrationUnavailable,
		"a caller must be able to tell this from an internal failure: it is the one the admin can fix")
	assert.Equal(t, 1, as.registrationCount(), "no registration is possible without an endpoint")

	unchanged, err := store.GetOauthConfigByID(ctx, oauthConfigID)
	require.NoError(t, err)
	assert.Equal(t, registeredClientID, unchanged.GetResolvedClientID(),
		"a failed attempt must leave the stored client alone")
}

// TestReregisterDynamicClient_UnknownConfig_Errors guards the handler's
// argument: an oauth_config_id that resolves to nothing must not be reported
// as a successful repair.
func TestReregisterDynamicClient_UnknownConfig_Errors(t *testing.T) {
	provider, _ := newDCRProvider(t)
	_, _, _, err := provider.ReregisterDynamicClient(context.Background(), "does-not-exist", dcrRedirectURI)
	assert.ErrorIs(t, err, schemas.ErrOAuth2ConfigNotFound)
}

// TestReregisterDynamicClient_RegistersTheRedirectURIConsentWillPresent pins
// which redirect_uri a replacement client is registered with.
//
// An authorization server checks the redirect_uri on an authorize request
// against the ones registered for THAT client. The consent that follows a
// re-registration presents whatever URI the caller computed from the current
// external URL, not the one stored on the oauth_configs row at bootstrap. Those
// agree until the external URL changes, and then a replacement registered with
// the stored URI is rejected at the authorize step: the connection the admin
// came to repair stays broken, for a reason unrelated to why it broke.
func TestReregisterDynamicClient_RegistersTheRedirectURIConsentWillPresent(t *testing.T) {
	as := newEvictableAuthServer(t)
	provider, store := newDCRProvider(t)
	ctx := context.Background()

	// Bootstrapped while Bifrost was reached at dcrRedirectURI's host.
	oauthConfigID, _ := dcrFixture(t, as, store, provider)

	// Bifrost has since moved behind a real hostname.
	const movedRedirectURI = "https://bifrost.example.com/api/oauth/callback"

	_, newClientID, _, err := provider.ReregisterDynamicClient(ctx, oauthConfigID, movedRedirectURI)
	require.NoError(t, err)

	// What the consent following it presents, built the way the handler does.
	_, flowID, err := provider.InitiateUserOAuthFlow(ctx, oauthConfigID, dcrMCPClientID, movedRedirectURI, schemas.MCPAuthModeAdmin)
	require.NoError(t, err)
	authorizeURL, err := provider.BuildAdminUpstreamAuthorizeURL(ctx, flowID)
	require.NoError(t, err)
	parsed, err := url.Parse(authorizeURL)
	require.NoError(t, err)
	presented := parsed.Query().Get("redirect_uri")
	require.Equal(t, movedRedirectURI, presented, "fixture check: consent presents the current URI, not the stored one")
	require.Equal(t, newClientID, parsed.Query().Get("client_id"), "fixture check: consent runs against the replacement")

	assert.Contains(t, as.redirectURIsFor(newClientID), presented,
		"the replacement must be registered with the redirect_uri the consent behind it presents, or the provider rejects the authorize request")
}

// TestReregisterDynamicClient_EmptyRedirectURI_FallsBackToStored covers the
// caller with no request to derive a URL from. The stored URI is the best
// available answer there, and is what every registration used before the
// redirect URI became a parameter.
func TestReregisterDynamicClient_EmptyRedirectURI_FallsBackToStored(t *testing.T) {
	as := newEvictableAuthServer(t)
	provider, store := newDCRProvider(t)

	oauthConfigID, _ := dcrFixture(t, as, store, provider)

	_, newClientID, _, err := provider.ReregisterDynamicClient(context.Background(), oauthConfigID, "  ")
	require.NoError(t, err)
	assert.Equal(t, []string{dcrRedirectURI}, as.redirectURIsFor(newClientID))
}

// TestReregisterDynamicClient_ReportsWhetherAnythingRotated pins the result
// callers gate their own invalidation on. Rotation cascades every bound token
// to needs_reauth, so a caller that also evicts cached tokens and closes the
// live connection must do that exactly when the cascade ran, and must not tear
// down a working connection when the provider handed back the registration it
// already held.
func TestReregisterDynamicClient_ReportsWhetherAnythingRotated(t *testing.T) {
	t.Run("a different client_id rotates", func(t *testing.T) {
		as := newEvictableAuthServer(t)
		provider, store := newDCRProvider(t)
		oauthConfigID, _ := dcrFixture(t, as, store, provider)

		_, _, rotated, err := provider.ReregisterDynamicClient(context.Background(), oauthConfigID, dcrRedirectURI)
		require.NoError(t, err)
		assert.True(t, rotated)
	})

	t.Run("the client_id already held rotates nothing", func(t *testing.T) {
		as := newEvictableAuthServer(t)
		provider, store := newDCRProvider(t)
		ctx := context.Background()
		oauthConfigID, registeredClientID := dcrFixture(t, as, store, provider)
		as.reissueClient(registeredClientID)

		previousClientID, newClientID, rotated, err := provider.ReregisterDynamicClient(ctx, oauthConfigID, dcrRedirectURI)
		require.NoError(t, err)
		assert.False(t, rotated)
		assert.Equal(t, previousClientID, newClientID, "reports both ids equal rather than claiming a swap")

		token, err := store.GetOauthTokenByID(ctx, dcrTokenID)
		require.NoError(t, err)
		assert.Equal(t, "active", token.Status, "nothing rotated, so nothing may be cascaded")
	})
}

// TestReregisterDynamicClient_ClassifiesProviderRefusals pins which
// registration failures are the caller's to fix. A 4xx is the provider
// understanding the request and saying no; an HTTP caller of Bifrost should
// hear that as its own 4xx. A 5xx is the provider falling over, which no change
// to the request repairs, and must not be dressed up as a refusal.
func TestReregisterDynamicClient_ClassifiesProviderRefusals(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		wantRejected bool
	}{
		{name: "403 is a refusal", status: http.StatusForbidden, wantRejected: true},
		{name: "400 is a refusal", status: http.StatusBadRequest, wantRejected: true},
		{name: "503 is an outage, not a refusal", status: http.StatusServiceUnavailable, wantRejected: false},
		{name: "500 is an outage, not a refusal", status: http.StatusInternalServerError, wantRejected: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			as := newEvictableAuthServer(t)
			provider, store := newDCRProvider(t)
			ctx := context.Background()
			oauthConfigID, registeredClientID := dcrFixture(t, as, store, provider)
			as.refuseRegistrations(tc.status)

			_, _, rotated, err := provider.ReregisterDynamicClient(ctx, oauthConfigID, dcrRedirectURI)
			require.Error(t, err)
			assert.False(t, rotated)
			assert.Equal(t, tc.wantRejected, errors.Is(err, ErrDynamicRegistrationRejected))
			assert.NotErrorIs(t, err, ErrDynamicRegistrationUnavailable, "an endpoint that answered is not a missing endpoint")
			assert.Contains(t, err.Error(), fmt.Sprintf("status %d", tc.status), "the provider's own status must survive into the message")

			unchanged, err := store.GetOauthConfigByID(ctx, oauthConfigID)
			require.NoError(t, err)
			assert.Equal(t, registeredClientID, unchanged.GetResolvedClientID(), "a failed attempt must leave the stored client alone")
		})
	}
}
