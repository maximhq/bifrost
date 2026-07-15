package lib

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
)

func assertRefreshInterval(t *testing.T, label string, got, want *int64) {
	t.Helper()
	if (got == nil) != (want == nil) {
		t.Fatalf("%s: expected nil=%v, got nil=%v", label, want == nil, got == nil)
	}
	if got != nil && want != nil && *got != *want {
		t.Fatalf("%s: expected %d, got %d", label, *want, *got)
	}
}

func ptrInt64ForRefreshIntervalTest(v int64) *int64 { return &v }

// TestClampListModelsRefreshInterval verifies config.json values below the
// shared configstore.MinListModelsRefreshIntervalSec floor are disabled
// (set to nil) rather than silently honored — the HTTP API/UI reject such
// values outright, and config-file loading must not be a way around that.
func TestClampListModelsRefreshInterval(t *testing.T) {
	cases := []struct {
		name    string
		in      *int64
		wantNil bool
	}{
		{"nil left alone", nil, true},
		{"zero left alone (already disabled)", ptrInt64ForRefreshIntervalTest(0), false}, // not clamped: 0 is not > 0
		{"below floor is disabled", ptrInt64ForRefreshIntervalTest(5), true},
		{"at floor is kept", ptrInt64ForRefreshIntervalTest(configstore.MinListModelsRefreshIntervalSec), false},
		{"above floor is kept", ptrInt64ForRefreshIntervalTest(3600), false},
	}
	SetLogger(&testLogger{})
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &configstore.ProviderConfig{ListModelsRefreshIntervalSec: tc.in}
			clampListModelsRefreshInterval(cfg, schemas.OpenAI)
			gotNil := cfg.ListModelsRefreshIntervalSec == nil
			if gotNil != tc.wantNil {
				t.Fatalf("input=%v: expected nil=%v, got nil=%v (value=%v)", tc.in, tc.wantNil, gotNil, cfg.ListModelsRefreshIntervalSec)
			}
			if !tc.wantNil && tc.in != nil && *cfg.ListModelsRefreshIntervalSec != *tc.in {
				t.Fatalf("expected unclamped value %d preserved, got %d", *tc.in, *cfg.ListModelsRefreshIntervalSec)
			}
		})
	}
}

// TestMergeProviderWithHash_PreservesRefreshIntervalOnHashMismatch guards
// against a real regression found in review: on a config.json hash mismatch
// (any field in the file changed), the file's parsed config used to replace
// the DB-loaded config wholesale — silently wiping a UI/API-configured
// list_models_refresh_interval_sec that config.json itself never declares.
func TestMergeProviderWithHash_PreservesRefreshIntervalOnHashMismatch(t *testing.T) {
	SetLogger(&testLogger{})
	existingInterval := ptrInt64ForRefreshIntervalTest(120)
	store := map[schemas.ModelProvider]configstore.ProviderConfig{
		schemas.OpenAI: {ConfigHash: "old-hash", ListModelsRefreshIntervalSec: existingInterval},
	}

	// File changed something unrelated (new hash), and doesn't declare the
	// refresh interval at all (nil) — simulating a typical config.json.
	fileCfg := configstore.ProviderConfig{ConfigHash: "new-hash"}
	mergeProviderWithHash(schemas.OpenAI, fileCfg, store)

	assertRefreshInterval(t, "hash-mismatch merge", store[schemas.OpenAI].ListModelsRefreshIntervalSec, existingInterval)
}

// TestMergeProviderWithHash_FileValueWinsWhenDeclared ensures the file's
// value is still respected when config.json explicitly declares the field —
// preservation only kicks in when the file is silent about it.
func TestMergeProviderWithHash_FileValueWinsWhenDeclared(t *testing.T) {
	SetLogger(&testLogger{})
	newInterval := ptrInt64ForRefreshIntervalTest(60)
	store := map[schemas.ModelProvider]configstore.ProviderConfig{
		schemas.OpenAI: {ConfigHash: "old-hash", ListModelsRefreshIntervalSec: ptrInt64ForRefreshIntervalTest(120)},
	}

	fileCfg := configstore.ProviderConfig{ConfigHash: "new-hash", ListModelsRefreshIntervalSec: newInterval}
	mergeProviderWithHash(schemas.OpenAI, fileCfg, store)

	assertRefreshInterval(t, "hash-mismatch merge with declared value", store[schemas.OpenAI].ListModelsRefreshIntervalSec, newInterval)
}

// TestProcessAuthoritativeProvider_PreservesRefreshInterval covers the same
// regression for the source_of_truth=config.json path.
func TestProcessAuthoritativeProvider_PreservesRefreshInterval(t *testing.T) {
	SetLogger(&testLogger{})
	existingInterval := ptrInt64ForRefreshIntervalTest(90)
	existingCfg := configstore.ProviderConfig{ListModelsRefreshIntervalSec: existingInterval}
	providers := map[schemas.ModelProvider]configstore.ProviderConfig{}

	fileCfg := configstore.ProviderConfig{}
	processAuthoritativeProvider("openai", fileCfg, existingCfg, true, providers)

	assertRefreshInterval(t, "authoritative merge", providers[schemas.OpenAI].ListModelsRefreshIntervalSec, existingInterval)
}
