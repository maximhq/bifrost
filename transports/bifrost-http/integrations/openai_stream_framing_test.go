package integrations

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/anthropic"
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
)

func TestOpenAIChatStreamWireConverter(t *testing.T) {
	role := `{"choices":[{"delta":{"role":"assistant"}}]}`
	content := `{"choices":[{"delta":{"content":"hello\\n\\nworld"}}],"extension":true}`
	for _, tc := range []struct {
		name string
		raw  interface{}
		want interface{}
	}{
		{"single frame", content, content},
		{"buffered role and content", role + "\n\n" + content, "data: " + role + "\n\ndata: " + content + "\n\n"},
		{"non-string raw value", []byte(content), []byte(content)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := &schemas.BifrostChatResponse{}
			resp.ExtraFields.Provider = schemas.OpenAI
			resp.ExtraFields.RawResponse = tc.raw
			event, result, err := openAIChatStreamWireConverter(nil, resp)
			require.NoError(t, err)
			require.Empty(t, event)
			require.Equal(t, tc.want, result)
			require.Equal(t, tc.raw, resp.ExtraFields.RawResponse, "raw logging payload must not change")
		})
	}
	for _, provider := range []schemas.ModelProvider{schemas.OpenAI, schemas.Anthropic} {
		resp := &schemas.BifrostChatResponse{}
		resp.ExtraFields.Provider = provider
		if provider != schemas.OpenAI {
			resp.ExtraFields.RawResponse = role + "\n\n" + content
		}
		_, result, err := openAIChatStreamWireConverter(nil, resp)
		require.NoError(t, err)
		require.Same(t, resp, result, "normalized responses must retain the existing conversion path")
	}
}

// Exercise the real provider, route converter, and transport writer. Checking
// RawResponse alone misses unframed JSON that a client cannot receive as SSE.
func TestOpenAIChatStreamUsageSSEFraming(t *testing.T) {
	for _, rawResponse := range []bool{true, false} {
		for _, finish := range []string{"stop", "tool_calls"} {
			for _, route := range CreateOpenAIRouteConfigs("", &mockHandlerStore{}) {
				if route.Path != "/v1/chat/completions" && route.Path != "/chat/completions" && route.Path != "/openai/deployments/{deploymentPath:*}" {
					continue
				}
				t.Run(fmt.Sprintf("raw=%t/%s%s", rawResponse, finish, route.Path), func(t *testing.T) {
					delta := `{"content":"hello"}`
					if finish == "tool_calls" {
						delta = `{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{}"}}]}`
					}
					frames := []string{
						`{"id":"test","model":"gpt-4o-mini","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
						fmt.Sprintf(`{"id":"test","model":"gpt-4o-mini","choices":[{"index":0,"delta":%s,"finish_reason":null}]}`, delta),
						fmt.Sprintf(`{"id":"test","model":"gpt-4o-mini","choices":[{"index":0,"delta":{},"finish_reason":%q}]}`, finish),
						`{"id":"test","model":"gpt-4o-mini","choices":[],"usage":{"prompt_tokens":5038,"completion_tokens":17,"total_tokens":5055,"prompt_tokens_details":{"cached_tokens":4992}},"unknown_upstream_field":"preserve me"}`,
					}
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "text/event-stream")
						for _, frame := range frames {
							fmt.Fprintf(w, "data: %s\n\n", frame)
						}
						fmt.Fprint(w, "data: [DONE]\n\n")
					}))
					defer upstream.Close()
					logger := bifrost.NewNoOpLogger()
					provider := openai.NewOpenAIProvider(&schemas.ProviderConfig{
						NetworkConfig:       schemas.NetworkConfig{BaseURL: upstream.URL},
						SendBackRawResponse: rawResponse,
					}, logger)
					parent, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					ctx := schemas.NewBifrostContext(parent, schemas.NoDeadline)
					stream, err := provider.ChatCompletionStream(ctx,
						func(_ *schemas.BifrostContext, response *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
							// The core pipeline normally stamps the routed provider before
							// the HTTP integration selects its native passthrough converter.
							if response != nil && response.ChatResponse != nil {
								response.ChatResponse.ExtraFields.Provider = schemas.OpenAI
							}
							return response, err
						}, nil, schemas.Key{Value: *schemas.NewSecretVar("test-key")},
						&schemas.BifrostChatRequest{
							Provider: schemas.OpenAI, Model: "gpt-4o-mini",
							Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}}},
						})
					require.Nil(t, err)
					router := NewGenericRouter(nil, &mockHandlerStore{}, nil, nil, nil, logger)
					httpCtx := &fasthttp.RequestCtx{}
					router.handleStreaming(httpCtx, ctx, route, "", stream, cancel)
					body, readErr := io.ReadAll(httpCtx.Response.BodyStream())
					require.NoError(t, readErr)
					// SSE dispatches only data fields; bare JSON is an unknown field and
					// must not be rescued by a lenient JSON-line parser.
					var events []string
					for _, event := range strings.Split(string(body), "\n\n") {
						var data []string
						for _, line := range strings.Split(event, "\n") {
							if value, ok := strings.CutPrefix(line, "data:"); ok {
								data = append(data, strings.TrimPrefix(value, " "))
							}
						}
						if len(data) > 0 {
							events = append(events, strings.Join(data, "\n"))
						}
					}
					if rawResponse {
						require.Equal(t, append(frames, "[DONE]"), events, "wire stream: %s", body)
					} else {
						require.Equal(t, "[DONE]", events[len(events)-1])
						require.GreaterOrEqual(t, len(events), 5, "finish and usage must be separate SSE events: %s", body)
						for _, event := range events[:len(events)-2] {
							require.Equal(t, "null", gjson.Get(event, "usage").Raw, "ordinary chunk: %s", event)
						}
						terminal := events[len(events)-3]
						require.Equal(t, finish, gjson.Get(terminal, "choices.0.finish_reason").String())
						usage := events[len(events)-2]
						require.Equal(t, "[]", gjson.Get(usage, "choices").Raw, "usage chunk must have empty choices: %s", usage)
						require.Equal(t, int64(5055), gjson.Get(usage, "usage.total_tokens").Int())
						require.Equal(t, int64(4992), gjson.Get(usage, "usage.prompt_tokens_details.cached_tokens").Int())
					}
				})
			}
		}
	}
}

// A mid-stream provider error on the OpenAI integration's Responses routes must be
// a typed "event: error" frame, the same framing the native /v1/responses handler
// emits; chat streams keep bare data frames, and Anthropic keeps its own error event.
func TestStreamErrorEventFramingPerRoute(t *testing.T) {
	findRoute := func(routes []RouteConfig, method, path string) RouteConfig {
		t.Helper()
		for _, route := range routes {
			if route.Method == method && route.Path == path {
				return route
			}
		}
		t.Fatalf("route %s %s not found", method, path)
		return RouteConfig{}
	}
	openAIRoutes := CreateOpenAIRouteConfigs("/openai", &mockHandlerStore{})
	anthropicRoutes := CreateAnthropicRouteConfigs("/anthropic", bifrost.NewNoOpLogger())
	for _, tc := range []struct {
		name        string
		route       RouteConfig
		requestType schemas.RequestType
		wantPrefix  string
		wantNoEvent bool
		streamType  schemas.RequestType
	}{
		{"openai v1 responses", findRoute(openAIRoutes, "POST", "/openai/v1/responses"), schemas.ResponsesStreamRequest, "event: error\ndata: {", false, ""},
		{"openai responses", findRoute(openAIRoutes, "POST", "/openai/responses"), schemas.ResponsesStreamRequest, "event: error\ndata: {", false, ""},
		{"openai responses retrieve", findRoute(openAIRoutes, "GET", "/openai/v1/responses/{response_id}"), schemas.ResponsesRetrieveStreamRequest, "event: error\ndata: {", false, ""},
		{"azure deployments responses", findRoute(openAIRoutes, "POST", "/openai/openai/deployments/{deploymentPath:*}"), schemas.ResponsesStreamRequest, "event: error\ndata: {", false, ""},
		{"openai responses synthesized error", findRoute(openAIRoutes, "POST", "/openai/v1/responses"), "", "event: error\ndata: {", false, ""},
		{"openai chat completions", findRoute(openAIRoutes, "POST", "/openai/v1/chat/completions"), schemas.ChatCompletionStreamRequest, "data: {", true, ""},
		{"azure deployments chat", findRoute(openAIRoutes, "POST", "/openai/openai/deployments/{deploymentPath:*}"), schemas.ChatCompletionStreamRequest, "data: {", true, ""},
		{"anthropic messages", findRoute(anthropicRoutes, "POST", "/anthropic/v1/messages"), schemas.ResponsesStreamRequest, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\"", false, ""},
		{"azure deployments responses synthesized error", findRoute(openAIRoutes, "POST", "/openai/openai/deployments/{deploymentPath:*}"), "", "event: error\ndata: {", false, schemas.ResponsesStreamRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bifrostErr := &schemas.BifrostError{
				StatusCode: schemas.Ptr(fasthttp.StatusBadGateway),
				Error:      &schemas.ErrorField{Message: "upstream stream failed"},
			}
			bifrostErr.ExtraFields.RequestType = tc.requestType
			stream := make(chan *schemas.BifrostStreamChunk, 1)
			stream <- &schemas.BifrostStreamChunk{BifrostError: bifrostErr}
			close(stream)

			parent, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ctx := schemas.NewBifrostContext(parent, schemas.NoDeadline)
			router := NewGenericRouter(nil, &mockHandlerStore{}, nil, nil, nil, bifrost.NewNoOpLogger())
			httpCtx := &fasthttp.RequestCtx{}
			router.handleStreaming(httpCtx, ctx, tc.route, tc.streamType, stream, cancel)
			body, err := io.ReadAll(httpCtx.Response.BodyStream())
			require.NoError(t, err)
			wire := string(body)
			require.True(t, strings.HasPrefix(wire, tc.wantPrefix), "wire stream: %q", wire)
			require.Contains(t, wire, "upstream stream failed")
			if tc.wantNoEvent {
				require.NotContains(t, wire, "event:", "chat streams carry bare data frames: %q", wire)
			}
			require.NotContains(t, wire, "[DONE]")
		})
	}
}

func TestAnthropicChatStreamSeparateUsageSSE(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `event: message_start
data: {"type":"message_start","message":{"id":"test","type":"message","role":"assistant","model":"claude-test","content":[],"usage":{"input_tokens":10,"output_tokens":0}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}

event: message_stop
data: {"type":"message_stop"}

`)
	}))
	defer upstream.Close()
	for _, prefix := range []string{"/openai", "/litellm", "/langchain", "/pydanticai"} {
		for _, route := range CreateOpenAIRouteConfigs(prefix, &mockHandlerStore{}) {
			if route.Path != prefix+"/v1/chat/completions" {
				continue
			}
			for _, strip := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/strip=%t", prefix, strip), func(t *testing.T) {
					logger := bifrost.NewNoOpLogger()
					provider := anthropic.NewAnthropicProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{BaseURL: upstream.URL}}, logger)
					parent, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					ctx := schemas.NewBifrostContext(parent, schemas.NoDeadline)
					stream, bifrostErr := provider.ChatCompletionStream(ctx,
						func(_ *schemas.BifrostContext, response *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
							if response != nil && response.ChatResponse != nil && response.ChatResponse.Usage != nil {
								response.ChatResponse.Usage.Cost = &schemas.BifrostCost{TotalCost: 0.25}
							}
							return response, err
						}, nil, schemas.Key{Value: *schemas.NewSecretVar("test-key")}, &schemas.BifrostChatRequest{
							Provider: schemas.Anthropic, Model: "claude-test", Input: []schemas.ChatMessage{{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}}},
							Params: &schemas.ChatParameters{StreamOptions: &schemas.ChatStreamOptions{IncludeUsage: schemas.Ptr(true)}},
						})
					require.Nil(t, bifrostErr)
					router := NewGenericRouter(nil, &mockHandlerStore{}, nil, nil, nil, logger)
					httpCtx := &fasthttp.RequestCtx{}
					if strip {
						httpCtx.SetUserValue(lib.FastHTTPUserValueExtraFieldsInHeaders, true)
					}
					router.handleStreaming(httpCtx, ctx, route, "", stream, cancel)
					body, err := io.ReadAll(httpCtx.Response.BodyStream())
					require.NoError(t, err)
					var events []string
					for _, line := range strings.Split(string(body), "\n") {
						if data, ok := strings.CutPrefix(line, "data: "); ok {
							events = append(events, data)
						}
					}
					require.GreaterOrEqual(t, len(events), 4, "wire: %s", body)
					require.Equal(t, "[DONE]", events[len(events)-1])
					finish := events[len(events)-3]
					require.Equal(t, "stop", gjson.Get(finish, "choices.0.finish_reason").String(), "wire: %s", body)
					require.Equal(t, "null", gjson.Get(finish, "usage").Raw)
					usage := events[len(events)-2]
					require.Equal(t, "[]", gjson.Get(usage, "choices").Raw)
					require.Equal(t, int64(15), gjson.Get(usage, "usage.total_tokens").Int())
					require.Equal(t, 0.25, gjson.Get(usage, "usage.cost").Float())
					if strip {
						require.False(t, gjson.Get(usage, "extra_fields").Exists())
						require.False(t, gjson.Get(finish, "extra_fields").Exists())
					}
				})
			}
		}
	}
}
