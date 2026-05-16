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
		Status: "success",
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
