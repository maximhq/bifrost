package handlers

import (
	"context"
	"errors"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	bifrost "github.com/maximhq/bifrost/core"
	coremcp "github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
)

// MCPResourcesAndPromptsServer serves upstream resources and prompts on /mcp, scoped by ctx like
// tools. Optional: without it /mcp serves tools only.
type MCPResourcesAndPromptsServer interface {
	GetAvailableMCPPrompts(ctx context.Context) []mcp.Prompt
	GetAvailableMCPResources(ctx context.Context) []mcp.Resource
	GetAvailableMCPResourceTemplates(ctx context.Context) []mcp.ResourceTemplate
	GetMCPPrompt(ctx context.Context, name string, arguments map[string]string) (*mcp.GetPromptResult, *schemas.BifrostError)
	ReadMCPResource(ctx context.Context, uri string) (*mcp.ReadResourceResult, *schemas.BifrostError)
	CompleteMCPArgument(ctx context.Context, params mcp.CompleteParams) (*mcp.CompleteResult, *schemas.BifrostError)
}

// resourcesAndPromptsOptions declares prompts, resources and completions, narrowing their lists per request.
func (h *MCPServerHandler) resourcesAndPromptsOptions(hooks *server.Hooks) []server.ServerOption {
	hooks.AddAfterListResources(func(ctx context.Context, _ any, _ *mcp.ListResourcesRequest, result *mcp.ListResourcesResult) {
		if result != nil && requestNarrowed(ctx) {
			result.Resources = keepCallersEntries(result.Resources, h.served.GetAvailableMCPResources(ctx), resourceURI)
		}
	})
	hooks.AddAfterListResourceTemplates(func(ctx context.Context, _ any, _ *mcp.ListResourceTemplatesRequest, result *mcp.ListResourceTemplatesResult) {
		if result != nil && requestNarrowed(ctx) {
			result.ResourceTemplates = keepCallersEntries(result.ResourceTemplates, h.served.GetAvailableMCPResourceTemplates(ctx), coremcp.ResourceTemplateURI)
		}
	})
	return []server.ServerOption{
		server.WithPromptCapabilities(true),
		server.WithResourceCapabilities(false, true),
		server.WithCompletions(),
		server.WithPromptFilter(func(ctx context.Context, prompts []mcp.Prompt) []mcp.Prompt {
			if !requestNarrowed(ctx) {
				return prompts
			}
			return keepCallersEntries(prompts, h.served.GetAvailableMCPPrompts(ctx), promptName)
		}),
		server.WithPromptCompletionProvider(h),
		server.WithResourceCompletionProvider(h),
	}
}

// syncResourcesAndPrompts installs every upstream's prompts, resources and templates; core
// re-checks the caller on each call.
func (h *MCPServerHandler) syncResourcesAndPrompts(ctx context.Context) {
	prompts := h.served.GetAvailableMCPPrompts(ctx)
	serverPrompts := make([]server.ServerPrompt, 0, len(prompts))
	for _, prompt := range prompts {
		serverPrompts = append(serverPrompts, server.ServerPrompt{Prompt: prompt, Handler: h.getPrompt})
	}
	resources := h.served.GetAvailableMCPResources(ctx)
	serverResources := make([]server.ServerResource, 0, len(resources))
	for _, resource := range resources {
		serverResources = append(serverResources, server.ServerResource{Resource: resource, Handler: h.readResource})
	}
	templates := h.served.GetAvailableMCPResourceTemplates(ctx)
	serverTemplates := make([]server.ServerResourceTemplate, 0, len(templates))
	for _, template := range templates {
		serverTemplates = append(serverTemplates, server.ServerResourceTemplate{Template: template, Handler: server.ResourceTemplateHandlerFunc(h.readResource)})
	}
	h.mcpServer.SetPrompts(serverPrompts...)
	h.mcpServer.SetResources(serverResources...)
	h.mcpServer.SetResourceTemplates(serverTemplates...)
	logger.Debug("Synced MCP server with %d prompts, %d resources, %d resource templates", len(prompts), len(resources), len(templates))
}

func (h *MCPServerHandler) getPrompt(ctx context.Context, request mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	result, bErr := h.served.GetMCPPrompt(ctx, request.Params.Name, request.Params.Arguments)
	if bErr != nil {
		return nil, upstreamError(bErr, mcp.INVALID_PARAMS)
	}
	return result, nil
}

func (h *MCPServerHandler) readResource(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	result, bErr := h.served.ReadMCPResource(ctx, request.Params.URI)
	if bErr != nil {
		code := mcp.RESOURCE_NOT_FOUND
		if mcp.IsModernProtocol(server.RequestProtocolVersion(ctx)) {
			code = mcp.INVALID_PARAMS
		}
		return nil, upstreamError(bErr, code)
	}
	return result.Contents, nil
}

// CompletePromptArgument completes a prompt argument from the upstream that owns the prompt.
func (h *MCPServerHandler) CompletePromptArgument(ctx context.Context, promptName string, argument mcp.CompleteArgument, completeContext mcp.CompleteContext) (*mcp.Completion, error) {
	return h.complete(ctx, mcp.CompleteParams{Ref: mcp.PromptReference{Type: "ref/prompt", Name: promptName}, Argument: argument, Context: completeContext})
}

// CompleteResourceArgument completes a resource template argument from the upstream that owns it.
func (h *MCPServerHandler) CompleteResourceArgument(ctx context.Context, uri string, argument mcp.CompleteArgument, completeContext mcp.CompleteContext) (*mcp.Completion, error) {
	return h.complete(ctx, mcp.CompleteParams{Ref: mcp.ResourceReference{Type: "ref/resource", URI: uri}, Argument: argument, Context: completeContext})
}

func (h *MCPServerHandler) complete(ctx context.Context, params mcp.CompleteParams) (*mcp.Completion, error) {
	result, bErr := h.served.CompleteMCPArgument(ctx, params)
	if bErr != nil {
		return nil, upstreamError(bErr, mcp.INVALID_PARAMS)
	}
	return &result.Completion, nil
}

// upstreamError maps a core error for the client: notFoundCode when the caller cannot reach it or the
// upstream has no such resource, invalid params when the upstream says so, and the authorization prompt
// when the upstream needs the caller's credential.
func upstreamError(bErr *schemas.BifrostError, notFoundCode int) error {
	if bErr.Error != nil {
		switch err := bErr.Error.Error; {
		case errors.Is(err, coremcp.ErrNotFound), errors.Is(err, mcp.ErrResourceNotFound):
			return server.NewHandlerError(jsonRPCError{code: notFoundCode, message: bifrost.GetErrorMessage(bErr)})
		case errors.Is(err, mcp.ErrInvalidParams):
			return server.NewHandlerError(jsonRPCError{code: mcp.INVALID_PARAMS, message: bifrost.GetErrorMessage(bErr)})
		}
	}
	if authReq := bErr.ExtraFields.MCPAuthRequired; authReq != nil {
		return errors.New(mcpAuthRequiredToolResult(authReq))
	}
	return fmt.Errorf("upstream MCP server failed: %s", bifrost.GetErrorMessage(bErr))
}

// jsonRPCError is a JSON-RPC error a handler may answer with as is.
type jsonRPCError struct {
	code    int
	message string
}

func (e jsonRPCError) Error() string { return e.message }

func (e jsonRPCError) JSONRPCError() mcp.JSONRPCError {
	return mcp.JSONRPCError{JSONRPC: mcp.JSONRPC_VERSION, Error: mcp.JSONRPCErrorDetails{Code: e.code, Message: e.message}}
}

// requestNarrowed reports whether the request carries an include list.
func requestNarrowed(ctx context.Context) bool {
	return ctx.Value(schemas.MCPContextKeyIncludeClients) != nil || ctx.Value(schemas.MCPContextKeyIncludeTools) != nil
}

// keepAllowed keeps the items whose key is among allowed's, in their order.
func keepAllowed[T, A any](items []T, itemKey func(T) string, allowed []A, allowedKey func(A) string) []T {
	keys := make(map[string]bool, len(allowed))
	for _, item := range allowed {
		keys[allowedKey(item)] = true
	}
	kept := make([]T, 0, len(items))
	for _, item := range items {
		if keys[itemKey(item)] {
			kept = append(kept, item)
		}
	}
	return kept
}

// keepCallersEntries is keepAllowed for entries of one type, each kept entry replaced by the
// caller's own: two upstreams can list the same resource URI with different details.
func keepCallersEntries[T any](items, allowed []T, key func(T) string) []T {
	byKey := make(map[string]T, len(allowed))
	for _, item := range allowed {
		byKey[key(item)] = item
	}
	kept := make([]T, 0, len(items))
	for _, item := range items {
		if own, ok := byKey[key(item)]; ok {
			kept = append(kept, own)
		}
	}
	return kept
}

func promptName(p mcp.Prompt) string    { return p.Name }
func resourceURI(r mcp.Resource) string { return r.URI }
