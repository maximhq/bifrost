package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	coremcp "github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
)

// Serving clients with per_user_discovery on.
//
// Such a client has no tool list of its own: each caller's tools, prompts and resources are
// discovered with that caller's credential. The shared server is filled with no caller, so for these
// clients it holds placeholders: each client's connect tool, and the names seen across its callers'
// discoveries, with no description, schema or other detail from any of them. A placeholder only lets
// tools/call, prompts/get and resources/read reach a handler here. The list hooks replace each
// placeholder with the caller's own entry or drop it, and a placeholder the caller does not hold is
// answered exactly as mcp-go answers a name nobody holds, so names cannot be probed for what another
// caller's account has.

// perCallerNamesTimeout bounds the read of the names a sync registers.
const perCallerNamesTimeout = 10 * time.Second

// perCallerEntries are the placeholders the shared server holds for clients with per_user_discovery on.
type perCallerEntries struct {
	tools     map[string]struct{} // <client>-connect and <client>-<tool>
	connects  map[string]struct{} // each client's <client>-connect, also in tools
	prompts   map[string]struct{} // <client>-<prompt>
	resources map[string]struct{} // resource URIs
	templates map[string]struct{} // resource template URIs
}

func newPerCallerEntries() *perCallerEntries {
	return &perCallerEntries{tools: map[string]struct{}{}, connects: map[string]struct{}{}, prompts: map[string]struct{}{}, resources: map[string]struct{}{}, templates: map[string]struct{}{}}
}

func (e *perCallerEntries) holdsTool(name string) bool {
	if e == nil {
		return false
	}
	_, ok := e.tools[name]
	return ok
}

func (e *perCallerEntries) equal(other *perCallerEntries) bool {
	if e == nil || other == nil {
		return e == other
	}
	return maps.Equal(e.tools, other.tools) && maps.Equal(e.connects, other.connects) && maps.Equal(e.prompts, other.prompts) &&
		maps.Equal(e.resources, other.resources) && maps.Equal(e.templates, other.templates)
}

// union holds every placeholder of e and other, so that while the server swaps one set for the
// other, the list hooks treat every placeholder either set registers as one.
func (e *perCallerEntries) union(other *perCallerEntries) *perCallerEntries {
	if e == nil {
		return other
	}
	if other == nil {
		return e
	}
	merged := newPerCallerEntries()
	for _, from := range []*perCallerEntries{e, other} {
		maps.Copy(merged.tools, from.tools)
		maps.Copy(merged.connects, from.connects)
		maps.Copy(merged.prompts, from.prompts)
		maps.Copy(merged.resources, from.resources)
		maps.Copy(merged.templates, from.templates)
	}
	return merged
}

// codeModeToolNames are the tools a caller reaches a code-mode client through.
var codeModeToolNames = []string{coremcp.ToolTypeListToolFiles, coremcp.ToolTypeReadToolFile, coremcp.ToolTypeGetToolDocs, coremcp.ToolTypeExecuteToolCode}

// loadPerCallerEntries reads the placeholders for every enabled client with per_user_discovery on,
// or nil when there is none. When the names cannot be read it returns the clients' connect tools
// with the read error, and the caller keeps the names it already installed.
func (h *MCPServerHandler) loadPerCallerEntries(ctx context.Context) (*perCallerEntries, error) {
	if h.config == nil {
		return nil, nil
	}
	clients := h.config.GetMCPPerUserDiscoveryClients()
	if len(clients) == 0 {
		return nil, nil
	}
	entries := newPerCallerEntries()
	ids := make([]string, 0, len(clients))
	for id, client := range clients {
		ids = append(ids, id)
		entries.tools[coremcp.ConnectToolName(client.Name)] = struct{}{}
		entries.connects[coremcp.ConnectToolName(client.Name)] = struct{}{}
		if client.CodeMode {
			// The shared list holds the code-mode tools only for a client it can see, which a
			// client with per_user_discovery on is not while no caller is known.
			for _, name := range codeModeToolNames {
				entries.tools[name] = struct{}{}
			}
		}
	}
	if h.config.ConfigStore == nil {
		return entries, nil
	}
	ctx, cancel := context.WithTimeout(ctx, perCallerNamesTimeout)
	defer cancel()
	names, err := h.config.ConfigStore.GetMCPPerUserDiscoveryNames(ctx, ids)
	if err != nil {
		return entries, fmt.Errorf("failed to read the names in per-user MCP discoveries: %w", err)
	}
	for id, held := range names {
		prefix := clients[id].Name + "-"
		for _, tool := range held.Tools {
			entries.tools[prefix+tool] = struct{}{}
		}
		for _, prompt := range held.Prompts {
			entries.prompts[prefix+prompt] = struct{}{}
		}
		for _, uri := range held.Resources {
			entries.resources[uri] = struct{}{}
		}
		for _, uri := range held.ResourceTemplates {
			entries.templates[uri] = struct{}{}
		}
	}
	return entries, nil
}

// ResyncPerCallerEntries brings the placeholders up to date after a discovery of a client with
// per_user_discovery on was written or deleted, by this process or another. The server is replaced
// only when a placeholder was added or dropped. Listening clients are told the lists changed either
// way: a write whose names other callers already hold still changes what its own caller lists.
func (h *MCPServerHandler) ResyncPerCallerEntries(ctx context.Context) error {
	h.syncMu.Lock()
	defer h.syncMu.Unlock()
	next, err := h.loadPerCallerEntries(ctx)
	if err != nil {
		return err
	}
	if next.equal(h.perCaller.Load()) {
		h.notifyListsChanged()
		return nil
	}
	h.install(next)
	return nil
}

// notifyListsChanged tells every listening client to list its tools, prompts and resources again,
// for the lists the server declares it announces changes to.
func (h *MCPServerHandler) notifyListsChanged() {
	h.mcpServer.SendNotificationToAllClients(mcp.MethodNotificationToolsListChanged, nil)
	if h.served != nil {
		h.mcpServer.SendNotificationToAllClients(mcp.MethodNotificationPromptsListChanged, nil)
		h.mcpServer.SendNotificationToAllClients(mcp.MethodNotificationResourcesListChanged, nil)
	}
}

// perCallerServerTools is a placeholder server tool for each per-caller tool name, skipping a name a
// shared tool already holds.
func (h *MCPServerHandler) perCallerServerTools(entries *perCallerEntries, shared []server.ServerTool) []server.ServerTool {
	if entries == nil {
		return nil
	}
	taken := make(map[string]struct{}, len(shared))
	for _, tool := range shared {
		taken[tool.Tool.Name] = struct{}{}
	}
	tools := make([]server.ServerTool, 0, len(entries.tools))
	for _, name := range slices.Sorted(maps.Keys(entries.tools)) {
		if _, ok := taken[name]; ok {
			continue
		}
		_, connect := entries.connects[name]
		tools = append(tools, server.ServerTool{
			Tool:    mcp.Tool{Name: name, InputSchema: mcp.ToolInputSchema{Type: "object"}},
			Handler: h.perCallerToolHandler(name, connect),
		})
	}
	return tools
}

// perCallerToolHandler runs name as the caller's own tool, with the caller's own output schema, or
// answers as for a name nobody holds when the caller does not hold it. A client's connect tool is
// the exception: it is listed only until the caller connects, but a call to it always reaches
// Bifrost, which answers it as on every other path (already connected, a sign-in link, or refused).
func (h *MCPServerHandler) perCallerToolHandler(name string, connect bool) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		own, ok := callersTool(h.toolManager.GetAvailableMCPTools(ctx), name)
		if !ok && connect {
			return h.runTool(ctx, name, false, request), nil
		}
		if !ok {
			return nil, server.NewHandlerError(jsonRPCError{
				code:    mcp.INVALID_PARAMS,
				message: fmt.Errorf("tool '%s' not found: %w", name, server.ErrToolNotFound).Error(),
			})
		}
		return h.runTool(ctx, name, own.MCP != nil && len(own.MCP.OutputSchema) > 0, request), nil
	}
}

// callersTool finds name among the caller's tools.
func callersTool(tools []schemas.ChatTool, name string) (schemas.ChatTool, bool) {
	for _, tool := range tools {
		if tool.Function != nil && tool.Function.Name == name {
			return tool, true
		}
	}
	return schemas.ChatTool{}, false
}

// perCallerServerPrompts is a placeholder for each per-caller prompt name a shared prompt does not hold.
func (h *MCPServerHandler) perCallerServerPrompts(entries *perCallerEntries, shared []mcp.Prompt) []server.ServerPrompt {
	if entries == nil {
		return nil
	}
	var prompts []server.ServerPrompt
	for _, name := range slices.Sorted(maps.Keys(entries.prompts)) {
		if slices.ContainsFunc(shared, func(p mcp.Prompt) bool { return p.Name == name }) {
			continue
		}
		prompts = append(prompts, server.ServerPrompt{
			Prompt: mcp.Prompt{Name: name},
			Handler: func(ctx context.Context, request mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
				if !slices.ContainsFunc(h.served.GetAvailableMCPPrompts(ctx), func(p mcp.Prompt) bool { return p.Name == name }) {
					return nil, server.NewHandlerError(jsonRPCError{
						code:    mcp.INVALID_PARAMS,
						message: fmt.Errorf("prompt '%s' not found: %w", name, server.ErrPromptNotFound).Error(),
					})
				}
				return h.getPrompt(ctx, request)
			},
		})
	}
	return prompts
}

// perCallerServerResources is a placeholder for each per-caller resource URI a shared resource does
// not hold.
func (h *MCPServerHandler) perCallerServerResources(entries *perCallerEntries, shared []mcp.Resource) []server.ServerResource {
	if entries == nil {
		return nil
	}
	var resources []server.ServerResource
	for _, uri := range slices.Sorted(maps.Keys(entries.resources)) {
		if slices.ContainsFunc(shared, func(r mcp.Resource) bool { return r.URI == uri }) {
			continue
		}
		resources = append(resources, server.ServerResource{
			Resource: mcp.Resource{URI: uri, Name: uri},
			Handler: func(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
				if !slices.ContainsFunc(h.served.GetAvailableMCPResources(ctx), func(r mcp.Resource) bool { return r.URI == uri }) {
					return nil, resourceNotFound(ctx, request.Params.URI)
				}
				return h.readResource(ctx, request)
			},
		})
	}
	return resources
}

// perCallerServerTemplates is a placeholder for each per-caller resource template a shared template
// does not hold. A template that does not parse is left out: no caller could read through it either.
func (h *MCPServerHandler) perCallerServerTemplates(entries *perCallerEntries, shared []mcp.ResourceTemplate) []server.ServerResourceTemplate {
	if entries == nil {
		return nil
	}
	var templates []server.ServerResourceTemplate
	for _, uri := range slices.Sorted(maps.Keys(entries.templates)) {
		if slices.ContainsFunc(shared, func(t mcp.ResourceTemplate) bool { return coremcp.ResourceTemplateURI(t) == uri }) {
			continue
		}
		var template mcp.ResourceTemplate
		if err := json.Unmarshal(fmt.Appendf(nil, `{"uriTemplate":%q,"name":%q}`, uri, uri), &template); err != nil {
			continue
		}
		templates = append(templates, server.ServerResourceTemplate{
			Template: template,
			Handler: func(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
				if !slices.ContainsFunc(h.served.GetAvailableMCPResourceTemplates(ctx), func(t mcp.ResourceTemplate) bool { return coremcp.ResourceTemplateURI(t) == uri }) {
					return nil, resourceNotFound(ctx, request.Params.URI)
				}
				return h.readResource(ctx, request)
			},
		})
	}
	return templates
}

// resourceNotFound is what mcp-go answers for a URI no resource or template serves.
func resourceNotFound(ctx context.Context, uri string) error {
	code := mcp.RESOURCE_NOT_FOUND
	if mcp.IsModernProtocol(server.RequestProtocolVersion(ctx)) {
		code = mcp.INVALID_PARAMS
	}
	return server.NewHandlerError(jsonRPCError{
		code:    code,
		message: fmt.Errorf("handler not found for resource URI '%s': %w", uri, server.ErrResourceNotFound).Error(),
	})
}
