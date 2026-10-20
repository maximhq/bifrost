package anthropic

import (
	"fmt"
	"reflect"
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

// A bare Claude id converts before governance picks a provider (issue #7768), so the
// ungrouped path must keep each thinking run at its wire position, and must still
// merge the run itself into one item.
func TestToBifrostResponsesRequestBareClaudeKeepsThinkingOrder(t *testing.T) {
	sig := func(s string) *string { return &s }
	th := func(text, s string) AnthropicContentBlock {
		return AnthropicContentBlock{Type: AnthropicContentBlockTypeThinking, Thinking: &text, Signature: sig(s)}
	}
	txt := func(text string) AnthropicContentBlock {
		return AnthropicContentBlock{Type: AnthropicContentBlockTypeText, Text: &text}
	}
	tool := func(id string) AnthropicContentBlock {
		return AnthropicContentBlock{Type: AnthropicContentBlockTypeToolUse, ID: &id, Name: sig("t"), Input: []byte(`{}`)}
	}
	req := &AnthropicMessageRequest{
		Model: "claude-opus-5-5",
		Messages: []AnthropicMessage{{
			Role: AnthropicMessageRoleAssistant,
			Content: AnthropicContent{ContentBlocks: []AnthropicContentBlock{
				th("a", "sig0"), th("a2", "sig0b"), txt("x"), tool("t0"), th("b", "sig1"), txt("y"), tool("t1"),
			}},
		}},
	}
	out := req.ToBifrostResponsesRequest(&schemas.BifrostContext{})
	var got []string
	for _, m := range out.Input {
		switch {
		case m.Type != nil && *m.Type == schemas.ResponsesMessageTypeReasoning:
			got = append(got, "reasoning:"+fmt.Sprint(len(m.Content.ContentBlocks)))
		case m.Type != nil && *m.Type == schemas.ResponsesMessageTypeFunctionCall:
			got = append(got, "call:"+*m.ResponsesToolMessage.CallID)
		default:
			got = append(got, "message")
		}
	}
	want := []string{"reasoning:2", "message", "call:t0", "reasoning:1", "message", "call:t1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("thinking order not preserved:\n got  %v\n want %v", got, want)
	}
}

// interleavedThinkingTurn builds an assistant turn that mixes signed thinking, redacted
// thinking, text and tool calls, in an order a hoisting converter would visibly change.
func interleavedThinkingTurn() []AnthropicContentBlock {
	str := func(s string) *string { return &s }
	th := func(text, sig string) AnthropicContentBlock {
		return AnthropicContentBlock{Type: AnthropicContentBlockTypeThinking, Thinking: str(text), Signature: str(sig)}
	}
	red := func(data string) AnthropicContentBlock {
		return AnthropicContentBlock{Type: AnthropicContentBlockTypeRedactedThinking, Data: str(data)}
	}
	txt := func(text string) AnthropicContentBlock {
		return AnthropicContentBlock{Type: AnthropicContentBlockTypeText, Text: str(text)}
	}
	tool := func(id string) AnthropicContentBlock {
		return AnthropicContentBlock{Type: AnthropicContentBlockTypeToolUse, ID: str(id), Name: str("get_weather"), Input: []byte(`{}`)}
	}
	return []AnthropicContentBlock{
		red("redacted-0"), txt("x0"), tool("toolu_0"),
		th("think-1", "sig-1"), red("redacted-1"), txt("x1"), tool("toolu_1"),
		th("think-2", "sig-2"), tool("toolu_2"),
	}
}

func interleavedThinkingMessages() []AnthropicMessage {
	str := func(s string) *string { return &s }
	result := func(id string) AnthropicContentBlock {
		return AnthropicContentBlock{Type: AnthropicContentBlockTypeToolResult, ToolUseID: str(id), Content: &AnthropicContent{ContentStr: str("ok")}}
	}
	return []AnthropicMessage{
		{Role: AnthropicMessageRoleUser, Content: AnthropicContent{ContentStr: str("go")}},
		{Role: AnthropicMessageRoleAssistant, Content: AnthropicContent{ContentBlocks: interleavedThinkingTurn()}},
		{Role: AnthropicMessageRoleUser, Content: AnthropicContent{ContentBlocks: []AnthropicContentBlock{result("toolu_0"), result("toolu_1"), result("toolu_2")}}},
	}
}

// blockLabels names a block by kind and its identity (signature, data or id), so a
// reordered or rewritten signed block shows up in a failure diff.
func blockLabels(blocks []AnthropicContentBlock) []string {
	var out []string
	for _, b := range blocks {
		switch b.Type {
		case AnthropicContentBlockTypeThinking:
			out = append(out, "thinking:"+*b.Signature)
		case AnthropicContentBlockTypeRedactedThinking:
			out = append(out, "redacted:"+*b.Data)
		case AnthropicContentBlockTypeText:
			out = append(out, "text:"+*b.Text)
		case AnthropicContentBlockTypeToolUse:
			out = append(out, "tool:"+*b.ID)
		}
	}
	return out
}

// Redacted thinking is converted by its own branch of the order-preserving path, and a
// thinking block followed directly by a redacted one must stay two items in wire order
// rather than merging (issue #7768).
func TestToBifrostResponsesRequestBareClaudeKeepsRedactedThinkingOrder(t *testing.T) {
	req := &AnthropicMessageRequest{Model: "claude-opus-5-5", Messages: interleavedThinkingMessages()}
	out := req.ToBifrostResponsesRequest(&schemas.BifrostContext{})
	var got []string
	for _, m := range out.Input {
		if m.Type == nil {
			continue
		}
		switch *m.Type {
		case schemas.ResponsesMessageTypeReasoning:
			if m.ResponsesReasoning != nil && m.ResponsesReasoning.EncryptedContent != nil {
				got = append(got, "redacted:"+*m.ResponsesReasoning.EncryptedContent)
			} else {
				got = append(got, "thinking:"+*m.Content.ContentBlocks[0].Signature)
			}
		case schemas.ResponsesMessageTypeFunctionCall:
			got = append(got, "tool:"+*m.ResponsesToolMessage.CallID)
		case schemas.ResponsesMessageTypeMessage:
			// assistant text only; the user turns carry no assistant content
			if m.Role != nil && *m.Role == schemas.ResponsesInputMessageRoleAssistant {
				got = append(got, "text")
			}
		}
	}
	want := []string{
		"redacted:redacted-0", "text", "tool:toolu_0",
		"thinking:sig-1", "redacted:redacted-1", "text", "tool:toolu_1",
		"thinking:sig-2", "tool:toolu_2",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("redacted/thinking order not preserved:\n got  %v\n want %v", got, want)
	}
}

// A bare Claude id can be served by Anthropic, Vertex or Azure as well as Bedrock, and all
// three rebuild the Anthropic message from the Bifrost items. The assistant turn must leave
// them exactly as the client sent it -- thinking, redacted thinking, text and tool calls in
// the same order and with the same signed payloads (issue #7768).
func TestBareClaudeInterleavedThinkingRoundTripsAcrossAnthropicFormatProviders(t *testing.T) {
	want := blockLabels(interleavedThinkingTurn())
	for _, prov := range []schemas.ModelProvider{schemas.Anthropic, schemas.Vertex, schemas.Azure} {
		t.Run(string(prov), func(t *testing.T) {
			ctx := &schemas.BifrostContext{}
			in := (&AnthropicMessageRequest{Model: "claude-opus-5-5", Messages: interleavedThinkingMessages()}).ToBifrostResponsesRequest(ctx)
			in.Provider = prov
			out, err := ToAnthropicResponsesRequest(ctx, in)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, m := range out.Messages {
				if m.Role == AnthropicMessageRoleAssistant {
					got = append(got, blockLabels(m.Content.ContentBlocks)...)
				}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("assistant turn changed on the way to %s:\n got  %v\n want %v", prov, got, want)
			}
		})
	}
}

// assistantTurnAfterEgress sends one assistant turn through ingress conversion and back out
// through the Anthropic-format egress, and returns the turn's block labels.
func assistantTurnAfterEgress(t *testing.T, model string, stream bool, provider schemas.ModelProvider, blocks []AnthropicContentBlock) []string {
	t.Helper()
	str := func(s string) *string { return &s }
	var results []AnthropicContentBlock
	for _, b := range blocks {
		if b.Type == AnthropicContentBlockTypeToolUse {
			results = append(results, AnthropicContentBlock{Type: AnthropicContentBlockTypeToolResult, ToolUseID: b.ID, Content: &AnthropicContent{ContentStr: str("ok")}})
		}
	}
	req := &AnthropicMessageRequest{Model: model, Messages: []AnthropicMessage{
		{Role: AnthropicMessageRoleUser, Content: AnthropicContent{ContentStr: str("go")}},
		{Role: AnthropicMessageRoleAssistant, Content: AnthropicContent{ContentBlocks: blocks}},
		{Role: AnthropicMessageRoleUser, Content: AnthropicContent{ContentBlocks: results}},
	}}
	if stream {
		req.Stream = schemas.Ptr(true)
	}
	ctx := &schemas.BifrostContext{}
	in := req.ToBifrostResponsesRequest(ctx)
	// governance may pick a different provider than the model string named
	in.Provider = provider
	out, err := ToAnthropicResponsesRequest(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range out.Messages {
		if m.Role == AnthropicMessageRoleAssistant {
			got = append(got, blockLabels(m.Content.ContentBlocks)...)
		}
	}
	return got
}

// A replayed assistant turn must leave the Anthropic-format egress exactly as the client sent
// it, whichever shape it has, whichever provider serves it (Bedrock InvokeModel uses this same
// builder), whichever way the model id names it, and streaming or not (issue #7768). The first
// two shapes are the long-standing "reasoning first" inputs and pin that they are unchanged.
func TestClaudeAssistantTurnKeepsOrderThroughAnthropicFormatEgress(t *testing.T) {
	str := func(s string) *string { return &s }
	th := func(i int) AnthropicContentBlock {
		return AnthropicContentBlock{Type: AnthropicContentBlockTypeThinking, Thinking: str("t"), Signature: str(fmt.Sprint("sig-", i))}
	}
	rd := func(i int) AnthropicContentBlock {
		return AnthropicContentBlock{Type: AnthropicContentBlockTypeRedactedThinking, Data: str(fmt.Sprint("red-", i))}
	}
	tx := func(i int) AnthropicContentBlock {
		return AnthropicContentBlock{Type: AnthropicContentBlockTypeText, Text: str(fmt.Sprint("x", i))}
	}
	tu := func(i int) AnthropicContentBlock {
		return AnthropicContentBlock{Type: AnthropicContentBlockTypeToolUse, ID: str(fmt.Sprint("toolu_", i)), Name: str("get_weather"), Input: []byte(`{}`)}
	}
	shapes := []struct {
		name   string
		blocks []AnthropicContentBlock
	}{
		{"reasoning first, parallel tool calls", []AnthropicContentBlock{th(0), tu(0), tu(1)}},
		{"reasoning first, text, tool call", []AnthropicContentBlock{th(0), tx(0), tu(0)}},
		{"thinking, text, tool, thinking, text, tool", []AnthropicContentBlock{th(0), tx(0), tu(0), th(1), tx(1), tu(1)}},
		{"thinking, tool, thinking, tool", []AnthropicContentBlock{th(0), tu(0), th(1), tu(1)}},
		{"tool, thinking, tool", []AnthropicContentBlock{tu(0), th(1), tu(1)}},
		{"text, thinking, tool", []AnthropicContentBlock{tx(0), th(1), tu(1)}},
		{"redacted, text, tool, redacted, text, tool", []AnthropicContentBlock{rd(0), tx(0), tu(0), rd(1), tx(1), tu(1)}},
		{"thinking, redacted, tool, thinking, redacted, tool", []AnthropicContentBlock{th(0), rd(0), tu(0), th(1), rd(1), tu(1)}},
	}
	models := []string{"claude-opus-5-5", "anthropic/claude-opus-5-5", "vertex/claude-opus-5-5", "azure/claude-opus-5-5", "bedrock/us.anthropic.claude-opus-5-5"}
	providers := []schemas.ModelProvider{schemas.Anthropic, schemas.Vertex, schemas.Azure, schemas.Bedrock}
	for _, shape := range shapes {
		want := blockLabels(shape.blocks)
		for _, model := range models {
			for _, prov := range providers {
				for _, stream := range []bool{false, true} {
					got := assistantTurnAfterEgress(t, model, stream, prov, shape.blocks)
					if !reflect.DeepEqual(got, want) {
						t.Errorf("%s | model=%s provider=%s stream=%v:\n got  %v\n want %v", shape.name, model, prov, stream, got, want)
					}
				}
			}
		}
	}
}

// The order-preserving path emits one reasoning item per thinking run. A thinking block and the
// redacted_thinking block that carries the same embedded OpenAI item id still belong to ONE
// item, and the id of one run must never leak into the next (issue #7768).
func TestOrderedConversionKeepsEmbeddedReasoningIDsPerRun(t *testing.T) {
	str := func(s string) *string { return &s }
	embed := func(id, payload string) *string {
		return str(providerUtils.EmbedReasoningItemID(&id, payload))
	}
	blocks := []AnthropicContentBlock{
		{Type: AnthropicContentBlockTypeThinking, Thinking: str("run one"), Signature: embed("rs_one", "sig-one")},
		{Type: AnthropicContentBlockTypeRedactedThinking, Data: embed("rs_one", "cipher-one")},
		{Type: AnthropicContentBlockTypeText, Text: str("between")},
		{Type: AnthropicContentBlockTypeThinking, Thinking: str("run two"), Signature: str("sig-two-no-id")},
		{Type: AnthropicContentBlockTypeRedactedThinking, Data: embed("rs_other", "cipher-other")},
		{Type: AnthropicContentBlockTypeToolUse, ID: str("toolu_1"), Name: str("t"), Input: []byte(`{}`)},
	}
	role := schemas.ResponsesMessageRoleType(AnthropicMessageRoleAssistant)
	ctx := schemas.NewBifrostContext(nil, time.Time{})
	out := convertAnthropicContentBlocksToResponsesMessagesOrdered(ctx, blocks, &role, false, "", true)

	var reasoning []schemas.ResponsesMessage
	var order []string
	for _, m := range out {
		switch {
		case m.Type != nil && *m.Type == schemas.ResponsesMessageTypeReasoning:
			reasoning = append(reasoning, m)
			order = append(order, "reasoning")
		case m.Type != nil && *m.Type == schemas.ResponsesMessageTypeFunctionCall:
			order = append(order, "call")
		default:
			order = append(order, "message")
		}
	}
	// run one (thinking + same-id redacted) -> 1 item; text; run two thinking -> 1 item;
	// a redacted block with a DIFFERENT id -> its own item; then the call
	if want := []string{"reasoning", "message", "reasoning", "reasoning", "call"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("item order = %v, want %v", order, want)
	}
	one := reasoning[0]
	if one.ID == nil || *one.ID != "rs_one" {
		t.Fatalf("run one id = %v, want rs_one", one.ID)
	}
	if one.Content == nil || len(one.Content.ContentBlocks) != 1 || *one.Content.ContentBlocks[0].Signature != "sig-one" {
		t.Fatalf("run one thinking block lost or rewritten: %+v", one.Content)
	}
	if one.ResponsesReasoning == nil || one.ResponsesReasoning.EncryptedContent == nil || *one.ResponsesReasoning.EncryptedContent != "cipher-one" {
		t.Fatalf("same-id redacted block was not folded into run one: %+v", one.ResponsesReasoning)
	}
	two := reasoning[1]
	if two.ID == nil || *two.ID == "rs_one" || *two.ID == "rs_other" {
		t.Fatalf("run two reused another run's id: %v", two.ID)
	}
	if two.ResponsesReasoning == nil || two.ResponsesReasoning.EncryptedContent != nil {
		t.Fatalf("run one's ciphertext leaked into run two: %+v", two.ResponsesReasoning)
	}
	other := reasoning[2]
	if other.ID == nil || *other.ID != "rs_other" || *other.ResponsesReasoning.EncryptedContent != "cipher-other" {
		t.Fatalf("different-id redacted block not kept as its own item: %+v", other)
	}
}
