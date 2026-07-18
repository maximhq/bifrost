package openai

import (
	"github.com/bytedance/sonic"

	"github.com/maximhq/bifrost/core/schemas"
)

// convertAnthropicToolSearchCallToOpenAINative converts a completed
// Anthropic-origin tool_search_tool_call neutral item (produced by ingesting
// a real Anthropic tool_search_tool_bm25/_regex round trip) into OpenAI's own
// native tool_search_call + tool_search_output item pair, for replay when a
// conversation's backend switches to OpenAI. Case #4 in the cross-provider
// tool_search mapping doc:
// memory/anthropicschema/gen/fix-execution/expanded-coverage/tool-search-cross-provider-mapping.md
//
// requestTools is the CURRENT request's tools[] (bifrostReq.Params.Tools) --
// needed to backfill full function definitions (description/parameters/
// defer_loading) for each discovered tool, since Anthropic's
// tool_search_tool_result only ever carries a bare tool name (documented
// limitation L2). A discovered name not found in requestTools degrades to a
// name-only definition rather than fabricating fields.
//
// Returns the item unchanged (wrapped in a single-element slice) if it isn't
// an Anthropic tool_search_tool_call item, or if building the native pair
// fails -- never drops an item silently.
func convertAnthropicToolSearchCallToOpenAINative(msg schemas.ResponsesMessage, requestTools []schemas.ResponsesTool) []schemas.ResponsesMessage {
	if msg.Type == nil || *msg.Type != schemas.ResponsesMessageTypeAnthropicToolSearchCall || msg.ResponsesToolMessage == nil {
		return []schemas.ResponsesMessage{msg}
	}

	callID := ""
	if msg.ResponsesToolMessage.CallID != nil {
		callID = *msg.ResponsesToolMessage.CallID
	} else if msg.ID != nil {
		callID = *msg.ID
	}

	argsJSON := "{}"
	if msg.ResponsesToolMessage.Arguments != nil && *msg.ResponsesToolMessage.Arguments != "" {
		argsJSON = *msg.ResponsesToolMessage.Arguments
	}

	callItem, err := schemas.NewOpenAIToolSearchCallItem(callID, argsJSON)
	if err != nil {
		return []schemas.ResponsesMessage{msg}
	}

	// The source call hasn't completed yet (no Output) -- emit only the call
	// half. Fabricating a "completed, empty result" tool_search_output here
	// would hide the real result once it actually arrives, corrupting
	// conversation state on a backend switch mid-search.
	if msg.ResponsesToolMessage.Output == nil {
		return []schemas.ResponsesMessage{callItem}
	}

	var discoveredNames []string
	if msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr != nil {
		if err := sonic.Unmarshal([]byte(*msg.ResponsesToolMessage.Output.ResponsesToolCallOutputStr), &discoveredNames); err != nil {
			// Malformed/unexpected output shape -- fall back to the original
			// item rather than fabricating a "completed, zero results"
			// tool_search_output, matching the two error paths below.
			return []schemas.ResponsesMessage{msg}
		}
	}

	discovered := make([]schemas.OpenAIToolSearchDiscoveredTool, 0, len(discoveredNames))
	for _, name := range discoveredNames {
		d := schemas.OpenAIToolSearchDiscoveredTool{Name: name}
		if def := findResponsesToolByName(requestTools, name); def != nil {
			d.Description = def.Description
			d.DeferLoading = def.DeferLoading
			if def.ResponsesToolFunction != nil {
				d.Parameters = def.ResponsesToolFunction.Parameters
			}
		}
		discovered = append(discovered, d)
	}

	outputItem, err := schemas.NewOpenAIToolSearchOutputItem(callID, discovered)
	if err != nil {
		return []schemas.ResponsesMessage{msg}
	}

	return []schemas.ResponsesMessage{callItem, outputItem}
}

// findResponsesToolByName looks up a declared tool by name in the current
// request's tools[] -- the same-request lookup mechanism backing the L2
// backfill (no cross-request state needed: Anthropic requires the tool to
// already be declared in this exact request for tool_search to find it at
// all, so its full definition is always in-memory for the duration of the
// request that discovered it).
func findResponsesToolByName(tools []schemas.ResponsesTool, name string) *schemas.ResponsesTool {
	for i := range tools {
		if tools[i].Name != nil && *tools[i].Name == name {
			return &tools[i]
		}
	}
	return nil
}
