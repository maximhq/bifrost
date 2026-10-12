package bifrost

import (
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/internal/injectedauth"
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
	tools         []schemas.ChatTool        // function tools named as the model sees them
	targets       map[string]injectedTarget // model-facing name -> MCP tool, for classifying and running the model's calls
	mcpNames      map[string]string         // prefixed MCP name -> model-facing name, so MCP auto-injection never declares one twice
	webSearchName string                    // model-facing name of the tool replacing native web search, "" when none
	maxDepth      int                       // model turns the tool loop may run before it stops executing
}

// injectedTarget is the MCP tool behind one model-facing injected tool name.
type injectedTarget struct {
	clientName string // owning MCP client
	toolName   string // prefixed "<client>-<tool>" name the MCP manager executes
}

func (s *injectedToolSet) isInjected(name string) bool {
	if s == nil {
		return false
	}
	_, ok := s.targets[name]
	return ok
}

// duplicates reports whether a client tool named name must be dropped: it carries an
// injected tool's name, or is the same MCP tool the regular MCP auto-injection added
// under its prefixed name. A tool choice pinning a dropped declaration is pointed at the
// injected tool by pinInjectedChatChoice/pinInjectedResponsesChoice.
func (s *injectedToolSet) duplicates(name string) bool {
	if s == nil || name == "" {
		return false
	}
	_, mcp := s.mcpNames[name]
	return mcp || s.isInjected(name)
}

// resolveInjectedTools returns the tools to inject for one attempt, or nil when the
// provider injects nothing for this request type.
//
// Each tool is declared to the model under its configured name, not the prefixed MCP
// name. A tool that cannot be resolved (client missing, disabled, or not exposing the
// tool) fails open on its own: it is left out and the rest are still injected. When it
// is the web_search tool, the caller's native web search goes out untouched. Failing
// closed would turn an MCP server outage into an outage of every model on the provider.
func resolveInjectedTools(src injectedToolSource, config *schemas.ProviderConfig, requestType schemas.RequestType, logger schemas.Logger) *injectedToolSet {
	if src == nil || config == nil || len(config.InjectedTools) == 0 {
		return nil
	}
	switch requestType {
	case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest, schemas.ResponsesRequest, schemas.ResponsesStreamRequest:
	default:
		return nil
	}
	set := &injectedToolSet{
		targets:  make(map[string]injectedTarget, len(config.InjectedTools)),
		mcpNames: make(map[string]string, len(config.InjectedTools)),
	}
	// The first row with a name owns it, resolved or not, so an unavailable row never
	// hands its name (and, for web_search, the native search it replaces) to a later one.
	seen := make(map[string]struct{}, len(config.InjectedTools))
	for _, ref := range config.InjectedTools {
		if _, dup := seen[ref.Name]; dup || ref.Name == "" {
			continue
		}
		seen[ref.Name] = struct{}{}
		tool, err := src.GetInjectedTool(ref.MCPClientName, ref.ToolName)
		if err != nil || tool.Function == nil {
			if logger != nil {
				logger.Warn("injected tool %s (%s/%s) unavailable, sending request without it: %v", ref.Name, ref.MCPClientName, ref.ToolName, err)
			}
			continue
		}
		// One MCP tool is injected once, under its first row's name, as the management API
		// requires; config.json and SDK accounts reach here unvalidated.
		if _, taken := set.mcpNames[tool.Function.Name]; taken {
			if logger != nil {
				logger.Warn("injected tool %s repeats MCP tool %s/%s, which an earlier row already injects; skipping it", ref.Name, ref.MCPClientName, ref.ToolName)
			}
			continue
		}
		set.targets[ref.Name] = injectedTarget{clientName: ref.MCPClientName, toolName: tool.Function.Name}
		set.mcpNames[tool.Function.Name] = ref.Name
		// GetInjectedTool returns a copy, so renaming it never touches the stored schema.
		tool.Function.Name = ref.Name
		set.tools = append(set.tools, tool)
		if ref.Name == schemas.InjectedToolNameWebSearch {
			set.webSearchName = ref.Name
		}
	}
	if len(set.tools) == 0 {
		return nil
	}
	set.maxDepth = src.GetMaxAgentDepth()
	if set.maxDepth <= 0 {
		set.maxDepth = schemas.DefaultMaxAgentDepth
	}
	return set
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
		// A client tool with an injected tool's name is replaced by it.
		if (tool.Function != nil && set.duplicates(tool.Function.Name)) || set.duplicates(tool.Name) {
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
	params.ToolChoice = pinInjectedChatChoice(params.ToolChoice, set)
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

// injectedChoiceTarget is the injected tool a function or custom tool choice for name
// must pin once the matching client declaration is dropped, or false when the choice is
// fine as it is. A name that is a declared injected tool pins that tool, even when it is
// also another injected tool's prefixed MCP name; otherwise a prefixed MCP name pins
// the tool injected for it.
func injectedChoiceTarget(set *injectedToolSet, name string, isFunction bool) (string, bool) {
	if set.isInjected(name) {
		// A function choice already pins the injected function; a custom one must become one.
		return name, !isFunction
	}
	alias, ok := set.mcpNames[name]
	return alias, ok
}

// pinInjectedChatChoice points a function or custom tool choice whose declaration an
// injected tool replaced at that injected function, and rewrites the entries of an
// allowed_tools choice the same way. Other choices pass through.
func pinInjectedChatChoice(choice *schemas.ChatToolChoice, set *injectedToolSet) *schemas.ChatToolChoice {
	if choice == nil || choice.ChatToolChoiceStruct == nil {
		return choice
	}
	s := choice.ChatToolChoiceStruct
	if s.Type == schemas.ChatToolChoiceTypeAllowedTools && s.AllowedTools != nil {
		return pinInjectedChatAllowedTools(choice, set)
	}
	var name string
	switch {
	case s.Type == schemas.ChatToolChoiceTypeFunction && s.Function != nil:
		name = s.Function.Name
	case s.Type == schemas.ChatToolChoiceTypeCustom && s.Custom != nil:
		name = s.Custom.Name
	default:
		return choice
	}
	target, ok := injectedChoiceTarget(set, name, s.Type == schemas.ChatToolChoiceTypeFunction)
	if !ok {
		return choice
	}
	return &schemas.ChatToolChoice{ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
		Type:     schemas.ChatToolChoiceTypeFunction,
		Function: &schemas.ChatToolChoiceFunction{Name: target},
	}}
}

// pinInjectedResponsesChoice is the Responses API parallel of pinInjectedChatChoice.
func pinInjectedResponsesChoice(choice *schemas.ResponsesToolChoice, set *injectedToolSet) *schemas.ResponsesToolChoice {
	if choice == nil || choice.ResponsesToolChoiceStruct == nil {
		return choice
	}
	s := choice.ResponsesToolChoiceStruct
	if s.Type == schemas.ResponsesToolChoiceTypeAllowedTools {
		return pinInjectedResponsesAllowedTools(choice, set)
	}
	if s.Name == nil || (s.Type != schemas.ResponsesToolChoiceTypeFunction && s.Type != schemas.ResponsesToolChoiceTypeCustom) {
		return choice
	}
	target, ok := injectedChoiceTarget(set, *s.Name, s.Type == schemas.ResponsesToolChoiceTypeFunction)
	if !ok {
		return choice
	}
	return &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{
		Type: schemas.ResponsesToolChoiceTypeFunction,
		Name: new(target),
	}}
}

// pinInjectedChatAllowedTools rewrites an allowed_tools choice whose entries name a
// declaration an injected tool dropped, so each names the injected tool, once. The
// caller's choice is returned as is when nothing changes, and is never written.
func pinInjectedChatAllowedTools(choice *schemas.ChatToolChoice, set *injectedToolSet) *schemas.ChatToolChoice {
	s := choice.ChatToolChoiceStruct
	changed := false
	seen := make(map[string]struct{}, len(s.AllowedTools.Tools))
	tools := make([]schemas.ChatToolChoiceAllowedToolsTool, 0, len(s.AllowedTools.Tools))
	for _, tool := range s.AllowedTools.Tools {
		if target, ok := injectedChoiceTarget(set, tool.Function.Name, true); ok {
			tool = schemas.ChatToolChoiceAllowedToolsTool{Type: string(schemas.ChatToolChoiceTypeFunction), Function: schemas.ChatToolChoiceFunction{Name: target}}
			changed = true
		}
		key := tool.Type + "\x00" + tool.Function.Name
		if _, dup := seen[key]; dup {
			changed = true
			continue
		}
		seen[key] = struct{}{}
		tools = append(tools, tool)
	}
	if !changed {
		return choice
	}
	return &schemas.ChatToolChoice{ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
		Type:         schemas.ChatToolChoiceTypeAllowedTools,
		AllowedTools: &schemas.ChatToolChoiceAllowedTools{Mode: s.AllowedTools.Mode, Tools: tools},
	}}
}

// pinInjectedResponsesAllowedTools is the Responses API parallel of
// pinInjectedChatAllowedTools. A native web search entry also names the injected
// web_search tool when one replaced it.
func pinInjectedResponsesAllowedTools(choice *schemas.ResponsesToolChoice, set *injectedToolSet) *schemas.ResponsesToolChoice {
	s := choice.ResponsesToolChoiceStruct
	changed := false
	seen := make(map[string]struct{}, len(s.Tools))
	tools := make([]schemas.ResponsesToolChoiceAllowedToolDef, 0, len(s.Tools))
	for _, tool := range s.Tools {
		target, ok := "", false
		switch {
		case set.webSearchName != "" && isNativeWebSearchType(tool.Type):
			target, ok = set.webSearchName, true
		case tool.Name != nil && (tool.Type == string(schemas.ResponsesToolChoiceTypeFunction) || tool.Type == string(schemas.ResponsesToolChoiceTypeCustom)):
			target, ok = injectedChoiceTarget(set, *tool.Name, tool.Type == string(schemas.ResponsesToolChoiceTypeFunction))
		}
		if ok {
			tool = schemas.ResponsesToolChoiceAllowedToolDef{Type: string(schemas.ResponsesToolChoiceTypeFunction), Name: new(target)}
			changed = true
		}
		key := tool.Type + "\x00"
		if tool.Name != nil {
			key += *tool.Name
		}
		if tool.ServerLabel != nil {
			key += "\x00" + *tool.ServerLabel
		}
		if _, dup := seen[key]; dup {
			changed = true
			continue
		}
		seen[key] = struct{}{}
		tools = append(tools, tool)
	}
	if !changed {
		return choice
	}
	cp := *s
	cp.Tools = tools
	return &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &cp}
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
		// A client tool with an injected tool's name is replaced by it.
		if tool.Name != nil && set.duplicates(*tool.Name) {
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
	params.ToolChoice = pinInjectedResponsesChoice(params.ToolChoice, set)
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

// runInjectedChatLoop runs a chat request whose provider injects tools. It dispatches
// a turn, executes the injected calls the model made, appends the call and its
// results to the conversation, and dispatches again until the model stops calling
// injected tools or maxDepth turns have run.
//
// The client sees one response: the final turn's, with every earlier turn's text in
// front of it and usage summed across turns. A turn that mixes injected and client
// calls drops the client calls before re-asking, since providers require every call in
// an assistant turn to be answered and only the client can answer its own. The model
// re-issues them in a later turn, which comes back to the client untouched.
func runInjectedChatLoop(
	original *schemas.BifrostChatRequest,
	set *injectedToolSet,
	dispatch func(*schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError),
	exec func(schemas.ChatAssistantMessageToolCall) *schemas.ChatMessage,
) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	turn := applyInjectedToolsChat(original, set)
	var usage *schemas.BifrostLLMUsage
	var priorText []string
	for depth := 1; ; depth++ {
		resp, bifrostErr := dispatch(turn)
		if bifrostErr != nil {
			billEarlierTurns(bifrostErr, usage)
			return nil, bifrostErr
		}
		msg := firstChatMessage(resp)
		var injected, client []schemas.ChatAssistantMessageToolCall
		if msg != nil && msg.ChatAssistantMessage != nil {
			for _, call := range msg.ToolCalls {
				if call.Function.Name != nil && set.isInjected(*call.Function.Name) {
					injected = append(injected, call)
				} else {
					client = append(client, call)
				}
			}
		}
		if len(injected) == 0 || depth >= set.maxDepth {
			if len(injected) > 0 {
				withoutInjectedCalls(resp, 0, client)
			}
			stripInjectedCallsFromOtherChoices(resp, set)
			resp.Usage = schemas.MergeBifrostLLMUsage(usage, resp.Usage)
			prependChatText(resp, priorText)
			return resp, nil
		}
		usage = schemas.MergeBifrostLLMUsage(usage, resp.Usage)
		if text := chatMessageText(msg); text != "" {
			priorText = append(priorText, text)
		}
		assistant := *msg
		assistantFields := *msg.ChatAssistantMessage
		assistantFields.ToolCalls = injected
		assistant.ChatAssistantMessage = &assistantFields

		next := *turn
		next.Params = relaxForcedChatChoice(turn.Params)
		next.Input = make([]schemas.ChatMessage, 0, len(turn.Input)+1+len(injected))
		next.Input = append(next.Input, turn.Input...)
		next.Input = append(next.Input, assistant)
		for _, result := range executeInjectedCalls(injected, exec) {
			next.Input = append(next.Input, *result)
		}
		turn = &next
	}
}

// runInjectedResponsesLoop is the Responses API parallel of runInjectedChatLoop. Every
// output item of an intermediate turn except the client's function calls is replayed as
// input, so reasoning items and their signatures reach the next turn intact.
func runInjectedResponsesLoop(
	original *schemas.BifrostResponsesRequest,
	set *injectedToolSet,
	dispatch func(*schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError),
	exec func(schemas.ChatAssistantMessageToolCall) *schemas.ChatMessage,
) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
	turn := applyInjectedToolsResponses(original, set)
	var usage *schemas.BifrostLLMUsage
	var priorItems []schemas.ResponsesMessage
	for depth := 1; ; depth++ {
		resp, bifrostErr := dispatch(turn)
		if bifrostErr != nil {
			billEarlierTurns(bifrostErr, usage)
			return nil, bifrostErr
		}
		var injected []schemas.ChatAssistantMessageToolCall
		for _, item := range resp.Output {
			if call, ok := injectedResponsesCall(item, set); ok {
				injected = append(injected, call)
			}
		}
		if len(injected) == 0 || depth >= set.maxDepth {
			output := make([]schemas.ResponsesMessage, 0, len(priorItems)+len(resp.Output))
			output = append(output, priorItems...)
			for _, item := range resp.Output {
				if _, ok := injectedResponsesCall(item, set); !ok {
					output = append(output, item)
				}
			}
			resp.Output = output
			resp.Usage = schemas.MergeBifrostLLMUsage(usage, resp.Usage.ToBifrostLLMUsage()).ToResponsesResponseUsage()
			return resp, nil
		}
		usage = schemas.MergeBifrostLLMUsage(usage, resp.Usage.ToBifrostLLMUsage())

		next := *turn
		next.Params = relaxForcedResponsesChoice(turn.Params)
		next.Input = make([]schemas.ResponsesMessage, 0, len(turn.Input)+len(resp.Output)+len(injected))
		next.Input = append(next.Input, turn.Input...)
		for _, item := range resp.Output {
			_, isInjected := injectedResponsesCall(item, set)
			if isFunctionCall(item) && !isInjected {
				continue
			}
			next.Input = append(next.Input, item)
			if !isFunctionCall(item) {
				priorItems = append(priorItems, item)
			}
		}
		for _, result := range executeInjectedCalls(injected, exec) {
			next.Input = append(next.Input, result.ToResponsesMessages()...)
		}
		turn = &next
	}
}

// executeInjectedCalls runs the calls in parallel and returns their results in call
// order, which is the order providers expect tool results in.
func executeInjectedCalls(calls []schemas.ChatAssistantMessageToolCall, exec func(schemas.ChatAssistantMessageToolCall) *schemas.ChatMessage) []*schemas.ChatMessage {
	results := make([]*schemas.ChatMessage, len(calls))
	var wg sync.WaitGroup
	for i, call := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = exec(call)
		}()
	}
	wg.Wait()
	return results
}

func isFunctionCall(item schemas.ResponsesMessage) bool {
	return item.Type != nil && *item.Type == schemas.ResponsesMessageTypeFunctionCall && item.ResponsesToolMessage != nil
}

// injectedResponsesCall returns the item as a chat tool call when it calls an injected tool.
func injectedResponsesCall(item schemas.ResponsesMessage, set *injectedToolSet) (schemas.ChatAssistantMessageToolCall, bool) {
	if !isFunctionCall(item) || item.Name == nil || !set.isInjected(*item.Name) {
		return schemas.ChatAssistantMessageToolCall{}, false
	}
	arguments := ""
	if item.Arguments != nil {
		arguments = *item.Arguments
	}
	return schemas.ChatAssistantMessageToolCall{
		ID:       item.CallID,
		Type:     new("function"),
		Function: schemas.ChatAssistantMessageToolCallFunction{Name: item.Name, Arguments: arguments},
	}, true
}

func firstChatMessage(resp *schemas.BifrostChatResponse) *schemas.ChatMessage {
	if resp == nil || len(resp.Choices) == 0 || resp.Choices[0].ChatNonStreamResponseChoice == nil {
		return nil
	}
	return resp.Choices[0].Message
}

// withoutInjectedCalls removes injected calls from the final response once the loop
// has stopped executing them. A call the client cannot run must never reach it.
func withoutInjectedCalls(resp *schemas.BifrostChatResponse, choice int, client []schemas.ChatAssistantMessageToolCall) {
	msg := resp.Choices[choice].Message
	cleaned := *msg
	if len(client) == 0 {
		cleaned.ChatAssistantMessage = nil
		if fields := msg.ChatAssistantMessage; fields.Reasoning != nil || len(fields.ReasoningDetails) > 0 || fields.Refusal != nil || len(fields.Annotations) > 0 || fields.Audio != nil {
			kept := *fields
			kept.ToolCalls = nil
			cleaned.ChatAssistantMessage = &kept
		}
		resp.Choices[choice].FinishReason = new(string(schemas.BifrostFinishReasonStop))
	} else {
		kept := *msg.ChatAssistantMessage
		kept.ToolCalls = client
		cleaned.ChatAssistantMessage = &kept
	}
	resp.Choices[choice].Message = &cleaned
}

// stripInjectedCallsFromOtherChoices cleans choices after the first. With n>1 the loop
// is driven by the first choice; the others are returned as they are, minus any
// injected call, which the client could not run.
func stripInjectedCallsFromOtherChoices(resp *schemas.BifrostChatResponse, set *injectedToolSet) {
	for i := 1; i < len(resp.Choices); i++ {
		choice := resp.Choices[i]
		if choice.ChatNonStreamResponseChoice == nil || choice.Message == nil || choice.Message.ChatAssistantMessage == nil {
			continue
		}
		var client []schemas.ChatAssistantMessageToolCall
		found := false
		for _, call := range choice.Message.ToolCalls {
			if call.Function.Name != nil && set.isInjected(*call.Function.Name) {
				found = true
			} else {
				client = append(client, call)
			}
		}
		if found {
			withoutInjectedCalls(resp, i, client)
		}
	}
}

// relaxForcedChatChoice returns params for the turn after an injected call ran. A forced
// choice (the remapped web search, "required") would force another tool call after
// every result until maxDepth, leaving the model no turn to answer in, so it becomes
// "auto". An allowed_tools choice keeps its set and only switches to mode auto. Params
// are copied; the caller's are never written.
func relaxForcedChatChoice(params *schemas.ChatParameters) *schemas.ChatParameters {
	if params == nil || !params.ToolChoice.IsForced() {
		return params
	}
	cp := *params
	// An allowed_tools set is the client's limit on which tools may run; only its mode relaxes.
	if s := params.ToolChoice.ChatToolChoiceStruct; s != nil && s.Type == schemas.ChatToolChoiceTypeAllowedTools && s.AllowedTools != nil {
		cp.ToolChoice = &schemas.ChatToolChoice{ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
			Type:         schemas.ChatToolChoiceTypeAllowedTools,
			AllowedTools: &schemas.ChatToolChoiceAllowedTools{Mode: string(schemas.ChatToolChoiceTypeAuto), Tools: s.AllowedTools.Tools},
		}}
		return &cp
	}
	cp.ToolChoice = &schemas.ChatToolChoice{ChatToolChoiceStr: new(string(schemas.ChatToolChoiceTypeAuto))}
	return &cp
}

// relaxForcedResponsesChoice is the Responses API parallel of relaxForcedChatChoice.
func relaxForcedResponsesChoice(params *schemas.ResponsesParameters) *schemas.ResponsesParameters {
	if params == nil || !params.ToolChoice.IsForced() {
		return params
	}
	cp := *params
	// An allowed_tools set is the client's limit on which tools may run; only its mode relaxes.
	if s := params.ToolChoice.ResponsesToolChoiceStruct; s != nil && s.Type == schemas.ResponsesToolChoiceTypeAllowedTools {
		relaxed := *s
		relaxed.Mode = new(string(schemas.ResponsesToolChoiceTypeAuto))
		cp.ToolChoice = &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &relaxed}
		return &cp
	}
	cp.ToolChoice = &schemas.ResponsesToolChoice{ResponsesToolChoiceStr: new(string(schemas.ResponsesToolChoiceTypeAuto))}
	return &cp
}

// billEarlierTurns adds the usage of turns that already finished to an error that ends
// the request. The provider bills only the turn it was serving, but every earlier turn
// was consumed too, and governance billing and logging read BilledUsage off the error.
func billEarlierTurns(bifrostErr *schemas.BifrostError, earlier *schemas.BifrostLLMUsage) {
	if bifrostErr == nil || earlier == nil {
		return
	}
	bifrostErr.ExtraFields.BilledUsage = schemas.MergeBifrostLLMUsage(earlier, bifrostErr.ExtraFields.BilledUsage)
}

func chatMessageText(msg *schemas.ChatMessage) string {
	if msg == nil || msg.Content == nil {
		return ""
	}
	if msg.Content.ContentStr != nil {
		return *msg.Content.ContentStr
	}
	var parts []string
	for _, block := range msg.Content.ContentBlocks {
		if block.Text != nil && *block.Text != "" {
			parts = append(parts, *block.Text)
		}
	}
	return strings.Join(parts, "\n\n")
}

// prependChatText puts earlier turns' text in front of the final answer, so text the
// model wrote before a search ("Let me look that up") is not lost to the client.
func prependChatText(resp *schemas.BifrostChatResponse, prior []string) {
	msg := firstChatMessage(resp)
	if msg == nil || len(prior) == 0 {
		return
	}
	text := strings.Join(prior, "\n\n")
	merged := *msg
	if msg.Content != nil && msg.Content.ContentStr == nil && len(msg.Content.ContentBlocks) > 0 {
		// A block answer keeps its blocks and their details; earlier text leads it.
		blocks := make([]schemas.ChatContentBlock, 0, len(msg.Content.ContentBlocks)+1)
		blocks = append(blocks, schemas.ChatContentBlock{Type: schemas.ChatContentBlockTypeText, Text: &text})
		blocks = append(blocks, msg.Content.ContentBlocks...)
		merged.Content = &schemas.ChatMessageContent{ContentBlocks: blocks}
	} else {
		if final := chatMessageText(msg); final != "" {
			text += "\n\n" + final
		}
		merged.Content = &schemas.ChatMessageContent{ContentStr: &text}
	}
	resp.Choices[0].Message = &merged
}

// injectedToolsForAttempt resolves the provider's injected tools for one attempt.
func (bifrost *Bifrost) injectedToolsForAttempt(ctx *schemas.BifrostContext, config *schemas.ProviderConfig, requestType schemas.RequestType) *injectedToolSet {
	if bifrost.MCPManager == nil {
		return nil
	}
	return resolveInjectedTools(bifrost.MCPManager, config, requestType, bifrost.logger)
}

// runInjectedTools serves one non-streaming attempt whose provider injects tools. Every
// turn goes to the same provider, model and key; plugins see only the final response.
func (bifrost *Bifrost) runInjectedTools(provider schemas.Provider, config *schemas.ProviderConfig, req *ChannelMessage, key schemas.Key, set *injectedToolSet) (*schemas.BifrostResponse, *schemas.BifrostError) {
	ctx := req.Context
	exec := func(call schemas.ChatAssistantMessageToolCall) *schemas.ChatMessage {
		return bifrost.executeInjectedCall(ctx, set, call)
	}
	switch req.RequestType {
	case schemas.ChatCompletionRequest:
		original := req.BifrostRequest.ChatRequest
		resp, bifrostErr := runInjectedChatLoop(original, set, func(turn *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
			return bifrost.dispatchChat(ctx, provider, config, key, turn, original)
		}, exec)
		if bifrostErr != nil {
			return nil, bifrostErr
		}
		return &schemas.BifrostResponse{ChatResponse: resp}, nil
	case schemas.ResponsesRequest:
		original := req.BifrostRequest.ResponsesRequest
		resp, bifrostErr := runInjectedResponsesLoop(original, set, func(turn *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError) {
			return bifrost.dispatchResponses(ctx, provider, config, key, turn, original)
		}, exec)
		if bifrostErr != nil {
			return nil, bifrostErr
		}
		return &schemas.BifrostResponse{ResponsesResponse: resp}, nil
	}
	return bifrost.handleProviderRequest(provider, config, req, key, nil)
}

// executeInjectedCall runs one injected tool call through the MCP plugin pipeline.
//
// The call runs in its own context marked with the tool it is allowed to run, so the
// client's tools_to_execute, the caller's include lists and the virtual key's tool
// permits do not refuse a tool the operator configured. A failure goes back to the
// model as an error result rather than failing the request: the model can answer
// without search, and a search outage must not become an outage of the provider.
func (bifrost *Bifrost) executeInjectedCall(ctx *schemas.BifrostContext, set *injectedToolSet, call schemas.ChatAssistantMessageToolCall) *schemas.ChatMessage {
	// From here the attempt has side effects a retry would repeat, and its response is
	// assembled from several turns (see BifrostContextKeyInjectedToolsExecuted).
	ctx.SetValue(schemas.BifrostContextKeyInjectedToolsExecuted, true)
	toolCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	toolCtx.SetValue(schemas.BifrostContextKeyMCPLogID, uuid.New().String())
	call, target := mcpInjectedCall(set, call)
	injectedauth.Set(toolCtx, target.clientName, target.toolName)
	result, bifrostErr := bifrost.MCPManager.ExecuteChatTool(toolCtx, &call)
	if bifrostErr == nil && result != nil {
		return result
	}
	message := "tool execution failed"
	if bifrostErr != nil {
		message = bifrostErr.GetErrorString()
	}
	return &schemas.ChatMessage{
		Role:            schemas.ChatMessageRoleTool,
		Content:         &schemas.ChatMessageContent{ContentStr: &message},
		ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: call.ID, IsError: new(true)},
	}
}

// mcpInjectedCall returns call renamed from the model-facing name to the prefixed MCP
// tool name the MCP manager executes, and the tool it names. The model only ever sees
// the configured name; plugins, governance and MCP logs see the real tool. call is a
// copy, so the caller's call keeps the model-facing name.
func mcpInjectedCall(set *injectedToolSet, call schemas.ChatAssistantMessageToolCall) (schemas.ChatAssistantMessageToolCall, injectedTarget) {
	if set == nil || call.Function.Name == nil {
		return call, injectedTarget{}
	}
	target, ok := set.targets[*call.Function.Name]
	if !ok {
		return call, injectedTarget{}
	}
	call.Function.Name = new(target.toolName)
	return call, target
}

// clearAnthropicPassthroughForInjectedTools moves an attempt with injected tools off
// Anthropic raw-body passthrough, the path Claude Code takes to Anthropic models, and
// returns the tool set the attempt should use.
//
// Passthrough forwards the caller's bytes, so the request rewrite (native web search
// out, MCP tool in) and the appended turns would never reach the wire, and the raw
// upstream events streamed back would show the client the injected tool_use blocks.
// The typed path is the one every non-Anthropic provider already serves Claude Code
// through. It runs before applyRawCaptureSignals for the same reason as
// clearAnthropicPassthroughForNonNativeProvider.
//
// A request whose conversation exists only in the raw body (a direct SDK call with an
// empty typed input) has nothing for the typed path to send. It keeps passthrough and
// goes out without the injected tool: injection fails open, as it does when the tool
// cannot be resolved.
//
// The passthrough settings are saved first, so a later attempt without injected tools
// (a fallback) gets them back from restoreAnthropicPassthroughAfterInjectedTools.
func clearAnthropicPassthroughForInjectedTools(ctx *schemas.BifrostContext, set *injectedToolSet, req *schemas.BifrostRequest) *injectedToolSet {
	if set == nil {
		return nil
	}
	if useRaw, _ := ctx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool); !useRaw {
		return set
	}
	if !hasTypedInput(req) {
		return nil
	}
	if _, saved := ctx.Value(injectedToolsPassthroughKey).(*passthroughSnapshot); !saved {
		snapshot := &passthroughSnapshot{values: make(map[schemas.BifrostContextKey]any, len(passthroughKeys))}
		for _, key := range passthroughKeys {
			snapshot.values[key] = ctx.Value(key)
		}
		ctx.SetValue(injectedToolsPassthroughKey, snapshot)
	}
	disableAnthropicPassthrough(ctx)
	return set
}

// restoreAnthropicPassthroughAfterInjectedTools puts back the passthrough settings an
// earlier attempt with injected tools switched off. Each attempt calls it before
// deciding passthrough afresh, so an attempt's own checks (a non-Anthropic provider,
// unsupported structured output, its own injected tools) still switch it off again.
func restoreAnthropicPassthroughAfterInjectedTools(ctx *schemas.BifrostContext) {
	snapshot, ok := ctx.Value(injectedToolsPassthroughKey).(*passthroughSnapshot)
	if !ok {
		return
	}
	for key, value := range snapshot.values {
		if value == nil {
			ctx.ClearValue(key)
		} else {
			ctx.SetValue(key, value)
		}
	}
	ctx.ClearValue(injectedToolsPassthroughKey)
}

// injectedToolsContextKey keys core-internal state for injected tools on the context.
type injectedToolsContextKey string

const injectedToolsPassthroughKey injectedToolsContextKey = "injected-tools-passthrough-snapshot"

// passthroughKeys are the settings disableAnthropicPassthrough changes.
var passthroughKeys = []schemas.BifrostContextKey{
	schemas.BifrostContextKeyUseRawRequestBody,
	schemas.BifrostContextKeyRawRequestBodyTextRewriter,
	schemas.BifrostContextKeyRawStreamTextCodec,
	schemas.BifrostContextKeySendBackRawResponse,
	schemas.BifrostContextKeyPassthroughOverridesPresent,
	schemas.BifrostContextKeyURLPath,
}

// passthroughSnapshot holds the passthrough settings as ingress set them. Small handles
// only: flags, a path and codec pointers.
type passthroughSnapshot struct {
	values map[schemas.BifrostContextKey]any
}

func hasTypedInput(req *schemas.BifrostRequest) bool {
	switch {
	case req == nil:
		return false
	case req.ResponsesRequest != nil:
		return len(req.ResponsesRequest.Input) > 0
	case req.ChatRequest != nil:
		return len(req.ChatRequest.Input) > 0
	}
	return false
}
