// apps-server serves MCP Apps (SEP-1865) tools and ui:// resources, so Bifrost's tests can cover
// serving them through the gateway against a real server process.
//
// Two servers share one port:
//   - /mcp, server "dash":
//   - view: a tool with a UI (_meta.ui.resourceUri ui://dash/view).
//   - refresh: app-only (_meta.ui.visibility ["app"]), for the UI to call.
//   - item: a UI served by the ui://dash/items/{id} template, also named by the OpenAI Apps SDK's
//     _meta["openai/outputTemplate"].
//   - status: a plain tool board also has, so its server-given name is ambiguous across servers.
//   - caps: reports whether the request declared the MCP Apps extension.
//   - /mcp2, server "board": status only.
//
// Every tool answers "<server>:<tool>", so a test can tell which server ran. Streamable HTTP only,
// on MCP_HTTP_PORT (default 3014).
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const (
	defaultHTTPPort = "3014"
	appsExtension   = "io.modelcontextprotocol/ui"
	appsMimeType    = "text/html;profile=mcp-app"
)

func main() {
	addr := "localhost:" + envOr("MCP_HTTP_PORT", defaultHTTPPort)
	mux := http.NewServeMux()
	mux.Handle("/mcp", server.NewStreamableHTTPServer(newDashServer(), server.WithEndpointPath("/mcp")))
	mux.Handle("/mcp2", server.NewStreamableHTTPServer(newBoardServer(), server.WithEndpointPath("/mcp2")))
	log.Printf("apps MCP servers listening on http://%s/mcp (dash) and /mcp2 (board)", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatal(err)
	}
}

func newDashServer() *server.MCPServer {
	s := server.NewMCPServer("dash", "1.0.0", server.WithToolCapabilities(false), server.WithResourceCapabilities(false, false))

	s.AddTool(withMeta(mcp.NewTool("view", mcp.WithDescription("Shows the dashboard")),
		map[string]any{"ui": map[string]any{"resourceUri": "ui://dash/view"}}), answer("dash", "view"))
	s.AddTool(withMeta(mcp.NewTool("refresh", mcp.WithDescription("Refreshes the dashboard; called by its UI")),
		map[string]any{"ui": map[string]any{"resourceUri": "ui://dash/view", "visibility": []any{"app"}}}), answer("dash", "refresh"))
	s.AddTool(withMeta(mcp.NewTool("item", mcp.WithDescription("Shows item 7")),
		map[string]any{"ui": map[string]any{"resourceUri": "ui://dash/items/7"}, "openai/outputTemplate": "ui://dash/items/7"}), answer("dash", "item"))
	s.AddTool(mcp.NewTool("status", mcp.WithDescription("Reports dash's status")), answer("dash", "status"))
	s.AddTool(mcp.NewTool("caps", mcp.WithDescription("Reports whether the request declared MCP Apps")),
		func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if declaresApps(ctx) {
				return mcp.NewToolResultText("mcp apps declared"), nil
			}
			return mcp.NewToolResultText("mcp apps not declared"), nil
		})

	s.AddResource(mcp.NewResource("ui://dash/view", "dashboard", mcp.WithMIMEType(appsMimeType)),
		func(_ context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
			return []mcp.ResourceContents{mcp.TextResourceContents{URI: req.Params.URI, MIMEType: appsMimeType, Text: "<html>dash view</html>"}}, nil
		})
	s.AddResourceTemplate(mcp.NewResourceTemplate("ui://dash/items/{id}", "item", mcp.WithTemplateMIMEType(appsMimeType)),
		func(_ context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
			id := strings.TrimPrefix(req.Params.URI, "ui://dash/items/")
			return []mcp.ResourceContents{mcp.TextResourceContents{URI: req.Params.URI, MIMEType: appsMimeType, Text: "<html>item " + id + "</html>"}}, nil
		})
	return s
}

func newBoardServer() *server.MCPServer {
	s := server.NewMCPServer("board", "1.0.0", server.WithToolCapabilities(false))
	s.AddTool(mcp.NewTool("status", mcp.WithDescription("Reports board's status")), answer("board", "status"))
	return s
}

// declaresApps reports whether the caller declared the MCP Apps extension: per request on 2026-07-28,
// at initialize before that.
func declaresApps(ctx context.Context) bool {
	var caps mcp.ClientCapabilities
	if info := server.RequestProtocolInfoFromContext(ctx); info != nil && info.ClientCapabilities != nil {
		caps = *info.ClientCapabilities
	} else if session, ok := server.ClientSessionFromContext(ctx).(server.SessionWithClientInfo); ok {
		caps = session.GetClientCapabilities()
	}
	_, ok := caps.Extensions[appsExtension]
	return ok
}

func withMeta(tool mcp.Tool, fields map[string]any) mcp.Tool {
	tool.Meta = &mcp.Meta{AdditionalFields: fields}
	return tool
}

func answer(serverName, tool string) server.ToolHandlerFunc {
	text := serverName + ":" + tool
	return func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return mcp.NewToolResultText(text), nil
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
