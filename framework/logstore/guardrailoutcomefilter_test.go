package logstore

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// guardrailOutcomeFixtures returns one log per guardrail shape: a recorded
// block, a legacy block with only the intervention error, a detect-only match,
// a judge call that allowed the request, an unrelated error and a clean
// success. IDs are prefixed so shared databases can be filtered and cleaned.
func guardrailOutcomeFixtures(prefix string, at time.Time) []*Log {
	ruleID := uint(9)
	return []*Log{
		{
			ID: prefix + "blocked", Timestamp: at, Provider: "openai", Model: "gpt-4o", Status: "error", Object: "chat.completion",
			ErrorDetailsParsed: &schemas.BifrostError{
				StatusCode: new(400),
				Error:      &schemas.ErrorField{Type: new("guardrail_intervention"), Message: "Secret detected"},
			},
			GuardrailDebugParsed: &schemas.BifrostGuardrailMetadata{Interventions: []schemas.BifrostGuardrailIntervention{
				{Phase: "input", RuleID: &ruleID, RuleName: "block-secrets", GuardrailName: "azure", Reason: "Secret detected"},
			}},
		},
		{
			ID: prefix + "legacy-blocked", Timestamp: at.Add(time.Second), Provider: "openai", Model: "gpt-4o", Status: "error", Object: "chat.completion",
			ErrorDetailsParsed: &schemas.BifrostError{
				StatusCode: new(400),
				Error:      &schemas.ErrorField{Type: new("guardrail_intervention"), Message: "Request blocked by guardrail"},
			},
		},
		{
			ID: prefix + "detected", Timestamp: at.Add(2 * time.Second), Provider: "openai", Model: "gpt-4o", Status: "success", Object: "chat.completion",
			GuardrailDebugParsed: &schemas.BifrostGuardrailMetadata{Detections: []schemas.BifrostGuardrailDetection{
				{Phase: "output", RuleName: "pii-audit", GuardrailName: "bedrock", Reason: "PII found"},
			}},
		},
		{
			ID: prefix + "judge-allowed", Timestamp: at.Add(3 * time.Second), Provider: "openai", Model: "gpt-4o", Status: "success", Object: "chat.completion",
			GuardrailDebugParsed: &schemas.BifrostGuardrailMetadata{JudgeCalls: []schemas.BifrostGuardrailJudgeCall{
				{Phase: "input", RuleName: "judge", Action: "NONE", TotalTokens: 12},
			}},
		},
		{
			ID: prefix + "rate-limited", Timestamp: at.Add(4 * time.Second), Provider: "openai", Model: "gpt-4o", Status: "error", Object: "chat.completion",
			ErrorDetailsParsed: &schemas.BifrostError{
				StatusCode: new(429),
				Error:      &schemas.ErrorField{Type: new("rate_limit_error"), Message: "slow down"},
			},
		},
		{ID: prefix + "clean", Timestamp: at.Add(5 * time.Second), Provider: "openai", Model: "gpt-4o", Status: "success", Object: "chat.completion"},
	}
}

// assertGuardrailOutcomeFilter seeds the fixtures into store and checks each
// outcome combination returns exactly the rows a guardrail acted on.
func assertGuardrailOutcomeFilter(t *testing.T, store LogStore, prefix string, at time.Time) {
	t.Helper()
	ctx := context.Background()
	for _, entry := range guardrailOutcomeFixtures(prefix, at) {
		require.NoError(t, entry.SerializeFields())
		require.NoError(t, store.CreateIfNotExists(ctx, entry))
	}
	start, end := at.Add(-time.Minute), at.Add(time.Minute)

	cases := []struct {
		outcomes []string
		want     []string
	}{
		{[]string{GuardrailOutcomeBlocked}, []string{"blocked", "legacy-blocked"}},
		{[]string{GuardrailOutcomeDetected}, []string{"detected"}},
		{[]string{GuardrailOutcomeBlocked, GuardrailOutcomeDetected}, []string{"blocked", "legacy-blocked", "detected"}},
		{[]string{"unknown"}, nil},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprint(tc.outcomes), func(t *testing.T) {
			result, err := store.SearchLogs(ctx, SearchFilters{StartTime: &start, EndTime: &end, GuardrailOutcomes: tc.outcomes}, PaginationOptions{Limit: 50})
			require.NoError(t, err)
			var got []string
			for _, row := range result.Logs {
				got = append(got, row.ID[len(prefix):])
			}
			want := slices.Clone(tc.want)
			slices.Sort(got)
			slices.Sort(want)
			assert.Equal(t, want, got)
		})
	}

	// The list rows carry what the Incidents view renders: the guardrail
	// metadata, and error details for guardrail blocks only, never for other
	// failures whose provider payload is unbounded.
	result, err := store.SearchLogs(ctx, SearchFilters{StartTime: &start, EndTime: &end}, PaginationOptions{Limit: 50})
	require.NoError(t, err)
	listed := map[string]Log{}
	for _, row := range result.Logs {
		listed[row.ID[len(prefix):]] = row
	}
	require.NotNil(t, listed["blocked"].GuardrailDebugParsed)
	assert.Equal(t, "block-secrets", listed["blocked"].GuardrailDebugParsed.Interventions[0].RuleName)
	require.NotNil(t, listed["detected"].GuardrailDebugParsed)
	assert.Equal(t, "pii-audit", listed["detected"].GuardrailDebugParsed.Detections[0].RuleName)
	require.NotNil(t, listed["legacy-blocked"].ErrorDetailsParsed)
	assert.Equal(t, "Request blocked by guardrail", listed["legacy-blocked"].ErrorDetailsParsed.Error.Message)
	assert.Nil(t, listed["rate-limited"].ErrorDetailsParsed, "non-guardrail errors stay out of the list")
}

// TestGuardrailOutcomeFilterSQLite verifies the guardrail outcome filter on SQLite.
func TestGuardrailOutcomeFilterSQLite(t *testing.T) {
	assertGuardrailOutcomeFilter(t, newTestSQLiteStore(t), "gof-", time.Date(2026, 3, 4, 5, 6, 0, 0, time.UTC))
}

// TestGuardrailOutcomeFilterPostgres verifies the guardrail outcome filter on Postgres.
func TestGuardrailOutcomeFilterPostgres(t *testing.T) {
	db := trySetupPostgresDB(t)
	if db == nil {
		t.Skip("Postgres not available")
	}
	// The bifrost_safe_jsonb helper SearchLogs relies on is created by the
	// migrations, so run them on a clean schema exactly as the parity tests do.
	dropAllManagedMatViews(db)
	require.NoError(t, db.Exec("DROP TABLE IF EXISTS mcp_tool_logs CASCADE").Error)
	require.NoError(t, db.Exec("DROP TABLE IF EXISTS async_jobs CASCADE").Error)
	require.NoError(t, db.Exec("DROP TABLE IF EXISTS webhook_deliveries CASCADE").Error)
	require.NoError(t, db.Exec("DROP TABLE IF EXISTS logs CASCADE").Error)
	require.NoError(t, db.Exec("CREATE TABLE IF NOT EXISTS migrations (id VARCHAR(255) PRIMARY KEY)").Error)
	require.NoError(t, db.Exec("DELETE FROM migrations").Error)
	require.NoError(t, triggerMigrations(context.Background(), db, testLogger{}))
	prefix := fmt.Sprintf("gof-pg-%d-", time.Now().UnixNano())
	store := &RDBLogStore{db: db, logger: testLogger{}}
	// A fixed historical window keeps rows written by other tests out of the result.
	assertGuardrailOutcomeFilter(t, store, prefix, time.Date(2011, 7, 8, 9, 10, 0, 0, time.UTC))
}

// TestGuardrailOutcomeFilterClickHouse verifies the guardrail outcome filter on ClickHouse.
func TestGuardrailOutcomeFilterClickHouse(t *testing.T) {
	store := trySetupClickHouseStore(t)
	if store == nil {
		t.Skip("ClickHouse not available")
	}
	prefix := fmt.Sprintf("gof-ch-%d-", time.Now().UnixNano())
	assertGuardrailOutcomeFilter(t, store, prefix, time.Date(2012, 7, 8, 9, 10, 0, 0, time.UTC))
}

// TestHybrid_GuardrailDebugStaysQueryable verifies guardrail_debug is kept in
// the database row so the outcome filter works with object storage on.
func TestHybrid_GuardrailDebugStaysQueryable(t *testing.T) {
	hybrid, inner, _ := newTestHybrid(t)
	defer hybrid.Close(context.Background())
	ctx := context.Background()

	input := "hello"
	now := time.Now().UTC()
	entry := &Log{
		ID: "log-guardrail", Timestamp: now, Provider: "openai", Model: "gpt-4o", Status: "success", Object: "chat.completion",
		InputHistoryParsed:  []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: &input}}},
		OutputMessageParsed: &schemas.ChatMessage{Content: &schemas.ChatMessageContent{ContentStr: new("reply")}},
		GuardrailDebugParsed: &schemas.BifrostGuardrailMetadata{Detections: []schemas.BifrostGuardrailDetection{
			{Phase: "input", RuleName: "pii-audit", Reason: "PII found"},
		}},
	}
	require.NoError(t, entry.SerializeFields())
	require.NoError(t, hybrid.CreateIfNotExists(ctx, entry))
	waitForOffload(t, inner, "log-guardrail")

	row, err := inner.FindByID(ctx, "log-guardrail")
	require.NoError(t, err)
	assert.Contains(t, row.GuardrailDebug, "pii-audit", "guardrail_debug must stay in the database row")
	assert.Empty(t, row.OutputMessage, "content is still offloaded")

	start, end := now.Add(-time.Hour), now.Add(time.Hour)
	found, err := hybrid.SearchLogs(ctx, SearchFilters{StartTime: &start, EndTime: &end, GuardrailOutcomes: []string{GuardrailOutcomeDetected}}, PaginationOptions{Limit: 10})
	require.NoError(t, err)
	assert.Len(t, found.Logs, 1)
}
