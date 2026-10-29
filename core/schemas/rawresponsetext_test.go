package schemas

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Frames follow the OpenAI chat.completion.chunk wire shape, including the fields a rewrite must preserve.
const (
	syncRoleFrame    = `{"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}],"obfuscation":"r1"}`
	syncContentFrame = `{"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"mail alex@example.com"},"finish_reason":null}],"obfuscation":"c1"}`
	syncFinishFrame  = `{"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"obfuscation":"f1"}`
	syncUsageFrame   = `{"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`
)

// Anthropic Messages stream events, as the provider sends them.
const (
	anthropicTextDelta    = `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"mail alex@example.com"}}`
	anthropicJSONDelta    = `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"to\":\"alex@example.com\"}"}}`
	anthropicBlockStop    = `{"type":"content_block_stop","index":0}`
	anthropicMessageDelta = `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":7}}`
)

// syncToolChunk builds a normalized chat stream chunk carrying one tool-call arguments delta.
func syncToolChunk(arguments string) *BifrostChatResponse {
	return &BifrostChatResponse{
		Choices: []BifrostResponseChoice{{
			ChatStreamResponseChoice: &ChatStreamResponseChoice{Delta: &ChatStreamResponseChoiceDelta{
				ToolCalls: []ChatAssistantMessageToolCall{{Index: 0, Function: ChatAssistantMessageToolCallFunction{Arguments: arguments}}},
			}},
		}},
	}
}

// syncResponsesDelta builds a normalized Responses delta event of the given type.
func syncResponsesDelta(eventType ResponsesStreamResponseType, delta string) *BifrostResponsesStreamResponse {
	return &BifrostResponsesStreamResponse{Type: eventType, OutputIndex: Ptr(0), ContentIndex: Ptr(0), Delta: Ptr(delta)}
}

// syncChatChunk builds a normalized chat stream chunk with optional content and finish reason.
func syncChatChunk(content *string, finishReason *string) *BifrostChatResponse {
	return &BifrostChatResponse{
		ID:     "chatcmpl-1",
		Object: "chat.completion.chunk",
		Choices: []BifrostResponseChoice{{
			Index:                    0,
			FinishReason:             finishReason,
			ChatStreamResponseChoice: &ChatStreamResponseChoice{Delta: &ChatStreamResponseChoiceDelta{Content: content}},
		}},
	}
}

// TestSyncRawResponseTextRewritesOnlyChangedChatText verifies a redacted delta is mirrored into the raw
// frame at the same path and every other raw byte is preserved.
func TestSyncRawResponseTextRewritesOnlyChangedChatText(t *testing.T) {
	before := syncChatChunk(Ptr("mail alex@example.com"), nil)
	after := syncChatChunk(Ptr("mail [EMAIL-1]"), nil)

	synced, err := SyncRawResponseText(syncContentFrame, RawResponseFormatOpenAIChatCompletionChunk, before, after)

	require.NoError(t, err)
	assert.Equal(t, `{"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"mail [EMAIL-1]"},"finish_reason":null}],"obfuscation":"c1"}`, synced)
}

// TestSyncRawResponseTextHandlesBundledFrames verifies held text is blanked in the frame that carries it while
// the bundled role frame and the blank-line separator stay byte-identical.
func TestSyncRawResponseTextHandlesBundledFrames(t *testing.T) {
	raw := syncRoleFrame + "\n\n" + syncContentFrame
	before := syncChatChunk(Ptr("mail alex@example.com"), nil)
	after := syncChatChunk(Ptr(""), nil)

	synced, err := SyncRawResponseText(raw, RawResponseFormatOpenAIChatCompletionChunk, before, after)

	require.NoError(t, err)
	assert.Equal(t, syncRoleFrame+"\n\n"+`{"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":""},"finish_reason":null}],"obfuscation":"c1"}`, synced)
}

// TestSyncRawResponseTextPlacesReleasedTextOnFinishFrame verifies text released on the terminal chunk lands in
// the finish frame's delta (the frame that owns the choice), not in the usage-only frame.
func TestSyncRawResponseTextPlacesReleasedTextOnFinishFrame(t *testing.T) {
	raw := syncFinishFrame + "\n\n" + syncUsageFrame
	before := syncChatChunk(nil, Ptr("stop"))
	after := syncChatChunk(Ptr("Contact [EMAIL-1] now."), Ptr("stop"))

	synced, err := SyncRawResponseText(raw, RawResponseFormatOpenAIChatCompletionChunk, before, after)

	require.NoError(t, err)
	assert.Equal(t, `{"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"Contact [EMAIL-1] now."},"finish_reason":"stop"}],"obfuscation":"f1"}`+"\n\n"+syncUsageFrame, synced)
}

// TestSyncRawResponseTextMovesTextSpreadOverFrames verifies text split across bundled frames is verified as a
// whole and rewritten into the last frame that carried part of it.
func TestSyncRawResponseTextMovesTextSpreadOverFrames(t *testing.T) {
	first := `{"choices":[{"index":0,"delta":{"content":"alex@"}}]}`
	second := `{"choices":[{"index":0,"delta":{"content":"example.com"}}]}`
	before := syncChatChunk(Ptr("alex@example.com"), nil)
	after := syncChatChunk(Ptr("[EMAIL-1]"), nil)

	synced, err := SyncRawResponseText(first+"\n\n"+second, RawResponseFormatOpenAIChatCompletionChunk, before, after)

	require.NoError(t, err)
	assert.Equal(t, `{"choices":[{"index":0,"delta":{"content":""}}]}`+"\n\n"+`{"choices":[{"index":0,"delta":{"content":"[EMAIL-1]"}}]}`, synced)
}

// TestSyncRawResponseTextLeavesUnchangedFieldsAlone verifies fields the plugin did not change (here reasoning)
// keep their raw value while content is rewritten.
func TestSyncRawResponseTextLeavesUnchangedFieldsAlone(t *testing.T) {
	raw := `{"choices":[{"index":0,"delta":{"content":"alex@example.com","reasoning_content":"echo alex@example.com"}}]}`
	before := syncChatChunk(Ptr("alex@example.com"), nil)
	before.Choices[0].Delta.Reasoning = Ptr("echo alex@example.com")
	after := syncChatChunk(Ptr("[EMAIL-1]"), nil)
	after.Choices[0].Delta.Reasoning = Ptr("echo alex@example.com")

	synced, err := SyncRawResponseText(raw, RawResponseFormatOpenAIChatCompletionChunk, before, after)

	require.NoError(t, err)
	assert.Equal(t, `{"choices":[{"index":0,"delta":{"content":"[EMAIL-1]","reasoning_content":"echo alex@example.com"}}]}`, synced)
}

// TestSyncRawResponseTextReturnsRawWhenNothingChanged verifies an unchanged chunk keeps its exact raw value.
func TestSyncRawResponseTextReturnsRawWhenNothingChanged(t *testing.T) {
	chunk := syncChatChunk(Ptr("mail alex@example.com"), nil)

	synced, err := SyncRawResponseText(syncContentFrame, RawResponseFormatOpenAIChatCompletionChunk, chunk, syncChatChunk(Ptr("mail alex@example.com"), nil))

	require.NoError(t, err)
	assert.Equal(t, syncContentFrame, synced)
}

// TestSyncRawResponseTextRewritesTextCompletionChoice verifies the legacy completions format.
func TestSyncRawResponseTextRewritesTextCompletionChoice(t *testing.T) {
	raw := `{"id":"cmpl-1","object":"text_completion","choices":[{"index":0,"text":"alex@example.com","finish_reason":null}]}`
	textChunk := func(text string) *BifrostTextCompletionResponse {
		return &BifrostTextCompletionResponse{Choices: []BifrostResponseChoice{{TextCompletionResponseChoice: &TextCompletionResponseChoice{Text: Ptr(text)}}}}
	}

	synced, err := SyncRawResponseText(raw, RawResponseFormatOpenAITextCompletionChunk, textChunk("alex@example.com"), textChunk("[EMAIL-1]"))

	require.NoError(t, err)
	assert.Equal(t, `{"id":"cmpl-1","object":"text_completion","choices":[{"index":0,"text":"[EMAIL-1]","finish_reason":null}]}`, synced)
}

// TestSyncRawResponseTextRewritesResponsesEvents verifies delta, done, and nested completed-response text paths.
func TestSyncRawResponseTextRewritesResponsesEvents(t *testing.T) {
	t.Run("delta", func(t *testing.T) {
		raw := `{"type":"response.output_text.delta","sequence_number":4,"item_id":"msg_1","output_index":0,"content_index":0,"delta":" alex","obfuscation":"o1"}`
		event := func(delta string) *BifrostResponsesStreamResponse {
			return &BifrostResponsesStreamResponse{Type: ResponsesStreamResponseTypeOutputTextDelta, SequenceNumber: 4, ItemID: Ptr("msg_1"), OutputIndex: Ptr(0), ContentIndex: Ptr(0), Delta: Ptr(delta)}
		}

		synced, err := SyncRawResponseText(raw, RawResponseFormatOpenAIResponsesStreamEvent, event(" alex"), event(""))

		require.NoError(t, err)
		assert.Equal(t, `{"type":"response.output_text.delta","sequence_number":4,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"","obfuscation":"o1"}`, synced)
	})

	t.Run("completed", func(t *testing.T) {
		raw := `{"type":"response.completed","sequence_number":9,"response":{"id":"resp_1","object":"response","status":"completed","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"mail alex@example.com","annotations":[]}]}]}}`
		event := func(text string) *BifrostResponsesStreamResponse {
			return &BifrostResponsesStreamResponse{
				Type:           ResponsesStreamResponseTypeCompleted,
				SequenceNumber: 9,
				Response: &BifrostResponsesResponse{
					ID: Ptr("resp_1"),
					Output: []ResponsesMessage{{
						ID:     Ptr("msg_1"),
						Type:   Ptr(ResponsesMessageTypeMessage),
						Role:   Ptr(ResponsesInputMessageRoleAssistant),
						Status: Ptr("completed"),
						Content: &ResponsesMessageContent{ContentBlocks: []ResponsesMessageContentBlock{{
							Type: ResponsesOutputMessageContentTypeText,
							Text: Ptr(text),
						}}},
					}},
				},
			}
		}

		synced, err := SyncRawResponseText(raw, RawResponseFormatOpenAIResponsesStreamEvent, event("mail alex@example.com"), event("mail [EMAIL-1]"))

		require.NoError(t, err)
		assert.Equal(t, `{"type":"response.completed","sequence_number":9,"response":{"id":"resp_1","object":"response","status":"completed","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"mail [EMAIL-1]","annotations":[]}]}]}}`, synced)
	})
}

// TestSyncRawResponseTextReportsUnsupportedPayloads verifies payloads the synchronizer does not understand are
// reported as unsupported so callers keep their previous handling instead of guessing.
func TestSyncRawResponseTextReportsUnsupportedPayloads(t *testing.T) {
	before := syncChatChunk(Ptr("mail alex@example.com"), nil)
	after := syncChatChunk(Ptr("mail [EMAIL-1]"), nil)

	cases := map[string]struct {
		raw    any
		format RawResponseFormat
		before any
		after  any
	}{
		"undeclared format":                {raw: syncContentFrame, format: "", before: before, after: after},
		"format for another payload":       {raw: syncContentFrame, format: RawResponseFormatOpenAIResponsesStreamEvent, before: before, after: after},
		"raw is not a string":              {raw: map[string]any{"choices": []any{}}, format: RawResponseFormatOpenAIChatCompletionChunk, before: before, after: after},
		"raw is not JSON":                  {raw: "event: ping", format: RawResponseFormatOpenAIChatCompletionChunk, before: before, after: after},
		"raw has no content field":         {raw: `{"choices":[{"index":0,"delta":{"text":"mail alex@example.com"}}]}`, format: RawResponseFormatOpenAIChatCompletionChunk, before: before, after: after},
		"content blocks without text":      {raw: `{"choices":[{"index":0,"delta":{"content":[{"type":"thinking","thinking":[]}]}}]}`, format: RawResponseFormatOpenAIChatCompletionChunk, before: syncChatChunk(nil, nil), after: syncChatChunk(Ptr("released"), nil)},
		"text format on a responses event": {raw: `{"choices":[{"index":0,"text":"mail alex@example.com"}]}`, format: RawResponseFormatOpenAITextCompletionChunk, before: syncResponsesDelta(ResponsesStreamResponseTypeOutputTextDelta, "mail alex@example.com"), after: syncResponsesDelta(ResponsesStreamResponseTypeOutputTextDelta, "mail [EMAIL-1]")},
		"anthropic malformed text delta":   {raw: `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta"}}`, format: RawResponseFormatAnthropicMessagesStreamEvent, before: before, after: after},
		"gemini partial arguments":         {raw: `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"f","args":{"to":"alex@example.com"}}}]}}]}`, format: RawResponseFormatGeminiStreamChunk, before: syncToolChunk(`{"to":"alex@`), after: syncToolChunk(`{"to":"`)},
		"anthropic closing event":          {raw: anthropicMessageDelta, format: RawResponseFormatAnthropicMessagesStreamEvent, before: syncChatChunk(nil, Ptr("stop")), after: syncChatChunk(Ptr("released"), Ptr("stop"))},
		"gemini code execution part":       {raw: `{"candidates":[{"content":{"role":"model","parts":[{"executableCode":{"language":"PYTHON","code":"print(1)"}},{"text":"mail alex@example.com"}]}}]}`, format: RawResponseFormatGeminiStreamChunk, before: before, after: after},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			synced, err := SyncRawResponseText(tc.raw, tc.format, tc.before, tc.after)

			require.Error(t, err)
			assert.True(t, errors.Is(err, ErrRawResponseTextUnsupported), "error %v", err)
			assert.Equal(t, tc.raw, synced, "an unsupported payload must be returned unchanged")
		})
	}
}

// TestSyncRawResponseTextRejectsMisalignedText verifies raw text that does not match the normalized original is
// reported (not overwritten), including raw text where the normalized payload had none.
func TestSyncRawResponseTextRejectsMisalignedText(t *testing.T) {
	t.Run("different original", func(t *testing.T) {
		raw := `{"choices":[{"index":0,"delta":{"content":"something else"}}]}`

		synced, err := SyncRawResponseText(raw, RawResponseFormatOpenAIChatCompletionChunk, syncChatChunk(Ptr("mail alex@example.com"), nil), syncChatChunk(Ptr("mail [EMAIL-1]"), nil))

		require.Error(t, err)
		assert.False(t, errors.Is(err, ErrRawResponseTextUnsupported))
		assert.Equal(t, raw, synced)
	})

	t.Run("different original in a mapped field", func(t *testing.T) {
		raw := `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"something else"}}`

		synced, err := SyncRawResponseText(raw, RawResponseFormatAnthropicMessagesStreamEvent, syncChatChunk(Ptr("mail alex@example.com"), nil), syncChatChunk(Ptr("mail [EMAIL-1]"), nil))

		require.Error(t, err)
		assert.False(t, errors.Is(err, ErrRawResponseTextUnsupported))
		assert.Equal(t, raw, synced)
	})

	t.Run("raw text where normalized had none", func(t *testing.T) {
		raw := `{"choices":[{"index":0,"delta":{"content":"leftover"},"finish_reason":"stop"}]}`

		synced, err := SyncRawResponseText(raw, RawResponseFormatOpenAIChatCompletionChunk, syncChatChunk(nil, Ptr("stop")), syncChatChunk(Ptr("released"), Ptr("stop")))

		require.Error(t, err)
		assert.Equal(t, raw, synced)
	})
}

// TestSyncRawResponseTextMapsProviderEvents verifies each provider format mirrors a normalized text change into the
// field its own event keeps that text in, leaving every other raw byte (reasoning included) unchanged.
func TestSyncRawResponseTextMapsProviderEvents(t *testing.T) {
	cases := map[string]struct {
		format        RawResponseFormat
		raw           string
		before, after any
		want          string
	}{
		"anthropic chat text": {
			format: RawResponseFormatAnthropicMessagesStreamEvent, raw: anthropicTextDelta,
			before: syncChatChunk(Ptr("mail alex@example.com"), nil), after: syncChatChunk(Ptr("mail [EMAIL-1]"), nil),
			want: `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"mail [EMAIL-1]"}}`,
		},
		"anthropic responses text held back": {
			format: RawResponseFormatAnthropicMessagesStreamEvent, raw: anthropicTextDelta,
			before: syncResponsesDelta(ResponsesStreamResponseTypeOutputTextDelta, "mail alex@example.com"),
			after:  syncResponsesDelta(ResponsesStreamResponseTypeOutputTextDelta, ""),
			want:   `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":""}}`,
		},
		"anthropic tool arguments": {
			format: RawResponseFormatAnthropicMessagesStreamEvent, raw: anthropicJSONDelta,
			before: syncToolChunk(`{"to":"alex@example.com"}`), after: syncToolChunk(`{"to":"[EMAIL-1]"}`),
			want: `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"to\":\"[EMAIL-1]\"}"}}`,
		},
		"anthropic structured output": {
			format: RawResponseFormatAnthropicMessagesStreamEvent, raw: anthropicJSONDelta,
			before: syncChatChunk(Ptr(`{"to":"alex@example.com"}`), nil), after: syncChatChunk(Ptr(`{"to":"[EMAIL-1]"}`), nil),
			want: `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"to\":\"[EMAIL-1]\"}"}}`,
		},
		"bedrock text": {
			format: RawResponseFormatBedrockConverseStreamEvent, raw: `{"contentBlockIndex":0,"delta":{"text":"mail alex@example.com"},"p":"abc"}`,
			before: syncChatChunk(Ptr("mail alex@example.com"), nil), after: syncChatChunk(Ptr("mail [EMAIL-1]"), nil),
			want: `{"contentBlockIndex":0,"delta":{"text":"mail [EMAIL-1]"},"p":"abc"}`,
		},
		"bedrock tool arguments": {
			format: RawResponseFormatBedrockConverseStreamEvent, raw: `{"contentBlockIndex":1,"delta":{"toolUse":{"input":"{\"to\":\"alex@example.com\"}"}},"p":"ab"}`,
			before: syncToolChunk(`{"to":"alex@example.com"}`), after: syncToolChunk(`{"to":"[EMAIL-1]"}`),
			want: `{"contentBlockIndex":1,"delta":{"toolUse":{"input":"{\"to\":\"[EMAIL-1]\"}"}},"p":"ab"}`,
		},
		"cohere text": {
			format: RawResponseFormatCohereChatStreamEvent, raw: `{"type":"content-delta","index":0,"delta":{"message":{"content":{"text":"mail alex@example.com"}}}}`,
			before: syncChatChunk(Ptr("mail alex@example.com"), nil), after: syncChatChunk(Ptr("mail [EMAIL-1]"), nil),
			want: `{"type":"content-delta","index":0,"delta":{"message":{"content":{"text":"mail [EMAIL-1]"}}}}`,
		},
		"cohere tool arguments": {
			format: RawResponseFormatCohereChatStreamEvent, raw: `{"type":"tool-call-delta","index":0,"delta":{"message":{"tool_calls":{"function":{"arguments":"{\"to\":\"alex@example.com\"}"}}}}}`,
			before: syncToolChunk(`{"to":"alex@example.com"}`), after: syncToolChunk(`{"to":"[EMAIL-1]"}`),
			want: `{"type":"tool-call-delta","index":0,"delta":{"message":{"tool_calls":{"function":{"arguments":"{\"to\":\"[EMAIL-1]\"}"}}}}}`,
		},
		"gemini text parts skip thoughts": {
			format: RawResponseFormatGeminiStreamChunk,
			raw:    `{"candidates":[{"content":{"role":"model","parts":[{"text":"check alex@example.com","thought":true},{"text":"mail alex@"},{"text":"example.com"}]},"index":0}]}`,
			before: syncChatChunk(Ptr("mail alex@example.com"), nil), after: syncChatChunk(Ptr("mail [EMAIL-1]"), nil),
			want: `{"candidates":[{"content":{"role":"model","parts":[{"text":"check alex@example.com","thought":true},{"text":""},{"text":"mail [EMAIL-1]"}]},"index":0}]}`,
		},
		"gemini responses delta": {
			format: RawResponseFormatGeminiStreamChunk, raw: `{"candidates":[{"content":{"role":"model","parts":[{"text":"mail alex@example.com"}]}}]}`,
			before: syncResponsesDelta(ResponsesStreamResponseTypeOutputTextDelta, "mail alex@example.com"),
			after:  syncResponsesDelta(ResponsesStreamResponseTypeOutputTextDelta, "mail [EMAIL-1]"),
			want:   `{"candidates":[{"content":{"role":"model","parts":[{"text":"mail [EMAIL-1]"}]}}]}`,
		},
		"replicate chat": {
			format: RawResponseFormatReplicateStreamEvent, raw: `{"Data":"mail alex@example.com","Event":"output","ID":""}`,
			before: syncChatChunk(Ptr("mail alex@example.com"), nil), after: syncChatChunk(Ptr("mail [EMAIL-1]"), nil),
			want: `{"Data":"mail [EMAIL-1]","Event":"output","ID":""}`,
		},
		"replicate text completion": {
			format: RawResponseFormatReplicateStreamEvent, raw: `{"Data":"mail alex@example.com","Event":"output","ID":""}`,
			before: &BifrostTextCompletionResponse{Choices: []BifrostResponseChoice{{TextCompletionResponseChoice: &TextCompletionResponseChoice{Text: Ptr("mail alex@example.com")}}}},
			after:  &BifrostTextCompletionResponse{Choices: []BifrostResponseChoice{{TextCompletionResponseChoice: &TextCompletionResponseChoice{Text: Ptr("mail [EMAIL-1]")}}}},
			want:   `{"Data":"mail [EMAIL-1]","Event":"output","ID":""}`,
		},
		"openai-compatible content blocks": {
			format: RawResponseFormatOpenAIChatCompletionChunk,
			raw:    `{"choices":[{"index":0,"delta":{"content":[{"type":"thinking","thinking":[{"type":"text","text":"alex@example.com?"}]},{"type":"text","text":"mail alex@example.com"}]}}]}`,
			before: syncChatChunk(Ptr("mail alex@example.com"), nil), after: syncChatChunk(Ptr("mail [EMAIL-1]"), nil),
			want: `{"choices":[{"index":0,"delta":{"content":[{"type":"thinking","thinking":[{"type":"text","text":"alex@example.com?"}]},{"type":"text","text":"mail [EMAIL-1]"}]}}]}`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			synced, err := SyncRawResponseText(tc.raw, tc.format, tc.before, tc.after)

			require.NoError(t, err)
			assert.Equal(t, tc.want, synced)
		})
	}
}

// TestSyncRawResponseTextLeavesTextAProviderEventDoesNotCarry verifies accumulated Responses text, which delta-only
// provider events never contain, is left out, while reasoning text a provider does carry is never rewritten.
func TestSyncRawResponseTextLeavesTextAProviderEventDoesNotCarry(t *testing.T) {
	t.Run("accumulated text on a closing event", func(t *testing.T) {
		done := func(text string) *BifrostResponsesStreamResponse {
			return &BifrostResponsesStreamResponse{Type: ResponsesStreamResponseTypeOutputTextDone, OutputIndex: Ptr(0), ContentIndex: Ptr(0), Text: Ptr(text)}
		}

		synced, err := SyncRawResponseText(anthropicBlockStop, RawResponseFormatAnthropicMessagesStreamEvent, done("mail alex@example.com"), done("mail [EMAIL-1]"))

		require.NoError(t, err)
		assert.Equal(t, anthropicBlockStop, synced)
	})

	t.Run("reasoning delta", func(t *testing.T) {
		raw := `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"check alex@example.com"}}`
		before := syncResponsesDelta(ResponsesStreamResponseTypeReasoningSummaryTextDelta, "check alex@example.com")
		after := syncResponsesDelta(ResponsesStreamResponseTypeReasoningSummaryTextDelta, "check [EMAIL-1]")

		synced, err := SyncRawResponseText(raw, RawResponseFormatAnthropicMessagesStreamEvent, before, after)

		require.ErrorIs(t, err, ErrRawResponseTextUnsupported)
		assert.Equal(t, raw, synced)
	})
}

// TestSyncRawResponseTextFillsEmptyMappedField verifies text released onto a provider text event whose field was
// already emptied goes back into that field, which is how held text returns to an earlier event.
func TestSyncRawResponseTextFillsEmptyMappedField(t *testing.T) {
	raw := `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":""}}`

	synced, err := SyncRawResponseText(raw, RawResponseFormatAnthropicMessagesStreamEvent, syncChatChunk(Ptr(""), nil), syncChatChunk(Ptr("Thanks!"), nil))

	require.NoError(t, err)
	assert.Equal(t, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Thanks!"}}`, synced)
}

// TestRawResponseFormatTextOnlyInDeltaEvents pins which formats have closing events without a text field.
func TestRawResponseFormatTextOnlyInDeltaEvents(t *testing.T) {
	cases := map[RawResponseFormat]bool{
		RawResponseFormatOpenAIChatCompletionChunk:    false,
		RawResponseFormatOpenAITextCompletionChunk:    false,
		RawResponseFormatOpenAIResponsesStreamEvent:   false,
		RawResponseFormatGeminiStreamChunk:            false,
		RawResponseFormatAnthropicMessagesStreamEvent: true,
		RawResponseFormatBedrockConverseStreamEvent:   true,
		RawResponseFormatCohereChatStreamEvent:        true,
		RawResponseFormatReplicateStreamEvent:         true,
		"":                                            false,
	}
	for format, want := range cases {
		assert.Equal(t, want, format.TextOnlyInDeltaEvents(), "format %q", format)
	}
}

// TestSyncRawResponseTextMapsAcrossPayloadTypes verifies raw frames of one API backing chunks converted to another
// API: chat frames behind Text chunks and Responses events, Responses events behind Chat chunks, and provider events
// behind Text chunks. Accumulated Responses text the chat frames never carry is left out.
func TestSyncRawResponseTextMapsAcrossPayloadTypes(t *testing.T) {
	textChunk := func(text string) *BifrostTextCompletionResponse {
		return &BifrostTextCompletionResponse{Choices: []BifrostResponseChoice{{TextCompletionResponseChoice: &TextCompletionResponseChoice{Text: Ptr(text)}}}}
	}
	argsFrame := func(arguments string) string {
		return `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":` + strconv.Quote(arguments) + `}}]}}]}`
	}
	cases := map[string]struct {
		format        RawResponseFormat
		raw           string
		before, after any
		want          string
	}{
		"chat frame behind a text chunk": {
			format: RawResponseFormatOpenAIChatCompletionChunk, raw: syncContentFrame,
			before: textChunk("mail alex@example.com"), after: textChunk("mail [EMAIL-1]"),
			want: `{"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"mail [EMAIL-1]"},"finish_reason":null}],"obfuscation":"c1"}`,
		},
		"bundled chat frames behind a responses text delta": {
			format: RawResponseFormatOpenAIChatCompletionChunk, raw: syncRoleFrame + "\n\n" + syncContentFrame,
			before: syncResponsesDelta(ResponsesStreamResponseTypeOutputTextDelta, "mail alex@example.com"),
			after:  syncResponsesDelta(ResponsesStreamResponseTypeOutputTextDelta, ""),
			want:   syncRoleFrame + "\n\n" + `{"id":"chatcmpl-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":""},"finish_reason":null}],"obfuscation":"c1"}`,
		},
		"chat frame behind a responses arguments delta": {
			format: RawResponseFormatOpenAIChatCompletionChunk, raw: argsFrame(`{"to":"alex@example.com"}`),
			before: syncResponsesDelta(ResponsesStreamResponseTypeFunctionCallArgumentsDelta, `{"to":"alex@example.com"}`),
			after:  syncResponsesDelta(ResponsesStreamResponseTypeFunctionCallArgumentsDelta, `{"to":"[EMAIL-1]"}`),
			want:   argsFrame(`{"to":"[EMAIL-1]"}`),
		},
		"chat frame behind a responses done event": {
			format: RawResponseFormatOpenAIChatCompletionChunk, raw: syncFinishFrame,
			before: &BifrostResponsesStreamResponse{Type: ResponsesStreamResponseTypeOutputTextDone, OutputIndex: Ptr(0), ContentIndex: Ptr(0), Text: Ptr("mail alex@example.com")},
			after:  &BifrostResponsesStreamResponse{Type: ResponsesStreamResponseTypeOutputTextDone, OutputIndex: Ptr(0), ContentIndex: Ptr(0), Text: Ptr("mail [EMAIL-1]")},
			want:   syncFinishFrame,
		},
		"responses event behind a chat chunk": {
			format: RawResponseFormatOpenAIResponsesStreamEvent, raw: `{"type":"response.output_text.delta","sequence_number":4,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"mail alex@example.com"}`,
			before: syncChatChunk(Ptr("mail alex@example.com"), nil), after: syncChatChunk(Ptr("mail [EMAIL-1]"), nil),
			want: `{"type":"response.output_text.delta","sequence_number":4,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"mail [EMAIL-1]"}`,
		},
		"responses arguments event behind a chat chunk": {
			format: RawResponseFormatOpenAIResponsesStreamEvent, raw: `{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":1,"delta":"{\"to\":\"alex@example.com\"}"}`,
			before: syncToolChunk(`{"to":"alex@example.com"}`), after: syncToolChunk(`{"to":"[EMAIL-1]"}`),
			want: `{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":1,"delta":"{\"to\":\"[EMAIL-1]\"}"}`,
		},
		"anthropic event behind a text chunk": {
			format: RawResponseFormatAnthropicMessagesStreamEvent, raw: anthropicTextDelta,
			before: textChunk("mail alex@example.com"), after: textChunk("mail [EMAIL-1]"),
			want: `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"mail [EMAIL-1]"}}`,
		},
		"cohere tool plan shown as responses text": {
			format: RawResponseFormatCohereChatStreamEvent, raw: `{"type":"tool-plan-delta","delta":{"message":{"tool_plan":"look up alex@example.com"}}}`,
			before: syncResponsesDelta(ResponsesStreamResponseTypeOutputTextDelta, "look up alex@example.com"),
			after:  syncResponsesDelta(ResponsesStreamResponseTypeOutputTextDelta, "look up [EMAIL-1]"),
			want:   `{"type":"tool-plan-delta","delta":{"message":{"tool_plan":"look up [EMAIL-1]"}}}`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			synced, err := SyncRawResponseText(tc.raw, tc.format, tc.before, tc.after)

			require.NoError(t, err)
			assert.Equal(t, tc.want, synced)
		})
	}
}

// TestSyncRawResponseTextReplacesGeminiArgumentObjects verifies Gemini's functionCall args objects follow redacted
// tool arguments, whether the edit comes from a Chat tool-call delta or a Responses item's accumulated arguments, and
// that arguments of calls the event does not hold are left out.
func TestSyncRawResponseTextReplacesGeminiArgumentObjects(t *testing.T) {
	const raw = `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"notify","args":{"to":"alex@example.com","count":2}}},{"functionCall":{"name":"other","args":{"q":"x"}}}]}}]}`
	const want = `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"notify","args":{"to":"[EMAIL-1]","count":2}}},{"functionCall":{"name":"other","args":{"q":"x"}}}]}}]}`
	itemDone := func(arguments string) *BifrostResponsesStreamResponse {
		return &BifrostResponsesStreamResponse{
			Type:        ResponsesStreamResponseTypeOutputItemDone,
			OutputIndex: Ptr(0),
			Item: &ResponsesMessage{
				Type:                 Ptr(ResponsesMessageTypeFunctionCall),
				ResponsesToolMessage: &ResponsesToolMessage{Name: Ptr("notify"), CallID: Ptr("call_1"), Arguments: Ptr(arguments)},
			},
		}
	}

	t.Run("chat tool call", func(t *testing.T) {
		synced, err := SyncRawResponseText(raw, RawResponseFormatGeminiStreamChunk, syncToolChunk(`{"count":2,"to":"alex@example.com"}`), syncToolChunk(`{"to":"[EMAIL-1]","count":2}`))

		require.NoError(t, err)
		assert.Equal(t, want, synced)
	})

	t.Run("responses output item", func(t *testing.T) {
		synced, err := SyncRawResponseText(raw, RawResponseFormatGeminiStreamChunk, itemDone(`{"to":"alex@example.com","count":2}`), itemDone(`{"to":"[EMAIL-1]","count":2}`))

		require.NoError(t, err)
		assert.Equal(t, want, synced)
	})

	t.Run("arguments of a call the event does not hold", func(t *testing.T) {
		synced, err := SyncRawResponseText(raw, RawResponseFormatGeminiStreamChunk, itemDone(`{"to":"bob@example.com"}`), itemDone(`{"to":"[EMAIL-2]"}`))

		require.NoError(t, err)
		assert.Equal(t, raw, synced)
	})
}

// TestSyncRawResponseTextLeavesAnthropicSignaturesAlone verifies a signature delta never takes edited text.
func TestSyncRawResponseTextLeavesAnthropicSignaturesAlone(t *testing.T) {
	raw := `{"type":"content_block_delta","index":1,"delta":{"type":"signature_delta","signature":"sig-alex@example.com"}}`

	synced, err := SyncRawResponseText(raw, RawResponseFormatAnthropicMessagesStreamEvent, syncResponsesDelta(ResponsesStreamResponseTypeOutputTextDelta, "sig-alex@example.com"), syncResponsesDelta(ResponsesStreamResponseTypeOutputTextDelta, ""))

	require.ErrorIs(t, err, ErrRawResponseTextUnsupported)
	assert.Equal(t, raw, synced)
}

// TestTextCompletionConversionKeepsRawResponseFormat verifies a chat chunk converted for a text-completion caller keeps
// its raw payload's format, so the chat frame still follows a change to the converted text.
func TestTextCompletionConversionKeepsRawResponseFormat(t *testing.T) {
	chat := syncChatChunk(Ptr("mail alex@example.com"), nil)
	chat.ExtraFields.RawResponse = syncContentFrame
	chat.ExtraFields.RawResponseFormat = RawResponseFormatOpenAIChatCompletionChunk

	text := chat.ToBifrostTextCompletionResponse()
	require.Equal(t, RawResponseFormatOpenAIChatCompletionChunk, text.ExtraFields.RawResponseFormat)

	redacted := *text
	redacted.Choices = []BifrostResponseChoice{{TextCompletionResponseChoice: &TextCompletionResponseChoice{Text: Ptr("mail [EMAIL-1]")}}}
	synced, err := SyncRawResponseText(text.ExtraFields.RawResponse, text.ExtraFields.RawResponseFormat, text, &redacted)
	require.NoError(t, err)
	assert.Contains(t, synced, `"content":"mail [EMAIL-1]"`)
}

// redactTestReplacements is the replacement map the RedactRawResponseText tests apply.
var redactTestReplacements = map[string]string{"alex@example.com": "[EMAIL-1]"}

// redactTestRaw fills each frame template's %s with the matching text as a JSON string and joins the frames with
// blank lines, as the stream accumulator stores a stream's raw payloads.
func redactTestRaw(t testing.TB, templates []string, texts []string) string {
	t.Helper()
	frames := make([]string, len(templates))
	for index, template := range templates {
		text, err := Marshal(texts[index])
		require.NoError(t, err)
		frames[index] = fmt.Sprintf(template, text)
	}
	return strings.Join(frames, "\n\n")
}

// TestRedactRawResponseTextJoinsValuesSplitAcrossFrames verifies each format's streamed text and tool arguments are
// joined across frames, with the replacement written into the frame that completes the match.
func TestRedactRawResponseTextJoinsValuesSplitAcrossFrames(t *testing.T) {
	text := []string{"mail alex@", "example.com today"}
	redactedText := []string{"mail ", "[EMAIL-1] today"}
	arguments := []string{`{"to":"alex@`, `example.com"}`}
	redactedArguments := []string{`{"to":"`, `[EMAIL-1]"}`}
	twice := func(template string) []string { return []string{template, template} }

	cases := map[string]struct {
		format RawResponseFormat
		frames []string
		texts  []string
		want   []string
	}{
		"openai chat content": {
			format: RawResponseFormatOpenAIChatCompletionChunk,
			frames: twice(`{"id":"chatcmpl-1","choices":[{"index":0,"delta":{"content":%s},"finish_reason":null}]}`),
			texts:  text, want: redactedText,
		},
		"openai chat tool arguments": {
			format: RawResponseFormatOpenAIChatCompletionChunk,
			frames: twice(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":%s}}]}}]}`),
			texts:  arguments, want: redactedArguments,
		},
		"openai-compatible content blocks": {
			format: RawResponseFormatOpenAIChatCompletionChunk,
			frames: twice(`{"choices":[{"index":0,"delta":{"content":[{"type":"text","text":%s}]}}]}`),
			texts:  text, want: redactedText,
		},
		"openai text completion": {
			format: RawResponseFormatOpenAITextCompletionChunk,
			frames: twice(`{"choices":[{"index":0,"text":%s}]}`),
			texts:  text, want: redactedText,
		},
		"openai responses text": {
			format: RawResponseFormatOpenAIResponsesStreamEvent,
			frames: twice(`{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":%s}`),
			texts:  text, want: redactedText,
		},
		"openai responses arguments": {
			format: RawResponseFormatOpenAIResponsesStreamEvent,
			frames: twice(`{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":1,"delta":%s}`),
			texts:  arguments, want: redactedArguments,
		},
		"anthropic text": {
			format: RawResponseFormatAnthropicMessagesStreamEvent,
			frames: twice(`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%s}}`),
			texts:  text, want: redactedText,
		},
		"anthropic tool input": {
			format: RawResponseFormatAnthropicMessagesStreamEvent,
			frames: twice(`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":%s}}`),
			texts:  arguments, want: redactedArguments,
		},
		"bedrock text": {
			format: RawResponseFormatBedrockConverseStreamEvent,
			frames: twice(`{"contentBlockIndex":0,"delta":{"text":%s},"p":"abc"}`),
			texts:  text, want: redactedText,
		},
		"bedrock tool input": {
			format: RawResponseFormatBedrockConverseStreamEvent,
			frames: twice(`{"contentBlockIndex":1,"delta":{"toolUse":{"input":%s}},"p":"ab"}`),
			texts:  arguments, want: redactedArguments,
		},
		"cohere text": {
			format: RawResponseFormatCohereChatStreamEvent,
			frames: twice(`{"type":"content-delta","index":0,"delta":{"message":{"content":{"text":%s}}}}`),
			texts:  text, want: redactedText,
		},
		"cohere tool arguments": {
			format: RawResponseFormatCohereChatStreamEvent,
			frames: twice(`{"type":"tool-call-delta","index":0,"delta":{"message":{"tool_calls":{"function":{"arguments":%s}}}}}`),
			texts:  arguments, want: redactedArguments,
		},
		"gemini text with an omitted zero index": {
			format: RawResponseFormatGeminiStreamChunk,
			frames: []string{
				`{"candidates":[{"content":{"role":"model","parts":[{"text":%s}]}}]}`,
				`{"candidates":[{"content":{"role":"model","parts":[{"text":%s}]},"index":0}]}`,
			},
			texts: text, want: redactedText,
		},
		"replicate output": {
			format: RawResponseFormatReplicateStreamEvent,
			frames: twice(`{"Data":%s,"Event":"output","ID":""}`),
			texts:  text, want: redactedText,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			raw := redactTestRaw(t, tc.frames, tc.texts)

			redacted := RedactRawResponseText(raw, tc.format, redactTestReplacements)

			assert.Equal(t, redactTestRaw(t, tc.frames, tc.want), redacted)
		})
	}
}

// TestRedactRawResponseTextKeepsStreamedValuesApart verifies pieces join only within their own value: parallel tool
// calls stay separate even when their frames interleave, and reasoning text is left alone.
func TestRedactRawResponseTextKeepsStreamedValuesApart(t *testing.T) {
	reasoning := `{"choices":[{"index":0,"delta":{"reasoning":%s}}]}`
	firstCall := `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":%s}}]}}]}`
	secondCall := `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":%s}}]}}]}`
	frames := []string{reasoning, reasoning, secondCall, firstCall, secondCall, firstCall}
	raw := redactTestRaw(t, frames, []string{"check alex@", "example.com first", `{"url":"https://`, `{"to":"alex@`, `example.com/docs"}`, `example.com"}`})

	redacted := RedactRawResponseText(raw, RawResponseFormatOpenAIChatCompletionChunk, redactTestReplacements)

	want := redactTestRaw(t, frames, []string{"check alex@", "example.com first", `{"url":"https://`, `{"to":"`, `example.com/docs"}`, `[EMAIL-1]"}`})
	assert.Equal(t, want, redacted)
}

// TestRedactRawResponseTextMatchesApplyLiteralReplacements verifies a streamed value's redacted pieces join to exactly
// what ApplyLiteralReplacements makes of the whole value, including where keys overlap.
func TestRedactRawResponseTextMatchesApplyLiteralReplacements(t *testing.T) {
	replacements := map[string]string{"Alex Rivera": "[NAME-1]", "Alex": "[NAME-2]", "alex@example.com": "[EMAIL-1]", "bcd": "[X]", "ab": "[Y]"}
	texts := []string{"Alex Ri", "vera, alex@exa", "mple.com and Alex a", "bcd."}
	frame := `{"Data":%s,"Event":"output","ID":""}`
	raw := redactTestRaw(t, []string{frame, frame, frame, frame}, texts)

	redacted := RedactRawResponseText(raw, RawResponseFormatReplicateStreamEvent, replacements)

	var joined strings.Builder
	gjson.ForEachLine(redacted, func(frame gjson.Result) bool {
		joined.WriteString(frame.Get("Data").String())
		return true
	})
	assert.Equal(t, ApplyLiteralReplacements(strings.Join(texts, ""), replacements), joined.String())
	assert.Equal(t, "[NAME-1], [EMAIL-1] and [NAME-2] a[X].", joined.String())
}

// TestRedactRawResponseTextLeavesUnsupportedRawUnchanged verifies raw is returned as it was when the format streams no
// text, nothing matches, or raw is not a sequence of JSON objects.
func TestRedactRawResponseTextLeavesUnsupportedRawUnchanged(t *testing.T) {
	template := `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%s}}`
	split := redactTestRaw(t, []string{template, template}, []string{"mail alex@", "example.com today"})

	cases := map[string]struct {
		raw    string
		format RawResponseFormat
	}{
		"undeclared format":   {raw: split},
		"nothing matches":     {raw: redactTestRaw(t, []string{template, template}, []string{"mail alex", " today"}), format: RawResponseFormatAnthropicMessagesStreamEvent},
		"truncated preview":   {raw: split[:len(split)-4], format: RawResponseFormatAnthropicMessagesStreamEvent},
		"non-object frame":    {raw: split + "\n\n" + `"upstream error"`, format: RawResponseFormatAnthropicMessagesStreamEvent},
		"text between frames": {raw: "event: content_block_delta\ndata: " + split, format: RawResponseFormatAnthropicMessagesStreamEvent},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.raw, RedactRawResponseText(tc.raw, tc.format, redactTestReplacements))
		})
	}
}

// BenchmarkRedactRawResponseText measures redacting a 1,000-frame Anthropic stream raw with one value split across two
// frames, the shape a long streamed answer leaves in a log.
func BenchmarkRedactRawResponseText(b *testing.B) {
	template := `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%s}}`
	templates := make([]string, 1000)
	texts := make([]string, 1000)
	for index := range texts {
		templates[index] = template
		texts[index] = "part " + strconv.Itoa(index) + " of a long streamed answer. "
	}
	texts[500], texts[501] = "reach alex@", "example.com today. "
	raw := redactTestRaw(b, templates, texts)

	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	for b.Loop() {
		RedactRawResponseText(raw, RawResponseFormatAnthropicMessagesStreamEvent, redactTestReplacements)
	}
}

// BenchmarkSyncRawResponseText measures mirroring one emptied streamed delta into the chunk's raw frame. The
// chunks carry that frame in their extra fields, as streamed chunks with raw responses enabled do.
func BenchmarkSyncRawResponseText(b *testing.B) {
	cases := []struct {
		name   string
		format RawResponseFormat
		raw    string
	}{
		{name: "openai_chat", format: RawResponseFormatOpenAIChatCompletionChunk, raw: syncContentFrame},
		{name: "anthropic", format: RawResponseFormatAnthropicMessagesStreamEvent, raw: anthropicTextDelta},
	}
	for _, tc := range cases {
		before := syncChatChunk(Ptr("mail alex@example.com"), nil)
		before.ExtraFields.RawResponse = tc.raw
		after := syncChatChunk(Ptr(""), nil)
		after.ExtraFields.RawResponse = tc.raw
		b.Run(tc.name+"/diff", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := SyncRawResponseText(tc.raw, tc.format, before, after); err != nil {
					b.Fatal(err)
				}
			}
		})
		edits := []RawTextEdit{{Path: syncChatContentPath, Before: "mail alex@example.com", After: ""}}
		b.Run(tc.name+"/edits", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := SyncRawResponseTextEdits(tc.raw, tc.format, after, edits); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// syncChatContentPath addresses the first choice's streamed content in a normalized chat chunk.
var syncChatContentPath = []string{"choices", "0", "delta", "content"}

// TestSyncRawResponseTextEditsMatchesPayloadDiff verifies known edits produce exactly the raw, and the error class,
// that diffing the two payload copies produces, across formats and payload types.
func TestSyncRawResponseTextEditsMatchesPayloadDiff(t *testing.T) {
	textChunk := func(text string) *BifrostTextCompletionResponse {
		return &BifrostTextCompletionResponse{Choices: []BifrostResponseChoice{{TextCompletionResponseChoice: &TextCompletionResponseChoice{Text: Ptr(text)}}}}
	}
	cases := map[string]struct {
		raw    string
		format RawResponseFormat
		before any
		after  any
		edit   RawTextEdit
	}{
		"openai chat hold": {
			raw: syncContentFrame, format: RawResponseFormatOpenAIChatCompletionChunk,
			before: syncChatChunk(Ptr("mail alex@example.com"), nil), after: syncChatChunk(Ptr(""), nil),
			edit: RawTextEdit{Path: syncChatContentPath, Before: "mail alex@example.com", After: ""},
		},
		"openai chat release on finish frame": {
			raw: syncFinishFrame + "\n\n" + syncUsageFrame, format: RawResponseFormatOpenAIChatCompletionChunk,
			before: syncChatChunk(nil, Ptr("stop")), after: syncChatChunk(Ptr("Contact [EMAIL-1] now."), Ptr("stop")),
			edit: RawTextEdit{Path: syncChatContentPath, Before: "", After: "Contact [EMAIL-1] now."},
		},
		"openai chat text spread over frames": {
			raw: `{"choices":[{"index":0,"delta":{"content":"alex@"}}]}` + "\n\n" + `{"choices":[{"index":0,"delta":{"content":"example.com"}}]}`, format: RawResponseFormatOpenAIChatCompletionChunk,
			before: syncChatChunk(Ptr("alex@example.com"), nil), after: syncChatChunk(Ptr("[EMAIL-1]"), nil),
			edit: RawTextEdit{Path: syncChatContentPath, Before: "alex@example.com", After: "[EMAIL-1]"},
		},
		"anthropic hold": {
			raw: anthropicTextDelta, format: RawResponseFormatAnthropicMessagesStreamEvent,
			before: syncChatChunk(Ptr("mail alex@example.com"), nil), after: syncChatChunk(Ptr(""), nil),
			edit: RawTextEdit{Path: syncChatContentPath, Before: "mail alex@example.com", After: ""},
		},
		"anthropic release into an emptied delta": {
			raw: `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":""}}`, format: RawResponseFormatAnthropicMessagesStreamEvent,
			before: syncResponsesDelta(ResponsesStreamResponseTypeOutputTextDelta, ""), after: syncResponsesDelta(ResponsesStreamResponseTypeOutputTextDelta, "mail [EMAIL-1]"),
			edit: RawTextEdit{Path: []string{"delta"}, Before: "", After: "mail [EMAIL-1]"},
		},
		"responses event": {
			raw: `{"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"mail alex@example.com"}`, format: RawResponseFormatOpenAIResponsesStreamEvent,
			before: syncResponsesDelta(ResponsesStreamResponseTypeOutputTextDelta, "mail alex@example.com"), after: syncResponsesDelta(ResponsesStreamResponseTypeOutputTextDelta, "mail [EMAIL-1]"),
			edit: RawTextEdit{Path: []string{"delta"}, Before: "mail alex@example.com", After: "mail [EMAIL-1]"},
		},
		"text completion": {
			raw: `{"id":"cmpl-1","object":"text_completion","choices":[{"index":0,"text":"alex@example.com","finish_reason":null}]}`, format: RawResponseFormatOpenAITextCompletionChunk,
			before: textChunk("alex@example.com"), after: textChunk("[EMAIL-1]"),
			edit: RawTextEdit{Path: []string{"choices", "0", "text"}, Before: "alex@example.com", After: "[EMAIL-1]"},
		},
		"misaligned raw": {
			raw: `{"choices":[{"index":0,"delta":{"content":"something else"}}]}`, format: RawResponseFormatOpenAIChatCompletionChunk,
			before: syncChatChunk(Ptr("mail alex@example.com"), nil), after: syncChatChunk(Ptr(""), nil),
			edit: RawTextEdit{Path: syncChatContentPath, Before: "mail alex@example.com", After: ""},
		},
		"undeclared format": {
			raw: syncContentFrame, format: "",
			before: syncChatChunk(Ptr("mail alex@example.com"), nil), after: syncChatChunk(Ptr(""), nil),
			edit: RawTextEdit{Path: syncChatContentPath, Before: "mail alex@example.com", After: ""},
		},
		"anthropic closing event": {
			raw: anthropicMessageDelta, format: RawResponseFormatAnthropicMessagesStreamEvent,
			before: syncChatChunk(nil, Ptr("stop")), after: syncChatChunk(Ptr("released"), Ptr("stop")),
			edit: RawTextEdit{Path: syncChatContentPath, Before: "", After: "released"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			diffed, diffErr := SyncRawResponseText(tc.raw, tc.format, tc.before, tc.after)
			edited, editErr := SyncRawResponseTextEdits(tc.raw, tc.format, tc.after, []RawTextEdit{tc.edit})

			assert.Equal(t, diffed, edited)
			assert.Equal(t, diffErr == nil, editErr == nil, "diff error %v, edits error %v", diffErr, editErr)
			assert.Equal(t, errors.Is(diffErr, ErrRawResponseTextUnsupported), errors.Is(editErr, ErrRawResponseTextUnsupported))
		})
	}
}

// TestSyncRawResponseTextEditsSkipsUnchangedEdits verifies an edit that changes nothing never touches raw, even
// where applying it would move text between frames or the format is undeclared.
func TestSyncRawResponseTextEditsSkipsUnchangedEdits(t *testing.T) {
	spread := `{"choices":[{"index":0,"delta":{"content":"alex@"}}]}` + "\n\n" + `{"choices":[{"index":0,"delta":{"content":"example.com"}}]}`
	unchanged := RawTextEdit{Path: syncChatContentPath, Before: "alex@example.com", After: "alex@example.com"}

	synced, err := SyncRawResponseTextEdits(spread, RawResponseFormatOpenAIChatCompletionChunk, syncChatChunk(nil, nil), []RawTextEdit{unchanged})
	require.NoError(t, err)
	assert.Equal(t, spread, synced)

	synced, err = SyncRawResponseTextEdits(syncContentFrame, "", syncChatChunk(nil, nil), []RawTextEdit{unchanged})
	require.NoError(t, err)
	assert.Equal(t, syncContentFrame, synced)

	_, err = SyncRawResponseTextEdits(syncContentFrame, RawResponseFormatOpenAIChatCompletionChunk, "not a chunk", []RawTextEdit{unchanged})
	require.ErrorIs(t, err, ErrRawResponseTextUnsupported)
}

// TestSyncRawResponseTextReturnsUnchangedRawForAnyFormat verifies a chunk whose text did not change needs no format:
// it returns raw as it was with no error, so callers can tell an unchanged chunk from one raw cannot follow.
func TestSyncRawResponseTextReturnsUnchangedRawForAnyFormat(t *testing.T) {
	chunk := syncChatChunk(Ptr("mail alex@example.com"), nil)

	synced, err := SyncRawResponseText(syncContentFrame, "", chunk, syncChatChunk(Ptr("mail alex@example.com"), nil))

	require.NoError(t, err)
	assert.Equal(t, syncContentFrame, synced)
}

// TestSyncRawResponseTextIgnoresExtraFields verifies a chunk's extra fields, including the raw payload it carries,
// never count as a text edit.
func TestSyncRawResponseTextIgnoresExtraFields(t *testing.T) {
	before := syncChatChunk(Ptr("mail alex@example.com"), nil)
	before.ExtraFields.RawResponse = syncContentFrame
	after := syncChatChunk(Ptr("mail alex@example.com"), nil)
	after.ExtraFields.RawResponse = "rewritten elsewhere"

	synced, err := SyncRawResponseText(syncContentFrame, RawResponseFormatOpenAIChatCompletionChunk, before, after)

	require.NoError(t, err)
	assert.Equal(t, syncContentFrame, synced)
	assert.Equal(t, syncContentFrame, before.ExtraFields.RawResponse, "the caller's payload keeps its extra fields")
}

// TestSplitRawJSONFramesPreservesLayout verifies frames split from a raw payload reassemble byte for byte, a rewrite
// of one frame leaves its neighbours intact, and anything other than whitespace between objects is rejected.
func TestSplitRawJSONFramesPreservesLayout(t *testing.T) {
	raw := "\n " + syncRoleFrame + "\n\n" + syncContentFrame + syncFinishFrame + "\n"
	frames, err := splitRawJSONFrames(raw)
	require.NoError(t, err)
	require.Len(t, frames.frames, 3)
	assert.Equal(t, raw, frames.String())

	require.NoError(t, frames.set(1, "choices.0.delta.content", "a much longer replacement than the original content"))
	assert.Equal(t, "\n "+syncRoleFrame+"\n\n"+strings.Replace(syncContentFrame, "mail alex@example.com", "a much longer replacement than the original content", 1)+syncFinishFrame+"\n", frames.String())

	for name, bad := range map[string]string{
		"junk between frames": syncRoleFrame + " , " + syncContentFrame,
		"trailing junk":       syncRoleFrame + " data:",
		"array frame":         `[1,2]`,
		"unterminated frame":  `{"choices":[`,
		"empty":               "  ",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := splitRawJSONFrames(bad)
			require.ErrorIs(t, err, ErrRawResponseTextUnsupported)
		})
	}
}
