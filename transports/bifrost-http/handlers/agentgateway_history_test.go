package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/fasthttp/router"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// agentHistoryAggregateStore records the filter each A2A aggregate query was
// called with. Only the A2A reads are implemented; every other LogStore method
// is unreachable from these routes.
type agentHistoryAggregateStore struct {
	logstore.LogStore
	statsFilter     *logstore.AgentLogHistoryFilter
	histogramFilter *logstore.AgentLogHistoryFilter
	bucketSize      int64
	entryID         string
	listCalled      bool
}

func (s *agentHistoryAggregateStore) GetAgentLogStats(_ context.Context, filter logstore.AgentLogHistoryFilter) (*logstore.AgentLogStats, error) {
	s.statsFilter = &filter
	return &logstore.AgentLogStats{TotalEntries: 3, SuccessCount: 2, ErrorCount: 1, SuccessRate: 66.67, AverageLatency: 120}, nil
}

func (s *agentHistoryAggregateStore) GetAgentHistogram(_ context.Context, filter logstore.AgentLogHistoryFilter, bucketSizeSeconds int64) (*logstore.AgentHistogramResult, error) {
	s.histogramFilter = &filter
	s.bucketSize = bucketSizeSeconds
	return &logstore.AgentHistogramResult{BucketSizeSeconds: bucketSizeSeconds}, nil
}

func (s *agentHistoryAggregateStore) ListAgentLogHistory(_ context.Context, _ logstore.AgentLogHistoryFilter, _ logstore.PaginationOptions) (*logstore.AgentLogHistoryResult, error) {
	s.listCalled = true
	return &logstore.AgentLogHistoryResult{Logs: []logstore.AgentLogSummary{}, Pagination: logstore.PaginationOptions{Limit: 50}}, nil
}

func (s *agentHistoryAggregateStore) FindAgentLog(_ context.Context, id string) (*logstore.AgentLog, error) {
	s.entryID = id
	return &logstore.AgentLog{ID: id}, nil
}

func agentHistoryAggregateRouter(store *agentHistoryAggregateStore) *router.Router {
	handler := &AgentGatewayHandler{config: &lib.Config{LogsStore: store}}
	r := router.New()
	handler.RegisterManagementRoutes(r)
	return r
}

func serveAgentHistory(r *router.Router, uri string) *fasthttp.RequestCtx {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.Header.SetMethod(fasthttp.MethodGet)
	ctx.Request.SetRequestURI(uri)
	r.Handler(ctx)
	return ctx
}

// The aggregate routes are static siblings of the pre-existing
// /api/agents/history/{id} wildcard. This pins that both still resolve, so a
// request for "stats" can never be mistaken for an entry id lookup.
func TestAgentGatewayHistoryRoutesResolve(t *testing.T) {
	store := &agentHistoryAggregateStore{}
	r := agentHistoryAggregateRouter(store)

	const window = "start_time=2026-01-01T00:00:00Z&end_time=2026-01-01T01:00:00Z"

	ctx := serveAgentHistory(r, "/api/agents/history/stats?"+window)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	require.NotNil(t, store.statsFilter, "stats must not fall through to the entry lookup")
	require.Empty(t, store.entryID)
	var stats logstore.AgentLogStats
	require.NoError(t, json.Unmarshal(ctx.Response.Body(), &stats))
	require.EqualValues(t, 3, stats.TotalEntries)

	ctx = serveAgentHistory(r, "/api/agents/history/histogram?"+window)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	require.NotNil(t, store.histogramFilter)
	require.Empty(t, store.entryID)
	// One hour is under the 2-hour threshold, so the shared ladder buckets by minute.
	require.EqualValues(t, 60, store.bucketSize)

	ctx = serveAgentHistory(r, "/api/agents/history/a2a-entry-1")
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	require.Equal(t, "a2a-entry-1", store.entryID)
}

func TestAgentGatewayHistoryDefaultRemainsMetadataOnly(t *testing.T) {
	store := &agentHistoryAggregateStore{}
	ctx := serveAgentHistory(agentHistoryAggregateRouter(store), "/api/agents/history?context_id=context-1&record_kind=request")
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	require.True(t, store.listCalled)
}

// The aggregates must see exactly the filters the list endpoint sees, including
// repeated multi-value params and the free-text search.
func TestAgentGatewayHistoryAggregatesShareListFilters(t *testing.T) {
	store := &agentHistoryAggregateStore{}
	r := agentHistoryAggregateRouter(store)

	query := "start_time=2026-01-01T00:00:00Z&end_time=2026-01-01T01:00:00Z" +
		"&agent_name=alpha&agent_name=beta&operation=SendMessage&status=error&search=task-alpha"

	ctx := serveAgentHistory(r, "/api/agents/history/stats?"+query)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	require.Equal(t, []string{"alpha", "beta"}, store.statsFilter.AgentName)
	require.Equal(t, []string{"SendMessage"}, store.statsFilter.Operation)
	require.Equal(t, []string{"error"}, store.statsFilter.Status)
	require.Equal(t, "task-alpha", store.statsFilter.Search)
	require.NotNil(t, store.statsFilter.StartTime)

	ctx = serveAgentHistory(r, "/api/agents/history/histogram?"+query)
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	require.Equal(t, []string{"alpha", "beta"}, store.histogramFilter.AgentName)
	require.Equal(t, "task-alpha", store.histogramFilter.Search)

	// An unbounded request is allowed, matching the LLM and MCP stats endpoints.
	store.statsFilter = nil
	ctx = serveAgentHistory(r, "/api/agents/history/stats")
	require.Equal(t, fasthttp.StatusOK, ctx.Response.StatusCode())
	require.NotNil(t, store.statsFilter)
	require.Nil(t, store.statsFilter.StartTime)
}
