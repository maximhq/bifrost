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

// guardrailMatchFixtures returns logs whose guardrail matches differ by action,
// rule, profile and phase, so each guardrail match filter can be told apart.
// blocked-a pairs a block by rule 1 with a redaction by rule 2, which is what
// shows that every filter must hold on the same match.
func guardrailMatchFixtures(prefix string, at time.Time) []*Log {
	id := func(value uint) *uint { return &value }
	log := func(name string, offset int, metadata *schemas.BifrostGuardrailMetadata) *Log {
		return &Log{
			ID: prefix + name, Timestamp: at.Add(time.Duration(offset) * time.Second), Provider: "openai", Model: "gpt-4o",
			Status: "success", Object: "chat.completion", GuardrailDebugParsed: metadata,
		}
	}
	blocked := log("blocked-a", 0, &schemas.BifrostGuardrailMetadata{
		Interventions: []schemas.BifrostGuardrailIntervention{{Phase: "input", RuleID: id(1), ProfileID: id(10), RuleName: "rule-a"}},
		Detections:    []schemas.BifrostGuardrailDetection{{Phase: "output", RuleID: id(2), ProfileID: id(20), Action: "redact", Enforced: new(true)}},
	})
	blocked.Status = "error"
	blocked.ErrorDetailsParsed = &schemas.BifrostError{StatusCode: new(400), Error: &schemas.ErrorField{Type: new("guardrail_intervention"), Message: "Blocked"}}
	legacy := log("legacy-blocked", 1, nil)
	legacy.Status = "error"
	legacy.ErrorDetailsParsed = &schemas.BifrostError{StatusCode: new(400), Error: &schemas.ErrorField{Type: new("guardrail_intervention"), Message: "Blocked"}}
	// Stored text that is not JSON, or JSON whose match lists are not arrays,
	// has no matches: no filter selects it and none fails on it.
	malformed := log("malformed", 9, nil)
	malformed.GuardrailDebug = `{"detections": [{"rule_id": 1`
	misshapen := log("misshapen", 10, nil)
	misshapen.GuardrailDebug = `{"interventions": {"rule_id": 1}, "detections": {"rule_id": 1, "action": "redact"}}`
	return []*Log{
		blocked,
		legacy,
		malformed,
		misshapen,
		log("redacted-a", 2, &schemas.BifrostGuardrailMetadata{Detections: []schemas.BifrostGuardrailDetection{
			{Phase: "output", RuleID: id(1), ProfileID: id(10), Action: "redact", Enforced: new(true)},
		}}),
		log("transformed-b", 3, &schemas.BifrostGuardrailMetadata{Detections: []schemas.BifrostGuardrailDetection{
			{Phase: "input", RuleID: id(2), ProfileID: id(20), Action: "transform", Enforced: new(true)},
		}}),
		log("detected-b", 4, &schemas.BifrostGuardrailMetadata{Detections: []schemas.BifrostGuardrailDetection{
			{Phase: "input", RuleID: id(2), ProfileID: id(21), Action: "detect", Enforced: new(true)},
		}}),
		// Detections recorded before actions were: no action and no enforced flag.
		log("detected-legacy", 5, &schemas.BifrostGuardrailMetadata{Detections: []schemas.BifrostGuardrailDetection{
			{Phase: "output", RuleID: id(3), RuleName: "rule-c"},
		}}),
		log("monitored-a", 6, &schemas.BifrostGuardrailMetadata{Detections: []schemas.BifrostGuardrailDetection{
			{Phase: "output", RuleID: id(1), ProfileID: id(10), Action: "block", Enforced: new(false)},
		}}),
		log("judge-only", 7, &schemas.BifrostGuardrailMetadata{JudgeCalls: []schemas.BifrostGuardrailJudgeCall{
			{Phase: "input", RuleID: id(1), ProfileID: id(10), Action: "NONE", TotalTokens: 12},
		}}),
		log("clean", 8, nil),
	}
}

// assertGuardrailMatchFilter seeds the match fixtures into store and checks
// each guardrail match filter combination returns exactly the expected rows.
func assertGuardrailMatchFilter(t *testing.T, store LogStore, prefix string, at time.Time) {
	t.Helper()
	ctx := context.Background()
	for _, entry := range guardrailMatchFixtures(prefix, at) {
		require.NoError(t, entry.SerializeFields())
		require.NoError(t, store.CreateIfNotExists(ctx, entry))
	}
	start, end := at.Add(-time.Minute), at.Add(time.Minute)

	cases := []struct {
		name    string
		filters SearchFilters
		want    []string
	}{
		{"blocked", SearchFilters{GuardrailActions: []string{GuardrailMatchActionBlocked}}, []string{"blocked-a", "legacy-blocked"}},
		{"redacted covers transforms", SearchFilters{GuardrailActions: []string{GuardrailMatchActionRedacted}}, []string{"blocked-a", "redacted-a", "transformed-b"}},
		{"detected", SearchFilters{GuardrailActions: []string{GuardrailMatchActionDetected}}, []string{"detected-b", "detected-legacy"}},
		{"monitored", SearchFilters{GuardrailActions: []string{GuardrailMatchActionMonitored}}, []string{"monitored-a"}},
		{"rule with any action", SearchFilters{GuardrailRuleIDs: []string{"1"}}, []string{"blocked-a", "redacted-a", "monitored-a"}},
		{"rule and action on the same match", SearchFilters{GuardrailActions: []string{GuardrailMatchActionBlocked}, GuardrailRuleIDs: []string{"1"}}, []string{"blocked-a"}},
		{"rule and action on different matches", SearchFilters{GuardrailActions: []string{GuardrailMatchActionBlocked}, GuardrailRuleIDs: []string{"2"}}, nil},
		{"profile", SearchFilters{GuardrailProfileIDs: []string{"20"}}, []string{"blocked-a", "transformed-b"}},
		{"phase and action", SearchFilters{GuardrailActions: []string{GuardrailMatchActionRedacted}, GuardrailPhases: []string{"input"}}, []string{"transformed-b"}},
		{"unknown action", SearchFilters{GuardrailActions: []string{"unknown"}}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			filters := tc.filters
			filters.StartTime, filters.EndTime = &start, &end
			result, err := store.SearchLogs(ctx, filters, PaginationOptions{Limit: 50})
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
}

// TestGuardrailMatchFilterSQLite verifies the guardrail match filters on SQLite.
func TestGuardrailMatchFilterSQLite(t *testing.T) {
	assertGuardrailMatchFilter(t, newTestSQLiteStore(t), "gmf-", time.Date(2026, 3, 4, 5, 6, 0, 0, time.UTC))
}

// TestGuardrailMatchFilterPostgres verifies the guardrail match filters on Postgres.
func TestGuardrailMatchFilterPostgres(t *testing.T) {
	db := trySetupPostgresDB(t)
	if db == nil {
		t.Skip("Postgres not available")
	}
	// Migrations run on a clean schema, as TestGuardrailOutcomeFilterPostgres does.
	dropAllManagedMatViews(db)
	require.NoError(t, db.Exec("DROP TABLE IF EXISTS mcp_tool_logs CASCADE").Error)
	require.NoError(t, db.Exec("DROP TABLE IF EXISTS async_jobs CASCADE").Error)
	require.NoError(t, db.Exec("DROP TABLE IF EXISTS webhook_deliveries CASCADE").Error)
	require.NoError(t, db.Exec("DROP TABLE IF EXISTS logs CASCADE").Error)
	require.NoError(t, db.Exec("CREATE TABLE IF NOT EXISTS migrations (id VARCHAR(255) PRIMARY KEY)").Error)
	require.NoError(t, db.Exec("DELETE FROM migrations").Error)
	require.NoError(t, triggerMigrations(context.Background(), db, testLogger{}))
	prefix := fmt.Sprintf("gmf-pg-%d-", time.Now().UnixNano())
	store := &RDBLogStore{db: db, logger: testLogger{}}
	assertGuardrailMatchFilter(t, store, prefix, time.Date(2011, 7, 8, 9, 10, 0, 0, time.UTC))
}

// TestGuardrailMatchFilterClickHouse verifies the guardrail match filters on ClickHouse.
func TestGuardrailMatchFilterClickHouse(t *testing.T) {
	store := trySetupClickHouseStore(t)
	if store == nil {
		t.Skip("ClickHouse not available")
	}
	prefix := fmt.Sprintf("gmf-ch-%d-", time.Now().UnixNano())
	assertGuardrailMatchFilter(t, store, prefix, time.Date(2012, 7, 8, 9, 10, 0, 0, time.UTC))
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
