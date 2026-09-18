package warp

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/maximhq/bifrost/framework/logstore"
)

// logsLinkMangled matches a markdown link target that is clearly meant to be
// the Logs page but had its path mangled in transit through the model: a
// scheme and bogus host prepended ("https://workspace/logs?..."), the
// "workspace" segment dropped entirely ("/logs?..."), or a missing leading
// slash. Despite prompt.go's "never invent a link, use it exactly as given"
// instruction, models don't reliably leave a root-relative path alone - one
// "fixes" what looks like a URL with no domain by adding a scheme, another
// drops a path segment it doesn't recognise - so this is corrected here
// rather than left to prompt compliance alone. The query string (built
// server-side by logDetailLink/logsViewLink and, per that same instruction,
// the one part models do tend to reproduce faithfully) is preserved as-is.
//
// The host, when present, must be exactly "workspace" - the mangled path
// segment models reinterpret as a hostname - so a genuinely external link
// (e.g. "https://example.com/logs", or an actual customer domain that
// happens to end in "/logs") is left alone rather than rewritten into a
// workspace-relative one.
var logsLinkMangled = regexp.MustCompile(`\]\(\s*(?:[a-zA-Z][a-zA-Z0-9+.-]*://workspace)?/?(?:workspace/)?logs(\?[^)\s]*)?\)`)

// sanitizeAnswerLinks repairs logsLinkMangled matches back to the canonical
// root-relative Logs link, so a broken link doesn't reach the client or get
// persisted to conversation history.
func sanitizeAnswerLinks(answer string) string {
	return logsLinkMangled.ReplaceAllString(answer, "]("+logsViewPath+"$1)")
}

// logsViewPath is the dashboard's Logs page. Links are built here rather than
// left to the model so the URL scheme lives in one place and the model only
// repeats what it was given.
const logsViewPath = "/workspace/logs"

// logDetailLink opens one request's detail sheet. The Logs page fetches the
// row by id when it is outside the current window, so no filters are needed.
func logDetailLink(id string) string {
	if id == "" {
		return ""
	}
	return logsViewPath + "?" + url.Values{"selected_log": {id}}.Encode()
}

// logsViewLink opens the Logs view with the same filters a tool ran, so a
// reader can see the rows behind a number instead of retyping the filters.
// The window is sent as unix seconds and only when both ends are set, which is
// what the page needs before it will honour an explicit range.
//
// Every filter the tools accept is carried. The latency and cost bounds used to
// be dropped because the Logs page had no URL parameter for them, which made
// the link silently wider than the number it came from - the page loads, the
// filters look applied, and the row count simply does not match. The page now
// reads all four, so the link reproduces the result rather than approximating
// it.
func logsViewLink(filters *logstore.SearchFilters) string {
	if filters == nil {
		return logsViewPath
	}
	values := url.Values{}
	lists := []struct {
		key    string
		values []string
	}{
		{"providers", filters.Providers},
		{"models", filters.Models},
		{"status", filters.Status},
		{"objects", filters.Objects},
		{"virtual_key_ids", filters.VirtualKeyIDs},
		{"user_ids", filters.UserIDs},
		{"team_ids", filters.TeamIDs},
		{"customer_ids", filters.CustomerIDs},
		{"business_unit_ids", filters.BusinessUnitIDs},
		{"project_ids", filters.ProjectIDs},
		{"apps", filters.Apps},
	}
	for _, list := range lists {
		if len(list.values) > 0 {
			// nuqs reads array parameters as one comma-separated value.
			values.Set(list.key, strings.Join(list.values, ","))
		}
	}
	if strings.TrimSpace(filters.ContentSearch) != "" {
		values.Set("content_search", filters.ContentSearch)
	}
	bounds := []struct {
		key   string
		value *float64
	}{
		{"min_latency", filters.MinLatency},
		{"max_latency", filters.MaxLatency},
		{"min_cost", filters.MinCost},
		{"max_cost", filters.MaxCost},
	}
	for _, bound := range bounds {
		if bound.value != nil {
			// 'f', -1: the shortest form that round-trips, so 0.002 stays 0.002
			// rather than becoming 0.002000 or 2e-03.
			values.Set(bound.key, strconv.FormatFloat(*bound.value, 'f', -1, 64))
		}
	}
	if filters.StartTime != nil && filters.EndTime != nil {
		values.Set("start_time", strconv.FormatInt(filters.StartTime.Unix(), 10))
		values.Set("end_time", strconv.FormatInt(filters.EndTime.Unix(), 10))
	}
	if len(values) == 0 {
		return logsViewPath
	}
	return logsViewPath + "?" + values.Encode()
}
