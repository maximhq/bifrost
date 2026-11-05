package bifrost

import (
	"slices"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// hoistResponsesAdditionalTools promotes embedded client declarations before wire conversion without mutating the request shared by fallback attempts.
func hoistResponsesAdditionalTools(request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesRequest, *schemas.BifrostError) {
	if request == nil {
		return nil, nil
	}
	var tools []schemas.ResponsesTool
	found := false
	for _, message := range request.Input {
		if message.Type == nil || *message.Type != schemas.ResponsesMessageTypeAdditionalTools {
			continue
		}
		found = true
		var declared []schemas.ResponsesTool
		if err := schemas.Unmarshal(message.AdditionalTools, &declared); err != nil {
			return nil, providerUtils.NewBifrostBadRequestError("invalid tools in additional_tools input item")
		}
		tools = append(tools, declared...)
	}
	if !found {
		return request, nil
	}

	prepared := *request
	params := schemas.ResponsesParameters{}
	if request.Params != nil {
		params = *request.Params
	}
	merged := make([]schemas.ResponsesTool, 0, len(params.Tools)+len(tools))
	merged = append(merged, params.Tools...)
	params.Tools = append(merged, tools...)
	prepared.Params = &params
	prepared.Input = make([]schemas.ResponsesMessage, 0, len(request.Input))
	for _, message := range request.Input {
		if message.Type == nil || *message.Type != schemas.ResponsesMessageTypeAdditionalTools {
			prepared.Input = append(prepared.Input, message)
		}
	}
	return &prepared, nil
}

// hoistClientToolSearchOutputTools declares the tools a caller's client-run tool search
// loaded, for a Claude request. Codex keeps its deferred tools on its side and returns
// the matches in tool_search_output items; OpenAI loads them from there, but the
// Anthropic Messages API only resolves a tool_reference to a tool declared in the
// request with defer_loading. Each loaded tool is therefore added to the tools,
// deferred (so the prompt-cache prefix is unchanged), before namespaces are flattened,
// so the loaded tools get the same flat names and response-side restore as the rest.
// The output items stay in input: the converter turns them into tool_result blocks.
//
// Only Claude requests that declare a client tool_search are touched; everything else,
// and a request whose history holds no client search output, comes back as the same
// pointer without allocating. Copy-on-write, so fallbacks see the caller's request.
func hoistClientToolSearchOutputTools(ctx *schemas.BifrostContext, request *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesRequest, *schemas.BifrostError) {
	if request == nil || !declaresClientToolSearch(request) || !schemas.IsAnthropicModelFamily(ctx, request.Model) {
		return request, nil
	}
	var declared []schemas.ResponsesTool
	if request.Params != nil {
		declared = request.Params.Tools
	}
	var loaded []schemas.ResponsesTool
	for i := range request.Input {
		message := &request.Input[i]
		if message.Type == nil || *message.Type != schemas.ResponsesMessageTypeToolSearchOutput ||
			!message.IsClientToolSearchItem() || len(message.ToolSearchOutputTools) == 0 {
			continue
		}
		var tools []schemas.ResponsesTool
		if err := schemas.Unmarshal(message.ToolSearchOutputTools, &tools); err != nil {
			return nil, providerUtils.NewBifrostBadRequestError("invalid tools in tool_search_output input item")
		}
		loaded = append(loaded, tools...)
	}
	if len(loaded) == 0 {
		return request, nil
	}

	merged := make([]schemas.ResponsesTool, len(declared), len(declared)+len(loaded))
	copy(merged, declared)
	functions := make(map[string]bool)
	namespaces := make(map[string]int)
	for i := range merged {
		tool := &merged[i]
		if tool.Name == nil {
			continue
		}
		switch tool.Type {
		case schemas.ResponsesToolTypeFunction, schemas.ResponsesToolTypeCustom:
			functions[*tool.Name] = true
		case schemas.ResponsesToolTypeNamespace:
			namespaces[*tool.Name] = i
		}
	}
	for _, tool := range loaded {
		if tool.Name == nil || *tool.Name == "" {
			continue
		}
		switch tool.Type {
		case schemas.ResponsesToolTypeFunction:
			// A tool found by several searches, or already declared, is declared once.
			if functions[*tool.Name] {
				continue
			}
			functions[*tool.Name] = true
			tool.DeferLoading = schemas.Ptr(true)
			merged = append(merged, tool)
		case schemas.ResponsesToolTypeNamespace:
			if tool.ResponsesToolNamespace == nil {
				continue
			}
			idx, seen := namespaces[*tool.Name]
			var members []schemas.ResponsesTool
			if seen && merged[idx].ResponsesToolNamespace != nil {
				members = slices.Clone(merged[idx].ResponsesToolNamespace.Tools)
			}
			for _, member := range tool.ResponsesToolNamespace.Tools {
				if member.Name == nil || slices.ContainsFunc(members, func(m schemas.ResponsesTool) bool {
					return m.Name != nil && *m.Name == *member.Name
				}) {
					continue
				}
				member.DeferLoading = schemas.Ptr(true)
				members = append(members, member)
			}
			if seen {
				merged[idx].ResponsesToolNamespace = &schemas.ResponsesToolNamespace{Tools: members}
				continue
			}
			tool.ResponsesToolNamespace = &schemas.ResponsesToolNamespace{Tools: members}
			namespaces[*tool.Name] = len(merged)
			merged = append(merged, tool)
		}
	}

	prepared := *request
	var params schemas.ResponsesParameters
	if request.Params != nil {
		params = *request.Params
	}
	params.Tools = merged
	prepared.Params = &params
	return &prepared, nil
}

// declaresClientToolSearch reports whether request declares a client-run tool_search,
// in tools or by a tool_addition block.
func declaresClientToolSearch(request *schemas.BifrostResponsesRequest) bool {
	if request.Params != nil {
		for i := range request.Params.Tools {
			if request.Params.Tools[i].IsClientToolSearch() {
				return true
			}
		}
	}
	found := false
	schemas.ForEachInlineToolDefinition(request.Input, func(def *schemas.ResponsesTool) bool {
		found = def.IsClientToolSearch()
		return !found
	})
	return found
}
