package handlers

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/encrypt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

type configHandlerTestStore struct {
	configstore.ConfigStore
	updateClientConfigCalls int
	flushSessionsCalls      int
}

func (s *configHandlerTestStore) UpdateClientConfig(ctx context.Context, config *configstore.ClientConfig) error {
	s.updateClientConfigCalls++
	return s.ConfigStore.UpdateClientConfig(ctx, config)
}

func (s *configHandlerTestStore) FlushSessions(ctx context.Context) error {
	s.flushSessionsCalls++
	return s.ConfigStore.FlushSessions(ctx)
}

type configHandlerTestManager struct {
	ConfigManager
	store configstore.ConfigStore
}

func (m *configHandlerTestManager) UpdateAuthConfig(ctx context.Context, config *configstore.AuthConfig) error {
	if config.IsEnabled && (config.AdminUserName == nil || config.AdminUserName.GetValue() == "" ||
		config.AdminPassword == nil || config.AdminPassword.GetValue() == "") {
		return errors.New("username and password are required when auth is enabled")
	}
	return m.store.UpdateAuthConfig(ctx, config)
}

func (m *configHandlerTestManager) ReloadClientConfigFromConfigStore(context.Context) error {
	return nil
}

func newConfigHandlerAuthTest(t *testing.T) (*ConfigHandler, *configHandlerTestStore) {
	t.Helper()

	baseStore := newRealOAuth2Store(t)
	store := &configHandlerTestStore{ConfigStore: baseStore}
	clientConfig := &configstore.ClientConfig{
		AllowedHeaders:   []string{"X-Existing"},
		LogRetentionDays: 30,
		Compat: configstore.CompatConfig{
			ConvertTextToChat:      true,
			ConvertChatToResponses: true,
			ShouldDropParams:       true,
			ShouldConvertParams:    true,
		},
	}
	require.NoError(t, store.UpdateClientConfig(context.Background(), clientConfig))
	store.updateClientConfigCalls = 0

	cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeHeaders, false)
	cfg.ClientConfig.AllowedHeaders = clientConfig.AllowedHeaders
	cfg.ClientConfig.LogRetentionDays = clientConfig.LogRetentionDays
	cfg.ClientConfig.Compat = clientConfig.Compat
	manager := &configHandlerTestManager{store: store}

	return NewConfigHandler(manager, cfg), store
}

func TestGetPasswordPolicyFailures(t *testing.T) {
	tests := []struct {
		name     string
		password string
		want     []string
	}{
		{
			name:     "valid password",
			password: "StrongPass1!",
			want:     []string{},
		},
		{
			name:     "missing all requirements",
			password: "",
			want: []string{
				"at least 12 characters",
				"one uppercase letter",
				"one lowercase letter",
				"one number",
				"one special character",
			},
		},
		{
			name:     "missing character classes",
			password: "weakpassword",
			want: []string{
				"one uppercase letter",
				"one number",
				"one special character",
			},
		},
		{
			name:     "32-character redaction-shaped password still validates",
			password: "aaaa************************bbbb",
			want: []string{
				"one uppercase letter",
				"one number",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getPasswordPolicyFailures(tt.password)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("getPasswordPolicyFailures() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShouldPreserveAdminPassword(t *testing.T) {
	tests := []struct {
		name     string
		password *schemas.SecretVar
		want     bool
	}{
		{
			name: "missing password",
			want: true,
		},
		{
			name:     "empty password",
			password: schemas.NewSecretVar(""),
			want:     true,
		},
		{
			name:     "admin password sentinel",
			password: schemas.NewSecretVar("<redacted>"),
			want:     true,
		},
		{
			name:     "alternate admin password sentinel",
			password: schemas.NewSecretVar("[REDACTED]"),
			want:     true,
		},
		{
			name:     "secret reference",
			password: schemas.NewSecretVar("env.ADMIN_PASSWORD"),
			want:     false,
		},
		{
			name:     "regular password",
			password: schemas.NewSecretVar("StrongPassword1!"),
			want:     false,
		},
		{
			name:     "32-character redaction-shaped password",
			password: schemas.NewSecretVar("Aa1b************************cdef"),
			want:     false,
		},
		{
			name:     "short redaction-shaped password",
			password: schemas.NewSecretVar("****"),
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldPreserveAdminPassword(tt.password); got != tt.want {
				t.Fatalf("shouldPreserveAdminPassword() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestAdminPasswordSecretReferenceHandling(t *testing.T) {
	t.Setenv("BIFROST_TEST_UNRESOLVED_ADMIN_PASSWORD", "")
	unresolved := schemas.NewSecretVar("env.BIFROST_TEST_UNRESOLVED_ADMIN_PASSWORD")
	require.EqualError(t, validateSubmittedAdminPassword(unresolved),
		"external reference env.BIFROST_TEST_UNRESOLVED_ADMIN_PASSWORD for admin_password resolved to an empty value")

	const submittedPassword = "ReferencedPassword1!"
	t.Setenv("BIFROST_TEST_ADMIN_PASSWORD", submittedPassword)
	password := schemas.NewSecretVar("env.BIFROST_TEST_ADMIN_PASSWORD")
	require.NoError(t, validateSubmittedAdminPassword(password))

	hashedPassword, err := hashAdminPassword(password)
	require.NoError(t, err)
	assert.Equal(t, password.GetRawRef(), hashedPassword.GetRawRef())
	assert.Equal(t, password.Type(), hashedPassword.Type())
	passwordMatches, err := encrypt.CompareHash(hashedPassword.GetValue(), submittedPassword)
	require.NoError(t, err)
	assert.True(t, passwordMatches)
}

func TestUpdateConfig_ReplacesAdminPasswordMatchingGenericSecretMask(t *testing.T) {
	SetLogger(&mockLogger{})
	handler, store := newConfigHandlerAuthTest(t)

	oldHash, err := encrypt.Hash("OldPassword1!")
	require.NoError(t, err)
	require.NoError(t, store.UpdateAuthConfig(context.Background(), &configstore.AuthConfig{
		AdminUserName: schemas.NewSecretVar("admin"),
		AdminPassword: schemas.NewSecretVar(oldHash),
		IsEnabled:     true,
	}))

	const submittedPassword = "Aa1b************************cdef"
	ctx := putConfigCtx(`{
		"client_config":{"allowed_headers":["X-Existing"],"log_retention_days":30},
		"auth_config":{
			"admin_username":{"value":"admin"},
			"admin_password":{"value":"` + submittedPassword + `"},
			"is_enabled":true
		}
	}`)
	handler.updateConfig(ctx)

	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	storedAuth, err := store.GetAuthConfig(context.Background())
	require.NoError(t, err)
	require.NotNil(t, storedAuth)
	assert.NotEqual(t, submittedPassword, storedAuth.AdminPassword.GetValue())
	assert.NotEqual(t, oldHash, storedAuth.AdminPassword.GetValue())
	passwordMatches, err := encrypt.CompareHash(storedAuth.AdminPassword.GetValue(), submittedPassword)
	require.NoError(t, err)
	assert.True(t, passwordMatches)
	assert.Equal(t, 1, store.flushSessionsCalls)
}

func TestUpdateConfig_RejectsWeakAdminPasswordBeforeConfigMutation(t *testing.T) {
	SetLogger(&mockLogger{})
	handler, store := newConfigHandlerAuthTest(t)

	oldHash, err := encrypt.Hash("OldPassword1!")
	require.NoError(t, err)
	require.NoError(t, store.UpdateAuthConfig(context.Background(), &configstore.AuthConfig{
		AdminUserName: schemas.NewSecretVar("admin"),
		AdminPassword: schemas.NewSecretVar(oldHash),
		IsEnabled:     true,
	}))

	ctx := putConfigCtx(`{
		"client_config":{"allowed_headers":["X-Mutated"],"log_retention_days":30},
		"auth_config":{
			"admin_username":{"value":"admin"},
			"admin_password":{"value":"****"},
			"is_enabled":true
		}
	}`)
	handler.updateConfig(ctx)

	require.Equal(t, fasthttp.StatusBadRequest, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	assert.Contains(t, string(ctx.Response.Body()), "auth password must include")
	assert.Zero(t, store.updateClientConfigCalls)
	storedClientConfig, err := store.GetClientConfig(context.Background())
	require.NoError(t, err)
	require.NotNil(t, storedClientConfig)
	assert.Equal(t, []string{"X-Existing"}, storedClientConfig.AllowedHeaders)
	storedAuth, err := store.GetAuthConfig(context.Background())
	require.NoError(t, err)
	require.NotNil(t, storedAuth)
	assert.Equal(t, oldHash, storedAuth.AdminPassword.GetValue())
	assert.Zero(t, store.flushSessionsCalls)
}

func TestUpdateConfig_UpdatesPasswordWithoutSubmittedUsername(t *testing.T) {
	SetLogger(&mockLogger{})
	handler, store := newConfigHandlerAuthTest(t)

	oldHash, err := encrypt.Hash("OldPassword1!")
	require.NoError(t, err)
	require.NoError(t, store.UpdateAuthConfig(context.Background(), &configstore.AuthConfig{
		AdminUserName: schemas.NewSecretVar("admin"),
		AdminPassword: schemas.NewSecretVar(oldHash),
		IsEnabled:     true,
	}))

	const submittedPassword = "NewPassword1!"
	ctx := putConfigCtx(`{
		"client_config":{"allowed_headers":["X-Existing"],"log_retention_days":30},
		"auth_config":{
			"admin_password":{"value":"` + submittedPassword + `"},
			"is_enabled":true
		}
	}`)
	handler.updateConfig(ctx)

	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	storedAuth, err := store.GetAuthConfig(context.Background())
	require.NoError(t, err)
	require.NotNil(t, storedAuth)
	assert.Equal(t, "admin", storedAuth.AdminUserName.GetValue())
	assert.NotEqual(t, oldHash, storedAuth.AdminPassword.GetValue())
	passwordMatches, err := encrypt.CompareHash(storedAuth.AdminPassword.GetValue(), submittedPassword)
	require.NoError(t, err)
	assert.True(t, passwordMatches)
	assert.Equal(t, 1, store.flushSessionsCalls)
}
