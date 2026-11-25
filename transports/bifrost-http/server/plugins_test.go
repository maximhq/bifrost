package server

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
)

// TestLoggingPluginConfigTakesCaptureClientIPFromLogsStore pins that every load of the logging plugin, a reload from the
// stored plugin config included, follows logs_store.capture_client_ip rather than whatever the plugin config carries.
func TestLoggingPluginConfigTakesCaptureClientIPFromLogsStore(t *testing.T) {
	for _, capture := range []bool{true, false} {
		bifrostConfig := &lib.Config{LogsStoreConfig: &logstore.Config{CaptureClientIP: capture}}
		// A stored plugin config, as a reload decodes it: no capture_client_ip, or a stale one.
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

// TestGovernanceLogTimestampPrecisionFollowsTheLogsStore pins that governance compares charges
// with resets at ClickHouse's millisecond timestamps only when a ClickHouse logs store is open.
func TestGovernanceLogTimestampPrecisionFollowsTheLogsStore(t *testing.T) {
	open := struct{ logstore.LogStore }{}
	for _, tc := range []struct {
		name   string
		config *lib.Config
		want   time.Duration
	}{
		{"clickhouse", &lib.Config{LogsStore: open, LogsStoreConfig: &logstore.Config{Type: logstore.LogStoreTypeClickHouse}}, time.Millisecond},
		{"postgres", &lib.Config{LogsStore: open, LogsStoreConfig: &logstore.Config{Type: logstore.LogStoreTypePostgres}}, 0},
		{"clickhouse not open", &lib.Config{LogsStoreConfig: &logstore.Config{Type: logstore.LogStoreTypeClickHouse}}, 0},
		{"no logs store", &lib.Config{}, 0},
	} {
		if got := GovernanceLogTimestampPrecision(tc.config); got != tc.want {
			t.Errorf("%s: precision = %v, want %v", tc.name, got, tc.want)
		}
	}
}
