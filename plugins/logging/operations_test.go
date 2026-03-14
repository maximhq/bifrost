package logging

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
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

func newTestStore(t *testing.T) logstore.LogStore {
	t.Helper()

	store, err := logstore.NewLogStore(context.Background(), &logstore.Config{
		Enabled: true,
		Type:    logstore.LogStoreTypeSQLite,
		Config: &logstore.SQLiteConfig{
			Path: filepath.Join(t.TempDir(), "logging.db"),
		},
	}, testLogger{})
	if err != nil {
		t.Fatalf("NewLogStore() error = %v", err)
	}
	return store
}

func TestInjectTraceAndSearchTraces(t *testing.T) {
	store := newTestStore(t)
	plugin := &LoggerPlugin{
		ctx:    context.Background(),
		store:  store,
		logger: testLogger{},
		done:   make(chan struct{}),
	}

	now := time.Now().UTC()

	// Create a trace with one LLM span
	trace := &schemas.Trace{
		TraceID:   "trace-1",
		StartTime: now,
		EndTime:   now.Add(100 * time.Millisecond),
		Attributes: map[string]interface{}{
			"trace_name": "test-trace",
		},
		Spans: []*schemas.Span{
			{
				SpanID:    "span-1",
				Kind:      schemas.SpanKindLLMCall,
				Name:      "llm-call",
				StartTime: now,
				EndTime:   now.Add(100 * time.Millisecond),
				Status:    schemas.SpanStatusOk,
				Attributes: map[string]interface{}{
					schemas.AttrProviderName:  "openai",
					schemas.AttrRequestModel:  "gpt-4o-mini",
					schemas.AttrPromptTokens:     10,
					schemas.AttrCompletionTokens: 5,
					schemas.AttrTotalTokens:      15,
				},
			},
		},
	}

	rootSpan, childSpans := plugin.convertTraceToSpanLogs(trace)
	if rootSpan == nil {
		t.Fatal("expected root span, got nil")
	}

	// Persist directly
	if err := store.CreateRootSpanWithChildren(context.Background(), rootSpan, childSpans); err != nil {
		t.Fatalf("CreateRootSpanWithChildren() error = %v", err)
	}

	// Search traces
	result, err := plugin.SearchTraces(context.Background(), logstore.SearchFilters{}, logstore.PaginationOptions{
		Limit:  50,
		SortBy: "timestamp",
		Order:  "desc",
	})
	if err != nil {
		t.Fatalf("SearchTraces() error = %v", err)
	}

	if len(result.Traces) != 1 {
		t.Fatalf("expected 1 trace, got %d", len(result.Traces))
	}

	if result.Traces[0].ID != "trace-1" {
		t.Fatalf("expected trace ID 'trace-1', got %q", result.Traces[0].ID)
	}

	if result.Traces[0].TotalTokens != 15 {
		t.Fatalf("expected total tokens 15, got %d", result.Traces[0].TotalTokens)
	}
}

func TestGetTraceReturnsRootAndChildren(t *testing.T) {
	store := newTestStore(t)
	plugin := &LoggerPlugin{
		ctx:    context.Background(),
		store:  store,
		logger: testLogger{},
		done:   make(chan struct{}),
	}

	now := time.Now().UTC()

	trace := &schemas.Trace{
		TraceID:   "trace-2",
		StartTime: now,
		EndTime:   now.Add(200 * time.Millisecond),
		Spans: []*schemas.Span{
			{
				SpanID:    "span-a",
				Kind:      schemas.SpanKindLLMCall,
				Name:      "first-call",
				StartTime: now,
				EndTime:   now.Add(100 * time.Millisecond),
				Status:    schemas.SpanStatusOk,
				Attributes: map[string]interface{}{
					schemas.AttrProviderName: "anthropic",
					schemas.AttrRequestModel: "claude-3",
				},
			},
			{
				SpanID:    "span-b",
				Kind:      schemas.SpanKindLLMCall,
				Name:      "second-call",
				StartTime: now.Add(100 * time.Millisecond),
				EndTime:   now.Add(200 * time.Millisecond),
				Status:    schemas.SpanStatusOk,
				Attributes: map[string]interface{}{
					schemas.AttrProviderName: "openai",
					schemas.AttrRequestModel: "gpt-4",
				},
			},
		},
	}

	rootSpan, childSpans := plugin.convertTraceToSpanLogs(trace)
	if err := store.CreateRootSpanWithChildren(context.Background(), rootSpan, childSpans); err != nil {
		t.Fatalf("CreateRootSpanWithChildren() error = %v", err)
	}

	root, children, err := plugin.GetTrace(context.Background(), "trace-2")
	if err != nil {
		t.Fatalf("GetTrace() error = %v", err)
	}

	if root == nil {
		t.Fatal("expected root span, got nil")
	}

	if len(children) != 2 {
		t.Fatalf("expected 2 child spans, got %d", len(children))
	}
}

func TestDeleteTraces(t *testing.T) {
	store := newTestStore(t)
	plugin := &LoggerPlugin{
		ctx:    context.Background(),
		store:  store,
		logger: testLogger{},
		done:   make(chan struct{}),
	}

	now := time.Now().UTC()

	trace := &schemas.Trace{
		TraceID:   "trace-del",
		StartTime: now,
		EndTime:   now.Add(100 * time.Millisecond),
		Spans: []*schemas.Span{
			{
				SpanID:    "span-del-1",
				Kind:      schemas.SpanKindLLMCall,
				Name:      "call",
				StartTime: now,
				EndTime:   now.Add(100 * time.Millisecond),
				Status:    schemas.SpanStatusOk,
				Attributes: map[string]interface{}{
					schemas.AttrProviderName: "openai",
					schemas.AttrRequestModel: "gpt-4",
				},
			},
		},
	}

	rootSpan, childSpans := plugin.convertTraceToSpanLogs(trace)
	if err := store.CreateRootSpanWithChildren(context.Background(), rootSpan, childSpans); err != nil {
		t.Fatalf("CreateRootSpanWithChildren() error = %v", err)
	}

	if err := plugin.DeleteTraces(context.Background(), []string{"trace-del"}); err != nil {
		t.Fatalf("DeleteTraces() error = %v", err)
	}

	root, _, err := plugin.GetTrace(context.Background(), "trace-del")
	if err == nil && root != nil {
		t.Fatal("expected trace to be deleted")
	}
}
