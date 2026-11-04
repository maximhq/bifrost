package openai

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const autoContainerRequest = `{"model":"gpt-6.1-sol","store":false,"input":[{"role":"user","content":"compute"}],"tools":[{"type":"code_interpreter","container":{"memory_limit":"1g","type":"auto"}}]}`

const codeInterpreterResponse = `{"id":"resp_1","object":"response","created_at":1,"status":"completed","output":[
	{"id":"ci_1","type":"code_interpreter_call","status":"completed","code":"1+1","container_id":"cntr_new","outputs":null},
	{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"2","annotations":[]}]}
],"tool_usage":{"web_search":{"num_requests":0}},"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}`

func parseOutput(t *testing.T, body string) []schemas.ResponsesMessage {
	t.Helper()
	var resp schemas.BifrostResponsesResponse
	require.NoError(t, sonic.Unmarshal([]byte(body), &resp))
	return resp.Output
}

func TestCountContainerSessions(t *testing.T) {
	codeCall := `{"output":[{"type":"code_interpreter_call","container_id":"cntr_new","status":"completed"}]}`
	twoCalls := `{"output":[{"type":"code_interpreter_call","container_id":"cntr_new","status":"completed"},{"type":"code_interpreter_call","container_id":"cntr_new","status":"completed"},{"type":"code_interpreter_call","container_id":"cntr_b","status":"completed"}]}`
	shellCall := `{"output":[{"type":"shell_call","call_id":"c1","status":"completed","action":{"commands":["ls"]}},{"type":"shell_call","call_id":"c2","status":"completed","action":{"commands":["pwd"]}}]}`
	shellCallWithID := `{"output":[{"type":"shell_call","call_id":"c1","status":"completed","action":{"commands":["ls"]},"environment":{"type":"container_reference","container_id":"cntr_new"}}]}`
	shellAuto := `{"input":"go","tools":[{"type":"shell","environment":{"type":"container_auto","memory_limit":"16g"}}]}`

	tests := []struct {
		name, request, response string
		want                    map[string]int
	}{
		{"auto with tier", autoContainerRequest, codeCall, map[string]int{"1g": 1}},
		{"auto without tier", `{"input":"go","tools":[{"type":"code_interpreter","container":{"type":"auto"}}]}`, codeCall, map[string]int{"default": 1}},
		{"higher tier", `{"input":"go","tools":[{"type":"code_interpreter","container":{"type":"auto","memory_limit":"4g"}}]}`, codeCall, map[string]int{"4g": 1}},
		{"calls in one container count once", autoContainerRequest, twoCalls, map[string]int{"1g": 2}},
		{"explicit container id was billed at create", `{"input":"go","tools":[{"type":"code_interpreter","container":"cntr_new"}]}`, codeCall, nil},
		{"container replayed in input is a reuse", `{"input":[{"type":"code_interpreter_call","container_id":"cntr_new"}],"tools":[{"type":"code_interpreter","container":{"type":"auto"}}]}`, codeCall, nil},
		{"replayed input with an expired container", `{"input":[{"type":"code_interpreter_call","container_id":"cntr_old"}],"tools":[{"type":"code_interpreter","container":{"type":"auto"}}]}`, codeCall, map[string]int{"default": 1}},
		{"previous_response_id is treated as reuse", `{"previous_response_id":"resp_0","input":"go","tools":[{"type":"code_interpreter","container":{"type":"auto"}}]}`, codeCall, nil},
		{"conversation is treated as reuse", `{"conversation":"conv_1","input":"go","tools":[{"type":"code_interpreter","container":{"type":"auto"}}]}`, codeCall, nil},
		{"null conversation is not reuse", `{"conversation":null,"input":"go","tools":[{"type":"code_interpreter","container":{"type":"auto"}}]}`, codeCall, map[string]int{"default": 1}},
		{"unused container is free", autoContainerRequest, `{"output":[{"type":"message","role":"assistant","content":[]}]}`, nil},
		{"unreadable request", `not json`, codeCall, nil},
		{"empty request", ``, codeCall, nil},
		{"shell auto without id counts once", shellAuto, shellCall, map[string]int{"16g": 1}},
		{"shell auto with id", shellAuto, shellCallWithID, map[string]int{"16g": 1}},
		{"shell container_reference was billed at create", `{"input":"go","tools":[{"type":"shell","environment":{"type":"container_reference","container_id":"cntr_new"}}]}`, shellCallWithID, nil},
		{"local shell is free", `{"input":"go","tools":[{"type":"shell","environment":{"type":"local"}}]}`, shellCall, nil},
		{"shell already in context", `{"input":[{"type":"shell_call","call_id":"c0"}],"tools":[{"type":"shell","environment":{"type":"container_auto"}}]}`, shellCall, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, countContainerSessions([]byte(tt.request), parseOutput(t, tt.response)))
		})
	}
}

func TestRecordResponsesContainerSessions_ReportedPayload(t *testing.T) {
	var resp schemas.BifrostResponsesResponse
	require.NoError(t, sonic.Unmarshal([]byte(codeInterpreterResponse), &resp))

	recordResponsesServerToolUsage(&resp, []byte(autoContainerRequest))

	require.NotNil(t, resp.Usage.ToolUsage)
	assert.Equal(t, map[string]int{"1g": 1}, resp.Usage.ToolUsage.ContainerSessions)

	wire, err := sonic.Marshal(resp)
	require.NoError(t, err)
	assert.NotContains(t, string(wire), "container_sessions", "derived counts must not reach clients")
	assert.Contains(t, string(wire), `"tool_usage":{"web_search":{"num_requests":0}}`)
}

func TestExtractOpenAIPassthroughUsage_ContainerSessions(t *testing.T) {
	t.Run("non streaming", func(t *testing.T) {
		u := ExtractOpenAIPassthroughUsage("POST", "/v1/responses", []byte(autoContainerRequest), []byte(codeInterpreterResponse))
		require.NotNil(t, u)
		require.NotNil(t, u.LLMUsage.ToolUsage)
		assert.Equal(t, map[string]int{"1g": 1}, u.LLMUsage.ToolUsage.ContainerSessions)
	})
	t.Run("response.completed event", func(t *testing.T) {
		event := `{"type":"response.completed","response":` + codeInterpreterResponse + `}`
		u := ExtractOpenAIPassthroughUsage("POST", "/v1/responses", []byte(autoContainerRequest), []byte(event))
		require.NotNil(t, u)
		require.NotNil(t, u.LLMUsage.ToolUsage)
		assert.Equal(t, map[string]int{"1g": 1}, u.LLMUsage.ToolUsage.ContainerSessions)
	})
	t.Run("no container tool call", func(t *testing.T) {
		body := `{"output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
		u := ExtractOpenAIPassthroughUsage("POST", "/v1/responses", []byte(autoContainerRequest), []byte(body))
		require.NotNil(t, u)
		assert.Nil(t, u.LLMUsage.ToolUsage)
	})
}

func BenchmarkCountContainerSessions_NoContainerTool(b *testing.B) {
	msg := schemas.ResponsesMessageTypeMessage
	output := []schemas.ResponsesMessage{{Type: &msg}, {Type: &msg}}
	req := []byte(autoContainerRequest)
	b.ReportAllocs()
	for b.Loop() {
		countContainerSessions(req, output)
	}
}

func BenchmarkCountContainerSessions_AutoContainer(b *testing.B) {
	var resp schemas.BifrostResponsesResponse
	if err := sonic.Unmarshal([]byte(codeInterpreterResponse), &resp); err != nil {
		b.Fatal(err)
	}
	req := []byte(autoContainerRequest)
	b.ReportAllocs()
	for b.Loop() {
		countContainerSessions(req, resp.Output)
	}
}

func containerSessionsRequest4g() *schemas.BifrostResponsesRequest {
	return &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-6.1-sol",
		Input: []schemas.ResponsesMessage{{
			Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")},
		}},
		Params: &schemas.ResponsesParameters{Tools: []schemas.ResponsesTool{{
			Type: schemas.ResponsesToolTypeCodeInterpreter,
			ResponsesToolCodeInterpreter: &schemas.ResponsesToolCodeInterpreter{
				Container: map[string]any{"type": "auto", "memory_limit": "4g"},
			},
		}}},
	}
}

func TestResponses_RecordsContainerSessions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(codeInterpreterResponse))
	}))
	defer server.Close()

	resp, bifrostErr := newStreamTestProvider(server.URL).Responses(newStreamTestContext(), testKey(), containerSessionsRequest4g())
	require.Nil(t, bifrostErr)
	require.NotNil(t, resp.Usage.ToolUsage)
	assert.Equal(t, map[string]int{"4g": 1}, resp.Usage.ToolUsage.ContainerSessions)
}

func TestResponsesStream_RecordsContainerSessions(t *testing.T) {
	server := completeSSEServer(t, "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":2,\"response\":"+
		strings.ReplaceAll(codeInterpreterResponse, "\n", "")+"}\n\n")
	defer server.Close()

	stream, bifrostErr := newStreamTestProvider(server.URL).ResponsesStream(newStreamTestContext(), passthroughPostHook, nil, testKey(), containerSessionsRequest4g())
	require.Nil(t, bifrostErr)
	chunks := collectChunks(t, stream)
	require.NotEmpty(t, chunks)
	last := chunks[len(chunks)-1].BifrostResponsesStreamResponse
	require.NotNil(t, last)
	require.NotNil(t, last.Response.Usage.ToolUsage)
	assert.Equal(t, map[string]int{"4g": 1}, last.Response.Usage.ToolUsage.ContainerSessions)
}

const imageToolResponse = `{"id":"resp_2","object":"response","created_at":1,"status":"completed","output":[],` +
	`"tool_usage":{"image_gen":{"input_tokens":100,"input_tokens_details":{"image_tokens":0,"text_tokens":100},"output_tokens":5000,"output_tokens_details":{"image_tokens":5000,"text_tokens":0},"total_tokens":5100},"web_search":{"num_requests":0}},` +
	`"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}`

const imageToolRequest = `{"model":"gpt-6.1-sol","input":"draw","tools":[{"type":"image_generation","model":"gpt-image-1-mini"}]}`

func TestRecordResponsesServerToolUsage_ImageGen(t *testing.T) {
	var resp schemas.BifrostResponsesResponse
	require.NoError(t, sonic.Unmarshal([]byte(imageToolResponse), &resp))
	recordResponsesServerToolUsage(&resp, []byte(imageToolRequest))

	require.NotNil(t, resp.Usage.ToolUsage)
	require.NotNil(t, resp.Usage.ToolUsage.ImageGen)
	assert.Equal(t, 5000, resp.Usage.ToolUsage.ImageGen.OutputTokensDetails.ImageTokens)
	assert.Equal(t, "gpt-image-1-mini", resp.Usage.ToolUsage.ImageGenModel)

	wire, err := sonic.Marshal(resp)
	require.NoError(t, err)
	assert.Contains(t, string(wire), `"image_gen":{"input_tokens":100`, "clients keep OpenAI's image_gen block")
	assert.NotContains(t, string(wire), "image_gen_model", "derived model must not reach clients")
}

func TestRecordImageGenToolModel(t *testing.T) {
	withTokens := func() *schemas.ToolUsage {
		return &schemas.ToolUsage{ImageGen: &schemas.ImageGenToolUsage{TotalTokens: 5}}
	}
	t.Run("tool without a model uses the API default", func(t *testing.T) {
		u := withTokens()
		recordImageGenToolModel(u, []byte(`{"tools":[{"type":"image_generation"}]}`))
		assert.Empty(t, u.ImageGenModel)
	})
	t.Run("ignores the other tools", func(t *testing.T) {
		u := withTokens()
		recordImageGenToolModel(u, []byte(`{"model":"gpt-6.1-sol","tools":[{"type":"function","name":"f"},{"type":"image_generation","model":"gpt-image-2.5-flare"}]}`))
		assert.Equal(t, "gpt-image-2.5-flare", u.ImageGenModel)
	})
	t.Run("zero counts skip the parse", func(t *testing.T) {
		u := &schemas.ToolUsage{ImageGen: &schemas.ImageGenToolUsage{}}
		recordImageGenToolModel(u, []byte(imageToolRequest))
		assert.Empty(t, u.ImageGenModel)
	})
}

func TestExtractOpenAIPassthroughUsage_ImageGen(t *testing.T) {
	for name, body := range map[string]string{
		"non streaming":            imageToolResponse,
		"response.completed event": `{"type":"response.completed","response":` + imageToolResponse + `}`,
	} {
		t.Run(name, func(t *testing.T) {
			u := ExtractOpenAIPassthroughUsage("POST", "/v1/responses", []byte(imageToolRequest), []byte(body))
			require.NotNil(t, u)
			require.NotNil(t, u.LLMUsage.ToolUsage)
			require.NotNil(t, u.LLMUsage.ToolUsage.ImageGen)
			assert.Equal(t, 5100, u.LLMUsage.ToolUsage.ImageGen.TotalTokens)
			assert.Equal(t, "gpt-image-1-mini", u.LLMUsage.ToolUsage.ImageGenModel)
		})
	}
}

func imageToolBifrostRequest() *schemas.BifrostResponsesRequest {
	req := containerSessionsRequest4g()
	req.Params.Tools = []schemas.ResponsesTool{{
		Type:                         schemas.ResponsesToolTypeImageGeneration,
		ResponsesToolImageGeneration: &schemas.ResponsesToolImageGeneration{Model: schemas.Ptr("gpt-image-1-mini")},
	}}
	return req
}

func TestResponses_RecordsImageGen(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(imageToolResponse))
	}))
	defer server.Close()

	resp, bifrostErr := newStreamTestProvider(server.URL).Responses(newStreamTestContext(), testKey(), imageToolBifrostRequest())
	require.Nil(t, bifrostErr)
	require.NotNil(t, resp.Usage.ToolUsage)
	assert.Equal(t, "gpt-image-1-mini", resp.Usage.ToolUsage.ImageGenModel)
}

func TestResponsesStream_RecordsImageGen(t *testing.T) {
	server := completeSSEServer(t, "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":2,\"response\":"+imageToolResponse+"}\n\n")
	defer server.Close()

	stream, bifrostErr := newStreamTestProvider(server.URL).ResponsesStream(newStreamTestContext(), passthroughPostHook, nil, testKey(), imageToolBifrostRequest())
	require.Nil(t, bifrostErr)
	chunks := collectChunks(t, stream)
	require.NotEmpty(t, chunks)
	last := chunks[len(chunks)-1].BifrostResponsesStreamResponse
	require.NotNil(t, last)
	require.NotNil(t, last.Response.Usage.ToolUsage)
	assert.Equal(t, 5000, last.Response.Usage.ToolUsage.ImageGen.OutputTokens)
	assert.Equal(t, "gpt-image-1-mini", last.Response.Usage.ToolUsage.ImageGenModel)
}

func BenchmarkRecordImageGenToolModel_NoImages(b *testing.B) {
	u := &schemas.ToolUsage{ImageGen: &schemas.ImageGenToolUsage{}}
	req := []byte(imageToolRequest)
	b.ReportAllocs()
	for b.Loop() {
		recordImageGenToolModel(u, req)
	}
}

func BenchmarkRecordImageGenToolModel_WithImages(b *testing.B) {
	u := &schemas.ToolUsage{ImageGen: &schemas.ImageGenToolUsage{TotalTokens: 5}}
	req := []byte(imageToolRequest)
	b.ReportAllocs()
	for b.Loop() {
		recordImageGenToolModel(u, req)
	}
}
