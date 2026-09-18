package warp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/mcptools"
)

// Warp's tools live on Bifrost's MCP server (framework/mcptools), not here.
// This file is the glue between the two: which of that server's tools Warp is
// allowed to call, how their declarations are fetched for the model, and the
// one tool that is Warp's own - ask_user, a chat UI primitive with no place on
// a server other callers reach.

// Now is a package-level seam so tests can pin "now" for the prompt's
// current-time reference.
var Now = func() time.Time { return time.Now().UTC() }

// MaxToolResultBytes mirrors mcptools' cap. It is what every tool result Warp
// receives has already been bounded to, and it sizes the conversation budget
// in agent.go.
const MaxToolResultBytes = mcptools.MaxToolResultBytes

// BifrostMCPClientName is the client name/ID under which the transport
// registers Bifrost's own MCP server (framework/mcptools). It must not contain
// a hyphen: core/mcp joins client and tool names as "<client>-<tool>" and
// rejects client names that would make the split ambiguous.
const BifrostMCPClientName = "bifrostmcp"

// mcpToolPrefix is what core/mcp prepends to every tool the server hosts.
const mcpToolPrefix = BifrostMCPClientName + "-"

// allowedTools is the subset of the server's tools Warp may call, by their
// un-prefixed names - the names the system prompt refers to. This list is the
// single source of truth: the model is offered exactly these, the MCP include
// header names exactly these, and executeTool dispatches only these.
var allowedTools = []string{
	"semantic_search_logs",
	"query_logs",
	"count_logs",
	"get_log_detail",
	"get_request_trace",
	"query_metrics",
	"query_usage_by",
	"query_model_performance",
	"describe_filter_space",
	"describe_virtual_key",
}

// allowedMCPToolNames lists the allowed tools in the prefixed
// "clientName-toolName" form core/mcp's include-filter matches against.
func allowedMCPToolNames() []string {
	names := make([]string, 0, len(allowedTools))
	for _, name := range allowedTools {
		names = append(names, mcpToolPrefix+name)
	}
	return names
}

// MCPToolLister is the loop's dependency on tool discovery: the declarations
// the server currently hosts, as core/mcp reports them for a request context.
// Like ChatFunc and MCPExecutor it is a function rather than *bifrost.Bifrost
// so tests can script the list. In production it wraps GetAvailableMCPTools.
type MCPToolLister func(ctx *schemas.BifrostContext) []schemas.ChatTool

// withoutTool wraps list so it never reports the named tool, for a tool the
// server hosts but this turn cannot use. declaredTools offers only what the
// lister reports, so this keeps it out of the model's view; a call the model
// makes anyway still reaches the server and gets the tool's own refusal.
func withoutTool(list MCPToolLister, name string) MCPToolLister {
	if list == nil {
		return nil
	}
	prefixed := mcpToolPrefix + name
	return func(ctx *schemas.BifrostContext) []schemas.ChatTool {
		tools := list(ctx)
		kept := make([]schemas.ChatTool, 0, len(tools))
		for _, tool := range tools {
			if tool.Function != nil && tool.Function.Name == prefixed {
				continue
			}
			kept = append(kept, tool)
		}
		return kept
	}
}

// declaredTools builds the tool set offered to the model for one turn:
// the server's declarations for the allowed subset, in allowedTools order,
// with the client prefix stripped so the names match the prompt, plus ask_user.
//
// A tool the server does not currently report is left out rather than failing
// the turn - a deployment with logging disabled hosts the server with every
// tool reporting itself unavailable, and the model is better told there is
// nothing to read than shown a tool that will refuse it.
func declaredTools(ctx context.Context, list MCPToolLister) ([]schemas.ResponsesTool, error) {
	byName := map[string]schemas.ChatTool{}
	if list != nil {
		bifrostCtx, cancel := schemas.NewBifrostContextWithCancel(ctx)
		bifrostCtx.SetValue(schemas.MCPContextKeyIncludeClients, []string{BifrostMCPClientName})
		bifrostCtx.SetValue(schemas.MCPContextKeyIncludeTools, allowedMCPToolNames())
		for _, tool := range list(bifrostCtx) {
			if tool.Function == nil || !strings.HasPrefix(tool.Function.Name, mcpToolPrefix) {
				continue
			}
			byName[strings.TrimPrefix(tool.Function.Name, mcpToolPrefix)] = tool
		}
		cancel()
	}

	declared := make([]schemas.ResponsesTool, 0, len(allowedTools)+1)
	for _, name := range allowedTools {
		tool, ok := byName[name]
		if !ok {
			continue
		}
		responses := tool.ToResponsesTool()
		responses.Name = new(name)
		restoreAuthoredOrder(name, responses)
		declared = append(declared, *responses)
	}

	askUser, err := askUserDeclaration()
	if err != nil {
		return nil, err
	}
	return append(declared, askUser), nil
}

// askUserDeclaration parses ask_user's schema into the provider-facing type.
func askUserDeclaration() (schemas.ResponsesTool, error) {
	var parameters schemas.ToolFunctionParameters
	if err := sonic.UnmarshalString(AskUserSchema, &parameters); err != nil {
		return schemas.ResponsesTool{}, fmt.Errorf("warp tool %s has an invalid schema: %w", AskUserTool, err)
	}
	return schemas.ResponsesTool{
		Type:        schemas.ResponsesToolTypeFunction,
		Name:        new(AskUserTool),
		Description: new(askUserDescription),
		ResponsesToolFunction: &schemas.ResponsesToolFunction{
			Parameters: &parameters,
		},
	}, nil
}

// restoreAuthoredOrder puts a declared tool's schema properties back into the
// order mcptools wrote them in. The trip through MCP decodes every schema into
// a plain Go map, so what comes back is ordered deterministically but
// alphabetically - and these schemas are authored deliberately, with the
// fields that steer a query first. Alphabetised, "start_time" sits sixteen
// fields down and the model asks which window to use instead of reading that
// it accepts "-7d"; "providers" sinks below "dimension" and the model tries to
// rank by a dimension that does not exist. The content is unchanged either
// way, so this only reorders what is already there.
func restoreAuthoredOrder(name string, tool *schemas.ResponsesTool) {
	if tool.ResponsesToolFunction == nil || tool.ResponsesToolFunction.Parameters == nil {
		return
	}
	// The parameters, and the maps inside them, are the MCP manager's stored
	// declaration - ToResponsesTool copies the pointer, not the schema - and
	// every concurrent turn is handed the same one. Copy each level before it
	// is written, so the reorder stays this turn's own. Only the top level of
	// each map is written, so shallow clones are enough.
	params := *tool.ResponsesToolFunction.Parameters
	tool.ResponsesToolFunction.Parameters = &params
	params.Properties = reorderProperties(params.Properties.Clone(), mcptools.PropertyOrder(name))
	filters, ok := params.Properties.Get("filters")
	if !ok {
		return
	}
	nested, ok := filters.(*schemas.OrderedMap)
	if !ok {
		return
	}
	nested = nested.Clone()
	params.Properties.Set("filters", nested)
	properties, ok := nested.Get("properties")
	if !ok {
		return
	}
	inner, ok := properties.(*schemas.OrderedMap)
	if !ok {
		return
	}
	nested.Set("properties", reorderProperties(inner, mcptools.FilterPropertyOrder()))
}

// reorderProperties returns properties with the keys order names first, in
// that order, and anything order does not name kept after them in the order it
// already had. A key order names but properties does not hold is skipped, so
// an order that has drifted from the schema degrades to a partial reordering
// rather than dropping a field the model needs.
func reorderProperties(properties *schemas.OrderedMap, order []string) *schemas.OrderedMap {
	if properties == nil || len(order) == 0 {
		return properties
	}
	reordered := schemas.NewOrderedMapWithCapacity(properties.Len())
	for _, key := range order {
		if value, ok := properties.Get(key); ok {
			reordered.Set(key, value)
		}
	}
	properties.Range(func(key string, value any) bool {
		if _, ok := reordered.Get(key); !ok {
			reordered.Set(key, value)
		}
		return true
	})
	return reordered
}
