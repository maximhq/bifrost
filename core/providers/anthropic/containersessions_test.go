package anthropic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const codeExecutionRequest = `{"model":"claude-sonnet-5-5","max_tokens":512,"messages":[],"tools":[{"name":"code_execution","type":"code_execution_20250825"}]}`

const codeExecutionResponse = `{"model":"claude-sonnet-5-5","id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"80779853376"}],"container":{"id":"container_01","expires_at":"2026-11-04T03:44:30.416269Z"},"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":6,"cache_creation_input_tokens":2971,"cache_read_input_tokens":2884,"output_tokens":71,"server_tool_use":{"web_search_requests":0,"web_fetch_requests":0}}}`

func TestIsBillableAnthropicContainer(t *testing.T) {
	tests := []struct {
		name, request string
		want          bool
	}{
		{"code execution alone", codeExecutionRequest, true},
		{"old web search keeps it billable", `{"tools":[{"type":"code_execution_20250825"},{"type":"web_search_20250305"}]}`, true},
		{"old web fetch keeps it billable", `{"tools":[{"type":"code_execution_20250825"},{"type":"web_fetch_20250910"}]}`, true},
		{"free with web_search_20260209", `{"tools":[{"type":"code_execution_20260120"},{"type":"web_search_20260209"}]}`, false},
		{"free with web_fetch_20260209", `{"tools":[{"type":"web_fetch_20260209"}]}`, false},
		{"free with a later web tool", `{"tools":[{"type":"web_fetch_20260318"}]}`, false},
		{"same container by id is a reuse", `{"container":"container_01","tools":[{"type":"code_execution_20250825"}]}`, false},
		{"same container by object is a reuse", `{"container":{"id":"container_01"},"tools":[{"type":"code_execution_20250825"}]}`, false},
		{"expired container replaced by a new one", `{"container":"container_00","tools":[{"type":"code_execution_20250825"}]}`, true},
		{"skills object without id", `{"container":{"skills":[{"type":"anthropic","skill_id":"pptx"}]},"tools":[{"type":"code_execution_20250825"}]}`, true},
		{"unreadable request", `not json`, false},
		{"empty request", ``, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isBillableAnthropicContainer([]byte(tt.request), "container_01"))
		})
	}
}

func TestExtractAnthropicPassthroughUsage_ContainerSession(t *testing.T) {
	u := ExtractAnthropicPassthroughUsage("/v1/messages", []byte(codeExecutionRequest), []byte(codeExecutionResponse))
	require.NotNil(t, u)
	require.NotNil(t, u.LLMUsage.ToolUsage)
	assert.Equal(t, map[string]int{schemas.ContainerTierDefault: 1}, u.LLMUsage.ToolUsage.ContainerSessions)

	free := `{"tools":[{"type":"web_search_20260209"}]}`
	u = ExtractAnthropicPassthroughUsage("/v1/messages", []byte(free), []byte(codeExecutionResponse))
	require.NotNil(t, u)
	assert.Nil(t, u.LLMUsage.ToolUsage)
}

func TestAnthropicPassthroughStreamUsage_ContainerSession(t *testing.T) {
	stream := &AnthropicPassthroughStreamUsage{RequestBody: []byte(codeExecutionRequest)}
	stream.ObserveEvent([]byte(`{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-sonnet-5-5","usage":{"input_tokens":6,"output_tokens":1}}}`))
	u := stream.ObserveEvent([]byte(`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null,"container":{"id":"container_01"}},"usage":{"output_tokens":71}}`))
	require.NotNil(t, u)
	require.NotNil(t, u.LLMUsage.ToolUsage)
	assert.Equal(t, map[string]int{schemas.ContainerTierDefault: 1}, u.LLMUsage.ToolUsage.ContainerSessions)
}

func BenchmarkIsBillableAnthropicContainer(b *testing.B) {
	req := []byte(codeExecutionRequest)
	b.ReportAllocs()
	for b.Loop() {
		isBillableAnthropicContainer(req, "container_01")
	}
}

const codeExecutionStream = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","content":[],"model":"claude-sonnet-5-5","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":6,"output_tokens":1}}}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null,"container":{"id":"container_01"}},"usage":{"output_tokens":71}}` + "\n\n" +
	"event: message_stop\n" + `data: {"type":"message_stop"}` + "\n\n"

func containerTestServer(contentType, body string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write([]byte(body))
	}))
}

func containerTestContext() (*schemas.BifrostContext, context.CancelFunc) {
	return schemas.NewBifrostContextWithCancel(context.Background())
}

func containerResponsesRequest(toolVersion string) *schemas.BifrostResponsesRequest {
	return &schemas.BifrostResponsesRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-sonnet-5-5",
		Input: []schemas.ResponsesMessage{{
			Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")},
		}},
		Params: &schemas.ResponsesParameters{Tools: []schemas.ResponsesTool{{
			Type:                         schemas.ResponsesToolTypeCodeInterpreter,
			ResponsesToolCodeInterpreter: &schemas.ResponsesToolCodeInterpreter{Version: schemas.Ptr(toolVersion)},
		}}},
	}
}

func containerChatRequest(t *testing.T) *schemas.BifrostChatRequest {
	t.Helper()
	var tool schemas.ChatTool
	require.NoError(t, sonic.Unmarshal([]byte(`{"type":"code_execution_20250825","name":"code_execution"}`), &tool))
	return &schemas.BifrostChatRequest{
		Provider: schemas.Anthropic,
		Model:    "claude-sonnet-5-5",
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}},
		Params:   &schemas.ChatParameters{Tools: []schemas.ChatTool{tool}},
	}
}

func TestResponses_RecordsContainerSession(t *testing.T) {
	server := containerTestServer("application/json", codeExecutionResponse)
	defer server.Close()
	ctx, cancel := containerTestContext()
	defer cancel()

	resp, bifrostErr := newTruncationTestProvider(server.URL).Responses(ctx, schemas.Key{}, containerResponsesRequest("code_execution_20250825"))
	require.Nil(t, bifrostErr)
	require.NotNil(t, resp.Usage.ToolUsage)
	assert.Equal(t, map[string]int{schemas.ContainerTierDefault: 1}, resp.Usage.ToolUsage.ContainerSessions)
}

func TestResponsesStream_RecordsContainerSession(t *testing.T) {
	server := containerTestServer("text/event-stream", codeExecutionStream)
	defer server.Close()
	ctx, cancel := containerTestContext()
	defer cancel()

	stream, bifrostErr := newTruncationTestProvider(server.URL).ResponsesStream(ctx, truncationPassthroughPostHook, nil, schemas.Key{}, containerResponsesRequest("code_execution_20250825"))
	require.Nil(t, bifrostErr)
	var usage *schemas.ResponsesResponseUsage
	for _, chunk := range collectTruncationChunks(t, stream) {
		if r := chunk.BifrostResponsesStreamResponse; r != nil && r.Response != nil && r.Response.Usage != nil {
			usage = r.Response.Usage
		}
	}
	require.NotNil(t, usage)
	require.NotNil(t, usage.ToolUsage)
	assert.Equal(t, map[string]int{schemas.ContainerTierDefault: 1}, usage.ToolUsage.ContainerSessions)
}

func TestChatCompletion_RecordsContainerSession(t *testing.T) {
	server := containerTestServer("application/json", codeExecutionResponse)
	defer server.Close()
	ctx, cancel := containerTestContext()
	defer cancel()

	resp, bifrostErr := newTruncationTestProvider(server.URL).ChatCompletion(ctx, schemas.Key{}, containerChatRequest(t))
	require.Nil(t, bifrostErr)
	require.NotNil(t, resp.Usage.ToolUsage)
	assert.Equal(t, map[string]int{schemas.ContainerTierDefault: 1}, resp.Usage.ToolUsage.ContainerSessions)
}

func TestChatCompletionStream_RecordsContainerSession(t *testing.T) {
	server := containerTestServer("text/event-stream", codeExecutionStream)
	defer server.Close()
	ctx, cancel := containerTestContext()
	defer cancel()

	stream, bifrostErr := newTruncationTestProvider(server.URL).ChatCompletionStream(ctx, truncationPassthroughPostHook, nil, schemas.Key{}, containerChatRequest(t))
	require.Nil(t, bifrostErr)
	var usage *schemas.BifrostLLMUsage
	for _, chunk := range collectTruncationChunks(t, stream) {
		if r := chunk.BifrostChatResponse; r != nil && r.Usage != nil {
			usage = r.Usage
		}
	}
	require.NotNil(t, usage)
	require.NotNil(t, usage.ToolUsage)
	assert.Equal(t, map[string]int{schemas.ContainerTierDefault: 1}, usage.ToolUsage.ContainerSessions)
}
