package handlers

import (
	"context"
	"slices"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/mark3labs/mcp-go/mcp"
	coremcp "github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// gatewayAppTools is what a sync learned about the MCP Apps tools it installed. Nil when there are none.
type gatewayAppTools struct {
	appOnly       map[string]struct{}  // tools only an MCP App may call
	aggregateMeta map[string]*mcp.Meta // _meta with ui:// URIs namespaced by client, for aggregate /mcp
}

// mcpAppsServerExtension declares MCP Apps on the gateway's capabilities.
var mcpAppsServerExtension = map[string]any{schemas.MCPAppsExtension: map[string]any{}}

// uiResourcePaths are the _meta keys naming a tool's ui:// resource: MCP Apps, its deprecated form,
// and the OpenAI Apps SDK's widget template.
var uiResourcePaths = []string{"ui.resourceUri", "ui/resourceUri", "openai/outputTemplate"}

// toolMeta is the _meta /mcp lists for a tool, and the namespaced one when it names a ui:// resource.
func toolMeta(tool schemas.ChatTool) (meta, aggregate *mcp.Meta) {
	if tool.MCP == nil || len(tool.MCP.Meta) == 0 {
		return nil, nil
	}
	meta = &mcp.Meta{}
	if sonic.Unmarshal(tool.MCP.Meta, meta) != nil {
		return nil, nil
	}
	clientName := coremcp.ToolClientName(tool.Function.Name)
	raw := []byte(tool.MCP.Meta)
	rewritten := false
	for _, path := range uiResourcePaths {
		uri := gjson.GetBytes(raw, path).String()
		if !strings.HasPrefix(uri, "ui://") {
			continue
		}
		if !rewritten {
			raw, rewritten = slices.Clone(raw), true // never edit the stored _meta
		}
		var err error
		if raw, err = sjson.SetBytes(raw, path, coremcp.GatewayUIResourceURI(clientName, uri)); err != nil {
			return meta, nil
		}
	}
	if !rewritten {
		return meta, nil
	}
	aggregate = &mcp.Meta{}
	if sonic.Unmarshal(raw, aggregate) != nil {
		return meta, nil
	}
	return meta, aggregate
}

// withAppTools records tool's MCP Apps facts on apps, allocating it on first use.
func withAppTools(apps *gatewayAppTools, tool schemas.ChatTool, aggregateMeta *mcp.Meta) *gatewayAppTools {
	appOnly := tool.MCP != nil && !tool.MCP.ModelVisible()
	if !appOnly && aggregateMeta == nil {
		return apps
	}
	if apps == nil {
		apps = &gatewayAppTools{appOnly: map[string]struct{}{}, aggregateMeta: map[string]*mcp.Meta{}}
	}
	if appOnly {
		apps.appOnly[tool.Function.Name] = struct{}{}
	}
	if aggregateMeta != nil {
		apps.aggregateMeta[tool.Function.Name] = aggregateMeta
	}
	return apps
}

// applyAppTools drops app-only tools the request may not use and, on aggregate /mcp, lists _meta with
// namespaced ui:// URIs. ctx carries the caller's capabilities; result.Tools is mcp-go's per-request copy.
func applyAppTools(ctx context.Context, apps *gatewayAppTools, result *mcp.ListToolsResult) {
	if apps == nil {
		return
	}
	if len(apps.appOnly) > 0 && !coremcp.AppToolsAllowed(ctx) {
		kept := result.Tools[:0]
		for _, tool := range result.Tools {
			if _, appOnly := apps.appOnly[tool.Name]; !appOnly {
				kept = append(kept, tool)
			}
		}
		result.Tools = kept
	}
	if len(apps.aggregateMeta) == 0 || !coremcp.GatewayAggregate(ctx) {
		return
	}
	for i, tool := range result.Tools {
		if meta, ok := apps.aggregateMeta[tool.Name]; ok {
			result.Tools[i].Meta = meta
		}
	}
}

// callersGatewayTool is a per-caller tool as tools/list sends it: on aggregate /mcp its _meta names
// its ui:// resources namespaced by client, as applyAppTools does for shared tools.
func callersGatewayTool(tool schemas.ChatTool, aggregate bool) mcp.Tool {
	listed := gatewayTool(tool)
	if aggregate {
		if _, namespaced := toolMeta(tool); namespaced != nil {
			listed.Meta = namespaced
		}
	}
	return listed
}
