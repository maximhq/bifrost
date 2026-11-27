package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/gemini"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

func newInteractionsRequestCtx(path string) *fasthttp.RequestCtx {
	ctx := &fasthttp.RequestCtx{}
	ctx.Request.SetRequestURI(path)
	return ctx
}

func parseInteractionsRequest(t *testing.T, body string) *gemini.GeminiInteractionsRequest {
	t.Helper()
	var req gemini.GeminiInteractionsRequest
	require.NoError(t, sonic.Unmarshal([]byte(body), &req))
	return &req
}

// TestGenAIInteractionsRoutesRegistered pins the paths: the Gemini API's /v1beta and /v1
// collections and the Vertex project/location shape, each with get, delete and cancel.
func TestGenAIInteractionsRoutesRegistered(t *testing.T) {
	got := map[string]bool{}
	for _, route := range CreateGenAIInteractionsRouteConfigs("/genai", nil) {
		got[route.Method+" "+route.Path] = true
	}
	for _, want := range []string{
		"POST /genai/v1beta/interactions",
		"GET /genai/v1beta/interactions/{interaction_id}",
		"DELETE /genai/v1beta/interactions/{interaction_id}",
		"POST /genai/v1beta/interactions/{interaction_id}/cancel",
		"POST /genai/v1/interactions",
		"POST /genai/v1/projects/{project}/locations/{location}/interactions",
		"GET /genai/v1beta1/projects/{project}/locations/{location}/interactions/{interaction_id}",
	} {
		assert.True(t, got[want], want)
	}
}

// TestGenAIInteractionsCreateProvider pins how a create picks its path: a request for Gemini or
// Vertex forwards the caller's body and returns Google's (native), and one for another provider is
// translated, refusing agent-only fields it would otherwise drop.
func TestGenAIInteractionsCreateProvider(t *testing.T) {
	cases := []struct {
		name       string
		path       string
		header     string
		body       string
		wantNative bool
		wantErr    string
		wantProv   schemas.ModelProvider
	}{
		{name: "bare model goes to gemini natively", path: "/genai/v1beta/interactions", body: `{"model":"gemini-3.8-flash","input":"hi"}`, wantNative: true, wantProv: schemas.Gemini},
		{name: "vertex path goes to vertex natively", path: "/genai/v1/projects/p/locations/global/interactions", body: `{"model":"gemini-3.8-flash","input":"hi"}`, wantNative: true, wantProv: schemas.Vertex},
		{name: "agent goes to gemini natively", path: "/genai/v1beta/interactions", body: `{"agent":"deep-research-preview-04-2026","input":"go","background":true,"agent_config":{"type":"deep-research"}}`, wantNative: true, wantProv: schemas.Gemini},
		{name: "provider prefix translates", path: "/genai/v1beta/interactions", body: `{"model":"openai/gpt-5","input":"hi"}`, wantNative: false, wantProv: schemas.OpenAI},
		{name: "x-model-provider translates", path: "/genai/v1beta/interactions", header: "anthropic", body: `{"model":"claude-sonnet-5-5","input":"hi"}`, wantNative: false, wantProv: schemas.Anthropic},
		{name: "agent fields on another provider are refused", path: "/genai/v1beta/interactions", body: `{"model":"openai/gpt-5","input":"hi","environment":{"type":"remote"}}`, wantErr: "environment"},
		{name: "agent fields with a fallback to another provider are refused", path: "/genai/v1beta/interactions", body: `{"model":"gemini-3.8-flash","input":"hi","environment":{"type":"remote"},"fallbacks":["openai/gpt-5"]}`, wantErr: "environment"},
		{name: "agent fields with a fallback inside Google's family stay native", path: "/genai/v1beta/interactions", body: `{"model":"gemini-3.8-flash","input":"hi","environment":{"type":"remote"},"fallbacks":["vertex/gemini-3.8-flash"]}`, wantNative: true, wantProv: schemas.Gemini},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := newInteractionsRequestCtx(tc.path)
			if tc.header != "" {
				ctx.Request.Header.Set("x-model-provider", tc.header)
			}
			ctx.Request.Header.Set("Api-Revision", "2026-05-20")
			bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			req := parseInteractionsRequest(t, tc.body)

			err := prepareGenAIInteractionsCreate(nil)(ctx, bifrostCtx, req)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			native, _ := bifrostCtx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool)
			assert.Equal(t, tc.wantNative, native)
			override, _ := bifrostCtx.Value(schemas.BifrostContextKeyPassthroughOverridesPresent).(bool)
			assert.Equal(t, tc.wantNative, override, "the native path's raw-response request counts without the transport's per-request override")
			interactions, _ := bifrostCtx.Value(schemas.BifrostContextKeyInteractionsAPI).(bool)
			assert.True(t, interactions)
			headers, _ := bifrostCtx.Value(schemas.BifrostContextKeyExtraHeaders).(map[string][]string)
			assert.Equal(t, []string{"2026-05-20"}, headers["Api-Revision"])

			converted, convErr := convertGenAIInteractionsCreate(bifrostCtx, req)
			require.NoError(t, convErr)
			assert.Equal(t, tc.wantProv, converted.ResponsesRequest.Provider)
		})
	}
}

// TestGenAIInteractionsResponseConverter pins the two answers: Google's own bytes on the native
// path, and an Interaction rendered from the Responses result once the attempt was translated
// (core turns the raw body off for a provider outside Google's family).
func TestGenAIInteractionsResponseConverter(t *testing.T) {
	native := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	native.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
	raw := map[string]interface{}{"id": "v1_raw", "object": "interaction"}
	resp := &schemas.BifrostResponsesResponse{ID: schemas.Ptr("v1_raw"), Model: "gemini-3.8-flash", Status: schemas.Ptr("completed")}
	resp.ExtraFields.RawResponse = raw
	out, err := genAIInteractionsResponseConverter(native, resp)
	require.NoError(t, err)
	assert.Equal(t, raw, out)

	translated := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	translated.SetValue(schemas.BifrostContextKeyUseRawRequestBody, false)
	out, err = genAIInteractionsResponseConverter(translated, resp)
	require.NoError(t, err)
	interaction, ok := out.(*gemini.GeminiInteraction)
	require.True(t, ok)
	assert.Equal(t, "v1_raw", interaction.ID)
	assert.Equal(t, gemini.GeminiInteractionStatusCompleted, interaction.Status)
}

// TestGenAIInteractionsStreamConverter pins the SSE framing: native chunks replay Google's event
// once (chunks without one are skipped) and the stream ends with Google's done frame; a translated
// stream is rendered as Interactions events.
func TestGenAIInteractionsStreamConverter(t *testing.T) {
	native := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	native.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)

	chunk := &schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeOutputTextDelta}
	chunk.ExtraFields.RawResponse = `{"event_type":"step.delta","index":0,"delta":{"type":"text","text":"Hi"}}`
	_, frame, err := genAIInteractionsStreamConverter(native, chunk)
	require.NoError(t, err)
	assert.Equal(t, "event: step.delta\ndata: {\"event_type\":\"step.delta\",\"index\":0,\"delta\":{\"type\":\"text\",\"text\":\"Hi\"}}\n\n", frame)

	_, frame, err = genAIInteractionsStreamConverter(native, &schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeContentPartAdded})
	require.NoError(t, err)
	assert.Nil(t, frame, "a chunk with no upstream event repeats one already sent")

	done := &schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeCompleted}
	done.ExtraFields.RawResponse = `{"event_type":"interaction.completed","interaction":{"id":"v1"}}`
	_, frame, err = genAIInteractionsStreamConverter(native, done)
	require.NoError(t, err)
	assert.True(t, strings.HasSuffix(frame.(string), "event: done\ndata: [DONE]\n\n"))

	translated := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, frame, err = genAIInteractionsStreamConverter(translated, &schemas.BifrostResponsesStreamResponse{
		Type:     schemas.ResponsesStreamResponseTypeCreated,
		Response: &schemas.BifrostResponsesResponse{ID: schemas.Ptr("resp_1"), Model: "gpt-5"},
	})
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(frame.(string), "event: interaction.created\ndata: {"), frame)
}

// TestGenAIInteractionsLifecycleRequest pins the get, cancel and delete fields: the id from the path,
// the provider from ?provider= (else the path and token shape), and stream resume parameters.
func TestGenAIInteractionsLifecycleRequest(t *testing.T) {
	ctx := newInteractionsRequestCtx("/genai/v1beta/interactions/v1_abc?stream=true&last_event_id=evt_7")
	ctx.SetUserValue("interaction_id", "v1_abc")
	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	retrieve := &schemas.BifrostResponsesRetrieveRequest{}
	require.NoError(t, prepareGenAIInteractionsLifecycle(nil)(ctx, bifrostCtx, retrieve))
	assert.Equal(t, "v1_abc", retrieve.ResponseID)
	assert.Equal(t, schemas.Gemini, retrieve.Provider)
	assert.True(t, retrieve.IsStreamingRequested())
	assert.Equal(t, "evt_7", *retrieve.LastEventID)
	native, _ := bifrostCtx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool)
	assert.True(t, native)

	ctx = newInteractionsRequestCtx("/genai/v1/projects/p/locations/global/interactions/v1_abc/cancel")
	ctx.SetUserValue("interaction_id", "v1_abc")
	cancel := &schemas.BifrostResponsesCancelRequest{}
	require.NoError(t, prepareGenAIInteractionsLifecycle(nil)(ctx, schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), cancel))
	assert.Equal(t, schemas.Vertex, cancel.Provider)

	ctx = newInteractionsRequestCtx("/genai/v1beta/interactions/resp_1?provider=openai")
	ctx.SetUserValue("interaction_id", "resp_1")
	bifrostCtx = schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	del := &schemas.BifrostResponsesDeleteRequest{}
	require.NoError(t, prepareGenAIInteractionsLifecycle(nil)(ctx, bifrostCtx, del))
	assert.Equal(t, schemas.OpenAI, del.Provider)
	native, _ = bifrostCtx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool)
	assert.False(t, native)
}

// TestGenAIInteractionsVertexPathPicksKey pins that a Vertex path's project and location reach key
// selection, on a create and on a call by id, so a key for another location is not used.
func TestGenAIInteractionsVertexPathPicksKey(t *testing.T) {
	prepare := map[string]func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext) error{
		"create": func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext) error {
			return prepareGenAIInteractionsCreate(nil)(ctx, bifrostCtx, parseInteractionsRequest(t, `{"model":"gemini-3.8-flash","input":"hi"}`))
		},
		"get": func(ctx *fasthttp.RequestCtx, bifrostCtx *schemas.BifrostContext) error {
			ctx.SetUserValue("interaction_id", "v1_abc")
			return prepareGenAIInteractionsLifecycle(nil)(ctx, bifrostCtx, &schemas.BifrostResponsesRetrieveRequest{})
		},
	}
	for name, run := range prepare {
		t.Run(name, func(t *testing.T) {
			ctx := newInteractionsRequestCtx("/genai/v1beta1/projects/p1/locations/eu/interactions")
			ctx.SetUserValue("project", "p1")
			ctx.SetUserValue("location", "eu")
			bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			require.NoError(t, run(ctx, bifrostCtx))
			assert.Equal(t, "eu", bifrostCtx.Value(schemas.BifrostContextKeyInteractionsLocation))
			assert.Equal(t, "p1", bifrostCtx.Value(schemas.BifrostContextKeyInteractionsProject))

			plain := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			require.NoError(t, run(newInteractionsRequestCtx("/genai/v1beta/interactions"), plain))
			assert.Nil(t, plain.Value(schemas.BifrostContextKeyInteractionsLocation))
		})
	}
}

// TestGenAIInteractionsConversionErrorNamesReason pins that a request the route cannot convert is
// answered with why, under Google's invalid_request code, instead of the router's generic message.
func TestGenAIInteractionsConversionErrorNamesReason(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, err := convertGenAIInteractionsCreate(ctx, parseInteractionsRequest(t, `{"model":"openai/gpt-4o-mini","input":"hi","tools":[{"type":"google_maps"}]}`))
	require.Error(t, err)
	body, ok := genAIInteractionsErrorConverter(ctx, newBifrostError(err, "failed to convert request to Bifrost format")).(*gemini.GeminiDecisionError)
	require.True(t, ok)
	assert.Contains(t, body.Error.Message, "google_maps")
	assert.Equal(t, "invalid_request", body.Error.Code)

	denied := &schemas.BifrostError{StatusCode: schemas.Ptr(403), Error: &schemas.ErrorField{Message: "Model 'x' is not allowed"}}
	body, ok = genAIInteractionsErrorConverter(ctx, denied).(*gemini.GeminiDecisionError)
	require.True(t, ok)
	assert.Equal(t, "permission_denied", body.Error.Code)
	assert.Equal(t, "Model 'x' is not allowed", body.Error.Message)
}

// interactionsProviderStore is a handler store that knows one custom provider.
type interactionsProviderStore struct {
	lib.HandlerStore
	name, base schemas.ModelProvider
}

func (s interactionsProviderStore) GetCustomProviderPassthrough(provider schemas.ModelProvider) (schemas.ModelProvider, bool, bool) {
	return s.base, false, provider == s.name
}

// TestGenAIInteractionsCustomGoogleProvider pins that a custom provider backed by Gemini or Vertex takes
// the native path, as the request, as a fallback, and on a call by id. One backed by another provider
// is translated, so it refuses agent-only fields.
func TestGenAIInteractionsCustomGoogleProvider(t *testing.T) {
	schemas.RegisterKnownProvider("my-google")
	t.Cleanup(func() { schemas.UnregisterKnownProvider("my-google") })
	for base, native := range map[schemas.ModelProvider]bool{schemas.Gemini: true, schemas.Vertex: true, schemas.OpenAI: false} {
		t.Run(string(base), func(t *testing.T) {
			store := interactionsProviderStore{name: "my-google", base: base}
			run := func(body, header string) (*schemas.BifrostContext, error) {
				ctx := newInteractionsRequestCtx("/genai/v1beta/interactions")
				if header != "" {
					ctx.Request.Header.Set("x-model-provider", header)
				}
				bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
				return bifrostCtx, prepareGenAIInteractionsCreate(store)(ctx, bifrostCtx, parseInteractionsRequest(t, body))
			}
			const agent = `{"agent":"deep-research-preview-04-2026","input":"hi"`
			bifrostCtx, err := run(agent+`}`, "my-google")
			useRaw, _ := bifrostCtx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool)
			assert.Equal(t, native, err == nil, "agent request: %v", err)
			assert.Equal(t, native, useRaw)

			_, err = run(agent+`,"fallbacks":["my-google/deep-research-preview-04-2026"]}`, "")
			assert.Equal(t, native, err == nil, "agent request with the custom provider as a fallback: %v", err)

			ctx := newInteractionsRequestCtx("/genai/v1beta/interactions/v1_abc?provider=my-google")
			ctx.SetUserValue("interaction_id", "v1_abc")
			bifrostCtx = schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			require.NoError(t, prepareGenAIInteractionsLifecycle(store)(ctx, bifrostCtx, &schemas.BifrostResponsesRetrieveRequest{}))
			useRaw, _ = bifrostCtx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool)
			assert.Equal(t, native, useRaw)
		})
	}
}

// TestRewriteGenAIInteractionsRawRequestBody pins guardrail redaction on the native path: text in
// the system instruction, a string input, content blocks, steps and function results is rewritten,
// and nothing else in the body is touched.
func TestRewriteGenAIInteractionsRawRequestBody(t *testing.T) {
	body := `{"model":"gemini-3.8-flash","system_instruction":"secret-a policy","input":[{"type":"user_input","content":[{"type":"text","text":"call secret-b"}]},{"type":"function_result","call_id":"c","result":[{"type":"text","text":"secret-c"}]}],"generation_config":{"stop_sequences":["secret-z"]}}`
	out, err := rewriteGenAIInteractionsRawRequestBody([]byte(body), map[string]string{"secret-a": "[A]", "secret-b": "[B]", "secret-c": "[C]"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"model":"gemini-3.8-flash","system_instruction":"[A] policy","input":[{"type":"user_input","content":[{"type":"text","text":"call [B]"}]},{"type":"function_result","call_id":"c","result":[{"type":"text","text":"[C]"}]}],"generation_config":{"stop_sequences":["secret-z"]}}`, string(out))

	// An object-valued function result is sent as JSON text, so its string leaves are redacted too.
	out, err = rewriteGenAIInteractionsRawRequestBody([]byte(`{"model":"m","input":[{"type":"text","text":"about secret-e"},{"type":"function_result","call_id":"c","result":{"note":"secret-e here","nested":{"list":["secret-e"]},"n":1}}]}`), map[string]string{"secret-e": "[E]"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"model":"m","input":[{"type":"text","text":"about [E]"},{"type":"function_result","call_id":"c","result":{"note":"[E] here","nested":{"list":["[E]"]},"n":1}}]}`, string(out))

	// So are those of an array-valued one, whatever keys hold them.
	out, err = rewriteGenAIInteractionsRawRequestBody([]byte(`{"model":"m","input":[{"type":"text","text":"about secret-f"},{"type":"function_result","call_id":"c","result":["secret-f",{"note":"secret-f"},[{"deep":"secret-f"}],3]}]}`), map[string]string{"secret-f": "[F]"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"model":"m","input":[{"type":"text","text":"about [F]"},{"type":"function_result","call_id":"c","result":["[F]",{"note":"[F]"},[{"deep":"[F]"}],3]}]}`, string(out))

	out, err = rewriteGenAIInteractionsRawRequestBody([]byte(`{"model":"m","input":"hello secret-d"}`), map[string]string{"secret-d": "[D]"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"model":"m","input":"hello [D]"}`, string(out))
}

// TestGenAIInteractionsStreamConfigNoHeartbeat pins that an Interactions stream sends no heartbeat
// comment: Google's SDKs parse every line, and the zero framing is a bare comment line.
func TestGenAIInteractionsStreamConfigNoHeartbeat(t *testing.T) {
	assert.Equal(t, lib.SSEHeartbeatNone, genAIInteractionsStreamConfig().HeartbeatFraming)
}

// TestGenAIInteractionsUnprefixedFallback pins that a fallback named without a provider goes to the
// request's default provider in both places it is read: the native-path check and the converted
// fallback list core runs. Before, conversion dropped it.
func TestGenAIInteractionsUnprefixedFallback(t *testing.T) {
	ctx := newInteractionsRequestCtx("/genai/v1beta/interactions")
	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := parseInteractionsRequest(t, `{"model":"gemini-3.8-flash","input":"hi","fallbacks":["gemini-3-flash-preview","openai/gpt-5"]}`)
	require.NoError(t, prepareGenAIInteractionsCreate(nil)(ctx, bifrostCtx, req))
	converted, err := convertGenAIInteractionsCreate(bifrostCtx, req)
	require.NoError(t, err)
	assert.Equal(t, []schemas.Fallback{{Provider: schemas.Gemini, Model: "gemini-3-flash-preview"}, {Provider: schemas.OpenAI, Model: "gpt-5"}}, converted.ResponsesRequest.Fallbacks)
}

// TestGenAIInteractionsNativeHistoryWithBuiltInSteps pins that a Gemini request replaying Google's
// own built-in tool steps runs natively (Google gets the raw body), while the same history with a
// fallback outside Google's family is refused up front, since that attempt would drop it.
func TestGenAIInteractionsNativeHistoryWithBuiltInSteps(t *testing.T) {
	const history = `"input":[
		{"type":"user_input","content":[{"type":"text","text":"who won?"}]},
		{"type":"google_search_call","id":"call_s","arguments":{"queries":["who won"]},"signature":"sg"},
		{"type":"google_search_result","call_id":"call_s","result":[{"search_suggestions":"<div/>"}]},
		{"type":"model_output","content":[{"type":"text","text":"Kane."}]},
		{"type":"user_input","content":[{"type":"text","text":"and before?"}]}
	]`
	ctx := newInteractionsRequestCtx("/genai/v1beta/interactions")
	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := parseInteractionsRequest(t, `{"model":"gemini-3.8-flash",`+history+`}`)
	require.NoError(t, prepareGenAIInteractionsCreate(nil)(ctx, bifrostCtx, req))
	converted, err := convertGenAIInteractionsCreate(bifrostCtx, req)
	require.NoError(t, err, "the native path forwards Google's own history")
	assert.Equal(t, schemas.Gemini, converted.ResponsesRequest.Provider)

	bifrostCtx = schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req = parseInteractionsRequest(t, `{"model":"gemini-3.8-flash","fallbacks":["openai/gpt-5"],`+history+`}`)
	err = prepareGenAIInteractionsCreate(nil)(ctx, bifrostCtx, req)
	require.Error(t, err, "an OpenAI fallback cannot carry Google's built-in tool history")
	assert.Contains(t, err.Error(), "google_search_call")
}

// TestGenAIInteractionsUnprefixedFallbackFollowsModelProvider pins that a fallback named without a
// provider runs on the provider the request's own model names, not the route's default.
func TestGenAIInteractionsUnprefixedFallbackFollowsModelProvider(t *testing.T) {
	ctx := newInteractionsRequestCtx("/genai/v1beta/interactions")
	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := parseInteractionsRequest(t, `{"model":"openai/gpt-4o-mini","input":"hi","fallbacks":["gpt-4o"]}`)
	require.NoError(t, prepareGenAIInteractionsCreate(nil)(ctx, bifrostCtx, req))
	converted, err := convertGenAIInteractionsCreate(bifrostCtx, req)
	require.NoError(t, err)
	assert.Equal(t, []schemas.Fallback{{Provider: schemas.OpenAI, Model: "gpt-4o"}}, converted.ResponsesRequest.Fallbacks)
}

// TestGenAIInteractionsNativeOnlyDeclarations pins that, on the native path, tools and steps with
// no Responses form (google_maps, an MCP allowlist in mode "none", a step type Bifrost does not
// model) leave only the routing copy: Google still gets the raw body. A fallback outside Google's
// family would drop them, so with one listed the request is refused up front.
func TestGenAIInteractionsNativeOnlyDeclarations(t *testing.T) {
	const body = `"input":[
			{"type":"user_input","content":[{"type":"text","text":"coffee near me"}]},
			{"type":"future_step","payload":{"x":1}},
			{"type":"user_input","content":[{"type":"text","text":"open now?"}]}
		],
		"tools":[
			{"type":"google_maps"},
			{"type":"mcp_server","name":"tax","url":"https://mcp.example.com","allowed_tools":[{"mode":"none","tools":["lookup"]}]},
			{"type":"function","name":"f","parameters":{"type":"object"}}
		]`
	for _, path := range []string{"/genai/v1beta/interactions", "/genai/v1/projects/p/locations/global/interactions"} {
		t.Run(path, func(t *testing.T) {
			ctx := newInteractionsRequestCtx(path)
			bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			req := parseInteractionsRequest(t, `{"model":"gemini-3.8-flash",`+body+`}`)
			require.NoError(t, prepareGenAIInteractionsCreate(nil)(ctx, bifrostCtx, req))
			native, _ := bifrostCtx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool)
			require.True(t, native)
			converted, err := convertGenAIInteractionsCreate(bifrostCtx, req)
			require.NoError(t, err, "a native request is not refused for declarations only Google reads")
			require.Len(t, converted.ResponsesRequest.Params.Tools, 1, "the routing copy keeps the tools it can express")
			assert.Equal(t, schemas.ResponsesToolTypeFunction, converted.ResponsesRequest.Params.Tools[0].Type)
			assert.Len(t, converted.ResponsesRequest.Input, 2, "the unknown step is left out of the routing copy")
			assert.Len(t, req.Tools, 3, "the caller's request is untouched")
		})
	}

	ctx := newInteractionsRequestCtx("/genai/v1beta/interactions")
	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := parseInteractionsRequest(t, `{"model":"gemini-3.8-flash","fallbacks":["openai/gpt-5"],`+body+`}`)
	err := prepareGenAIInteractionsCreate(nil)(ctx, bifrostCtx, req)
	require.Error(t, err, "an OpenAI fallback cannot carry google_maps")
}

// TestGenAIInteractionsStreamJSONIsSorted pins that a translated stream's JSON is written with
// sorted map keys, so the same event always has the same bytes.
func TestGenAIInteractionsStreamJSONIsSorted(t *testing.T) {
	properties := make(map[string]any, 16)
	for i := range 16 {
		properties[fmt.Sprintf("key_%02d", i)] = map[string]any{"z": 1, "a": 2, "m": 3}
	}
	attributes := properties
	item := schemas.ResponsesMessage{
		ID: schemas.Ptr("fs_1"), Type: schemas.Ptr(schemas.ResponsesMessageTypeFileSearchCall), Status: schemas.Ptr("completed"),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{ResponsesFileSearchToolCall: &schemas.ResponsesFileSearchToolCall{
			Queries: []string{"q"}, Results: []schemas.ResponsesFileSearchToolCallResult{{Attributes: &attributes}},
		}},
	}
	render := func() string {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		_, _, err := genAIInteractionsStreamConverter(ctx, &schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeOutputItemAdded, OutputIndex: schemas.Ptr(0), Item: &item})
		require.NoError(t, err)
		_, frame, err := genAIInteractionsStreamConverter(ctx, &schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeOutputItemDone, OutputIndex: schemas.Ptr(0), Item: &item})
		require.NoError(t, err)
		return frame.(string)
	}
	first := render()
	require.Contains(t, first, "key_15")
	for range 20 {
		require.Equal(t, first, render())
	}
}

// TestGenAIInteractionsRecordsTranslationRefusal pins that a native create records why it cannot be
// translated, so a provider routing picks later is refused instead of sent a reduced request.
func TestGenAIInteractionsRecordsTranslationRefusal(t *testing.T) {
	for body, want := range map[string]string{
		`{"model":"gemini-3.8-flash","input":"hi"}`:                                                                                                 "",
		`{"model":"gemini-3.8-flash","input":"hi","response_format":{"type":"image"}}`:                                                              "response_format",
		`{"agent":"deep-research-preview-04-2026","input":"hi"}`:                                                                                    "agent",
		`{"model":"gemini-3.8-flash","input":"hi","tools":[{"type":"google_maps"}]}`:                                                                "google_maps",
		`{"model":"gemini-3.8-flash","input":[{"type":"google_maps_call","id":"g"},{"type":"user_input","content":[{"type":"text","text":"hi"}]}]}`: "google_maps_call",
	} {
		bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		req := parseInteractionsRequest(t, body)
		require.NoError(t, prepareGenAIInteractionsCreate(nil)(newInteractionsRequestCtx("/genai/v1beta/interactions"), bifrostCtx, req))
		_, err := convertGenAIInteractionsCreate(bifrostCtx, req)
		require.NoError(t, err, body)
		reason, _ := bifrostCtx.Value(schemas.BifrostContextKeyGenAITranslationRefusal).(string)
		if want == "" {
			assert.Empty(t, reason, body)
		} else {
			assert.Contains(t, reason, want, body)
		}
	}
}

// TestGenAIInteractionsStreamStateInManager pins that a translated stream keeps its state outside the
// request context, which holds an id only, and that the stream's cleanup drops it. Two streams with the
// same request id never share state.
func TestGenAIInteractionsStreamStateInManager(t *testing.T) {
	entries := func() (n int) {
		genAIInteractionsStreams.Range(func(_, _ any) bool { n++; return true })
		return n
	}
	start := entries()
	open := func() *schemas.BifrostContext {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyRequestID, "same-id")
		_, frame, err := genAIInteractionsStreamConverter(ctx, &schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeCreated, Response: &schemas.BifrostResponsesResponse{ID: schemas.Ptr("resp_1"), Model: "gpt-5"}})
		require.NoError(t, err)
		require.Contains(t, frame, "interaction.created")
		return ctx
	}
	a, b := open(), open()
	assert.Equal(t, start+2, entries())
	_, isString := a.Value(genAIInteractionsStreamStateKey).(string)
	assert.True(t, isString, "the context holds a handle")
	cleanup := genAIInteractionsStreamConfig().Cleanup
	require.NotNil(t, cleanup)
	cleanup(a)
	cleanup(a)
	cleanup(b)
	cleanup(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline))
	assert.Equal(t, start, entries())
}

// TestGenAIInteractionsRemovedCompletionFrame pins that a completion frame a guardrail removed is rendered
// from the rewritten response, so the client still sees the run end. A chunk that never had a frame is not.
func TestGenAIInteractionsRemovedCompletionFrame(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
	text := "mail [EMAIL-1]"
	chunk := &schemas.BifrostResponsesStreamResponse{Type: schemas.ResponsesStreamResponseTypeCompleted, Response: &schemas.BifrostResponsesResponse{
		ID: schemas.Ptr("v1_x"), Model: "gemini-3.8-flash", Status: schemas.Ptr("completed"),
		Output: []schemas.ResponsesMessage{{Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage), Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant), Content: &schemas.ResponsesMessageContent{ContentBlocks: []schemas.ResponsesMessageContentBlock{{Type: schemas.ResponsesOutputMessageContentTypeText, Text: &text}}}}},
	}}
	_, frame, err := genAIInteractionsStreamConverter(ctx, chunk)
	require.NoError(t, err)
	assert.Nil(t, frame)

	chunk.ExtraFields.RawResponseFormat = schemas.RawResponseFormatGeminiInteractionsStreamEvent
	_, frame, err = genAIInteractionsStreamConverter(ctx, chunk)
	require.NoError(t, err)
	require.IsType(t, "", frame)
	assert.Contains(t, frame, "event: interaction.completed")
	assert.Contains(t, frame, "mail [EMAIL-1]")
	assert.Contains(t, frame, "[DONE]")
}

// TestGenAIInteractionsNamesNeitherModelNorAgent pins that a create naming neither is the caller's mistake.
func TestGenAIInteractionsNamesNeitherModelNorAgent(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	err := prepareGenAIInteractionsCreate(nil)(newInteractionsRequestCtx("/genai/v1beta/interactions"), ctx, parseInteractionsRequest(t, `{"input":"hi"}`))
	require.Error(t, err)
	body, ok := genAIInteractionsErrorConverter(ctx, newBifrostError(err, "failed to execute pre-request callback")).(*gemini.GeminiDecisionError)
	require.True(t, ok)
	assert.Equal(t, "invalid_request", body.Error.Code)
	assert.Contains(t, body.Error.Message, "neither a model nor an agent")
}

// TestGenAIInteractionsCacheReplay pins a native stream answered from the cache. An entry stored without
// Google's frames, by another route, is rendered as Interactions events; one stored with them replays them,
// each once. Without a cache hit, a chunk with no frame still repeats an event already sent.
func TestGenAIInteractionsCacheReplay(t *testing.T) {
	zero := 0
	chunk := func(eventType schemas.ResponsesStreamResponseType, raw string) *schemas.BifrostResponsesStreamResponse {
		resp := &schemas.BifrostResponsesStreamResponse{Type: eventType}
		switch eventType {
		case schemas.ResponsesStreamResponseTypeCreated, schemas.ResponsesStreamResponseTypeCompleted:
			resp.Response = &schemas.BifrostResponsesResponse{ID: schemas.Ptr("v1_x"), Model: "gemini-3.8-flash", Status: schemas.Ptr("completed")}
		case schemas.ResponsesStreamResponseTypeOutputItemAdded:
			resp.OutputIndex, resp.Item = &zero, &schemas.ResponsesMessage{ID: schemas.Ptr("m"), Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage), Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant), Content: &schemas.ResponsesMessageContent{}}
		case schemas.ResponsesStreamResponseTypeOutputTextDelta:
			resp.OutputIndex, resp.Delta = &zero, schemas.Ptr("hello")
		}
		if raw != "" {
			resp.ExtraFields.RawResponse = raw
		}
		return resp
	}
	run := func(hit bool, chunks ...*schemas.BifrostResponsesStreamResponse) string {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
		schemas.SetCacheLookupOnContext(ctx, &schemas.BifrostCacheMetadata{CacheHit: hit})
		defer releaseGenAIInteractionsStreamState(ctx)
		var out strings.Builder
		for _, c := range chunks {
			_, frame, err := genAIInteractionsStreamConverter(ctx, c)
			require.NoError(t, err)
			if s, ok := frame.(string); ok {
				out.WriteString(s)
			}
		}
		return out.String()
	}
	bare := []*schemas.BifrostResponsesStreamResponse{
		chunk(schemas.ResponsesStreamResponseTypeCreated, ""), chunk(schemas.ResponsesStreamResponseTypeOutputItemAdded, ""),
		chunk(schemas.ResponsesStreamResponseTypeOutputTextDelta, ""), chunk(schemas.ResponsesStreamResponseTypeCompleted, ""),
	}
	replayed := run(true, bare...)
	assert.Contains(t, replayed, "hello")
	assert.Contains(t, replayed, "event: interaction.completed")
	assert.Contains(t, replayed, "[DONE]")
	assert.Empty(t, run(false, bare...))

	framed := run(true, chunk(schemas.ResponsesStreamResponseTypeCreated, `{"event_type":"interaction.created"}`), chunk(schemas.ResponsesStreamResponseTypeInProgress, ""),
		chunk(schemas.ResponsesStreamResponseTypeOutputTextDelta, `{"event_type":"step.delta"}`), chunk(schemas.ResponsesStreamResponseTypeOutputItemAdded, ""), chunk(schemas.ResponsesStreamResponseTypeCompleted, `{"event_type":"interaction.completed"}`))
	assert.Equal(t, 3, strings.Count(framed, "data: {"), framed)

	// A stream captured for logs joins the frames no chunk stood for; each replays as an event of its own.
	joined := run(true, chunk(schemas.ResponsesStreamResponseTypeCreated, `{"event_type":"interaction.created"}`),
		chunk(schemas.ResponsesStreamResponseTypeCompleted, "{\"event_type\":\"interaction.status_update\",\"status\":\"in_progress\"}\n{\"event_type\":\"interaction.completed\"}"))
	assert.Equal(t, "event: interaction.created\ndata: {\"event_type\":\"interaction.created\"}\n\n"+
		"event: interaction.status_update\ndata: {\"event_type\":\"interaction.status_update\",\"status\":\"in_progress\"}\n\n"+
		"event: interaction.completed\ndata: {\"event_type\":\"interaction.completed\"}\n\n"+genAIInteractionsDoneFrame, joined)

	// An entry generateContent stored, or whose frames another API sent, is rendered, not replayed.
	foreign := run(true, chunk(schemas.ResponsesStreamResponseTypeCreated, `{"candidates":[]}`), chunk(schemas.ResponsesStreamResponseTypeOutputItemAdded, ""),
		chunk(schemas.ResponsesStreamResponseTypeOutputTextDelta, `{"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}`), chunk(schemas.ResponsesStreamResponseTypeCompleted, `{"candidates":[{"finishReason":"STOP"}]}`))
	assert.NotContains(t, foreign, "candidates")
	assert.Contains(t, foreign, "hello")
	assert.Contains(t, foreign, "event: interaction.completed")
}

// TestGenAIInteractionsCachedResponseFormat pins a unary cache hit: an Interaction is returned as Google sent it,
// and the reply of another API is rendered as an Interaction. A response that is not a hit is Google's own.
func TestGenAIInteractionsCachedResponseFormat(t *testing.T) {
	convert := func(hit bool, raw interface{}) string {
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
		schemas.SetCacheLookupOnContext(ctx, &schemas.BifrostCacheMetadata{CacheHit: hit})
		out, err := genAIInteractionsResponseConverter(ctx, &schemas.BifrostResponsesResponse{ID: schemas.Ptr("v1_x"), Model: "gemini-3.8-flash", Status: schemas.Ptr("completed"), ExtraFields: schemas.BifrostResponseExtraFields{RawResponse: raw}})
		require.NoError(t, err)
		body, err := sonic.Marshal(out)
		require.NoError(t, err)
		return string(body)
	}
	const interaction, generate = `{"id":"v1_g","status":"completed","steps":[]}`, `{"candidates":[],"responseId":"r"}`
	for name, wrap := range map[string]func(string) interface{}{
		"raw":    func(s string) interface{} { return json.RawMessage(s) },
		"string": func(s string) interface{} { return s },
		"decoded": func(s string) interface{} {
			var v map[string]interface{}
			require.NoError(t, sonic.UnmarshalString(s, &v))
			return v
		},
	} {
		assert.Contains(t, convert(true, wrap(interaction)), "v1_g", name)
		rendered := convert(true, wrap(generate))
		assert.NotContains(t, rendered, "candidates", name)
		assert.Contains(t, rendered, `"object":"interaction"`, name)
	}
	assert.Contains(t, convert(false, json.RawMessage(generate)), "candidates", "only a cache hit can hold another API's reply")
}

// BenchmarkGenAIInteractionsCreate measures what the route adds to a native create before core runs:
// picking the path and building the request governance and logging read.
func BenchmarkGenAIInteractionsCreate(b *testing.B) {
	var req gemini.GeminiInteractionsRequest
	if err := sonic.Unmarshal([]byte(`{"model":"gemini-3.8-flash","input":"Remember the number 42.","previous_interaction_id":"v1_abc","generation_config":{"max_output_tokens":100}}`), &req); err != nil {
		b.Fatal(err)
	}
	ctx := newInteractionsRequestCtx("/genai/v1beta/interactions")
	prepare := prepareGenAIInteractionsCreate(nil)
	b.ReportAllocs()
	for b.Loop() {
		bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		if err := prepare(ctx, bifrostCtx, &req); err != nil {
			b.Fatal(err)
		}
		if _, err := convertGenAIInteractionsCreate(bifrostCtx, &req); err != nil {
			b.Fatal(err)
		}
	}
}

// TestGenAIInteractionsTranslatedClientSearchKeepsDeclaredName pins that a client tool_search item
// another provider returns without a name comes back under the name the request declared, unary
// and streamed, as the Gemini path already does. Before, it came back as "tool_search".
func TestGenAIInteractionsTranslatedClientSearchKeepsDeclaredName(t *testing.T) {
	ctx := newInteractionsRequestCtx("/genai/v1beta/interactions")
	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := parseInteractionsRequest(t, `{"model":"openai/gpt-5","input":"hi","tools":[{"type":"tool_search","execution":"client","name":"search_tool_registry"}]}`)
	require.NoError(t, prepareGenAIInteractionsCreate(nil)(ctx, bifrostCtx, req))

	call := schemas.ResponsesMessage{
		ID:   schemas.Ptr("ts_1"),
		Type: schemas.Ptr(schemas.ResponsesMessageTypeToolSearchCall),
		ResponsesToolMessage: &schemas.ResponsesToolMessage{
			CallID:    schemas.Ptr("call_1"),
			Execution: schemas.Ptr(schemas.ToolSearchExecutionClient),
		},
	}
	out, err := genAIInteractionsResponseConverter(bifrostCtx, &schemas.BifrostResponsesResponse{ID: schemas.Ptr("resp_1"), Model: "gpt-5", Status: schemas.Ptr("completed"), Output: []schemas.ResponsesMessage{call}})
	require.NoError(t, err)
	interaction, ok := out.(*gemini.GeminiInteraction)
	require.True(t, ok)
	require.Len(t, interaction.Steps, 1)
	require.NotNil(t, interaction.Steps[0].Name)
	assert.Equal(t, "search_tool_registry", *interaction.Steps[0].Name, "the unary step keeps the declared name")

	outputIndex := 0
	var frames strings.Builder
	for _, chunk := range []*schemas.BifrostResponsesStreamResponse{
		{Type: schemas.ResponsesStreamResponseTypeCreated, Response: &schemas.BifrostResponsesResponse{ID: schemas.Ptr("resp_1"), Model: "gpt-5"}},
		{Type: schemas.ResponsesStreamResponseTypeOutputItemAdded, OutputIndex: &outputIndex, Item: &call},
		{Type: schemas.ResponsesStreamResponseTypeOutputItemDone, OutputIndex: &outputIndex, Item: &call},
	} {
		_, frame, err := genAIInteractionsStreamConverter(bifrostCtx, chunk)
		require.NoError(t, err)
		if s, ok := frame.(string); ok {
			frames.WriteString(s)
		}
	}
	assert.Contains(t, frames.String(), `"name":"search_tool_registry"`, "the streamed step keeps the declared name: %s", frames.String())
	assert.NotContains(t, frames.String(), `"name":"tool_search"`)
}
