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

// seedRotateOauthConfig inserts an oauth_configs row with the given
// client_id/client_secret and returns a freshly re-fetched copy (via
// GetOauthConfigByID, mirroring how real callers load it before rotating).
// Re-fetching matters when the package's encryption key is initialized (see
// encryption_test.go's package-level encrypt.Init): the row's BeforeSave hook
// encrypts ClientSecret.Val on the very struct passed to Create, mutating it
// in place, so the pointer used to insert the row is left holding ciphertext
// rather than the plaintext value the test declared.
func seedRotateOauthConfig(t *testing.T, s *RDBConfigStore, id, clientID, clientSecret string) *tables.TableOauthConfig {
	t.Helper()
	cfg := &tables.TableOauthConfig{
		ID:           id,
		ClientID:     schemas.NewSecretVar(clientID),
		ClientSecret: schemas.NewSecretVar(clientSecret),
		AuthorizeURL: "https://auth.example.com/authorize",
		TokenURL:     "https://auth.example.com/token",
		Scopes:       `["read"]`,
		RedirectURI:  "http://127.0.0.1/cb",
		Status:       "authorized",
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	}
	require.NoError(t, s.DB().Create(cfg).Error)
	fresh, err := s.GetOauthConfigByID(context.Background(), id)
	require.NoError(t, err)
	require.NotNil(t, fresh)
	return fresh
}

// baseRotateFields returns an MCPOAuthConfigFields that resolves to exactly
// what seedRotateOauthConfig stored — the starting point for tests that only
// want to change one field.
func baseRotateFields(clientID, clientSecret string) MCPOAuthConfigFields {
	return MCPOAuthConfigFields{
		ClientID:     schemas.NewSecretVar(clientID),
		ClientSecret: schemas.NewSecretVar(clientSecret),
		AuthorizeURL: "https://auth.example.com/authorize",
		TokenURL:     "https://auth.example.com/token",
		Scopes:       []string{"read"},
	}
}

// seedRotateToken inserts a mcp_oauth_tokens row bound to oauthConfigID with
// the given auth_mode and initial status.
func seedRotateToken(t *testing.T, s *RDBConfigStore, id, oauthConfigID, authMode, status string) {
	t.Helper()
	require.NoError(t, s.DB().Create(&tables.TableMCPOauthToken{
		ID:            id,
		AuthMode:      authMode,
		OauthConfigID: oauthConfigID,
		Status:        status,
		AccessToken:   "at-" + id,
		TokenType:     "Bearer",
		CreatedAt:     time.Now(),
		UpdatedAt:     time.Now(),
	}).Error)
}

func tokenStatus(t *testing.T, s *RDBConfigStore, id string) string {
	t.Helper()
	tok, err := s.GetOauthTokenByID(context.Background(), id)
	require.NoError(t, err)
	require.NotNil(t, tok)
	return tok.Status
}

// TestRotateMCPOAuthConfig_ChangedCredentialsCascadesAllAuthModes pins the
// core rotation behavior: when the resolved client_id/client_secret actually
// differ from what's stored, the oauth_configs row is updated in place and
// every bound token is cascaded to needs_reauth regardless of auth_mode —
// shared and per-user rows alike, since rotation is security-driven and must
// not leave any holder silently still trusted.
func TestRotateMCPOAuthConfig_ChangedCredentialsCascadesAllAuthModes(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()
	cfg := seedRotateOauthConfig(t, s, "cfg-1", "old-client-id", "old-client-secret")
	seedRotateToken(t, s, "tok-shared", cfg.ID, "shared", "active")
	seedRotateToken(t, s, "tok-user", cfg.ID, "user", "active")

	fields := baseRotateFields("new-client-id", "new-client-secret")
	rotated, err := s.RotateMCPOAuthConfig(ctx, cfg, fields)
	require.NoError(t, err)
	assert.True(t, rotated, "changed credentials must report a rotation happened")

	stored, err := s.GetOauthConfigByID(ctx, cfg.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, "new-client-id", stored.GetResolvedClientID(), "stored client_id must reflect the new value")
	assert.Equal(t, "new-client-secret", stored.GetResolvedClientSecret(), "stored client_secret must reflect the new value")

	assert.Equal(t, "needs_reauth", tokenStatus(t, s, "tok-shared"), "shared-mode token must be cascaded")
	assert.Equal(t, "needs_reauth", tokenStatus(t, s, "tok-user"), "user-mode token must be cascaded too, since rotation has no auth_mode filter")
}

// TestRotateMCPOAuthConfig_ChangedSecondaryFieldsCascadesToo proves the
// widened scope: authorize_url/token_url/registration_url/resource/scopes
// drift is treated exactly like client_id/client_secret drift — any of them
// changing alone (with credentials unchanged) still rotates the row and
// cascades needs_reauth, since a changed authorize_url/token_url can point
// at a different identity provider and changed scopes/resource mean
// already-issued tokens were consented under permissions that no longer
// apply.
func TestRotateMCPOAuthConfig_ChangedSecondaryFieldsCascadesToo(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()
	cfg := seedRotateOauthConfig(t, s, "cfg-secondary", "same-client-id", "same-client-secret")
	seedRotateToken(t, s, "tok-secondary", cfg.ID, "shared", "active")

	fields := MCPOAuthConfigFields{
		ClientID:        schemas.NewSecretVar("same-client-id"),
		ClientSecret:    schemas.NewSecretVar("same-client-secret"),
		AuthorizeURL:    "https://auth.example.com/v2/authorize",
		TokenURL:        "https://auth.example.com/v2/token",
		RegistrationURL: "https://auth.example.com/register",
		Resource:        "https://mcp.example.com",
		Scopes:          []string{"read", "write"},
	}
	rotated, err := s.RotateMCPOAuthConfig(ctx, cfg, fields)
	require.NoError(t, err)
	assert.True(t, rotated, "secondary-field-only drift must still rotate")

	stored, err := s.GetOauthConfigByID(ctx, cfg.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, "https://auth.example.com/v2/authorize", stored.AuthorizeURL)
	assert.Equal(t, "https://auth.example.com/v2/token", stored.TokenURL)
	require.NotNil(t, stored.RegistrationURL)
	assert.Equal(t, "https://auth.example.com/register", *stored.RegistrationURL)
	assert.Equal(t, "https://mcp.example.com", stored.Resource)

	assert.Equal(t, "needs_reauth", tokenStatus(t, s, "tok-secondary"))
}

// TestRotateMCPOAuthConfig_ScopesComparisonIsOrderIndependent verifies scope
// drift detection compares sets, not sequences — reordering the same scopes
// in the file must not be treated as drift.
func TestRotateMCPOAuthConfig_ScopesComparisonIsOrderIndependent(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()
	cfg := seedRotateOauthConfig(t, s, "cfg-scopes", "client-id", "client-secret")
	// seedRotateOauthConfig stores Scopes: `["read"]`; give it two scopes to
	// reorder against. Re-fetch afterward rather than reusing cfg directly:
	// UpdateOauthConfig's Save triggers BeforeSave, which would otherwise
	// leave cfg.ClientSecret encrypted in place (see
	// TestRotateMCPOAuthConfig_DoesNotMutateCallerSecretVar's doc comment),
	// making the plaintext fields below spuriously "differ" on ClientSecret
	// instead of testing what this test is actually about.
	cfg.Scopes = `["read","write"]`
	require.NoError(t, s.UpdateOauthConfig(ctx, cfg))
	cfg, err := s.GetOauthConfigByID(ctx, cfg.ID)
	require.NoError(t, err)
	require.NotNil(t, cfg)

	fields := baseRotateFields("client-id", "client-secret")
	fields.Scopes = []string{"write", "read"}
	rotated, err := s.RotateMCPOAuthConfig(ctx, cfg, fields)
	require.NoError(t, err)
	assert.False(t, rotated, "reordered-but-equal scopes must not be treated as drift")
}

// TestRotateMCPOAuthConfig_IdenticalCredentialsIsNoop verifies that
// resolving to byte-identical values across every field skips both the DB
// write and the reauth cascade entirely: a caller that resends the same
// real values (or round-trips a fetch-modify-put unchanged) must not
// invalidate every existing holder's token for no reason.
func TestRotateMCPOAuthConfig_IdenticalCredentialsIsNoop(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()
	cfg := seedRotateOauthConfig(t, s, "cfg-2", "same-client-id", "same-client-secret")
	seedRotateToken(t, s, "tok-shared-2", cfg.ID, "shared", "active")
	seedRotateToken(t, s, "tok-user-2", cfg.ID, "user", "active")

	beforeUpdatedAt := cfg.UpdatedAt

	fields := baseRotateFields("same-client-id", "same-client-secret")
	rotated, err := s.RotateMCPOAuthConfig(ctx, cfg, fields)
	require.NoError(t, err)
	assert.False(t, rotated, "identical values must report no rotation happened")

	stored, err := s.GetOauthConfigByID(ctx, cfg.ID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.True(t, stored.UpdatedAt.Equal(beforeUpdatedAt), "no-op rotation must not write the oauth_configs row")
	assert.Equal(t, "same-client-id", stored.GetResolvedClientID())
	assert.Equal(t, "same-client-secret", stored.GetResolvedClientSecret())

	assert.Equal(t, "active", tokenStatus(t, s, "tok-shared-2"), "no-op rotation must not cascade shared token")
	assert.Equal(t, "active", tokenStatus(t, s, "tok-user-2"), "no-op rotation must not cascade user token")
}

// TestRotateMCPOAuthConfig_NilConfigErrors verifies a nil
// existingOauthConfig is rejected with an error rather than panicking or
// silently doing nothing.
func TestRotateMCPOAuthConfig_NilConfigErrors(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()

	rotated, err := s.RotateMCPOAuthConfig(ctx, nil, baseRotateFields("a", "b"))
	assert.Error(t, err)
	assert.False(t, rotated)
}

// TestRotateMCPOAuthConfig_DoesNotMutateCallerSecretVar pins the fix for a
// real aliasing bug: UpdateOauthConfig's GORM Save triggers
// TableOauthConfig.BeforeSave, which encrypts ClientSecret.Val in place when
// encryption is enabled (this package's tests run with it enabled, see
// encryption_test.go). If existingOauthConfig.ClientSecret were assigned the
// caller's fields.ClientSecret pointer directly instead of a clone, that
// encryption would silently corrupt the caller's own SecretVar into
// ciphertext as a side effect of this call — e.g. turning a config.json
// sync's in-memory authorizedOauth.ClientSecret, or the HTTP handler's
// req.OauthConfig.ClientSecret, into garbage the caller never expected to
// change.
func TestRotateMCPOAuthConfig_DoesNotMutateCallerSecretVar(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()
	cfg := seedRotateOauthConfig(t, s, "cfg-3", "old-client-id", "old-client-secret")

	fields := baseRotateFields("new-client-id-3", "new-client-secret-3")

	rotated, err := s.RotateMCPOAuthConfig(ctx, cfg, fields)
	require.NoError(t, err)
	assert.True(t, rotated)

	assert.Equal(t, "new-client-id-3", fields.ClientID.GetValue(), "caller's ClientID SecretVar must stay plaintext and untouched")
	assert.Equal(t, "new-client-secret-3", fields.ClientSecret.GetValue(), "caller's ClientSecret SecretVar must stay plaintext and untouched, not encrypted in place")
	assert.NotSame(t, fields.ClientID, cfg.ClientID, "the row must hold a clone, not the caller's own ClientID pointer")
	assert.NotSame(t, fields.ClientSecret, cfg.ClientSecret, "the row must hold a clone, not the caller's own ClientSecret pointer")
}

// TestMCPOAuthConfigFields_DiffersFrom pins the comparison rules in
// isolation, independent of any DB write.
func TestMCPOAuthConfigFields_DiffersFrom(t *testing.T) {
	stored := &tables.TableOauthConfig{
		ClientID:     schemas.NewSecretVar("stored-client-id"),
		ClientSecret: schemas.NewSecretVar("stored-client-secret"),
		AuthorizeURL: "https://auth.example.com/authorize",
		TokenURL:     "https://auth.example.com/token",
		Resource:     "https://mcp.example.com",
		Scopes:       `["read","write"]`,
	}

	t.Run("nil existing config always differs", func(t *testing.T) {
		assert.True(t, baseRotateFields("a", "b").DiffersFrom(nil))
	})

	t.Run("identical fields do not differ", func(t *testing.T) {
		fields := MCPOAuthConfigFields{
			ClientID:     schemas.NewSecretVar("stored-client-id"),
			ClientSecret: schemas.NewSecretVar("stored-client-secret"),
			AuthorizeURL: "https://auth.example.com/authorize",
			TokenURL:     "https://auth.example.com/token",
			Resource:     "https://mcp.example.com",
			Scopes:       []string{"write", "read"}, // reordered, still equal as a set
		}
		assert.False(t, fields.DiffersFrom(stored))
	})

	t.Run("registration_url nil-vs-empty is not a difference", func(t *testing.T) {
		fields := MCPOAuthConfigFields{
			ClientID:     schemas.NewSecretVar("stored-client-id"),
			ClientSecret: schemas.NewSecretVar("stored-client-secret"),
			AuthorizeURL: "https://auth.example.com/authorize",
			TokenURL:     "https://auth.example.com/token",
			Resource:     "https://mcp.example.com",
			Scopes:       []string{"read", "write"},
			// RegistrationURL left "" — stored.RegistrationURL is also nil.
		}
		assert.False(t, fields.DiffersFrom(stored))
	})

	t.Run("a changed registration_url is a difference", func(t *testing.T) {
		fields := MCPOAuthConfigFields{
			ClientID:        schemas.NewSecretVar("stored-client-id"),
			ClientSecret:    schemas.NewSecretVar("stored-client-secret"),
			AuthorizeURL:    "https://auth.example.com/authorize",
			TokenURL:        "https://auth.example.com/token",
			Resource:        "https://mcp.example.com",
			Scopes:          []string{"read", "write"},
			RegistrationURL: "https://auth.example.com/register",
		}
		assert.True(t, fields.DiffersFrom(stored))
	})

	t.Run("a changed resource is a difference", func(t *testing.T) {
		fields := MCPOAuthConfigFields{
			ClientID:     schemas.NewSecretVar("stored-client-id"),
			ClientSecret: schemas.NewSecretVar("stored-client-secret"),
			AuthorizeURL: "https://auth.example.com/authorize",
			TokenURL:     "https://auth.example.com/token",
			Resource:     "https://elsewhere.example.com",
			Scopes:       []string{"read", "write"},
		}
		assert.True(t, fields.DiffersFrom(stored))
	})
}
