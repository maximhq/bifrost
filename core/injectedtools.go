package bifrost

import (
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// Provider-injected tools (ProviderConfig.InjectedTools) are MCP tools Bifrost adds to
// every chat and responses request a provider serves, and executes itself when the
// model calls them. The client never declares them and never sees their calls.
//
// Everything here is per attempt and copy-on-write: req.BifrostRequest is shared across
// retries and fallbacks, so a fallback to a provider without injected tools must see
// the caller's request exactly as sent, native web search tool included.

// injectedToolSource is the slice of the MCP manager that injected tools need.
type injectedToolSource interface {
	GetInjectedTool(clientName, toolName string) (schemas.ChatTool, error)
	GetMaxAgentDepth() int
}

// injectedToolSet is the resolved set of tools injected into one attempt.
type injectedToolSet struct {
	tools         []schemas.ChatTool  // function tools with prefixed names
	names         map[string]struct{} // prefixed names, for classifying the model's calls
	webSearchName string              // prefixed name of the web search tool, "" when not configured
	maxDepth      int                 // model turns the tool loop may run before it stops executing
}

func (s *injectedToolSet) isInjected(name string) bool {
	if s == nil {
		return false
	}
	_, ok := s.names[name]
	return ok
}

// resolveInjectedTools returns the tools to inject for one attempt, or nil when the
// provider injects nothing for this request type.
//
// A tool that cannot be resolved (client missing, disabled, or not exposing the tool)
// fails open: the request goes out as the caller sent it, native web search included.
// Failing closed would turn an MCP server outage into an outage of every model on the
// provider.
func resolveInjectedTools(src injectedToolSource, config *schemas.ProviderConfig, requestType schemas.RequestType, logger schemas.Logger) *injectedToolSet {
	if src == nil || config == nil || config.InjectedTools.IsEmpty() {
		return nil
	}
	switch requestType {
	case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest, schemas.ResponsesRequest, schemas.ResponsesStreamRequest:
	default:
		return nil
	}
	ref := config.InjectedTools.WebSearch
	tool, err := src.GetInjectedTool(ref.MCPClientName, ref.ToolName)
	if err != nil || tool.Function == nil {
		if logger != nil {
			logger.Warn("injected web search tool %s/%s unavailable, sending request without it: %v", ref.MCPClientName, ref.ToolName, err)
		}
		return nil
	}
	maxDepth := src.GetMaxAgentDepth()
	if maxDepth <= 0 {
		maxDepth = schemas.DefaultMaxAgentDepth
	}
	name := tool.Function.Name
	return &injectedToolSet{
		tools:         []schemas.ChatTool{tool},
		names:         map[string]struct{}{name: {}},
		webSearchName: name,
		maxDepth:      maxDepth,
	}
}

// isNativeWebSearchType reports whether a tool type is a provider-hosted web search:
// Anthropic's versioned web_search_*, OpenAI's web_search and web_search_preview*, and
// OpenRouter's openrouter:web_search.
func isNativeWebSearchType(t string) bool {
	return strings.HasPrefix(t, "web_search") || t == schemas.ResponsesToolTypeOpenRouterPrefix+"web_search"
}

// nativeToolName is the name a tool choice uses to pin a native web search tool.
// Anthropic declares it on the tool ("web_search"); OpenAI's hosted tool carries none
// and is pinned by type, so the conventional name stands in.
func nativeToolName(name string) string {
	if name == "" {
		return "web_search"
	}
	return name
}

// applyInjectedToolsChat returns a copy of r with the native web search removed and the
// injected tools declared. r and its Params are never written.
func applyInjectedToolsChat(r *schemas.BifrostChatRequest, set *injectedToolSet) *schemas.BifrostChatRequest {
	if r == nil || set == nil {
		return r
	}
	cp := *r
	var params schemas.ChatParameters
	if r.Params != nil {
		params = *r.Params
	}
	replaced := map[string]struct{}{}
	tools := make([]schemas.ChatTool, 0, len(params.Tools)+len(set.tools))
	for _, tool := range params.Tools {
		if set.webSearchName != "" && isNativeWebSearchType(string(tool.Type)) {
			replaced[nativeToolName(tool.Name)] = struct{}{}
			continue
		}
		if tool.Function != nil && set.isInjected(tool.Function.Name) {
			continue
		}
		tools = append(tools, tool)
	}
	// A name a retained tool still carries points at that tool, not the removed one.
	for _, tool := range tools {
		if tool.Function != nil {
			delete(replaced, tool.Function.Name)
		}
		if tool.Name != "" {
			delete(replaced, tool.Name)
		}
	}
	params.Tools = append(tools, set.tools...)
	if set.webSearchName != "" {
		params.WebSearchOptions = nil
		params.ToolChoice = remapChatToolChoice(params.ToolChoice, replaced, set.webSearchName)
	}
	cp.Params = &params
	return &cp
}

// remapChatToolChoice points a tool choice that pinned a replaced native tool at the
// injected tool, so a forced web search stays forced. Other choices pass through.
func remapChatToolChoice(choice *schemas.ChatToolChoice, replaced map[string]struct{}, injected string) *schemas.ChatToolChoice {
	if choice == nil || choice.ChatToolChoiceStruct == nil {
		return choice
	}
	s := choice.ChatToolChoiceStruct
	pinned := isNativeWebSearchType(string(s.Type))
	if s.Function != nil {
		_, pinned = replaced[s.Function.Name]
	}
	if !pinned {
		return choice
	}
	return &schemas.ChatToolChoice{ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
		Type:     schemas.ChatToolChoiceTypeFunction,
		Function: &schemas.ChatToolChoiceFunction{Name: injected},
	}}
}

// applyInjectedToolsResponses is the Responses API parallel of applyInjectedToolsChat.
func applyInjectedToolsResponses(r *schemas.BifrostResponsesRequest, set *injectedToolSet) *schemas.BifrostResponsesRequest {
	if r == nil || set == nil {
		return r
	}
	cp := *r
	var params schemas.ResponsesParameters
	if r.Params != nil {
		params = *r.Params
	}
	replaced := map[string]struct{}{}
	tools := make([]schemas.ResponsesTool, 0, len(params.Tools)+len(set.tools))
	for _, tool := range params.Tools {
		if set.webSearchName != "" && isNativeWebSearchType(string(tool.Type)) {
			var name string
			if tool.Name != nil {
				name = *tool.Name
			}
			replaced[nativeToolName(name)] = struct{}{}
			continue
		}
		if tool.Name != nil && set.isInjected(*tool.Name) {
			continue
		}
		tools = append(tools, tool)
	}
	// A name a retained tool still carries points at that tool, not the removed one.
	for _, tool := range tools {
		if tool.Name != nil {
			delete(replaced, *tool.Name)
		}
	}
	for i := range set.tools {
		tools = append(tools, *set.tools[i].ToResponsesTool())
	}
	params.Tools = tools
	if set.webSearchName != "" {
		params.ToolChoice = remapResponsesToolChoice(params.ToolChoice, replaced, set.webSearchName)
	}
	cp.Params = &params
	return &cp
}

func remapResponsesToolChoice(choice *schemas.ResponsesToolChoice, replaced map[string]struct{}, injected string) *schemas.ResponsesToolChoice {
	if choice == nil || choice.ResponsesToolChoiceStruct == nil {
		return choice
	}
	s := choice.ResponsesToolChoiceStruct
	pinned := isNativeWebSearchType(string(s.Type))
	if s.Type == schemas.ResponsesToolChoiceTypeFunction && s.Name != nil {
		_, pinned = replaced[*s.Name]
	}
	if !pinned {
		return choice
	}
	return &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{
		Type: schemas.ResponsesToolChoiceTypeFunction,
		Name: new(injected),
	}}
}
