package server

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/plugins/heimdall"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
)

// TestLoggingPluginConfigTakesCaptureClientIPFromLogsStore pins that every load of the logging plugin, a reload from the
// stored plugin config included, follows logs_store.capture_client_ip rather than whatever the plugin config carries.
func TestLoggingPluginConfigTakesCaptureClientIPFromLogsStore(t *testing.T) {
	for _, capture := range []bool{true, false} {
		bifrostConfig := &lib.Config{LogsStoreConfig: &logstore.Config{CaptureClientIP: capture}}
		// A stored plugin config, as a PUT /api/plugins/logging reload decodes it: no capture_client_ip, or a stale one.
		for _, stored := range []map[string]any{{}, {"capture_client_ip": !capture}} {
			got, err := loggingPluginConfig(stored, bifrostConfig)
			if err != nil {
				t.Fatalf("loggingPluginConfig(%v): %v", stored, err)
			}
			if got.CaptureClientIP != capture {
				t.Errorf("logs_store capture=%t, stored %v: CaptureClientIP = %t, want %t", capture, stored, got.CaptureClientIP, capture)
			}
		}
	}
	got, err := loggingPluginConfig(map[string]any{"capture_client_ip": true}, &lib.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if got.CaptureClientIP {
		t.Error("without a logs_store config, capture stays off")
	}
}

// TestLoadBuiltinPlugin_Heimdall pins that the heimdall built-in is known to the loader, says what it lacks, takes no
// config as the defaults, reads a stored config, and rejects a bad one.
func TestLoadBuiltinPlugin_Heimdall(t *testing.T) {
	if !lib.IsBuiltinPlugin(heimdall.PluginName) {
		t.Fatal("heimdall is not in the built-in plugin list")
	}
	// The server sets the package logger before it loads plugins.
	prev := logger
	logger = bifrost.NewDefaultLogger(schemas.LogLevelError)
	t.Cleanup(func() { logger = prev })
	ctx := context.Background()

	// Heimdall reads the logs and keeps runs in the config store; without them it says so.
	if _, err := loadBuiltinPlugin(ctx, heimdall.PluginName, nil, &lib.Config{}); err == nil || !strings.Contains(err.Error(), "log store") {
		t.Errorf("no stores: err = %v, want one that names the missing log store", err)
	}

	dir := t.TempDir()
	configStore, err := configstore.NewConfigStore(ctx, &configstore.Config{Enabled: true, Type: configstore.ConfigStoreTypeSQLite,
		Config: &configstore.SQLiteConfig{Path: filepath.Join(dir, "config.db")}}, logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = configStore.Close(ctx) })
	logsStore, err := logstore.NewLogStore(ctx, &logstore.Config{Enabled: true, Type: logstore.LogStoreTypeSQLite,
		Config: &logstore.SQLiteConfig{Path: filepath.Join(dir, "logs.db")}}, logger)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &lib.Config{ConfigStore: configStore, LogsStore: logsStore}

	if _, err := loadBuiltinPlugin(ctx, heimdall.PluginName, nil, &lib.Config{ConfigStore: configStore}); err == nil {
		t.Error("no log store: expected an error")
	}
	if _, err := loadBuiltinPlugin(ctx, heimdall.PluginName, nil, &lib.Config{LogsStore: logsStore}); err == nil {
		t.Error("no config store: expected an error")
	}

	for name, pluginConfig := range map[string]any{
		"no config":     nil,
		"stored map":    map[string]any{"max_concurrency": 2, "call_timeout": "30s", "defaults": map[string]any{"margin": 0.2}},
		"typed config":  &heimdall.Config{MaxSessions: 10},
		"empty map":     map[string]any{},
		"json document": `{"max_steps": 5}`,
	} {
		plugin, err := loadBuiltinPlugin(ctx, heimdall.PluginName, pluginConfig, cfg)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if plugin.GetName() != heimdall.PluginName {
			t.Errorf("%s: plugin name = %q", name, plugin.GetName())
		}
		if _, ok := plugin.(schemas.LLMPlugin); !ok {
			t.Errorf("%s: plugin does not implement LLMPlugin", name)
		}
		_ = plugin.Cleanup()
	}
	if _, err := loadBuiltinPlugin(ctx, heimdall.PluginName, map[string]any{"call_timeout": "never"}, cfg); err == nil {
		t.Error("expected an error for an invalid call_timeout")
	}
	if _, err := loadBuiltinPlugin(ctx, heimdall.PluginName, map[string]any{"defaults": map[string]any{"margin": 7}}, cfg); err == nil {
		t.Error("expected an error for an invalid default margin")
	}
	if _, err := loadBuiltinPlugin(ctx, heimdall.PluginName, 42, cfg); err == nil {
		t.Error("expected an error for a config of the wrong type")
	}
}
