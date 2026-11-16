package handlers

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	coremcp "github.com/maximhq/bifrost/core/mcp"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/valyala/fasthttp"
)

// mcpToolNamesHeader picks the names a /mcp/<slug> request for one MCP client lists that client's
// tools by. An MCP client such as Claude Code already namespaces every tool by the name its user
// gave the server entry (mcp__<entry>__<tool>), so the client prefix Bifrost adds repeats it there.
const mcpToolNamesHeader = "x-bf-mcp-tool-names"

const (
	// mcpToolNamesPrefixed lists Bifrost's own names, <client>-<tool>. The default.
	mcpToolNamesPrefixed = "prefixed"
	// mcpToolNamesServer lists the names the client's own server gave its tools.
	mcpToolNamesServer = "server"
)

// applyToolNamesHeader stamps x-bf-mcp-tool-names on an admitted request. "server" needs a slug
// that serves one MCP client: on /mcp and on a Virtual MCP, tools of several servers may share a
// name, so the request is refused rather than served names that would not say which tool they are.
func applyToolNamesHeader(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext) *mcpRefusal {
	value := string(ctx.Request.Header.Peek(mcpToolNamesHeader))
	switch value {
	case "", mcpToolNamesPrefixed:
		return nil
	case mcpToolNamesServer:
		if client, _ := bifrostCtx.Value(schemas.BifrostContextKeyMCPGatewayClient).(string); client == "" {
			return &mcpRefusal{status: fasthttp.StatusBadRequest, message: fmt.Sprintf("%s: %s is only served on /mcp/<slug> for a single MCP client", mcpToolNamesHeader, mcpToolNamesServer)}
		}
		bifrostCtx.SetValue(schemas.BifrostContextKeyMCPServerToolNames, true)
		return nil
	}
	return &mcpRefusal{status: fasthttp.StatusBadRequest, message: fmt.Sprintf("invalid %s %q: must be %q or %q", mcpToolNamesHeader, value, mcpToolNamesPrefixed, mcpToolNamesServer)}
}

// listServerToolNames renames, in a tools/list answer for a request that asked for server-given
// names, each of the slug client's tools to the name its own server gave it. A name is renamed only
// when tools/call resolves it back to that tool (resolveToolAlias), so every listed name is callable:
// a server-given name that is also another tool's exact name keeps its full name, since exact names
// win. Tools that are not the client's own (code-mode tools) keep their names. Runs after every
// filter, which all match Bifrost's prefixed names.
func listServerToolNames(ctx context.Context, index *gatewayToolIndex, result *mcp.ListToolsResult) {
	if wants, _ := ctx.Value(schemas.BifrostContextKeyMCPServerToolNames).(bool); !wants || index == nil {
		return
	}
	client, _ := ctx.Value(schemas.BifrostContextKeyMCPGatewayClient).(string)
	if client == "" {
		return
	}
	for i, tool := range result.Tools {
		if coremcp.ToolClientName(tool.Name) != client {
			continue
		}
		bare := coremcp.StripClientPrefix(tool.Name, client)
		if index.aliasFor(bare, client, tool.Name) {
			result.Tools[i].Name = bare
		}
	}
}

// aliasFor reports whether bare is indexed as the server-given name of client's tool name.
func (index *gatewayToolIndex) aliasFor(bare, client, name string) bool {
	for _, alias := range index.aliases[bare] {
		if alias.client == client && alias.name == name {
			return true
		}
	}
	return false
}
