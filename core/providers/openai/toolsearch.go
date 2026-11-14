package openai

import (
	"encoding/json"
	"slices"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

const toolSearchExecutionServer = "server"

// provisionToolSearch makes a request whose functions are deferred carry the
// tool_search OpenAI requires for them ("Deferred tools require tools.tool_search"),
// on models that support tool search. It decides keepDeferLoading for the
// marshaller; without support every function goes out eagerly.
//
// The search it provisions, in order:
//   - the request's own tool_search tool, cleaned of the Anthropic variant name;
//   - the caller's own search function (Claude Code's ToolSearch), rewritten into a
//     client-executed tool_search, with its calls and tool_reference results
//     replayed as tool_search_call / tool_search_output, unless tool_choice pins that
//     function by name, which a nameless tool_search could no longer satisfy;
//   - otherwise OpenAI's hosted tool_search.
//
// Typed tool_search_call items, the shape Anthropic ingress builds from a replayed
// server-side search, become OpenAI's hosted call/output pair; when no tool_search goes
// out they mean nothing to OpenAI and are dropped. Runs after
// filterUnsupportedTools, on slices that are not shared with the caller's request.
func (req *OpenAIResponsesRequest) provisionToolSearch(supported bool) {
	req.keepDeferLoading = false
	if !supported || len(req.Tools) == 0 {
		req.dropTypedToolSearchCalls()
		return
	}
	hasDeferred := false
	searchIdx := -1
	for i := range req.Tools {
		tool := &req.Tools[i]
		if tool.DeferLoading != nil && *tool.DeferLoading {
			hasDeferred = true
		}
		if tool.Type == schemas.ResponsesToolTypeToolSearch && searchIdx < 0 {
			searchIdx = i
		}
	}
	if !hasDeferred && searchIdx < 0 {
		req.dropTypedToolSearchCalls()
		return
	}
	req.keepDeferLoading = true
	req.Tools = slices.Clone(req.Tools)

	if searchIdx >= 0 {
		// Anthropic ingress records its regex/bm25 variant in Name; OpenAI has no such field.
		req.Tools[searchIdx].Name = nil
	} else if name := schemas.ClientToolSearchFunctionName(req.Tools, req.Input.OpenAIResponsesRequestInputArray); name != "" &&
		!toolChoiceNamesFunction(req.ToolChoice, name) {
		req.mapClientToolSearchFunction(name)
	} else {
		req.Tools = append(req.Tools, schemas.ResponsesTool{Type: schemas.ResponsesToolTypeToolSearch})
	}
	req.replayTypedToolSearchCalls()
}

// mapClientToolSearchFunction rewrites the caller's search function into a
// client-executed tool_search, and its history into tool_search_call /
// tool_search_output items that load the tools its results referenced.
func (req *OpenAIResponsesRequest) mapClientToolSearchFunction(name string) {
	idx := slices.IndexFunc(req.Tools, func(t schemas.ResponsesTool) bool {
		return t.Type == schemas.ResponsesToolTypeFunction && t.Name != nil && *t.Name == name
	})
	if idx < 0 {
		return
	}
	search := schemas.ResponsesTool{
		Type:                    schemas.ResponsesToolTypeToolSearch,
		Description:             req.Tools[idx].Description,
		ResponsesToolToolSearch: &schemas.ResponsesToolToolSearch{Execution: schemas.Ptr(schemas.ToolSearchExecutionClient)},
	}
	if fn := req.Tools[idx].ResponsesToolFunction; fn != nil {
		search.ResponsesToolToolSearch.Parameters = fn.Parameters
	}
	req.Tools[idx] = search

	input := req.Input.OpenAIResponsesRequestInputArray
	if len(input) == 0 {
		return
	}
	searchCalls := make(map[string]struct{})
	rewritten := make([]schemas.ResponsesMessage, len(input))
	for i := range input {
		msg := input[i]
		switch {
		case msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeFunctionCall &&
			msg.ResponsesToolMessage != nil && msg.Name != nil && *msg.Name == name && msg.CallID != nil:
			searchCalls[*msg.CallID] = struct{}{}
			args := "{}"
			if msg.Arguments != nil && json.Valid([]byte(*msg.Arguments)) {
				args = *msg.Arguments
			}
			msg = schemas.ResponsesMessage{
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeToolSearchCall),
				Status: schemas.Ptr("completed"),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					CallID:    msg.CallID,
					Execution: schemas.Ptr(schemas.ToolSearchExecutionClient),
					Arguments: &args,
				},
			}
		case msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeFunctionCallOutput &&
			msg.ResponsesToolMessage != nil && msg.CallID != nil:
			if _, ok := searchCalls[*msg.CallID]; !ok {
				break
			}
			var refs []string
			if msg.Output != nil {
				for _, block := range msg.Output.ResponsesFunctionToolCallOutputBlocks {
					if block.Type == schemas.ResponsesInputMessageContentBlockTypeToolReference && block.ToolName != nil {
						refs = append(refs, *block.ToolName)
					}
				}
			}
			msg = req.toolSearchOutputItem(msg.CallID, schemas.ToolSearchExecutionClient, refs)
		}
		rewritten[i] = msg
	}
	req.Input.OpenAIResponsesRequestInputArray = rewritten
}

// replayTypedToolSearchCalls turns each typed tool_search_call (built by Anthropic
// ingress from server_tool_use plus tool_search_tool_result) into OpenAI's hosted
// tool_search_call followed by the tool_search_output that loaded its references. A
// typed tool_search_output for a replayed call is consumed, since the regenerated one
// replaces it. Verbatim items from an OpenAI-shaped client already carry the OpenAI shape.
func (req *OpenAIResponsesRequest) replayTypedToolSearchCalls() {
	input := req.Input.OpenAIResponsesRequestInputArray
	if !slices.ContainsFunc(input, isTypedToolSearchCall) {
		return
	}
	replayed := make([]schemas.ResponsesMessage, 0, len(input)+1)
	replayedCalls := make(map[string]struct{})
	for i := range input {
		msg := input[i]
		if isTypedToolSearchOutput(msg) && msg.CallID != nil {
			if _, ok := replayedCalls[*msg.CallID]; ok {
				continue
			}
		}
		if !isTypedToolSearchCall(msg) {
			replayed = append(replayed, msg)
			continue
		}
		args := "{}"
		var refs []string
		if msg.ResponsesToolMessage != nil {
			if msg.CallID != nil {
				replayedCalls[*msg.CallID] = struct{}{}
			}
			if msg.Arguments != nil && json.Valid([]byte(*msg.Arguments)) {
				args = *msg.Arguments
			}
			if msg.ResponsesToolSearchCall != nil {
				refs = msg.ResponsesToolSearchCall.ToolReferences
			}
		}
		replayed = append(replayed,
			schemas.ResponsesMessage{
				Type:   schemas.Ptr(schemas.ResponsesMessageTypeToolSearchCall),
				Status: schemas.Ptr("completed"),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					Execution: schemas.Ptr(toolSearchExecutionServer),
					Arguments: &args,
				},
			},
			req.toolSearchOutputItem(nil, toolSearchExecutionServer, refs),
		)
	}
	req.Input.OpenAIResponsesRequestInputArray = replayed
}

// toolChoiceNamesFunction reports whether choice pins the function called name, as a
// forced function or an allowed_tools entry.
func toolChoiceNamesFunction(choice *schemas.ResponsesToolChoice, name string) bool {
	if choice == nil || choice.ResponsesToolChoiceStruct == nil {
		return false
	}
	pinned := choice.ResponsesToolChoiceStruct
	if pinned.Type == schemas.ResponsesToolChoiceTypeFunction && pinned.Name != nil && *pinned.Name == name {
		return true
	}
	return slices.ContainsFunc(pinned.Tools, func(t schemas.ResponsesToolChoiceAllowedToolDef) bool {
		return t.Type == string(schemas.ResponsesToolTypeFunction) && t.Name != nil && *t.Name == name
	})
}

// isTypedToolSearchCall reports whether msg is a server-side tool_search_call held in
// Bifrost's typed shape rather than OpenAI's verbatim bytes.
// dropTypedToolSearchCalls removes typed tool search history from a request that
// carries no tool_search.
func (req *OpenAIResponsesRequest) dropTypedToolSearchCalls() {
	input := req.Input.OpenAIResponsesRequestInputArray
	if !slices.ContainsFunc(input, func(m schemas.ResponsesMessage) bool { return isTypedToolSearchCall(m) || isTypedToolSearchOutput(m) }) {
		return
	}
	req.Input.OpenAIResponsesRequestInputArray = slices.DeleteFunc(slices.Clone(input), func(m schemas.ResponsesMessage) bool {
		return isTypedToolSearchCall(m) || isTypedToolSearchOutput(m)
	})
}

func isTypedToolSearchCall(msg schemas.ResponsesMessage) bool {
	return msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeToolSearchCall &&
		!msg.IsVerbatimItem() && !msg.IsClientToolSearchItem()
}

// isTypedToolSearchOutput is the output-side counterpart of isTypedToolSearchCall.
func isTypedToolSearchOutput(msg schemas.ResponsesMessage) bool {
	return msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeToolSearchOutput &&
		!msg.IsVerbatimItem() && !msg.IsClientToolSearchItem()
}

// toolSearchOutputItem builds a tool_search_output that loads the named tools,
// taking each definition from the request's tools. A name the request does not
// declare is skipped: OpenAI can only load a tool it was given.
func (req *OpenAIResponsesRequest) toolSearchOutputItem(callID *string, execution string, names []string) schemas.ResponsesMessage {
	loaded := make([]schemas.ResponsesTool, 0, len(names))
	for _, name := range names {
		idx := slices.IndexFunc(req.Tools, func(t schemas.ResponsesTool) bool {
			return t.Type == schemas.ResponsesToolTypeFunction && t.Name != nil && *t.Name == name
		})
		if idx < 0 {
			continue
		}
		tool := req.Tools[idx]
		tool.DeferLoading = schemas.Ptr(true)
		loaded = append(loaded, tool)
	}
	tools := json.RawMessage("[]")
	if encoded, err := providerUtils.MarshalSorted(loaded); err == nil {
		tools = encoded
	}
	return schemas.ResponsesMessage{
		Type:                  schemas.Ptr(schemas.ResponsesMessageTypeToolSearchOutput),
		Status:                schemas.Ptr("completed"),
		ToolSearchOutputTools: tools,
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID:    callID,
			Execution: schemas.Ptr(execution),
		},
	}
}
