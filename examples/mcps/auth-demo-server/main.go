package main

// auth-demo-server demonstrates two layers of authentication for HTTP MCP servers:
//
//  1. CONNECTION-LEVEL AUTH (X-API-Key header)
//     Enforced in HTTP middleware on every request (initialize, tools/list,
//     tools/call). A missing or wrong key is rejected before the MCP server
//     sees the message at all.
//
//  2. TOOL-EXECUTION AUTH (X-Tool-Token header)
//     A separate secret token checked exclusively inside sensitive tool handlers
//     at call time. Public tools ignore it; the connection middleware does not
//     inspect it at all. This lets you scope a second credential to tool
//     execution only — distinct from the connection credential.
//
// HOW BIFROST SENDS HEADERS
//
// Bifrost has a single `headers` field on MCPClientConfig. Those same headers are
// used in two places:
//   - At connection time: passed to transport.WithHTTPHeaders() so every HTTP
//     request to the server carries them.
//   - At tool-call time: copied onto CallToolRequest.Header so the server can
//     read them inside the tool handler via the request context.
//
// This means all configured headers are present on EVERY request — there is no
// separate "connection-only" vs "tool-only" header mechanism in Bifrost. To
// distinguish the two auth levels you simply use different header names, both
// configured in the same `headers` map. The server then enforces each header
// at the appropriate layer (middleware vs. handler).
//
// Bifrost config example:
//
//	{
//	  "name": "auth_demo",
//	  "connection_type": "http",
//	  "connection_string": "http://localhost:3002/",
//	  "auth_type": "headers",
//	  "headers": {
//	    "X-API-Key":    "super-secret-key",
//	    "X-Tool-Token": "tool-exec-secret"
//	  },
//	  "tools_to_execute": ["*"]
//	}

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const (
	// connectionAPIKey is checked in HTTP middleware on every request
	// (initialize, tools/list, tools/call).
	// In production, load this from an environment variable or secrets manager.
	connectionAPIKey = "super-secret-key"

	// toolExecToken is checked exclusively inside sensitive tool handlers —
	// never in the connection middleware. It acts as a second independent
	// credential that gates tool execution only.
	// In production, load this from an environment variable or secrets manager.
	toolExecToken = "tool-exec-secret"
)

// contextKey is a private type so we don't collide with other packages' context keys.
type contextKey string

const requestHeadersKey contextKey = "request_headers"

func main() {
	s := server.NewMCPServer("auth-demo-server", "1.0.0")

	// public_info only requires connection-level auth (X-API-Key).
	// Any authenticated client can call it without a tool execution token.
	publicTool := mcp.NewTool(
		"public_info",
		mcp.WithDescription("Returns non-sensitive public information. Requires connection auth (X-API-Key) only."),
		mcp.WithString("topic", mcp.Required(), mcp.Description("Topic to look up")),
	)
	s.AddTool(publicTool, publicInfoHandler)

	// secret_data requires BOTH connection-level auth (X-API-Key) AND a
	// dedicated tool-execution token (X-Tool-Token) checked inside the handler.
	// In Bifrost both headers live in the same `headers` map and arrive on
	// every request, so the handler reads X-Tool-Token from context and
	// validates it independently of the connection credential.
	secretTool := mcp.NewTool(
		"secret_data",
		mcp.WithDescription("Returns sensitive data. Requires connection auth (X-API-Key) AND tool-execution auth (X-Tool-Token)."),
		mcp.WithString("resource", mcp.Required(), mcp.Description("Resource name to fetch")),
	)
	s.AddTool(secretTool, secretDataHandler)

	httpServer := server.NewStreamableHTTPServer(s)

	// Middleware chain (outermost = first to run):
	//   1. connectionAuthMiddleware  — rejects requests with a wrong/missing X-API-Key
	//   2. injectHeadersMiddleware   — stores the request headers in context so
	//                                  tool handlers can read them for tool-level auth
	//   3. httpServer                — the MCP server itself
	handler := connectionAuthMiddleware(injectHeadersMiddleware(httpServer))

	addr := "localhost:3002"
	log.Printf("auth-demo-server listening on http://%s/", addr)
	log.Printf("\nAuth layers:")
	log.Printf("  Connection-level:   X-API-Key: %s  (middleware rejects all requests without it)", connectionAPIKey)
	log.Printf("  Tool-execution:     X-Tool-Token: %s  (only secret_data checks this, validated inside the handler)", toolExecToken)
	log.Printf("\nNote: Bifrost sends all `headers` on both connection setup AND every tool call.")
	log.Printf("Both X-API-Key and X-Tool-Token go in the same `headers` map.")
	log.Printf("The server enforces each at the right layer: middleware vs. handler.\n")
	log.Printf("Bifrost config:")
	log.Printf(`
{
  "name": "auth_demo",
  "connection_type": "http",
  "connection_string": "http://%s/",
  "auth_type": "headers",
  "headers": {
    "X-API-Key":    "%s",
    "X-Tool-Token": "%s"
  },
  "tools_to_execute": ["*"]
}
`, addr, connectionAPIKey, toolExecToken)

	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

// ─── Middleware ───────────────────────────────────────────────────────────────

// connectionAuthMiddleware enforces connection-level authentication.
// Every HTTP request — including initialize, tools/list, and tools/call —
// must carry the correct X-API-Key header. A missing or wrong key results
// in HTTP 401 before the MCP server processes anything.
func connectionAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-API-Key")
		if key == "" {
			http.Error(w, "connection auth required: missing X-API-Key header", http.StatusUnauthorized)
			return
		}
		if key != connectionAPIKey {
			http.Error(w, "connection auth failed: invalid X-API-Key", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// injectHeadersMiddleware stores the raw HTTP request headers in the context
// so that tool handlers can read them for tool-level auth checks.
// This is needed because MCP tool handlers only receive (ctx, CallToolRequest)
// — they don't have direct access to the http.Request.
func injectHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), requestHeadersKey, r.Header)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// ─── Tool handlers ────────────────────────────────────────────────────────────

// publicInfoHandler handles "public_info". Connection auth has already been
// verified by middleware, so no further auth check is needed here.
func publicInfoHandler(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Topic string `json:"topic"`
	}
	if err := parseArgs(req, &args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf(
		"Public info about %q: this data is available to all authenticated clients.", args.Topic,
	)), nil
}

// secretDataHandler handles "secret_data". Connection-level auth (X-API-Key)
// has already been verified by middleware. Here we additionally check
// X-Tool-Token — a separate secret dedicated to authorizing tool execution.
// Bifrost sends it as part of the same `headers` map, so it arrives on every
// request including this tool call; the middleware intentionally ignores it.
func secretDataHandler(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	// ── Tool-execution token check ───────────────────────────────────────────
	headers, ok := ctx.Value(requestHeadersKey).(http.Header)
	if !ok {
		return mcp.NewToolResultError("tool auth error: request headers unavailable in context"), nil
	}
	token := headers.Get("X-Tool-Token")
	if token == "" {
		return mcp.NewToolResultError("tool auth required: missing X-Tool-Token header"), nil
	}
	if token != toolExecToken {
		return mcp.NewToolResultError("tool auth failed: invalid X-Tool-Token"), nil
	}
	// ── Auth passed, proceed ─────────────────────────────────────────────────

	var args struct {
		Resource string `json:"resource"`
	}
	if err := parseArgs(req, &args); err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}

	return mcp.NewToolResultText(fmt.Sprintf(
		"Secret data for resource %q: [classified content — X-API-Key + X-Tool-Token verified]", args.Resource,
	)), nil
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

func parseArgs(req mcp.CallToolRequest, dst any) error {
	b, err := json.Marshal(req.Params.Arguments)
	if err != nil {
		return fmt.Errorf("failed to marshal arguments: %w", err)
	}
	if err := json.Unmarshal(b, dst); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}
