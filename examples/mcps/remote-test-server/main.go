// remote-test-server serves one tool set over BOTH of Bifrost's remote MCP
// transports - streamable HTTP and SSE - from a single process.
//
// It exists so core/internal/mcptests can cover the remote-transport paths
// without an external dependency. Those tests previously required MCP_HTTP_URL
// and MCP_SSE_URL to point at a hosted MCP server; when that host went away the
// suite did not skip, it ran and burned five connect retries per test. A local
// server makes the remote-transport coverage hermetic and secret-free.
//
// Deliberately plain: no auth, no OAuth, no rejected methods. The other HTTP
// servers in examples/mcps each exercise one specific defect (http-no-ping-server
// rejects ping, auth-demo-server demands X-API-Key, oauth-demo-server runs a
// full OAuth 2.1 flow), which makes all three unusable as a generic fixture -
// connection_test.go's header case sends a junk bearer token and asserts the
// connection SUCCEEDS.
//
// Ports default to 3011 (HTTP) and 3012 (SSE) and are overridable so a test
// harness facing a port conflict can bind elsewhere.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const (
	defaultHTTPPort = "3011"
	defaultSSEPort  = "3012"

	httpEndpointPath    = "/mcp"
	sseEndpointPath     = "/sse"
	messageEndpointPath = "/message"
)

func main() {
	httpPort := envOr("MCP_HTTP_PORT", defaultHTTPPort)
	ssePort := envOr("MCP_SSE_PORT", defaultSSEPort)

	// Two MCPServer instances rather than one shared between transports: the
	// SSE server keeps per-session state keyed off the server it was built
	// with, and the tests register the HTTP and SSE clients side by side
	// (codemode_files_test.go's multi-server case), so they must not contend.
	// Paths are pinned rather than left to the SDK's defaults so the URLs the
	// test harness builds cannot drift with an mcp-go upgrade. These happen to
	// match v0.43.2's defaults ("/mcp", "/sse" + "/message").
	httpSrv := server.NewStreamableHTTPServer(newMCPServer(),
		server.WithEndpointPath(httpEndpointPath))
	sseSrv := server.NewSSEServer(newMCPServer(),
		server.WithSSEEndpoint(sseEndpointPath),
		server.WithMessageEndpoint(messageEndpointPath))

	errCh := make(chan error, 2)

	go func() {
		addr := "localhost:" + httpPort
		log.Printf("streamable HTTP MCP server listening on http://%s%s", addr, httpEndpointPath)
		errCh <- fmt.Errorf("http transport: %w", httpSrv.Start(addr))
	}()

	go func() {
		addr := "localhost:" + ssePort
		log.Printf("SSE MCP server listening on http://%s%s", addr, sseEndpointPath)
		errCh <- fmt.Errorf("sse transport: %w", sseSrv.Start(addr))
	}()

	// Either listener dying makes the process useless to the tests, so fail
	// loudly instead of limping along serving one transport.
	log.Fatal(<-errCh)
}

// newMCPServer builds the tool set. The names and shapes mirror
// http-no-ping-server so fixtures stay interchangeable between the two; the
// tests assert on transport behaviour and on generated code-mode stubs, not on
// specific tool semantics, but they do need every tool to carry a typed schema
// (codemode_files_test.go asserts the generated servers/*.pyi files are
// non-empty).
func newMCPServer() *server.MCPServer {
	s := server.NewMCPServer("remote-test-server", "1.0.0")

	s.AddTool(mcp.NewTool(
		"echo",
		mcp.WithDescription("Echo back the input message"),
		mcp.WithString("message", mcp.Required(), mcp.Description("Message to echo")),
	), echoHandler)

	s.AddTool(mcp.NewTool(
		"add",
		mcp.WithDescription("Add two numbers"),
		mcp.WithNumber("a", mcp.Required(), mcp.Description("First number")),
		mcp.WithNumber("b", mcp.Required(), mcp.Description("Second number")),
	), addHandler)

	s.AddTool(mcp.NewTool(
		"greet",
		mcp.WithDescription("Greet someone by name"),
		mcp.WithString("name", mcp.Required(), mcp.Description("Name to greet")),
	), greetHandler)

	return s
}

// Handlers return JSON objects rather than prose. Code mode parses a tool's
// text content as JSON when it can and binds the result as a Starlark dict
// (see extractResultFromChatMessage / goToStarlark in
// core/mcp/codemode/starlark), so a plain-text result would surface as a
// string - codemode_tools_test.go asserts on `type(r) == "dict"`.
func echoHandler(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	message, err := req.RequireString("message")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return jsonResult(map[string]any{"echoed": message})
}

func addHandler(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	a, err := req.RequireFloat("a")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	b, err := req.RequireFloat("b")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return jsonResult(map[string]any{"sum": a + b})
}

func greetHandler(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	name, err := req.RequireString("name")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return jsonResult(map[string]any{"greeting": "Hello, " + strings.TrimSpace(name) + "!"})
}

// jsonResult marshals v and returns it as the tool's text content.
func jsonResult(v map[string]any) (*mcp.CallToolResult, error) {
	encoded, err := json.Marshal(v)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(string(encoded)), nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
