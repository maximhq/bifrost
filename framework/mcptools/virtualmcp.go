package mcptools

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
			}
			name, err := stringArg(args, "name")
			if err != nil {
				return nil, err
			}
			description, hasDesc, err := optionalStringArg(args, "description")
			if err != nil {
				return nil, err
			}
			slug, _, err := optionalStringArg(args, "endpoint_slug")
			if err != nil {
				return nil, err
			}
			enabled := true
			if _, present := args["enabled"]; present {
				enabled, err = boolArg(args, "enabled")
				if err != nil {
					return nil, err
				}
			}
			tools, err := parseVirtualMCPTools(args["tools"])
			if err != nil {
				return nil, err
			}
			def := &tables.TableVirtualMCP{
				Name:         name,
				EndpointSlug: slug,
				Enabled:      enabled,
				ParsedTools:  tools,
			}
			if hasDesc {
				def.Description = &description
			}
			if err := gov.CreateVirtualMCP(ctx, def); err != nil {
				if errors.Is(err, configstore.ErrAlreadyExists) {
					return nil, fmt.Errorf("a virtual mcp named %q already exists", name)
				}
				return nil, fmt.Errorf("create virtual mcp failed: %w", err)
			}
			if reloader := requireReloader(deps); reloader != nil {
				reloaded, err := reloader.ReloadVirtualMCP(ctx, def.ID)
				if err != nil {
					return projectVirtualMCP(def, nil), fmt.Errorf("stored but live reload failed: %w", err)
				}
				if reloaded != nil {
					def = reloaded
				}
			}
			return projectVirtualMCP(def, nil), nil
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
			gov, err := requireGovernance(deps)
			if err != nil {
				return nil, err
			}
			id, err := uintArg(args, "virtual_mcp_id")
			if err != nil {
				return nil, err
			}
			def, err := gov.GetVirtualMCPByID(ctx, id)
			if err != nil {
				if errors.Is(err, configstore.ErrNotFound) {
					return nil, fmt.Errorf("no virtual mcp with id %d", id)
				}
				return nil, fmt.Errorf("virtual mcp lookup failed: %w", err)
			}
			if name, ok, err := optionalStringArg(args, "name"); err != nil {
				return nil, err
			} else if ok {
				def.Name = name
			}
			if description, ok, err := optionalStringArg(args, "description"); err != nil {
				return nil, err
			} else if ok {
				def.Description = &description
			}
			if _, present := args["enabled"]; present {
				enabled, err := boolArg(args, "enabled")
				if err != nil {
					return nil, err
				}
				def.Enabled = enabled
			}
			if _, present := args["tools"]; present {
				tools, err := parseVirtualMCPTools(args["tools"])
				if err != nil {
					return nil, err
				}
				def.ParsedTools = tools
			}
			if err := gov.UpdateVirtualMCP(ctx, def); err != nil {
				return nil, fmt.Errorf("update virtual mcp failed: %w", err)
			}
			if reloader := requireReloader(deps); reloader != nil {
				reloaded, err := reloader.ReloadVirtualMCP(ctx, def.ID)
				if err != nil {
					return projectVirtualMCP(def, nil), fmt.Errorf("stored but live reload failed: %w", err)
				}
				if reloaded != nil {
					def = reloaded
				}
			}
			return projectVirtualMCP(def, nil), nil
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
	if attach {
		// The HTTP handler checks the key exists; without it an attach to a
		// typo lands as a row pointing at nothing.
		if _, err := gov.GetVirtualKey(ctx, vkID); err != nil {
			if errors.Is(err, configstore.ErrNotFound) {
				return nil, fmt.Errorf("no virtual key with id %q", vkID)
			}
			return nil, fmt.Errorf("virtual key lookup failed: %w", err)
		}
		if err := gov.AttachVirtualMCPToVirtualKey(ctx, id, vkID); err != nil {
			return nil, fmt.Errorf("attach virtual mcp failed: %w", err)
		}
	} else {
		if err := gov.DetachVirtualMCPFromVirtualKey(ctx, id, vkID); err != nil {
			return nil, fmt.Errorf("detach virtual mcp failed: %w", err)
		}
	}
	// The key-to-virtual-MCP assignment the gateway enforces lives in its own
	// in-memory map; reloading the key or the definition does not touch it, so
	// without this a detach reported success while the key kept the tools.
	if reloader := requireReloader(deps); reloader != nil {
		var err error
		if attach {
			err = reloader.AttachVirtualMCPToVirtualKeyInMemory(ctx, vkID, id)
		} else {
			err = reloader.DetachVirtualMCPFromVirtualKeyInMemory(ctx, vkID, id)
		}
		if err != nil {
			verb := map[bool]string{true: "attached", false: "detached"}[attach]
			return nil, fmt.Errorf("%s in the database, but the live assignment was not updated, so it takes effect only after a restart: %w", verb, err)
		}
		_, _ = reloader.ReloadVirtualMCP(ctx, id)
		_, _ = reloader.ReloadVirtualKey(ctx, vkID)
	}
	vkIDs, _ := gov.GetVirtualKeyIDsForVirtualMCP(ctx, id)
	return map[string]any{
		"virtual_mcp_id":  id,
		"virtual_key_id":  vkID,
		"attached":        attach,
		"virtual_key_ids": vkIDs,
	}, nil
}

func parseVirtualMCPTools(raw any) ([]tables.MCPToolSpec, error) {
	if raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("tools must be an array, got %T", raw)
	}
	out := make([]tables.MCPToolSpec, 0, len(items))
	for i, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("tools[%d] must be an object", i)
		}
		clientID, err := stringArg(obj, "mcp_client_id")
		if err != nil {
			return nil, fmt.Errorf("tools[%d]: %w", i, err)
		}
		names, err := stringSliceAllowEmpty(obj, "tool_names")
		if err != nil {
			return nil, fmt.Errorf("tools[%d]: %w", i, err)
		}
		out = append(out, tables.MCPToolSpec{MCPClientID: clientID, ToolNames: names})
	}
	return out, nil
}

func stringSliceAllowEmpty(raw map[string]any, key string) ([]string, error) {
	value, present := raw[key]
	if !present || value == nil {
		return []string{}, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an array of strings", key)
	}
	result := make([]string, 0, len(items))
	for index, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("%s[%d] must be a string", key, index)
		}
		if strings.TrimSpace(text) == "" {
			return nil, fmt.Errorf("%s[%d] must not be empty", key, index)
		}
		result = append(result, text)
	}
	return result, nil
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
