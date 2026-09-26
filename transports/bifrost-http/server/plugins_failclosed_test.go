package server

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/plugins"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
)

const failClosedTestSentinel = "sk-test-sentinel-should-never-leak-4f19c2"

// TestLoadCustomPluginsFailsClosedOnInstantiateError: an enabled, non-enterprise
// plugin whose shared object cannot be loaded must make loadCustomPlugins return
// an error, and that error must not carry the plugin's path.
func TestLoadCustomPluginsFailsClosedOnInstantiateError(t *testing.T) {
	logger = noopTestLogger{}
	path := "/no/such/path/" + failClosedTestSentinel + ".so"
	config := &lib.Config{
		PluginLoader: plugins.NewSharedObjectPluginLoader(nil),
		PluginConfigs: []*schemas.PluginConfig{
			{Enabled: true, Name: "capture", Path: &path},
		},
	}
	server := &BifrostHTTPServer{Config: config}

	err := server.loadCustomPlugins(context.Background())
	if err == nil {
		t.Fatal("loadCustomPlugins returned nil error for an enabled, non-enterprise plugin that failed to instantiate — this is exactly the fail-open defect this patch exists to close")
	}
	if strings.Contains(err.Error(), failClosedTestSentinel) {
		t.Fatalf("returned aggregate error leaked plugin-config text (sentinel %q found in %q) — the aggregate must stay a count + generic message only; raw detail belongs solely in the per-plugin UpdatePluginOverallStatus/logger.Error surface", failClosedTestSentinel, err.Error())
	}
}

// TestLoadCustomPluginsFailsClosedOnNilPlugin: a loader that returns (nil, nil)
// hits the defensive nil-plugin branch, which must also count as a failure.
func TestLoadCustomPluginsFailsClosedOnNilPlugin(t *testing.T) {
	logger = noopTestLogger{}
	path := "/irrelevant/nil-plugin.so"
	config := &lib.Config{
		PluginLoader: nilReturningLoader{},
		PluginConfigs: []*schemas.PluginConfig{
			{Enabled: true, Name: "capture", Path: &path},
		},
	}
	server := &BifrostHTTPServer{Config: config}

	if err := server.loadCustomPlugins(context.Background()); err == nil {
		t.Fatal("loadCustomPlugins returned nil error when InstantiatePlugin returned a nil plugin with no error — the defensive nil-check path must also fail closed")
	}
}

// TestLoadCustomPluginsSkipsEnterprisePluginFailure: enterprise plugins keep
// today's skip-and-continue behavior and never trip the fail-closed error.
func TestLoadCustomPluginsSkipsEnterprisePluginFailure(t *testing.T) {
	logger = noopTestLogger{}
	path := "/no/such/path/datadog.so"
	config := &lib.Config{
		PluginLoader: plugins.NewSharedObjectPluginLoader(nil),
		PluginConfigs: []*schemas.PluginConfig{
			// "datadog" is a member of enterprisePlugins (server.go) — its
			// failure must stay silently skipped, unchanged from upstream.
			{Enabled: true, Name: "datadog", Path: &path},
		},
	}
	server := &BifrostHTTPServer{Config: config}

	if err := server.loadCustomPlugins(context.Background()); err != nil {
		t.Fatalf("loadCustomPlugins returned an error for a failed ENTERPRISE plugin — the pre-existing silent-skip behavior for enterprisePlugins must be unchanged: %v", err)
	}
}

// TestLoadCustomPluginsReturnsNilWithNoEnabledCustomPlugins: with no enabled
// custom plugin configured, loadCustomPlugins still returns nil.
func TestLoadCustomPluginsReturnsNilWithNoEnabledCustomPlugins(t *testing.T) {
	logger = noopTestLogger{}
	path := "/no/such/path.so"
	config := &lib.Config{
		PluginLoader: plugins.NewSharedObjectPluginLoader(nil),
		PluginConfigs: []*schemas.PluginConfig{
			// Disabled: the loop's disabled-plugin branch runs (a verify
			// call against the same missing path) but must never count
			// toward failureCount — only an ENABLED plugin's failure does.
			{Enabled: false, Name: "capture", Path: &path},
		},
	}
	server := &BifrostHTTPServer{Config: config}

	if err := server.loadCustomPlugins(context.Background()); err != nil {
		t.Fatalf("loadCustomPlugins returned an error with zero enabled custom plugins (a disabled plugin only): %v", err)
	}
}

// nilReturningLoader is a minimal fake plugins.PluginLoader whose LoadPlugin
// deliberately returns (nil, nil) — a shape the real SharedObjectPluginLoader
// never produces on its own — needed to directly exercise loadCustomPlugins'
// defensive "plugin == nil but err == nil" branch without depending on an
// upstream implementation quirk to reach it.
type nilReturningLoader struct{}

// LoadPlugin returns (nil, nil) to reach loadCustomPlugins' nil-plugin branch.
func (nilReturningLoader) LoadPlugin(path string, config any) (schemas.BasePlugin, error) {
	return nil, nil
}

// VerifyBasePlugin always succeeds so the loader is reached.
func (nilReturningLoader) VerifyBasePlugin(path string) (string, error) {
	return "", errors.New("not implemented in fake loader")
}
