package handlers

import (
	"context"
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
type stubConfigManager struct{}

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

// TestUpdateConfig_PersistsVKRotationCooldown pins the regression where PUT
// /api/config validated vk_rotation_cooldown but never copied it into the
// persisted config, so saves from the security settings UI silently dropped
// the value and it snapped back on the next fetch.
func TestUpdateConfig_PersistsVKRotationCooldown(t *testing.T) {
	SetLogger(&mockLogger{})
	store := newRealOAuth2Store(t)
	cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeHeaders, false)
	h := &ConfigHandler{store: cfg, configManager: stubConfigManager{}}

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
