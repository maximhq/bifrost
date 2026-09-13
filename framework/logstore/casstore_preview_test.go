package logstore

// Hybrid-preview parity tests for the CAS content_summary column.
//
// Contract under test (the minimal fix): in CAS mode the row's
// content_summary is exactly what hybrid mode persists — the last user
// message preview, UTF-8-safe truncated to maxContentSummaryBytes (2048) —
// on every write path (create, batch, map update, struct update), and empty
// for hidden rows. No reconstruction, no auxiliary tables: search scope is
// the preview, which is hybrid's intended degradation (keywords only present
// beyond the preview are not findable through content search; the full
// payload remains available through CAS hydration).

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/objectstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// previewHistoryNeedle / previewOutputNeedle / previewEarlyNeedle are distinct
// keywords used to pin down the search scope: only the last user message may
// end up in the preview column.
const (
	previewNeedle   = "previewneedle"
	outputNeedle    = "outputneedle"
	earlyNeedle     = "earlyhistoryneedle"
	previewPadBytes = 4096 // comfortably above maxContentSummaryBytes
)

// previewTestEntry builds a multi-turn chat entry: early history (needle never
// in the preview), an assistant reply, a long final user message (the preview
// source, truncated at 2048 bytes) and a long output (offloaded to CAS).
func previewTestEntry(id string) *Log {
	bigPad := strings.Repeat("p", previewPadBytes)
	history := []schemas.ChatMessage{
		{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: strPtr(earlyNeedle + " " + bigPad)}},
		{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: strPtr("assistant reply")}},
		{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: strPtr(previewNeedle + " " + bigPad)}},
	}
	return &Log{
		ID:                  id,
		Timestamp:           time.Now().UTC(),
		Provider:            "openai",
		Model:               "gpt-test",
		Status:              "success",
		Object:              "chat.completion",
		InputHistoryParsed:  history,
		OutputMessageParsed: &schemas.ChatMessage{Content: &schemas.ChatMessageContent{ContentStr: strPtr(outputNeedle + " " + bigPad)}},
	}
}

// multiBytePreviewEntry puts multi-byte UTF-8 runes exactly around the 2048
// byte cap so a byte-blind truncation would split a rune.
func multiBytePreviewEntry(id string) *Log {
	// 2045 ASCII bytes + "é" (2 bytes) + "€" (3 bytes) + trailing text.
	tail := "é€" + strings.Repeat("z", 64)
	return &Log{
		ID:        id,
		Timestamp: time.Now().UTC(),
		Provider:  "openai",
		Model:     "gpt-test",
		Status:    "success",
		Object:    "chat.completion",
		InputHistoryParsed: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: strPtr("mb " + strings.Repeat("a", 2042) + tail)}},
		},
		OutputMessageParsed: &schemas.ChatMessage{Content: &schemas.ChatMessageContent{ContentStr: strPtr("ok")}},
	}
}

// --- create-path parity ---

// TestCas_CreateSummaryMatchesHybrid is the strongest acceptance: for the
// same multi-turn input, the CAS row's content_summary is byte-identical to
// the hybrid row's, at most 2048 bytes, and UTF-8 safe.
func TestCas_CreateSummaryMatchesHybrid(t *testing.T) {
	ctx := context.Background()

	cas, casInner := newTestCas(t)
	defer cas.Close(ctx)
	hybrid, hybridInner, _ := newTestHybrid(t)
	defer hybrid.Close(ctx)

	casEntry := previewTestEntry("parity-1")
	require.NoError(t, casEntry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, casEntry))

	hybridEntry := previewTestEntry("parity-1")
	require.NoError(t, hybridEntry.SerializeFields())
	require.NoError(t, hybrid.CreateIfNotExists(ctx, hybridEntry))

	casRow, err := casInner.FindByID(ctx, "parity-1")
	require.NoError(t, err)
	hybridRow, err := hybridInner.FindByID(ctx, "parity-1")
	require.NoError(t, err)

	assert.Equal(t, hybridRow.ContentSummary, casRow.ContentSummary,
		"CAS content_summary must be byte-identical to hybrid mode for the same input")
	assert.LessOrEqual(t, len(casRow.ContentSummary), maxContentSummaryBytes)
	assert.True(t, utf8.ValidString(casRow.ContentSummary), "truncation must not split a UTF-8 rune")
	assert.Contains(t, casRow.ContentSummary, previewNeedle, "preview holds the last user message")
	assert.NotContains(t, casRow.ContentSummary, outputNeedle, "output text is not in the preview")
	assert.NotContains(t, casRow.ContentSummary, earlyNeedle, "early history is not in the preview")

	// Both stores propagate the persisted value back to the caller's entry.
	assert.Equal(t, casRow.ContentSummary, casEntry.ContentSummary,
		"CAS propagates the persisted preview back like hybrid does")
	assert.Equal(t, hybridRow.ContentSummary, hybridEntry.ContentSummary)
}

// TestCas_CreateSummaryMultiByteCapSafe pins the UTF-8 boundary behavior of
// the create-path cap.
func TestCas_CreateSummaryMultiByteCapSafe(t *testing.T) {
	cas, inner := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	entry := multiBytePreviewEntry("multibyte-1")
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))

	row, err := inner.FindByID(ctx, "multibyte-1")
	require.NoError(t, err)
	assert.LessOrEqual(t, len(row.ContentSummary), maxContentSummaryBytes)
	assert.True(t, utf8.ValidString(row.ContentSummary))
	assert.Equal(t, truncateTag("mb "+strings.Repeat("a", 2042)+"é€"+strings.Repeat("z", 64), maxContentSummaryBytes),
		row.ContentSummary, "preview is the 2048-byte UTF-8-safe truncation of the last user message")
}

// TestCas_BatchCreateSummaryCapped: the batch create path persists the same
// capped hybrid preview.
func TestCas_BatchCreateSummaryCapped(t *testing.T) {
	cas, inner := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	var batch []*Log
	for i := 0; i < 3; i++ {
		e := previewTestEntry("batch-preview-" + string(rune('a'+i)))
		require.NoError(t, e.SerializeFields())
		batch = append(batch, e)
	}
	require.NoError(t, cas.BatchCreateIfNotExists(ctx, batch))

	// Hybrid reference for the same input.
	hybrid, hybridInner, _ := newTestHybrid(t)
	defer hybrid.Close(ctx)
	refEntry := previewTestEntry("batch-preview-a")
	require.NoError(t, refEntry.SerializeFields())
	require.NoError(t, hybrid.CreateIfNotExists(ctx, refEntry))
	refRow, err := hybridInner.FindByID(ctx, "batch-preview-a")
	require.NoError(t, err)

	for _, e := range batch {
		row, err := inner.FindByID(ctx, e.ID)
		require.NoError(t, err)
		assert.LessOrEqual(t, len(row.ContentSummary), maxContentSummaryBytes, "batch row %s", e.ID)
		assert.Equal(t, refRow.ContentSummary, row.ContentSummary,
			"batch row %s summary must equal the hybrid preview", e.ID)
	}
}

// --- update-path caps ---

// TestCas_UpdateSummaryCappedOutputOnly reproduces the shape of the logging
// plugin's streaming/output-only summary update (plugins/logging/
// operations.go: a map update whose content_summary carries the serialized
// full output text) and asserts the row value is capped.
func TestCas_UpdateSummaryCappedOutputOnly(t *testing.T) {
	cas, inner := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	entry := previewTestEntry("update-capped")
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))

	// Streaming-style output-only summary: the full response text as the
	// summary plus a big output_message payload (CAS-offloaded).
	longOutput := "streamed response body " + strings.Repeat("w ", 4000)
	require.NoError(t, cas.Update(ctx, "update-capped", map[string]interface{}{
		"content_summary": longOutput,
		"output_message":  longOutput,
		"status":          "success",
	}))

	row, err := inner.FindByID(ctx, "update-capped")
	require.NoError(t, err)
	assert.LessOrEqual(t, len(row.ContentSummary), maxContentSummaryBytes,
		"update-path summary must be capped at 2048 bytes")
	assert.Equal(t, truncateTag(longOutput, maxContentSummaryBytes), row.ContentSummary)
	assert.True(t, utf8.ValidString(row.ContentSummary))

	// A []byte value is capped the same way.
	longBytes := []byte(strings.Repeat("b", 5000))
	require.NoError(t, cas.Update(ctx, "update-capped", map[string]interface{}{
		"content_summary": longBytes,
	}))
	row, err = inner.FindByID(ctx, "update-capped")
	require.NoError(t, err)
	assert.Equal(t, string(longBytes[:maxContentSummaryBytes]), row.ContentSummary)

	// Empty stays empty.
	require.NoError(t, cas.Update(ctx, "update-capped", map[string]interface{}{
		"content_summary": "",
	}))
	row, err = inner.FindByID(ctx, "update-capped")
	require.NoError(t, err)
	assert.Empty(t, row.ContentSummary)

	// Struct updates flow through the same cap.
	structEntry := &Log{ContentSummary: strings.Repeat("s", 9999)}
	require.NoError(t, cas.Update(ctx, "update-capped", structEntry))
	row, err = inner.FindByID(ctx, "update-capped")
	require.NoError(t, err)
	assert.Equal(t, strings.Repeat("s", maxContentSummaryBytes), row.ContentSummary)
}

// TestHybrid_UpdateSummaryIsUncapped_DocumentedDivergence pins what hybrid
// mode actually does on the update path: HybridLogStore.Update passes the map
// through to the RDB store verbatim, so an output-only summary update writes
// the FULL text into content_summary there. CAS mode caps update-path writes
// at 2048 bytes anyway (user decision: bounded row storage). This test keeps
// the divergence honest and visible instead of implicit.
func TestHybrid_UpdateSummaryIsUncapped_DocumentedDivergence(t *testing.T) {
	hybrid, inner, _ := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	entry := previewTestEntry("hybrid-uncapped")
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, hybrid.CreateIfNotExists(ctx, entry))

	longOutput := "streamed response body " + strings.Repeat("w ", 4000)
	require.NoError(t, hybrid.Update(ctx, "hybrid-uncapped", map[string]interface{}{
		"content_summary": longOutput,
	}))

	row, err := inner.FindByID(ctx, "hybrid-uncapped")
	require.NoError(t, err)
	assert.Equal(t, longOutput, row.ContentSummary,
		"hybrid's update path is an uncapped passthrough; CAS capping is a deliberate, documented divergence")
}

// --- hidden rows ---

func TestCas_HiddenSummaryStaysEmpty(t *testing.T) {
	cas, inner := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	// Hidden create keeps no summary.
	hidden := previewTestEntry("hidden-create")
	hidden.ContentHidden = true
	require.NoError(t, hidden.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, hidden))
	row, err := inner.FindByID(ctx, "hidden-create")
	require.NoError(t, err)
	assert.Empty(t, row.ContentSummary)

	// Hiding an existing row empties the summary.
	visible := previewTestEntry("hidden-update")
	require.NoError(t, visible.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, visible))
	row, err = inner.FindByID(ctx, "hidden-update")
	require.NoError(t, err)
	require.NotEmpty(t, row.ContentSummary)

	require.NoError(t, cas.Update(ctx, "hidden-update", map[string]interface{}{"content_hidden": true}))
	row, err = inner.FindByID(ctx, "hidden-update")
	require.NoError(t, err)
	assert.Empty(t, row.ContentSummary, "hiding must clear the summary")

	// Updates on a hidden row cannot put content back.
	require.NoError(t, cas.Update(ctx, "hidden-update", map[string]interface{}{
		"content_summary": strings.Repeat("x", 5000),
	}))
	row, err = inner.FindByID(ctx, "hidden-update")
	require.NoError(t, err)
	assert.Empty(t, row.ContentSummary, "hidden rows keep no summary even when one is supplied")
}

// --- search scope ---

// TestCas_SearchScopeIsPreview_SQLite documents the intended hybrid-consistent
// degradation: content search (SQLite LIKE) hits keywords inside the preview
// and misses keywords only present in the output or in early history.
func TestCas_SearchScopeIsPreview_SQLite(t *testing.T) {
	cas, _ := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	entry := previewTestEntry("scope-sqlite")
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, entry))

	hit, err := cas.SearchLogs(ctx, SearchFilters{ContentSearch: previewNeedle}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	require.Len(t, hit.Logs, 1, "keyword inside the preview must be found")
	assert.Equal(t, "scope-sqlite", hit.Logs[0].ID)

	miss, err := cas.SearchLogs(ctx, SearchFilters{ContentSearch: outputNeedle}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	assert.Empty(t, miss.Logs, "output-only keyword is out of the preview scope (intended hybrid-consistent degradation)")

	miss, err = cas.SearchLogs(ctx, SearchFilters{ContentSearch: earlyNeedle}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	assert.Empty(t, miss.Logs, "early-history keyword is out of the preview scope (intended hybrid-consistent degradation)")
}

// --- Postgres lifecycle (real CasLogStore on a disposable container) ---

// previewTestPgDSN is provided by the reproduction script: a disposable,
// resource-limited PostgreSQL container reachable from the test host. Empty
// means skip.
const previewTestPgDSNEnv = "CAS_PREVIEW_FIX_PG"

func previewTestPgDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv(previewTestPgDSNEnv)
	if dsn == "" {
		t.Skip("CAS_PREVIEW_FIX_PG not set; skipping PostgreSQL preview test")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, sqlDB.Ping())

	// Clean slate in this test's dedicated schema.
	require.NoError(t, db.Exec("CREATE SCHEMA IF NOT EXISTS cas_preview_fix").Error)
	dropAllManagedMatViews(db)
	for _, table := range []string{"cas_inventories", "cas_inventory_state", "cas_payloads", "cas_refs", "cas_blobs", "logs"} {
		require.NoError(t, db.Exec("DROP TABLE IF EXISTS "+table+" CASCADE").Error)
	}
	require.NoError(t, db.Exec("DROP TABLE IF EXISTS migrations").Error)
	require.NoError(t, triggerMigrations(context.Background(), db, hybridTestLogger{}))
	return db
}

// TestCas_PreviewLifecyclePostgres runs the full preview lifecycle on
// PostgreSQL: create (hybrid-parity summary), output-only update (capped),
// FTS search scope, hide, delete.
func TestCas_PreviewLifecyclePostgres(t *testing.T) {
	db := previewTestPgDB(t)
	ctx := context.Background()

	// Hybrid reference over the same database (in-memory object store).
	hybridInner := &RDBLogStore{db: db, logger: hybridTestLogger{}}
	hybrid := newHybridLogStore(hybridInner, objectstore.NewInMemoryObjectStore(), "preview-test", hybridTestLogger{}, nil)
	defer hybrid.Close(ctx)

	hybridEntry := previewTestEntry("pg-lifecycle")
	require.NoError(t, hybridEntry.SerializeFields())
	require.NoError(t, hybrid.CreateIfNotExists(ctx, hybridEntry))
	hybridRow, err := hybridInner.FindByID(ctx, "pg-lifecycle")
	require.NoError(t, err)
	require.NotEmpty(t, hybridRow.ContentSummary)
	// Let the async offload settle before reusing the ID below.
	waitForOffload(t, hybridInner, "pg-lifecycle")

	// Reset the logs table for the CAS phase (same schema, clean rows).
	require.NoError(t, db.Exec("DELETE FROM logs").Error)

	inner := &RDBLogStore{db: db, logger: hybridTestLogger{}}
	cfg := &ContentAddressedConfig{Enabled: true, MinFieldBytes: 64, MinChunkBytes: 32}
	cas, err := newCasLogStore(ctx, inner, cfg, hybridTestLogger{})
	require.NoError(t, err)
	defer cas.Close(ctx)

	casEntry := previewTestEntry("pg-lifecycle")
	require.NoError(t, casEntry.SerializeFields())
	require.NoError(t, cas.CreateIfNotExists(ctx, casEntry))

	row, err := inner.FindByID(ctx, "pg-lifecycle")
	require.NoError(t, err)
	assert.Equal(t, hybridRow.ContentSummary, row.ContentSummary,
		"PG: CAS content_summary must be byte-identical to hybrid mode")
	assert.LessOrEqual(t, len(row.ContentSummary), maxContentSummaryBytes)
	assert.Positive(t, casPointerCount(t, cas, "pg-lifecycle", "input_history"),
		"big input history must be offloaded to CAS on PG")

	// Output-only streaming-style update: capped on PG too. The keyword sits at
	// the END of the output, beyond the 2048-byte summary cap, so it proves
	// both the cap (search miss) and full hydration (payload read).
	longOutput := "streamed response body " + strings.Repeat("w ", 4000) + " deepoutputtail"
	require.NoError(t, cas.Update(ctx, "pg-lifecycle", map[string]interface{}{
		"content_summary": longOutput,
		"output_message":  longOutput,
		"status":          "success",
	}))
	row, err = inner.FindByID(ctx, "pg-lifecycle")
	require.NoError(t, err)
	assert.Equal(t, truncateTag(longOutput, maxContentSummaryBytes), row.ContentSummary,
		"PG: update-path summary must be capped at 2048 bytes")
	assert.NotContains(t, row.ContentSummary, "deepoutputtail",
		"PG: the cap must cut the tail keyword out of the search column")

	// PG content search uses to_tsvector(left(content_summary, 250000)) @@
	// plainto_tsquery('simple', ?): the capped column is fully in range, so
	// the scope is exactly the preview.
	hit, err := cas.SearchLogs(ctx, SearchFilters{ContentSearch: "streamed"}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	require.Len(t, hit.Logs, 1, "PG FTS: keyword inside the capped summary must be found")
	assert.Equal(t, "pg-lifecycle", hit.Logs[0].ID)

	miss, err := cas.SearchLogs(ctx, SearchFilters{ContentSearch: "deepoutputtail"}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	assert.Empty(t, miss.Logs, "PG FTS: keyword beyond the 2048-byte preview cap is out of scope")

	// Hydration still serves the full payload.
	found, err := cas.FindByID(ctx, "pg-lifecycle")
	require.NoError(t, err)
	assert.Contains(t, found.OutputMessage, "deepoutputtail", "PG: full output remains available through CAS hydration")

	// Hide empties the summary; delete removes the row.
	require.NoError(t, cas.Update(ctx, "pg-lifecycle", map[string]interface{}{"content_hidden": true}))
	row, err = inner.FindByID(ctx, "pg-lifecycle")
	require.NoError(t, err)
	assert.Empty(t, row.ContentSummary)

	require.NoError(t, cas.DeleteLog(ctx, "pg-lifecycle"))
	_, err = inner.FindByID(ctx, "pg-lifecycle")
	require.ErrorIs(t, err, ErrNotFound)
}

// --- growth regression ---

// TestCas_SummaryGrowthBounded replays the original agent-loop repro shape:
// each turn's log carries the whole conversation so far, so the pre-fix
// inline full summary grew without bound (quadratic total storage across
// turns). The row summary must now stay at the 2048-byte hybrid preview cap.
//
// When CAS_PREVIEW_GROWTH_OUT is set, per-turn measurements are written as
// JSON (used to produce before/after evidence against the pre-fix baseline
// build of the same test). When CAS_PREVIEW_ASSERT_BOUNDED=0 the boundedness
// assertions are skipped (used to capture the baseline's unbounded numbers).
func TestCas_SummaryGrowthBounded(t *testing.T) {
	cas, inner := newTestCas(t)
	defer cas.Close(context.Background())
	ctx := context.Background()

	const turns = 24
	const turnBytes = 2048

	type turnRow struct {
		Turn            int `json:"turn"`
		SummaryBytes    int `json:"content_summary_bytes"`
		InputHistoryLen int `json:"input_history_column_bytes"`
	}
	rowsOut := make([]turnRow, 0, turns)

	var history []schemas.ChatMessage
	for turn := 0; turn < turns; turn++ {
		history = append(history,
			schemas.ChatMessage{Role: schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: strPtr("turn question " + string(rune('a'+turn%26)) + " " + strings.Repeat("q ", turnBytes/2))}},
			schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant,
				Content: &schemas.ChatMessageContent{ContentStr: strPtr(strings.Repeat("r ", 256))}},
		)
		id := "growth-" + string(rune('a'+turn%26)) + string(rune('0'+turn/10)) + string(rune('0'+turn%10))
		entry := &Log{
			ID:                  id,
			Timestamp:           time.Now().UTC(),
			Provider:            "openai",
			Model:               "gpt-test",
			Status:              "success",
			Object:              "chat.completion",
			InputHistoryParsed:  append([]schemas.ChatMessage{}, history...),
			OutputMessageParsed: &schemas.ChatMessage{Content: &schemas.ChatMessageContent{ContentStr: strPtr("ok " + strings.Repeat("o ", 128))}},
		}
		require.NoError(t, entry.SerializeFields())
		require.NoError(t, cas.CreateIfNotExists(ctx, entry))

		row, err := inner.FindByID(ctx, id)
		require.NoError(t, err)
		rowsOut = append(rowsOut, turnRow{
			Turn:            turn,
			SummaryBytes:    len(row.ContentSummary),
			InputHistoryLen: len(row.InputHistory),
		})
	}

	total := 0
	for _, r := range rowsOut {
		total += r.SummaryBytes
	}
	if out := os.Getenv("CAS_PREVIEW_GROWTH_OUT"); out != "" {
		payload := map[string]any{
			"turns":                       turns,
			"turn_bytes":                  turnBytes,
			"rows":                        rowsOut,
			"total_content_summary_bytes": total,
			"max_content_summary_bytes":   maxContentSummaryBytes,
		}
		data, err := json.MarshalIndent(payload, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(out, data, 0o644))
	}

	if os.Getenv("CAS_PREVIEW_ASSERT_BOUNDED") == "0" {
		t.Logf("boundedness assertions skipped (baseline measurement mode): total=%d", total)
		return
	}

	for _, r := range rowsOut {
		assert.LessOrEqual(t, r.SummaryBytes, maxContentSummaryBytes,
			"turn %d: inline summary must stay at the preview cap", r.Turn)
	}
	assert.LessOrEqual(t, total, turns*maxContentSummaryBytes,
		"total inline summary storage must be bounded by turns x 2048, got %d", total)
}
