package warp

import (
	"encoding/json"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/maximhq/bifrost/framework/logstore"
)

// Links in an answer are checked against what the tools handed out, because a
// model does not reliably leave a root-relative path alone. One "fixes" what
// looks like a URL with no domain by adding a scheme ("https://workspace/logs"),
// another writes it protocol-relative ("//workspace/logs", which a browser reads
// as a host named workspace), promotes the path to a host ("https://logs?..."),
// invents a domain outright, or escapes the slashes. A pattern per shape kept
// missing the next one: of the links in saved answers, a third were still broken
// after a three-shape repair had run over them. What models do reproduce
// faithfully is the query string - the window's unix seconds, a row's id - so
// that is what identifies a link, not whatever was put in front of it.

// issuedLinks is every Logs link the tools returned in a conversation, keyed by
// its query string in canonical order.
type issuedLinks map[string]string

// issuedLogsLink finds Logs links in a tool result or an earlier answer. The
// path is matched without its leading context on purpose: an earlier answer's
// "https://workspace/logs?..." still carries the query a tool built.
var issuedLogsLink = regexp.MustCompile(`/workspace/logs\?([^\s"'()<>\\]+)`)

// collect records the Logs links in text. The first spelling of a query wins,
// which is the tool's own.
func (issued issuedLinks) collect(text string) {
	for _, match := range issuedLogsLink.FindAllStringSubmatch(text, -1) {
		key, ok := canonicalQuery(match[1])
		if !ok {
			continue
		}
		if _, seen := issued[key]; !seen {
			issued[key] = logsViewPath + "?" + match[1]
		}
	}
}

// canonicalQuery puts a query string in one order and one escaping, so a link
// the model reordered still matches the one it was given.
func canonicalQuery(raw string) (string, bool) {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return "", false
	}
	return values.Encode(), true
}

// logsPageParams is every URL parameter the Logs page reads (its useQueryStates
// block in ui/app/workspace/logs/page.tsx). A Logs link carrying any other
// parameter opens, looks filtered, and shows a wider set than the number beside
// it, so it is treated as invented.
var logsPageParams = map[string]struct{}{
	"parent_request_id": {}, "providers": {}, "models": {}, "aliases": {}, "status": {}, "stop_reasons": {},
	"tool_call_names": {}, "objects": {}, "selected_key_ids": {}, "virtual_key_ids": {}, "routing_rule_ids": {},
	"routing_engine_used": {}, "apps": {}, "user_agents": {}, "complexity_tiers": {}, "complexity_mechanisms": {},
	"session_id": {}, "user_ids": {}, "team_ids": {}, "customer_ids": {}, "business_unit_ids": {}, "project_ids": {},
	"content_search": {}, "request_id": {}, "min_latency": {}, "max_latency": {}, "min_cost": {}, "max_cost": {},
	"min_tokens": {}, "max_tokens": {},
	"start_time": {}, "end_time": {}, "limit": {}, "offset": {}, "sort_by": {}, "order": {}, "polling": {},
	"period": {}, "missing_cost_only": {}, "cache_hit_types": {}, "metadata_filters": {}, "selected_log": {},
	"grouped": {},
}

// markdownLink matches an inline link, allowing one level of brackets in its
// text ("[the [Warp] row](...)").
var markdownLink = regexp.MustCompile(`(\[(?:[^\[\]]|\[[^\[\]]*\])*\])\(([^)]*)\)`)

// linkTitle matches the optional title after a link target.
var linkTitle = regexp.MustCompile(`\s+["'(].*$`)

// sanitizeAnswerLinks makes every link in an answer one that opens, so a broken
// link doesn't reach the client or get persisted to conversation history. A
// link whose query a tool issued becomes the issued link; any other link to the
// Logs page gets its path repaired and its query kept; a link into the
// dashboard that no tool could have returned loses its target and keeps its
// text. External links are left alone. issued may be nil, which skips only the
// first of those.
func sanitizeAnswerLinks(answer string, issued issuedLinks) string {
	answer = fencedIssueLink.ReplaceAllString(answer, "[Request this in Bifrost's issue tracker]($1)\n")
	matches := markdownLink.FindAllStringSubmatchIndex(answer, -1)
	if len(matches) == 0 {
		return answer
	}
	var builder strings.Builder
	last := 0
	for _, match := range matches {
		// An image is not a link, and its source is not the dashboard's.
		if match[0] > 0 && answer[match[0]-1] == '!' {
			continue
		}
		text, target := answer[match[2]:match[3]], answer[match[4]:match[5]]
		builder.WriteString(answer[last:match[0]])
		last = match[1]
		switch resolved, verdict := resolveLinkTarget(target, issued); verdict {
		case linkRewritten:
			builder.WriteString(text + "(" + resolved + ")")
		case linkInvented:
			builder.WriteString(text[1 : len(text)-1])
		default:
			builder.WriteString(answer[match[0]:match[1]])
		}
	}
	builder.WriteString(answer[last:])
	return builder.String()
}

type linkVerdict int

const (
	// linkUntouched is a link that is not the dashboard's to judge.
	linkUntouched linkVerdict = iota
	linkRewritten
	linkInvented
)

// resolveLinkTarget decides what one link target becomes.
//
// "workspace" and "logs" as a host are the path's own segments, reinterpreted
// by a model that wanted a domain, so those count as the dashboard. Any other
// host is somebody else's site unless the query is one a tool issued: a
// genuinely external "https://example.com/logs" is left alone.
func resolveLinkTarget(target string, issued issuedLinks) (string, linkVerdict) {
	raw := strings.TrimSpace(target)
	raw = strings.TrimSuffix(strings.TrimPrefix(raw, "<"), ">")
	raw = linkTitle.ReplaceAllString(raw, "")
	raw = strings.ReplaceAll(raw, `\`, "")
	raw = strings.ReplaceAll(raw, "&amp;", "&")
	// A space the model decoded back out of a search term.
	raw = strings.ReplaceAll(raw, " ", "+")
	if raw == "" || strings.HasPrefix(raw, "#") {
		return "", linkUntouched
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "" && parsed.Host == "") {
		// Unparseable, or a scheme with no host (mailto:, tel:).
		return "", linkUntouched
	}
	host := strings.ToLower(parsed.Host)
	foreign := host != "" && host != "workspace" && host != "logs"

	segments := strings.FieldsFunc(parsed.Path, func(r rune) bool { return r == '/' })
	if !foreign && host != "" {
		segments = append([]string{host}, segments...)
	}
	key, parseable := canonicalQuery(parsed.RawQuery)

	if foreign {
		if len(segments) > 0 && segments[len(segments)-1] == "logs" && parsed.RawQuery != "" && parseable {
			if link, ok := issued[key]; ok {
				return link, linkRewritten
			}
		}
		return "", linkUntouched
	}
	if !slices.Equal(segments, []string{"workspace", "logs"}) && !slices.Equal(segments, []string{"logs"}) {
		return "", linkInvented
	}
	if parsed.RawQuery == "" {
		return logsViewPath, linkRewritten
	}
	if !parseable {
		return "", linkInvented
	}
	if link, ok := issued[key]; ok {
		return link, linkRewritten
	}
	values, _ := url.ParseQuery(parsed.RawQuery)
	for param := range values {
		if _, known := logsPageParams[param]; !known {
			return "", linkInvented
		}
	}
	return logsViewPath + "?" + parsed.RawQuery, linkRewritten
}

// fencedIssueLink matches a fenced code block whose only content is the
// feature-request link from the prompt. The model wrapped it in a block
// labelled "github issue link placeholder", and the dashboard rendered a code
// viewer with a scrollbar where a link belonged. A fence holding anything else
// - real code that happens to contain the URL - does not match.
var fencedIssueLink = regexp.MustCompile(`\x60{3}[^\n\x60]*\n\s*(https://github\.com/maximhq/bifrost/issues/new\S*)\s*\n\x60{3}\n?`)

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
// Every filter the Logs page can apply is carried, whether or not a tool sets
// it today, so a filter a tool starts accepting links correctly from the first
// call (TestWarpLogsViewLinkCarriesEveryFilter holds this to SearchFilters field
// by field). A dropped filter makes the link silently wider than the number it
// came from - the page loads, the filters look applied, and the row count simply
// does not match. The latency and cost bounds were dropped that way until the
// page grew parameters for them, and stop_reasons, cache_hit_types and the token
// bounds after them.
// failuresLink is logsViewLink narrowed to the failed requests, for a result
// that reports a success rate.
//
// A rate needs both outcomes in the query, so logs_link on such a result opens
// every request: asked about the failure rate, Warp linked "the filtered logs"
// and the page showed 1,226 rows instead of the 72 failures. Empty when nothing
// failed, when nothing was requested (an empty window reports a 0% rate), or
// when the query's own status filter already excludes errors - a link to an
// empty page is worse than no link.
func failuresLink(filters *logstore.SearchFilters, totalRequests int64, successRate float64) string {
	if filters == nil || totalRequests == 0 || successRate >= 100 {
		return ""
	}
	if len(filters.Status) > 0 && !slices.Contains(filters.Status, "error") {
		return ""
	}
	narrowed := *filters
	narrowed.Status = []string{"error"}
	return logsViewLink(&narrowed)
}

// linkableInLogsView reports whether the Logs page can show what these filters
// select. It has no error-type, error-code or status-code filter, by decision,
// so a query narrowed by one cannot be reproduced there: the link would open
// every failure beside a count of seventeen. Such a result gets no link - each
// row still carries its own - rather than one that is silently wider.
func linkableInLogsView(filters *logstore.SearchFilters) bool {
	return len(filters.ErrorTypes) == 0 && len(filters.ErrorCodes) == 0 && len(filters.StatusCodes) == 0
}

// setLogsLink puts logs_link on a result when there is one to give.
func setLogsLink(out map[string]any, filters *logstore.SearchFilters) map[string]any {
	if link := logsViewLink(filters); link != "" {
		out["logs_link"] = link
	}
	return out
}

func logsViewLink(filters *logstore.SearchFilters) string {
	if filters == nil {
		return logsViewPath
	}
	if !linkableInLogsView(filters) {
		return ""
	}
	values := url.Values{}
	lists := []struct {
		key    string
		values []string
	}{
		{"providers", filters.Providers},
		{"models", filters.Models},
		{"aliases", filters.Aliases},
		{"status", filters.Status},
		{"stop_reasons", filters.StopReasons},
		{"tool_call_names", filters.ToolCallNames},
		{"objects", filters.Objects},
		{"selected_key_ids", filters.SelectedKeyIDs},
		{"virtual_key_ids", filters.VirtualKeyIDs},
		{"routing_rule_ids", filters.RoutingRuleIDs},
		{"routing_engine_used", filters.RoutingEngineUsed},
		{"complexity_tiers", filters.ComplexityTiers},
		{"complexity_mechanisms", filters.ComplexityMechanisms},
		{"user_ids", filters.UserIDs},
		{"team_ids", filters.TeamIDs},
		{"customer_ids", filters.CustomerIDs},
		{"business_unit_ids", filters.BusinessUnitIDs},
		{"project_ids", filters.ProjectIDs},
		{"apps", filters.Apps},
		{"user_agents", filters.UserAgents},
		{"cache_hit_types", filters.CacheHitTypes},
	}
	for _, list := range lists {
		if len(list.values) > 0 {
			// nuqs reads array parameters as one comma-separated value.
			values.Set(list.key, strings.Join(arrayItems(list.values), ","))
		}
	}
	texts := []struct {
		key, value string
	}{
		{"content_search", filters.ContentSearch},
		{"request_id", filters.RequestID},
		{"parent_request_id", filters.ParentRequestID},
		{"session_id", filters.SessionID},
	}
	for _, text := range texts {
		if strings.TrimSpace(text.value) != "" {
			values.Set(text.key, text.value)
		}
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
	if filters.MinTokens != nil {
		values.Set("min_tokens", strconv.Itoa(*filters.MinTokens))
	}
	if filters.MaxTokens != nil {
		values.Set("max_tokens", strconv.Itoa(*filters.MaxTokens))
	}
	if filters.MissingCostOnly {
		values.Set("missing_cost_only", "true")
	}
	if len(filters.MetadataFilters) > 0 {
		// The page reads this one as a JSON object. encoding/json rather than
		// sonic because it sorts map keys: the same filters must give the same
		// link, or a reordered copy stops matching the link that was issued.
		if encoded, err := json.Marshal(filters.MetadataFilters); err == nil {
			values.Set("metadata_filters", string(encoded))
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

// arrayItems prepares values for one of the page's comma-separated array
// parameters. The page splits on commas and then URI-decodes each item, so an
// item holding a comma or a percent sign - a User-Agent, mostly - is URI-encoded
// the way the page's own serializer writes it. Everything else is left as it
// is: ids and model names are the common case, and they stay readable.
func arrayItems(items []string) []string {
	out := make([]string, len(items))
	for i, item := range items {
		if strings.ContainsAny(item, ",%") {
			item = url.PathEscape(item)
		}
		out[i] = item
	}
	return out
}

// A ranking is rendered as a table with each row's name linked, and the
// result's logs_link cannot serve those rows: it carries the tool's filters,
// not the row's, so every row opened the same unfiltered Logs page. Each row
// gets its own link instead, narrowed to that row. The row came out of the
// same filtered query, so narrowing never leaves the scope the number covered.

type linkedModelRanking struct {
	logstore.ModelRankingWithTrend
	Link string `json:"link,omitempty"`
}

// linkModelRankings attaches a Logs link per model row, filtered to that row's
// model and provider on top of the tool's own filters.
func linkModelRankings(result *logstore.ModelRankingResult, filters *logstore.SearchFilters) map[string]any {
	rows := []linkedModelRanking{}
	if result != nil {
		rows = make([]linkedModelRanking, len(result.Rankings))
		for i, ranking := range result.Rankings {
			narrowed := *filters
			narrowed.Models = []string{ranking.Model}
			narrowed.Providers = []string{ranking.Provider}
			rows[i] = linkedModelRanking{ModelRankingWithTrend: ranking, Link: logsViewLink(&narrowed)}
		}
	}
	return map[string]any{"rankings": rows}
}

type linkedDimensionRanking struct {
	logstore.DimensionRankingWithTrend
	Link string `json:"link,omitempty"`
}

// linkedDimensionRankingResult keeps every field of the store's result and
// replaces only its rankings: the outer Rankings field shadows the embedded
// one, so a field added to DimensionRankingResult still reaches the model.
type linkedDimensionRankingResult struct {
	*logstore.DimensionRankingResult
	Rankings []linkedDimensionRanking `json:"rankings"`
}

// unassignedRankingID is the id the store gives owner-less traffic in a rollup
// ranking. The Logs page cannot filter on the absence of an owner, so that
// row gets no link rather than one that opens everyone's traffic.
const unassignedRankingID = "unassigned"

// narrowToDimension returns filters narrowed to one ranking row, or false for
// a dimension the Logs page has no URL parameter for.
func narrowToDimension(filters *logstore.SearchFilters, dimension logstore.RankingDimension, id string) (*logstore.SearchFilters, bool) {
	narrowed := *filters
	value := []string{id}
	switch dimension {
	case logstore.RankingDimensionUser:
		narrowed.UserIDs = value
	case logstore.RankingDimensionVirtualKey:
		narrowed.VirtualKeyIDs = value
	case logstore.RankingDimensionTeam:
		narrowed.TeamIDs = value
	case logstore.RankingDimensionCustomer:
		narrowed.CustomerIDs = value
	case logstore.RankingDimensionBusinessUnit:
		narrowed.BusinessUnitIDs = value
	case logstore.RankingDimensionProject:
		narrowed.ProjectIDs = value
	case logstore.RankingDimensionApp:
		narrowed.Apps = value
	case logstore.RankingDimensionUserAgent:
		narrowed.UserAgents = value
	case logstore.RankingDimensionRoutingRule:
		narrowed.RoutingRuleIDs = value
	case logstore.RankingDimensionSelectedKey:
		narrowed.SelectedKeyIDs = value
	case logstore.RankingDimensionAlias:
		narrowed.Aliases = value
	case logstore.RankingDimensionComplexityTier:
		narrowed.ComplexityTiers = value
	case logstore.RankingDimensionComplexityMechanism:
		narrowed.ComplexityMechanisms = value
	case logstore.RankingDimensionRoutingEngine:
		narrowed.RoutingEngineUsed = value
	case logstore.RankingDimensionToolCallName:
		narrowed.ToolCallNames = value
	default:
		return nil, false
	}
	return &narrowed, true
}

// linkDimensionRankings attaches a Logs link per row where one can be built.
func linkDimensionRankings(result *logstore.DimensionRankingResult, filters *logstore.SearchFilters, dimension logstore.RankingDimension) *linkedDimensionRankingResult {
	if result == nil {
		result = &logstore.DimensionRankingResult{Dimension: dimension}
	}
	rows := make([]linkedDimensionRanking, len(result.Rankings))
	for i, ranking := range result.Rankings {
		rows[i] = linkedDimensionRanking{DimensionRankingWithTrend: ranking}
		if ranking.ID == "" || ranking.ID == unassignedRankingID {
			continue
		}
		if narrowed, ok := narrowToDimension(filters, dimension, ranking.ID); ok {
			rows[i].Link = logsViewLink(narrowed)
		}
	}
	return &linkedDimensionRankingResult{DimensionRankingResult: result, Rankings: rows}
}
