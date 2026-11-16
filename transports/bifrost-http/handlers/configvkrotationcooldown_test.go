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
func (stubConfigManager) ValidateConfiguredSetupToken(string) bool                        { return false }
func (stubConfigManager) ReloadClientConfigFromConfigStore(context.Context) error         { return nil }
func (stubConfigManager) UpdateSyncConfig(context.Context) error                          { return nil }
func (stubConfigManager) ForceReloadPricing(context.Context) error                        { return nil }
func (stubConfigManager) UpdateDropExcessRequests(context.Context, bool)                  {}
func (stubConfigManager) UpdateMCPToolManagerConfig(context.Context, int, int, string, bool, int, int, *schemas.MCPCodeModeLimits) error {
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

// PUT /api/config sets the global MCP max protocol version; omitting it keeps the stored cap, so a
// caller unaware of the field never lifts the upgrade pin, and an unsupported value is rejected.
func TestUpdateConfig_MCPMaxProtocolVersion(t *testing.T) {
	SetLogger(&mockLogger{})
	store := newRealOAuth2Store(t)
	cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeHeaders, false)
	h := &ConfigHandler{store: cfg, configManager: stubConfigManager{}}
	save := func(t *testing.T, body string) int {
		t.Helper()
		ctx := putConfigCtx(`{"client_config":{` + body + `"log_retention_days":7}}`)
		h.updateConfig(ctx)
		return ctx.Response.StatusCode()
	}
	stored := func(t *testing.T) string {
		t.Helper()
		persisted, err := store.GetClientConfig(bgCtx())
		require.NoError(t, err)
		return persisted.MCPMaxProtocolVersion
	}

	require.Equal(t, fasthttp.StatusOK, save(t, `"mcp_max_protocol_version":"2025-06-18",`))
	assert.Equal(t, "2025-06-18", stored(t))

	require.Equal(t, fasthttp.StatusOK, save(t, ``))
	assert.Equal(t, "2025-06-18", stored(t), "omitted keeps the cap")

	require.Equal(t, fasthttp.StatusBadRequest, save(t, `"mcp_max_protocol_version":"latest",`))
	assert.Equal(t, "2025-06-18", stored(t))

	require.Equal(t, fasthttp.StatusOK, save(t, `"mcp_max_protocol_version":"auto",`))
	assert.Equal(t, "auto", stored(t))
}

// tokenizerRecorder records the tokenizer families each post-save reload hands to the
// running client. Like the server's reload, it reads the stored list.
type tokenizerRecorder struct {
	stubConfigManager
	stored  func() []string
	applied [][]string
}

func (r *tokenizerRecorder) ReloadClientConfigFromConfigStore(context.Context) error {
	r.applied = append(r.applied, r.stored())
	return nil
}

// PUT /api/config sets usage_estimation_tokenizers: names are normalized and persisted, and
// the running client gets the stored list from the reload that follows a successful save.
// Omitting the field keeps the stored list, and a rejected request applies nothing.
func TestUpdateConfig_UsageEstimationTokenizers(t *testing.T) {
	SetLogger(&mockLogger{})
	store := newRealOAuth2Store(t)
	cfg := newTestOAuth2Config(store, configtables.MCPServerAuthModeHeaders, false)
	recorder := &tokenizerRecorder{}
	h := &ConfigHandler{store: cfg, configManager: recorder}
	recorder.stored = func() []string {
		persisted, err := store.GetClientConfig(bgCtx())
		require.NoError(t, err)
		return persisted.UsageEstimationTokenizers
	}
	save := func(t *testing.T, body string) int {
		t.Helper()
		ctx := putConfigCtx(`{"client_config":{` + body + `"log_retention_days":7}}`)
		h.updateConfig(ctx)
		return ctx.Response.StatusCode()
	}
	stored := func(t *testing.T) []string {
		t.Helper()
		persisted, err := store.GetClientConfig(bgCtx())
		require.NoError(t, err)
		return persisted.UsageEstimationTokenizers
	}

	require.Equal(t, fasthttp.StatusOK, save(t, `"usage_estimation_tokenizers":[" Anthropic ","llama3","anthropic"],`))
	assert.Equal(t, []string{"anthropic", "llama3"}, stored(t))
	assert.Equal(t, []string{"anthropic", "llama3"}, cfg.ClientConfig.UsageEstimationTokenizers)
	require.Len(t, recorder.applied, 1)
	assert.Equal(t, []string{"anthropic", "llama3"}, recorder.applied[0])

	require.Equal(t, fasthttp.StatusOK, save(t, ``))
	assert.Equal(t, []string{"anthropic", "llama3"}, stored(t), "omitted keeps the stored list")
	require.Len(t, recorder.applied, 2)
	assert.Equal(t, []string{"anthropic", "llama3"}, recorder.applied[1], "the reload re-applies the stored list")

	require.Equal(t, fasthttp.StatusBadRequest, save(t, `"usage_estimation_tokenizers":["gpt2"],`))
	assert.Equal(t, []string{"anthropic", "llama3"}, stored(t))
	assert.Len(t, recorder.applied, 2, "a rejected list is never applied")

	// A valid list on a request that a later check rejects must not reach the runtime.
	require.Equal(t, fasthttp.StatusBadRequest, save(t, `"usage_estimation_tokenizers":["deepseek"],"mcp_code_mode_binding_level":"bogus",`))
	assert.Equal(t, []string{"anthropic", "llama3"}, stored(t))
	assert.Len(t, recorder.applied, 2, "a list on a rejected request is never applied")

	require.Equal(t, fasthttp.StatusOK, save(t, `"usage_estimation_tokenizers":[],`))
	assert.Empty(t, stored(t), "an empty list returns to the base encodings")
	require.Len(t, recorder.applied, 3)
	assert.Empty(t, recorder.applied[2])
}
