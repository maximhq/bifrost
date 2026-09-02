package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Reasoning round-trip across the OpenAI-compatible surface.
//
// The reported failure: a client replays assistant reasoning as OpenRouter-style
// reasoning_details, and an OpenAI-compatible upstream never sees it. The wire carries
// reasoning only via OpenAIChatAssistantMessage.Reasoning -> reasoning_content, and
// ReasoningDetails is deliberately inbound-only, so anything that leaves the normalized
// Reasoning field nil silently deletes the model's chain of thought on the next turn.
//
// Two ingress surfaces have to agree:
//
//   - OpenAI-integration routes parse into OpenAIMessage and normalize in
//     ConvertOpenAIMessagesToBifrostMessages.
//   - The native /v1/chat/completions surface parses into schemas.ChatMessage, so the fold
//     lives in ChatAssistantMessage.UnmarshalJSON.
//
// Both must produce the same outbound reasoning_content.

const replaySentinel = "REPLAY_SENTINEL_4f9d"

// capturedChatBody runs a real chat completion against a test transport and returns the
// JSON body actually sent upstream — not Bifrost's internal object.
func capturedChatBody(t *testing.T, request *schemas.BifrostChatRequest) map[string]any {
	t.Helper()

	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			return
		}
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Errorf("decode body: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"m",`+
			`"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	provider := NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: server.URL},
	}, testNoopLogger{})

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	key := schemas.Key{Value: schemas.SecretVar{Val: "test-api-key"}}
	if _, bifrostErr := provider.ChatCompletion(ctx, key, request); bifrostErr != nil {
		t.Fatalf("ChatCompletion: %v", bifrostErr.Error.Message)
	}
	require.NotNil(t, captured, "no request body reached the test transport")
	return captured
}

// messagesOf returns the wire messages as decoded JSON objects.
func messagesOf(t *testing.T, captured map[string]any) []map[string]any {
	t.Helper()
	raw, ok := captured["messages"].([]any)
	require.True(t, ok, "expected a messages array on the wire, got %#v", captured["messages"])
	out := make([]map[string]any, 0, len(raw))
	for _, m := range raw {
		obj, ok := m.(map[string]any)
		require.True(t, ok)
		out = append(out, obj)
	}
	return out
}

// TestNativeIngressReasoningDetailsReachesUpstream is the primary acceptance test for the
// reported bug: reasoning that arrives as reasoning_details[].text must leave Bifrost toward
// an OpenAI-compatible upstream as assistant reasoning_content.
//
// The request body here is the client's literal JSON, so the test exercises the whole chain:
// JSON parse -> schemas.ChatAssistantMessage.Reasoning -> provider conversion -> wire bytes.
func TestNativeIngressReasoningDetailsReachesUpstream(t *testing.T) {
	payload := `{"model":"mlxserve/qwen","messages":[` +
		`{"role":"system","content":"You are a terminal."},` +
		`{"role":"user","content":"Run the cases."},` +
		`{"role":"assistant","content":"Case 001 verified. Proceeding to case 002.",` +
		`"reasoning_details":[{"index":0,"type":"reasoning.text","text":"CASE_001 succeeded, so continue with CASE_002."}],` +
		`"tool_calls":[{"id":"call_1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"printf 'CASE_002_OK\\n'\"}"}}]},` +
		`{"role":"tool","tool_call_id":"call_1","content":"CASE_002_OK\n"}]}`

	var parsed struct {
		Model    string                `json:"model"`
		Messages []schemas.ChatMessage `json:"messages"`
	}
	require.NoError(t, sonic.Unmarshal([]byte(payload), &parsed), "client payload must parse")
	msgs := parsed.Messages
	require.Len(t, msgs, 4)

	// Ingress: the fold must have populated the normalized field.
	require.NotNil(t, msgs[2].ChatAssistantMessage)
	require.NotNil(t, msgs[2].ChatAssistantMessage.Reasoning,
		"reasoning_details[].text must fold into ChatAssistantMessage.Reasoning at parse time")
	assert.Equal(t, "CASE_001 succeeded, so continue with CASE_002.", *msgs[2].ChatAssistantMessage.Reasoning)
	require.Len(t, msgs[2].ChatAssistantMessage.ReasoningDetails, 1,
		"the structured detail must stay available internally for provider-specific paths")

	captured := capturedChatBody(t, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    parsed.Model,
		Input:    msgs,
		Params:   &schemas.ChatParameters{},
	})

	wire := messagesOf(t, captured)
	require.Len(t, wire, 4)

	assistant := wire[2]
	assert.Equal(t, "assistant", assistant["role"])
	assert.Equal(t, "Case 001 verified. Proceeding to case 002.", assistant["content"],
		"visible content must be unchanged")

	got, ok := assistant["reasoning_content"].(string)
	require.True(t, ok, "historical reasoning must reach the upstream as reasoning_content, got %#v", assistant)
	assert.Equal(t, "CASE_001 succeeded, so continue with CASE_002.", got)

	// reasoning_details is inbound-only: forwarding a client's structured replay to an
	// arbitrary OpenAI-compatible upstream is what the conservative design rejects.
	assert.NotContains(t, assistant, "reasoning_details",
		"structured reasoning_details must not be forwarded to a generic OpenAI-compatible upstream")
	assert.NotContains(t, assistant, "reasoning",
		"the inbound reasoning alias must not be forwarded either")

	// The tool call and its result must survive untouched alongside the reasoning.
	calls, ok := assistant["tool_calls"].([]any)
	require.True(t, ok, "tool_calls must survive")
	require.Len(t, calls, 1)
	call := calls[0].(map[string]any)
	assert.Equal(t, "call_1", call["id"])
	fn := call["function"].(map[string]any)
	assert.Equal(t, "bash", fn["name"])
	assert.Equal(t, `{"command":"printf 'CASE_002_OK\n'"}`, fn["arguments"])

	assert.Equal(t, "tool", wire[3]["role"])
	assert.Equal(t, "call_1", wire[3]["tool_call_id"])
	assert.Equal(t, "CASE_002_OK\n", wire[3]["content"])
}

// TestCustomProviderReasoningDetailsRoundTrip covers the deployed topology: a custom
// provider whose base type is openai skips filterOpenAISpecificParameters but still shares
// the message converter, so the reasoning replay must survive there too.
func TestCustomProviderReasoningDetailsRoundTrip(t *testing.T) {
	payload := `{"model":"mlxserve/custom","messages":[` +
		`{"role":"assistant","reasoning_details":[{"index":0,"type":"reasoning.text","text":"` + replaySentinel + `"}],` +
		`"content":"thinking out loud","tool_calls":[{"id":"call_9","type":"function","function":{"name":"bash","arguments":"{}"}}]}]}`

	var parsed struct {
		Model    string                `json:"model"`
		Messages []schemas.ChatMessage `json:"messages"`
	}
	require.NoError(t, sonic.Unmarshal([]byte(payload), &parsed))

	captured := capturedChatBody(t, &schemas.BifrostChatRequest{
		Provider: schemas.ModelProvider("mlxserve"),
		Model:    "custom",
		Input:    parsed.Messages,
		Params:   &schemas.ChatParameters{},
	})

	assistant := messagesOf(t, captured)[0]
	got, ok := assistant["reasoning_content"].(string)
	require.True(t, ok, "custom provider must receive reasoning_content, got %#v", assistant)
	assert.Equal(t, replaySentinel, got)
	assert.NotContains(t, assistant, "reasoning_details")
}

// TestInboundReasoningSpellingsNormalizeConsistently pins that the two OpenAI-compatible
// entry surfaces agree on precedence: reasoning_content > reasoning > reasoning_details text.
func TestInboundReasoningSpellingsNormalizeConsistently(t *testing.T) {
	tests := []struct {
		name     string
		payload  string
		expected string
	}{
		{"reasoning_content only", `{"role":"assistant","reasoning_content":"A"}`, "A"},
		{"reasoning only", `{"role":"assistant","reasoning":"B"}`, "B"},
		{"reasoning_details only", `{"role":"assistant","reasoning_details":[{"index":0,"type":"reasoning.text","text":"C"}]}`, "C"},
		{"content beats reasoning", `{"role":"assistant","reasoning_content":"A","reasoning":"B"}`, "A"},
		{"content beats details", `{"role":"assistant","reasoning_content":"A","reasoning_details":[{"index":0,"type":"reasoning.text","text":"C"}]}`, "A"},
		{"reasoning beats details", `{"role":"assistant","reasoning":"B","reasoning_details":[{"index":0,"type":"reasoning.text","text":"C"}]}`, "B"},
		{"all three", `{"role":"assistant","reasoning_content":"A","reasoning":"B","reasoning_details":[{"index":0,"type":"reasoning.text","text":"C"}]}`, "A"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Surface A: native /v1/chat/completions (schemas.ChatMessage).
			var native schemas.ChatMessage
			require.NoError(t, sonic.Unmarshal([]byte(tt.payload), &native))
			require.NotNil(t, native.ChatAssistantMessage)
			require.NotNil(t, native.ChatAssistantMessage.Reasoning)
			assert.Equal(t, tt.expected, *native.ChatAssistantMessage.Reasoning,
				"native ingress precedence")

			// Surface B: OpenAI-integration route (OpenAIMessage).
			var wire OpenAIMessage
			require.NoError(t, sonic.Unmarshal([]byte(tt.payload), &wire))
			converted := ConvertOpenAIMessagesToBifrostMessages([]OpenAIMessage{wire})
			require.Len(t, converted, 1)
			require.NotNil(t, converted[0].ChatAssistantMessage)
			require.NotNil(t, converted[0].ChatAssistantMessage.Reasoning)
			assert.Equal(t, tt.expected, *converted[0].ChatAssistantMessage.Reasoning,
				"OpenAI-integration ingress must agree with native ingress")
		})
	}
}

// TestOpenAIWireRoundTripPreservesReasoning walks a message through both converters and
// asserts the reasoning comes back out as reasoning_content.
func TestOpenAIWireRoundTripPreservesReasoning(t *testing.T) {
	payload := `{"role":"assistant","content":"visible","reasoning_details":[{"index":0,"type":"reasoning.text","text":"KEEP_ME"}]}`

	var native schemas.ChatMessage
	require.NoError(t, sonic.Unmarshal([]byte(payload), &native))

	var wire OpenAIMessage
	require.NoError(t, sonic.Unmarshal([]byte(payload), &wire))

	out := ConvertBifrostMessagesToOpenAIMessages(ConvertOpenAIMessagesToBifrostMessages([]OpenAIMessage{wire}))
	require.Len(t, out, 1)
	require.NotNil(t, out[0].OpenAIChatAssistantMessage)
	require.NotNil(t, out[0].OpenAIChatAssistantMessage.Reasoning)
	assert.Equal(t, "KEEP_ME", *out[0].OpenAIChatAssistantMessage.Reasoning)

	// Same expectation on the native ingress side.
	require.NotNil(t, native.ChatAssistantMessage.Reasoning)
	assert.Equal(t, "KEEP_ME", *native.ChatAssistantMessage.Reasoning)

	encoded, err := sonic.Marshal(out)
	require.NoError(t, err)
	assert.Contains(t, string(encoded), `"reasoning_content":"KEEP_ME"`)
	assert.NotContains(t, string(encoded), `"reasoning_details"`,
		"reasoning_details is inbound-only and must not reappear on the outbound wire")
}

// TestPlainAssistantMessageGainsNoReasoningFields guards against the fold inventing fields.
func TestPlainAssistantMessageGainsNoReasoningFields(t *testing.T) {
	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()

	answer := "no reasoning here"
	req := ToOpenAIChatRequest(ctx, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "some-model",
		Input: []schemas.ChatMessage{{
			Role:    schemas.ChatMessageRoleAssistant,
			Content: &schemas.ChatMessageContent{ContentStr: &answer},
		}},
		Params: &schemas.ChatParameters{},
	})
	require.NotNil(t, req)

	encoded, err := sonic.Marshal(req.Messages)
	require.NoError(t, err)

	var decoded []map[string]any
	require.NoError(t, sonic.Unmarshal(encoded, &decoded))
	require.Len(t, decoded, 1)
	for _, key := range []string{"reasoning", "reasoning_content", "reasoning_details"} {
		_, present := decoded[0][key]
		assert.False(t, present, "a plain assistant message must not gain %q on the wire", key)
	}
	assert.Equal(t, answer, decoded[0]["content"])
}

// TestSignatureOnlyDetailsAreNotInventedIntoReasoning pins the boundary between text
// reasoning and opaque provider signatures: an encrypted or signature-bearing detail must not
// be summarized into a fake plaintext chain of thought, which would then travel to an
// upstream that cannot use it and would displace the real replay.
func TestSignatureOnlyDetailsAreNotInventedIntoReasoning(t *testing.T) {
	tests := []struct {
		name    string
		payload string
	}{
		{"encrypted", `{"role":"assistant","reasoning_details":[{"index":0,"type":"reasoning.encrypted","data":"gAAAA..."}]}`},
		{"signature", `{"role":"assistant","reasoning_details":[{"index":0,"type":"reasoning.text","signature":"sig"}]}`},
		{"summary", `{"role":"assistant","reasoning_details":[{"index":0,"type":"reasoning.summary","summary":"digest"}]}`},
		{"null text", `{"role":"assistant","reasoning_details":[{"index":0,"type":"reasoning.text","text":null}]}`},
		{"empty text", `{"role":"assistant","reasoning_details":[{"index":0,"type":"reasoning.text","text":""}]}`},
		{"empty array", `{"role":"assistant","reasoning_details":[]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var native schemas.ChatMessage
			require.NoError(t, sonic.Unmarshal([]byte(tt.payload), &native), "must not panic")
			if native.ChatAssistantMessage != nil {
				assert.Nil(t, native.ChatAssistantMessage.Reasoning,
					"a detail carrying no plaintext must not synthesize Reasoning")
			}

			var wire OpenAIMessage
			require.NoError(t, sonic.Unmarshal([]byte(tt.payload), &wire))
			converted := ConvertOpenAIMessagesToBifrostMessages([]OpenAIMessage{wire})
			require.Len(t, converted, 1)
			if converted[0].ChatAssistantMessage != nil {
				assert.Nil(t, converted[0].ChatAssistantMessage.Reasoning)
			}
		})
	}
}

// TestSignedDetailsStayAvailableForProviderPaths confirms the text fold does not consume the
// structured details: Anthropic's replay path rebuilds signed thinking blocks from
// ReasoningDetails and must still see signature/data after normalization.
func TestSignedDetailsStayAvailableForProviderPaths(t *testing.T) {
	signature := "anthropic-sig"
	data := "redacted-blob"
	payload := `{"role":"assistant","content":"answer","reasoning_details":[` +
		`{"index":0,"type":"reasoning.text","text":"visible thoughts","signature":"` + signature + `"},` +
		`{"index":1,"type":"reasoning.encrypted","data":"` + data + `"}]}`

	var native schemas.ChatMessage
	require.NoError(t, sonic.Unmarshal([]byte(payload), &native))
	require.NotNil(t, native.ChatAssistantMessage)

	details := native.ChatAssistantMessage.ReasoningDetails
	require.Len(t, details, 2, "both details must survive untouched")
	require.NotNil(t, details[0].Signature)
	assert.Equal(t, signature, *details[0].Signature)
	assert.Equal(t, "visible thoughts", *details[0].Text)
	assert.Equal(t, schemas.BifrostReasoningDetailsTypeEncrypted, details[1].Type)
	require.NotNil(t, details[1].Data)
	assert.Equal(t, data, *details[1].Data)

	// The fold takes the first plaintext and leaves the array alone.
	require.NotNil(t, native.ChatAssistantMessage.Reasoning)
	assert.Equal(t, "visible thoughts", *native.ChatAssistantMessage.Reasoning)

	assert.True(t, hasAnthropicRedactedThinking(details),
		"the encrypted detail must still be recognizable to the Anthropic replay path")
}

func hasAnthropicRedactedThinking(details []schemas.ChatReasoningDetails) bool {
	for _, detail := range details {
		if detail.Type == schemas.BifrostReasoningDetailsTypeEncrypted && detail.Data != nil && *detail.Data != "" {
			return true
		}
	}
	return false
}

// TestAssistantReasoningSurvivesToolLoop covers the loop shape where the bug showed up:
// assistant(reasoning+tool_call) -> tool -> assistant, replayed to an upstream that must see
// the reasoning on the historical assistant turn. Uses the client's literal JSON so the parse
// fold is included in what is proven.
func TestAssistantReasoningSurvivesToolLoop(t *testing.T) {
	payload := `{"model":"mlxserve/qwen","messages":[` +
		`{"role":"user","content":"run the cases one at a time"},` +
		`{"role":"assistant","content":"Case 001 verified. Proceeding to case 002.",` +
		`"reasoning_details":[{"index":0,"type":"reasoning.text","text":"CASE_001 succeeded, so continue with CASE_002."}],` +
		`"tool_calls":[{"id":"call_1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"printf 'CASE_002_OK\\n'\"}"}}]},` +
		`{"role":"tool","tool_call_id":"call_1","content":"CASE_002_OK\n"}]}`

	var parsed struct {
		Model    string                `json:"model"`
		Messages []schemas.ChatMessage `json:"messages"`
	}
	require.NoError(t, sonic.Unmarshal([]byte(payload), &parsed))

	captured := capturedChatBody(t, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    parsed.Model,
		Params:   &schemas.ChatParameters{},
		Input:    parsed.Messages,
	})

	wire := messagesOf(t, captured)
	require.Len(t, wire, 3)

	assistant := wire[1]
	got, ok := assistant["reasoning_content"].(string)
	require.True(t, ok, "assistant tool-call turn lost its reasoning on the wire: %#v", assistant)
	assert.Equal(t, "CASE_001 succeeded, so continue with CASE_002.", got)
	assert.Equal(t, "Case 001 verified. Proceeding to case 002.", assistant["content"])
	assert.NotContains(t, assistant, "reasoning_details")

	// Reasoning text and tool call must be intact simultaneously.
	calls, ok := assistant["tool_calls"].([]any)
	require.True(t, ok)
	require.Len(t, calls, 1)
	call := calls[0].(map[string]any)
	assert.Equal(t, "call_1", call["id"])
	assert.Equal(t, "bash", call["function"].(map[string]any)["name"])

	assert.Equal(t, "tool", wire[2]["role"])
	assert.Equal(t, "call_1", wire[2]["tool_call_id"])
	assert.Equal(t, "CASE_002_OK\n", wire[2]["content"])
}

// TestInternallyConstructedDetailsOnlyTurnHasNoWireReasoning pins a deliberate limit of the
// conservative design, so the remaining gap is explicit rather than assumed away.
//
// reasoning_details is inbound-only on the OpenAI wire: the only carrier outbound is the
// normalized Reasoning field. A message assembled in Go that populates ReasoningDetails but
// leaves Reasoning nil therefore sends no reasoning upstream — the JSON-parse fold cannot
// help because no JSON parse happens. DeepSeek depends on this same property to decide when
// thinking must be forced off (core/providers/deepseek/thinking_test.go), so it is a
// contract, not an oversight. Internal callers that need reasoning on the wire must set
// Reasoning.
func TestInternallyConstructedDetailsOnlyTurnHasNoWireReasoning(t *testing.T) {
	reasoning := "built internally, never parsed from JSON"
	callID := "call_1"
	name := "bash"

	ctx, cancel := schemas.NewBifrostContextWithCancel(nil)
	defer cancel()

	req := ToOpenAIChatRequest(ctx, &schemas.BifrostChatRequest{
		Provider: schemas.OpenAI,
		Model:    "some-model",
		Params:   &schemas.ChatParameters{},
		Input: []schemas.ChatMessage{{
			Role: schemas.ChatMessageRoleAssistant,
			ChatAssistantMessage: &schemas.ChatAssistantMessage{
				ReasoningDetails: []schemas.ChatReasoningDetails{{
					Index: 0,
					Type:  schemas.BifrostReasoningDetailsTypeText,
					Text:  &reasoning,
				}},
				ToolCalls: []schemas.ChatAssistantMessageToolCall{{
					ID:       &callID,
					Function: schemas.ChatAssistantMessageToolCallFunction{Name: &name, Arguments: `{}`},
				}},
			},
		}},
	})
	require.NotNil(t, req)

	encoded, err := sonic.Marshal(req.Messages)
	require.NoError(t, err)

	var decoded []map[string]any
	require.NoError(t, sonic.Unmarshal(encoded, &decoded))
	require.Len(t, decoded, 1)
	assert.NotContains(t, decoded[0], "reasoning_content",
		"details-only must not be invented into outbound reasoning_content; set Reasoning instead")
	assert.NotContains(t, decoded[0], "reasoning_details")
}
