package mcp

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/maximhq/bifrost/core/schemas"
)

// listResourcesAndPrompts fills discovery's lists for what caps advertises; a failed list keeps the
// one in previous.
func (m *MCPManager) listResourcesAndPrompts(ctx context.Context, each time.Duration, conn *client.Client, clientName string, caps mcp.ServerCapabilities, discovery, previous *schemas.MCPDiscovery) {
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
		if err != nil {
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
		}
	}
	if caps.Prompts != nil {
		discovery.Prompts = previous.Prompts
		listCtx, cancel := bounded()
		prompts, err := m.runListPromptsWithHooks(listCtx, conn, clientName)
		cancel()
		if err != nil {
			m.logger.Warn("%s [%s] Could not list prompts, keeping the previous list: %v", MCPLogPrefix, clientName, err)
		} else {
			discovery.Prompts = prompts
		}
	}
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

// runListWithHooks runs one resources or prompts list call through the MCP plugin gate. A PreHook may
// short-circuit it with a synthetic response or an error; a PostHook may rewrite the result.
func runListWithHooks[T any](m *MCPManager, ctx context.Context, req *schemas.BifrostMCPRequest, list func(context.Context) (T, error), wrap func(T) *schemas.BifrostMCPResponse, unwrap func(*schemas.BifrostMCPResponse) (T, bool)) (T, error) {
	var zero T
	gateCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	start := time.Now()
	resp, bErr := m.RunWithPluginPipeline(gateCtx, req, func(*schemas.BifrostMCPRequest) (*schemas.BifrostMCPResponse, error) {
		result, err := list(gateCtx)
		if err != nil {
			return nil, err
		}
		resp := wrap(result)
		resp.ExtraFields = schemas.BifrostMCPResponseExtraFields{Latency: time.Since(start).Milliseconds()}
		return resp, nil
	})
	if bErr != nil {
		return zero, fmt.Errorf("failed to %s: %s", req.RequestType.OTelMethodName(), bErr.GetErrorString())
	}
	// No response is a failure, not an empty list, so the caller keeps the previous one.
	if resp == nil {
		return zero, fmt.Errorf("failed to %s: no response", req.RequestType.OTelMethodName())
	}
	result, ok := unwrap(resp)
	if !ok {
		return zero, fmt.Errorf("failed to %s: no list in the response", req.RequestType.OTelMethodName())
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
