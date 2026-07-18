package schemas

import "strings"

// ToolSearchBridgeNamespaceID is Bifrost's own reserved namespace identifier
// for the synthetic tool_search bridge shown to callers on the OpenAI
// Responses API surface when the actual backend implements tool_search
// natively in a different shape (e.g. Anthropic's tool_search_tool_bm25/
// tool_search_tool_regex, which are server-executed and have no OpenAI
// equivalent on the wire). It is never a genuine user-declared namespace name
// — callers must not declare a namespace tool with this exact identifier.
//
// This exists because Anthropic's tool_search algorithm choice (bm25 vs
// regex) has no analogue in OpenAI's own generic "tool_search" tool type
// (single algorithm, chosen by the harness, not selectable on the wire).
// Modelling both algorithms as two grouped functions under one namespace tool
// lets the caller address them explicitly instead of Bifrost silently
// defaulting to bm25.
const ToolSearchBridgeNamespaceID = "bifrost_tool_search_bridge"

// Grouped function names exposed under ToolSearchBridgeNamespaceID.
const (
	ToolSearchBridgeFuncBM25  = "tool_search_bm25"
	ToolSearchBridgeFuncRegex = "tool_search_regex"
)

// Description text for the bridge namespace and its grouped sub-tools.
// OpenAI's real namespace tool type requires a non-empty "description" on
// BOTH the namespace declaration itself AND every grouped sub-tool entry —
// confirmed live: a bare {"type":"namespace","name":"...","tools":[...]}
// with no descriptions is rejected outright with
// "Missing required parameter: 'tools[0].description'" (and, once that's
// fixed, "...'tools[0].tools[0].type'" if the sub-tool also lacks
// "type":"function"). BuildToolSearchBridgeNamespaceDeclaration is the
// canonical, spec-complete constructor — prefer it over hand-rolling the
// shape to avoid reintroducing either 400.
const (
	ToolSearchBridgeNamespaceDescription = "Search hidden/deferred tools by keyword (bm25) or regex pattern."
	ToolSearchBridgeFuncBM25Description  = "Keyword search over deferred tools."
	ToolSearchBridgeFuncRegexDescription = "Regex pattern search over deferred tools."
)

// BuildToolSearchBridgeNamespaceDeclaration returns the canonical,
// spec-complete namespace tool declaration for the tool_search bridge —
// {"type":"namespace","name":"bifrost_tool_search_bridge","description":...,
// "tools":[{"type":"function","name":"tool_search_bm25","description":...},
// {"type":"function","name":"tool_search_regex","description":...}]} —
// valid against a real OpenAI-compatible backend's required-field checks.
// Callers constructing this declaration (rather than receiving it from
// CollapseToolSearchDeclarationsToBridgeNamespace) should use this instead of
// hand-rolling the shape.
func BuildToolSearchBridgeNamespaceDeclaration() ResponsesTool {
	return ResponsesTool{
		Type:        ResponsesToolTypeNamespace,
		Name:        Ptr(ToolSearchBridgeNamespaceID),
		Description: Ptr(ToolSearchBridgeNamespaceDescription),
		ResponsesToolNamespace: &ResponsesToolNamespace{
			Tools: []ResponsesTool{
				{Type: ResponsesToolTypeFunction, Name: Ptr(ToolSearchBridgeFuncBM25), Description: Ptr(ToolSearchBridgeFuncBM25Description)},
				{Type: ResponsesToolTypeFunction, Name: Ptr(ToolSearchBridgeFuncRegex), Description: Ptr(ToolSearchBridgeFuncRegexDescription)},
			},
		},
	}
}

// Anthropic's native tool_search sub-tool type/name strings. Duplicated here
// (rather than imported from core/providers/anthropic) because this file is
// provider-agnostic — core/schemas must not depend on a specific provider
// package — and these two literal strings are Anthropic's stable public API
// names, unlikely to change independently of this file.
const (
	anthropicToolSearchNameBM25  = "tool_search_tool_bm25"
	anthropicToolSearchNameRegex = "tool_search_tool_regex"
)

// IsToolSearchBridgeNamespace reports whether name is Bifrost's reserved
// tool_search bridge namespace identifier, as opposed to a genuine
// user-declared namespace tool that happens to share the "namespace" type.
func IsToolSearchBridgeNamespace(name *string) bool {
	return name != nil && *name == ToolSearchBridgeNamespaceID
}

// bridgeFuncIsRegex centralizes the bm25-vs-regex resolution so every
// converter in this file (and, ideally, the pre-existing Anthropic egress
// heuristic at core/providers/anthropic/responses.go which independently does
// strings.Contains(Name,"regex")) agrees on the same rule.
func bridgeFuncIsRegex(name string) bool {
	return strings.Contains(name, "regex")
}

// anthropicToolSearchNameForBridgeFunc maps a grouped bridge function name
// ("tool_search_bm25"/"tool_search_regex") to Anthropic's native sub-tool
// name. Defaults to bm25 for any unrecognized function name, matching the
// existing default at the Anthropic declaration-egress path.
func anthropicToolSearchNameForBridgeFunc(funcName string) string {
	if bridgeFuncIsRegex(funcName) {
		return anthropicToolSearchNameRegex
	}
	return anthropicToolSearchNameBM25
}

// bridgeFuncForAnthropicToolSearchName is the reverse of
// anthropicToolSearchNameForBridgeFunc.
func bridgeFuncForAnthropicToolSearchName(anthropicName string) string {
	if bridgeFuncIsRegex(anthropicName) {
		return ToolSearchBridgeFuncRegex
	}
	return ToolSearchBridgeFuncBM25
}

// ExpandToolSearchBridgeDeclaration turns the caller-facing namespace
// declaration for the tool_search bridge into the neutral tool_search
// declaration(s) that the rest of the pipeline (Anthropic declaration egress
// at responses.go:7069-7079) already knows how to render onto a backend.
// Only expands the sub-tools actually present under the namespace's
// grouped Tools[] — a caller that re-declares a previously-collapsed
// bm25-only namespace (see CollapseToolSearchDeclarationsToBridgeNamespace)
// must not have the request silently widened to include regex too.
// Returns the tools unchanged (same slice) and false when no bridge
// namespace entry is present, or when the namespace has no recognized
// sub-tools (falls back to bm25 only, matching the existing Anthropic
// egress default for an unrecognized/absent algorithm hint).
func ExpandToolSearchBridgeDeclaration(tools []ResponsesTool) ([]ResponsesTool, bool) {
	idx := -1
	for i := range tools {
		if tools[i].Type == ResponsesToolTypeNamespace && IsToolSearchBridgeNamespace(tools[i].Name) {
			idx = i
			break
		}
	}
	if idx == -1 {
		return tools, false
	}

	var sawBM25, sawRegex bool
	if ns := tools[idx].ResponsesToolNamespace; ns != nil {
		for _, sub := range ns.Tools {
			if sub.Name == nil {
				continue
			}
			if bridgeFuncIsRegex(*sub.Name) {
				sawRegex = true
			} else {
				sawBM25 = true
			}
		}
	}
	if !sawBM25 && !sawRegex {
		// No recognized sub-tools declared under the namespace — default to
		// bm25, matching the existing Anthropic egress default.
		sawBM25 = true
	}

	expanded := make([]ResponsesTool, 0, 2)
	if sawBM25 {
		expanded = append(expanded, ResponsesTool{Type: ResponsesToolTypeToolSearch, Name: Ptr(anthropicToolSearchNameBM25)})
	}
	if sawRegex {
		expanded = append(expanded, ResponsesTool{Type: ResponsesToolTypeToolSearch, Name: Ptr(anthropicToolSearchNameRegex)})
	}

	out := make([]ResponsesTool, 0, len(tools)+len(expanded))
	out = append(out, tools[:idx]...)
	out = append(out, expanded...)
	out = append(out, tools[idx+1:]...)
	return out, true
}

// CollapseToolSearchDeclarationsToBridgeNamespace is the reverse of
// ExpandToolSearchBridgeDeclaration: given a tools[] list containing the
// neutral tool_search declaration(s), render them back into the single
// caller-facing namespace declaration. Used when Bifrost serializes a
// tools[] list back out to an OpenAI-Responses-shaped caller whose backend is
// not itself OpenAI (e.g. Anthropic). Tools not of type tool_search pass
// through unchanged; if no tool_search entries are present, returns the input
// unchanged.
func CollapseToolSearchDeclarationsToBridgeNamespace(tools []ResponsesTool) []ResponsesTool {
	var sawBM25, sawRegex bool
	out := make([]ResponsesTool, 0, len(tools))
	for _, t := range tools {
		if t.Type == ResponsesToolTypeToolSearch {
			if t.Name != nil && bridgeFuncIsRegex(*t.Name) {
				sawRegex = true
			} else {
				sawBM25 = true
			}
			continue
		}
		out = append(out, t)
	}
	if !sawBM25 && !sawRegex {
		return tools
	}

	// Build off the canonical, spec-complete declaration (both
	// description fields and each sub-tool's "type":"function" present —
	// see BuildToolSearchBridgeNamespaceDeclaration's doc comment for the
	// two live 400s a hand-rolled, incomplete shape triggers), then drop
	// whichever sub-tool wasn't actually seen.
	canonical := BuildToolSearchBridgeNamespaceDeclaration()
	grouped := make([]ResponsesTool, 0, 2)
	for _, sub := range canonical.ResponsesToolNamespace.Tools {
		if sub.Name == nil {
			continue
		}
		if (*sub.Name == ToolSearchBridgeFuncBM25 && sawBM25) || (*sub.Name == ToolSearchBridgeFuncRegex && sawRegex) {
			grouped = append(grouped, sub)
		}
	}
	canonical.ResponsesToolNamespace = &ResponsesToolNamespace{Tools: grouped}
	out = append(out, canonical)
	return out
}

// isToolSearchBridgeFunctionCall reports whether msg is a function_call item
// tagged with the reserved bridge namespace (as opposed to a genuine
// namespace-grouped user function call, which will never carry this specific
// reserved ID).
func isToolSearchBridgeFunctionCall(msg *ResponsesMessage) bool {
	return msg.Type != nil && *msg.Type == ResponsesMessageTypeFunctionCall &&
		msg.ResponsesToolMessage != nil && IsToolSearchBridgeNamespace(msg.ResponsesToolMessage.Namespace)
}

func isToolSearchBridgeFunctionCallOutput(msg *ResponsesMessage) bool {
	return msg.Type != nil && *msg.Type == ResponsesMessageTypeFunctionCallOutput &&
		msg.ResponsesToolMessage != nil && IsToolSearchBridgeNamespace(msg.ResponsesToolMessage.Namespace)
}

// ExpandToolSearchBridgeItems scans messages for namespace-tagged
// function_call/function_call_output pairs belonging to the tool_search
// bridge (matched by CallID) and merges each pair into a single neutral
// tool_search_tool_call item (schemas.ResponsesMessageTypeAnthropicToolSearchCall),
// exactly the shape the Anthropic-egress converter
// (convertBifrostToolSearchCallToAnthropicBlocks) already knows how to
// render. Non-bridge items (including genuine user namespace calls, which
// never carry ToolSearchBridgeNamespaceID) pass through unchanged.
//
// If a function_call's matching function_call_output has not arrived yet
// (call still in flight), the call is expanded on its own with
// Status "in_progress" and no Output — mirroring the Anthropic ingest
// behavior for an unpaired server_tool_use. Conversely, an output item with
// no matching call in this slice (e.g. a caller trimmed/paginated history to
// only the tail of a pair) is passed through unchanged rather than silently
// dropped — losing the discovered-tool result would otherwise be invisible.
func ExpandToolSearchBridgeItems(messages []ResponsesMessage) []ResponsesMessage {
	hasBridgeItem := false
	for i := range messages {
		if isToolSearchBridgeFunctionCall(&messages[i]) || isToolSearchBridgeFunctionCallOutput(&messages[i]) {
			hasBridgeItem = true
			break
		}
	}
	if !hasBridgeItem {
		return messages
	}

	// Index outputs by call_id for O(1) pairing lookup, and track which
	// call_ids have a matching call present so an orphaned output (no call
	// in this slice) is never silently dropped.
	outputByCallID := make(map[string]*ResponsesMessage)
	callIDsWithCall := make(map[string]bool)
	for i := range messages {
		if isToolSearchBridgeFunctionCallOutput(&messages[i]) && messages[i].ResponsesToolMessage.CallID != nil {
			outputByCallID[*messages[i].ResponsesToolMessage.CallID] = &messages[i]
		}
		if isToolSearchBridgeFunctionCall(&messages[i]) && messages[i].ResponsesToolMessage.CallID != nil {
			callIDsWithCall[*messages[i].ResponsesToolMessage.CallID] = true
		}
	}

	out := make([]ResponsesMessage, 0, len(messages))
	for i := range messages {
		msg := messages[i]
		switch {
		case isToolSearchBridgeFunctionCall(&msg):
			out = append(out, mergeToolSearchBridgeCall(msg, outputByCallID))
		case isToolSearchBridgeFunctionCallOutput(&msg):
			var callID string
			if msg.ResponsesToolMessage.CallID != nil {
				callID = *msg.ResponsesToolMessage.CallID
			}
			if callIDsWithCall[callID] {
				// Folded into the merged call item above. Skip emitting it a
				// second time as a standalone item.
				continue
			}
			// No matching call in this slice -- preserve as-is rather than
			// dropping the discovered-tool result.
			out = append(out, msg)
		default:
			out = append(out, msg)
		}
	}
	return out
}

// mergeToolSearchBridgeCall builds the neutral tool_search_tool_call item
// from a bridge function_call, folding in the matching function_call_output
// (if present) via outputByCallID.
func mergeToolSearchBridgeCall(call ResponsesMessage, outputByCallID map[string]*ResponsesMessage) ResponsesMessage {
	subFuncName := ""
	if call.ResponsesToolMessage.Name != nil {
		subFuncName = *call.ResponsesToolMessage.Name
	}
	anthropicName := anthropicToolSearchNameForBridgeFunc(subFuncName)

	merged := ResponsesMessage{
		Type:   Ptr(ResponsesMessageTypeAnthropicToolSearchCall),
		ID:     call.ID,
		Status: Ptr("in_progress"),
		ResponsesToolMessage: &ResponsesToolMessage{
			CallID:    call.ResponsesToolMessage.CallID,
			Name:      Ptr(anthropicName),
			Arguments: call.ResponsesToolMessage.Arguments,
		},
	}

	var callID string
	if call.ResponsesToolMessage.CallID != nil {
		callID = *call.ResponsesToolMessage.CallID
	}
	if output, ok := outputByCallID[callID]; ok && output.ResponsesToolMessage != nil {
		merged.Status = Ptr("completed")
		merged.ResponsesToolMessage.Output = output.ResponsesToolMessage.Output
	}

	return merged
}

// CollapseToolSearchItemToNamespacePair is the reverse of
// ExpandToolSearchBridgeItems for a single item: it renders one neutral
// tool_search_tool_call item back into the caller-facing
// function_call/function_call_output pair tagged with
// ToolSearchBridgeNamespaceID. Returns nil if msg is not a tool_search_tool_call
// item. Returns a single-element slice (just the call, status "in_progress")
// if the call has no Output yet.
func CollapseToolSearchItemToNamespacePair(msg ResponsesMessage) []ResponsesMessage {
	if msg.Type == nil || *msg.Type != ResponsesMessageTypeAnthropicToolSearchCall || msg.ResponsesToolMessage == nil {
		return nil
	}

	anthropicName := ""
	if msg.ResponsesToolMessage.Name != nil {
		anthropicName = *msg.ResponsesToolMessage.Name
	}
	bridgeFuncName := bridgeFuncForAnthropicToolSearchName(anthropicName)

	// Status must reflect the source item's actual completion state -- an
	// in-progress Anthropic tool_search_tool_call (no Output yet) must not
	// be presented to the caller as "completed"; that would hide a
	// still-running search from a client polling/replaying history.
	callStatus := "in_progress"
	if msg.ResponsesToolMessage.Output != nil {
		callStatus = "completed"
	}

	call := ResponsesMessage{
		Type:   Ptr(ResponsesMessageTypeFunctionCall),
		ID:     msg.ID,
		Status: Ptr(callStatus),
		ResponsesToolMessage: &ResponsesToolMessage{
			CallID:    msg.ResponsesToolMessage.CallID,
			Name:      Ptr(bridgeFuncName),
			Namespace: Ptr(ToolSearchBridgeNamespaceID),
			Arguments: msg.ResponsesToolMessage.Arguments,
		},
	}

	if msg.ResponsesToolMessage.Output == nil {
		return []ResponsesMessage{call}
	}

	output := ResponsesMessage{
		Type:   Ptr(ResponsesMessageTypeFunctionCallOutput),
		Status: Ptr("completed"),
		ResponsesToolMessage: &ResponsesToolMessage{
			CallID:    msg.ResponsesToolMessage.CallID,
			Namespace: Ptr(ToolSearchBridgeNamespaceID),
			Output:    msg.ResponsesToolMessage.Output,
		},
	}

	return []ResponsesMessage{call, output}
}
