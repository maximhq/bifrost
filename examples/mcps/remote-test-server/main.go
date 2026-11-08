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
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const (
	defaultHTTPPort = "3011"
	defaultSSEPort  = "3012"

	// slowEchoDelay is how long slow_echo holds before answering.
	slowEchoDelay = 1500 * time.Millisecond

	httpEndpointPath    = "/mcp"
	sseEndpointPath     = "/sse"
	messageEndpointPath = "/message"

	// Advertised on the initialize handshake so the instruction-forwarding path has a
	// live upstream in environments that configure this fixture (the provider harness
	// points its sse_mcp client here). Short enough not to trip the gateway's cap.
	defaultInstructions = "Prefer echo for plain text; call add only with numeric operands."
)

func main() {
	httpPort := envOr("MCP_HTTP_PORT", defaultHTTPPort)
	ssePort := envOr("MCP_SSE_PORT", defaultSSEPort)
	// Not envOr: an explicitly empty MCP_INSTRUCTIONS is the "advertises none" case
	// and must not fall back to the default.
	instructions, hasInstructions := os.LookupEnv("MCP_INSTRUCTIONS")
	if !hasInstructions {
		instructions = defaultInstructions
	}

	// Two MCPServer instances rather than one shared between transports: the
	// SSE server keeps per-session state keyed off the server it was built
	// with, and the tests register the HTTP and SSE clients side by side
	// (codemode_files_test.go's multi-server case), so they must not contend.
	// Paths are pinned rather than left to the SDK's defaults so the URLs the
	// test harness builds cannot drift with an mcp-go upgrade. These happen to
	// match v0.43.2's defaults ("/mcp", "/sse" + "/message").
	httpSrv := server.NewStreamableHTTPServer(newMCPServer(instructions),
		server.WithEndpointPath(httpEndpointPath))
	sseSrv := server.NewSSEServer(newMCPServer(instructions),
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
// chartTool declares a title and an output schema and answers with structured content plus an image,
// so results that are not plain text can be checked end to end.
func chartTool() mcp.Tool {
	return mcp.NewTool(
		"chart",
		mcp.WithDescription("Plot the points and return them with a chart image"),
		mcp.WithTitleAnnotation("Chart"),
		mcp.WithReadOnlyHintAnnotation(true),
		mcp.WithDestructiveHintAnnotation(false),
		mcp.WithOutputSchema[struct {
			Points int `json:"points"`
		}](),
	)
}

func chartHandler(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	result := mcp.NewToolResultStructured(map[string]any{"points": 3}, "3 points")
	// A 1x1 transparent PNG.
	result.Content = append(result.Content, mcp.NewImageContent("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=", "image/png"))
	return result, nil
}

// archiveHandler answers with a PDF, a zip and an SVG, so a gateway that sends only the media
// every provider reads can be checked: the PDF is kept, the zip and the SVG are named instead.
func archiveHandler(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{Content: []mcp.Content{
		mcp.NewTextContent("archive below"),
		mcp.NewEmbeddedResource(mcp.BlobResourceContents{URI: "file:///fixture/report.pdf", MIMEType: "application/pdf", Blob: "JVBERi0xLjQK"}),
		mcp.NewEmbeddedResource(mcp.BlobResourceContents{URI: "file:///fixture/notes.zip", MIMEType: "application/zip", Blob: "UEsDBAoAAAAAAA=="}),
		mcp.NewEmbeddedResource(mcp.BlobResourceContents{URI: "file:///fixture/hello.txt", MIMEType: "text/plain", Blob: "SGVsbG8="}),
		mcp.NewImageContent("PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciLz4=", "image/svg+xml"),
	}}, nil
}

func newMCPServer(instructions string) *server.MCPServer {
	opts := []server.ServerOption{}
	if instructions != "" {
		opts = append(opts, server.WithInstructions(instructions))
	}
	opts = append(opts, server.WithResourceCapabilities(false, true), server.WithPromptCapabilities(true))
	s := server.NewMCPServer("remote-test-server", "1.0.0", opts...)

	// Back the provider-harness resources and prompts discovery cases: one of each, plus a
	// resource template, so Bifrost has something to list besides tools.
	s.AddResource(mcp.NewResource("file:///fixture/handbook.md", "handbook",
		mcp.WithResourceDescription("Fixture handbook"), mcp.WithMIMEType("text/markdown"),
	), handbookHandler)
	// A comma in a URI is legal; the MCP log filters must still find a read of it.
	s.AddResource(mcp.NewResource("file:///fixture/report,2026.md", "report", mcp.WithMIMEType("text/markdown")),
		func(context.Context, mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
			return []mcp.ResourceContents{mcp.TextResourceContents{URI: "file:///fixture/report,2026.md", MIMEType: "text/markdown", Text: "# Report"}}, nil
		})
	s.AddResourceTemplate(mcp.NewResourceTemplate("fixture://notes/{id}", "note"), noteHandler)
	s.AddPrompt(mcp.NewPrompt("review",
		mcp.WithPromptDescription("Review a change"),
		mcp.WithArgument("diff", mcp.ArgumentDescription("The change to review"), mcp.RequiredArgument()),
	), reviewPromptHandler)

	s.AddTool(chartTool(), chartHandler)
	s.AddTool(mcp.NewTool("archive", mcp.WithDescription("Return a report, an archive, a text file and a diagram")), archiveHandler)

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

	// Backs the provider-harness injected web search cases. Its one result carries a
	// fact no model knows, so an answer containing it proves Bifrost ran the tool.
	s.AddTool(mcp.NewTool(
		"web_search",
		mcp.WithDescription("Search the web and return the most relevant results for a query"),
		mcp.WithString("query", mcp.Required(), mcp.Description("Search query")),
	), webSearchHandler)

	// Back the provider-harness code mode result cases. Each returns one shape a nested call
	// has to hand a script as a value: several JSON blocks, structured content beside a
	// different text copy, and a JSON object wrapped in a JSON string.
	s.AddTool(mcp.NewTool(
		"multi_block",
		mcp.WithDescription("Return two results as two JSON text blocks"),
	), multiBlockHandler)

	s.AddTool(mcp.NewTool(
		"structured_result",
		mcp.WithDescription("Return a structured result next to a text copy that differs from it"),
	), structuredResultHandler)

	s.AddTool(mcp.NewTool(
		"json_string",
		mcp.WithDescription("Return a JSON object wrapped in a JSON string"),
	), jsonStringHandler)

	// Backs the provider-harness code mode failed-call case for a failure flagged with isError
	// whose text opens with "Error: ", the way Bifrost's own in-process tools report one.
	s.AddTool(mcp.NewTool(
		"prefixed_error",
		mcp.WithDescription("Fail with isError and text that opens with \"Error: \""),
	), prefixedErrorHandler)

	// Backs the provider-harness agent-loop latency cases: a fixed wait between agent turns
	// that must be counted as upstream time, not Bifrost overhead.
	s.AddTool(mcp.NewTool(
		"slow_echo",
		mcp.WithDescription("Echo back the input message after a fixed delay"),
		mcp.WithString("message", mcp.Required(), mcp.Description("Message to echo")),
	), slowEchoHandler)

	return s
}

func handbookHandler(context.Context, mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	return []mcp.ResourceContents{mcp.TextResourceContents{URI: "file:///fixture/handbook.md", MIMEType: "text/markdown", Text: "# Fixture handbook"}}, nil
}

func noteHandler(_ context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	return []mcp.ResourceContents{mcp.TextResourceContents{URI: req.Params.URI, Text: "note"}}, nil
}

func reviewPromptHandler(_ context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	return mcp.NewGetPromptResult("Review a change", []mcp.PromptMessage{
		mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent("Review this change: "+req.Params.Arguments["diff"])),
	}), nil
}

func slowEchoHandler(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	select {
	case <-time.After(slowEchoDelay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return echoHandler(ctx, req)
}

func prefixedErrorHandler(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultError("Error: contact not found"), nil
}

func multiBlockHandler(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{Content: []mcp.Content{
		mcp.NewTextContent(`{"id":1,"title":"first"}`),
		mcp.NewTextContent(`{"id":2,"title":"second"}`),
	}}, nil
}

// The text copy differs from the structured result on purpose, so a case can tell which one
// reached the script.
func structuredResultHandler(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return &mcp.CallToolResult{
		Content:           []mcp.Content{mcp.NewTextContent(`{"source":"text"}`)},
		StructuredContent: map[string]any{"source": "structured"},
	}, nil
}

func jsonStringHandler(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultText(`"{\"title\":\"Example Domain\"}"`), nil
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

// HarnessSearchFact is the fact web_search returns, asserted by the provider harness.
const HarnessSearchFact = "The Bifrost harness lighthouse is painted teal-and-amber and its keeper is named Orrin Vale."

func webSearchHandler(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	query, err := req.RequireString("query")
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return jsonResult(map[string]any{
		"query": query,
		"results": []map[string]any{{
			"title":   "Bifrost harness lighthouse",
			"url":     "https://example.com/bifrost-harness-lighthouse",
			"snippet": HarnessSearchFact,
		}},
	})
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
