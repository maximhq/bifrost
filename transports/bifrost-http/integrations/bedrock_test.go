package integrations

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/bytedance/sonic"
	"github.com/fasthttp/router"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/bedrock"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/kvstore"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/valyala/fasthttp"
)

// Exercise native ingress, the actual Bedrock provider/signing/decoder, and the
// HTTP EventStream writer. The only substituted component is the AWS upstream.
func TestBedrockInvokeMistralWire(t *testing.T) {
	const model = "mistral.mistral-large-3-675b-instruct"
	for _, prefix := range []string{"/bedrock", "/langchain", "/litellm", "/pydanticai", "/cursor"} {
		for _, scenario := range []struct {
			name       string
			stop       string
			mixedTools bool
		}{{"end_turn", "end_turn", false}, {"max_tokens", "max_tokens", false}, {"tool_use", "tool_use", false}, {"mixed tools", "tool_use", true}} {
			stop := scenario.stop
			for _, suffix := range []string{"invoke", "invoke-with-response-stream"} {
				t.Run(prefix+"/"+scenario.name+"/"+suffix, func(t *testing.T) {
					captured := make(chan *http.Request, 1)
					upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						body, err := io.ReadAll(r.Body)
						if err != nil {
							t.Error(err)
							return
						}
						assert.Equal(t, int64(256), gjson.GetBytes(body, "inferenceConfig.maxTokens").Int())
						assert.Equal(t, "Hello", gjson.GetBytes(body, "messages.0.content.0.text").String())
						if stop == "tool_use" {
							assert.Equal(t, "weather", gjson.GetBytes(body, "toolConfig.tools.0.toolSpec.name").String())
							assert.Equal(t, "time", gjson.GetBytes(body, "toolConfig.tools.1.toolSpec.name").String())
						}
						captured <- r
						content := `[{"text":"hello"}]`
						delta := `{"text":"hello"}`
						if stop == "tool_use" {
							content = `[{"toolUse":{"toolUseId":"call_1","name":"weather","input":{}}}]`
							delta = `{"toolUse":{"input":"{}"}}`
							if scenario.mixedTools {
								content = `[{"text":"checking"},{"toolUse":{"toolUseId":"call_1","name":"weather","input":{}}},{"text":"and time"},{"toolUse":{"toolUseId":"call_2","name":"time","input":{}}}]`
							}
						}
						if !strings.HasSuffix(r.URL.Path, "/converse-stream") {
							w.Header().Set("Content-Type", "application/json")
							_, _ = io.WriteString(w, `{"output":{"message":{"role":"assistant","content":`+content+`}},"stopReason":"`+stop+`","usage":{"inputTokens":15,"outputTokens":51,"totalTokens":66}}`)
							return
						}
						w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
						frames := [][2]string{{"messageStart", `{"role":"assistant"}`}}
						if stop == "tool_use" {
							frames = append(frames, [2]string{"contentBlockStart", `{"contentBlockIndex":0,"start":{"toolUse":{"toolUseId":"call_1","name":"weather"}}}`})
						}
						frames = append(frames, [2]string{"contentBlockDelta", `{"contentBlockIndex":0,"delta":` + delta + `}`}, [2]string{"contentBlockStop", `{"contentBlockIndex":0}`}, [2]string{"messageStop", `{"stopReason":"` + stop + `"}`}, [2]string{"metadata", `{"usage":{"inputTokens":15,"outputTokens":51,"totalTokens":66}}`})
						if scenario.mixedTools {
							frames = [][2]string{
								{"messageStart", `{"role":"assistant"}`},
								{"contentBlockDelta", `{"contentBlockIndex":0,"delta":{"text":"checking"}}`},
								{"contentBlockStop", `{"contentBlockIndex":0}`},
								{"contentBlockStart", `{"contentBlockIndex":1,"start":{"toolUse":{"toolUseId":"call_1","name":"weather"}}}`},
								{"contentBlockDelta", `{"contentBlockIndex":1,"delta":{"toolUse":{"input":"{"}}}`},
								{"contentBlockDelta", `{"contentBlockIndex":1,"delta":{"toolUse":{"input":"}"}}}`},
								{"contentBlockStop", `{"contentBlockIndex":1}`},
								{"contentBlockDelta", `{"contentBlockIndex":2,"delta":{"text":"and time"}}`},
								{"contentBlockStop", `{"contentBlockIndex":2}`},
								{"contentBlockStart", `{"contentBlockIndex":3,"start":{"toolUse":{"toolUseId":"call_2","name":"time"}}}`},
								{"contentBlockDelta", `{"contentBlockIndex":3,"delta":{"toolUse":{"input":"{"}}}`},
								{"contentBlockDelta", `{"contentBlockIndex":3,"delta":{"toolUse":{"input":"}"}}}`},
								{"contentBlockStop", `{"contentBlockIndex":3}`},
								{"messageStop", `{"stopReason":"tool_use"}`},
								{"metadata", `{"usage":{"inputTokens":15,"outputTokens":51,"totalTokens":66}}`},
							}
						}
						for _, frame := range frames {
							err := eventstream.NewEncoder().Encode(w, eventstream.Message{Headers: eventstream.Headers{{Name: ":message-type", Value: eventstream.StringValue("event")}, {Name: ":event-type", Value: eventstream.StringValue(frame[0])}}, Payload: []byte(frame[1])})
							if err != nil {
								t.Error(err)
								return
							}
							w.(http.Flusher).Flush()
						}
					}))
					defer upstream.Close()
					logger := bifrost.NewNoOpLogger()
					provider, err := bedrock.NewBedrockProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{InsecureSkipVerify: true, DefaultRequestTimeoutInSeconds: 5}}, logger)
					require.NoError(t, err)
					key := schemas.Key{BedrockKeyConfig: &schemas.BedrockKeyConfig{AccessKey: *schemas.NewSecretVar("AKIDEXAMPLE"), SecretKey: *schemas.NewSecretVar("test-secret"), Region: schemas.NewSecretVar("us-west-2"), Endpoints: &schemas.BedrockEndpoints{Runtime: schemas.NewSecretVar(upstream.URL)}}}
					parent, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					ctx := schemas.NewBifrostContext(parent, schemas.NoDeadline)
					store := &mockHandlerStore{}
					var route RouteConfig
					for _, candidate := range CreateBedrockRouteConfigs(prefix, store) {
						if candidate.Path == prefix+"/model/{modelId}/"+suffix {
							route = candidate
							break
						}
					}
					require.NotNil(t, route.RequestConverter)
					httpCtx := &fasthttp.RequestCtx{}
					httpCtx.SetUserValue("modelId", model)
					httpCtx.Request.SetBodyString(`{"max_tokens":256,"temperature":0,"messages":[{"role":"user","content":"Hello"}]}`)
					if stop == "tool_use" {
						// Use the existing Converse-shaped toolConfig compatibility input.
						// Native tools[].function ingress is independently tracked in #8940.
						httpCtx.Request.SetBodyString(`{"max_tokens":256,"temperature":0,"messages":[{"role":"user","content":"Hello"}],"toolConfig":{"tools":[{"toolSpec":{"name":"weather","description":"Get weather","inputSchema":{"json":{"type":"object","properties":{}}}}},{"toolSpec":{"name":"time","description":"Get time","inputSchema":{"json":{"type":"object","properties":{}}}}}]}}`)
					}
					typ := route.GetHTTPRequestType(httpCtx)
					require.Equal(t, schemas.ResponsesRequest, typ)
					ctx.SetValue(schemas.BifrostContextKeyHTTPRequestType, typ)
					ingress := route.GetRequestTypeInstance(ctx)
					require.NoError(t, sonic.Unmarshal(httpCtx.Request.Body(), ingress))
					require.NoError(t, route.PreCallback(httpCtx, ctx, ingress))
					req, err := route.RequestConverter(ctx, ingress)
					require.NoError(t, err)
					router := NewGenericRouter(nil, store, nil, nil, nil, logger)
					var payloads [][]byte
					if suffix == "invoke" {
						resp, bifrostErr := provider.Responses(ctx, key, req.ResponsesRequest)
						require.Nil(t, bifrostErr)
						out, err := route.ResponsesResponseConverter(ctx, resp)
						require.NoError(t, err)
						router.sendSuccess(httpCtx, ctx, route.ErrorConverter, out, nil)
						assert.Equal(t, fasthttp.StatusOK, httpCtx.Response.StatusCode())
						assert.Equal(t, "application/json", string(httpCtx.Response.Header.ContentType()))
						payloads = append(payloads, httpCtx.Response.Body())
					} else {
						stream, bifrostErr := provider.ResponsesStream(ctx, func(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
							// Core stamps the routed identity before the transport converter.
							if resp != nil && resp.ResponsesStreamResponse != nil {
								resp.ResponsesStreamResponse.ExtraFields.ResolvedModelUsed = model
							}
							return resp, err
						}, nil, key, req.ResponsesRequest)
						require.Nil(t, bifrostErr)
						router.handleStreaming(httpCtx, ctx, route, "", stream, cancel)
						// HTTP stream headers are set by handleStreamingRequest. This
						// lower-level test checks the EventStream writer; the harness
						// checks headers through the complete HTTP request path.
						wire, err := io.ReadAll(httpCtx.Response.BodyStream())
						require.NoError(t, err)
						reader := bytes.NewReader(wire)
						decoder := eventstream.NewDecoder()
						for {
							frame, err := decoder.Decode(reader, nil)
							if err == io.EOF {
								break
							}
							require.NoError(t, err)
							require.Equal(t, "chunk", frame.Headers.Get(":event-type").String())
							assert.Equal(t, "event", eventStreamHeaderString(t, frame.Headers, ":message-type"))
							assert.Equal(t, "application/json", eventStreamHeaderString(t, frame.Headers, ":content-type"))
							data, err := base64.StdEncoding.DecodeString(gjson.GetBytes(frame.Payload, "bytes").String())
							require.NoError(t, err)
							payloads = append(payloads, data)
						}
					}
					require.NotEmpty(t, payloads)
					object := "chat.completion"
					if suffix != "invoke" {
						object += ".chunk"
					}
					for i, raw := range payloads {
						assert.Equal(t, object, gjson.GetBytes(raw, "object").String(), "%s", raw)
						assert.NotEmpty(t, gjson.GetBytes(raw, "id").String())
						assert.Equal(t, gjson.GetBytes(payloads[0], "id").String(), gjson.GetBytes(raw, "id").String())
						assert.Positive(t, gjson.GetBytes(raw, "created").Int())
						assert.Equal(t, gjson.GetBytes(payloads[0], "created").Int(), gjson.GetBytes(raw, "created").Int())
						assert.Equal(t, model, gjson.GetBytes(raw, "model").String())
						assert.False(t, gjson.GetBytes(raw, "type").Exists(), "%s", raw)
						assert.False(t, gjson.GetBytes(raw, "extra_fields").Exists())
						require.Len(t, gjson.GetBytes(raw, "choices").Array(), 1)
						assert.True(t, gjson.GetBytes(raw, "choices.0.index").Exists())
						assert.Equal(t, int64(0), gjson.GetBytes(raw, "choices.0.index").Int())
						if suffix == "invoke" {
							assert.False(t, gjson.GetBytes(raw, "choices.0.delta").Exists())
							assert.Equal(t, "assistant", gjson.GetBytes(raw, "choices.0.message.role").String())
						} else {
							assert.False(t, gjson.GetBytes(raw, "choices.0.message").Exists())
							if i == 0 {
								assert.Equal(t, "assistant", gjson.GetBytes(raw, "choices.0.delta.role").String())
							}
							if i < len(payloads)-1 {
								assert.True(t, gjson.GetBytes(raw, "choices.0.finish_reason").Exists())
								assert.Equal(t, gjson.Null, gjson.GetBytes(raw, "choices.0.finish_reason").Type)
								assert.False(t, gjson.GetBytes(raw, "usage").Exists())
							} else {
								assert.JSONEq(t, `{}`, gjson.GetBytes(raw, "choices.0.delta").Raw)
							}
						}
					}
					if stop == "tool_use" {
						type toolResult struct{ id, name, arguments string }
						tools := make(map[int64]toolResult)
						for _, raw := range payloads {
							path := "choices.0.delta.tool_calls"
							if suffix == "invoke" {
								path = "choices.0.message.tool_calls"
							}
							for i, call := range gjson.GetBytes(raw, path).Array() {
								index := int64(i)
								if suffix != "invoke" {
									index = call.Get("index").Int()
								}
								tool := tools[index]
								if id := call.Get("id").String(); id != "" {
									assert.Equal(t, "function", call.Get("type").String())
									tool.id = id
								}
								tool.name += call.Get("function.name").String()
								tool.arguments += call.Get("function.arguments").String()
								tools[index] = tool
							}
						}
						names := []string{"weather"}
						if scenario.mixedTools {
							names = append(names, "time")
						}
						require.Len(t, tools, len(names))
						for i, name := range names {
							tool, exists := tools[int64(i)]
							require.True(t, exists)
							assert.Equal(t, []string{"call_1", "call_2"}[i], tool.id)
							assert.Equal(t, name, tool.name)
							assert.JSONEq(t, `{}`, tool.arguments)
						}
					}
					if !scenario.mixedTools && stop != "tool_use" {
						text := gjson.GetBytes(payloads[0], "choices.0.message.content").String()
						if suffix != "invoke" {
							text = ""
							for _, raw := range payloads {
								text += gjson.GetBytes(raw, "choices.0.delta.content").String()
							}
						}
						assert.Equal(t, "hello", text)
					}

					if scenario.mixedTools {
						if suffix == "invoke" {
							assert.Equal(t, "checkingand time", gjson.GetBytes(payloads[0], "choices.0.message.content").String())
						} else {
							var indexes []int64
							for _, raw := range payloads {
								for _, call := range gjson.GetBytes(raw, "choices.0.delta.tool_calls").Array() {
									indexes = append(indexes, call.Get("index").Int())
								}
							}
							assert.Equal(t, []int64{0, 0, 0, 1, 1, 1}, indexes)
						}
					}
					last := payloads[len(payloads)-1]
					finish := map[string]string{"end_turn": "stop", "max_tokens": "length", "tool_use": "tool_calls"}[stop]
					assert.Equal(t, finish, gjson.GetBytes(last, "choices.0.finish_reason").String(), "%s", last)
					assert.JSONEq(t, `{"prompt_tokens":15,"completion_tokens":51,"total_tokens":66}`, gjson.GetBytes(last, "usage").Raw)
					r := <-captured
					action := "converse"
					if suffix != "invoke" {
						action += "-stream"
					}
					assert.Equal(t, "/model/"+model+"/"+action, r.URL.Path)
					assert.Contains(t, r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/")
				})
			}
		}
	}
}

func TestBedrockInvokeMistralProviderErrors(t *testing.T) {
	const model = "mistral.mistral-large-3-675b-instruct"
	for _, prefix := range []string{"/bedrock", "/langchain", "/litellm", "/pydanticai", "/cursor"} {
		for _, tc := range []struct {
			name, suffix string
			streamError  bool
		}{
			{"unary validation", "invoke", false},
			{"stream startup validation", "invoke-with-response-stream", false},
			{"stream exception", "invoke-with-response-stream", true},
		} {
			t.Run(prefix+"/"+tc.name, func(t *testing.T) {
				upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					assert.Contains(t, r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/")
					if !tc.streamError {
						w.Header().Set("Content-Type", "application/json")
						w.Header().Set("X-Amzn-Errortype", "ValidationException")
						w.WriteHeader(http.StatusBadRequest)
						_, _ = io.WriteString(w, `{"__type":"ValidationException","message":"fixture validation"}`)
						return
					}
					assert.Equal(t, "/model/"+model+"/converse-stream", r.URL.Path)
					w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
					encoder := eventstream.NewEncoder()
					for _, frame := range [][2]string{
						{"messageStart", `{"role":"assistant"}`},
						{"contentBlockDelta", `{"contentBlockIndex":0,"delta":{"text":"partial"}}`},
					} {
						if err := encoder.Encode(w, eventstream.Message{Headers: eventstream.Headers{
							{Name: ":message-type", Value: eventstream.StringValue("event")},
							{Name: ":event-type", Value: eventstream.StringValue(frame[0])},
						}, Payload: []byte(frame[1])}); err != nil {
							t.Error(err)
							return
						}
					}
					if err := encoder.Encode(w, eventstream.Message{Headers: eventstream.Headers{
						{Name: ":message-type", Value: eventstream.StringValue("exception")},
						{Name: ":exception-type", Value: eventstream.StringValue("throttlingException")},
					}, Payload: []byte(`{"message":"fixture throttle"}`)}); err != nil {
						t.Error(err)
					}
				}))
				defer upstream.Close()
				logger := bifrost.NewNoOpLogger()
				provider, err := bedrock.NewBedrockProvider(&schemas.ProviderConfig{NetworkConfig: schemas.NetworkConfig{InsecureSkipVerify: true, DefaultRequestTimeoutInSeconds: 5}}, logger)
				require.NoError(t, err)
				key := schemas.Key{BedrockKeyConfig: &schemas.BedrockKeyConfig{AccessKey: *schemas.NewSecretVar("AKIDEXAMPLE"), SecretKey: *schemas.NewSecretVar("test-secret"), Region: schemas.NewSecretVar("us-west-2"), Endpoints: &schemas.BedrockEndpoints{Runtime: schemas.NewSecretVar(upstream.URL)}}}
				parent, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				ctx := schemas.NewBifrostContext(parent, schemas.NoDeadline)
				store := &mockHandlerStore{}
				var route RouteConfig
				for _, candidate := range CreateBedrockRouteConfigs(prefix, store) {
					if candidate.Path == prefix+"/model/{modelId}/"+tc.suffix {
						route = candidate
						break
					}
				}
				require.NotNil(t, route.RequestConverter)
				httpCtx := &fasthttp.RequestCtx{}
				httpCtx.SetUserValue("modelId", model)
				httpCtx.Request.SetBodyString(`{"max_tokens":32,"messages":[{"role":"user","content":"Hello"}]}`)
				ctx.SetValue(schemas.BifrostContextKeyHTTPRequestType, route.GetHTTPRequestType(httpCtx))
				ingress := route.GetRequestTypeInstance(ctx)
				require.NoError(t, sonic.Unmarshal(httpCtx.Request.Body(), ingress))
				require.NoError(t, route.PreCallback(httpCtx, ctx, ingress))
				req, err := route.RequestConverter(ctx, ingress)
				require.NoError(t, err)
				router := NewGenericRouter(nil, store, nil, nil, nil, logger)
				if tc.suffix == "invoke" {
					resp, bifrostErr := provider.Responses(ctx, key, req.ResponsesRequest)
					require.Nil(t, resp)
					require.NotNil(t, bifrostErr)
					router.sendError(httpCtx, ctx, route.ErrorConverter, bifrostErr)
				} else {
					stream, bifrostErr := provider.ResponsesStream(ctx, func(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
						if resp != nil && resp.ResponsesStreamResponse != nil {
							resp.ResponsesStreamResponse.ExtraFields.ResolvedModelUsed = model
						}
						return resp, err
					}, nil, key, req.ResponsesRequest)
					if tc.streamError {
						require.Nil(t, bifrostErr)
						router.handleStreaming(httpCtx, ctx, route, "", stream, cancel)
						wire, err := io.ReadAll(httpCtx.Response.BodyStream())
						require.NoError(t, err)
						reader := bytes.NewReader(wire)
						decoder := eventstream.NewDecoder()
						var chunks, exceptions int
						for {
							frame, err := decoder.Decode(reader, nil)
							if err == io.EOF {
								break
							}
							require.NoError(t, err)
							if eventStreamHeaderString(t, frame.Headers, ":message-type") == "exception" {
								exceptions++
								assert.Equal(t, "throttlingException", eventStreamHeaderString(t, frame.Headers, ":exception-type"))
								assert.Equal(t, "throttlingException", gjson.GetBytes(frame.Payload, "__type").String())
								assert.Contains(t, gjson.GetBytes(frame.Payload, "message").String(), "fixture throttle")
								assert.False(t, gjson.GetBytes(frame.Payload, "choices").Exists())
								assert.Zero(t, reader.Len(), "exception must terminate the stream")
							} else {
								chunks++
								assert.Equal(t, "chunk", eventStreamHeaderString(t, frame.Headers, ":event-type"))
								data, err := base64.StdEncoding.DecodeString(gjson.GetBytes(frame.Payload, "bytes").String())
								require.NoError(t, err)
								assert.Equal(t, "chat.completion.chunk", gjson.GetBytes(data, "object").String())
								assert.Equal(t, gjson.Null, gjson.GetBytes(data, "choices.0.finish_reason").Type)
								if chunks == 2 {
									assert.Equal(t, "partial", gjson.GetBytes(data, "choices.0.delta.content").String())
								}
							}
						}
						assert.Equal(t, 2, chunks)
						assert.Equal(t, 1, exceptions)
						return
					}
					require.Nil(t, stream)
					require.NotNil(t, bifrostErr)
					router.sendStreamError(httpCtx, ctx, route, bifrostErr)
				}
				assert.Equal(t, fasthttp.StatusBadRequest, httpCtx.Response.StatusCode())
				assert.Equal(t, "application/json", string(httpCtx.Response.Header.ContentType()))
				assert.Equal(t, "ValidationException", gjson.GetBytes(httpCtx.Response.Body(), "__type").String())
				assert.Contains(t, gjson.GetBytes(httpCtx.Response.Body(), "message").String(), "fixture validation")
				assert.False(t, gjson.GetBytes(httpCtx.Response.Body(), "choices").Exists())
			})
		}
	}
}

// mockHandlerStore implements lib.HandlerStore for testing
type mockHandlerStore struct {
	headerMatcher              *lib.HeaderMatcher
	availableProviders         []schemas.ModelProvider
	mcpHeaderCombinedAllowlist schemas.WhiteList
	modelCatalog               *modelcatalog.ModelCatalog
	customProviders            map[schemas.ModelProvider]mockCustomProvider
}

// mockCustomProvider is what GetCustomProviderPassthrough reports for one custom provider.
type mockCustomProvider struct {
	base        schemas.ModelProvider
	passthrough bool
}

const bedrockMistralInvokeModel = "mistral.mistral-large-3-675b-instruct"
const bedrockMistralInvokeBody = `{"max_tokens":256,"temperature":0,"tool_choice":"auto","messages":[{"role":"user","content":"What is the weather in San Francisco? Use the tool."}],"tools":[{"type":"function","function":{"name":"get_weather","description":"Get weather","parameters":{"type":"object","properties":{"location":{"type":"string"},"unit":{"type":"string"}},"required":["location"]}}}]}`

// A real Bedrock provider pointed at a local TLS endpoint: no provider replacement,
// short-circuit plugin, or AWS credentials are needed to exercise the complete route.
type bedrockInvokeTestAccount struct {
	host string
	ca   string
}

func (bedrockInvokeTestAccount) GetConfiguredProviders() ([]schemas.ModelProvider, error) {
	return []schemas.ModelProvider{schemas.Bedrock}, nil
}
func (a bedrockInvokeTestAccount) GetKeysForProvider(context.Context, schemas.ModelProvider) ([]schemas.Key, error) {
	return []schemas.Key{{ID: "invoke-test", Weight: 1, Models: schemas.WhiteList{"*"}, Value: *schemas.NewSecretVar("test-token"),
		BedrockKeyConfig: &schemas.BedrockKeyConfig{Region: schemas.NewSecretVar("us-west-2"),
			Endpoints: &schemas.BedrockEndpoints{Runtime: schemas.NewSecretVar(a.host)}}}}, nil
}
func (a bedrockInvokeTestAccount) GetConfigForProvider(schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	nc := schemas.DefaultNetworkConfig
	nc.CACertPEM = schemas.NewSecretVar(a.ca)
	nc.AllowPrivateNetwork = true
	nc.MaxRetries = 0
	nc.DefaultRequestTimeoutInSeconds = 5
	return &schemas.ProviderConfig{NetworkConfig: nc,
		ConcurrencyAndBufferSize: schemas.ConcurrencyAndBufferSize{Concurrency: 1, BufferSize: 4}}, nil
}

func newBedrockInvokeHTTPTestServer(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	upstream := httptest.NewTLSServer(handler)
	t.Cleanup(upstream.Close)
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: upstream.Certificate().Raw}))
	client, err := bifrost.Init(context.Background(), schemas.BifrostConfig{
		Account: bedrockInvokeTestAccount{host: strings.TrimPrefix(upstream.URL, "https://"), ca: ca}, Logger: bifrost.NewNoOpLogger(),
	})
	require.NoError(t, err)
	t.Cleanup(func() { client.Shutdown() })
	r := router.New()
	NewBedrockRouter(client, &mockHandlerStore{}, nil, bifrost.NewNoOpLogger()).RegisterRoutes(r)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := &fasthttp.Server{Handler: r.Handler}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Shutdown() })
	return "http://" + listener.Addr().String()
}

func TestBedrockMistralInvokeHTTP(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			var hits atomic.Int32
			requests := make(chan []byte, 10)
			base := newBedrockInvokeHTTPTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				body, err := io.ReadAll(r.Body)
				if err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				requests <- body
				wantPath := "/model/" + bedrockMistralInvokeModel + "/invoke"
				if stream {
					wantPath += "-with-response-stream"
				}
				if r.URL.Path != wantPath {
					w.WriteHeader(400)
					_, _ = io.WriteString(w, `{"__type":"ValidationException","message":"expected native Invoke route"}`)
					return
				}
				if !strings.Contains(string(body), `"function"`) || gjson.GetBytes(body, "tools.0.function.name").String() != "get_weather" {
					http.Error(w, "native tools missing", 400)
					return
				}
				if r.Header.Get(bedrock.GuardrailIdentifierHeader) != "gr-test" || r.Header.Get(bedrock.GuardrailVersionHeader) != "1" {
					http.Error(w, "guardrail headers missing", 400)
					return
				}
				none := gjson.GetBytes(body, "tool_choice").String() == "none"
				w.Header().Set("x-amzn-requestid", "invoke-request")
				if stream {
					w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
					encoder := eventstream.NewEncoder()
					chunks := []string{
						`{"id":"chatcmpl-test","object":"chat.completion.chunk","model":"` + bedrockMistralInvokeModel + `","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call12345","type":"function","function":{"name":"get_weather","arguments":""}}]}}]}`,
						`{"id":"chatcmpl-test","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"location\":\"SF\"}"}}]}}]}`,
						`{"id":"chatcmpl-test","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":12,"completion_tokens":8,"total_tokens":20}}`,
					}
					if none {
						chunks = []string{
							`{"id":"chatcmpl-test","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":"sunny"}}]}`,
							`{"id":"chatcmpl-test","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":8,"total_tokens":20}}`,
						}
					}
					chunks[len(chunks)-1] = strings.TrimSuffix(chunks[len(chunks)-1], "}") + `,"amazon-bedrock-guardrailAction":"NONE","amazon-bedrock-trace":{"guardrail":{"inputAssessment":{}}}}`
					for _, chunk := range chunks {
						payload, _ := sonic.Marshal(bedrock.BedrockInvokeStreamChunkEvent{Bytes: []byte(chunk)})
						_ = encoder.Encode(w, eventstream.Message{Headers: eventstream.Headers{
							{Name: ":message-type", Value: eventstream.StringValue("event")},
							{Name: ":event-type", Value: eventstream.StringValue("chunk")},
						}, Payload: payload})
					}
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if none {
					_, _ = io.WriteString(w, `{"object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"sunny"},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":8,"total_tokens":20},"amazon-bedrock-guardrailAction":"NONE","amazon-bedrock-trace":{"guardrail":{"inputAssessment":{}}}}`)
					return
				}
				_, _ = io.WriteString(w, `{"id":"chatcmpl-test","object":"chat.completion","model":"`+bedrockMistralInvokeModel+`","choices":[{"index":0,"message":{"role":"assistant","tool_calls":[{"id":"call12345","type":"function","function":{"name":"get_weather","arguments":"{\"location\":\"SF\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":12,"completion_tokens":8,"total_tokens":20}}`)
			})
			path := "/bedrock/model/" + bedrockMistralInvokeModel + "/invoke"
			if stream {
				path += "-with-response-stream"
			}
			httpClient := &http.Client{Timeout: 10 * time.Second}
			for _, choice := range []string{"auto", "any", "none"} {
				body := strings.Replace(bedrockMistralInvokeBody, `"tool_choice":"auto"`, `"tool_choice":"`+choice+`"`, 1)
				httpRequest, err := http.NewRequest(http.MethodPost, base+path, strings.NewReader(body))
				require.NoError(t, err)
				httpRequest.Header.Set("Content-Type", "application/json")
				httpRequest.Header.Set(bedrock.GuardrailIdentifierHeader, "gr-test")
				httpRequest.Header.Set(bedrock.GuardrailVersionHeader, "1")
				resp, err := httpClient.Do(httpRequest)
				require.NoError(t, err)
				wire, err := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				require.NoError(t, err)
				require.Equal(t, 200, resp.StatusCode, "%s: %s", choice, wire)
				upstreamBody := <-requests
				assert.Equal(t, choice, gjson.GetBytes(upstreamBody, "tool_choice").String())
				assert.Equal(t, int64(256), gjson.GetBytes(upstreamBody, "max_tokens").Int())
				assert.False(t, gjson.GetBytes(upstreamBody, "model").Exists())
				assert.False(t, gjson.GetBytes(upstreamBody, "stream_options").Exists())
				assert.False(t, gjson.GetBytes(upstreamBody, "max_completion_tokens").Exists())
				schema := gjson.GetBytes(upstreamBody, "tools.0.function.parameters").Raw
				assert.Less(t, strings.Index(schema, `"location"`), strings.Index(schema, `"unit"`), "property order must survive")
				if !stream {
					if choice == "none" {
						assert.Equal(t, "stop", gjson.GetBytes(wire, "choices.0.finish_reason").String())
						assert.False(t, gjson.GetBytes(wire, "choices.0.message.tool_calls").Exists())
					} else {
						assert.Equal(t, "tool_calls", gjson.GetBytes(wire, "choices.0.finish_reason").String())
						assert.Equal(t, "get_weather", gjson.GetBytes(wire, "choices.0.message.tool_calls.0.function.name").String())
					}
					if choice == "none" {
						assert.Equal(t, "NONE", gjson.GetBytes(wire, "amazon-bedrock-guardrailAction").String())
						assert.True(t, gjson.GetBytes(wire, "amazon-bedrock-trace.guardrail").Exists())
					}
					assert.Equal(t, int64(20), gjson.GetBytes(wire, "usage.total_tokens").Int())
					assert.False(t, gjson.GetBytes(wire, "type").Exists())
				} else {
					decoder := eventstream.NewDecoder()
					reader := bytes.NewReader(wire)
					var args strings.Builder
					var finish string
					var usage int64
					var toolName string
					var action string
					for reader.Len() > 0 {
						frame, err := decoder.Decode(reader, nil)
						require.NoError(t, err)
						require.Equal(t, "chunk", frame.Headers.Get(":event-type").String())
						var chunk bedrock.BedrockInvokeStreamChunkEvent
						require.NoError(t, sonic.Unmarshal(frame.Payload, &chunk))
						if value := gjson.GetBytes(chunk.Bytes, "amazon-bedrock-guardrailAction").String(); value != "" {
							action = value
						}
						assert.Equal(t, "chat.completion.chunk", gjson.GetBytes(chunk.Bytes, "object").String())
						args.WriteString(gjson.GetBytes(chunk.Bytes, "choices.0.delta.tool_calls.0.function.arguments").String())
						if name := gjson.GetBytes(chunk.Bytes, "choices.0.delta.tool_calls.0.function.name").String(); name != "" {
							toolName = name
						}
						if reason := gjson.GetBytes(chunk.Bytes, "choices.0.finish_reason").String(); reason != "" {
							finish = reason
						}
						if count := gjson.GetBytes(chunk.Bytes, "usage.total_tokens").Int(); count > 0 {
							usage = count
						}
					}
					if choice == "none" {
						assert.Empty(t, toolName)
						assert.Empty(t, args.String())
						assert.Equal(t, "stop", finish)
					} else {
						assert.Equal(t, "get_weather", toolName)
						assert.JSONEq(t, `{"location":"SF"}`, args.String())
						assert.Equal(t, "tool_calls", finish)
					}
					assert.Equal(t, "NONE", action)
					assert.Equal(t, int64(20), usage)
				}
			}
			// Replay remains native, including when none forbids another tool call.
			replay := `{"tool_choice":"none","tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object"}}}],"messages":[{"role":"system","content":"Weather assistant"},{"role":"user","content":"Weather?"},{"role":"assistant","tool_calls":[{"id":"call12345","function":{"name":"get_weather","arguments":"{}"}}]},{"role":"tool","tool_call_id":"call12345","content":"sunny"}]}`
			httpRequest, err := http.NewRequest(http.MethodPost, base+path, strings.NewReader(replay))
			require.NoError(t, err)
			httpRequest.Header.Set("Content-Type", "application/json")
			httpRequest.Header.Set(bedrock.GuardrailIdentifierHeader, "gr-test")
			httpRequest.Header.Set(bedrock.GuardrailVersionHeader, "1")
			resp, err := httpClient.Do(httpRequest)
			require.NoError(t, err)
			wire, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			require.NoError(t, err)
			require.Equal(t, 200, resp.StatusCode, "%s", wire)
			replayed := <-requests
			assert.Equal(t, "system", gjson.GetBytes(replayed, "messages.0.role").String())
			assert.Equal(t, "call12345", gjson.GetBytes(replayed, "messages.2.tool_calls.0.id").String())
			assert.Equal(t, "tool", gjson.GetBytes(replayed, "messages.3.role").String())
			assert.Equal(t, "call12345", gjson.GetBytes(replayed, "messages.3.tool_call_id").String())
			assert.Equal(t, int32(4), hits.Load())
		})
	}
}

func TestBedrockMistralInvokeHTTPValidation(t *testing.T) {
	var hits atomic.Int32
	base := newBedrockInvokeHTTPTestServer(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1); http.Error(w, "must not reach AWS", 500) })
	for _, route := range []string{"invoke", "invoke-with-response-stream"} {
		for _, tc := range []struct{ body, want string }{
			{`{"messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"parameters":{}}}]}`, "tools[0].function.name"},
			{`{"messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":" "}}]}`, "tools[0].function.name"},
			{`{"messages":[{"role":"user","content":"x"}],"tools":[{"name":"get_weather","input_schema":{}}]}`, "tools[0]"},
			{`{"messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":null}]}`, "tools[0].function"},
			{`{"messages":[{"role":"user","content":"x"}],"tool_choice":"invalid"}`, "tool_choice"},
			{`{"messages":[{"role":"user","content":"x"}],"tool_choice":"any"}`, "tool_choice"},
			{`{"messages":[{"role":"tool","content":"sunny"}]}`, "tool_call_id"},
		} {
			t.Run(route+"/"+tc.want+"/"+tc.body, func(t *testing.T) {
				resp, err := http.Post(base+"/bedrock/model/"+bedrockMistralInvokeModel+"/"+route, "application/json", strings.NewReader(tc.body))
				require.NoError(t, err)
				wire, err := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				require.NoError(t, err)
				assert.Equal(t, 400, resp.StatusCode, "%s", wire)
				assert.Equal(t, "ValidationException", gjson.GetBytes(wire, "__type").String())
				assert.Contains(t, gjson.GetBytes(wire, "message").String(), tc.want)
			})
		}
	}
	assert.Zero(t, hits.Load())
}

func BenchmarkBedrockInvokeRequestConversion(b *testing.B) {
	for _, tc := range []struct{ name, model, body string }{
		{"anthropic_no_tools", "anthropic.claude-sonnet-4-6", `{"messages":[{"role":"user","content":"Hello"}],"max_tokens":256}`},
		{"anthropic_tools", "anthropic.claude-sonnet-4-6", `{"messages":[{"role":"user","content":"Hello"}],"tools":[{"name":"get_weather","input_schema":{"type":"object"}}],"tool_choice":{"type":"auto"}}`},
		{"nova", "amazon.nova-lite-v1:0", `{"messages":[{"role":"user","content":[{"text":"Hello"}]}]}`},
		{"mistral_tools", bedrockMistralInvokeModel, bedrockMistralInvokeBody},
	} {
		b.Run(tc.name, func(b *testing.B) {
			route := createBedrockInvokeRouteConfig("/bedrock", nil)
			httpCtx := &fasthttp.RequestCtx{}
			httpCtx.SetUserValue("modelId", tc.model)
			httpCtx.Request.SetBodyString(tc.body)
			httpCtx.SetUserValue(schemas.BifrostContextKeyHTTPRequestType, route.GetHTTPRequestType(httpCtx))
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			ctx.SetValue(schemas.BifrostContextKeyHTTPRequestType, route.GetHTTPRequestType(httpCtx))
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				req := route.GetRequestTypeInstance(httpCtx)
				if err := sonic.Unmarshal([]byte(tc.body), req); err != nil {
					b.Fatal(err)
				}
				if err := route.PreCallback(httpCtx, ctx, req); err != nil {
					b.Fatal(err)
				}
				if _, err := route.RequestConverter(ctx, req); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func (m *mockHandlerStore) GetHeaderMatcher() *lib.HeaderMatcher {
	return m.headerMatcher
}

func (m *mockHandlerStore) GetStreamChunkInterceptor() lib.StreamChunkInterceptor {
	return nil
}

func (m *mockHandlerStore) GetAsyncJobExecutor() *logstore.AsyncJobExecutor {
	return nil
}

func (m *mockHandlerStore) GetAsyncJobResultTTL() int {
	return 3600
}

func (m *mockHandlerStore) GetKVStore() *kvstore.Store {
	return nil
}

func (m *mockHandlerStore) GetMCPHeaderCombinedAllowlist() schemas.WhiteList {
	return m.mcpHeaderCombinedAllowlist
}

func (m *mockHandlerStore) ShouldAllowPerRequestStorageOverride() bool {
	return false
}

func (m *mockHandlerStore) ShouldAllowPerRequestRawOverride() bool {
	return false
}

func (m *mockHandlerStore) ShouldAllowDirectKeys() bool {
	return false
}

func (m *mockHandlerStore) ShouldSendExtraFieldsInHeaders() bool {
	return false
}

func (m *mockHandlerStore) IsProviderConfigured(provider schemas.ModelProvider) bool {
	for _, p := range m.availableProviders {
		if p == provider {
			return true
		}
	}
	return false
}

func (m *mockHandlerStore) GetCustomProviderPassthrough(provider schemas.ModelProvider) (schemas.ModelProvider, bool, bool) {
	custom, ok := m.customProviders[provider]
	return custom.base, custom.passthrough, ok
}

func (m *mockHandlerStore) GetMCPExternalServerURL() string {
	return ""
}

func (m *mockHandlerStore) GetMCPExternalClientURL() string {
	return ""
}

func (m *mockHandlerStore) GetModelCatalog() *modelcatalog.ModelCatalog {
	return m.modelCatalog
}

// Ensure mockHandlerStore implements lib.HandlerStore
var _ lib.HandlerStore = (*mockHandlerStore)(nil)

func TestGenericRouter_MarkDeprecatedListModelsResponseUsesCatalog(t *testing.T) {
	pricingPath := filepath.Join(t.TempDir(), "pricing.json")
	pricingJSON := []byte(`{
		"deprecated-model": {"provider":"openai","mode":"chat","base_model":"deprecated-model","is_deprecated":true},
		"current-model": {"provider":"openai","mode":"chat","base_model":"current-model"}
	}`)
	require.NoError(t, os.WriteFile(pricingPath, pricingJSON, 0o600))
	ds := datasheet.New(nil, nil, datasheet.Config{URL: "file://" + pricingPath})
	require.NoError(t, ds.LoadFromURLIntoMemory(t.Context()))
	router := NewGenericRouter(nil, &mockHandlerStore{modelCatalog: modelcatalog.NewTestCatalogWithDatasheet(ds)}, nil, nil, nil, nil)
	resp := &schemas.BifrostListModelsResponse{Data: []schemas.Model{
		{ID: "openai/deprecated-model"},
		{ID: "openai/current-model"},
		{ID: "openai/provider-deprecated", IsDeprecated: true},
	}}

	router.markDeprecatedListModelsResponse(resp)

	// No models are removed; deprecated ones are flagged instead.
	require.Len(t, resp.Data, 3)
	byID := map[string]schemas.Model{}
	for _, m := range resp.Data {
		byID[m.ID] = m
	}
	assert.True(t, byID["openai/deprecated-model"].IsDeprecated)
	assert.False(t, byID["openai/current-model"].IsDeprecated)
	assert.True(t, byID["openai/provider-deprecated"].IsDeprecated)
}

func Test_parseS3URI(t *testing.T) {
	tests := []struct {
		name       string
		uri        string
		wantBucket string
		wantKey    string
	}{
		{
			name:       "full S3 URI with key",
			uri:        "s3://my-bucket/path/to/file.jsonl",
			wantBucket: "my-bucket",
			wantKey:    "path/to/file.jsonl",
		},
		{
			name:       "S3 URI with bucket only",
			uri:        "s3://my-bucket/",
			wantBucket: "my-bucket",
			wantKey:    "",
		},
		{
			name:       "S3 URI with bucket no trailing slash",
			uri:        "s3://my-bucket",
			wantBucket: "my-bucket",
			wantKey:    "",
		},
		{
			name:       "plain bucket name",
			uri:        "my-bucket",
			wantBucket: "my-bucket",
			wantKey:    "",
		},
		{
			name:       "S3 URI with nested key",
			uri:        "s3://bucket-name/folder1/folder2/file.txt",
			wantBucket: "bucket-name",
			wantKey:    "folder1/folder2/file.txt",
		},
		{
			name:       "empty string",
			uri:        "",
			wantBucket: "",
			wantKey:    "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotBucket, gotKey := parseS3URI(tt.uri)
			assert.Equal(t, tt.wantBucket, gotBucket, "bucket mismatch")
			assert.Equal(t, tt.wantKey, gotKey, "key mismatch")
		})
	}
}

func Test_createBedrockRouteConfigs(t *testing.T) {
	handlerStore := &mockHandlerStore{}
	routes := CreateBedrockRouteConfigs("/bedrock", handlerStore)

	assert.Len(t, routes, 6, "should have 6 bedrock routes")

	expectedRoutes := []struct {
		path   string
		method string
	}{
		{"/bedrock/model/{modelId}/converse", "POST"},
		{"/bedrock/model/{modelId}/converse-stream", "POST"},
		{"/bedrock/model/{modelId}/invoke-with-response-stream", "POST"},
		{"/bedrock/model/{modelId}/invoke", "POST"},
		{"/bedrock/rerank", "POST"},
		{"/bedrock/model/{modelId}/count-tokens", "POST"},
	}

	for i, expected := range expectedRoutes {
		assert.Equal(t, expected.path, routes[i].Path, "route %d path mismatch", i)
		assert.Equal(t, expected.method, routes[i].Method, "route %d method mismatch", i)
		assert.Equal(t, RouteConfigTypeBedrock, routes[i].Type, "route %d type mismatch", i)
		assert.NotNil(t, routes[i].GetRequestTypeInstance, "route %d GetRequestTypeInstance should not be nil", i)
		assert.NotNil(t, routes[i].ErrorConverter, "route %d ErrorConverter should not be nil", i)
	}
}

func Test_createBedrockConverseRouteConfig(t *testing.T) {
	handlerStore := &mockHandlerStore{}
	route := createBedrockConverseRouteConfig("/bedrock", handlerStore)

	assert.Equal(t, "/bedrock/model/{modelId}/converse", route.Path)
	assert.Equal(t, "POST", route.Method)
	assert.Equal(t, RouteConfigTypeBedrock, route.Type)
	assert.NotNil(t, route.GetRequestTypeInstance)
	assert.NotNil(t, route.RequestConverter)
	assert.NotNil(t, route.ResponsesResponseConverter)
	assert.NotNil(t, route.ErrorConverter)
	assert.NotNil(t, route.PreCallback)

	// Verify request instance type
	reqInstance := route.GetRequestTypeInstance(context.Background())
	_, ok := reqInstance.(*bedrock.BedrockConverseRequest)
	assert.True(t, ok, "GetRequestTypeInstance should return *bedrock.BedrockConverseRequest")
}

func Test_createBedrockConverseStreamRouteConfig(t *testing.T) {
	handlerStore := &mockHandlerStore{}
	route := createBedrockConverseStreamRouteConfig("/bedrock", handlerStore)

	assert.Equal(t, "/bedrock/model/{modelId}/converse-stream", route.Path)
	assert.Equal(t, "POST", route.Method)
	assert.Equal(t, RouteConfigTypeBedrock, route.Type)
	assert.NotNil(t, route.StreamConfig)
	assert.NotNil(t, route.StreamConfig.ErrorConverter)
	assert.NotNil(t, route.StreamConfig.ResponsesStreamResponseConverter)

	// Verify request instance type
	reqInstance := route.GetRequestTypeInstance(context.Background())
	_, ok := reqInstance.(*bedrock.BedrockConverseRequest)
	assert.True(t, ok, "GetRequestTypeInstance should return *bedrock.BedrockConverseRequest")
}

func Test_createBedrockInvokeRouteConfig(t *testing.T) {
	handlerStore := &mockHandlerStore{}
	route := createBedrockInvokeRouteConfig("/bedrock", handlerStore)

	assert.Equal(t, "/bedrock/model/{modelId}/invoke", route.Path)
	assert.Equal(t, "POST", route.Method)
	assert.Equal(t, RouteConfigTypeBedrock, route.Type)
	assert.NotNil(t, route.TextResponseConverter)
	assert.NotNil(t, route.ResponsesResponseConverter)

	// Verify request instance type
	reqInstance := route.GetRequestTypeInstance(context.Background())
	_, ok := reqInstance.(*bedrock.BedrockInvokeRequest)
	assert.True(t, ok, "GetRequestTypeInstance should return *bedrock.BedrockInvokeRequest")
}

func TestBedrockInvokeEmbeddingResponseLangChainCohereAlias(t *testing.T) {
	handlerStore := &mockHandlerStore{}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	response := &schemas.BifrostEmbeddingResponse{
		Model: "cohere.embed-english-v3",
		Data: []schemas.EmbeddingData{
			{
				Index:  0,
				Object: "embedding",
				Embedding: schemas.EmbeddingStruct{
					EmbeddingArray: []float64{0.25, 0.75},
				},
			},
		},
		Object: "list",
	}

	bedrockRoute := createBedrockInvokeRouteConfig("/bedrock", handlerStore)
	bedrockResult, err := bedrockRoute.EmbeddingResponseConverter(ctx, response)
	require.NoError(t, err)
	bedrockJSON, err := json.Marshal(bedrockResult)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"embeddings":[[0.25,0.75]],
		"response_type":"embeddings_floats"
	}`, string(bedrockJSON), "native Bedrock must retain the AWS Cohere envelope")

	langChainRoute := withLangChainBedrockEmbeddingCompatibility([]RouteConfig{createBedrockInvokeRouteConfig("/langchain", handlerStore)})[0]
	langChainResult, err := langChainRoute.EmbeddingResponseConverter(ctx, response)
	require.NoError(t, err)
	langChainJSON, err := json.Marshal(langChainResult)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"embedding":[0.25,0.75],
		"embeddings":[[0.25,0.75]],
		"response_type":"embeddings_floats"
	}`, string(langChainJSON), "LangChain needs singular embedding while native clients retain embeddings")
}

func TestBedrockInvokeTypedCohereResponseLangChainAlias(t *testing.T) {
	handlerStore := &mockHandlerStore{}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	response := &schemas.BifrostEmbeddingResponse{
		Model:  "cohere.embed-v4:0",
		Object: "list",
		Data: []schemas.EmbeddingData{
			{
				Index:          0,
				Object:         "embedding",
				Embedding:      schemas.EmbeddingStruct{EmbeddingArray: []float64{0.25, 0.75}},
				EncodingFormat: schemas.EmbeddingEncodingFloat,
			},
			{
				Index:          0,
				Object:         "embedding",
				Embedding:      schemas.EmbeddingStruct{EmbeddingInt8Array: []int8{1, -1}},
				EncodingFormat: schemas.EmbeddingEncodingInt8,
			},
		},
	}

	bedrockRoute := createBedrockInvokeRouteConfig("/bedrock", handlerStore)
	bedrockResult, err := bedrockRoute.EmbeddingResponseConverter(ctx, response)
	require.NoError(t, err)
	bedrockJSON, err := json.Marshal(bedrockResult)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"response_type":"embeddings_by_type",
		"embeddings":{"float":[[0.25,0.75]],"int8":[[1,-1]]}
	}`, string(bedrockJSON), "native Bedrock must retain the typed AWS envelope")

	langChainRoute := withLangChainBedrockEmbeddingCompatibility([]RouteConfig{createBedrockInvokeRouteConfig("/langchain", handlerStore)})[0]
	result, err := langChainRoute.EmbeddingResponseConverter(ctx, response)
	require.NoError(t, err)
	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"response_type":"embeddings_by_type",
		"embedding":[0.25,0.75],
		"embeddings":{"float":[[0.25,0.75]],"int8":[[1,-1]]}
	}`, string(resultJSON), "the alias must expose the float representation LangChain expects")
}

func TestBedrockInvokeTypedCohereResponseWithoutFloatIsNotAliased(t *testing.T) {
	handlerStore := &mockHandlerStore{}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	response := &schemas.BifrostEmbeddingResponse{
		Model:  "cohere.embed-v4:0",
		Object: "list",
		Data: []schemas.EmbeddingData{
			{
				Index:          0,
				Object:         "embedding",
				Embedding:      schemas.EmbeddingStruct{EmbeddingInt8Array: []int8{1, -1}},
				EncodingFormat: schemas.EmbeddingEncodingInt8,
			},
		},
	}

	// LangChain's BedrockEmbeddings reads a number[] and never requests typed
	// encodings, so an int8-only envelope has nothing meaningful to alias.
	langChainRoute := withLangChainBedrockEmbeddingCompatibility([]RouteConfig{createBedrockInvokeRouteConfig("/langchain", handlerStore)})[0]
	result, err := langChainRoute.EmbeddingResponseConverter(ctx, response)
	require.NoError(t, err)
	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"response_type":"embeddings_by_type",
		"embeddings":{"int8":[[1,-1]]}
	}`, string(resultJSON))
}

func TestBedrockInvokeEmbeddingResponseLangChainLeavesTitanUnchanged(t *testing.T) {
	handlerStore := &mockHandlerStore{}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	response := &schemas.BifrostEmbeddingResponse{
		Model: "amazon.titan-embed-text-v2:0",
		Data: []schemas.EmbeddingData{
			{
				Index:  0,
				Object: "embedding",
				Embedding: schemas.EmbeddingStruct{
					EmbeddingArray: []float64{0.25, 0.75},
				},
			},
		},
		Object: "list",
	}

	langChainRoute := withLangChainBedrockEmbeddingCompatibility([]RouteConfig{createBedrockInvokeRouteConfig("/langchain", handlerStore)})[0]
	result, err := langChainRoute.EmbeddingResponseConverter(ctx, response)
	require.NoError(t, err)
	_, ok := result.(*bedrock.BedrockInvokeEmbeddingResp)
	assert.True(t, ok)
}

func TestBedrockInvokeTypedTitanResponseLangChainLeavesEnvelopeUnchanged(t *testing.T) {
	handlerStore := &mockHandlerStore{}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	response := &schemas.BifrostEmbeddingResponse{
		Model:  "amazon.titan-embed-text-v2:0",
		Object: "list",
		Data: []schemas.EmbeddingData{
			{
				Index:          0,
				Object:         "embedding",
				Embedding:      schemas.EmbeddingStruct{EmbeddingArray: []float64{0.25, 0.75}},
				EncodingFormat: schemas.EmbeddingEncodingFloat,
			},
			{
				Index:          0,
				Object:         "embedding",
				Embedding:      schemas.EmbeddingStruct{EmbeddingInt8Array: []int8{1, 0}},
				EncodingFormat: schemas.EmbeddingEncodingBinary,
			},
		},
	}

	langChainRoute := withLangChainBedrockEmbeddingCompatibility([]RouteConfig{createBedrockInvokeRouteConfig("/langchain", handlerStore)})[0]
	result, err := langChainRoute.EmbeddingResponseConverter(ctx, response)
	require.NoError(t, err)
	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"embedding":[0.25,0.75],
		"embeddingsByType":{"float":[0.25,0.75],"binary":[1,0]},
		"inputTextTokenCount":0
	}`, string(resultJSON), "Titan already carries the singular field AWS defines; the alias must not touch it")
}

func TestBedrockInvokeTypedCohereResponseUsesResolvedModelForAlias(t *testing.T) {
	handlerStore := &mockHandlerStore{}
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	response := &schemas.BifrostEmbeddingResponse{
		Object: "list",
		ExtraFields: schemas.BifrostResponseExtraFields{
			ResolvedModelUsed: "cohere.embed-v4:0",
		},
		Data: []schemas.EmbeddingData{
			{
				Index:          0,
				Object:         "embedding",
				Embedding:      schemas.EmbeddingStruct{EmbeddingArray: []float64{0.25, 0.75}},
				EncodingFormat: schemas.EmbeddingEncodingFloat,
			},
		},
	}

	langChainRoute := withLangChainBedrockEmbeddingCompatibility([]RouteConfig{createBedrockInvokeRouteConfig("/langchain", handlerStore)})[0]
	result, err := langChainRoute.EmbeddingResponseConverter(ctx, response)
	require.NoError(t, err)
	resultJSON, err := json.Marshal(result)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"response_type":"embeddings_by_type",
		"embedding":[0.25,0.75],
		"embeddings":{"float":[[0.25,0.75]]}
	}`, string(resultJSON), "family resolution must fall back to the resolved model")
}

func Test_createBedrockInvokeWithResponseStreamRouteConfig(t *testing.T) {
	handlerStore := &mockHandlerStore{}
	route := createBedrockInvokeWithResponseStreamRouteConfig("/bedrock", handlerStore)

	assert.Equal(t, "/bedrock/model/{modelId}/invoke-with-response-stream", route.Path)
	assert.Equal(t, "POST", route.Method)
	assert.Equal(t, RouteConfigTypeBedrock, route.Type)
	assert.NotNil(t, route.StreamConfig)
	assert.NotNil(t, route.StreamConfig.ErrorConverter)
	assert.NotNil(t, route.StreamConfig.TextStreamResponseConverter)
	assert.NotNil(t, route.StreamConfig.ResponsesStreamResponseConverter)

	// Verify request instance type
	reqInstance := route.GetRequestTypeInstance(context.Background())
	_, ok := reqInstance.(*bedrock.BedrockInvokeRequest)
	assert.True(t, ok, "GetRequestTypeInstance should return *bedrock.BedrockInvokeRequest")
}

func Test_bedrockStreamErrorConverterEncodesEventStreamException(t *testing.T) {
	errType := "PluginDenied"
	statusCode := fasthttp.StatusForbidden
	bifrostErr := &schemas.BifrostError{
		Type:       &errType,
		StatusCode: &statusCode,
		Error: &schemas.ErrorField{
			Message: "blocked by policy",
		},
	}

	converted := bedrockStreamErrorConverter(nil, bifrostErr)
	exception, ok := converted.(*bedrockEventStreamException)
	require.True(t, ok)

	reader := lib.NewSSEStreamReader()
	require.True(t, sendBedrockEventStreamException(reader, eventstream.NewEncoder(), exception, bifrost.NewNoOpLogger()))
	reader.Done()

	body, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.False(t, bytes.HasPrefix(body, []byte("data: ")), "Bedrock streaming errors must not be plain SSE")

	msg, err := eventstream.NewDecoder().Decode(bytes.NewReader(body), nil)
	require.NoError(t, err)
	assert.Equal(t, "exception", eventStreamHeaderString(t, msg.Headers, ":message-type"))
	assert.Equal(t, "PluginDenied", eventStreamHeaderString(t, msg.Headers, ":exception-type"))
	assert.JSONEq(t, `{"__type":"PluginDenied","message":"blocked by policy"}`, string(msg.Payload))
	assert.False(t, strings.Contains(string(body), "data:"), "AWS EventStream bytes must not contain SSE framing")
}

func Test_toBedrockEventStreamExceptionAcceptsBedrockError(t *testing.T) {
	exception, ok := toBedrockEventStreamException(&bedrock.BedrockError{
		Type:    "ValidationException",
		Message: "invalid request",
	})
	require.True(t, ok)

	reader := lib.NewSSEStreamReader()
	require.True(t, sendBedrockEventStreamException(reader, eventstream.NewEncoder(), exception, bifrost.NewNoOpLogger()))
	reader.Done()

	body, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.False(t, bytes.HasPrefix(body, []byte("data: ")), "Bedrock streaming errors must not be plain SSE")

	msg, err := eventstream.NewDecoder().Decode(bytes.NewReader(body), nil)
	require.NoError(t, err)
	assert.Equal(t, "exception", eventStreamHeaderString(t, msg.Headers, ":message-type"))
	assert.Equal(t, "ValidationException", eventStreamHeaderString(t, msg.Headers, ":exception-type"))
	assert.JSONEq(t, `{"__type":"ValidationException","message":"invalid request"}`, string(msg.Payload))
}

func Test_handleStreamingBedrockUnknownErrorResponseFallsBackToEventStreamException(t *testing.T) {
	stream := make(chan *schemas.BifrostStreamChunk, 1)
	statusCode := fasthttp.StatusInternalServerError
	stream <- &schemas.BifrostStreamChunk{
		BifrostError: &schemas.BifrostError{
			StatusCode: &statusCode,
			Error: &schemas.ErrorField{
				Message: "plugin returned an unsupported error envelope",
			},
		},
	}
	close(stream)

	router := NewGenericRouter(nil, &mockHandlerStore{}, nil, nil, nil, bifrost.NewNoOpLogger())
	ctx := &fasthttp.RequestCtx{}
	rec := newCancelRecorder()
	router.handleStreaming(ctx, nil, RouteConfig{
		Type: RouteConfigTypeBedrock,
		StreamConfig: &StreamConfig{
			ErrorConverter: func(ctx *schemas.BifrostContext, err *schemas.BifrostError) interface{} {
				return map[string]interface{}{
					"unexpected": "shape",
				}
			},
		},
	}, "", stream, rec.cancel)

	body, err := io.ReadAll(ctx.Response.BodyStream())
	require.NoError(t, err)
	require.NotEmpty(t, body)
	require.False(t, bytes.HasPrefix(body, []byte("data: ")), "Bedrock fallback errors must not be plain SSE")

	msg, err := eventstream.NewDecoder().Decode(bytes.NewReader(body), nil)
	require.NoError(t, err)
	assert.Equal(t, "exception", eventStreamHeaderString(t, msg.Headers, ":message-type"))
	assert.Equal(t, "InternalServerException", eventStreamHeaderString(t, msg.Headers, ":exception-type"))
	assert.JSONEq(t, `{"__type":"InternalServerException","message":"An error occurred while processing your request"}`, string(msg.Payload))
	// handleStreaming cancels the request context on every exit path, a clean end-of-stream
	// included, so the client-disconnect watcher goroutine ConvertToBifrostContext started
	// cannot outlive the request (see Test_handleStreaming_RetentionCancelsAndLeavesNoGoroutine).
	// The cancel therefore no longer distinguishes a successful fallback write from a real
	// client disconnect — the frame asserted above is what does: on a disconnect the producer
	// returns early and no InternalServerException is ever written.
	rec.requireCancelled(t, "handleStreaming did not cancel the request context after the stream ended")
}

func eventStreamHeaderString(t *testing.T, headers eventstream.Headers, name string) string {
	t.Helper()
	for _, header := range headers {
		if header.Name == name {
			value, ok := header.Value.Get().(string)
			require.True(t, ok, "%s header must be a string", name)
			return value
		}
	}
	t.Fatalf("missing EventStream header %s", name)
	return ""
}

func Test_createBedrockRerankRouteConfig(t *testing.T) {
	handlerStore := &mockHandlerStore{}
	route := createBedrockRerankRouteConfig("/bedrock", handlerStore)

	assert.Equal(t, "/bedrock/rerank", route.Path)
	assert.Equal(t, "POST", route.Method)
	assert.Equal(t, RouteConfigTypeBedrock, route.Type)
	assert.NotNil(t, route.GetHTTPRequestType)
	assert.Equal(t, schemas.RerankRequest, route.GetHTTPRequestType(nil))
	assert.NotNil(t, route.GetRequestTypeInstance)
	assert.NotNil(t, route.RequestConverter)
	assert.NotNil(t, route.RerankResponseConverter)
	assert.NotNil(t, route.ErrorConverter)
	assert.NotNil(t, route.PreCallback)

	// Verify request instance type
	reqInstance := route.GetRequestTypeInstance(context.Background())
	_, ok := reqInstance.(*bedrock.BedrockRerankRequest)
	assert.True(t, ok, "GetRequestTypeInstance should return *bedrock.BedrockRerankRequest")
}

func Test_createBedrockRerankResponseConverterEmitsBedrockShape(t *testing.T) {
	handlerStore := &mockHandlerStore{}
	route := createBedrockRerankRouteConfig("/bedrock", handlerStore)
	require.NotNil(t, route.RerankResponseConverter)

	resp := &schemas.BifrostRerankResponse{
		Results: []schemas.RerankResult{
			{Index: 1, RelevanceScore: 0.92, Document: &schemas.RerankDocument{Text: "Paris is capital of France"}},
			{Index: 0, RelevanceScore: 0.11, Document: &schemas.RerankDocument{Text: "Berlin is capital of Germany"}},
		},
		Model: "arn:aws:bedrock:us-east-1::foundation-model/cohere.rerank-v3-5:0",
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider: schemas.Bedrock,
		},
	}

	converted, err := route.RerankResponseConverter(nil, resp)
	require.NoError(t, err)

	bedrockResp, ok := converted.(*bedrock.BedrockRerankResponse)
	require.True(t, ok, "converter should emit *bedrock.BedrockRerankResponse")
	require.Len(t, bedrockResp.Results, 2)
	assert.Equal(t, 1, bedrockResp.Results[0].Index)
	assert.InDelta(t, 0.92, bedrockResp.Results[0].RelevanceScore, 1e-9)
	require.NotNil(t, bedrockResp.Results[0].Document)
	require.NotNil(t, bedrockResp.Results[0].Document.TextDocument)
	assert.Equal(t, "Paris is capital of France", bedrockResp.Results[0].Document.TextDocument.Text)
	assert.Equal(t, "Berlin is capital of Germany", bedrockResp.Results[1].Document.TextDocument.Text)

	// AWS SDKs key off camelCase relevanceScore; the Bifrost shape must not leak.
	encoded, err := sonic.Marshal(bedrockResp)
	require.NoError(t, err)
	var payload map[string]interface{}
	require.NoError(t, sonic.Unmarshal(encoded, &payload))
	assert.NotContains(t, payload, "model")
	assert.NotContains(t, payload, "extra_fields")
	results, ok := payload["results"].([]interface{})
	require.True(t, ok)
	first, ok := results[0].(map[string]interface{})
	require.True(t, ok)
	assert.Contains(t, first, "relevanceScore")
	assert.NotContains(t, first, "relevance_score")
}

func Test_createBedrockRerankResponseConverterUsesRawResponse(t *testing.T) {
	handlerStore := &mockHandlerStore{}
	route := createBedrockRerankRouteConfig("/bedrock", handlerStore)
	require.NotNil(t, route.RerankResponseConverter)

	raw := map[string]interface{}{"results": []interface{}{}}
	resp := &schemas.BifrostRerankResponse{
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider:    schemas.Bedrock,
			RawResponse: raw,
		},
	}
	converted, err := route.RerankResponseConverter(nil, resp)
	require.NoError(t, err)
	assert.Equal(t, raw, converted)
}

func Test_createBedrockRerankResponseConverterConvertsForCrossProvider(t *testing.T) {
	handlerStore := &mockHandlerStore{}
	route := createBedrockRerankRouteConfig("/bedrock", handlerStore)

	// A Cohere-shaped raw body must never reach a Bedrock client.
	resp := &schemas.BifrostRerankResponse{
		Results: []schemas.RerankResult{{Index: 0, RelevanceScore: 0.5}},
		ExtraFields: schemas.BifrostResponseExtraFields{
			Provider:    schemas.Cohere,
			RawResponse: map[string]interface{}{"id": "r-123"},
		},
	}
	converted, err := route.RerankResponseConverter(nil, resp)
	require.NoError(t, err)
	bedrockResp, ok := converted.(*bedrock.BedrockRerankResponse)
	require.True(t, ok, "cross-provider responses must be converted, not passed through")
	require.Len(t, bedrockResp.Results, 1)
}

func Test_createBedrockRerankRouteRequestConverter(t *testing.T) {
	handlerStore := &mockHandlerStore{}
	route := createBedrockRerankRouteConfig("/bedrock", handlerStore)
	require.NotNil(t, route.RequestConverter)

	topN := 1
	req := &bedrock.BedrockRerankRequest{
		Queries: []bedrock.BedrockRerankQuery{
			{
				Type:      "TEXT",
				TextQuery: bedrock.BedrockRerankTextRef{Text: "capital of france"},
			},
		},
		Sources: []bedrock.BedrockRerankSource{
			{
				Type: "INLINE",
				InlineDocumentSource: bedrock.BedrockRerankInlineSource{
					Type:         "TEXT",
					TextDocument: &bedrock.BedrockRerankTextValue{Text: "Paris is capital of France"},
				},
			},
		},
		RerankingConfiguration: bedrock.BedrockRerankingConfiguration{
			Type: "BEDROCK_RERANKING_MODEL",
			BedrockRerankingConfiguration: bedrock.BedrockRerankingModelConfiguration{
				NumberOfResults: &topN,
				ModelConfiguration: bedrock.BedrockRerankModelConfiguration{
					ModelARN: "arn:aws:bedrock:us-east-1::foundation-model/cohere.rerank-v3-5:0",
				},
			},
		},
	}

	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bifrostReq, err := route.RequestConverter(bifrostCtx, req)
	require.NoError(t, err)
	require.NotNil(t, bifrostReq)
	require.NotNil(t, bifrostReq.RerankRequest)
	// Converters leave Provider empty; resolution happens later in the
	// modelcatalogresolver PreRequestHook.
	assert.Equal(t, schemas.ModelProvider(""), bifrostReq.RerankRequest.Provider)
	assert.Equal(t, "capital of france", bifrostReq.RerankRequest.Query)
	require.Len(t, bifrostReq.RerankRequest.Documents, 1)
	assert.Equal(t, "Paris is capital of France", bifrostReq.RerankRequest.Documents[0].Text)
	require.NotNil(t, bifrostReq.RerankRequest.Params)
	require.NotNil(t, bifrostReq.RerankRequest.Params.TopN)
	assert.Equal(t, 1, *bifrostReq.RerankRequest.Params.TopN)
	// The live Rerank API returns no document, so the route must not force documents on.
	assert.Nil(t, bifrostReq.RerankRequest.Params.ReturnDocuments)
}

func Test_createBedrockRouteConfigsIncludesRerankForCompositePrefixes(t *testing.T) {
	handlerStore := &mockHandlerStore{}
	prefixes := []string{"/litellm", "/langchain", "/pydanticai"}

	for _, prefix := range prefixes {
		routes := CreateBedrockRouteConfigs(prefix, handlerStore)
		found := false
		for _, route := range routes {
			if route.Path == prefix+"/rerank" && route.Method == "POST" {
				found = true
				break
			}
		}
		assert.Truef(t, found, "expected rerank route for prefix %s", prefix)
	}
}

func Test_createBedrockBatchRouteConfigs(t *testing.T) {
	handlerStore := &mockHandlerStore{}
	routes := createBedrockBatchRouteConfigs("/bedrock", handlerStore)

	assert.Len(t, routes, 4, "should have 4 batch routes")

	expectedRoutes := []struct {
		path   string
		method string
	}{
		{"/bedrock/model-invocation-job", "POST"},
		{"/bedrock/model-invocation-jobs", "GET"},
		{"/bedrock/model-invocation-job/{job_arn}", "GET"},
		{"/bedrock/model-invocation-job/{job_arn}/stop", "POST"},
	}

	for i, expected := range expectedRoutes {
		assert.Equal(t, expected.path, routes[i].Path, "batch route %d path mismatch", i)
		assert.Equal(t, expected.method, routes[i].Method, "batch route %d method mismatch", i)
		assert.Equal(t, RouteConfigTypeBedrock, routes[i].Type, "batch route %d type mismatch", i)
		assert.NotNil(t, routes[i].GetRequestTypeInstance, "batch route %d GetRequestTypeInstance should not be nil", i)
		assert.NotNil(t, routes[i].BatchRequestConverter, "batch route %d BatchCreateRequestConverter should not be nil", i)
		assert.NotNil(t, routes[i].ErrorConverter, "batch route %d ErrorConverter should not be nil", i)
		assert.NotNil(t, routes[i].PreCallback, "batch route %d PreCallback should not be nil", i)
	}
}

func Test_createBedrockFilesRouteConfigs(t *testing.T) {
	handlerStore := &mockHandlerStore{}
	routes := createBedrockFilesRouteConfigs("/bedrock/files", handlerStore)

	assert.Len(t, routes, 5, "should have 5 file routes")

	expectedRoutes := []struct {
		path   string
		method string
	}{
		{"/bedrock/files/{bucket}/{key:*}", "PUT"},
		{"/bedrock/files/{bucket}/{key:*}", "GET"},
		{"/bedrock/files/{bucket}/{key:*}", "HEAD"},
		{"/bedrock/files/{bucket}/{key:*}", "DELETE"},
		{"/bedrock/files/{bucket}", "GET"},
	}

	for i, expected := range expectedRoutes {
		assert.Equal(t, expected.path, routes[i].Path, "file route %d path mismatch", i)
		assert.Equal(t, expected.method, routes[i].Method, "file route %d method mismatch", i)
		assert.Equal(t, RouteConfigTypeBedrock, routes[i].Type, "file route %d type mismatch", i)
		assert.NotNil(t, routes[i].GetRequestTypeInstance, "file route %d GetRequestTypeInstance should not be nil", i)
		assert.NotNil(t, routes[i].ErrorConverter, "file route %d ErrorConverter should not be nil", i)
	}
}

func Test_parseS3PutObjectRequest(t *testing.T) {
	tests := []struct {
		name         string
		bucket       string
		key          string
		body         []byte
		wantErr      bool
		wantBucket   string
		wantKey      string
		wantFilename string
	}{
		{
			name:         "valid request",
			bucket:       "my-bucket",
			key:          "folder/file.jsonl",
			body:         []byte(`{"test": "data"}`),
			wantErr:      false,
			wantBucket:   "my-bucket",
			wantKey:      "folder/file.jsonl",
			wantFilename: "file.jsonl",
		},
		{
			name:         "simple key without folder",
			bucket:       "bucket",
			key:          "file.txt",
			body:         []byte("content"),
			wantErr:      false,
			wantBucket:   "bucket",
			wantKey:      "file.txt",
			wantFilename: "file.txt",
		},
		{
			name:    "missing bucket",
			bucket:  "",
			key:     "file.txt",
			body:    []byte("content"),
			wantErr: true,
		},
		{
			name:    "missing key",
			bucket:  "bucket",
			key:     "",
			body:    []byte("content"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			ctx.Request.SetBody(tt.body)

			if tt.bucket != "" {
				ctx.SetUserValue("bucket", tt.bucket)
			}
			if tt.key != "" {
				ctx.SetUserValue("key", tt.key)
			}

			req := &bedrock.BedrockFileUploadRequest{}
			err := parseS3PutObjectRequest(ctx, req)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.wantBucket, req.Bucket)
			assert.Equal(t, tt.wantKey, req.Key)
			assert.Equal(t, tt.wantFilename, req.Filename)
			assert.Equal(t, "batch", req.Purpose)
			assert.Equal(t, tt.body, req.Body)
		})
	}
}

func Test_parseS3PutObjectRequest_invalidType(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	ctx.SetUserValue("bucket", "bucket")
	ctx.SetUserValue("key", "key")

	// Pass wrong type
	err := parseS3PutObjectRequest(ctx, "invalid type")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid request type")
}

func Test_s3PutObjectPostCallback(t *testing.T) {
	tests := []struct {
		name       string
		response   interface{}
		wantStatus int
		wantETag   string
	}{
		{
			name: "valid response with ID",
			response: &schemas.BifrostFileUploadResponse{
				ID: "file-123",
			},
			wantStatus: 200,
			wantETag:   "\"file-123\"",
		},
		{
			name:       "nil response",
			response:   nil,
			wantStatus: 200,
			wantETag:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			err := s3PutObjectPostCallback(ctx, nil, tt.response)

			assert.NoError(t, err)
			assert.Equal(t, tt.wantStatus, ctx.Response.StatusCode())
			assert.Equal(t, "application/xml", string(ctx.Response.Header.ContentType()))
			assert.Equal(t, "bifrost", string(ctx.Response.Header.Peek("x-amz-request-id")))

			if tt.wantETag != "" {
				assert.Equal(t, tt.wantETag, string(ctx.Response.Header.Peek("ETag")))
			}
		})
	}
}

func Test_s3GetObjectPostCallback(t *testing.T) {
	tests := []struct {
		name            string
		response        interface{}
		wantContentType string
		wantLength      string
		wantETag        string
	}{
		{
			name: "valid response",
			response: &schemas.BifrostFileContentResponse{
				Content:     []byte("test content"),
				ContentType: "application/json",
				FileID:      "file-456",
			},
			wantContentType: "application/json",
			wantLength:      "12",
			wantETag:        "\"file-456\"",
		},
		{
			name:            "nil response",
			response:        nil,
			wantContentType: "",
			wantLength:      "",
			wantETag:        "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			err := s3GetObjectPostCallback(ctx, nil, tt.response)

			assert.NoError(t, err)

			if tt.wantContentType != "" {
				assert.Equal(t, tt.wantContentType, string(ctx.Response.Header.Peek("Content-Type")))
				assert.Equal(t, tt.wantLength, string(ctx.Response.Header.Peek("Content-Length")))
				assert.Equal(t, "bifrost", string(ctx.Response.Header.Peek("x-amz-request-id")))
			}

			if tt.wantETag != "" {
				assert.Equal(t, tt.wantETag, string(ctx.Response.Header.Peek("ETag")))
			}
		})
	}
}

func Test_s3HeadObjectPostCallback(t *testing.T) {
	tests := []struct {
		name       string
		response   interface{}
		wantStatus int
		wantLength string
		wantETag   string
	}{
		{
			name: "valid response",
			response: &schemas.BifrostFileRetrieveResponse{
				ID:    "file-789",
				Bytes: 1024,
			},
			wantStatus: 200,
			wantLength: "1024",
			wantETag:   "\"file-789\"",
		},
		{
			name:       "nil response",
			response:   nil,
			wantStatus: 200,
			wantLength: "",
			wantETag:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			err := s3HeadObjectPostCallback(ctx, nil, tt.response)

			assert.NoError(t, err)
			assert.Equal(t, tt.wantStatus, ctx.Response.StatusCode())

			if tt.wantLength != "" {
				assert.Equal(t, "application/octet-stream", string(ctx.Response.Header.Peek("Content-Type")))
				assert.Equal(t, tt.wantLength, string(ctx.Response.Header.Peek("Content-Length")))
				assert.Equal(t, "bifrost", string(ctx.Response.Header.Peek("x-amz-request-id")))
				assert.Equal(t, tt.wantETag, string(ctx.Response.Header.Peek("ETag")))
			}
		})
	}
}

func Test_s3DeleteObjectPostCallback(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	err := s3DeleteObjectPostCallback(ctx, nil, nil)

	assert.NoError(t, err)
	assert.Equal(t, 204, ctx.Response.StatusCode())
	assert.Equal(t, "bifrost", string(ctx.Response.Header.Peek("x-amz-request-id")))
}

func Test_s3ListObjectsV2PostCallback(t *testing.T) {
	ctx := &fasthttp.RequestCtx{}
	err := s3ListObjectsV2PostCallback(ctx, nil, nil)

	assert.NoError(t, err)
	assert.Equal(t, "application/xml", string(ctx.Response.Header.ContentType()))
	assert.Equal(t, "bifrost", string(ctx.Response.Header.Peek("x-amz-request-id")))
}

func Test_extractBedrockBatchListQueryParams(t *testing.T) {
	handlerStore := &mockHandlerStore{}

	tests := []struct {
		name           string
		queryParams    map[string]string
		wantMaxResults int
		wantNextToken  string
		wantStatus     string
		wantName       string
	}{
		{
			name: "all params",
			queryParams: map[string]string{
				"maxResults":   "50",
				"nextToken":    "token123",
				"statusEquals": "InProgress",
				"nameContains": "test-job",
			},
			wantMaxResults: 50,
			wantNextToken:  "token123",
			wantStatus:     "InProgress",
			wantName:       "test-job",
		},
		{
			name:           "no params",
			queryParams:    map[string]string{},
			wantMaxResults: 0,
			wantNextToken:  "",
			wantStatus:     "",
			wantName:       "",
		},
		{
			name: "invalid maxResults",
			queryParams: map[string]string{
				"maxResults": "invalid",
			},
			wantMaxResults: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			for k, v := range tt.queryParams {
				ctx.QueryArgs().Add(k, v)
			}

			req := &bedrock.BedrockBatchListRequest{}
			callback := extractBedrockBatchListQueryParams(handlerStore)

			bifrostCtx := createTestBifrostContext()
			err := callback(ctx, bifrostCtx, req)

			assert.NoError(t, err)
			assert.Equal(t, tt.wantMaxResults, req.MaxResults)
			assert.Equal(t, tt.wantStatus, req.StatusEquals)
			assert.Equal(t, tt.wantName, req.NameContains)

			if tt.wantNextToken != "" {
				assert.NotNil(t, req.NextToken)
				assert.Equal(t, tt.wantNextToken, *req.NextToken)
			}
		})
	}
}

func Test_extractBedrockJobArnFromPath(t *testing.T) {
	handlerStore := &mockHandlerStore{}

	tests := []struct {
		name        string
		jobArn      interface{}
		provider    schemas.ModelProvider
		wantErr     bool
		wantJobArn  string
		errContains string
	}{
		{
			name:       "valid job ARN for Bedrock",
			jobArn:     "arn:aws:bedrock:us-east-1:123456789012:batch:job-123",
			provider:   schemas.Bedrock,
			wantErr:    false,
			wantJobArn: "arn:aws:bedrock:us-east-1:123456789012:batch:job-123",
		},
		{
			name:       "URL encoded job ARN",
			jobArn:     "arn%3Aaws%3Abedrock%3Aus-east-1%3A123456789012%3Abatch%3Ajob-123",
			provider:   schemas.Bedrock,
			wantErr:    false,
			wantJobArn: "arn:aws:bedrock:us-east-1:123456789012:batch:job-123",
		},
		{
			name:       "non-Bedrock provider strips ARN prefix",
			jobArn:     "arn:aws:bedrock:us-east-1:444444444444:batch:job-456",
			provider:   schemas.OpenAI,
			wantErr:    false,
			wantJobArn: "job-456",
		},
		{
			name:        "missing job_arn",
			jobArn:      nil,
			provider:    schemas.Bedrock,
			wantErr:     true,
			errContains: "job_arn is required",
		},
		{
			name:        "empty job_arn",
			jobArn:      "",
			provider:    schemas.Bedrock,
			wantErr:     true,
			errContains: "job_arn must be a non-empty string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			if tt.jobArn != nil {
				ctx.SetUserValue("job_arn", tt.jobArn)
			}

			req := &bedrock.BedrockBatchRetrieveRequest{}
			callback := extractBedrockJobArnFromPath(handlerStore)

			bifrostCtx := createTestBifrostContextWithProvider(tt.provider)
			err := callback(ctx, bifrostCtx, req)

			if tt.wantErr {
				assert.Error(t, err)
				if tt.errContains != "" {
					assert.Contains(t, err.Error(), tt.errContains)
				}
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.wantJobArn, req.JobIdentifier)
		})
	}
}

func Test_extractS3ListObjectsV2Params(t *testing.T) {
	handlerStore := &mockHandlerStore{}

	tests := []struct {
		name                  string
		bucket                string
		queryParams           map[string]string
		wantErr               bool
		wantBucket            string
		wantPrefix            string
		wantMaxKeys           int
		wantContinuationToken string
	}{
		{
			name:   "all params",
			bucket: "my-bucket",
			queryParams: map[string]string{
				"prefix":             "folder/",
				"max-keys":           "100",
				"continuation-token": "token-abc",
			},
			wantErr:               false,
			wantBucket:            "my-bucket",
			wantPrefix:            "folder/",
			wantMaxKeys:           100,
			wantContinuationToken: "token-abc",
		},
		{
			name:        "bucket only",
			bucket:      "simple-bucket",
			queryParams: map[string]string{},
			wantErr:     false,
			wantBucket:  "simple-bucket",
			wantPrefix:  "",
			wantMaxKeys: 1000,
		},
		{
			name:    "missing bucket",
			bucket:  "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			if tt.bucket != "" {
				ctx.SetUserValue("bucket", tt.bucket)
			}
			for k, v := range tt.queryParams {
				ctx.QueryArgs().Add(k, v)
			}

			req := &bedrock.BedrockFileListRequest{}
			callback := extractS3ListObjectsV2Params(handlerStore)

			bifrostCtx := createTestBifrostContext()
			err := callback(ctx, bifrostCtx, req)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.wantBucket, req.Bucket)
			assert.Equal(t, tt.wantPrefix, req.Prefix)
			assert.Equal(t, tt.wantMaxKeys, req.MaxKeys)
			assert.Equal(t, tt.wantContinuationToken, req.ContinuationToken)

			// Verify context values
			assert.Equal(t, tt.wantBucket, bifrostCtx.Value(s3ContextKeyBucket))
			assert.Equal(t, tt.wantPrefix, bifrostCtx.Value(s3ContextKeyPrefix))
			assert.Equal(t, tt.wantMaxKeys, bifrostCtx.Value(s3ContextKeyMaxKeys))
		})
	}
}

func Test_extractS3BucketKeyFromPath(t *testing.T) {
	handlerStore := &mockHandlerStore{}

	tests := []struct {
		name       string
		bucket     string
		key        string
		fileID     string
		opType     string
		wantErr    bool
		wantBucket string
		wantKey    string
		wantS3URI  string
	}{
		{
			name:       "content operation",
			bucket:     "my-bucket",
			key:        "path/to/file.txt",
			fileID:     "file-123",
			opType:     "content",
			wantErr:    false,
			wantBucket: "my-bucket",
			wantKey:    "path/to/file.txt",
			wantS3URI:  "s3://my-bucket/path/to/file.txt",
		},
		{
			name:    "missing bucket",
			bucket:  "",
			key:     "file.txt",
			opType:  "content",
			wantErr: true,
		},
		{
			name:    "missing key",
			bucket:  "bucket",
			key:     "",
			opType:  "content",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := &fasthttp.RequestCtx{}
			if tt.bucket != "" {
				ctx.SetUserValue("bucket", tt.bucket)
			}
			if tt.key != "" {
				ctx.SetUserValue("key", tt.key)
			}
			if tt.fileID != "" {
				ctx.Request.Header.Set("If-Match", tt.fileID)
			}

			callback := extractS3BucketKeyFromPath(handlerStore, tt.opType)
			bifrostCtx := createTestBifrostContext()

			var req interface{}
			switch tt.opType {
			case "content":
				req = &bedrock.BedrockFileContentRequest{}
			case "retrieve":
				req = &bedrock.BedrockFileRetrieveRequest{}
			case "delete":
				req = &bedrock.BedrockFileDeleteRequest{}
			}

			err := callback(ctx, bifrostCtx, req)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)

			switch r := req.(type) {
			case *bedrock.BedrockFileContentRequest:
				assert.Equal(t, tt.wantBucket, r.Bucket)
				assert.Equal(t, tt.wantKey, r.Prefix)
				assert.Equal(t, tt.wantS3URI, r.S3Uri)
				assert.Equal(t, tt.fileID, r.ETag)
			}
		})
	}
}

// Helper functions for creating test contexts

func createTestBifrostContext() *schemas.BifrostContext {
	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bifrostCtx.SetValue(bifrostContextKeyProvider, schemas.Bedrock)
	return bifrostCtx
}

func createTestBifrostContextWithProvider(provider schemas.ModelProvider) *schemas.BifrostContext {
	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	bifrostCtx.SetValue(bifrostContextKeyProvider, provider)
	return bifrostCtx
}

// Test_createBedrockInvokeRouteConfig_MarksAnthropicInvokeIngress is the transport half of
// #7649. AWS Converse never reports a thinking-token breakdown, so a request that arrives on
// the InvokeModel-shaped ingress with thinking requested has to be served by InvokeModel
// upstream to keep usage.output_tokens_details.thinking_tokens. The ingress cannot pick the
// upstream API itself (that is the provider's call), so it marks the context and the Bedrock
// provider's routing predicate keys on the marker. Both invoke routes must set it.
func Test_createBedrockInvokeRouteConfig_MarksAnthropicInvokeIngress(t *testing.T) {
	marker := schemas.BifrostContextKey("bedrock-anthropic-invoke-ingress")
	body := `{"anthropic_version":"bedrock-2023-05-31","max_tokens":64,"messages":[{"role":"user","content":"hi"}],"thinking":{"type":"adaptive"}}`

	for _, tc := range []struct {
		name  string
		route RouteConfig
	}{
		{name: "invoke", route: createBedrockInvokeRouteConfig("/bedrock", &mockHandlerStore{})},
		{name: "invoke-with-response-stream", route: createBedrockInvokeWithResponseStreamRouteConfig("/bedrock", &mockHandlerStore{})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := &bedrock.BedrockInvokeRequest{}
			require.NoError(t, sonic.Unmarshal([]byte(body), req))
			req.ModelID = "bedrock/global.anthropic.claude-sonnet-5"

			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			ctx.SetValue(schemas.BifrostContextKeyHTTPRequestType, schemas.ResponsesRequest)

			bifrostReq, err := tc.route.RequestConverter(ctx, req)
			require.NoError(t, err)
			require.NotNil(t, bifrostReq.ResponsesRequest)
			require.NotNil(t, bifrostReq.ResponsesRequest.Params)
			require.NotNil(t, bifrostReq.ResponsesRequest.Params.Reasoning, "thinking must survive the ingress conversion")

			marked, _ := ctx.Value(marker).(bool)
			assert.True(t, marked, "the InvokeModel-shaped ingress must mark the context so the Bedrock provider can route thinking requests to InvokeModel upstream")
		})
	}
}

// Test_bedrockPreCallback_ReadsInvokeGuardrailHeaders pins the InvokeModel ingress half of
// #7696's follow-up: the X-Amzn-Bedrock-Guardrail* request headers, the AWS-native way to
// name a guardrail on InvokeModel, reach the canonical guardrailConfig extra param.
func Test_bedrockPreCallback_ReadsInvokeGuardrailHeaders(t *testing.T) {
	body := `{"anthropic_version":"bedrock-2023-05-31","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`
	for _, tc := range []struct {
		name  string
		route RouteConfig
	}{
		{name: "invoke", route: createBedrockInvokeRouteConfig("/bedrock", &mockHandlerStore{})},
		{name: "invoke-with-response-stream", route: createBedrockInvokeWithResponseStreamRouteConfig("/bedrock", &mockHandlerStore{})},
	} {
		t.Run(tc.name+"/headers present", func(t *testing.T) {
			httpCtx := &fasthttp.RequestCtx{}
			httpCtx.SetUserValue("modelId", "global.anthropic.claude-haiku-4-5-20251001-v1:0")
			httpCtx.Request.Header.Set("X-Amzn-Bedrock-GuardrailIdentifier", "gr-header")
			httpCtx.Request.Header.Set("X-Amzn-Bedrock-GuardrailVersion", "3")
			httpCtx.Request.Header.Set("X-Amzn-Bedrock-Trace", "enabled")

			req := &bedrock.BedrockInvokeRequest{}
			require.NoError(t, sonic.Unmarshal([]byte(body), req))
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			ctx.SetValue(schemas.BifrostContextKeyHTTPRequestType, schemas.ResponsesRequest)
			require.NoError(t, tc.route.PreCallback(httpCtx, ctx, req))

			bifrostReq, err := tc.route.RequestConverter(ctx, req)
			require.NoError(t, err)
			require.NotNil(t, bifrostReq.ResponsesRequest)
			require.NotNil(t, bifrostReq.ResponsesRequest.Params)
			config, ok := bifrostReq.ResponsesRequest.Params.ExtraParams["guardrailConfig"].(map[string]interface{})
			require.True(t, ok, "guardrail headers must reach the canonical guardrailConfig extra param: %v", bifrostReq.ResponsesRequest.Params.ExtraParams)
			assert.Equal(t, "gr-header", config["guardrailIdentifier"])
			assert.Equal(t, "3", config["guardrailVersion"])
			assert.Equal(t, "enabled", config["trace"])
		})
		t.Run(tc.name+"/headers absent", func(t *testing.T) {
			httpCtx := &fasthttp.RequestCtx{}
			httpCtx.SetUserValue("modelId", "global.anthropic.claude-haiku-4-5-20251001-v1:0")
			req := &bedrock.BedrockInvokeRequest{}
			require.NoError(t, sonic.Unmarshal([]byte(body), req))
			ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			ctx.SetValue(schemas.BifrostContextKeyHTTPRequestType, schemas.ResponsesRequest)
			require.NoError(t, tc.route.PreCallback(httpCtx, ctx, req))
			bifrostReq, err := tc.route.RequestConverter(ctx, req)
			require.NoError(t, err)
			if bifrostReq.ResponsesRequest.Params != nil {
				_, has := bifrostReq.ResponsesRequest.Params.ExtraParams["guardrailConfig"]
				assert.False(t, has, "no guardrail must be invented without headers")
			}
		})
	}
}
