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
// tools reach are implemented; the rest of LogReader is embedded as a
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
	sawContext      context.Context
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
	return &logstore.SearchStats{}, nil
}

func (f *fakeLogReader) GetCostHistogram(ctx context.Context, filters *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.CostHistogramResult, error) {
	f.sawContext = ctx
	f.histogramBucket = bucketSizeSeconds
	return &logstore.CostHistogramResult{}, nil
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

	declared, err := chatTools(tools)
	require.NoError(t, err)
	require.Len(t, declared, len(tools))

	for _, tool := range declared {
		require.NotNil(t, tool.Function, "tool must declare a function")
		require.NotEmpty(t, tool.Function.Name)
		require.NotNil(t, tool.Function.Description)
		require.NotEmpty(t, *tool.Function.Description, "%s needs a description; it is the only thing telling the model when to use it", tool.Function.Name)
		require.NotNil(t, tool.Function.Parameters)
		require.Equal(t, "object", tool.Function.Parameters.Type)
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

	_, err := runTool(t, "query_virtual_key_usage", deps, map[string]any{
		"filters": map[string]any{},
		"limit":   float64(9999),
	})
	require.NoError(t, err)
	require.NotNil(t, fake.rankingFilters.RankingLimit)
	require.Equal(t, MaxRankingRows, *fake.rankingFilters.RankingLimit)
	require.Equal(t, logstore.RankingDimensionVirtualKey, fake.rankingDimension)
}

func TestWarpUserFlowUsesUserDimension(t *testing.T) {
	fake := &fakeLogReader{}
	_, err := runTool(t, "query_user_usage", &ToolDeps{logManager: fake}, map[string]any{
		"filters": map[string]any{},
	})
	require.NoError(t, err)
	require.Equal(t, logstore.RankingDimensionUser, fake.rankingDimension)
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
