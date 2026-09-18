package mcptools

import (
	"context"
	"net/url"
	"slices"
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

// "View all users in Logs" under a ranking of every user opened the Logs page
// with a time range and nothing else: the ranking's own filters named no user,
// so its logs_link was the whole deployment's traffic, Unassigned requests and
// all, beside a list of nineteen people. A ranking's link opens the requests of
// the rows it returned.
func TestRankingLogsLinkOpensTheRankedRows(t *testing.T) {
	ranked := &logstore.DimensionRankingResult{
		Dimension: logstore.RankingDimensionUser,
		Rankings: []logstore.DimensionRankingWithTrend{
			{DimensionRankingEntry: logstore.DimensionRankingEntry{ID: "u-rohan", Name: "Rohan"}},
			{DimensionRankingEntry: logstore.DimensionRankingEntry{ID: "u-pratham", Name: "Pratham"}},
			{DimensionRankingEntry: logstore.DimensionRankingEntry{ID: unassignedRankingID, Name: "Unassigned"}},
		},
	}
	out, err := runTool(t, "query_usage_by", &Deps{LogManager: &fakeLogReader{dimensionRankingResult: ranked}}, map[string]any{
		"dimension": "user", "filters": map[string]any{"start_time": "-30d", "providers": []any{"openai"}},
	})
	require.NoError(t, err)
	link, _ := out.(map[string]any)["logs_link"].(string)
	parsed, err := url.Parse(link)
	require.NoError(t, err)
	require.Equal(t, "u-rohan,u-pratham", parsed.Query().Get("user_ids"), "the link opens the ranked users, not everyone: %s", link)
	require.Equal(t, "openai", parsed.Query().Get("providers"), "the tool's own filters still apply")
	require.NotEmpty(t, parsed.Query().Get("start_time"))
	// The model has to be able to say what the link leaves out.
	require.Contains(t, out.(map[string]any)["logs_link_covers"], "2 user")
	require.Contains(t, out.(map[string]any)["logs_link_covers"], "Unassigned")

	// A dimension the Logs page cannot filter on keeps the tool's own link
	// rules, and an empty ranking has no rows to open.
	empty := &logstore.DimensionRankingResult{Dimension: logstore.RankingDimensionUser}
	out, err = runTool(t, "query_usage_by", &Deps{LogManager: &fakeLogReader{dimensionRankingResult: empty}}, map[string]any{
		"dimension": "user", "filters": map[string]any{"start_time": "-30d"},
	})
	require.NoError(t, err)
	require.NotContains(t, out.(map[string]any), "logs_link_covers")
	require.NotContains(t, out.(map[string]any)["logs_link"], "user_ids")
}

// ---------------------------------------------------------------------------
// Link matrix: what link each filter, tool, ranking dimension and condition
// produces. Each table is checked against the list it covers, so a filter, tool
// or dimension added later fails here until its link is stated.
// ---------------------------------------------------------------------------

// The window every matrix case runs over, as the tools receive it and as the
// Logs page reads it.
const (
	matrixStart     = "2026-09-01T00:00:00Z"
	matrixEnd       = "2026-09-02T00:00:00Z"
	matrixStartUnix = "1788220800"
	matrixEndUnix   = "1788307200"
)

// matrixFilters is the window plus extra, widened to everyone, so a case run
// under a caller view is not narrowed to the caller's own id.
// TestLogsLinkCarriesTheScopeTheQueryRan covers that default.
func matrixFilters(extra map[string]any) map[string]any {
	filters := map[string]any{"start_time": matrixStart, "end_time": matrixEnd, "scope": "all"}
	for key, value := range extra {
		filters[key] = value
	}
	return filters
}

// matrixQuery is the query a link must carry: the window plus params.
func matrixQuery(params map[string]string) url.Values {
	want := url.Values{"start_time": {matrixStartUnix}, "end_time": {matrixEndUnix}}
	for key, value := range params {
		want.Set(key, value)
	}
	return want
}

// pageArray reads an array parameter the way the Logs page does
// (parseAsArrayOf in ui/lib/queryParamsParser.ts): split on commas, then
// URI-decode each item.
func pageArray(t *testing.T, value string) []string {
	t.Helper()
	items := strings.Split(value, ",")
	for i, item := range items {
		decoded, err := url.PathUnescape(item)
		require.NoError(t, err, "the page cannot decode %q", item)
		items[i] = decoded
	}
	return items
}

// resultMap is a tool result as the model receives it: JSON, then decoded.
func resultMap(t *testing.T, result any) map[string]any {
	t.Helper()
	encoded, err := sonic.Marshal(result)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, sonic.Unmarshal(encoded, &out))
	return out
}

// requireLogsLink checks a link opens the Logs page with exactly want, no
// parameter more or less.
func requireLogsLink(t *testing.T, link any, want url.Values, context string) {
	t.Helper()
	require.Equal(t, want, linkQuery(t, link), context)
}

// One case per filter the tools accept: the argument as the model sends it, and
// the parameter the Logs page must receive.
func TestLogsLinkPerFilterArgument(t *testing.T) {
	type filterCase struct {
		arg any
		// want is the parameter's value; array is the items the page must read
		// back, for a value that needs encoding. Neither set means the filter has
		// no parameter of its own.
		want  string
		array []string
		// noLink is a filter the Logs page cannot apply: the result carries no
		// logs_link rather than a wider one.
		noLink bool
	}
	agent := "Mozilla/5.0 (KHTML, like Gecko) 100%"
	cases := map[string]filterCase{
		"start_time":            {arg: matrixStart, want: matrixStartUnix},
		"end_time":              {arg: matrixEnd, want: matrixEndUnix},
		"providers":             {arg: []any{"openai", "anthropic"}, want: "openai,anthropic"},
		"models":                {arg: []any{"gpt-4o", "claude-3-opus"}, want: "gpt-4o,claude-3-opus"},
		"status":                {arg: []any{"error", "cancelled"}, want: "error,cancelled"},
		"stop_reasons":          {arg: []any{"length", "tool_calls"}, want: "length,tool_calls"},
		"objects":               {arg: []any{"responses", "responses_stream"}, want: "responses,responses_stream"},
		"virtual_key_ids":       {arg: []any{"vk-1", "vk-2"}, want: "vk-1,vk-2"},
		"team_ids":              {arg: []any{"team-platform"}, want: "team-platform"},
		"customer_ids":          {arg: []any{"cust-acme"}, want: "cust-acme"},
		"user_ids":              {arg: []any{"u-1", "u-2"}, want: "u-1,u-2"},
		"business_unit_ids":     {arg: []any{"bu-1"}, want: "bu-1"},
		"project_ids":           {arg: []any{"proj-1"}, want: "proj-1"},
		"apps":                  {arg: []any{"support-widget"}, want: "support-widget"},
		"min_latency":           {arg: float64(400), want: "400"},
		"max_latency":           {arg: 1500.5, want: "1500.5"},
		"min_tokens":            {arg: float64(10), want: "10"},
		"max_tokens":            {arg: float64(2000), want: "2000"},
		"min_cost":              {arg: float64(0), want: "0"},
		"max_cost":              {arg: 0.002, want: "0.002"},
		"cache_hit_types":       {arg: []any{"direct", "semantic"}, want: "direct,semantic"},
		"routing_rule_ids":      {arg: []any{"rule-premium"}, want: "rule-premium"},
		"routing_engine_used":   {arg: []any{"routing-rule", "governance"}, want: "routing-rule,governance"},
		"selected_key_ids":      {arg: []any{"key-1"}, want: "key-1"},
		"aliases":               {arg: []any{"fast"}, want: "fast"},
		"complexity_tiers":      {arg: []any{"SIMPLE", "COMPLEX"}, want: "SIMPLE,COMPLEX"},
		"complexity_mechanisms": {arg: []any{"semantic"}, want: "semantic"},
		"tool_call_names":       {arg: []any{"get_weather"}, want: "get_weather"},
		"user_agents":           {arg: []any{agent, "curl/8.0"}, array: []string{agent, "curl/8.0"}},
		"metadata_filters":      {arg: map[string]any{"env": "prod", "app": "web"}, want: `{"app":"web","env":"prod"}`},
		"session_id":            {arg: "sess-1", want: "sess-1"},
		"request_id":            {arg: "req-1", want: "req-1"},
		"parent_request_id":     {arg: "req-0", want: "req-0"},
		"missing_cost_only":     {arg: true, want: "true"},
		"content_search":        {arg: "payment declined, 50% off", want: "payment declined, 50% off"},
		"error_types":           {arg: []any{"overloaded_error"}, noLink: true},
		"error_codes":           {arg: []any{"context_length_exceeded"}, noLink: true},
		"status_codes":          {arg: []any{float64(429)}, noLink: true},
		// Widens the question; the page has no parameter for it.
		"scope": {arg: "all"},
	}

	var schema struct {
		Properties map[string]any `json:"properties"`
	}
	require.NoError(t, sonic.UnmarshalString(FilterSchema, &schema))
	for name := range schema.Properties {
		require.Contains(t, cases, name, "filter %s is offered to the model and has no link case here", name)
	}
	for name := range cases {
		require.Contains(t, schema.Properties, name, "case %s is not a filter the tools offer", name)
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			result, err := runTool(t, "count_logs", &Deps{LogManager: &fakeLogReader{}}, map[string]any{
				"filters": matrixFilters(map[string]any{name: tc.arg}),
			})
			require.NoError(t, err)
			out := resultMap(t, result)
			if tc.noLink {
				require.NotContains(t, out, "logs_link", "the Logs page cannot filter by %s", name)
				require.NotContains(t, out, "failures_link")
				return
			}
			want := matrixQuery(nil)
			switch {
			case tc.array != nil:
				raw := linkQuery(t, out["logs_link"]).Get(name)
				require.Equal(t, tc.array, pageArray(t, raw), "the page reads %q back as different items", raw)
				want.Set(name, raw)
			case tc.want != "":
				want.Set(name, tc.want)
			}
			requireLogsLink(t, out["logs_link"], want, name)
		})
	}
}

// Filters combine: a link carries all of them at once, and an array value holding
// the page's own separator is read back as the items that went in.
func TestLogsLinkCombinesFilters(t *testing.T) {
	result, err := runTool(t, "count_logs", &Deps{LogManager: &fakeLogReader{}}, map[string]any{
		"filters": matrixFilters(map[string]any{
			"providers": []any{"openai"}, "models": []any{"gpt-4o"}, "status": []any{"error"},
			"team_ids": []any{"team-platform"}, "min_cost": 0.01, "content_search": "refund",
			"metadata_filters": map[string]any{"env": "prod"},
		}),
	})
	require.NoError(t, err)
	requireLogsLink(t, resultMap(t, result)["logs_link"], matrixQuery(map[string]string{
		"providers": "openai", "models": "gpt-4o", "status": "error", "team_ids": "team-platform",
		"min_cost": "0.01", "content_search": "refund", "metadata_filters": `{"env":"prod"}`,
	}), "combined filters")

	for _, value := range []string{"a,b", "50%", "a b", "x/y", "naïve"} {
		link := logsViewLink(&logstore.SearchFilters{Apps: []string{value, "plain"}})
		require.Equal(t, []string{value, "plain"}, pageArray(t, linkQuery(t, link).Get("apps")), "app %q", value)
	}
}

// The default scope is a filter like any other: a link beside a number scoped
// to the asker opens the asker's requests, and a widened one does not.
func TestLogsLinkCarriesTheScopeTheQueryRan(t *testing.T) {
	caller := CallerView{DefaultUserID: "u-asker"}
	cases := map[string]struct {
		ctx     context.Context
		filters map[string]any
		want    map[string]string
	}{
		"identified caller, nothing named": {WithCallerView(context.Background(), caller), nil, map[string]string{"user_ids": "u-asker"}},
		"identified caller, scope all":     {WithCallerView(context.Background(), caller), map[string]any{"scope": "all"}, nil},
		"identified caller, team named":    {WithCallerView(context.Background(), caller), map[string]any{"team_ids": []any{"team-1"}}, map[string]string{"team_ids": "team-1"}},
		"identified caller, key named":     {WithCallerView(context.Background(), caller), map[string]any{"virtual_key_ids": []any{"vk-1"}}, map[string]string{"virtual_key_ids": "vk-1"}},
		"unrestricted caller":              {WithCallerView(context.Background(), CallerView{DefaultUserID: "u-asker", Unrestricted: true}), nil, nil},
		"nobody identified":                {context.Background(), nil, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			filters := map[string]any{"start_time": matrixStart, "end_time": matrixEnd}
			for key, value := range tc.filters {
				filters[key] = value
			}
			result := mustRunToolCtx(t, tc.ctx, "count_logs", &Deps{LogManager: &fakeLogReader{}}, map[string]any{"filters": filters})
			requireLogsLink(t, resultMap(t, result)["logs_link"], matrixQuery(tc.want), name)
		})
	}
}

// Every tool that takes filters hands back a link to them.
func TestEveryFilteredToolLinksItsFilters(t *testing.T) {
	cases := map[string]map[string]any{
		SemanticSearchToolName:    {"queries": []any{"payment failures"}},
		"query_logs":              {},
		"count_logs":              {},
		"query_metrics":           {"metrics": []any{"summary"}},
		"query_usage_by":          {"dimension": "app"},
		"query_model_performance": {},
		RenderChartToolName:       {"kind": "line", "metric": "requests", "interval": "day", "title": "Requests per day"},
	}
	for _, tool := range buildTools() {
		if !strings.Contains(tool.schemaJSON, `"filters"`) {
			require.NotContains(t, cases, tool.name)
			continue
		}
		require.Contains(t, cases, tool.name, "%s takes filters and has no link case here", tool.name)
	}

	// An empty ranking, so query_usage_by has no rows to narrow its link to.
	deps := func() *Deps {
		return &Deps{
			LogManager: &fakeLogReader{dimensionRankingResult: &logstore.DimensionRankingResult{}},
			Semantic:   &fakeSemanticSearcher{},
		}
	}
	want := matrixQuery(map[string]string{"providers": "openai", "status": "success"})
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			args["filters"] = matrixFilters(map[string]any{"providers": []any{"openai"}, "status": []any{"success"}})
			result, err := runTool(t, name, deps(), args)
			require.NoError(t, err)
			out := resultMap(t, result)
			requireLogsLink(t, out["logs_link"], want, name)

			if name == RenderChartToolName {
				spec, ok := out["chart"].(map[string]any)
				require.True(t, ok, "render_chart returns its spec: %v", out)
				requireLogsLink(t, spec["link"], want, "the chart's own Open in Logs")
			}

			// And none of them links what the page cannot show.
			args["filters"] = matrixFilters(map[string]any{"error_types": []any{"overloaded_error"}})
			result, err = runTool(t, name, deps(), args)
			require.NoError(t, err)
			out = resultMap(t, result)
			require.NotContains(t, out, "logs_link", "%s linked a query the Logs page cannot reproduce", name)
			if name == RenderChartToolName {
				require.NotContains(t, out["chart"], "link")
			}
		})
	}
}

// A listed request opens itself, whatever filters found it.
func TestLogRowsLinkToTheirOwnRequest(t *testing.T) {
	fake := &fakeLogReader{searchResult: &logstore.SearchResult{
		Logs:       []logstore.Log{{ID: "req-1", Provider: "openai"}, {ID: "req/2", Provider: "anthropic"}},
		Pagination: logstore.PaginationOptions{TotalCount: 2},
	}}
	result, err := runTool(t, "query_logs", &Deps{LogManager: fake}, map[string]any{
		"filters": matrixFilters(map[string]any{"status": []any{"error"}}),
	})
	require.NoError(t, err)
	out := resultMap(t, result)
	rows := out["rows"].([]any)
	require.Len(t, rows, 2)
	for i, id := range []string{"req-1", "req/2"} {
		requireLogsLink(t, rows[i].(map[string]any)["link"], url.Values{"selected_log": {id}}, id)
	}
	requireLogsLink(t, out["logs_link"], matrixQuery(map[string]string{"status": "error"}), "the list as a whole")
}

// A per-provider row opens that provider under the tool's filters; the result's
// own link stays on the whole result.
func TestProviderRowsLinkToTheirOwnProvider(t *testing.T) {
	fake := &fakeLogReader{
		providerCostHistogramResult: &logstore.ProviderCostHistogramResult{Providers: []string{"openai", "anthropic"}},
		statsByProvider: map[string]*logstore.SearchStats{
			"openai":    {TotalRequests: 10, SuccessRate: 100},
			"anthropic": {TotalRequests: 5, SuccessRate: 80},
		},
	}
	result, err := runTool(t, "query_metrics", &Deps{LogManager: fake}, map[string]any{
		"filters": matrixFilters(map[string]any{"team_ids": []any{"team-1"}}), "metrics": []any{"cost"}, "group_by": "provider",
	})
	require.NoError(t, err)
	out := resultMap(t, result)
	totals := out["provider_totals"].(map[string]any)
	require.Len(t, totals, 2)
	for _, provider := range []string{"openai", "anthropic"} {
		requireLogsLink(t, totals[provider].(map[string]any)["link"],
			matrixQuery(map[string]string{"team_ids": "team-1", "providers": provider}), provider)
	}
	requireLogsLink(t, out["logs_link"], matrixQuery(map[string]string{"team_ids": "team-1"}), "the whole result")
}

// One case per ranking dimension: the parameter a row narrows to, or none for a
// dimension the Logs page cannot filter on. The ranking's own link follows the
// same rule - the ranked rows where the page can show them, the tool's filters
// where it cannot.
func TestRankingLinksPerDimension(t *testing.T) {
	params := map[logstore.RankingDimension]string{
		logstore.RankingDimensionUser:                "user_ids",
		logstore.RankingDimensionVirtualKey:          "virtual_key_ids",
		logstore.RankingDimensionTeam:                "team_ids",
		logstore.RankingDimensionCustomer:            "customer_ids",
		logstore.RankingDimensionBusinessUnit:        "business_unit_ids",
		logstore.RankingDimensionProject:             "project_ids",
		logstore.RankingDimensionApp:                 "apps",
		logstore.RankingDimensionUserAgent:           "user_agents",
		logstore.RankingDimensionRoutingRule:         "routing_rule_ids",
		logstore.RankingDimensionRoutingEngine:       "routing_engine_used",
		logstore.RankingDimensionSelectedKey:         "selected_key_ids",
		logstore.RankingDimensionAlias:               "aliases",
		logstore.RankingDimensionComplexityTier:      "complexity_tiers",
		logstore.RankingDimensionComplexityMechanism: "complexity_mechanisms",
		logstore.RankingDimensionToolCallName:        "tool_call_names",
		logstore.RankingDimensionErrorType:           "",
		logstore.RankingDimensionErrorCode:           "",
		logstore.RankingDimensionStatusCode:          "",
		logstore.RankingDimensionFailReason:          "",
		logstore.RankingDimensionGuardrailRule:       "",
		logstore.RankingDimensionGuardrailAction:     "",
	}
	require.Len(t, params, len(rankingDimensions), "a case here is not a dimension query_usage_by offers")
	for _, dimension := range rankingDimensions {
		require.Contains(t, params, dimension.value, "dimension %s has no link case here", dimension.value)
	}

	for dimension, param := range params {
		t.Run(string(dimension), func(t *testing.T) {
			fake := &fakeLogReader{dimensionRankingResult: &logstore.DimensionRankingResult{
				Dimension: dimension,
				Rankings: []logstore.DimensionRankingWithTrend{
					{DimensionRankingEntry: logstore.DimensionRankingEntry{ID: "id-1", Name: "One"}},
					{DimensionRankingEntry: logstore.DimensionRankingEntry{ID: "id-2", Name: "Two"}},
					{DimensionRankingEntry: logstore.DimensionRankingEntry{ID: unassignedRankingID, Name: "Unassigned"}},
				},
			}}
			result, err := runTool(t, "query_usage_by", &Deps{LogManager: fake}, map[string]any{
				"dimension": string(dimension), "filters": matrixFilters(map[string]any{"providers": []any{"openai"}}),
			})
			require.NoError(t, err)
			out := resultMap(t, result)
			rows := out["rankings"].(map[string]any)["rankings"].([]any)
			require.Len(t, rows, 3)
			toolFilters := map[string]string{"providers": "openai"}

			if param == "" {
				for _, row := range rows {
					require.NotContains(t, row, "link", "the Logs page cannot filter by %s", dimension)
				}
				requireLogsLink(t, out["logs_link"], matrixQuery(toolFilters), "the tool's own filters")
				require.NotContains(t, out, "logs_link_covers")
				return
			}
			for i, id := range []string{"id-1", "id-2"} {
				requireLogsLink(t, rows[i].(map[string]any)["link"],
					matrixQuery(map[string]string{"providers": "openai", param: id}), "row "+id)
			}
			require.NotContains(t, rows[2], "link", "Unassigned has no Logs filter")
			requireLogsLink(t, out["logs_link"],
				matrixQuery(map[string]string{"providers": "openai", param: "id-1,id-2"}), "the ranked rows")
			covers, _ := out["logs_link_covers"].(string)
			require.Contains(t, covers, "2 "+string(dimension)+" rows")
			require.Contains(t, covers, "Unassigned")
		})
	}
}

// What a ranking's link falls back to, and what it never does.
func TestRankingLogsLinkConditions(t *testing.T) {
	row := func(id string) logstore.DimensionRankingWithTrend {
		return logstore.DimensionRankingWithTrend{DimensionRankingEntry: logstore.DimensionRankingEntry{ID: id}}
	}
	cases := map[string]struct {
		rows       []logstore.DimensionRankingWithTrend
		filters    map[string]any
		want       map[string]string
		noLink     bool
		covers     string
		noMentions string
	}{
		"ranked rows":             {rows: []logstore.DimensionRankingWithTrend{row("u-1"), row("u-2")}, want: map[string]string{"user_ids": "u-1,u-2"}, covers: "2 user rows", noMentions: "Unassigned"},
		"one row":                 {rows: []logstore.DimensionRankingWithTrend{row("u-1")}, want: map[string]string{"user_ids": "u-1"}, covers: "1 user rows"},
		"rows and unassigned":     {rows: []logstore.DimensionRankingWithTrend{row("u-1"), row(unassignedRankingID)}, want: map[string]string{"user_ids": "u-1"}, covers: "Unassigned"},
		"only unassigned":         {rows: []logstore.DimensionRankingWithTrend{row(unassignedRankingID)}, want: nil},
		"a row with no id":        {rows: []logstore.DimensionRankingWithTrend{row(""), row("u-1")}, want: map[string]string{"user_ids": "u-1"}, covers: "1 user rows"},
		"no rows":                 {rows: nil, want: nil},
		"rows under a team":       {rows: []logstore.DimensionRankingWithTrend{row("u-1")}, filters: map[string]any{"team_ids": []any{"team-1"}}, want: map[string]string{"team_ids": "team-1", "user_ids": "u-1"}, covers: "1 user rows"},
		"rows narrow a user list": {rows: []logstore.DimensionRankingWithTrend{row("u-1")}, filters: map[string]any{"user_ids": []any{"u-1", "u-9"}}, want: map[string]string{"user_ids": "u-1"}, covers: "1 user rows"},
		"an unlinkable filter":    {rows: []logstore.DimensionRankingWithTrend{row("u-1")}, filters: map[string]any{"status_codes": []any{float64(429)}}, noLink: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fake := &fakeLogReader{dimensionRankingResult: &logstore.DimensionRankingResult{Dimension: logstore.RankingDimensionUser, Rankings: tc.rows}}
			result, err := runTool(t, "query_usage_by", &Deps{LogManager: fake}, map[string]any{
				"dimension": "user", "filters": matrixFilters(tc.filters),
			})
			require.NoError(t, err)
			out := resultMap(t, result)
			if tc.noLink {
				require.NotContains(t, out, "logs_link")
				require.NotContains(t, out, "logs_link_covers", "nothing to describe without a link")
				return
			}
			requireLogsLink(t, out["logs_link"], matrixQuery(tc.want), name)
			if tc.covers == "" {
				require.NotContains(t, out, "logs_link_covers", "the link is the tool's own filters")
				return
			}
			require.Contains(t, out["logs_link_covers"], tc.covers)
			if tc.noMentions != "" {
				require.NotContains(t, out["logs_link_covers"], tc.noMentions)
			}
		})
	}
}

// failures_link exists only when there are failures to open and the page can
// show them.
func TestFailuresLinkConditions(t *testing.T) {
	cases := map[string]struct {
		filters logstore.SearchFilters
		total   int64
		rate    float64
		// want is the status the link carries; empty means no link.
		want string
	}{
		"some failed":                      {total: 100, rate: 90, want: "error"},
		"everything failed":                {total: 100, rate: 0, want: "error"},
		"nothing failed":                   {total: 100, rate: 100},
		"no requests":                      {total: 0, rate: 0},
		"filtered to errors already":       {filters: logstore.SearchFilters{Status: []string{"error"}}, total: 10, rate: 0, want: "error"},
		"filtered to errors and successes": {filters: logstore.SearchFilters{Status: []string{"success", "error"}}, total: 10, rate: 50, want: "error"},
		"filtered to successes":            {filters: logstore.SearchFilters{Status: []string{"success"}}, total: 10, rate: 50},
		"filtered by error type":           {filters: logstore.SearchFilters{ErrorTypes: []string{"overloaded_error"}}, total: 10, rate: 0},
		"filtered by error code":           {filters: logstore.SearchFilters{ErrorCodes: []string{"x"}}, total: 10, rate: 0},
		"filtered by status code":          {filters: logstore.SearchFilters{StatusCodes: []int{429}}, total: 10, rate: 0},
		"other filters carry over":         {filters: logstore.SearchFilters{Providers: []string{"anthropic"}}, total: 10, rate: 50, want: "error"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			before := slices.Clone(tc.filters.Status)
			link := failuresLink(&tc.filters, tc.total, tc.rate)
			require.Equal(t, before, tc.filters.Status, "the caller's filters are not modified")
			if tc.want == "" {
				require.Empty(t, link)
				return
			}
			want := url.Values{"status": {tc.want}}
			if len(tc.filters.Providers) > 0 {
				want.Set("providers", strings.Join(tc.filters.Providers, ","))
			}
			requireLogsLink(t, link, want, name)
		})
	}
	require.Empty(t, failuresLink(nil, 10, 50))

	// And on the results that report a rate.
	for name, args := range map[string]map[string]any{
		"count_logs":    {},
		"query_metrics": {"metrics": []any{"summary"}},
	} {
		fake := &fakeLogReader{statsResponses: []*logstore.SearchStats{{TotalRequests: 100, SuccessRate: 90}}}
		args["filters"] = matrixFilters(map[string]any{"providers": []any{"anthropic"}})
		result, err := runTool(t, name, &Deps{LogManager: fake}, args)
		require.NoError(t, err)
		out := resultMap(t, result)
		requireLogsLink(t, out["failures_link"], matrixQuery(map[string]string{"providers": "anthropic", "status": "error"}), name)
		requireLogsLink(t, out["logs_link"], matrixQuery(map[string]string{"providers": "anthropic"}), name)
	}
}
