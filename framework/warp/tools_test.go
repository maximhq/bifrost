package warp

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/require"
)

// fakeLogReader records what the tools asked for. Only the methods Warp's
// tools reach are implemented; the rest of logging.LogManager is embedded as a
// nil interface, so an executor that starts calling something new fails loudly
// with a nil-pointer panic in tests rather than silently widening Warp's reach.
//
// mu guards every field below: the agent loop runs a step's tool calls
// concurrently (see Agent.Run), so a test scripting two calls that both reach
// this fake - two query_metrics calls, or query_metrics alongside count_logs,
// both hitting GetStats - hits it from more than one goroutine at once. The
// four org-hierarchy lookups already had this same problem solved a different
// way (a distinct field per method, see the comment below) because they have
// always run concurrently, inside describe_filter_space's own fan-out; mu is
// the general form of that fix for every other method here.
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
	if f.dimensionRankingResult != nil {
		return f.dimensionRankingResult, nil
	}
	return &logstore.DimensionRankingResult{
		Dimension: dimension,
		Rankings:  []logstore.DimensionRankingWithTrend{{}},
	}, nil
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

func runTool(t *testing.T, name string, deps *ToolDeps, args map[string]any) (any, error) {
	t.Helper()
	// Built for the deps under test: the semantic tool is only in the set when a
	// searcher exists, which is the behaviour TestWarpToolsOmitSemanticSearch...
	// pins, so a test exercising that tool has to supply one.
	tool, ok := toolByName(buildToolsFor(deps.semantic), name)
	require.True(t, ok, "tool %s should exist", name)
	// Default to an identified caller. A deployment with no user identity has no
	// default scope, so an unscoped query from one is refused - correct, but it
	// is a case of its own rather than the baseline these tools were written
	// against. Tests about scoping set deps.scope explicitly.
	if !deps.scope.HasIdentity && deps.scope.UserID == "" {
		deps.scope = Scope{HasIdentity: true, UserID: "test-caller"}
	}
	return tool.execute(context.Background(), deps, args)
}

// Every declared schema must parse into the provider-facing type. A typo here
// would otherwise surface as a provider rejecting the whole request at runtime,
// which is a far more expensive place to find it.
func TestWarpToolSchemasAreValid(t *testing.T) {
	tools := buildTools()
	require.NotEmpty(t, tools)

	declared, err := responsesTools(tools)
	require.NoError(t, err)
	require.Len(t, declared, len(tools))

	for _, tool := range declared {
		require.Equal(t, schemas.ResponsesToolTypeFunction, tool.Type)
		require.NotNil(t, tool.Name)
		require.NotEmpty(t, *tool.Name)
		require.NotNil(t, tool.Description)
		require.NotEmpty(t, *tool.Description, "%s needs a description; it is the only thing telling the model when to use it", *tool.Name)
		require.NotNil(t, tool.ResponsesToolFunction, "tool must declare a function")
		require.NotNil(t, tool.ResponsesToolFunction.Parameters)
		require.Equal(t, "object", tool.ResponsesToolFunction.Parameters.Type)
	}
}

// declaredTools is what Agent.Run actually calls - each set has to agree with
// a fresh responsesTools(buildToolsFor(...)) parse, and repeated calls have to
// return the same result, or the memoization would be observable as a bug
// instead of the pure optimization it is meant to be.
func TestWarpDeclaredToolsMatchesFreshParse(t *testing.T) {
	for _, semantic := range []bool{false, true} {
		var searcher *SemanticSearcher
		if semantic {
			searcher = &SemanticSearcher{}
		}
		fresh, err := responsesTools(buildToolsFor(searcher))
		require.NoError(t, err)

		cached, err := declaredTools(semantic)
		require.NoError(t, err)
		require.Equal(t, fresh, cached, "semantic=%v", semantic)

		again, err := declaredTools(semantic)
		require.NoError(t, err)
		require.Same(t, &cached[0], &again[0], "repeated calls must reuse the same backing array, not re-parse")
	}
}

// The prompt tells the model semantic_search_logs exists whenever a searcher
// is configured, so the declarations sent with the request have to carry it -
// and must not carry it otherwise.
func TestWarpDeclaredToolsFollowSemanticAvailability(t *testing.T) {
	declaredNames := func(semantic bool) []string {
		tools, err := declaredTools(semantic)
		require.NoError(t, err)
		names := make([]string, 0, len(tools))
		for _, tool := range tools {
			names = append(names, *tool.Name)
		}
		return names
	}
	require.Contains(t, declaredNames(true), SemanticSearchToolName)
	require.NotContains(t, declaredNames(false), SemanticSearchToolName)
}

// The cap protects the context window, so it has to hold regardless of what the
// model asks for.
func TestWarpQueryLogsClampsLimit(t *testing.T) {
	fake := &fakeLogReader{}
	deps := &ToolDeps{logManager: fake}

	_, err := runTool(t, "query_logs", deps, map[string]any{
		"filters": map[string]any{},
		"limit":   float64(5000),
	})
	require.NoError(t, err)
	require.Equal(t, MaxLogRows, fake.searchPagination.Limit)
}

func TestWarpRankingClampsLimit(t *testing.T) {
	fake := &fakeLogReader{}
	deps := &ToolDeps{logManager: fake}

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

func TestWarpUsageByFlowUsesRequestedDimension(t *testing.T) {
	for _, dim := range []logstore.RankingDimension{
		logstore.RankingDimensionUser, logstore.RankingDimensionVirtualKey, logstore.RankingDimensionTeam,
		logstore.RankingDimensionCustomer, logstore.RankingDimensionBusinessUnit, logstore.RankingDimensionProject,
		logstore.RankingDimensionApp, logstore.RankingDimensionUserAgent,
	} {
		t.Run(string(dim), func(t *testing.T) {
			fake := &fakeLogReader{}
			_, err := runTool(t, "query_usage_by", &ToolDeps{logManager: fake}, map[string]any{
				"dimension": string(dim),
				"filters":   map[string]any{},
			})
			require.NoError(t, err)
			require.Equal(t, dim, fake.rankingDimension)
		})
	}
}

func TestWarpUsageByRejectsUnknownDimension(t *testing.T) {
	fake := &fakeLogReader{}
	_, err := runTool(t, "query_usage_by", &ToolDeps{logManager: fake}, map[string]any{
		"dimension": "region",
		"filters":   map[string]any{},
	})
	require.ErrorContains(t, err, `unknown dimension "region"`)
}

// A dropped filter answers a different question than the one asked, and neither
// the model nor the reader can tell. Rejecting is the only safe behaviour.
func TestWarpRejectsUnknownFilterField(t *testing.T) {
	fake := &fakeLogReader{}
	_, err := runTool(t, "query_logs", &ToolDeps{logManager: fake}, map[string]any{
		"filters": map[string]any{"provider": "openai"}, // singular; the real field is "providers"
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknown filter fields: provider")
	require.Nil(t, fake.searchFilters, "the query must not run with a silently dropped filter")
}

// Every field the FilterSchema declares must actually reach SearchFilters, or
// the model is offered a knob that silently does nothing.
func TestWarpFilterAcceptsPreviouslyMissingFields(t *testing.T) {
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
func TestWarpFilterRejectsInvalidTokenBounds(t *testing.T) {
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
func TestWarpStopReasonsFilterIsNotRejectedAsUnknown(t *testing.T) {
	fake := &fakeLogReader{}
	_, err := runTool(t, "query_logs", &ToolDeps{logManager: fake}, map[string]any{
		"filters": map[string]any{"stop_reasons": []any{"length"}},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"length"}, fake.searchFilters.StopReasons)
}

// A question naming a project is naming a scope, same as team, customer or
// business unit - it must not be silently widened to the caller's own traffic.
func TestWarpProjectFilterCountsAsANamedScope(t *testing.T) {
	filters := &logstore.SearchFilters{ProjectIDs: []string{"proj-1"}}
	applyScope(filters, Scope{HasIdentity: true, UserID: "user-7"}, false)
	require.Empty(t, filters.UserIDs, "naming a project must not also narrow to the caller")
}

func TestWarpFilterTimeParsing(t *testing.T) {
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
func TestWarpBoundToolResultReplacesRatherThanTruncates(t *testing.T) {
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

func TestWarpBoundToolResultPassesSmallPayloads(t *testing.T) {
	bounded := boundToolResult(map[string]any{"total": 42})
	require.Contains(t, bounded, `"total":42`)
	require.NotContains(t, bounded, "result too large")
}

// ContentHidden is a promise the deployment made about that request's payload.
// Warp is an API like any other and must not be the place it resurfaces.
func TestWarpNeverReturnsHiddenContent(t *testing.T) {
	entry := &logstore.Log{
		ID:             "hidden-row",
		Timestamp:      time.Now().UTC(),
		Provider:       "openai",
		Model:          "gpt-4o",
		Status:         "success",
		ContentHidden:  true,
		ContentSummary: "a secret the operator asked us not to store",
	}
	row := projectLog(entry, true, DetailContentChars)
	require.Empty(t, row.Content)
	require.Equal(t, "hidden-row", row.ID)
}

func TestWarpIncludesContentOnlyWhenAsked(t *testing.T) {
	entry := &logstore.Log{
		ID:             "visible-row",
		Timestamp:      time.Now().UTC(),
		Provider:       "openai",
		Model:          "gpt-4o",
		Status:         "success",
		ContentSummary: "what is the weather",
	}
	require.Empty(t, projectLog(entry, false, LogContentChars).Content)
	require.Equal(t, "what is the weather", projectLog(entry, true, LogContentChars).Content)
}

func TestWarpTruncatesLongContent(t *testing.T) {
	entry := &logstore.Log{
		ID:             "long-row",
		Timestamp:      time.Now().UTC(),
		ContentSummary: strings.Repeat("x", LogContentChars*3),
	}
	row := projectLog(entry, true, LogContentChars)
	require.Contains(t, row.Content, "[truncated]")
	require.Less(t, len(row.Content), LogContentChars*2)
}

// Token counts come from the denormalized columns, which survive object-storage
// offload and content-hidden rows. Reading them from the token_usage payload
// would report zero for exactly those rows.
func TestWarpUsesDenormalizedTokenColumns(t *testing.T) {
	entry := &logstore.Log{
		ID:               "tokens",
		Timestamp:        time.Now().UTC(),
		ContentHidden:    true,
		PromptTokens:     120,
		CompletionTokens: 45,
	}
	row := projectLog(entry, false, LogContentChars)
	require.Equal(t, 120, row.InputTokens)
	require.Equal(t, 45, row.OutputTokens)
}

func TestWarpQueryLogsReportsTotalSeparately(t *testing.T) {
	fake := &fakeLogReader{searchResult: &logstore.SearchResult{
		Logs:       []logstore.Log{{ID: "a", Timestamp: time.Now().UTC()}, {ID: "b", Timestamp: time.Now().UTC()}},
		Pagination: logstore.PaginationOptions{TotalCount: 12400},
	}}
	result, err := runTool(t, "query_logs", &ToolDeps{logManager: fake}, map[string]any{
		"filters": map[string]any{},
	})
	require.NoError(t, err)

	payload := result.(map[string]any)
	require.Equal(t, 2, payload["returned"])
	require.Equal(t, int64(12400), payload["total_matching"])
}

func TestWarpMetricsRequiresAtLeastOneMetric(t *testing.T) {
	_, err := runTool(t, "query_metrics", &ToolDeps{logManager: &fakeLogReader{}}, map[string]any{
		"filters": map[string]any{},
		"metrics": []any{},
	})
	require.ErrorContains(t, err, "metrics must list at least one")
}

func TestWarpMetricsRejectsUnknownMetric(t *testing.T) {
	_, err := runTool(t, "query_metrics", &ToolDeps{logManager: &fakeLogReader{}}, map[string]any{
		"filters": map[string]any{},
		"metrics": []any{"vibes"},
	})
	require.ErrorContains(t, err, "unknown metric")
}

func TestWarpMetricsSummaryUsesStats(t *testing.T) {
	fake := &fakeLogReader{}
	_, err := runTool(t, "query_metrics", &ToolDeps{logManager: fake}, map[string]any{
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
func TestWarpMetricsComparesToPreviousPeriod(t *testing.T) {
	fake := &fakeLogReader{statsResponses: []*logstore.SearchStats{
		{TotalRequests: 200, TotalTokens: 20000, TotalCost: 40}, // current period
		{TotalRequests: 100, TotalTokens: 10000, TotalCost: 20}, // previous period
	}}
	result, err := runTool(t, "query_metrics", &ToolDeps{logManager: fake}, map[string]any{
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
func TestWarpMetricsCompareToPreviousZeroBaseline(t *testing.T) {
	fake := &fakeLogReader{statsResponses: []*logstore.SearchStats{
		{TotalRequests: 200, TotalTokens: 20000, TotalCost: 40}, // current period
		{TotalRequests: 100, TotalTokens: 0, TotalCost: 0},      // previous period
	}}
	result, err := runTool(t, "query_metrics", &ToolDeps{logManager: fake}, map[string]any{
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

func TestWarpMetricsCompareToPreviousRequiresSummary(t *testing.T) {
	_, err := runTool(t, "query_metrics", &ToolDeps{logManager: &fakeLogReader{}}, map[string]any{
		"filters":             map[string]any{},
		"metrics":             []any{"cost"},
		"compare_to_previous": true,
	})
	require.ErrorContains(t, err, `compare_to_previous requires metrics to include "summary"`)
}

// A previous period with no traffic at all is a real answer ("nothing to
// compare against"), not a divide-by-zero.
func TestWarpMetricsCompareToPreviousHandlesEmptyPreviousPeriod(t *testing.T) {
	fake := &fakeLogReader{statsResponses: []*logstore.SearchStats{
		{TotalRequests: 50},
		{TotalRequests: 0},
	}}
	result, err := runTool(t, "query_metrics", &ToolDeps{logManager: fake}, map[string]any{
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
	result, err := runTool(t, "query_metrics", &ToolDeps{logManager: fake}, map[string]any{
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
func TestWarpMetricsRejectsTooManyBuckets(t *testing.T) {
	// Pinned: relative offsets resolve against Now, and a real clock would drift
	// the window this test depends on.
	previous := Now
	Now = func() time.Time { return time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC) }
	defer func() { Now = previous }()

	// ~26 years at 30-day buckets is ~324 buckets, over the 200 ceiling.
	_, err := runTool(t, "query_metrics", &ToolDeps{logManager: &fakeLogReader{}}, map[string]any{
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
	_, err = runTool(t, "query_metrics", &ToolDeps{logManager: &fakeLogReader{}}, map[string]any{
		"filters": map[string]any{"start_time": "-47h", "end_time": "2026-08-17T00:00:00Z"},
		"metrics": []any{"cost"},
	})
	require.NoError(t, err, "a 47h window is 47 hourly buckets and must be accepted")

	// The ceiling is about buckets, so it must not reach a request that builds
	// none: a summary over the same over-long range is one aggregate query.
	fake := &fakeLogReader{}
	_, err = runTool(t, "query_metrics", &ToolDeps{logManager: fake}, map[string]any{
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
func TestWarpMetricsLatencySummaryStaysUnderBudgetFor12HourWindow(t *testing.T) {
	const bucketCount = 72 // 12h at the 10-minute bucket size that window resolves to
	buckets := make([]logstore.LatencyHistogramBucket, bucketCount)
	for i := range buckets {
		buckets[i] = logstore.LatencyHistogramBucket{
			Timestamp: time.Now(), AvgLatency: 234.567, P90Latency: 450.123, P95Latency: 600.345, P99Latency: 890.123,
			AvgOverhead: 12.345, P90Overhead: 23.456, P95Overhead: 34.567, P99Overhead: 45.678, TotalRequests: 142,
		}
	}
	fake := &fakeLogReader{latencyHistogramResult: &logstore.LatencyHistogramResult{Buckets: buckets, BucketSizeSeconds: 600}}

	result, err := runTool(t, "query_metrics", &ToolDeps{logManager: fake}, map[string]any{
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
func TestWarpMetricsSummarizesRequestsHistogram(t *testing.T) {
	fake := &fakeLogReader{histogramResult: &logstore.HistogramResult{
		Buckets: []logstore.HistogramBucket{
			{Count: 10, Success: 9, Error: 1},
			{Count: 20, Success: 18, Error: 2},
			{Count: 30, Success: 27, Error: 3},
		},
		BucketSizeSeconds: 600,
	}}
	result, err := runTool(t, "query_metrics", &ToolDeps{logManager: fake}, map[string]any{
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

func TestWarpMetricsSummarizesTokensHistogram(t *testing.T) {
	fake := &fakeLogReader{tokenHistogramResult: &logstore.TokenHistogramResult{
		Buckets: []logstore.TokenHistogramBucket{
			{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150},
			{PromptTokens: 200, CompletionTokens: 100, TotalTokens: 300},
		},
	}}
	result, err := runTool(t, "query_metrics", &ToolDeps{logManager: fake}, map[string]any{
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
func TestWarpMetricsSummarizesCostHistogramKeepsModelList(t *testing.T) {
	fake := &fakeLogReader{costHistogramResult: &logstore.CostHistogramResult{
		Buckets: []logstore.CostHistogramBucket{
			{TotalCost: 1.5, ByModel: map[string]float64{"gpt-4o": 1.5}},
			{TotalCost: 2.5, ByModel: map[string]float64{"gpt-4o": 2.5}},
		},
		Models: []string{"gpt-4o"},
	}}
	result, err := runTool(t, "query_metrics", &ToolDeps{logManager: fake}, map[string]any{
		"filters": map[string]any{}, "metrics": []any{"cost"},
	})
	require.NoError(t, err)

	cost := result.(map[string]any)["cost"].(map[string]any)
	total := cost["total_cost"].(seriesSummary)
	require.InDelta(t, 4.0, *total.Total, 0.001)
	require.Equal(t, []string{"gpt-4o"}, cost["models"])
	require.NotContains(t, cost, "by_model", "a per-bucket, per-model series is the exact nested detail a summary must not reintroduce")
}

// The cost summary lists model names but no per-model amounts. Asked for
// "model-wise spend", a model that reached query_metrics first saw names with
// no numbers beside them and told the user per-model spend was unsupported.
// The result says where the per-model figures are instead.
func TestWarpMetricsCostPointsToPerModelSpend(t *testing.T) {
	fake := &fakeLogReader{costHistogramResult: &logstore.CostHistogramResult{Models: []string{"gpt-4o"}}}
	result, err := runTool(t, "query_metrics", &ToolDeps{logManager: fake}, map[string]any{
		"filters": map[string]any{}, "metrics": []any{"cost"},
	})
	require.NoError(t, err)
	cost := result.(map[string]any)["cost"].(map[string]any)
	require.Contains(t, cost["per_model_cost"], "query_model_performance")
}

// query_model_performance is the tool that answers per-model spend - every row
// carries total, input and output cost - but its description only said
// "usage", so a spend question was routed elsewhere.
func TestWarpModelPerformanceAdvertisesSpend(t *testing.T) {
	tool, ok := toolByName(buildTools(), "query_model_performance")
	require.True(t, ok)
	for _, phrase := range []string{"spend", "input cost", "output cost", "model-wise"} {
		require.Contains(t, tool.description, phrase)
	}
}

func TestWarpMetricsSummarizesThroughputHistogram(t *testing.T) {
	fake := &fakeLogReader{throughputHistogramResult: &logstore.ThroughputHistogramResult{
		Buckets: []logstore.ThroughputHistogramBucket{
			{TokensPerSecond: 10, TotalCompletionTokens: 100, TotalRequests: 5},
			{TokensPerSecond: 20, TotalCompletionTokens: 200, TotalRequests: 10},
		},
	}}
	result, err := runTool(t, "query_metrics", &ToolDeps{logManager: fake}, map[string]any{
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
func TestWarpMetricsProviderGroupedStaysRawWithCoarseBuckets(t *testing.T) {
	previous := Now
	Now = func() time.Time { return time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC) }
	defer func() { Now = previous }()

	fake := &fakeLogReader{providerLatencyHistogramResult: &logstore.ProviderLatencyHistogramResult{
		Buckets:   []logstore.ProviderLatencyHistogramBucket{{ByProvider: map[string]logstore.ProviderLatencyStats{"openai": {AvgLatency: 100}}}},
		Providers: []string{"openai"},
	}}
	result, err := runTool(t, "query_metrics", &ToolDeps{logManager: fake}, map[string]any{
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

// A per-provider total must come from the store, not from the model adding up
// buckets. The coarse buckets are aligned to the bucket size, so the first one
// starts before the window and, on Postgres, is read from whole matview hours.
// A model summing them dropped that first bucket as out of range and reported
// Anthropic $0.20 short of the dashboard's total over the same window.
func TestWarpMetricsProviderGroupedReportsExactTotals(t *testing.T) {
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
	result, err := runTool(t, "query_metrics", &ToolDeps{logManager: fake}, map[string]any{
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
func TestWarpMetricsSummaryOnlyProviderGroupingReturnsTotals(t *testing.T) {
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
	result, err := runTool(t, "query_metrics", &ToolDeps{logManager: fake}, map[string]any{
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

// "requests" has no GetProvider*Histogram counterpart, unlike every other
// metric. Silently falling back to the ungrouped histogram would answer a
// different question than "requests per provider" and look identical to a
// real breakdown, so the combination must be rejected instead.
func TestWarpMetricsRequestsRejectsProviderGrouping(t *testing.T) {
	fake := &fakeLogReader{}
	_, err := runTool(t, "query_metrics", &ToolDeps{logManager: fake}, map[string]any{
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
func TestWarpToolsPassCallerContextToStore(t *testing.T) {
	type scopeKey struct{}
	fake := &fakeLogReader{}
	tool, ok := toolByName(buildTools(), "query_logs")
	require.True(t, ok)

	ctx := context.WithValue(context.Background(), scopeKey{}, "caller-scope")
	_, err := tool.execute(ctx, &ToolDeps{logManager: fake}, map[string]any{"filters": map[string]any{}})
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
func TestWarpProjectLogExposesStructuredErrorFields(t *testing.T) {
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
	row := projectLog(entry, false, LogContentChars)
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
	row = projectLog(statusOnly, false, LogContentChars)
	require.Equal(t, "service unavailable", row.ErrorMessage)
	require.Empty(t, row.ErrorType)
	require.Equal(t, 503, row.StatusCode)
}

func TestWarpGetLogDetailRequiresID(t *testing.T) {
	_, err := runTool(t, "get_log_detail", &ToolDeps{logManager: &fakeLogReader{}}, map[string]any{})
	require.ErrorContains(t, err, "log_id is required")
}

// Logged traffic is the least predictable data in the system: a tool-call turn
// carries nil Content, and an offloaded payload leaves the parsed history empty.
// Content is a pointer, so an unguarded read here panics on a real log row.
func TestWarpLogContentHandlesNilMessageContent(t *testing.T) {
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
		row := projectLog(entry, true, LogContentChars)
		require.Contains(t, row.Content, "hello")
	})
}

// Log content is arbitrary user text and is routinely non-ASCII. Cutting at a
// byte offset splits a multi-byte rune, and the tool result then carries
// invalid UTF-8 that serializes as a replacement character - the model sees
// mojibake where the original character was. The budgets are documented as
// character counts, so the cut has to be rune-based too.
func TestWarpTruncateTextCutsOnRuneBoundaries(t *testing.T) {
	for name, text := range map[string]string{
		"cjk":      strings.Repeat("日本語", 40),
		"emoji":    strings.Repeat("🚀", 40),
		"accented": strings.Repeat("café", 40),
		"mixed":    strings.Repeat("aé日🚀", 30),
	} {
		for _, limit := range []int{1, 7, 10, 33, 50} {
			got := truncateText(text, limit)
			require.True(t, utf8.ValidString(got),
				"%s at limit %d produced invalid UTF-8: %q", name, limit, got)

			trimmed := strings.TrimSuffix(got, "... [truncated]")
			require.LessOrEqual(t, utf8.RuneCountInString(trimmed), limit,
				"%s at limit %d kept more than %d characters", name, limit, limit)
		}
	}

	// Short text is returned whole, and the budget counts characters, so a
	// string of `limit` runes must not be truncated even though it is longer
	// than `limit` bytes.
	exact := strings.Repeat("日", 10)
	require.Equal(t, exact, truncateText(exact, 10))
	require.Equal(t, "ascii", truncateText("ascii", 10))
}

// describe_filter_space exists so the model stops guessing filter values. A
// description that names a dimension the result never carries causes the exact
// failure the tool was added to prevent: the model trusts the promise, asks for
// providers, gets nothing back, and filters on a guess anyway. So the prose and
// the result map have to agree in both directions.
func TestWarpDescribeFilterSpaceDescriptionMatchesResult(t *testing.T) {
	tool, ok := toolByName(buildTools(), "describe_filter_space")
	require.True(t, ok)

	result, err := tool.execute(context.Background(), &ToolDeps{logManager: &fakeFilterSpaceReader{}}, map[string]any{})
	require.NoError(t, err)
	returned, ok := result.(map[string]any)
	require.True(t, ok, "describe_filter_space must return a map")

	// The prose name each result key is advertised under.
	names := map[string]string{
		"models":       "models",
		"virtual_keys": "virtual keys",
		"apps":         "apps",
		"stop_reasons": "stop reasons",
		// The merged tool also answers "who is asking and what could they mean",
		// so the hierarchy it enumerates is advertised the same way.
		"teams":                "teams",
		"customers":            "customers",
		"business_units":       "business units",
		"caller_is_identified": "who is asking",
		"default_scope":        "who is asking",
		// What routed a request, from the lookups behind the Logs page's filters.
		"routing_rules":   "routing rules",
		"provider_keys":   "provider keys",
		"aliases":         "aliases",
		"routing_engines": "routing engines",
		"tool_call_names": "tool call names",
		"metadata":        "metadata keys",
	}
	for key := range returned {
		phrase, known := names[key]
		require.True(t, known, "result key %q has no known prose name; add one here and to the description", key)
		require.Contains(t, tool.description, phrase,
			"description must advertise %q, which the tool returns", key)
	}

	// And nothing it cannot deliver. providers is the live example: query_logs
	// accepts a providers filter, but LogReader has no provider-listing method,
	// so this tool cannot enumerate them and must not claim to.
	for key, phrase := range names {
		if _, returns := returned[key]; !returns {
			require.NotContains(t, tool.description, phrase,
				"description advertises %q but the tool does not return it", key)
		}
	}
	require.NotContains(t, tool.description, "providers",
		"describe_filter_space cannot enumerate providers: LogReader has no provider-listing method")
}

// fakeFilterSpaceReader implements only the four listing methods
// describe_filter_space reaches, so any new call panics loudly.
type fakeFilterSpaceReader struct {
	LogReaderStub
}

func (f *fakeFilterSpaceReader) GetAvailableModels(_ context.Context, _ int, query string) ([]string, error) {
	return matchingValues(query, "gpt-4o"), nil
}

// Nothing routed in this deployment: the lookups exist and come back empty.
func (f *fakeFilterSpaceReader) GetAvailableRoutingRules(context.Context, int, string) ([]KeyPair, error) {
	return nil, nil
}
func (f *fakeFilterSpaceReader) GetAvailableSelectedKeys(context.Context, int, string) ([]KeyPair, error) {
	return nil, nil
}
func (f *fakeFilterSpaceReader) GetAvailableAliases(context.Context, int, string) ([]string, error) {
	return nil, nil
}
func (f *fakeFilterSpaceReader) GetAvailableRoutingEngines(context.Context, int, string) ([]string, error) {
	return nil, nil
}
func (f *fakeFilterSpaceReader) GetAvailableToolCallNames(context.Context, int, string) ([]string, error) {
	return nil, nil
}
func (f *fakeFilterSpaceReader) GetAvailableMetadataKeys(context.Context, int, string) (map[string][]string, error) {
	return nil, nil
}
func (f *fakeFilterSpaceReader) GetAvailableApps(_ context.Context, _ int, query string) ([]string, error) {
	return matchingValues(query, "dashboard"), nil
}
func (f *fakeFilterSpaceReader) GetAvailableStopReasons(_ context.Context, _ int, query string) ([]string, error) {
	return matchingValues(query, "stop"), nil
}
func (f *fakeFilterSpaceReader) GetAvailableTeams(_ context.Context, _ int, query string) ([]KeyPair, error) {
	return matchingPairs(query, KeyPair{ID: "team-1", Name: "Team One"}), nil
}
func (f *fakeFilterSpaceReader) GetAvailableCustomers(_ context.Context, _ int, query string) ([]KeyPair, error) {
	return matchingPairs(query, KeyPair{ID: "cust-1", Name: "Customer One"}), nil
}
func (f *fakeFilterSpaceReader) GetAvailableBusinessUnits(_ context.Context, _ int, query string) ([]KeyPair, error) {
	return matchingPairs(query, KeyPair{ID: "bu-1", Name: "Unit One"}), nil
}

// matchingValues and matchingPairs apply search the way the store does: a
// case-insensitive substring of the value (or its name), empty matching all.
func matchingValues(query string, values ...string) []string {
	out := []string{}
	for _, v := range values {
		if strings.Contains(strings.ToLower(v), strings.ToLower(query)) {
			out = append(out, v)
		}
	}
	return out
}

func matchingPairs(query string, pairs ...KeyPair) []KeyPair {
	out := []KeyPair{}
	for _, p := range pairs {
		if strings.Contains(strings.ToLower(p.Name), strings.ToLower(query)) || strings.Contains(strings.ToLower(p.ID), strings.ToLower(query)) {
			out = append(out, p)
		}
	}
	return out
}
func (f *fakeFilterSpaceReader) GetAvailableVirtualKeys(_ context.Context, _ int, query string) ([]KeyPair, error) {
	return matchingPairs(query, KeyPair{ID: "vk-1", Name: "default"}), nil
}

// The same principle parseFilters already applies to unknown field names: a
// filter that is silently dropped answers a broader question than the one
// asked, and neither the model nor the reader can tell. A wrong-shaped value
// went the other way - stringSlice and floatPtr returned nothing, applyFilters
// skipped the filter, and the query widened without a word.
func TestWarpFilterRejectsWrongShapedValues(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	caller := Scope{HasIdentity: true, UserID: "u-1"}
	for name, filters := range map[string]map[string]any{
		"array is not an array":      {"providers": "openai"},
		"array holds a number":       {"models": []any{"gpt-4o", 42}},
		"array holds an object":      {"team_ids": []any{map[string]any{"id": "t-1"}}},
		"numeric filter is a string": {"min_cost": "0.02"},
		"numeric filter is an array": {"max_latency": []any{500}},
		"content search is a number": {"content_search": 42},
	} {
		_, err := filterArg(map[string]any{"filters": filters}, now, caller)
		require.Error(t, err, name)
	}

	// Well-shaped values still parse.
	parsed, err := filterArg(map[string]any{"filters": map[string]any{
		"providers": []any{"openai"}, "min_cost": 0.02, "content_search": "declined",
	}}, now, caller)
	require.NoError(t, err)
	require.Equal(t, []string{"openai"}, parsed.Providers)
	require.InDelta(t, 0.02, *parsed.MinCost, 1e-9)
}

// Unbounded arrays and an unbounded substring both turn into query predicates,
// and this package is required to bound what it hands the store.
func TestWarpFilterBoundsInputSize(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	caller := Scope{HasIdentity: true, UserID: "u-1"}
	huge := make([]any, MaxFilterValues+1)
	for i := range huge {
		huge[i] = fmt.Sprintf("model-%d", i)
	}
	_, err := filterArg(map[string]any{"filters": map[string]any{"models": huge}}, now, caller)
	require.ErrorContains(t, err, "models")

	_, err = filterArg(map[string]any{"filters": map[string]any{
		"content_search": strings.Repeat("x", MaxContentSearchChars+1),
	}}, now, caller)
	require.ErrorContains(t, err, "content_search")

	// At the limit is fine.
	atLimit := make([]any, MaxFilterValues)
	for i := range atLimit {
		atLimit[i] = fmt.Sprintf("model-%d", i)
	}
	_, err = filterArg(map[string]any{"filters": map[string]any{"models": atLimit}}, now, caller)
	require.NoError(t, err)

	// The limit counts characters, as the schema's maxLength does - not bytes.
	// 500 two-byte runes passed the advertised schema limit and then failed
	// here, with the error reporting a byte count as a character count.
	_, err = filterArg(map[string]any{"filters": map[string]any{
		"content_search": strings.Repeat("é", MaxContentSearchChars),
	}}, now, caller)
	require.NoError(t, err, "a search at the advertised character limit must be accepted whatever its byte length")
	_, err = filterArg(map[string]any{"filters": map[string]any{
		"content_search": strings.Repeat("é", MaxContentSearchChars+1),
	}}, now, caller)
	require.ErrorContains(t, err, fmt.Sprintf("%d characters", MaxContentSearchChars+1),
		"the rejection must report the character count, not the byte count")
}

// group_by: "provider" is accepted for every metric, but GetHistogram has no
// provider breakdown - so asking for requests per provider returned
// deployment-wide totals under a heading that says otherwise.
func TestWarpMetricsRejectsUnsupportedProviderGrouping(t *testing.T) {
	_, err := runTool(t, "query_metrics", &ToolDeps{logManager: &fakeLogReader{}}, map[string]any{
		"filters":  map[string]any{},
		"metrics":  []any{"requests"},
		"group_by": "provider",
	})
	require.ErrorContains(t, err, "requests")

	// Requests without grouping is still the ordinary path.
	_, err = runTool(t, "query_metrics", &ToolDeps{logManager: &fakeLogReader{}}, map[string]any{
		"filters": map[string]any{}, "metrics": []any{"cost"}, "group_by": "none",
	})
	require.NoError(t, err)
}

// The declared schema is advertised to the model, not enforced on the way back:
// chatTools forwards the JSON schema to the provider, and nothing validates the
// arguments that return. A group_by the schema never offered therefore fell
// through to the ungrouped branch, so the model asked for one breakdown and got
// aggregates for another - with no error to tell it apart from a real answer.
func TestWarpMetricsRejectsUnknownGrouping(t *testing.T) {
	for _, groupBy := range []string{"model", "virtual_key", "PROVIDER", " provider"} {
		_, err := runTool(t, "query_metrics", &ToolDeps{logManager: &fakeLogReader{}}, map[string]any{
			"filters": map[string]any{}, "metrics": []any{"cost"}, "group_by": groupBy,
		})
		require.ErrorContains(t, err, "group_by", "group_by %q must be rejected, not silently ungrouped", groupBy)
	}

	// The three the schema does offer must still work, including omitting it.
	for _, args := range []map[string]any{
		{"filters": map[string]any{}, "metrics": []any{"cost"}},
		{"filters": map[string]any{}, "metrics": []any{"cost"}, "group_by": ""},
		{"filters": map[string]any{}, "metrics": []any{"cost"}, "group_by": "none"},
		{"filters": map[string]any{}, "metrics": []any{"cost"}, "group_by": "provider"},
	} {
		_, err := runTool(t, "query_metrics", &ToolDeps{logManager: &fakeLogReader{}}, args)
		require.NoError(t, err)
	}
}

// A non-object `filters` was discarded by the type assertion and became nil,
// which parseFilters reads as "no filters" - so a malformed argument widened the
// query to the default unfiltered 24 hours instead of failing it. Silently
// broadening a query is the dangerous direction: the model gets more data than
// it asked for and no signal that its filter was ignored.
func TestWarpFilterRejectsNonObjectFilters(t *testing.T) {
	for _, filters := range []any{"last 24h", []any{"openai"}, 42.0, true} {
		_, err := runTool(t, "query_logs", &ToolDeps{logManager: &fakeLogReader{}}, map[string]any{
			"filters": filters,
		})
		require.ErrorContains(t, err, "filters", "filters %#v must be rejected, not dropped", filters)
	}

	// Absent and empty both legitimately mean "no filters".
	_, err := runTool(t, "query_logs", &ToolDeps{logManager: &fakeLogReader{}}, map[string]any{})
	require.NoError(t, err)
	_, err = runTool(t, "query_logs", &ToolDeps{logManager: &fakeLogReader{}}, map[string]any{"filters": map[string]any{}})
	require.NoError(t, err)
}

// sort_by and order are advertised as enums and never checked on the way back.
//
// searchLogs converts an unknown SortBy to timestamp and any Order that is not
// "asc" to DESC, so a malformed value runs a different query and returns rows
// that look like an answer to the question asked. Same failure as an unchecked
// group_by: the model is never told its argument was ignored.
func TestWarpQueryLogsRejectsUnknownSortAndOrder(t *testing.T) {
	for _, sortBy := range []string{"duration", "TIMESTAMP", "cost "} {
		_, err := runTool(t, "query_logs", &ToolDeps{logManager: &fakeLogReader{}}, map[string]any{
			"filters": map[string]any{}, "sort_by": sortBy,
		})
		require.ErrorContains(t, err, "sort_by", "sort_by %q must be rejected, not silently changed", sortBy)
	}
	for _, order := range []string{"ascending", "DESC", "up"} {
		_, err := runTool(t, "query_logs", &ToolDeps{logManager: &fakeLogReader{}}, map[string]any{
			"filters": map[string]any{}, "order": order,
		})
		require.ErrorContains(t, err, "order", "order %q must be rejected", order)
	}

	// Everything the schema does offer, plus omitting them, must still work.
	for _, args := range []map[string]any{
		{"filters": map[string]any{}},
		{"filters": map[string]any{}, "sort_by": "timestamp", "order": "asc"},
		{"filters": map[string]any{}, "sort_by": "latency", "order": "desc"},
		{"filters": map[string]any{}, "sort_by": "tokens"},
		{"filters": map[string]any{}, "sort_by": "cost"},
	} {
		_, err := runTool(t, "query_logs", &ToolDeps{logManager: &fakeLogReader{}}, args)
		require.NoError(t, err, "args %v are within the schema", args)
	}
}

// A time value of the wrong shape must fail, not fall back to the default
// window. Returning nil for a present non-string turned a malformed argument
// into a valid query over different dates - the model asked about March and was
// answered about the last 24 hours, with nothing marking the difference.
func TestWarpFilterRejectsWrongShapedTimes(t *testing.T) {
	for _, value := range []any{42.0, true, []any{"2026-09-01"}, map[string]any{}} {
		for _, field := range []string{"start_time", "end_time"} {
			_, err := runTool(t, "query_logs", &ToolDeps{logManager: &fakeLogReader{}}, map[string]any{
				"filters": map[string]any{field: value},
			})
			require.ErrorContains(t, err, field, "%s of type %T must be rejected", field, value)
		}
	}

	// A blank string is not a timestamp either.
	_, err := runTool(t, "query_logs", &ToolDeps{logManager: &fakeLogReader{}}, map[string]any{
		"filters": map[string]any{"start_time": "   "},
	})
	require.ErrorContains(t, err, "start_time")

	// Absent still means the default window, which is the documented behaviour.
	_, err = runTool(t, "query_logs", &ToolDeps{logManager: &fakeLogReader{}}, map[string]any{
		"filters": map[string]any{},
	})
	require.NoError(t, err)
}

// end_time is documented as defaulting to now, so a model spelling that out as
// "now" is asking for the default, not sending a malformed value. Rejecting it
// failed the whole tool call, and the model told the user it needed a valid end
// time before it could answer a question with no time in it at all.
func TestWarpParseTimeAcceptsNow(t *testing.T) {
	now := time.Date(2026, 9, 21, 11, 6, 0, 0, time.UTC)
	for _, text := range []string{"now", "NOW", " now "} {
		parsed, err := parseTime(text, now)
		require.NoError(t, err, text)
		require.Equal(t, now, *parsed, text)
	}
	_, err := runTool(t, "query_model_performance", &ToolDeps{logManager: &fakeLogReader{}}, map[string]any{
		"filters": map[string]any{"start_time": "-7d", "end_time": "now"},
	})
	require.NoError(t, err)
	_, err = parseTime("nowish", now)
	require.Error(t, err, "only the exact word is the current time")
}

// Every case here used to produce a successful answer to a question nobody
// asked: a different window, a broader filter, or a silently narrowed result.
func TestWarpToolArgsRejectMalformedValues(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	t.Run("relative offsets reject trailing text", func(t *testing.T) {
		for _, text := range []string{"-7daysd", "-7d-extrad", "-d", "-0d", "--7d", "-NaNd", "-Infd"} {
			_, err := parseTime(text, now)
			require.Error(t, err, "offset %q must be rejected, not read as its numeric prefix", text)
		}
		for text, want := range map[string]time.Duration{
			"-7d":   7 * 24 * time.Hour,
			"-1d":   24 * time.Hour,
			"-0.5d": 12 * time.Hour,
			"-30d":  30 * 24 * time.Hour,
		} {
			parsed, err := parseTime(text, now)
			require.NoError(t, err, text)
			require.Equal(t, now.Add(-want), *parsed, text)
		}
	})

	t.Run("filter arrays reject empty elements", func(t *testing.T) {
		// models: [""] used to drop the filter entirely and run unfiltered.
		_, err := stringSliceField(map[string]any{"models": []any{""}}, "models")
		require.ErrorContains(t, err, "models[0] must not be empty")
		_, err = stringSliceField(map[string]any{"models": []any{"gpt-4o", "  "}}, "models")
		require.ErrorContains(t, err, "models[1] must not be empty")
		values, err := stringSliceField(map[string]any{"models": []any{"gpt-4o"}}, "models")
		require.NoError(t, err)
		require.Equal(t, []string{"gpt-4o"}, values)
	})

	t.Run("boolean flags reject wrong types", func(t *testing.T) {
		// A non-boolean read as false answered without the content that was
		// asked for, indistinguishably from a deployment that stores none.
		for _, value := range []any{"true", 1, []any{true}} {
			_, err := boolArg(map[string]any{"include_content": value}, "include_content")
			require.ErrorContains(t, err, "include_content must be a boolean")
		}
		flag, err := boolArg(map[string]any{"include_content": true}, "include_content")
		require.NoError(t, err)
		require.True(t, flag)
		flag, err = boolArg(map[string]any{}, "include_content")
		require.NoError(t, err)
		require.False(t, flag)
	})

	t.Run("enum lists reject bad elements by index", func(t *testing.T) {
		allowed := []string{"summary", "cost"}
		_, err := enumSliceArg(map[string]any{"metrics": []any{"cost", 42}}, "metrics", "metric", allowed, maxQueryMetrics)
		require.ErrorContains(t, err, "metrics[1] must be a string")
		_, err = enumSliceArg(map[string]any{"metrics": []any{"vibes"}}, "metrics", "metric", allowed, maxQueryMetrics)
		require.ErrorContains(t, err, "unknown metric at metrics[0]")
		_, err = enumSliceArg(map[string]any{"metrics": []any{}}, "metrics", "metric", allowed, maxQueryMetrics)
		require.ErrorContains(t, err, "must list at least one")
		_, err = enumSliceArg(map[string]any{}, "metrics", "metric", allowed, maxQueryMetrics)
		require.ErrorContains(t, err, "must list at least one")
		values, err := enumSliceArg(map[string]any{"metrics": []any{"cost", "summary"}}, "metrics", "metric", allowed, maxQueryMetrics)
		require.NoError(t, err)
		require.Equal(t, []string{"cost", "summary"}, values)
	})
}

// Each case here used to succeed with a different question than the one asked:
// a widened filter, a different limit, a default window, or a hundred queries.
func TestWarpToolArgsRejectMalformedValuesRoundTwo(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)

	t.Run("limit rejects present-but-unusable values", func(t *testing.T) {
		for _, bad := range []any{"ten", -5.0, 0.0, 2.5, []any{1}} {
			_, err := intArg(map[string]any{"limit": bad}, "limit", 10, 25)
			require.Error(t, err, "limit %v must be rejected, not replaced with the default", bad)
		}
		// Absent still means the default, and the cap still clamps.
		value, err := intArg(map[string]any{}, "limit", 10, 25)
		require.NoError(t, err)
		require.Equal(t, 10, value)
		value, err = intArg(map[string]any{"limit": 500.0}, "limit", 10, 25)
		require.NoError(t, err)
		require.Equal(t, 25, value, "the cap clamps rather than failing the call")
	})

	t.Run("an explicit null time is not an absent one", func(t *testing.T) {
		// FilterSchema declares both as strings, so null was never in the
		// contract - but indexing the map gives nil for absent and null alike, so
		// it silently took the default window.
		_, err := parseFilters(map[string]any{"start_time": nil}, now)
		require.ErrorContains(t, err, "start_time must be a string, got null")
		_, err = parseFilters(map[string]any{"end_time": nil}, now)
		require.ErrorContains(t, err, "end_time must be a string, got null")
		// Omitting the field is still how you ask for the default.
		filters, err := parseFilters(map[string]any{}, now)
		require.NoError(t, err)
		require.NotNil(t, filters.StartTime)
	})

	t.Run("the metrics list is bounded and deduplicated", func(t *testing.T) {
		allowed := []string{"summary", "requests", "tokens", "cost", "latency", "throughput"}
		// Each metric is its own database query, so a repeated valid value is a
		// repeated query - the schema's maxItems never bound anything at runtime.
		long := make([]any, 0, 200)
		for range 200 {
			long = append(long, "cost")
		}
		_, err := enumSliceArg(map[string]any{"metrics": long}, "metrics", "metric", allowed, maxQueryMetrics)
		require.ErrorContains(t, err, "at most")
		_, err = enumSliceArg(map[string]any{"metrics": []any{"cost", "cost"}}, "metrics", "metric", allowed, maxQueryMetrics)
		require.ErrorContains(t, err, "repeats")
		// Five distinct valid metrics still overrun the schema's advertised
		// maxItems of 4 - the vocabulary has six values, so "every element is
		// valid and unique" alone exceeds the published contract by two queries.
		_, err = enumSliceArg(map[string]any{"metrics": []any{"summary", "requests", "tokens", "cost", "latency"}}, "metrics", "metric", allowed, maxQueryMetrics)
		require.ErrorContains(t, err, "at most 4")
		values, err := enumSliceArg(map[string]any{"metrics": []any{"cost", "summary"}}, "metrics", "metric", allowed, maxQueryMetrics)
		require.NoError(t, err)
		require.Equal(t, []string{"cost", "summary"}, values)
	})

	t.Run("only histogram metrics need a bucket", func(t *testing.T) {
		require.False(t, isHistogramMetric("summary"))
		for _, metric := range []string{"requests", "tokens", "cost", "latency", "throughput"} {
			require.True(t, isHistogramMetric(metric), metric)
		}
	})
}

// An empty content_search reached the logstore as if the field were omitted -
// the query applies the predicate only when ContentSearch is non-empty - so a
// filtered question came back with unfiltered rows and nothing said so.
func TestWarpContentSearchRejectsEmptyValues(t *testing.T) {
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	for _, bad := range []any{"", "   ", nil} {
		_, err := parseFilters(map[string]any{"content_search": bad}, now)
		require.Error(t, err, "content_search %v must be rejected rather than silently dropped", bad)
	}
	// Omitting it is still how you search without a content filter.
	filters, err := parseFilters(map[string]any{}, now)
	require.NoError(t, err)
	require.Empty(t, filters.ContentSearch)

	filters, err = parseFilters(map[string]any{"content_search": "card declined"}, now)
	require.NoError(t, err)
	require.Equal(t, "card declined", filters.ContentSearch)
}

// An explicitly empty or null filter array asks for a filter and then names
// nothing to filter on. stringSliceField returned nil for both, applyFilters
// skips a zero-length slice, and the query ran unfiltered - a broader answer
// than the one asked for, with nothing saying the filter had been dropped.
func TestWarpFilterArraysRejectEmptyAndNull(t *testing.T) {
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
func TestWarpStringArgRejectsWrongShapes(t *testing.T) {
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

// describe_scope reads its dimension lists from rankings, so the fake answers
// those too - with nothing, which is enough to exercise the payload shape.
func (f *fakeFilterSpaceReader) GetDimensionRankings(context.Context, *logstore.SearchFilters, logstore.RankingDimension) (*logstore.DimensionRankingResult, error) {
	return &logstore.DimensionRankingResult{}, nil
}

// The caller-only note must only be used when the caller is the whole story.
//
// parseFilters fills each dimension independently, so a filter can carry the
// caller's own user id *and* a team. Reporting that as "scoped to the person
// asking" tells the model to say something narrower than the query actually
// covers - the exact failure the note exists to prevent.
func TestWarpScopeNoteNamesEveryDimension(t *testing.T) {
	caller := Scope{HasIdentity: true, UserID: "u-1"}

	onlyCaller := &logstore.SearchFilters{UserIDs: []string{"u-1"}}
	require.Equal(t, "self", scopeNote(onlyCaller, caller))

	for name, filters := range map[string]*logstore.SearchFilters{
		"with a team":          {UserIDs: []string{"u-1"}, TeamIDs: []string{"team-1"}},
		"with a customer":      {UserIDs: []string{"u-1"}, CustomerIDs: []string{"cust-1"}},
		"with a business unit": {UserIDs: []string{"u-1"}, BusinessUnitIDs: []string{"bu-1"}},
		"with a virtual key":   {UserIDs: []string{"u-1"}, VirtualKeyIDs: []string{"vk-1"}},
		"with a project":       {UserIDs: []string{"u-1"}, ProjectIDs: []string{"proj-1"}},
	} {
		// Not "self": the caller is one of several dimensions here, and reporting
		// it as caller-only tells the model the answer is narrower than the query.
		require.Equal(t, "named", scopeNote(filters, caller), name)
	}
}

// The dimension ranking result must stay flat once the scope note is added.
//
// DimensionRankingResult already serializes as {"rankings": [...], "dimension":
// ..., totals}, so wrapping it in another {"rankings": result} produced
// rankings.rankings and buried the dimension and totals a level down. The model
// reads this JSON directly: a shape it does not expect is not a parse error, it
// is an answer built on fields the model could not find.
func TestWarpDimensionRankingShapeStaysFlat(t *testing.T) {
	out, err := runTool(t, "query_usage_by", &ToolDeps{logManager: &fakeLogReader{}}, map[string]any{
		"dimension": "user",
		"filters":   map[string]any{},
	})
	require.NoError(t, err)

	encoded, err := sonic.Marshal(out)
	require.NoError(t, err)
	var shape map[string]any
	require.NoError(t, sonic.Unmarshal(encoded, &shape))

	require.Contains(t, shape, "rankings")
	require.Contains(t, shape, "scope", "the scope note rides alongside, not instead of, the result")
	require.Contains(t, shape, "window", "the resolved window rides alongside too")
}

// The prompt tells the model to say when it is looking at a sample rather than
// the whole set, but "returned" and "total_matching" leave it to infer that by
// comparing two numbers - and an inferred caveat is the one it drops. An
// explicit flag is the signal the instruction can actually key on.
func TestWarpQueryLogsMarksSampledResults(t *testing.T) {
	rows := func(n int) []logstore.Log {
		out := make([]logstore.Log, n)
		for i := range out {
			out[i].ID = fmt.Sprintf("req-%d", i)
		}
		return out
	}

	// Fewer rows than matched: a sample.
	fake := &fakeLogReader{searchResult: &logstore.SearchResult{
		Logs:       rows(10),
		Pagination: logstore.PaginationOptions{Limit: 10},
	}}
	fake.searchResult.Pagination.TotalCount = 1200
	result, err := runTool(t, "query_logs", &ToolDeps{logManager: fake}, map[string]any{"filters": map[string]any{}})
	require.NoError(t, err)
	out := result.(map[string]any)
	require.Equal(t, true, out["sampled"], "10 of 1200 rows is a sample and must say so")

	// Everything that matched: not a sample.
	fake = &fakeLogReader{searchResult: &logstore.SearchResult{
		Logs:       rows(3),
		Pagination: logstore.PaginationOptions{Limit: 10},
	}}
	fake.searchResult.Pagination.TotalCount = 3
	result, err = runTool(t, "query_logs", &ToolDeps{logManager: fake}, map[string]any{"filters": map[string]any{}})
	require.NoError(t, err)
	out = result.(map[string]any)
	require.Equal(t, false, out["sampled"], "every matching row was returned")
}

// The semantic tool is only usable where an embedding executor was configured.
// Declaring it regardless means the model is told a capability exists, spends a
// step calling it, and gets an error back - and on a deployment with no
// embedding provider that is every single time it tries.
func TestWarpToolsOmitSemanticSearchWhenUnavailable(t *testing.T) {
	withSearcher := buildToolsFor(&SemanticSearcher{})
	_, present := toolByName(withSearcher, SemanticSearchToolName)
	require.True(t, present, "a configured deployment still offers semantic search")

	without := buildToolsFor(nil)
	_, present = toolByName(without, SemanticSearchToolName)
	require.False(t, present, "a tool that cannot run must not be advertised to the model")

	// Everything else is still there, so the agent is not crippled by the gap.
	for _, name := range []string{"query_logs", "query_metrics", "describe_filter_space"} {
		_, ok := toolByName(without, name)
		require.True(t, ok, "%s must still be offered", name)
	}
}

// Rows carry a link to their own detail sheet and every listing carries a
// link to the same filters in the Logs view, so a reader can open what Warp
// summarised instead of retyping the filters by hand.
func TestWarpListingsCarryDashboardLinks(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	oldNow := Now
	Now = func() time.Time { return now }
	defer func() { Now = oldNow }()
	fake := &fakeLogReader{searchResult: &logstore.SearchResult{
		Logs: []logstore.Log{{ID: "req-9", Timestamp: now.Add(-time.Hour), Provider: "gemini", Model: "gemini-3.1-flash-lite", Status: "success"}},
	}}
	result, err := runTool(t, "query_logs", &ToolDeps{logManager: fake}, map[string]any{
		"filters": map[string]any{"providers": []any{"gemini"}, "start_time": "-7d"},
	})
	require.NoError(t, err)
	response := result.(map[string]any)
	rows := response["rows"].([]logRow)
	require.Len(t, rows, 1)
	require.Equal(t, "/workspace/logs?selected_log=req-9", rows[0].Link)
	link, _ := response["logs_link"].(string)
	require.Contains(t, link, "/workspace/logs?")
	require.Contains(t, link, "providers=gemini")

	counted, err := runTool(t, "count_logs", &ToolDeps{logManager: fake}, map[string]any{"filters": map[string]any{"providers": []any{"gemini"}}})
	require.NoError(t, err)
	require.Contains(t, counted.(map[string]any)["logs_link"], "providers=gemini")
}

// "Tell me about the failure rate" was answered from query_metrics filtered to
// status success,error - both are needed for a rate - so the only link on the
// result opened every request, and "Open the filtered logs" showed 1,226 rows
// instead of the 72 failures. A result that reports a success rate must also
// carry a link narrowed to the failures, and none when nothing failed.
func TestWarpSuccessRateResultsCarryFailuresLink(t *testing.T) {
	args := map[string]any{"filters": map[string]any{"start_time": "-7d", "status": []any{"success", "error"}}}

	fake := &fakeLogReader{statsResponses: []*logstore.SearchStats{{TotalRequests: 1226, SuccessRate: 94.13}, {TotalRequests: 1226, SuccessRate: 94.13}}}
	metrics, err := runTool(t, "query_metrics", &ToolDeps{logManager: fake}, map[string]any{"filters": args["filters"], "metrics": []any{"summary"}})
	require.NoError(t, err)
	counted, err := runTool(t, "count_logs", &ToolDeps{logManager: fake}, args)
	require.NoError(t, err)
	for name, result := range map[string]any{"query_metrics": metrics, "count_logs": counted} {
		link, _ := result.(map[string]any)["failures_link"].(string)
		require.Regexp(t, `status=error(&|$)`, link, name)
		require.NotContains(t, link, "success", name)
	}

	clean := &fakeLogReader{statsResponses: []*logstore.SearchStats{{TotalRequests: 10, SuccessRate: 100}}}
	counted, err = runTool(t, "count_logs", &ToolDeps{logManager: clean}, args)
	require.NoError(t, err)
	require.NotContains(t, counted.(map[string]any), "failures_link", "nothing failed, so there is nothing to link to")

	// An empty window reports a 0% success rate, which is not 72 failures: the
	// link would open an empty page.
	empty := &fakeLogReader{statsResponses: []*logstore.SearchStats{{TotalRequests: 0, SuccessRate: 0}, {TotalRequests: 0, SuccessRate: 0}}}
	metrics, err = runTool(t, "query_metrics", &ToolDeps{logManager: empty}, map[string]any{"filters": args["filters"], "metrics": []any{"summary"}})
	require.NoError(t, err)
	counted, err = runTool(t, "count_logs", &ToolDeps{logManager: empty}, args)
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
func TestWarpFiltersNeverNarrowToWarp(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	for _, apps := range [][]any{{"Warp"}, {"warp"}, {"Claude Code", "Warp"}} {
		_, err := filterArg(map[string]any{"filters": map[string]any{"apps": apps}}, now, Scope{})
		require.Error(t, err, "apps %v", apps)
		require.Contains(t, err.Error(), "query_usage_by")
		require.NotContains(t, err.Error(), `scope "warp"`, "the error must not point at another way to narrow to Warp")
	}

	_, err := filterArg(map[string]any{"filters": map[string]any{"scope": "warp"}}, now, Scope{})
	require.Error(t, err, `scope "warp" is how the same misreading got through`)

	filters, err := filterArg(map[string]any{"filters": map[string]any{"apps": []any{"Claude Code"}}}, now, Scope{})
	require.NoError(t, err)
	require.Equal(t, []string{"Claude Code"}, filters.Apps)
}

// "Show me the failed requests in the past week" matched 145 rows, and the
// over-the-cap guidance only offered narrowing or slicing - so the model told
// the person it could not list them and asked how to narrow, while the
// logs_link it was holding already opened exactly that set in the Logs view.
// Past the row cap, the guidance must hand over the link as the answer to a
// "show me" question rather than make narrowing the only path.
func TestWarpCountLogsOverCapPointsAtLogsLink(t *testing.T) {
	for _, total := range []int64{MaxLogRows + 120, LargeResultThreshold + 1} {
		fake := &fakeLogReader{statsResponses: []*logstore.SearchStats{{TotalRequests: total}}}
		counted, err := runTool(t, "count_logs", &ToolDeps{logManager: fake}, map[string]any{
			"filters": map[string]any{"status": []any{"error"}, "start_time": "-7d"},
		})
		require.NoError(t, err)
		guidance, _ := counted.(map[string]any)["guidance"].(string)
		require.Contains(t, guidance, "logs_link", "total %d: guidance must offer the filtered Logs view", total)

		// A query the Logs page cannot reproduce gets no logs_link, so the
		// guidance must not hand over a link the result does not carry.
		fake = &fakeLogReader{statsResponses: []*logstore.SearchStats{{TotalRequests: total}}}
		counted, err = runTool(t, "count_logs", &ToolDeps{logManager: fake}, map[string]any{
			"filters": map[string]any{"status_codes": []any{float64(429)}, "start_time": "-7d"},
		})
		require.NoError(t, err)
		require.NotContains(t, counted.(map[string]any), "logs_link")
		guidance, _ = counted.(map[string]any)["guidance"].(string)
		require.NotContains(t, guidance, "logs_link", "total %d: no link to offer", total)
		require.Contains(t, guidance, "narrow", "total %d: narrowing or slicing is the path left", total)
	}
}

// The warp-scope provenance block the prompt requires needs an absolute
// window on every answer with numbers, but only query_metrics used to report
// one - every other flow resolved a window internally (to filter rows) and
// then threw it away, leaving the model to recompute "-7d" as an absolute
// date from the current-time reference by hand. Every flow that resolves a
// window now reports it back, in the same format, so there is nothing left to
// recompute.
func TestWarpToolsReportResolvedWindow(t *testing.T) {
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
			result, err := runTool(t, tc.tool, &ToolDeps{logManager: &fakeLogReader{}}, tc.args)
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
func TestWarpDescribeFilterSpaceUsesDistinctLookups(t *testing.T) {
	fake := &fakeLogReader{
		availableTeams:       []KeyPair{{ID: "t1", Name: "Payments"}, {ID: "t2", Name: ""}},
		availableCustomers:   []KeyPair{{ID: "c1", Name: "Acme"}},
		availableVirtualKeys: []KeyPair{{ID: "vk1", Name: "prod"}},
	}
	result, err := runTool(t, "describe_filter_space", &ToolDeps{logManager: fake, scope: Scope{}}, map[string]any{})
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
func TestWarpDescribeFilterSpaceMergesScopeAndFilterValues(t *testing.T) {
	fake := &fakeLogReader{
		availableModels:      []string{"gpt-4o"},
		availableApps:        []string{"cli"},
		availableStopReasons: []string{"stop", "length"},
		availableTeams:       []KeyPair{{ID: "t1", Name: "Payments"}},
	}
	result, err := runTool(t, "describe_filter_space", &ToolDeps{logManager: fake, scope: Scope{HasIdentity: true, UserID: "user-7"}}, map[string]any{})
	require.NoError(t, err)
	out := result.(map[string]any)

	// The describe_scope half.
	require.Equal(t, true, out["caller_is_identified"])
	require.Equal(t, "the person asking", out["default_scope"])
	require.Equal(t, "user-7", out["caller_user_id"])
	require.Equal(t, []string{"Payments (t1)"}, out["teams"])

	// The describe_filter_space half.
	require.Equal(t, []string{"gpt-4o"}, out["models"])
	require.Equal(t, []string{"cli"}, out["apps"])
	require.Equal(t, []string{"stop", "length"}, out["stop_reasons"])
}

func TestWarpDescribeFilterSpaceDefaultScopeWhenUnidentified(t *testing.T) {
	// Not runTool: it defaults an empty Scope to an identified caller as a
	// convenience for the majority of tests, which is exactly the case this one
	// needs to observe, so the tool is executed directly.
	tool, ok := toolByName(buildTools(), "describe_filter_space")
	require.True(t, ok)
	result, err := tool.execute(context.Background(), &ToolDeps{logManager: &fakeLogReader{}, scope: Scope{}}, map[string]any{})
	require.NoError(t, err)
	out := result.(map[string]any)
	require.Equal(t, false, out["caller_is_identified"])
	// This string is read at the moment the model decides how to ask. It used to
	// say only "ask which team, customer or business unit is meant", and the
	// replies echoed it as prose - "I need you to pick a scope first: team,
	// customer, or business unit." - with nothing to click. It names the tool.
	require.Contains(t, out["default_scope"], AskUserTool)
	require.Contains(t, out["default_scope"], "never in prose")
	require.NotContains(t, out, "caller_user_id")
}

// describe_scope used to hardcode "" for the org-hierarchy lookups
// regardless of what the model passed, which was a real gap once the tools
// merged and search became meaningful across all of them. Every lookup - not
// just models/apps/stop_reasons - must see the same search string now.
func TestWarpDescribeFilterSpaceSearchReachesEveryLookup(t *testing.T) {
	fake := &fakeLogReader{}
	_, err := runTool(t, "describe_filter_space", &ToolDeps{logManager: fake}, map[string]any{"search": "pay"})
	require.NoError(t, err)
	require.Equal(t, "pay", fake.teamsQuerySeen)
	require.Equal(t, "pay", fake.customersQuerySeen)
	require.Equal(t, "pay", fake.businessUnitsQuerySeen)
	require.Equal(t, "pay", fake.virtualKeysQuerySeen)
}

// Asked what was causing 17 invalid_request_error failures, Warp could count them
// and had no way to fetch them: rows filter on status, not on error type. It
// pulled the newest failures of every kind, traced three overloaded_errors, ran
// out of steps - and on "try again" compared the count against fail_reason, a
// tally of retry attempts, and reported that the requests were fine. The rows
// behind an error ranking are one filter away.
func TestWarpFiltersAcceptErrorTypeCodeAndStatus(t *testing.T) {
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
	_, err = runTool(t, "query_logs", &ToolDeps{logManager: fake}, map[string]any{
		"filters": map[string]any{"start_time": "-7d", "error_types": []any{"invalid_request_error"}},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"invalid_request_error"}, fake.searchFilters.ErrorTypes)
}

// The Logs page has no error-type, error-code or status-code filter, so a link
// built from such a query would open every failure while sitting beside a count
// of seventeen. No link is better than one that is silently wider; each row
// still carries its own.
func TestWarpResultsFilteredByErrorFieldsCarryNoLogsLink(t *testing.T) {
	for _, field := range []string{"error_types", "error_codes", "status_codes"} {
		value := []any{"invalid_request_error"}
		if field == "status_codes" {
			value = []any{float64(400)}
		}
		result, err := runTool(t, "count_logs", &ToolDeps{logManager: &fakeLogReader{}}, map[string]any{
			"filters": map[string]any{"start_time": "-7d", field: value},
		})
		require.NoError(t, err)
		require.NotContains(t, result.(map[string]any), "logs_link", "filtered by %s", field)
		require.NotContains(t, result.(map[string]any), "failures_link", "filtered by %s", field)
	}
	result, err := runTool(t, "count_logs", &ToolDeps{logManager: &fakeLogReader{}}, map[string]any{
		"filters": map[string]any{"start_time": "-7d", "status": []any{"error"}},
	})
	require.NoError(t, err)
	require.Contains(t, result.(map[string]any), "logs_link")
}

func TestWarpGuidesFromAnErrorRankingToItsRows(t *testing.T) {
	content := systemInstructions(&schemas.WarpConfig{}, true)
	require.Contains(t, content, "query_logs with error_types")
	require.Contains(t, content, "status_code")
	require.Contains(t, content, "fail_reason counts retry attempts")

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
func TestWarpToolsAcceptEveryLogsFilter(t *testing.T) {
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
func TestWarpDescribeFilterSpaceListsRoutingValues(t *testing.T) {
	result, err := runTool(t, "describe_filter_space", &ToolDeps{logManager: &fakeLogReader{}}, map[string]any{})
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
func TestWarpRanksAndLinksRoutingDimensions(t *testing.T) {
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
		_, rows := rankingRowsJSON(t, "query_usage_by", &ToolDeps{logManager: fake},
			map[string]any{"dimension": dimension, "filters": map[string]any{"start_time": "-7d"}}, "rankings")
		require.Len(t, rows, 1, dimension)
		require.Equal(t, "id-1", linkQuery(t, rows[0]["link"]).Get(param), dimension)
	}

	content := systemInstructions(&schemas.WarpConfig{}, true)
	require.Contains(t, content, "Per routing rule, provider key, alias, routing engine, complexity tier or tool call: query_usage_by")
	require.Contains(t, content, "how a routing rule is configured")
}

// A row says which rule, key and alias handled it, so a list of requests can be
// read without tracing each one.
func TestWarpLogRowsCarryRoutingFields(t *testing.T) {
	rule, ruleName, alias, tier := "rule-premium", "Premium tier", "smart", "COMPLEX"
	fake := &fakeLogReader{searchResult: &logstore.SearchResult{Logs: []logstore.Log{{
		ID: "req-1", Timestamp: Now(), Provider: "anthropic", Model: "claude-sonnet-5", Status: "success",
		RoutingRuleID: &rule, RoutingRuleName: &ruleName, SelectedKeyName: "anthropic-primary", Alias: &alias, ComplexityTier: &tier,
		ToolCallNames: []string{"get_weather"},
	}}}}
	out, err := runTool(t, "query_logs", &ToolDeps{logManager: fake}, map[string]any{"filters": map[string]any{"start_time": "-1d"}})
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

// search narrows by part of a value's name. A live run passed {"search":"team"}
// to find teams, got back only teams whose names contain "team" - none - and
// told the person no team had traffic. The schema says what search is, and an
// empty filtered result says how to recover rather than reading as a census.
func TestWarpDescribeFilterSpaceSearchIsANameFragment(t *testing.T) {
	tool, ok := toolByName(buildTools(), "describe_filter_space")
	require.True(t, ok)
	require.Contains(t, tool.schemaJSON, "never a category such as")

	result, err := tool.execute(context.Background(), &ToolDeps{logManager: &fakeFilterSpaceReader{}}, map[string]any{"search": "customer segment"})
	require.NoError(t, err)
	guidance, _ := result.(map[string]any)["guidance"].(string)
	require.Contains(t, guidance, `Nothing matched search "customer segment"`)
	require.Contains(t, guidance, "without search")

	unfiltered, err := tool.execute(context.Background(), &ToolDeps{logManager: &fakeFilterSpaceReader{}}, map[string]any{})
	require.NoError(t, err)
	require.NotContains(t, unfiltered.(map[string]any), "guidance", "an unfiltered listing is the census; it needs no recovery hint")
}

// Live runs asked query_usage_by for a model breakdown (no such dimension, so
// the model gave up) and answered "which provider errors most" with the
// selected_key ranking, naming an API key instead of the provider.
func TestWarpUsageByPointsProviderAndModelBreakdownsElsewhere(t *testing.T) {
	tool, ok := toolByName(buildTools(), "query_usage_by")
	require.True(t, ok)
	require.Contains(t, tool.description, "There is no provider or model dimension")
	require.Contains(t, tool.description, "selected_key (the provider API key Bifrost sent the request with, by name - not the provider")
}

// A live run filtered error_codes: ["overloaded_error"]. That is an error type,
// error_code is empty for Anthropic, so the incident matched zero rows.
func TestWarpErrorCodesFilterSendsErrorTypesElsewhere(t *testing.T) {
	require.Contains(t, FilterSchema, "overloaded_error and rate_limit_error are error types")
}

// Told there is no model dimension, a live run ranked by alias instead to
// answer "was the incident isolated to one model?". The requests named their
// models directly, so every alias was empty, and Warp concluded the overload
// "affected Anthropic broadly" when all 30 failures were one model.
func TestWarpAliasIsNotAModelStandIn(t *testing.T) {
	tool, ok := toolByName(buildTools(), "query_usage_by")
	require.True(t, ok)
	require.Contains(t, tool.description, "empty when a request named its model directly - never a stand-in for the model")
}

// Two further live runs still asked query_usage_by for dimension "model", got
// the generic "unknown dimension" list, and then ranked by alias or gave up.
// The model reads the error at the moment it retries, so the error names the
// tool that answers the question it was trying to ask.
func TestWarpUsageByRedirectsModelAndProviderDimensions(t *testing.T) {
	for dimension, want := range map[string]string{
		"model":    "query_model_performance",
		"models":   "query_model_performance",
		"provider": "query_metrics with group_by provider",
	} {
		_, err := runTool(t, "query_usage_by", &ToolDeps{logManager: &fakeFilterSpaceReader{}}, map[string]any{"dimension": dimension, "filters": map[string]any{"start_time": "-7d"}})
		require.Error(t, err, dimension)
		require.Contains(t, err.Error(), want, dimension)
		require.Contains(t, err.Error(), "same filters", dimension)
	}
}

// query_metrics reduced every ungrouped series to one summary, and grouped ones
// to a dozen buckets that do not line up with days, so "errors per day this
// week" had no single call: a live run made a count_logs call per day - seven
// to nine calls for a table, or for the numbers behind a chart it then drew in
// mermaid. interval returns the series bucket by bucket at an hour or a day.
func TestWarpQueryMetricsIntervalReturnsBuckets(t *testing.T) {
	day := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	fake := &fakeLogReader{histogramResult: &logstore.HistogramResult{
		BucketSizeSeconds: 86400,
		Buckets: []logstore.HistogramBucket{
			{Timestamp: day, Count: 10, Success: 9, Error: 1},
			{Timestamp: day.Add(24 * time.Hour), Count: 12, Success: 8, Error: 4},
		},
	}}
	out, err := runTool(t, "query_metrics", &ToolDeps{logManager: fake}, map[string]any{
		"filters": map[string]any{"start_time": "-7d"}, "metrics": []any{"requests"}, "interval": "day",
	})
	require.NoError(t, err)
	require.Equal(t, int64(86400), fake.histogramBucket)
	encoded, err := sonic.Marshal(out)
	require.NoError(t, err)
	var shape struct {
		Requests struct {
			Buckets []logstore.HistogramBucket `json:"buckets"`
		} `json:"requests"`
	}
	require.NoError(t, sonic.Unmarshal(encoded, &shape))
	require.Len(t, shape.Requests.Buckets, 2, "a day interval returns every bucket, not a summary: %s", encoded)
	require.Equal(t, int64(4), shape.Requests.Buckets[1].Error)

	_, err = runTool(t, "query_metrics", &ToolDeps{logManager: &fakeLogReader{}}, map[string]any{
		"filters": map[string]any{"start_time": "-30d"}, "metrics": []any{"requests"}, "interval": "hour",
	})
	require.ErrorContains(t, err, `interval "day"`, "720 hourly buckets is over the cap; the error names the way out")

	_, err = runTool(t, "query_metrics", &ToolDeps{logManager: &fakeLogReader{}}, map[string]any{
		"filters": map[string]any{"start_time": "-7d"}, "metrics": []any{"cost"}, "group_by": "provider", "interval": "day",
	})
	require.ErrorContains(t, err, "interval")
}
