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
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/network"
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
	db           *gorm.DB
	beforeRotate func() // runs as a rotation starts, standing in for a concurrent writer
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
// them differs, cascading every bound token to needs_reauth, and only while the
// stored client_id is still ExpectedClientID when that is set.
func (s *dcrConfigStore) RotateMCPOAuthConfig(ctx context.Context, existing *tables.TableOauthConfig, fields configstore.MCPOAuthConfigFields) (bool, error) {
	if existing == nil {
		return false, errors.New("oauth config is nil")
	}
	if !fields.DiffersFrom(existing) {
		return false, nil
	}
	if s.beforeRotate != nil {
		s.beforeRotate()
	}
	if fields.ExpectedClientID != nil {
		var matched int64
		if err := s.db.WithContext(ctx).Model(&tables.TableOauthConfig{}).Where("id = ? AND client_id = ?", existing.ID, *fields.ExpectedClientID).Count(&matched).Error; err != nil {
			return false, err
		}
		if matched == 0 {
			return false, configstore.ErrOAuthConfigChanged
		}
	}
	if fields.ExpectedIssuer != nil {
		stored, err := s.GetOauthConfigByID(ctx, existing.ID)
		if err != nil {
			return false, err
		}
		if stored == nil || stored.Issuer != *fields.ExpectedIssuer {
			return false, configstore.ErrOAuthConfigChanged
		}
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
	if fields.RedirectURI != "" {
		existing.RedirectURI = fields.RedirectURI
	}
	existing.Issuer, existing.IssParameterSupported, existing.DynamicallyRegistered = fields.Issuer, fields.IssParameterSupported, fields.DynamicallyRegistered
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

// BindOAuthConfigIssuer mirrors RDBConfigStore's: only the binding columns, only while the row matches.
func (s *dcrConfigStore) BindOAuthConfigIssuer(ctx context.Context, id, expectedIssuer, expectedTokenURL, issuer string, issParameterSupported bool) (bool, error) {
	result := s.db.WithContext(ctx).Model(&tables.TableOauthConfig{}).
		Where("id = ? AND COALESCE(issuer, '') = ? AND token_url = ?", id, expectedIssuer, expectedTokenURL).
		Updates(map[string]any{"issuer": issuer, "iss_parameter_supported": issParameterSupported})
	return result.RowsAffected > 0, result.Error
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

// RecordOAuthFlowIssuer mirrors RDBConfigStore's: only the issuer, only while the flow is pending with state.
func (s *dcrConfigStore) RecordOAuthFlowIssuer(ctx context.Context, id, state, issuer string, issParameterSupported bool) (bool, error) {
	result := s.db.WithContext(ctx).Model(&tables.TableMCPOauthFlow{}).
		Where("id = ? AND state = ? AND status = ?", id, state, "pending").
		Updates(map[string]any{"issuer": issuer, "iss_parameter_supported": issParameterSupported})
	return result.RowsAffected > 0, result.Error
}

func (s *dcrConfigStore) GetOauthUserSessionByState(ctx context.Context, state string) (*tables.TableMCPOauthFlow, error) {
	var flow tables.TableMCPOauthFlow
	if err := s.db.WithContext(ctx).Where("state = ?", state).First(&flow).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &flow, nil
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

func (s *dcrConfigStore) GetMCPClientConfigByID(ctx context.Context, id string) (*schemas.MCPClientConfig, error) {
	var client tables.TableMCPClient
	if err := s.db.WithContext(ctx).Where("client_id = ?", id).First(&client).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, configstore.ErrNotFound
		}
		return nil, err
	}
	return &schemas.MCPClientConfig{ID: client.ClientID, Name: client.Name, OauthConfigID: client.OauthConfigID, RequirePublicTarget: client.RequirePublicTarget}, nil
}

func (s *dcrConfigStore) GetMCPClientByOauthConfigID(ctx context.Context, oauthConfigID string) (*tables.TableMCPClient, error) {
	var client tables.TableMCPClient
	if err := s.db.WithContext(ctx).Where("oauth_config_id = ?", oauthConfigID).First(&client).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, configstore.ErrNotFound
		}
		return nil, err
	}
	return &client, nil
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

// useProductionOAuthDialer drops TestMain's unguarded dialer for the rest of
// the test, so OAuth requests go through the real destination policy, and
// installs allow (nil for none) as the private-target allowlist.
func useProductionOAuthDialer(t *testing.T, allow ...string) {
	t.Helper()
	prevDial := testDialContextOverride
	prevAllow := privateTargetAllowlist.Load()
	testDialContextOverride = nil
	t.Cleanup(func() {
		testDialContextOverride = prevDial
		SetPrivateTargetAllowlist(prevAllow)
	})
	if len(allow) == 0 {
		SetPrivateTargetAllowlist(nil)
		return
	}
	al, err := network.ValidatePrivateTargetAllowlist(allow)
	require.NoError(t, err)
	SetPrivateTargetAllowlist(al)
}

// TestOAuthLoopbackProviderFollowsPrivateTargetAllowlist pins the MCP OAuth
// destination policy end to end against a real loopback authorization server
// and the production dialer: an MCP client registered by an authenticated
// admin (RequirePublicTarget=false) reaches a private OAuth server once the
// operator allowlists it, for bootstrap registration, refresh and
// re-registration alike; without the allowlist, for a client registered with
// auth bypassed (RequirePublicTarget=true), or when the owning client cannot
// be identified, every hop stays public-only.
func TestOAuthLoopbackProviderFollowsPrivateTargetAllowlist(t *testing.T) {
	for _, tc := range []struct {
		name                string
		requirePublicTarget bool
		allow               []string
		unlinked            bool
		wantReachable       bool
	}{
		{name: "authenticated client, loopback allowlisted", allow: []string{"127.0.0.1"}, wantReachable: true},
		{name: "authenticated client, no allowlist"},
		{name: "authenticated client, other host allowlisted", allow: []string{"10.0.0.0/8"}},
		{name: "auth-bypassed client, loopback allowlisted", requirePublicTarget: true, allow: []string{"127.0.0.1"}},
		{name: "unidentifiable client, loopback allowlisted", unlinked: true, allow: []string{"127.0.0.1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			as := newEvictableAuthServer(t)
			provider, store := newDCRProvider(t)
			ctx := context.Background()
			oauthConfigID, _ := dcrFixture(t, as, store, provider)
			if tc.unlinked {
				require.NoError(t, store.db.Where("client_id = ?", dcrMCPClientID).Delete(&tables.TableMCPClient{}).Error)
				require.NoError(t, store.db.Model(&tables.TableMCPOauthToken{}).Where("id = ?", dcrTokenID).Update("mcp_client_id", "").Error)
			} else {
				require.NoError(t, store.db.Model(&tables.TableMCPClient{}).Where("client_id = ?", dcrMCPClientID).Update("require_public_target", tc.requirePublicTarget).Error)
			}
			useProductionOAuthDialer(t, tc.allow...)

			assertReach := func(what string, err error) {
				t.Helper()
				if tc.wantReachable {
					require.NoError(t, err, what)
					return
				}
				require.Error(t, err, what)
				require.Contains(t, err.Error(), "blocked connection to non-public address", what)
			}

			_, err := provider.InitiateOAuthFlow(ctx, &schemas.OAuth2Config{
				AuthorizeURL:        as.server.URL + "/authorize",
				TokenURL:            as.server.URL + "/token",
				RegistrationURL:     bifrost.Ptr(as.server.URL + "/register"),
				RedirectURI:         dcrRedirectURI,
				ServerURL:           as.server.URL,
				RequirePublicTarget: tc.requirePublicTarget || tc.unlinked,
			})
			assertReach("bootstrap dynamic registration", err)

			assertReach("refresh", provider.RefreshAccessToken(ctx, dcrTokenID))

			_, _, _, err = provider.ReregisterDynamicClient(ctx, oauthConfigID, dcrRedirectURI)
			assertReach("re-registration", err)
		})
	}
}

// TestInitiateOAuthFlowResourceFromDiscovery pins which resource indicator the
// authorize URL and the stored oauth config carry after discovery: never one a
// protected resource metadata document names for a different server, and an
// administrator's explicit resource always wins.
func TestInitiateOAuthFlowResourceFromDiscovery(t *testing.T) {
	otherAS := newOtherAuthServer(t)
	s := newResourceMetadataServer(t, func(s *resourceMetadataServer) {
		s.advertised = map[string]any{
			"resource":              "https://other-mcp.example/mcp",
			"authorization_servers": []string{otherAS.URL},
		}
	})

	cases := []struct {
		name         string
		resource     string
		wantResource string
	}{
		{name: "non-matching discovered resource is not sent", resource: "", wantResource: ""},
		{name: "explicit resource wins", resource: "https://admin-chosen.example/mcp", wantResource: "https://admin-chosen.example/mcp"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider, store := newDCRProvider(t)
			ctx := context.Background()
			initiation, err := provider.InitiateOAuthFlow(ctx, &schemas.OAuth2Config{
				ClientID:    schemas.NewSecretVar("client-id"),
				RedirectURI: dcrRedirectURI,
				ServerURL:   s.server.URL + "/mcp",
				Resource:    tc.resource,
			})
			require.NoError(t, err)

			authorizeURL, err := url.Parse(initiation.AuthorizeURL)
			require.NoError(t, err)
			require.Equal(t, s.server.URL+"/authorize", authorizeURL.Scheme+"://"+authorizeURL.Host+authorizeURL.Path)
			require.Equal(t, tc.wantResource, authorizeURL.Query().Get("resource"))

			stored, err := store.GetOauthConfigByID(ctx, initiation.OauthConfigID)
			require.NoError(t, err)
			require.NotNil(t, stored)
			require.Equal(t, tc.wantResource, stored.Resource)
		})
	}
}

// discoverableAuthServer is a discoverable MCP server + authorization server that counts DCR calls.
// authorizationServers, when set, are the authorization servers its protected resource metadata names.
type discoverableAuthServer struct {
	server               *httptest.Server
	registrations        int
	redirectURIs         []string // from the last registration
	mu                   sync.Mutex
	authorizationServers []string
	onDiscover           func() // runs while the authorization server metadata is served, standing in for a concurrent writer
}

// pointTo sets the authorization servers its protected resource metadata names, in order.
func (as *discoverableAuthServer) pointTo(issuers ...string) {
	as.mu.Lock()
	defer as.mu.Unlock()
	as.authorizationServers = issuers
}

func newDiscoverableAuthServer(t *testing.T, supportsCIMD bool) *discoverableAuthServer {
	t.Helper()
	as := &discoverableAuthServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+as.server.URL+`/.well-known/oauth-protected-resource"`)
		w.WriteHeader(http.StatusUnauthorized)
	})
	mux.HandleFunc("/.well-known/oauth-protected-resource", func(w http.ResponseWriter, r *http.Request) {
		as.mu.Lock()
		authorizationServers := as.authorizationServers
		as.mu.Unlock()
		if len(authorizationServers) == 0 {
			authorizationServers = []string{as.server.URL}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"resource": as.server.URL + "/mcp", "authorization_servers": authorizationServers})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		as.mu.Lock()
		onDiscover := as.onDiscover
		as.onDiscover = nil
		as.mu.Unlock()
		if onDiscover != nil {
			onDiscover()
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                as.server.URL,
			"authorization_endpoint":                as.server.URL + "/authorize",
			"token_endpoint":                        as.server.URL + "/token",
			"registration_endpoint":                 as.server.URL + "/register",
			"code_challenge_methods_supported":      []string{"S256"},
			"client_id_metadata_document_supported": supportsCIMD,

			"authorization_response_iss_parameter_supported": true,
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RedirectURIs []string `json:"redirect_uris"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		as.mu.Lock()
		as.registrations++
		as.redirectURIs = body.RedirectURIs
		n := as.registrations
		as.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"client_id": fmt.Sprintf("dyn-%d", n)})
	})
	as.server = httptest.NewServer(mux)
	t.Cleanup(as.server.Close)
	return as
}

// Client ID selection: configured client_id, else CIMD when supported and usable, else DCR.
func TestInitiateOAuthFlow_ChoosesClientIDMetadataDocument(t *testing.T) {
	const documentURL = "https://bifrost.example.com/api/oauth/client-metadata.json"
	cases := []struct {
		name         string
		supportsCIMD bool
		documentURL  string
		clientID     *schemas.SecretVar
		endpoints    string // "" = discovered, "own" = entered by hand, "other" = another server's, entered by hand, "other-token" = only another server's token endpoint entered
		wantClientID string // "" = a dynamically registered one
	}{
		{"supported", true, documentURL, nil, "", documentURL},
		{"not advertised", false, documentURL, nil, "", ""},
		{"document not served over https", true, "http://bifrost.example.com/api/oauth/client-metadata.json", nil, "", ""},
		{"document on a loopback host", true, "https://localhost:8080/api/oauth/client-metadata.json", nil, "", ""},
		{"no document", true, "", nil, "", ""},
		{"configured client_id", true, documentURL, schemas.NewSecretVar("preset"), "", "preset"},
		{"endpoints entered by hand, supported", true, documentURL, nil, "own", documentURL},
		{"endpoints entered by hand, not advertised", false, documentURL, nil, "own", ""},
		{"endpoints entered by hand for another authorization server", true, documentURL, nil, "other", ""},
		{"token endpoint entered by hand for another authorization server", true, documentURL, nil, "other-token", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			as := newDiscoverableAuthServer(t, tc.supportsCIMD)
			provider, _ := newDCRProvider(t)
			config := &schemas.OAuth2Config{
				ClientID:          tc.clientID,
				ServerURL:         as.server.URL + "/mcp",
				RedirectURI:       "https://bifrost.example.com/api/oauth/callback",
				ClientMetadataURL: tc.documentURL,
			}
			switch tc.endpoints {
			case "own":
				config.AuthorizeURL, config.TokenURL = as.server.URL+"/authorize", as.server.URL+"/token"
			case "other":
				config.AuthorizeURL, config.TokenURL = "https://other.example.com/authorize", "https://other.example.com/token"
			case "other-token":
				config.TokenURL = "https://other.example.com/token" // authorize_url is discovered
			}
			if tc.endpoints != "" && tc.endpoints != "other-token" {
				registrationURL := as.server.URL + "/register"
				config.RegistrationURL = &registrationURL
			}
			initiation, err := provider.InitiateOAuthFlow(context.Background(), config)
			require.NoError(t, err)
			authorize, err := url.Parse(initiation.AuthorizeURL)
			require.NoError(t, err)
			got := authorize.Query().Get("client_id")
			if tc.wantClientID == "" {
				assert.Equal(t, "dyn-1", got, "falls back to dynamic client registration")
				assert.Equal(t, 1, as.registrations)
				return
			}
			assert.Equal(t, tc.wantClientID, got)
			assert.Zero(t, as.registrations, "no dynamic registration")
		})
	}
}

// use_client_metadata_document registers with Bifrost's document whether or not the authorization
// server advertises support, including when its endpoints were entered by hand because discovery
// failed; it refuses a client_id alongside it and a document no authorization server could fetch.
func TestInitiateOAuthFlow_UseClientMetadataDocument(t *testing.T) {
	const documentURL = "https://bifrost.example.com/api/oauth/client-metadata.json"
	cases := []struct {
		name        string
		documentURL string
		clientID    *schemas.SecretVar
		byHand      bool // endpoints entered by hand for a server that cannot be discovered
		wantErr     error
	}{
		{"not advertised", documentURL, nil, false, nil},
		{"undiscoverable server, endpoints by hand", documentURL, nil, true, nil},
		{"with a client_id", documentURL, schemas.NewSecretVar("preset"), false, ErrClientMetadataDocumentWithClientID},
		{"document not served over https", "http://bifrost.example.com/api/oauth/client-metadata.json", nil, false, ErrClientMetadataDocumentUnavailable},
		{"document on a loopback host", "https://localhost:8080/api/oauth/client-metadata.json", nil, false, ErrClientMetadataDocumentUnavailable},
		{"no document", "", nil, false, ErrClientMetadataDocumentUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			as := newDiscoverableAuthServer(t, false)
			provider, store := newDCRProvider(t)
			config := &schemas.OAuth2Config{
				ClientID:                  tc.clientID,
				ClientSecret:              schemas.NewSecretVar("entered-secret"),
				ServerURL:                 as.server.URL + "/mcp",
				RedirectURI:               "https://bifrost.example.com/api/oauth/callback",
				ClientMetadataURL:         tc.documentURL,
				UseClientMetadataDocument: true,
			}
			if tc.byHand {
				config.ServerURL = "https://undiscoverable.example.com/mcp"
				config.AuthorizeURL, config.TokenURL = "https://undiscoverable.example.com/authorize", "https://undiscoverable.example.com/token"
			}
			initiation, err := provider.InitiateOAuthFlow(context.Background(), config)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				assert.Zero(t, as.registrations, "no dynamic registration")
				return
			}
			require.NoError(t, err)
			authorize, err := url.Parse(initiation.AuthorizeURL)
			require.NoError(t, err)
			assert.Equal(t, documentURL, authorize.Query().Get("client_id"))
			assert.Zero(t, as.registrations, "no dynamic registration")
			stored, err := store.GetOauthConfigByID(context.Background(), initiation.OauthConfigID)
			require.NoError(t, err)
			assert.False(t, stored.ClientSecret.IsSet(), "no secret belongs to a metadata document client")
		})
	}
}

// A Client ID Metadata Document client is public (its document declares no token endpoint auth), so a
// client_secret entered without a client_id is not kept for it.
func TestInitiateOAuthFlow_ClientIDMetadataDocumentDropsSuppliedSecret(t *testing.T) {
	const documentURL = "https://bifrost.example.com/api/oauth/client-metadata.json"
	as := newDiscoverableAuthServer(t, true)
	provider, store := newDCRProvider(t)
	initiation, err := provider.InitiateOAuthFlow(context.Background(), &schemas.OAuth2Config{
		ClientSecret:      schemas.NewSecretVar("entered-secret"),
		ServerURL:         as.server.URL + "/mcp",
		RedirectURI:       "https://bifrost.example.com/api/oauth/callback",
		ClientMetadataURL: documentURL,
	})
	require.NoError(t, err)
	stored, err := store.GetOauthConfigByID(context.Background(), initiation.OauthConfigID)
	require.NoError(t, err)
	assert.Equal(t, documentURL, stored.GetResolvedClientID())
	assert.False(t, stored.ClientSecret.IsSet(), "no secret belongs to a metadata document client")
}

// A metadata-document client_id is the URL of Bifrost's document, so it follows Bifrost's address: a
// sign-in under another base (the external URL changed, or none is set and another host is used)
// moves the stored client_id to that base's document, and tokens issued to the old one must sign in
// again. Under the same base nothing changes.
func TestSignIn_ClientIDMetadataDocumentFollowsBaseURL(t *testing.T) {
	const (
		oldBase = "https://bifrost.example.com"
		newBase = "https://gateway.example.com"
	)
	const central = "https://central.example.com/api/oauth/client-metadata.json"
	for _, tc := range []struct {
		name, configured, signInBase, wantClientID, wantStatus string
	}{
		{"base changed", "", newBase, newBase + "/api/oauth/client-metadata.json", "needs_reauth"},
		{"same base", "", oldBase, oldBase + "/api/oauth/client-metadata.json", "active"},
		// The authorization server cannot fetch a loopback or plain-http document, so the config stays.
		{"unusable base", "", "http://localhost:8080", oldBase + "/api/oauth/client-metadata.json", "active"},
		// Another host's document at the same path was configured by hand; it is not Bifrost's to move.
		{"configured document on another host", central, newBase, central, "active"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			as := newDiscoverableAuthServer(t, true)
			provider, store := newDCRProvider(t)
			provider.SetClientMetadataDocumentPaths("/api/oauth/callback", "/api/oauth/client-metadata.json")
			var rotated []string
			provider.SetCredentialsRotatedHook(func(_ context.Context, mcpClientID string) { rotated = append(rotated, mcpClientID) })
			ctx := context.Background()
			config := &schemas.OAuth2Config{
				ServerURL:         as.server.URL + "/mcp",
				RedirectURI:       oldBase + "/api/oauth/callback",
				ClientMetadataURL: oldBase + "/api/oauth/client-metadata.json",
			}
			if tc.configured != "" {
				config.ClientID = schemas.NewSecretVar(tc.configured)
			}
			initiation, err := provider.InitiateOAuthFlow(ctx, config)
			require.NoError(t, err)
			require.NoError(t, store.db.Create(&tables.TableMCPClient{ClientID: dcrMCPClientID, Name: "grafana", OauthConfigID: bifrost.Ptr(initiation.OauthConfigID)}).Error)
			require.NoError(t, store.db.Create(&tables.TableMCPOauthToken{
				ID: "cimd-token", AuthMode: "shared", MCPClientID: dcrMCPClientID, OauthConfigID: initiation.OauthConfigID,
				Status: "active", AccessToken: "at", TokenType: "Bearer",
			}).Error)

			_, flowID, err := provider.InitiateUserOAuthFlow(ctx, initiation.OauthConfigID, dcrMCPClientID, tc.signInBase+"/api/oauth/callback", schemas.MCPAuthModeAdmin)
			require.NoError(t, err)
			authorizeURL, err := provider.BuildAdminUpstreamAuthorizeURL(ctx, flowID)
			require.NoError(t, err)

			assert.Equal(t, tc.wantClientID, authorizeURLClientID(t, authorizeURL))
			stored, err := store.GetOauthConfigByID(ctx, initiation.OauthConfigID)
			require.NoError(t, err)
			assert.Equal(t, tc.wantClientID, stored.GetResolvedClientID())
			assert.Equal(t, as.server.URL, stored.Issuer, "moving the document keeps the issuer binding")
			token, err := store.GetOauthTokenByID(ctx, "cimd-token")
			require.NoError(t, err)
			assert.Equal(t, tc.wantStatus, token.Status, "tokens issued to the old client_id cannot be refreshed under the new one")
			if tc.wantStatus == "needs_reauth" {
				assert.Equal(t, []string{dcrMCPClientID}, rotated, "the client's cached tokens and connection are dropped once")
			} else {
				assert.Empty(t, rotated, "nothing rotated, nothing dropped")
			}
		})
	}
}

// Two sign-ins that loaded the config before its client_id followed Bifrost's new address move it once:
// the one that lost the race neither moves it again nor signs out the token the winner's sign-in got.
func TestSignIn_StaleClientIDMetadataDocumentMoveIsNotRepeated(t *testing.T) {
	const (
		oldBase = "https://bifrost.example.com"
		newBase = "https://gateway.example.com"
	)
	as := newDiscoverableAuthServer(t, true)
	provider, store := newDCRProvider(t)
	provider.SetClientMetadataDocumentPaths("/api/oauth/callback", "/api/oauth/client-metadata.json")
	var rotated []string
	provider.SetCredentialsRotatedHook(func(_ context.Context, mcpClientID string) { rotated = append(rotated, mcpClientID) })
	ctx := context.Background()
	initiation, err := provider.InitiateOAuthFlow(ctx, &schemas.OAuth2Config{
		ServerURL:         as.server.URL + "/mcp",
		RedirectURI:       oldBase + "/api/oauth/callback",
		ClientMetadataURL: oldBase + "/api/oauth/client-metadata.json",
	})
	require.NoError(t, err)
	require.NoError(t, store.db.Create(&tables.TableMCPClient{ClientID: dcrMCPClientID, Name: "grafana", OauthConfigID: bifrost.Ptr(initiation.OauthConfigID)}).Error)
	staleA, err := store.GetOauthConfigByID(ctx, initiation.OauthConfigID)
	require.NoError(t, err)
	staleB, err := store.GetOauthConfigByID(ctx, initiation.OauthConfigID)
	require.NoError(t, err)

	moved, err := provider.followClientMetadataDocument(ctx, staleA, dcrMCPClientID, newBase+"/api/oauth/callback")
	require.NoError(t, err)
	require.Equal(t, newBase+"/api/oauth/client-metadata.json", moved.GetResolvedClientID())
	// The winner's sign-in completes under the new client_id.
	require.NoError(t, store.db.Create(&tables.TableMCPOauthToken{
		ID: "fresh-token", AuthMode: "shared", MCPClientID: dcrMCPClientID, OauthConfigID: initiation.OauthConfigID,
		Status: "active", AccessToken: "at", TokenType: "Bearer",
	}).Error)

	again, err := provider.followClientMetadataDocument(ctx, staleB, dcrMCPClientID, newBase+"/api/oauth/callback")
	require.NoError(t, err)
	assert.Equal(t, newBase+"/api/oauth/client-metadata.json", again.GetResolvedClientID())
	token, err := store.GetOauthTokenByID(ctx, "fresh-token")
	require.NoError(t, err)
	assert.Equal(t, "active", token.Status, "the stale sign-in must not sign out the token issued after the move")
	assert.Equal(t, []string{dcrMCPClientID}, rotated, "the client's tokens and connection are dropped once")
}

// The client_id keeps following Bifrost's address across moves, not only the first one.
func TestSignIn_ClientIDMetadataDocumentFollowsEveryMove(t *testing.T) {
	as := newDiscoverableAuthServer(t, true)
	provider, store := newDCRProvider(t)
	provider.SetClientMetadataDocumentPaths("/api/oauth/callback", "/api/oauth/client-metadata.json")
	ctx := context.Background()
	initiation, err := provider.InitiateOAuthFlow(ctx, &schemas.OAuth2Config{
		ServerURL:         as.server.URL + "/mcp",
		RedirectURI:       "https://bifrost.example.com/api/oauth/callback",
		ClientMetadataURL: "https://bifrost.example.com/api/oauth/client-metadata.json",
	})
	require.NoError(t, err)
	require.NoError(t, store.db.Create(&tables.TableMCPClient{ClientID: dcrMCPClientID, Name: "grafana", OauthConfigID: bifrost.Ptr(initiation.OauthConfigID)}).Error)
	for _, base := range []string{"https://gateway.example.com", "https://edge.example.com"} {
		_, flowID, err := provider.InitiateUserOAuthFlow(ctx, initiation.OauthConfigID, dcrMCPClientID, base+"/api/oauth/callback", schemas.MCPAuthModeAdmin)
		require.NoError(t, err)
		authorizeURL, err := provider.BuildAdminUpstreamAuthorizeURL(ctx, flowID)
		require.NoError(t, err)
		assert.Equal(t, base+"/api/oauth/client-metadata.json", authorizeURLClientID(t, authorizeURL))
	}
}

// The authorization server's issuer is recorded on the oauth config and on the flow, from discovery
// or, for hand-entered endpoints, from a discovery that names the same token endpoint.
func TestInitiateOAuthFlow_RecordsIssuer(t *testing.T) {
	for _, tc := range []struct {
		name       string
		endpoints  string // "" = discovered, "own" = entered by hand, "other" = another server's
		wantIssuer bool
	}{
		{"discovered", "", true},
		{"endpoints entered by hand", "own", true},
		{"endpoints entered by hand for another authorization server", "other", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			as := newDiscoverableAuthServer(t, false)
			provider, store := newDCRProvider(t)
			config := &schemas.OAuth2Config{ServerURL: as.server.URL + "/mcp", RedirectURI: dcrRedirectURI}
			switch tc.endpoints {
			case "own":
				config.AuthorizeURL, config.TokenURL = as.server.URL+"/authorize", as.server.URL+"/token"
			case "other":
				config.AuthorizeURL, config.TokenURL = "https://other.example.com/authorize", "https://other.example.com/token"
			}
			config.RegistrationURL = bifrost.Ptr(as.server.URL + "/register")
			initiation, err := provider.InitiateOAuthFlow(context.Background(), config)
			require.NoError(t, err)

			stored, err := store.GetOauthConfigByID(context.Background(), initiation.OauthConfigID)
			require.NoError(t, err)
			flow, err := store.GetOauthUserSessionByState(context.Background(), initiation.State)
			require.NoError(t, err)
			want := ""
			if tc.wantIssuer {
				want = as.server.URL
			}
			assert.Equal(t, want, stored.Issuer)
			assert.Equal(t, tc.wantIssuer, stored.IssParameterSupported)
			assert.True(t, stored.DynamicallyRegistered)
			assert.Equal(t, want, flow.Issuer, "the flow records the issuer its response must name")
			assert.Equal(t, tc.wantIssuer, flow.IssParameterSupported)
		})
	}
}

// When the MCP server moves to another authorization server, the next sign-in moves a Client ID
// Metadata Document id with it, registers dynamically registered credentials again there, and
// refuses pre-registered ones. Creating the flow, which a tool call does, checks nothing.
func TestSignIn_AuthorizationServerChanged(t *testing.T) {
	const documentURL = "https://bifrost.example.com/api/oauth/client-metadata.json"
	for _, tc := range []struct {
		name         string
		clientID     *schemas.SecretVar
		documentURL  string
		wantErr      bool
		wantClientID string // "" = registered with the new server
	}{
		{"dynamically registered", nil, "", false, ""},
		{"client id metadata document", nil, documentURL, false, documentURL},
		{"pre-registered", schemas.NewSecretVar("preset"), "", true, "preset"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mcp := newDiscoverableAuthServer(t, true)
			moved := newDiscoverableAuthServer(t, true)
			provider, store := newDCRProvider(t)
			ctx := context.Background()
			initiation, err := provider.InitiateOAuthFlow(ctx, &schemas.OAuth2Config{
				ClientID: tc.clientID, ClientMetadataURL: tc.documentURL,
				ServerURL: mcp.server.URL + "/mcp", RedirectURI: dcrRedirectURI,
			})
			require.NoError(t, err)
			before, err := store.GetOauthConfigByID(ctx, initiation.OauthConfigID)
			require.NoError(t, err)
			require.Equal(t, mcp.server.URL, before.Issuer)

			var changed []string
			provider.SetCredentialsRotatedHook(func(_ context.Context, mcpClientID string) { changed = append(changed, mcpClientID) })
			mcp.pointTo(moved.server.URL)
			_, flowID, err := provider.InitiateUserOAuthFlow(ctx, initiation.OauthConfigID, dcrMCPClientID, dcrRedirectURI, schemas.MCPAuthModeAdmin)
			require.NoError(t, err)
			untouched, err := store.GetOauthConfigByID(ctx, initiation.OauthConfigID)
			require.NoError(t, err)
			assert.Equal(t, mcp.server.URL, untouched.Issuer, "creating the flow does not run discovery")

			authorizeURL, err := provider.BuildAdminUpstreamAuthorizeURL(ctx, flowID)
			after, loadErr := store.GetOauthConfigByID(ctx, initiation.OauthConfigID)
			require.NoError(t, loadErr)
			if tc.wantErr {
				require.ErrorIs(t, err, ErrAuthorizationServerChanged)
				assert.Equal(t, mcp.server.URL, after.Issuer, "nothing is rebound")
				assert.Equal(t, tc.wantClientID, after.GetResolvedClientID())
				assert.Empty(t, changed, "a refused change drops nothing")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, moved.server.URL, after.Issuer)
			assert.Equal(t, []string{dcrMCPClientID}, changed, "the client's cached tokens and connection are dropped once")
			assert.Equal(t, moved.server.URL+"/token", after.TokenURL, "endpoints follow the new server")
			if tc.wantClientID == "" {
				assert.Equal(t, 1, moved.registrations, "registered again with the new server")
				assert.Equal(t, "dyn-1", after.GetResolvedClientID())
			} else {
				assert.Zero(t, moved.registrations)
				assert.Equal(t, tc.wantClientID, after.GetResolvedClientID())
			}
			assert.True(t, strings.HasPrefix(authorizeURL, moved.server.URL+"/authorize"), authorizeURL)
			flow, err := store.GetOauthFlowByID(ctx, flowID)
			require.NoError(t, err)
			assert.Equal(t, moved.server.URL, flow.Issuer, "the flow expects the new server's iss")
		})
	}
}

// The documented recovery for hand-entered credentials after an authorization server change: install
// credentials registered with the new server (and its endpoints, when they differ), then reauthorize.
func TestSignIn_ReplacementCredentialsRecoverFromServerChange(t *testing.T) {
	for _, tc := range []struct {
		name         string
		sameEndpoint bool
	}{
		{"new server at new endpoints", false},
		{"issuer renamed at the same endpoints", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := newDiscoverableAuthServer(t, false)
			current := newDiscoverableAuthServer(t, false)
			provider, store := newDCRProvider(t)
			ctx := context.Background()
			stored := &tables.TableOauthConfig{
				ID: "preset", ClientID: schemas.NewSecretVar("old-client"), ClientSecret: schemas.NewSecretVar("old-secret"),
				AuthorizeURL: old.server.URL + "/authorize", TokenURL: old.server.URL + "/token", Issuer: old.server.URL,
				RedirectURI: dcrRedirectURI, ServerURL: current.server.URL + "/mcp", Status: "authorized",
			}
			if tc.sameEndpoint {
				// The server now names itself differently behind the endpoints the config already has.
				stored.AuthorizeURL, stored.TokenURL, stored.Issuer = current.server.URL+"/authorize", current.server.URL+"/token", "https://renamed.example.com"
			}
			require.NoError(t, store.db.Create(stored).Error)
			_, err := reauthorize(t, provider, "preset")
			require.ErrorIs(t, err, ErrAuthorizationServerChanged, "fixture check: the change is refused first")

			existing, err := store.GetOauthConfigByID(ctx, "preset")
			require.NoError(t, err)
			fields := configstore.MCPOAuthConfigFields{
				ClientID: schemas.NewSecretVar("new-client"), ClientSecret: schemas.NewSecretVar("new-secret"),
				AuthorizeURL: current.server.URL + "/authorize", TokenURL: current.server.URL + "/token",
			}
			fields.KeepBinding(existing)
			_, err = store.RotateMCPOAuthConfig(ctx, existing, fields)
			require.NoError(t, err)

			authorizeURL, err := reauthorize(t, provider, "preset")
			require.NoError(t, err, "replacement credentials registered with the current server sign in")
			assert.Equal(t, "new-client", authorizeURLClientID(t, authorizeURL))
			after, err := store.GetOauthConfigByID(ctx, "preset")
			require.NoError(t, err)
			assert.Equal(t, current.server.URL, after.Issuer, "bound to the current server")
		})
	}
}

// A code issued by the authorization server a flow started at is never posted to another one: when the
// config was rebound while the flow was open, completing it is refused before the token exchange.
func TestCompleteFlow_RefusesCodeAfterAuthorizationServerChange(t *testing.T) {
	for _, mode := range []schemas.MCPAuthMode{schemas.MCPAuthModeAdmin, schemas.MCPAuthModeSession} {
		t.Run(string(mode), func(t *testing.T) {
			var hits atomic.Int64
			tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				hits.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "stolen", "token_type": "Bearer"})
			}))
			t.Cleanup(tokenServer.Close)
			store := &reauthStore{testConfigStore: newTestConfigStore()}
			store.oauthConfigs["cfg-1"] = &tables.TableOauthConfig{
				ID: "cfg-1", ClientID: schemas.NewSecretVar("client-id"), TokenURL: tokenServer.URL + "/token",
				RedirectURI: "http://localhost/callback", Issuer: "https://new.example.com",
			}
			store.flow = &tables.TableMCPOauthFlow{
				ID: "flow-1", MCPClientID: "mcp-1", OauthConfigID: "cfg-1", State: "state-1", CodeVerifier: "verifier",
				SessionID: "sess-1", FlowMode: string(mode), Status: "claiming", ExpiresAt: time.Now().Add(10 * time.Minute),
				Issuer: "https://old.example.com",
			}
			provider := NewOAuth2Provider(store, bifrost.NewDefaultLogger(schemas.LogLevelError))
			var err error
			if mode == schemas.MCPAuthModeAdmin {
				err = provider.CompleteOAuthFlow(context.Background(), "state-1", "code-1")
			} else {
				_, err = provider.CompleteUserOAuthFlow(context.Background(), "state-1", "code-1")
			}
			require.ErrorIs(t, err, ErrAuthorizationServerChanged)
			assert.Zero(t, hits.Load(), "the code is not sent to the new server's token endpoint")
		})
	}
}

// An MCP server that advertises several authorization servers has not moved while the one the config is
// bound to is still listed, even when another listed server answers discovery first.
func TestSignIn_BoundServerStillAdvertisedIsNotAChange(t *testing.T) {
	for _, tc := range []struct {
		name    string
		dynamic bool
	}{
		{"pre-registered", false},
		{"dynamically registered", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first := newDiscoverableAuthServer(t, false)
			bound := newDiscoverableAuthServer(t, false)
			first.pointTo(first.server.URL, bound.server.URL)
			provider, store := newDCRProvider(t)
			var rotated []string
			provider.SetCredentialsRotatedHook(func(_ context.Context, mcpClientID string) { rotated = append(rotated, mcpClientID) })
			require.NoError(t, store.db.Create(&tables.TableOauthConfig{
				ID: "multi", ClientID: schemas.NewSecretVar("bound-client"), AuthorizeURL: bound.server.URL + "/authorize", TokenURL: bound.server.URL + "/token",
				RegistrationURL: bifrost.Ptr(bound.server.URL + "/register"), Issuer: bound.server.URL, IssParameterSupported: true, DynamicallyRegistered: tc.dynamic,
				RedirectURI: dcrRedirectURI, ServerURL: first.server.URL + "/mcp", Status: "authorized",
			}).Error)

			authorizeURL, err := reauthorize(t, provider, "multi")
			require.NoError(t, err, "the bound server is still advertised")
			assert.True(t, strings.HasPrefix(authorizeURL, bound.server.URL+"/authorize"), authorizeURL)
			assert.Equal(t, "bound-client", authorizeURLClientID(t, authorizeURL))
			stored, err := store.GetOauthConfigByID(context.Background(), "multi")
			require.NoError(t, err)
			assert.Equal(t, bound.server.URL, stored.Issuer)
			assert.Zero(t, first.registrations, "no registration with the other advertised server")
			assert.Empty(t, rotated, "nothing is signed out")
		})
	}
}

// Registering again with a new authorization server uses the callback the sign-in will present, not
// the one stored when the config was created: the external URL may have changed since.
func TestSignIn_AuthorizationServerChanged_RegistersTheFlowsRedirectURI(t *testing.T) {
	const currentRedirectURI = "https://gateway.example.com/api/oauth/callback"
	mcp := newDiscoverableAuthServer(t, false)
	moved := newDiscoverableAuthServer(t, false)
	provider, _ := newDCRProvider(t)
	ctx := context.Background()
	initiation, err := provider.InitiateOAuthFlow(ctx, &schemas.OAuth2Config{ServerURL: mcp.server.URL + "/mcp", RedirectURI: dcrRedirectURI})
	require.NoError(t, err)

	mcp.pointTo(moved.server.URL)
	_, flowID, err := provider.InitiateUserOAuthFlow(ctx, initiation.OauthConfigID, dcrMCPClientID, currentRedirectURI, schemas.MCPAuthModeAdmin)
	require.NoError(t, err)
	_, err = provider.BuildAdminUpstreamAuthorizeURL(ctx, flowID)
	require.NoError(t, err)
	require.Equal(t, 1, moved.registrations)
	assert.Equal(t, []string{currentRedirectURI}, moved.redirectURIs)
}

// Two replicas moving one config at once: the one that stores second keeps the first one's
// registration, so every sign-in uses one client_id, and only the winner drops cached state.
func TestSignIn_AuthorizationServerChanged_LosesToConcurrentRebind(t *testing.T) {
	mcp := newDiscoverableAuthServer(t, false)
	moved := newDiscoverableAuthServer(t, false)
	provider, store := newDCRProvider(t)
	ctx := context.Background()
	initiation, err := provider.InitiateOAuthFlow(ctx, &schemas.OAuth2Config{ServerURL: mcp.server.URL + "/mcp", RedirectURI: dcrRedirectURI})
	require.NoError(t, err)

	var changed []string
	provider.SetCredentialsRotatedHook(func(_ context.Context, mcpClientID string) { changed = append(changed, mcpClientID) })
	store.beforeRotate = func() {
		store.beforeRotate = nil
		winner, err := store.GetOauthConfigByID(ctx, initiation.OauthConfigID)
		require.NoError(t, err)
		winner.ClientID, winner.Issuer = schemas.NewSecretVar("winner-client"), moved.server.URL
		winner.AuthorizeURL, winner.TokenURL = moved.server.URL+"/authorize", moved.server.URL+"/token"
		require.NoError(t, store.UpdateOauthConfig(ctx, winner))
	}
	mcp.pointTo(moved.server.URL)
	_, flowID, err := provider.InitiateUserOAuthFlow(ctx, initiation.OauthConfigID, dcrMCPClientID, dcrRedirectURI, schemas.MCPAuthModeAdmin)
	require.NoError(t, err)
	authorizeURL, err := provider.BuildAdminUpstreamAuthorizeURL(ctx, flowID)
	require.NoError(t, err)

	assert.Equal(t, "winner-client", authorizeURLClientID(t, authorizeURL))
	stored, err := store.GetOauthConfigByID(ctx, initiation.OauthConfigID)
	require.NoError(t, err)
	assert.Equal(t, "winner-client", stored.GetResolvedClientID())
	assert.Empty(t, changed, "the winning replica drops the cached state")
}

// A credential edit that lands while a sign-in checks its authorization server is neither undone by the
// check's write nor ignored by the sign-in: only the binding is written, and the sign-in uses the
// stored config as it is after the check, whether or not the check wrote anything.
func TestSignIn_CredentialEditDuringCheckIsKept(t *testing.T) {
	for _, tc := range []struct {
		name   string
		issuer string // "" = unbound, so the check adopts the issuer; else already bound, so it writes nothing
	}{
		{"issuer adopted", ""},
		{"nothing to write", "bound"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			as := newDiscoverableAuthServer(t, false)
			provider, store := newDCRProvider(t)
			ctx := context.Background()
			issuer := ""
			if tc.issuer != "" {
				issuer = as.server.URL
			}
			require.NoError(t, store.db.Create(&tables.TableOauthConfig{
				ID: "edited", ClientID: schemas.NewSecretVar("preset"), AuthorizeURL: as.server.URL + "/authorize", TokenURL: as.server.URL + "/token",
				RedirectURI: dcrRedirectURI, ServerURL: as.server.URL + "/mcp", Status: "authorized", Issuer: issuer, IssParameterSupported: issuer != "",
			}).Error)
			as.onDiscover = func() {
				edited, err := store.GetOauthConfigByID(ctx, "edited")
				require.NoError(t, err)
				edited.ClientID = schemas.NewSecretVar("entered-by-admin")
				require.NoError(t, store.UpdateOauthConfig(ctx, edited))
			}

			authorizeURL, err := reauthorize(t, provider, "edited")
			require.NoError(t, err)
			stored, err := store.GetOauthConfigByID(ctx, "edited")
			require.NoError(t, err)
			assert.Equal(t, "entered-by-admin", stored.GetResolvedClientID(), "the check must not write back the credentials it read")
			assert.Equal(t, as.server.URL, stored.Issuer)
			assert.Equal(t, "entered-by-admin", authorizeURLClientID(t, authorizeURL), "the sign-in must use the stored credentials")
		})
	}
}

// A config stored before issuers were recorded adopts the discovered one at its next sign-in, but
// only for the authorization server its token endpoint belongs to.
func TestSignIn_AdoptsIssuerForUnboundConfig(t *testing.T) {
	for _, tc := range []struct {
		name       string
		tokenURL   string // "" = the discovered server's
		wantIssuer bool
	}{
		{"same authorization server", "", true},
		{"another token endpoint", "https://other.example.com/token", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			as := newDiscoverableAuthServer(t, false)
			provider, store := newDCRProvider(t)
			provider.SetCredentialsRotatedHook(func(context.Context, string) { t.Error("adopting an issuer is not a change of authorization server") })
			ctx := context.Background()
			tokenURL := tc.tokenURL
			if tokenURL == "" {
				tokenURL = as.server.URL + "/token"
			}
			require.NoError(t, store.db.Create(&tables.TableOauthConfig{
				ID: "legacy", ClientID: schemas.NewSecretVar("preset"), AuthorizeURL: as.server.URL + "/authorize", TokenURL: tokenURL,
				RedirectURI: dcrRedirectURI, ServerURL: as.server.URL + "/mcp", Status: "authorized",
			}).Error)

			_, err := reauthorize(t, provider, "legacy")
			require.NoError(t, err)
			stored, err := store.GetOauthConfigByID(ctx, "legacy")
			require.NoError(t, err)
			if tc.wantIssuer {
				assert.Equal(t, as.server.URL, stored.Issuer)
				assert.True(t, stored.IssParameterSupported)
			} else {
				assert.Empty(t, stored.Issuer)
			}
			assert.Equal(t, "preset", stored.GetResolvedClientID(), "adopting an issuer never rotates credentials")
		})
	}
}

// A sign-in renewed while the first one discovers its authorization server keeps the renewed state and
// verifier; the first one is refused as no longer pending.
func TestSignIn_RenewedDuringDiscoveryKeepsNewState(t *testing.T) {
	as := newDiscoverableAuthServer(t, false)
	provider, store := newDCRProvider(t)
	ctx := context.Background()
	require.NoError(t, store.db.Create(&tables.TableOauthConfig{
		ID: "legacy", ClientID: schemas.NewSecretVar("preset"), AuthorizeURL: as.server.URL + "/authorize", TokenURL: as.server.URL + "/token",
		RedirectURI: dcrRedirectURI, ServerURL: as.server.URL + "/mcp", Status: "authorized",
	}).Error)
	_, flowID, err := provider.InitiateUserOAuthFlow(ctx, "legacy", dcrMCPClientID, dcrRedirectURI, schemas.MCPAuthModeAdmin)
	require.NoError(t, err)

	var renewed tables.TableMCPOauthFlow
	as.onDiscover = func() {
		_, renewedID, err := provider.InitiateUserOAuthFlow(ctx, "legacy", dcrMCPClientID, dcrRedirectURI, schemas.MCPAuthModeAdmin)
		require.NoError(t, err)
		require.Equal(t, flowID, renewedID, "fixture check: the pending row is renewed in place")
		require.NoError(t, store.db.Where("id = ?", flowID).First(&renewed).Error)
	}
	_, err = provider.BuildAdminUpstreamAuthorizeURL(ctx, flowID)
	require.ErrorIs(t, err, schemas.ErrOAuth2FlowNotPending)

	var stored tables.TableMCPOauthFlow
	require.NoError(t, store.db.Where("id = ?", flowID).First(&stored).Error)
	require.NotEmpty(t, renewed.State)
	assert.Equal(t, renewed.State, stored.State, "the renewed state survives")
	assert.Equal(t, renewed.CodeVerifier, stored.CodeVerifier)
}

// A config deleted while a sign-in discovers its authorization server ends that sign-in with not found.
func TestSignIn_ConfigDeletedDuringDiscovery(t *testing.T) {
	as := newDiscoverableAuthServer(t, false)
	provider, store := newDCRProvider(t)
	ctx := context.Background()
	require.NoError(t, store.db.Create(&tables.TableOauthConfig{
		ID: "legacy", ClientID: schemas.NewSecretVar("preset"), AuthorizeURL: as.server.URL + "/authorize", TokenURL: as.server.URL + "/token",
		RedirectURI: dcrRedirectURI, ServerURL: as.server.URL + "/mcp", Status: "authorized",
	}).Error)
	_, flowID, err := provider.InitiateUserOAuthFlow(ctx, "legacy", dcrMCPClientID, dcrRedirectURI, schemas.MCPAuthModeAdmin)
	require.NoError(t, err)

	as.onDiscover = func() { require.NoError(t, store.db.Delete(&tables.TableOauthConfig{}, "id = ?", "legacy").Error) }
	_, err = provider.BuildAdminUpstreamAuthorizeURL(ctx, flowID)
	require.ErrorIs(t, err, schemas.ErrOAuth2ConfigNotFound)
}
