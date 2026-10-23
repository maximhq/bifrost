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
		"undeclared format":            {raw: syncContentFrame, format: "", before: before, after: after},
		"format for another payload":   {raw: syncContentFrame, format: RawResponseFormatOpenAIResponsesStreamEvent, before: before, after: after},
		"raw is not a string":          {raw: map[string]any{"choices": []any{}}, format: RawResponseFormatOpenAIChatCompletionChunk, before: before, after: after},
		"raw is not JSON":              {raw: "event: ping", format: RawResponseFormatOpenAIChatCompletionChunk, before: before, after: after},
		"raw content is a block array": {raw: `{"choices":[{"index":0,"delta":{"content":[{"type":"text","text":"mail alex@example.com"}]}}]}`, format: RawResponseFormatOpenAIChatCompletionChunk, before: before, after: after},
		"raw has no content field":     {raw: `{"choices":[{"index":0,"delta":{"text":"mail alex@example.com"}}]}`, format: RawResponseFormatOpenAIChatCompletionChunk, before: before, after: after},
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

	t.Run("raw text where normalized had none", func(t *testing.T) {
		raw := `{"choices":[{"index":0,"delta":{"content":"leftover"},"finish_reason":"stop"}]}`

		synced, err := SyncRawResponseText(raw, RawResponseFormatOpenAIChatCompletionChunk, syncChatChunk(nil, Ptr("stop")), syncChatChunk(Ptr("released"), Ptr("stop")))

		require.Error(t, err)
		assert.Equal(t, raw, synced)
	})
}
