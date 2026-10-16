package mcptools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

// bifrostMCPClientName is the reserved in-process server name. Duplicated from
// framework/warp so this package does not import it.
const bifrostMCPClientName = "bifrostmcp"

func listMCPClientsTool() Tool {
	return Tool{
		name:        "list_mcp_clients",
		description: "Configured MCP clients (id, name, connection type, disabled). Never returns connection strings, headers or tokens.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "search": {"type": "string"},
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
			search, _, err := optionalStringArg(args, "search")
			if err != nil {
				return nil, err
			}
			rows, total, err := gov.GetMCPClientsPaginated(ctx, configstore.MCPClientsQueryParams{
				Limit: limit, Search: search,
			})
			if err != nil {
				return nil, fmt.Errorf("list mcp clients failed: %w", err)
			}
			out := make([]map[string]any, 0, len(rows))
			for i := range rows {
				out = append(out, projectMCPClient(&rows[i]))
			}
			return map[string]any{"mcp_clients": out, "total": total, "returned": len(out)}, nil
		},
	}
}

func addMCPClientTool() Tool {
	return Tool{
		name:        "add_mcp_client",
		description: "Register an HTTP or SSE MCP client. Stdio is refused on this path (it would exec a command without the HTTP admin-auth gate). The name bifrostmcp is reserved. connection_string is stored and never returned.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "name": {"type": "string", "minLength": 1, "description": "ASCII, no hyphens or spaces, must not start with a digit."},
    "connection_type": {"type": "string", "enum": ["http", "sse"]},
    "connection_string": {"type": "string", "minLength": 1, "description": "HTTP or SSE URL."},
    "tools_to_execute": {"type": "array", "items": {"type": "string"}, "description": "Defaults to [\"*\"] (all tools)."}
  },
  "required": ["name", "connection_type", "connection_string"]
}`,
		noLogs:   true,
		mutating: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
			}
			for _, required := range []string{"name", "connection_string"} {
				if _, err := stringArg(args, required); err != nil {
					return nil, err
				}
			}
			// Checked here as well as by the route: a stdio client is a
			// command this process would run, and this tool does not offer it.
			connType, err := enumArg(args, "connection_type", "", []string{"http", "sse"})
			if err != nil {
				return nil, err
			}
			if connType == "" {
				return nil, fmt.Errorf("connection_type is required")
			}
			// The route answers without the id it gave the client, so the id
			// is chosen here and sent.
			id := uuid.NewString()
			body := adminBody{"client_id": id, "connection_type": connType, "tools_to_execute": []string{"*"}}
			body.copySet(args, "name", "connection_string", "tools_to_execute")
			if _, err := callAdmin(ctx, deps, http.MethodPost, "/api/mcp/client", body); err != nil {
				return nil, err
			}
			out := map[string]any{"client_id": id, "name": args["name"], "connection_type": connType, "live": true}
			if row, err := gov.GetMCPClientByID(ctx, id); err == nil && row != nil {
				out = projectMCPClient(row)
				out["tools_to_execute"] = []string(row.ToolsToExecute)
				out["live"] = true
			}
			return out, nil
		},
	}
}

func updateMCPClientTool() Tool {
	return Tool{
		name:        "update_mcp_client",
		description: "Update an MCP client's name, disabled flag or tools_to_execute. Connection type and URL are not changed here. Stdio clients cannot be created on this path.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "client_id": {"type": "string", "minLength": 1},
    "name": {"type": "string"},
    "disabled": {"type": "boolean"},
    "tools_to_execute": {"type": "array", "items": {"type": "string"}}
  },
  "required": ["client_id"]
}`,
		noLogs:   true,
		mutating: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
			}
			id, err := stringArg(args, "client_id")
			if err != nil {
				return nil, err
			}
			path, err := adminPath("/api/mcp/client/{id}", id)
			if err != nil {
				return nil, err
			}
			// A null list would reach the route as "not sent" and leave the
			// list alone, where the caller may have meant "none": an explicit
			// value is required so [] cannot be confused with it.
			if value, present := args["tools_to_execute"]; present && value == nil {
				return nil, fmt.Errorf(`tools_to_execute must be an array: ["*"] for every tool, [] for none; omit it to leave the list unchanged`)
			}
			body := adminBody{}.copyPresent(args, "name", "disabled", "tools_to_execute")
			if _, err := callAdmin(ctx, deps, http.MethodPut, path, body); err != nil {
				return nil, err
			}
			row, err := gov.GetMCPClientByID(ctx, id)
			if err != nil {
				return nil, fmt.Errorf("the change was made, but the client could not be read back: %w", err)
			}
			return projectMCPClient(row), nil
		},
	}
}

func reconnectMCPClientTool() Tool {
	return Tool{
		name:        "reconnect_mcp_client",
		description: "Re-dial one MCP client in this process. Needs a live MCP runtime.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "client_id": {"type": "string", "minLength": 1}
  },
  "required": ["client_id"]
}`,
		noLogs:   true,
		mutating: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			id, err := stringArg(args, "client_id")
			if err != nil {
				return nil, err
			}
			path, err := adminPath("/api/mcp/client/{id}/reconnect", id)
			if err != nil {
				return nil, err
			}
			if _, err := callAdmin(ctx, deps, http.MethodPost, path, nil); err != nil {
				return nil, err
			}
			return map[string]any{"client_id": id, "reconnected": true}, nil
		},
	}
}

func listMCPClientToolsTool() Tool {
	return Tool{
		name:        "list_mcp_client_tools",
		description: "Tools one MCP client offers: the live list from the connected client when it is loaded, otherwise the tools last discovered and stored. Names only; no argument payloads.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "client_id": {"type": "string", "minLength": 1}
  },
  "required": ["client_id"]
}`,
		noLogs: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
			}
			id, err := stringArg(args, "client_id")
			if err != nil {
				return nil, err
			}
			row, err := gov.GetMCPClientByID(ctx, id)
			if err != nil {
				if errors.Is(err, configstore.ErrNotFound) {
					return nil, fmt.Errorf("no mcp client with id %q", id)
				}
				return nil, fmt.Errorf("mcp client lookup failed: %w", err)
			}
			if deps.MCPTools != nil {
				if live, ok := deps.MCPTools(ctx, row.ClientID); ok {
					names := append([]string{}, live...)
					sort.Strings(names)
					return map[string]any{
						"client_id": row.ClientID,
						"name":      row.Name,
						"tools":     names,
						"returned":  len(names),
					}, nil
				}
			}
			names := []string{}
			if strings.TrimSpace(row.DiscoveredToolsJSON) != "" {
				var tools map[string]schemas.ChatTool
				if err := json.Unmarshal([]byte(row.DiscoveredToolsJSON), &tools); err == nil {
					for name := range tools {
						names = append(names, name)
					}
				}
			}
			sort.Strings(names)
			return map[string]any{
				"client_id": row.ClientID,
				"name":      row.Name,
				"tools":     names,
				"returned":  len(names),
			}, nil
		},
	}
}

// MCPToolLister names the tools the live MCP manager holds for one client,
// without the client prefix. ok is false when the manager has no such client
// (not connected, or not loaded on this node), and the stored row answers
// instead. Shared http, sse and openapi clients keep their tools only there.
type MCPToolLister func(ctx context.Context, clientID string) (tools []string, ok bool)

func projectMCPClient(row *tables.TableMCPClient) map[string]any {
	out := map[string]any{
		"client_id":       row.ClientID,
		"name":            row.Name,
		"connection_type": row.ConnectionType,
		"disabled":        row.Disabled,
		"auth_type":       row.AuthType,
	}
	if row.EndpointSlug != "" {
		out["endpoint_slug"] = row.EndpointSlug
	}
	return out
}
