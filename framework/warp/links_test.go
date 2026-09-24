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

	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/mcptools"
	"github.com/stretchr/testify/require"
)

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

	link := mcptools.LogsViewLink(filters)
	values, err := url.ParseQuery(strings.TrimPrefix(link, mcptools.LogsViewPath+"?"))
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
