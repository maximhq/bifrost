package lib

// =============================================================================
// E2E Pricing Config Integration Tests — Issue #2312
//
// These tests simulate production scenarios end-to-end using:
//   - Real SQLite config stores (not mocks)
//   - Real log capture (not silent noop loggers)
//   - The actual initFrameworkConfig code path
//   - DB read → resolve → write lifecycle
//
// Run: go test ./transports/bifrost-http/lib/... -run TestPricingE2E -v
// =============================================================================

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework"
	configstore "github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Capturing logger — records every emitted log line for assertion
// =============================================================================

type capturingLogger struct {
	mu   sync.Mutex
	logs []string
}

func newCapturingLogger() *capturingLogger { return &capturingLogger{} }

func (l *capturingLogger) append(level, msg string, args ...any) {
	line := fmt.Sprintf("[%s] %s", level, fmt.Sprintf(msg, args...))
	l.mu.Lock()
	l.logs = append(l.logs, line)
	l.mu.Unlock()
}

func (l *capturingLogger) Debug(msg string, args ...any) { l.append("DEBUG", msg, args...) }
func (l *capturingLogger) Info(msg string, args ...any)  { l.append("INFO", msg, args...) }
func (l *capturingLogger) Warn(msg string, args ...any)  { l.append("WARN", msg, args...) }
func (l *capturingLogger) Error(msg string, args ...any) { l.append("ERROR", msg, args...) }
func (l *capturingLogger) Fatal(msg string, args ...any) { l.append("FATAL", msg, args...) }
func (l *capturingLogger) SetLevel(_ schemas.LogLevel)   {}
func (l *capturingLogger) SetOutputType(_ schemas.LoggerOutputType) {}
func (l *capturingLogger) LogHTTPRequest(_ schemas.LogLevel, _ string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

// hasLog returns true if any captured line contains all provided substrings.
func (l *capturingLogger) hasLog(substrings ...string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, line := range l.logs {
		match := true
		for _, s := range substrings {
			if !strings.Contains(line, s) {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// dump returns all captured log lines joined for test failure output.
func (l *capturingLogger) dump() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.logs, "\n")
}

// =============================================================================
// Helpers
// =============================================================================

// makeStore creates a real SQLite-backed configstore in a temp directory.
func makeStore(t *testing.T, dir string) configstore.ConfigStore {
	t.Helper()
	store, err := configstore.NewConfigStore(context.Background(), &configstore.Config{
		Enabled: true,
		Type:    configstore.ConfigStoreTypeSQLite,
		Config:  &configstore.SQLiteConfig{Path: filepath.Join(dir, "config.db")},
	}, &testLogger{})
	require.NoError(t, err, "failed to create SQLite configstore")
	t.Cleanup(func() { store.Close(context.Background()) })
	return store
}

// resolveWithCapture calls ResolveFrameworkPricingConfig with a capturing logger
// installed, then restores the previous logger.
func resolveWithCapture(
	t *testing.T,
	log *capturingLogger,
	dbConfig *configstoreTables.TableFrameworkConfig,
	fileConfig *framework.FrameworkConfig,
) (*configstoreTables.TableFrameworkConfig, *modelcatalog.Config, bool) {
	if t != nil {
		t.Helper()
	}
	prev := getLogger()
	SetLogger(log)
	defer SetLogger(prev)
	return ResolveFrameworkPricingConfig(dbConfig, fileConfig)
}

// initWithCapture calls initFrameworkConfig with a capturing logger installed.
func initWithCapture(
	t *testing.T,
	log *capturingLogger,
	store configstore.ConfigStore,
	frameworkCfg *framework.FrameworkConfig,
) *Config {
	t.Helper()
	cfg := &Config{ConfigStore: store}
	configData := &ConfigData{FrameworkConfig: frameworkCfg}
	prev := getLogger()
	SetLogger(log)
	defer SetLogger(prev)
	initFrameworkConfig(context.Background(), cfg, configData)
	return cfg
}

// getLogger returns the module-level logger var.
// This uses the same package-level var as SetLogger.
func getLogger() schemas.Logger { return logger }

// pt is a generic pointer helper.
func ptStr(s string) *string  { return &s }
func ptI64(n int64) *int64    { return &n }
func ptF64(f float64) *float64 { return &f }

// defaultSyncSecs is the production default converted to seconds.
var defaultSyncSecs = int64(modelcatalog.DefaultPricingSyncInterval.Seconds())

// =============================================================================
// STEP 2 — Baseline: no config.json, no DB → built-in defaults
// =============================================================================

func TestPricingE2E_Step2_Baseline(t *testing.T) {
	log := newCapturingLogger()

	tableOut, catalogOut, needsDBUpdate := resolveWithCapture(t, log, nil, nil)

	// Values must equal built-in defaults.
	require.Equal(t, modelcatalog.DefaultPricingURL, *tableOut.PricingURL,
		"pricing_url must be the built-in default")
	require.Equal(t, defaultSyncSecs, *tableOut.PricingSyncInterval,
		"pricing_sync_interval must be the built-in default (%d s)", defaultSyncSecs)
	require.Equal(t, *tableOut.PricingURL, *catalogOut.PricingURL)
	require.Equal(t, *tableOut.PricingSyncInterval, *catalogOut.PricingSyncInterval)

	// No DB update needed when there is no DB.
	require.False(t, needsDBUpdate, "no DB update should be needed for a fully absent config")

	// The resolution log must report both as "default".
	assert.True(t, log.hasLog("resolved pricing config", "source: default", "source: default"),
		"expected resolution log with default sources\n%s", log.dump())

	t.Logf("PASS — baseline: url=%q interval=%d s (both default)", *tableOut.PricingURL, *tableOut.PricingSyncInterval)
}

// =============================================================================
// STEP 3 — File config: valid values override defaults
// =============================================================================

func TestPricingE2E_Step3_FileConfigValid(t *testing.T) {
	log := newCapturingLogger()

	fileConfig := &framework.FrameworkConfig{
		Pricing: &modelcatalog.Config{
			PricingURL:          ptStr("https://example.com/pricing.json"),
			PricingSyncInterval: ptI64(7200),
		},
	}

	tableOut, catalogOut, needsDBUpdate := resolveWithCapture(t, log, nil, fileConfig)

	require.Equal(t, "https://example.com/pricing.json", *tableOut.PricingURL,
		"pricing_url must come from file config")
	require.Equal(t, int64(7200), *tableOut.PricingSyncInterval,
		"pricing_sync_interval must be 7200 s from file config")
	require.Equal(t, *tableOut.PricingURL, *catalogOut.PricingURL)
	require.Equal(t, *tableOut.PricingSyncInterval, *catalogOut.PricingSyncInterval)
	require.False(t, needsDBUpdate, "no DB update needed — no DB row exists yet")

	// No warnings about clamping or fallback should appear.
	assert.False(t, log.hasLog("WARN"), "unexpected WARN in file-config-valid case\n%s", log.dump())

	// Resolution log must reflect file source for both fields.
	assert.True(t, log.hasLog("resolved pricing config", "source: file"),
		"expected 'source: file' in resolution log\n%s", log.dump())

	// Debug logs must show file values.
	assert.True(t, log.hasLog("DEBUG", "pricing_url resolved from file"),
		"expected debug log for url resolved from file\n%s", log.dump())
	assert.True(t, log.hasLog("DEBUG", "pricing_sync_interval resolved from file"),
		"expected debug log for interval resolved from file\n%s", log.dump())

	t.Logf("PASS — file config: url=%q interval=%d s (both file)", *tableOut.PricingURL, *tableOut.PricingSyncInterval)
}

// =============================================================================
// STEP 4 — Minimum constraint validation
// =============================================================================

func TestPricingE2E_Step4A_TooLow_ClampsTo3600(t *testing.T) {
	log := newCapturingLogger()

	fileConfig := &framework.FrameworkConfig{
		Pricing: &modelcatalog.Config{PricingSyncInterval: ptI64(100)},
	}

	tableOut, catalogOut, _ := resolveWithCapture(t, log, nil, fileConfig)

	require.Equal(t, modelcatalog.MinimumPricingSyncIntervalSec, *tableOut.PricingSyncInterval,
		"too-low interval must be clamped to minimum 3600 s")
	require.Equal(t, *tableOut.PricingSyncInterval, *catalogOut.PricingSyncInterval)

	assert.True(t, log.hasLog("WARN", "below minimum", "clamping to 3600"),
		"expected WARN clamping message\n%s", log.dump())

	t.Logf("PASS — too-low: 100 s clamped to %d s", *tableOut.PricingSyncInterval)
}

func TestPricingE2E_Step4B_Zero_IgnoredUseDefault(t *testing.T) {
	log := newCapturingLogger()

	fileConfig := &framework.FrameworkConfig{
		Pricing: &modelcatalog.Config{PricingSyncInterval: ptI64(0)},
	}

	tableOut, _, _ := resolveWithCapture(t, log, nil, fileConfig)

	require.Equal(t, defaultSyncSecs, *tableOut.PricingSyncInterval,
		"zero interval must be ignored; default must be used")

	assert.True(t, log.hasLog("WARN", "invalid"), "expected WARN about invalid zero value\n%s", log.dump())
	assert.False(t, log.hasLog("source: file"), "interval source must NOT be 'file' for rejected zero\n%s", log.dump())

	t.Logf("PASS — zero: ignored, default %d s applied", *tableOut.PricingSyncInterval)
}

func TestPricingE2E_Step4C_Negative_IgnoredUseDefault(t *testing.T) {
	log := newCapturingLogger()

	fileConfig := &framework.FrameworkConfig{
		Pricing: &modelcatalog.Config{PricingSyncInterval: ptI64(-500)},
	}

	tableOut, _, _ := resolveWithCapture(t, log, nil, fileConfig)

	require.Equal(t, defaultSyncSecs, *tableOut.PricingSyncInterval,
		"negative interval must be ignored; default must be used")

	assert.True(t, log.hasLog("WARN", "invalid"), "expected WARN about invalid negative value\n%s", log.dump())

	t.Logf("PASS — negative: ignored, default %d s applied", *tableOut.PricingSyncInterval)
}

// =============================================================================
// STEP 5 — Env variable resolution
// =============================================================================

func TestPricingE2E_Step5A_ValidEnvVar(t *testing.T) {
	t.Setenv("BIFROST_E2E_PRICING_URL", "https://env.example.com/pricing.json")
	log := newCapturingLogger()

	fileConfig := &framework.FrameworkConfig{
		Pricing: &modelcatalog.Config{PricingURL: ptStr("env.BIFROST_E2E_PRICING_URL")},
	}

	tableOut, catalogOut, _ := resolveWithCapture(t, log, nil, fileConfig)

	require.Equal(t, "https://env.example.com/pricing.json", *tableOut.PricingURL,
		"env.VAR prefix must be resolved to env value")
	require.Equal(t, *tableOut.PricingURL, *catalogOut.PricingURL)

	assert.False(t, log.hasLog("WARN"), "no WARN expected for valid env var\n%s", log.dump())

	t.Logf("PASS — valid env: resolved to %q", *tableOut.PricingURL)
}

func TestPricingE2E_Step5B_MissingEnvVar_PreservesLiteral(t *testing.T) {
	log := newCapturingLogger()

	rawURL := "env.BIFROST_E2E_PRICING_URL_DEFINITELY_NOT_SET_XYZ9999"

	fileConfig := &framework.FrameworkConfig{
		Pricing: &modelcatalog.Config{PricingURL: ptStr(rawURL)},
	}

	tableOut, catalogOut, _ := resolveWithCapture(t, log, nil, fileConfig)

	// Must preserve the literal "env.*" string — NOT silently fall back to default URL.
	require.Equal(t, rawURL, *tableOut.PricingURL,
		"missing env var must preserve the original 'env.*' string, not silently default")
	require.Equal(t, *tableOut.PricingURL, *catalogOut.PricingURL)

	assert.True(t, log.hasLog("WARN", "env variable not found", rawURL),
		"expected WARN about missing env var preserving original value\n%s", log.dump())

	// The key requirement: NOT replaced with default URL
	require.NotEqual(t, modelcatalog.DefaultPricingURL, *tableOut.PricingURL,
		"must NOT silently fall back to built-in default URL")

	t.Logf("PASS — missing env: preserved literal %q (not default URL)", *tableOut.PricingURL)
}

func TestPricingE2E_Step5C_EmbeddedEnvNotExpanded(t *testing.T) {
	t.Setenv("BIFROST_E2E_HOST", "host.example.com")
	log := newCapturingLogger()

	// URL that has env.VAR embedded mid-string — NOT a full-string "env." prefix.
	embeddedURL := "https://env.BIFROST_E2E_HOST/pricing.json"

	fileConfig := &framework.FrameworkConfig{
		Pricing: &modelcatalog.Config{PricingURL: ptStr(embeddedURL)},
	}

	tableOut, catalogOut, _ := resolveWithCapture(t, log, nil, fileConfig)

	require.Equal(t, embeddedURL, *tableOut.PricingURL,
		"URL that does not start with 'env.' must be treated as a literal — partial/embedded env refs are NOT expanded")
	require.Equal(t, *tableOut.PricingURL, *catalogOut.PricingURL)

	// No WARN expected — this is valid literal input.
	assert.False(t, log.hasLog("WARN"), "no WARN expected for non-prefixed URL\n%s", log.dump())

	// MUST NOT be expanded to host.example.com form.
	require.NotContains(t, *tableOut.PricingURL, "host.example.com",
		"embedded env ref must NOT be expanded")

	t.Logf("PASS — embedded env: returned verbatim %q", *tableOut.PricingURL)
}

// =============================================================================
// STEP 6 — Database precedence (real SQLite)
// =============================================================================

func TestPricingE2E_Step6A_DBOverridesFile(t *testing.T) {
	dir := t.TempDir()
	store := makeStore(t, dir)
	ctx := context.Background()

	// Seed DB with specific values.
	dbInterval := int64(3600)
	dbURL := "https://db.example.com/pricing.json"
	require.NoError(t, store.UpdateFrameworkConfig(ctx, &configstoreTables.TableFrameworkConfig{
		PricingURL:          &dbURL,
		PricingSyncInterval: &dbInterval,
	}))

	log := newCapturingLogger()

	// File wants 7200s and a different URL.
	fileConfig := &framework.FrameworkConfig{
		Pricing: &modelcatalog.Config{
			PricingURL:          ptStr("https://file.example.com/pricing.json"),
			PricingSyncInterval: ptI64(7200),
		},
	}

	dbConfig, err := store.GetFrameworkConfig(ctx)
	require.NoError(t, err)

	tableOut, catalogOut, _ := resolveWithCapture(t, log, dbConfig, fileConfig)

	// DB must win over file for both fields.
	require.Equal(t, dbURL, *tableOut.PricingURL, "DB url must override file url")
	require.Equal(t, int64(3600), *tableOut.PricingSyncInterval, "DB interval must override file interval")
	require.Equal(t, *tableOut.PricingURL, *catalogOut.PricingURL)
	require.Equal(t, *tableOut.PricingSyncInterval, *catalogOut.PricingSyncInterval)

	// Override log must show both the file value and the DB value.
	assert.True(t, log.hasLog("INFO", "pricing_url overridden by DB"),
		"expected override log for url\n%s", log.dump())
	assert.True(t, log.hasLog("INFO", "pricing_sync_interval overridden by DB"),
		"expected override log for interval\n%s", log.dump())

	// Resolution log must report DB as source.
	assert.True(t, log.hasLog("resolved pricing config", "source: db"),
		"expected resolution log with 'source: db'\n%s", log.dump())

	t.Logf("PASS — DB override: url=%q interval=%d s (both db)", *tableOut.PricingURL, *tableOut.PricingSyncInterval)
}

func TestPricingE2E_Step6B_DBCorruptedZero_FileWins_DBBackfilled(t *testing.T) {
	dir := t.TempDir()
	store := makeStore(t, dir)
	ctx := context.Background()

	// Simulate pre-fix bug: interval was serialized as nanoseconds → stored as 0.
	corruptedInterval := int64(0)
	dbURL := "https://db.example.com/pricing.json"
	require.NoError(t, store.UpdateFrameworkConfig(ctx, &configstoreTables.TableFrameworkConfig{
		PricingURL:          &dbURL,
		PricingSyncInterval: &corruptedInterval,
	}))

	log := newCapturingLogger()

	fileConfig := &framework.FrameworkConfig{
		Pricing: &modelcatalog.Config{
			PricingSyncInterval: ptI64(7200),
		},
	}

	dbConfig, err := store.GetFrameworkConfig(ctx)
	require.NoError(t, err)

	tableOut, catalogOut, needsDBUpdate := resolveWithCapture(t, log, dbConfig, fileConfig)

	// Corrupted DB interval must be rejected; file must win.
	require.Equal(t, int64(7200), *tableOut.PricingSyncInterval,
		"file interval must win when DB has corrupted zero value")
	require.Equal(t, int64(7200), *catalogOut.PricingSyncInterval)

	// DB URL is valid and must still be used.
	require.Equal(t, dbURL, *tableOut.PricingURL,
		"valid DB url must still be respected even when interval is corrupted")

	// DB must be flagged for backfill.
	require.True(t, needsDBUpdate,
		"needsDBUpdate must be true so the healed value is persisted to DB")

	// WARN must explain the corruption.
	assert.True(t, log.hasLog("WARN", "corrupted", "backfilling"),
		"expected WARN about corrupted DB value and backfill\n%s", log.dump())

	t.Logf("PASS — DB corrupted (0): file %d s wins, needsDBUpdate=true", *tableOut.PricingSyncInterval)
}

func TestPricingE2E_Step6C_DBNullFields_FileUsed_DBBackfilled(t *testing.T) {
	dir := t.TempDir()
	store := makeStore(t, dir)
	ctx := context.Background()

	// Insert a DB row with NULL fields (mimics an incomplete migration or first boot).
	require.NoError(t, store.UpdateFrameworkConfig(ctx, &configstoreTables.TableFrameworkConfig{
		PricingURL:          nil,
		PricingSyncInterval: nil,
	}))

	log := newCapturingLogger()

	fileConfig := &framework.FrameworkConfig{
		Pricing: &modelcatalog.Config{
			PricingURL:          ptStr("https://file.example.com/pricing.json"),
			PricingSyncInterval: ptI64(3600),
		},
	}

	dbConfig, err := store.GetFrameworkConfig(ctx)
	require.NoError(t, err)

	tableOut, catalogOut, needsDBUpdate := resolveWithCapture(t, log, dbConfig, fileConfig)

	// File config must be used when DB fields are NULL.
	require.Equal(t, "https://file.example.com/pricing.json", *tableOut.PricingURL,
		"file url must be used when DB url field is NULL")
	require.Equal(t, int64(3600), *tableOut.PricingSyncInterval,
		"file interval must be used when DB interval field is NULL")
	require.Equal(t, *tableOut.PricingURL, *catalogOut.PricingURL)
	require.Equal(t, *tableOut.PricingSyncInterval, *catalogOut.PricingSyncInterval)

	// DB must be scheduled for backfill.
	require.True(t, needsDBUpdate,
		"needsDBUpdate must be true so file values are persisted to DB")

	t.Logf("PASS — DB NULL fields: file config used, needsDBUpdate=true")
}

// =============================================================================
// STEP 7 — modelcatalog.Init stores the resolved interval correctly
//           (verifies the Init → getPricingSyncInterval path)
// =============================================================================

// noSyncFunc is a shouldSyncPricingFunc that prevents real HTTP requests during tests.
var noSyncFunc = func(_ context.Context) bool { return false }

func TestPricingE2E_Step7_RuntimeInterval_StoredCorrectly(t *testing.T) {
	dir := t.TempDir()
	store := makeStore(t, dir)
	ctx := context.Background()

	clg := newCapturingLogger()
	prevLogger := getLogger()
	SetLogger(clg)
	defer SetLogger(prevLogger)

	// Scenario A: interval=3600 s stored in modelcatalog
	// noSyncFunc prevents real HTTP requests to pricing URL during this unit test.
	syncSeconds := int64(3600)
	cfg := &modelcatalog.Config{
		PricingURL:          ptStr("https://example.com/pricing.json"),
		PricingSyncInterval: &syncSeconds,
	}
	mc, err := modelcatalog.Init(ctx, cfg, store, noSyncFunc, clg)
	require.NoError(t, err)
	defer mc.Cleanup()

	// The startup Info log must reflect the correct duration.
	assert.True(t, clg.hasLog("INFO", "pricing sync interval set to", "1h0m0s"),
		"expected startup log: interval=1h0m0s\n%s", clg.dump())

	// Verify the scheduler model: syncWorkerTickerPeriod (1h) is also logged.
	assert.True(t, clg.hasLog("INFO", "scheduler checks every", "1h0m0s"),
		"expected startup log: scheduler ticker=1h0m0s\n%s", clg.dump())

	t.Logf("PASS — Init: interval=3600 s logged as 1h0m0s, ticker=1h0m0s")
}

func TestPricingE2E_Step7_RuntimeInterval_24h_Default(t *testing.T) {
	dir := t.TempDir()
	store := makeStore(t, dir)
	ctx := context.Background()

	clg := newCapturingLogger()
	prevLogger := getLogger()
	SetLogger(clg)
	defer SetLogger(prevLogger)

	// Nil PricingURL: defaults apply. noSyncFunc prevents real HTTP requests.
	cfg := &modelcatalog.Config{}
	mc, err := modelcatalog.Init(ctx, cfg, store, noSyncFunc, clg)
	require.NoError(t, err)
	defer mc.Cleanup()

	// Must show 24h default.
	assert.True(t, clg.hasLog("INFO", "pricing sync interval set to", "24h0m0s"),
		"expected startup log: interval=24h0m0s (default)\n%s", clg.dump())

	t.Logf("PASS — Init: nil config → interval=24h0m0s (default)")
}

func TestPricingE2E_Step7_RuntimeInterval_BelowTickerHasNoEffect(t *testing.T) {
	// Demonstrates the scheduler model:
	// pricingSyncInterval < syncWorkerTickerPeriod (1h) is clamped to minimum 3600s
	// by ResolveFrameworkPricingConfig BEFORE reaching Init, so pricingSyncInterval
	// can never be set below 3600s (= syncWorkerTickerPeriod) in production.
	// In that case the scheduler's effective check period IS the minimum.
	log := newCapturingLogger()

	tooLow := int64(100)
	fileConfig := &framework.FrameworkConfig{
		Pricing: &modelcatalog.Config{PricingSyncInterval: &tooLow},
	}

	tableOut, _, _ := resolveWithCapture(t, log, nil, fileConfig)

	// After resolution, the value passed to Init will be 3600, not 100.
	require.Equal(t, int64(3600), *tableOut.PricingSyncInterval,
		"value reaching modelcatalog.Init must be >= 3600 s after resolution")

	t.Logf("PASS — scheduler model: 100 s input always yields %d s at Init boundary", *tableOut.PricingSyncInterval)
}

// =============================================================================
// STEP 8 — Restart consistency: DB values survive restarts
// =============================================================================

func TestPricingE2E_Step8_RestartConsistency(t *testing.T) {
	dir := t.TempDir()
	store := makeStore(t, dir)
	ctx := context.Background()

	// ---------- First boot ----------
	// File config sets interval=7200 s. No DB row yet.
	// When dbConfig==nil, ResolveFrameworkPricingConfig returns needsDBUpdate=false because
	// there is no existing DB row to backfill — initFrameworkConfig handles the initial row
	// creation separately (via the frameworkConfigFromDB==nil check). We simulate that here
	// by writing the resolved config to the store manually, as initFrameworkConfig would do.
	log1 := newCapturingLogger()
	fileConfig := &framework.FrameworkConfig{
		Pricing: &modelcatalog.Config{
			PricingURL:          ptStr("https://file.example.com/pricing.json"),
			PricingSyncInterval: ptI64(7200),
		},
	}
	dbConfig, _ := store.GetFrameworkConfig(ctx)
	require.Nil(t, dbConfig, "no DB row should exist before first boot")

	tableOut1, _, needsWrite1 := resolveWithCapture(t, log1, dbConfig, fileConfig)

	require.Equal(t, int64(7200), *tableOut1.PricingSyncInterval, "first boot: file config used")
	// needsWrite1 is false when dbConfig==nil: no existing row to backfill.
	// initFrameworkConfig creates the initial row via the frameworkConfigFromDB==nil branch.
	require.False(t, needsWrite1, "needsDBUpdate is false for nil dbConfig (no existing row to patch)")

	// Simulate the initial DB row creation that initFrameworkConfig performs.
	require.NoError(t, store.UpdateFrameworkConfig(ctx, tableOut1))

	// ---------- Second boot (restart) ----------
	// File config still says 7200, but the DB already has 7200 written.
	log2 := newCapturingLogger()
	dbConfig2, err := store.GetFrameworkConfig(ctx)
	require.NoError(t, err, "DB row must exist after first boot write")
	require.NotNil(t, dbConfig2)

	tableOut2, _, needsWrite2 := resolveWithCapture(t, log2, dbConfig2, fileConfig)

	// DB should be authoritative — values match.
	require.Equal(t, int64(7200), *tableOut2.PricingSyncInterval,
		"second boot: DB value (7200 s) persists correctly")
	require.Equal(t, "https://file.example.com/pricing.json", *tableOut2.PricingURL,
		"second boot: DB url persists correctly")

	// After a write where file==DB, no update is needed (no change in values).
	require.False(t, needsWrite2,
		"second boot: no DB update needed when DB already has the correct values")

	// Source must be "db" on second boot.
	assert.True(t, log2.hasLog("resolved pricing config", "source: db"),
		"second boot must report 'source: db'\n%s", log2.dump())

	// Override logs must NOT appear (file and DB are identical).
	assert.False(t, log2.hasLog("overridden by DB"),
		"no override log expected when file and DB values are identical\n%s", log2.dump())

	t.Logf("PASS — restart consistency: DB values survive restart, source=db, no spurious override log")
}

func TestPricingE2E_Step8_RestartConsistency_DBWinsOverChangedFile(t *testing.T) {
	dir := t.TempDir()
	store := makeStore(t, dir)
	ctx := context.Background()

	// Seed DB with 3600 s (as if written by a previous boot or API call).
	dbURL := "https://db.example.com/pricing.json"
	require.NoError(t, store.UpdateFrameworkConfig(ctx, &configstoreTables.TableFrameworkConfig{
		PricingURL:          &dbURL,
		PricingSyncInterval: ptI64(3600),
	}))

	log := newCapturingLogger()

	// File now has DIFFERENT values (someone edited config.json after initial setup).
	fileConfig := &framework.FrameworkConfig{
		Pricing: &modelcatalog.Config{
			PricingURL:          ptStr("https://new-file.example.com/pricing.json"),
			PricingSyncInterval: ptI64(86400),
		},
	}

	dbConfig, err := store.GetFrameworkConfig(ctx)
	require.NoError(t, err)

	tableOut, _, _ := resolveWithCapture(t, log, dbConfig, fileConfig)

	// DB is authoritative — file changes must NOT override.
	require.Equal(t, dbURL, *tableOut.PricingURL,
		"DB url must override changed file url after initial setup")
	require.Equal(t, int64(3600), *tableOut.PricingSyncInterval,
		"DB interval must override changed file interval after initial setup")

	// Override logs must explain the discrepancy.
	assert.True(t, log.hasLog("INFO", "pricing_url overridden by DB"),
		"expected url override log\n%s", log.dump())
	assert.True(t, log.hasLog("INFO", "pricing_sync_interval overridden by DB"),
		"expected interval override log\n%s", log.dump())

	t.Logf("PASS — DB wins over changed file config on restart")
}

// =============================================================================
// STEP 9 — Failure simulation
// =============================================================================

func TestPricingE2E_Step9A_InvalidURL_NotSilentlyReplaced(t *testing.T) {
	log := newCapturingLogger()

	// "invalid-url" has no "env." prefix — treated as a plain string.
	// It is NOT a URL format error at this layer (config layer ≠ HTTP fetch layer).
	// The invalid URL should be stored verbatim and fail visibly when fetched.
	fileConfig := &framework.FrameworkConfig{
		Pricing: &modelcatalog.Config{
			PricingURL: ptStr("invalid-url"),
		},
	}

	tableOut, catalogOut, _ := resolveWithCapture(t, log, nil, fileConfig)

	// The resolver must NOT silently replace an unambiguous non-env string URL with
	// the default — it is stored as-is so the HTTP fetch fails visibly.
	require.Equal(t, "invalid-url", *tableOut.PricingURL,
		"invalid URL must be stored verbatim — visible failure at fetch time, not silent fallback")
	require.Equal(t, *tableOut.PricingURL, *catalogOut.PricingURL)

	// No WARN expected at config resolution layer.
	assert.False(t, log.hasLog("WARN"), "no WARN at config layer for a literal non-env URL\n%s", log.dump())

	t.Logf("PASS — invalid URL preserved verbatim %q (will fail visibly at fetch)", *tableOut.PricingURL)
}

func TestPricingE2E_Step9B_MissingEnvURL_NotReplacedWithDefault(t *testing.T) {
	// The most dangerous silent failure: "env.VAR" lookup fails → must NOT fall back
	// to the built-in default URL silently. This would mask a misconfiguration.
	log := newCapturingLogger()

	rawURL := "env.BIFROST_E2E_PRICING_URL_NONEXISTENT_PRODUCTION_XYZ"

	fileConfig := &framework.FrameworkConfig{
		Pricing: &modelcatalog.Config{PricingURL: ptStr(rawURL)},
	}

	tableOut, _, _ := resolveWithCapture(t, log, nil, fileConfig)

	require.Equal(t, rawURL, *tableOut.PricingURL,
		"failed env lookup must preserve the original 'env.*' string — NOT default URL")
	require.NotEqual(t, modelcatalog.DefaultPricingURL, *tableOut.PricingURL,
		"CRITICAL: must NOT silently fall back to default URL on env lookup failure")

	assert.True(t, log.hasLog("WARN", "env variable not found"),
		"expected WARN about env lookup failure\n%s", log.dump())

	t.Logf("PASS — missing env URL: preserved literal, NOT silently replaced with default")
}

// =============================================================================
// STEP 10 — Final assertions: no nil pointers, no panics, no silent fallback
// =============================================================================

func TestPricingE2E_Step10_NoNilPointers_AllInputCombinations(t *testing.T) {

	type tc struct {
		name   string
		db     *configstoreTables.TableFrameworkConfig
		file   *framework.FrameworkConfig
	}
	cases := []tc{
		{"nil/nil", nil, nil},
		{"nil/empty-framework", nil, &framework.FrameworkConfig{}},
		{"nil/empty-pricing", nil, &framework.FrameworkConfig{Pricing: &modelcatalog.Config{}}},
		{"empty-db/nil", &configstoreTables.TableFrameworkConfig{}, nil},
		{"empty-db/empty-file", &configstoreTables.TableFrameworkConfig{}, &framework.FrameworkConfig{}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			log := newCapturingLogger()
			tableOut, catalogOut, _ := resolveWithCapture(t, log, tc.db, tc.file)

			require.NotNil(t, tableOut, "TableFrameworkConfig must never be nil")
			require.NotNil(t, tableOut.PricingURL, "TableFrameworkConfig.PricingURL must never be nil")
			require.NotNil(t, tableOut.PricingSyncInterval, "TableFrameworkConfig.PricingSyncInterval must never be nil")
			require.NotNil(t, catalogOut, "modelcatalog.Config must never be nil")
			require.NotNil(t, catalogOut.PricingURL, "modelcatalog.Config.PricingURL must never be nil")
			require.NotNil(t, catalogOut.PricingSyncInterval, "modelcatalog.Config.PricingSyncInterval must never be nil")

			// Resolution log must always be emitted — no silent resolution.
			assert.True(t, log.hasLog("resolved pricing config"),
				"resolution log must always be emitted\n%s", log.dump())
		})
	}
}

func TestPricingE2E_Step10_NoPanic_ConcurrentResolution(t *testing.T) {
	// Ensure ResolveFrameworkPricingConfig is safe under concurrent calls.
	// We use a single shared capturing logger (thread-safe) and call
	// ResolveFrameworkPricingConfig directly — NOT via resolveWithCapture — to
	// avoid racing on the package-level `logger` variable.
	sharedLog := newCapturingLogger()
	prev := getLogger()
	SetLogger(sharedLog)
	defer SetLogger(prev)

	interval := int64(7200)
	fileConfig := &framework.FrameworkConfig{
		Pricing: &modelcatalog.Config{PricingSyncInterval: &interval},
	}

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tableOut, _, _ := ResolveFrameworkPricingConfig(nil, fileConfig)
			if tableOut == nil || tableOut.PricingSyncInterval == nil {
				t.Errorf("nil output under concurrent access")
			}
		}()
	}
	wg.Wait()
	t.Logf("PASS — 50 concurrent calls completed without panic or nil output")
}

// =============================================================================
// STEP 10 — Full pipeline: initFrameworkConfig with real SQLite store
// =============================================================================

func TestPricingE2E_Step10_FullPipeline_WithRealSQLite(t *testing.T) {
	dir := t.TempDir()
	store := makeStore(t, dir)
	ctx := context.Background()
	log := newCapturingLogger()

	// Boot 1: no DB, file has 7200 s.
	cfg1 := &Config{ConfigStore: store}
	fileConfig := &framework.FrameworkConfig{
		Pricing: &modelcatalog.Config{
			PricingURL:          ptStr("https://file.example.com/pricing.json"),
			PricingSyncInterval: ptI64(7200),
		},
	}
	configData := &ConfigData{FrameworkConfig: fileConfig}

	prev := getLogger()
	SetLogger(log)
	initFrameworkConfig(ctx, cfg1, configData)
	SetLogger(prev)
	if cfg1.ModelCatalog != nil {
		t.Cleanup(func() { cfg1.ModelCatalog.Cleanup() })
	}

	require.NotNil(t, cfg1.FrameworkConfig, "FrameworkConfig must be populated after init")
	require.NotNil(t, cfg1.FrameworkConfig.Pricing, "Pricing config must not be nil")
	require.Equal(t, int64(7200), *cfg1.FrameworkConfig.Pricing.PricingSyncInterval,
		"pricing_sync_interval must be 7200 s after file-based init")

	// DB must have been backfilled.
	dbRow, err := store.GetFrameworkConfig(ctx)
	require.NoError(t, err, "DB must have a row after first init")
	require.NotNil(t, dbRow, "DB row must not be nil")
	require.NotNil(t, dbRow.PricingSyncInterval, "DB interval field must be populated")
	require.Equal(t, int64(7200), *dbRow.PricingSyncInterval,
		"DB must be backfilled with 7200 s after first init")

	// Boot 2 (restart): same file config, DB has the values.
	log2 := newCapturingLogger()
	cfg2 := &Config{ConfigStore: store}
	prev2 := getLogger()
	SetLogger(log2)
	initFrameworkConfig(ctx, cfg2, configData)
	SetLogger(prev2)
	if cfg2.ModelCatalog != nil {
		t.Cleanup(func() { cfg2.ModelCatalog.Cleanup() })
	}

	require.Equal(t, int64(7200), *cfg2.FrameworkConfig.Pricing.PricingSyncInterval,
		"pricing_sync_interval must be 7200 s on restart (DB authoritative)")

	// Second boot must report DB as source.
	assert.True(t, log2.hasLog("resolved pricing config", "source: db"),
		"second boot must use DB as source\n%s", log2.dump())

	// Boot 3: DB has 7200, file changed to 3600 — DB must still win.
	log3 := newCapturingLogger()
	cfg3 := &Config{ConfigStore: store}
	changedFileConfig := &ConfigData{FrameworkConfig: &framework.FrameworkConfig{
		Pricing: &modelcatalog.Config{
			PricingURL:          ptStr("https://new-file.example.com/pricing.json"),
			PricingSyncInterval: ptI64(3600),
		},
	}}
	prev3 := getLogger()
	SetLogger(log3)
	initFrameworkConfig(ctx, cfg3, changedFileConfig)
	SetLogger(prev3)
	if cfg3.ModelCatalog != nil {
		t.Cleanup(func() { cfg3.ModelCatalog.Cleanup() })
	}

	require.Equal(t, int64(7200), *cfg3.FrameworkConfig.Pricing.PricingSyncInterval,
		"DB (7200 s) must win over changed file config (3600 s) on third boot")
	assert.True(t, log3.hasLog("INFO", "pricing_sync_interval overridden by DB"),
		"expected override log on third boot\n%s", log3.dump())

	t.Logf("PASS — full pipeline (3 boots): file→DB backfill→DB authoritative→DB wins over changed file")
}

// =============================================================================
// STEP 10 — Timing: modelcatalog.Init correctly converts seconds → Duration
// =============================================================================

func TestPricingE2E_Step10_SecondsToDurationConversion(t *testing.T) {
	// Before the fix, PricingSyncInterval was *time.Duration and JSON `3600` was
	// deserialized as 3600 nanoseconds → .Seconds() → 0 → ignored.
	// This test proves the fix: 3600 (seconds) must produce a 1-hour duration.
	dir := t.TempDir()
	store := makeStore(t, dir)
	ctx := context.Background()

	clg := newCapturingLogger()
	prev := getLogger()
	SetLogger(clg)
	defer SetLogger(prev)

	syncSeconds := int64(3600)
	cfg := &modelcatalog.Config{PricingSyncInterval: &syncSeconds}
	// noSyncFunc prevents real HTTP requests to the pricing URL during this unit test.
	mc, err := modelcatalog.Init(ctx, cfg, store, noSyncFunc, clg)
	require.NoError(t, err)
	defer mc.Cleanup()

	// The critical assertion: if the old *time.Duration bug were present,
	// this would show "3.6µs" not "1h0m0s".
	assert.True(t, clg.hasLog("INFO", "1h0m0s"),
		"3600 seconds must produce 1h0m0s duration — not 3.6µs (the pre-fix bug)\n%s", clg.dump())

	// Also verify 7200 → 2h (reuse the same store since we're only testing Init startup log)
	clg2 := newCapturingLogger()
	SetLogger(clg2)
	syncSeconds2 := int64(7200)
	cfg2 := &modelcatalog.Config{PricingSyncInterval: &syncSeconds2}
	mc2, err := modelcatalog.Init(ctx, cfg2, store, noSyncFunc, clg2)
	require.NoError(t, err)
	defer mc2.Cleanup()
	SetLogger(prev)

	assert.True(t, clg2.hasLog("INFO", "2h0m0s"),
		"7200 seconds must produce 2h0m0s duration\n%s", clg2.dump())

	t.Logf("PASS — seconds→duration conversion: 3600→1h, 7200→2h (pre-fix bug would give 3.6µs/7.2µs)")
}

// =============================================================================
// Benchmark: ResolveFrameworkPricingConfig has negligible overhead
// =============================================================================

func BenchmarkResolveFrameworkPricingConfig(b *testing.B) {
	// Set up a realistic scenario with both DB and file config populated.
	dbURL := "https://db.example.com/pricing.json"
	dbInterval := int64(3600)
	dbConfig := &configstoreTables.TableFrameworkConfig{
		ID:                  1,
		PricingURL:          &dbURL,
		PricingSyncInterval: &dbInterval,
	}
	fileConfig := &framework.FrameworkConfig{
		Pricing: &modelcatalog.Config{
			PricingURL:          ptStr("https://file.example.com/pricing.json"),
			PricingSyncInterval: ptI64(7200),
		},
	}
	noop := &testLogger{}
	SetLogger(noop)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ResolveFrameworkPricingConfig(dbConfig, fileConfig)
	}
}

// =============================================================================
// Summary helper test: print a human-readable scenario table
// =============================================================================

func TestPricingE2E_SummaryTable(t *testing.T) {
	type scenario struct {
		name     string
		expected string
		testFunc func() (actual string, match bool)
	}

	defaultInterval := fmt.Sprintf("%d s", defaultSyncSecs)

	scenarios := []scenario{
		{
			name:     "Baseline (no config/DB)",
			expected: fmt.Sprintf("url=%q interval=%s source=default", modelcatalog.DefaultPricingURL, defaultInterval),
			testFunc: func() (string, bool) {
				log := newCapturingLogger()
				t, c, _ := resolveWithCapture(nil, log, nil, nil)
				actual := fmt.Sprintf("url=%q interval=%d s source=%s", *t.PricingURL, *t.PricingSyncInterval,
					func() string {
						if log.hasLog("source: default") {
							return "default"
						}
						return "other"
					}())
				_ = c
				return actual, log.hasLog("source: default") && *t.PricingSyncInterval == defaultSyncSecs
			},
		},
		{
			name:     "File config (7200 s)",
			expected: "interval=7200 s source=file",
			testFunc: func() (string, bool) {
				log := newCapturingLogger()
				fc := &framework.FrameworkConfig{Pricing: &modelcatalog.Config{PricingSyncInterval: ptI64(7200)}}
				tab, _, _ := resolveWithCapture(nil, log, nil, fc)
				return fmt.Sprintf("interval=%d s", *tab.PricingSyncInterval),
					*tab.PricingSyncInterval == 7200
			},
		},
		{
			name:     "Too low (100 s) → clamped",
			expected: "interval=3600 s WARN=clamped",
			testFunc: func() (string, bool) {
				log := newCapturingLogger()
				fc := &framework.FrameworkConfig{Pricing: &modelcatalog.Config{PricingSyncInterval: ptI64(100)}}
				tab, _, _ := resolveWithCapture(nil, log, nil, fc)
				return fmt.Sprintf("interval=%d s WARN=%v", *tab.PricingSyncInterval, log.hasLog("WARN", "clamping")),
					*tab.PricingSyncInterval == 3600 && log.hasLog("WARN", "clamping")
			},
		},
		{
			name:     "Zero → ignored",
			expected: fmt.Sprintf("interval=%s WARN=invalid", defaultInterval),
			testFunc: func() (string, bool) {
				log := newCapturingLogger()
				fc := &framework.FrameworkConfig{Pricing: &modelcatalog.Config{PricingSyncInterval: ptI64(0)}}
				tab, _, _ := resolveWithCapture(nil, log, nil, fc)
				return fmt.Sprintf("interval=%d s WARN=%v", *tab.PricingSyncInterval, log.hasLog("WARN")),
					*tab.PricingSyncInterval == defaultSyncSecs && log.hasLog("WARN")
			},
		},
		{
			name:     "Missing env var → literal preserved",
			expected: "url=env.MISSING_XYZ (NOT default URL)",
			testFunc: func() (string, bool) {
				log := newCapturingLogger()
				fc := &framework.FrameworkConfig{Pricing: &modelcatalog.Config{PricingURL: ptStr("env.MISSING_XYZ_123")}}
				tab, _, _ := resolveWithCapture(nil, log, nil, fc)
				return fmt.Sprintf("url=%q", *tab.PricingURL),
					*tab.PricingURL == "env.MISSING_XYZ_123" && *tab.PricingURL != modelcatalog.DefaultPricingURL
			},
		},
	}

	t.Log("")
	t.Log("╔══════════════════════════════════════════════════════════════════════════════╗")
	t.Log("║               E2E PRICING CONFIG SCENARIO SUMMARY TABLE                    ║")
	t.Log("╠══════════════════════════════════════════╦══════════════════╦═════════════╣")
	t.Log("║ Scenario                                 ║ Expected         ║ Match?      ║")
	t.Log("╠══════════════════════════════════════════╬══════════════════╬═════════════╣")

	allPass := true
	for _, sc := range scenarios {
		actual, match := sc.testFunc()
		status := "✔ PASS"
		if !match {
			status = "✘ FAIL"
			allPass = false
		}
		t.Logf("║ %-40s ║ %-16s ║ %-11s ║", sc.name, actual[:min(len(actual), 16)], status)
	}

	t.Log("╚══════════════════════════════════════════╩══════════════════╩═════════════╝")
	t.Log("")

	if !allPass {
		t.Fail()
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Ensure time is imported (used in TestPricingE2E_Step7 indirectly via modelcatalog)
var _ = time.Second
