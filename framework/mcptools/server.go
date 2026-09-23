package mcptools

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/maximhq/bifrost/core/schemas"
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
		description := tool.description
		if tool.mutating {
			description = writeToolDescriptionPrefix + description
		}
		declaration := mcp.NewToolWithRawSchema(tool.name, description, json.RawMessage(tool.schemaJSON))
		declaration.Annotations.ReadOnlyHint = mcp.ToBoolPtr(!tool.mutating)
		mcpServer.AddTool(declaration, toolHandler(deps, tool))
	}
	return mcpServer
}

// IsBuiltinWriteTool reports whether toolName on clientName is one of this
// server's write tools. The logging plugin keeps their arguments and results
// out of the MCP tool log: that is where provider-key values, connection
// strings, plugin configs and the one-time secrets from create_virtual_key,
// rotate_virtual_key and create_webhook travel. Read tools stay fully logged.
func IsBuiltinWriteTool(clientName, toolName string) bool {
	return clientName == bifrostMCPClientName && writeToolNames()[toolName]
}

var writeToolNames = sync.OnceValue(func() map[string]bool {
	names := map[string]bool{}
	for _, tool := range buildTools() {
		if tool.mutating {
			names[tool.name] = true
		}
	}
	return names
})

// writeToolDescriptionPrefix tells a model up front who may run a write tool,
// and how far its change reaches, rather than letting it find out per call.
const writeToolDescriptionPrefix = "Changes Bifrost configuration: runs only if this tool is granted to the calling virtual key, or for a dashboard admin when no key is sent. A key under a team or customer changes only what that team or customer owns; a key with neither changes deployment-wide configuration. "

// writeAccessKey marks a request the transport allowed to run write tools.
// Unexported, so only GrantWriteAccess can set it: no header, plugin or
// context-key mapping can forge it.
type writeAccessKey struct{}

// userValueSetter is the one method GrantWriteAccess needs; *fasthttp.RequestCtx
// has it, and naming the interface keeps fasthttp out of this package.
type userValueSetter interface {
	SetUserValue(key any, value any)
}

// GrantWriteAccess marks an ungoverned request - one that carries no virtual
// key or other identity governance resolves permits for - as allowed to run
// write tools. The transport follows the admin API's rule: it calls this when
// dashboard authentication is disabled (the admin API is open then too), and
// otherwise only after validating an admin credential. A governed request does
// not need it; see writeAllowed.
//
// It is set on the fasthttp request because that is the parent of every
// BifrostContext built for the request, which is how it reaches the in-process
// tool handler through core's tool execution.
func GrantWriteAccess(ctx userValueSetter) {
	ctx.SetUserValue(writeAccessKey{}, true)
}

// WithWriteAccess is GrantWriteAccess for a plain context, for callers that do
// not start from a fasthttp request.
func WithWriteAccess(ctx context.Context) context.Context {
	return context.WithValue(ctx, writeAccessKey{}, true)
}

// writeAllowed decides whether a write tool may run for this call.
//
// A governed request - governance resolved permits for its virtual key or user
// - is allowed: governance already narrowed the tools it may see to its grants
// and checks the grant again in PreMCPHook before execution, so reaching this
// handler means the tool was granted. Whatever a key is granted on Bifrost it
// can do through these tools, and nothing more.
//
// A grant is read like any other MCP grant: "bifrostmcp-*" is every tool of
// this server, write tools included. How far a granted write reaches is the
// key's tenant, applied after this check (tenantCallContext): a key under a
// team or customer is refused every deployment-wide tool and changes only rows
// its tenant owns, while a key with neither changes deployment-wide
// configuration - granting it this server's write tools makes it an admin.
//
// An ungoverned request has no grants to consult, which is exactly the caller
// the old hole let through with every tool. It falls back to the admin API's
// rule, carried by GrantWriteAccess.
func writeAllowed(ctx context.Context) bool {
	if g := schemas.GrantFromContext(ctx); g != nil && g.Access() != nil {
		return true
	}
	return HasWriteAccess(ctx)
}

// HasWriteAccess reports whether the transport allowed this ungoverned request
// to write.
func HasWriteAccess(ctx context.Context) bool {
	granted, _ := ctx.Value(writeAccessKey{}).(bool)
	return granted
}

// StaticDeps adapts a fixed Deps to NewServer, for callers whose dependencies
// never change after construction.
func StaticDeps(deps *Deps) func() *Deps {
	return func() *Deps { return deps }
}

// toolHandler adapts one Tool's execute closure into the mcp-go handler
// shape, applying the same result-size bound every tool result gets
// regardless of which flow produced it.
func toolHandler(deps func() *Deps, tool Tool) server.ToolHandlerFunc {
	// Read off the schema once: it is the one place a tool's arguments are
	// declared, so a refusal can never disagree with what was advertised.
	accepted := tool.argumentNames()
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if tool.mutating && !writeAllowed(ctx) {
			return mcp.NewToolResultError(tool.name + " changes Bifrost's configuration and this request has no permission to: send a virtual key this tool is granted to, or the dashboard admin credentials."), nil
		}
		ctx, refusal := tenantCallContext(ctx, tool)
		if refusal != "" {
			return mcp.NewToolResultError(refusal), nil
		}
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
		if current.LogManager == nil && !tool.noLogs {
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
		if len(args) == 0 {
			return nil
		}
		unknown := make([]string, 0, len(args))
		for name := range args {
			unknown = append(unknown, name)
		}
		slices.Sort(unknown)
		return fmt.Errorf("%s does not take %s", tool, strings.Join(unknown, ", "))
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
