package handlers

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	coremcp "github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// gatewayToolIndex is what a sync learned about the tools it installed, for the MCP Apps rules /mcp
// applies per request. Nil when no tool needs any.
type gatewayToolIndex struct {
	appOnly       map[string]struct{}           // tools only an MCP App may call
	aggregateMeta map[string]*mcp.Meta          // _meta with ui:// URIs namespaced by client, for aggregate /mcp
	aliases       map[string][]gatewayToolAlias // server-given name -> the tools it can stand for
	refusal       map[string]struct{}           // server-given names installed as hidden tools so a refusal can explain itself
}

// gatewayToolAlias is a tool a server-given (unprefixed) name can stand for. MCP Apps call tools by the
// names their own server gave them, so /mcp accepts those too.
type gatewayToolAlias struct {
	name      string // prefixed tool name
	client    string
	appOnly   bool // for a shared tool; a per-caller tool's is the caller's own
	perCaller bool // a placeholder for a client with per_user_discovery on: which caller holds it varies
}

// hasAppTools reports whether any tool is app-only or names a ui:// resource.
func (index *gatewayToolIndex) hasAppTools() bool {
	return index != nil && (len(index.appOnly) > 0 || len(index.aggregateMeta) > 0)
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

// indexTool records tool on index, allocating it on first use: its MCP Apps facts and its server-given name.
func indexTool(index *gatewayToolIndex, tool schemas.ChatTool, aggregateMeta *mcp.Meta) *gatewayToolIndex {
	appOnly := tool.MCP != nil && !tool.MCP.ModelVisible()
	client := coremcp.ToolClientName(tool.Function.Name)
	if !appOnly && aggregateMeta == nil && client == "" {
		return index
	}
	if index == nil {
		index = &gatewayToolIndex{}
	}
	if appOnly {
		if index.appOnly == nil {
			index.appOnly = map[string]struct{}{}
		}
		index.appOnly[tool.Function.Name] = struct{}{}
	}
	if aggregateMeta != nil {
		if index.aggregateMeta == nil {
			index.aggregateMeta = map[string]*mcp.Meta{}
		}
		index.aggregateMeta[tool.Function.Name] = aggregateMeta
	}
	if client != "" {
		if index.aliases == nil {
			index.aliases = map[string][]gatewayToolAlias{}
		}
		bare := coremcp.StripClientPrefix(tool.Function.Name, client)
		index.aliases[bare] = append(index.aliases[bare], gatewayToolAlias{name: tool.Function.Name, client: client, appOnly: appOnly})
	}
	return index
}

// applyAppTools drops app-only tools the request may not use and, on aggregate /mcp, lists _meta with
// namespaced ui:// URIs. ctx carries the caller's capabilities; result.Tools is mcp-go's per-request copy.
func applyAppTools(ctx context.Context, index *gatewayToolIndex, result *mcp.ListToolsResult) {
	if !index.hasAppTools() {
		return
	}
	if len(index.appOnly) > 0 && !coremcp.AppToolsAllowed(ctx) {
		kept := result.Tools[:0]
		for _, tool := range result.Tools {
			if _, appOnly := index.appOnly[tool.Name]; !appOnly {
				kept = append(kept, tool)
			}
		}
		result.Tools = kept
	}
	if len(index.aggregateMeta) == 0 || !coremcp.GatewayAggregate(ctx) {
		return
	}
	for i, tool := range result.Tools {
		if meta, ok := index.aggregateMeta[tool.Name]; ok {
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

// indexPerCallerNames records the server-given name of each per-caller placeholder tool, allocating
// index on first use. Connect and code-mode tools are Bifrost's names, not a server's, and get none.
func indexPerCallerNames(index *gatewayToolIndex, perCaller *perCallerEntries) *gatewayToolIndex {
	if perCaller == nil {
		return index
	}
	for name := range perCaller.tools {
		if _, connect := perCaller.connects[name]; connect {
			continue
		}
		client := coremcp.ToolClientName(name)
		if client == "" {
			continue
		}
		if index == nil {
			index = &gatewayToolIndex{}
		}
		if index.aliases == nil {
			index.aliases = map[string][]gatewayToolAlias{}
		}
		bare := coremcp.StripClientPrefix(name, client)
		index.aliases[bare] = append(index.aliases[bare], gatewayToolAlias{name: name, client: client, perCaller: true})
	}
	return index
}

// refusalTools drops aliases that are already a tool's exact name (exact names win), and installs a
// hidden tool for each name a request could be refused (several servers have it, or an app-only tool
// does) so the refusal can say why. Resolved names never reach these: resolveCallToolName renames them.
func (h *MCPServerHandler) refusalTools(index *gatewayToolIndex, installed []server.ServerTool) []server.ServerTool {
	if index == nil {
		return installed
	}
	exact := make(map[string]struct{}, len(installed))
	for _, tool := range installed {
		exact[tool.Tool.Name] = struct{}{}
	}
	for bare, candidates := range index.aliases {
		if _, taken := exact[bare]; taken {
			delete(index.aliases, bare)
			continue
		}
		// A per-caller candidate may be app-only, or missing, for the caller, so it can be refused too.
		if len(candidates) == 1 && !candidates[0].appOnly && !candidates[0].perCaller {
			continue
		}
		if index.refusal == nil {
			index.refusal = map[string]struct{}{}
		}
		index.refusal[bare] = struct{}{}
		installed = append(installed, server.ServerTool{
			Tool: mcp.Tool{Name: bare, InputSchema: mcp.ToolInputSchema{Type: "object"}},
			Handler: func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				var refusal toolAliasRefusal
				if _, err := h.resolveToolAlias(ctx, bare); errors.As(err, &refusal) {
					return mcp.NewToolResultError(string(refusal)), nil
				}
				// Not found for this caller, or resolvable only since the hook ran (a sync in between).
				return nil, toolNotFoundError(bare)
			},
		})
	}
	return installed
}

// resolveCallToolName renames a tools/call for a server-given name to the tool it stands for, before
// mcp-go looks the tool up (and after it checked Mcp-Name against the name the caller sent). Exact
// names cost one map lookup.
func (h *MCPServerHandler) resolveCallToolName(ctx context.Context, _ any, request *mcp.CallToolRequest) {
	index := h.toolIndex.Load()
	if index == nil || request == nil {
		return
	}
	if _, alias := index.aliases[request.Params.Name]; !alias {
		return
	}
	if target, err := h.resolveToolAlias(ctx, request.Params.Name); err == nil {
		request.Params.Name = target.name
	}
}

// toolAliasRefusal is why a server-given name the caller can reach resolves to no tool; it answers as
// a tool error so a model calling through the gateway can retry with a full name.
type toolAliasRefusal string

func (r toolAliasRefusal) Error() string { return string(r) }

// toolNotFoundError is mcp-go's own answer for an unknown tool, so a name the caller cannot reach
// looks the same as one that does not exist.
func toolNotFoundError(name string) error {
	return server.NewHandlerError(jsonRPCError{code: mcp.INVALID_PARAMS, message: fmt.Sprintf("tool '%s' not found: %v", name, server.ErrToolNotFound)})
}

// resolveToolAlias picks the tool a server-given name means for this request: on /mcp/<slug> for one
// server, that server's tool; on /mcp and Virtual MCPs, the one reachable tool by that name that models
// and apps may both call. Otherwise a toolAliasRefusal, or toolNotFoundError when the caller reaches none.
func (h *MCPServerHandler) resolveToolAlias(ctx context.Context, bare string) (gatewayToolAlias, error) {
	var candidates []gatewayToolAlias
	if index := h.toolIndex.Load(); index != nil {
		candidates = index.aliases[bare]
	}
	if client, _ := ctx.Value(schemas.BifrostContextKeyMCPGatewayClient).(string); client != "" {
		if i := slices.IndexFunc(candidates, func(c gatewayToolAlias) bool { return c.client == client }); i >= 0 {
			return candidates[i], nil
		}
		return gatewayToolAlias{}, toolNotFoundError(bare)
	}
	// The caller's own tools decide when its include lists narrow what it reaches, or when a candidate is
	// per-caller. WithAllTools keeps the include lists but not the app-only rule, so an app-only tool the
	// caller can reach is refused as app-only, as on /mcp, rather than as unknown.
	var callers map[string]bool // reachable tool name -> app-only
	if requestNarrowed(ctx) || slices.ContainsFunc(candidates, func(c gatewayToolAlias) bool { return c.perCaller }) {
		tools := h.toolManager.GetAvailableMCPTools(coremcp.WithAllTools(withCallerCapabilities(ctx)))
		callers = make(map[string]bool, len(tools))
		for _, tool := range tools {
			if tool.Function != nil {
				callers[tool.Function.Name] = !tool.MCP.ModelVisible()
			}
		}
	}
	var usable []gatewayToolAlias
	appOnly := false
	for _, c := range candidates {
		candidateAppOnly := c.appOnly
		if callers != nil {
			callerAppOnly, ok := callers[c.name]
			if !ok {
				continue
			}
			candidateAppOnly = callerAppOnly
		}
		if candidateAppOnly {
			appOnly = true
			continue
		}
		usable = append(usable, c)
	}
	switch {
	case len(usable) == 1:
		return usable[0], nil
	case len(usable) > 1:
		full := make([]string, len(usable))
		for i, c := range usable {
			full[i] = c.name
		}
		return gatewayToolAlias{}, toolAliasRefusal(fmt.Sprintf("tool '%s' is ambiguous here: call one of %s by its full name", bare, strings.Join(full, ", ")))
	case appOnly:
		return gatewayToolAlias{}, toolAliasRefusal(fmt.Sprintf("tool '%s' is only callable from an MCP App, on its server's own /mcp/<slug> endpoint", bare))
	}
	return gatewayToolAlias{}, toolNotFoundError(bare)
}

// dropRefusalTools leaves the hidden refusal tools out of a tools/list answer.
func dropRefusalTools(index *gatewayToolIndex, result *mcp.ListToolsResult) {
	if index == nil || len(index.refusal) == 0 {
		return
	}
	kept := result.Tools[:0]
	for _, tool := range result.Tools {
		if _, hidden := index.refusal[tool.Name]; !hidden {
			kept = append(kept, tool)
		}
	}
	result.Tools = kept
}
