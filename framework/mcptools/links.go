package mcptools

import (
	"encoding/json"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/maximhq/bifrost/framework/logstore"
)

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

// LogsViewPath and LogsViewLink are the same path and builder, exported for
// Warp: it repairs the links a model copies out of these tools' results, and has
// to agree with them on what a Logs link is.
const LogsViewPath = logsViewPath

// LogsViewLink is logsViewLink for callers outside this package.
func LogsViewLink(filters *logstore.SearchFilters) string { return logsViewLink(filters) }
