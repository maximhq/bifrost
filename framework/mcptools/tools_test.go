package mcptools

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bytedance/sonic"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/require"
)

// LogReaderStub embeds the LogReader interface without implementing it.
//
// This is the point: a fake embedding it satisfies the interface at compile
// time, but any method the fake does not override panics with a nil-pointer
// dereference when called. The tools are supposed to touch a small, known
// subset of the read surface, so a test that suddenly panics is telling us an
// executor started reaching somewhere new - which is exactly the change that
// should require a human to look.
type LogReaderStub struct {
	LogReader
}

// fakeLogReader records what the tools asked for. Only the methods the tools
// reach are implemented; the rest of LogReader is embedded as a nil interface,
// so an executor that starts calling something new fails loudly with a
// nil-pointer panic in tests rather than silently widening the read surface.
//
// mu guards every field below: this server serves concurrent callers, and
// describe_filter_space fans its lookups out, so the fake is hit from more
// than one goroutine at once.
type fakeLogReader struct {
	LogReaderStub

	mu sync.Mutex

	searchFilters    *logstore.SearchFilters
	searchPagination *logstore.PaginationOptions
	searchResult     *logstore.SearchResult

	rankingFilters   *logstore.SearchFilters
	rankingDimension logstore.RankingDimension
	// Canned ranking responses. Nil means the minimal shape each method has
	// always returned.
	modelRankingResult     *logstore.ModelRankingResult
	dimensionRankingResult *logstore.DimensionRankingResult

	histogramBucket int64
	// Canned histogram responses. Nil means "empty result, zero buckets" -
	// enough for tests that only care about a call reaching the store, not
	// about what came back.
	histogramResult                   *logstore.HistogramResult
	latencyHistogramResult            *logstore.LatencyHistogramResult
	tokenHistogramResult              *logstore.TokenHistogramResult
	costHistogramResult               *logstore.CostHistogramResult
	throughputHistogramResult         *logstore.ThroughputHistogramResult
	providerLatencyHistogramResult    *logstore.ProviderLatencyHistogramResult
	providerTokenHistogramResult      *logstore.ProviderTokenHistogramResult
	providerCostHistogramResult       *logstore.ProviderCostHistogramResult
	providerThroughputHistogramResult *logstore.ProviderThroughputHistogramResult
	statsCalled                       bool
	// Distinct-value lookups, the cheap path describe_filter_space takes.
	availableTeams         []KeyPair
	availableCustomers     []KeyPair
	availableBusinessUnits []KeyPair
	availableVirtualKeys   []KeyPair
	availableModels        []string
	availableApps          []string
	availableStopReasons   []string
	// Each records the query string its own lookup was called with - a
	// distinct field per method rather than one shared slice, since the four
	// org-hierarchy lookups run concurrently and a shared field would race.
	teamsQuerySeen         string
	customersQuerySeen     string
	businessUnitsQuerySeen string
	virtualKeysQuerySeen   string
	// statsCalls counts them, so a test can assert how many of a turn's tool
	// calls actually reached the store rather than only that one did.
	statsCalls int
	// statsFiltersSeen records the filters passed to each GetStats call in
	// order, so a test can tell a current-period call from a previous-period
	// one apart by the window each carried.
	statsFiltersSeen []*logstore.SearchFilters
	// statsResponses, when set, is returned one per call in order instead of
	// the zero-value default - the shape compare_to_previous needs to script
	// a current period distinct from a previous one.
	statsResponses []*logstore.SearchStats
	// statsByProvider, when set, answers a GetStats call narrowed to exactly
	// one provider - order-independent, so concurrent per-provider calls get
	// the right stats regardless of which one reaches the fake first.
	statsByProvider map[string]*logstore.SearchStats
	sawContext      context.Context
	// *Delay, when set, makes the matching method take real wall-clock time
	// before returning - the lever a test has to prove several calls actually
	// overlap (or complete out of order) rather than just not visibly failing
	// when run together.
	statsDelay          time.Duration
	histogramDelay      time.Duration
	costHistogramDelay  time.Duration
	tokenHistogramDelay time.Duration
	// activeStatsCalls and peakStatsCalls track how many GetStats calls are
	// in flight at once, atomically - the lever a test has to prove a global
	// concurrency cap (see toolCallSem) actually bounds callers spread across
	// several Agent/turn instances, not just within one.
	activeStatsCalls int32
	peakStatsCalls   int32
	// entered and release, when both set, turn GetStats into a rendezvous
	// point: each call sends on entered right after it is counted in
	// activeStatsCalls, then blocks until release is closed. This lets a
	// test wait for an exact number of calls to actually be in flight at
	// once and assert on that count directly, instead of inferring overlap
	// from wall-clock delays and a timing margin that could flake under
	// scheduler jitter.
	entered chan struct{}
	release chan struct{}

	// getLogFunc, when set, answers GetLog by id - the lever get_request_trace's
	// tests use to script a root row and its fallback children as distinct rows
	// behind distinct ids. Unset behaves like a real store asked for a row that
	// does not exist, rather than panicking through the embedded nil stub.
	getLogFunc func(ctx context.Context, id string) (*logstore.Log, error)

	sessionLogs     *logstore.SessionDetailResult
	sessionSummary  *logstore.SessionSummaryResult
	droppedRequests int64
	mcpSearchResult *logstore.MCPToolLogSearchResult
	// mcpSearchFilters and sessionIDSeen record what the tool asked the store
	// for, so a test can check the call reached it with the caller's input.
	mcpSearchFilters *logstore.MCPToolLogSearchFilters
	sessionIDSeen    string
	mcpStats         *logstore.MCPToolLogStats
	mcpLog           *logstore.MCPToolLog
	mcpHistogram     *logstore.MCPHistogramResult
	mcpCostHistogram *logstore.MCPCostHistogramResult
	mcpTopTools      *logstore.MCPTopToolsResult
}

func (f *fakeLogReader) GetLog(ctx context.Context, id string) (*logstore.Log, error) {
	f.mu.Lock()
	fn := f.getLogFunc
	f.sawContext = ctx
	f.mu.Unlock()
	if fn == nil {
		return nil, fmt.Errorf("no log found with id %s", id)
	}
	return fn(ctx, id)
}

func (f *fakeLogReader) GetSessionLogs(ctx context.Context, sessionID string, pagination *logstore.PaginationOptions) (*logstore.SessionDetailResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sawContext = ctx
	f.sessionIDSeen = sessionID
	if f.sessionLogs == nil {
		return &logstore.SessionDetailResult{SessionID: sessionID}, nil
	}
	return f.sessionLogs, nil
}

func (f *fakeLogReader) GetSessionSummary(ctx context.Context, sessionID string) (*logstore.SessionSummaryResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sawContext = ctx
	if f.sessionSummary == nil {
		return &logstore.SessionSummaryResult{SessionID: sessionID}, nil
	}
	return f.sessionSummary, nil
}

func (f *fakeLogReader) GetDroppedRequests(context.Context) int64 {
	return f.droppedRequests
}

func (f *fakeLogReader) GetMCPToolLog(ctx context.Context, id string) (*logstore.MCPToolLog, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sawContext = ctx
	if f.mcpLog == nil {
		return nil, fmt.Errorf("no mcp log found with id %s", id)
	}
	return f.mcpLog, nil
}

func (f *fakeLogReader) SearchMCPToolLogs(ctx context.Context, filters *logstore.MCPToolLogSearchFilters, pagination *logstore.PaginationOptions) (*logstore.MCPToolLogSearchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sawContext = ctx
	f.mcpSearchFilters = filters
	if f.mcpSearchResult != nil {
		return f.mcpSearchResult, nil
	}
	return &logstore.MCPToolLogSearchResult{}, nil
}

func (f *fakeLogReader) GetMCPToolLogStats(ctx context.Context, filters *logstore.MCPToolLogSearchFilters) (*logstore.MCPToolLogStats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sawContext = ctx
	if f.mcpStats != nil {
		return f.mcpStats, nil
	}
	return &logstore.MCPToolLogStats{}, nil
}

func (f *fakeLogReader) GetMCPHistogram(ctx context.Context, filters logstore.MCPToolLogSearchFilters, bucketSizeSeconds int64) (*logstore.MCPHistogramResult, error) {
	if f.mcpHistogram != nil {
		return f.mcpHistogram, nil
	}
	return &logstore.MCPHistogramResult{BucketSizeSeconds: bucketSizeSeconds}, nil
}

func (f *fakeLogReader) GetMCPCostHistogram(ctx context.Context, filters logstore.MCPToolLogSearchFilters, bucketSizeSeconds int64) (*logstore.MCPCostHistogramResult, error) {
	if f.mcpCostHistogram != nil {
		return f.mcpCostHistogram, nil
	}
	return &logstore.MCPCostHistogramResult{BucketSizeSeconds: bucketSizeSeconds}, nil
}

func (f *fakeLogReader) GetMCPTopTools(ctx context.Context, filters logstore.MCPToolLogSearchFilters, limit int) (*logstore.MCPTopToolsResult, error) {
	if f.mcpTopTools != nil {
		return f.mcpTopTools, nil
	}
	return &logstore.MCPTopToolsResult{}, nil
}

// enter records one more of this fake's calls starting, updating the
// high-water mark if this is the most that have ever overlapped, and returns
// the matching exit func.
func (f *fakeLogReader) enter() (exit func()) {
	active := atomic.AddInt32(&f.activeStatsCalls, 1)
	for {
		peak := atomic.LoadInt32(&f.peakStatsCalls)
		if active <= peak || atomic.CompareAndSwapInt32(&f.peakStatsCalls, peak, active) {
			break
		}
	}
	return func() { atomic.AddInt32(&f.activeStatsCalls, -1) }
}

func (f *fakeLogReader) Search(ctx context.Context, filters *logstore.SearchFilters, pagination *logstore.PaginationOptions) (*logstore.SearchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sawContext = ctx
	f.searchFilters, f.searchPagination = filters, pagination
	if f.searchResult != nil {
		return f.searchResult, nil
	}
	return &logstore.SearchResult{Logs: nil, Pagination: *pagination}, nil
}

func (f *fakeLogReader) GetDimensionRankings(ctx context.Context, filters *logstore.SearchFilters, dimension logstore.RankingDimension) (*logstore.DimensionRankingResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sawContext = ctx
	f.rankingFilters, f.rankingDimension = filters, dimension
	// A test that cares what was ranked sets the result; the rest get the
	// refactor's empty default.
	if f.dimensionRankingResult != nil {
		return f.dimensionRankingResult, nil
	}
	return &logstore.DimensionRankingResult{}, nil
}

func (f *fakeLogReader) GetModelRankings(ctx context.Context, filters *logstore.SearchFilters) (*logstore.ModelRankingResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sawContext = ctx
	f.rankingFilters = filters
	if f.modelRankingResult != nil {
		return f.modelRankingResult, nil
	}
	return &logstore.ModelRankingResult{}, nil
}

// wait sleeps for delay, if any, without holding the fake's lock - holding it
// across the sleep would serialize every concurrent caller through this fake
// regardless of whether the agent loop itself ran them concurrently, which is
// exactly the thing a test using a delay is trying to observe. ctx.Done()
// still cuts it short, the same as a real, cancellable store call would.
func wait(ctx context.Context, delay time.Duration) {
	if delay <= 0 {
		return
	}
	select {
	case <-time.After(delay):
	case <-ctx.Done():
	}
}

func (f *fakeLogReader) GetStats(ctx context.Context, filters *logstore.SearchFilters) (*logstore.SearchStats, error) {
	f.mu.Lock()
	f.sawContext = ctx
	f.statsCalled = true
	f.statsFiltersSeen = append(f.statsFiltersSeen, filters)
	var resp *logstore.SearchStats
	if len(filters.Providers) == 1 && f.statsByProvider[filters.Providers[0]] != nil {
		resp = f.statsByProvider[filters.Providers[0]]
	} else if len(f.statsResponses) > f.statsCalls {
		resp = f.statsResponses[f.statsCalls]
	} else {
		resp = &logstore.SearchStats{}
	}
	f.statsCalls++
	delay := f.statsDelay
	entered, release := f.entered, f.release
	f.mu.Unlock()

	defer f.enter()()
	if entered != nil && release != nil {
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
		}
	}
	wait(ctx, delay)
	return resp, nil
}

func (f *fakeLogReader) GetHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.HistogramResult, error) {
	f.mu.Lock()
	f.sawContext = ctx
	f.histogramBucket = bucketSizeSeconds
	result := f.histogramResult
	delay := f.histogramDelay
	f.mu.Unlock()

	wait(ctx, delay)
	if result != nil {
		return result, nil
	}
	return &logstore.HistogramResult{}, nil
}

func (f *fakeLogReader) GetLatencyHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.LatencyHistogramResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sawContext = ctx
	f.histogramBucket = bucketSizeSeconds
	if f.latencyHistogramResult != nil {
		return f.latencyHistogramResult, nil
	}
	return &logstore.LatencyHistogramResult{}, nil
}

func (f *fakeLogReader) GetTokenHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.TokenHistogramResult, error) {
	f.mu.Lock()
	f.sawContext = ctx
	f.histogramBucket = bucketSizeSeconds
	result := f.tokenHistogramResult
	delay := f.tokenHistogramDelay
	f.mu.Unlock()

	wait(ctx, delay)
	if result != nil {
		return result, nil
	}
	return &logstore.TokenHistogramResult{}, nil
}

func (f *fakeLogReader) GetCostHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.CostHistogramResult, error) {
	f.mu.Lock()
	f.sawContext = ctx
	f.histogramBucket = bucketSizeSeconds
	result := f.costHistogramResult
	delay := f.costHistogramDelay
	f.mu.Unlock()

	wait(ctx, delay)
	if result != nil {
		return result, nil
	}
	return &logstore.CostHistogramResult{}, nil
}

func (f *fakeLogReader) GetThroughputHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ThroughputHistogramResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sawContext = ctx
	f.histogramBucket = bucketSizeSeconds
	if f.throughputHistogramResult != nil {
		return f.throughputHistogramResult, nil
	}
	return &logstore.ThroughputHistogramResult{}, nil
}

func (f *fakeLogReader) GetProviderLatencyHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ProviderLatencyHistogramResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sawContext = ctx
	f.histogramBucket = bucketSizeSeconds
	if f.providerLatencyHistogramResult != nil {
		return f.providerLatencyHistogramResult, nil
	}
	return &logstore.ProviderLatencyHistogramResult{}, nil
}

func (f *fakeLogReader) GetProviderTokenHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ProviderTokenHistogramResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sawContext = ctx
	f.histogramBucket = bucketSizeSeconds
	if f.providerTokenHistogramResult != nil {
		return f.providerTokenHistogramResult, nil
	}
	return &logstore.ProviderTokenHistogramResult{}, nil
}

func (f *fakeLogReader) GetProviderCostHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ProviderCostHistogramResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sawContext = ctx
	f.histogramBucket = bucketSizeSeconds
	if f.providerCostHistogramResult != nil {
		return f.providerCostHistogramResult, nil
	}
	return &logstore.ProviderCostHistogramResult{}, nil
}

func (f *fakeLogReader) GetProviderThroughputHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ProviderThroughputHistogramResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sawContext = ctx
	f.histogramBucket = bucketSizeSeconds
	if f.providerThroughputHistogramResult != nil {
		return f.providerThroughputHistogramResult, nil
	}
	return &logstore.ProviderThroughputHistogramResult{}, nil
}

func (f *fakeLogReader) GetAvailableTeams(_ context.Context, _ int, query string) ([]KeyPair, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.teamsQuerySeen = query
	return f.availableTeams, nil
}

func (f *fakeLogReader) GetAvailableCustomers(_ context.Context, _ int, query string) ([]KeyPair, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.customersQuerySeen = query
	return f.availableCustomers, nil
}

func (f *fakeLogReader) GetAvailableBusinessUnits(_ context.Context, _ int, query string) ([]KeyPair, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.businessUnitsQuerySeen = query
	return f.availableBusinessUnits, nil
}

func (f *fakeLogReader) GetAvailableVirtualKeys(_ context.Context, _ int, query string) ([]KeyPair, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.virtualKeysQuerySeen = query
	return f.availableVirtualKeys, nil
}

func (f *fakeLogReader) GetAvailableModels(_ context.Context, _ int, _ string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.availableModels, nil
}

func (f *fakeLogReader) GetAvailableApps(_ context.Context, _ int, _ string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.availableApps, nil
}

func (f *fakeLogReader) GetAvailableStopReasons(_ context.Context, _ int, _ string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.availableStopReasons, nil
}

// runTool executes one tool directly against deps, the way toolHandler does
// minus the MCP envelope. ctx carries whatever default scope the test opted
// in to via WithDefaultUserScope.
func runTool(t *testing.T, name string, deps *Deps, args map[string]any) (any, error) {
	t.Helper()
	return runToolCtx(t, context.Background(), name, deps, args)
}

func runToolCtx(t *testing.T, ctx context.Context, name string, deps *Deps, args map[string]any) (any, error) {
	t.Helper()
	tool, ok := toolByName(buildTools(), name)
	require.True(t, ok, "tool %s should exist", name)
	return tool.execute(ctx, deps, args)
}

// runToolViaHandler calls a tool the way the MCP server does, through
// toolHandler, so server-level refusals such as the missing log store apply.
func runToolViaHandler(t *testing.T, name string, deps *Deps, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	tool, ok := toolByName(buildTools(), name)
	require.True(t, ok, "tool %s should exist", name)
	request := mcp.CallToolRequest{}
	request.Params.Name = name
	request.Params.Arguments = args
	result, err := toolHandler(StaticDeps(deps), *tool)(context.Background(), request)
	require.NoError(t, err)
	return result
}

// runToolViaHandlerCtx is runToolViaHandler with the caller's context, for the
// server-level checks that read it, such as write access.
func runToolViaHandlerCtx(t *testing.T, ctx context.Context, name string, deps *Deps, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	tool, ok := toolByName(buildTools(), name)
	require.True(t, ok, "tool %s should exist", name)
	request := mcp.CallToolRequest{}
	request.Params.Name = name
	request.Params.Arguments = args
	result, err := toolHandler(StaticDeps(deps), *tool)(ctx, request)
	require.NoError(t, err)
	return result
}

// toolByName looks up a tool by the name a caller used.
func toolByName(tools []Tool, name string) (*Tool, bool) {
	for i := range tools {
		if tools[i].name == name {
			return &tools[i], true
		}
	}
	return nil, false
}

// scoped returns a context opted in to userID's default scope.
func scoped(userID string) context.Context {
	return WithDefaultUserScope(context.Background(), userID)
}

// Every declared schema must parse into the provider-facing type. A typo here
// would otherwise surface as a model rejecting the whole request at runtime,
// which is a far more expensive place to find it.
func TestToolSchemasAreValid(t *testing.T) {
	tools := buildTools()
	require.NotEmpty(t, tools)

	for _, tool := range tools {
		require.NotEmpty(t, tool.name)
		require.NotEmpty(t, tool.description, "%s needs a description; it is the only thing telling the model when to use it", tool.name)
		var parameters schemas.ToolFunctionParameters
		require.NoError(t, sonic.UnmarshalString(tool.schemaJSON, &parameters), "%s schema must parse", tool.name)
		require.Equal(t, "object", parameters.Type, tool.name)
	}
}

// The server declares exactly the tools buildTools builds, under the same
// names, with the same schemas - what an external MCP client sees via
// tools/list is the same document the model behind Warp sees.
func TestServerDeclaresEveryTool(t *testing.T) {
	srv := NewServer(StaticDeps(&Deps{}))
	client, err := mcpclient.NewInProcessClient(srv)
	require.NoError(t, err)
	defer client.Close()
	ctx := context.Background()
	require.NoError(t, client.Start(ctx))
	_, err = client.Initialize(ctx, mcp.InitializeRequest{Params: mcp.InitializeParams{
		ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
		ClientInfo:      mcp.Implementation{Name: "test", Version: "0"},
	}})
	require.NoError(t, err)

	listed, err := client.ListTools(ctx, mcp.ListToolsRequest{})
	require.NoError(t, err)
	names := make([]string, 0, len(listed.Tools))
	byName := map[string]Tool{}
	for _, tool := range buildTools() {
		byName[tool.name] = tool
	}
	for _, tool := range listed.Tools {
		names = append(names, tool.Name)
		declared, ok := byName[tool.Name]
		require.True(t, ok, tool.Name)
		require.NotNil(t, tool.Annotations.ReadOnlyHint, tool.Name)
		require.Equal(t, !declared.mutating, *tool.Annotations.ReadOnlyHint, tool.Name)
		// The admin-only notice leads every write tool's description, so a
		// model knows before calling that a virtual key will be refused.
		require.Equal(t, declared.mutating, strings.HasPrefix(tool.Description, writeToolDescriptionPrefix), tool.Name)
	}
	want := make([]string, 0, len(buildTools()))
	for _, tool := range buildTools() {
		want = append(want, tool.name)
	}
	require.ElementsMatch(t, want, names)
}

// The cap protects the context window, so it has to hold regardless of what the
// model asks for.
func TestQueryLogsClampsLimit(t *testing.T) {
	fake := &fakeLogReader{}
	deps := &Deps{LogManager: fake}

	_, err := runTool(t, "query_logs", deps, map[string]any{
		"filters": map[string]any{},
		"limit":   float64(5000),
	})
	require.NoError(t, err)
	require.Equal(t, MaxLogRows, fake.searchPagination.Limit)
}

func TestRankingClampsLimit(t *testing.T) {
	fake := &fakeLogReader{}
	deps := &Deps{LogManager: fake}

	_, err := runTool(t, "query_usage_by", deps, map[string]any{
		"dimension": "virtual_key",
		"filters":   map[string]any{},
		"limit":     float64(9999),
	})
	require.NoError(t, err)
	require.NotNil(t, fake.rankingFilters.RankingLimit)
	require.Equal(t, MaxRankingRows, *fake.rankingFilters.RankingLimit)
	require.Equal(t, logstore.RankingDimensionVirtualKey, fake.rankingDimension)
}

func TestUsageByFlowUsesRequestedDimension(t *testing.T) {
	for _, dim := range []logstore.RankingDimension{
		logstore.RankingDimensionUser, logstore.RankingDimensionVirtualKey, logstore.RankingDimensionTeam,
		logstore.RankingDimensionCustomer, logstore.RankingDimensionBusinessUnit, logstore.RankingDimensionProject,
		logstore.RankingDimensionApp, logstore.RankingDimensionUserAgent,
	} {
		t.Run(string(dim), func(t *testing.T) {
			fake := &fakeLogReader{}
			_, err := runTool(t, "query_usage_by", &Deps{LogManager: fake}, map[string]any{
				"dimension": string(dim),
				"filters":   map[string]any{},
			})
			require.NoError(t, err)
			require.Equal(t, dim, fake.rankingDimension)
		})
	}
}

func TestUsageByRejectsUnknownDimension(t *testing.T) {
	fake := &fakeLogReader{}
	_, err := runTool(t, "query_usage_by", &Deps{LogManager: fake}, map[string]any{
		"dimension": "region",
		"filters":   map[string]any{},
	})
	require.ErrorContains(t, err, `unknown dimension "region"`)
}

// A dropped filter answers a different question than the one asked, and neither
// the model nor the reader can tell. Rejecting is the only safe behaviour.
func TestRejectsUnknownFilterField(t *testing.T) {
	fake := &fakeLogReader{}
	_, err := runTool(t, "query_logs", &Deps{LogManager: fake}, map[string]any{
		"filters": map[string]any{"provider": "openai"}, // singular; the real field is "providers"
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown filter fields: provider")
	require.Nil(t, fake.searchFilters, "the query must not run with a silently dropped filter")
}

// Every field the FilterSchema declares must actually reach SearchFilters, or
// the model is offered a knob that silently does nothing.
func TestFilterAcceptsPreviouslyMissingFields(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	filters, err := parseFilters(map[string]any{
		"stop_reasons":    []any{"length", "content_filter"},
		"objects":         []any{"embedding"},
		"project_ids":     []any{"proj-1"},
		"min_tokens":      float64(10),
		"max_tokens":      float64(5000),
		"cache_hit_types": []any{"semantic"},
	}, now)
	require.NoError(t, err)
	require.Equal(t, []string{"length", "content_filter"}, filters.StopReasons)
	require.Equal(t, []string{"embedding"}, filters.Objects)
	require.Equal(t, []string{"proj-1"}, filters.ProjectIDs)
	require.NotNil(t, filters.MinTokens)
	require.Equal(t, 10, *filters.MinTokens)
	require.NotNil(t, filters.MaxTokens)
	require.Equal(t, 5000, *filters.MaxTokens)
	require.Equal(t, []string{"semantic"}, filters.CacheHitTypes)
}

// A fractional or out-of-range token bound must be rejected rather than
// silently truncated into a threshold the caller never asked for.
func TestFilterRejectsInvalidTokenBounds(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

	t.Run("fractional min_tokens", func(t *testing.T) {
		_, err := parseFilters(map[string]any{"min_tokens": float64(10.5)}, now)
		require.ErrorContains(t, err, "min_tokens must be an integer")
	})

	t.Run("fractional max_tokens", func(t *testing.T) {
		_, err := parseFilters(map[string]any{"max_tokens": float64(5000.25)}, now)
		require.ErrorContains(t, err, "max_tokens must be an integer")
	})

	t.Run("out of platform int range", func(t *testing.T) {
		_, err := parseFilters(map[string]any{"max_tokens": math.MaxFloat64}, now)
		require.ErrorContains(t, err, "max_tokens is out of range")
	})

	// float64(-math.MinInt) is 2^63 - the first magnitude float64 cannot
	// distinguish from math.MaxInt (2^63-1), since 2^63-1 is not exactly
	// representable and rounds up to it. A `>` check against float64(math.MaxInt)
	// would let this exact value through and then convert it with int(),
	// which the Go spec leaves implementation-defined for a value int64
	// cannot hold - so this must be rejected, on both bounds.
	t.Run("min_tokens at the first float64 value int cannot represent", func(t *testing.T) {
		_, err := parseFilters(map[string]any{"min_tokens": float64(-math.MinInt)}, now)
		require.ErrorContains(t, err, "min_tokens is out of range")
	})

	t.Run("max_tokens at the first float64 value int cannot represent", func(t *testing.T) {
		_, err := parseFilters(map[string]any{"max_tokens": float64(-math.MinInt)}, now)
		require.ErrorContains(t, err, "max_tokens is out of range")
	})
}

// describe_filter_space already surfaces stop_reasons as a discoverable
// value; filtering on one must not then be rejected as unknown.
func TestStopReasonsFilterIsNotRejectedAsUnknown(t *testing.T) {
	fake := &fakeLogReader{}
	_, err := runTool(t, "query_logs", &Deps{LogManager: fake}, map[string]any{
		"filters": map[string]any{"stop_reasons": []any{"length"}},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"length"}, fake.searchFilters.StopReasons)
}

// A question naming a project is naming a scope, same as team, customer or
// business unit - it must not be silently widened to the caller's own traffic.
func TestProjectFilterCountsAsANamedScope(t *testing.T) {
	filters := &logstore.SearchFilters{ProjectIDs: []string{"proj-1"}}
	applyScope(filters, Scope{HasIdentity: true, UserID: "user-7"}, false)
	require.Empty(t, filters.UserIDs, "naming a project must not also narrow to the caller")
}

func TestFilterTimeParsing(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

	t.Run("relative days", func(t *testing.T) {
		filters, err := parseFilters(map[string]any{"start_time": "-7d"}, now)
		require.NoError(t, err)
		require.Equal(t, now.Add(-7*24*time.Hour), *filters.StartTime)
		require.Equal(t, now, *filters.EndTime)
	})

	t.Run("relative hours", func(t *testing.T) {
		filters, err := parseFilters(map[string]any{"start_time": "-30m"}, now)
		require.NoError(t, err)
		require.Equal(t, now.Add(-30*time.Minute), *filters.StartTime)
	})

	t.Run("absolute rfc3339", func(t *testing.T) {
		filters, err := parseFilters(map[string]any{"start_time": "2026-08-01T00:00:00Z"}, now)
		require.NoError(t, err)
		require.Equal(t, 2026, filters.StartTime.Year())
		require.Equal(t, time.August, filters.StartTime.Month())
	})

	t.Run("defaults to last 24h", func(t *testing.T) {
		filters, err := parseFilters(nil, now)
		require.NoError(t, err)
		require.Equal(t, now.Add(-DefaultLookback), *filters.StartTime)
	})

	t.Run("rejects inverted range", func(t *testing.T) {
		_, err := parseFilters(map[string]any{
			"start_time": "2026-08-10T00:00:00Z",
			"end_time":   "2026-08-01T00:00:00Z",
		}, now)
		require.ErrorContains(t, err, "start_time must be before end_time")
	})

	t.Run("rejects unparseable offset", func(t *testing.T) {
		_, err := parseFilters(map[string]any{"start_time": "last tuesday"}, now)
		require.ErrorContains(t, err, "start_time")
	})
}

// An oversized result is replaced, never truncated: a tail-truncated JSON
// document reads as complete to the model, which then answers from a fragment
// without hedging.
func TestBoundToolResultReplacesRatherThanTruncates(t *testing.T) {
	huge := make([]string, 4000)
	for i := range huge {
		huge[i] = fmt.Sprintf("row-%d-with-some-padding-to-make-this-large", i)
	}
	bounded := boundToolResult(map[string]any{"rows": huge})

	require.Contains(t, bounded, "result too large")
	require.Contains(t, bounded, `"truncated":true`)
	require.NotContains(t, bounded, "row-3999", "the payload must be dropped, not tail-truncated")
	require.Less(t, len(bounded), MaxToolResultBytes)
}

func TestBoundToolResultPassesSmallPayloads(t *testing.T) {
	bounded := boundToolResult(map[string]any{"total": 42})
	require.Contains(t, bounded, `"total":42`)
	require.NotContains(t, bounded, "result too large")
}

// ContentHidden is a promise the deployment made about that request's payload.
// Warp is an API like any other and must not be the place it resurfaces.
func TestNeverReturnsHiddenContent(t *testing.T) {
	entry := &logstore.Log{
		ID:             "hidden-row",
		Timestamp:      time.Now().UTC(),
		Provider:       "openai",
		Model:          "gpt-4o",
		Status:         "success",
		ContentHidden:  true,
		ContentSummary: "a secret the operator asked us not to store",
	}
	row := ProjectLog(entry, true, DetailContentChars)
	require.Empty(t, row.Content)
	require.Equal(t, "hidden-row", row.ID)
}

func TestIncludesContentOnlyWhenAsked(t *testing.T) {
	entry := &logstore.Log{
		ID:             "visible-row",
		Timestamp:      time.Now().UTC(),
		Provider:       "openai",
		Model:          "gpt-4o",
		Status:         "success",
		ContentSummary: "what is the weather",
	}
	require.Empty(t, ProjectLog(entry, false, LogContentChars).Content)
	require.Equal(t, "what is the weather", ProjectLog(entry, true, LogContentChars).Content)
}

func TestTruncatesLongContent(t *testing.T) {
	entry := &logstore.Log{
		ID:             "long-row",
		Timestamp:      time.Now().UTC(),
		ContentSummary: strings.Repeat("x", LogContentChars*3),
	}
	row := ProjectLog(entry, true, LogContentChars)
	require.Contains(t, row.Content, "[truncated]")
	require.Less(t, len(row.Content), LogContentChars*2)
}

// Token counts come from the denormalized columns, which survive object-storage
// offload and content-hidden rows. Reading them from the token_usage payload
// would report zero for exactly those rows.
func TestUsesDenormalizedTokenColumns(t *testing.T) {
	entry := &logstore.Log{
		ID:               "tokens",
		Timestamp:        time.Now().UTC(),
		ContentHidden:    true,
		PromptTokens:     120,
		CompletionTokens: 45,
	}
	row := ProjectLog(entry, false, LogContentChars)
	require.Equal(t, 120, row.InputTokens)
	require.Equal(t, 45, row.OutputTokens)
}

func TestQueryLogsReportsTotalSeparately(t *testing.T) {
	fake := &fakeLogReader{searchResult: &logstore.SearchResult{
		Logs:       []logstore.Log{{ID: "a", Timestamp: time.Now().UTC()}, {ID: "b", Timestamp: time.Now().UTC()}},
		Pagination: logstore.PaginationOptions{TotalCount: 12400},
	}}
	result, err := runTool(t, "query_logs", &Deps{LogManager: fake}, map[string]any{
		"filters": map[string]any{},
	})
	require.NoError(t, err)

	payload := result.(map[string]any)
	require.Equal(t, 2, payload["returned"])
	require.Equal(t, int64(12400), payload["total_matching"])
}

func TestMetricsRequiresAtLeastOneMetric(t *testing.T) {
	_, err := runTool(t, "query_metrics", &Deps{LogManager: &fakeLogReader{}}, map[string]any{
		"filters": map[string]any{},
		"metrics": []any{},
	})
	require.ErrorContains(t, err, "metrics must list at least one")
}

func TestMetricsRejectsUnknownMetric(t *testing.T) {
	_, err := runTool(t, "query_metrics", &Deps{LogManager: &fakeLogReader{}}, map[string]any{
		"filters": map[string]any{},
		"metrics": []any{"vibes"},
	})
	require.ErrorContains(t, err, "unknown metric")
}

func TestMetricsSummaryUsesStats(t *testing.T) {
	fake := &fakeLogReader{}
	_, err := runTool(t, "query_metrics", &Deps{LogManager: fake}, map[string]any{
		"filters": map[string]any{},
		"metrics": []any{"summary"},
	})
	require.NoError(t, err)
	require.True(t, fake.statsCalled)
}

// compare_to_previous exists so "is it up or down vs last period" costs one
// call instead of two - the model calling query_metrics itself with a shifted
// window. Prove the tool does that shifted call internally and returns a
// trend rather than requiring the caller to.
func TestMetricsComparesToPreviousPeriod(t *testing.T) {
	fake := &fakeLogReader{statsResponses: []*logstore.SearchStats{
		{TotalRequests: 200, TotalTokens: 20000, TotalCost: 40}, // current period
		{TotalRequests: 100, TotalTokens: 10000, TotalCost: 20}, // previous period
	}}
	result, err := runTool(t, "query_metrics", &Deps{LogManager: fake}, map[string]any{
		"filters":             map[string]any{"start_time": "-7d"},
		"metrics":             []any{"summary"},
		"compare_to_previous": true,
	})
	require.NoError(t, err)
	require.Equal(t, 2, fake.statsCalls, "one call for the window asked about, one for the period before it")

	out := result.(map[string]any)
	trend := out["previous_period"].(map[string]any)
	require.Equal(t, true, trend["has_previous_period"])
	require.InDelta(t, 100.0, trend["requests_trend"], 0.001) // 100 -> 200 requests
	require.InDelta(t, 100.0, trend["tokens_trend"], 0.001)
	require.InDelta(t, 100.0, trend["cost_trend"], 0.001)

	// The previous-period query must be shifted, not a repeat of the same window.
	require.Len(t, fake.statsFiltersSeen, 2)
	current, previous := fake.statsFiltersSeen[0], fake.statsFiltersSeen[1]
	span := current.EndTime.Sub(*current.StartTime)
	require.True(t, previous.StartTime.Equal(current.StartTime.Add(-span)), "previous period must be the same length")
	// GetStats bounds are inclusive at both ends, so a previous period ending
	// exactly at the current start would count a log stamped at that instant
	// in both periods.
	require.True(t, previous.EndTime.Before(*current.StartTime), "previous period must not share the boundary instant")
	require.True(t, previous.EndTime.Equal(current.StartTime.Add(-time.Microsecond)), "and must stop one stored tick short of it")
	require.Equal(t, current.StartTime.UTC().Format(time.RFC3339Nano), trend["window"].(map[string]string)["end"], "the reported window still ends at the current start")
}

// A previous period with requests but no tokens or cost gives the current
// period nothing to be a percentage of. Each metric is judged on its own
// baseline: the zero ones report null rather than a 0% that reads as
// "unchanged", while requests - and has_previous_period - still compare.
func TestMetricsCompareToPreviousZeroBaseline(t *testing.T) {
	fake := &fakeLogReader{statsResponses: []*logstore.SearchStats{
		{TotalRequests: 200, TotalTokens: 20000, TotalCost: 40}, // current period
		{TotalRequests: 100, TotalTokens: 0, TotalCost: 0},      // previous period
	}}
	result, err := runTool(t, "query_metrics", &Deps{LogManager: fake}, map[string]any{
		"filters":             map[string]any{"start_time": "-7d"},
		"metrics":             []any{"summary"},
		"compare_to_previous": true,
	})
	require.NoError(t, err)

	trend := result.(map[string]any)["previous_period"].(map[string]any)
	require.Equal(t, true, trend["has_previous_period"])
	require.InDelta(t, 100.0, trend["requests_trend"], 0.001)
	require.Contains(t, trend, "tokens_trend")
	require.Nil(t, trend["tokens_trend"], "zero-to-nonzero tokens must not read as 0%")
	require.Contains(t, trend, "cost_trend")
	require.Nil(t, trend["cost_trend"], "zero-to-nonzero cost must not read as 0%")
}

func TestMetricsCompareToPreviousRequiresSummary(t *testing.T) {
	_, err := runTool(t, "query_metrics", &Deps{LogManager: &fakeLogReader{}}, map[string]any{
		"filters":             map[string]any{},
		"metrics":             []any{"cost"},
		"compare_to_previous": true,
	})
	require.ErrorContains(t, err, `compare_to_previous requires metrics to include "summary"`)
}

// A previous period with no traffic at all is a real answer ("nothing to
// compare against"), not a divide-by-zero.
func TestMetricsCompareToPreviousHandlesEmptyPreviousPeriod(t *testing.T) {
	fake := &fakeLogReader{statsResponses: []*logstore.SearchStats{
		{TotalRequests: 50},
		{TotalRequests: 0},
	}}
	result, err := runTool(t, "query_metrics", &Deps{LogManager: fake}, map[string]any{
		"filters":             map[string]any{},
		"metrics":             []any{"summary"},
		"compare_to_previous": true,
	})
	require.NoError(t, err)
	trend := result.(map[string]any)["previous_period"].(map[string]any)
	require.Equal(t, false, trend["has_previous_period"])
	require.Equal(t, 0.0, trend["requests_trend"])
}

// An empty previous period is still a zero baseline for tokens and cost, so
// nonzero current values must report null there too - not the 0% that reads as
// "unchanged" just because the previous period had no requests.
func TestWarpMetricsCompareToPreviousEmptyPeriodNullsTokensAndCost(t *testing.T) {
	fake := &fakeLogReader{statsResponses: []*logstore.SearchStats{
		{TotalRequests: 50, TotalTokens: 5000, TotalCost: 10}, // current period
		{TotalRequests: 0, TotalTokens: 0, TotalCost: 0},      // previous period
	}}
	result, err := runTool(t, "query_metrics", &Deps{LogManager: fake}, map[string]any{
		"filters":             map[string]any{"start_time": "-7d"},
		"metrics":             []any{"summary"},
		"compare_to_previous": true,
	})
	require.NoError(t, err)

	trend := result.(map[string]any)["previous_period"].(map[string]any)
	require.Equal(t, false, trend["has_previous_period"])
	require.Equal(t, 0.0, trend["requests_trend"])
	require.Contains(t, trend, "tokens_trend")
	require.Nil(t, trend["tokens_trend"], "zero-to-nonzero tokens must not read as 0%")
	require.Contains(t, trend, "cost_trend")
	require.Nil(t, trend["cost_trend"], "zero-to-nonzero cost must not read as 0%")
}

// An over-long window must be rejected with advice rather than silently
// returning thousands of buckets the model cannot tell were excessive.
//
// Where that ceiling actually sits is not obvious: DefaultBucketSize widens the
// bucket as the span grows, so every band below a year is self-limiting (a 47h
// window is 47 hourly buckets, nowhere near the cap). Only the top band is flat
// - once the span passes a year the bucket stops growing at 30 days - so
// MaxHistogramBuckets is first exceeded somewhere past 16 years. The span below
// is deliberately on the far side of that; a shorter one would make this test
// pass without ever reaching the check it names.
func TestMetricsRejectsTooManyBuckets(t *testing.T) {
	// Pinned: relative offsets resolve against Now, and a real clock would drift
	// the window this test depends on.
	previous := Now
	Now = func() time.Time { return time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC) }
	defer func() { Now = previous }()

	// ~26 years at 30-day buckets is ~324 buckets, over the 200 ceiling.
	_, err := runTool(t, "query_metrics", &Deps{LogManager: &fakeLogReader{}}, map[string]any{
		"filters": map[string]any{"start_time": "2000-01-01T00:00:00Z", "end_time": "2026-08-17T00:00:00Z"},
		"metrics": []any{"cost"},
	})
	// Unconditionally: a nil error here means the ceiling stopped working, which
	// is precisely the regression this test exists to catch. Guarding the
	// assertion behind `if err != nil` made it pass in exactly that case.
	require.Error(t, err)
	require.ErrorContains(t, err, "buckets")

	// The companion half: a range the adaptive bucket handles must still be
	// accepted, so the ceiling cannot be "fixed" by rejecting everything.
	_, err = runTool(t, "query_metrics", &Deps{LogManager: &fakeLogReader{}}, map[string]any{
		"filters": map[string]any{"start_time": "-47h", "end_time": "2026-08-17T00:00:00Z"},
		"metrics": []any{"cost"},
	})
	require.NoError(t, err, "a 47h window is 47 hourly buckets and must be accepted")

	// The ceiling is about buckets, so it must not reach a request that builds
	// none: a summary over an over-long range is one aggregate query.
	fake := &fakeLogReader{}
	_, err = runTool(t, "query_metrics", &Deps{LogManager: fake}, map[string]any{
		"filters": map[string]any{"start_time": "2000-01-01T00:00:00Z", "end_time": "2026-08-17T00:00:00Z"},
		"metrics": []any{"summary"},
	})
	require.NoError(t, err, "a summary-only request builds no buckets and must not hit the bucket ceiling")
	require.True(t, fake.statsCalled)
}

// The store pads idle time slots with zero-valued buckets. Those are gaps, not
// requests that took 0ms: fed into the latency series they dragged min to 0,
// reported first/last as 0 whenever the window started or ended quiet, and
// diluted the percentile means. Request counts are additive, so they keep the
// gaps.
func TestWarpLatencySummarySkipsEmptyBuckets(t *testing.T) {
	summary := summarizeLatencyHistogram(&logstore.LatencyHistogramResult{BucketSizeSeconds: 600, Buckets: []logstore.LatencyHistogramBucket{
		{},
		{AvgLatency: 100, P90Latency: 200, P95Latency: 300, P99Latency: 400, AvgOverhead: 10, P90Overhead: 20, P95Overhead: 30, P99Overhead: 40, TotalRequests: 1},
		{},
		{AvgLatency: 300, P90Latency: 400, P95Latency: 500, P99Latency: 600, AvgOverhead: 30, P90Overhead: 40, P95Overhead: 50, P99Overhead: 60, TotalRequests: 3},
		{},
	}})
	require.Equal(t, seriesSummary{Mean: 250, Min: 100, Max: 300, First: 100, Last: 300}, summary["avg_latency"])
	require.Equal(t, seriesSummary{Mean: 300, Min: 200, Max: 400, First: 200, Last: 400}, summary["p90_latency"])
	require.Equal(t, seriesSummary{Mean: 500, Min: 400, Max: 600, First: 400, Last: 600}, summary["p99_latency"])
	require.Equal(t, seriesSummary{Mean: 25, Min: 10, Max: 30, First: 10, Last: 30}, summary["avg_overhead"])
	require.Equal(t, seriesSummary{Mean: 40, Min: 30, Max: 50, First: 30, Last: 50}, summary["p95_overhead"])
	total := 4.0
	require.Equal(t, seriesSummary{Total: &total, Mean: 0.8, Min: 0, Max: 3, First: 0, Last: 0}, summary["total_requests"], "idle buckets are real zero-request counts")
	require.Equal(t, 5, summary["buckets"])
}

// Same gap-padding as latency: an idle bucket's 0 tokens/sec is no traffic, not
// a stalled stream, so it must not drag the throughput min/mean/first/last to
// 0. Token and request counts are additive and keep every bucket.
func TestWarpThroughputSummarySkipsEmptyBuckets(t *testing.T) {
	summary := summarizeThroughputHistogram(&logstore.ThroughputHistogramResult{BucketSizeSeconds: 600, Buckets: []logstore.ThroughputHistogramBucket{
		{},
		{TokensPerSecond: 40, TotalCompletionTokens: 400, TotalRequests: 1},
		{},
		{TokensPerSecond: 80, TotalCompletionTokens: 800, TotalRequests: 3},
		{},
	}})
	require.Equal(t, seriesSummary{Mean: 60, Min: 40, Max: 80, First: 40, Last: 80}, summary["tokens_per_second"])
	tokens, requests := 1200.0, 4.0
	require.Equal(t, seriesSummary{Total: &tokens, Mean: 240, Min: 0, Max: 800, First: 0, Last: 0}, summary["total_completion_tokens"], "idle buckets are real zero-token counts")
	require.Equal(t, seriesSummary{Total: &requests, Mean: 0.8, Min: 0, Max: 3, First: 0, Last: 0}, summary["total_requests"], "idle buckets are real zero-request counts")
	require.Equal(t, 5, summary["buckets"])
}

// The concrete failure this fixes: query_metrics's own description promised a
// summary, but every series was returned bucket by bucket. A 12-hour latency
// series (72 buckets at the 10-minute size that window gets, 9 numeric fields
// each) serializes to about 18KB - over MaxToolResultBytes - so the result was
// discarded and the model retried, on exactly the query the tool is supposed
// to make cheap.
func TestMetricsLatencySummaryStaysUnderBudgetFor12HourWindow(t *testing.T) {
	const bucketCount = 72 // 12h at the 10-minute bucket size that window resolves to
	buckets := make([]logstore.LatencyHistogramBucket, bucketCount)
	for i := range buckets {
		buckets[i] = logstore.LatencyHistogramBucket{
			Timestamp: time.Now(), AvgLatency: 234.567, P90Latency: 450.123, P95Latency: 600.345, P99Latency: 890.123,
			AvgOverhead: 12.345, P90Overhead: 23.456, P95Overhead: 34.567, P99Overhead: 45.678, TotalRequests: 142,
		}
	}
	fake := &fakeLogReader{latencyHistogramResult: &logstore.LatencyHistogramResult{Buckets: buckets, BucketSizeSeconds: 600}}

	result, err := runTool(t, "query_metrics", &Deps{LogManager: fake}, map[string]any{
		"filters": map[string]any{"start_time": "-12h"},
		"metrics": []any{"latency"},
	})
	require.NoError(t, err)

	// bound is what the agent loop actually applies before a result reaches the
	// model (see boundToolResult); the fix is only real if it fits under that.
	bound := boundToolResult(result)
	require.Less(t, len(bound), MaxToolResultBytes)
	require.NotContains(t, bound, "result too large", "the old bucket-by-bucket payload would have been discarded here")

	latency, ok := result.(map[string]any)["latency"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, bucketCount, latency["buckets"], "the bucket count travels with the summary even though the buckets themselves do not")
	avg, ok := latency["avg_latency"].(seriesSummary)
	require.True(t, ok)
	require.InDelta(t, 234.567, avg.Mean, 0.001)
	require.Nil(t, avg.Total, "a percentile-style field must not carry a meaningless sum")
}

// requests, tokens and total_requests are additive - a total is a real number
// for them - which the fields above are not. Small, hand-checkable series so
// the reduction math itself is verified, not just that it runs.
func TestMetricsSummarizesRequestsHistogram(t *testing.T) {
	fake := &fakeLogReader{histogramResult: &logstore.HistogramResult{
		Buckets: []logstore.HistogramBucket{
			{Count: 10, Success: 9, Error: 1},
			{Count: 20, Success: 18, Error: 2},
			{Count: 30, Success: 27, Error: 3},
		},
		BucketSizeSeconds: 600,
	}}
	result, err := runTool(t, "query_metrics", &Deps{LogManager: fake}, map[string]any{
		"filters": map[string]any{}, "metrics": []any{"requests"},
	})
	require.NoError(t, err)

	requests := result.(map[string]any)["requests"].(map[string]any)
	count := requests["count"].(seriesSummary)
	require.NotNil(t, count.Total)
	require.InDelta(t, 60, *count.Total, 0.001)
	require.InDelta(t, 20, count.Mean, 0.001)
	require.InDelta(t, 10, count.Min, 0.001)
	require.InDelta(t, 30, count.Max, 0.001)
	require.InDelta(t, 10, count.First, 0.001)
	require.InDelta(t, 30, count.Last, 0.001)
}

func TestMetricsSummarizesTokensHistogram(t *testing.T) {
	fake := &fakeLogReader{tokenHistogramResult: &logstore.TokenHistogramResult{
		Buckets: []logstore.TokenHistogramBucket{
			{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150},
			{PromptTokens: 200, CompletionTokens: 100, TotalTokens: 300},
		},
	}}
	result, err := runTool(t, "query_metrics", &Deps{LogManager: fake}, map[string]any{
		"filters": map[string]any{}, "metrics": []any{"tokens"},
	})
	require.NoError(t, err)

	tokens := result.(map[string]any)["tokens"].(map[string]any)
	total := tokens["total_tokens"].(seriesSummary)
	require.NotNil(t, total.Total)
	require.InDelta(t, 450, *total.Total, 0.001)
}

// The per-bucket by_model breakdown is dropped rather than summarized - a
// per-model series-of-series is exactly the nested detail this exists to
// avoid - but the top-level model list, already cheap, must survive.
func TestMetricsSummarizesCostHistogramKeepsModelList(t *testing.T) {
	fake := &fakeLogReader{costHistogramResult: &logstore.CostHistogramResult{
		Buckets: []logstore.CostHistogramBucket{
			{TotalCost: 1.5, ByModel: map[string]float64{"gpt-4o": 1.5}},
			{TotalCost: 2.5, ByModel: map[string]float64{"gpt-4o": 2.5}},
		},
		Models: []string{"gpt-4o"},
	}}
	result, err := runTool(t, "query_metrics", &Deps{LogManager: fake}, map[string]any{
		"filters": map[string]any{}, "metrics": []any{"cost"},
	})
	require.NoError(t, err)

	cost := result.(map[string]any)["cost"].(map[string]any)
	total := cost["total_cost"].(seriesSummary)
	require.InDelta(t, 4.0, *total.Total, 0.001)
	require.Equal(t, []string{"gpt-4o"}, cost["models"])
	require.NotContains(t, cost, "by_model", "a per-bucket, per-model series is the exact nested detail a summary must not reintroduce")
}

func TestMetricsSummarizesThroughputHistogram(t *testing.T) {
	fake := &fakeLogReader{throughputHistogramResult: &logstore.ThroughputHistogramResult{
		Buckets: []logstore.ThroughputHistogramBucket{
			{TokensPerSecond: 10, TotalCompletionTokens: 100, TotalRequests: 5},
			{TokensPerSecond: 20, TotalCompletionTokens: 200, TotalRequests: 10},
		},
	}}
	result, err := runTool(t, "query_metrics", &Deps{LogManager: fake}, map[string]any{
		"filters": map[string]any{}, "metrics": []any{"throughput"},
	})
	require.NoError(t, err)

	throughput := result.(map[string]any)["throughput"].(map[string]any)
	tps := throughput["tokens_per_second"].(seriesSummary)
	require.Nil(t, tps.Total, "a rate is not additive across buckets")
	require.InDelta(t, 15, tps.Mean, 0.001)
	totalRequests := throughput["total_requests"].(seriesSummary)
	require.NotNil(t, totalRequests.Total)
	require.InDelta(t, 15, *totalRequests.Total, 0.001)
}

// group_by=provider stays as raw buckets rather than a summary - collapsing
// the per-provider split away would defeat the reason to group by it - but it
// still has to use the same coarse bucket size query_model_performance's
// include_performance path already uses, or it inherits the same overflow
// this whole fix exists to close.
func TestMetricsProviderGroupedStaysRawWithCoarseBuckets(t *testing.T) {
	previous := Now
	Now = func() time.Time { return time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC) }
	defer func() { Now = previous }()

	fake := &fakeLogReader{providerLatencyHistogramResult: &logstore.ProviderLatencyHistogramResult{
		Buckets:   []logstore.ProviderLatencyHistogramBucket{{ByProvider: map[string]logstore.ProviderLatencyStats{"openai": {AvgLatency: 100}}}},
		Providers: []string{"openai"},
	}}
	result, err := runTool(t, "query_metrics", &Deps{LogManager: fake}, map[string]any{
		"filters":  map[string]any{"start_time": "-24h"},
		"metrics":  []any{"latency"},
		"group_by": "provider",
	})
	require.NoError(t, err)

	latency, ok := result.(map[string]any)["latency"].(*logstore.ProviderLatencyHistogramResult)
	require.True(t, ok, "the provider-grouped path must return the raw result, not a summary")
	require.Equal(t, []string{"openai"}, latency.Providers)

	coarse, err := coarseBucketSize(&logstore.SearchFilters{StartTime: new(Now().Add(-24 * time.Hour)), EndTime: new(Now())})
	require.NoError(t, err)
	require.Equal(t, coarse, fake.histogramBucket, "group_by=provider must use the coarse bucket size, not the fine one the summarized path can afford")
}

// "requests" has no GetProvider*Histogram counterpart, unlike every other
// metric. Silently falling back to the ungrouped histogram would answer a
// different question than "requests per provider" and look identical to a
// real breakdown, so the combination must be rejected instead.
func TestMetricsRequestsRejectsProviderGrouping(t *testing.T) {
	fake := &fakeLogReader{}
	_, err := runTool(t, "query_metrics", &Deps{LogManager: fake}, map[string]any{
		"filters":  map[string]any{"start_time": "-24h"},
		"metrics":  []any{"requests"},
		"group_by": "provider",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), `"requests"`)
	require.Zero(t, fake.histogramBucket, "the rejected combination must not run any query")
}

// The scope lives on the context. If an executor ever swaps in a fresh context
// the store stops filtering rows and every caller sees the whole deployment.
func TestToolsPassCallerContextToStore(t *testing.T) {
	type scopeKey struct{}
	fake := &fakeLogReader{}
	tool, ok := toolByName(buildTools(), "query_logs")
	require.True(t, ok)

	ctx := context.WithValue(context.Background(), scopeKey{}, "caller-scope")
	_, err := tool.execute(ctx, &Deps{LogManager: fake}, map[string]any{"filters": map[string]any{}})
	require.NoError(t, err)
	require.Equal(t, "caller-scope", fake.sawContext.Value(scopeKey{}),
		"the caller's context must reach the store, or queryscope stops filtering rows")
}

// A row's error fields must be usable to tally "what kinds of errors are
// these" across many rows, so error_type/error_code/status_code need to come
// through structured rather than only inside the free-text message - and
// error_message itself must fall back to a sensible string (via
// BifrostError.GetErrorString) even when the provider's Error field is absent
// and only a status code was recorded.
func TestProjectLogExposesStructuredErrorFields(t *testing.T) {
	entry := &logstore.Log{
		ID: "req-1",
		ErrorDetailsParsed: &schemas.BifrostError{
			StatusCode: new(429),
			Error: &schemas.ErrorField{
				Type:    new("rate_limit_error"),
				Code:    new("rate_limited"),
				Message: "rate limit exceeded",
			},
		},
	}
	row := ProjectLog(entry, false, LogContentChars)
	require.Equal(t, "rate limit exceeded", row.ErrorMessage)
	require.Equal(t, "rate_limit_error", row.ErrorType)
	require.Equal(t, "rate_limited", row.ErrorCode)
	require.Equal(t, 429, row.StatusCode)

	// No Error field at all - GetErrorString falls back to a status-derived
	// string rather than leaving error_message blank.
	statusOnly := &logstore.Log{
		ID:                 "req-2",
		ErrorDetailsParsed: &schemas.BifrostError{StatusCode: new(503)},
	}
	row = ProjectLog(statusOnly, false, LogContentChars)
	require.Equal(t, "service unavailable", row.ErrorMessage)
	require.Empty(t, row.ErrorType)
	require.Equal(t, 503, row.StatusCode)
}

func TestGetLogDetailRequiresID(t *testing.T) {
	_, err := runTool(t, "get_log_detail", &Deps{LogManager: &fakeLogReader{}}, map[string]any{})
	require.ErrorContains(t, err, "log_id is required")
}

// Logged traffic is the least predictable data in the system: a tool-call turn
// carries nil Content, and an offloaded payload leaves the parsed history empty.
// Content is a pointer, so an unguarded read here panics on a real log row.
func TestLogContentHandlesNilMessageContent(t *testing.T) {
	entry := &logstore.Log{
		ID:        "nil-content",
		Timestamp: time.Now().UTC(),
		InputHistoryParsed: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleUser, Content: nil},
			{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: new("hello")}},
		},
		OutputMessageParsed: &schemas.ChatMessage{Role: schemas.ChatMessageRoleAssistant, Content: nil},
	}
	require.NotPanics(t, func() {
		row := ProjectLog(entry, true, LogContentChars)
		require.Contains(t, row.Content, "hello")
	})
}

// Rows carry a link to their own detail sheet and every listing carries a
// link to the same filters in the Logs view, so a reader can open what Warp
// summarised instead of retyping the filters by hand.
func TestListingsCarryDashboardLinks(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	oldNow := Now
	Now = func() time.Time { return now }
	defer func() { Now = oldNow }()
	fake := &fakeLogReader{searchResult: &logstore.SearchResult{
		Logs: []logstore.Log{{ID: "req-9", Timestamp: now.Add(-time.Hour), Provider: "gemini", Model: "gemini-3.1-flash-lite", Status: "success"}},
	}}
	result, err := runTool(t, "query_logs", &Deps{LogManager: fake}, map[string]any{
		"filters": map[string]any{"providers": []any{"gemini"}, "start_time": "-7d"},
	})
	require.NoError(t, err)
	response := result.(map[string]any)
	rows := response["rows"].([]LogRow)
	require.Len(t, rows, 1)
	require.Equal(t, "/workspace/logs?selected_log=req-9", rows[0].Link)
	link, _ := response["logs_link"].(string)
	require.Contains(t, link, "/workspace/logs?")
	require.Contains(t, link, "providers=gemini")

	counted, err := runTool(t, "count_logs", &Deps{LogManager: fake}, map[string]any{"filters": map[string]any{"providers": []any{"gemini"}}})
	require.NoError(t, err)
	require.Contains(t, counted.(map[string]any)["logs_link"], "providers=gemini")
}

// The warp-scope provenance block the prompt requires needs an absolute
// window on every answer with numbers, but only query_metrics used to report
// one - every other flow resolved a window internally (to filter rows) and
// then threw it away, leaving the model to recompute "-7d" as an absolute
// date from the current-time reference by hand. Every flow that resolves a
// window now reports it back, in the same format, so there is nothing left to
// recompute.
func TestToolsReportResolvedWindow(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	oldNow := Now
	Now = func() time.Time { return now }
	defer func() { Now = oldNow }()
	wantWindow := map[string]string{"start": "2026-08-28T12:00:00Z", "end": "2026-09-04T12:00:00Z"} // "-7d"

	cases := []struct {
		tool string
		args map[string]any
	}{
		{"query_logs", map[string]any{"filters": map[string]any{"start_time": "-7d"}}},
		{"count_logs", map[string]any{"filters": map[string]any{"start_time": "-7d"}}},
		{"query_usage_by", map[string]any{"dimension": "user", "filters": map[string]any{"start_time": "-7d"}}},
		{"query_model_performance", map[string]any{"filters": map[string]any{"start_time": "-7d"}}},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			result, err := runTool(t, tc.tool, &Deps{LogManager: &fakeLogReader{}}, tc.args)
			require.NoError(t, err)
			require.Equal(t, wantWindow, result.(map[string]any)["window"], "%s must report the absolute window it resolved -7d to", tc.tool)
		})
	}
}

// describe_filter_space precedes most metric questions, so it has to be
// cheap. The org-hierarchy dimensions used to be ranked over 30 days, which on
// the enterprise path fans each row out through JSON-array columns - tens of
// seconds on a large log table, all to learn which names exist. The distinct
// lookups the Logs filter bar uses answer that in milliseconds.
func TestDescribeFilterSpaceUsesDistinctLookups(t *testing.T) {
	fake := &fakeLogReader{
		availableTeams:       []KeyPair{{ID: "t1", Name: "Payments"}, {ID: "t2", Name: ""}},
		availableCustomers:   []KeyPair{{ID: "c1", Name: "Acme"}},
		availableVirtualKeys: []KeyPair{{ID: "vk1", Name: "prod"}},
	}
	result, err := runTool(t, "describe_filter_space", &Deps{LogManager: fake}, map[string]any{})
	require.NoError(t, err)
	out := result.(map[string]any)
	require.Equal(t, []string{"Payments (t1)", "t2"}, out["teams"])
	require.Equal(t, []string{"Acme (c1)"}, out["customers"])
	require.Equal(t, []string{}, out["business_units"])
	require.Equal(t, []KeyPair{{ID: "vk1", Name: "prod"}}, out["virtual_keys"])
	require.Empty(t, fake.rankingDimension, "no ranking query may run for a name lookup")
}

// This tool used to be two - describe_scope and describe_filter_space - that
// each independently fetched virtual keys. Merged, there is exactly one
// GetAvailableVirtualKeys call and one result carrying everything either half
// used to answer on its own: caller identity, org-hierarchy names, and the
// plain filter-value lists.
func TestDescribeFilterSpaceMergesScopeAndFilterValues(t *testing.T) {
	fake := &fakeLogReader{
		availableModels:      []string{"gpt-4o"},
		availableApps:        []string{"cli"},
		availableStopReasons: []string{"stop", "length"},
		availableTeams:       []KeyPair{{ID: "t1", Name: "Payments"}},
	}
	result, err := runToolCtx(t, scoped("user-7"), "describe_filter_space", &Deps{LogManager: fake}, map[string]any{})
	require.NoError(t, err)
	out := result.(map[string]any)

	// The describe_scope half.
	require.Equal(t, true, out["caller_is_identified"])
	require.Equal(t, "user-7", out["caller_user_id"])
	require.Equal(t, "the person asking", out["default_scope"])
	require.Equal(t, []string{"Payments (t1)"}, out["teams"])

	// The describe_filter_space half.
	require.Equal(t, []string{"gpt-4o"}, out["models"])
	require.Equal(t, []string{"cli"}, out["apps"])
	require.Equal(t, []string{"stop", "length"}, out["stop_reasons"])
}

func TestDescribeFilterSpaceDefaultScopeWhenUnidentified(t *testing.T) {
	result, err := runTool(t, "describe_filter_space", &Deps{LogManager: &fakeLogReader{}}, map[string]any{})
	require.NoError(t, err)
	out := result.(map[string]any)
	require.Equal(t, false, out["caller_is_identified"])
	// This string is read at the moment the model decides how to ask. It used to
	// say only "ask which team, customer or business unit is meant", and the
	// replies echoed it as prose - "I need you to pick a scope first: team,
	// customer, or business unit." - with nothing to click. It names the tool.
	// Worded for any MCP client; Warp's own prompt names its question tool.
	require.Contains(t, out["default_scope"], "question tool")
	require.Contains(t, out["default_scope"], "not as prose")
	require.NotContains(t, out, "caller_user_id")
}

// describe_scope used to hardcode "" for the org-hierarchy lookups
// regardless of what the model passed, which was a real gap once the tools
// merged and search became meaningful across all of them. Every lookup - not
// just models/apps/stop_reasons - must see the same search string now.
func TestDescribeFilterSpaceSearchReachesEveryLookup(t *testing.T) {
	fake := &fakeLogReader{}
	_, err := runTool(t, "describe_filter_space", &Deps{LogManager: fake}, map[string]any{"search": "pay"})
	require.NoError(t, err)
	require.Equal(t, "pay", fake.teamsQuerySeen)
	require.Equal(t, "pay", fake.customersQuerySeen)
	require.Equal(t, "pay", fake.businessUnitsQuerySeen)
	require.Equal(t, "pay", fake.virtualKeysQuerySeen)
}

// The content_search limit counts characters, as the schema's maxLength does -
// not bytes. 500 two-byte runes passed the advertised limit and then failed
// here, with the error reporting a byte count as a character count.
func TestContentSearchLimitCountsCharacters(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	_, err := parseFilters(map[string]any{"content_search": strings.Repeat("é", MaxContentSearchChars)}, now)
	require.NoError(t, err, "a search at the advertised character limit must be accepted whatever its byte length")
	_, err = parseFilters(map[string]any{"content_search": strings.Repeat("é", MaxContentSearchChars+1)}, now)
	require.ErrorContains(t, err, fmt.Sprintf("%d characters", MaxContentSearchChars+1),
		"the rejection must report the character count, not the byte count")
}

// Five distinct valid metrics still overrun query_metrics' advertised maxItems
// of 4 - the vocabulary has six values, so "every element is valid and unique"
// alone exceeds the published contract by two queries.
func TestEnumSliceArgEnforcesAdvertisedMaxItems(t *testing.T) {
	allowed := []string{"summary", "requests", "tokens", "cost", "latency", "throughput"}
	_, err := enumSliceArg(map[string]any{"metrics": []any{"summary", "requests", "tokens", "cost", "latency"}}, "metrics", "metric", allowed, maxQueryMetrics)
	require.ErrorContains(t, err, "at most 4")
	values, err := enumSliceArg(map[string]any{"metrics": []any{"cost", "summary"}}, "metrics", "metric", allowed, maxQueryMetrics)
	require.NoError(t, err)
	require.Equal(t, []string{"cost", "summary"}, values)
}

// An explicitly empty or null filter array asks for a filter and then names
// nothing to filter on. stringSliceField returned nil for both, applyFilters
// skips a zero-length slice, and the query ran unfiltered - a broader answer
// than the one asked for, with nothing saying the filter had been dropped.
func TestFilterArraysRejectEmptyAndNull(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	for _, key := range []string{"providers", "models", "status", "user_ids", "apps"} {
		_, err := parseFilters(map[string]any{key: []any{}}, now)
		require.ErrorContains(t, err, "must list at least one value", key)
		_, err = parseFilters(map[string]any{key: nil}, now)
		require.ErrorContains(t, err, "got null", key)
	}
	// Omitting the field is still how you search without that filter.
	filters, err := parseFilters(map[string]any{}, now)
	require.NoError(t, err)
	require.Empty(t, filters.Providers)

	filters, err = parseFilters(map[string]any{"providers": []any{"openai"}}, now)
	require.NoError(t, err)
	require.Equal(t, []string{"openai"}, filters.Providers)
}

// A present non-string log_id used to read as an omitted one, so the model was
// told it forgot a field it had actually sent and retried the same shape.
func TestStringArgRejectsWrongShapes(t *testing.T) {
	for _, bad := range []any{42.0, nil, "", "   ", []any{"a"}} {
		_, err := stringArg(map[string]any{"log_id": bad}, "log_id")
		require.Error(t, err, "log_id %v must be rejected", bad)
	}
	_, err := stringArg(map[string]any{}, "log_id")
	require.ErrorContains(t, err, "log_id is required")
	value, err := stringArg(map[string]any{"log_id": "abc"}, "log_id")
	require.NoError(t, err)
	require.Equal(t, "abc", value)
}

// The routing lookups return fixed values: what matters to the tools is that
// they are asked, and that what comes back reaches the model.
func (f *fakeLogReader) GetAvailableRoutingRules(context.Context, int, string) ([]KeyPair, error) {
	return []KeyPair{{ID: "rule-premium", Name: "Premium tier"}}, nil
}

func (f *fakeLogReader) GetAvailableSelectedKeys(context.Context, int, string) ([]KeyPair, error) {
	return []KeyPair{{ID: "key-1", Name: "anthropic-primary"}}, nil
}

func (f *fakeLogReader) GetAvailableAliases(context.Context, int, string) ([]string, error) {
	return []string{"smart"}, nil
}

func (f *fakeLogReader) GetAvailableRoutingEngines(context.Context, int, string) ([]string, error) {
	return []string{"routing-rule", "loadbalancing"}, nil
}

func (f *fakeLogReader) GetAvailableToolCallNames(context.Context, int, string) ([]string, error) {
	return []string{"get_weather"}, nil
}

func (f *fakeLogReader) GetAvailableMetadataKeys(context.Context, int, string) (map[string][]string, error) {
	return map[string][]string{"env": {"prod", "staging"}}, nil
}

// The cost summary lists model names but no per-model amounts. Asked for
// "model-wise spend", a model that reached query_metrics first saw names with
// no numbers beside them and told the user per-model spend was unsupported.
// The result says where the per-model figures are instead.
func TestMetricsCostPointsToPerModelSpend(t *testing.T) {
	fake := &fakeLogReader{costHistogramResult: &logstore.CostHistogramResult{Models: []string{"gpt-4o"}}}
	result, err := runTool(t, "query_metrics", &Deps{LogManager: fake}, map[string]any{
		"filters": map[string]any{}, "metrics": []any{"cost"},
	})
	require.NoError(t, err)
	cost := result.(map[string]any)["cost"].(map[string]any)
	require.Contains(t, cost["per_model_cost"], "query_model_performance")
}

// query_model_performance is the tool that answers per-model spend - every row
// carries total, input and output cost - but its description only said
// "usage", so a spend question was routed elsewhere.
func TestModelPerformanceAdvertisesSpend(t *testing.T) {
	tool, ok := toolByName(buildTools(), "query_model_performance")
	require.True(t, ok)
	for _, phrase := range []string{"spend", "input cost", "output cost", "model-wise"} {
		require.Contains(t, tool.description, phrase)
	}
}

// A per-provider total must come from the store, not from the model adding up
// buckets. The coarse buckets are aligned to the bucket size, so the first one
// starts before the window and, on Postgres, is read from whole matview hours.
// A model summing them dropped that first bucket as out of range and reported
// Anthropic $0.20 short of the dashboard's total over the same window.
func TestMetricsProviderGroupedReportsExactTotals(t *testing.T) {
	previous := Now
	Now = func() time.Time { return time.Date(2026, 9, 21, 11, 6, 0, 0, time.UTC) }
	defer func() { Now = previous }()

	fake := &fakeLogReader{
		providerCostHistogramResult: &logstore.ProviderCostHistogramResult{
			Buckets: []logstore.ProviderCostHistogramBucket{
				{TotalCost: 0.2332, ByProvider: map[string]float64{"anthropic": 0.2332}},
				{TotalCost: 8.1, ByProvider: map[string]float64{"anthropic": 7.8, "openai": 0.3}},
			},
			Providers: []string{"anthropic", "openai"},
		},
		statsByProvider: map[string]*logstore.SearchStats{
			"anthropic": {TotalRequests: 717, TotalCost: 8.2614},
			"openai":    {TotalRequests: 497, TotalCost: 0.3573},
		},
	}
	result, err := runTool(t, "query_metrics", &Deps{LogManager: fake}, map[string]any{
		"filters":  map[string]any{"start_time": "-7d", "status": []any{"success"}},
		"metrics":  []any{"cost"},
		"group_by": "provider",
	})
	require.NoError(t, err)

	totals, ok := result.(map[string]any)["provider_totals"].(map[string]providerTotal)
	require.True(t, ok, "a provider-grouped result must carry exact per-provider totals")
	require.Len(t, totals, 2)
	require.InDelta(t, 8.2614, totals["anthropic"].TotalCost, 1e-9)
	require.EqualValues(t, 717, totals["anthropic"].TotalRequests)
	require.InDelta(t, 0.3573, totals["openai"].TotalCost, 1e-9)

	// Each provider row opens its own traffic. With only the result's logs_link
	// to hand, a per-provider table linked Anthropic and OpenAI to the same
	// unfiltered page.
	require.Contains(t, totals["anthropic"].Link, "providers=anthropic")
	require.Contains(t, totals["anthropic"].Link, "status=success", "the tool's own filters carry over")
	require.Contains(t, totals["openai"].Link, "providers=openai")

	require.Len(t, fake.statsFiltersSeen, 2)
	for _, seen := range fake.statsFiltersSeen {
		require.Len(t, seen.Providers, 1, "each total is narrowed to one provider")
		require.Equal(t, []string{"success"}, seen.Status, "the tool's own filters carry over")
		require.Equal(t, Now().Add(-7*24*time.Hour), *seen.StartTime, "the exact window, not the bucket-aligned one")
	}
}

// "Which provider had the highest failure rate" is a summary question, and the
// model asked for exactly that: metrics ["summary"], group_by "provider". The
// provider list only came out of a per-provider histogram, so with no
// histogram metric asked for the grouping was dropped without a word - one
// deployment-wide summary came back, and the model told the person it had no
// per-provider split. provider_totals, which carries each provider's success
// rate, must come back for a summary-only grouped call too.
func TestMetricsSummaryOnlyProviderGroupingReturnsTotals(t *testing.T) {
	previous := Now
	Now = func() time.Time { return time.Date(2026, 9, 21, 11, 6, 0, 0, time.UTC) }
	defer func() { Now = previous }()

	fake := &fakeLogReader{
		providerCostHistogramResult: &logstore.ProviderCostHistogramResult{Providers: []string{"anthropic", "openai"}},
		statsByProvider: map[string]*logstore.SearchStats{
			"anthropic": {TotalRequests: 400, SuccessRate: 77.6},
			"openai":    {TotalRequests: 300, SuccessRate: 99.1},
		},
	}
	result, err := runTool(t, "query_metrics", &Deps{LogManager: fake}, map[string]any{
		"filters":  map[string]any{"start_time": "-7d"},
		"metrics":  []any{"summary"},
		"group_by": "provider",
	})
	require.NoError(t, err)

	out := result.(map[string]any)
	totals, ok := out["provider_totals"].(map[string]providerTotal)
	require.True(t, ok, "group_by provider must not be dropped when only summary is asked for")
	require.InDelta(t, 77.6, totals["anthropic"].SuccessRate, 1e-9)
	require.InDelta(t, 99.1, totals["openai"].SuccessRate, 1e-9)
	require.NotContains(t, out, "cost", "discovering providers must not add a metric nobody asked for")
}

// end_time is documented as defaulting to now, so a model spelling that out as
// "now" is asking for the default, not sending a malformed value. Rejecting it
// failed the whole tool call, and the model told the user it needed a valid end
// time before it could answer a question with no time in it at all.
func TestParseTimeAcceptsNow(t *testing.T) {
	now := time.Date(2026, 9, 21, 11, 6, 0, 0, time.UTC)
	for _, text := range []string{"now", "NOW", " now "} {
		parsed, err := parseTime(text, now)
		require.NoError(t, err, text)
		require.Equal(t, now, *parsed, text)
	}
	_, err := runTool(t, "query_model_performance", &Deps{LogManager: &fakeLogReader{}}, map[string]any{
		"filters": map[string]any{"start_time": "-7d", "end_time": "now"},
	})
	require.NoError(t, err)
	_, err = parseTime("nowish", now)
	require.Error(t, err, "only the exact word is the current time")
}

// "Tell me about the failure rate" was answered from query_metrics filtered to
// status success,error - both are needed for a rate - so the only link on the
// result opened every request, and "Open the filtered logs" showed 1,226 rows
// instead of the 72 failures. A result that reports a success rate must also
// carry a link narrowed to the failures, and none when nothing failed.
func TestSuccessRateResultsCarryFailuresLink(t *testing.T) {
	args := map[string]any{"filters": map[string]any{"start_time": "-7d", "status": []any{"success", "error"}}}

	fake := &fakeLogReader{statsResponses: []*logstore.SearchStats{{TotalRequests: 1226, SuccessRate: 94.13}, {TotalRequests: 1226, SuccessRate: 94.13}}}
	metrics, err := runTool(t, "query_metrics", &Deps{LogManager: fake}, map[string]any{"filters": args["filters"], "metrics": []any{"summary"}})
	require.NoError(t, err)
	counted, err := runTool(t, "count_logs", &Deps{LogManager: fake}, args)
	require.NoError(t, err)
	for name, result := range map[string]any{"query_metrics": metrics, "count_logs": counted} {
		link, _ := result.(map[string]any)["failures_link"].(string)
		require.Regexp(t, `status=error(&|$)`, link, name)
		require.NotContains(t, link, "success", name)
	}

	clean := &fakeLogReader{statsResponses: []*logstore.SearchStats{{TotalRequests: 10, SuccessRate: 100}}}
	counted, err = runTool(t, "count_logs", &Deps{LogManager: clean}, args)
	require.NoError(t, err)
	require.NotContains(t, counted.(map[string]any), "failures_link", "nothing failed, so there is nothing to link to")

	// An empty window reports a 0% success rate, which is not 72 failures: the
	// link would open an empty page.
	empty := &fakeLogReader{statsResponses: []*logstore.SearchStats{{TotalRequests: 0, SuccessRate: 0}, {TotalRequests: 0, SuccessRate: 0}}}
	metrics, err = runTool(t, "query_metrics", &Deps{LogManager: empty}, map[string]any{"filters": args["filters"], "metrics": []any{"summary"}})
	require.NoError(t, err)
	counted, err = runTool(t, "count_logs", &Deps{LogManager: empty}, args)
	require.NoError(t, err)
	for name, result := range map[string]any{"query_metrics": metrics, "count_logs": counted} {
		require.NotContains(t, result.(map[string]any), "failures_link", "no requests, so there is nothing to link to (%s)", name)
	}
}

// Asked "what did I spend on each provider", gpt-5.4-mini sent apps: ["Warp"]
// twice - once under a prompt telling it to "filter it out with apps", once
// under one saying never to - and reported Warp's own spend as the person's.
// Refusing "Warp" in apps with a scope "warp" escape hatch did not hold either:
// the next run sent scope "warp" for the same question and reported $5.84
// against a $9.08 dashboard as "all traffic". There is no filter that narrows
// to Warp's own queries at all now; query_usage_by by app still shows Warp's
// cost as one row among the others.
func TestFiltersNeverNarrowToWarp(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	for _, apps := range [][]any{{"Warp"}, {"warp"}, {"Claude Code", "Warp"}} {
		_, _, err := filterArg(context.Background(), map[string]any{"filters": map[string]any{"apps": apps}}, now)
		require.Error(t, err, "apps %v", apps)
		require.Contains(t, err.Error(), "query_usage_by")
		require.NotContains(t, err.Error(), `scope "warp"`, "the error must not point at another way to narrow to Warp")
	}

	_, _, err := filterArg(context.Background(), map[string]any{"filters": map[string]any{"scope": "warp"}}, now)
	require.Error(t, err, `scope "warp" is how the same misreading got through`)

	filters, _, err := filterArg(context.Background(), map[string]any{"filters": map[string]any{"apps": []any{"Claude Code"}}}, now)
	require.NoError(t, err)
	require.Equal(t, []string{"Claude Code"}, filters.Apps)
}

// "Show me the failed requests in the past week" matched 145 rows, and the
// over-the-cap guidance only offered narrowing or slicing - so the model told
// the person it could not list them and asked how to narrow, while the
// logs_link it was holding already opened exactly that set in the Logs view.
// Past the row cap, the guidance must hand over the link as the answer to a
// "show me" question rather than make narrowing the only path.
func TestCountLogsOverCapPointsAtLogsLink(t *testing.T) {
	for _, total := range []int64{MaxLogRows + 120, LargeResultThreshold + 1} {
		fake := &fakeLogReader{statsResponses: []*logstore.SearchStats{{TotalRequests: total}}}
		counted, err := runTool(t, "count_logs", &Deps{LogManager: fake}, map[string]any{
			"filters": map[string]any{"status": []any{"error"}, "start_time": "-7d"},
		})
		require.NoError(t, err)
		guidance, _ := counted.(map[string]any)["guidance"].(string)
		require.Contains(t, guidance, "logs_link", "total %d: guidance must offer the filtered Logs view", total)

		// A query the Logs page cannot reproduce gets no logs_link, so the
		// guidance must not hand over a link the result does not carry.
		fake = &fakeLogReader{statsResponses: []*logstore.SearchStats{{TotalRequests: total}}}
		counted, err = runTool(t, "count_logs", &Deps{LogManager: fake}, map[string]any{
			"filters": map[string]any{"status_codes": []any{float64(429)}, "start_time": "-7d"},
		})
		require.NoError(t, err)
		require.NotContains(t, counted.(map[string]any), "logs_link")
		guidance, _ = counted.(map[string]any)["guidance"].(string)
		require.NotContains(t, guidance, "logs_link", "total %d: no link to offer", total)
		require.Contains(t, guidance, "narrow", "total %d: narrowing or slicing is the path left", total)
	}
}

// Asked what was causing 17 invalid_request_error failures, Warp could count them
// and had no way to fetch them: rows filter on status, not on error type. It
// pulled the newest failures of every kind, traced three overloaded_errors, ran
// out of steps - and on "try again" compared the count against fail_reason, a
// tally of retry attempts, and reported that the requests were fine. The rows
// behind an error ranking are one filter away.
func TestFiltersAcceptErrorTypeCodeAndStatus(t *testing.T) {
	filters, err := parseFilters(map[string]any{
		"status":       []any{"error"},
		"error_types":  []any{"invalid_request_error"},
		"error_codes":  []any{"context_length_exceeded"},
		"status_codes": []any{float64(400), float64(413)},
	}, Now())
	require.NoError(t, err)
	require.Equal(t, []string{"invalid_request_error"}, filters.ErrorTypes)
	require.Equal(t, []string{"context_length_exceeded"}, filters.ErrorCodes)
	require.Equal(t, []int{400, 413}, filters.StatusCodes)

	// A malformed value is refused, not dropped: a dropped filter answers a
	// wider question than the one asked, and says nothing about it.
	for _, bad := range []any{[]any{"400"}, []any{400.5}, []any{float64(99)}, []any{float64(600)}, float64(400), []any{}} {
		_, err := parseFilters(map[string]any{"status_codes": bad}, Now())
		require.ErrorContains(t, err, "status_codes", "value %v", bad)
	}

	// They reach the store.
	fake := &fakeLogReader{}
	_, err = runTool(t, "query_logs", &Deps{LogManager: fake}, map[string]any{
		"filters": map[string]any{"start_time": "-7d", "error_types": []any{"invalid_request_error"}},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"invalid_request_error"}, fake.searchFilters.ErrorTypes)
}

// The Logs page has no error-type, error-code or status-code filter, so a link
// built from such a query would open every failure while sitting beside a count
// of seventeen. No link is better than one that is silently wider; each row
// still carries its own.
func TestResultsFilteredByErrorFieldsCarryNoLogsLink(t *testing.T) {
	for _, field := range []string{"error_types", "error_codes", "status_codes"} {
		value := []any{"invalid_request_error"}
		if field == "status_codes" {
			value = []any{float64(400)}
		}
		result, err := runTool(t, "count_logs", &Deps{LogManager: &fakeLogReader{}}, map[string]any{
			"filters": map[string]any{"start_time": "-7d", field: value},
		})
		require.NoError(t, err)
		require.NotContains(t, result.(map[string]any), "logs_link", "filtered by %s", field)
		require.NotContains(t, result.(map[string]any), "failures_link", "filtered by %s", field)
	}
	result, err := runTool(t, "count_logs", &Deps{LogManager: &fakeLogReader{}}, map[string]any{
		"filters": map[string]any{"start_time": "-7d", "status": []any{"error"}},
	})
	require.NoError(t, err)
	require.Contains(t, result.(map[string]any), "logs_link")
}

// The prompt's half of this is pinned in framework/warp
// (TestWarpPromptRoutesErrorAndRoutingQuestions).
func TestGuidesFromAnErrorRankingToItsRows(t *testing.T) {
	tool, ok := toolByName(buildTools(), "query_usage_by")
	require.True(t, ok)
	require.Contains(t, tool.description, "status_code (")
	require.Contains(t, tool.description, "never compare its counts with error_type")
	require.Contains(t, FilterSchema, `"error_types"`)
	require.Contains(t, FilterSchema, `"status_codes"`)
}

// Anything the Logs page can filter on, Warp has to be able to ask about.
// Routing rules, provider keys, aliases, routing engines, complexity tiers, tool
// calls and metadata were all filters there and arguments nowhere here, so "how
// much went through the premium rule" had no query behind it. Every field of
// SearchFilters is checked against the schema the model is shown, so a filter
// added to the store and the page fails here until the tools take it too.
func TestToolsAcceptEveryLogsFilter(t *testing.T) {
	notAnArgument := map[string]string{
		"roots_only":     "a display mode of the Logs page (grouped), not a question about traffic",
		"group_sessions": "a display mode of the Logs page (sessions collapsed), not a question about traffic",
		"ranking_limit":  "set from each tool's own limit argument",
	}
	var schema struct {
		Properties map[string]any `json:"properties"`
	}
	require.NoError(t, sonic.UnmarshalString(FilterSchema, &schema))
	fields := reflect.TypeOf(logstore.SearchFilters{})
	for i := range fields.NumField() {
		name := strings.Split(fields.Field(i).Tag.Get("json"), ",")[0]
		if _, skip := notAnArgument[name]; skip {
			continue
		}
		require.Contains(t, schema.Properties, name, "SearchFilters.%s is a Logs filter the tools do not offer", fields.Field(i).Name)
	}

	filters, err := parseFilters(map[string]any{
		"routing_rule_ids": []any{"rule-premium"}, "selected_key_ids": []any{"key-1"}, "aliases": []any{"smart"},
		"routing_engine_used": []any{"loadbalancing"}, "complexity_tiers": []any{"COMPLEX"}, "complexity_mechanisms": []any{"semantic"},
		"tool_call_names": []any{"get_weather"}, "user_agents": []any{"curl/8"},
		"metadata_filters": map[string]any{"env": "prod"}, "session_id": "sess-1", "parent_request_id": "req-0", "request_id": "req-1",
		"missing_cost_only": true,
	}, Now())
	require.NoError(t, err)
	require.Equal(t, []string{"rule-premium"}, filters.RoutingRuleIDs)
	require.Equal(t, []string{"key-1"}, filters.SelectedKeyIDs)
	require.Equal(t, []string{"smart"}, filters.Aliases)
	require.Equal(t, []string{"loadbalancing"}, filters.RoutingEngineUsed)
	require.Equal(t, []string{"COMPLEX"}, filters.ComplexityTiers)
	require.Equal(t, []string{"semantic"}, filters.ComplexityMechanisms)
	require.Equal(t, []string{"get_weather"}, filters.ToolCallNames)
	require.Equal(t, []string{"curl/8"}, filters.UserAgents)
	require.Equal(t, map[string]string{"env": "prod"}, filters.MetadataFilters)
	require.Equal(t, "sess-1", filters.SessionID)
	require.Equal(t, "req-0", filters.ParentRequestID)
	require.Equal(t, "req-1", filters.RequestID)
	require.True(t, filters.MissingCostOnly)

	// Refused, not dropped.
	for _, bad := range []map[string]any{
		{"metadata_filters": map[string]any{"env": 1}}, {"metadata_filters": "env=prod"}, {"metadata_filters": map[string]any{}},
		{"session_id": "  "}, {"request_id": 7}, {"missing_cost_only": "yes"},
	} {
		_, err := parseFilters(bad, Now())
		require.Error(t, err, "%v", bad)
	}
}

// A guessed rule id returns an empty result that reads like a finding, which is
// how "chat_completion" produced "no records" on a busy day. The values that
// exist are listed where the model already looks for names.
func TestDescribeFilterSpaceListsRoutingValues(t *testing.T) {
	result, err := runTool(t, "describe_filter_space", &Deps{LogManager: &fakeLogReader{}}, map[string]any{})
	require.NoError(t, err)
	out := result.(map[string]any)
	require.Equal(t, []KeyPair{{ID: "rule-premium", Name: "Premium tier"}}, out["routing_rules"])
	require.Equal(t, []KeyPair{{ID: "key-1", Name: "anthropic-primary"}}, out["provider_keys"])
	require.Equal(t, []string{"smart"}, out["aliases"])
	require.Equal(t, []string{"routing-rule", "loadbalancing"}, out["routing_engines"])
	require.Equal(t, []string{"get_weather"}, out["tool_call_names"])
	require.Equal(t, map[string][]string{"env": {"prod", "staging"}}, out["metadata"])
}

// "Which rule takes the most traffic" is a ranking, and each row opens as the
// requests it counted - these are filters the Logs page has.
func TestRanksAndLinksRoutingDimensions(t *testing.T) {
	cases := map[string]string{
		"routing_rule": "routing_rule_ids", "selected_key": "selected_key_ids", "alias": "aliases",
		"complexity_tier": "complexity_tiers", "complexity_mechanism": "complexity_mechanisms",
		"routing_engine": "routing_engine_used", "tool_call_name": "tool_call_names",
	}
	for dimension, param := range cases {
		fake := &fakeLogReader{dimensionRankingResult: &logstore.DimensionRankingResult{
			Dimension: logstore.RankingDimension(dimension),
			Rankings:  []logstore.DimensionRankingWithTrend{{DimensionRankingEntry: logstore.DimensionRankingEntry{ID: "id-1", Name: "One", TotalRequests: 7}}},
		}}
		_, rows := rankingRowsJSON(t, "query_usage_by", &Deps{LogManager: fake},
			map[string]any{"dimension": dimension, "filters": map[string]any{"start_time": "-7d"}}, "rankings")
		require.Len(t, rows, 1, dimension)
		require.Equal(t, "id-1", linkQuery(t, rows[0]["link"]).Get(param), dimension)
	}
}

// A row says which rule, key and alias handled it, so a list of requests can be
// read without tracing each one.
func TestLogRowsCarryRoutingFields(t *testing.T) {
	rule, ruleName, alias, tier := "rule-premium", "Premium tier", "smart", "COMPLEX"
	fake := &fakeLogReader{searchResult: &logstore.SearchResult{Logs: []logstore.Log{{
		ID: "req-1", Timestamp: Now(), Provider: "anthropic", Model: "claude-sonnet-5", Status: "success",
		RoutingRuleID: &rule, RoutingRuleName: &ruleName, SelectedKeyName: "anthropic-primary", Alias: &alias, ComplexityTier: &tier,
		ToolCallNames: []string{"get_weather"},
	}}}}
	out, err := runTool(t, "query_logs", &Deps{LogManager: fake}, map[string]any{"filters": map[string]any{"start_time": "-1d"}})
	require.NoError(t, err)
	encoded, err := sonic.Marshal(out)
	require.NoError(t, err)
	var shape struct {
		Rows []map[string]any `json:"rows"`
	}
	require.NoError(t, sonic.Unmarshal(encoded, &shape))
	rows := shape.Rows
	require.Len(t, rows, 1)
	require.Equal(t, "Premium tier", rows[0]["routing_rule"])
	require.Equal(t, "anthropic-primary", rows[0]["provider_key"])
	require.Equal(t, "smart", rows[0]["alias"])
	require.Equal(t, "COMPLEX", rows[0]["complexity_tier"])
	require.Equal(t, []any{"get_weather"}, rows[0]["tool_calls"])
}

// "What caused it" was refused while the exact count of every failure by kind sat
// behind the tenth dimension of a tool described as "who is spending the most".
// The description says so up front.
func TestUsageByDescriptionLeadsWithTheFailureBreakdown(t *testing.T) {
	tool, ok := toolByName(buildTools(), "query_usage_by")
	require.True(t, ok)
	failures := strings.Index(tool.description, "failure breakdown")
	require.GreaterOrEqual(t, failures, 0)
	require.Less(t, failures, strings.Index(tool.description, "Dimensions:"), "said up front, not left to the dimension list")
	require.Contains(t, tool.description, "what caused the failure spike")
}

// An argument a tool does not take is named, with the ones it does, rather than
// dropped - see refuseUnknownArguments. Every tool's schema has to declare its
// arguments for that to hold, and the unknown-filter error is not allowed to
// fall behind the schema again.
func TestServerRefusesArgumentsAToolDoesNotTake(t *testing.T) {
	for _, tool := range buildTools() {
		if len(tool.argumentNames()) == 0 {
			require.NoError(t, refuseUnknownArguments(tool.name, nil, map[string]any{}))
			require.ErrorContains(t, refuseUnknownArguments(tool.name, nil, map[string]any{"unexpected": true}), tool.name)
			continue
		}
		require.NotEmpty(t, tool.argumentNames(), "tool %s declares no arguments", tool.name)
	}
	tool, ok := toolByName(buildTools(), "query_model_performance")
	require.True(t, ok)
	err := refuseUnknownArguments(tool.name, tool.argumentNames(), map[string]any{"filters": map[string]any{}, "metrics": []any{"x"}, "group_by": "none", "limit": 5.0})
	require.ErrorContains(t, err, "does not take group_by, metrics")
	require.ErrorContains(t, err, "include_performance")
	require.NoError(t, refuseUnknownArguments(tool.name, tool.argumentNames(), map[string]any{"filters": map[string]any{}, "limit": 5.0}))

	_, err = parseFilters(map[string]any{"error_type": []any{"x"}}, Now())
	require.ErrorContains(t, err, "error_types")
	require.ErrorContains(t, err, "status_codes")
}

// filters is required by every flow's schema. Anything but an object used to
// parse as "no filters" and answer over the default window, which reads as an
// answer to the question asked.
func TestFilterArgRejectsMissingOrMistypedFilters(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	for name, args := range map[string]map[string]any{
		"missing": {},
		"null":    {"filters": nil},
		"string":  {"filters": "last 7 days"},
		"array":   {"filters": []any{"-7d"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := filterArg(context.Background(), args, now)
			require.Error(t, err)
			require.Contains(t, err.Error(), "filters")
		})
	}
	_, _, err := filterArg(context.Background(), map[string]any{"filters": map[string]any{}}, now)
	require.NoError(t, err)
}

// The limit is in bytes, so a cut that lands inside a multibyte character
// must back off to its start rather than emit invalid UTF-8.
func TestTruncateTextKeepsRuneBoundary(t *testing.T) {
	text := "ab\u00e9\u00e9\u00e9" // "ab" + three two-byte runes
	for limit := 0; limit < len(text); limit++ {
		got := truncateText(text, limit)
		require.True(t, utf8.ValidString(got), "limit %d produced invalid UTF-8: %q", limit, got)
		require.True(t, strings.HasSuffix(got, "... [truncated]"))
	}
	require.Equal(t, "ab\u00e9... [truncated]", truncateText(text, 5))
	require.Equal(t, text, truncateText(text, len(text)))
}

// deps is resolved per call: a logging plugin enabled after boot has to be
// reachable through the server that was built before it existed.
func TestServerResolvesDepsPerCall(t *testing.T) {
	var current *Deps = &Deps{}
	srv := NewServer(func() *Deps { return current })
	client, err := mcpclient.NewInProcessClient(srv)
	require.NoError(t, err)
	defer client.Close()
	ctx := context.Background()
	require.NoError(t, client.Start(ctx))
	_, err = client.Initialize(ctx, mcp.InitializeRequest{Params: mcp.InitializeParams{
		ProtocolVersion: mcp.LATEST_PROTOCOL_VERSION,
		ClientInfo:      mcp.Implementation{Name: "test", Version: "0"},
	}})
	require.NoError(t, err)

	call := func() *mcp.CallToolResult {
		result, err := client.CallTool(ctx, mcp.CallToolRequest{Params: mcp.CallToolParams{
			Name:      "count_logs",
			Arguments: map[string]any{"filters": map[string]any{}},
		}})
		require.NoError(t, err)
		return result
	}
	require.True(t, call().IsError, "no log store yet")
	current = &Deps{LogManager: &fakeLogReader{}}
	require.False(t, call().IsError, "log store bound after the server was built")
}

// toolFacts is what the source says about one tool: its flags as declared, and
// whether its code (directly or through package helpers) reads deps.LogManager.
type toolFacts struct {
	noLogs, mutating, readsLogs bool
}

// scanToolFacts parses the package's non-test sources and reports, per tool,
// its declared flags and whether its function reaches LogManager. Reaching is
// followed through package-level helper calls, since tools share builders.
func scanToolFacts(t *testing.T) map[string]toolFacts {
	t.Helper()
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	require.NoError(t, err)
	var files []*ast.File
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, err)
		files = append(files, file)
	}

	funcs := map[string]*ast.FuncDecl{}
	for _, file := range files {
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil && fn.Recv == nil {
				funcs[fn.Name.Name] = fn
			}
		}
	}
	// Direct readers, then close over calls until nothing changes.
	reads := map[string]bool{}
	calls := map[string][]string{}
	for name, fn := range funcs {
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.SelectorExpr:
				if n.Sel.Name == "LogManager" {
					reads[name] = true
				}
			case *ast.CallExpr:
				if ident, ok := n.Fun.(*ast.Ident); ok {
					if _, isFunc := funcs[ident.Name]; isFunc {
						calls[name] = append(calls[name], ident.Name)
					}
				}
			}
			return true
		})
	}
	for changed := true; changed; {
		changed = false
		for name, callees := range calls {
			if reads[name] {
				continue
			}
			for _, callee := range callees {
				if reads[callee] {
					reads[name], changed = true, true
					break
				}
			}
		}
	}

	facts := map[string]toolFacts{}
	for name, fn := range funcs {
		if fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
			continue
		}
		if ident, ok := fn.Type.Results.List[0].Type.(*ast.Ident); !ok || ident.Name != "Tool" {
			continue
		}
		var lit *ast.CompositeLit
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			if c, ok := node.(*ast.CompositeLit); ok && lit == nil {
				if ident, ok := c.Type.(*ast.Ident); ok && ident.Name == "Tool" {
					lit = c
					return false
				}
			}
			return true
		})
		if lit == nil {
			continue
		}
		toolName, f := "", toolFacts{readsLogs: reads[name]}
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, _ := kv.Key.(*ast.Ident)
			if key == nil {
				continue
			}
			switch key.Name {
			case "name":
				switch v := kv.Value.(type) {
				case *ast.BasicLit:
					toolName = strings.Trim(v.Value, `"`)
				case *ast.Ident:
					if v.Name == "SemanticSearchToolName" {
						toolName = SemanticSearchToolName
					}
				}
			case "noLogs":
				f.noLogs = isTrueIdent(kv.Value)
			case "mutating":
				f.mutating = isTrueIdent(kv.Value)
			}
		}
		require.NotEmpty(t, toolName, "%s returns a Tool whose name the scan cannot read", name)
		facts[toolName] = f
	}
	return facts
}

func isTrueIdent(expr ast.Expr) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == "true"
}

// noLogs must match what a tool does. Set on a tool that reads the log store,
// it dereferences a nil reader when logging is disabled; missing on one that
// does not, the tool is refused for no reason on such a deployment.
func TestNoLogsMatchesLogStoreUse(t *testing.T) {
	facts := scanToolFacts(t)
	require.Len(t, facts, len(buildTools()), "the scan must see every tool buildTools returns")
	for name, f := range facts {
		if f.readsLogs {
			require.False(t, f.noLogs, "%s reads deps.LogManager but is marked noLogs", name)
		} else {
			require.True(t, f.noLogs, "%s never reads deps.LogManager but is not marked noLogs, so it is refused when logging is off", name)
		}
	}
}

// Every tool name starts with a verb, and the verb says whether it writes. A
// write tool left unmarked is advertised read-only, and a client may run it
// without the confirmation it reserves for side effects. A tool whose verb is
// in neither list fails here, so a new verb forces the decision.
func TestMutatingMatchesToolVerb(t *testing.T) {
	writeVerbs := []string{"create_", "update_", "delete_", "deactivate_", "rotate_", "add_", "attach_", "detach_", "reconnect_", "refresh_"}
	readVerbs := []string{"list_", "describe_", "get_", "query_", "count_", "semantic_search_"}
	hasPrefix := func(name string, prefixes []string) bool {
		return slices.ContainsFunc(prefixes, func(p string) bool { return strings.HasPrefix(name, p) })
	}
	for _, tool := range buildTools() {
		switch {
		case hasPrefix(tool.name, writeVerbs):
			require.True(t, tool.mutating, "%s changes state but is not marked mutating", tool.name)
		case hasPrefix(tool.name, readVerbs):
			require.False(t, tool.mutating, "%s only reads but is marked mutating", tool.name)
		default:
			t.Errorf("%s starts with a verb neither list names; add it to the write or read verbs", tool.name)
		}
	}
}

// A whole number past the destination type's range must be refused: the
// float-to-int conversion is implementation-defined there and can wrap.
func TestIntegerArgsRejectOutOfRange(t *testing.T) {
	huge := map[string]any{"n": 1e19}
	_, err := optionalInt64Arg(huge, "n")
	require.ErrorContains(t, err, "out of range")
	_, err = optionalInt64Arg(map[string]any{"n": -float64(math.MinInt64)}, "n")
	require.ErrorContains(t, err, "out of range")
	_, err = optionalIntArg(huge, "n")
	require.ErrorContains(t, err, "out of range")
	_, err = uintArg(map[string]any{"n": 1e20}, "n")
	require.ErrorContains(t, err, "out of range")

	n, err := optionalInt64Arg(map[string]any{"n": float64(1 << 53)}, "n")
	require.NoError(t, err)
	require.Equal(t, int64(1<<53), *n)
	u, err := uintArg(map[string]any{"n": 42.0}, "n")
	require.NoError(t, err)
	require.Equal(t, uint(42), u)

	limit, err := intArg(map[string]any{"limit": 1e19}, "limit", 10, 100)
	require.NoError(t, err)
	require.Equal(t, 100, limit)
}

// An ungoverned request - no virtual key, no user - has no grants to consult.
// Without the admin-API grant the transport sets, every write tool is refused
// before it runs.
func TestWriteToolsRequireWriteAccess(t *testing.T) {
	for _, tool := range buildTools() {
		if !tool.mutating {
			continue
		}
		result := runToolViaHandler(t, tool.name, &Deps{}, map[string]any{})
		require.True(t, result.IsError, tool.name)
		require.Contains(t, result.Content[0].(mcp.TextContent).Text, "has no permission to", tool.name)
	}
}

// With the grant, a write tool runs; a read tool never needed it.
func TestWriteAccessLetsWriteToolsRun(t *testing.T) {
	result := runToolViaHandlerCtx(t, WithWriteAccess(context.Background()), "update_feature_flag", &Deps{}, map[string]any{"id": "x", "enabled": true})
	require.True(t, result.IsError)
	require.NotContains(t, result.Content[0].(mcp.TextContent).Text, "has no permission to")
	require.Contains(t, result.Content[0].(mcp.TextContent).Text, "feature flag store is not available")

	read := runToolViaHandler(t, "get_version", &Deps{Version: "v1"}, map[string]any{})
	require.False(t, read.IsError)
}

// governedGrant stands in for the grant governance installs on a request whose
// virtual key or user holds permits. Only Access is read here.
type governedGrant struct {
	schemas.Grant
	access schemas.Access
}

func (g *governedGrant) Access() schemas.Access { return g.access }

type grantedAccess struct{ schemas.Access }

// What a virtual key may do on Bifrost it may do through these tools: a governed
// request reaches a write tool only after governance matched it to the key's
// grants, so the tool runs without any admin marker. A grant with no access -
// governance found nothing to govern - is still ungoverned and refused.
func TestWriteToolsFollowGovernedGrants(t *testing.T) {
	governed := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	governed.SetGrant(&governedGrant{access: grantedAccess{}})
	result := runToolViaHandlerCtx(t, governed, "update_feature_flag", &Deps{}, map[string]any{"id": "x", "enabled": true})
	require.NotContains(t, result.Content[0].(mcp.TextContent).Text, "has no permission to")
	require.Contains(t, result.Content[0].(mcp.TextContent).Text, "feature flag store is not available")

	ungoverned := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ungoverned.SetGrant(&governedGrant{})
	result = runToolViaHandlerCtx(t, ungoverned, "update_feature_flag", &Deps{}, map[string]any{"id": "x", "enabled": true})
	require.Contains(t, result.Content[0].(mcp.TextContent).Text, "has no permission to")
}

// The logging plugin withholds content for exactly this server's write tools:
// every mutating tool, no read tool, and nothing on any other client.
func TestIsBuiltinWriteTool(t *testing.T) {
	for _, tool := range buildTools() {
		require.Equal(t, tool.mutating, IsBuiltinWriteTool(bifrostMCPClientName, tool.name), tool.name)
	}
	require.False(t, IsBuiltinWriteTool("github", "create_virtual_key"), "another client's tool of the same name is not ours")
}
