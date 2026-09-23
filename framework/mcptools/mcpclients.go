package mcptools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
				out = append(out, projectMCPClient(&rows[i], false))
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
			name, err := stringArg(args, "name")
			if err != nil {
				return nil, err
			}
			if err := validateMCPClientName(name); err != nil {
				return nil, err
			}
			if name == bifrostMCPClientName {
				return nil, fmt.Errorf("name %q is reserved for Bifrost's built-in MCP server", name)
			}
			connType, err := enumArg(args, "connection_type", "", []string{"http", "sse"})
			if err != nil {
				return nil, err
			}
			if connType == "" {
				return nil, fmt.Errorf("connection_type is required")
			}
			url, err := stringArg(args, "connection_string")
			if err != nil {
				return nil, err
			}
			tools := schemas.WhiteList{"*"}
			if raw, present := args["tools_to_execute"]; present && raw != nil {
				parsed, err := stringSliceAllowEmpty(map[string]any{"tools_to_execute": raw}, "tools_to_execute")
				if err != nil {
					return nil, err
				}
				tools = schemas.WhiteList(parsed)
			}
			cfg := &schemas.MCPClientConfig{
				ID:               uuid.NewString(),
				Name:             name,
				ConnectionType:   schemas.MCPConnectionType(connType),
				ConnectionString: schemas.NewSecretVar(url),
				ToolsToExecute:   tools,
			}
			if err := gov.CreateMCPClientConfig(ctx, cfg); err != nil {
				if errors.Is(err, configstore.ErrAlreadyExists) {
					return nil, fmt.Errorf("an mcp client named %q already exists", name)
				}
				return nil, fmt.Errorf("add mcp client failed: %w", err)
			}
			live := false
			if deps.MCPRuntime != nil {
				if err := deps.MCPRuntime.AddMCPClient(ctx, cfg); err != nil {
					return map[string]any{
						"client_id": cfg.ID,
						"name":      cfg.Name,
						"live":      false,
						"note":      "stored, but live dial failed: " + err.Error(),
					}, nil
				}
				live = true
			}
			out := map[string]any{
				"client_id":        cfg.ID,
				"name":             cfg.Name,
				"connection_type":  connType,
				"tools_to_execute": []string(tools),
				"live":             live,
			}
			if !live {
				out["note"] = "stored; may not be live until the process reconnects MCP clients"
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
			row, err := gov.GetMCPClientByID(ctx, id)
			if err != nil {
				if errors.Is(err, configstore.ErrNotFound) {
					return nil, fmt.Errorf("no mcp client with id %q", id)
				}
				return nil, fmt.Errorf("mcp client lookup failed: %w", err)
			}
			if row.Name == bifrostMCPClientName {
				return nil, fmt.Errorf("the built-in %q client cannot be updated here", bifrostMCPClientName)
			}
			if name, ok, err := optionalStringArg(args, "name"); err != nil {
				return nil, err
			} else if ok {
				if err := validateMCPClientName(name); err != nil {
					return nil, err
				}
				if name == bifrostMCPClientName {
					return nil, fmt.Errorf("name %q is reserved", name)
				}
				row.Name = name
			}
			if _, present := args["disabled"]; present {
				disabled, err := boolArg(args, "disabled")
				if err != nil {
					return nil, err
				}
				row.Disabled = disabled
			}
			var toolsToExecute schemas.WhiteList
			_, hasTools := args["tools_to_execute"]
			if hasTools {
				// Null used to be skipped, reading as "leave it alone"; an
				// explicit value is required so [] (no tools) cannot be
				// confused with it.
				if args["tools_to_execute"] == nil {
					return nil, fmt.Errorf(`tools_to_execute must be an array: ["*"] for every tool, [] for none; omit it to leave the list unchanged`)
				}
				parsed, err := stringSliceAllowEmpty(map[string]any{"tools_to_execute": args["tools_to_execute"]}, "tools_to_execute")
				if err != nil {
					return nil, err
				}
				toolsToExecute = schemas.WhiteList(parsed)
				// The store serializes the decoded field, not the JSON column:
				// setting only ToolsToExecuteJSON was silently overwritten with
				// the old list on save.
				row.ToolsToExecute = toolsToExecute
			}
			if err := gov.UpdateMCPClientConfig(ctx, id, row); err != nil {
				return nil, fmt.Errorf("update mcp client failed: %w", err)
			}
			// Apply the same edit to the live client, as the HTTP handler does;
			// stored alone, a disable kept the client connected and serving
			// tools until restart.
			if deps.MCPClients != nil {
				live, err := deps.MCPClients.GetMCPClientConfig(id)
				if err != nil {
					return nil, fmt.Errorf("stored, but the live client was not found, so the change applies after the next restart: %w", err)
				}
				live.Name = row.Name
				live.Disabled = row.Disabled
				if hasTools {
					live.ToolsToExecute = toolsToExecute
				}
				if err := deps.MCPClients.UpdateMCPClient(ctx, id, live); err != nil {
					return nil, fmt.Errorf("stored, but applying it to the live client failed, so it takes effect after the next restart: %w", err)
				}
			}
			return projectMCPClient(row, false), nil
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
			if deps.MCPRuntime == nil {
				return nil, fmt.Errorf("mcp runtime is not available; the client cannot be redialed from this process")
			}
			if err := deps.MCPRuntime.ReconnectMCPClient(ctx, id); err != nil {
				return nil, fmt.Errorf("reconnect mcp client failed: %w", err)
			}
			return map[string]any{"client_id": id, "reconnected": true}, nil
		},
	}
}

func listMCPClientToolsTool() Tool {
	return Tool{
		name:        "list_mcp_client_tools",
		description: "Tools last discovered on one MCP client. Names only; no argument payloads.",
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
			names := []string{}
			if strings.TrimSpace(row.DiscoveredToolsJSON) != "" {
				var tools map[string]schemas.ChatTool
				if err := json.Unmarshal([]byte(row.DiscoveredToolsJSON), &tools); err == nil {
					for name := range tools {
						names = append(names, name)
					}
				}
			}
			return map[string]any{
				"client_id": row.ClientID,
				"name":      row.Name,
				"tools":     names,
				"returned":  len(names),
			}, nil
		},
	}
}

func projectMCPClient(row *tables.TableMCPClient, _ bool) map[string]any {
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

func validateMCPClientName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("name is required for MCP client")
	}
	for _, r := range name {
		if r > 127 {
			return fmt.Errorf("name must contain only ASCII characters")
		}
	}
	if strings.Contains(name, "-") {
		return fmt.Errorf("name cannot contain hyphens")
	}
	if strings.Contains(name, " ") {
		return fmt.Errorf("name cannot contain spaces")
	}
	if len(name) > 0 && name[0] >= '0' && name[0] <= '9' {
		return fmt.Errorf("name cannot start with a number")
	}
	return nil
}
