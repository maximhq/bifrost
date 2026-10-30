package openai

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/valyala/fasthttp"
)

// booleanSchemaToolParameters carries boolean subschemas next to boolean keyword values
// and boolean instance data, which must survive the rewrite as booleans.
const booleanSchemaToolParameters = `{"type":"object","properties":{` +
	`"state":true,"denied":false,` +
	`"tags":{"type":"array","items":true},` +
	`"choice":{"anyOf":[{"type":"array","items":true},{"type":"null"}]},` +
	`"strict":{"type":"object","additionalProperties":false},` +
	`"open":{"type":"object","additionalProperties":true},` +
	`"flag":{"type":"boolean","enum":[true,false],"const":true,"default":true}` +
	`},"required":["state"],"additionalProperties":false}`

const booleanSchemaModel = "acme-model-v1"

var booleanSchemaModes = []string{"chat", "chat-stream", "responses", "responses-stream"}

// booleanSchemaChatRequest builds a caller-owned request with nested boolean schemas.
func booleanSchemaChatRequest(t *testing.T) *schemas.BifrostChatRequest {
	t.Helper()
	var params schemas.ToolFunctionParameters
	require.NoError(t, json.Unmarshal([]byte(booleanSchemaToolParameters), &params))
	return &schemas.BifrostChatRequest{
		Provider: "acme",
		Model:    booleanSchemaModel,
		Input:    []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hi")}}},
		Params: &schemas.ChatParameters{Tools: []schemas.ChatTool{{
			Type:     schemas.ChatToolTypeFunction,
			Function: &schemas.ChatToolFunction{Name: "set_state", Parameters: &params},
		}}},
	}
}

// booleanSchemaServer records the last request body and answers in mode's wire shape.
func booleanSchemaServer(t *testing.T, mode string) (*httptest.Server, *atomic.Value) {
	t.Helper()
	chatBody := `{"id":"chatcmpl_test","object":"chat.completion","created":1,"model":"acme-model-v1","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`
	responseBody := `{"id":"resp_test","object":"response","created_at":1,"status":"completed","model":"acme-model-v1","output":[],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`
	var body atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		body.Store(data)
		switch mode {
		case "chat":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, chatBody)
		case "responses":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, responseBody)
		case "chat-stream":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, `data: {"id":"chatcmpl_test","object":"chat.completion.chunk","created":1,"model":"acme-model-v1","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\n")
			fmt.Fprint(w, "data: [DONE]\n\n")
		default:
			w.Header().Set("Content-Type", "text/event-stream")
			event, _ := json.Marshal(map[string]any{"type": "response.completed", "sequence_number": 0, "response": json.RawMessage(responseBody)})
			fmt.Fprint(w, "data: "+string(event)+"\n\n")
		}
	}))
	t.Cleanup(server.Close)
	return server, &body
}

// drainBooleanSchemaStream requires a provider stream to close within the test timeout.
func drainBooleanSchemaStream(t *testing.T, stream chan *schemas.BifrostStreamChunk) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-stream:
			if !ok {
				return
			}
		case <-timeout:
			t.Fatal("timed out waiting for the provider stream to close")
		}
	}
}

// capturedBooleanSchemaBody returns the body received by the test upstream.
func capturedBooleanSchemaBody(t *testing.T, body *atomic.Value) []byte {
	t.Helper()
	data, _ := body.Load().([]byte)
	require.NotEmpty(t, data, "the upstream never received a request")
	return data
}

// sendThroughProvider sends chat (or its Responses form) through an OpenAI-base custom
// provider with the given flag and returns the body the upstream received.
func sendThroughProvider(t *testing.T, mode string, flagged bool, chat *schemas.BifrostChatRequest) []byte {
	t.Helper()
	server, body := booleanSchemaServer(t, mode)
	var customConfig schemas.CustomProviderConfig
	require.NoError(t, json.Unmarshal([]byte(fmt.Sprintf(`{"base_provider_type":"openai","boolean_schema_nodes_unsupported":%t}`, flagged)), &customConfig))
	customConfig.CustomProviderKey = "acme"
	provider := NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig:        schemas.NetworkConfig{BaseURL: server.URL, DefaultRequestTimeoutInSeconds: 10, AllowPrivateNetwork: true},
		CustomProviderConfig: &customConfig,
	}, testNoopLogger{})
	ctx, cancel := schemas.NewBifrostContextWithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	key := schemas.Key{Value: *schemas.NewSecretVar("test-key"), Models: []string{"*"}}

	var bErr *schemas.BifrostError
	switch mode {
	case "chat":
		_, bErr = provider.ChatCompletion(ctx, key, chat)
	case "chat-stream":
		var stream chan *schemas.BifrostStreamChunk
		stream, bErr = provider.ChatCompletionStream(ctx, passthroughPostHook, nil, key, chat)
		if bErr == nil {
			drainBooleanSchemaStream(t, stream)
		}
	case "responses":
		_, bErr = provider.Responses(ctx, key, chat.ToResponsesRequest())
	case "responses-stream":
		var stream chan *schemas.BifrostStreamChunk
		stream, bErr = provider.ResponsesStream(ctx, passthroughPostHook, nil, key, chat.ToResponsesRequest())
		if bErr == nil {
			drainBooleanSchemaStream(t, stream)
		}
	}
	if bErr != nil {
		t.Fatalf("%s through the provider failed: %+v", mode, bErr.Error)
	}
	return capturedBooleanSchemaBody(t, body)
}

// sendDirect builds the wire body the way the provider did before the flag existed: the
// caller's request handed straight to the shared OpenAI handler.
func sendDirect(t *testing.T, mode string, chat *schemas.BifrostChatRequest) []byte {
	t.Helper()
	server, body := booleanSchemaServer(t, mode)
	ctx, cancel := schemas.NewBifrostContextWithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
	client := &fasthttp.Client{}
	auth := BearerAuthHeader(schemas.Key{Value: *schemas.NewSecretVar("test-key")})
	provider := schemas.ModelProvider("acme")

	var bErr *schemas.BifrostError
	switch mode {
	case "chat":
		_, bErr = HandleOpenAIChatCompletionRequest(ctx, client, server.URL+"/v1/chat/completions", chat, auth, nil, false, false, provider, nil, nil, nil, testNoopLogger{})
	case "chat-stream":
		var stream chan *schemas.BifrostStreamChunk
		stream, bErr = HandleOpenAIChatCompletionStreaming(ctx, client, server.URL+"/v1/chat/completions", chat, auth, nil, 5, false, false, provider, passthroughPostHook, nil, nil, nil, nil, nil, nil, testNoopLogger{}, nil)
		if bErr == nil {
			drainBooleanSchemaStream(t, stream)
		}
	case "responses":
		_, bErr = HandleOpenAIResponsesRequest(ctx, client, server.URL+"/v1/responses", chat.ToResponsesRequest(), auth, nil, false, false, provider, nil, nil, nil, testNoopLogger{})
	case "responses-stream":
		var stream chan *schemas.BifrostStreamChunk
		stream, bErr = HandleOpenAIResponsesStreaming(ctx, client, server.URL+"/v1/responses", chat.ToResponsesRequest(), auth, nil, 5, false, false, provider, passthroughPostHook, nil, nil, nil, nil, nil, testNoopLogger{}, nil)
		if bErr == nil {
			drainBooleanSchemaStream(t, stream)
		}
	}
	if bErr != nil {
		t.Fatalf("direct %s failed: %+v", mode, bErr.Error)
	}
	return capturedBooleanSchemaBody(t, body)
}

// booleanSchemaParametersPath selects the tool-parameter location for each wire format.
func booleanSchemaParametersPath(mode string) string {
	if mode == "chat" || mode == "chat-stream" {
		return "tools.0.function.parameters"
	}
	return "tools.0.parameters"
}

// A custom provider declaring boolean_schema_nodes_unsupported sends every boolean
// subschema in object form on Chat Completions and Responses, streaming or not, and
// changes nothing else: boolean keyword values, instance data and the model ID stay
// as they were, and the caller's request is not written through.
func TestBooleanSchemaNodesUnsupported_FlaggedProviderRewritesToolSchemas(t *testing.T) {
	for _, mode := range booleanSchemaModes {
		t.Run(mode, func(t *testing.T) {
			chat := booleanSchemaChatRequest(t)
			flagged := sendThroughProvider(t, mode, true, chat)
			unflagged := sendThroughProvider(t, mode, false, booleanSchemaChatRequest(t))
			params := booleanSchemaParametersPath(mode)

			for path, want := range map[string]string{
				".properties.state":                       `{}`,
				".properties.denied":                      `{"not":{}}`,
				".properties.tags.items":                  `{}`,
				".properties.choice.anyOf.0.items":        `{}`,
				".properties.choice.anyOf.1.type":         `"null"`,
				".properties.strict.additionalProperties": `false`,
				".properties.open.additionalProperties":   `true`,
				".properties.flag.enum":                   `[true,false]`,
				".properties.flag.const":                  `true`,
				".properties.flag.default":                `true`,
				".required":                               `["state"]`,
				".additionalProperties":                   `false`,
			} {
				require.Equal(t, want, gjson.GetBytes(flagged, params+path).Raw, "flagged %s%s in %s", params, path, flagged)
			}
			require.Equal(t, booleanSchemaModel, gjson.GetBytes(flagged, "model").String(), "the model ID must not change")

			// Everything outside the tool parameters is byte-identical to the unflagged body.
			restored, err := sjson.SetRawBytes(append([]byte(nil), flagged...), params, []byte(gjson.GetBytes(unflagged, params).Raw))
			require.NoError(t, err)
			require.Equal(t, string(unflagged), string(restored), "the rewrite touched more than the tool parameters")

			// The caller's request still holds its booleans and its model.
			state, _ := chat.Params.Tools[0].Function.Parameters.Properties.Get("state")
			require.Equal(t, true, state, "the caller's tool schema was mutated")
			require.Equal(t, booleanSchemaModel, chat.Model)
		})
	}
}

// Without the flag the custom provider's body is byte-identical to the one the shared
// handler builds from the untouched request, booleans included.
func TestBooleanSchemaNodesUnsupported_UnflaggedProviderIsByteIdentical(t *testing.T) {
	for _, mode := range booleanSchemaModes {
		t.Run(mode, func(t *testing.T) {
			unflagged := sendThroughProvider(t, mode, false, booleanSchemaChatRequest(t))
			direct := sendDirect(t, mode, booleanSchemaChatRequest(t))
			require.Equal(t, string(direct), string(unflagged))
			params := booleanSchemaParametersPath(mode)
			require.Equal(t, `true`, gjson.GetBytes(unflagged, params+".properties.state").Raw)
			require.Equal(t, `true`, gjson.GetBytes(unflagged, params+".properties.tags.items").Raw)
			require.Equal(t, booleanSchemaModel, gjson.GetBytes(unflagged, "model").String())
		})
	}
}

// TestBooleanSchemaNodesUnsupported_OnlyOptedInOpenAIBaseRewrites checks that
// ineligible providers return the original requests without allocating.
func TestBooleanSchemaNodesUnsupported_OnlyOptedInOpenAIBaseRewrites(t *testing.T) {
	for name, config := range map[string]*schemas.CustomProviderConfig{
		"native":     nil,
		"omitted":    {BaseProviderType: schemas.OpenAI},
		"disabled":   {BaseProviderType: schemas.OpenAI, BooleanSchemaNodesUnsupported: false},
		"other-base": {BaseProviderType: schemas.Anthropic, BooleanSchemaNodesUnsupported: true},
	} {
		t.Run(name, func(t *testing.T) {
			provider := &OpenAIProvider{customProviderConfig: config}
			chat := booleanSchemaChatRequest(t)
			responses := chat.ToResponsesRequest()
			require.Same(t, chat, provider.normalizeChatToolSchemas(chat))
			require.Same(t, responses, provider.normalizeResponsesToolSchemas(responses))
			require.Zero(t, testing.AllocsPerRun(100, func() {
				provider.normalizeChatToolSchemas(chat)
				provider.normalizeResponsesToolSchemas(responses)
			}))
		})
	}
}
