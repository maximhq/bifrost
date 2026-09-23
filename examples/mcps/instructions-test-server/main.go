// instructions-test-server serves a small tool set over streamable HTTP and
// returns a configurable `instructions` string from the MCP initialize
// handshake.
//
// It exists because the instructions-forwarding path in Bifrost's MCP gateway
// cannot be exercised by a single fixture. The gateway's job there is to
// AGGREGATE the instructions of every upstream a caller may see - labeling each
// block with its source server, ordering them deterministically, applying the
// size caps, and withholding the ones the caller was not granted. All of that
// needs at least two upstreams advertising DIFFERENT text at the same time,
// which is what the configurable name/instructions pair below is for:
//
//	MCP_SERVER_NAME=alpha MCP_INSTRUCTIONS="Alpha rule." MCP_HTTP_PORT=3021 ./instructions-test-server
//	MCP_SERVER_NAME=beta  MCP_INSTRUCTIONS="Beta rule."  MCP_HTTP_PORT=3022 ./instructions-test-server
//
// Leaving MCP_INSTRUCTIONS empty is itself a case worth running: an upstream
// that advertises nothing must contribute no heading at all to the aggregate,
// rather than an empty section.
//
// Deliberately plain otherwise - no auth, no OAuth, no rejected methods - so
// nothing but the instructions field distinguishes it from a boring server.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const (
	defaultHTTPPort = "3021"
	defaultName     = "instructions-test-server"

	httpEndpointPath = "/mcp"

	// The default text is long enough to be recognizable in an aggregate but
	// short enough not to trip the gateway's per-client cap.
	defaultInstructions = "Always call describe_policy before calling echo, and quote its result verbatim."
)

func main() {
	httpPort := envOr("MCP_HTTP_PORT", defaultHTTPPort)
	name := envOr("MCP_SERVER_NAME", defaultName)
	// Not envOr: an explicitly empty MCP_INSTRUCTIONS is the "advertises none"
	// case and must not fall back to the default.
	instructions, hasInstructions := os.LookupEnv("MCP_INSTRUCTIONS")
	if !hasInstructions {
		instructions = defaultInstructions
	}

	srv := server.NewStreamableHTTPServer(
		newMCPServer(name, instructions),
		server.WithEndpointPath(httpEndpointPath),
	)

	addr := "localhost:" + httpPort
	log.Printf("MCP server %q listening on http://%s%s", name, addr, httpEndpointPath)
	if instructions == "" {
		log.Printf("advertising no instructions")
	} else {
		log.Printf("advertising instructions: %q", instructions)
	}
	log.Fatal(srv.Start(addr))
}

// newMCPServer builds the tool set. describe_policy exists so a caller can see
// through the tool layer whether the instructions actually reached it: the
// text it returns is the same text the handshake advertises.
func newMCPServer(name, instructions string) *server.MCPServer {
	s := server.NewMCPServer(name, "1.0.0",
		server.WithToolCapabilities(true),
		server.WithInstructions(instructions),
	)

	s.AddTool(mcp.NewTool(
		"echo",
		mcp.WithDescription("Echo back the input message"),
		mcp.WithString("message", mcp.Required(), mcp.Description("Message to echo")),
	), echoHandler)

	s.AddTool(mcp.NewTool(
		"describe_policy",
		mcp.WithDescription("Return this server's own usage policy"),
	), policyHandler(name, instructions))

	return s
}

func echoHandler(_ context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	message, err := request.RequireString("message")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return jsonResult(map[string]any{"echo": message})
}

func policyHandler(name, instructions string) server.ToolHandlerFunc {
	return func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return jsonResult(map[string]any{"server": name, "policy": instructions})
	}
}

// jsonResult returns a JSON object rather than prose, matching the other
// fixtures here: code mode parses a tool's text content as JSON when it can.
func jsonResult(payload map[string]any) (*mcp.CallToolResult, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("failed to encode result: %v", err)), nil
	}
	return mcp.NewToolResultText(string(encoded)), nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
