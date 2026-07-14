package anthropic

import (
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// OpenAI validates encrypted reasoning content against the exact item id it was issued
// with, but Anthropic's redacted_thinking block schema only carries type and data, so the
// id is lost at egress and replay used to mint a fresh random id that OpenAI rejects with
// "Encrypted content item_id did not match the target item id". These tests pin the fix:
// a Bifrost envelope inside data preserves (item_id, payload) across the round trip, is
// decoded only at the final destination-provider conversion, never alters native Anthropic
// payloads, and is dropped rather than forwarded when the destination is Anthropic.
// Codec-level cases live next to the codec in providers/utils/reasoningenvelope_test.go.

// openAIReasoningOutput builds the neutral response Output an OpenAI reasoning model
// produces: an encrypted reasoning item followed by the visible assistant text. The text
// message is required because pending reasoning blocks are only flushed into an adjacent
// assistant message.
func openAIReasoningOutput(items ...schemas.ResponsesMessage) []schemas.ResponsesMessage {
	return append(items, schemas.ResponsesMessage{
		ID:   schemas.Ptr("msg_visible"),
		Type: schemas.Ptr(schemas.ResponsesMessageTypeMessage),
		Role: schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
		Content: &schemas.ResponsesMessageContent{
			ContentBlocks: []schemas.ResponsesMessageContentBlock{{
				Type: schemas.ResponsesOutputMessageContentTypeText,
				Text: schemas.Ptr("final answer"),
			}},
		},
	})
}

func encryptedReasoningItem(itemID, payload string) schemas.ResponsesMessage {
	return schemas.ResponsesMessage{
		ID:   schemas.Ptr(itemID),
		Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
		ResponsesReasoning: &schemas.ResponsesReasoning{
			Summary:          []schemas.ResponsesReasoningSummary{},
			EncryptedContent: schemas.Ptr(payload),
		},
	}
}

func collectRedactedBlocks(blocks []AnthropicContentBlock) []AnthropicContentBlock {
	var redacted []AnthropicContentBlock
	for _, block := range blocks {
		if block.Type == AnthropicContentBlockTypeRedactedThinking {
			redacted = append(redacted, block)
		}
	}
	return redacted
}

// TestRedactedThinkingEnvelope_OpenAIRoundTrip walks the full multi-turn loop: an
// OpenAI-origin encrypted reasoning item is egressed to an Anthropic redacted_thinking
// block carrying the envelope, the client echoes it back, replay ingress restores the
// original item id while keeping the envelope for deferred decoding, and the OpenAI
// final-mile conversion sends the raw ciphertext under the original id.
func TestRedactedThinkingEnvelope_OpenAIRoundTrip(t *testing.T) {
	t.Parallel()

	const itemID = "rs_orig_0123456789abcdef"
	const payload = "OPENAI_ENCRYPTED_X"

	// Egress: neutral OpenAI response to Anthropic-format client response.
	resp := &schemas.BifrostResponsesResponse{
		Output:      openAIReasoningOutput(encryptedReasoningItem(itemID, payload)),
		ExtraFields: schemas.BifrostResponseExtraFields{Provider: schemas.OpenAI},
	}
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	anthropicResp := ToAnthropicResponsesResponse(ctx, resp)

	redacted := collectRedactedBlocks(anthropicResp.Content)
	if len(redacted) != 1 {
		t.Fatalf("want 1 redacted_thinking block on egress, got %d", len(redacted))
	}
	if redacted[0].Data == nil {
		t.Fatal("redacted_thinking block has no data")
	}
	envelope := *redacted[0].Data
	if envelope == payload {
		t.Fatal("OpenAI-origin egress must wrap the payload, got raw ciphertext")
	}
	envProvider, envID, envPayload, ok := providerUtils.UnwrapEncryptedReasoning(envelope)
	if !ok || envProvider != string(schemas.OpenAI) || envID != itemID || envPayload != payload {
		t.Fatalf("envelope = (%q, %q, %q, %v), want (openai, %q, %q, true)", envProvider, envID, envPayload, ok, itemID, payload)
	}

	// Replay: the client echoes the block on the next turn.
	replayReq := &AnthropicMessageRequest{
		Model:     "openai/o3",
		MaxTokens: 1024,
		Messages: []AnthropicMessage{{
			Role: AnthropicMessageRoleAssistant,
			Content: AnthropicContent{ContentBlocks: []AnthropicContentBlock{
				{Type: AnthropicContentBlockTypeRedactedThinking, Data: schemas.Ptr(envelope)},
			}},
		}},
	}
	bifrostReq := replayReq.ToBifrostResponsesRequest(nil)

	var reasoningItem *schemas.ResponsesMessage
	for i := range bifrostReq.Input {
		if bifrostReq.Input[i].Type != nil && *bifrostReq.Input[i].Type == schemas.ResponsesMessageTypeReasoning {
			reasoningItem = &bifrostReq.Input[i]
		}
	}
	if reasoningItem == nil {
		t.Fatalf("no reasoning item in replayed input (%d items)", len(bifrostReq.Input))
	}
	if reasoningItem.ID == nil || *reasoningItem.ID != itemID {
		t.Errorf("replayed item id = %v, want %q restored from the envelope", reasoningItem.ID, itemID)
	}
	if reasoningItem.ResponsesReasoning == nil || reasoningItem.ResponsesReasoning.EncryptedContent == nil ||
		*reasoningItem.ResponsesReasoning.EncryptedContent != envelope {
		t.Error("replayed EncryptedContent must keep the full envelope for deferred decoding")
	}

	// Final mile: the OpenAI provider conversion unwraps to raw ciphertext under the original id.
	openaiReq := openai.ToOpenAIResponsesRequest(nil, bifrostReq)
	var sent *schemas.ResponsesMessage
	for i := range openaiReq.Input.OpenAIResponsesRequestInputArray {
		msg := &openaiReq.Input.OpenAIResponsesRequestInputArray[i]
		if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeReasoning {
			sent = msg
		}
	}
	if sent == nil {
		t.Fatal("no reasoning item in the outgoing OpenAI request")
	}
	if sent.ID == nil || *sent.ID != itemID {
		t.Errorf("outgoing item id = %v, want %q", sent.ID, itemID)
	}
	if sent.ResponsesReasoning == nil || sent.ResponsesReasoning.EncryptedContent == nil ||
		*sent.ResponsesReasoning.EncryptedContent != payload {
		t.Error("outgoing encrypted_content must be the raw payload, not the envelope")
	}
}

// TestRedactedThinkingEnvelope_SummaryAndEncryptedRoundTrip covers an OpenAI reasoning
// item that carries BOTH a visible summary and encrypted state (e.g. summaries enabled
// next to encrypted_content). Egress must emit the summary as thinking blocks AND the
// envelope as a redacted_thinking block, and after replay the OpenAI request must carry
// exactly one encrypted item under the original id.
func TestRedactedThinkingEnvelope_SummaryAndEncryptedRoundTrip(t *testing.T) {
	t.Parallel()

	const itemID = "rs_orig_with_summary"
	const payload = "OPENAI_ENCRYPTED_WITH_SUMMARY"
	const summary = "visible summary text"

	item := encryptedReasoningItem(itemID, payload)
	item.ResponsesReasoning.Summary = []schemas.ResponsesReasoningSummary{{Text: summary}}

	resp := &schemas.BifrostResponsesResponse{
		Output:      openAIReasoningOutput(item),
		ExtraFields: schemas.BifrostResponseExtraFields{Provider: schemas.OpenAI},
	}
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	anthropicResp := ToAnthropicResponsesResponse(ctx, resp)

	// Both block kinds must be present: the summary as thinking, the state as redacted.
	var thinkingSeen bool
	for _, block := range anthropicResp.Content {
		if block.Type == AnthropicContentBlockTypeThinking && block.Thinking != nil && *block.Thinking == summary {
			thinkingSeen = true
		}
	}
	if !thinkingSeen {
		t.Error("summary must still egress as a thinking block")
	}
	redacted := collectRedactedBlocks(anthropicResp.Content)
	if len(redacted) != 1 || redacted[0].Data == nil {
		t.Fatalf("want 1 redacted_thinking block alongside the summary, got %d", len(redacted))
	}
	_, envID, envPayload, ok := providerUtils.UnwrapEncryptedReasoning(*redacted[0].Data)
	if !ok || envID != itemID || envPayload != payload {
		t.Fatalf("envelope = (%q, %q, %v), want (%q, %q, true)", envID, envPayload, ok, itemID, payload)
	}

	// Replay both blocks adjacently, as the client echoes them.
	replayReq := &AnthropicMessageRequest{
		Model:     "openai/o3",
		MaxTokens: 1024,
		Messages: []AnthropicMessage{{
			Role: AnthropicMessageRoleAssistant,
			Content: AnthropicContent{ContentBlocks: []AnthropicContentBlock{
				{Type: AnthropicContentBlockTypeThinking, Thinking: schemas.Ptr(summary)},
				{Type: AnthropicContentBlockTypeRedactedThinking, Data: redacted[0].Data},
			}},
		}},
	}
	bifrostReq := replayReq.ToBifrostResponsesRequest(nil)

	var envelopeItems []*schemas.ResponsesMessage
	for i := range bifrostReq.Input {
		msg := &bifrostReq.Input[i]
		if msg.ResponsesReasoning != nil && msg.ResponsesReasoning.EncryptedContent != nil {
			envelopeItems = append(envelopeItems, msg)
		}
	}
	if len(envelopeItems) != 1 {
		t.Fatalf("want exactly 1 replayed item with encrypted content, got %d", len(envelopeItems))
	}
	if envelopeItems[0].ID == nil || *envelopeItems[0].ID != itemID {
		t.Errorf("replayed encrypted item id = %v, want %q", envelopeItems[0].ID, itemID)
	}

	// Final mile: exactly one encrypted item, raw ciphertext under the original id.
	openaiReq := openai.ToOpenAIResponsesRequest(nil, bifrostReq)
	var encryptedSent []*schemas.ResponsesMessage
	for i := range openaiReq.Input.OpenAIResponsesRequestInputArray {
		msg := &openaiReq.Input.OpenAIResponsesRequestInputArray[i]
		if msg.ResponsesReasoning != nil && msg.ResponsesReasoning.EncryptedContent != nil {
			encryptedSent = append(encryptedSent, msg)
		}
	}
	if len(encryptedSent) != 1 {
		t.Fatalf("want exactly 1 outgoing item with encrypted content, got %d", len(encryptedSent))
	}
	if encryptedSent[0].ID == nil || *encryptedSent[0].ID != itemID {
		t.Errorf("outgoing item id = %v, want %q", encryptedSent[0].ID, itemID)
	}
	if *encryptedSent[0].ResponsesReasoning.EncryptedContent != payload {
		t.Error("outgoing encrypted_content must be the raw payload, not the envelope")
	}
}

// TestRedactedThinkingEnvelope_AnthropicNativePassthrough pins today's behavior for
// native Anthropic payloads: egress data stays byte-identical to the encrypted content
// and replay still mints a random rs_ id while carrying the raw data.
func TestRedactedThinkingEnvelope_AnthropicNativePassthrough(t *testing.T) {
	t.Parallel()

	const payload = "EmwKAhgBEgy3vaFTgeKrzXhpwEr_NATIVE_PAYLOAD"

	// Egress from an Anthropic-origin response must not wrap.
	resp := &schemas.BifrostResponsesResponse{
		Output:      openAIReasoningOutput(encryptedReasoningItem("rs_native_item", payload)),
		ExtraFields: schemas.BifrostResponseExtraFields{Provider: schemas.Anthropic},
	}
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	anthropicResp := ToAnthropicResponsesResponse(ctx, resp)
	redacted := collectRedactedBlocks(anthropicResp.Content)
	if len(redacted) != 1 {
		t.Fatalf("want 1 redacted_thinking block on egress, got %d", len(redacted))
	}
	if redacted[0].Data == nil || *redacted[0].Data != payload {
		t.Errorf("Anthropic-origin egress data = %v, want the raw payload byte-identical", redacted[0].Data)
	}

	// Replay of raw (non-envelope) data keeps today's behavior in both converter shapes.
	block := AnthropicContentBlock{Type: AnthropicContentBlockTypeRedactedThinking, Data: schemas.Ptr(payload)}
	role := schemas.ResponsesInputMessageRoleAssistant
	for name, msgs := range map[string][]schemas.ResponsesMessage{
		"grouped":   convertAnthropicContentBlocksToResponsesMessagesGrouped([]AnthropicContentBlock{block}, &role, true),
		"ungrouped": convertAnthropicContentBlocksToResponsesMessages(ctx, []AnthropicContentBlock{block}, &role, true, ""),
	} {
		if len(msgs) != 1 {
			t.Fatalf("%s: want 1 replayed message, got %d", name, len(msgs))
		}
		if msgs[0].ID == nil || !strings.HasPrefix(*msgs[0].ID, "rs_") || len(*msgs[0].ID) <= len("rs_") {
			t.Errorf("%s: replayed id = %v, want a freshly minted rs_ id", name, msgs[0].ID)
		}
		if msgs[0].ResponsesReasoning == nil || msgs[0].ResponsesReasoning.EncryptedContent == nil ||
			*msgs[0].ResponsesReasoning.EncryptedContent != payload {
			t.Errorf("%s: replayed EncryptedContent must be the raw data unchanged", name)
		}
	}
}

// TestRedactedThinkingEnvelope_MultipleBlocksPreservePairing egresses two OpenAI
// encrypted reasoning items and replays them, asserting each block keeps its own
// (item_id, payload) pair in order.
func TestRedactedThinkingEnvelope_MultipleBlocksPreservePairing(t *testing.T) {
	t.Parallel()

	pairs := []struct{ itemID, payload string }{
		{"rs_first_item", "PAYLOAD_ONE"},
		{"rs_second_item", "PAYLOAD_TWO"},
	}
	resp := &schemas.BifrostResponsesResponse{
		Output: openAIReasoningOutput(
			encryptedReasoningItem(pairs[0].itemID, pairs[0].payload),
			encryptedReasoningItem(pairs[1].itemID, pairs[1].payload),
		),
		ExtraFields: schemas.BifrostResponseExtraFields{Provider: schemas.OpenAI},
	}
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	anthropicResp := ToAnthropicResponsesResponse(ctx, resp)

	redacted := collectRedactedBlocks(anthropicResp.Content)
	if len(redacted) != 2 {
		t.Fatalf("want 2 redacted_thinking blocks, got %d", len(redacted))
	}
	for i, pair := range pairs {
		if redacted[i].Data == nil {
			t.Fatalf("block %d has no data", i)
		}
		_, envID, envPayload, ok := providerUtils.UnwrapEncryptedReasoning(*redacted[i].Data)
		if !ok || envID != pair.itemID || envPayload != pair.payload {
			t.Errorf("block %d envelope = (%q, %q, %v), want (%q, %q, true)", i, envID, envPayload, ok, pair.itemID, pair.payload)
		}
	}

	// Replay both blocks in one assistant message through both converter shapes.
	blocks := []AnthropicContentBlock{
		{Type: AnthropicContentBlockTypeRedactedThinking, Data: redacted[0].Data},
		{Type: AnthropicContentBlockTypeRedactedThinking, Data: redacted[1].Data},
	}
	role := schemas.ResponsesInputMessageRoleAssistant
	for name, msgs := range map[string][]schemas.ResponsesMessage{
		"grouped":   convertAnthropicContentBlocksToResponsesMessagesGrouped(blocks, &role, true),
		"ungrouped": convertAnthropicContentBlocksToResponsesMessages(ctx, blocks, &role, true, ""),
	} {
		if len(msgs) != 2 {
			t.Fatalf("%s: want 2 replayed reasoning items, got %d", name, len(msgs))
		}
		for i, pair := range pairs {
			if msgs[i].ID == nil || *msgs[i].ID != pair.itemID {
				t.Errorf("%s: item %d id = %v, want %q", name, i, msgs[i].ID, pair.itemID)
			}
			_, _, envPayload, ok := providerUtils.UnwrapEncryptedReasoning(*msgs[i].ResponsesReasoning.EncryptedContent)
			if !ok || envPayload != pair.payload {
				t.Errorf("%s: item %d payload = (%q, %v), want (%q, true)", name, i, envPayload, ok, pair.payload)
			}
		}
	}
}

// TestToAnthropicResponsesRequest_DropsEnvelopeRedactedBlocks pins the fallback safety:
// an Anthropic-bound request built from history that contains an OpenAI envelope must not
// forward the foreign ciphertext to Claude. The block is dropped, sibling tool_use blocks
// survive.
func TestToAnthropicResponsesRequest_DropsEnvelopeRedactedBlocks(t *testing.T) {
	t.Parallel()

	envelope := providerUtils.WrapEncryptedReasoning(string(schemas.OpenAI), "rs_orig_drop", "OPENAI_ONLY_X")
	item := schemas.ResponsesMessage{
		ID:   schemas.Ptr("rs_orig_drop"),
		Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
		ResponsesReasoning: &schemas.ResponsesReasoning{
			Summary:          []schemas.ResponsesReasoningSummary{},
			EncryptedContent: schemas.Ptr(envelope),
		},
	}

	req := &schemas.BifrostResponsesRequest{
		Model: "claude-sonnet-4-5-20250929",
		Input: []schemas.ResponsesMessage{item, functionCallItem()},
	}
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	out, err := ToAnthropicResponsesRequest(ctx, req)
	if err != nil {
		t.Fatalf("ToAnthropicResponsesRequest: %v", err)
	}

	toolUseSeen := false
	for _, msg := range out.Messages {
		for _, block := range msg.Content.ContentBlocks {
			switch block.Type {
			case AnthropicContentBlockTypeRedactedThinking:
				t.Errorf("envelope redacted block must be dropped for an Anthropic destination, got data %v", block.Data)
			case AnthropicContentBlockTypeToolUse:
				toolUseSeen = true
			}
		}
	}
	if !toolUseSeen {
		t.Error("sibling tool_use block must survive the drop")
	}
}

// TestToAnthropicResponsesStreamResponse_EnvelopeEgress pins the streaming egress: the
// output_item.added handler wraps OpenAI-origin encrypted reasoning in the envelope and
// leaves other providers' payloads raw.
func TestToAnthropicResponsesStreamResponse_EnvelopeEgress(t *testing.T) {
	t.Parallel()

	const itemID = "rs_stream_item"
	const payload = "STREAM_ENCRYPTED_X"

	tests := map[string]struct {
		provider     schemas.ModelProvider
		wantEnvelope bool
	}{
		"openai origin wraps": {provider: schemas.OpenAI, wantEnvelope: true},
		"native origin raw":   {provider: schemas.Anthropic, wantEnvelope: false},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			item := encryptedReasoningItem(itemID, payload)
			streamResp := &schemas.BifrostResponsesStreamResponse{
				Type:        schemas.ResponsesStreamResponseTypeOutputItemAdded,
				OutputIndex: schemas.Ptr(0),
				Item:        &item,
				ExtraFields: schemas.BifrostResponseExtraFields{Provider: tt.provider},
			}
			ctx := schemas.NewBifrostContext(nil, time.Time{})
			frames := ToAnthropicResponsesStreamResponse(ctx, streamResp)

			var data *string
			for _, frame := range frames {
				if frame == nil || frame.ContentBlock == nil {
					continue
				}
				if frame.Type == AnthropicStreamEventTypeContentBlockStart &&
					frame.ContentBlock.Type == AnthropicContentBlockTypeRedactedThinking {
					data = frame.ContentBlock.Data
				}
			}
			if data == nil {
				t.Fatalf("no redacted_thinking content_block_start emitted (%d frames)", len(frames))
			}
			if tt.wantEnvelope {
				_, envID, envPayload, ok := providerUtils.UnwrapEncryptedReasoning(*data)
				if !ok || envID != itemID || envPayload != payload {
					t.Errorf("stream envelope = (%q, %q, %v), want (%q, %q, true)", envID, envPayload, ok, itemID, payload)
				}
			} else if *data != payload {
				t.Errorf("native stream data = %q, want raw payload %q", *data, payload)
			}
		})
	}
}
