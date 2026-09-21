package mcptools

import (
	"context"
	"fmt"

	"github.com/maximhq/bifrost/framework/logstore"
)

func getSessionTool() Tool {
	return Tool{
		name:        "get_session",
		description: "Requests in one conversation, newest last. Needs the session id from a log row or get_session_summary.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "session_id": {"type": "string", "minLength": 1},
    "limit": {"type": "integer", "minimum": 1, "maximum": 25}
  },
  "required": ["session_id"]
}`,
		tenantScoped: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			sessionID, err := stringArg(args, "session_id")
			if err != nil {
				return nil, err
			}
			limit, err := intArg(args, "limit", 10, MaxLogRows)
			if err != nil {
				return nil, err
			}
			result, err := deps.LogManager.GetSessionLogs(ctx, sessionID, &logstore.PaginationOptions{
				Limit: limit, Offset: 0, SortBy: "timestamp", Order: "asc",
			})
			if err != nil {
				return nil, fmt.Errorf("session lookup failed: %w", err)
			}
			if result == nil || result.Count == 0 {
				return nil, fmt.Errorf("no session with id %q", sessionID)
			}
			rows := make([]LogRow, 0, len(result.Logs))
			for i := range result.Logs {
				rows = append(rows, ProjectLog(&result.Logs[i], false, LogContentChars))
			}
			return map[string]any{
				"session_id": result.SessionID,
				"count":      result.Count,
				"has_more":   result.HasMore,
				"rows":       rows,
			}, nil
		},
	}
}

func getSessionSummaryTool() Tool {
	return Tool{
		name:        "get_session_summary",
		description: "Totals for one conversation: request count, cost, tokens, duration. Use get_session for the rows.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "session_id": {"type": "string", "minLength": 1}
  },
  "required": ["session_id"]
}`,
		tenantScoped: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			sessionID, err := stringArg(args, "session_id")
			if err != nil {
				return nil, err
			}
			result, err := deps.LogManager.GetSessionSummary(ctx, sessionID)
			if err != nil {
				return nil, fmt.Errorf("session summary failed: %w", err)
			}
			if result == nil || result.Count == 0 {
				return nil, fmt.Errorf("no session with id %q", sessionID)
			}
			return result, nil
		},
	}
}

func getDroppedRequestsTool() Tool {
	return Tool{
		name:        "get_dropped_requests",
		description: "How many requests this process dropped because the provider queue was full. A process-local counter, not a logstore query.",
		schemaJSON: `{
  "type": "object",
  "properties": {}
}`,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			return map[string]any{"dropped_requests": deps.LogManager.GetDroppedRequests(ctx)}, nil
		},
	}
}
