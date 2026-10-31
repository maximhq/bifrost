package toolsearch

import (
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

	withSearch := deferredRequest()
	withSearch.Params.Tools = append(withSearch.Params.Tools, schemas.ResponsesTool{Type: schemas.ResponsesToolTypeToolSearch})
	assert.False(t, Candidate(withSearch), "the request brings its own tool_search")

	clientSearch := deferredRequest()
	clientSearch.Params.Tools = append(clientSearch.Params.Tools, functionTool("ToolSearch", "search", false))
	assert.False(t, Candidate(clientSearch), "the client runs its own search")

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
