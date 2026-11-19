package server

import (
	"testing"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/logstore"
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

// TestGovernancePluginConfigFollowsExcludeCachedTokens pins that every load of the governance plugin, a reload from
// the stored plugin config included, reads client.exclude_cached_tokens_from_rate_limits live. The stored config
// cannot carry the getter, and a missing getter means off, so a reload would otherwise drop the setting until restart.
func TestGovernancePluginConfigFollowsExcludeCachedTokens(t *testing.T) {
	bifrostConfig := &lib.Config{ClientConfig: &configstore.ClientConfig{ExcludeCachedTokensFromRateLimits: true}}
	// A stored plugin config, as a PUT /api/plugins/governance reload decodes it.
	for _, stored := range []any{map[string]any{}, `{"is_vk_mandatory":true}`} {
		got, err := governancePluginConfig(stored, bifrostConfig)
		if err != nil {
			t.Fatalf("governancePluginConfig(%v): %v", stored, err)
		}
		if got.ExcludeCachedTokensFromRateLimits == nil {
			t.Fatalf("stored %v: the getter is not attached", stored)
		}
		bifrostConfig.ClientConfig.ExcludeCachedTokensFromRateLimits = true
		if !got.ExcludeCachedTokensFromRateLimits() {
			t.Errorf("stored %v: getter = false with the setting on", stored)
		}
		bifrostConfig.ClientConfig.ExcludeCachedTokensFromRateLimits = false
		if got.ExcludeCachedTokensFromRateLimits() {
			t.Errorf("stored %v: getter = true after the setting was turned off", stored)
		}
	}
}
