package warp

import (
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/stretchr/testify/require"
)

// Every row and every aggregate Warp reports can be opened in the Logs view.
// The links are built here, server-side, so the model never has to guess the
// dashboard's URL scheme - it only has to repeat what it was given.
func TestWarpLogDetailLink(t *testing.T) {
	require.Equal(t, "/workspace/logs?selected_log=req-1", logDetailLink("req-1"))
	require.Equal(t, "/workspace/logs?selected_log=a%2Fb", logDetailLink("a/b"), "ids are escaped")
	require.Empty(t, logDetailLink(""), "no id, no link")
}

func TestWarpLogsViewLinkEncodesFilters(t *testing.T) {
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

func TestWarpLogsViewLinkOmitsEmptyFilters(t *testing.T) {
	require.Equal(t, "/workspace/logs", logsViewLink(&logstore.SearchFilters{}))
	require.Equal(t, "/workspace/logs", logsViewLink(nil))
	// A half-open window is dropped rather than sent as one side only, which the
	// Logs page would ignore in favour of its default hour.
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	require.Equal(t, "/workspace/logs", logsViewLink(&logstore.SearchFilters{StartTime: &start}))
}

// content_search has a URL parameter on the Logs page, so a link that drops it
// sends the reader to a wider result set than the number they clicked from.
func TestWarpLogsLinkCarriesContentSearch(t *testing.T) {
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

// Models don't reliably leave a root-relative link alone, despite prompt.go
// telling them not to invent one: one prepends a scheme and a bogus host,
// another drops the "workspace" segment it doesn't recognise. Either way the
// link goes nowhere, in every environment, so it is repaired before the
// answer is streamed to the client or saved to history - the query string a
// tool built is preserved exactly.
func TestWarpSanitizeAnswerLinksRepairsMangledPaths(t *testing.T) {
	cases := map[string]string{
		// A scheme and host prepended to the whole path.
		"See [this request](https://workspace/logs?selected_log=req-1) for details.": "See [this request](/workspace/logs?selected_log=req-1) for details.",
		"[logs](http://workspace/logs?providers=openai)":                             "[logs](/workspace/logs?providers=openai)",
		// The "workspace" segment dropped entirely.
		"[logs](/logs?start_time=1&end_time=2)": "[logs](/workspace/logs?start_time=1&end_time=2)",
		// No leading slash at all.
		"[logs](workspace/logs?start_time=1&end_time=2)": "[logs](/workspace/logs?start_time=1&end_time=2)",
		"[logs](logs?start_time=1&end_time=2)":           "[logs](/workspace/logs?start_time=1&end_time=2)",
		// No query string (logsViewLink with no filters).
		"[logs](/logs)": "[logs](/workspace/logs)",
		// A correct link, or unrelated text, passes through untouched.
		"See [this request](/workspace/logs?selected_log=req-1) for details.": "See [this request](/workspace/logs?selected_log=req-1) for details.",
		"no links here": "no links here",
		// A genuinely external link that happens to end in "/logs" is not a
		// mangled workspace path and must be left alone.
		"[external logs](https://example.com/logs)":         "[external logs](https://example.com/logs)",
		"[external logs](https://example.com/logs?foo=bar)": "[external logs](https://example.com/logs?foo=bar)",
		"[not us](https://workspace.attacker.example/logs)": "[not us](https://workspace.attacker.example/logs)",
	}
	for input, want := range cases {
		require.Equal(t, want, sanitizeAnswerLinks(input, nil), "input: %s", input)
	}
}

// Shapes taken from saved answers that had already been through the old
// three-shape repair: a protocol-relative "//workspace/logs" the browser read
// as a host named workspace, "https://logs" with the path promoted to a host,
// JSON-escaped slashes, and padding inside the parentheses. Each keeps the
// query the tool built.
func TestWarpSanitizeAnswerLinksRepairsShapesSeenInTranscripts(t *testing.T) {
	cases := map[string]string{
		"[spend](//workspace/logs?end_time=2&start_time=1)":               "[spend](/workspace/logs?end_time=2&start_time=1)",
		"[failures](https://logs?end_time=2&start_time=1&status=error)":   "[failures](/workspace/logs?end_time=2&start_time=1&status=error)",
		`[failures](\/workspace\/logs?end_time=2&start_time=1)`:           "[failures](/workspace/logs?end_time=2&start_time=1)",
		"[failures]( /workspace/logs?end_time=2&start_time=1 )":           "[failures](/workspace/logs?end_time=2&start_time=1)",
		"[row](/workspace/logs/?selected_log=req-1)":                      "[row](/workspace/logs?selected_log=req-1)",
		"[row](/workspace/logs?end_time=2&amp;start_time=1)":              "[row](/workspace/logs?end_time=2&start_time=1)",
		`[row](/workspace/logs?selected_log=req-1 "open the request")`:    "[row](/workspace/logs?selected_log=req-1)",
		"an image is not a link: ![chart](https://example.com/chart.png)": "an image is not a link: ![chart](https://example.com/chart.png)",
	}
	for input, want := range cases {
		require.Equal(t, want, sanitizeAnswerLinks(input, nil), "input: %s", input)
	}
}

// A model that does not like a URL with no domain invents one. The host cannot
// be told from a real external site by its shape, but the query string can: it
// carries the window's unix seconds or a row's id, and only a tool could have
// written it. A link whose query a tool issued this conversation is rewritten to
// the issued link whatever was put in front of it; the same host with a query
// nobody issued is somebody else's page and is left alone.
func TestWarpSanitizeAnswerLinksMatchesIssuedLinksByQuery(t *testing.T) {
	issued := issuedLinks{}
	issued.collect(`{"logs_link":"/workspace/logs?end_time=1789717379&start_time=1789112579","rows":[{"link":"/workspace/logs?selected_log=78d59bab"}]}`)

	require.Equal(t,
		"[spend](/workspace/logs?end_time=1789717379&start_time=1789112579)",
		sanitizeAnswerLinks("[spend](https://bifrost-dashboard.example.com/workspace/logs?end_time=1789717379&start_time=1789112579)", issued))
	// Parameter order is not the model's to keep.
	require.Equal(t,
		"[spend](/workspace/logs?end_time=1789717379&start_time=1789112579)",
		sanitizeAnswerLinks("[spend](http://localhost:8080/workspace/logs?start_time=1789112579&end_time=1789717379)", issued))
	require.Equal(t,
		"[row](/workspace/logs?selected_log=78d59bab)",
		sanitizeAnswerLinks("[row](https://app.example.com/logs?selected_log=78d59bab)", issued))

	external := "[their logs](https://example.com/logs?end_time=5&start_time=4)"
	require.Equal(t, external, sanitizeAnswerLinks(external, issued))
}

// No tool returns a link to any page but Logs, so a root-relative link anywhere
// else was made up, and so was a Logs link filtered on a parameter the page does
// not read - it opens, looks filtered, and shows a wider set than the number
// beside it. The text stays and the link goes: a link that leads nowhere is
// worse than no link.
func TestWarpSanitizeAnswerLinksUnlinksInventedDashboardPages(t *testing.T) {
	cases := map[string]string{
		"See [team-a's key](/workspace/virtual-keys/vk-123) for its budget.": "See team-a's key for its budget.",
		"[this request](/workspace/logs/req-1)":                              "this request",
		"[overloaded errors](/workspace/logs?error_type=overloaded_error)":   "overloaded errors",
		"[the [Warp] row](/workspace/governance)":                            "the [Warp] row",
		// A query the model composed from parameters the page does read still
		// opens what it says, so it is kept.
		"[anthropic failures](/workspace/logs?providers=anthropic&status=error)": "[anthropic failures](/workspace/logs?providers=anthropic&status=error)",
		// External links and in-page anchors are not dashboard links.
		"[docs](https://docs.getbifrost.ai/warp)": "[docs](https://docs.getbifrost.ai/warp)",
		"[above](#summary)":                       "[above](#summary)",
	}
	for input, want := range cases {
		require.Equal(t, want, sanitizeAnswerLinks(input, nil), "input: %s", input)
	}
}

// A link has to reproduce the result it sits beside, for every filter the Logs
// page can apply - not only the ones in use when the link builder was written.
// stop_reasons, cache_hit_types and the token bounds were accepted by the tools
// and dropped from the link, so the page opened wider than the number. Every
// field of SearchFilters is set here by reflection, so a filter added to the
// store fails this test until the link carries it or it is listed as having no
// place in a URL.
func TestWarpLogsViewLinkCarriesEveryFilter(t *testing.T) {
	notInURL := map[string]string{
		"roots_only":     "a display mode of the page (grouped), not a filter a tool sets",
		"group_sessions": "a display mode of the page (sessions collapsed), not a filter a tool sets",
		"ranking_limit":  "a row cap on ranking queries, not a filter",
		// Deliberately absent from the Logs page, so a query narrowed by one
		// gets no link at all (TestWarpResultsFilteredByErrorFieldsCarryNoLogsLink)
		// rather than a link that is silently wider.
		"error_types":  "the Logs page has no error-type filter",
		"error_codes":  "the Logs page has no error-code filter",
		"status_codes": "the Logs page has no status-code filter",
	}
	filters := &logstore.SearchFilters{}
	value := reflect.ValueOf(filters).Elem()
	var expected []string
	for i := range value.NumField() {
		field := value.Field(i)
		name := strings.Split(value.Type().Field(i).Tag.Get("json"), ",")[0]
		if _, skip := notInURL[name]; skip {
			continue
		}
		expected = append(expected, name)
		switch field.Interface().(type) {
		case []string:
			field.Set(reflect.ValueOf([]string{"a", "b"}))
		case string:
			field.SetString("a")
		case bool:
			field.SetBool(true)
		case *float64:
			field.Set(reflect.ValueOf(new(1.5)))
		case *int:
			field.Set(reflect.ValueOf(new(7)))
		case *time.Time:
			field.Set(reflect.ValueOf(new(time.Unix(1789112579, 0))))
		case map[string]string:
			field.Set(reflect.ValueOf(map[string]string{"env": "prod", "app": "web"}))
		default:
			t.Fatalf("SearchFilters.%s has a type this test cannot fill: teach it, and logsViewLink", value.Type().Field(i).Name)
		}
	}

	link := logsViewLink(filters)
	values, err := url.ParseQuery(strings.TrimPrefix(link, logsViewPath+"?"))
	require.NoError(t, err)
	for _, name := range expected {
		require.Contains(t, values, name, "logsViewLink drops the %s filter", name)
		require.Contains(t, logsPageParams, name, "the Logs page has no %s parameter, so the link repair would unlink it", name)
	}
	// In the forms the page parses: comma-joined arrays, a JSON object with
	// stable key order, a bare true.
	require.Equal(t, "a,b", values.Get("stop_reasons"))
	require.Equal(t, `{"app":"web","env":"prod"}`, values.Get("metadata_filters"))
	require.Equal(t, "true", values.Get("missing_cost_only"))
	require.Equal(t, "7", values.Get("min_tokens"))

	// And a link the builder wrote survives the repair byte for byte.
	require.Equal(t, "[all]("+link+")", sanitizeAnswerLinks("[all]("+link+")", nil))
}

// logsPageParams is a copy of the Logs page's URL state, and a copy drifts. When
// the page is in the tree, its useQueryStates block is the source of truth: a
// parameter added there is accepted in links from the next test run, not the
// next incident.
func TestWarpLogsPageParamsMatchTheLogsPage(t *testing.T) {
	page, err := os.ReadFile(filepath.Join("..", "..", "ui", "app", "workspace", "logs", "page.tsx"))
	if err != nil {
		t.Skip("the dashboard source is not in this tree")
	}
	block := regexp.MustCompile(`(?s)useQueryStates\(\s*\{(.*?)\n\t\t\},`).FindSubmatch(page)
	require.NotNil(t, block, "could not find the Logs page's useQueryStates block")
	onPage := map[string]struct{}{}
	for _, match := range regexp.MustCompile(`(?m)^\t\t\t([a-z_]+): parseAs`).FindAllSubmatch(block[1], -1) {
		onPage[string(match[1])] = struct{}{}
	}
	require.NotEmpty(t, onPage)
	require.Equal(t, onPage, logsPageParams)
}

// The model wrapped the feature-request link in a fenced block labelled
// "github issue link placeholder", which the dashboard rendered as a code
// viewer with a scrollbar instead of a link. A fence holding nothing but that
// URL is unwrapped into a plain link; a fence holding real code is left alone.
func TestWarpSanitizeAnswerLinksUnfencesIssueLink(t *testing.T) {
	url := "https://github.com/maximhq/bifrost/issues/new?title=[Warp]+clarify+failure+breakdown+scope&labels=enhancement"
	for _, fence := range []string{"```github issue link placeholder\n", "```\n", "```text\n"} {
		input := "Warp cannot see guardrails.\n\n" + fence + url + "\n```\nThanks."
		require.Equal(t, "Warp cannot see guardrails.\n\n[Request this in Bifrost's issue tracker]("+url+")\nThanks.", sanitizeAnswerLinks(input, nil), "fence %q", fence)
	}
	code := "```bash\ncurl " + url + "\n```"
	require.Equal(t, code, sanitizeAnswerLinks(code, nil))
}

// rankingRowsJSON runs a ranking tool and returns its "rankings" rows (found at
// path) as decoded JSON, the shape the model actually reads.
func rankingRowsJSON(t *testing.T, name string, deps *ToolDeps, args map[string]any, path ...string) (map[string]any, []map[string]any) {
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
func TestWarpModelRankingRowsLinkToTheirOwnModel(t *testing.T) {
	fake := &fakeLogReader{modelRankingResult: &logstore.ModelRankingResult{
		Rankings: []logstore.ModelRankingWithTrend{
			{ModelRankingEntry: logstore.ModelRankingEntry{Model: "claude-opus-5", Provider: "anthropic", TotalCost: 2.35}},
			{ModelRankingEntry: logstore.ModelRankingEntry{Model: "gpt-4o", Provider: "openai", TotalCost: 0.1}},
		},
	}}
	shape, rows := rankingRowsJSON(t, "query_model_performance", &ToolDeps{logManager: fake},
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
func TestWarpDimensionRankingRowsLinkToTheirOwnEntity(t *testing.T) {
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
			shape, rows := rankingRowsJSON(t, "query_usage_by", &ToolDeps{logManager: fake},
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
	_, rows := rankingRowsJSON(t, "query_usage_by", &ToolDeps{logManager: fake},
		map[string]any{"dimension": "user_agent", "filters": map[string]any{"start_time": "-7d"}}, "rankings")
	items := strings.Split(linkQuery(t, rows[0]["link"]).Get("user_agents"), ",")
	require.Len(t, items, 1, "the comma inside the agent must not read as a separator")
	decoded, err := url.PathUnescape(items[0])
	require.NoError(t, err)
	require.Equal(t, agent, decoded)

	// A dimension the page cannot filter on still gets no link.
	fake = &fakeLogReader{dimensionRankingResult: result(logstore.RankingDimensionErrorType)}
	_, rows = rankingRowsJSON(t, "query_usage_by", &ToolDeps{logManager: fake},
		map[string]any{"dimension": "error_type", "filters": map[string]any{"start_time": "-7d", "status": []any{"error"}}}, "rankings")
	require.NotContains(t, rows[0], "link", "the Logs page has no error_type filter")
}
