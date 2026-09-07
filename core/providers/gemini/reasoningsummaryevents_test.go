package gemini

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// reasoningSummaryStreamChunks mirrors a Gemini stream with visible thinking: two
// thought parts, then the answer, then a terminal chunk.
func reasoningSummaryStreamChunks() []*GenerateContentResponse {
	return []*GenerateContentResponse{
		{
			ResponseID:   "resp-reasoning",
			ModelVersion: "gemini-2.5-pro",
			Candidates: []*Candidate{{
				Content: &Content{Role: "model", Parts: []*Part{{Text: "First I check the docs.", Thought: true}}},
			}},
		},
		{
			ResponseID:   "resp-reasoning",
			ModelVersion: "gemini-2.5-pro",
			Candidates: []*Candidate{{
				Content: &Content{Role: "model", Parts: []*Part{{Text: "Then I answer.", Thought: true}}},
			}},
		},
		{
			ResponseID:   "resp-reasoning",
			ModelVersion: "gemini-2.5-pro",
			Candidates: []*Candidate{{
				Content: &Content{Role: "model", Parts: []*Part{{Text: "Done."}}},
			}},
		},
		{
			ResponseID:   "resp-reasoning",
			ModelVersion: "gemini-2.5-pro",
			Candidates:   []*Candidate{{FinishReason: FinishReasonStop}},
			UsageMetadata: &GenerateContentResponseUsageMetadata{
				PromptTokenCount:     4,
				CandidatesTokenCount: 6,
				TotalTokenCount:      10,
			},
		},
	}
}

func runGeminiReasoningStream(t *testing.T, chunks []*GenerateContentResponse) []*schemas.BifrostResponsesStreamResponse {
	t.Helper()

	state := &GeminiResponsesStreamState{}
	state.flush()

	var all []*schemas.BifrostResponsesStreamResponse
	seq := 0
	for _, chunk := range chunks {
		events, bifrostErr := chunk.ToBifrostResponsesStream(seq, state)
		require.Nil(t, bifrostErr, "unexpected forward conversion error")
		all = append(all, events...)
		seq += len(events)
	}
	return all
}

func geminiStreamEventsOfType(events []*schemas.BifrostResponsesStreamResponse, eventType schemas.ResponsesStreamResponseType) []*schemas.BifrostResponsesStreamResponse {
	var matched []*schemas.BifrostResponsesStreamResponse
	for _, event := range events {
		if event.Type == eventType {
			matched = append(matched, event)
		}
	}
	return matched
}

// TestGeminiResponsesStreamReasoningSummaryEventFields pins the fields the Responses
// streaming spec marks required and non-nullable on the reasoning_summary_* events.
// They were emitted as bare shells -- no summary_index anywhere, no text on
// reasoning_summary_text.done, no part on the reasoning_summary_part events -- so a
// client reading part.text or indexing the summary array crashed mid-stream.
func TestGeminiResponsesStreamReasoningSummaryEventFields(t *testing.T) {
	events := runGeminiReasoningStream(t, reasoningSummaryStreamChunks())

	thoughts := []string{"First I check the docs.", "Then I answer."}

	partAdded := geminiStreamEventsOfType(events, schemas.ResponsesStreamResponseTypeReasoningSummaryPartAdded)
	require.Len(t, partAdded, len(thoughts))
	for _, event := range partAdded {
		require.NotNil(t, event.SummaryIndex, "reasoning_summary_part.added must carry summary_index")
		assert.Equal(t, 0, *event.SummaryIndex)
		require.NotNil(t, event.Part, "reasoning_summary_part.added must carry part")
		assert.Equal(t, schemas.ResponsesOutputMessageContentTypeSummaryText, event.Part.Type)
		require.NotNil(t, event.Part.Text)
		assert.Equal(t, "", *event.Part.Text, "the part opens empty")
	}

	deltas := geminiStreamEventsOfType(events, schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta)
	require.Len(t, deltas, len(thoughts))
	for i, event := range deltas {
		require.NotNil(t, event.SummaryIndex, "reasoning_summary_text.delta must carry summary_index")
		assert.Equal(t, 0, *event.SummaryIndex)
		require.NotNil(t, event.Delta)
		assert.Equal(t, thoughts[i], *event.Delta)
	}

	textDone := geminiStreamEventsOfType(events, schemas.ResponsesStreamResponseTypeReasoningSummaryTextDone)
	require.Len(t, textDone, len(thoughts))
	for i, event := range textDone {
		require.NotNil(t, event.SummaryIndex, "reasoning_summary_text.done must carry summary_index")
		assert.Equal(t, 0, *event.SummaryIndex)
		require.NotNil(t, event.Text, "reasoning_summary_text.done must carry the full summary text")
		assert.Equal(t, thoughts[i], *event.Text)
	}

	partDone := geminiStreamEventsOfType(events, schemas.ResponsesStreamResponseTypeReasoningSummaryPartDone)
	require.Len(t, partDone, len(thoughts))
	for i, event := range partDone {
		require.NotNil(t, event.SummaryIndex, "reasoning_summary_part.done must carry summary_index")
		assert.Equal(t, 0, *event.SummaryIndex)
		require.NotNil(t, event.Part, "reasoning_summary_part.done must carry part")
		assert.Equal(t, schemas.ResponsesOutputMessageContentTypeSummaryText, event.Part.Type)
		require.NotNil(t, event.Part.Text)
		assert.Equal(t, thoughts[i], *event.Part.Text)
	}

	// Every reasoning event names the item it belongs to, and each thought part is its
	// own item -- summary_index 0 has to resolve against that item's summary array.
	for _, event := range append(append(partAdded, deltas...), append(textDone, partDone...)...) {
		require.NotNil(t, event.ItemID, "reasoning events must carry item_id")
		require.NotNil(t, event.OutputIndex, "reasoning events must carry output_index")
	}
}

// TestGeminiResponsesStreamReasoningItemCarriesSummary pins that the summary block the
// reasoning_summary_* events describe also travels on the reasoning item, so the thought
// text survives into response.completed's output instead of being dropped.
func TestGeminiResponsesStreamReasoningItemCarriesSummary(t *testing.T) {
	events := runGeminiReasoningStream(t, reasoningSummaryStreamChunks())

	var reasoningItems []*schemas.ResponsesMessage
	for _, event := range geminiStreamEventsOfType(events, schemas.ResponsesStreamResponseTypeOutputItemDone) {
		if event.Item != nil && event.Item.Type != nil && *event.Item.Type == schemas.ResponsesMessageTypeReasoning {
			reasoningItems = append(reasoningItems, event.Item)
		}
	}
	require.Len(t, reasoningItems, 2)

	for i, thought := range []string{"First I check the docs.", "Then I answer."} {
		item := reasoningItems[i]
		require.NotNil(t, item.ResponsesReasoning)
		require.Len(t, item.ResponsesReasoning.Summary, 1)
		assert.Equal(t, schemas.ResponsesReasoningContentBlockTypeSummaryText, item.ResponsesReasoning.Summary[0].Type)
		assert.Equal(t, thought, item.ResponsesReasoning.Summary[0].Text)
	}

	var terminal *schemas.BifrostResponsesStreamResponse
	for _, event := range events {
		if event.Type == schemas.ResponsesStreamResponseTypeCompleted {
			terminal = event
		}
	}
	require.NotNil(t, terminal)
	require.NotNil(t, terminal.Response)

	var summarized []string
	for _, msg := range terminal.Response.Output {
		if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeReasoning && msg.ResponsesReasoning != nil {
			for _, summary := range msg.ResponsesReasoning.Summary {
				summarized = append(summarized, summary.Text)
			}
		}
	}
	assert.Equal(t, []string{"First I check the docs.", "Then I answer."}, summarized,
		"reasoning text must reach response.completed's output")
}

// TestGeminiResponsesStreamThoughtSignatureSurvives pins that a thought part's
// thoughtSignature reaches the client. Gemini 3 puts it on the thought part itself and
// requires it back on replay -- there is a dedicated finish reason for its absence -- but
// the streaming converter read only part.Text, so the signature was dropped on the floor
// while the non-streaming converter carried it.
func TestGeminiResponsesStreamThoughtSignatureSurvives(t *testing.T) {
	rawSignature := []byte{0x01, 0x02, 0xff, 0xfe, 0x7f}
	encoded := base64.StdEncoding.EncodeToString(rawSignature)

	events := runGeminiReasoningStream(t, []*GenerateContentResponse{
		{
			ResponseID:   "resp-signed",
			ModelVersion: "gemini-3-pro-preview",
			Candidates: []*Candidate{{
				Content: &Content{Role: "model", Parts: []*Part{{
					Text:             "Weighing the options.",
					Thought:          true,
					ThoughtSignature: rawSignature,
				}}},
			}},
		},
		{
			ResponseID:   "resp-signed",
			ModelVersion: "gemini-3-pro-preview",
			Candidates:   []*Candidate{{FinishReason: FinishReasonStop}},
		},
	})

	deltas := geminiStreamEventsOfType(events, schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta)
	require.Len(t, deltas, 1)
	require.NotNil(t, deltas[0].Signature, "the signature must ride the delta carrying the text it signs")
	assert.Equal(t, encoded, *deltas[0].Signature)

	var reasoningItem *schemas.ResponsesMessage
	for _, event := range geminiStreamEventsOfType(events, schemas.ResponsesStreamResponseTypeOutputItemDone) {
		if event.Item != nil && event.Item.Type != nil && *event.Item.Type == schemas.ResponsesMessageTypeReasoning {
			reasoningItem = event.Item
		}
	}
	require.NotNil(t, reasoningItem)
	require.NotNil(t, reasoningItem.ResponsesReasoning)
	require.NotNil(t, reasoningItem.ResponsesReasoning.EncryptedContent,
		"the completed reasoning item must carry the signature for replay")
	assert.Equal(t, encoded, *reasoningItem.ResponsesReasoning.EncryptedContent)

	// Round trip: the /genai passthrough must reproduce Gemini's own part -- text, thought
	// and signature together -- rather than splitting it or dropping the signature.
	reverseState := NewBifrostToGeminiStreamState()
	native := ToGeminiResponsesStreamResponse(deltas[0], reverseState)
	require.NotNil(t, native)
	require.Len(t, native.Candidates, 1)
	require.Len(t, native.Candidates[0].Content.Parts, 1)
	part := native.Candidates[0].Content.Parts[0]
	assert.Equal(t, "Weighing the options.", part.Text)
	assert.True(t, part.Thought)
	assert.Equal(t, rawSignature, part.ThoughtSignature)

	// Anthropic and Bedrock send the signature on an event of its own, with no text.
	// Keying the conversion on Delta alone dropped that event's replay token entirely.
	signatureOnly := &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta,
		SequenceNumber: 1,
		OutputIndex:    schemas.Ptr(0),
		ItemID:         schemas.Ptr("rs_signature_only"),
		SummaryIndex:   schemas.Ptr(0),
		Signature:      &encoded,
	}
	require.Nil(t, signatureOnly.Delta, "precondition: this event carries no text")

	nativeSigOnly := ToGeminiResponsesStreamResponse(signatureOnly, NewBifrostToGeminiStreamState())
	require.NotNil(t, nativeSigOnly, "a signature-only delta must not be dropped")
	require.Len(t, nativeSigOnly.Candidates, 1)
	require.Len(t, nativeSigOnly.Candidates[0].Content.Parts, 1)
	sigPart := nativeSigOnly.Candidates[0].Content.Parts[0]
	assert.Equal(t, "", sigPart.Text, "the documented signature-only shape keeps an empty text")
	assert.True(t, sigPart.Thought)
	assert.Equal(t, rawSignature, sigPart.ThoughtSignature)

	// An unusable signature stays dropped rather than reaching the client as garbage.
	unusable := &schemas.BifrostResponsesStreamResponse{
		Type:           schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta,
		SequenceNumber: 2,
		OutputIndex:    schemas.Ptr(0),
		ItemID:         schemas.Ptr("rs_unusable"),
		SummaryIndex:   schemas.Ptr(0),
		Signature:      schemas.Ptr("not-base64!!"),
	}
	assert.Nil(t, ToGeminiResponsesStreamResponse(unusable, NewBifrostToGeminiStreamState()),
		"a signature that cannot be decoded produces no part")
}

// The signature-only part the reverse converter now emits for an Anthropic/Bedrock signature
// delta must carry the empty `text` data field #6745 established. Without it the part is a
// metadata-only object, which Google's own clients tolerate but strict adapters reject.
func TestGeminiSignatureOnlyThoughtPartKeepsEmptyText(t *testing.T) {
	encoded := "c2lnbmF0dXJl"
	native := ToGeminiResponsesStreamResponse(&schemas.BifrostResponsesStreamResponse{
		Type:         schemas.ResponsesStreamResponseTypeReasoningSummaryTextDelta,
		OutputIndex:  schemas.Ptr(0),
		ItemID:       schemas.Ptr("rs_1"),
		SummaryIndex: schemas.Ptr(0),
		Signature:    &encoded,
	}, NewBifrostToGeminiStreamState())
	if native == nil {
		t.Fatal("signature-only delta produced no chunk")
	}
	raw, err := json.Marshal(native.Candidates[0].Content.Parts[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("PART: %s", raw)
	if !gjson.GetBytes(raw, "text").Exists() {
		t.Errorf("empty text data field missing — regresses #6745: %s", raw)
	}
	if gjson.GetBytes(raw, "thoughtSignature").String() != encoded {
		t.Errorf("signature not single-encoded: %s", raw)
	}
	if !gjson.GetBytes(raw, "thought").Bool() {
		t.Errorf("thought marker missing: %s", raw)
	}
}
