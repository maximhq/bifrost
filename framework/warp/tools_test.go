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
	statsCalled     bool
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

func (f *fakeLogReader) GetCostHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.CostHistogramResult, error) {
	f.sawContext = ctx
	f.histogramBucket = bucketSizeSeconds
	return &logstore.CostHistogramResult{}, nil
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
