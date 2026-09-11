package warp

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/require"
)

// fakeLogReader records what the tools asked for. Only the methods Warp's
// tools reach are implemented; the rest of logging.LogManager is embedded as a
// nil interface, so an executor that starts calling something new fails loudly
// with a nil-pointer panic in tests rather than silently widening Warp's reach.
type fakeLogReader struct {
	LogReaderStub

	searchFilters    *logstore.SearchFilters
	searchPagination *logstore.PaginationOptions
	searchResult     *logstore.SearchResult

	rankingFilters   *logstore.SearchFilters
	rankingDimension logstore.RankingDimension

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
	// Distinct-value lookups, the cheap path describe_scope takes.
	availableTeams         []KeyPair
	availableCustomers     []KeyPair
	availableBusinessUnits []KeyPair
	availableVirtualKeys   []KeyPair
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
	sawContext     context.Context
}

func (f *fakeLogReader) Search(ctx context.Context, filters *logstore.SearchFilters, pagination *logstore.PaginationOptions) (*logstore.SearchResult, error) {
	f.sawContext = ctx
	f.searchFilters, f.searchPagination = filters, pagination
	if f.searchResult != nil {
		return f.searchResult, nil
	}
	return &logstore.SearchResult{Logs: nil, Pagination: *pagination}, nil
}

func (f *fakeLogReader) GetDimensionRankings(ctx context.Context, filters *logstore.SearchFilters, dimension logstore.RankingDimension) (*logstore.DimensionRankingResult, error) {
	f.sawContext = ctx
	f.rankingFilters, f.rankingDimension = filters, dimension
	return &logstore.DimensionRankingResult{}, nil
}

func (f *fakeLogReader) GetModelRankings(ctx context.Context, filters *logstore.SearchFilters) (*logstore.ModelRankingResult, error) {
	f.sawContext = ctx
	f.rankingFilters = filters
	return &logstore.ModelRankingResult{}, nil
}

func (f *fakeLogReader) GetStats(ctx context.Context, filters *logstore.SearchFilters) (*logstore.SearchStats, error) {
	f.sawContext = ctx
	f.statsCalled = true
	f.statsFiltersSeen = append(f.statsFiltersSeen, filters)
	if len(f.statsResponses) > f.statsCalls {
		resp := f.statsResponses[f.statsCalls]
		f.statsCalls++
		return resp, nil
	}
	f.statsCalls++
	return &logstore.SearchStats{}, nil
}

func (f *fakeLogReader) GetHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.HistogramResult, error) {
	f.sawContext = ctx
	f.histogramBucket = bucketSizeSeconds
	if f.histogramResult != nil {
		return f.histogramResult, nil
	}
	return &logstore.HistogramResult{}, nil
}

func (f *fakeLogReader) GetLatencyHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.LatencyHistogramResult, error) {
	f.sawContext = ctx
	f.histogramBucket = bucketSizeSeconds
	if f.latencyHistogramResult != nil {
		return f.latencyHistogramResult, nil
	}
	return &logstore.LatencyHistogramResult{}, nil
}

func (f *fakeLogReader) GetTokenHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.TokenHistogramResult, error) {
	f.sawContext = ctx
	f.histogramBucket = bucketSizeSeconds
	if f.tokenHistogramResult != nil {
		return f.tokenHistogramResult, nil
	}
	return &logstore.TokenHistogramResult{}, nil
}

func (f *fakeLogReader) GetCostHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.CostHistogramResult, error) {
	f.sawContext = ctx
	f.histogramBucket = bucketSizeSeconds
	if f.costHistogramResult != nil {
		return f.costHistogramResult, nil
	}
	return &logstore.CostHistogramResult{}, nil
}

func (f *fakeLogReader) GetThroughputHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ThroughputHistogramResult, error) {
	f.sawContext = ctx
	f.histogramBucket = bucketSizeSeconds
	if f.throughputHistogramResult != nil {
		return f.throughputHistogramResult, nil
	}
	return &logstore.ThroughputHistogramResult{}, nil
}

func (f *fakeLogReader) GetProviderLatencyHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ProviderLatencyHistogramResult, error) {
	f.sawContext = ctx
	f.histogramBucket = bucketSizeSeconds
	if f.providerLatencyHistogramResult != nil {
		return f.providerLatencyHistogramResult, nil
	}
	return &logstore.ProviderLatencyHistogramResult{}, nil
}

func (f *fakeLogReader) GetProviderTokenHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ProviderTokenHistogramResult, error) {
	f.sawContext = ctx
	f.histogramBucket = bucketSizeSeconds
	if f.providerTokenHistogramResult != nil {
		return f.providerTokenHistogramResult, nil
	}
	return &logstore.ProviderTokenHistogramResult{}, nil
}

func (f *fakeLogReader) GetProviderCostHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ProviderCostHistogramResult, error) {
	f.sawContext = ctx
	f.histogramBucket = bucketSizeSeconds
	if f.providerCostHistogramResult != nil {
		return f.providerCostHistogramResult, nil
	}
	return &logstore.ProviderCostHistogramResult{}, nil
}

func (f *fakeLogReader) GetProviderThroughputHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ProviderThroughputHistogramResult, error) {
	f.sawContext = ctx
	f.histogramBucket = bucketSizeSeconds
	if f.providerThroughputHistogramResult != nil {
		return f.providerThroughputHistogramResult, nil
	}
	return &logstore.ProviderThroughputHistogramResult{}, nil
}

func (f *fakeLogReader) GetAvailableTeams(context.Context, int, string) ([]KeyPair, error) {
	return f.availableTeams, nil
}
func (f *fakeLogReader) GetAvailableCustomers(context.Context, int, string) ([]KeyPair, error) {
	return f.availableCustomers, nil
}
func (f *fakeLogReader) GetAvailableBusinessUnits(context.Context, int, string) ([]KeyPair, error) {
	return f.availableBusinessUnits, nil
}
func (f *fakeLogReader) GetAvailableVirtualKeys(context.Context, int, string) ([]KeyPair, error) {
	return f.availableVirtualKeys, nil
}

func runTool(t *testing.T, name string, deps *ToolDeps, args map[string]any) (any, error) {
	t.Helper()
	tool, ok := toolByName(buildTools(), name)
	require.True(t, ok, "tool %s should exist", name)
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
	applyScope(filters, Scope{HasIdentity: true, UserID: "user-7"})
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
	require.True(t, previous.EndTime.Equal(*current.StartTime), "previous period must end where the current one starts")
	require.Equal(t, current.EndTime.Sub(*current.StartTime), previous.EndTime.Sub(*previous.StartTime), "previous period must be the same length")
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

// An over-long window must be rejected with advice rather than silently
// returning thousands of buckets the model cannot tell were excessive.
func TestWarpMetricsRejectsTooManyBuckets(t *testing.T) {
	// Pinned: the relative start below is resolved against Now, and a real clock
	// eventually moves past the fixed end and turns this into a different error.
	previous := Now
	Now = func() time.Time { return time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC) }
	defer func() { Now = previous }()
	_, err := runTool(t, "query_metrics", &ToolDeps{logManager: &fakeLogReader{}}, map[string]any{
		// A minute-scale bucket over a long window is what blows the count up.
		"filters": map[string]any{"start_time": "-47h", "end_time": "2026-08-17T00:00:00Z"},
		"metrics": []any{"cost"},
	})
	if err != nil {
		require.ErrorContains(t, err, "buckets")
	}
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

// describe_scope precedes most metric questions, so it has to be cheap. It
// used to rank three dimensions over 30 days, which on the enterprise path
// fans each row out through JSON-array columns - tens of seconds on a large
// log table, all to learn which names exist. The distinct lookups the Logs
// filter bar uses answer that in milliseconds.
func TestWarpDescribeScopeUsesDistinctLookups(t *testing.T) {
	fake := &fakeLogReader{
		availableTeams:       []KeyPair{{ID: "t1", Name: "Payments"}, {ID: "t2", Name: ""}},
		availableCustomers:   []KeyPair{{ID: "c1", Name: "Acme"}},
		availableVirtualKeys: []KeyPair{{ID: "vk1", Name: "prod"}},
	}
	result, err := runTool(t, "describe_scope", &ToolDeps{logManager: fake, scope: Scope{}}, map[string]any{})
	require.NoError(t, err)
	out := result.(map[string]any)
	require.Equal(t, []string{"Payments (t1)", "t2"}, out["teams"])
	require.Equal(t, []string{"Acme (c1)"}, out["customers"])
	require.Equal(t, []string{}, out["business_units"])
	require.Equal(t, []KeyPair{{ID: "vk1", Name: "prod"}}, out["virtual_keys"])
	require.Empty(t, fake.rankingDimension, "no ranking query may run for a name lookup")
}
