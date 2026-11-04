package server

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/plugins/promptcompression"
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

// TestLoadBuiltinPluginsRegistersPromptCompression pins that a prompt_compression entry in
// PluginConfigs is picked up by the built-in loader, and that a disabled entry is only marked
// disabled. A bare entry with no config block must be enough: every field has a default.
func TestLoadBuiltinPluginsRegistersPromptCompression(t *testing.T) {
	previousLogger := logger
	logger = noopTestLogger{}
	t.Cleanup(func() { logger = previousLogger })

	t.Run("enabled entry registers the plugin", func(t *testing.T) {
		s := &BifrostHTTPServer{Ctx: schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), Config: &lib.Config{
			ClientConfig:  &configstore.ClientConfig{},
			PluginConfigs: []*schemas.PluginConfig{{Name: promptcompression.PluginName, Enabled: true}},
		}}
		if err := s.loadBuiltinPlugins(context.Background()); err != nil {
			t.Fatalf("loadBuiltinPlugins: %v", err)
		}
		if !s.Config.IsPluginLoaded(promptcompression.PluginName) {
			status, _ := s.Config.GetPluginStatusByName(promptcompression.PluginName)
			t.Fatalf("%s is not loaded: %+v", promptcompression.PluginName, status)
		}
		status, ok := s.Config.GetPluginStatusByName(promptcompression.PluginName)
		if !ok || status.Status != schemas.PluginStatusActive {
			t.Fatalf("status = %+v (found=%t), want active", status, ok)
		}
	})

	t.Run("disabled entry is marked disabled", func(t *testing.T) {
		s := &BifrostHTTPServer{Ctx: schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), Config: &lib.Config{
			ClientConfig:  &configstore.ClientConfig{},
			PluginConfigs: []*schemas.PluginConfig{{Name: promptcompression.PluginName, Enabled: false}},
		}}
		// markPluginDisabled only flips an existing status entry, which startup seeds from the
		// plugin list before the loader runs.
		s.Config.UpdatePluginOverallStatus(promptcompression.PluginName, promptcompression.PluginName, schemas.PluginStatusActive, nil, nil)
		if err := s.loadBuiltinPlugins(context.Background()); err != nil {
			t.Fatalf("loadBuiltinPlugins: %v", err)
		}
		if s.Config.IsPluginLoaded(promptcompression.PluginName) {
			t.Fatalf("%s must not be loaded when disabled", promptcompression.PluginName)
		}
		status, ok := s.Config.GetPluginStatusByName(promptcompression.PluginName)
		if !ok || status.Status != schemas.PluginStatusDisabled {
			t.Fatalf("status = %+v (found=%t), want disabled", status, ok)
		}
	})
}
