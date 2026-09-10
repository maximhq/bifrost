package server

import (
	"context"
	"errors"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/require"
)

type fakePromptsPlugin struct {
	name    string
	reloads int
	err     error
}

func (p *fakePromptsPlugin) GetName() string { return p.name }
func (p *fakePromptsPlugin) Cleanup() error  { return nil }
func (p *fakePromptsPlugin) Reload(context.Context) error {
	p.reloads++
	return p.err
}

// nonReloadingPlugin is registered under the prompts plugin name but cannot reload.
type nonReloadingPlugin struct{ name string }

func (p *nonReloadingPlugin) GetName() string { return p.name }
func (p *nonReloadingPlugin) Cleanup() error  { return nil }

func promptCacheServer(plugins ...schemas.BasePlugin) *BifrostHTTPServer {
	SetLogger(bifrost.NewNoOpLogger())
	config := &lib.Config{}
	config.BasePlugins.Store(&plugins)
	return &BifrostHTTPServer{
		Config: config,
		Ctx:    schemas.NewBifrostContext(context.Background(), schemas.NoDeadline),
	}
}

func TestReloadPromptCacheReloadsThePlugin(t *testing.T) {
	plugin := &fakePromptsPlugin{name: "prompts"}
	server := promptCacheServer(plugin)

	require.NoError(t, server.ReloadPromptCache(context.Background()))
	require.Equal(t, 1, plugin.reloads)
}

// A prompt write must not fail just because the prompts plugin is disabled.
func TestReloadPromptCacheWithoutPluginIsNoOp(t *testing.T) {
	server := promptCacheServer()

	require.NoError(t, server.ReloadPromptCache(context.Background()))
}

func TestReloadPromptCacheUsesPluginNameFromContext(t *testing.T) {
	plugin := &fakePromptsPlugin{name: "enterprise-prompts"}
	server := promptCacheServer(plugin)
	ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyPromptsPluginName, "enterprise-prompts")
	server.Ctx = schemas.NewBifrostContext(ctx, schemas.NoDeadline)

	require.NoError(t, server.ReloadPromptCache(context.Background()))
	require.Equal(t, 1, plugin.reloads, "enterprise registers the plugin under its own name")
}

func TestReloadPromptCacheReturnsPluginError(t *testing.T) {
	plugin := &fakePromptsPlugin{name: "prompts", err: errors.New("database unavailable")}
	server := promptCacheServer(plugin)

	require.ErrorContains(t, server.ReloadPromptCache(context.Background()), "database unavailable")
}

// A plugin that cannot reload must not fail the write, but it does warn rather than pass silently.
func TestReloadPromptCacheWithIncompatiblePluginIsNoOp(t *testing.T) {
	server := promptCacheServer(&nonReloadingPlugin{name: "prompts"})

	require.NoError(t, server.ReloadPromptCache(context.Background()))
}
