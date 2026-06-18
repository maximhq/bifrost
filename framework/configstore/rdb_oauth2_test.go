package configstore

import (
	"context"
	"testing"
	"time"

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
	require.NoError(t, s.DB().Create(&tables.TableVirtualKey{ID: "vk-1", Name: "Alpha VK", Value: "sk-bf-alpha"}).Error)

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

	rows, err := s.ListOAuth2Sessions(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 2, "revoked grants are excluded")

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
