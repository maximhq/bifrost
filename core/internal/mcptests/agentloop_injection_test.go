package mcptests

import (
	"github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
)

// injectToolsAsGatewayDoes runs the step the gateway runs before it sends a request to the model:
// the manager adds its tools to the request and records on ctx what it added.
//
// Agent mode only auto-executes a code mode meta-tool (listToolFiles, readToolFile, getToolDocs,
// executeToolCode) that Bifrost itself added to the request, so a test that hands the agent loop a
// response calling one has to start from a context that went through the same step. The request
// here is a throwaway that declares no tools of its own, so nothing the caller declared stands in
// the way of Bifrost's own copy.
func injectToolsAsGatewayDoes(ctx *schemas.BifrostContext, manager *mcp.MCPManager) {
	manager.AddToolsToRequest(ctx, &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: "gpt-4"},
	})
}
