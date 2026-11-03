package datasheet

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

// Two datasheet rows that fold onto the same stored key (model, provider, mode): extractModelName
// drops the "bedrock_mantle/" prefix and normalizeProvider maps both providers to "bedrock".
const bedrockMantleCollisionDatasheet = `{
  "anthropic.claude-sonnet-5-5": {"provider": "bedrock", "mode": "chat",
    "input_cost_per_token": 2e-06, "output_cost_per_token": 1e-05},
  "bedrock_mantle/anthropic.claude-sonnet-5-5": {"provider": "bedrock_mantle", "mode": "chat",
    "input_cost_per_token": 2.2e-06, "output_cost_per_token": 1.1e-05}
}`

func deref(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

// syncRepeats makes the check meaningful: a wrong implementation picks the winner from Go's
// randomized map order, so one run passes by luck half the time, and thirty never do.
const syncRepeats = 30

// TestSyncFromURLBedrockRowBeatsMantleRow pins #8489: when a bedrock row and a bedrock_mantle row
// collapse to one stored key, the stored price must be the bedrock row's on every sync, not
// whichever row the map iteration returned first.
func TestSyncFromURLBedrockRowBeatsMantleRow(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	ctx := context.Background()
	cs, err := configstore.NewConfigStore(ctx, &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "config.db")},
	}, logger)
	if err != nil {
		t.Fatalf("failed to create config store: %v", err)
	}
	t.Cleanup(func() {
		if err := cs.Close(ctx); err != nil {
			t.Errorf("failed to close config store: %v", err)
		}
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(bedrockMantleCollisionDatasheet))
	}))
	defer srv.Close()
	store := New(cs, logger, Config{URL: srv.URL, ModelParametersURL: srv.URL})

	for i := 0; i < syncRepeats; i++ {
		if err := store.SyncFromURL(ctx); err != nil {
			t.Fatalf("sync %d: %v", i, err)
		}
		rows, err := cs.GetModelPrices(ctx)
		if err != nil {
			t.Fatalf("sync %d: reading prices: %v", i, err)
		}
		var found int
		for _, row := range rows {
			if row.Model != "anthropic.claude-sonnet-5-5" || row.Provider != "bedrock" || row.Mode != "chat" {
				continue
			}
			found++
			if row.InputCostPerToken == nil || *row.InputCostPerToken != 2e-06 {
				t.Fatalf("sync %d stored input cost %v, want 2e-06 from the bedrock row", i, deref(row.InputCostPerToken))
			}
		}
		if found != 1 {
			t.Fatalf("sync %d: %d stored rows for the collapsed key, want 1", i, found)
		}
	}
}

// TestApplyPricingDataBedrockRowBeatsMantleRow is the same rule for the no-config-store path,
// where the later row in map order used to overwrite the earlier one.
func TestApplyPricingDataBedrockRowBeatsMantleRow(t *testing.T) {
	entries := map[string]Entry{}
	in, out := 2e-06, 1e-05
	inMantle, outMantle := 2.2e-06, 1.1e-05
	entries["anthropic.claude-sonnet-5-5"] = Entry{Provider: "bedrock", Mode: "chat", InputCostPerToken: &in, OutputCostPerToken: &out}
	entries["bedrock_mantle/anthropic.claude-sonnet-5-5"] = Entry{Provider: "bedrock_mantle", Mode: "chat", InputCostPerToken: &inMantle, OutputCostPerToken: &outMantle}

	store := NewTestStore(nil)
	for i := 0; i < syncRepeats; i++ {
		store.applyPricingData(entries)
		store.mu.RLock()
		row, ok := store.pricingData[makeKey("anthropic.claude-sonnet-5-5", "bedrock", "chat")]
		store.mu.RUnlock()
		if !ok || row.InputCostPerToken == nil || *row.InputCostPerToken != 2e-06 {
			t.Fatalf("apply %d stored input cost %v, want 2e-06 from the bedrock row", i, deref(row.InputCostPerToken))
		}
	}
}

// warnRecorder captures Warn and Debug output; the embedded nil Logger covers the methods a test never reaches.
type warnRecorder struct {
	schemas.Logger
	warns  []string
	debugs []string
}

func (r *warnRecorder) Warn(format string, args ...any) {
	r.warns = append(r.warns, fmt.Sprintf(format, args...))
}

func (r *warnRecorder) Debug(format string, args ...any) {
	r.debugs = append(r.debugs, fmt.Sprintf(format, args...))
}

func costEntry(provider string, input float64) Entry {
	out := input * 5
	return Entry{Provider: provider, Mode: "chat", InputCostPerToken: &input, OutputCostPerToken: &out}
}

// With no exact-provider row among the candidates, the smaller datasheet key wins, whatever the
// map order, and a fold between different prices is reported once per sync.
func TestResolvePricingRowsTieBreakIsOrderIndependentAndWarns(t *testing.T) {
	entries := map[string]Entry{
		"bedrock_mantle/m": costEntry("bedrock_mantle", 3e-06),
		"bedrock_x/m":      costEntry("bedrock_x", 2e-06),
		"bedrock_a/m":      costEntry("bedrock_a", 1e-06),
	}
	for i := 0; i < syncRepeats; i++ {
		rec := &warnRecorder{}
		s := NewTestStore(nil)
		s.logger = rec
		rows := s.resolvePricingRows(entries)
		if len(rows) != 1 || rows[0].InputCostPerToken == nil || *rows[0].InputCostPerToken != 1e-06 {
			t.Fatalf("run %d: rows=%+v, want one row at 1e-06 from bedrock_a/m", i, rows)
		}
		if len(rec.warns) != 1 || !strings.Contains(rec.warns[0], "bedrock_a/m kept over bedrock_mantle/m, bedrock_x/m") {
			t.Fatalf("run %d: warnings=%q, want one naming the kept and dropped rows", i, rec.warns)
		}
	}
}

// Rows folded onto one key at the same price lose nothing, so they stay quiet.
func TestResolvePricingRowsSamePriceFoldIsSilent(t *testing.T) {
	rec := &warnRecorder{}
	s := NewTestStore(nil)
	s.logger = rec
	rows := s.resolvePricingRows(map[string]Entry{
		"m":                costEntry("bedrock", 2e-06),
		"bedrock_mantle/m": costEntry("bedrock_mantle", 2e-06),
		"anthropic.opus":   costEntry("bedrock", 5e-06),
	})
	if len(rows) != 2 || len(rec.warns) != 0 {
		t.Fatalf("rows=%d warnings=%q, want 2 rows and no warning", len(rows), rec.warns)
	}
}

// Rows that agree on list prices but differ on another cost field still lose pricing in the fold,
// so the fold is reported.
func TestResolvePricingRowsNonListPriceFoldWarns(t *testing.T) {
	rec := &warnRecorder{}
	s := NewTestStore(nil)
	s.logger = rec
	a, b := costEntry("bedrock_mantle", 2e-06), costEntry("bedrock_x", 2e-06)
	cacheA, cacheB := 2e-07, 3e-07
	a.CacheReadInputTokenCost, b.CacheReadInputTokenCost = &cacheA, &cacheB
	rows := s.resolvePricingRows(map[string]Entry{"bedrock_mantle/m": a, "bedrock_x/m": b})
	if len(rows) != 1 || len(rec.warns) != 1 || !strings.Contains(rec.warns[0], "bedrock_mantle/m kept over bedrock_x/m") {
		t.Fatalf("rows=%d warnings=%q, want 1 row and one warning for the cache-read price difference", len(rows), rec.warns)
	}
}

// When the exact-provider rule picks the survivor the outcome is intended, so it is logged at Debug
// and never as a Warn on every sync.
func TestResolvePricingRowsExactRuleFoldLogsDebugNotWarn(t *testing.T) {
	rec := &warnRecorder{}
	s := NewTestStore(nil)
	s.logger = rec
	rows := s.resolvePricingRows(map[string]Entry{
		"m":                costEntry("bedrock", 2e-06),
		"bedrock_mantle/m": costEntry("bedrock_mantle", 2.2e-06),
	})
	if len(rows) != 1 || len(rec.warns) != 0 {
		t.Fatalf("rows=%d warnings=%q, want 1 row and no warning", len(rows), rec.warns)
	}
	if len(rec.debugs) != 1 || !strings.Contains(rec.debugs[0], "m kept over bedrock_mantle/m") {
		t.Fatalf("debugs=%q, want one debug line naming the kept and dropped rows", rec.debugs)
	}
}

// BenchmarkResolvePricingRows is the whole per-sync dedup for a datasheet of realistic size.
func BenchmarkResolvePricingRows(b *testing.B) {
	entries := make(map[string]Entry, 5000)
	for i := 0; i < 5000; i++ {
		in := float64(i+1) * 1e-09
		entries[fmt.Sprintf("openai/model-%d", i)] = costEntry("openai", in)
	}
	for i := 0; i < 50; i++ { // folded twins, as bedrock_mantle rows are
		in := float64(i+1) * 1e-07
		entries[fmt.Sprintf("bedrock_mantle/anthropic.m-%d", i)] = costEntry("bedrock_mantle", in*1.1)
		entries[fmt.Sprintf("anthropic.m-%d", i)] = costEntry("bedrock", in)
	}
	s := NewTestStore(nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = s.resolvePricingRows(entries)
	}
}

// The exact-provider rule must decide on its own, not ride on key order (#8492 review): here the
// bedrock row has the LARGER datasheet key, so only the rule keeps its price. Real datasheet keys
// such as "openai.*" and "nvidia.*" sort after "bedrock_mantle/".
func TestResolvePricingRowsExactProviderBeatsSmallerKey(t *testing.T) {
	entries := map[string]Entry{
		"openai.x":                costEntry("bedrock", 2e-06),
		"bedrock_mantle/openai.x": costEntry("bedrock_mantle", 3e-06),
	}
	for i := 0; i < syncRepeats; i++ {
		rows := NewTestStore(nil).resolvePricingRows(entries)
		if len(rows) != 1 || rows[0].InputCostPerToken == nil || *rows[0].InputCostPerToken != 2e-06 {
			t.Fatalf("run %d: got %d rows (first input cost %v), want the bedrock row (2e-06) although its key sorts after bedrock_mantle/", i, len(rows), firstInputCost(rows))
		}
	}
}

func firstInputCost(rows []configstoreTables.TableModelPricing) any {
	if len(rows) == 0 || rows[0].InputCostPerToken == nil {
		return nil
	}
	return *rows[0].InputCostPerToken
}
