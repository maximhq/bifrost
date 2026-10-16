package mcptools

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

func listMCPAuthSessionsTool() Tool {
	return Tool{
		name: "list_mcp_auth_sessions",
		description: "Per-user OAuth tokens and header credentials for MCP clients, newest first. Returns status and identity, never access tokens or header values. " +
			"oauth_total and headers_total count each kind; page with offset, or pass kind to list one kind only.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "mcp_client_id": {"type": "string"},
    "kind": {"type": "string", "enum": ["oauth", "headers"], "description": "Only this kind of credential. Omit for both."},
    "status": {"type": "string", "description": "Only rows with this status, e.g. active, expired, revoked, needs_update."},
    "user_id": {"type": "string"},
    "virtual_key_id": {"type": "string"},
    "limit": {"type": "integer", "minimum": 1, "maximum": 20},
    "offset": {"type": "integer", "minimum": 0}
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
			offset := 0
			if n, err := optionalIntArg(args, "offset"); err != nil {
				return nil, err
			} else if n != nil {
				if *n < 0 {
					return nil, fmt.Errorf("offset must not be negative, got %d", *n)
				}
				offset = *n
			}
			kind, err := enumArg(args, "kind", "", []string{"oauth", "headers"})
			if err != nil {
				return nil, err
			}
			// Filters are pushed to the store, which applies them in SQL.
			params := configstore.MCPSessionsFilterParams{}
			for arg, field := range map[string]*[]string{
				"mcp_client_id":  &params.MCPClientIDs,
				"status":         &params.Statuses,
				"user_id":        &params.UserIDs,
				"virtual_key_id": &params.VirtualKeyIDs,
			} {
				if v, ok, err := optionalStringArg(args, arg); err != nil {
					return nil, err
				} else if ok {
					*field = []string{v}
				}
			}
			type session struct {
				created time.Time
				row     map[string]any
			}
			var rows []session
			oauthTotal, headersTotal := 0, 0
			if kind != "headers" {
				tokens, err := gov.ListOauthUserTokens(ctx, params)
				if err != nil {
					return nil, fmt.Errorf("list mcp oauth sessions failed: %w", err)
				}
				oauthTotal = len(tokens)
				for i := range tokens {
					rows = append(rows, session{tokens[i].CreatedAt, projectOauthSession(&tokens[i])})
				}
			}
			if kind != "oauth" {
				headers, err := gov.ListMCPPerUserHeaderCredentials(ctx, params)
				if err != nil {
					return nil, fmt.Errorf("list mcp header sessions failed: %w", err)
				}
				headersTotal = len(headers)
				for i := range headers {
					rows = append(rows, session{headers[i].CreatedAt, projectHeaderSession(&headers[i])})
				}
			}
			// One order across both tables, so a page is the newest rows of
			// either kind rather than every token before any header
			// credential - which hid the header credentials entirely once
			// tokens filled the page.
			sort.SliceStable(rows, func(i, j int) bool { return rows[i].created.After(rows[j].created) })
			total := len(rows)
			page := make([]map[string]any, 0, limit)
			for i := offset; i < total && len(page) < limit; i++ {
				page = append(page, rows[i].row)
			}
			return map[string]any{
				"sessions":      page,
				"returned":      len(page),
				"total":         total,
				"oauth_total":   oauthTotal,
				"headers_total": headersTotal,
				"has_more":      offset+len(page) < total,
			}, nil
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
