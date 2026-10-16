package mcptools

import (
	"context"
	"errors"
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
			if errors.Is(err, logstore.ErrNotFound) || (err == nil && (result == nil || result.Count == 0)) {
				return nil, sessionNotFound(sessionID, scopeFromContext(ctx))
			}
			if err != nil {
				return nil, fmt.Errorf("session lookup failed: %w", err)
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
			if errors.Is(err, logstore.ErrNotFound) || (err == nil && (result == nil || result.Count == 0)) {
				return nil, sessionNotFound(sessionID, scopeFromContext(ctx))
			}
			if err != nil {
				return nil, fmt.Errorf("session summary failed: %w", err)
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

// sessionNotFound is logNotFound for a session id. A session counts the rows
// the caller may see, so one whose every request is outside their view loads
// as empty exactly as an unknown id does, and the refusal says what was
// searched rather than calling the id wrong.
func sessionNotFound(id string, scope Scope) error {
	if scope.Visibility == "" {
		return fmt.Errorf("no session found with id %q", id)
	}
	return fmt.Errorf("no session found with id %q among the requests the person asking may see (%s). A session outside that is not visible to them, so say their view is limited to it rather than that the id does not exist or is mistyped", id, scope.Visibility)
}
