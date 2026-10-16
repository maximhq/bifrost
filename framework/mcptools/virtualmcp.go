package mcptools

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/configstore/tables"
)

func listVirtualMCPsTool() Tool {
	return Tool{
		name:        "list_virtual_mcps",
		description: "Virtual MCP servers (id, name, slug, enabled). A virtual MCP is a named bundle of tools from one or more source clients.",
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
			rows, total, err := gov.GetVirtualMCPsPaginated(ctx, configstore.VirtualMCPsQueryParams{
				Limit: limit, Search: search,
			})
			if err != nil {
				return nil, fmt.Errorf("list virtual mcps failed: %w", err)
			}
			out := make([]map[string]any, 0, len(rows))
			for i := range rows {
				out = append(out, projectVirtualMCP(&rows[i], nil))
			}
			return map[string]any{"virtual_mcps": out, "total": total, "returned": len(out)}, nil
		},
	}
}

func describeVirtualMCPTool() Tool {
	return Tool{
		name:        "describe_virtual_mcp",
		description: "One virtual MCP: tool specs and attached virtual-key ids.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "virtual_mcp_id": {"type": "integer", "minimum": 1}
  },
  "required": ["virtual_mcp_id"]
}`,
		noLogs: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
			}
			id, err := uintArg(args, "virtual_mcp_id")
			if err != nil {
				return nil, err
			}
			row, err := gov.GetVirtualMCPByID(ctx, id)
			if err != nil {
				if errors.Is(err, configstore.ErrNotFound) {
					return nil, fmt.Errorf("no virtual mcp with id %d", id)
				}
				return nil, fmt.Errorf("virtual mcp lookup failed: %w", err)
			}
			vkIDs, err := gov.GetVirtualKeyIDsForVirtualMCP(ctx, id)
			if err != nil {
				return nil, fmt.Errorf("virtual mcp assignments failed: %w", err)
			}
			return projectVirtualMCP(row, vkIDs), nil
		},
	}
}

func createVirtualMCPTool() Tool {
	return Tool{
		name:        "create_virtual_mcp",
		description: "Create a virtual MCP server (a named bundle of tools from source MCP clients). Attach it to virtual keys afterwards.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "name": {"type": "string", "minLength": 1},
    "description": {"type": "string"},
    "endpoint_slug": {"type": "string", "description": "URL-safe slug served at /mcp/<slug>. Derived from name when omitted."},
    "enabled": {"type": "boolean"},
    "tools": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "mcp_client_id": {"type": "string"},
          "tool_names": {"type": "array", "items": {"type": "string"}, "description": "[\"*\"] means all tools from that client."}
        },
        "required": ["mcp_client_id", "tool_names"]
      }
    }
  },
  "required": ["name"]
}`,
		noLogs:   true,
		mutating: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			if _, err := stringArg(args, "name"); err != nil {
				return nil, err
			}
			body := adminBody{}.copySet(args, "name", "description", "endpoint_slug", "enabled", "tools")
			response, err := callAdmin(ctx, deps, http.MethodPost, "/api/mcp/virtual-mcps", body)
			if err != nil {
				return nil, err
			}
			return projectVirtualMCPResponse(response)
		},
	}
}

func updateVirtualMCPTool() Tool {
	return Tool{
		name:        "update_virtual_mcp",
		description: "Update a virtual MCP's name, description, enabled flag or tool specs. The endpoint slug is immutable.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "virtual_mcp_id": {"type": "integer", "minimum": 1},
    "name": {"type": "string"},
    "description": {"type": "string"},
    "enabled": {"type": "boolean"},
    "tools": {
      "type": "array",
      "items": {
        "type": "object",
        "properties": {
          "mcp_client_id": {"type": "string"},
          "tool_names": {"type": "array", "items": {"type": "string"}}
        },
        "required": ["mcp_client_id", "tool_names"]
      }
    }
  },
  "required": ["virtual_mcp_id"]
}`,
		noLogs:   true,
		mutating: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			id, err := uintArg(args, "virtual_mcp_id")
			if err != nil {
				return nil, err
			}
			path, err := adminPath("/api/mcp/virtual-mcps/{id}", strconv.FormatUint(uint64(id), 10))
			if err != nil {
				return nil, err
			}
			body := adminBody{}.copySet(args, "name", "description", "enabled", "tools")
			response, err := callAdmin(ctx, deps, http.MethodPut, path, body)
			if err != nil {
				return nil, err
			}
			return projectVirtualMCPResponse(response)
		},
	}
}

func attachVirtualMCPTool() Tool {
	return Tool{
		name:        "attach_virtual_mcp",
		description: "Grant a virtual key access to a virtual MCP.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "virtual_mcp_id": {"type": "integer", "minimum": 1},
    "virtual_key_id": {"type": "string", "minLength": 1}
  },
  "required": ["virtual_mcp_id", "virtual_key_id"]
}`,
		noLogs:   true,
		mutating: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			return mutateVirtualMCPAssignment(ctx, deps, args, true)
		},
	}
}

func detachVirtualMCPTool() Tool {
	return Tool{
		name:        "detach_virtual_mcp",
		description: "Revoke a virtual key's access to a virtual MCP.",
		schemaJSON: `{
  "type": "object",
  "properties": {
    "virtual_mcp_id": {"type": "integer", "minimum": 1},
    "virtual_key_id": {"type": "string", "minLength": 1}
  },
  "required": ["virtual_mcp_id", "virtual_key_id"]
}`,
		noLogs:   true,
		mutating: true,
		execute: func(ctx context.Context, deps *Deps, args map[string]any) (any, error) {
			return mutateVirtualMCPAssignment(ctx, deps, args, false)
		},
	}
}

func mutateVirtualMCPAssignment(ctx context.Context, deps *Deps, args map[string]any, attach bool) (any, error) {
	gov, err := requireGovernance(deps)
	if err != nil {
		return nil, err
	}
	id, err := uintArg(args, "virtual_mcp_id")
	if err != nil {
		return nil, err
	}
	vkID, err := stringArg(args, "virtual_key_id")
	if err != nil {
		return nil, err
	}
	path, err := adminPath("/api/mcp/virtual-mcps/{id}/virtual-keys/{vkId}", strconv.FormatUint(uint64(id), 10), vkID)
	if err != nil {
		return nil, err
	}
	method := http.MethodPost
	if !attach {
		method = http.MethodDelete
	}
	if _, err := callAdmin(ctx, deps, method, path, nil); err != nil {
		return nil, err
	}
	vkIDs, _ := gov.GetVirtualKeyIDsForVirtualMCP(ctx, id)
	return map[string]any{
		"virtual_mcp_id":  id,
		"virtual_key_id":  vkID,
		"attached":        attach,
		"virtual_key_ids": vkIDs,
	}, nil
}

// projectVirtualMCPResponse projects the definition a virtual MCP route answers
// with, and the keys it is assigned to when the route named them.
func projectVirtualMCPResponse(response []byte) (any, error) {
	// Only the fields projected below: the route formats its timestamps as
	// text of its own, which the table's time fields would not read.
	var answered struct {
		ID            uint                 `json:"id"`
		Name          string               `json:"name"`
		EndpointSlug  string               `json:"endpoint_slug"`
		Description   *string              `json:"description"`
		Enabled       bool                 `json:"enabled"`
		Tools         []tables.MCPToolSpec `json:"tools"`
		VirtualKeyIDs []string             `json:"virtual_key_ids"`
	}
	if err := decodeAdminField(response, "virtual_mcp", &answered); err != nil {
		return nil, err
	}
	return projectVirtualMCP(&tables.TableVirtualMCP{
		ID:           answered.ID,
		Name:         answered.Name,
		EndpointSlug: answered.EndpointSlug,
		Description:  answered.Description,
		Enabled:      answered.Enabled,
		ParsedTools:  answered.Tools,
	}, answered.VirtualKeyIDs), nil
}

func projectVirtualMCP(def *tables.TableVirtualMCP, vkIDs []string) map[string]any {
	out := map[string]any{
		"id":            def.ID,
		"name":          def.Name,
		"endpoint_slug": def.EndpointSlug,
		"enabled":       def.Enabled,
		"tools":         def.ParsedTools,
	}
	if def.Description != nil && *def.Description != "" {
		out["description"] = *def.Description
	}
	if vkIDs != nil {
		out["virtual_key_ids"] = vkIDs
	}
	return out
}
