package openapimcp

import (
	"context"
	"errors"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// BuildServer creates the in-process MCP server: one tool per synthesized
// operation, each handled by the shared Executor.
func BuildServer(opts ServerOptions) (*server.MCPServer, error) {
	if opts.Synthesis == nil || len(opts.Synthesis.Tools) == 0 {
		return nil, errors.New("no tools to serve")
	}
	exec, err := NewExecutor(opts)
	if err != nil {
		return nil, err
	}
	doc := opts.Synthesis.Document
	name := "bifrost-openapi"
	if opts.ClientName != "" {
		name += "-" + opts.ClientName
	}
	version := doc.Version
	if version == "" {
		version = "1.0.0"
	}
	serverOpts := []server.ServerOption{server.WithToolCapabilities(false)}
	if instructions := buildInstructions(doc); instructions != "" {
		serverOpts = append(serverOpts, server.WithInstructions(instructions))
	}
	s := server.NewMCPServer(name, version, serverOpts...)
	for i := range opts.Synthesis.Tools {
		tool := &opts.Synthesis.Tools[i]
		t := mcp.NewToolWithRawSchema(tool.Name, tool.Description, tool.InputSchema)
		t.Annotations = tool.Annotations
		s.AddTool(t, exec.handler(tool))
	}
	return s, nil
}

// handler adapts one tool to mcp-go. Request-building problems (missing
// required arguments) and upstream failures are returned as error results so the
// model can react; nothing here is a protocol error.
func (e *Executor) handler(tool *Tool) server.ToolHandlerFunc {
	return func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := req.GetArguments()
		if args == nil {
			args = map[string]any{}
		}
		result, err := e.Execute(ctx, tool, args)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return result, nil
	}
}

// buildInstructions serves the API description as MCP server instructions.
func buildInstructions(doc *Document) string {
	desc := strings.TrimSpace(doc.Description)
	if desc == "" {
		return ""
	}
	if doc.Title != "" {
		desc = doc.Title + ": " + desc
	}
	if len(desc) > maxInstructionsLen {
		desc = strings.TrimSpace(desc[:maxInstructionsLen]) + "…"
	}
	return desc
}
