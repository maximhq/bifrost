package warp

import (
	"net/url"
	"testing"
	"time"

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
		require.Equal(t, want, sanitizeAnswerLinks(input), "input: %s", input)
	}
}
