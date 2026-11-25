package gemini

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"
	"strings"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

// Tool retrieval (tool search) on the Interactions API. A request declares {"type":"tool_search"}
// and marks the function or mcp_server tools to withhold with defer_loading; the model sees only
// their names and short descriptions until it searches. Google runs the search inside the same
// call and reports it as a tool_search_call step and a tool_search_result step carrying the full
// declarations it loaded, each with a signature Google needs back when the history is replayed.
// They map onto the Responses tool_search_call and tool_search_output items OpenAI uses for its
// own hosted search (execution "server"), and the signature rides the item's encrypted_content.
//
// With execution "client" the caller runs the search: Google asks for it with an ordinary
// function_call to the tool_search tool's name, and the caller answers with a function_result
// listing the declarations to load. That call and answer are the Responses client tool_search_call
// and tool_search_output items.
//
// generateContent has no tool search, so a request carrying a server tool_search runs on the
// Interactions API (see ShouldUseGeminiInteractions). A client tool_search stays on generateContent
// as a plain function named after the search (see lowerGeminiClientToolSearch).
const (
	GeminiInteractionsStepToolSearchCall   = "tool_search_call"
	GeminiInteractionsStepToolSearchResult = "tool_search_result"

	geminiInteractionsToolSearch = "tool_search"

	// geminiDefaultClientToolSearchName names a client tool_search the caller sent without a name.
	// Google requires one, and it is the name the model's function_call carries.
	geminiDefaultClientToolSearchName = "tool_search"
)

// toolSearchExecutionServer marks a tool_search_call or tool_search_output the provider ran.
const toolSearchExecutionServer = "server"

func isGeminiToolSearchStep(stepType string) bool {
	return stepType == GeminiInteractionsStepToolSearchCall || stepType == GeminiInteractionsStepToolSearchResult
}

func isResponsesToolSearchItem(itemType schemas.ResponsesMessageType) bool {
	return itemType == schemas.ResponsesMessageTypeToolSearchCall || itemType == schemas.ResponsesMessageTypeToolSearchOutput
}

// supportsGeminiToolSearch reports whether model serves tool retrieval: Gemini 3.6 and later. It
// only decides whether Bifrost adds the tool_search a deferred tool needs; a tool_search the
// caller sends is forwarded for Google to accept or refuse.
func supportsGeminiToolSearch(model string) bool {
	model = strings.ToLower(model)
	idx := strings.Index(model, "gemini-")
	if idx == -1 {
		return false
	}
	rest := model[idx+len("gemini-"):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return false
	}
	major, err := strconv.Atoi(rest[:end])
	if err != nil {
		return false
	}
	if major != 3 {
		return major > 3
	}
	if end >= len(rest) || rest[end] != '.' {
		return false
	}
	minorEnd := end + 1
	for minorEnd < len(rest) && rest[minorEnd] >= '0' && rest[minorEnd] <= '9' {
		minorEnd++
	}
	minor, err := strconv.Atoi(rest[end+1 : minorEnd])
	return err == nil && minor >= 6
}

// hasGeminiServerToolSearch reports whether tools carry a tool_search Google runs itself. It
// does not allocate: ShouldUseGeminiInteractions calls it on every Gemini Responses request.
func hasGeminiServerToolSearch(tools []schemas.ResponsesTool) bool {
	for i := range tools {
		if tools[i].Type == schemas.ResponsesToolTypeToolSearch && !tools[i].IsClientToolSearch() {
			return true
		}
	}
	return false
}

// clientToolSearchDescription is the description a client tool_search goes upstream with: the
// caller's, or a default, since Google requires one.
func clientToolSearchDescription(tool *schemas.ResponsesTool) *string {
	if tool.Description != nil && *tool.Description != "" {
		return tool.Description
	}
	return schemas.Ptr(geminiClientToolSearchDescription)
}

// toGeminiInteractionsToolSearchTool renders a Responses tool_search. A client search keeps its
// name, description and parameters; Google requires the first two, so each has a default. A server
// search takes no settings.
func toGeminiInteractionsToolSearchTool(tool schemas.ResponsesTool) GeminiInteractionsTool {
	if !tool.IsClientToolSearch() {
		return GeminiInteractionsTool{Type: geminiInteractionsToolSearch}
	}
	name := geminiDefaultClientToolSearchName
	if tool.Name != nil && *tool.Name != "" {
		name = *tool.Name
	}
	native := GeminiInteractionsTool{
		Type:        geminiInteractionsToolSearch,
		Execution:   schemas.Ptr(schemas.ToolSearchExecutionClient),
		Name:        &name,
		Description: clientToolSearchDescription(&tool),
	}
	if tool.ResponsesToolToolSearch != nil {
		native.Parameters = tool.ResponsesToolToolSearch.Parameters
	}
	return native
}

// applyGeminiInteractionsDeferredTools settles defer_loading on the converted tools. Google refuses
// a deferred tool without a server tool_search, and a deferred tool beside a client one:
//   - a request that brings only its own server tool_search is sent as it is;
//   - a request that searches on the caller's side (a client tool_search, or a function the
//     caller runs as its search) loads its tools up front, as Google has no deferral for it;
//   - otherwise a model that serves tool retrieval gets the tool_search the deferral needs, as
//     the OpenAI converter adds it for gpt-5.4+, and an older model loads every tool up front.
func applyGeminiInteractionsDeferredTools(tools []GeminiInteractionsTool, model string, callerSearches bool) []GeminiInteractionsTool {
	deferred, serverSearch, clientSearch := false, false, false
	for i := range tools {
		switch {
		case tools[i].Type == geminiInteractionsToolSearch && tools[i].Execution == nil:
			serverSearch = true
		case tools[i].Type == geminiInteractionsToolSearch:
			clientSearch = true
		case tools[i].DeferLoading != nil && *tools[i].DeferLoading:
			deferred = true
		}
	}
	if !deferred || (serverSearch && !clientSearch) {
		return tools
	}
	if !clientSearch && !callerSearches && supportsGeminiToolSearch(model) {
		return append(tools, GeminiInteractionsTool{Type: geminiInteractionsToolSearch})
	}
	for i := range tools {
		tools[i].DeferLoading = nil
	}
	return tools
}

// geminiClientToolSearchName is the name a client tool_search was sent under, "" when there is
// none. The model calls the search by that name.
func geminiClientToolSearchName(tools []schemas.ResponsesTool) string {
	for i := range tools {
		if tools[i].IsClientToolSearch() {
			if tools[i].Name != nil && *tools[i].Name != "" {
				return *tools[i].Name
			}
			return geminiDefaultClientToolSearchName
		}
	}
	return ""
}

// GeminiInteractionsRequestClientToolSearchName is the name an Interactions request's client
// tool_search was declared under, "" when it declares none.
func GeminiInteractionsRequestClientToolSearchName(tools []GeminiInteractionsTool) string {
	for i := range tools {
		if tools[i].Type == geminiInteractionsToolSearch && tools[i].Execution != nil && *tools[i].Execution == schemas.ToolSearchExecutionClient {
			if tools[i].Name != nil && *tools[i].Name != "" {
				return *tools[i].Name
			}
			return geminiDefaultClientToolSearchName
		}
	}
	return ""
}

// geminiInteractionsClientToolSearchName reads the client tool_search name from an Interactions
// request body, the converted one or a native caller's own. Types are compared decoded, so a body
// that escapes a character is read the same; only the tools array is scanned.
func geminiInteractionsClientToolSearchName(body []byte) string {
	name := ""
	gjson.GetBytes(body, "tools").ForEach(func(_, tool gjson.Result) bool {
		if tool.Get("type").String() == geminiInteractionsToolSearch && tool.Get("execution").String() == schemas.ToolSearchExecutionClient {
			name = tool.Get("name").String()
			return false
		}
		return true
	})
	return name
}

// geminiToolSearchStepToResponses renders a tool_search_call or tool_search_result step as its
// Responses item. Each step is one whole item: the call names what the model searched for, and
// the result lists the declarations it loaded.
func geminiToolSearchStepToResponses(step GeminiInteractionsItem, input bool) schemas.ResponsesMessage {
	execution := toolSearchExecutionServer
	item := schemas.ResponsesMessage{ResponsesToolMessage: &schemas.ResponsesToolMessage{Execution: &execution}}
	if step.Signature != nil && *step.Signature != "" {
		signature := *step.Signature
		item.ResponsesReasoning = &schemas.ResponsesReasoning{EncryptedContent: &signature}
	}
	if !input {
		item.Status = schemas.Ptr("completed")
	}
	if step.Type == GeminiInteractionsStepToolSearchCall {
		item.Type = schemas.Ptr(schemas.ResponsesMessageTypeToolSearchCall)
		item.ID, item.CallID = step.ID, step.ID
		arguments := "{}"
		if trimmed := bytes.TrimSpace(step.Arguments); len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
			arguments = string(trimmed)
		}
		item.Arguments = &arguments
		return item
	}
	item.Type = schemas.Ptr(schemas.ResponsesMessageTypeToolSearchOutput)
	item.CallID = step.CallID
	item.ToolSearchOutputTools = geminiToolSearchResultToResponsesTools(step.Result)
	return item
}

// geminiDefaultToolNamespace prefixes the name of a declared function in a search result.
const geminiDefaultToolNamespace = "default_api:"

// geminiToolSearchResultToResponsesTools lists the declarations a search loaded as Responses
// tools. Interactions function declarations are already flat Responses function tools; an entry
// without a type is a function. Google lists a function under its internal namespace, and its
// signature still verifies a replay without it, so the caller gets the name it declared.
func geminiToolSearchResultToResponsesTools(result json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(result)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return json.RawMessage("[]")
	}
	var entries []json.RawMessage
	if schemas.Unmarshal(trimmed, &entries) != nil {
		return json.RawMessage("[]")
	}
	changed := false
	for i, entry := range entries {
		if !gjson.GetBytes(entry, "type").Exists() {
			if withType, err := providerUtils.SetRawJSONField(entry, "type", []byte(`"function"`)); err == nil {
				entry, changed = withType, true
			}
		}
		if name, ok := strings.CutPrefix(gjson.GetBytes(entry, "name").String(), geminiDefaultToolNamespace); ok && name != "" {
			if renamed, err := providerUtils.SetJSONField(entry, "name", name); err == nil {
				entry, changed = renamed, true
			}
		}
		entries[i] = entry
	}
	if !changed {
		return json.RawMessage(trimmed)
	}
	raw, err := providerUtils.MarshalSorted(entries)
	if err != nil {
		return json.RawMessage("[]")
	}
	return raw
}

// responsesToolSearchItemToGeminiInteractionStep renders a tool_search_call or tool_search_output
// input item as the step Google takes back. A server item replays as its own step only with the
// signature Google issued it under; one without (another provider's search) is history Google
// cannot verify, so it is dropped like any other built-in tool item. A client item is the
// function_call and function_result of the caller's own search, named after its tool_search.
func responsesToolSearchItemToGeminiInteractionStep(msg schemas.ResponsesMessage, clientSearchName string) (GeminiInteractionsItem, bool, error) {
	tm := msg.ResponsesToolMessage
	callID := msg.ID
	if tm != nil && tm.CallID != nil && *tm.CallID != "" {
		callID = tm.CallID
	}
	isCall := *msg.Type == schemas.ResponsesMessageTypeToolSearchCall
	if msg.IsClientToolSearchItem() {
		if clientSearchName == "" {
			return GeminiInteractionsItem{}, false, providerUtils.InvalidRequestErrorf("a client %s input item needs a tool_search tool with execution \"client\" in tools", *msg.Type)
		}
		if callID == nil || *callID == "" {
			return GeminiInteractionsItem{}, false, providerUtils.InvalidRequestErrorf("a client %s input item has no call_id", *msg.Type)
		}
		name := clientSearchName
		if isCall {
			return GeminiInteractionsItem{Type: GeminiInteractionsStepFunctionCall, ID: callID, Name: &name, Arguments: toolSearchArguments(tm)}, true, nil
		}
		if failure, message := clientToolSearchOutcome(msg); failure != "" || message != "" {
			// A failed search goes back as the failure the caller reported, and one that found
			// nothing as the message it gave.
			text, err := providerUtils.MarshalSorted(failure + message)
			if err != nil {
				return GeminiInteractionsItem{}, false, err
			}
			result := GeminiInteractionsItem{Type: GeminiInteractionsStepFunctionResult, CallID: callID, Name: &name, Result: text}
			if failure != "" {
				result.IsError = schemas.Ptr(true)
			}
			return result, true, nil
		}
		return GeminiInteractionsItem{Type: GeminiInteractionsStepFunctionResult, CallID: callID, Name: &name, Result: toolSearchOutputTools(msg)}, true, nil
	}
	if msg.ResponsesReasoning == nil || msg.ResponsesReasoning.EncryptedContent == nil || *msg.ResponsesReasoning.EncryptedContent == "" {
		return GeminiInteractionsItem{}, false, nil
	}
	signature := *msg.ResponsesReasoning.EncryptedContent
	if isCall {
		return GeminiInteractionsItem{Type: GeminiInteractionsStepToolSearchCall, ID: callID, Arguments: toolSearchArguments(tm), Signature: &signature}, true, nil
	}
	return GeminiInteractionsItem{Type: GeminiInteractionsStepToolSearchResult, CallID: callID, Result: toolSearchOutputTools(msg), Signature: &signature}, true, nil
}

// responsesToolSearchItemToGeminiInteractionSteps renders a tool_search item another provider
// returned as the step an Interactions caller reads. A client item is the function call or result
// of the caller's search. When the item carries no name it takes clientSearchName, the name the
// request declared, and "tool_search" when that is empty too.
func responsesToolSearchItemToGeminiInteractionSteps(item schemas.ResponsesMessage, clientSearchName string) []GeminiInteractionsItem {
	tm := item.ResponsesToolMessage
	callID := item.ID
	if tm != nil && tm.CallID != nil && *tm.CallID != "" {
		callID = tm.CallID
	}
	isCall := *item.Type == schemas.ResponsesMessageTypeToolSearchCall
	var signature *string
	if item.ResponsesReasoning != nil && item.ResponsesReasoning.EncryptedContent != nil && *item.ResponsesReasoning.EncryptedContent != "" {
		signature = item.ResponsesReasoning.EncryptedContent
	}
	if item.IsClientToolSearchItem() {
		name := geminiDefaultClientToolSearchName
		switch {
		case tm.Name != nil && *tm.Name != "":
			name = *tm.Name
		case clientSearchName != "":
			name = clientSearchName
		}
		if isCall {
			return []GeminiInteractionsItem{{Type: GeminiInteractionsStepFunctionCall, ID: callID, Name: &name, Arguments: toolSearchArguments(tm)}}
		}
		return []GeminiInteractionsItem{{Type: GeminiInteractionsStepFunctionResult, CallID: callID, Name: &name, Result: toolSearchOutputTools(item)}}
	}
	if isCall {
		return []GeminiInteractionsItem{{Type: GeminiInteractionsStepToolSearchCall, ID: callID, Arguments: toolSearchArguments(tm), Signature: signature}}
	}
	return []GeminiInteractionsItem{{Type: GeminiInteractionsStepToolSearchResult, CallID: callID, Result: toolSearchOutputTools(item), Signature: signature}}
}

// hostedSearches are the hosted tool searches whose output has not come yet, in order.
type hostedSearches []string

// pair is item, or for a hosted search's output that names no call, as OpenAI's does not, a copy that answers the
// earliest search still without one.
func (s *hostedSearches) pair(item schemas.ResponsesMessage) schemas.ResponsesMessage {
	if item.Type == nil || !isResponsesToolSearchItem(*item.Type) || item.IsClientToolSearchItem() {
		return item
	}
	tm := item.ResponsesToolMessage
	named := tm != nil && tm.CallID != nil && *tm.CallID != ""
	if *item.Type == schemas.ResponsesMessageTypeToolSearchCall {
		if !named && item.ID != nil {
			*s = append(*s, *item.ID)
		}
		return item
	}
	if named || len(*s) == 0 {
		return item
	}
	id := (*s)[0]
	*s = (*s)[1:]
	answered := schemas.ResponsesToolMessage{}
	if tm != nil {
		answered = *tm
	}
	answered.CallID = &id
	item.ResponsesToolMessage = &answered
	return item
}

func toolSearchArguments(tm *schemas.ResponsesToolMessage) json.RawMessage {
	if tm == nil || tm.Arguments == nil {
		return json.RawMessage("{}")
	}
	trimmed := strings.TrimSpace(*tm.Arguments)
	if trimmed == "" || !json.Valid([]byte(trimmed)) {
		return json.RawMessage("{}")
	}
	return json.RawMessage(trimmed)
}

func toolSearchOutputTools(msg schemas.ResponsesMessage) json.RawMessage {
	if len(msg.ToolSearchOutputTools) == 0 {
		return json.RawMessage("[]")
	}
	return msg.ToolSearchOutputTools
}

// markGeminiClientToolSearchCalls turns the model's call to the caller's tool_search into the
// client tool_search_call item a Responses caller dispatches, in place. Only a function call named
// after the client search changes.
func markGeminiClientToolSearchCalls(output []schemas.ResponsesMessage, clientSearchName string) {
	if clientSearchName == "" {
		return
	}
	for i := range output {
		item := &output[i]
		if item.Type == nil || *item.Type != schemas.ResponsesMessageTypeFunctionCall || item.ResponsesToolMessage == nil ||
			item.Name == nil || *item.Name != clientSearchName {
			continue
		}
		toClientToolSearchCall(item)
	}
}

func toClientToolSearchCall(item *schemas.ResponsesMessage) {
	item.Type = schemas.Ptr(schemas.ResponsesMessageTypeToolSearchCall)
	item.Execution = schemas.Ptr(schemas.ToolSearchExecutionClient)
	item.Name = nil
	if item.Arguments == nil || strings.TrimSpace(*item.Arguments) == "" {
		item.Arguments = schemas.Ptr("{}")
	}
}

// geminiClientToolSearchStepToResponses renders an input step of the caller's own search as its
// client tool_search item: a function_call named after the client search, or the function_result
// answering one (matched by name, or by the id of an earlier call when the result omits the name).
// It reports false for any other step. calls records the call ids seen so far, and which are searches.
func geminiClientToolSearchStepToResponses(step GeminiInteractionsItem, clientSearchName string, calls *map[string]bool) (schemas.ResponsesMessage, bool) {
	named := step.Name != nil && *step.Name == clientSearchName
	switch step.Type {
	case GeminiInteractionsStepFunctionCall:
		if step.ID != nil {
			if *calls == nil {
				*calls = map[string]bool{}
			}
			(*calls)[*step.ID] = named
		}
		if !named {
			return schemas.ResponsesMessage{}, false
		}
		arguments := "{}"
		if trimmed := bytes.TrimSpace(step.Arguments); len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null")) {
			arguments = string(trimmed)
		}
		item := schemas.ResponsesMessage{ID: step.ID, Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall), ResponsesToolMessage: &schemas.ResponsesToolMessage{CallID: step.ID, Arguments: &arguments}}
		toClientToolSearchCall(&item)
		return item, true
	case GeminiInteractionsStepFunctionResult:
		if !named {
			if step.CallID == nil {
				return schemas.ResponsesMessage{}, false
			}
			// A continuation may answer a call of the previous turn, which is not in this request,
			// without naming the tool; its definitions still say it is the search's output.
			search, known := (*calls)[*step.CallID]
			if known && !search || !known && (step.Name != nil || !isGeminiFunctionDeclarationList(step.Result)) {
				return schemas.ResponsesMessage{}, false
			}
		}
		item := schemas.ResponsesMessage{
			Type:                 schemas.Ptr(schemas.ResponsesMessageTypeToolSearchOutput),
			ResponsesToolMessage: &schemas.ResponsesToolMessage{CallID: step.CallID, Execution: schemas.Ptr(schemas.ToolSearchExecutionClient)},
		}
		if step.IsError != nil && *step.IsError {
			// A failed search loaded nothing. Responses has no failure on tool_search_output, so the
			// item is incomplete and keeps the caller's error text for a replay to Google.
			text := geminiInteractionsResultText(step.Result)
			if text == "" {
				text = geminiClientToolSearchFailure
			}
			item.Status = schemas.Ptr("incomplete")
			item.Error = &schemas.ResponsesToolMessageError{ResponsesToolMessageErrorStr: &text}
			item.ToolSearchOutputTools = json.RawMessage("[]")
			return item, true
		}
		item.ToolSearchOutputTools = geminiToolSearchResultToResponsesTools(step.Result)
		var message string
		if schemas.Unmarshal(step.Result, &message) == nil && message != "" {
			// A search that found nothing may say so in words. Responses has no place for them, so
			// they stay in memory for the replay to Google.
			item.Output = &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: &message}
		}
		return item, true
	}
	return schemas.ResponsesMessage{}, false
}

// isGeminiFunctionDeclarationList reports whether a function_result holds function definitions and
// nothing else, which only a tool search returns.
func isGeminiFunctionDeclarationList(result json.RawMessage) bool {
	list := gjson.ParseBytes(result)
	if !list.IsArray() {
		return false
	}
	found := false
	all := true
	list.ForEach(func(_, entry gjson.Result) bool {
		found = true
		all = entry.Get("type").String() == "function" && entry.Get("name").String() != ""
		return all
	})
	return found && all
}

// clientToolSearchOutcome is what a client tool_search_output says besides the tools it loaded: the
// error of a failed search, or the message of one that found nothing. A failure decoded from JSON
// keeps only its status.
func clientToolSearchOutcome(msg schemas.ResponsesMessage) (failure, message string) {
	tm := msg.ResponsesToolMessage
	if tm != nil && tm.Error.IsError() {
		return tm.Error.Text(), ""
	}
	if msg.Status != nil && *msg.Status == "incomplete" {
		return geminiClientToolSearchFailure, ""
	}
	if tm != nil && tm.Output != nil && tm.Output.ResponsesToolCallOutputStr != nil {
		return "", *tm.Output.ResponsesToolCallOutputStr
	}
	return "", ""
}

// geminiClientToolSearchFailure describes a failed client search that came without an error text.
const geminiClientToolSearchFailure = "tool search failed"

// geminiClientToolSearchDescription describes a client tool_search the caller sent without one.
const geminiClientToolSearchDescription = "Search for tools to load. Matching tools become available to call."

// geminiClientToolSearchKeyType keys the function name a client tool_search went upstream as on
// generateContent, so the unary and streamed responses can turn the model's call back into it.
type geminiClientToolSearchKeyType struct{}

var geminiClientToolSearchKey = geminiClientToolSearchKeyType{}

// geminiClientToolSearchFrom returns the name RecordGeminiClientToolSearch stored, "" when the
// request declared no client tool_search.
func geminiClientToolSearchFrom(ctx *schemas.BifrostContext) string {
	if ctx == nil {
		return ""
	}
	name, _ := ctx.Value(geminiClientToolSearchKey).(string)
	return name
}

// RecordGeminiClientToolSearch stores the name request's client tool_search goes upstream as, for
// the response conversions of a generateContent attempt, clearing any name an earlier attempt on a
// reused context left behind.
func RecordGeminiClientToolSearch(ctx *schemas.BifrostContext, request *schemas.BifrostResponsesRequest) {
	if ctx == nil {
		return
	}
	name := ""
	if request != nil && request.Params != nil {
		name = geminiClientToolSearchName(request.Params.Tools)
	}
	if ctx.Value(geminiClientToolSearchKey) != nil {
		ctx.ClearValue(geminiClientToolSearchKey)
	}
	if name != "" {
		ctx.SetValue(geminiClientToolSearchKey, name)
	}
}

// MarkGeminiClientToolSearchCalls turns the model's call to a client tool_search on a
// generateContent response back into the client tool_search_call the caller dispatches. It is a
// no-op unless the request declared a client tool_search.
func MarkGeminiClientToolSearchCalls(ctx *schemas.BifrostContext, resp *schemas.BifrostResponsesResponse) {
	if resp == nil {
		return
	}
	markGeminiClientToolSearchCalls(resp.Output, geminiClientToolSearchFrom(ctx))
}

// lowerGeminiClientToolSearch rewrites a client tool_search for generateContent, which has no tool
// search: the tool becomes a function named after the search, its tool_search_call and
// tool_search_output history become that function's call and output, and the functions a search
// loaded that tools does not already declare are declared, so the model can call them. It returns
// the search name, "" when tools declare no client search. Copy-on-write: the request shared by
// fallback attempts is never modified.
func lowerGeminiClientToolSearch(tools []schemas.ResponsesTool, input []schemas.ResponsesMessage) ([]schemas.ResponsesTool, []schemas.ResponsesMessage, string, error) {
	name := geminiClientToolSearchName(tools)
	if name == "" {
		return tools, input, "", nil
	}
	declared := make(map[string]bool, len(tools))
	lowered := make([]schemas.ResponsesTool, 0, len(tools)+1)
	for i := range tools {
		tool := tools[i]
		if tool.IsClientToolSearch() {
			tool = geminiClientToolSearchAsFunction(&tools[i], name)
		} else if tool.Name != nil && *tool.Name == name && tool.Type == schemas.ResponsesToolTypeFunction {
			return nil, nil, "", providerUtils.InvalidRequestErrorf("a function tool is named %q, the name of the client tool_search", name)
		}
		if tool.Type == schemas.ResponsesToolTypeFunction && tool.Name != nil {
			declared[*tool.Name] = true
		}
		lowered = append(lowered, tool)
	}
	var out []schemas.ResponsesMessage
	for i := range input {
		msg := &input[i]
		if msg.Type == nil || (*msg.Type != schemas.ResponsesMessageTypeToolSearchCall && *msg.Type != schemas.ResponsesMessageTypeToolSearchOutput) ||
			!msg.IsClientToolSearchItem() {
			continue
		}
		if out == nil {
			out = slices.Clone(input)
		}
		tm := msg.ResponsesToolMessage
		callID := msg.ID
		if tm != nil && tm.CallID != nil && *tm.CallID != "" {
			callID = tm.CallID
		}
		if callID == nil || *callID == "" {
			return nil, nil, "", providerUtils.InvalidRequestErrorf("a client %s input item has no call_id", *msg.Type)
		}
		searchName := name
		if *msg.Type == schemas.ResponsesMessageTypeToolSearchCall {
			args := string(toolSearchArguments(tm))
			out[i] = schemas.ResponsesMessage{
				Type:                 schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				Status:               msg.Status,
				ResponsesToolMessage: &schemas.ResponsesToolMessage{CallID: callID, Name: &searchName, Arguments: &args},
			}
			continue
		}
		result := string(toolSearchOutputTools(*msg))
		if failure, message := clientToolSearchOutcome(*msg); failure != "" || message != "" {
			result = failure + message
		} else {
			var loaded []schemas.ResponsesTool
			if err := schemas.Unmarshal(msg.ToolSearchOutputTools, &loaded); err == nil {
				for _, tool := range loaded {
					if tool.Type == schemas.ResponsesToolTypeFunction && tool.Name != nil && *tool.Name == name {
						// Its calls would read back as the search, so the caller could never run it.
						return nil, nil, "", providerUtils.InvalidRequestErrorf("a tool the client tool_search loaded is named %q, the name of the search", name)
					}
					if tool.Type == schemas.ResponsesToolTypeFunction && tool.Name != nil && !declared[*tool.Name] {
						declared[*tool.Name] = true
						lowered = append(lowered, tool)
					}
				}
			}
		}
		out[i] = schemas.ResponsesMessage{
			Type:   schemas.Ptr(schemas.ResponsesMessageTypeFunctionCallOutput),
			Status: msg.Status,
			ResponsesToolMessage: &schemas.ResponsesToolMessage{CallID: callID, Name: &searchName,
				Output: &schemas.ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: &result}},
		}
	}
	if out == nil {
		out = input
	}
	return lowered, out, name, nil
}

// geminiClientToolSearchAsFunction is the function a client tool_search goes upstream as on
// generateContent: the caller's description and parameters, else a default description and a
// required string query.
func geminiClientToolSearchAsFunction(tool *schemas.ResponsesTool, name string) schemas.ResponsesTool {
	var params *schemas.ToolFunctionParameters
	if tool.ResponsesToolToolSearch != nil {
		params = tool.ResponsesToolToolSearch.Parameters
	}
	if params == nil {
		params = &schemas.ToolFunctionParameters{
			Type:       "object",
			Properties: schemas.NewOrderedMapFromPairs(schemas.KV("query", map[string]any{"type": "string"})),
			Required:   []string{"query"},
		}
	}
	return schemas.ResponsesTool{
		Type:                  schemas.ResponsesToolTypeFunction,
		Name:                  schemas.Ptr(name),
		Description:           clientToolSearchDescription(tool),
		ResponsesToolFunction: &schemas.ResponsesToolFunction{Parameters: params},
	}
}
