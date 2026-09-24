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
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/require"
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
	for _, finish := range []string{"stop", "tool_calls"} {
		for _, route := range CreateOpenAIRouteConfigs("", &mockHandlerStore{}) {
			if route.Path != "/v1/chat/completions" && route.Path != "/chat/completions" && route.Path != "/openai/deployments/{deploymentPath:*}" {
				continue
			}
			t.Run(finish+route.Path, func(t *testing.T) {
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
					SendBackRawResponse: true,
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
				router.handleStreaming(httpCtx, ctx, route, stream, cancel)
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
				require.Equal(t, append(frames, "[DONE]"), events, "wire stream: %s", body)
			})
		}
	}
}
