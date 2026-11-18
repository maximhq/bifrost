package utils

import (
	"reflect"
	"slices"
	"strings"
	"sync"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

const (
	customToolInputParam       = "input"
	customToolInputDescription = "Raw input for the tool, passed as a plain string."
)

// customToolParameters builds the function schema for a custom tool, with its grammar in the description.
func customToolParameters(syntax, definition string) *schemas.ToolFunctionParameters {
	description := customToolInputDescription
	if definition != "" {
		if syntax == "" {
			syntax = "grammar"
		}
		description += " It must match this " + syntax + " definition:\n" + definition
	}
	return &schemas.ToolFunctionParameters{
		Type: "object",
		Properties: schemas.NewOrderedMapFromPairs(
			schemas.KV(customToolInputParam, schemas.NewOrderedMapFromPairs(
				schemas.KV("type", "string"),
				schemas.KV("description", description),
			)),
		),
		Required:             []string{customToolInputParam},
		AdditionalProperties: &schemas.AdditionalPropertiesStruct{AdditionalPropertiesBool: schemas.Ptr(false)},
	}
}

// customToolsUnsupported reports whether the model needs custom tools sent as function tools.
func customToolsUnsupported(ctx *schemas.BifrostContext, provider schemas.ModelProvider, model string) bool {
	return !schemas.ResolveModelCaps(schemas.ResolveBaseProvider(ctx, provider), schemas.ResolveCanonicalModel(ctx, model)).SupportsCustomTools(false)
}

// wrapCustomToolInput encodes a custom tool input as function call arguments.
func wrapCustomToolInput(input string) string {
	encoded, err := MarshalSorted(map[string]string{customToolInputParam: input})
	if err != nil {
		encoded, _ = MarshalSorted(map[string]string{customToolInputParam: ""})
	}
	return string(encoded)
}

// unwrapCustomToolInput extracts the custom tool input, returning other arguments as-is.
func unwrapCustomToolInput(arguments string) string {
	field := GetJSONField([]byte(arguments), customToolInputParam)
	if !field.Exists() {
		return arguments
	}
	return field.String()
}

// RewriteChatCustomTools returns a copy of r with custom tools and replayed custom calls as
// functions, or r unchanged. The copy records the rewritten names for RestoreChatCustomToolCalls.
func RewriteChatCustomTools(ctx *schemas.BifrostContext, provider schemas.ModelProvider, r *schemas.BifrostChatRequest) *schemas.BifrostChatRequest {
	if r == nil {
		return r
	}
	hasTools := r.Params != nil && slices.ContainsFunc(r.Params.Tools, isChatCustomTool)
	hasCalls := slices.ContainsFunc(r.Input, hasChatCustomToolCall)
	if !hasTools && !hasCalls {
		return r
	}
	if !customToolsUnsupported(ctx, provider, r.Model) {
		return r
	}
	cp := *r
	if hasTools {
		params := *r.Params
		params.Tools = make([]schemas.ChatTool, len(r.Params.Tools))
		names := make(map[string]struct{})
		for i, tool := range r.Params.Tools {
			if isChatCustomTool(tool) {
				tool = chatCustomToolAsFunction(tool)
				names[tool.Function.Name] = struct{}{}
			}
			params.Tools[i] = tool
		}
		params.ToolChoice = chatCustomToolChoiceAsFunction(params.ToolChoice)
		cp.Params = &params
		cp.CustomToolNames = names
	}
	if hasCalls {
		cp.Input = slices.Clone(r.Input)
		for i := range cp.Input {
			if hasChatCustomToolCall(cp.Input[i]) {
				cp.Input[i] = chatCustomToolCallsAsFunctions(cp.Input[i])
			}
		}
	}
	return &cp
}

func hasChatCustomToolCall(message schemas.ChatMessage) bool {
	return message.ChatAssistantMessage != nil &&
		slices.ContainsFunc(message.ChatAssistantMessage.ToolCalls, func(tc schemas.ChatAssistantMessageToolCall) bool { return tc.Custom != nil })
}

// chatCustomToolCallsAsFunctions returns message with its custom calls as function calls, copying what it changes.
func chatCustomToolCallsAsFunctions(message schemas.ChatMessage) schemas.ChatMessage {
	assistant := *message.ChatAssistantMessage
	assistant.ToolCalls = slices.Clone(assistant.ToolCalls)
	for i, tc := range assistant.ToolCalls {
		if tc.Custom == nil {
			continue
		}
		tc.Type = schemas.Ptr(string(schemas.ChatToolTypeFunction))
		tc.Function = schemas.ChatAssistantMessageToolCallFunction{Name: tc.Custom.Name, Arguments: wrapCustomToolInput(tc.Custom.Input)}
		tc.Custom = nil
		assistant.ToolCalls[i] = tc
	}
	message.ChatAssistantMessage = &assistant
	return message
}

// isChatCustomTool reports whether tool is a named custom tool; a nameless one is left for the upstream to reject.
func isChatCustomTool(tool schemas.ChatTool) bool {
	return tool.Type == schemas.ChatToolTypeCustom && tool.Custom != nil && tool.Custom.Name != ""
}

func chatCustomToolAsFunction(tool schemas.ChatTool) schemas.ChatTool {
	var name, syntax, definition string
	var description *string
	if tool.Custom != nil {
		name = tool.Custom.Name
		description = tool.Custom.Description
		if tool.Custom.Format != nil && tool.Custom.Format.Grammar != nil {
			syntax = tool.Custom.Format.Grammar.Syntax
			definition = tool.Custom.Format.Grammar.Definition
		}
	}
	tool.Type = schemas.ChatToolTypeFunction
	tool.Function = &schemas.ChatToolFunction{
		Name:        name,
		Description: description,
		Parameters:  customToolParameters(syntax, definition),
	}
	tool.Custom = nil
	tool.Name = ""
	tool.InvalidateSerialized()
	return tool
}

func chatCustomToolChoiceAsFunction(choice *schemas.ChatToolChoice) *schemas.ChatToolChoice {
	if choice == nil || choice.ChatToolChoiceStruct == nil {
		return choice
	}
	if choice.ChatToolChoiceStruct.Type == schemas.ChatToolChoiceTypeAllowedTools {
		return chatCustomAllowedToolsAsFunctions(choice)
	}
	if choice.ChatToolChoiceStruct.Type != schemas.ChatToolChoiceTypeCustom {
		return choice
	}
	name := ""
	if choice.ChatToolChoiceStruct.Custom != nil {
		name = choice.ChatToolChoiceStruct.Custom.Name
	}
	return &schemas.ChatToolChoice{ChatToolChoiceStruct: &schemas.ChatToolChoiceStruct{
		Type:     schemas.ChatToolChoiceTypeFunction,
		Function: &schemas.ChatToolChoiceFunction{Name: name},
	}}
}

// chatCustomAllowedToolsAsFunctions returns choice with its custom allowed_tools entries as function entries.
func chatCustomAllowedToolsAsFunctions(choice *schemas.ChatToolChoice) *schemas.ChatToolChoice {
	allowed := choice.ChatToolChoiceStruct.AllowedTools
	if allowed == nil {
		return choice
	}
	var tools []schemas.ChatToolChoiceAllowedToolsTool
	for i, tool := range allowed.Tools {
		if tool.Type != string(schemas.ChatToolChoiceTypeCustom) {
			continue
		}
		if tools == nil {
			tools = slices.Clone(allowed.Tools)
		}
		name := ""
		if tool.Custom != nil {
			name = tool.Custom.Name
		}
		tools[i] = schemas.ChatToolChoiceAllowedToolsTool{Type: string(schemas.ChatToolChoiceTypeFunction), Function: schemas.ChatToolChoiceFunction{Name: name}}
	}
	if tools == nil {
		return choice
	}
	allowedCopy := *allowed
	allowedCopy.Tools = tools
	structCopy := *choice.ChatToolChoiceStruct
	structCopy.AllowedTools = &allowedCopy
	return &schemas.ChatToolChoice{ChatToolChoiceStruct: &structCopy}
}

// ResponsesAdditionalToolsNeedHoist reports whether additional_tools may hold a custom tool to rewrite.
func ResponsesAdditionalToolsNeedHoist(ctx *schemas.BifrostContext, provider schemas.ModelProvider, r *schemas.BifrostResponsesRequest) bool {
	if r == nil {
		return false
	}
	found := false
	for _, message := range r.Input {
		if message.Type != nil && *message.Type == schemas.ResponsesMessageTypeAdditionalTools &&
			rawToolsHaveCustom(gjson.ParseBytes(message.AdditionalTools)) {
			found = true
			break
		}
	}
	return found && customToolsUnsupported(ctx, provider, r.Model)
}

// rawToolsHaveCustom reports whether a raw tools array declares a custom tool, namespace members included.
func rawToolsHaveCustom(tools gjson.Result) bool {
	found := false
	tools.ForEach(func(_, tool gjson.Result) bool {
		switch tool.Get("type").String() {
		case string(schemas.ResponsesToolTypeCustom):
			found = true
		case string(schemas.ResponsesToolTypeNamespace):
			found = rawToolsHaveCustom(tool.Get("tools"))
		}
		return !found
	})
	return found
}

// RewriteResponsesCustomTools returns a copy of r with custom tools and calls as functions, or r unchanged.
func RewriteResponsesCustomTools(ctx *schemas.BifrostContext, provider schemas.ModelProvider, r *schemas.BifrostResponsesRequest) *schemas.BifrostResponsesRequest {
	if r == nil {
		return r
	}
	hasTools := r.Params != nil && containsResponsesCustomTool(r.Params.Tools)
	hasItems := slices.ContainsFunc(r.Input, isCustomCallItem)
	if !hasTools && !hasItems {
		return r
	}
	if !customToolsUnsupported(ctx, provider, r.Model) {
		return r
	}
	cp := *r
	if hasTools {
		params := *r.Params
		names := make(map[string]struct{})
		params.Tools = responsesCustomToolsAsFunctions(params.Tools, "", names)
		params.ToolChoice = responsesCustomToolChoiceAsFunction(params.ToolChoice)
		cp.Params = &params
		cp.CustomToolNames = names
	}
	if hasItems {
		cp.Input = responsesCustomCallItemsAsFunctions(slices.Clone(r.Input))
	}
	return &cp
}

func isCustomCallItem(message schemas.ResponsesMessage) bool {
	return message.Type != nil && message.ResponsesToolMessage != nil &&
		(*message.Type == schemas.ResponsesMessageTypeCustomToolCall || *message.Type == schemas.ResponsesMessageTypeCustomToolCallOutput)
}

// responsesCustomToolsAsFunctions returns tools with custom tools as functions and records their names.
func responsesCustomToolsAsFunctions(tools []schemas.ResponsesTool, namespace string, names map[string]struct{}) []schemas.ResponsesTool {
	if !containsResponsesCustomTool(tools) {
		return tools
	}
	rewritten := make([]schemas.ResponsesTool, len(tools))
	for i, tool := range tools {
		switch {
		case tool.Type == schemas.ResponsesToolTypeCustom:
			if tool.Name != nil && names != nil {
				names[customToolKey(namespace, *tool.Name)] = struct{}{}
			}
			var syntax, definition string
			if tool.ResponsesToolCustom != nil && tool.ResponsesToolCustom.Format != nil {
				if f := tool.ResponsesToolCustom.Format; f.Definition != nil {
					definition = *f.Definition
					if f.Syntax != nil {
						syntax = *f.Syntax
					}
				}
			}
			tool.Type = schemas.ResponsesToolTypeFunction
			tool.ResponsesToolCustom = nil
			tool.ResponsesToolFunction = &schemas.ResponsesToolFunction{
				Parameters: customToolParameters(syntax, definition),
				Strict:     schemas.Ptr(false),
			}
		case tool.ResponsesToolNamespace != nil && containsResponsesCustomTool(tool.ResponsesToolNamespace.Tools):
			namespaceCopy := *tool.ResponsesToolNamespace
			var nested map[string]struct{} // an unnamed namespace records nothing
			nestedName := ""
			if tool.Name != nil {
				nested, nestedName = names, *tool.Name
			}
			namespaceCopy.Tools = responsesCustomToolsAsFunctions(tool.ResponsesToolNamespace.Tools, nestedName, nested)
			tool.ResponsesToolNamespace = &namespaceCopy
		}
		rewritten[i] = tool
	}
	return rewritten
}

func containsResponsesCustomTool(tools []schemas.ResponsesTool) bool {
	for _, tool := range tools {
		if tool.Type == schemas.ResponsesToolTypeCustom {
			return true
		}
		if tool.ResponsesToolNamespace != nil && containsResponsesCustomTool(tool.ResponsesToolNamespace.Tools) {
			return true
		}
	}
	return false
}

// customToolKey keys a tool by namespace and name; "functions" is the default namespace.
func customToolKey(namespace, name string) string {
	if namespace == "" || namespace == "functions" {
		return name
	}
	return namespace + "\x00" + name
}

func responsesCustomToolChoiceAsFunction(choice *schemas.ResponsesToolChoice) *schemas.ResponsesToolChoice {
	if choice == nil || choice.ResponsesToolChoiceStruct == nil {
		return choice
	}
	s := choice.ResponsesToolChoiceStruct
	if s.Type == schemas.ResponsesToolChoiceTypeCustom {
		structCopy := *s
		structCopy.Type = schemas.ResponsesToolChoiceTypeFunction
		return &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &structCopy}
	}
	if s.Type == schemas.ResponsesToolChoiceTypeAllowedTools {
		var tools []schemas.ResponsesToolChoiceAllowedToolDef
		for i, tool := range s.Tools {
			if tool.Type != string(schemas.ResponsesToolTypeCustom) {
				continue
			}
			if tools == nil {
				tools = slices.Clone(s.Tools)
			}
			tools[i].Type = string(schemas.ResponsesToolTypeFunction)
		}
		if tools != nil {
			structCopy := *s
			structCopy.Tools = tools
			return &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &structCopy}
		}
	}
	return choice
}

func responsesCustomCallItemsAsFunctions(messages []schemas.ResponsesMessage) []schemas.ResponsesMessage {
	for i := range messages {
		message := &messages[i]
		if !isCustomCallItem(*message) {
			continue
		}
		if *message.Type == schemas.ResponsesMessageTypeCustomToolCall {
			toolMessage := *message.ResponsesToolMessage
			input := ""
			if toolMessage.ResponsesCustomToolCall != nil {
				input = toolMessage.ResponsesCustomToolCall.Input
			}
			toolMessage.ResponsesCustomToolCall = nil
			toolMessage.Arguments = schemas.Ptr(wrapCustomToolInput(input))
			message.ResponsesToolMessage = &toolMessage
			message.Type = schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall)
		} else {
			message.Type = schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput)
		}
		// OpenAI rejects function_call ids not starting with "fc"; call_id still pairs the output.
		if message.ID != nil && !strings.HasPrefix(*message.ID, "fc") {
			message.ID = nil
		}
	}
	return messages
}

func restoreCustomToolCall(item *schemas.ResponsesMessage, names map[string]struct{}) bool {
	if item == nil || item.Type == nil || *item.Type != schemas.ResponsesMessageTypeFunctionCall ||
		item.ResponsesToolMessage == nil || item.ResponsesToolMessage.Name == nil {
		return false
	}
	namespace := ""
	if item.ResponsesToolMessage.Namespace != nil {
		namespace = *item.ResponsesToolMessage.Namespace
	}
	if _, ok := names[customToolKey(namespace, *item.ResponsesToolMessage.Name)]; !ok {
		return false
	}
	functionCallAsCustomCall(item)
	return true
}

func functionCallAsCustomCall(item *schemas.ResponsesMessage) {
	toolMessage := *item.ResponsesToolMessage
	input := ""
	if toolMessage.Arguments != nil {
		input = unwrapCustomToolInput(*toolMessage.Arguments)
	}
	toolMessage.Arguments = nil
	toolMessage.ResponsesCustomToolCall = &schemas.ResponsesCustomToolCall{Input: input}
	item.ResponsesToolMessage = &toolMessage
	item.Type = schemas.Ptr(schemas.ResponsesMessageTypeCustomToolCall)
}

// RestoreResponsesToolCalls restores namespaced tool names, then custom tool calls.
func RestoreResponsesToolCalls(prepared *schemas.BifrostResponsesRequest, resp *schemas.BifrostResponsesResponse) {
	if prepared == nil || resp == nil {
		return
	}
	if len(prepared.NamespaceToolAliases) > 0 {
		restoreNamespacedItems(resp.Output, prepared.NamespaceToolAliases)
	}
	restoreResponsesCustomToolCalls(prepared.CustomToolNames, resp)
}

// WrapResponsesToolRestorePostHookRunner does the same on every stream chunk.
func WrapResponsesToolRestorePostHookRunner(runner schemas.PostHookRunner, prepared *schemas.BifrostResponsesRequest) schemas.PostHookRunner {
	if prepared == nil {
		return runner
	}
	runner = wrapCustomToolRestorePostHookRunner(runner, prepared.CustomToolNames)
	return wrapNamespaceRestorePostHookRunner(runner, prepared.NamespaceToolAliases)
}

// restoreResponsesCustomToolCalls turns function calls of rewritten custom tools back into custom_tool_call items.
func restoreResponsesCustomToolCalls(names map[string]struct{}, resp *schemas.BifrostResponsesResponse) {
	if resp == nil || len(names) == 0 {
		return
	}
	for i := range resp.Output {
		restoreCustomToolCall(&resp.Output[i], names)
	}
}

// RestoreRetrievedCustomToolCalls restores custom tool calls on a stored response, learning the names from its tools.
func RestoreRetrievedCustomToolCalls(resp *schemas.BifrostResponsesResponse) {
	if resp == nil {
		return
	}
	restoreResponsesCustomToolCalls(customToolNamesFromTools(resp.Tools), resp)
}

// customToolNamesFromTools rebuilds the rewritten custom tool names from a stored response's tools.
func customToolNamesFromTools(tools []schemas.ResponsesTool) map[string]struct{} {
	var names map[string]struct{}
	collectRewrittenCustomToolNames(tools, "", &names)
	return names
}

func collectRewrittenCustomToolNames(tools []schemas.ResponsesTool, namespace string, names *map[string]struct{}) {
	for _, tool := range tools {
		if tool.ResponsesToolNamespace != nil && tool.Name != nil {
			collectRewrittenCustomToolNames(tool.ResponsesToolNamespace.Tools, *tool.Name, names)
			continue
		}
		if tool.Type != schemas.ResponsesToolTypeFunction || tool.Name == nil || tool.ResponsesToolFunction == nil ||
			!isCustomToolParameters(tool.ResponsesToolFunction.Parameters) {
			continue
		}
		if *names == nil {
			*names = make(map[string]struct{})
		}
		(*names)[customToolKey(namespace, *tool.Name)] = struct{}{}
	}
}

// isCustomToolParameters reports whether params is the schema customToolParameters builds.
func isCustomToolParameters(params *schemas.ToolFunctionParameters) bool {
	if params == nil || params.Properties == nil || params.Properties.Len() != 1 ||
		len(params.Required) != 1 || params.Required[0] != customToolInputParam {
		return false
	}
	value, _ := params.Properties.Get(customToolInputParam)
	input, ok := value.(*schemas.OrderedMap)
	if !ok {
		return false
	}
	description, _ := input.Get("description")
	text, _ := description.(string)
	return strings.HasPrefix(text, customToolInputDescription)
}

// RewriteCompactionCustomCalls returns r with replayed custom calls as function calls, and the func that restores the response.
func RewriteCompactionCustomCalls(ctx *schemas.BifrostContext, provider schemas.ModelProvider, r *schemas.BifrostCompactionRequest) (*schemas.BifrostCompactionRequest, func(*schemas.BifrostCompactionResponse)) {
	if r == nil || !slices.ContainsFunc(r.Input, isCustomCallItem) {
		return r, noCompactionRestore
	}
	if !customToolsUnsupported(ctx, provider, r.Model) {
		return r, noCompactionRestore
	}
	callIDs := make(map[string]struct{})
	for _, message := range r.Input {
		if isCustomCallItem(message) && message.CallID != nil {
			callIDs[*message.CallID] = struct{}{}
		}
	}
	cp := *r
	cp.Input = responsesCustomCallItemsAsFunctions(slices.Clone(r.Input))
	return &cp, func(resp *schemas.BifrostCompactionResponse) { restoreCompactionCustomToolCalls(callIDs, resp) }
}

func noCompactionRestore(*schemas.BifrostCompactionResponse) {}

// restoreCompactionCustomToolCalls turns the converted calls kept by compaction, and their outputs, back into custom items.
func restoreCompactionCustomToolCalls(callIDs map[string]struct{}, resp *schemas.BifrostCompactionResponse) {
	if resp == nil || len(callIDs) == 0 {
		return
	}
	for i := range resp.Output {
		item := &resp.Output[i]
		if item.Type == nil || item.ResponsesToolMessage == nil || item.CallID == nil {
			continue
		}
		if _, ok := callIDs[*item.CallID]; !ok {
			continue
		}
		switch *item.Type {
		case schemas.ResponsesMessageTypeFunctionCall:
			functionCallAsCustomCall(item)
		case schemas.ResponsesMessageTypeFunctionCallOutput:
			item.Type = schemas.Ptr(schemas.ResponsesMessageTypeCustomToolCallOutput)
		}
	}
}

// wrapCustomToolRestorePostHookRunner restores custom tool call events on each Responses stream chunk.
func wrapCustomToolRestorePostHookRunner(runner schemas.PostHookRunner, names map[string]struct{}) schemas.PostHookRunner {
	if len(names) == 0 || runner == nil {
		return runner
	}
	return wrapCustomToolRestorer(runner, &customToolStreamRestorer{names: names, calls: map[string]struct{}{}})
}

// WrapRetrievedCustomToolRestorePostHookRunner restores custom tool calls on a replayed stored
// response, learning the rewritten names from the first event that carries the response.
func WrapRetrievedCustomToolRestorePostHookRunner(runner schemas.PostHookRunner) schemas.PostHookRunner {
	if runner == nil {
		return runner
	}
	return wrapCustomToolRestorer(runner, &customToolStreamRestorer{learn: true, calls: map[string]struct{}{}})
}

func wrapCustomToolRestorer(runner schemas.PostHookRunner, restorer *customToolStreamRestorer) schemas.PostHookRunner {
	return func(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		if result != nil && result.ResponsesStreamResponse != nil && !restorer.restore(result.ResponsesStreamResponse) {
			return result, &schemas.BifrostError{StreamControl: &schemas.StreamControl{SkipStream: schemas.Ptr(true)}}
		}
		return runner(ctx, result, bifrostErr)
	}
}

type customToolStreamRestorer struct {
	mu    sync.Mutex
	names map[string]struct{}
	calls map[string]struct{} // restored item ids
	learn bool                // names come from the first event carrying the response
}

// restore rewrites an event in place and reports whether to send it; argument deltas are dropped.
func (r *customToolStreamRestorer) restore(event *schemas.BifrostResponsesStreamResponse) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.learn && event.Response != nil {
		r.learn = false
		r.names = customToolNamesFromTools(event.Response.Tools)
	}
	if len(r.names) == 0 {
		return true
	}
	switch event.Type {
	case schemas.ResponsesStreamResponseTypeOutputItemAdded, schemas.ResponsesStreamResponseTypeOutputItemDone:
		if restoreCustomToolCall(event.Item, r.names) && event.Item.ID != nil {
			r.calls[*event.Item.ID] = struct{}{}
		}
	case schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta:
		return !r.tracked(event.ItemID)
	case schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDone:
		if !r.tracked(event.ItemID) {
			return true
		}
		input := ""
		if event.Arguments != nil {
			input = unwrapCustomToolInput(*event.Arguments)
		}
		event.Type = schemas.ResponsesStreamResponseTypeCustomToolCallInputDone
		event.Arguments = nil
		event.Input = &input
	case schemas.ResponsesStreamResponseTypeCompleted, schemas.ResponsesStreamResponseTypeIncomplete:
		if event.Response != nil {
			restoreResponsesCustomToolCalls(r.names, event.Response)
		}
	}
	return true
}

func (r *customToolStreamRestorer) tracked(itemID *string) bool {
	if itemID == nil {
		return false
	}
	_, ok := r.calls[*itemID]
	return ok
}

// RestoreChatCustomToolCalls turns function calls of rewritten custom tools back into custom calls.
func RestoreChatCustomToolCalls(prepared *schemas.BifrostChatRequest, resp *schemas.BifrostChatResponse) {
	if prepared == nil || resp == nil || len(prepared.CustomToolNames) == 0 {
		return
	}
	names := prepared.CustomToolNames
	for _, choice := range resp.Choices {
		if choice.ChatNonStreamResponseChoice == nil || choice.Message == nil || choice.Message.ChatAssistantMessage == nil {
			continue
		}
		calls := choice.Message.ChatAssistantMessage.ToolCalls
		for i := range calls {
			if isRewrittenChatCall(calls[i], names) {
				calls[i] = functionCallAsChatCustomCall(calls[i], unwrapCustomToolInput(calls[i].Function.Arguments))
			}
		}
	}
}

func isRewrittenChatCall(tc schemas.ChatAssistantMessageToolCall, names map[string]struct{}) bool {
	if tc.Custom != nil || tc.Function.Name == nil {
		return false
	}
	_, ok := names[*tc.Function.Name]
	return ok
}

func functionCallAsChatCustomCall(tc schemas.ChatAssistantMessageToolCall, input string) schemas.ChatAssistantMessageToolCall {
	tc.Type = schemas.Ptr(string(schemas.ChatToolTypeCustom))
	tc.Custom = &schemas.ChatAssistantMessageToolCallCustom{Name: tc.Function.Name, Input: input}
	tc.Function = schemas.ChatAssistantMessageToolCallFunction{}
	return tc
}

// WrapChatCustomToolRestorePostHookRunner restores custom tool calls on each chat stream chunk.
// A restored call's argument fragments are held back, since only the whole arguments object
// unwraps to the input, which is sent with the chunk that carries the finish reason.
func WrapChatCustomToolRestorePostHookRunner(runner schemas.PostHookRunner, prepared *schemas.BifrostChatRequest) schemas.PostHookRunner {
	if prepared == nil || len(prepared.CustomToolNames) == 0 || runner == nil {
		return runner
	}
	restorer := &chatCustomToolStreamRestorer{names: prepared.CustomToolNames}
	return func(ctx *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		if result != nil && result.ChatResponse != nil && !restorer.restore(result.ChatResponse) {
			return result, &schemas.BifrostError{StreamControl: &schemas.StreamControl{SkipStream: schemas.Ptr(true)}}
		}
		return runner(ctx, result, bifrostErr)
	}
}

type chatCustomToolStreamRestorer struct {
	mu    sync.Mutex
	names map[string]struct{}
	calls map[chatStreamCallKey]*strings.Builder // arguments of restored calls, by choice and tool index
	order []chatStreamCallKey
}

type chatStreamCallKey struct {
	choice int
	index  uint16
}

// restore rewrites a chunk in place and reports whether to send it; a chunk emptied by held-back fragments is not sent.
func (r *chatCustomToolStreamRestorer) restore(chunk *schemas.BifrostChatResponse) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	dropped := false
	for i := range chunk.Choices {
		choice := &chunk.Choices[i]
		if choice.ChatStreamResponseChoice == nil {
			continue
		}
		if choice.Delta != nil && len(choice.Delta.ToolCalls) > 0 {
			kept := r.restoreDeltas(choice.Index, choice.Delta.ToolCalls)
			if len(kept) < len(choice.Delta.ToolCalls) {
				dropped = true
			}
			if len(kept) == 0 {
				kept = nil
			}
			choice.Delta.ToolCalls = kept
		}
		if choice.FinishReason != nil {
			if inputs := r.flush(choice.Index); len(inputs) > 0 {
				if choice.Delta == nil {
					choice.Delta = &schemas.ChatStreamResponseChoiceDelta{}
				}
				choice.Delta.ToolCalls = append(choice.Delta.ToolCalls, inputs...)
			}
		}
	}
	return !dropped || !isEmptyChatChunk(chunk)
}

// isEmptyChatChunk reports whether a chunk carries no usage and no choice with anything to send.
func isEmptyChatChunk(chunk *schemas.BifrostChatResponse) bool {
	if chunk.Usage != nil {
		return false
	}
	for _, choice := range chunk.Choices {
		if choice.FinishReason != nil || choice.LogProbs != nil {
			return false
		}
		if choice.ChatStreamResponseChoice != nil && choice.Delta != nil && !reflect.ValueOf(*choice.Delta).IsZero() {
			return false
		}
	}
	return true
}

// restoreDeltas turns a rewritten call's header into a custom delta and holds back its arguments.
func (r *chatCustomToolStreamRestorer) restoreDeltas(choice int, deltas []schemas.ChatAssistantMessageToolCall) []schemas.ChatAssistantMessageToolCall {
	kept := make([]schemas.ChatAssistantMessageToolCall, 0, len(deltas))
	for _, tc := range deltas {
		key := chatStreamCallKey{choice: choice, index: tc.Index}
		if args, tracked := r.calls[key]; tracked {
			args.WriteString(tc.Function.Arguments)
			continue
		}
		if isRewrittenChatCall(tc, r.names) {
			if r.calls == nil {
				r.calls = make(map[chatStreamCallKey]*strings.Builder)
			}
			args := &strings.Builder{}
			args.WriteString(tc.Function.Arguments)
			r.calls[key] = args
			r.order = append(r.order, key)
			tc = functionCallAsChatCustomCall(tc, "")
		}
		kept = append(kept, tc)
	}
	return kept
}

// flush returns one custom input delta per restored call of the choice.
func (r *chatCustomToolStreamRestorer) flush(choice int) []schemas.ChatAssistantMessageToolCall {
	var deltas []schemas.ChatAssistantMessageToolCall
	for _, key := range r.order {
		args, ok := r.calls[key]
		if key.choice != choice || !ok {
			continue
		}
		deltas = append(deltas, schemas.ChatAssistantMessageToolCall{
			Index:  key.index,
			Custom: &schemas.ChatAssistantMessageToolCallCustom{Input: unwrapCustomToolInput(args.String())},
		})
		delete(r.calls, key)
	}
	return deltas
}
