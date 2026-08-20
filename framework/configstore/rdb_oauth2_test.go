package configstore

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupOAuth2TestStore extends the base in-memory store with the OAuth2 issuance
// tables, which are not part of the base migration set.
func setupOAuth2TestStore(t *testing.T) *RDBConfigStore {
	t.Helper()
	s := setupRDBTestStore(t)
	require.NoError(t, s.DB().AutoMigrate(
		&tables.TableOAuth2Client{},
		&tables.TableOAuth2AuthorizeRequest{},
		&tables.TableOAuth2RefreshToken{},
	))
	return s
}

// seedAuthorizeRequest inserts a request in the given status with a future expiry.
func seedAuthorizeRequest(t *testing.T, s *RDBConfigStore, id string, status tables.OAuth2AuthorizeRequestStatus, codeHash *string, expires time.Time) {
	t.Helper()
	req := &tables.TableOAuth2AuthorizeRequest{
		ID:                  id,
		ClientID:            "client-1",
		RedirectURI:         "http://127.0.0.1/cb",
		State:               "state",
		Scope:               "mcp",
		Resource:            "https://bifrost.test/mcp",
		CodeChallenge:       "challenge",
		CodeChallengeMethod: "S256",
		Status:              status,
		CodeHash:            codeHash,
		ExpiresAt:           expires,
		CreatedAt:           time.Now(),
		UpdatedAt:           time.Now(),
	}
	require.NoError(t, s.CreateOAuth2AuthorizeRequest(context.Background(), req))
}

// makeRefreshToken builds a refresh-token row with sensible defaults.
func makeRefreshToken(id, familyID, clientID, hash string) *tables.TableOAuth2RefreshToken {
	return &tables.TableOAuth2RefreshToken{
		ID:        id,
		TokenHash: hash,
		FamilyID:  familyID,
		ClientID:  clientID,
		BfMode:    "vk",
		BfSub:     "vk-1",
		Scope:     "mcp",
		Resource:  "https://bifrost.test/mcp",
		CreatedAt: time.Now(),
	}
}

// seedExpiringTokenFixtures installs the token/config/client helpers shared by
// the GetExpiringOauthTokens tests. Every token is created already-expired so
// only the token-status/client conditions decide whether it is selected.
// Tokens link to their owning config via OauthConfigID — the replacement for
// the retired TableOauthConfig.TokenID FK shortcut.
func seedExpiringTokenFixtures(t *testing.T, s *RDBConfigStore) (mkToken func(id, oauthConfigID, status string), mkConfig func(id string), mkClient func(name, oauthConfigID string, disabled bool)) {
	t.Helper()
	require.NoError(t, s.DB().AutoMigrate(&tables.TableOauthConfig{}, &tables.TableMCPOauthToken{}, &tables.TableMCPClient{}))
	past := time.Now().Add(-time.Hour)

	mkToken = func(id, oauthConfigID, status string) {
		require.NoError(t, s.DB().Create(&tables.TableMCPOauthToken{
			ID: id, AuthMode: "shared", OauthConfigID: oauthConfigID, Status: status,
			AccessToken: "at-" + id, TokenType: "Bearer",
			ExpiresAt: &past, CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}).Error)
	}
	mkConfig = func(id string) {
		require.NoError(t, s.DB().Create(&tables.TableOauthConfig{
			ID: id, RedirectURI: "http://127.0.0.1/cb", Status: "authorized",
			CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}).Error)
	}
	mkClient = func(name, oauthConfigID string, disabled bool) {
		require.NoError(t, s.DB().Create(&tables.TableMCPClient{
			ClientID: "cid-" + name, Name: name, ConnectionType: "http",
			AuthType: "oauth", OauthConfigID: &oauthConfigID, Disabled: disabled,
			CreatedAt: time.Now(), UpdatedAt: time.Now(),
		}).Error)
	}
	return mkToken, mkConfig, mkClient
}

func expiringTokenIDs(t *testing.T, s *RDBConfigStore) map[string]bool {
	t.Helper()
	got, err := s.GetExpiringOauthTokens(context.Background(), time.Now().Add(time.Minute), []string{"shared"})
	require.NoError(t, err)
	ids := make(map[string]bool, len(got))
	for _, tk := range got {
		ids[tk.ID] = true
	}
	return ids
}

// TestGetExpiringOauthTokens_ExcludesNonActiveTokens verifies the refresh
// worker query skips tokens whose own status is already 'needs_reauth', so a
// permanently-dead grant is not retried — and re-logged — on every tick. Each
// config gets an enabled MCP client so token status is the only deciding
// condition.
func TestGetExpiringOauthTokens_ExcludesNonActiveTokens(t *testing.T) {
	s := setupRDBTestStore(t)
	mkToken, mkConfig, mkClient := seedExpiringTokenFixtures(t, s)

	mkConfig("cfg-live")
	mkToken("tok-live", "cfg-live", "active")
	mkClient("client-live", "cfg-live", false)

	mkConfig("cfg-needs-reauth")
	mkToken("tok-needs-reauth", "cfg-needs-reauth", "needs_reauth")
	mkClient("client-needs-reauth", "cfg-needs-reauth", false)

	ids := expiringTokenIDs(t, s)
	assert.True(t, ids["tok-live"], "active token with an enabled client should be refreshed")
	assert.False(t, ids["tok-needs-reauth"], "token already marked needs_reauth must be excluded")
}

// TestGetExpiringOauthTokens_FiltersByAuthMode verifies the caller-supplied
// authModes slice, not a hardcoded "shared" clause, decides which token
// holder types come back. With authModes=["shared"] (TokenRefreshWorker's
// default), a same-config, same-expiry "user"-mode row must not be picked up
// — the coverage the old hardcoded `auth_mode = 'shared'` filter gave for
// free, now expressed as an explicit parameter instead.
func TestGetExpiringOauthTokens_FiltersByAuthMode(t *testing.T) {
	s := setupRDBTestStore(t)
	mkToken, mkConfig, mkClient := seedExpiringTokenFixtures(t, s)
	past := time.Now().Add(-time.Hour)

	mkConfig("cfg-mixed")
	mkToken("tok-shared", "cfg-mixed", "active")
	mkClient("client-mixed", "cfg-mixed", false)
	require.NoError(t, s.DB().Create(&tables.TableMCPOauthToken{
		ID: "tok-user", AuthMode: "user", OauthConfigID: "cfg-mixed", Status: "active",
		AccessToken: "at-tok-user", TokenType: "Bearer",
		ExpiresAt: &past, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}).Error)

	shared, err := s.GetExpiringOauthTokens(context.Background(), time.Now().Add(time.Minute), []string{"shared"})
	require.NoError(t, err)
	sharedIDs := make(map[string]bool, len(shared))
	for _, tk := range shared {
		sharedIDs[tk.ID] = true
	}
	assert.True(t, sharedIDs["tok-shared"], "shared token must be returned when authModes=[shared]")
	assert.False(t, sharedIDs["tok-user"], "user-mode token must be excluded when authModes=[shared]")

	both, err := s.GetExpiringOauthTokens(context.Background(), time.Now().Add(time.Minute), []string{"shared", "user"})
	require.NoError(t, err)
	bothIDs := make(map[string]bool, len(both))
	for _, tk := range both {
		bothIDs[tk.ID] = true
	}
	assert.True(t, bothIDs["tok-shared"], "shared token must be returned when authModes=[shared,user]")
	assert.True(t, bothIDs["tok-user"], "user-mode token must be returned when authModes=[shared,user]")

	empty, err := s.GetExpiringOauthTokens(context.Background(), time.Now().Add(time.Minute), nil)
	require.NoError(t, err)
	assert.Empty(t, empty, "an empty authModes must match nothing rather than falling back to all modes")
}

// TestGetExpiringOauthTokens_RequiresEnabledClient verifies the refresh worker
// only keeps tokens warm while at least one enabled MCP client references the
// owning oauth_config. Disabled-only and unreferenced configs are skipped —
// their tokens catch up via GetAccessToken's inline refresh on next use.
func TestGetExpiringOauthTokens_RequiresEnabledClient(t *testing.T) {
	s := setupRDBTestStore(t)
	mkToken, mkConfig, mkClient := seedExpiringTokenFixtures(t, s)

	mkConfig("cfg-enabled")
	mkToken("tok-enabled", "cfg-enabled", "active")
	mkClient("client-enabled", "cfg-enabled", false)

	mkConfig("cfg-disabled")
	mkToken("tok-disabled", "cfg-disabled", "active")
	mkClient("client-disabled", "cfg-disabled", true)

	mkConfig("cfg-shared")
	mkToken("tok-shared", "cfg-shared", "active")
	mkClient("client-shared-off", "cfg-shared", true)
	mkClient("client-shared-on", "cfg-shared", false)

	mkConfig("cfg-no-client")
	mkToken("tok-no-client", "cfg-no-client", "active")

	mkToken("tok-orphan", "", "active") // no owning config at all

	ids := expiringTokenIDs(t, s)
	assert.True(t, ids["tok-enabled"], "token with an enabled client should be refreshed")
	assert.False(t, ids["tok-disabled"], "token referenced only by a disabled client must be excluded")
	assert.True(t, ids["tok-shared"], "config shared with at least one enabled client should be refreshed")
	assert.False(t, ids["tok-no-client"], "token whose config has no client rows must be excluded")
	assert.False(t, ids["tok-orphan"], "token with no owning config must be excluded")
}

// TestCreateOauthToken_AdminMode_UpsertsByMCPClientIDNotDuplicate guards the
// gap CodeRabbit flagged in CreateOauthToken's upsert-lookup switch: an
// admin-mode token (no UserID/VirtualKeyID/SessionID, bound only by
// MCPClientID — see InitiateUserOAuthFlow's MCPAuthModeAdmin case) matched no
// case and fell through to the default "not found" branch, so every call
// always inserted a fresh row instead of reusing the existing binding's row.
// RetainExchangeAdminCredential calls CreateOauthToken with exactly this
// shape on every token_exchange admin-credential repair, and its own doc
// comment promises "a repair replaces the existing row's credential" — this
// pins that an admin-mode call is a true upsert, not an unconditional insert.
func TestCreateOauthToken_AdminMode_UpsertsByMCPClientIDNotDuplicate(t *testing.T) {
	s := setupRDBTestStore(t)
	require.NoError(t, s.DB().AutoMigrate(&tables.TableMCPOauthToken{}))
	ctx := context.Background()

	first := &tables.TableMCPOauthToken{
		ID:          "tok-admin-1",
		AuthMode:    "admin",
		MCPClientID: "client-1",
		AccessToken: "first-access-token",
		TokenType:   "Bearer",
		Status:      "active",
	}
	require.NoError(t, s.CreateOauthToken(ctx, first))

	// A second call for the same (auth_mode='admin', mcp_client_id) binding —
	// a repair, per RetainExchangeAdminCredential's own doc comment — must
	// update the existing row, not insert a second one.
	second := &tables.TableMCPOauthToken{
		ID:          "tok-admin-2",
		AuthMode:    "admin",
		MCPClientID: "client-1",
		AccessToken: "second-access-token",
		TokenType:   "Bearer",
		Status:      "active",
	}
	require.NoError(t, s.CreateOauthToken(ctx, second))

	var rows []tables.TableMCPOauthToken
	require.NoError(t, s.DB().Where("auth_mode = ? AND mcp_client_id = ?", "admin", "client-1").Find(&rows).Error)
	require.Len(t, rows, 1, "must reuse the existing row for this binding, not insert a duplicate")
	assert.Equal(t, "second-access-token", rows[0].AccessToken, "the repair's new credential must win")

	// A different mcp_client_id is a different binding — it must get its own row.
	other := &tables.TableMCPOauthToken{
		ID:          "tok-admin-3",
		AuthMode:    "admin",
		MCPClientID: "client-2",
		AccessToken: "other-client-access-token",
		TokenType:   "Bearer",
		Status:      "active",
	}
	require.NoError(t, s.CreateOauthToken(ctx, other))
	var allAdminRows []tables.TableMCPOauthToken
	require.NoError(t, s.DB().Where("auth_mode = ?", "admin").Find(&allAdminRows).Error)
	assert.Len(t, allAdminRows, 2, "a different mcp_client_id must not reuse another client's admin row")
}

func TestGetOAuth2SigningKey_AutoGeneratesAndIsStable(t *testing.T) {
	s := setupOAuth2TestStore(t)
	ctx := context.Background()

	first, err := s.GetOAuth2SigningKey(ctx)
	require.NoError(t, err)
	require.NotNil(t, first)
	assert.NotEmpty(t, first.KID)
	assert.NotEmpty(t, first.PrivateKeyPEM)
	assert.NotEmpty(t, first.PublicKeyPEM)

	// A second call must return the same persisted key, not mint a new one.
	second, err := s.GetOAuth2SigningKey(ctx)
	require.NoError(t, err)
	assert.Equal(t, first.KID, second.KID)
}

func TestConsentOAuth2AuthorizeRequest_AtomicPendingTransition(t *testing.T) {
	s := setupOAuth2TestStore(t)
	ctx := context.Background()
	seedAuthorizeRequest(t, s, "req-1", tables.OAuth2AuthorizeRequestStatusPending, nil, time.Now().Add(time.Minute))

	req := &tables.TableOAuth2AuthorizeRequest{
		ID:        "req-1",
		CodeHash:  strPtr("code-hash-1"),
		BfMode:    "vk",
		BfSub:     "vk-1",
		UpdatedAt: time.Now(),
	}
	require.NoError(t, s.ConsentOAuth2AuthorizeRequest(ctx, req))

	got, err := s.GetOAuth2AuthorizeRequestByID(ctx, "req-1")
	require.NoError(t, err)
	assert.Equal(t, tables.OAuth2AuthorizeRequestStatusConsented, got.Status)
	require.NotNil(t, got.CodeHash)
	assert.Equal(t, "code-hash-1", *got.CodeHash)
	assert.Equal(t, "vk", got.BfMode)
	assert.Equal(t, "vk-1", got.BfSub)

	// A second consent on the now-consented row matches zero rows: ErrNotFound,
	// and the originally minted code hash is left untouched.
	err = s.ConsentOAuth2AuthorizeRequest(ctx, &tables.TableOAuth2AuthorizeRequest{
		ID: "req-1", CodeHash: strPtr("code-hash-2"), UpdatedAt: time.Now(),
	})
	assert.ErrorIs(t, err, ErrNotFound)

	got, err = s.GetOAuth2AuthorizeRequestByID(ctx, "req-1")
	require.NoError(t, err)
	assert.Equal(t, "code-hash-1", *got.CodeHash)
}

func TestConsumeOAuth2AuthorizeRequest_SingleUse(t *testing.T) {
	s := setupOAuth2TestStore(t)
	ctx := context.Background()
	seedAuthorizeRequest(t, s, "req-1", tables.OAuth2AuthorizeRequestStatusConsented, strPtr("ch"), time.Now().Add(time.Minute))

	rt := makeRefreshToken("rt-1", "req-1", "client-1", "hash-1")
	require.NoError(t, s.ConsumeOAuth2AuthorizeRequest(ctx, "req-1", rt))

	got, err := s.GetOAuth2AuthorizeRequestByID(ctx, "req-1")
	require.NoError(t, err)
	assert.Equal(t, tables.OAuth2AuthorizeRequestStatusCodeIssued, got.Status)
	stored, err := s.GetOAuth2RefreshTokenByHash(ctx, "hash-1")
	require.NoError(t, err)
	assert.Equal(t, "rt-1", stored.ID)

	// Reuse of the same code: the row is already code_issued, so the second
	// exchange matches zero rows and no second token is minted.
	rt2 := makeRefreshToken("rt-2", "req-1", "client-1", "hash-2")
	err = s.ConsumeOAuth2AuthorizeRequest(ctx, "req-1", rt2)
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = s.GetOAuth2RefreshTokenByHash(ctx, "hash-2")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestConsumeOAuth2AuthorizeRequest_ExpiredCodeRejected(t *testing.T) {
	s := setupOAuth2TestStore(t)
	ctx := context.Background()
	seedAuthorizeRequest(t, s, "req-1", tables.OAuth2AuthorizeRequestStatusConsented, strPtr("ch"), time.Now().Add(-time.Minute))

	rt := makeRefreshToken("rt-1", "req-1", "client-1", "hash-1")
	err := s.ConsumeOAuth2AuthorizeRequest(ctx, "req-1", rt)
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = s.GetOAuth2RefreshTokenByHash(ctx, "hash-1")
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestRotateOAuth2RefreshToken_RotationAndReplayGuard(t *testing.T) {
	s := setupOAuth2TestStore(t)
	ctx := context.Background()
	old := makeRefreshToken("rt-old", "fam-1", "client-1", "hash-old")
	require.NoError(t, s.DB().WithContext(ctx).Create(old).Error)

	newRT := makeRefreshToken("rt-new", "fam-1", "client-1", "hash-new")
	require.NoError(t, s.RotateOAuth2RefreshToken(ctx, "rt-old", newRT))

	// Old token is now revoked (no longer returned by the active-only lookup) but
	// the new one is active and carries the same family.
	_, err := s.GetOAuth2RefreshTokenByHash(ctx, "hash-old")
	assert.ErrorIs(t, err, ErrNotFound)
	active, err := s.GetOAuth2RefreshTokenByHash(ctx, "hash-new")
	require.NoError(t, err)
	assert.Equal(t, "fam-1", active.FamilyID)

	revoked, err := s.GetOAuth2RefreshTokenByHashAny(ctx, "hash-old")
	require.NoError(t, err)
	require.NotNil(t, revoked.RevokedAt)

	// Replaying the already-revoked token cannot rotate again.
	err = s.RotateOAuth2RefreshToken(ctx, "rt-old", makeRefreshToken("rt-x", "fam-1", "client-1", "hash-x"))
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestRevokeOAuth2RefreshTokensByFamilyID(t *testing.T) {
	s := setupOAuth2TestStore(t)
	ctx := context.Background()
	require.NoError(t, s.DB().Create(makeRefreshToken("a", "fam-1", "c", "ha")).Error)
	require.NoError(t, s.DB().Create(makeRefreshToken("b", "fam-1", "c", "hb")).Error)
	require.NoError(t, s.DB().Create(makeRefreshToken("c", "fam-2", "c", "hc")).Error)

	require.NoError(t, s.RevokeOAuth2RefreshTokensByFamilyID(ctx, "fam-1"))

	// fam-1 fully revoked; fam-2 untouched.
	_, err := s.GetOAuth2RefreshTokenByHash(ctx, "ha")
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = s.GetOAuth2RefreshTokenByHash(ctx, "hb")
	assert.ErrorIs(t, err, ErrNotFound)
	survivor, err := s.GetOAuth2RefreshTokenByHash(ctx, "hc")
	require.NoError(t, err)
	assert.Equal(t, "fam-2", survivor.FamilyID)
}

func TestSweepOAuth2RefreshTokens(t *testing.T) {
	s := setupOAuth2TestStore(t)
	ctx := context.Background()
	retention := time.Hour

	oldRevoked := time.Now().Add(-2 * time.Hour)
	recentRevoked := time.Now().Add(-time.Minute)

	staleTok := makeRefreshToken("stale", "f", "c", "h-stale")
	staleTok.RevokedAt = &oldRevoked
	recentTok := makeRefreshToken("recent", "f", "c", "h-recent")
	recentTok.RevokedAt = &recentRevoked
	activeTok := makeRefreshToken("active", "f", "c", "h-active")
	require.NoError(t, s.DB().Create(staleTok).Error)
	require.NoError(t, s.DB().Create(recentTok).Error)
	require.NoError(t, s.DB().Create(activeTok).Error)

	deleted, err := s.SweepOAuth2RefreshTokens(ctx, retention)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)

	// Stale revoked gone; recently-revoked and active survive (still needed for
	// replay detection / use).
	_, err = s.GetOAuth2RefreshTokenByHashAny(ctx, "h-stale")
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = s.GetOAuth2RefreshTokenByHashAny(ctx, "h-recent")
	require.NoError(t, err)
	_, err = s.GetOAuth2RefreshTokenByHash(ctx, "h-active")
	require.NoError(t, err)
}

func TestSweepOAuth2RefreshTokens_NonPositiveRetentionIsNoop(t *testing.T) {
	s := setupOAuth2TestStore(t)
	ctx := context.Background()
	revoked := time.Now().Add(-time.Hour)
	tok := makeRefreshToken("r", "f", "c", "h")
	tok.RevokedAt = &revoked
	require.NoError(t, s.DB().Create(tok).Error)

	deleted, err := s.SweepOAuth2RefreshTokens(ctx, 0)
	require.NoError(t, err)
	assert.Equal(t, int64(0), deleted)
	_, err = s.GetOAuth2RefreshTokenByHashAny(ctx, "h")
	require.NoError(t, err, "non-positive retention must not delete the replay-detection window")
}

func TestSweepOrphanedOAuth2Clients(t *testing.T) {
	s := setupOAuth2TestStore(t)
	ctx := context.Background()
	grace := time.Hour
	old := time.Now().Add(-2 * time.Hour)
	recent := time.Now()

	// withToken: backs a refresh token row → kept regardless of age.
	require.NoError(t, s.DB().Create(&tables.TableOAuth2Client{
		ID: "c-token", ClientID: "with-token", RedirectURIs: []string{"http://127.0.0.1/cb"},
		GrantTypes: []string{"authorization_code"}, CreatedAt: old,
	}).Error)
	require.NoError(t, s.DB().Create(makeRefreshToken("rt", "fam", "with-token", "h")).Error)

	// orphanOld: no tokens, registered before the grace cutoff → swept.
	require.NoError(t, s.DB().Create(&tables.TableOAuth2Client{
		ID: "c-old", ClientID: "orphan-old", RedirectURIs: []string{"http://127.0.0.1/cb"},
		GrantTypes: []string{"authorization_code"}, CreatedAt: old,
	}).Error)

	// orphanFresh: no tokens but mid-handshake (within grace) → kept.
	require.NoError(t, s.DB().Create(&tables.TableOAuth2Client{
		ID: "c-fresh", ClientID: "orphan-fresh", RedirectURIs: []string{"http://127.0.0.1/cb"},
		GrantTypes: []string{"authorization_code"}, CreatedAt: recent,
	}).Error)

	deleted, err := s.SweepOrphanedOAuth2Clients(ctx, grace)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)

	_, err = s.GetOAuth2ClientByClientID(ctx, "orphan-old")
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = s.GetOAuth2ClientByClientID(ctx, "with-token")
	require.NoError(t, err)
	_, err = s.GetOAuth2ClientByClientID(ctx, "orphan-fresh")
	require.NoError(t, err)
}

func TestSweepExpiredOAuth2AuthorizeRequests(t *testing.T) {
	s := setupOAuth2TestStore(t)
	ctx := context.Background()
	past := time.Now().Add(-time.Minute)
	future := time.Now().Add(time.Minute)

	seedAuthorizeRequest(t, s, "pending-expired", tables.OAuth2AuthorizeRequestStatusPending, nil, past)
	seedAuthorizeRequest(t, s, "consented-expired", tables.OAuth2AuthorizeRequestStatusConsented, strPtr("ch"), past)
	seedAuthorizeRequest(t, s, "issued-expired", tables.OAuth2AuthorizeRequestStatusCodeIssued, strPtr("ch2"), past)
	seedAuthorizeRequest(t, s, "pending-fresh", tables.OAuth2AuthorizeRequestStatusPending, nil, future)

	require.NoError(t, s.SweepExpiredOAuth2AuthorizeRequests(ctx))

	// Expired pending/consented are gone; an expired code_issued row is retained
	// (it represents a completed exchange), and a fresh pending row survives.
	_, err := s.GetOAuth2AuthorizeRequestByID(ctx, "pending-expired")
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = s.GetOAuth2AuthorizeRequestByID(ctx, "consented-expired")
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = s.GetOAuth2AuthorizeRequestByID(ctx, "issued-expired")
	require.NoError(t, err)
	_, err = s.GetOAuth2AuthorizeRequestByID(ctx, "pending-fresh")
	require.NoError(t, err)
}

func TestRevokeOAuth2RefreshTokensByMode(t *testing.T) {
	s := setupOAuth2TestStore(t)
	ctx := context.Background()
	sessionTok := makeRefreshToken("s1", "f1", "c", "h-session")
	sessionTok.BfMode = "session"
	vkTok := makeRefreshToken("v1", "f2", "c", "h-vk") // BfMode "vk" from helper default
	require.NoError(t, s.DB().Create(sessionTok).Error)
	require.NoError(t, s.DB().Create(vkTok).Error)

	require.NoError(t, s.RevokeOAuth2RefreshTokensByMode(ctx, "session"))

	// Only session-mode tokens revoked; vk-mode untouched.
	_, err := s.GetOAuth2RefreshTokenByHash(ctx, "h-session")
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = s.GetOAuth2RefreshTokenByHash(ctx, "h-vk")
	require.NoError(t, err)
}

func TestListOAuth2Sessions_JoinsAndExcludesRevoked(t *testing.T) {
	s := setupOAuth2TestStore(t)
	ctx := context.Background()

	require.NoError(t, s.DB().Create(&tables.TableOAuth2Client{
		ID: "crow", ClientID: "client-1", ClientName: "Test Client",
		RedirectURIs: []string{"http://127.0.0.1/cb"}, GrantTypes: []string{"authorization_code"}, CreatedAt: time.Now(),
	}).Error)
	require.NoError(t, s.DB().Create(&tables.TableVirtualKey{ID: "vk-1", Name: "Alpha VK", Value: *schemas.NewSecretVar("sk-bf-alpha")}).Error)

	vkTok := makeRefreshToken("rt-vk", "f1", "client-1", "h-vk")
	vkTok.BfSub = "vk-1" // joins to governance_virtual_keys.id
	sessTok := makeRefreshToken("rt-sess", "f2", "client-1", "h-sess")
	sessTok.BfMode = "session"
	sessTok.BfSub = "sess-xyz"
	revokedAt := time.Now()
	deadTok := makeRefreshToken("rt-dead", "f3", "client-1", "h-dead")
	deadTok.RevokedAt = &revokedAt
	require.NoError(t, s.DB().Create(vkTok).Error)
	require.NoError(t, s.DB().Create(sessTok).Error)
	require.NoError(t, s.DB().Create(deadTok).Error)

	rows, total, err := s.ListOAuth2Sessions(ctx, OAuth2SessionsQueryParams{})
	require.NoError(t, err)
	require.Len(t, rows, 2, "revoked grants are excluded")
	require.Equal(t, int64(2), total, "total count excludes revoked grants")

	byID := map[string]OAuth2SessionRow{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	assert.Equal(t, "Test Client", byID["rt-vk"].ClientName)
	assert.Equal(t, "Alpha VK", byID["rt-vk"].BfSubDisplay, "vk mode resolves the VK name")
	assert.Empty(t, byID["rt-sess"].BfSubDisplay, "session mode has no display name")

	// Round-trip the per-id load + revoke gate used by the management API.
	got, err := s.GetOAuth2SessionByID(ctx, "rt-vk")
	require.NoError(t, err)
	assert.Equal(t, "vk", got.BfMode)
	require.NoError(t, s.RevokeOAuth2Session(ctx, "rt-vk"))
	_, err = s.GetOAuth2SessionByID(ctx, "rt-vk")
	assert.ErrorIs(t, err, ErrNotFound)
	// Revoking an already-revoked grant reports not-found.
	assert.ErrorIs(t, s.RevokeOAuth2Session(ctx, "rt-dead"), ErrNotFound)
}

// TestListOAuth2Sessions_FilterAndPaginate pins the DB-side filtering +
// pagination: ordering (created_at DESC), limit/offset paging, the total count
// (independent of the page slice), the bf_mode filter, and case-insensitive
// search across the joined client name, the joined VK display name, and the
// bound identity (bf_sub).
func TestListOAuth2Sessions_FilterAndPaginate(t *testing.T) {
	s := setupOAuth2TestStore(t)
	ctx := context.Background()

	require.NoError(t, s.DB().Create(&tables.TableOAuth2Client{
		ID: "c1", ClientID: "client-1", ClientName: "Acme Server",
		RedirectURIs: []string{"http://127.0.0.1/cb"}, GrantTypes: []string{"authorization_code"}, CreatedAt: time.Now(),
	}).Error)
	require.NoError(t, s.DB().Create(&tables.TableOAuth2Client{
		ID: "c2", ClientID: "client-2", ClientName: "Beta Server",
		RedirectURIs: []string{"http://127.0.0.1/cb"}, GrantTypes: []string{"authorization_code"}, CreatedAt: time.Now(),
	}).Error)
	require.NoError(t, s.DB().Create(&tables.TableVirtualKey{ID: "vk-1", Name: "Alpha VK", Value: *schemas.NewSecretVar("sk-bf-alpha")}).Error)

	base := time.Now()
	rtA := makeRefreshToken("rt-a", "fa", "client-1", "h-a") // vk mode, bf_sub vk-1 → display "Alpha VK"
	rtA.CreatedAt = base.Add(-3 * time.Minute)
	rtB := makeRefreshToken("rt-b", "fb", "client-1", "h-b")
	rtB.BfMode, rtB.BfSub = "user", "user@acme.com"
	rtB.CreatedAt = base.Add(-2 * time.Minute)
	rtC := makeRefreshToken("rt-c", "fc", "client-2", "h-c")
	rtC.BfMode, rtC.BfSub = "session", "sess-xyz"
	rtC.CreatedAt = base.Add(-1 * time.Minute)
	require.NoError(t, s.DB().Create(rtA).Error)
	require.NoError(t, s.DB().Create(rtB).Error)
	require.NoError(t, s.DB().Create(rtC).Error)

	ids := func(rows []OAuth2SessionRow) []string {
		out := make([]string, len(rows))
		for i, r := range rows {
			out[i] = r.ID
		}
		return out
	}

	// Page 1 (newest first), limit 2 — total reflects all matches, not the page.
	rows, total, err := s.ListOAuth2Sessions(ctx, OAuth2SessionsQueryParams{Limit: 2})
	require.NoError(t, err)
	assert.Equal(t, int64(3), total)
	assert.Equal(t, []string{"rt-c", "rt-b"}, ids(rows), "ordered created_at DESC")

	// Page 2.
	rows, total, err = s.ListOAuth2Sessions(ctx, OAuth2SessionsQueryParams{Limit: 2, Offset: 2})
	require.NoError(t, err)
	assert.Equal(t, int64(3), total)
	assert.Equal(t, []string{"rt-a"}, ids(rows))

	// bf_mode filter.
	rows, total, err = s.ListOAuth2Sessions(ctx, OAuth2SessionsQueryParams{Modes: []string{"user"}})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	assert.Equal(t, []string{"rt-b"}, ids(rows))

	// Search matches the joined VK display name.
	rows, total, err = s.ListOAuth2Sessions(ctx, OAuth2SessionsQueryParams{Search: "alpha"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	assert.Equal(t, []string{"rt-a"}, ids(rows))

	// Search matches the joined client name.
	rows, total, err = s.ListOAuth2Sessions(ctx, OAuth2SessionsQueryParams{Search: "beta"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	assert.Equal(t, []string{"rt-c"}, ids(rows))

	// Search matches the bound identity (bf_sub).
	rows, total, err = s.ListOAuth2Sessions(ctx, OAuth2SessionsQueryParams{Search: "user@acme"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), total)
	assert.Equal(t, []string{"rt-b"}, ids(rows))
}

// TestListOAuth2Sessions_StableTiebreakerSameTimestamp pins the secondary id sort:
// when grants share an identical created_at, ordering by created_at alone is
// nondeterministic, so offset paging could repeat or skip rows. The id tiebreaker
// makes every page deterministic — here the two pages partition all four rows
// (ordered id DESC) with no duplicates and no gaps.
func TestListOAuth2Sessions_StableTiebreakerSameTimestamp(t *testing.T) {
	s := setupOAuth2TestStore(t)
	ctx := context.Background()

	ids := func(rows []OAuth2SessionRow) []string {
		out := make([]string, len(rows))
		for i, r := range rows {
			out[i] = r.ID
		}
		return out
	}

	// All four grants share the same created_at, so only the id tiebreaker can
	// give a stable order.
	ts := time.Now()
	for _, id := range []string{"rt-1", "rt-2", "rt-3", "rt-4"} {
		rt := makeRefreshToken(id, "fam-"+id, "client-x", "hash-"+id)
		rt.CreatedAt = ts
		require.NoError(t, s.DB().Create(rt).Error)
	}

	// Page 1 and Page 2 (limit 2 each) must partition all rows in id-DESC order.
	page1, total, err := s.ListOAuth2Sessions(ctx, OAuth2SessionsQueryParams{Limit: 2})
	require.NoError(t, err)
	assert.Equal(t, int64(4), total)
	assert.Equal(t, []string{"rt-4", "rt-3"}, ids(page1), "page 1 ordered by id DESC on tied timestamps")

	page2, total, err := s.ListOAuth2Sessions(ctx, OAuth2SessionsQueryParams{Limit: 2, Offset: 2})
	require.NoError(t, err)
	assert.Equal(t, int64(4), total)
	assert.Equal(t, []string{"rt-2", "rt-1"}, ids(page2), "page 2 continues without overlap or gap")
}

// TestSweepConvergence_TokenSweepThenClientSweep pins the documented ordering: a
// client whose only tokens are revoked is collected only after the token sweep
// removes those aged rows, leaving the client backing zero tokens.
func TestSweepConvergence_TokenSweepThenClientSweep(t *testing.T) {
	s := setupOAuth2TestStore(t)
	ctx := context.Background()
	old := time.Now().Add(-2 * time.Hour)

	require.NoError(t, s.DB().Create(&tables.TableOAuth2Client{
		ID: "c", ClientID: "revoked-only", RedirectURIs: []string{"http://127.0.0.1/cb"},
		GrantTypes: []string{"authorization_code"}, CreatedAt: old,
	}).Error)
	revokedAt := old
	tok := makeRefreshToken("rt", "fam", "revoked-only", "h")
	tok.RevokedAt = &revokedAt
	require.NoError(t, s.DB().Create(tok).Error)

	// Before the token sweep, the client still backs a (revoked) token row → kept.
	deleted, err := s.SweepOrphanedOAuth2Clients(ctx, time.Hour)
	require.NoError(t, err)
	assert.Equal(t, int64(0), deleted)

	// Token sweep removes the aged revoked row, then the client is collectible.
	_, err = s.SweepOAuth2RefreshTokens(ctx, time.Hour)
	require.NoError(t, err)
	deleted, err = s.SweepOrphanedOAuth2Clients(ctx, time.Hour)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
	_, err = s.GetOAuth2ClientByClientID(ctx, "revoked-only")
	assert.ErrorIs(t, err, ErrNotFound)
}

// TestRefreshOauthTokenFieldsIfActive_CAS verifies the compare-and-swap guard:
// a write only commits while the row's stored refresh_token still matches the
// value the caller redeemed upstream (expectedPriorRefreshToken). This is what
// prevents two concurrent refreshes of the same row — e.g. from two different
// cluster nodes racing the same MCP client's periodic connection checker —
// from clobbering each other: whichever one wins the race and writes first
// advances the row's refresh_token, and the loser's write (still carrying the
// now-superseded prior value) is rejected instead of overwriting the winner's
// fresher credentials.
func TestRefreshOauthTokenFieldsIfActive_CAS(t *testing.T) {
	s := setupRDBTestStore(t)
	require.NoError(t, s.DB().AutoMigrate(&tables.TableMCPOauthToken{}))
	ctx := context.Background()
	future := time.Now().Add(time.Hour)

	require.NoError(t, s.DB().Create(&tables.TableMCPOauthToken{
		ID: "tok-1", AuthMode: "shared", OauthConfigID: "cfg-1", Status: "active",
		AccessToken: "at-original", RefreshToken: "rt-original", TokenType: "Bearer",
		ExpiresAt: &future, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}).Error)

	// Two "nodes" both read the row with rt-original as the refresh_token and
	// both redeem it upstream (out of scope here — we're testing only the
	// store-level CAS), racing to write back their own new credentials.
	updatedA, err := s.RefreshOauthTokenFieldsIfActive(ctx, "tok-1", "rt-original", "at-node-a", "rt-node-a", &future, time.Now())
	require.NoError(t, err)
	assert.True(t, updatedA, "the first writer, whose expectedPriorRefreshToken still matches the stored value, must win")

	// The second writer's redemption is now stale: the row's refresh_token has
	// already moved to rt-node-a, so its write (still keyed off rt-original)
	// must be rejected rather than clobbering node A's fresher credentials.
	updatedB, err := s.RefreshOauthTokenFieldsIfActive(ctx, "tok-1", "rt-original", "at-node-b", "rt-node-b", &future, time.Now())
	require.NoError(t, err)
	assert.False(t, updatedB, "a write whose expectedPriorRefreshToken no longer matches the stored value must lose the race")

	stored, err := s.GetOauthTokenByID(ctx, "tok-1")
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, "at-node-a", stored.AccessToken, "the loser's write must not have overwritten the winner's access token")
	assert.Equal(t, "rt-node-a", stored.RefreshToken, "the loser's write must not have overwritten the winner's refresh token")

	// A write with the now-current refresh_token still succeeds — the CAS
	// guard rejects stale writers, not every subsequent write.
	updatedC, err := s.RefreshOauthTokenFieldsIfActive(ctx, "tok-1", "rt-node-a", "at-node-c", "rt-node-c", &future, time.Now())
	require.NoError(t, err)
	assert.True(t, updatedC, "a write keyed off the row's actual current refresh_token must succeed")
}

// TestRefreshOauthTokenFieldsIfActive_StatusGuardStillApplies is a narrow
// regression check that adding the refresh_token CAS comparison did not
// regress the pre-existing status guard: a row already flipped to
// 'needs_reauth' (e.g. by a concurrent RotateMCPOAuthConfig) must still
// reject the write even when expectedPriorRefreshToken matches exactly.
func TestRefreshOauthTokenFieldsIfActive_StatusGuardStillApplies(t *testing.T) {
	s := setupRDBTestStore(t)
	require.NoError(t, s.DB().AutoMigrate(&tables.TableMCPOauthToken{}))
	ctx := context.Background()
	future := time.Now().Add(time.Hour)

	require.NoError(t, s.DB().Create(&tables.TableMCPOauthToken{
		ID: "tok-2", AuthMode: "shared", OauthConfigID: "cfg-2", Status: "needs_reauth",
		AccessToken: "at-original", RefreshToken: "rt-original", TokenType: "Bearer",
		ExpiresAt: &future, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}).Error)

	updated, err := s.RefreshOauthTokenFieldsIfActive(ctx, "tok-2", "rt-original", "at-new", "rt-new", &future, time.Now())
	require.NoError(t, err)
	assert.False(t, updated, "a non-'active' row must reject the write even with a matching expectedPriorRefreshToken")
}
