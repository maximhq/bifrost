package schemas

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		"undeclared format":           {raw: syncContentFrame, format: "", before: before, after: after},
		"format for another payload":  {raw: syncContentFrame, format: RawResponseFormatOpenAIResponsesStreamEvent, before: before, after: after},
		"raw is not a string":         {raw: map[string]any{"choices": []any{}}, format: RawResponseFormatOpenAIChatCompletionChunk, before: before, after: after},
		"raw is not JSON":             {raw: "event: ping", format: RawResponseFormatOpenAIChatCompletionChunk, before: before, after: after},
		"raw has no content field":    {raw: `{"choices":[{"index":0,"delta":{"text":"mail alex@example.com"}}]}`, format: RawResponseFormatOpenAIChatCompletionChunk, before: before, after: after},
		"content blocks without text": {raw: `{"choices":[{"index":0,"delta":{"content":[{"type":"thinking","thinking":[]}]}}]}`, format: RawResponseFormatOpenAIChatCompletionChunk, before: syncChatChunk(nil, nil), after: syncChatChunk(Ptr("released"), nil)},
		"format on another payload":   {raw: anthropicTextDelta, format: RawResponseFormatAnthropicMessagesStreamEvent, before: &BifrostTextCompletionResponse{}, after: &BifrostTextCompletionResponse{}},
		"anthropic closing event":     {raw: anthropicMessageDelta, format: RawResponseFormatAnthropicMessagesStreamEvent, before: syncChatChunk(nil, Ptr("stop")), after: syncChatChunk(Ptr("released"), Ptr("stop"))},
		"gemini code execution part":  {raw: `{"candidates":[{"content":{"role":"model","parts":[{"executableCode":{"language":"PYTHON","code":"print(1)"}},{"text":"mail alex@example.com"}]}}]}`, format: RawResponseFormatGeminiStreamChunk, before: before, after: after},
		"gemini function call args":   {raw: `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"f","args":{"to":"alex@example.com"}}}]}}]}`, format: RawResponseFormatGeminiStreamChunk, before: syncToolChunk(`{"to":"alex@example.com"}`), after: syncToolChunk(`{"to":"[EMAIL-1]"}`)},
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
