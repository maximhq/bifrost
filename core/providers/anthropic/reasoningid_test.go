package anthropic

import (
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/providers/openai"
	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// findReasoningMessage returns the first reasoning-typed message in msgs, or
// fails the test if none is present.
func findReasoningMessage(t *testing.T, msgs []schemas.ResponsesMessage) *schemas.ResponsesMessage {
	t.Helper()
	for i := range msgs {
		if msgs[i].Type != nil && *msgs[i].Type == schemas.ResponsesMessageTypeReasoning {
			return &msgs[i]
		}
	}
	t.Fatalf("no reasoning message found among %d converted messages", len(msgs))
	return nil
}

// TestConvertAnthropicContentBlocks_RedactedThinkingRecoversEmbeddedID pins the
// primary crash site (OpenAI targets go through the non-grouped converter): a
// replayed redacted_thinking block whose data carries an embedded reasoning item
// id must recover that exact id, not mint a fresh random one, and must strip the
// marker so the forwarded encrypted_content is byte-identical to the original
// ciphertext OpenAI issued it for.
func TestConvertAnthropicContentBlocks_RedactedThinkingRecoversEmbeddedID(t *testing.T) {
	const originalID = "rs_original"
	const ciphertext = "CIPHERTEXT_BOUND_TO_rs_original"
	embedded := providerUtils.EmbedReasoningItemID(schemas.Ptr(originalID), ciphertext)

	blocks := []AnthropicContentBlock{
		{Type: AnthropicContentBlockTypeRedactedThinking, Data: &embedded},
	}
	roleVal := schemas.ResponsesMessageRoleType(AnthropicMessageRoleAssistant)
	ctx := schemas.NewBifrostContext(nil, time.Time{})

	out := convertAnthropicContentBlocksToResponsesMessages(ctx, blocks, &roleVal, false, "")
	msg := findReasoningMessage(t, out)

	if msg.ID == nil || *msg.ID != originalID {
		t.Errorf("recovered id = %v, want %q", msg.ID, originalID)
	}
	if msg.ResponsesReasoning == nil || msg.ResponsesReasoning.EncryptedContent == nil || *msg.ResponsesReasoning.EncryptedContent != ciphertext {
		t.Errorf("recovered encrypted_content = %v, want %q", msg.ResponsesReasoning, ciphertext)
	}
}

// TestConvertAnthropicContentBlocksGrouped_RedactedThinkingRecoversEmbeddedID is
// the Bedrock-grouped twin of the above: structurally the same bug, fixed for
// symmetry/defense-in-depth even though it isn't the reported trigger.
func TestConvertAnthropicContentBlocksGrouped_RedactedThinkingRecoversEmbeddedID(t *testing.T) {
	const originalID = "rs_original_grouped"
	const ciphertext = "CIPHERTEXT_GROUPED"
	embedded := providerUtils.EmbedReasoningItemID(schemas.Ptr(originalID), ciphertext)

	blocks := []AnthropicContentBlock{
		{Type: AnthropicContentBlockTypeRedactedThinking, Data: &embedded},
	}
	roleVal := schemas.ResponsesMessageRoleType(AnthropicMessageRoleAssistant)

	out := convertAnthropicContentBlocksToResponsesMessagesGrouped(blocks, &roleVal, false)
	msg := findReasoningMessage(t, out)

	if msg.ID == nil || *msg.ID != originalID {
		t.Errorf("recovered id = %v, want %q", msg.ID, originalID)
	}
	if msg.ResponsesReasoning == nil || msg.ResponsesReasoning.EncryptedContent == nil || *msg.ResponsesReasoning.EncryptedContent != ciphertext {
		t.Errorf("recovered encrypted_content = %v, want %q", msg.ResponsesReasoning, ciphertext)
	}
}

// TestConvertAnthropicContentBlocks_ThinkingRecoversEmbeddedID covers the
// visible-summary sibling: a replayed thinking block whose signature carries an
// embedded id must recover that id on the merged reasoning message, and the
// content block's signature must be stripped back to whatever (if anything) was
// there before embedding.
func TestConvertAnthropicContentBlocks_ThinkingRecoversEmbeddedID(t *testing.T) {
	const originalID = "rs_thinking_original"
	text := "Step 1: consider the problem."
	embedded := providerUtils.EmbedReasoningItemID(schemas.Ptr(originalID), "")

	blocks := []AnthropicContentBlock{
		{Type: AnthropicContentBlockTypeThinking, Thinking: &text, Signature: &embedded},
	}
	roleVal := schemas.ResponsesMessageRoleType(AnthropicMessageRoleAssistant)
	ctx := schemas.NewBifrostContext(nil, time.Time{})

	out := convertAnthropicContentBlocksToResponsesMessages(ctx, blocks, &roleVal, false, "")
	msg := findReasoningMessage(t, out)

	if msg.ID == nil || *msg.ID != originalID {
		t.Errorf("recovered id = %v, want %q", msg.ID, originalID)
	}
}

// TestConvertAnthropicContentBlocks_GenuineSignatureFallsBackToRandomID is the
// safety-net case: a plain, unmarked signature/data value -- exactly what a
// genuine Anthropic-native thinking/redacted_thinking block looks like -- must
// keep falling back to a fresh random id, and must never have its bytes altered.
// This must pass both before and after the fix.
func TestConvertAnthropicContentBlocks_GenuineSignatureFallsBackToRandomID(t *testing.T) {
	const genuineData = "EqoBCkYIARgCKkCVn3G8_a_real_anthropic_redacted_thinking_payload"
	blocks := []AnthropicContentBlock{
		{Type: AnthropicContentBlockTypeRedactedThinking, Data: schemas.Ptr(genuineData)},
	}
	roleVal := schemas.ResponsesMessageRoleType(AnthropicMessageRoleAssistant)
	ctx := schemas.NewBifrostContext(nil, time.Time{})

	out := convertAnthropicContentBlocksToResponsesMessages(ctx, blocks, &roleVal, false, "")
	msg := findReasoningMessage(t, out)

	if msg.ID == nil || !hasPrefix(*msg.ID, "rs_") {
		t.Errorf("fallback id = %v, want a fresh rs_-prefixed random id", msg.ID)
	}
	if msg.ResponsesReasoning == nil || msg.ResponsesReasoning.EncryptedContent == nil || *msg.ResponsesReasoning.EncryptedContent != genuineData {
		t.Errorf("genuine data must pass through byte-for-byte unchanged, got %v", msg.ResponsesReasoning)
	}
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// TestConvertBifrostReasoning_BothSummaryAndEncryptedContentEmitBothBlocks pins
// a real OpenAI shape: a reasoning item can carry both a visible summary and
// encrypted_content together. The current if/else-if silently drops the
// encrypted half whenever a summary is present; both must survive as separate
// Anthropic blocks (a thinking block and a redacted_thinking block).
func TestConvertBifrostReasoning_BothSummaryAndEncryptedContentEmitBothBlocks(t *testing.T) {
	const ciphertext = "CIPHERTEXT_ALONGSIDE_SUMMARY"
	msg := &schemas.ResponsesMessage{
		ID:   schemas.Ptr("rs_both_shapes"),
		Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
		ResponsesReasoning: &schemas.ResponsesReasoning{
			Summary: []schemas.ResponsesReasoningSummary{
				{Type: schemas.ResponsesReasoningContentBlockTypeSummaryText, Text: "The user asked a math question."},
			},
			EncryptedContent: schemas.Ptr(ciphertext),
		},
	}

	blocks := convertBifrostReasoningToAnthropicThinking(schemas.NewBifrostContext(nil, schemas.NoDeadline), msg, schemas.OpenAI, "gpt-5")

	var sawThinking, sawRedacted bool
	for _, b := range blocks {
		switch b.Type {
		case AnthropicContentBlockTypeThinking:
			sawThinking = true
		case AnthropicContentBlockTypeRedactedThinking:
			sawRedacted = true
			if b.Data == nil {
				t.Error("redacted_thinking block has nil data")
				continue
			}
			id, payload, ok := providerUtils.ExtractReasoningItemID(*b.Data)
			if !ok {
				t.Errorf("redacted_thinking block data = %q, want an embedded reasoning item id", *b.Data)
				continue
			}
			if id == nil || *id != *msg.ID {
				t.Errorf("embedded id = %v, want %q", id, *msg.ID)
			}
			if payload != ciphertext {
				t.Errorf("redacted_thinking block payload = %q, want %q", payload, ciphertext)
			}
		}
	}
	if !sawThinking {
		t.Error("expected a thinking block for the visible summary, got none")
	}
	if !sawRedacted {
		t.Error("expected a redacted_thinking block for the encrypted_content, got none (the encrypted half was dropped)")
	}
}

// TestBifrostAnthropicToOpenAI_RedactedThinkingReplayPreservesID is the
// end-to-end regression for the reported crash: "The encrypted content for item
// rs_... could not be verified. Reason: Encrypted content item_id did not match
// the target item id." It drives the full round trip with no live network calls:
// an OpenAI-origin reasoning item -> the Anthropic wire block Claude Code
// receives -> the client echoing that block back on a follow-up turn -> the
// outbound OpenAI Responses request Bifrost is about to send. Without the fix,
// the final id is a freshly minted random string while encrypted_content stays
// bound to the original id -- exactly the mismatch OpenAI rejects.
func TestBifrostAnthropicToOpenAI_RedactedThinkingReplayPreservesID(t *testing.T) {
	const originalID = "rs_ORIGINAL123"
	const ciphertext = "CIPHERTEXT_BOUND_TO_rs_ORIGINAL123"

	// 1. OpenAI-origin reasoning item, as it would look fresh off the wire.
	original := &schemas.ResponsesMessage{
		ID:   schemas.Ptr(originalID),
		Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
		ResponsesReasoning: &schemas.ResponsesReasoning{
			Summary:          []schemas.ResponsesReasoningSummary{},
			EncryptedContent: schemas.Ptr(ciphertext),
		},
	}

	// 2. Egress: convert to the Anthropic block Claude Code receives.
	blocks := convertBifrostReasoningToAnthropicThinking(schemas.NewBifrostContext(nil, schemas.NoDeadline), original, schemas.OpenAI, "gpt-5")
	if len(blocks) != 1 || blocks[0].Type != AnthropicContentBlockTypeRedactedThinking {
		t.Fatalf("expected 1 redacted_thinking block, got %+v", blocks)
	}

	// 3. Client echo: the client sends that exact block back on the next turn,
	// routed to an OpenAI-family model (non-grouped / non-Bedrock path).
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	anthropicMessages := []AnthropicMessage{
		{Role: AnthropicMessageRoleAssistant, Content: AnthropicContent{ContentBlocks: blocks}},
	}
	bifrostMessages := ConvertAnthropicMessagesToBifrostMessages(ctx, anthropicMessages, nil, false, false)

	// 4. Convert onward to the actual OpenAI wire request.
	bifrostReq := &schemas.BifrostResponsesRequest{
		Model: "gpt-5.1",
		Input: bifrostMessages,
	}
	openaiReq := openai.ToOpenAIResponsesRequest(ctx, bifrostReq)
	if openaiReq == nil {
		t.Fatal("ToOpenAIResponsesRequest returned nil")
	}

	var found *schemas.ResponsesMessage
	for i := range openaiReq.Input.OpenAIResponsesRequestInputArray {
		m := &openaiReq.Input.OpenAIResponsesRequestInputArray[i]
		if m.Type != nil && *m.Type == schemas.ResponsesMessageTypeReasoning {
			found = m
			break
		}
	}
	if found == nil {
		t.Fatal("no reasoning item in the outbound OpenAI request")
	}
	if found.ID == nil || *found.ID != originalID {
		t.Errorf("outbound reasoning item id = %v, want %q (the id OpenAI actually issued)", found.ID, originalID)
	}
	if found.ResponsesReasoning == nil || found.ResponsesReasoning.EncryptedContent == nil || *found.ResponsesReasoning.EncryptedContent != ciphertext {
		t.Errorf("outbound encrypted_content = %v, want %q", found.ResponsesReasoning, ciphertext)
	}
}

// countReasoningMessages returns the number of reasoning-typed messages in msgs.
func countReasoningMessages(msgs []schemas.ResponsesMessage) int {
	n := 0
	for _, m := range msgs {
		if m.Type != nil && *m.Type == schemas.ResponsesMessageTypeReasoning {
			n++
		}
	}
	return n
}

// TestConvertAnthropicContentBlocks_ThinkingAndRedactedThinkingMergeIntoOneMessage
// pins the paired-block reconstruction on the non-grouped path: an OpenAI
// reasoning item with both a visible summary and encrypted_content is emitted
// on the wire as a thinking block followed by a redacted_thinking block, both
// carrying the same embedded id (egress always emits thinking first). Without
// the fix, reconstruction produces two reasoning messages sharing that id
// instead of one message with both fields.
func TestConvertAnthropicContentBlocks_ThinkingAndRedactedThinkingMergeIntoOneMessage(t *testing.T) {
	const originalID = "rs_paired"
	const summaryText = "Step 1: consider the problem."
	const ciphertext = "CIPHERTEXT_PAIRED_WITH_SUMMARY"
	embeddedSignature := providerUtils.EmbedReasoningItemID(schemas.Ptr(originalID), "")
	embeddedData := providerUtils.EmbedReasoningItemID(schemas.Ptr(originalID), ciphertext)

	blocks := []AnthropicContentBlock{
		{Type: AnthropicContentBlockTypeThinking, Thinking: schemas.Ptr(summaryText), Signature: &embeddedSignature},
		{Type: AnthropicContentBlockTypeRedactedThinking, Data: &embeddedData},
	}
	roleVal := schemas.ResponsesMessageRoleType(AnthropicMessageRoleAssistant)
	ctx := schemas.NewBifrostContext(nil, time.Time{})

	out := convertAnthropicContentBlocksToResponsesMessages(ctx, blocks, &roleVal, false, "")

	if n := countReasoningMessages(out); n != 1 {
		t.Fatalf("expected exactly 1 reasoning message, got %d", n)
	}
	msg := findReasoningMessage(t, out)
	if msg.ID == nil || *msg.ID != originalID {
		t.Errorf("recovered id = %v, want %q", msg.ID, originalID)
	}
	if msg.Content == nil || len(msg.Content.ContentBlocks) != 1 || msg.Content.ContentBlocks[0].Text == nil || *msg.Content.ContentBlocks[0].Text != summaryText {
		t.Errorf("visible summary missing or wrong, got %+v", msg.Content)
	}
	if msg.ResponsesReasoning == nil || msg.ResponsesReasoning.EncryptedContent == nil || *msg.ResponsesReasoning.EncryptedContent != ciphertext {
		t.Errorf("encrypted_content missing or wrong, got %v", msg.ResponsesReasoning)
	}
}

// TestConvertAnthropicContentBlocksGrouped_ThinkingAndRedactedThinkingMergeIntoOneMessage
// is the Bedrock-grouped twin of the above: same paired-block input, same
// one-message-with-both-fields expectation.
func TestConvertAnthropicContentBlocksGrouped_ThinkingAndRedactedThinkingMergeIntoOneMessage(t *testing.T) {
	const originalID = "rs_paired_grouped"
	const summaryText = "Step 1: consider the grouped problem."
	const ciphertext = "CIPHERTEXT_PAIRED_WITH_SUMMARY_GROUPED"
	embeddedSignature := providerUtils.EmbedReasoningItemID(schemas.Ptr(originalID), "")
	embeddedData := providerUtils.EmbedReasoningItemID(schemas.Ptr(originalID), ciphertext)

	blocks := []AnthropicContentBlock{
		{Type: AnthropicContentBlockTypeThinking, Thinking: schemas.Ptr(summaryText), Signature: &embeddedSignature},
		{Type: AnthropicContentBlockTypeRedactedThinking, Data: &embeddedData},
	}
	roleVal := schemas.ResponsesMessageRoleType(AnthropicMessageRoleAssistant)

	out := convertAnthropicContentBlocksToResponsesMessagesGrouped(blocks, &roleVal, false)

	if n := countReasoningMessages(out); n != 1 {
		t.Fatalf("expected exactly 1 reasoning message, got %d", n)
	}
	msg := findReasoningMessage(t, out)
	if msg.ID == nil || *msg.ID != originalID {
		t.Errorf("recovered id = %v, want %q", msg.ID, originalID)
	}
	if msg.Content == nil || len(msg.Content.ContentBlocks) != 1 || msg.Content.ContentBlocks[0].Text == nil || *msg.Content.ContentBlocks[0].Text != summaryText {
		t.Errorf("visible summary missing or wrong, got %+v", msg.Content)
	}
	if msg.ResponsesReasoning == nil || msg.ResponsesReasoning.EncryptedContent == nil || *msg.ResponsesReasoning.EncryptedContent != ciphertext {
		t.Errorf("encrypted_content missing or wrong, got %v", msg.ResponsesReasoning)
	}
}
