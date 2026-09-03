package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configtables "github.com/maximhq/bifrost/framework/configstore/tables"
)

// stubConfigManager is a no-op ConfigManager for exercising updateConfig's
// persistence path without a live runtime behind it.
type stubConfigManager struct {
	graceRevocations int
	graceRevokeErr   error
}

func (stubConfigManager) UpdateAuthConfig(context.Context, *configstore.AuthConfig) error { return nil }
func (stubConfigManager) ValidateSetupToken(string) bool                                  { return true }
func (stubConfigManager) ReloadClientConfigFromConfigStore(context.Context) error         { return nil }
func (stubConfigManager) UpdateSyncConfig(context.Context) error                          { return nil }
func (stubConfigManager) ForceReloadPricing(context.Context) error                        { return nil }
func (stubConfigManager) UpdateDropExcessRequests(context.Context, bool)                  {}
func (stubConfigManager) UpdateMCPToolManagerConfig(context.Context, int, int, string, bool) error {
	return nil
}
func (stubConfigManager) ReloadPlugin(context.Context, string, *string, any, *schemas.PluginPlacement, *int) error {
	return nil
}
func (stubConfigManager) RemovePlugin(context.Context, string) error { return nil }
func (stubConfigManager) ReloadProxyConfig(context.Context, *configtables.GlobalProxyConfig) error {
	return nil
}
func (stubConfigManager) ReloadHeaderFilterConfig(context.Context, *configtables.GlobalHeaderFilterConfig) error {
	return nil
}
func (s *stubConfigManager) RevokeVirtualKeyRotationGrace(context.Context) error {
	s.graceRevocations++
	return s.graceRevokeErr
}

// TestUpdateConfig_PersistsVKRotationCooldown pins the regression where PUT
// /api/config validated vk_rotation_cooldown but never copied it into the
// persisted config, so saves from the security settings UI silently dropped
// the value and it snapped back on the next fetch.
func TestUpdateConfig_PersistsVKRotationCooldown(t *testing.T) {
	SetLogger(&mockLogger{})
	store := newRealOAuth2Store(t)
	cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeHeaders, false)
	h := &ConfigHandler{store: cfg, configManager: &stubConfigManager{}}

	save := func(t *testing.T, cooldownJSON string) {
		t.Helper()
		ctx := putConfigCtx(`{"client_config":{"vk_rotation_cooldown":` + cooldownJSON + `,"log_retention_days":7}}`)
		h.updateConfig(ctx)
		require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))
	}

	t.Run("duration string is persisted and applied in memory", func(t *testing.T) {
		save(t, `"5m"`)
		persisted, err := store.GetClientConfig(bgCtx())
		require.NoError(t, err)
		assert.Equal(t, 5*time.Minute, persisted.VKRotationCooldown.D(), "vk_rotation_cooldown must survive a config save")
		assert.Equal(t, 5*time.Minute, cfg.ClientConfig.VKRotationCooldown.D(), "in-memory client config must carry the new cooldown")
	})

	t.Run("zero clears a previously stored cooldown", func(t *testing.T) {
		// Self-contained: store a non-zero cooldown first so this subtest
		// exercises the clear transition even when run in isolation.
		save(t, `"5m"`)
		save(t, `0`)
		persisted, err := store.GetClientConfig(bgCtx())
		require.NoError(t, err)
		assert.Equal(t, time.Duration(0), persisted.VKRotationCooldown.D(), "clearing the cooldown must persist 0")
		assert.Equal(t, time.Duration(0), cfg.ClientConfig.VKRotationCooldown.D())
	})

	// An empty duration string is what a cleared UI field or a config file with
	// "vk_rotation_cooldown": "" sends. It means "no cooldown" - the same as 0 -
	// so it must clear the stored value instead of failing validation.
	t.Run("empty string clears a previously stored cooldown", func(t *testing.T) {
		save(t, `"5m"`)
		save(t, `""`)
		persisted, err := store.GetClientConfig(bgCtx())
		require.NoError(t, err)
		assert.Equal(t, time.Duration(0), persisted.VKRotationCooldown.D(), "an empty cooldown must persist 0")
		assert.Equal(t, time.Duration(0), cfg.ClientConfig.VKRotationCooldown.D())
	})
}

// TestUpdateConfig_ZeroCooldownRevokesInFlightGracePeriods pins the reported
// bug: the cooldown is stamped onto each key's previous_value_expires_at at
// rotation time and never re-read, so saving 0 changed only what future
// rotations would stamp. Keys rotated under the old cooldown kept accepting
// their previous value for the rest of the original window, contradicting the
// settings copy ("Leave empty (or 0) to have the old value stop working
// immediately"). Every spelling of "no cooldown" must trigger the revocation,
// and a non-zero save must leave in-flight windows alone.
func TestUpdateConfig_ZeroCooldownRevokesInFlightGracePeriods(t *testing.T) {
	SetLogger(&mockLogger{})

	for _, tc := range []struct {
		name        string
		cooldown    string
		wantRevokes int
	}{
		{name: "integer zero", cooldown: `0`, wantRevokes: 1},
		{name: "empty duration string", cooldown: `""`, wantRevokes: 1},
		{name: "zero duration string", cooldown: `"0s"`, wantRevokes: 1},
		{name: "non-zero leaves in-flight windows running", cooldown: `"5m"`, wantRevokes: 0},
		{name: "explicit null counts as clearing", cooldown: `null`, wantRevokes: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newRealOAuth2Store(t)
			cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeHeaders, false)
			manager := &stubConfigManager{}
			h := &ConfigHandler{store: cfg, configManager: manager}

			ctx := putConfigCtx(`{"client_config":{"vk_rotation_cooldown":` + tc.cooldown + `,"log_retention_days":7}}`)
			h.updateConfig(ctx)
			require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))

			assert.Equal(t, tc.wantRevokes, manager.graceRevocations)
		})
	}
}

// TestUpdateConfig_GraceRevocationFailureFailsRequest ensures a failed
// revocation surfaces instead of reporting success: the UI would otherwise show
// the grace period as disabled while retired key values kept authenticating.
func TestUpdateConfig_GraceRevocationFailureFailsRequest(t *testing.T) {
	SetLogger(&mockLogger{})
	store := newRealOAuth2Store(t)
	cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeHeaders, false)
	manager := &stubConfigManager{graceRevokeErr: errors.New("boom")}
	h := &ConfigHandler{store: cfg, configManager: manager}

	ctx := putConfigCtx(`{"client_config":{"vk_rotation_cooldown":0,"log_retention_days":7}}`)
	h.updateConfig(ctx)
	assert.Equal(t, fasthttp.StatusInternalServerError, ctx.Response.StatusCode())
	assert.Contains(t, string(ctx.Response.Body()), "rotation grace")
}

// TestUpdateConfig_OmittedCooldownDoesNotRevoke covers the partial-update case.
// ClientConfig is a value struct, so a PUT that never mentions
// vk_rotation_cooldown decodes to 0 exactly like an explicit clear. Revoking on
// that would let an unrelated settings save invalidate every retired key value
// the caller said nothing about, so the revocation requires the field to be
// present in the request body.
func TestUpdateConfig_OmittedCooldownDoesNotRevoke(t *testing.T) {
	SetLogger(&mockLogger{})
	store := newRealOAuth2Store(t)
	cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeHeaders, false)
	manager := &stubConfigManager{}
	h := &ConfigHandler{store: cfg, configManager: manager}

	ctx := putConfigCtx(`{"client_config":{"log_retention_days":7}}`)
	h.updateConfig(ctx)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode(), string(ctx.Response.Body()))

	assert.Zero(t, manager.graceRevocations, "an omitted cooldown must not revoke live grace windows")
}
