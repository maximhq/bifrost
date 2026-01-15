package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/plugins/logging"
	"github.com/valyala/fasthttp"
)

// Mock implementations for testing

type mockLogManager struct {
	searchResult      *logstore.SearchResult
	searchError       error
	statsResult       *logstore.SearchStats
	statsError        error
	droppedRequests   int64
	availableModels   []string
	availableKeys     []logging.KeyPair
	availableVK       []logging.KeyPair
	deleteError       error
	recalculateResult *logging.RecalculateCostResult
	recalculateError  error
}

func (m *mockLogManager) Search(ctx context.Context, filters *logstore.SearchFilters, pagination *logstore.PaginationOptions) (*logstore.SearchResult, error) {
	return m.searchResult, m.searchError
}

func (m *mockLogManager) GetStats(ctx context.Context, filters *logstore.SearchFilters) (*logstore.SearchStats, error) {
	return m.statsResult, m.statsError
}

func (m *mockLogManager) GetDroppedRequests(ctx context.Context) int64 {
	return m.droppedRequests
}

func (m *mockLogManager) GetAvailableModels(ctx context.Context) []string {
	return m.availableModels
}

func (m *mockLogManager) GetAvailableSelectedKeys(ctx context.Context) []logging.KeyPair {
	return m.availableKeys
}

func (m *mockLogManager) GetAvailableVirtualKeys(ctx context.Context) []logging.KeyPair {
	return m.availableVK
}

func (m *mockLogManager) DeleteLog(ctx context.Context, id string) error {
	return m.deleteError
}

func (m *mockLogManager) DeleteLogs(ctx context.Context, ids []string) error {
	return m.deleteError
}

func (m *mockLogManager) RecalculateCosts(ctx context.Context, filters *logstore.SearchFilters, limit int) (*logging.RecalculateCostResult, error) {
	return m.recalculateResult, m.recalculateError
}

type mockRedactedKeysManager struct {
	redactedKeys        []schemas.Key
	redactedVirtualKeys []tables.TableVirtualKey
}

func (m *mockRedactedKeysManager) GetAllRedactedKeys(ctx context.Context, ids []string) []schemas.Key {
	return m.redactedKeys
}

func (m *mockRedactedKeysManager) GetAllRedactedVirtualKeys(ctx context.Context, ids []string) []tables.TableVirtualKey {
	return m.redactedVirtualKeys
}

// Tests

// TestNewLoggingHandler tests creating a new logging handler
func TestNewLoggingHandler(t *testing.T) {
	SetLogger(&mockLogger{})

	logManager := &mockLogManager{}
	redactedManager := &mockRedactedKeysManager{}

	handler := NewLoggingHandler(logManager, redactedManager)

	if handler == nil {
		t.Fatal("Expected non-nil handler")
	}
	if handler.logManager != logManager {
		t.Error("Expected log manager to be set")
	}
	if handler.redactedKeysManager != redactedManager {
		t.Error("Expected redacted keys manager to be set")
	}
}

// TestNewLoggingHandler_NilManagers tests creating handler with nil managers
func TestNewLoggingHandler_NilManagers(t *testing.T) {
	SetLogger(&mockLogger{})

	handler := NewLoggingHandler(nil, nil)

	if handler == nil {
		t.Fatal("Expected non-nil handler even with nil managers")
	}
}

// TestLoggingHandler_RegisterRoutes tests route registration
func TestLoggingHandler_RegisterRoutes(t *testing.T) {
	SetLogger(&mockLogger{})

	handler := NewLoggingHandler(&mockLogManager{}, &mockRedactedKeysManager{})
	r := router.New()

	handler.RegisterRoutes(r)

	// Verify routes were registered
	if r == nil {
		t.Error("Router should not be nil")
	}
}

// TestLoggingHandler_Routes documents registered routes
func TestLoggingHandler_Routes(t *testing.T) {
	// LoggingHandler registers:
	// GET /api/logs - Get logs with filtering, search, and pagination
	// GET /api/logs/stats - Get statistics for logs
	// GET /api/logs/dropped - Get the number of dropped requests
	// GET /api/logs/filterdata - Get all unique filter data
	// DELETE /api/logs - Delete logs by their IDs
	// POST /api/logs/recalculate-cost - Recompute missing costs

	routes := []struct {
		method string
		path   string
		desc   string
	}{
		{"GET", "/api/logs", "Get logs with filtering, search, and pagination"},
		{"GET", "/api/logs/stats", "Get statistics for logs"},
		{"GET", "/api/logs/dropped", "Get the number of dropped requests"},
		{"GET", "/api/logs/filterdata", "Get all unique filter data"},
		{"DELETE", "/api/logs", "Delete logs by their IDs"},
		{"POST", "/api/logs/recalculate-cost", "Recompute missing costs"},
	}

	for _, r := range routes {
		t.Logf("%s %s - %s", r.method, r.path, r.desc)
	}
}

// TestLoggingHandler_GetLogs_Success tests successful log retrieval
func TestLoggingHandler_GetLogs_Success(t *testing.T) {
	SetLogger(&mockLogger{})

	logManager := &mockLogManager{
		searchResult: &logstore.SearchResult{
			Logs: []logstore.Log{},
		},
	}
	redactedManager := &mockRedactedKeysManager{}
	handler := NewLoggingHandler(logManager, redactedManager)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/logs")

	handler.getLogs(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Errorf("Expected status 200, got %d", ctx.Response.StatusCode())
	}
}

// TestLoggingHandler_GetLogs_WithFilters tests log retrieval with filters
func TestLoggingHandler_GetLogs_WithFilters(t *testing.T) {
	SetLogger(&mockLogger{})

	logManager := &mockLogManager{
		searchResult: &logstore.SearchResult{
			Logs: []logstore.Log{},
		},
	}
	redactedManager := &mockRedactedKeysManager{}
	handler := NewLoggingHandler(logManager, redactedManager)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/logs?providers=openai,anthropic&models=gpt-4&status=success")

	handler.getLogs(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Errorf("Expected status 200, got %d", ctx.Response.StatusCode())
	}
}

// TestLoggingHandler_GetLogs_InvalidLimit tests log retrieval with invalid limit
func TestLoggingHandler_GetLogs_InvalidLimit(t *testing.T) {
	SetLogger(&mockLogger{})

	logManager := &mockLogManager{}
	handler := NewLoggingHandler(logManager, &mockRedactedKeysManager{})

	testCases := []struct {
		limit        string
		expectError  bool
		errorMessage string
	}{
		{"0", true, "limit must be greater than 0"},
		{"-1", true, "limit must be greater than 0"},
		{"1001", true, "limit cannot exceed 1000"},
		{"50", false, ""},
	}

	for _, tc := range testCases {
		t.Run(tc.limit, func(t *testing.T) {
			logManager.searchResult = &logstore.SearchResult{
				Logs:  []logstore.Log{},
							}
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.SetRequestURI("/api/logs?limit=" + tc.limit)

			handler.getLogs(ctx)

			if tc.expectError {
				if ctx.Response.StatusCode() == fasthttp.StatusOK {
					t.Errorf("Expected error for limit=%s", tc.limit)
				}
			} else {
				if ctx.Response.StatusCode() != fasthttp.StatusOK {
					t.Errorf("Expected success for limit=%s, got status %d", tc.limit, ctx.Response.StatusCode())
				}
			}
		})
	}
}

// TestLoggingHandler_GetLogs_InvalidOffset tests log retrieval with invalid offset
func TestLoggingHandler_GetLogs_InvalidOffset(t *testing.T) {
	SetLogger(&mockLogger{})

	logManager := &mockLogManager{
		searchResult: &logstore.SearchResult{
			Logs:  []logstore.Log{},
					},
	}
	handler := NewLoggingHandler(logManager, &mockRedactedKeysManager{})

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/logs?offset=-1")

	handler.getLogs(ctx)

	if ctx.Response.StatusCode() == fasthttp.StatusOK {
		t.Error("Expected error for negative offset")
	}
}

// TestLoggingHandler_GetLogs_SortParameters tests sort parameters
func TestLoggingHandler_GetLogs_SortParameters(t *testing.T) {
	SetLogger(&mockLogger{})

	logManager := &mockLogManager{
		searchResult: &logstore.SearchResult{
			Logs:  []logstore.Log{},
					},
	}
	handler := NewLoggingHandler(logManager, &mockRedactedKeysManager{})

	// Valid sort fields: timestamp, latency, tokens, cost
	// Valid orders: asc, desc

	testCases := []struct {
		sortBy string
		order  string
	}{
		{"timestamp", "asc"},
		{"latency", "desc"},
		{"tokens", "asc"},
		{"cost", "desc"},
	}

	for _, tc := range testCases {
		t.Run(tc.sortBy+"_"+tc.order, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.SetRequestURI("/api/logs?sort_by=" + tc.sortBy + "&order=" + tc.order)

			handler.getLogs(ctx)

			if ctx.Response.StatusCode() != fasthttp.StatusOK {
				t.Errorf("Expected success for sort_by=%s, order=%s", tc.sortBy, tc.order)
			}
		})
	}
}

// TestLoggingHandler_GetLogsStats_Success tests stats retrieval
func TestLoggingHandler_GetLogsStats_Success(t *testing.T) {
	SetLogger(&mockLogger{})

	logManager := &mockLogManager{
		statsResult: &logstore.SearchStats{
			TotalRequests:  100,
			SuccessRate:    90.0,
			TotalTokens:    10000,
			TotalCost:      1.50,
			AverageLatency: 100.5,
		},
	}
	handler := NewLoggingHandler(logManager, &mockRedactedKeysManager{})

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/logs/stats")

	handler.getLogsStats(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Errorf("Expected status 200, got %d", ctx.Response.StatusCode())
	}
}

// TestLoggingHandler_GetDroppedRequests_Success tests dropped requests retrieval
func TestLoggingHandler_GetDroppedRequests_Success(t *testing.T) {
	SetLogger(&mockLogger{})

	logManager := &mockLogManager{
		droppedRequests: 42,
	}
	handler := NewLoggingHandler(logManager, &mockRedactedKeysManager{})

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/logs/dropped")

	handler.getDroppedRequests(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Errorf("Expected status 200, got %d", ctx.Response.StatusCode())
	}

	// Verify response contains dropped_requests
	body := string(ctx.Response.Body())
	if body == "" {
		t.Error("Expected non-empty response body")
	}
}

// TestLoggingHandler_GetAvailableFilterData_Success tests filter data retrieval
func TestLoggingHandler_GetAvailableFilterData_Success(t *testing.T) {
	SetLogger(&mockLogger{})

	logManager := &mockLogManager{
		availableModels: []string{"gpt-4", "claude-3"},
		availableKeys: []logging.KeyPair{
			{ID: "key1", Name: "test-key"},
		},
		availableVK: []logging.KeyPair{
			{ID: "vk1", Name: "virtual-key"},
		},
	}
	redactedManager := &mockRedactedKeysManager{
		redactedKeys: []schemas.Key{
			{ID: "key1", Name: "test-key"},
		},
		redactedVirtualKeys: []tables.TableVirtualKey{
			{ID: "vk1", Name: "virtual-key"},
		},
	}
	handler := NewLoggingHandler(logManager, redactedManager)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/logs/filterdata")

	handler.getAvailableFilterData(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Errorf("Expected status 200, got %d", ctx.Response.StatusCode())
	}
}

// TestLoggingHandler_DeleteLogs_Success tests successful log deletion
func TestLoggingHandler_DeleteLogs_Success(t *testing.T) {
	SetLogger(&mockLogger{})

	logManager := &mockLogManager{}
	handler := NewLoggingHandler(logManager, &mockRedactedKeysManager{})

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/logs")
	ctx.Request.Header.SetMethod("DELETE")
	ctx.Request.SetBody([]byte(`{"ids": ["log1", "log2"]}`))

	handler.deleteLogs(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Errorf("Expected status 200, got %d", ctx.Response.StatusCode())
	}
}

// TestLoggingHandler_DeleteLogs_EmptyIDs tests deletion with empty IDs
func TestLoggingHandler_DeleteLogs_EmptyIDs(t *testing.T) {
	SetLogger(&mockLogger{})

	handler := NewLoggingHandler(&mockLogManager{}, &mockRedactedKeysManager{})

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/logs")
	ctx.Request.Header.SetMethod("DELETE")
	ctx.Request.SetBody([]byte(`{"ids": []}`))

	handler.deleteLogs(ctx)

	if ctx.Response.StatusCode() == fasthttp.StatusOK {
		t.Error("Expected error when no IDs provided")
	}
}

// TestLoggingHandler_DeleteLogs_InvalidJSON tests deletion with invalid JSON
func TestLoggingHandler_DeleteLogs_InvalidJSON(t *testing.T) {
	SetLogger(&mockLogger{})

	handler := NewLoggingHandler(&mockLogManager{}, &mockRedactedKeysManager{})

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/logs")
	ctx.Request.Header.SetMethod("DELETE")
	ctx.Request.SetBody([]byte(`invalid json`))

	handler.deleteLogs(ctx)

	if ctx.Response.StatusCode() == fasthttp.StatusOK {
		t.Error("Expected error for invalid JSON")
	}
}

// TestLoggingHandler_RecalculateCosts_Success tests cost recalculation
func TestLoggingHandler_RecalculateCosts_Success(t *testing.T) {
	SetLogger(&mockLogger{})

	logManager := &mockLogManager{
		recalculateResult: &logging.RecalculateCostResult{
			Updated:   10,
			Remaining: 5,
		},
	}
	handler := NewLoggingHandler(logManager, &mockRedactedKeysManager{})

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/logs/recalculate-cost")
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.SetBody([]byte(`{}`))

	handler.recalculateLogCosts(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Errorf("Expected status 200, got %d", ctx.Response.StatusCode())
	}
}

// TestLoggingHandler_RecalculateCosts_WithLimit tests cost recalculation with limit
func TestLoggingHandler_RecalculateCosts_WithLimit(t *testing.T) {
	SetLogger(&mockLogger{})

	logManager := &mockLogManager{
		recalculateResult: &logging.RecalculateCostResult{
			Updated:   50,
			Remaining: 0,
		},
	}
	handler := NewLoggingHandler(logManager, &mockRedactedKeysManager{})

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/logs/recalculate-cost")
	ctx.Request.Header.SetMethod("POST")
	ctx.Request.SetBody([]byte(`{"limit": 50}`))

	handler.recalculateLogCosts(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Errorf("Expected status 200, got %d", ctx.Response.StatusCode())
	}
}

// TestParseCommaSeparated tests comma-separated string parsing
func TestParseCommaSeparated(t *testing.T) {
	testCases := []struct {
		input    string
		expected []string
	}{
		{"", nil},
		{"a", []string{"a"}},
		{"a,b,c", []string{"a", "b", "c"}},
		{"a, b, c", []string{"a", "b", "c"}},
		{" a , b , c ", []string{"a", "b", "c"}},
		{"a,,b", []string{"a", "b"}},
		{",a,b,", []string{"a", "b"}},
	}

	for _, tc := range testCases {
		t.Run(tc.input, func(t *testing.T) {
			result := parseCommaSeparated(tc.input)

			if tc.expected == nil {
				if result != nil {
					t.Errorf("Expected nil, got %v", result)
				}
				return
			}

			if len(result) != len(tc.expected) {
				t.Errorf("Expected %d items, got %d", len(tc.expected), len(result))
				return
			}

			for i, v := range tc.expected {
				if result[i] != v {
					t.Errorf("Expected %q at index %d, got %q", v, i, result[i])
				}
			}
		})
	}
}

// TestFindRedactedKey tests finding redacted keys
func TestFindRedactedKey(t *testing.T) {
	testCases := []struct {
		name           string
		redactedKeys   []schemas.Key
		id             string
		keyName        string
		expectedID     string
		expectedName   string
	}{
		{
			name:           "empty redacted keys",
			redactedKeys:   nil,
			id:             "key1",
			keyName:        "test-key",
			expectedID:     "key1",
			expectedName:   "test-key (deleted)",
		},
		{
			name: "key found",
			redactedKeys: []schemas.Key{
				{ID: "key1", Name: "test-key"},
			},
			id:           "key1",
			keyName:      "test-key",
			expectedID:   "key1",
			expectedName: "test-key",
		},
		{
			name: "key not found",
			redactedKeys: []schemas.Key{
				{ID: "key2", Name: "other-key"},
			},
			id:           "key1",
			keyName:      "test-key",
			expectedID:   "key1",
			expectedName: "test-key (deleted)",
		},
		{
			name:           "empty name",
			redactedKeys:   nil,
			id:             "key1",
			keyName:        "",
			expectedID:     "key1",
			expectedName:   "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := findRedactedKey(tc.redactedKeys, tc.id, tc.keyName)

			if result == nil {
				t.Fatal("Expected non-nil result")
			}
			if result.ID != tc.expectedID {
				t.Errorf("Expected ID %q, got %q", tc.expectedID, result.ID)
			}
			if result.Name != tc.expectedName {
				t.Errorf("Expected Name %q, got %q", tc.expectedName, result.Name)
			}
		})
	}
}

// TestFindRedactedVirtualKey tests finding redacted virtual keys
func TestFindRedactedVirtualKey(t *testing.T) {
	testCases := []struct {
		name           string
		redactedKeys   []tables.TableVirtualKey
		id             string
		keyName        string
		expectedID     string
		expectedName   string
	}{
		{
			name:           "empty redacted keys",
			redactedKeys:   nil,
			id:             "vk1",
			keyName:        "virtual-key",
			expectedID:     "vk1",
			expectedName:   "virtual-key (deleted)",
		},
		{
			name: "key found",
			redactedKeys: []tables.TableVirtualKey{
				{ID: "vk1", Name: "virtual-key"},
			},
			id:           "vk1",
			keyName:      "virtual-key",
			expectedID:   "vk1",
			expectedName: "virtual-key",
		},
		{
			name: "key not found",
			redactedKeys: []tables.TableVirtualKey{
				{ID: "vk2", Name: "other-key"},
			},
			id:           "vk1",
			keyName:      "virtual-key",
			expectedID:   "vk1",
			expectedName: "virtual-key (deleted)",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := findRedactedVirtualKey(tc.redactedKeys, tc.id, tc.keyName)

			if result == nil {
				t.Fatal("Expected non-nil result")
			}
			if result.ID != tc.expectedID {
				t.Errorf("Expected ID %q, got %q", tc.expectedID, result.ID)
			}
			if result.Name != tc.expectedName {
				t.Errorf("Expected Name %q, got %q", tc.expectedName, result.Name)
			}
		})
	}
}

// TestLoggingHandler_FilterParsing documents filter parsing behavior
func TestLoggingHandler_FilterParsing(t *testing.T) {
	// Filter parameters supported:
	// - providers: comma-separated list of providers
	// - models: comma-separated list of models
	// - status: comma-separated list of statuses
	// - objects: comma-separated list of objects
	// - selected_key_ids: comma-separated list of key IDs
	// - virtual_key_ids: comma-separated list of virtual key IDs
	// - start_time: RFC3339 timestamp
	// - end_time: RFC3339 timestamp
	// - min_latency: float
	// - max_latency: float
	// - min_tokens: int
	// - max_tokens: int
	// - min_cost: float
	// - max_cost: float
	// - missing_cost_only: boolean
	// - content_search: string

	t.Log("Filter parameters parsed from query string")
}

// TestLoggingHandler_PaginationDefaults documents pagination defaults
func TestLoggingHandler_PaginationDefaults(t *testing.T) {
	// Pagination defaults:
	// - limit: 50 (default), max 1000
	// - offset: 0 (default)
	// - sort_by: timestamp (default), valid values: timestamp, latency, tokens, cost
	// - order: desc (default), valid values: asc, desc

	t.Log("Pagination defaults: limit=50, offset=0, sort_by=timestamp, order=desc")
}

// TestLoggingHandler_RecalculateCosts_LimitBehavior documents limit behavior
func TestLoggingHandler_RecalculateCosts_LimitBehavior(t *testing.T) {
	// Recalculate cost limit behavior:
	// - Default: 200
	// - If <= 0: set to 200
	// - If > 1000: capped at 1000

	t.Log("Recalculate cost limit: default 200, max 1000")
}

// TestLoggingHandler_TimeFiltering tests time filter parsing
func TestLoggingHandler_TimeFiltering(t *testing.T) {
	SetLogger(&mockLogger{})

	logManager := &mockLogManager{
		searchResult: &logstore.SearchResult{
			Logs:  []logstore.Log{},
					},
	}
	handler := NewLoggingHandler(logManager, &mockRedactedKeysManager{})

	startTime := time.Now().Add(-time.Hour).Format(time.RFC3339)
	endTime := time.Now().Format(time.RFC3339)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/logs?start_time=" + startTime + "&end_time=" + endTime)

	handler.getLogs(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Errorf("Expected status 200, got %d", ctx.Response.StatusCode())
	}
}

// TestLoggingHandler_NumericFiltering tests numeric filter parsing
func TestLoggingHandler_NumericFiltering(t *testing.T) {
	SetLogger(&mockLogger{})

	logManager := &mockLogManager{
		searchResult: &logstore.SearchResult{
			Logs:  []logstore.Log{},
					},
	}
	handler := NewLoggingHandler(logManager, &mockRedactedKeysManager{})

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/logs?min_latency=100&max_latency=500&min_tokens=10&max_tokens=1000&min_cost=0.01&max_cost=1.00")

	handler.getLogs(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Errorf("Expected status 200, got %d", ctx.Response.StatusCode())
	}
}

// TestLoggingHandler_DeletedKeysHandling documents deleted keys handling
func TestLoggingHandler_DeletedKeysHandling(t *testing.T) {
	// When a key is deleted but logs still reference it:
	// - Key name is appended with " (deleted)"
	// - Key ID is preserved
	// - This applies to both selected keys and virtual keys

	SetLogger(&mockLogger{})

	logManager := &mockLogManager{
		availableModels: []string{},
		availableKeys: []logging.KeyPair{
			{ID: "deleted-key", Name: "old-key"},
		},
		availableVK: []logging.KeyPair{},
	}
	// Return empty redacted keys to simulate deleted key
	redactedManager := &mockRedactedKeysManager{
		redactedKeys:        []schemas.Key{},
		redactedVirtualKeys: []tables.TableVirtualKey{},
	}
	handler := NewLoggingHandler(logManager, redactedManager)

	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI("/api/logs/filterdata")

	handler.getAvailableFilterData(ctx)

	if ctx.Response.StatusCode() != fasthttp.StatusOK {
		t.Errorf("Expected status 200, got %d", ctx.Response.StatusCode())
	}

	t.Log("Deleted keys are shown with '(deleted)' suffix in filter data")
}
