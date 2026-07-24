package logstore

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

type testLogger struct{}

func (testLogger) Debug(string, ...any)                   {}
func (testLogger) Info(string, ...any)                    {}
func (testLogger) Warn(string, ...any)                    {}
func (testLogger) Error(string, ...any)                   {}
func (testLogger) Fatal(string, ...any)                   {}
func (testLogger) SetLevel(schemas.LogLevel)              {}
func (testLogger) SetOutputType(schemas.LoggerOutputType) {}
func (testLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func newTestSQLiteStore(t *testing.T) *RDBLogStore {
	t.Helper()

	store, err := newSqliteLogStore(context.Background(), &SQLiteConfig{
		Path: filepath.Join(t.TempDir(), "logs.db"),
	}, testLogger{})
	if err != nil {
		t.Fatalf("newSqliteLogStore() error = %v", err)
	}
	return store
}

func TestCancelledStatusIncludedInLogAggregates(t *testing.T) {
	store := newTestSQLiteStore(t)
	ctx := context.Background()
	base := time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)
	cost := 0.01
	successLatency := 100.0
	errorLatency := 200.0
	cancelledLatency := 300.0
	processingLatency := 400.0

	entries := []*Log{
		{
			ID:          "aggregate-success",
			Timestamp:   base,
			Object:      "chat.completion",
			Provider:    "openai",
			Model:       "gpt-4o-mini",
			Status:      "success",
			Latency:     &successLatency,
			TotalTokens: 10,
			Cost:        &cost,
		},
		{
			ID:          "aggregate-error",
			Timestamp:   base.Add(time.Minute),
			Object:      "chat.completion",
			Provider:    "openai",
			Model:       "gpt-4o-mini",
			Status:      "error",
			Latency:     &errorLatency,
			TotalTokens: 20,
			Cost:        &cost,
		},
		{
			ID:          "aggregate-cancelled",
			Timestamp:   base.Add(2 * time.Minute),
			Object:      "chat.completion",
			Provider:    "openai",
			Model:       "gpt-4o-mini",
			Status:      "cancelled",
			Latency:     &cancelledLatency,
			TotalTokens: 30,
			Cost:        &cost,
		},
		{
			ID:        "aggregate-processing",
			Timestamp: base.Add(3 * time.Minute),
			Object:    "chat.completion",
			Provider:  "openai",
			Model:     "gpt-4o-mini",
			Status:    "processing",
			Latency:   &processingLatency,
		},
	}

	for _, entry := range entries {
		if err := store.Create(ctx, entry); err != nil {
			t.Fatalf("Create(%s) error = %v", entry.ID, err)
		}
	}

	search, err := store.SearchLogs(ctx, SearchFilters{Status: []string{"cancelled"}}, PaginationOptions{Limit: 10})
	if err != nil {
		t.Fatalf("SearchLogs(cancelled) error = %v", err)
	}
	if search.Stats.TotalRequests != 1 {
		t.Fatalf("expected cancelled search total to be 1, got %d", search.Stats.TotalRequests)
	}

	allStats, err := store.GetStats(ctx, SearchFilters{})
	if err != nil {
		t.Fatalf("GetStats(all) error = %v", err)
	}
	if allStats.TotalRequests != 4 {
		t.Fatalf("expected total requests to include processing rows, got %d", allStats.TotalRequests)
	}
	if allStats.CacheHitRateTotalRequests == nil || *allStats.CacheHitRateTotalRequests != 3 {
		t.Fatalf("expected terminal request denominator to include cancelled rows, got %v", allStats.CacheHitRateTotalRequests)
	}

	cancelledStats, err := store.GetStats(ctx, SearchFilters{Status: []string{"cancelled"}})
	if err != nil {
		t.Fatalf("GetStats(cancelled) error = %v", err)
	}
	if cancelledStats.TotalRequests != 1 {
		t.Fatalf("expected cancelled stats total to be 1, got %d", cancelledStats.TotalRequests)
	}
	if cancelledStats.SuccessRate != 0 {
		t.Fatalf("expected cancelled success rate to be 0, got %f", cancelledStats.SuccessRate)
	}
	if cancelledStats.AverageLatency != 300 {
		t.Fatalf("expected cancelled average latency 300, got %f", cancelledStats.AverageLatency)
	}

	start := base.Add(-time.Minute)
	end := base.Add(5 * time.Minute)
	hist, err := store.GetHistogram(ctx, SearchFilters{
		Status:    []string{"cancelled"},
		StartTime: &start,
		EndTime:   &end,
	}, 3600)
	if err != nil {
		t.Fatalf("GetHistogram(cancelled) error = %v", err)
	}
	var cancelledBucket *HistogramBucket
	for i := range hist.Buckets {
		if hist.Buckets[i].Count > 0 {
			cancelledBucket = &hist.Buckets[i]
			break
		}
	}
	if cancelledBucket == nil {
		t.Fatal("expected a non-empty cancelled histogram bucket")
	}
	if cancelledBucket.Count != 1 || cancelledBucket.Success != 0 || cancelledBucket.Error != 0 || cancelledBucket.Cancelled != 1 {
		t.Fatalf("unexpected cancelled histogram bucket: %+v", *cancelledBucket)
	}
}

func TestLogCreateSerializesFields(t *testing.T) {
	store := newTestSQLiteStore(t)
	prompt := "hello"
	reply := "world"

	entry := &Log{
		ID:        "log-1",
		Timestamp: time.Now().UTC(),
		Object:    "chat_completion",
		Provider:  "openai",
		Model:     "gpt-4o-mini",
		Status:    "success",
		InputHistoryParsed: []schemas.ChatMessage{{
			Role: schemas.ChatMessageRoleUser,
			Content: &schemas.ChatMessageContent{
				ContentStr: &prompt,
			},
		}},
		OutputMessageParsed: &schemas.ChatMessage{
			Role: schemas.ChatMessageRoleAssistant,
			Content: &schemas.ChatMessageContent{
				ContentStr: &reply,
			},
		},
	}

	if err := store.Create(context.Background(), entry); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	logEntry, err := store.FindByID(context.Background(), entry.ID)
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if logEntry.InputHistory == "" {
		t.Fatalf("expected InputHistory to be serialized")
	}
	if logEntry.OutputMessage == "" {
		t.Fatalf("expected OutputMessage to be serialized")
	}
	if logEntry.ContentSummary == "" {
		t.Fatalf("expected ContentSummary to be populated")
	}
	if logEntry.CreatedAt.IsZero() {
		t.Fatalf("expected CreatedAt to be populated")
	}
	if logEntry.IncNumber != nil {
		t.Fatalf("expected SQLite Create to leave inc_number nil, got %v", logEntry.IncNumber)
	}
}

func TestSQLiteCreateLeavesIncNumberNull(t *testing.T) {
	store := newTestSQLiteStore(t)
	ctx := context.Background()

	entries := []*Log{
		{
			ID:        "inc-null-1",
			Timestamp: time.Now().UTC(),
			Object:    "chat_completion",
			Provider:  "openai",
			Model:     "gpt-4o-mini",
			Status:    "success",
		},
		{
			ID:        "inc-null-2",
			Timestamp: time.Now().UTC(),
			Object:    "chat_completion",
			Provider:  "openai",
			Model:     "gpt-4o-mini",
			Status:    "success",
		},
	}
	if err := store.BatchCreateIfNotExists(ctx, entries); err != nil {
		t.Fatalf("BatchCreateIfNotExists() error = %v", err)
	}

	first, err := store.FindByID(ctx, "inc-null-1")
	if err != nil {
		t.Fatalf("FindByID(inc-null-1) error = %v", err)
	}
	second, err := store.FindByID(ctx, "inc-null-2")
	if err != nil {
		t.Fatalf("FindByID(inc-null-2) error = %v", err)
	}
	if first.IncNumber != nil || second.IncNumber != nil {
		t.Fatalf("expected SQLite batch insert to leave inc_numbers nil, got first=%v second=%v", first.IncNumber, second.IncNumber)
	}
}

func TestGetNodeUsageSinceTracksMaxTimestampAndExclusiveCursor(t *testing.T) {
	store := newTestSQLiteStore(t)
	ctx := context.Background()
	nodeID := "node-ghost"
	otherNodeID := "node-other"
	budgetIDs := []string{"budget-1"}
	rateLimitIDs := []string{"rl-1"}
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	cost1 := 1.25
	cost2 := 2.50
	otherCost := 99.0
	inc1 := int64(1)
	inc2 := int64(2)
	inc3 := int64(3)

	entries := []*Log{
		{
			ID:                 "usage-1",
			IncNumber:          &inc1,
			Timestamp:          base.Add(time.Second),
			Object:             "chat.completion",
			Provider:           "openai",
			Model:              "gpt-4o-mini",
			Status:             "success",
			ClusterNodeID:      &nodeID,
			BudgetIDsParsed:    budgetIDs,
			RateLimitIDsParsed: rateLimitIDs,
			Cost:               &cost1,
			TotalTokens:        10,
		},
		{
			ID:                 "usage-2",
			IncNumber:          &inc2,
			Timestamp:          base.Add(2 * time.Second),
			Object:             "chat.completion",
			Provider:           "openai",
			Model:              "gpt-4o-mini",
			Status:             "success",
			ClusterNodeID:      &nodeID,
			BudgetIDsParsed:    budgetIDs,
			RateLimitIDsParsed: rateLimitIDs,
			Cost:               &cost2,
			TotalTokens:        20,
		},
		{
			ID:                 "usage-other-node",
			Timestamp:          base.Add(3 * time.Second),
			Object:             "chat.completion",
			Provider:           "openai",
			Model:              "gpt-4o-mini",
			Status:             "success",
			ClusterNodeID:      &otherNodeID,
			BudgetIDsParsed:    budgetIDs,
			RateLimitIDsParsed: rateLimitIDs,
			Cost:               &otherCost,
			TotalTokens:        100,
		},
	}
	for _, entry := range entries {
		if err := store.Create(ctx, entry); err != nil {
			t.Fatalf("Create(%s) error = %v", entry.ID, err)
		}
	}

	usage, err := store.GetNodeUsageAfter(ctx, nodeID, NodeUsageCursor{Timestamp: base})
	if err != nil {
		t.Fatalf("GetNodeUsageSince() error = %v", err)
	}
	if usage.RowCount != 2 {
		t.Fatalf("expected 2 rows, got %d", usage.RowCount)
	}
	if got := usage.BudgetCosts["budget-1"]; got != cost1+cost2 {
		t.Fatalf("expected budget cost %.2f, got %.2f", cost1+cost2, got)
	}
	if got := usage.RateLimitRequests["rl-1"]; got != 2 {
		t.Fatalf("expected 2 rate-limit requests, got %d", got)
	}
	if got := usage.RateLimitTokens["rl-1"]; got != 30 {
		t.Fatalf("expected 30 rate-limit tokens, got %d", got)
	}
	if !usage.MaxTimestamp.Equal(base.Add(2 * time.Second)) {
		t.Fatalf("expected max timestamp %s, got %s", base.Add(2*time.Second), usage.MaxTimestamp)
	}
	if usage.MaxLogID != "usage-2" {
		t.Fatalf("expected max log ID usage-2, got %s", usage.MaxLogID)
	}
	if usage.NextCursor.Timestamp != usage.MaxTimestamp || usage.NextCursor.LogID != usage.MaxLogID || usage.NextCursor.IncNumber == nil || *usage.NextCursor.IncNumber != inc2 {
		t.Fatalf("expected next cursor to match max row and inc_number, got %+v", usage.NextCursor)
	}

	lateCost := 3.75
	lateEntry := &Log{
		ID:                 "usage-late",
		IncNumber:          &inc3,
		Timestamp:          base.Add(time.Second),
		Object:             "chat.completion",
		Provider:           "openai",
		Model:              "gpt-4o-mini",
		Status:             "success",
		ClusterNodeID:      &nodeID,
		BudgetIDsParsed:    budgetIDs,
		RateLimitIDsParsed: rateLimitIDs,
		Cost:               &lateCost,
		TotalTokens:        30,
	}
	if err := store.Create(ctx, lateEntry); err != nil {
		t.Fatalf("Create(%s) error = %v", lateEntry.ID, err)
	}

	usage, err = store.GetNodeUsageAfter(ctx, nodeID, usage.NextCursor)
	if err != nil {
		t.Fatalf("GetNodeUsageAfter(after inc cursor) error = %v", err)
	}
	if usage.RowCount != 1 {
		t.Fatalf("expected late row with higher inc_number, got rows=%d", usage.RowCount)
	}
	if got := usage.BudgetCosts["budget-1"]; got != lateCost {
		t.Fatalf("expected late budget cost %.2f, got %.2f", lateCost, got)
	}
	if usage.NextCursor.IncNumber == nil || *usage.NextCursor.IncNumber != inc3 {
		t.Fatalf("expected next cursor inc_number %d, got %+v", inc3, usage.NextCursor)
	}

	usage, err = store.GetNodeUsageAfter(ctx, nodeID, usage.NextCursor)
	if err != nil {
		t.Fatalf("GetNodeUsageAfter(after max) error = %v", err)
	}
	if usage.RowCount != 0 {
		t.Fatalf("expected no rows after exclusive cursor, got rows=%d", usage.RowCount)
	}
	// When no rows are returned, NextCursor should preserve the incoming cursor (not rewind).
	if !usage.NextCursor.Timestamp.Equal(base.Add(2*time.Second)) || usage.NextCursor.LogID != "usage-2" || usage.NextCursor.IncNumber == nil || *usage.NextCursor.IncNumber != inc3 {
		t.Fatalf("expected cursor to be preserved when no rows returned, got %+v", usage.NextCursor)
	}
}

func TestGetNodeUsageAfterIncludesSameTimestampGreaterLogIDs(t *testing.T) {
	store := newTestSQLiteStore(t)
	ctx := context.Background()
	nodeID := "node-ghost"
	budgetIDs := []string{"budget-1"}
	rateLimitIDs := []string{"rl-1"}
	timestamp := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	cost1 := 1.0
	cost2 := 2.0
	cost3 := 4.0

	entries := []*Log{
		{
			ID:                 "usage-1",
			Timestamp:          timestamp,
			Object:             "chat.completion",
			Provider:           "openai",
			Model:              "gpt-4o-mini",
			Status:             "success",
			ClusterNodeID:      &nodeID,
			BudgetIDsParsed:    budgetIDs,
			RateLimitIDsParsed: rateLimitIDs,
			Cost:               &cost1,
			TotalTokens:        10,
		},
		{
			ID:                 "usage-2",
			Timestamp:          timestamp,
			Object:             "chat.completion",
			Provider:           "openai",
			Model:              "gpt-4o-mini",
			Status:             "success",
			ClusterNodeID:      &nodeID,
			BudgetIDsParsed:    budgetIDs,
			RateLimitIDsParsed: rateLimitIDs,
			Cost:               &cost2,
			TotalTokens:        20,
		},
		{
			ID:                 "usage-3",
			Timestamp:          timestamp,
			Object:             "chat.completion",
			Provider:           "openai",
			Model:              "gpt-4o-mini",
			Status:             "success",
			ClusterNodeID:      &nodeID,
			BudgetIDsParsed:    budgetIDs,
			RateLimitIDsParsed: rateLimitIDs,
			Cost:               &cost3,
			TotalTokens:        40,
		},
	}
	for _, entry := range entries {
		if err := store.Create(ctx, entry); err != nil {
			t.Fatalf("Create(%s) error = %v", entry.ID, err)
		}
	}

	// Cursor after usage-1: should get usage-2 and usage-3 (same timestamp, greater IDs)
	usage, err := store.GetNodeUsageAfter(ctx, nodeID, NodeUsageCursor{Timestamp: timestamp, LogID: "usage-1"})
	if err != nil {
		t.Fatalf("GetNodeUsageAfter() error = %v", err)
	}
	if usage.RowCount != 2 {
		t.Fatalf("expected 2 same-timestamp rows after usage-1, got %d", usage.RowCount)
	}
	if got := usage.BudgetCosts["budget-1"]; got != cost2+cost3 {
		t.Fatalf("expected budget cost %.2f, got %.2f", cost2+cost3, got)
	}
	if got := usage.RateLimitRequests["rl-1"]; got != 2 {
		t.Fatalf("expected 2 rate-limit requests, got %d", got)
	}
	if got := usage.RateLimitTokens["rl-1"]; got != 60 {
		t.Fatalf("expected 60 rate-limit tokens, got %d", got)
	}
	if !usage.MaxTimestamp.Equal(timestamp) || usage.MaxLogID != "usage-3" {
		t.Fatalf("expected cursor %s/usage-3, got %s/%s", timestamp, usage.MaxTimestamp, usage.MaxLogID)
	}
}

func TestMCPToolLogCreateSerializesFields(t *testing.T) {
	store := newTestSQLiteStore(t)

	entry := &MCPToolLog{
		ID:        "mcp-1",
		Timestamp: time.Now().UTC(),
		ToolName:  "echo",
		Status:    "success",
		ArgumentsParsed: map[string]any{
			"message": "hello",
		},
		ResultParsed: map[string]any{
			"ok": true,
		},
		RedactionMapping: `plain:{"input":{"EMAIL-1":"private@example.com"}}`,
	}

	if err := store.CreateMCPToolLog(context.Background(), entry); err != nil {
		t.Fatalf("CreateMCPToolLog() error = %v", err)
	}

	logEntry, err := store.FindMCPToolLog(context.Background(), entry.ID)
	if err != nil {
		t.Fatalf("FindMCPToolLog() error = %v", err)
	}
	if logEntry.Arguments == "" {
		t.Fatalf("expected Arguments to be serialized")
	}
	if logEntry.Result == "" {
		t.Fatalf("expected Result to be serialized")
	}
	if logEntry.RedactionMapping != entry.RedactionMapping {
		t.Fatalf("RedactionMapping = %q, want %q", logEntry.RedactionMapping, entry.RedactionMapping)
	}
}

func TestBuildBulkUpdateCostPostgresSQL(t *testing.T) {
	updates := map[string]float64{
		"log-a": 1.25,
		"log-b": 2.5,
	}

	query, args := buildBulkUpdateCostPostgresSQL([]string{"log-a", "log-b"}, updates)
	wantQuery := "UPDATE logs SET cost = v.cost FROM (VALUES ($1::text,$2::float8),($3::text,$4::float8)) AS v(id, cost) WHERE logs.id = v.id"
	wantArgs := []interface{}{"log-a", 1.25, "log-b", 2.5}

	if query != wantQuery {
		t.Fatalf("query mismatch\n got: %s\nwant: %s", query, wantQuery)
	}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args mismatch\n got: %#v\nwant: %#v", args, wantArgs)
	}
}

func TestUpdateSerializesStructEntry(t *testing.T) {
	store := newTestSQLiteStore(t)
	now := time.Now().UTC()
	entry := &Log{
		ID:        "log-update",
		Timestamp: now,
		Object:    "chat_completion",
		Provider:  "openai",
		Model:     "gpt-4o-mini",
		Status:    "processing",
	}

	if err := store.Create(context.Background(), entry); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	reply := "updated response"
	if err := store.Update(context.Background(), entry.ID, Log{
		Status: "success",
		OutputMessageParsed: &schemas.ChatMessage{
			Role: schemas.ChatMessageRoleAssistant,
			Content: &schemas.ChatMessageContent{
				ContentStr: &reply,
			},
		},
		TokenUsageParsed: &schemas.BifrostLLMUsage{
			PromptTokens:     3,
			CompletionTokens: 7,
			TotalTokens:      10,
		},
	}); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	logEntry, err := store.FindByID(context.Background(), entry.ID)
	if err != nil {
		t.Fatalf("FindByID() error = %v", err)
	}
	if logEntry.OutputMessage == "" {
		t.Fatalf("expected OutputMessage to be serialized on Update")
	}
	if logEntry.TokenUsage == "" {
		t.Fatalf("expected TokenUsage to be serialized on Update")
	}
	if logEntry.TotalTokens != 10 {
		t.Fatalf("expected TotalTokens to be updated, got %d", logEntry.TotalTokens)
	}
}

func TestUpdateMCPToolLogSerializesStructEntry(t *testing.T) {
	store := newTestSQLiteStore(t)
	now := time.Now().UTC()
	entry := &MCPToolLog{
		ID:        "mcp-update",
		Timestamp: now,
		ToolName:  "echo",
		Status:    "processing",
	}

	if err := store.CreateMCPToolLog(context.Background(), entry); err != nil {
		t.Fatalf("CreateMCPToolLog() error = %v", err)
	}

	if err := store.UpdateMCPToolLog(context.Background(), entry.ID, MCPToolLog{
		Status:           "success",
		RedactionMapping: `plain:{"output":{"EMAIL-2":"result@example.com"}}`,
		ResultParsed: map[string]any{
			"message": "done",
		},
	}); err != nil {
		t.Fatalf("UpdateMCPToolLog() error = %v", err)
	}

	logEntry, err := store.FindMCPToolLog(context.Background(), entry.ID)
	if err != nil {
		t.Fatalf("FindMCPToolLog() error = %v", err)
	}
	if logEntry.Result == "" {
		t.Fatalf("expected Result to be serialized on UpdateMCPToolLog")
	}
	if logEntry.RedactionMapping == "" {
		t.Fatal("expected RedactionMapping to be updated")
	}
}

func TestBulkUpdateCostSQLiteFallback(t *testing.T) {
	store := newTestSQLiteStore(t)
	now := time.Now().UTC()
	entries := []*Log{
		{
			ID:        "log-a",
			Timestamp: now,
			Object:    "chat_completion",
			Provider:  "openai",
			Model:     "gpt-4o-mini",
			Status:    "success",
		},
		{
			ID:        "log-b",
			Timestamp: now,
			Object:    "chat_completion",
			Provider:  "openai",
			Model:     "gpt-4o-mini",
			Status:    "success",
		},
	}
	for _, entry := range entries {
		if err := store.Create(context.Background(), entry); err != nil {
			t.Fatalf("Create(%s) error = %v", entry.ID, err)
		}
	}

	if err := store.BulkUpdateCost(context.Background(), map[string]float64{
		"log-a": 1.5,
		"log-b": 2.5,
	}); err != nil {
		t.Fatalf("BulkUpdateCost() error = %v", err)
	}

	for id, wantCost := range map[string]float64{"log-a": 1.5, "log-b": 2.5} {
		logEntry, err := store.FindByID(context.Background(), id)
		if err != nil {
			t.Fatalf("FindByID(%s) error = %v", id, err)
		}
		if logEntry.Cost == nil || *logEntry.Cost != wantCost {
			t.Fatalf("cost mismatch for %s: got %v want %v", id, logEntry.Cost, wantCost)
		}
	}
}
