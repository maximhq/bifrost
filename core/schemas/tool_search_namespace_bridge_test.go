package schemas

import (
	"testing"
)

func TestExpandToolSearchBridgeDeclaration(t *testing.T) {
	tools := []ResponsesTool{
		{Type: ResponsesToolTypeFunction, Name: Ptr("get_weather")},
		{Type: ResponsesToolTypeNamespace, Name: Ptr(ToolSearchBridgeNamespaceID), ResponsesToolNamespace: &ResponsesToolNamespace{
			Tools: []ResponsesTool{
				{Type: ResponsesToolTypeFunction, Name: Ptr(ToolSearchBridgeFuncBM25)},
				{Type: ResponsesToolTypeFunction, Name: Ptr(ToolSearchBridgeFuncRegex)},
			},
		}},
	}

	out, expanded := ExpandToolSearchBridgeDeclaration(tools)
	if !expanded {
		t.Fatal("expected bridge namespace to be detected and expanded")
	}
	if len(out) != 3 {
		t.Fatalf("expected 3 tools (1 kept + 2 expanded), got %d: %+v", len(out), out)
	}
	if out[0].Name == nil || *out[0].Name != "get_weather" {
		t.Errorf("unrelated tool must survive untouched, got %+v", out[0])
	}

	var sawBM25, sawRegex bool
	for _, tool := range out[1:] {
		if tool.Type != ResponsesToolTypeToolSearch {
			t.Errorf("expected expanded entries to be type tool_search, got %s", tool.Type)
		}
		if tool.Name == nil {
			t.Fatal("expanded tool_search entry missing Name")
		}
		switch *tool.Name {
		case anthropicToolSearchNameBM25:
			sawBM25 = true
		case anthropicToolSearchNameRegex:
			sawRegex = true
		}
	}
	if !sawBM25 || !sawRegex {
		t.Errorf("expected both bm25 and regex sub-tools, got %+v", out[1:])
	}
}

// TestExpandToolSearchBridgeDeclaration_OnlyBM25Declared guards against
// silently widening a request's tool surface: a caller that only declares
// the bm25 sub-tool under the bridge namespace (e.g. because it's replaying
// a previously-collapsed bm25-only namespace, see
// CollapseToolSearchDeclarationsToBridgeNamespace_OnlyBM25Seen) must expand
// to bm25 only -- Anthropic must never receive an uninvited regex
// declaration.
func TestExpandToolSearchBridgeDeclaration_OnlyBM25Declared(t *testing.T) {
	tools := []ResponsesTool{
		{Type: ResponsesToolTypeNamespace, Name: Ptr(ToolSearchBridgeNamespaceID), ResponsesToolNamespace: &ResponsesToolNamespace{
			Tools: []ResponsesTool{
				{Type: ResponsesToolTypeFunction, Name: Ptr(ToolSearchBridgeFuncBM25)},
			},
		}},
	}

	out, expanded := ExpandToolSearchBridgeDeclaration(tools)
	if !expanded {
		t.Fatal("expected bridge namespace to be detected and expanded")
	}
	if len(out) != 1 {
		t.Fatalf("expected exactly 1 expanded tool_search entry (bm25 only), got %d: %+v", len(out), out)
	}
	if out[0].Type != ResponsesToolTypeToolSearch || out[0].Name == nil || *out[0].Name != anthropicToolSearchNameBM25 {
		t.Errorf("expected a single bm25 tool_search entry, got %+v", out[0])
	}
}

func TestExpandToolSearchBridgeDeclaration_NoBridgePresent(t *testing.T) {
	tools := []ResponsesTool{
		{Type: ResponsesToolTypeFunction, Name: Ptr("get_weather")},
		// A genuine user namespace tool -- must NOT be touched, even though
		// it shares the "namespace" type, because its Name isn't the
		// reserved bridge ID.
		{Type: ResponsesToolTypeNamespace, Name: Ptr("my_own_toolgroup"), ResponsesToolNamespace: &ResponsesToolNamespace{
			Tools: []ResponsesTool{{Type: ResponsesToolTypeFunction, Name: Ptr("do_thing")}},
		}},
	}
	out, expanded := ExpandToolSearchBridgeDeclaration(tools)
	if expanded {
		t.Fatal("must not treat a genuine user namespace tool as the bridge")
	}
	if len(out) != 2 || out[1].Name == nil || *out[1].Name != "my_own_toolgroup" {
		t.Errorf("genuine namespace tool must survive unchanged, got %+v", out)
	}
}

func TestCollapseToolSearchDeclarationsToBridgeNamespace(t *testing.T) {
	tools := []ResponsesTool{
		{Type: ResponsesToolTypeFunction, Name: Ptr("get_weather")},
		{Type: ResponsesToolTypeToolSearch, Name: Ptr(anthropicToolSearchNameBM25)},
		{Type: ResponsesToolTypeToolSearch, Name: Ptr(anthropicToolSearchNameRegex)},
	}
	out := CollapseToolSearchDeclarationsToBridgeNamespace(tools)
	if len(out) != 2 {
		t.Fatalf("expected 2 tools (1 kept + 1 collapsed namespace), got %d: %+v", len(out), out)
	}
	ns := out[1]
	if ns.Type != ResponsesToolTypeNamespace || ns.Name == nil || !IsToolSearchBridgeNamespace(ns.Name) {
		t.Fatalf("expected collapsed bridge namespace tool, got %+v", ns)
	}
	if ns.ResponsesToolNamespace == nil || len(ns.ResponsesToolNamespace.Tools) != 2 {
		t.Fatalf("expected 2 grouped functions under the bridge namespace, got %+v", ns.ResponsesToolNamespace)
	}
}

// TestCollapseToolSearchDeclarationsToBridgeNamespace_IsSpecComplete guards
// against regressing to the bare shape that a real OpenAI-compatible backend
// rejects outright: "Missing required parameter: 'tools[0].description'"
// (namespace-level) and "...'tools[0].tools[0].type'" (sub-tool level),
// confirmed live. The collapsed declaration must always carry a description
// on itself and on every grouped sub-tool, and every sub-tool must be typed
// "function".
func TestCollapseToolSearchDeclarationsToBridgeNamespace_IsSpecComplete(t *testing.T) {
	tools := []ResponsesTool{
		{Type: ResponsesToolTypeToolSearch, Name: Ptr(anthropicToolSearchNameBM25)},
		{Type: ResponsesToolTypeToolSearch, Name: Ptr(anthropicToolSearchNameRegex)},
	}
	out := CollapseToolSearchDeclarationsToBridgeNamespace(tools)
	if len(out) != 1 {
		t.Fatalf("expected 1 collapsed namespace tool, got %d: %+v", len(out), out)
	}
	ns := out[0]
	if ns.Description == nil || *ns.Description == "" {
		t.Fatal("collapsed namespace declaration must carry a non-empty description")
	}
	if ns.ResponsesToolNamespace == nil || len(ns.ResponsesToolNamespace.Tools) != 2 {
		t.Fatalf("expected 2 grouped sub-tools, got %+v", ns.ResponsesToolNamespace)
	}
	for _, sub := range ns.ResponsesToolNamespace.Tools {
		if sub.Type != ResponsesToolTypeFunction {
			t.Errorf("grouped sub-tool %v must be typed function, got %q", sub.Name, sub.Type)
		}
		if sub.Description == nil || *sub.Description == "" {
			t.Errorf("grouped sub-tool %v must carry a non-empty description", sub.Name)
		}
	}
}

// TestCollapseToolSearchDeclarationsToBridgeNamespace_OnlyBM25Seen verifies
// the collapsed namespace omits the regex sub-tool when only bm25 was ever
// declared, while still keeping the surviving sub-tool spec-complete.
func TestCollapseToolSearchDeclarationsToBridgeNamespace_OnlyBM25Seen(t *testing.T) {
	tools := []ResponsesTool{
		{Type: ResponsesToolTypeToolSearch, Name: Ptr(anthropicToolSearchNameBM25)},
	}
	out := CollapseToolSearchDeclarationsToBridgeNamespace(tools)
	ns := out[len(out)-1]
	if len(ns.ResponsesToolNamespace.Tools) != 1 {
		t.Fatalf("expected exactly 1 grouped sub-tool (bm25 only), got %+v", ns.ResponsesToolNamespace.Tools)
	}
	sub := ns.ResponsesToolNamespace.Tools[0]
	if sub.Name == nil || *sub.Name != ToolSearchBridgeFuncBM25 {
		t.Fatalf("expected the surviving sub-tool to be bm25, got %+v", sub)
	}
	if sub.Description == nil || *sub.Description == "" {
		t.Error("surviving sub-tool must still carry a non-empty description")
	}
}

// TestBuildToolSearchBridgeNamespaceDeclaration_IsSpecComplete is the direct
// unit test for the canonical constructor itself.
func TestBuildToolSearchBridgeNamespaceDeclaration_IsSpecComplete(t *testing.T) {
	ns := BuildToolSearchBridgeNamespaceDeclaration()
	if ns.Type != ResponsesToolTypeNamespace {
		t.Fatalf("expected type namespace, got %q", ns.Type)
	}
	if !IsToolSearchBridgeNamespace(ns.Name) {
		t.Fatalf("expected the reserved bridge namespace name, got %v", ns.Name)
	}
	if ns.Description == nil || *ns.Description == "" {
		t.Fatal("expected a non-empty top-level description")
	}
	if ns.ResponsesToolNamespace == nil || len(ns.ResponsesToolNamespace.Tools) != 2 {
		t.Fatalf("expected 2 grouped sub-tools, got %+v", ns.ResponsesToolNamespace)
	}
	for _, sub := range ns.ResponsesToolNamespace.Tools {
		if sub.Type != ResponsesToolTypeFunction {
			t.Errorf("sub-tool %v must be typed function, got %q", sub.Name, sub.Type)
		}
		if sub.Description == nil || *sub.Description == "" {
			t.Errorf("sub-tool %v must carry a non-empty description", sub.Name)
		}
	}
}

func TestToolSearchBridgeDeclarationRoundTrip(t *testing.T) {
	original := []ResponsesTool{
		{Type: ResponsesToolTypeFunction, Name: Ptr("get_weather")},
		{Type: ResponsesToolTypeNamespace, Name: Ptr(ToolSearchBridgeNamespaceID), ResponsesToolNamespace: &ResponsesToolNamespace{
			Tools: []ResponsesTool{
				{Type: ResponsesToolTypeFunction, Name: Ptr(ToolSearchBridgeFuncBM25)},
				{Type: ResponsesToolTypeFunction, Name: Ptr(ToolSearchBridgeFuncRegex)},
			},
		}},
	}
	expanded, ok := ExpandToolSearchBridgeDeclaration(original)
	if !ok {
		t.Fatal("expected expansion")
	}
	collapsed := CollapseToolSearchDeclarationsToBridgeNamespace(expanded)
	if len(collapsed) != len(original) {
		t.Fatalf("round trip changed tool count: got %d, want %d", len(collapsed), len(original))
	}
	if collapsed[1].Name == nil || !IsToolSearchBridgeNamespace(collapsed[1].Name) {
		t.Errorf("round trip did not restore the bridge namespace, got %+v", collapsed[1])
	}
}

func TestExpandToolSearchBridgeItems_BM25Completed(t *testing.T) {
	messages := []ResponsesMessage{
		{Role: Ptr(ResponsesInputMessageRoleUser), Content: &ResponsesMessageContent{ContentStr: Ptr("find a weather tool")}},
		{Type: Ptr(ResponsesMessageTypeFunctionCall), ID: Ptr("fc_1"), ResponsesToolMessage: &ResponsesToolMessage{
			CallID: Ptr("call_1"), Name: Ptr(ToolSearchBridgeFuncBM25), Namespace: Ptr(ToolSearchBridgeNamespaceID),
			Arguments: Ptr(`{"query":"weather"}`),
		}},
		{Type: Ptr(ResponsesMessageTypeFunctionCallOutput), ResponsesToolMessage: &ResponsesToolMessage{
			CallID: Ptr("call_1"), Namespace: Ptr(ToolSearchBridgeNamespaceID),
			Output: &ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: Ptr(`["get_weather","get_forecast"]`)},
		}},
	}

	out := ExpandToolSearchBridgeItems(messages)
	if len(out) != 2 {
		t.Fatalf("expected call+output pair merged into 1 item (plus the user message) = 2 total, got %d: %+v", len(out), out)
	}
	merged := out[1]
	if merged.Type == nil || *merged.Type != ResponsesMessageTypeAnthropicToolSearchCall {
		t.Fatalf("expected merged item type tool_search_tool_call, got %v", merged.Type)
	}
	if merged.Status == nil || *merged.Status != "completed" {
		t.Errorf("expected status completed once output is folded in, got %v", merged.Status)
	}
	if merged.ResponsesToolMessage.Name == nil || *merged.ResponsesToolMessage.Name != anthropicToolSearchNameBM25 {
		t.Errorf("expected resolved anthropic name %s, got %v", anthropicToolSearchNameBM25, merged.ResponsesToolMessage.Name)
	}
	if merged.ResponsesToolMessage.CallID == nil || *merged.ResponsesToolMessage.CallID != "call_1" {
		t.Errorf("call id lost in merge: %v", merged.ResponsesToolMessage.CallID)
	}
	if merged.ResponsesToolMessage.Output == nil || merged.ResponsesToolMessage.Output.ResponsesToolCallOutputStr == nil ||
		*merged.ResponsesToolMessage.Output.ResponsesToolCallOutputStr != `["get_weather","get_forecast"]` {
		t.Errorf("output not folded into merged item: %+v", merged.ResponsesToolMessage.Output)
	}
}

func TestExpandToolSearchBridgeItems_RegexResolvesCorrectly(t *testing.T) {
	messages := []ResponsesMessage{
		{Type: Ptr(ResponsesMessageTypeFunctionCall), ResponsesToolMessage: &ResponsesToolMessage{
			CallID: Ptr("call_2"), Name: Ptr(ToolSearchBridgeFuncRegex), Namespace: Ptr(ToolSearchBridgeNamespaceID),
			Arguments: Ptr(`{"query":"^GET .*"}`),
		}},
	}
	out := ExpandToolSearchBridgeItems(messages)
	if len(out) != 1 {
		t.Fatalf("expected 1 merged (unpaired) item, got %d", len(out))
	}
	if out[0].Status == nil || *out[0].Status != "in_progress" {
		t.Errorf("unpaired call must stay in_progress, got %v", out[0].Status)
	}
	if out[0].ResponsesToolMessage.Name == nil || *out[0].ResponsesToolMessage.Name != anthropicToolSearchNameRegex {
		t.Errorf("expected regex resolved, got %v -- bm25-default must not silently win for an explicit regex call", out[0].ResponsesToolMessage.Name)
	}
}

// TestExpandToolSearchBridgeItems_GenuineNamespaceUnaffected is the critical
// regression guard: a real, user-declared namespace-grouped function call
// (unrelated to tool_search) must pass through completely untouched, proving
// the reserved sentinel actually discriminates instead of matching on shape
// alone.
func TestExpandToolSearchBridgeItems_GenuineNamespaceUnaffected(t *testing.T) {
	messages := []ResponsesMessage{
		{Type: Ptr(ResponsesMessageTypeFunctionCall), ID: Ptr("fc_real"), ResponsesToolMessage: &ResponsesToolMessage{
			CallID: Ptr("call_real"), Name: Ptr("create_event"), Namespace: Ptr("calendar"),
			Arguments: Ptr(`{"title":"standup"}`),
		}},
		{Type: Ptr(ResponsesMessageTypeFunctionCallOutput), ResponsesToolMessage: &ResponsesToolMessage{
			CallID: Ptr("call_real"), Namespace: Ptr("calendar"),
			Output: &ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: Ptr(`"created"`)},
		}},
	}
	out := ExpandToolSearchBridgeItems(messages)
	if len(out) != 2 {
		t.Fatalf("genuine namespace call+output must both survive untouched, got %d items: %+v", len(out), out)
	}
	if out[0].Type == nil || *out[0].Type != ResponsesMessageTypeFunctionCall {
		t.Errorf("genuine namespace call must not be reinterpreted as tool_search_tool_call, got %v", out[0].Type)
	}
	if out[1].Type == nil || *out[1].Type != ResponsesMessageTypeFunctionCallOutput {
		t.Errorf("genuine namespace output must not be dropped/merged, got %v", out[1].Type)
	}
}

// TestExpandToolSearchBridgeItems_OrphanedOutputPreserved guards against
// silently dropping a discovered-tool result: if a caller's history has been
// trimmed/paginated to only the function_call_output half of a bridge pair
// (no matching call in this slice), it must be preserved unchanged rather
// than vanishing. Found by an automated codex review pass.
func TestExpandToolSearchBridgeItems_OrphanedOutputPreserved(t *testing.T) {
	messages := []ResponsesMessage{
		{Type: Ptr(ResponsesMessageTypeFunctionCallOutput), ResponsesToolMessage: &ResponsesToolMessage{
			CallID: Ptr("call_orphan"), Namespace: Ptr(ToolSearchBridgeNamespaceID),
			Output: &ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: Ptr(`["get_weather"]`)},
		}},
	}
	out := ExpandToolSearchBridgeItems(messages)
	if len(out) != 1 {
		t.Fatalf("orphaned output must be preserved, not dropped, got %d items: %+v", len(out), out)
	}
	if out[0].Type == nil || *out[0].Type != ResponsesMessageTypeFunctionCallOutput {
		t.Errorf("expected the orphaned output to survive as function_call_output, got %v", out[0].Type)
	}
	if out[0].ResponsesToolMessage.Output == nil || out[0].ResponsesToolMessage.Output.ResponsesToolCallOutputStr == nil ||
		*out[0].ResponsesToolMessage.Output.ResponsesToolCallOutputStr != `["get_weather"]` {
		t.Errorf("discovered-tool result lost from orphaned output: %+v", out[0].ResponsesToolMessage.Output)
	}
}

func TestCollapseToolSearchItemToNamespacePair(t *testing.T) {
	msg := ResponsesMessage{
		ID:     Ptr("srvtoolu_1"),
		Type:   Ptr(ResponsesMessageTypeAnthropicToolSearchCall),
		Status: Ptr("completed"),
		ResponsesToolMessage: &ResponsesToolMessage{
			CallID:    Ptr("srvtoolu_1"),
			Name:      Ptr(anthropicToolSearchNameRegex),
			Arguments: Ptr(`{"query":"^GET .*"}`),
			Output:    &ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: Ptr(`["get_request"]`)},
		},
	}

	pair := CollapseToolSearchItemToNamespacePair(msg)
	if len(pair) != 2 {
		t.Fatalf("expected [call, output] pair, got %d items", len(pair))
	}
	call, output := pair[0], pair[1]
	if call.Type == nil || *call.Type != ResponsesMessageTypeFunctionCall {
		t.Errorf("expected function_call, got %v", call.Type)
	}
	if call.ResponsesToolMessage.Namespace == nil || !IsToolSearchBridgeNamespace(call.ResponsesToolMessage.Namespace) {
		t.Errorf("call must carry the reserved bridge namespace, got %v", call.ResponsesToolMessage.Namespace)
	}
	if call.ResponsesToolMessage.Name == nil || *call.ResponsesToolMessage.Name != ToolSearchBridgeFuncRegex {
		t.Errorf("expected grouped function name %s (regex preserved), got %v", ToolSearchBridgeFuncRegex, call.ResponsesToolMessage.Name)
	}
	if output.Type == nil || *output.Type != ResponsesMessageTypeFunctionCallOutput {
		t.Errorf("expected function_call_output, got %v", output.Type)
	}
	if output.ResponsesToolMessage.CallID == nil || *output.ResponsesToolMessage.CallID != "srvtoolu_1" {
		t.Errorf("output call_id must match the call, got %v", output.ResponsesToolMessage.CallID)
	}
}

// TestCollapseToolSearchItemToNamespacePair_InProgressStaysInProgress guards
// against presenting an unfinished Anthropic tool_search call as completed:
// the collapsed function_call's Status must mirror the source item's actual
// completion state (no Output yet == still in_progress), not be hardcoded to
// "completed". Found by an automated codex review pass.
func TestCollapseToolSearchItemToNamespacePair_InProgressStaysInProgress(t *testing.T) {
	msg := ResponsesMessage{
		ID:     Ptr("srvtoolu_2"),
		Type:   Ptr(ResponsesMessageTypeAnthropicToolSearchCall),
		Status: Ptr("in_progress"),
		ResponsesToolMessage: &ResponsesToolMessage{
			CallID:    Ptr("srvtoolu_2"),
			Name:      Ptr(anthropicToolSearchNameBM25),
			Arguments: Ptr(`{"query":"weather"}`),
			// No Output -- the search hasn't completed yet.
		},
	}

	pair := CollapseToolSearchItemToNamespacePair(msg)
	if len(pair) != 1 {
		t.Fatalf("expected a single-element slice (call only, no output) for an unfinished search, got %d items: %+v", len(pair), pair)
	}
	call := pair[0]
	if call.Status == nil || *call.Status != "in_progress" {
		t.Errorf("unfinished search must collapse to status in_progress, not fabricate completed, got %v", call.Status)
	}
}

// TestToolSearchBridgeItemRoundTrip proves expand(collapse(x)) == x for the
// fields that matter (algorithm choice, call id, arguments, output),
// end-to-end through both directions.
func TestToolSearchBridgeItemRoundTrip(t *testing.T) {
	original := ResponsesMessage{
		ID:     Ptr("srvtoolu_rt"),
		Type:   Ptr(ResponsesMessageTypeAnthropicToolSearchCall),
		Status: Ptr("completed"),
		ResponsesToolMessage: &ResponsesToolMessage{
			CallID:    Ptr("srvtoolu_rt"),
			Name:      Ptr(anthropicToolSearchNameBM25),
			Arguments: Ptr(`{"query":"weather"}`),
			Output:    &ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: Ptr(`["get_weather"]`)},
		},
	}

	pair := CollapseToolSearchItemToNamespacePair(original)
	merged := ExpandToolSearchBridgeItems(pair)
	if len(merged) != 1 {
		t.Fatalf("expected round trip to re-merge into 1 item, got %d: %+v", len(merged), merged)
	}
	got := merged[0]
	if got.ResponsesToolMessage.Name == nil || *got.ResponsesToolMessage.Name != anthropicToolSearchNameBM25 {
		t.Errorf("algorithm lost in round trip: got %v, want %s", got.ResponsesToolMessage.Name, anthropicToolSearchNameBM25)
	}
	if got.ResponsesToolMessage.Arguments == nil || *got.ResponsesToolMessage.Arguments != `{"query":"weather"}` {
		t.Errorf("arguments lost in round trip: got %v", got.ResponsesToolMessage.Arguments)
	}
	if got.ResponsesToolMessage.Output == nil || got.ResponsesToolMessage.Output.ResponsesToolCallOutputStr == nil ||
		*got.ResponsesToolMessage.Output.ResponsesToolCallOutputStr != `["get_weather"]` {
		t.Errorf("output lost in round trip: got %+v", got.ResponsesToolMessage.Output)
	}
}

// TestToolSearchBridgeJSONIsDeterministic guards against prompt-cache
// breakage: marshaling the same collapsed namespace pair twice must produce
// byte-identical JSON, since Bifrost's egress relies on MarshalSorted-style
// deterministic key ordering everywhere else for this exact reason.
func TestToolSearchBridgeJSONIsDeterministic(t *testing.T) {
	msg := ResponsesMessage{
		ID:     Ptr("srvtoolu_cache"),
		Type:   Ptr(ResponsesMessageTypeAnthropicToolSearchCall),
		Status: Ptr("completed"),
		ResponsesToolMessage: &ResponsesToolMessage{
			CallID:    Ptr("srvtoolu_cache"),
			Name:      Ptr(anthropicToolSearchNameBM25),
			Arguments: Ptr(`{"query":"weather"}`),
			Output:    &ResponsesToolMessageOutputStruct{ResponsesToolCallOutputStr: Ptr(`["get_weather","get_forecast"]`)},
		},
	}

	pair1 := CollapseToolSearchItemToNamespacePair(msg)
	pair2 := CollapseToolSearchItemToNamespacePair(msg)

	b1, err := Marshal(pair1)
	if err != nil {
		t.Fatalf("marshal pair1: %v", err)
	}
	b2, err := Marshal(pair2)
	if err != nil {
		t.Fatalf("marshal pair2: %v", err)
	}
	if string(b1) != string(b2) {
		t.Fatalf("non-deterministic JSON across identical inputs would break prompt caching:\n%s\nvs\n%s", b1, b2)
	}
}
