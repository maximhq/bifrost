package antigravity

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

// Compile-time proof that the provider satisfies the full Provider interface.
var _ schemas.Provider = (*AntigravityProvider)(nil)

func passthroughPostHook(_ *schemas.BifrostContext, result *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
	return result, bifrostErr
}

func newTestContext() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
}

func userMessage(text string) schemas.ChatMessage {
	return schemas.ChatMessage{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr(text)}}
}

// overrideEndpoints points the OAuth and discovery calls at server for one test.
func overrideEndpoints(t *testing.T, server *httptest.Server) {
	t.Helper()
	prevToken, prevUserinfo, prevLoad, prevOnboard, prevInterval := tokenEndpoint, userinfoEndpoint, loadCodeAssistURL, onboardUserURL, onboardRetryInterval
	tokenEndpoint = server.URL + "/token"
	userinfoEndpoint = server.URL + "/userinfo"
	loadCodeAssistURL = server.URL + "/v1internal:loadCodeAssist"
	onboardUserURL = server.URL + "/v1internal:onboardUser"
	onboardRetryInterval = time.Millisecond
	t.Cleanup(func() {
		tokenEndpoint, userinfoEndpoint, loadCodeAssistURL, onboardUserURL, onboardRetryInterval = prevToken, prevUserinfo, prevLoad, prevOnboard, prevInterval
	})
}

func newTestProvider(t *testing.T, baseURL string, updater schemas.KeyCredentialUpdater) *AntigravityProvider {
	t.Helper()
	provider, err := NewAntigravityProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: baseURL, AllowPrivateNetwork: true},
	}, noopLogger{}, updater)
	require.NoError(t, err)
	return provider
}

func uniqueToken(t *testing.T) string {
	return "1//test-" + strings.ReplaceAll(t.Name(), "/", "-") + fmt.Sprintf("-%d", time.Now().UnixNano())
}

func TestParseCredentials(t *testing.T) {
	t.Run("bare refresh token", func(t *testing.T) {
		creds, err := ParseCredentials("  1//0gabc  ")
		require.NoError(t, err)
		assert.Equal(t, &Credentials{RefreshToken: "1//0gabc"}, creds)
	})
	t.Run("json form", func(t *testing.T) {
		creds, err := ParseCredentials(`{"refresh_token":"1//x","project_id":"proj-1","email":"a@b.com"}`)
		require.NoError(t, err)
		assert.Equal(t, &Credentials{RefreshToken: "1//x", ProjectID: "proj-1", Email: "a@b.com"}, creds)
	})
	t.Run("rejects empty, malformed and incomplete values", func(t *testing.T) {
		for _, value := range []string{"", "   ", `{"project_id":"p"}`, `{bad json`, "two words"} {
			_, err := ParseCredentials(value)
			assert.Error(t, err, value)
		}
	})
	t.Run("encode round-trips compactly", func(t *testing.T) {
		encoded, err := (&Credentials{RefreshToken: "1//x", ProjectID: "p"}).Encode()
		require.NoError(t, err)
		assert.Equal(t, `{"refresh_token":"1//x","project_id":"p"}`, encoded)
		back, err := ParseCredentials(encoded)
		require.NoError(t, err)
		assert.Equal(t, "p", back.ProjectID)
		_, err = (&Credentials{}).Encode()
		assert.Error(t, err)
	})
}

func TestPKCEAndAuthURL(t *testing.T) {
	verifier, challenge, err := NewPKCE()
	require.NoError(t, err)
	assert.Len(t, verifier, 128)
	sum := sha256.Sum256([]byte(verifier))
	assert.Equal(t, base64.RawURLEncoding.EncodeToString(sum[:]), challenge)

	authURL := BuildAuthURL("", "state123", challenge)
	parsed, err := url.Parse(authURL)
	require.NoError(t, err)
	assert.Equal(t, "accounts.google.com", parsed.Host)
	q := parsed.Query()
	assert.Equal(t, "code", q.Get("response_type"))
	assert.Equal(t, defaultClientID, q.Get("client_id"))
	assert.Equal(t, DefaultRedirectURI, q.Get("redirect_uri"))
	assert.Equal(t, challenge, q.Get("code_challenge"))
	assert.Equal(t, "S256", q.Get("code_challenge_method"))
	assert.Equal(t, "offline", q.Get("access_type"))
	assert.Equal(t, "consent", q.Get("prompt"))
	assert.Equal(t, "state123", q.Get("state"))
	assert.Contains(t, q.Get("scope"), "https://www.googleapis.com/auth/cloud-platform")
	assert.Contains(t, q.Get("scope"), "https://www.googleapis.com/auth/cclog")

	t.Setenv(envClientID, "custom-client")
	parsed, err = url.Parse(BuildAuthURL("http://localhost:1/cb", "s", "c"))
	require.NoError(t, err)
	assert.Equal(t, "custom-client", parsed.Query().Get("client_id"))
	assert.Equal(t, "http://localhost:1/cb", parsed.Query().Get("redirect_uri"))
}

func TestResolveWireModel(t *testing.T) {
	cases := []struct {
		model, effort, wire, level string
	}{
		{"gemini-3.1-pro-high", "", "gemini-pro-agent", ""},
		{"gemini-3.1-pro-preview", "high", "gemini-pro-agent", ""},
		{"gemini-3.1-pro", "", "gemini-pro-agent", ""},
		{"gemini-3.1-pro", "high", "gemini-pro-agent", "high"},
		{"gemini-3.1-pro", "low", "gemini-3.1-pro-low", "low"},
		{"gemini-3.1-pro", "xhigh", "gemini-pro-agent", ""},
		{"gemini-3.1-pro", "none", "gemini-pro-agent", ""},
		{"gemini-3.8-flash", "", "gemini-3.8-flash-medium", ""},
		{"gemini-3.8-flash", "high", "gemini-3.8-flash-high", ""},
		{"gemini-3.8-flash", "max", "gemini-3.8-flash-high", ""},
		{"gemini-3.8-flash", "minimal", "gemini-3.8-flash-medium", ""},
		{"gemini-3.8-flash", "none", "gemini-3.8-flash-medium", ""},
		{"gemini-3.7-flash", "", "gemini-3.7-flash-tiered", "medium"},
		{"gemini-3.7-flash", "low", "gemini-3.7-flash-tiered", "low"},
		{"gemini-3.7-flash", "minimal", "gemini-3.7-flash-tiered", "medium"},
		{"gemini-3.7-flash", "none", "gemini-3.7-flash-tiered", "medium"},
		{"gemini-3.6-flash-high", "", "gemini-3.7-flash-tiered", "high"},
		{"gemini-3.6-flash-high", "low", "gemini-3.7-flash-tiered", "low"},
		{"gemini-3.6-flash-high", "minimal", "gemini-3.7-flash-tiered", "high"},
		{"gemini-3.5-flash-low", "", "gemini-3.7-flash-tiered", "medium"},
		{"claude-sonnet-4-6", "", "claude-sonnet-4-6", ""},
		{"claude-sonnet-4-6", "minimal", "claude-sonnet-4-6", ""},
		{"claude-sonnet-4-6", "none", "claude-sonnet-4-6", ""},
		{"claude-opus-4-6-thinking", "max", "claude-opus-4-6-thinking", "high"},
		{"claude-opus-4-6-thinking", "medium", "claude-opus-4-6-thinking", "medium"},
		{"gemini-2.5-pro", "high", "gemini-2.5-pro", ""},
		{"gemini-3.1-flash-image", "high", "gemini-3.1-flash-image", ""},
		{"gpt-oss-120b-medium", "high", "gpt-oss-120b-medium", ""},
	}
	for _, c := range cases {
		wire, level := resolveWireModel(c.model, c.effort)
		assert.Equal(t, c.wire, wire, "%s/%s", c.model, c.effort)
		assert.Equal(t, c.level, level, "%s/%s", c.model, c.effort)
	}
}

func TestSanitizeToolParameters(t *testing.T) {
	raw := decodeSchema(json.RawMessage(`{
		"type":"object",
		"$defs":{"Loc":{"type":"object","properties":{"city":{"type":"string","minLength":1}},"required":["city","ghost"]}},
		"properties":{
			"where":{"$ref":"#/$defs/Loc","description":"place"},
			"unit":{"anyOf":[{"type":"string","enum":["c"]},{"type":"string","enum":["f"]},{"type":"null"}]},
			"count":{"type":["integer","null"],"format":"int32","maximum":5},
			"tags":{"type":"array"},
			"mode":{"const":"fast"},
			"bad":{"type":"tuple"}
		},
		"required":["where","missing"],
		"additionalProperties":false
	}`))
	got := sanitizeToolParameters(raw)
	encoded, err := marshalJSON(got)
	require.NoError(t, err)
	assert.JSONEq(t, `{
		"type":"object",
		"properties":{
			"where":{"type":"object","description":"place","properties":{"city":{"type":"string"}},"required":["city"]},
			"unit":{"type":"string","enum":["c","f"],"nullable":true},
			"count":{"type":"integer","nullable":true,"format":"int32"},
			"tags":{"type":"array","items":{"type":"string"}},
			"mode":{"type":"string","enum":["fast"]},
			"bad":{}
		},
		"required":["where"]
	}`, string(encoded))

	assert.Equal(t, emptyObjectSchema(), sanitizeToolParameters(nil))
	assert.Equal(t, emptyObjectSchema(), sanitizeToolParameters("nope"))
	assert.Equal(t, emptyObjectSchema(), sanitizeToolParameters(map[string]any{"type": "string"}))
	assert.Equal(t, emptyObjectSchema(), sanitizeToolParameters(map[string]any{"type": "object"}))
}

func TestToolNameCodec(t *testing.T) {
	codec := newToolNameCodec()
	assert.Equal(t, "get_weather", codec.encode("get_weather"))
	wire := codec.encode("mcp.server/read file")
	assert.Equal(t, "mcp_server_read_file", wire)
	assert.Equal(t, "mcp.server/read file", codec.decode(wire))
	assert.Equal(t, "_9lives", codec.encode("9lives"))
	long := strings.Repeat("a", 80)
	encodedLong := codec.encode(long)
	assert.Len(t, encodedLong, 64)
	assert.True(t, validToolName.MatchString(encodedLong))
	// a second name sanitizing to the same wire name gets a distinct one
	other := codec.encode("mcp server/read.file")
	assert.NotEqual(t, wire, other)
	assert.True(t, validToolName.MatchString(other))
	assert.Equal(t, "mcp server/read.file", codec.decode(other))
	assert.Equal(t, "mcp.server/read file", codec.decode(wire))
}

func toolHistoryRequest(model string) *schemas.BifrostChatRequest {
	params := schemas.ToolFunctionParameters{Type: "object"}
	return &schemas.BifrostChatRequest{
		Provider: schemas.Antigravity,
		Model:    model,
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleSystem, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("x-anthropic-billing-header: cc=1\nBe terse.")}},
			userMessage("weather in Paris and Rome?"),
			{
				Role: schemas.ChatMessageRoleAssistant,
				ChatAssistantMessage: &schemas.ChatAssistantMessage{ToolCalls: []schemas.ChatAssistantMessageToolCall{
					{ID: schemas.Ptr("call.1"), Type: schemas.Ptr("function"), Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("weather.get"), Arguments: `{"city":"Paris"}`}},
					{ID: schemas.Ptr("call.2"), Type: schemas.Ptr("function"), Function: schemas.ChatAssistantMessageToolCallFunction{Name: schemas.Ptr("weather.get"), Arguments: `{"city":"Rome"}`}},
				}},
			},
			{Role: schemas.ChatMessageRoleTool, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr(`{"t":20}`)}, ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr("call.1")}},
			{Role: schemas.ChatMessageRoleTool, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("sunny")}, ChatToolMessage: &schemas.ChatToolMessage{ToolCallID: schemas.Ptr("call.2")}},
		},
		Params: &schemas.ChatParameters{
			MaxCompletionTokens: schemas.Ptr(200000),
			Temperature:         schemas.Ptr(3.5),
			Stop:                []string{"a", "a", "", "b", "c", "d", "e", "f"},
			Tools: []schemas.ChatTool{{
				Type:     schemas.ChatToolTypeFunction,
				Function: &schemas.ChatToolFunction{Name: "weather.get", Description: schemas.Ptr("Weather"), Parameters: &params},
			}},
		},
	}
}

func compilePlan(t *testing.T, request *schemas.BifrostChatRequest) map[string]any {
	t.Helper()
	plan, bifrostErr := buildRequestPlan(newTestContext(), request)
	require.Nil(t, bifrostErr)
	var inner map[string]any
	require.NoError(t, json.Unmarshal(plan.request, &inner))
	return inner
}

func TestCompileWireRequestGemini(t *testing.T) {
	inner := compilePlan(t, toolHistoryRequest("gemini-3.1-pro"))

	keys := make([]string, 0, len(inner))
	for k := range inner {
		keys = append(keys, k)
	}
	assert.ElementsMatch(t, []string{"contents", "systemInstruction", "tools", "generationConfig", "sessionId"}, keys)

	system := inner["systemInstruction"].(map[string]any)
	assert.NotContains(t, system, "role")
	assert.Equal(t, "Be terse.", system["parts"].([]any)[0].(map[string]any)["text"])

	gen := inner["generationConfig"].(map[string]any)
	assert.EqualValues(t, 65535, gen["maxOutputTokens"])
	assert.EqualValues(t, 2, gen["temperature"])
	assert.Equal(t, []any{"a", "b", "c", "d", "e"}, gen["stopSequences"])
	assert.Equal(t, map[string]any{"includeThoughts": true}, gen["thinkingConfig"])

	// An explicit effort the backend rejects never reaches the wire as thinkingLevel.
	minimal := toolHistoryRequest("gemini-3.7-flash")
	minimal.Params.Reasoning = &schemas.ChatReasoning{Effort: schemas.Ptr("minimal")}
	minimalGen := compilePlan(t, minimal)["generationConfig"].(map[string]any)
	assert.Equal(t, map[string]any{"thinkingLevel": "medium", "includeThoughts": true}, minimalGen["thinkingConfig"])
	disabled := toolHistoryRequest("claude-sonnet-4-6")
	disabled.Params.Reasoning = &schemas.ChatReasoning{Enabled: schemas.Ptr(false)}
	assert.NotContains(t, compilePlan(t, disabled)["generationConfig"].(map[string]any), "thinkingConfig")
	high := toolHistoryRequest("gemini-3.1-pro")
	high.Params.Reasoning = &schemas.ChatReasoning{Effort: schemas.Ptr("high")}
	highGen := compilePlan(t, high)["generationConfig"].(map[string]any)
	assert.Equal(t, map[string]any{"thinkingLevel": "high", "includeThoughts": true}, highGen["thinkingConfig"])

	decl := inner["tools"].([]any)[0].(map[string]any)["functionDeclarations"].([]any)[0].(map[string]any)
	assert.Equal(t, "weather_get", decl["name"])
	assert.Equal(t, map[string]any{"type": "object", "properties": map[string]any{}}, decl["parameters"])
	assert.NotContains(t, decl, "parametersJsonSchema")

	contents := inner["contents"].([]any)
	require.Len(t, contents, 3)
	model := contents[1].(map[string]any)
	assert.Equal(t, "model", model["role"])
	parts := model["parts"].([]any)
	require.Len(t, parts, 2)
	first, second := parts[0].(map[string]any), parts[1].(map[string]any)
	assert.Equal(t, thoughtSignatureSentinel, first["thoughtSignature"])
	assert.NotContains(t, second, "thoughtSignature")
	assert.Equal(t, "weather_get", first["functionCall"].(map[string]any)["name"])
	assert.Equal(t, "call.1", first["functionCall"].(map[string]any)["id"])

	results := contents[2].(map[string]any)["parts"].([]any)
	require.Len(t, results, 2)
	assert.Equal(t, "weather_get", results[0].(map[string]any)["functionResponse"].(map[string]any)["name"])

	// Session id is stable for the same first user text.
	again := compilePlan(t, toolHistoryRequest("gemini-3.1-pro"))
	assert.Equal(t, inner["sessionId"], again["sessionId"])
	assert.True(t, strings.HasPrefix(inner["sessionId"].(string), "-"))
}

func TestCompileWireRequestClaude(t *testing.T) {
	request := toolHistoryRequest("claude-sonnet-4-6")
	request.Params.ToolChoice = &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("required")}
	inner := compilePlan(t, request)

	assert.Equal(t, map[string]any{"functionCallingConfig": map[string]any{"mode": "VALIDATED"}}, inner["toolConfig"])
	gen := inner["generationConfig"].(map[string]any)
	assert.EqualValues(t, 64000, gen["maxOutputTokens"])
	assert.NotContains(t, gen, "thinkingConfig")

	model := inner["contents"].([]any)[1].(map[string]any)
	for _, p := range model["parts"].([]any) {
		part := p.(map[string]any)
		assert.NotContains(t, part, "thoughtSignature")
		id := part["functionCall"].(map[string]any)["id"].(string)
		assert.Regexp(t, `^[A-Za-z0-9_-]+$`, id)
	}
	callID := model["parts"].([]any)[0].(map[string]any)["functionCall"].(map[string]any)["id"]
	resultID := inner["contents"].([]any)[2].(map[string]any)["parts"].([]any)[0].(map[string]any)["functionResponse"].(map[string]any)["id"]
	assert.Equal(t, callID, resultID)

	request.Params.ToolChoice = &schemas.ChatToolChoice{ChatToolChoiceStr: schemas.Ptr("none")}
	inner = compilePlan(t, request)
	assert.NotContains(t, inner, "tools")
	assert.NotContains(t, inner, "toolConfig")

	// Structured output is refused for non-Gemini models. (The Gemini converter already
	// drops JSON mode when tools are present on such models, so test it without tools.)
	request.Params.Tools, request.Params.ToolChoice = nil, nil
	request.Params.ResponseFormat = schemas.Ptr[interface{}](map[string]any{"type": "json_object"})
	_, bifrostErr := buildRequestPlan(newTestContext(), request)
	require.NotNil(t, bifrostErr)
	assert.Equal(t, http.StatusBadRequest, *bifrostErr.StatusCode)
}

func TestCompileContentsEdges(t *testing.T) {
	inner := compilePlan(t, &schemas.BifrostChatRequest{
		Model: "gemini-3.8-flash",
		Input: []schemas.ChatMessage{
			{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("hello")}},
			userMessage("hi"),
			{Role: schemas.ChatMessageRoleAssistant, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr("partial")}},
		},
	})
	contents := inner["contents"].([]any)
	assert.Equal(t, "user", contents[0].(map[string]any)["role"])
	assert.Equal(t, "user", contents[len(contents)-1].(map[string]any)["role"])
	plan, bifrostErr := buildRequestPlan(newTestContext(), &schemas.BifrostChatRequest{Model: "gemini-3.8-flash", Input: []schemas.ChatMessage{userMessage("x")}})
	require.Nil(t, bifrostErr)
	assert.Equal(t, "gemini-3.8-flash-medium", plan.wireModel)
}

func TestEnvelope(t *testing.T) {
	plan := &requestPlan{wireModel: "gemini-pro-agent", request: json.RawMessage(`{"contents":[]}`)}
	raw, err := plan.envelope("proj-9")
	require.NoError(t, err)
	var env map[string]any
	require.NoError(t, json.Unmarshal(raw, &env))
	assert.Equal(t, "gemini-pro-agent", env["model"])
	assert.Equal(t, "antigravity", env["userAgent"])
	assert.Equal(t, "agent", env["requestType"])
	assert.Equal(t, "proj-9", env["project"])
	assert.True(t, strings.HasPrefix(env["requestId"].(string), "agent-"))
	assert.Equal(t, map[string]any{"contents": []any{}}, env["request"])
}

// fakeUpstream serves the token endpoint and the v1internal API.
type fakeUpstream struct {
	server       *httptest.Server
	refreshes    atomic.Int32
	generateHits atomic.Int32
	lastBody     atomic.Value
	lastHeaders  atomic.Value
	generate     func(w http.ResponseWriter, hit int32)
	stream       func(w http.ResponseWriter)
}

func newFakeUpstream(t *testing.T) *fakeUpstream {
	f := &fakeUpstream{}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		switch r.URL.Path {
		case "/token":
			n := f.refreshes.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"access_token":"access-%d","expires_in":3600}`, n)
		case "/v1internal:generateContent":
			f.lastBody.Store(body)
			f.lastHeaders.Store(r.Header.Clone())
			f.generate(w, f.generateHits.Add(1))
		case "/v1internal:streamGenerateContent":
			f.lastBody.Store(body)
			f.lastHeaders.Store(r.Header.Clone())
			assert.Equal(t, "sse", r.URL.Query().Get("alt"))
			f.stream(w)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.server.Close)
	overrideEndpoints(t, f.server)
	return f
}

func TestChatCompletionUnwrapsResponseAndRetriesOn401(t *testing.T) {
	upstream := newFakeUpstream(t)
	upstream.generate = func(w http.ResponseWriter, hit int32) {
		if hit == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"code":401,"message":"token expired","status":"UNAUTHENTICATED"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"response":{"responseId":"r1","modelVersion":"gemini-pro-agent","candidates":[{"content":{"role":"model","parts":[{"text":"thinking","thought":true},{"text":"Hello"},{"functionCall":{"name":"weather_get","args":{"city":"Paris"},"id":"c1"}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15}}}`))
	}
	provider := newTestProvider(t, upstream.server.URL, nil)
	key := schemas.Key{ID: "k1", Value: *schemas.NewSecretVar(fmt.Sprintf(`{"refresh_token":%q,"project_id":"proj-1"}`, uniqueToken(t)))}

	request := toolHistoryRequest("gemini-3.1-pro")
	response, bifrostErr := provider.ChatCompletion(newTestContext(), key, request)
	require.Nil(t, bifrostErr)

	assert.EqualValues(t, 2, upstream.generateHits.Load())
	assert.EqualValues(t, 2, upstream.refreshes.Load(), "the rejected token must be replaced once")

	headers := upstream.lastHeaders.Load().(http.Header)
	assert.Equal(t, "Bearer access-2", headers.Get("Authorization"))
	assert.Equal(t, userAgent, headers.Get("User-Agent"))
	assert.Equal(t, "application/json", headers.Get("Content-Type"))

	var env map[string]any
	require.NoError(t, json.Unmarshal(upstream.lastBody.Load().([]byte), &env))
	assert.Equal(t, "proj-1", env["project"])
	assert.Equal(t, "gemini-pro-agent", env["model"])

	require.Len(t, response.Choices, 1)
	msg := response.Choices[0].Message
	require.NotNil(t, msg.Content.ContentStr)
	assert.Equal(t, "Hello", *msg.Content.ContentStr)
	require.Len(t, msg.ChatAssistantMessage.ToolCalls, 1)
	assert.Equal(t, "weather.get", *msg.ChatAssistantMessage.ToolCalls[0].Function.Name)
	require.NotNil(t, response.Usage)
	assert.Equal(t, 10, response.Usage.PromptTokens)
	assert.Equal(t, 5, response.Usage.CompletionTokens)
}

func TestChatCompletionMapsQuotaError(t *testing.T) {
	upstream := newFakeUpstream(t)
	upstream.generate = func(w http.ResponseWriter, _ int32) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":429,"message":"You have exhausted your capacity on this model. Your quota will reset after 2h.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"39s"}]}}`))
	}
	provider := newTestProvider(t, upstream.server.URL, nil)
	key := schemas.Key{ID: "k1", Value: *schemas.NewSecretVar(fmt.Sprintf(`{"refresh_token":%q,"project_id":"p"}`, uniqueToken(t)))}

	_, bifrostErr := provider.ChatCompletion(newTestContext(), key, &schemas.BifrostChatRequest{Model: "gemini-3.8-flash", Input: []schemas.ChatMessage{userMessage("hi")}})
	require.NotNil(t, bifrostErr)
	assert.Equal(t, http.StatusTooManyRequests, *bifrostErr.StatusCode)
	assert.Equal(t, "insufficient_quota", *bifrostErr.Error.Type)
	assert.Contains(t, bifrostErr.Error.Message, "Antigravity quota exhausted")
	assert.EqualValues(t, 39000, bifrostErr.ExtraFields.RetryAfter)
}

func TestNormalizeUpstreamError(t *testing.T) {
	validation := &upstreamError{Code: 403, Message: "Please verify", Status: "PERMISSION_DENIED"}
	validation.Details = append(validation.Details, struct {
		Type       string `json:"@type"`
		Reason     string `json:"reason"`
		RetryDelay string `json:"retryDelay"`
	}{Type: "type.googleapis.com/google.rpc.ErrorInfo", Reason: "VALIDATION_REQUIRED"})
	err := normalizeUpstreamError(nil, 403, validation)
	assert.Equal(t, 403, *err.StatusCode)
	assert.Contains(t, err.Error.Message, "VALIDATION_REQUIRED")

	err = normalizeUpstreamError(nil, 403, &upstreamError{Code: 403, Message: "To continue, verify your account"})
	assert.Contains(t, err.Error.Message, "verification required")

	err = normalizeUpstreamError(nil, 429, &upstreamError{Code: 429, Message: "Too many requests per minute", Status: "RESOURCE_EXHAUSTED"})
	assert.Equal(t, "RESOURCE_EXHAUSTED", *err.Error.Type)
	assert.Contains(t, err.Error.Message, "rate limit exceeded")

	err = normalizeUpstreamError(nil, 0, nil)
	assert.Equal(t, http.StatusBadGateway, *err.StatusCode)
}

func TestChatCompletionStream(t *testing.T) {
	upstream := newFakeUpstream(t)
	upstream.stream = func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		frames := []string{
			`{"response":{"responseId":"s1","modelVersion":"gemini-3.8-flash-medium","candidates":[{"content":{"role":"model","parts":[{"text":"Hel"}]}}]}}`,
			`{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"lo"},{"functionCall":{"name":"weather_get","args":{"city":"Rome"}}}]},"finishReason":"STOP"}]}}`,
			`{"response":{"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3,"totalTokenCount":10}}}`,
		}
		for _, f := range frames {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", f)
		}
	}
	provider := newTestProvider(t, upstream.server.URL, nil)
	key := schemas.Key{ID: "k1", Value: *schemas.NewSecretVar(fmt.Sprintf(`{"refresh_token":%q,"project_id":"p"}`, uniqueToken(t)))}

	request := toolHistoryRequest("gemini-3.8-flash")
	stream, bifrostErr := provider.ChatCompletionStream(newTestContext(), passthroughPostHook, nil, key, request)
	require.Nil(t, bifrostErr)

	var text strings.Builder
	var toolNames []string
	var final *schemas.BifrostChatResponse
	for chunk := range stream {
		require.Nil(t, chunk.BifrostError)
		resp := chunk.BifrostChatResponse
		require.NotNil(t, resp)
		assert.Equal(t, "s1", resp.ID)
		for _, choice := range resp.Choices {
			if choice.Delta.Content != nil {
				text.WriteString(*choice.Delta.Content)
			}
			for _, call := range choice.Delta.ToolCalls {
				toolNames = append(toolNames, *call.Function.Name)
			}
			if choice.FinishReason != nil {
				final = resp
			}
		}
	}
	assert.Equal(t, "Hello", text.String())
	assert.Equal(t, []string{"weather.get"}, toolNames)
	require.NotNil(t, final, "the stream must end on a chunk with a finish reason")
	assert.Equal(t, "tool_calls", *final.Choices[0].FinishReason)
	require.NotNil(t, final.Usage)
	assert.Equal(t, 7, final.Usage.PromptTokens)
	assert.Equal(t, 3, final.Usage.CompletionTokens)
}

func TestChatCompletionStreamInlineErrorAndTruncation(t *testing.T) {
	upstream := newFakeUpstream(t)
	var body string
	upstream.stream = func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, body)
	}
	provider := newTestProvider(t, upstream.server.URL, nil)
	key := schemas.Key{ID: "k1", Value: *schemas.NewSecretVar(fmt.Sprintf(`{"refresh_token":%q,"project_id":"p"}`, uniqueToken(t)))}
	request := &schemas.BifrostChatRequest{Model: "gemini-3.8-flash", Input: []schemas.ChatMessage{userMessage("hi")}}

	collectError := func() *schemas.BifrostError {
		stream, bifrostErr := provider.ChatCompletionStream(newTestContext(), passthroughPostHook, nil, key, request)
		require.Nil(t, bifrostErr)
		var last *schemas.BifrostError
		for chunk := range stream {
			if chunk.BifrostError != nil {
				last = chunk.BifrostError
			}
		}
		return last
	}

	body = "data: {\"error\":{\"code\":429,\"message\":\"rate limit\",\"status\":\"RESOURCE_EXHAUSTED\"}}\n\n"
	streamErr := collectError()
	require.NotNil(t, streamErr)
	assert.Equal(t, 429, *streamErr.StatusCode)

	body = "data: {\"response\":{\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"cut\"}]}}]}}\n\n"
	streamErr = collectError()
	require.NotNil(t, streamErr)
	assert.Contains(t, streamErr.Error.Message, "without a terminal signal")
}

func TestResolveAuthDiscoversProjectAndPersists(t *testing.T) {
	var loadCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
		case "/v1internal:loadCodeAssist":
			loadCalls.Add(1)
			assert.Equal(t, "Bearer tok", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`{"cloudaicompanionProject":{"id":"found-proj"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	overrideEndpoints(t, server)

	persisted := make(chan string, 1)
	provider := newTestProvider(t, server.URL, func(_ context.Context, provider schemas.ModelProvider, keyID string, value string) error {
		assert.Equal(t, schemas.Antigravity, provider)
		assert.Equal(t, "key-1", keyID)
		persisted <- value
		return nil
	})
	refresh := uniqueToken(t)
	key := schemas.Key{ID: "key-1", Value: *schemas.NewSecretVar(refresh)}

	auth, bifrostErr := provider.resolveAuth(newTestContext(), key, "")
	require.Nil(t, bifrostErr)
	assert.Equal(t, "tok", auth.accessToken)
	assert.Equal(t, "found-proj", auth.projectID)

	select {
	case value := <-persisted:
		creds, err := ParseCredentials(value)
		require.NoError(t, err)
		assert.Equal(t, refresh, creds.RefreshToken)
		assert.Equal(t, "found-proj", creds.ProjectID)

		// The re-encoded value starts with a warm cache entry.
		warm, bifrostErr := provider.resolveAuth(newTestContext(), schemas.Key{ID: "key-1", Value: *schemas.NewSecretVar(value)}, "")
		require.Nil(t, bifrostErr)
		assert.Equal(t, "tok", warm.accessToken)
	case <-time.After(2 * time.Second):
		t.Fatal("credential updater was not called")
	}

	// Cached: no further discovery.
	_, bifrostErr = provider.resolveAuth(newTestContext(), key, "")
	require.Nil(t, bifrostErr)
	assert.EqualValues(t, 1, loadCalls.Load())
}

func TestResolveAuthInvalidGrantIsCredentialFailure(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`))
	}))
	defer server.Close()
	overrideEndpoints(t, server)

	provider := newTestProvider(t, server.URL, nil)
	key := schemas.Key{ID: "k", Value: *schemas.NewSecretVar(uniqueToken(t))}
	_, bifrostErr := provider.resolveAuth(newTestContext(), key, "")
	require.NotNil(t, bifrostErr)
	assert.Equal(t, http.StatusUnauthorized, *bifrostErr.StatusCode)
	assert.Contains(t, bifrostErr.Error.Message, "invalid_grant")

	// Negative cache: a second request does not hit the token endpoint again.
	_, bifrostErr = provider.resolveAuth(newTestContext(), key, "")
	require.NotNil(t, bifrostErr)
	assert.EqualValues(t, 1, calls.Load())
}

func TestResolveAuthSingleFlight(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		time.Sleep(20 * time.Millisecond)
		_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
	}))
	defer server.Close()
	overrideEndpoints(t, server)

	provider := newTestProvider(t, server.URL, nil)
	key := schemas.Key{Value: *schemas.NewSecretVar(fmt.Sprintf(`{"refresh_token":%q,"project_id":"p"}`, uniqueToken(t)))}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, bifrostErr := provider.resolveAuth(newTestContext(), key, "")
			assert.Nil(t, bifrostErr)
		}()
	}
	wg.Wait()
	assert.EqualValues(t, 1, calls.Load())
}

func TestExchangeCode(t *testing.T) {
	var onboardCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			require.NoError(t, r.ParseForm())
			assert.Equal(t, "authorization_code", r.PostForm.Get("grant_type"))
			assert.Equal(t, "the-code", r.PostForm.Get("code"))
			assert.Equal(t, "verifier", r.PostForm.Get("code_verifier"))
			assert.Equal(t, DefaultRedirectURI, r.PostForm.Get("redirect_uri"))
			_, _ = w.Write([]byte(`{"access_token":"acc","refresh_token":"1//new","expires_in":3600}`))
		case "/userinfo":
			_, _ = w.Write([]byte(`{"email":"User@Example.com","id":"1"}`))
		case "/v1internal:loadCodeAssist":
			w.WriteHeader(http.StatusForbidden)
		case "/v1internal:onboardUser":
			body, _ := io.ReadAll(r.Body)
			assert.Contains(t, string(body), `"tier_id":"free-tier"`)
			if onboardCalls.Add(1) == 1 {
				_, _ = w.Write([]byte(`{"done":false}`))
				return
			}
			_, _ = w.Write([]byte(`{"done":true,"response":{"cloudaicompanionProject":{"id":"onboarded"}}}`))
		}
	}))
	defer server.Close()
	overrideEndpoints(t, server)

	creds, err := ExchangeCode(context.Background(), "the-code", "", "verifier")
	require.NoError(t, err)
	assert.Equal(t, &Credentials{RefreshToken: "1//new", ProjectID: "onboarded", Email: "user@example.com"}, creds)
	assert.EqualValues(t, 2, onboardCalls.Load())
}

func TestAvailableModelsToModelInfos(t *testing.T) {
	var available availableModelsResponse
	require.NoError(t, json.Unmarshal([]byte(`{
		"models":{
			"gemini-3.8-flash-low":{"maxTokens":900000},
			"gemini-3.8-flash-medium":{"maxTokens":1000000},
			"gemini-3.8-flash-high":{"maxTokens":1048576},
			"gemini-3.7-flash-tiered":{"maxTokens":1048576},
			"claude-sonnet-4-6":{"displayName":"Claude Sonnet 4.6","maxTokens":250000}
		},
		"agentModelSorts":[{"groups":[{"modelIds":["gemini-3.8-flash-low","gemini-3.8-flash-medium","gemini-3.8-flash-high","claude-sonnet-4-6","gemini-3.6-flash"]}]}],
		"tieredModelIds":{"flash":["gemini-3.7-flash-tiered"]},
		"imageGenerationModelIds":["gemini-3.1-flash-image"]
	}`), &available))
	infos := available.toModelInfos()
	ids := make([]string, len(infos))
	for i, info := range infos {
		ids[i] = info.ID
	}
	assert.Equal(t, []string{"claude-sonnet-4-6", "gemini-3.1-flash-image", "gemini-3.7-flash", "gemini-3.8-flash"}, ids)
	assert.Equal(t, 900000, infos[3].ContextWindow)

	resp := toBifrostListModelsResponse(staticModels, schemas.Key{}, true)
	require.Len(t, resp.Data, len(staticModels))
	assert.Equal(t, "antigravity/gemini-3.8-flash", resp.Data[0].ID)
}

func TestListModelsFallsBackToStaticCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_, _ = w.Write([]byte(`{"access_token":"tok","expires_in":3600}`))
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	overrideEndpoints(t, server)

	provider := newTestProvider(t, server.URL, nil)
	key := schemas.Key{ID: "k", Value: *schemas.NewSecretVar(fmt.Sprintf(`{"refresh_token":%q,"project_id":"p"}`, uniqueToken(t)))}
	resp, bifrostErr := provider.ListModels(newTestContext(), []schemas.Key{key}, &schemas.BifrostListModelsRequest{Provider: schemas.Antigravity, Unfiltered: true})
	require.Nil(t, bifrostErr)
	assert.Len(t, resp.Data, len(staticModels))
}

func TestParseAntigravityErrorAppliesRetryAfterHeader(t *testing.T) {
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)
	resp.SetStatusCode(http.StatusTooManyRequests)
	resp.Header.Set("Retry-After", "7")
	resp.SetBodyString(`{"error":{"code":429,"message":"Too many requests","status":"RESOURCE_EXHAUSTED"}}`)
	bifrostErr := parseAntigravityError(resp)
	assert.Equal(t, http.StatusTooManyRequests, *bifrostErr.StatusCode)
	assert.EqualValues(t, 7000, bifrostErr.ExtraFields.RetryAfter)
	assert.Equal(t, "429", *bifrostErr.Error.Code)
}

func TestStreamFirstChunkIsErrorBeforeAnyOutput(t *testing.T) {
	upstream := newFakeUpstream(t)
	upstream.stream = func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		// A frame whose parts convert to nothing, then an inline quota error.
		_, _ = io.WriteString(w, "data: {\"response\":{\"responseId\":\"s\",\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"\"}]}}]}}\n\n"+
			"data: {\"error\":{\"code\":429,\"message\":\"Resource has been exhausted\",\"status\":\"RESOURCE_EXHAUSTED\",\"details\":[{\"@type\":\"type.googleapis.com/google.rpc.RetryInfo\",\"retryDelay\":\"12s\"}]}}\n\n")
	}
	provider := newTestProvider(t, upstream.server.URL, nil)
	key := schemas.Key{ID: "first-chunk", Value: *schemas.NewSecretVar(fmt.Sprintf(`{"refresh_token":%q,"project_id":"p"}`, uniqueToken(t)))}
	ctx := newTestContext()
	stream, bifrostErr := provider.ChatCompletionStream(ctx, passthroughPostHook, nil, key, &schemas.BifrostChatRequest{Model: "gemini-3.8-flash", Input: []schemas.ChatMessage{userMessage("hi")}})
	require.Nil(t, bifrostErr)

	wrapped, drained, firstErr := providerUtils.CheckFirstStreamChunkForError(ctx, stream)
	<-drained
	assert.Nil(t, wrapped)
	require.NotNil(t, firstErr, "the inline error must be the first chunk")
	assert.Equal(t, http.StatusTooManyRequests, *firstErr.StatusCode)
	assert.EqualValues(t, 12000, firstErr.ExtraFields.RetryAfter)
}

func TestRetryAfterOnlyFromUpstreamHints(t *testing.T) {
	upstream := newFakeUpstream(t)
	upstream.generate = func(w http.ResponseWriter, _ int32) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"code":429,"message":"Too many requests","status":"RESOURCE_EXHAUSTED"}}`))
	}
	upstream.stream = func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"error\":{\"code\":429,\"message\":\"Too many requests\",\"status\":\"RESOURCE_EXHAUSTED\"}}\n\n")
	}
	provider := newTestProvider(t, upstream.server.URL, nil)
	key := schemas.Key{ID: "no-hint", Value: *schemas.NewSecretVar(fmt.Sprintf(`{"refresh_token":%q,"project_id":"p"}`, uniqueToken(t)))}
	request := &schemas.BifrostChatRequest{Model: "gemini-3.8-flash", Input: []schemas.ChatMessage{userMessage("hi")}}

	_, bifrostErr := provider.ChatCompletion(newTestContext(), key, request)
	require.NotNil(t, bifrostErr)
	assert.Equal(t, http.StatusTooManyRequests, *bifrostErr.StatusCode)
	assert.Zero(t, bifrostErr.ExtraFields.RetryAfter)

	stream, bifrostErr := provider.ChatCompletionStream(newTestContext(), passthroughPostHook, nil, key, request)
	require.Nil(t, bifrostErr)
	var streamErr *schemas.BifrostError
	for chunk := range stream {
		if chunk.BifrostError != nil {
			streamErr = chunk.BifrostError
		}
	}
	require.NotNil(t, streamErr)
	assert.Zero(t, streamErr.ExtraFields.RetryAfter)

	// Refresh failures carry no hint either.
	assert.Zero(t, refreshError(&tokenError{Status: http.StatusTooManyRequests}).ExtraFields.RetryAfter)
}

func TestTokenCacheKeyedByKeyID(t *testing.T) {
	var mu sync.Mutex
	var refreshed []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			require.NoError(t, r.ParseForm())
			mu.Lock()
			refreshed = append(refreshed, r.PostForm.Get("refresh_token"))
			n := len(refreshed)
			mu.Unlock()
			_, _ = fmt.Fprintf(w, `{"access_token":"tok-%d","expires_in":3600}`, n)
		case "/v1internal:loadCodeAssist":
			_, _ = w.Write([]byte(`{"cloudaicompanionProject":"discovered"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	overrideEndpoints(t, server)

	persisted := make(chan string, 4)
	provider := newTestProvider(t, server.URL, func(_ context.Context, _ schemas.ModelProvider, _ string, value string) error {
		persisted <- value
		return nil
	})
	keyID := "cache-" + uniqueToken(t)
	original := uniqueToken(t)
	key := schemas.Key{ID: keyID, Value: *schemas.NewSecretVar(original)}

	auth, bifrostErr := provider.resolveAuth(newTestContext(), key, "")
	require.Nil(t, bifrostErr)
	assert.Equal(t, "tok-1", auth.accessToken)

	var written string
	select {
	case written = <-persisted:
	case <-time.After(2 * time.Second):
		t.Fatal("credential was not written back")
	}

	// The written-back value and the original value share the key's entry: no refresh,
	// no second entry.
	for _, value := range []string{written, original} {
		auth, bifrostErr = provider.resolveAuth(newTestContext(), schemas.Key{ID: keyID, Value: *schemas.NewSecretVar(value)}, "")
		require.Nil(t, bifrostErr)
		assert.Equal(t, "tok-1", auth.accessToken)
		_, valueKeyed := tokenPool.Load("value:" + valueHash(value))
		assert.False(t, valueKeyed)
	}
	_, idKeyed := tokenPool.Load("id:" + keyID)
	assert.True(t, idKeyed)

	// The operator replaces the credential: the cached token is not reused.
	replacement := uniqueToken(t)
	auth, bifrostErr = provider.resolveAuth(newTestContext(), schemas.Key{ID: keyID, Value: *schemas.NewSecretVar(fmt.Sprintf(`{"refresh_token":%q,"project_id":"operator-proj"}`, replacement))}, "")
	require.Nil(t, bifrostErr)
	assert.Equal(t, "tok-2", auth.accessToken)
	assert.Equal(t, "operator-proj", auth.projectID)
	mu.Lock()
	assert.Equal(t, []string{original, replacement}, refreshed)
	mu.Unlock()
	select {
	case v := <-persisted:
		t.Fatalf("a complete operator credential must not be rewritten, got %s", v)
	case <-time.After(50 * time.Millisecond):
	}
}
