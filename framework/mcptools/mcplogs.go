package mcptools

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/maximhq/bifrost/framework/logstore"
)

// MCPFilterSchema is the MCP-execution counterpart of FilterSchema. Its time,
// scope and dimension fields carry FilterSchema's wording, so a caller moving
// between the two tables reads the same rules on both.
const MCPFilterSchema = `{
  "type": "object",
  "description": "Narrows which MCP tool executions are considered. Omit a field to leave that dimension unfiltered. If start_time is omitted the last 24 hours are used.",
  "properties": {
    "start_time": {"type": "string", "description": "A relative offset like -7d, -24h, -30m for a rolling window. For a calendar day: \"today\", \"yesterday\" or a date like 2026-09-03, which starts at midnight in the asker's time zone. For a moment: a local time like 2026-09-03T14:00, read in the asker's time zone, or an RFC3339 timestamp with an explicit offset."},
    "end_time": {"type": "string", "description": "Same forms as start_time, or \"now\". end_time is inclusive: a calendar day here means the end of that day, so one whole day is start_time and end_time set to that same day, never the day after. Defaults to now."},
    "tool_names": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50},
    "server_labels": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50},
    "status": {"type": "array", "items": {"type": "string", "enum": ["success", "error", "processing"]}, "minItems": 1, "maxItems": 50, "description": "success, error, or processing."},
    "virtual_key_ids": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50},
    "team_ids": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50},
    "customer_ids": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50},
    "user_ids": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50},
    "business_unit_ids": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50},
    "project_ids": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50},
    "device_ids": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50, "description": "Developer machines whose endpoint agent observed the call."},
    "apps": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50},
    "user_agents": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50, "description": "Raw User-Agent strings. Prefer apps, which groups them."},
    "agent_names": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50, "description": "Agents whose runs made the call."},
    "llm_request_ids": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50, "description": "The LLM requests (query_logs ids) whose tool calls these were."},
    "session_id": {"type": "string", "minLength": 1, "description": "Exact Bifrost session id."},
    "agent_correlation_id": {"type": "string", "minLength": 1, "description": "Exact id of one agent run."},
    "min_latency": {"type": "number", "description": "Milliseconds."},
    "max_latency": {"type": "number", "description": "Milliseconds."},
    "content_search": {"type": "string", "minLength": 1, "maxLength": 500, "description": "Substring match against tool arguments and results. Omit the field rather than sending an empty string."},
    "scope": {"type": "string", "enum": ["all"], "description": "Set to \"all\" when the question is explicitly about everyone's traffic. Without it, a caller with a default user scope and no team, customer, business unit, project, user, device or virtual key named is scoped to their own traffic. It widens the question, not the permission: results are still limited to what the caller may see."}
  }
}`

func queryMCPLogsTool() Tool {
	return Tool{
		name: "query_mcp_logs",
		description: "List MCP tool executions. These live on a different table than query_logs: LLM traffic never appears here, and MCP traffic never appears there. " +
			"Use count_mcp_logs or query_mcp_metrics for aggregates; use get_mcp_log_detail for one execution's arguments and result.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "filters": ` + MCPFilterSchema + `,
    "limit": {"type": "integer", "minimum": 1, "maximum": 25},
    "include_content": {"type": "boolean", "description": "Include truncated arguments and result text."}
  },
  "required": ["filters"]
}`,
		tenantScoped: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			filters, scope, err := mcpFilterArg(ctx, args)
			if err != nil {
				return nil, err
			}
			limit, err := intArg(args, "limit", 10, MaxLogRows)
			if err != nil {
				return nil, err
			}
			include, err := boolArg(args, "include_content")
			if err != nil {
				return nil, err
			}
			result, err := deps.LogManager.SearchMCPToolLogs(ctx, filters, &logstore.PaginationOptions{
				Limit: limit, Offset: 0, SortBy: "timestamp", Order: "desc",
			})
			if err != nil {
				return nil, fmt.Errorf("mcp log search failed: %w", err)
			}
			rows := make([]map[string]any, 0, len(result.Logs))
			for i := range result.Logs {
				rows = append(rows, projectMCPLog(&result.Logs[i], include))
			}
			out := map[string]any{
				"rows":           rows,
				"returned":       len(rows),
				"total_matching": result.Pagination.TotalCount,
				// sampled says the rows are the newest few of more, so a
				// caller does not read a page as the whole population.
				"sampled": int64(len(rows)) < result.Pagination.TotalCount,
				// Offset is always 0, so more exist exactly when the page is
				// short of the total. HasLogs only says the store is non-empty,
				// and len(rows) >= limit reads a result of exactly limit as more.
				"has_more": int64(len(rows)) < result.Pagination.TotalCount,
				"scope":    mcpScopeNote(filters, scope),
				"window":   mcpResolvedWindow(filters),
			}
			return setMCPLogsLink(out, filters), nil
		},
	}
}

func countMCPLogsTool() Tool {
	return Tool{
		name:        "count_mcp_logs",
		description: "Count MCP tool executions and report success rate, average latency and total cost. Use query_mcp_logs for the rows themselves.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "filters": ` + MCPFilterSchema + `
  },
  "required": ["filters"]
}`,
		tenantScoped: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			filters, scope, err := mcpFilterArg(ctx, args)
			if err != nil {
				return nil, err
			}
			stats, err := deps.LogManager.GetMCPToolLogStats(ctx, filters)
			if err != nil {
				return nil, fmt.Errorf("mcp log stats failed: %w", err)
			}
			out := map[string]any{
				"total_executions":   stats.TotalExecutions,
				"success_rate":       stats.SuccessRate,
				"average_latency_ms": stats.AverageLatency,
				"total_cost":         stats.TotalCost,
				"scope":              mcpScopeNote(filters, scope),
				"window":             mcpResolvedWindow(filters),
			}
			setMCPLogsLink(out, filters)
			if link := mcpFailuresLink(filters, stats.TotalExecutions, stats.SuccessRate); link != "" {
				out["failures_link"] = link
			}
			// A bare zero reads as "no MCP traffic"; a large count invites
			// listing it row by row. Say what the next useful call is.
			switch {
			case stats.TotalExecutions == 0:
				out["guidance"] = "Nothing matched. Widen the time range or check tool_names and server_labels before concluding there is no MCP traffic."
			case stats.TotalExecutions > LargeResultThreshold:
				out["too_many_to_list"] = true
				out["guidance"] = fmt.Sprintf("%d executions match - too many to list. Use query_mcp_metrics or query_mcp_usage_by, or a sorted query_mcp_logs top-N.", stats.TotalExecutions)
				if _, linked := out["logs_link"]; linked {
					out["guidance"] = fmt.Sprintf("%d executions match - too many to list here. If the person asked to see them, link logs_link, which opens this exact set on the MCP Logs page. Otherwise use query_mcp_metrics or query_mcp_usage_by, or a sorted query_mcp_logs top-N.", stats.TotalExecutions)
				}
			default:
				out["guidance"] = "Small enough to list with query_mcp_logs if individual rows are needed."
			}
			return out, nil
		},
	}
}

func getMCPLogDetailTool() Tool {
	return Tool{
		name:        "get_mcp_log_detail",
		description: "One MCP tool execution: truncated arguments, result and error. Needs the id query_mcp_logs returned, as log_id.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "log_id": {"type": "string", "minLength": 1, "description": "The id of a row query_mcp_logs returned."}
  },
  "required": ["log_id"]
}`,
		tenantScoped: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			// log_id, the same name get_log_detail takes, so a caller moving
			// between the two tables does not have to learn a second one.
			id, err := stringArg(args, "log_id")
			if err != nil {
				return nil, err
			}
			entry, err := deps.LogManager.GetMCPToolLog(ctx, id)
			// The store reports a missing row as ErrNotFound, and a row outside
			// the caller's queryscope is missing to them too: the refusal says
			// what was searched rather than calling the id wrong.
			if errors.Is(err, logstore.ErrNotFound) || (err == nil && entry == nil) {
				return nil, mcpLogNotFound(id, scopeFromContext(ctx))
			}
			if err != nil {
				return nil, fmt.Errorf("mcp log lookup failed: %w", err)
			}
			return projectMCPLog(entry, true), nil
		},
	}
}

// mcpLogNotFound is logNotFound for the MCP logs table.
func mcpLogNotFound(id string, scope Scope) error {
	if scope.Visibility == "" {
		return fmt.Errorf("no mcp log found with id %s", id)
	}
	return fmt.Errorf("no mcp log found with id %s among the rows the person asking may see (%s). An MCP log outside that is not visible to them, so say their view is limited to it rather than that the id does not exist or is mistyped", id, scope.Visibility)
}

// mcpMetrics are what query_mcp_metrics can return. The schema's enum and
// maxItems and the runtime check all read this one list.
var mcpMetrics = []string{"summary", "volume", "cost"}

func queryMCPMetricsTool() Tool {
	return Tool{
		name:        "query_mcp_metrics",
		description: "MCP execution totals, and volume and cost over time. 'summary' answers how much MCP traffic there was in one call. Use query_mcp_usage_by for the hottest tools.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "filters": ` + MCPFilterSchema + `,
    "metrics": {
      "type": "array",
      "minItems": 1,
      "maxItems": ` + strconv.Itoa(len(mcpMetrics)) + `,
      "items": {"type": "string", "enum": ["summary", "volume", "cost"]},
      "description": "'summary' returns overall totals and is usually the right starting point."
    }
  },
  "required": ["filters", "metrics"]
}`,
		tenantScoped: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			filters, scope, err := mcpFilterArg(ctx, args)
			if err != nil {
				return nil, err
			}
			if filters.StartTime == nil || filters.EndTime == nil {
				return nil, fmt.Errorf("filters must include a time range")
			}
			metrics, err := enumSliceArg(args, "metrics", "metric", mcpMetrics, len(mcpMetrics))
			if err != nil {
				return nil, err
			}
			search := &logstore.SearchFilters{StartTime: filters.StartTime, EndTime: filters.EndTime}
			bucket, err := bucketSize(search)
			if err != nil {
				return nil, err
			}
			out := map[string]any{"scope": mcpScopeNote(filters, scope), "window": mcpResolvedWindow(filters)}
			setMCPLogsLink(out, filters)
			for _, metric := range metrics {
				switch metric {
				case "summary":
					stats, err := deps.LogManager.GetMCPToolLogStats(ctx, filters)
					if err != nil {
						return nil, fmt.Errorf("mcp stats query failed: %w", err)
					}
					out["summary"] = stats
					if link := mcpFailuresLink(filters, stats.TotalExecutions, stats.SuccessRate); link != "" {
						out["failures_link"] = link
					}
				case "volume":
					volume, err := deps.LogManager.GetMCPHistogram(ctx, *filters, bucket)
					if err != nil {
						return nil, fmt.Errorf("mcp histogram failed: %w", err)
					}
					out["volume"] = volume
				case "cost":
					cost, err := deps.LogManager.GetMCPCostHistogram(ctx, *filters, bucket)
					if err != nil {
						return nil, fmt.Errorf("mcp cost histogram failed: %w", err)
					}
					out["cost"] = cost
				}
			}
			return out, nil
		},
	}
}

// linkedMCPTool is one query_mcp_usage_by row with an MCP Logs link narrowed
// to that tool, so a ranking's rows do not all open the same page.
type linkedMCPTool struct {
	logstore.MCPTopToolResult
	Link string `json:"link,omitempty"`
}

func queryMCPUsageByTool() Tool {
	return Tool{
		name:        "query_mcp_usage_by",
		description: "Hottest MCP tools by call count and cost in the window.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "filters": ` + MCPFilterSchema + `,
    "limit": {"type": "integer", "minimum": 1, "maximum": 20}
  },
  "required": ["filters"]
}`,
		tenantScoped: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			// A ranking of tools is a ranking of what the traffic was, not of who
			// sent it, so it keeps the caller's default scope (see rankingScope).
			filters, scope, err := mcpFilterArg(ctx, args)
			if err != nil {
				return nil, err
			}
			limit, err := intArg(args, "limit", 10, MaxRankingRows)
			if err != nil {
				return nil, err
			}
			result, err := deps.LogManager.GetMCPTopTools(ctx, *filters, limit)
			if err != nil {
				return nil, fmt.Errorf("mcp top tools failed: %w", err)
			}
			tools := []linkedMCPTool{}
			if result != nil {
				tools = make([]linkedMCPTool, len(result.Tools))
				for i, tool := range result.Tools {
					narrowed := *filters
					narrowed.ToolNames = []string{tool.ToolName}
					tools[i] = linkedMCPTool{MCPTopToolResult: tool, Link: mcpLogsViewLink(&narrowed)}
				}
			}
			out := map[string]any{"tools": tools, "scope": mcpScopeNote(filters, scope), "window": mcpResolvedWindow(filters)}
			return setMCPLogsLink(out, filters), nil
		},
	}
}

// mcpResolvedWindow is the window a result actually covers, so the caller can
// state it rather than guess what "-7d" resolved to.
func mcpResolvedWindow(filters *logstore.MCPToolLogSearchFilters) map[string]string {
	return formatWindow(*filters.StartTime, *filters.EndTime)
}

// mcpFilterArg is filterArg for the MCP logs table: it parses the filter object
// against the turn's clock and applies the caller's default scope.
func mcpFilterArg(ctx context.Context, args map[string]any) (*logstore.MCPToolLogSearchFilters, Scope, error) {
	scope := scopeFromContext(ctx)
	value, present := args["filters"]
	if !present || value == nil {
		return nil, scope, fmt.Errorf("filters is required; pass an object, {} for the defaults")
	}
	raw, ok := value.(map[string]any)
	if !ok {
		return nil, scope, fmt.Errorf("filters must be an object, got %T", value)
	}
	filters, err := parseMCPFilters(raw, nowFrom(ctx))
	if err != nil {
		return nil, scope, err
	}
	all, err := scopeAllArg(raw)
	if err != nil {
		return nil, scope, err
	}
	applyMCPScope(filters, scope, all)
	return filters, scope, nil
}

// mcpScopeDimensions is the part of an MCP filter that names whose traffic it
// is, in the shape filtersNameAScope and scopeNote read, so the MCP tools
// follow the same scope rules as the LLM ones rather than a copy of them.
func mcpScopeDimensions(filters *logstore.MCPToolLogSearchFilters) *logstore.SearchFilters {
	return &logstore.SearchFilters{
		UserIDs: filters.UserIDs, TeamIDs: filters.TeamIDs, CustomerIDs: filters.CustomerIDs,
		BusinessUnitIDs: filters.BusinessUnitIDs, ProjectIDs: filters.ProjectIDs, VirtualKeyIDs: filters.VirtualKeyIDs,
	}
}

// mcpFiltersNameAScope is filtersNameAScope for the MCP table. A device is a
// person's machine, so naming one is naming whose traffic it is: layering the
// caller's id on top would return a confident zero for a colleague's laptop.
func mcpFiltersNameAScope(filters *logstore.MCPToolLogSearchFilters) bool {
	return filtersNameAScope(mcpScopeDimensions(filters)) || len(filters.DeviceIDs) > 0
}

// applyMCPScope is applyScope for the MCP table.
func applyMCPScope(filters *logstore.MCPToolLogSearchFilters, scope Scope, all bool) {
	if filters == nil || !scope.HasIdentity || scope.Unrestricted || all {
		return
	}
	if mcpFiltersNameAScope(filters) {
		return
	}
	filters.UserIDs = []string{scope.UserID}
}

// mcpScopeNote is scopeNote for the MCP table.
func mcpScopeNote(filters *logstore.MCPToolLogSearchFilters, scope Scope) string {
	if len(filters.DeviceIDs) > 0 {
		return "named"
	}
	return scopeNote(mcpScopeDimensions(filters), scope)
}

// mcpLogStatuses are the values the logging plugin writes to an MCP log row's
// status. An unknown one is refused: it would match nothing, and an empty
// result reads as "no such executions" rather than as a typo.
var mcpLogStatuses = []string{"success", "error", "processing"}

// parseMCPFilters is parseFilters for the MCP table: unknown keys refused with
// the same typed error, time bounds read the same way against now (the turn's
// clock, in the asker's zone), and the window handed on in UTC.
func parseMCPFilters(raw map[string]any, now time.Time) (*logstore.MCPToolLogSearchFilters, error) {
	if raw == nil {
		raw = map[string]any{}
	}
	known := map[string]bool{
		"start_time": true, "end_time": true, "tool_names": true, "server_labels": true,
		"status": true, "virtual_key_ids": true, "team_ids": true, "customer_ids": true,
		"user_ids": true, "business_unit_ids": true, "project_ids": true, "device_ids": true,
		"apps": true, "user_agents": true, "agent_names": true, "llm_request_ids": true,
		"session_id": true, "agent_correlation_id": true, "min_latency": true, "max_latency": true,
		"content_search": true, "scope": true,
	}
	unknown := []string{}
	for key := range raw {
		if !known[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, &unknownFilterFieldsError{fields: unknown, supported: slices.Sorted(maps.Keys(known))}
	}
	if err := rejectNullTimeBounds(raw); err != nil {
		return nil, err
	}
	start, err := parseTime(raw["start_time"], now)
	if err != nil {
		return nil, fmt.Errorf("start_time: %w", err)
	}
	end, err := parseTimeBound(raw["end_time"], now, true)
	if err != nil {
		return nil, fmt.Errorf("end_time: %w", err)
	}
	if end == nil {
		end = &now
	}
	if start == nil {
		defaulted := end.Add(-DefaultLookback)
		start = &defaulted
	}
	if start.After(*end) {
		return nil, fmt.Errorf("start_time must be before end_time")
	}
	resolvedStart, resolvedEnd := start.UTC(), end.UTC()
	filters := &logstore.MCPToolLogSearchFilters{StartTime: &resolvedStart, EndTime: &resolvedEnd}
	for key, target := range map[string]*[]string{
		"tool_names": &filters.ToolNames, "server_labels": &filters.ServerLabels,
		"status": &filters.Status, "virtual_key_ids": &filters.VirtualKeyIDs,
		"team_ids": &filters.TeamIDs, "customer_ids": &filters.CustomerIDs,
		"user_ids": &filters.UserIDs, "business_unit_ids": &filters.BusinessUnitIDs,
		"project_ids": &filters.ProjectIDs, "device_ids": &filters.DeviceIDs,
		"apps": &filters.Apps, "user_agents": &filters.UserAgents,
		"agent_names": &filters.AgentNames, "llm_request_ids": &filters.LLMRequestIDs,
	} {
		values, err := stringSliceField(raw, key)
		if err != nil {
			return nil, err
		}
		*target = values
	}
	for i, status := range filters.Status {
		if !slices.Contains(mcpLogStatuses, status) {
			return nil, fmt.Errorf("status[%d] is %q; supported statuses are %s", i, status, strings.Join(mcpLogStatuses, ", "))
		}
	}
	for key, target := range map[string]*string{
		"session_id": &filters.SessionID, "agent_correlation_id": &filters.AgentCorrelationID,
	} {
		value, err := exactStringField(raw, key)
		if err != nil {
			return nil, err
		}
		*target = value
	}
	if value, err := floatField(raw, "min_latency"); err != nil {
		return nil, err
	} else {
		filters.MinLatency = value
	}
	if value, err := floatField(raw, "max_latency"); err != nil {
		return nil, err
	} else {
		filters.MaxLatency = value
	}
	search, err := contentSearchField(raw)
	if err != nil {
		return nil, err
	}
	filters.ContentSearch = search
	return filters, nil
}

func projectMCPLog(entry *logstore.MCPToolLog, includeContent bool) map[string]any {
	out := map[string]any{
		"id":           entry.ID,
		"timestamp":    entry.Timestamp.UTC().Format("2006-01-02T15:04:05Z07:00"),
		"tool_name":    entry.ToolName,
		"status":       entry.Status,
		"latency_ms":   derefFloat(entry.Latency),
		"cost":         derefFloat(entry.Cost),
		"request_id":   entry.RequestID,
		"server_label": entry.ServerLabel,
		// The MCP logs page opened on this execution, so a caller can hand a
		// person the row itself rather than a description of where to find it.
		"link": mcpLogDetailLink(entry.ID),
	}
	// The LLM request and session tie a tool call back to the conversation
	// that made it: get_log_detail and get_session take these ids as they are.
	for key, value := range map[string]*string{
		"llm_request_id": entry.LLMRequestID, "session_id": entry.SessionID,
		"agent_correlation_id": entry.AgentCorrelationID, "user_id": entry.UserID,
	} {
		if value != nil && *value != "" {
			out[key] = *value
		}
	}
	if entry.VirtualKeyName != nil {
		out["virtual_key_name"] = *entry.VirtualKeyName
	}
	if entry.ErrorDetailsParsed != nil {
		out["error_message"] = truncateText(entry.ErrorDetailsParsed.GetErrorString(), 300)
	}
	if includeContent {
		if entry.Arguments != "" {
			out["arguments"] = truncateText(entry.Arguments, DetailContentChars)
		}
		if entry.Result != "" {
			out["result"] = truncateText(entry.Result, DetailContentChars)
		}
	}
	return out
}
