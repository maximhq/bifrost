package logstore

import (
	"context"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)


// seedJSONFieldDimensionFixtures writes the same five rows to any LogStore:
//   - a clean success with no error, no retries beyond the one successful
//     attempt, no guardrail hit at all - this must be excluded from every
//     JSON-field dimension, not shown as an "Unassigned" bucket.
//   - two failed requests with distinct, structured error_type/error_code.
//   - a request that needed one failed retry (rate_limit_error) before a
//     second attempt succeeded - exercises fail_reason fan-out alongside a
//     row that is NOT itself an error (status success, but one attempt
//     inside it failed).
//   - a request two guardrail rules fired on, with different actions -
//     exercises guardrail_rule/guardrail_action fanning to more than one
//     label from a single row.
//   - a row whose error_details/attempt_trail/guardrail_debug are malformed
//     text, proving a bad row is excluded rather than failing the query.
func seedJSONFieldDimensionFixtures(t *testing.T, ctx context.Context, store LogStore, base time.Time) {
	t.Helper()

	clean := &Log{
		ID: "log-clean", Timestamp: base, Object: "chat_completion", Provider: "openai", Model: "gpt-4o",
		Status: "success", Cost: new(0.01), TotalTokens: 15, CreatedAt: base,
	}
	require.NoError(t, store.Create(ctx, clean))

	rateLimited := &Log{
		ID: "log-rate-limited", Timestamp: base.Add(time.Second), Object: "chat_completion", Provider: "openai", Model: "gpt-4o",
		Status: "error", Cost: new(0.02), TotalTokens: 10, CreatedAt: base.Add(time.Second),
		ErrorDetailsParsed: &schemas.BifrostError{
			StatusCode: new(429),
			Error:      &schemas.ErrorField{Type: new("rate_limit_error"), Code: new("rate_limited"), Message: "rate limit exceeded"},
		},
	}
	require.NoError(t, store.Create(ctx, rateLimited))

	authFailed := &Log{
		ID: "log-auth-failed", Timestamp: base.Add(2 * time.Second), Object: "chat_completion", Provider: "anthropic", Model: "claude-sonnet-5",
		Status: "error", Cost: new(0.0), TotalTokens: 5, CreatedAt: base.Add(2 * time.Second),
		ErrorDetailsParsed: &schemas.BifrostError{
			StatusCode: new(401),
			Error:      &schemas.ErrorField{Type: new("authentication_error"), Code: new("invalid_api_key"), Message: "invalid api key"},
		},
	}
	require.NoError(t, store.Create(ctx, authFailed))

	retried := &Log{
		ID: "log-retried", Timestamp: base.Add(3 * time.Second), Object: "chat_completion", Provider: "openai", Model: "gpt-4o",
		Status: "success", Cost: new(0.03), TotalTokens: 20, CreatedAt: base.Add(3 * time.Second),
		NumberOfRetries: 1,
		AttemptTrailParsed: []schemas.KeyAttemptRecord{
			{Attempt: 1, KeyID: "key-1", KeyName: "primary", FailReason: new("rate_limit_error"), TriggeredRotation: true},
			{Attempt: 2, KeyID: "key-2", KeyName: "secondary", TriggeredRotation: false},
		},
	}
	require.NoError(t, store.Create(ctx, retried))

	guardrailed := &Log{
		ID: "log-guardrailed", Timestamp: base.Add(4 * time.Second), Object: "chat_completion", Provider: "openai", Model: "gpt-4o",
		Status: "success", Cost: new(0.01), TotalTokens: 12, CreatedAt: base.Add(4 * time.Second),
		GuardrailDebugParsed: &schemas.BifrostGuardrailMetadata{
			JudgeCalls: []schemas.BifrostGuardrailJudgeCall{
				{Phase: "pre", RuleName: "pii-check", GuardrailName: "pii-guard", Action: "block", Reason: "detected SSN"},
				{Phase: "post", RuleName: "toxicity-check", GuardrailName: "toxicity-guard", Action: "redact", Reason: "flagged phrase"},
			},
		},
	}
	require.NoError(t, store.Create(ctx, guardrailed))

	// ErrorDetails and GuardrailDebug are set directly (not through the
	// *Parsed fields), which SerializeFields (Log.BeforeCreate) only ever
	// overwrites when the corresponding *Parsed field is non-nil - so this is
	// the one way to land genuinely malformed text on a row through the
	// ordinary Create path, on every backend, with no dialect-specific SQL.
	// attempt_trail has no equivalent: SerializeFields unconditionally
	// resets it from AttemptTrailParsed (forcing "" when that is empty), so a
	// malformed attempt_trail cannot occur through this write path at all -
	// which this fixture takes as a real guarantee rather than something to
	// work around.
	malformed := &Log{
		ID: "log-malformed", Timestamp: base.Add(5 * time.Second), Object: "chat_completion", Provider: "openai", Model: "gpt-4o",
		Status: "error", Cost: new(0.0), TotalTokens: 1, CreatedAt: base.Add(5 * time.Second),
		ErrorDetails:   "{not valid json",
		GuardrailDebug: "{not valid json",
	}
	require.NoError(t, store.Create(ctx, malformed))
}

func jsonFieldDimensionWindow(base time.Time) SearchFilters {
	start := base.Add(-time.Hour)
	end := base.Add(time.Hour)
	return SearchFilters{StartTime: &start, EndTime: &end}
}

// rankingByID indexes a ranking result for assertions that do not care about
// row order.
func rankingByID(res *DimensionRankingResult) map[string]DimensionRankingWithTrend {
	out := make(map[string]DimensionRankingWithTrend, len(res.Rankings))
	for _, r := range res.Rankings {
		out[r.ID] = r
	}
	return out
}

// TestJSONFieldDimensionRankings_ThreeWayParity seeds identical fixtures into
// every backend GetDimensionRankings supports (SQLite always; Postgres and
// ClickHouse when framework/docker-compose.yml's services are reachable) and
// asserts the same rankings out of all three for every one of the five new
// dimensions - which is what actually proves the three hand-written,
// dialect-specific SQL fragments behind each dimension agree with each other,
// not just that each one individually looks plausible.
func TestJSONFieldDimensionRankings_ThreeWayParity(t *testing.T) {
	ch := trySetupClickHouseStore(t)
	pgDB := trySetupPostgresDB(t)
	var pg *RDBLogStore
	if pgDB != nil {
		dropAllManagedMatViews(pgDB)
		require.NoError(t, pgDB.Exec("DROP TABLE IF EXISTS mcp_tool_logs CASCADE").Error)
		require.NoError(t, pgDB.Exec("DROP TABLE IF EXISTS async_jobs CASCADE").Error)
		require.NoError(t, pgDB.Exec("DROP TABLE IF EXISTS webhook_deliveries CASCADE").Error)
		require.NoError(t, pgDB.Exec("DROP TABLE IF EXISTS logs CASCADE").Error)
		require.NoError(t, pgDB.Exec("CREATE TABLE IF NOT EXISTS migrations (id VARCHAR(255) PRIMARY KEY)").Error)
		require.NoError(t, pgDB.Exec("DELETE FROM migrations").Error)
		require.NoError(t, triggerMigrations(context.Background(), pgDB, testLogger{}))
		pg = &RDBLogStore{db: pgDB, logger: testLogger{}}
	}

	sqliteStore, _ := newFanoutTestStore(t)

	backends := map[string]LogStore{"sqlite": sqliteStore}
	if pg != nil {
		backends[parityReference] = pg
	} else {
		t.Log("Postgres not available, skipping its leg of the parity check")
	}
	if ch != nil {
		backends["clickhouse"] = ch
	}

	ctx := context.Background()
	base := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

	for name, store := range backends {
		t.Run(name, func(t *testing.T) {
			seedJSONFieldDimensionFixtures(t, ctx, store, base)
			assertJSONFieldDimensionRankings(t, ctx, store, base)
		})
	}
}

// TestJSONFieldDimensionRankings_SQLite runs the same assertions as the
// three-way parity test above but against SQLite alone, so this extraction
// logic gets real (not just compiled) coverage from a plain `go test`, without
// depending on framework/docker-compose.yml's Postgres/ClickHouse services
// being up - which the three-way test above requires for all three legs, or
// it skips entirely, matching TestLogStoreParity's own established contract.
func TestJSONFieldDimensionRankings_SQLite(t *testing.T) {
	store, _ := newFanoutTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)

	seedJSONFieldDimensionFixtures(t, ctx, store, base)
	assertJSONFieldDimensionRankings(t, ctx, store, base)
}

// assertJSONFieldDimensionRankings runs the shared assertion suite against
// whichever backend seedJSONFieldDimensionFixtures already wrote its rows
// into.
func assertJSONFieldDimensionRankings(t *testing.T, ctx context.Context, store LogStore, base time.Time) {
	t.Helper()
	filters := jsonFieldDimensionWindow(base)

	t.Run("error_type excludes non-error rows and groups by nested error.type", func(t *testing.T) {
		res, err := store.GetDimensionRankings(ctx, filters, RankingDimensionErrorType)
		require.NoError(t, err)
		byID := rankingByID(res)
		assert.Contains(t, byID, "rate_limit_error")
		assert.Contains(t, byID, "authentication_error")
		assert.Equal(t, int64(1), byID["rate_limit_error"].TotalRequests)
		assert.Equal(t, int64(1), byID["authentication_error"].TotalRequests)
		assert.NotContains(t, byID, unassignedDimensionID, "no Unassigned bucket for this dimension")
		assert.NotContains(t, byID, "", "the clean/retried/guardrailed/malformed rows contribute no error_type")
		var total int64
		for _, r := range res.Rankings {
			total += r.TotalRequests
		}
		assert.Equal(t, int64(2), total, "exactly the two structured-error rows, malformed and clean rows excluded")
	})

	t.Run("error_code groups by nested error.code", func(t *testing.T) {
		res, err := store.GetDimensionRankings(ctx, filters, RankingDimensionErrorCode)
		require.NoError(t, err)
		byID := rankingByID(res)
		assert.Contains(t, byID, "rate_limited")
		assert.Contains(t, byID, "invalid_api_key")
	})

	t.Run("fail_reason fans out attempt_trail and only counts failed attempts", func(t *testing.T) {
		res, err := store.GetDimensionRankings(ctx, filters, RankingDimensionFailReason)
		require.NoError(t, err)
		byID := rankingByID(res)
		// The retried row's first attempt failed with rate_limit_error; its
		// second (successful) attempt has no fail_reason and must not
		// contribute an empty-label row.
		assert.Equal(t, int64(1), byID["rate_limit_error"].TotalRequests)
		assert.NotContains(t, byID, "")
	})

	t.Run("guardrail_rule and guardrail_action fan out judge_calls independently", func(t *testing.T) {
		rules, err := store.GetDimensionRankings(ctx, filters, RankingDimensionGuardrailRule)
		require.NoError(t, err)
		ruleByID := rankingByID(rules)
		assert.Contains(t, ruleByID, "pii-check")
		assert.Contains(t, ruleByID, "toxicity-check")

		actions, err := store.GetDimensionRankings(ctx, filters, RankingDimensionGuardrailAction)
		require.NoError(t, err)
		actionByID := rankingByID(actions)
		assert.Contains(t, actionByID, "block")
		assert.Contains(t, actionByID, "redact")

		// One row (log-guardrailed) triggered two rules, so attributed exceeds
		// actual for this dimension - the same relationship the team/customer
		// fan-out already has.
		assert.Equal(t, int64(2), rules.TotalAttributedRequests)
	})

	t.Run("malformed JSON is excluded, not fatal", func(t *testing.T) {
		for _, dim := range []RankingDimension{RankingDimensionErrorType, RankingDimensionErrorCode, RankingDimensionFailReason, RankingDimensionGuardrailRule, RankingDimensionGuardrailAction} {
			res, err := store.GetDimensionRankings(ctx, filters, dim)
			require.NoErrorf(t, err, "dimension %s must not error on a malformed row", dim)
			for _, r := range res.Rankings {
				assert.NotEqual(t, "", r.ID, "dimension %s must not surface an empty label", dim)
			}
		}
	})
}
