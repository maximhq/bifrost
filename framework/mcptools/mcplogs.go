package mcptools

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/maximhq/bifrost/framework/logstore"
)

// MCPFilterSchema is the MCP-execution counterpart of FilterSchema.
const MCPFilterSchema = `{
  "type": "object",
  "description": "Narrows which MCP tool executions are considered. Omit a field to leave that dimension unfiltered. If start_time is omitted the last 24 hours are used.",
  "properties": {
    "start_time": {"type": "string", "description": "A relative offset like -7d or an RFC3339 timestamp."},
    "end_time": {"type": "string", "description": "RFC3339 timestamp, a relative offset like -1d, or \"now\". Defaults to now."},
    "tool_names": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50},
    "server_labels": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50},
    "status": {"type": "array", "items": {"type": "string", "enum": ["success", "error", "processing"]}, "minItems": 1, "maxItems": 50, "description": "success, error, or processing."},
    "virtual_key_ids": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50},
    "team_ids": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50},
    "customer_ids": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50},
    "user_ids": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50},
    "apps": {"type": "array", "items": {"type": "string", "minLength": 1}, "minItems": 1, "maxItems": 50},
    "min_latency": {"type": "number", "description": "Milliseconds."},
    "max_latency": {"type": "number", "description": "Milliseconds."},
    "content_search": {"type": "string", "minLength": 1, "maxLength": 500}
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
			filters, err := mcpFilterArg(args)
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
			return map[string]any{
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
				"window":   mcpResolvedWindow(filters),
			}, nil
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
			filters, err := mcpFilterArg(args)
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
				"window":             mcpResolvedWindow(filters),
			}
			// A bare zero reads as "no MCP traffic"; a large count invites
			// listing it row by row. Say what the next useful call is.
			switch {
			case stats.TotalExecutions == 0:
				out["guidance"] = "Nothing matched. Widen the time range or check tool_names and server_labels before concluding there is no MCP traffic."
			case stats.TotalExecutions > LargeResultThreshold:
				out["too_many_to_list"] = true
				out["guidance"] = fmt.Sprintf("%d executions match - too many to list. Use query_mcp_metrics or query_mcp_usage_by, or a sorted query_mcp_logs top-N.", stats.TotalExecutions)
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
			if err != nil {
				return nil, fmt.Errorf("mcp log lookup failed: %w", err)
			}
			if entry == nil {
				return nil, fmt.Errorf("no mcp log with id %q", id)
			}
			return projectMCPLog(entry, true), nil
		},
	}
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
			filters, err := mcpFilterArg(args)
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
			out := map[string]any{"window": mcpResolvedWindow(filters)}
			for _, metric := range metrics {
				switch metric {
				case "summary":
					stats, err := deps.LogManager.GetMCPToolLogStats(ctx, filters)
					if err != nil {
						return nil, fmt.Errorf("mcp stats query failed: %w", err)
					}
					out["summary"] = stats
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
			filters, err := mcpFilterArg(args)
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
			tools := []logstore.MCPTopToolResult{}
			if result != nil {
				tools = result.Tools
			}
			return map[string]any{"tools": tools, "window": mcpResolvedWindow(filters)}, nil
		},
	}
}

// mcpResolvedWindow is the window a result actually covers, so the caller can
// state it rather than guess what "-7d" resolved to.
func mcpResolvedWindow(filters *logstore.MCPToolLogSearchFilters) map[string]string {
	return formatWindow(*filters.StartTime, *filters.EndTime)
}

func mcpFilterArg(args map[string]any) (*logstore.MCPToolLogSearchFilters, error) {
	raw, ok := args["filters"].(map[string]any)
	if !ok {
		if args["filters"] == nil {
			return nil, fmt.Errorf("filters is required")
		}
		return nil, fmt.Errorf("filters must be an object, got %T", args["filters"])
	}
	return parseMCPFilters(raw)
}

// mcpLogStatuses are the values the logging plugin writes to an MCP log row's
// status. An unknown one is refused: it would match nothing, and an empty
// result reads as "no such executions" rather than as a typo.
var mcpLogStatuses = []string{"success", "error", "processing"}

func parseMCPFilters(raw map[string]any) (*logstore.MCPToolLogSearchFilters, error) {
	now := Now()
	if raw == nil {
		raw = map[string]any{}
	}
	known := map[string]bool{
		"start_time": true, "end_time": true, "tool_names": true, "server_labels": true,
		"status": true, "virtual_key_ids": true, "team_ids": true, "customer_ids": true,
		"user_ids": true, "apps": true, "min_latency": true, "max_latency": true,
		"content_search": true,
	}
	unknown := []string{}
	for key := range raw {
		if !known[key] {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		supported := slices.Sorted(maps.Keys(known))
		return nil, fmt.Errorf("unknown filter fields: %s. Supported fields are: %s", strings.Join(unknown, ", "), strings.Join(supported, ", "))
	}
	if err := rejectNullTimeBounds(raw); err != nil {
		return nil, err
	}
	start, err := parseTime(raw["start_time"], now)
	if err != nil {
		return nil, fmt.Errorf("start_time: %w", err)
	}
	end, err := parseTime(raw["end_time"], now)
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
	filters := &logstore.MCPToolLogSearchFilters{StartTime: start, EndTime: end}
	for key, target := range map[string]*[]string{
		"tool_names": &filters.ToolNames, "server_labels": &filters.ServerLabels,
		"status": &filters.Status, "virtual_key_ids": &filters.VirtualKeyIDs,
		"team_ids": &filters.TeamIDs, "customer_ids": &filters.CustomerIDs,
		"user_ids": &filters.UserIDs, "apps": &filters.Apps,
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
	if entry.UserID != nil && *entry.UserID != "" {
		out["user_id"] = *entry.UserID
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
