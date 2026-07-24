package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/queryscope"
	"github.com/maximhq/bifrost/framework/sidekiq"
	loggingplugin "github.com/maximhq/bifrost/plugins/logging"
	"github.com/valyala/fasthttp"
	"gorm.io/gorm"
)

// TestShouldUseFilterDataCacheAllowsUnscopedEmptyQuery verifies unscoped
// requests can still share the no-query filterdata cache.
func TestShouldUseFilterDataCacheAllowsUnscopedEmptyQuery(t *testing.T) {
	if !shouldUseFilterDataCache(context.Background(), "") {
		t.Fatal("expected unscoped empty-query request to use filterdata cache")
	}
	if !shouldUseFilterDataCache(context.Background(), "   ") {
		t.Fatal("expected whitespace-only query to use filterdata cache")
	}
}

// TestShouldUseFilterDataCacheRejectsSearchQuery verifies search requests are
// request-specific and must not share the empty-query cache.
func TestShouldUseFilterDataCacheRejectsSearchQuery(t *testing.T) {
	if shouldUseFilterDataCache(context.Background(), "vk") {
		t.Fatal("expected non-empty query to bypass filterdata cache")
	}
}

// TestShouldUseFilterDataCacheRejectsScopedContext verifies DAC-scoped
// requests never consume or populate the shared all-data cache.
func TestShouldUseFilterDataCacheRejectsScopedContext(t *testing.T) {
	ctx := queryscope.WithQueryScope(context.Background(), func(db *gorm.DB) *gorm.DB {
		return db.Where("1 = 0")
	})
	if shouldUseFilterDataCache(ctx, "") {
		t.Fatal("expected scoped request to bypass filterdata cache")
	}
}

func TestGetDashboard(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		failStats  bool
		wantStatus int
		assert     func(t *testing.T, mgr *dashboardLogManager, body []byte)
	}{
		{
			name:       "success includes all sections",
			query:      "providers=openai&models=gpt-4&tool_names=calculator&server_labels=primary",
			wantStatus: fasthttp.StatusOK,
			assert: func(t *testing.T, mgr *dashboardLogManager, body []byte) {
				t.Helper()
				var payload map[string]json.RawMessage
				if err := json.Unmarshal(body, &payload); err != nil {
					t.Fatalf("decode dashboard response: %v", err)
				}
				for _, key := range []string{"meta", "overview", "provider_usage", "model_rankings", "dimension_rankings", "mcp"} {
					if _, ok := payload[key]; !ok {
						t.Fatalf("expected top-level key %q in response", key)
					}
				}
				var response logstore.DashboardResult
				if err := json.Unmarshal(body, &response); err != nil {
					t.Fatalf("decode dashboard result: %v", err)
				}
				if response.Overview.Models == nil || response.ModelRankings.Histogram == nil {
					t.Fatal("expected shared model histogram to populate both sections")
				}
				if len(response.DimensionRankings) != len(dashboardRankingDimensions) {
					t.Fatalf("expected %d dimension rankings, got %d", len(dashboardRankingDimensions), len(response.DimensionRankings))
				}
				if got := mgr.lastLLMFilters.Providers; len(got) != 1 || got[0] != "openai" {
					t.Fatalf("expected LLM providers filter, got %#v", got)
				}
				if got := mgr.lastMCPFilters.ToolNames; len(got) != 1 || got[0] != "calculator" {
					t.Fatalf("expected MCP tool_names filter, got %#v", got)
				}
			},
		},
		{
			name:       "sub-query error returns no partial dashboard",
			failStats:  true,
			wantStatus: fasthttp.StatusInternalServerError,
			assert: func(t *testing.T, mgr *dashboardLogManager, body []byte) {
				t.Helper()
				if json.Valid(body) {
					var payload map[string]json.RawMessage
					if err := json.Unmarshal(body, &payload); err == nil {
						if _, ok := payload["overview"]; ok {
							t.Fatalf("expected error payload, got partial dashboard: %s", string(body))
						}
					}
				}
			},
		},
		{
			name:       "MCP filters are isolated from LLM filters",
			query:      "providers=openai&models=gpt-4&tool_names=calculator,clock&server_labels=primary&virtual_key_ids=vk-llm",
			wantStatus: fasthttp.StatusOK,
			assert: func(t *testing.T, mgr *dashboardLogManager, body []byte) {
				t.Helper()
				if len(mgr.lastLLMFilters.Providers) != 1 || mgr.lastLLMFilters.Providers[0] != "openai" {
					t.Fatalf("expected LLM providers filter, got %#v", mgr.lastLLMFilters.Providers)
				}
				if len(mgr.lastMCPFilters.ToolNames) != 2 {
					t.Fatalf("expected MCP tool filters, got %#v", mgr.lastMCPFilters.ToolNames)
				}
				if mgr.lastLLMFilters.ContentSearch == "calculator" {
					t.Fatal("MCP tool_names leaked into LLM filters")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			SetLogger(&mockLogger{})

			mgr := &dashboardLogManager{failStats: tt.failStats}
			h := &LoggingHandler{logManager: mgr}
			var req fasthttp.Request
			uri := "/api/logs/dashboard"
			if tt.query != "" {
				uri += "?" + tt.query
			}
			req.SetRequestURI(uri)

			ctx := &fasthttp.RequestCtx{}
			ctx.Init(&req, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}, nil)

			h.getDashboard(ctx)

			if got := ctx.Response.StatusCode(); got != tt.wantStatus {
				t.Fatalf("expected status %d, got %d: %s", tt.wantStatus, got, string(ctx.Response.Body()))
			}
			if tt.assert != nil {
				tt.assert(t, mgr, ctx.Response.Body())
			}
		})
	}
}

func TestRecalculateLogCostsResolvesPeriodFilter(t *testing.T) {
	SetLogger(&mockLogger{})

	mgr := &dashboardLogManager{}
	store := newFakeSidekiqStore()
	runner := sidekiq.New(store, &mockLogger{}, 1, "")
	h := &LoggingHandler{logManager: mgr}
	h.SetSidekiqBackend(runner, store)

	var req fasthttp.Request
	req.Header.SetMethod(fasthttp.MethodPost)
	req.SetRequestURI("/api/logs/recalculate-cost")
	req.Header.SetContentType("application/json")
	req.SetBodyString(`{"filters":{"period":"1h"}}`)

	ctx := &fasthttp.RequestCtx{}
	ctx.Init(&req, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}, nil)

	h.recalculateLogCosts(ctx)

	// The job is enqueued for background processing, so the endpoint returns 202.
	if got := ctx.Response.StatusCode(); got != fasthttp.StatusAccepted {
		t.Fatalf("expected status 202, got %d: %s", got, string(ctx.Response.Body()))
	}
	// The period must be resolved into an explicit window before the job is built.
	filters := mgr.lastRecalculateFilters
	if filters.StartTime == nil || filters.EndTime == nil {
		t.Fatalf("expected period to resolve start/end, got start=%v end=%v", filters.StartTime, filters.EndTime)
	}
	if !filters.EndTime.After(*filters.StartTime) {
		t.Fatalf("expected end_time after start_time, got start=%s end=%s", filters.StartTime, filters.EndTime)
	}
	if store.createdCount() != 1 {
		t.Fatalf("expected exactly one job to be enqueued, got %d", store.createdCount())
	}
}

func TestRecalculateLogCostsRejectsDuplicateJob(t *testing.T) {
	SetLogger(&mockLogger{})

	mgr := &dashboardLogManager{}
	store := newFakeSidekiqStore()
	// Seed an in-flight job so the endpoint should refuse to start a second one.
	store.inFlight = &tables.TableSidekiqJob{
		ID:       "logs_recalculate_cost_existing",
		Kind:     loggingplugin.CostRecalcJobKind,
		Status:   tables.SidekiqStatusRunning,
		Metadata: "{}",
	}
	runner := sidekiq.New(store, &mockLogger{}, 1, "")
	h := &LoggingHandler{logManager: mgr}
	h.SetSidekiqBackend(runner, store)

	var req fasthttp.Request
	req.Header.SetMethod(fasthttp.MethodPost)
	req.SetRequestURI("/api/logs/recalculate-cost")
	req.Header.SetContentType("application/json")
	req.SetBodyString(`{"filters":{"period":"1h"}}`)

	ctx := &fasthttp.RequestCtx{}
	ctx.Init(&req, &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}, nil)

	h.recalculateLogCosts(ctx)

	if got := ctx.Response.StatusCode(); got != fasthttp.StatusConflict {
		t.Fatalf("expected status 409, got %d: %s", got, string(ctx.Response.Body()))
	}
	if store.createdCount() != 0 {
		t.Fatalf("expected no new job to be enqueued, got %d", store.createdCount())
	}
}

// fakeSidekiqStore implements both sidekiq.Store (for the runner) and
// handlers.SidekiqJobStore (for the endpoints), backed by an in-memory map.
type fakeSidekiqStore struct {
	mu       sync.Mutex
	jobs     map[string]*tables.TableSidekiqJob
	created  int
	inFlight *tables.TableSidekiqJob
}

func newFakeSidekiqStore() *fakeSidekiqStore {
	return &fakeSidekiqStore{jobs: make(map[string]*tables.TableSidekiqJob)}
}

func (s *fakeSidekiqStore) createdCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.created
}

func (s *fakeSidekiqStore) CreateSidekiqJob(ctx context.Context, job *tables.TableSidekiqJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.created++
	copy := *job
	s.jobs[job.ID] = &copy
	return nil
}

func (s *fakeSidekiqStore) GetSidekiqJob(ctx context.Context, id string) (*tables.TableSidekiqJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job, ok := s.jobs[id]; ok {
		copy := *job
		return &copy, nil
	}
	return nil, nil
}

func (s *fakeSidekiqStore) GetInFlightSidekiqJobByKind(ctx context.Context, kind string) (*tables.TableSidekiqJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inFlight != nil && s.inFlight.Kind == kind {
		copy := *s.inFlight
		return &copy, nil
	}
	return nil, nil
}

func (s *fakeSidekiqStore) ClaimSidekiqJob(ctx context.Context, id, runnerID string, staleBefore time.Time) (bool, error) {
	return true, nil
}
func (s *fakeSidekiqStore) HeartbeatSidekiqJob(ctx context.Context, id, runnerID string) (bool, error) {
	return true, nil
}
func (s *fakeSidekiqStore) UpdateSidekiqJobProgress(ctx context.Context, id, runnerID, metadata string) error {
	return nil
}
func (s *fakeSidekiqStore) CompleteSidekiqJob(ctx context.Context, id, runnerID, metadata string) error {
	return nil
}
func (s *fakeSidekiqStore) FailSidekiqJob(ctx context.Context, id, runnerID, metadata, lastErr string) error {
	return nil
}
func (s *fakeSidekiqStore) ListClaimableSidekiqJobs(ctx context.Context, staleBefore time.Time) ([]tables.TableSidekiqJob, error) {
	return nil, nil
}

type dashboardLogManager struct {
	failStats              bool
	lastLLMFilters         logstore.SearchFilters
	lastMCPFilters         logstore.MCPToolLogSearchFilters
	lastRecalculateFilters logstore.SearchFilters
	lastRecalculateContext chan context.Context
}

func (m *dashboardLogManager) GetLog(ctx context.Context, id string) (*logstore.Log, error) {
	return nil, nil
}
func (m *dashboardLogManager) Search(ctx context.Context, filters *logstore.SearchFilters, pagination *logstore.PaginationOptions) (*logstore.SearchResult, error) {
	return nil, nil
}
func (m *dashboardLogManager) GetSessionLogs(ctx context.Context, sessionID string, pagination *logstore.PaginationOptions) (*logstore.SessionDetailResult, error) {
	return nil, nil
}
func (m *dashboardLogManager) GetSessionSummary(ctx context.Context, sessionID string) (*logstore.SessionSummaryResult, error) {
	return nil, nil
}
func (m *dashboardLogManager) GetStats(ctx context.Context, filters *logstore.SearchFilters) (*logstore.SearchStats, error) {
	m.lastLLMFilters = *filters
	if m.failStats {
		return nil, errors.New("stats failed")
	}
	return &logstore.SearchStats{}, nil
}
func (m *dashboardLogManager) GetHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.HistogramResult, error) {
	return &logstore.HistogramResult{}, nil
}
func (m *dashboardLogManager) GetTokenHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.TokenHistogramResult, error) {
	return &logstore.TokenHistogramResult{}, nil
}
func (m *dashboardLogManager) GetCostHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.CostHistogramResult, error) {
	return &logstore.CostHistogramResult{}, nil
}
func (m *dashboardLogManager) GetModelHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ModelHistogramResult, error) {
	return &logstore.ModelHistogramResult{}, nil
}
func (m *dashboardLogManager) GetLatencyHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.LatencyHistogramResult, error) {
	return &logstore.LatencyHistogramResult{}, nil
}
func (m *dashboardLogManager) GetProviderCostHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ProviderCostHistogramResult, error) {
	return &logstore.ProviderCostHistogramResult{}, nil
}
func (m *dashboardLogManager) GetProviderTokenHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ProviderTokenHistogramResult, error) {
	return &logstore.ProviderTokenHistogramResult{}, nil
}
func (m *dashboardLogManager) GetProviderLatencyHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ProviderLatencyHistogramResult, error) {
	return &logstore.ProviderLatencyHistogramResult{}, nil
}
func (m *dashboardLogManager) GetThroughputHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ThroughputHistogramResult, error) {
	return &logstore.ThroughputHistogramResult{}, nil
}
func (m *dashboardLogManager) GetProviderThroughputHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ProviderThroughputHistogramResult, error) {
	return &logstore.ProviderThroughputHistogramResult{}, nil
}
func (m *dashboardLogManager) GetModelRankings(ctx context.Context, filters *logstore.SearchFilters) (*logstore.ModelRankingResult, error) {
	return &logstore.ModelRankingResult{}, nil
}
func (m *dashboardLogManager) GetDimensionRankings(ctx context.Context, filters *logstore.SearchFilters, dimension logstore.RankingDimension) (*logstore.DimensionRankingResult, error) {
	return &logstore.DimensionRankingResult{Dimension: dimension}, nil
}
func (m *dashboardLogManager) GetDroppedRequests(ctx context.Context) int64 { return 0 }
func (m *dashboardLogManager) GetAvailableModels(ctx context.Context, limit int, query string) ([]string, error) {
	return nil, nil
}
func (m *dashboardLogManager) GetAvailableAliases(ctx context.Context, limit int, query string) ([]string, error) {
	return nil, nil
}
func (m *dashboardLogManager) GetAvailableSelectedKeys(ctx context.Context, limit int, query string) ([]loggingplugin.KeyPair, error) {
	return nil, nil
}
func (m *dashboardLogManager) GetAvailableVirtualKeys(ctx context.Context, limit int, query string) ([]loggingplugin.KeyPair, error) {
	return nil, nil
}
func (m *dashboardLogManager) GetAvailableRoutingRules(ctx context.Context, limit int, query string) ([]loggingplugin.KeyPair, error) {
	return nil, nil
}
func (m *dashboardLogManager) GetAvailableRoutingEngines(ctx context.Context, limit int, query string) ([]string, error) {
	return nil, nil
}
func (m *dashboardLogManager) GetAvailableStopReasons(ctx context.Context, limit int, query string) ([]string, error) {
	return nil, nil
}
func (m *dashboardLogManager) GetAvailableTeams(ctx context.Context, limit int, query string) ([]loggingplugin.KeyPair, error) {
	return nil, nil
}
func (m *dashboardLogManager) GetAvailableCustomers(ctx context.Context, limit int, query string) ([]loggingplugin.KeyPair, error) {
	return nil, nil
}
func (m *dashboardLogManager) GetAvailableUsers(ctx context.Context, limit int, query string) ([]loggingplugin.KeyPair, error) {
	return nil, nil
}
func (m *dashboardLogManager) GetAvailableBusinessUnits(ctx context.Context, limit int, query string) ([]loggingplugin.KeyPair, error) {
	return nil, nil
}
func (m *dashboardLogManager) GetAvailableMetadataKeys(ctx context.Context, limit int, query string) (map[string][]string, error) {
	return nil, nil
}
func (m *dashboardLogManager) GetDimensionCostHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64, dimension logstore.HistogramDimension) (*logstore.DimensionCostHistogramResult, error) {
	return nil, nil
}
func (m *dashboardLogManager) GetDimensionTokenHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64, dimension logstore.HistogramDimension) (*logstore.DimensionTokenHistogramResult, error) {
	return nil, nil
}
func (m *dashboardLogManager) GetDimensionLatencyHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64, dimension logstore.HistogramDimension) (*logstore.DimensionLatencyHistogramResult, error) {
	return nil, nil
}
func (m *dashboardLogManager) DeleteLog(ctx context.Context, id string) error     { return nil }
func (m *dashboardLogManager) DeleteLogs(ctx context.Context, ids []string) error { return nil }
func (m *dashboardLogManager) RecalculateCosts(ctx context.Context, filters *logstore.SearchFilters, limit int) (*loggingplugin.RecalculateCostResult, error) {
	m.lastRecalculateFilters = *filters
	return &loggingplugin.RecalculateCostResult{}, nil
}
func (m *dashboardLogManager) RecalculateCostsWithProgress(ctx context.Context, filters *logstore.SearchFilters, limit int, progress func(loggingplugin.RecalculateCostProgress)) (*loggingplugin.RecalculateCostResult, error) {
	m.lastRecalculateFilters = *filters
	if m.lastRecalculateContext != nil {
		m.lastRecalculateContext <- ctx
	}
	return nil, nil
}
func (m *dashboardLogManager) BuildCostRecalcJobMeta(ctx context.Context, filters logstore.SearchFilters, missingCostOnly bool) (string, error) {
	m.lastRecalculateFilters = filters
	return "{}", nil
}
func (m *dashboardLogManager) RunCostRecalcJob(ctx context.Context, metaJSON string, checkpoint func(string) error) (string, error) {
	return metaJSON, nil
}
func (m *dashboardLogManager) GetMCPToolLog(ctx context.Context, id string) (*logstore.MCPToolLog, error) {
	return nil, nil
}
func (m *dashboardLogManager) SearchMCPToolLogs(ctx context.Context, filters *logstore.MCPToolLogSearchFilters, pagination *logstore.PaginationOptions) (*logstore.MCPToolLogSearchResult, error) {
	return nil, nil
}
func (m *dashboardLogManager) GetMCPToolLogStats(ctx context.Context, filters *logstore.MCPToolLogSearchFilters) (*logstore.MCPToolLogStats, error) {
	return nil, nil
}
func (m *dashboardLogManager) GetAvailableToolNames(ctx context.Context, limit int, query string) ([]string, error) {
	return nil, nil
}
func (m *dashboardLogManager) GetAvailableServerLabels(ctx context.Context, limit int, query string) ([]string, error) {
	return nil, nil
}
func (m *dashboardLogManager) GetAvailableMCPVirtualKeys(ctx context.Context, limit int, query string) ([]loggingplugin.KeyPair, error) {
	return nil, nil
}
func (m *dashboardLogManager) GetMCPHistogram(ctx context.Context, filters logstore.MCPToolLogSearchFilters, bucketSizeSeconds int64) (*logstore.MCPHistogramResult, error) {
	m.lastMCPFilters = filters
	return &logstore.MCPHistogramResult{}, nil
}
func (m *dashboardLogManager) GetMCPCostHistogram(ctx context.Context, filters logstore.MCPToolLogSearchFilters, bucketSizeSeconds int64) (*logstore.MCPCostHistogramResult, error) {
	return &logstore.MCPCostHistogramResult{}, nil
}
func (m *dashboardLogManager) GetMCPTopTools(ctx context.Context, filters logstore.MCPToolLogSearchFilters, limit int) (*logstore.MCPTopToolsResult, error) {
	return &logstore.MCPTopToolsResult{}, nil
}

func (m *dashboardLogManager) DeleteMCPToolLogs(ctx context.Context, ids []string) error { return nil }

func (m *dashboardLogManager) CreateUserAgentMapping(ctx context.Context, mapping *logstore.UserAgentMapping) (*logstore.UserAgentMapping, error) {
	return nil, nil
}

func (m *dashboardLogManager) DeleteUserAgentMapping(ctx context.Context, id string) error {
	return nil
}

func (m *dashboardLogManager) UpdateUserAgentMapping(ctx context.Context, id string, mapping *logstore.UserAgentMapping) (*logstore.UserAgentMapping, error) {
	return nil, nil
}

func (m *dashboardLogManager) ListUserAgentMappings(ctx context.Context) ([]logstore.UserAgentMapping, error) {
	return nil, nil
}

func (m *dashboardLogManager) GetAvailableUserAgents(ctx context.Context, _ int, _ string) ([]string, error) {
	return nil, nil
}

func (m *dashboardLogManager) GetAvailableApps(ctx context.Context, _ int, _ string) ([]string, error) {
	return nil, nil
}

func (m *dashboardLogManager) GetAvailableMCPApps(ctx context.Context, _ int, _ string) ([]string, error) {
	return nil, nil
}

func (m *dashboardLogManager) GetAvailableMCPUserAgents(ctx context.Context, _ int, _ string) ([]string, error) {
	return nil, nil
}
