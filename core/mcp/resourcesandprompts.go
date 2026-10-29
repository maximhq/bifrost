package mcp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
)

// listResourcesAndPrompts fills discovery's lists for what caps advertises; a failed list keeps the
// one in previous. It returns an error when nothing it asked answered, which is the liveness signal
// for a server without tools. A templates list counts only when it answers: many servers lack one.
func (m *MCPManager) listResourcesAndPrompts(ctx context.Context, each time.Duration, conn *client.Client, clientName string, caps mcp.ServerCapabilities, discovery, previous *schemas.MCPDiscovery) error {
	var failures []error
	attempted := 0
	templatesAnswered := false
	// each > 0 gives every list call its own budget, detached from ctx's deadline (which tools/list
	// may have spent) but keeping its values.
	bounded := func() (context.Context, context.CancelFunc) {
		if each <= 0 {
			return ctx, func() {}
		}
		return context.WithTimeout(context.WithoutCancel(ctx), each)
	}
	if previous == nil {
		previous = &schemas.MCPDiscovery{}
	}
	if caps.Resources != nil {
		discovery.Resources, discovery.ResourceTemplates = previous.Resources, previous.ResourceTemplates
		listCtx, cancel := bounded()
		resources, err := m.runListResourcesWithHooks(listCtx, conn, clientName)
		cancel()
		attempted++
		if err != nil {
			failures = append(failures, err)
			m.logger.Warn("%s [%s] Could not list resources, keeping the previous list: %v", MCPLogPrefix, clientName, err)
		} else {
			discovery.Resources = resources
		}
		listCtx, cancel = bounded()
		templates, err := m.runListResourceTemplatesWithHooks(listCtx, conn, clientName)
		cancel()
		if err != nil {
			m.logger.Debug("%s [%s] Could not list resource templates, keeping the previous list: %v", MCPLogPrefix, clientName, err)
		} else {
			discovery.ResourceTemplates = templates
			templatesAnswered = true
		}
	}
	if caps.Prompts != nil {
		discovery.Prompts = previous.Prompts
		listCtx, cancel := bounded()
		prompts, err := m.runListPromptsWithHooks(listCtx, conn, clientName)
		cancel()
		attempted++
		if err != nil {
			failures = append(failures, err)
			m.logger.Warn("%s [%s] Could not list prompts, keeping the previous list: %v", MCPLogPrefix, clientName, err)
		} else {
			discovery.Prompts = prompts
		}
	}
	if attempted > 0 && len(failures) == attempted && !templatesAnswered {
		return errors.Join(failures...)
	}
	return nil
}

// prefixPrompts names each prompt <client>-<prompt>, the scheme tools use.
func prefixPrompts(prompts []mcp.Prompt, clientName string) []mcp.Prompt {
	out := make([]mcp.Prompt, len(prompts))
	for i, prompt := range prompts {
		prompt.Name = clientName + "-" + prompt.Name
		out[i] = prompt
	}
	return out
}

// runGated runs op through the MCP plugin gate: a PreHook may short-circuit it and a PostHook may
// rewrite the result. ok is false when no hook or op left a result.
func runGated[T any](m *MCPManager, ctx context.Context, req *schemas.BifrostMCPRequest, op func(*schemas.BifrostContext, *schemas.BifrostMCPRequest) (T, error), wrap func(T) *schemas.BifrostMCPResponse, unwrap func(*schemas.BifrostMCPResponse) (T, bool)) (result T, ok bool, bErr *schemas.BifrostError) {
	gateCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	start := time.Now()
	resp, bErr := m.RunWithPluginPipeline(gateCtx, req, func(preReq *schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
		result, err := op(gateCtx, preReq)
		if err != nil {
			return nil, err
		}
		resp := wrap(result)
		// preReq names the server that answered: a read may move to another owner after the pre-hooks.
		resp.ExtraFields = schemas.BifrostMCPResponseExtraFields{ClientName: preReq.ClientName, Latency: time.Since(start).Milliseconds()}
		return resp, nil
	})
	if bErr != nil || resp == nil {
		return result, false, bErr
	}
	result, ok = unwrap(resp)
	return result, ok, nil
}

// runListWithHooks runs one resources or prompts list call through the plugin gate.
func runListWithHooks[T any](m *MCPManager, ctx context.Context, req *schemas.BifrostMCPRequest, list func(context.Context) (T, error), wrap func(T) *schemas.BifrostMCPResponse, unwrap func(*schemas.BifrostMCPResponse) (T, bool)) (T, error) {
	result, ok, bErr := runGated(m, ctx, req, func(gateCtx *schemas.BifrostContext, _ *schemas.BifrostMCPRequest) (T, error) { return list(gateCtx) }, wrap, unwrap)
	if bErr != nil {
		return result, fmt.Errorf("failed to %s: %s", req.RequestType.OTelMethodName(), bErr.GetErrorString())
	}
	// No response is a failure, not an empty list, so the caller keeps the previous one.
	if !ok {
		return result, fmt.Errorf("failed to %s: no list in the response", req.RequestType.OTelMethodName())
	}
	return result, nil
}

func (m *MCPManager) runListResourcesWithHooks(ctx context.Context, conn *client.Client, clientName string) ([]mcp.Resource, error) {
	return runListWithHooks(m, ctx,
		&schemas.BifrostMCPRequest{RequestType: schemas.MCPRequestTypeListResources, ClientName: clientName, BifrostMCPListResourcesRequest: &schemas.BifrostMCPListResourcesRequest{}},
		func(ctx context.Context) ([]mcp.Resource, error) {
			result, err := conn.ListResources(ctx, mcp.ListResourcesRequest{})
			if err != nil {
				return nil, err
			}
			return result.Resources, nil
		},
		func(r []mcp.Resource) *schemas.BifrostMCPResponse {
			return &schemas.BifrostMCPResponse{BifrostMCPListResourcesResponse: &schemas.BifrostMCPListResourcesResponse{Resources: r}}
		},
		func(resp *schemas.BifrostMCPResponse) ([]mcp.Resource, bool) {
			if resp.BifrostMCPListResourcesResponse == nil {
				return nil, false
			}
			return resp.BifrostMCPListResourcesResponse.Resources, true
		})
}

func (m *MCPManager) runListResourceTemplatesWithHooks(ctx context.Context, conn *client.Client, clientName string) ([]mcp.ResourceTemplate, error) {
	return runListWithHooks(m, ctx,
		&schemas.BifrostMCPRequest{RequestType: schemas.MCPRequestTypeListResourceTemplates, ClientName: clientName, BifrostMCPListResourceTemplatesRequest: &schemas.BifrostMCPListResourceTemplatesRequest{}},
		func(ctx context.Context) ([]mcp.ResourceTemplate, error) {
			result, err := conn.ListResourceTemplates(ctx, mcp.ListResourceTemplatesRequest{})
			if err != nil {
				return nil, err
			}
			return result.ResourceTemplates, nil
		},
		func(t []mcp.ResourceTemplate) *schemas.BifrostMCPResponse {
			return &schemas.BifrostMCPResponse{BifrostMCPListResourceTemplatesResponse: &schemas.BifrostMCPListResourceTemplatesResponse{ResourceTemplates: t}}
		},
		func(resp *schemas.BifrostMCPResponse) ([]mcp.ResourceTemplate, bool) {
			if resp.BifrostMCPListResourceTemplatesResponse == nil {
				return nil, false
			}
			return resp.BifrostMCPListResourceTemplatesResponse.ResourceTemplates, true
		})
}

func (m *MCPManager) runListPromptsWithHooks(ctx context.Context, conn *client.Client, clientName string) ([]mcp.Prompt, error) {
	return runListWithHooks(m, ctx,
		&schemas.BifrostMCPRequest{RequestType: schemas.MCPRequestTypeListPrompts, ClientName: clientName, BifrostMCPListPromptsRequest: &schemas.BifrostMCPListPromptsRequest{}},
		func(ctx context.Context) ([]mcp.Prompt, error) {
			result, err := conn.ListPrompts(ctx, mcp.ListPromptsRequest{})
			if err != nil {
				return nil, err
			}
			return prefixPrompts(result.Prompts, clientName), nil
		},
		func(p []mcp.Prompt) *schemas.BifrostMCPResponse {
			return &schemas.BifrostMCPResponse{BifrostMCPListPromptsResponse: &schemas.BifrostMCPListPromptsResponse{Prompts: p}}
		},
		func(resp *schemas.BifrostMCPResponse) ([]mcp.Prompt, bool) {
			if resp.BifrostMCPListPromptsResponse == nil {
				return nil, false
			}
			return resp.BifrostMCPListPromptsResponse.Prompts, true
		})
}

// rebindPromptsToClientName re-prefixes prompts after a client rename, the way
// rebindToolsToClientName does for tools. Resources keep their URIs.
func rebindPromptsToClientName(prompts []mcp.Prompt, clientName string) []mcp.Prompt {
	if prompts == nil {
		return nil
	}
	rebound := slices.Clone(prompts)
	for i := range rebound {
		rebound[i].Name = rebindName(rebound[i].Name, clientName)
	}
	return rebound
}

// reachableClients returns copies of the enabled clients ctx may reach, by name, each holding only the
// prompts, resources and templates its allow-lists expose. A tool filter reaches a client through its
// "<client>-*" entry or one of its tools, so a server without tools is reached by the wildcard.
func (m *MCPManager) reachableClients(ctx context.Context) []*schemas.MCPClientState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	clients := make([]*schemas.MCPClientState, 0, len(m.clientMap))
	for _, state := range m.clientMap {
		if !m.reachable(ctx, state) {
			continue
		}
		config := state.ExecutionConfig
		stateCopy := *state
		stateCopy.Prompts = exposed(state.Prompts, config.PromptsToExpose, func(p mcp.Prompt) string { return stripClientPrefix(p.Name, config.Name) })
		stateCopy.Resources = exposed(state.Resources, config.ResourcesToExpose, resourceURI)
		stateCopy.ResourceTemplates = exposed(state.ResourceTemplates, config.ResourcesToExpose, ResourceTemplateURI)
		clients = append(clients, &stateCopy)
	}
	sortByName(clients)
	return clients
}

// reachable reports whether ctx's include lists let a request reach state: the client is included,
// and, when the tools are narrowed, one of its tools or its wildcard is listed. Callers hold m.mu.
func (m *MCPManager) reachable(ctx context.Context, state *schemas.MCPClientState) bool {
	config := state.ExecutionConfig
	if config == nil || state.State == schemas.MCPConnectionStateDisabled {
		return false
	}
	includeClients, _ := ctx.Value(schemas.MCPContextKeyIncludeClients).([]string)
	if !shouldIncludeClient(config.Name, includeClients, m.logger) {
		return false
	}
	includeTools, narrowed := ctx.Value(schemas.MCPContextKeyIncludeTools).([]string)
	return !narrowed || slices.ContainsFunc(includeTools, func(entry string) bool {
		_, isTool := state.ToolMap[entry]
		return isTool || entry == config.Name+"-*"
	})
}

// exposed keeps the items allow lists; a nil list exposes everything.
func exposed[T any](items []T, allow schemas.WhiteList, key func(T) string) []T {
	if allow == nil || allow.IsUnrestricted() || len(items) == 0 {
		return items
	}
	kept := make([]T, 0, len(items))
	for _, item := range items {
		if allow.Contains(key(item)) {
			kept = append(kept, item)
		}
	}
	return kept
}

func resourceURI(r mcp.Resource) string { return r.URI }

func sortByName(clients []*schemas.MCPClientState) {
	slices.SortFunc(clients, func(a, b *schemas.MCPClientState) int {
		return strings.Compare(a.ExecutionConfig.Name, b.ExecutionConfig.Name)
	})
}

// GetAvailablePrompts lists the prompts of every client ctx may reach.
func (m *MCPManager) GetAvailablePrompts(ctx context.Context) []mcp.Prompt {
	var prompts []mcp.Prompt
	for _, state := range m.reachableClients(ctx) {
		prompts = append(prompts, state.Prompts...)
	}
	return prompts
}

// GetAvailableResources lists the resources of every client ctx may reach, each URI once.
func (m *MCPManager) GetAvailableResources(ctx context.Context) []mcp.Resource {
	var resources []mcp.Resource
	seen := map[string]bool{}
	for _, state := range m.reachableClients(ctx) {
		for _, resource := range state.Resources {
			if !seen[resource.URI] {
				seen[resource.URI] = true
				resources = append(resources, resource)
			}
		}
	}
	return resources
}

// GetAvailableResourceTemplates lists the templates of every client ctx may reach, each once.
func (m *MCPManager) GetAvailableResourceTemplates(ctx context.Context) []mcp.ResourceTemplate {
	var templates []mcp.ResourceTemplate
	seen := map[string]bool{}
	for _, state := range m.reachableClients(ctx) {
		for _, template := range state.ResourceTemplates {
			if raw := ResourceTemplateURI(template); raw != "" && !seen[raw] {
				seen[raw] = true
				templates = append(templates, template)
			}
		}
	}
	return templates
}

// GetPrompt runs prompts/get on the client that owns the prefixed prompt name.
func (m *MCPManager) GetPrompt(ctx *schemas.BifrostContext, name string, arguments map[string]string) (*mcp.GetPromptResult, *schemas.BifrostError) {
	state := promptOwner(m.reachableClients(ctx), name)
	if state == nil {
		return nil, mcpNotFound(fmt.Sprintf("prompt %q not found", name))
	}
	req := &schemas.BifrostMCPRequest{
		RequestType:                schemas.MCPRequestTypeGetPrompt,
		ClientName:                 state.ExecutionConfig.Name,
		BifrostMCPGetPromptRequest: &schemas.BifrostMCPGetPromptRequest{Name: name, Arguments: arguments},
	}
	return runOnClient(m, ctx, req, state,
		func(clients []*schemas.MCPClientState) *schemas.MCPClientState { return promptOwner(clients, name) },
		func(ctx context.Context, conn *client.Client, req *schemas.BifrostMCPRequest) (*mcp.GetPromptResult, error) {
			get := mcp.GetPromptRequest{}
			get.Params.Name = stripClientPrefix(req.BifrostMCPGetPromptRequest.Name, req.ClientName)
			get.Params.Arguments = req.BifrostMCPGetPromptRequest.Arguments
			return conn.GetPrompt(ctx, get)
		},
		func(r *mcp.GetPromptResult) *schemas.BifrostMCPResponse {
			return &schemas.BifrostMCPResponse{BifrostMCPGetPromptResponse: &schemas.BifrostMCPGetPromptResponse{Prompt: r}}
		},
		func(resp *schemas.BifrostMCPResponse) (*mcp.GetPromptResult, bool) {
			if resp.BifrostMCPGetPromptResponse == nil {
				return nil, false
			}
			return resp.BifrostMCPGetPromptResponse.Prompt, true
		})
}

// ReadResource runs resources/read on the first client listing the URI or a matching template.
func (m *MCPManager) ReadResource(ctx *schemas.BifrostContext, uri string) (*mcp.ReadResourceResult, *schemas.BifrostError) {
	state := resourceOwner(m.reachableClients(ctx), uri)
	if state == nil {
		return nil, mcpNotFound(fmt.Sprintf("resource %q not found", uri))
	}
	req := &schemas.BifrostMCPRequest{
		RequestType:                   schemas.MCPRequestTypeReadResource,
		ClientName:                    state.ExecutionConfig.Name,
		BifrostMCPReadResourceRequest: &schemas.BifrostMCPReadResourceRequest{URI: uri},
	}
	return runOnClient(m, ctx, req, state,
		func(clients []*schemas.MCPClientState) *schemas.MCPClientState { return resourceOwner(clients, uri) },
		func(ctx context.Context, conn *client.Client, req *schemas.BifrostMCPRequest) (*mcp.ReadResourceResult, error) {
			read := mcp.ReadResourceRequest{}
			read.Params.URI = req.BifrostMCPReadResourceRequest.URI
			return conn.ReadResource(ctx, read)
		},
		func(r *mcp.ReadResourceResult) *schemas.BifrostMCPResponse {
			return &schemas.BifrostMCPResponse{BifrostMCPReadResourceResponse: &schemas.BifrostMCPReadResourceResponse{Resource: r}}
		},
		func(resp *schemas.BifrostMCPResponse) (*mcp.ReadResourceResult, bool) {
			if resp.BifrostMCPReadResourceResponse == nil {
				return nil, false
			}
			return resp.BifrostMCPReadResourceResponse.Resource, true
		})
}

// Complete runs completion/complete on the client that owns the referenced prompt or resource. A
// reference no reachable client owns, or an upstream without completions, completes to nothing.
func (m *MCPManager) Complete(ctx *schemas.BifrostContext, params mcp.CompleteParams) (*mcp.CompleteResult, *schemas.BifrostError) {
	state := completionOwner(m.reachableClients(ctx), params.Ref)
	if state == nil {
		return emptyCompletion(), nil
	}
	req := &schemas.BifrostMCPRequest{
		RequestType:               schemas.MCPRequestTypeComplete,
		ClientName:                state.ExecutionConfig.Name,
		BifrostMCPCompleteRequest: &schemas.BifrostMCPCompleteRequest{Params: params},
	}
	return runOnClient(m, ctx, req, state,
		func(clients []*schemas.MCPClientState) *schemas.MCPClientState {
			return completionOwner(clients, params.Ref)
		},
		func(ctx context.Context, conn *client.Client, req *schemas.BifrostMCPRequest) (*mcp.CompleteResult, error) {
			// mcp-go answers an unknown method on 2026-07-28 with a 404 its client reads as a dead session.
			if conn.GetServerCapabilities().Completions == nil {
				return emptyCompletion(), nil
			}
			complete := mcp.CompleteRequest{Params: req.BifrostMCPCompleteRequest.Params}
			if ref, ok := complete.Params.Ref.(mcp.PromptReference); ok {
				ref.Name = stripClientPrefix(ref.Name, req.ClientName)
				complete.Params.Ref = ref
			}
			return conn.Complete(ctx, complete)
		},
		func(r *mcp.CompleteResult) *schemas.BifrostMCPResponse {
			return &schemas.BifrostMCPResponse{BifrostMCPCompleteResponse: &schemas.BifrostMCPCompleteResponse{Completion: r}}
		},
		func(resp *schemas.BifrostMCPResponse) (*mcp.CompleteResult, bool) {
			if resp.BifrostMCPCompleteResponse == nil {
				return nil, false
			}
			return resp.BifrostMCPCompleteResponse.Completion, true
		})
}

// runOnClient runs call through the plugin gate on state's connection, acquired after the PreHooks
// pass as for a tool call, within the tool execution timeout. An auth failure or dead session is
// recovered like a tool call's; these reads are safe to retry.
func runOnClient[T any](m *MCPManager, ctx *schemas.BifrostContext, req *schemas.BifrostMCPRequest, state *schemas.MCPClientState, owner func([]*schemas.MCPClientState) *schemas.MCPClientState, call func(context.Context, *client.Client, *schemas.BifrostMCPRequest) (T, error), wrap func(T) *schemas.BifrostMCPResponse, unwrap func(*schemas.BifrostMCPResponse) (T, bool)) (T, *schemas.BifrostError) {
	result, ok, bErr := runGated(m, ctx, req, func(gateCtx *schemas.BifrostContext, preReq *schemas.BifrostMCPRequest) (T, error) {
		var zero T
		// The owner was picked before the pre-hooks ran; one (governance) may have narrowed access since.
		// Pick again among what the request can still reach, so a URI two servers expose goes to the
		// one it is allowed.
		m.mu.RLock()
		stillReachable := m.reachable(gateCtx, state)
		m.mu.RUnlock()
		if !stillReachable {
			next := owner(m.reachableClients(gateCtx))
			if next == nil {
				return zero, fmt.Errorf("%w: no server this request can reach owns it", ErrNotFound)
			}
			state = next
			preReq.ClientName = next.ExecutionConfig.Name
		}
		if state.State == schemas.MCPConnectionStateNeedsReauth {
			return zero, fmt.Errorf("client %s needs re-authorization (its admin credential expired or was revoked)", state.ExecutionConfig.Name)
		}
		timeout := m.toolsManager.executionTimeout(state.ExecutionConfig)
		callCtx, cancel := context.WithTimeout(gateCtx, timeout)
		defer cancel()
		conn, release, err := m.AcquireClientConn(gateCtx, state)
		if err != nil {
			return zero, err
		}
		result, err := call(callCtx, conn, preReq)
		recoverable := err != nil && (isAuthFailure(err) || (isDeadSessionError(err) && hasProtocolSession(conn)))
		release()
		if !recoverable {
			return result, err
		}
		recoveryCtx := schemas.NewBifrostContext(callCtx, schemas.NoDeadline)
		if recovered, _ := m.toolsManager.recoverCall(recoveryCtx, req.RequestType.OTelMethodName(), state, state.ExecutionConfig, timeout, false, nil,
			func(retryCtx context.Context, conn *client.Client) error {
				var retryErr error
				result, retryErr = call(retryCtx, conn, preReq)
				return retryErr
			}); recovered {
			return result, nil
		}
		return zero, err
	}, wrap, unwrap)
	if bErr == nil && !ok {
		bErr = &schemas.BifrostError{Error: &schemas.ErrorField{Message: req.RequestType.OTelMethodName() + " returned no result"}}
	}
	return result, bErr
}

// completionOwner returns the client owning the prompt or resource ref names, or nil.
func completionOwner(clients []*schemas.MCPClientState, ref any) *schemas.MCPClientState {
	switch ref := ref.(type) {
	case mcp.PromptReference:
		return promptOwner(clients, ref.Name)
	case mcp.ResourceReference:
		return resourceOwner(clients, ref.URI)
	}
	return nil
}

// promptOwner returns the client listing the prefixed prompt name, or nil.
func promptOwner(clients []*schemas.MCPClientState, name string) *schemas.MCPClientState {
	for _, state := range clients {
		if slices.ContainsFunc(state.Prompts, func(p mcp.Prompt) bool { return p.Name == name }) {
			return state
		}
	}
	return nil
}

// resourceOwner returns the first client listing uri, else the first with a template matching it.
func resourceOwner(clients []*schemas.MCPClientState, uri string) *schemas.MCPClientState {
	for _, state := range clients {
		if slices.ContainsFunc(state.Resources, func(r mcp.Resource) bool { return r.URI == uri }) {
			return state
		}
	}
	for _, state := range clients {
		for _, template := range state.ResourceTemplates {
			if raw := ResourceTemplateURI(template); raw != "" && (raw == uri || template.URITemplate.Regexp().MatchString(uri)) {
				return state
			}
		}
	}
	return nil
}

// ResourceTemplateURI is the template's URI template, or "" when it has none.
func ResourceTemplateURI(template mcp.ResourceTemplate) string {
	if template.URITemplate == nil || template.URITemplate.Template == nil {
		return ""
	}
	return template.URITemplate.Raw()
}

// ErrNotFound marks a prompt or resource the caller cannot reach, whether or not it exists.
var ErrNotFound = errors.New("not found")

func emptyCompletion() *mcp.CompleteResult {
	return &mcp.CompleteResult{Completion: mcp.Completion{Values: []string{}}}
}

func mcpNotFound(message string) *schemas.BifrostError {
	return &schemas.BifrostError{Error: &schemas.ErrorField{Message: message, Error: ErrNotFound}}
}
