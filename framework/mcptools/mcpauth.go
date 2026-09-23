package mcptools

import (
	"context"
	"fmt"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

func listMCPAuthSessionsTool() Tool {
	return Tool{
		name:        "list_mcp_auth_sessions",
		description: "Per-user OAuth tokens and header credentials for MCP clients. Returns status and identity, never access tokens or header values.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "mcp_client_id": {"type": "string"},
    "limit": {"type": "integer", "minimum": 1, "maximum": 20}
  }
}`,
		noLogs: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
			}
			limit, err := listLimit(args)
			if err != nil {
				return nil, err
			}
			params := configstore.MCPSessionsFilterParams{}
			if id, ok, err := optionalStringArg(args, "mcp_client_id"); err != nil {
				return nil, err
			} else if ok {
				params.MCPClientIDs = []string{id}
			}
			tokens, err := gov.ListOauthUserTokens(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("list mcp oauth sessions failed: %w", err)
			}
			headers, err := gov.ListMCPPerUserHeaderCredentials(ctx, params)
			if err != nil {
				return nil, fmt.Errorf("list mcp header sessions failed: %w", err)
			}
			out := make([]map[string]any, 0, len(tokens)+len(headers))
			for i := range tokens {
				out = append(out, projectOauthSession(&tokens[i]))
			}
			for i := range headers {
				out = append(out, projectHeaderSession(&headers[i]))
			}
			if len(out) > limit {
				out = out[:limit]
			}
			return map[string]any{"sessions": out, "returned": len(out)}, nil
		},
	}
}

func projectOauthSession(row *tables.TableMCPOauthToken) map[string]any {
	out := map[string]any{
		"id":            row.ID,
		"kind":          "oauth",
		"auth_mode":     row.AuthMode,
		"mcp_client_id": row.MCPClientID,
		"status":        row.Status,
		"has_refresh":   row.RefreshToken != "",
	}
	if row.VirtualKeyID != nil {
		out["virtual_key_id"] = *row.VirtualKeyID
	}
	if row.UserID != nil {
		out["user_id"] = *row.UserID
	}
	// The session id is a bearer credential: x-bf-mcp-session-id selects this
	// token or credential, so replaying it acts as the session upstream. Say
	// that one is bound, never what it is.
	if row.SessionID != "" {
		out["session_bound"] = true
	}
	if row.ExpiresAt != nil {
		out["expires_at"] = row.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z07:00")
	}
	if row.MCPClient != nil {
		out["mcp_client_name"] = row.MCPClient.Name
	}
	return out
}

func projectHeaderSession(row *tables.TableMCPPerUserHeaderCredential) map[string]any {
	out := map[string]any{
		"id":            row.ID,
		"kind":          "headers",
		"auth_mode":     row.AuthMode,
		"mcp_client_id": row.MCPClientID,
		"status":        row.Status,
	}
	if row.VirtualKeyID != nil {
		out["virtual_key_id"] = *row.VirtualKeyID
	}
	if row.UserID != nil {
		out["user_id"] = *row.UserID
	}
	// The session id is a bearer credential: x-bf-mcp-session-id selects this
	// token or credential, so replaying it acts as the session upstream. Say
	// that one is bound, never what it is.
	if row.SessionID != "" {
		out["session_bound"] = true
	}
	if row.MCPClient != nil {
		out["mcp_client_name"] = row.MCPClient.Name
	}
	return out
}
