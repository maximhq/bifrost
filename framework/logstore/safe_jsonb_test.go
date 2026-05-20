package logstore

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
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
