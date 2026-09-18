package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// NewServer builds the in-process MCP server that hosts every tool in this
// package. It is handed to bifrost.Bifrost.AddMCPClient as an
// MCPConnectionTypeInProcess client, the same mechanism any other in-process
// MCP client already uses - so no core/mcp changes were needed to add this
// server, only a caller of the existing generic path.
//
// deps is called once per tool call, not once here: the log store and the
// semantic searcher behind it are rebound when the logging plugin is enabled
// or removed at runtime, and a Deps captured at boot would keep answering
// through whichever reader existed then. Nothing caller-specific comes from
// it either, since each handler resolves scope fresh from ctx (see filterArg).
func NewServer(deps func() *Deps) *server.MCPServer {
	mcpServer := server.NewMCPServer(
		"Bifrost-MCP-Server",
		"1.0.0",
		server.WithToolCapabilities(true),
	)
	for _, tool := range buildTools() {
		declaration := mcp.NewToolWithRawSchema(tool.name, tool.description, json.RawMessage(tool.schemaJSON))
		// Every tool here only reads logs, metrics and governance config.
		declaration.Annotations.ReadOnlyHint = mcp.ToBoolPtr(true)
		mcpServer.AddTool(declaration, toolHandler(deps, tool))
	}
	return mcpServer
}

// StaticDeps adapts a fixed Deps to NewServer, for callers whose dependencies
// never change after construction.
func StaticDeps(deps *Deps) func() *Deps {
	return func() *Deps { return deps }
}

// logFreeTools are the tools that never touch Deps.LogManager: semantic search
// goes through its own searcher and describe_virtual_key reads governance. Both
// report their own dependency missing. Every other tool needs the log store,
// so a new tool is refused without one unless it is listed here.
var logFreeTools = []string{SemanticSearchToolName, "describe_virtual_key"}

// toolHandler adapts one Tool's execute closure into the mcp-go handler
// shape, applying the same result-size bound every tool result gets
// regardless of which flow produced it.
func toolHandler(deps func() *Deps, tool Tool) server.ToolHandlerFunc {
	// Read off the schema once: it is the one place a tool's arguments are
	// declared, so a refusal can never disagree with what was advertised.
	accepted := tool.argumentNames()
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := refuseUnknownArguments(tool.name, accepted, request.GetArguments()); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		current := deps()
		if current == nil {
			current = &Deps{}
		}
		// Logging can be disabled, or its plugin removed at runtime, and the
		// server stays up either way; the call is refused rather than
		// dereferencing a reader that is not there.
		if current.LogManager == nil && !slices.Contains(logFreeTools, tool.name) {
			return mcp.NewToolResultError(tool.name + " is unavailable: logging is not enabled on this deployment"), nil
		}
		result, err := tool.execute(ctx, current, request.GetArguments())
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(boundToolResult(result)), nil
	}
}

// refuseUnknownArguments names any argument a tool does not take, rather than
// dropping it. Dropped, the call runs as if it had been honoured, and the caller
// reads the result as shaped by an argument that never existed - then guesses
// another. A small model did exactly that with query_metrics' arguments on
// query_model_performance, a step at a time.
func refuseUnknownArguments(tool string, accepted []string, args map[string]any) error {
	if len(accepted) == 0 {
		return nil
	}
	unknown := []string{}
	for name := range args {
		if !slices.Contains(accepted, name) {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	slices.Sort(unknown)
	return fmt.Errorf("%s does not take %s. Its arguments are: %s. Filters such as time range, provider, model and status go inside filters",
		tool, strings.Join(unknown, ", "), strings.Join(accepted, ", "))
}
