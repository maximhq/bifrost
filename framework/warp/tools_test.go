package warp

import (
	"context"
	"fmt"
	"github.com/bytedance/sonic"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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
	sawContext context.Context
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
	return &logstore.DimensionRankingResult{
		Dimension: dimension,
		Rankings:  []logstore.DimensionRankingWithTrend{{}},
	}, nil
}

func (f *fakeLogReader) GetModelRankings(ctx context.Context, filters *logstore.SearchFilters) (*logstore.ModelRankingResult, error) {
	f.sawContext = ctx
	f.rankingFilters = filters
	return &logstore.ModelRankingResult{}, nil
}

func (f *fakeLogReader) GetStats(ctx context.Context, filters *logstore.SearchFilters) (*logstore.SearchStats, error) {
	f.sawContext = ctx
	f.statsCalled = true
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

func (f *fakeFilterSpaceReader) GetAvailableModels(context.Context, int, string) ([]string, error) {
	return []string{"gpt-4o"}, nil
}
func (f *fakeFilterSpaceReader) GetAvailableApps(context.Context, int, string) ([]string, error) {
	return []string{"dashboard"}, nil
}
func (f *fakeFilterSpaceReader) GetAvailableStopReasons(context.Context, int, string) ([]string, error) {
	return []string{"stop"}, nil
}
func (f *fakeFilterSpaceReader) GetAvailableVirtualKeys(context.Context, int, string) ([]KeyPair, error) {
	return []KeyPair{{ID: "vk-1", Name: "default"}}, nil
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

// GetProviderCostHistogram lets the provider-grouped path actually run.
// LogReaderStub embeds a nil LogReader, so every method it does not override
// panics - which is why the grouped branch was previously only ever asserted on
// its rejection, never on its success.
func (f *fakeLogReader) GetProviderCostHistogram(_ context.Context, _ *logstore.SearchFilters, bucketSizeSeconds int64) (*logstore.ProviderCostHistogramResult, error) {
	f.histogramBucket = bucketSizeSeconds
	return &logstore.ProviderCostHistogramResult{}, nil
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

// describe_scope's result goes to the configured model, which is frequently a
// third-party provider. The model needs to know *whether* the caller is
// identified so it can decide whether to ask whose traffic is meant; the stable
// id itself is only ever used server-side by applyScope, so sending it is
// identity data leaving the deployment for no benefit.
func TestWarpDescribeScopeDoesNotLeakCallerUserID(t *testing.T) {
	tool, ok := toolByName(buildTools(), "describe_scope")
	require.True(t, ok)

	deps := &ToolDeps{logManager: &fakeLogReader{}, scope: Scope{HasIdentity: true, UserID: "u-secret-42"}}
	result, err := tool.execute(context.Background(), deps, map[string]any{})
	require.NoError(t, err)

	out := result.(map[string]any)
	require.Equal(t, true, out["caller_is_identified"], "the model still needs to know an identity exists")
	require.NotContains(t, out, "caller_user_id", "the caller's stable id must not reach the model")

	encoded := boundToolResult(out)
	require.NotContains(t, encoded, "u-secret-42", "the id must not reach the model by any key")
}

// An identified caller who asks about everyone's traffic gets narrowed to their
// own, because "named no scope" and "explicitly asked for all" were the same
// empty filter. The two have to be distinguishable, and row-level queryscope
// still bounds what "all" can actually return.
func TestWarpFilterScopeAllBypassesCallerDefault(t *testing.T) {
	caller := Scope{HasIdentity: true, UserID: "u-1"}
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	// Omitted: the caller default still applies, which is the safe reading of a
	// question that did not say whose traffic it meant.
	defaulted, err := filterArg(map[string]any{"filters": map[string]any{}}, now, caller)
	require.NoError(t, err)
	require.Equal(t, []string{"u-1"}, defaulted.UserIDs)

	// Explicit: the caller asked about the whole deployment and must get it.
	all, err := filterArg(map[string]any{"filters": map[string]any{"scope": "all"}}, now, caller)
	require.NoError(t, err)
	require.Empty(t, all.UserIDs, `scope "all" must not be narrowed back to the caller`)

	// Explicit caller scope stays explicit.
	mine, err := filterArg(map[string]any{"filters": map[string]any{"scope": "caller"}}, now, caller)
	require.NoError(t, err)
	require.Equal(t, []string{"u-1"}, mine.UserIDs)

	// A named dimension still wins over the default, unchanged.
	named, err := filterArg(map[string]any{"filters": map[string]any{"team_ids": []any{"t-1"}}}, now, caller)
	require.NoError(t, err)
	require.Empty(t, named.UserIDs)
	require.Equal(t, []string{"t-1"}, named.TeamIDs)

	// An unrecognised value is rejected rather than silently read as "caller":
	// guessing here would answer a different question than the one asked.
	_, err = filterArg(map[string]any{"filters": map[string]any{"scope": "everyone"}}, now, caller)
	require.ErrorContains(t, err, "scope")
}

// Two things the explicit scope marker got wrong.
//
// "caller" without a caller is not a scope at all: applyScope returns early
// without an identity, so the query runs with no traffic dimension and - where
// no queryscope is set - covers everything. Silently answering a different
// question than the one asked is the failure this whole mechanism exists to
// prevent, so it is refused.
//
// And "all" is not the whole deployment: ScopedDB still applies the caller's
// queryscope, so the result is only what they are permitted to see. Telling the
// model otherwise puts a claim in the answer that the data does not support.
func TestWarpScopeMarkerTellsTheTruthAboutCoverage(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	anonymous := Scope{}
	_, err := filterArg(map[string]any{"filters": map[string]any{"scope": "caller"}}, now, anonymous)
	require.Error(t, err, `"caller" with no caller identity must be refused, not answered deployment-wide`)
	require.ErrorContains(t, err, "caller")

	// Omitted is still fine for an anonymous caller - that is the OSS path, and
	// queryscope (where present) is what bounds it.
	_, err = filterArg(map[string]any{"filters": map[string]any{}}, now, anonymous)
	require.NoError(t, err)

	// The note must not promise more than the row filter allows.
	note := scopeNote(&logstore.SearchFilters{}, Scope{HasIdentity: true, UserID: "u-1"})
	require.NotContains(t, note, "whole deployment",
		"queryscope still limits the rows, so the note must not claim deployment-wide coverage")
	require.Contains(t, note, "permitted")
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
	require.Contains(t, scopeNote(onlyCaller, caller), "the person asking")

	for name, filters := range map[string]*logstore.SearchFilters{
		"with a team":          {UserIDs: []string{"u-1"}, TeamIDs: []string{"team-1"}},
		"with a customer":      {UserIDs: []string{"u-1"}, CustomerIDs: []string{"cust-1"}},
		"with a business unit": {UserIDs: []string{"u-1"}, BusinessUnitIDs: []string{"bu-1"}},
		"with a virtual key":   {UserIDs: []string{"u-1"}, VirtualKeyIDs: []string{"vk-1"}},
	} {
		note := scopeNote(filters, caller)
		require.NotContains(t, note, "the person asking",
			"%s: the caller is not the only dimension, so the note must not claim they are", name)
		require.Contains(t, note, "dimensions named in the filters", name)
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
	out, err := runTool(t, "query_user_usage", &ToolDeps{logManager: &fakeLogReader{}}, map[string]any{
		"filters": map[string]any{},
	})
	require.NoError(t, err)

	encoded, err := sonic.Marshal(out)
	require.NoError(t, err)
	var shape map[string]any
	require.NoError(t, sonic.Unmarshal(encoded, &shape))

	require.Contains(t, shape, "rankings")
	require.IsType(t, []any{}, shape["rankings"], "rankings must be the list itself, not a nested object")
	require.Contains(t, shape, "dimension", "the dimension must stay top-level")
	require.Contains(t, shape, "scope", "the scope note rides alongside, not instead of, the result")
}

// An explicit "caller" scope must be applied, even alongside a named dimension.
//
// applyScope returned early whenever any dimension was named, so
// scope:"caller" plus team_ids became a team-wide query with the caller
// dropped. The store applies UserIDs and each dimension as separate WHERE
// clauses, so the two intersect - which is what "my traffic in that team"
// means, and what the mode documents. Dropping the user filter answers about
// everyone in the team while the model reports it as the caller's own.
func TestWarpCallerScopeIntersectsNamedDimensions(t *testing.T) {
	caller := Scope{HasIdentity: true, UserID: "u-1"}
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	for name, dimension := range map[string]map[string]any{
		"team":          {"team_ids": []any{"team-1"}},
		"customer":      {"customer_ids": []any{"cust-1"}},
		"business unit": {"business_unit_ids": []any{"bu-1"}},
		"virtual key":   {"virtual_key_ids": []any{"vk-1"}},
	} {
		raw := map[string]any{"scope": "caller"}
		for key, value := range dimension {
			raw[key] = value
		}
		filters, err := filterArg(map[string]any{"filters": raw}, now, caller)
		require.NoError(t, err, name)
		require.Equal(t, []string{"u-1"}, filters.UserIDs,
			"%s: an explicit caller scope must survive alongside the named dimension", name)
	}

	// The named dimension must still stand on its own when no scope is given -
	// narrowing "how did team X do?" to the asker would answer a different
	// question.
	filters, err := filterArg(map[string]any{
		"filters": map[string]any{"team_ids": []any{"team-1"}},
	}, now, caller)
	require.NoError(t, err)
	require.Empty(t, filters.UserIDs, "an unscoped question about a team is about the team")

	// And "all" still widens.
	filters, err = filterArg(map[string]any{
		"filters": map[string]any{"scope": "all", "team_ids": []any{"team-1"}},
	}, now, caller)
	require.NoError(t, err)
	require.Empty(t, filters.UserIDs)
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
