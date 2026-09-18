package mcptools

import (
	"context"
	"encoding/json"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// NewServer builds the in-process MCP server that hosts every tool in this
// package. It is handed to bifrost.Bifrost.AddMCPClient as an
// MCPConnectionTypeInProcess client, the same mechanism any other in-process
// MCP client already uses - so no core/mcp changes were needed to add this
// server, only a caller of the existing generic path.
//
// deps is shared across every concurrent caller this server serves; nothing
// caller-specific is captured here, since each handler resolves scope fresh
// from ctx (see filterArg).
func NewServer(deps *Deps) *server.MCPServer {
	mcpServer := server.NewMCPServer(
		"Bifrost-MCP-Server",
		"1.0.0",
		server.WithToolCapabilities(true),
	)
	for _, tool := range buildTools() {
		mcpServer.AddTool(
			mcp.NewToolWithRawSchema(tool.name, tool.description, json.RawMessage(tool.schemaJSON)),
			toolHandler(deps, tool),
		)
	}
	return mcpServer
}

// toolHandler adapts one Tool's execute closure into the mcp-go handler
// shape, applying the same result-size bound every tool result gets
// regardless of which flow produced it.
func toolHandler(deps *Deps, tool Tool) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		result, err := tool.execute(ctx, deps, request.GetArguments())
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(boundToolResult(result)), nil
	}
}
