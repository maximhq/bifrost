package logstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// malformedHistoryCase covers the scenarios that previously aborted /api/logs
// via the unsafe input_history::jsonb cast (and the mirror cases on
// responses_input_history). Each case exercises one shape of bad data; the
// SearchLogs query must complete without error and return every row.
type malformedHistoryCase struct {
	name          string
	objectType    string // defaults to "chat.completion"; set to "realtime.turn" to test passthrough branch
	inputHistory  string // raw TEXT stored in logs.input_history
	respHistory   string // raw TEXT stored in logs.responses_input_history
	shouldCrashPG bool   // true if the row would have aborted the pre-fix Postgres list query
}

func malformedHistoryCases() []malformedHistoryCase {
	// Built at runtime so the source file stays free of literal NUL bytes /
	// lone surrogates that the Go parser would reject.
	bs := "\\"
	u0000 := bs + "u0000"
	uD800 := bs + "uD800"
	uDC00 := bs + "uDC00"

	// Build a large but valid JSON array to exercise the happy path on inputs
	// that wouldn't fit comfortably inline.
	var big strings.Builder
	big.WriteByte('[')
	for i := 0; i < 1000; i++ {
		if i > 0 {
			big.WriteByte(',')
		}
		big.WriteString(`{"role":"user","content":"msg"}`)
	}
	big.WriteString(`,{"role":"assistant","content":"last"}]`)

	return []malformedHistoryCase{
		// ---------- malformed: jsonb cast failures (22P02) ----------
		{
			name:          "unterminated_object_in_array",
			inputHistory:  `[{"role":"user","content":"hi"`,
			shouldCrashPG: true,
		},
		{
			name:          "garbage_after_bracket",
			inputHistory:  `[abc, not json]`,
			shouldCrashPG: true,
		},
		{
			name:          "trailing_comma",
			inputHistory:  `[{"role":"user","content":"hi"},]`,
			shouldCrashPG: true,
		},
		{
			name:          "unclosed_array_only",
			inputHistory:  `[`,
			shouldCrashPG: true,
		},
		{
			name:          "open_bracket_then_brace_unclosed",
			inputHistory:  `[{`,
			shouldCrashPG: true,
		},
		{
			name:          "nan_value_not_valid_json",
			inputHistory:  `[NaN]`,
			shouldCrashPG: true,
		},
		{
			name:          "infinity_value_not_valid_json",
			inputHistory:  `[Infinity]`,
			shouldCrashPG: true,
		},
		// ---------- malformed: jsonb cast failures (22P05 character set) ----------
		{
			name:          "unpaired_high_surrogate",
			inputHistory:  `[{"role":"user","content":"bad ` + uD800 + ` surrogate"}]`,
			shouldCrashPG: true,
		},
		{
			name:          "unpaired_low_surrogate",
			inputHistory:  `[{"role":"user","content":"bad ` + uDC00 + ` low"}]`,
			shouldCrashPG: true,
		},
		{
			name:          "bad_surrogate_pair_high_then_ascii",
			inputHistory:  `[{"role":"user","content":"` + uD800 + bs + `u0041"}]`,
			shouldCrashPG: true,
		},
		{
			name:          "u0000_escape_inside_string",
			inputHistory:  `[{"role":"user","content":"null byte ` + u0000 + ` here"}]`,
			shouldCrashPG: true,
		},
		// ---------- valid happy path: should pass through fast lane ----------
		{
			name:         "literal_backslash_u0000_valid_jsonb",
			inputHistory: `[{"role":"user","content":"backslash-u-zero \\u0000 literal"}]`,
		},
		{
			name:         "single_element_array",
			inputHistory: `[{"role":"user","content":"only one"}]`,
		},
		{
			name:         "array_of_primitives",
			inputHistory: `[1,2,3]`,
		},
		{
			name:         "array_with_null_last_element",
			inputHistory: `[{"role":"user","content":"x"}, null]`,
		},
		{
			name:         "deeply_nested_valid",
			inputHistory: `[{"role":"user","content":{"nested":{"deep":{"value":42}}}}]`,
		},
		{
			name:         "unicode_emoji_content",
			inputHistory: `[{"role":"user","content":"hello 🎉 world ✨"}]`,
		},
		{
			name:         "large_valid_array",
			inputHistory: big.String(),
		},
		// ---------- valid non-array / structurally OK: fall through to raw ----------
		{
			name:         "leading_whitespace_then_array",
			inputHistory: "   [\t{\"role\":\"user\",\"content\":\"ok\"}]",
		},
		{
			name:         "top_level_object_not_array",
			inputHistory: `{"not":"an array"}`,
		},
		{
			name:         "null_literal",
			inputHistory: `null`,
		},
		{
			name:         "whitespace_only",
			inputHistory: "   \t  ",
		},
		// ---------- realtime.turn passthrough: outer CASE bypasses safe function ----------
		{
			name:         "realtime_turn_malformed_passthrough",
			objectType:   "realtime.turn",
			inputHistory: `[{"role":"user"`, // malformed; must not crash even though safe fn is bypassed
		},
		// ---------- mirror column ----------
		{
			name:          "malformed_responses_input_history",
			respHistory:   `[{"role":"user"`,
			shouldCrashPG: true,
		},
		{
			name:        "valid_responses_input_history",
			respHistory: `[{"role":"user","content":"ok"},{"role":"assistant","content":"hi"}]`,
		},
	}
}

// insertMalformedLog inserts a logs row with the given raw input_history /
// responses_input_history TEXT values, bypassing GORM serialization so the
// exact byte sequence reaches the database.
func insertMalformedLog(t *testing.T, db *gorm.DB, c malformedHistoryCase, ts time.Time) string {
	t.Helper()
	id := uuid.New().String()
	objType := c.objectType
	if objType == "" {
		objType = "chat.completion"
	}
	err := db.Exec(`
		INSERT INTO logs (id, timestamp, object_type, provider, model, status,
			input_history, responses_input_history, created_at)
		VALUES (?, ?, ?, 'openai', 'gpt-4', 'success', ?, ?, ?)
	`, id, ts, objType, c.inputHistory, c.respHistory, ts).Error
	require.NoError(t, err, "failed to insert row for case %q", c.name)
	return id
}

// runMalformedHistorySuite exercises SearchLogs against each malformed case
// and asserts the query completes successfully for every row. Reused by the
// SQLite and Postgres entry points so both dialect branches of
// listSelectColumns are covered.
func runMalformedHistorySuite(t *testing.T, store *RDBLogStore, db *gorm.DB) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()

	cases := malformedHistoryCases()
	expectedIDs := make(map[string]string, len(cases))
	for i, c := range cases {
		// Spread timestamps so the DESC sort is stable per-case.
		id := insertMalformedLog(t, db, c, now.Add(-time.Duration(i)*time.Second))
		expectedIDs[id] = c.name
	}

	result, err := store.SearchLogs(ctx, SearchFilters{}, PaginationOptions{Limit: 1000})
	require.NoError(t, err, "SearchLogs must not fail on malformed input_history")
	require.NotNil(t, result)

	// Every inserted row must come back, regardless of payload shape.
	gotIDs := make(map[string]bool, len(result.Logs))
	for _, l := range result.Logs {
		gotIDs[l.ID] = true
	}
	for id, name := range expectedIDs {
		assert.True(t, gotIDs[id], "row for case %q (id=%s) missing from SearchLogs result", name, id)
	}
}

// TestSearchLogs_MalformedInputHistory_SQLite exercises the SQLite branch of
// listSelectColumns, which now gates json_extract on json_valid + json_type.
func TestSearchLogs_MalformedInputHistory_SQLite(t *testing.T) {
	store := newTestSQLiteStore(t)
	defer store.Close(context.Background())
	runMalformedHistorySuite(t, store, store.db)
}

// TestSearchLogs_MalformedInputHistory_Postgres exercises the Postgres branch,
// which now routes through the bifrost_safe_jsonb PL/pgSQL helper. Skipped
// when Postgres is unavailable.
func TestSearchLogs_MalformedInputHistory_Postgres(t *testing.T) {
	store, db := setupPerfTestDB(t)
	runMalformedHistorySuite(t, store, db)
}

// TestSearchLogs_MalformedInputHistory_ClickHouse exercises the ClickHouse
// branch, which gates JSONExtractRaw on isValidJSON + JSONType + JSONLength.
// Skipped when ClickHouse is unavailable.
func TestSearchLogs_MalformedInputHistory_ClickHouse(t *testing.T) {
	store := trySetupClickHouseStore(t)
	runMalformedHistorySuite(t, store.RDBLogStore, store.db)
}

// TestSearchLogs_HistoryProjection_ClickHouse pins the projected values the
// ClickHouse list query returns for input_history and responses_input_history:
// a valid non-empty array collapses to its last element, realtime.turn rows
// keep both histories whole, and empty, invalid, or non-array payloads come
// back byte-for-byte unchanged. The ID-only malformed suite above cannot catch
// a regression that returns the full history or picks the wrong element.
func TestSearchLogs_HistoryProjection_ClickHouse(t *testing.T) {
	store := trySetupClickHouseStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	var big strings.Builder
	big.WriteByte('[')
	for i := 0; i < 1000; i++ {
		big.WriteString(`{"role":"user","content":"msg"},`)
	}
	big.WriteString(`{"role":"assistant","content":"last"}]`)

	multi := `[{"role":"user","content":"hi"},{"role":"assistant","content":"last"}]`
	multiResp := `[{"type":"message","role":"user"},{"type":"message","role":"assistant"}]`

	cases := []struct {
		name       string
		objectType string
		input      string
		resp       string
		wantInput  string
		wantResp   string
	}{
		// last-element extraction on both columns
		{name: "multi_element_both_columns", input: multi, resp: multiResp,
			wantInput: `[{"role":"assistant","content":"last"}]`, wantResp: `[{"type":"message","role":"assistant"}]`},
		{name: "single_element", input: `[{"role":"user","content":"only one"}]`, wantInput: `[{"role":"user","content":"only one"}]`},
		{name: "array_of_primitives", input: `[1,2,3]`, wantInput: `[3]`},
		{name: "null_last_element", input: `[{"role":"user","content":"x"},null]`, wantInput: `[null]`},
		{name: "nested_object", input: `[{"role":"user","content":{"nested":{"deep":{"value":42}}}}]`,
			wantInput: `[{"role":"user","content":{"nested":{"deep":{"value":42}}}}]`},
		{name: "unicode_content", input: `[{"role":"user","content":"hello 🎉 world ✨"}]`, wantInput: `[{"role":"user","content":"hello 🎉 world ✨"}]`},
		{name: "leading_whitespace_array", input: "   [{\"role\":\"user\",\"content\":\"ok\"}]", wantInput: `[{"role":"user","content":"ok"}]`},
		{name: "large_array_last_only", input: big.String(), wantInput: `[{"role":"assistant","content":"last"}]`},

		// realtime.turn keeps the whole history on both columns
		{name: "realtime_turn_passthrough", objectType: "realtime.turn", input: multi, resp: multiResp, wantInput: multi, wantResp: multiResp},
		{name: "realtime_turn_malformed_passthrough", objectType: "realtime.turn", input: `[{"role":"user"`, wantInput: `[{"role":"user"`},

		// empty / non-array / invalid payloads are returned unchanged
		{name: "empty_string", input: "", wantInput: ""},
		{name: "empty_array", input: `[]`, wantInput: `[]`},
		{name: "top_level_object", input: `{"not":"an array"}`, wantInput: `{"not":"an array"}`},
		{name: "null_literal", input: `null`, wantInput: `null`},
		{name: "number_literal", input: `42`, wantInput: `42`},
		{name: "whitespace_only", input: "   \t  ", wantInput: "   \t  "},
		{name: "unterminated_object", input: `[{"role":"user","content":"hi"`, wantInput: `[{"role":"user","content":"hi"`},
		{name: "garbage_after_bracket", input: `[abc, not json]`, wantInput: `[abc, not json]`},
		{name: "trailing_comma", input: `[{"role":"user","content":"hi"},]`, wantInput: `[{"role":"user","content":"hi"},]`},
		{name: "nan_value", input: `[NaN]`, wantInput: `[NaN]`},

		// mirror column on its own
		{name: "responses_only_truncated", resp: multiResp, wantResp: `[{"type":"message","role":"assistant"}]`},
		{name: "responses_only_malformed", resp: `[{"type":"message"`, wantResp: `[{"type":"message"`},
		{name: "responses_only_empty_array", resp: `[]`, wantResp: `[]`},
	}

	ids := make(map[string]int, len(cases))
	for i, c := range cases {
		id := insertMalformedLog(t, store.db, malformedHistoryCase{
			name:         c.name,
			objectType:   c.objectType,
			inputHistory: c.input,
			respHistory:  c.resp,
		}, now.Add(-time.Duration(i)*time.Second))
		ids[id] = i
	}

	result, err := store.SearchLogs(ctx, SearchFilters{}, PaginationOptions{Limit: 1000})
	require.NoError(t, err)
	require.Len(t, result.Logs, len(cases), "every inserted row must come back")

	seen := make(map[string]bool, len(cases))
	for _, l := range result.Logs {
		i, ok := ids[l.ID]
		require.Truef(t, ok, "unexpected row %s in result", l.ID)
		c := cases[i]
		seen[l.ID] = true
		assert.Equalf(t, c.wantInput, l.InputHistory, "case %q: input_history", c.name)
		assert.Equalf(t, c.wantResp, l.ResponsesInputHistory, "case %q: responses_input_history", c.name)
	}
	assert.Len(t, seen, len(cases), "every case must be asserted exactly once")
}

// TestBifrostSafeJsonb_DirectInvocation exercises the PL/pgSQL helper in
// isolation so a regression in the function body shows up here rather than
// only at the list-query level. Each subtest asserts the exact TEXT the
// function returns so we can also detect silent behaviour drift on the
// happy path (e.g. canonical jsonb spacing changes between PG versions).
func TestBifrostSafeJsonb_DirectInvocation(t *testing.T) {
	_, db := setupPerfTestDB(t)

	bs := "\\"
	u0000 := bs + "u0000"
	uD800 := bs + "uD800"
	uDC00 := bs + "uDC00"

	// Build large valid array — last element should be `{"last": true}`.
	var big strings.Builder
	big.WriteByte('[')
	for i := 0; i < 500; i++ {
		big.WriteString(`{"i":`)
		big.WriteByte(byte('0' + (i % 10)))
		big.WriteString(`},`)
	}
	big.WriteString(`{"last":true}]`)

	cases := []struct {
		name string
		in   string
		want string
	}{
		// fast-path bypasses
		{name: "empty_string", in: "", want: ""},
		{name: "empty_array", in: "[]", want: "[]"},

		// happy path: last-element extraction
		{name: "valid_two_element_array", in: `[{"a":1},{"b":2}]`, want: `[{"b": 2}]`},
		{name: "single_element_array", in: `[{"a":1}]`, want: `[{"a": 1}]`},
		{name: "array_of_primitives", in: `[1,2,3]`, want: `[3]`},
		{name: "array_with_null_last", in: `[{"a":1},null]`, want: `[null]`},
		{name: "nested_object_last", in: `[{"x":{"y":{"z":42}}}]`, want: `[{"x": {"y": {"z": 42}}}]`},
		{name: "large_valid_array_last_only", in: big.String(), want: `[{"last": true}]`},

		// non-array values: function returns raw TEXT unchanged
		{name: "object_not_array_returns_raw", in: `{"x":1}`, want: `{"x":1}`},
		{name: "null_literal_returns_raw", in: `null`, want: `null`},
		{name: "number_literal_returns_raw", in: `42`, want: `42`},
		{name: "string_literal_returns_raw", in: `"hello"`, want: `"hello"`},
		{name: "whitespace_only_returns_raw", in: "   \t  ", want: "   \t  "},

		// malformed: EXCEPTION branch catches, returns raw TEXT
		{name: "unterminated_returns_raw", in: `[{"role":"user"`, want: `[{"role":"user"`},
		{name: "garbage_after_bracket_returns_raw", in: `[abc]`, want: `[abc]`},
		{name: "trailing_comma_returns_raw", in: `[1,2,]`, want: `[1,2,]`},
		{name: "nan_returns_raw", in: `[NaN]`, want: `[NaN]`},
		{name: "infinity_returns_raw", in: `[Infinity]`, want: `[Infinity]`},
		{name: "high_surrogate_returns_raw", in: `[{"c":"` + uD800 + `"}]`, want: `[{"c":"` + uD800 + `"}]`},
		{name: "low_surrogate_returns_raw", in: `[{"c":"` + uDC00 + `"}]`, want: `[{"c":"` + uDC00 + `"}]`},
		{name: "bad_surrogate_pair_returns_raw", in: `[{"c":"` + uD800 + bs + `u0041"}]`, want: `[{"c":"` + uD800 + bs + `u0041"}]`},
		{name: "u0000_escape_returns_raw", in: `[{"c":"x` + u0000 + `y"}]`, want: `[{"c":"x` + u0000 + `y"}]`},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got string
			err := db.Raw("SELECT bifrost_safe_jsonb(?)", c.in).Scan(&got).Error
			require.NoError(t, err, "bifrost_safe_jsonb must not propagate parse errors")
			assert.Equal(t, c.want, got)
		})
	}
}

// TestBifrostSafeJsonb_NullInput verifies the function handles SQL NULL
// (distinct from empty string) by returning NULL without erroring.
func TestBifrostSafeJsonb_NullInput(t *testing.T) {
	_, db := setupPerfTestDB(t)

	var got *string
	err := db.Raw("SELECT bifrost_safe_jsonb(NULL::text)").Scan(&got).Error
	require.NoError(t, err, "bifrost_safe_jsonb(NULL) must not error")
	assert.Nil(t, got, "bifrost_safe_jsonb(NULL) should return NULL")
}

// setupPGTextSafetyStore returns a migrated Postgres store with the text-safety
// callbacks installed and the GIN expression indexes built, so inserts evaluate
// metadata::jsonb and team_ids::jsonb exactly as production does.
func setupPGTextSafetyStore(t *testing.T) (*RDBLogStore, *gorm.DB) {
	t.Helper()
	store, db := setupPerfTestDB(t)
	registerPGTextSafetyCallbacks(db)
	ctx := context.Background()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	conn, err := sqlDB.Conn(ctx)
	require.NoError(t, err)
	defer conn.Close()
	require.NoError(t, ensureMetadataGINIndex(ctx, conn))
	require.NoError(t, ensureMultiTeamBusinessUnitGINIndexes(ctx, conn))
	return store, db
}

func pgTextSafetyBatch(prefix string, n int) []*Log {
	now := time.Now().UTC()
	entries := make([]*Log, n)
	for i := range entries {
		entries[i] = &Log{
			ID:        fmt.Sprintf("%s-%03d", prefix, i),
			Timestamp: now,
			Provider:  "openai",
			Model:     "gpt-4o",
			Status:    "success",
			Object:    "chat.completion",
		}
	}
	return entries
}

// TestPGTextSafety_PoisonedRowDoesNotFailBatch pins that one row carrying bytes
// Postgres rejects (a raw NUL, invalid UTF-8, or a JSON escape jsonb refuses in
// an indexed column) no longer fails the whole multi-row INSERT with SQLSTATE
// 22021 / 22P05 / 22P02, which made the writer fall back to per-row inserts.
func TestPGTextSafety_PoisonedRowDoesNotFailBatch(t *testing.T) {
	store, db := setupPGTextSafetyStore(t)
	ctx := context.Background()

	cases := []struct {
		name   string
		poison func(l *Log)
	}{
		{"raw_nul_in_raw_response", func(l *Log) { l.RawResponse = "a\x00b" }},
		{"raw_nul_in_varchar_model", func(l *Log) { l.Model = "gpt\x00-4o" }},
		{"invalid_utf8_in_passthrough_body", func(l *Log) { l.PassthroughResponseBody = "ID3\x03\xff\xfb" }},
		{"invalid_utf8_in_serialized_history", func(l *Log) {
			content := "caf\xc3"
			l.InputHistoryParsed = []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &content}}}
		}},
		{"nul_escape_in_gin_indexed_metadata", func(l *Log) { l.MetadataParsed = map[string]interface{}{"note": "a\x00b"} }},
		{"nul_escape_in_gin_indexed_team_ids", func(l *Log) { l.TeamIDsParsed = []string{"team\x00"} }},
		{"lone_surrogate_in_gin_indexed_metadata", func(l *Log) {
			l.MetadataParsed = map[string]interface{}{"raw": json.RawMessage(`"\ud800"`)}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.NoError(t, db.Exec("TRUNCATE logs").Error)
			entries := pgTextSafetyBatch(c.name, 67)
			c.poison(entries[33])
			require.NoError(t, store.BatchCreateIfNotExists(ctx, entries), "one poisoned row must not fail the batch")
			var count int64
			require.NoError(t, db.Model(&Log{}).Count(&count).Error)
			assert.Equal(t, int64(67), count)
		})
	}
}

// TestPGTextSafety_JSONColumnsStayJSONBCastable pins that every JSON column the
// ranking and dimension queries cast with ::jsonb stays castable after a NUL or
// a lone surrogate reaches it, so one row cannot error a whole dashboard query.
func TestPGTextSafety_JSONColumnsStayJSONBCastable(t *testing.T) {
	store, db := setupPGTextSafetyStore(t)
	ctx := context.Background()

	entries := pgTextSafetyBatch("jsonb-cast", 2)
	msg := "upstream said a\x00b"
	entries[0].Status = "error"
	entries[0].ErrorDetailsParsed = &schemas.BifrostError{Error: &schemas.ErrorField{Message: msg}}
	out := "reply a\x00b"
	entries[0].OutputMessageParsed = &schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: &out}}
	entries[1].ParamsParsed = map[string]interface{}{"raw": json.RawMessage(`"\udc00"`)}
	require.NoError(t, store.BatchCreateIfNotExists(ctx, entries))

	var n int64
	err := db.Raw(`SELECT COUNT(*) FROM logs WHERE
		(NULLIF(error_details, '')::jsonb IS NOT NULL OR error_details IS NULL OR error_details = '')
		AND (NULLIF(output_message, '')::jsonb IS NOT NULL OR output_message IS NULL OR output_message = '')
		AND (NULLIF(params, '')::jsonb IS NOT NULL OR params IS NULL OR params = '')`).Scan(&n).Error
	require.NoError(t, err, "stored JSON columns must cast to jsonb")
	assert.Equal(t, int64(2), n)
}

// TestPGTextSafety_LiteralBackslashU0000RoundTrips pins that text which merely
// mentions the six characters \u0000 (an escaped backslash in JSON) is stored
// unchanged, rather than being cut into a different escape or invalid JSON.
func TestPGTextSafety_LiteralBackslashU0000RoundTrips(t *testing.T) {
	store, _ := setupPGTextSafetyStore(t)
	ctx := context.Background()

	for i, content := range []string{`in Go write \u0000 for NUL`, `ends with \u0000`, `C:\\u0000dir`} {
		entry := pgTextSafetyBatch(fmt.Sprintf("literal-%d", i), 1)[0]
		c := content
		entry.InputHistoryParsed = []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &c}}}
		require.NoError(t, store.BatchCreateIfNotExists(ctx, []*Log{entry}))

		got, err := store.FindByID(ctx, entry.ID)
		require.NoError(t, err)
		require.Len(t, got.InputHistoryParsed, 1, "input_history must stay valid JSON: %q", got.InputHistory)
		require.NotNil(t, got.InputHistoryParsed[0].Content)
		require.NotNil(t, got.InputHistoryParsed[0].Content.ContentStr)
		assert.Equal(t, content, *got.InputHistoryParsed[0].Content.ContentStr)
	}
}

// TestPGTextSafety_UpdatesAndMCPLogs pins the same guarantee on the update
// paths (struct and map targets) and on MCP tool logs.
func TestPGTextSafety_UpdatesAndMCPLogs(t *testing.T) {
	store, _ := setupPGTextSafetyStore(t)
	ctx := context.Background()

	entry := pgTextSafetyBatch("update", 1)[0]
	require.NoError(t, store.BatchCreateIfNotExists(ctx, []*Log{entry}))
	require.NoError(t, store.Update(ctx, entry.ID, &Log{RawRequest: "req\x00body"}), "struct update")
	require.NoError(t, store.Update(ctx, entry.ID, map[string]interface{}{"raw_response": "resp\x00body\xff"}), "map update")

	got, err := store.FindByID(ctx, entry.ID)
	require.NoError(t, err)
	assert.Equal(t, "reqbody", got.RawRequest)
	assert.Equal(t, "respbody\uFFFD", got.RawResponse)

	now := time.Now().UTC()
	mcp := &MCPToolLog{
		ID:              "mcp-text-safety",
		Timestamp:       now,
		ToolName:        "search\x00",
		Status:          "success",
		ArgumentsParsed: map[string]interface{}{"q": "caf\xc3"},
		ResultParsed:    "found\x00",
	}
	require.NoError(t, store.BatchCreateMCPToolLogsIfNotExists(ctx, []*MCPToolLog{mcp}))
}

// TestPGTextSafety_PlainTextColumnsKeepEscapeText pins that plain-text columns
// which merely start with "{" or "[" are not run through the JSON escape
// sanitizer: routing_engine_logs always starts with "[<timestamp>]", and
// content_summary does whenever a message starts with JSON. Neither is cast to
// jsonb, so the six characters \u0000 or \ud800 in them must survive as typed.
func TestPGTextSafety_PlainTextColumnsKeepEscapeText(t *testing.T) {
	store, db := setupPGTextSafetyStore(t)
	ctx := context.Background()

	routing := "[1700000000] [routing] [info] - matched rule for \\u0000 and \\ud800\n"
	summary := `{"note": "literal \u0000 and \ud800 in a pasted payload"}`
	entry := pgTextSafetyBatch("plain-text", 1)[0]
	entry.RoutingEngineLogs = routing
	require.NoError(t, store.BatchCreateIfNotExists(ctx, []*Log{entry}))
	// content_summary is rebuilt by SerializeFields on insert, so set it with a
	// map update, which is how the column is written without a rebuild.
	require.NoError(t, store.Update(ctx, entry.ID, map[string]interface{}{"content_summary": summary}))

	var got struct {
		RoutingEngineLogs string
		ContentSummary    string
	}
	require.NoError(t, db.Raw("SELECT routing_engine_logs, content_summary FROM logs WHERE id = ?", entry.ID).Scan(&got).Error)
	assert.Equal(t, routing, got.RoutingEngineLogs)
	assert.Equal(t, summary, got.ContentSummary)
}

// TestPGTextSafety_MapUpdateShapes pins the update-map shapes GORM accepts:
// JSON with leading whitespace (Postgres IS JSON accepts it, so the
// metadata::jsonb index still casts it), a pointer to the map (GORM unwraps
// it), and keys spelled as Go field names (GORM resolves them to columns).
func TestPGTextSafety_MapUpdateShapes(t *testing.T) {
	store, db := setupPGTextSafetyStore(t)
	ctx := context.Background()

	entry := pgTextSafetyBatch("map-shapes", 1)[0]
	require.NoError(t, store.BatchCreateIfNotExists(ctx, []*Log{entry}))
	readColumns := func() (out struct {
		Metadata          string
		RawResponse       string
		ContentSummary    string
		RoutingEngineLogs string
	}) {
		require.NoError(t, db.Raw("SELECT metadata, raw_response, content_summary, routing_engine_logs FROM logs WHERE id = ?", entry.ID).Scan(&out).Error)
		return out
	}

	t.Run("leading_whitespace_json", func(t *testing.T) {
		require.NoError(t, store.Update(ctx, entry.ID, map[string]interface{}{"metadata": " \n\t{\"note\":\"a\\u0000b\"}"}),
			"metadata with whitespace before { must still be made jsonb-castable")
		assert.Equal(t, " \n\t{\"note\":\"ab\"}", readColumns().Metadata)
	})

	t.Run("pointer_to_map", func(t *testing.T) {
		updates := map[string]interface{}{"raw_response": "resp\x00body"}
		require.NoError(t, store.Update(ctx, entry.ID, &updates), "a *map update must be cleaned like a map")
		assert.Equal(t, "respbody", readColumns().RawResponse)
	})

	t.Run("go_field_name_keys", func(t *testing.T) {
		summary := `{"note": "literal \u0000 and \ud800"}`
		routing := "[1700000000] [routing] [info] - literal \\u0000 and \\ud800\n"
		require.NoError(t, store.Update(ctx, entry.ID, map[string]interface{}{"ContentSummary": summary, "RoutingEngineLogs": routing}))
		got := readColumns()
		assert.Equal(t, summary, got.ContentSummary, "plain-text column named by its Go field must keep its escape text")
		assert.Equal(t, routing, got.RoutingEngineLogs)
	})
}

// TestPGTextSafety_StructUpdateNULOnlyValueIsWritten pins that a struct update
// whose value is only NUL bytes still replaces the column. Cleaning it to ""
// would make GORM's struct Updates skip the field as a zero value and silently
// keep the previous value, so it is stored as U+FFFD instead.
func TestPGTextSafety_StructUpdateNULOnlyValueIsWritten(t *testing.T) {
	store, db := setupPGTextSafetyStore(t)
	ctx := context.Background()

	entry := pgTextSafetyBatch("nul-only", 1)[0]
	entry.RawRequest = "previous body"
	require.NoError(t, store.BatchCreateIfNotExists(ctx, []*Log{entry}))
	require.NoError(t, store.Update(ctx, entry.ID, &Log{RawRequest: "\x00\x00"}))

	var got string
	require.NoError(t, db.Raw("SELECT raw_request FROM logs WHERE id = ?", entry.ID).Scan(&got).Error)
	assert.Equal(t, "�", got, "a NUL-only update must replace the previous value, not be skipped")
}
