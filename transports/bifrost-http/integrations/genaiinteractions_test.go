package integrations

import (
	"context"
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
	for _, route := range CreateGenAIInteractionsRouteConfigs("/genai") {
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

			err := prepareGenAIInteractionsCreate(ctx, bifrostCtx, req)
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
	require.NoError(t, prepareGenAIInteractionsLifecycle(ctx, bifrostCtx, retrieve))
	assert.Equal(t, "v1_abc", retrieve.ResponseID)
	assert.Equal(t, schemas.Gemini, retrieve.Provider)
	assert.True(t, retrieve.IsStreamingRequested())
	assert.Equal(t, "evt_7", *retrieve.LastEventID)
	native, _ := bifrostCtx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool)
	assert.True(t, native)

	ctx = newInteractionsRequestCtx("/genai/v1/projects/p/locations/global/interactions/v1_abc/cancel")
	ctx.SetUserValue("interaction_id", "v1_abc")
	cancel := &schemas.BifrostResponsesCancelRequest{}
	require.NoError(t, prepareGenAIInteractionsLifecycle(ctx, schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), cancel))
	assert.Equal(t, schemas.Vertex, cancel.Provider)

	ctx = newInteractionsRequestCtx("/genai/v1beta/interactions/resp_1?provider=openai")
	ctx.SetUserValue("interaction_id", "resp_1")
	bifrostCtx = schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	del := &schemas.BifrostResponsesDeleteRequest{}
	require.NoError(t, prepareGenAIInteractionsLifecycle(ctx, bifrostCtx, del))
	assert.Equal(t, schemas.OpenAI, del.Provider)
	native, _ = bifrostCtx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool)
	assert.False(t, native)
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
	require.NoError(t, prepareGenAIInteractionsCreate(ctx, bifrostCtx, req))
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
	require.NoError(t, prepareGenAIInteractionsCreate(ctx, bifrostCtx, req))
	converted, err := convertGenAIInteractionsCreate(bifrostCtx, req)
	require.NoError(t, err, "the native path forwards Google's own history")
	assert.Equal(t, schemas.Gemini, converted.ResponsesRequest.Provider)

	bifrostCtx = schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req = parseInteractionsRequest(t, `{"model":"gemini-3.8-flash","fallbacks":["openai/gpt-5"],`+history+`}`)
	err = prepareGenAIInteractionsCreate(ctx, bifrostCtx, req)
	require.Error(t, err, "an OpenAI fallback cannot carry Google's built-in tool history")
	assert.Contains(t, err.Error(), "google_search_call")
}

// TestGenAIInteractionsUnprefixedFallbackFollowsModelProvider pins that a fallback named without a
// provider runs on the provider the request's own model names, not the route's default.
func TestGenAIInteractionsUnprefixedFallbackFollowsModelProvider(t *testing.T) {
	ctx := newInteractionsRequestCtx("/genai/v1beta/interactions")
	bifrostCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	req := parseInteractionsRequest(t, `{"model":"openai/gpt-4o-mini","input":"hi","fallbacks":["gpt-4o"]}`)
	require.NoError(t, prepareGenAIInteractionsCreate(ctx, bifrostCtx, req))
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
			require.NoError(t, prepareGenAIInteractionsCreate(ctx, bifrostCtx, req))
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
	err := prepareGenAIInteractionsCreate(ctx, bifrostCtx, req)
	require.Error(t, err, "an OpenAI fallback cannot carry google_maps")
}
