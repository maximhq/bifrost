// input-required-server is an MCP server whose tools ask the client for input mid-call, the 2026-07-28
// multi round-trip pattern (input_required). It exists so Bifrost's tests can cover passing those
// requests through the gateway against a real server process; the other servers in examples/mcps use
// an mcp-go release that predates input_required.
//
// Tools:
//   - echo: returns its text, needs no input.
//   - ask_environment: asks the user which environment to deploy to (form elicitation).
//   - connect_account: sends the user to a URL first (URL elicitation).
//   - summarize: asks the client's model for a completion (sampling).
//   - busy: answers "retry later" once, then completes on the retry.
//
// Each request's state names the tool it belongs to, so a retry carrying another tool's state is
// refused. Streamable HTTP only, on MCP_HTTP_PORT (default 3013).
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const (
	defaultHTTPPort  = "3013"
	httpEndpointPath = "/mcp"
	connectURL       = "https://example.com/connect"
)

func main() {
	addr := "localhost:" + envOr("MCP_HTTP_PORT", defaultHTTPPort)
	httpSrv := server.NewStreamableHTTPServer(newMCPServer(), server.WithEndpointPath(httpEndpointPath))
	log.Printf("input-required MCP server listening on http://%s%s", addr, httpEndpointPath)
	if err := httpSrv.Start(addr); err != nil {
		log.Fatal(err)
	}
}

func newMCPServer() *server.MCPServer {
	s := server.NewMCPServer("input-required-server", "1.0.0", server.WithToolCapabilities(false))

	s.AddTool(mcp.NewTool("echo",
		mcp.WithDescription("Returns the text it is given"),
		mcp.WithString("text", mcp.Required(), mcp.Description("Text to echo")),
	), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText(req.GetString("text", "")), nil
	})

	s.AddTool(mcp.NewTool("ask_environment",
		mcp.WithDescription("Asks the user which environment to deploy to, then deploys"),
	), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := checkState(req, "ask_environment"); err != nil {
			return nil, err
		}
		answer := server.ElicitationResponse(req.Params.InputResponses, "environment")
		if answer == nil {
			return server.NewInputRequestBuilder("ask_environment").Elicit("environment", mcp.ElicitationParams{
				Message: "Which environment should I deploy to?",
				RequestedSchema: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"environment": map[string]any{"type": "string", "enum": []string{"staging", "production"}},
					},
					"required": []string{"environment"},
				},
			}).ToolResult(), nil
		}
		if answer.Action != mcp.ElicitationResponseActionAccept {
			return mcp.NewToolResultText("deployment cancelled"), nil
		}
		content, _ := answer.Content.(map[string]any)
		return mcp.NewToolResultText(fmt.Sprintf("deployed to %v", content["environment"])), nil
	})

	s.AddTool(mcp.NewTool("connect_account",
		mcp.WithDescription("Sends the user to a page to connect their account, then lists it"),
	), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := checkState(req, "connect_account"); err != nil {
			return nil, err
		}
		if answer := server.ElicitationResponse(req.Params.InputResponses, "connect"); answer != nil {
			if answer.Action != mcp.ElicitationResponseActionAccept {
				return mcp.NewToolResultText("connection cancelled"), nil
			}
			return mcp.NewToolResultText("account connected"), nil
		}
		return server.NewInputRequestBuilder("connect_account").Elicit("connect", mcp.ElicitationParams{
			Mode:          mcp.ElicitationModeURL,
			Message:       "Connect your account to continue",
			URL:           connectURL,
			ElicitationID: "connect-1",
		}).ToolResult(), nil
	})

	s.AddTool(mcp.NewTool("summarize",
		mcp.WithDescription("Asks the client's model to summarise a text"),
		mcp.WithString("text", mcp.Description("Text to summarise")),
	), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := checkState(req, "summarize"); err != nil {
			return nil, err
		}
		if answer := server.SamplingResponse(req.Params.InputResponses, "summary"); answer != nil {
			if text, ok := answer.Content.(mcp.TextContent); ok {
				return mcp.NewToolResultText(text.Text), nil
			}
			return mcp.NewToolResultText("summary received"), nil
		}
		return server.NewInputRequestBuilder("summarize").Sample("summary", mcp.CreateMessageParams{
			Messages:  []mcp.SamplingMessage{{Role: mcp.RoleUser, Content: mcp.NewTextContent("Summarise: " + req.GetString("text", ""))}},
			MaxTokens: 100,
		}).ToolResult(), nil
	})

	s.AddTool(mcp.NewTool("busy",
		mcp.WithDescription("Answers retry later once, then completes"),
	), func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := checkState(req, "busy"); err != nil {
			return nil, err
		}
		if req.Params.RequestState == "busy" {
			return mcp.NewToolResultText("done after retry"), nil
		}
		return server.NewInputRequestBuilder("busy").ToolResult(), nil
	})

	return s
}

// checkState refuses a retry whose requestState belongs to another tool.
func checkState(req mcp.CallToolRequest, tool string) error {
	if state := req.Params.RequestState; state != "" && state != tool {
		return fmt.Errorf("requestState %q does not belong to %s", state, tool)
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
