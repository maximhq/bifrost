package mcptools

import (
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/require"
)

// Every row and every aggregate the tools report can be opened in the Logs view.
// The links are built here, server-side, so the model never has to guess the
// dashboard's URL scheme - it only has to repeat what it was given.
func TestLogDetailLink(t *testing.T) {
	require.Equal(t, "/workspace/logs?selected_log=req-1", logDetailLink("req-1"))
	require.Equal(t, "/workspace/logs?selected_log=a%2Fb", logDetailLink("a/b"), "ids are escaped")
	require.Empty(t, logDetailLink(""), "no id, no link")
}

func TestLogsViewLinkEncodesFilters(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(24 * time.Hour)
	filters := &logstore.SearchFilters{
		Providers: []string{"gemini", "openai"},
		Models:    []string{"gemini-3.1-flash-lite"},
		Status:    []string{"success"},
		UserIDs:   []string{"u-1"},
		StartTime: &start,
		EndTime:   &end,
	}
	link := logsViewLink(filters)
	require.True(t, len(link) > len("/workspace/logs?"))
	require.Contains(t, link, "providers=gemini%2Copenai")
	require.Contains(t, link, "models=gemini-3.1-flash-lite")
	require.Contains(t, link, "status=success")
	require.Contains(t, link, "user_ids=u-1")
	// The Logs page keys its window on unix seconds, and only honours a window
	// when both ends are present.
	require.Contains(t, link, "start_time=1788220800")
	require.Contains(t, link, "end_time=1788307200")
}

func TestLogsViewLinkOmitsEmptyFilters(t *testing.T) {
	require.Equal(t, "/workspace/logs", logsViewLink(&logstore.SearchFilters{}))
	require.Equal(t, "/workspace/logs", logsViewLink(nil))
	// A half-open window is dropped rather than sent as one side only, which the
	// Logs page would ignore in favour of its default hour.
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	require.Equal(t, "/workspace/logs", logsViewLink(&logstore.SearchFilters{StartTime: &start}))
}

// content_search has a URL parameter on the Logs page, so a link that drops it
// sends the reader to a wider result set than the number they clicked from.
func TestLogsLinkCarriesContentSearch(t *testing.T) {
	search := "payment declined"
	link := logsViewLink(&logstore.SearchFilters{ContentSearch: search, Models: []string{"gpt-4o"}})
	require.Contains(t, link, "content_search=payment+declined")
	require.Contains(t, link, "models=gpt-4o")
}

// A link that drops the latency and cost bounds opens a wider set than the
// number it was generated from, and nothing about the page says so.
func TestLogsViewLinkCarriesNumericBounds(t *testing.T) {
	minLatency, maxLatency, minCost, maxCost := 400.0, 1500.5, 0.0, 0.002
	link := logsViewLink(&logstore.SearchFilters{
		MinLatency: &minLatency, MaxLatency: &maxLatency, MinCost: &minCost, MaxCost: &maxCost,
	})
	parsed, err := url.Parse(link)
	require.NoError(t, err)
	query := parsed.Query()
	require.Equal(t, "400", query.Get("min_latency"))
	require.Equal(t, "1500.5", query.Get("max_latency"))
	// A zero bound is a real filter, not an absent one.
	require.Equal(t, "0", query.Get("min_cost"))
	require.Equal(t, "0.002", query.Get("max_cost"), "shortest round-tripping form, not 0.002000 or 2e-03")
}

// rankingRowsJSON runs a ranking tool and returns its "rankings" rows (found at
// path) as decoded JSON, the shape the model actually reads.
func rankingRowsJSON(t *testing.T, name string, deps *Deps, args map[string]any, path ...string) (map[string]any, []map[string]any) {
	t.Helper()
	out, err := runTool(t, name, deps, args)
	require.NoError(t, err)
	encoded, err := sonic.Marshal(out)
	require.NoError(t, err)
	var shape map[string]any
	require.NoError(t, sonic.Unmarshal(encoded, &shape))
	node := shape
	for _, key := range path {
		next, ok := node[key].(map[string]any)
		require.True(t, ok, "missing %s in %s", key, encoded)
		node = next
	}
	raw, ok := node["rankings"].([]any)
	require.True(t, ok, "missing rankings in %s", encoded)
	rows := make([]map[string]any, len(raw))
	for i, row := range raw {
		rows[i] = row.(map[string]any)
	}
	return shape, rows
}

func linkQuery(t *testing.T, link any) url.Values {
	t.Helper()
	text, ok := link.(string)
	require.True(t, ok, "link must be a string, got %T", link)
	parsed, err := url.Parse(text)
	require.NoError(t, err)
	require.Equal(t, logsViewPath, parsed.Path)
	return parsed.Query()
}

// A model ranking is rendered as a table with each model name linked. The
// result's logs_link carries only the window, so a model that reused it for
// every row sent every click to the same unfiltered Logs page. Each row carries
// its own link, narrowed to that row's model and provider.
func TestModelRankingRowsLinkToTheirOwnModel(t *testing.T) {
	fake := &fakeLogReader{modelRankingResult: &logstore.ModelRankingResult{
		Rankings: []logstore.ModelRankingWithTrend{
			{ModelRankingEntry: logstore.ModelRankingEntry{Model: "claude-opus-5", Provider: "anthropic", TotalCost: 2.35}},
			{ModelRankingEntry: logstore.ModelRankingEntry{Model: "gpt-4o", Provider: "openai", TotalCost: 0.1}},
		},
	}}
	shape, rows := rankingRowsJSON(t, "query_model_performance", &Deps{LogManager: fake},
		map[string]any{"filters": map[string]any{"start_time": "-7d", "status": []any{"success"}}}, "models")
	require.Len(t, rows, 2)

	want := []struct{ model, provider string }{{"claude-opus-5", "anthropic"}, {"gpt-4o", "openai"}}
	for i, row := range rows {
		require.Equal(t, want[i].model, row["model"], "the ranking fields stay flat on the row")
		query := linkQuery(t, row["link"])
		require.Equal(t, want[i].model, query.Get("models"))
		require.Equal(t, want[i].provider, query.Get("providers"))
		require.Equal(t, "success", query.Get("status"), "the tool's own filters carry over")
		require.NotEmpty(t, query.Get("start_time"))
		require.NotEmpty(t, query.Get("end_time"))
	}
	require.Empty(t, linkQuery(t, shape["logs_link"]).Get("models"), "logs_link still covers the whole result")
}

// Same bug for query_usage_by: each row is linked to the Logs view filtered to
// that entity. A dimension the Logs page cannot filter on, or the synthetic
// Unassigned bucket, gets no row link rather than a link wider than the row.
func TestDimensionRankingRowsLinkToTheirOwnEntity(t *testing.T) {
	result := func(dimension logstore.RankingDimension) *logstore.DimensionRankingResult {
		return &logstore.DimensionRankingResult{
			Dimension:               dimension,
			TotalActualRequests:     10,
			TotalAttributedRequests: 12,
			Rankings: []logstore.DimensionRankingWithTrend{
				{DimensionRankingEntry: logstore.DimensionRankingEntry{ID: "id-1", Name: "One", TotalRequests: 7}},
				{DimensionRankingEntry: logstore.DimensionRankingEntry{ID: "unassigned", Name: "Unassigned", TotalRequests: 5}},
			},
		}
	}
	cases := map[string]string{
		"team":          "team_ids",
		"customer":      "customer_ids",
		"business_unit": "business_unit_ids",
		"project":       "project_ids",
		"virtual_key":   "virtual_key_ids",
		"user":          "user_ids",
		"app":           "apps",
	}
	for dimension, param := range cases {
		t.Run(dimension, func(t *testing.T) {
			fake := &fakeLogReader{dimensionRankingResult: result(logstore.RankingDimension(dimension))}
			shape, rows := rankingRowsJSON(t, "query_usage_by", &Deps{LogManager: fake},
				map[string]any{"dimension": dimension, "filters": map[string]any{"start_time": "-7d"}}, "rankings")
			require.Len(t, rows, 2)
			require.Equal(t, "One", rows[0]["name"])
			query := linkQuery(t, rows[0]["link"])
			require.Equal(t, "id-1", query.Get(param))
			require.NotEmpty(t, query.Get("start_time"))
			require.NotContains(t, rows[1], "link", "Unassigned has no Logs filter to link to")

			totals := shape["rankings"].(map[string]any)
			require.Equal(t, dimension, totals["dimension"], "the result's other fields survive the row links")
			require.EqualValues(t, 10, totals["total_actual_requests"])
			require.EqualValues(t, 12, totals["total_attributed_requests"])
		})
	}

	// A User-Agent carries commas, and the page splits an array parameter on
	// them, then URI-decodes each item. Written raw, "(KHTML, like Gecko)" opened
	// as two agents that match nothing. An item holding a comma or a percent
	// sign is URI-encoded the way the page's own serializer does it.
	agent := "Mozilla/5.0 (KHTML, like Gecko) 100%"
	ranked := result(logstore.RankingDimensionUserAgent)
	ranked.Rankings[0].ID = agent
	fake := &fakeLogReader{dimensionRankingResult: ranked}
	_, rows := rankingRowsJSON(t, "query_usage_by", &Deps{LogManager: fake},
		map[string]any{"dimension": "user_agent", "filters": map[string]any{"start_time": "-7d"}}, "rankings")
	items := strings.Split(linkQuery(t, rows[0]["link"]).Get("user_agents"), ",")
	require.Len(t, items, 1, "the comma inside the agent must not read as a separator")
	decoded, err := url.PathUnescape(items[0])
	require.NoError(t, err)
	require.Equal(t, agent, decoded)

	// A dimension the page cannot filter on still gets no link.
	fake = &fakeLogReader{dimensionRankingResult: result(logstore.RankingDimensionErrorType)}
	_, rows = rankingRowsJSON(t, "query_usage_by", &Deps{LogManager: fake},
		map[string]any{"dimension": "error_type", "filters": map[string]any{"start_time": "-7d", "status": []any{"error"}}}, "rankings")
	require.NotContains(t, rows[0], "link", "the Logs page has no error_type filter")
}
