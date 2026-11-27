package toolsearch

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func functionTool(name, description string, deferred bool) schemas.ResponsesTool {
	tool := schemas.ResponsesTool{
		Type:        schemas.ResponsesToolTypeFunction,
		Name:        schemas.Ptr(name),
		Description: schemas.Ptr(description),
		ResponsesToolFunction: &schemas.ResponsesToolFunction{Parameters: &schemas.ToolFunctionParameters{
			Type: "object", Properties: schemas.NewOrderedMapFromPairs(schemas.KV("city", map[string]any{"type": "string"})),
		}},
	}
	if deferred {
		tool.DeferLoading = schemas.Ptr(true)
	}
	return tool
}

func deferredRequest() *schemas.BifrostResponsesRequest {
	return &schemas.BifrostResponsesRequest{
		Provider: schemas.Gemini,
		Model:    "gemini-3.8-flash",
		Input: []schemas.ResponsesMessage{{
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("weather in Tokyo?")},
		}},
		Params: &schemas.ResponsesParameters{Tools: []schemas.ResponsesTool{
			functionTool("lookup_city", "Look up a city", false),
			functionTool("get_weather", "Get the current weather for a city", true),
			functionTool("get_stock_price", "Get a stock quote", true),
		}},
	}
}

func functionCall(callID, name, args string) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID: schemas.Ptr(callID), Name: schemas.Ptr(name), Arguments: schemas.Ptr(args),
		},
	}
}

func searchCall(args string) schemas.ChatAssistantMessageToolCall {
	return schemas.ChatAssistantMessageToolCall{
		ID:       schemas.Ptr("call_s1"),
		Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr(ToolName), Arguments: args},
	}
}

// keptTools runs every caller tool through the session the way a turn's request does.
func keptTools(s *Session, req *schemas.BifrostResponsesRequest) []schemas.ResponsesTool {
	var kept []schemas.ResponsesTool
	for _, tool := range req.Params.Tools {
		if tool, ok := s.FilterTool(tool); ok {
			kept = append(kept, tool)
		}
	}
	return kept
}

func names(tools []schemas.ResponsesTool) []string {
	var out []string
	for _, tool := range tools {
		out = append(out, *tool.Name)
	}
	return out
}

func TestCandidate(t *testing.T) {
	assert.True(t, Candidate(deferredRequest()))

	noDeferred := deferredRequest()
	noDeferred.Params.Tools = noDeferred.Params.Tools[:1]
	assert.False(t, Candidate(noDeferred), "nothing deferred")

	// Codex declares a client tool_search on every model. Candidate only runs for
	// providers with no tool search, where nothing else can serve it.
	withSearch := deferredRequest()
	withSearch.Params.Tools = append(withSearch.Params.Tools, clientToolSearch())
	assert.True(t, Candidate(withSearch), "a tool_search the provider cannot run does not block the gateway search")

	assert.True(t, Candidate(codexRequest()), "deferred tools inside a namespace count")
	memberDeferred := codexRequest()
	memberDeferred.Params.Tools[2].DeferLoading = nil
	memberDeferred.Params.Tools[2].ResponsesToolNamespace.Tools[1].DeferLoading = schemas.Ptr(true)
	assert.True(t, Candidate(memberDeferred), "a deferred member of an eager namespace counts")
	eagerNamespace := codexRequest()
	eagerNamespace.Params.Tools[2].DeferLoading = nil
	assert.False(t, Candidate(eagerNamespace), "nothing deferred inside the namespaces")

	clientSearch := deferredRequest()
	clientSearch.Params.Tools = append(clientSearch.Params.Tools, functionTool("ToolSearch", "search", false))
	assert.False(t, Candidate(clientSearch), "the client runs its own search")
	clientSearchNamespace := codexRequest()
	clientSearchNamespace.Params.Tools = append(clientSearchNamespace.Params.Tools, functionTool("ToolSearch", "search", false))
	assert.False(t, Candidate(clientSearchNamespace), "the client runs its own search over deferred namespace members too")
	defaultNamespaceSearch := codexRequest()
	functions := &defaultNamespaceSearch.Params.Tools[1].ResponsesToolNamespace.Tools
	*functions = append(*functions, functionTool("ToolSearch", "search", false))
	assert.False(t, Candidate(defaultNamespaceSearch), "a client search declared as functions.ToolSearch is the client's own search")

	assert.False(t, Candidate(&schemas.BifrostResponsesRequest{}))

	// A continuation only sends new input, so the tools the conversation already
	// loaded are invisible; deferred tools then go out eagerly instead.
	continuation := deferredRequest()
	continuation.Params.PreviousResponseID = schemas.Ptr("resp_prev")
	assert.False(t, Candidate(continuation), "a previous_response_id continuation hides earlier calls")

	// A conversation persists every turn, so internal search turns would be stored and
	// replayed again; deferred tools go out eagerly instead.
	conversation := deferredRequest()
	conversation.Params.Conversation = &schemas.ResponsesResponseConversation{ResponsesResponseConversationStr: schemas.Ptr("conv_1")}
	assert.False(t, Candidate(conversation), "a conversation-backed request would persist the search turns")
}

func clientToolSearch() schemas.ResponsesTool {
	return schemas.ResponsesTool{
		Type:                    schemas.ResponsesToolTypeToolSearch,
		ResponsesToolToolSearch: &schemas.ResponsesToolToolSearch{Execution: schemas.Ptr(schemas.ToolSearchExecutionClient)},
	}
}

// codexRequest is the shape Codex sends with supports_search_tool: a client
// tool_search, its default tools in the "functions" namespace, and an MCP server's
// tools in a namespace deferred as a whole.
func codexRequest() *schemas.BifrostResponsesRequest {
	req := deferredRequest()
	mcp := schemas.ResponsesTool{
		Type: schemas.ResponsesToolTypeNamespace, Name: schemas.Ptr("mcp__demo"), DeferLoading: schemas.Ptr(true),
		ResponsesToolNamespace: &schemas.ResponsesToolNamespace{Tools: []schemas.ResponsesTool{
			functionTool("get_weather", "Get the current weather for a city", false),
			functionTool("get_stock_price", "Get a stock quote", false),
		}},
	}
	req.Params.Tools = []schemas.ResponsesTool{
		clientToolSearch(),
		{
			Type: schemas.ResponsesToolTypeNamespace, Name: schemas.Ptr("functions"),
			ResponsesToolNamespace: &schemas.ResponsesToolNamespace{Tools: []schemas.ResponsesTool{functionTool("exec", "Run a command", false)}},
		},
		mcp,
	}
	return req
}

func memberNames(tool schemas.ResponsesTool) []string {
	return names(tool.ResponsesToolNamespace.Tools)
}

// The session holds back the deferred members of a namespace and drops the caller's
// tool_search, which the gateway serves. A search loads members under the flat name
// the model sees, and the namespace then goes out with only those, eagerly.
func TestSessionHoldsBackNamespaceMembers(t *testing.T) {
	req := codexRequest()
	s := NewSession(req)
	assert.Equal(t, []string{"functions"}, names(keptTools(s, req)), "tool_search dropped, the deferred namespace held back")
	assert.Contains(t, *s.Tool().Function.Description, "- mcp__demo__get_weather: Get the current weather for a city")

	result := s.Execute(searchCall(`{"query":"weather"}`))
	assert.Contains(t, *result.Content.ContentStr, "mcp__demo__get_weather")

	kept := keptTools(s, req)
	require.Equal(t, []string{"functions", "mcp__demo"}, names(kept))
	assert.Equal(t, []string{"get_weather"}, memberNames(kept[1]))
	assert.Nil(t, kept[1].DeferLoading, "the namespace goes out eagerly")
	assert.Nil(t, kept[1].ResponsesToolNamespace.Tools[0].DeferLoading, "a loaded member goes out eagerly")
	assert.Same(t, req.Params.Tools[1].ResponsesToolNamespace, kept[0].ResponsesToolNamespace, "a namespace with nothing deferred is not copied")
	assert.Len(t, req.Params.Tools[2].ResponsesToolNamespace.Tools, 2, "the caller's namespace is untouched")
	assert.True(t, *req.Params.Tools[2].DeferLoading, "the caller's namespace is untouched")

	byName := codexRequest()
	s = NewSession(byName)
	s.Execute(searchCall(`{"query":"select:mcp__demo__get_stock_price"}`))
	assert.Equal(t, []string{"get_stock_price"}, memberNames(keptTools(s, byName)[1]))
}

// A keyword only the namespace's description carries finds its members, as the
// flattened declaration carries that description too. The listing still leads with
// the member's own description.
func TestSessionSearchesNamespaceDescription(t *testing.T) {
	req := codexRequest()
	req.Params.Tools = append(req.Params.Tools, schemas.ResponsesTool{
		Type: schemas.ResponsesToolTypeNamespace, Name: schemas.Ptr("mcp__crm"), DeferLoading: schemas.Ptr(true),
		Description:            schemas.Ptr("Salesforce customer records"),
		ResponsesToolNamespace: &schemas.ResponsesToolNamespace{Tools: []schemas.ResponsesTool{functionTool("list_items", "List items", false)}},
	})
	s := NewSession(req)
	assert.Contains(t, *s.Tool().Function.Description, "- mcp__crm__list_items: List items")
	assert.NotContains(t, *s.Tool().Function.Description, "Salesforce", "the listing shows the member's own first line")

	result := s.Execute(searchCall(`{"query":"salesforce"}`))
	assert.Contains(t, *result.Content.ContentStr, "mcp__crm__list_items")
	kept := keptTools(s, req)
	require.Contains(t, names(kept), "mcp__crm")
	crm := kept[slices.Index(names(kept), "mcp__crm")]
	assert.Equal(t, "List items", *crm.ResponsesToolNamespace.Tools[0].Description, "the declaration sent upstream is the caller's")
}

// A namespace member the conversation already called (Codex echoes the namespace on
// the function_call) stays loaded on the next turn.
func TestSessionLoadsNamespaceMembersCalledInHistory(t *testing.T) {
	req := codexRequest()
	call := functionCall("call_w0", "get_weather", `{"city":"Paris"}`)
	call.Namespace = schemas.Ptr("mcp__demo")
	req.Input = append(req.Input, call)
	kept := keptTools(NewSession(req), req)
	require.Equal(t, []string{"functions", "mcp__demo"}, names(kept))
	assert.Equal(t, []string{"get_weather"}, memberNames(kept[1]))
}

// clientSearchOutput is a client tool_search_output item that loaded tools (raw JSON).
func clientSearchOutput(t *testing.T, callID, tools string) schemas.ResponsesMessage {
	var msg schemas.ResponsesMessage
	require.NoError(t, schemas.Unmarshal([]byte(`{"type":"tool_search_output","execution":"client","call_id":"`+callID+`","status":"completed","tools":`+tools+`}`), &msg))
	return msg
}

// demoMembers is a namespace of the given member names, as a search output lists it.
func demoMembers(names ...string) string {
	members := make([]string, 0, len(names))
	for _, name := range names {
		members = append(members, `{"type":"function","name":"`+name+`"}`)
	}
	return `[{"type":"namespace","name":"mcp__demo","tools":[` + strings.Join(members, ",") + `]}]`
}

// demoNamespaceRequest defers a namespace of members t0..t<n-1> plus get_weather.
func demoNamespaceRequest(n int) *schemas.BifrostResponsesRequest {
	req := codexRequest()
	ns := &req.Params.Tools[2].ResponsesToolNamespace.Tools
	for i := 0; i < n; i++ {
		*ns = append(*ns, functionTool(fmt.Sprintf("t%d", i), "demo", false))
	}
	return req
}

// A tool the conversation already called is loaded anyway, so it does not use up the
// allowance for tools loaded from the client's earlier searches.
func TestSessionHistoryAllowanceSkipsCalledTools(t *testing.T) {
	req := demoNamespaceRequest(MaxHistoryLoaded)
	newest := make([]string, 0, MaxHistoryLoaded)
	for i := 0; i < MaxHistoryLoaded; i++ {
		newest = append(newest, fmt.Sprintf("t%d", i))
	}
	req.Input = append(req.Input, clientSearchOutput(t, "call_1", demoMembers("get_weather")), clientSearchOutput(t, "call_2", demoMembers(newest...)))
	for i, name := range newest {
		call := functionCall(fmt.Sprintf("call_t%d", i), name, `{}`)
		call.Namespace = schemas.Ptr("mcp__demo")
		req.Input = append(req.Input, call)
	}
	kept := keptTools(NewSession(req), req)
	require.Equal(t, []string{"functions", "mcp__demo"}, names(kept))
	assert.Contains(t, memberNames(kept[1]), "get_weather", "the older search's tool fits: the called tools took no slots")
}

// A history call that names a deferred namespace member without its namespace keeps
// that member declared, resolved the same way a bare tool_choice name is.
func TestSessionHistoryLoadsBareCalledMember(t *testing.T) {
	req := codexRequest()
	req.Input = append(req.Input, functionCall("call_1", "get_weather", `{}`))
	kept := keptTools(NewSession(req), req)
	require.Contains(t, names(kept), "mcp__demo")
	assert.Equal(t, []string{"get_weather"}, memberNames(kept[slices.Index(names(kept), "mcp__demo")]))

	// A call with its namespace is keyed by it, and loads nothing elsewhere.
	req = codexRequest()
	call := functionCall("call_1", "get_weather", `{}`)
	call.Namespace = schemas.Ptr("mcp__other")
	req.Input = append(req.Input, call)
	assert.NotContains(t, names(keptTools(NewSession(req), req)), "mcp__demo")
}

// Eager tools a search output repeats, and tools the request no longer declares, are not
// held back, so they do not use up the allowance either.
func TestSessionHistoryAllowanceCountsOnlyDeferredTools(t *testing.T) {
	req := codexRequest()
	eager := make([]string, 0, MaxHistoryLoaded)
	for i := 0; i < MaxHistoryLoaded; i++ {
		name := fmt.Sprintf("e%d", i)
		eager = append(eager, `{"type":"function","name":"`+name+`"}`)
		req.Params.Tools = append(req.Params.Tools, functionTool(name, "eager", false))
	}
	gone := `{"type":"function","name":"removed_tool"}`
	req.Input = append(req.Input,
		clientSearchOutput(t, "call_1", demoMembers("get_weather")),
		clientSearchOutput(t, "call_2", `[`+strings.Join(append(eager, gone), ",")+`]`))
	kept := keptTools(NewSession(req), req)
	require.Contains(t, names(kept), "mcp__demo")
	assert.Equal(t, []string{"get_weather"}, memberNames(kept[slices.Index(names(kept), "mcp__demo")]))

	// Genuinely deferred tools still fill the allowance.
	full := demoNamespaceRequest(MaxHistoryLoaded + 5)
	listed := make([]string, 0, MaxHistoryLoaded+5)
	for i := 0; i < MaxHistoryLoaded+5; i++ {
		listed = append(listed, fmt.Sprintf("t%d", i))
	}
	full.Input = append(full.Input, clientSearchOutput(t, "call_1", demoMembers(listed...)))
	kept = keptTools(NewSession(full), full)
	assert.Len(t, memberNames(kept[1]), MaxHistoryLoaded)
}

// Only a function a search output lists loads a deferred function: a custom tool of the
// same name is a different tool, and loading for it would also spend the allowance.
func TestSessionHistoryLoadsOnlyFunctionResults(t *testing.T) {
	req := deferredRequest()
	req.Input = append(req.Input, clientSearchOutput(t, "call_1", `[{"type":"custom","name":"get_weather"}]`))
	assert.Equal(t, []string{"lookup_city"}, names(keptTools(NewSession(req), req)), "a custom result does not load the get_weather function")

	ns := demoNamespaceRequest(1)
	ns.Input = append(ns.Input, clientSearchOutput(t, "call_1", `[{"type":"namespace","name":"mcp__demo","tools":[{"type":"custom","name":"t0"}]}]`))
	assert.Equal(t, []string{"functions"}, names(keptTools(NewSession(ns), ns)), "a custom member does not load the t0 function")

	loaded := deferredRequest()
	loaded.Input = append(loaded.Input, clientSearchOutput(t, "call_1", `[{"type":"function","name":"get_weather"}]`))
	assert.Equal(t, []string{"lookup_city", "get_weather"}, names(keptTools(NewSession(loaded), loaded)))
}

func TestNativeSupport(t *testing.T) {
	for _, tc := range []struct {
		provider schemas.ModelProvider
		model    string
		want     bool
	}{
		{schemas.OpenAI, "gpt-6-sol", true},
		{schemas.Azure, "gpt-5.4", true},
		{schemas.OpenAI, "gpt-4o", false},
		{schemas.Anthropic, "claude-sonnet-4-6", true},
		{schemas.Vertex, "claude-sonnet-4-6", true},
		{schemas.Vertex, "gemini-3.8-flash", false},
		{schemas.Gemini, "gemini-3.8-flash", false},
		{schemas.Groq, "openai/gpt-oss-120b", false},
	} {
		assert.Equal(t, tc.want, NativeSupport(tc.provider, tc.model), "%s/%s", tc.provider, tc.model)
	}
}

// A search loads its matches: the next turn sends them, eagerly, and the result
// names them for the model.
func TestSessionExecuteLoadsMatches(t *testing.T) {
	req := deferredRequest()
	s := NewSession(req)
	assert.Equal(t, []string{"lookup_city"}, names(keptTools(s, req)), "deferred tools are held back")

	result := s.Execute(searchCall(`{"query":"weather"}`))
	require.NotNil(t, result.ChatToolMessage)
	assert.Equal(t, "call_s1", *result.ChatToolMessage.ToolCallID)
	assert.Equal(t, schemas.ChatMessageRoleTool, result.Role)
	assert.Contains(t, *result.Content.ContentStr, "get_weather")

	kept := keptTools(s, req)
	assert.Equal(t, []string{"lookup_city", "get_weather"}, names(kept))
	assert.Nil(t, kept[1].DeferLoading, "a loaded tool goes out eagerly")
	assert.True(t, *req.Params.Tools[1].DeferLoading, "the caller's tool is untouched")
}

func TestSessionExecuteReportsNoMatch(t *testing.T) {
	req := deferredRequest()
	s := NewSession(req)
	result := s.Execute(searchCall(`{"query":"zzz"}`))
	assert.Contains(t, *result.Content.ContentStr, "No tool matched")
	assert.Equal(t, []string{"lookup_city"}, names(keptTools(s, req)))
}

// A deferred tool the conversation already called stays loaded on the next turn.
func TestSessionLoadsToolsCalledInHistory(t *testing.T) {
	req := deferredRequest()
	req.Input = append(req.Input, functionCall("call_w0", "get_weather", `{"city":"Paris"}`))
	assert.Equal(t, []string{"lookup_city", "get_weather"}, names(keptTools(NewSession(req), req)))
}

// A tool_choice that names a deferred function needs that tool declared from the
// first turn, or the request pins a tool it never sends.
func TestSessionLoadsToolsNamedByToolChoice(t *testing.T) {
	forced := deferredRequest()
	forced.Params.ToolChoice = &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{
		Type: schemas.ResponsesToolChoiceTypeFunction, Name: schemas.Ptr("get_weather"),
	}}
	assert.Equal(t, []string{"lookup_city", "get_weather"}, names(keptTools(NewSession(forced), forced)))

	allowed := deferredRequest()
	allowed.Params.ToolChoice = &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{
		Type: schemas.ResponsesToolChoiceTypeAllowedTools, Mode: schemas.Ptr("required"),
		Tools: []schemas.ResponsesToolChoiceAllowedToolDef{
			{Type: "function", Name: schemas.Ptr("get_stock_price")},
			{Type: "mcp", ServerLabel: schemas.Ptr("crm")},
		},
	}}
	assert.Equal(t, []string{"lookup_city", "get_stock_price"}, names(keptTools(NewSession(allowed), allowed)))

	custom := deferredRequest()
	custom.Params.ToolChoice = &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{
		Type: schemas.ResponsesToolChoiceTypeCustom, Name: schemas.Ptr("get_weather"),
	}}
	assert.Equal(t, []string{"lookup_city"}, names(keptTools(NewSession(custom), custom)), "a custom tool choice does not load a function")
}

// A tool_choice may name a deferred namespace member by its bare name, which the
// flattening step later rewrites to the member's alias. The session loads that member
// so it is declared. A top-level function of the same name wins, as it does when the
// choice is realiased, and a name several namespaces share loads them all so the
// flattening step reports the ambiguity instead of the upstream rejecting an unknown name.
func TestSessionLoadsNamespaceMembersNamedByToolChoice(t *testing.T) {
	forcedChoice := func(name string) *schemas.ResponsesToolChoice {
		return &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{
			Type: schemas.ResponsesToolChoiceTypeFunction, Name: schemas.Ptr(name),
		}}
	}

	forced := codexRequest()
	forced.Params.ToolChoice = forcedChoice("get_weather")
	kept := keptTools(NewSession(forced), forced)
	require.Equal(t, []string{"functions", "mcp__demo"}, names(kept), "the bare name loads its deferred member")
	assert.Equal(t, []string{"get_weather"}, memberNames(kept[1]))

	qualified := codexRequest()
	qualified.Params.ToolChoice = forcedChoice("mcp__demo__get_stock_price")
	kept = keptTools(NewSession(qualified), qualified)
	require.Equal(t, []string{"functions", "mcp__demo"}, names(kept), "an aliased name loads its member")
	assert.Equal(t, []string{"get_stock_price"}, memberNames(kept[1]))

	allowed := codexRequest()
	allowed.Params.ToolChoice = &schemas.ResponsesToolChoice{ResponsesToolChoiceStruct: &schemas.ResponsesToolChoiceStruct{
		Type: schemas.ResponsesToolChoiceTypeAllowedTools, Mode: schemas.Ptr("required"),
		Tools: []schemas.ResponsesToolChoiceAllowedToolDef{{Type: "function", Name: schemas.Ptr("get_stock_price")}},
	}}
	kept = keptTools(NewSession(allowed), allowed)
	require.Equal(t, []string{"functions", "mcp__demo"}, names(kept), "an allowed_tools name loads its deferred member")
	assert.Equal(t, []string{"get_stock_price"}, memberNames(kept[1]))

	topLevel := codexRequest()
	topLevel.Params.Tools = append(topLevel.Params.Tools, functionTool("get_weather", "Top-level weather", true))
	topLevel.Params.ToolChoice = forcedChoice("get_weather")
	assert.Equal(t, []string{"functions", "get_weather"}, names(keptTools(NewSession(topLevel), topLevel)), "a top-level function of the same name wins")

	ambiguous := codexRequest()
	ambiguous.Params.Tools = append(ambiguous.Params.Tools, schemas.ResponsesTool{
		Type: schemas.ResponsesToolTypeNamespace, Name: schemas.Ptr("mcp__other"),
		ResponsesToolNamespace: &schemas.ResponsesToolNamespace{Tools: []schemas.ResponsesTool{
			functionTool("get_weather", "Other weather", true),
		}},
	})
	ambiguous.Params.ToolChoice = forcedChoice("get_weather")
	kept = keptTools(NewSession(ambiguous), ambiguous)
	require.Equal(t, []string{"functions", "mcp__demo", "mcp__other"}, names(kept), "a shared bare name loads every match")
	assert.Equal(t, []string{"get_weather"}, memberNames(kept[1]))
	assert.Equal(t, []string{"get_weather"}, memberNames(kept[2]))
}

func TestSessionToolListsDeferredTools(t *testing.T) {
	tool := NewSession(deferredRequest()).Tool()
	require.NotNil(t, tool.Function)
	assert.Equal(t, ToolName, tool.Function.Name)
	assert.Contains(t, *tool.Function.Description, "- get_weather: Get the current weather for a city")
	assert.Contains(t, *tool.Function.Description, "- get_stock_price: Get a stock quote")
	assert.NotContains(t, *tool.Function.Description, "lookup_city", "eager tools are not listed")
	assert.Equal(t, []string{"query"}, tool.Function.Parameters.Required)
}

// The injected tool loop runs a turn's calls in parallel.
func TestSessionConcurrentExecute(t *testing.T) {
	req := deferredRequest()
	s := NewSession(req)
	done := make(chan struct{})
	for _, q := range []string{"weather", "stock", "weather", "stock"} {
		go func() {
			defer func() { done <- struct{}{} }()
			s.Execute(searchCall(`{"query":"` + q + `"}`))
			keptTools(s, req)
		}()
	}
	for range 4 {
		<-done
	}
	assert.Equal(t, []string{"lookup_city", "get_weather", "get_stock_price"}, names(keptTools(s, req)))
}

func TestSessionToolCapsTheListing(t *testing.T) {
	req := deferredRequest()
	req.Params.Tools = nil
	for i := 0; i < maxListedTools+5; i++ {
		req.Params.Tools = append(req.Params.Tools, functionTool("tool_"+strings.Repeat("x", i%3), "line one\nline two", true))
	}
	desc := *NewSession(req).Tool().Function.Description
	assert.Contains(t, desc, "- tool_x: line one")
	assert.NotContains(t, desc, "line two")
	assert.Contains(t, desc, "... and 5 more")
}

// The check every Responses request pays: no allocation when no tool is deferred.
func BenchmarkCandidateNoDeferredTools(b *testing.B) {
	req := deferredRequest()
	req.Params.Tools = []schemas.ResponsesTool{functionTool("lookup_city", "Look up a city", false), functionTool("get_weather", "weather", false)}
	b.ReportAllocs()
	for b.Loop() {
		if Candidate(req) {
			b.Fatal("unexpected candidate")
		}
	}
}

func BenchmarkCandidateNoTools(b *testing.B) {
	req := &schemas.BifrostResponsesRequest{Params: &schemas.ResponsesParameters{}}
	b.ReportAllocs()
	for b.Loop() {
		if Candidate(req) {
			b.Fatal("unexpected candidate")
		}
	}
}

// A Codex request with nothing deferred: Candidate now walks namespace members instead
// of returning at the tool_search, and must still not allocate.
func BenchmarkCandidateEagerCodexNamespaces(b *testing.B) {
	req := codexRequest()
	req.Params.Tools[2].DeferLoading = nil
	for i := range 100 {
		req.Params.Tools[2].ResponsesToolNamespace.Tools = append(req.Params.Tools[2].ResponsesToolNamespace.Tools,
			functionTool(fmt.Sprintf("tool_%d", i), "eager", false))
	}
	b.ReportAllocs()
	for b.Loop() {
		if Candidate(req) {
			b.Fatal("unexpected candidate")
		}
	}
}
