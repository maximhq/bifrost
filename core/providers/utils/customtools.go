package utils

import (
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

// RewriteChatCustomTools returns a copy of r with custom tools as function tools, or r unchanged.
func RewriteChatCustomTools(ctx *schemas.BifrostContext, provider schemas.ModelProvider, r *schemas.BifrostChatRequest) *schemas.BifrostChatRequest {
	if r == nil || r.Params == nil || !slices.ContainsFunc(r.Params.Tools, isChatCustomTool) {
		return r
	}
	if schemas.ResolveModelCaps(schemas.ResolveBaseProvider(ctx, provider), schemas.ResolveCanonicalModel(ctx, r.Model)).SupportsCustomTools(false) {
		return r
	}
	params := *r.Params
	params.Tools = make([]schemas.ChatTool, len(r.Params.Tools))
	for i, tool := range r.Params.Tools {
		if isChatCustomTool(tool) {
			tool = chatCustomToolAsFunction(tool)
		}
		params.Tools[i] = tool
	}
	params.ToolChoice = chatCustomToolChoiceAsFunction(params.ToolChoice)
	cp := *r
	cp.Params = &params
	return &cp
}

func isChatCustomTool(tool schemas.ChatTool) bool {
	return tool.Type == schemas.ChatToolTypeCustom
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
	if choice == nil || choice.ChatToolChoiceStruct == nil ||
		choice.ChatToolChoiceStruct.Type != schemas.ChatToolChoiceTypeCustom {
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
	return found && !schemas.ResolveModelCaps(schemas.ResolveBaseProvider(ctx, provider), schemas.ResolveCanonicalModel(ctx, r.Model)).SupportsCustomTools(false)
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
	if schemas.ResolveModelCaps(schemas.ResolveBaseProvider(ctx, provider), schemas.ResolveCanonicalModel(ctx, r.Model)).SupportsCustomTools(false) {
		return r
	}
	cp := *r
	if hasTools {
		params := *r.Params
		names := make(map[string]struct{})
		collectCustomToolNames(params.Tools, "", names)
		params.Tools = responsesCustomToolsAsFunctions(params.Tools)
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

func responsesCustomToolsAsFunctions(tools []schemas.ResponsesTool) []schemas.ResponsesTool {
	if !containsResponsesCustomTool(tools) {
		return tools
	}
	rewritten := make([]schemas.ResponsesTool, len(tools))
	for i, tool := range tools {
		switch {
		case tool.Type == schemas.ResponsesToolTypeCustom:
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
			namespaceCopy.Tools = responsesCustomToolsAsFunctions(tool.ResponsesToolNamespace.Tools)
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

func collectCustomToolNames(tools []schemas.ResponsesTool, namespace string, names map[string]struct{}) {
	for _, tool := range tools {
		if tool.Type == schemas.ResponsesToolTypeCustom && tool.Name != nil {
			names[customToolKey(namespace, *tool.Name)] = struct{}{}
		}
		if tool.ResponsesToolNamespace != nil && tool.Name != nil {
			collectCustomToolNames(tool.ResponsesToolNamespace.Tools, *tool.Name, names)
		}
	}
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

// RestoreResponsesCustomToolCalls turns function calls of rewritten custom tools back into custom_tool_call items.
func RestoreResponsesCustomToolCalls(names map[string]struct{}, resp *schemas.BifrostResponsesResponse) {
	if resp == nil || len(names) == 0 {
		return
	}
	for i := range resp.Output {
		restoreCustomToolCall(&resp.Output[i], names)
	}
}

// CustomToolNamesFromTools rebuilds the rewritten custom tool names from a stored response's tools.
func CustomToolNamesFromTools(tools []schemas.ResponsesTool) map[string]struct{} {
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

// RewriteCompactionCustomCalls returns a copy of r with replayed custom calls as function calls, and their call IDs.
func RewriteCompactionCustomCalls(ctx *schemas.BifrostContext, provider schemas.ModelProvider, r *schemas.BifrostCompactionRequest) (*schemas.BifrostCompactionRequest, map[string]struct{}) {
	if r == nil || !slices.ContainsFunc(r.Input, isCustomCallItem) {
		return r, nil
	}
	if schemas.ResolveModelCaps(schemas.ResolveBaseProvider(ctx, provider), schemas.ResolveCanonicalModel(ctx, r.Model)).SupportsCustomTools(false) {
		return r, nil
	}
	callIDs := make(map[string]struct{})
	for _, message := range r.Input {
		if isCustomCallItem(message) && message.CallID != nil {
			callIDs[*message.CallID] = struct{}{}
		}
	}
	cp := *r
	cp.Input = responsesCustomCallItemsAsFunctions(slices.Clone(r.Input))
	return &cp, callIDs
}

// RestoreCompactionCustomToolCalls turns the converted calls kept by compaction, and their outputs, back into custom items.
func RestoreCompactionCustomToolCalls(callIDs map[string]struct{}, resp *schemas.BifrostCompactionResponse) {
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

// WrapCustomToolRestorePostHookRunner restores custom tool call events on each Responses stream chunk.
func WrapCustomToolRestorePostHookRunner(runner schemas.PostHookRunner, names map[string]struct{}) schemas.PostHookRunner {
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
		r.names = CustomToolNamesFromTools(event.Response.Tools)
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
			RestoreResponsesCustomToolCalls(r.names, event.Response)
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
