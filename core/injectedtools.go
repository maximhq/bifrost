package bifrost

import (
	"strings"
	"sync"

	"github.com/google/uuid"
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
// "auto". Params are copied; the caller's are never written.
func relaxForcedChatChoice(params *schemas.ChatParameters) *schemas.ChatParameters {
	if params == nil || !params.ToolChoice.IsForced() {
		return params
	}
	cp := *params
	cp.ToolChoice = &schemas.ChatToolChoice{ChatToolChoiceStr: new(string(schemas.ChatToolChoiceTypeAuto))}
	return &cp
}

// relaxForcedResponsesChoice is the Responses API parallel of relaxForcedChatChoice.
func relaxForcedResponsesChoice(params *schemas.ResponsesParameters) *schemas.ResponsesParameters {
	if params == nil || !params.ToolChoice.IsForced() {
		return params
	}
	cp := *params
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
	// A raw-body request goes to the provider byte for byte, so a rewrite of the parsed
	// request would never reach the wire. Send it as the caller wrote it.
	if useRaw, _ := ctx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool); useRaw {
		return nil
	}
	return resolveInjectedTools(bifrost.MCPManager, config, requestType, bifrost.logger)
}

// runInjectedTools serves one non-streaming attempt whose provider injects tools. Every
// turn goes to the same provider, model and key; plugins see only the final response.
func (bifrost *Bifrost) runInjectedTools(provider schemas.Provider, config *schemas.ProviderConfig, req *ChannelMessage, key schemas.Key, set *injectedToolSet) (*schemas.BifrostResponse, *schemas.BifrostError) {
	ctx := req.Context
	exec := func(call schemas.ChatAssistantMessageToolCall) *schemas.ChatMessage {
		return bifrost.executeInjectedCall(ctx, call)
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
func (bifrost *Bifrost) executeInjectedCall(ctx *schemas.BifrostContext, call schemas.ChatAssistantMessageToolCall) *schemas.ChatMessage {
	// From here the attempt has side effects a retry would repeat, and its response is
	// assembled from several turns (see BifrostContextKeyInjectedToolsExecuted).
	ctx.SetValue(schemas.BifrostContextKeyInjectedToolsExecuted, true)
	toolCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
	toolCtx.SetValue(schemas.BifrostContextKeyMCPLogID, uuid.New().String())
	// Client names never contain a hyphen, so the prefix before the first one is the
	// owning client, as everywhere else MCP tool names are split.
	clientName, _, _ := strings.Cut(*call.Function.Name, "-")
	toolCtx.SetValue(schemas.BifrostContextKeyInjectedToolExecution, schemas.InjectedToolAuthorization{ClientName: clientName, ToolName: *call.Function.Name})
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
