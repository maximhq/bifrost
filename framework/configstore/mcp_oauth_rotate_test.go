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

// TestRotateMCPOAuthConfig_ExpectedClientIDClaimsTheRow: with ExpectedClientID set, a rotation from a
// copy loaded before another caller rotated the row is refused with ErrOAuthConfigChanged and writes
// nothing, so it cannot sign out the token issued after the first rotation.
func TestRotateMCPOAuthConfig_ExpectedClientIDClaimsTheRow(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()
	first := seedRotateOauthConfig(t, s, "cfg-claim", "old-client-id", "secret")
	stale, err := s.GetOauthConfigByID(ctx, first.ID)
	require.NoError(t, err)

	fields := baseRotateFields("new-client-id", "secret")
	fields.ExpectedClientID = schemas.Ptr("old-client-id")
	rotated, err := s.RotateMCPOAuthConfig(ctx, first, fields)
	require.NoError(t, err)
	require.True(t, rotated)
	seedRotateToken(t, s, "tok-fresh", first.ID, "shared", "active")

	rotated, err = s.RotateMCPOAuthConfig(ctx, stale, fields)
	require.ErrorIs(t, err, ErrOAuthConfigChanged)
	assert.False(t, rotated)
	assert.Equal(t, "active", tokenStatus(t, s, "tok-fresh"), "the refused rotation must not cascade")
	stored, err := s.GetOauthConfigByID(ctx, first.ID)
	require.NoError(t, err)
	assert.Equal(t, "new-client-id", stored.GetResolvedClientID())
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

// The issuer binding is stored with rotated credentials, and differing only in it is no rotation:
// a resubmission that does not carry it (a manual edit) leaves the stored binding alone.
func TestRotateMCPOAuthConfig_StoresIssuerBinding(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()
	cfg := seedRotateOauthConfig(t, s, "cfg-issuer", "old-client", "secret")
	cfg.Issuer, cfg.IssParameterSupported = "https://auth.example.com", true
	require.NoError(t, s.UpdateOauthConfig(ctx, cfg))
	stored, err := s.GetOauthConfigByID(ctx, cfg.ID)
	require.NoError(t, err)

	rotated, err := s.RotateMCPOAuthConfig(ctx, stored, baseRotateFields("old-client", "secret"))
	require.NoError(t, err)
	assert.False(t, rotated)
	stored, err = s.GetOauthConfigByID(ctx, cfg.ID)
	require.NoError(t, err)
	assert.Equal(t, "https://auth.example.com", stored.Issuer)

	fields := baseRotateFields("new-client", "secret")
	fields.Issuer, fields.IssParameterSupported, fields.DynamicallyRegistered = "https://moved.example.com", true, true
	rotated, err = s.RotateMCPOAuthConfig(ctx, stored, fields)
	require.NoError(t, err)
	assert.True(t, rotated)
	stored, err = s.GetOauthConfigByID(ctx, cfg.ID)
	require.NoError(t, err)
	assert.Equal(t, "https://moved.example.com", stored.Issuer)
	assert.True(t, stored.IssParameterSupported)
	assert.True(t, stored.DynamicallyRegistered)
}

// A new issuer is a change on its own: the authorization server moved even when its URLs did not, so
// the binding is stored and tokens from the previous server must re-authenticate.
func TestRotateMCPOAuthConfig_IssuerOnlyChangeRotates(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()
	cfg := seedRotateOauthConfig(t, s, "cfg-issuer-only", "client", "secret")
	cfg.Issuer = "https://auth.example.com"
	require.NoError(t, s.UpdateOauthConfig(ctx, cfg))
	seedRotateToken(t, s, "tok-issuer-only", cfg.ID, "user", "active")
	stored, err := s.GetOauthConfigByID(ctx, cfg.ID)
	require.NoError(t, err)

	fields := baseRotateFields("client", "secret")
	fields.Issuer = "https://moved.example.com"
	assert.True(t, fields.DiffersFrom(stored))
	rotated, err := s.RotateMCPOAuthConfig(ctx, stored, fields)
	require.NoError(t, err)
	assert.True(t, rotated)
	stored, err = s.GetOauthConfigByID(ctx, cfg.ID)
	require.NoError(t, err)
	assert.Equal(t, "https://moved.example.com", stored.Issuer)
	assert.Equal(t, "needs_reauth", tokenStatus(t, s, "tok-issuer-only"))
}

// KeepBinding carries the stored binding into an edit that does not set it: the issuer stays while the
// endpoints stay, and the dynamic-registration flag stays with its client_id.
func TestMCPOAuthConfigFields_KeepBinding(t *testing.T) {
	stored := &tables.TableOauthConfig{
		ClientID:     schemas.NewSecretVar("client"),
		AuthorizeURL: "https://auth.example.com/authorize",
		TokenURL:     "https://auth.example.com/token",
		Issuer:       "https://auth.example.com", IssParameterSupported: true, DynamicallyRegistered: true,
	}
	t.Run("scope-only edit keeps everything", func(t *testing.T) {
		fields := baseRotateFields("client", "secret")
		fields.Scopes = []string{"read", "write"}
		fields.KeepBinding(stored)
		assert.Equal(t, "https://auth.example.com", fields.Issuer)
		assert.True(t, fields.IssParameterSupported)
		assert.True(t, fields.DynamicallyRegistered)
	})
	t.Run("a new client_id drops the binding", func(t *testing.T) {
		fields := baseRotateFields("entered-by-admin", "secret")
		fields.KeepBinding(stored)
		assert.False(t, fields.DynamicallyRegistered)
		assert.Empty(t, fields.Issuer, "replacement credentials re-bind at the next sign-in")
		assert.False(t, fields.IssParameterSupported)
	})
	t.Run("a new token endpoint drops the issuer", func(t *testing.T) {
		fields := baseRotateFields("client", "secret")
		fields.TokenURL = "https://other.example.com/token"
		fields.KeepBinding(stored)
		assert.Empty(t, fields.Issuer)
		assert.False(t, fields.IssParameterSupported)
	})
}

// A rebind applies only while the stored issuer is still the one it moved from: of two replicas
// rebinding at once, the second loses and keeps the first one's client.
func TestRotateMCPOAuthConfig_RebindLosesToConcurrentRebind(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()
	cfg := seedRotateOauthConfig(t, s, "cfg-rebind-race", "old-client", "secret")
	cfg.Issuer = "https://auth.example.com"
	require.NoError(t, s.UpdateOauthConfig(ctx, cfg))
	from := "https://auth.example.com"
	rebind := func(clientID string) error {
		stored, err := s.GetOauthConfigByID(ctx, cfg.ID)
		require.NoError(t, err)
		stored.Issuer = from // both replicas read the old issuer
		fields := baseRotateFields(clientID, "secret")
		fields.Issuer, fields.DynamicallyRegistered, fields.ExpectedIssuer = "https://moved.example.com", true, &from
		_, err = s.RotateMCPOAuthConfig(ctx, stored, fields)
		return err
	}
	require.NoError(t, rebind("first-client"))
	require.ErrorIs(t, rebind("second-client"), ErrOAuthConfigChanged)
	stored, err := s.GetOauthConfigByID(ctx, cfg.ID)
	require.NoError(t, err)
	assert.Equal(t, "first-client", stored.GetResolvedClientID())
}

// BindOAuthConfigIssuer writes only the binding, and only while the row is still the one the caller
// reasoned about: an edit that landed meanwhile is neither overwritten nor bound.
func TestBindOAuthConfigIssuer(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()
	seedRotateOauthConfig(t, s, "cfg-bind", "client", "secret")

	bound, err := s.BindOAuthConfigIssuer(ctx, "cfg-bind", "", "https://auth.example.com/token", "https://auth.example.com", true)
	require.NoError(t, err)
	assert.True(t, bound)
	stored, err := s.GetOauthConfigByID(ctx, "cfg-bind")
	require.NoError(t, err)
	assert.Equal(t, "https://auth.example.com", stored.Issuer)
	assert.True(t, stored.IssParameterSupported)
	assert.Equal(t, "client", stored.GetResolvedClientID(), "credentials are not written")

	// An admin edit moved the token endpoint after the caller read the row.
	stored.TokenURL = "https://moved.example.com/token"
	stored.ClientID = schemas.NewSecretVar("edited")
	require.NoError(t, s.UpdateOauthConfig(ctx, stored))
	bound, err = s.BindOAuthConfigIssuer(ctx, "cfg-bind", "https://auth.example.com", "https://auth.example.com/token", "https://auth.example.com", false)
	require.NoError(t, err)
	assert.False(t, bound)
	stored, err = s.GetOauthConfigByID(ctx, "cfg-bind")
	require.NoError(t, err)
	assert.True(t, stored.IssParameterSupported, "nothing is written once the row moved on")
	assert.Equal(t, "edited", stored.GetResolvedClientID())
}

// RecordOAuthFlowIssuer writes only the issuer, and only while the flow is still pending with the
// state the caller read: a renewed or completed flow is left alone.
func TestRecordOAuthFlowIssuer(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()
	seedRotateOauthConfig(t, s, "cfg-flow", "client", "secret")
	flow := &tables.TableMCPOauthFlow{
		ID: "flow-1", MCPClientID: "mcp-1", OauthConfigID: "cfg-flow", State: "state-1", CodeVerifier: "verifier-1",
		FlowMode: "admin", Status: "pending", ExpiresAt: time.Now().Add(time.Minute),
	}
	require.NoError(t, s.CreateOauthUserSession(ctx, flow))

	recorded, err := s.RecordOAuthFlowIssuer(ctx, "flow-1", "state-1", "https://auth.example.com", true)
	require.NoError(t, err)
	assert.True(t, recorded)
	stored, err := s.GetOauthUserSessionByState(ctx, "state-1")
	require.NoError(t, err)
	assert.Equal(t, "https://auth.example.com", stored.Issuer)
	assert.True(t, stored.IssParameterSupported)

	// A renewal gave the row a new state and verifier after the caller read it.
	stored.State, stored.CodeVerifier, stored.Issuer = "state-2", "verifier-2", ""
	require.NoError(t, s.UpdateOauthUserSession(ctx, stored))
	recorded, err = s.RecordOAuthFlowIssuer(ctx, "flow-1", "state-1", "https://auth.example.com", true)
	require.NoError(t, err)
	assert.False(t, recorded)
	stored, err = s.GetOauthUserSessionByState(ctx, "state-2")
	require.NoError(t, err)
	assert.Empty(t, stored.Issuer, "nothing is written once the flow moved on")
	assert.Equal(t, "verifier-2", stored.CodeVerifier)

	stored.Status = "authorized"
	require.NoError(t, s.UpdateOauthUserSession(ctx, stored))
	recorded, err = s.RecordOAuthFlowIssuer(ctx, "flow-1", "state-2", "https://auth.example.com", true)
	require.NoError(t, err)
	assert.False(t, recorded, "a completed flow is not written")
}

// A row from before the issuer columns holds NULL, not "": both conditional writes still read it as
// unbound, so an upgraded config binds at its next sign-in.
func TestOAuthIssuerClaims_MatchMigratedNullIssuer(t *testing.T) {
	s := setupRDBTestStore(t)
	ctx := context.Background()
	migrated := func(id string) {
		seedRotateOauthConfig(t, s, id, "client", "secret")
		require.NoError(t, s.DB().Exec("UPDATE oauth_configs SET issuer = NULL WHERE id = ?", id).Error)
	}

	migrated("cfg-null-bind")
	bound, err := s.BindOAuthConfigIssuer(ctx, "cfg-null-bind", "", "https://auth.example.com/token", "https://auth.example.com", true)
	require.NoError(t, err)
	assert.True(t, bound, "a NULL issuer is unbound")

	migrated("cfg-null-rotate")
	stored, err := s.GetOauthConfigByID(ctx, "cfg-null-rotate")
	require.NoError(t, err)
	unbound := ""
	fields := baseRotateFields("moved-client", "secret")
	fields.Issuer, fields.ExpectedIssuer = "https://moved.example.com", &unbound
	_, err = s.RotateMCPOAuthConfig(ctx, stored, fields)
	require.NoError(t, err, "a rebind from an unbound config applies to a NULL issuer")
}
