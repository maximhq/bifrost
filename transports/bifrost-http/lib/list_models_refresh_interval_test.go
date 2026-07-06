package lib

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
)

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
