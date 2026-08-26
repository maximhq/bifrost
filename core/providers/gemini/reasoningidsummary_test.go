package gemini

import (
	"strings"
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

// Regression test for issue #6329: the non-streaming Gemini -> Responses conversion
// used to emit reasoning output items without `id` and (when no thought signature was
// present) without `summary`. Strict OpenAI Responses clients (e.g. the Vercel AI SDK)
// reject the whole output array on that shape:
//
//	output[0].id: expected string, received undefined
//	output[0].summary: expected array, received undefined
//
// which also discards a perfectly valid function_call that follows the reasoning item.
func TestReasoningItemIDAndSummary(t *testing.T) {
	buildResp := func(parts []*Part) *GenerateContentResponse {
		return &GenerateContentResponse{
			ModelVersion: "gemini-2.5-pro",
			Candidates: []*Candidate{
				{
					FinishReason: FinishReasonStop,
					Content: &Content{
						Role:  "model",
						Parts: parts,
					},
				},
			},
		}
	}

	assertValidReasoningItem := func(t *testing.T, reasoning schemas.ResponsesMessage) {
		t.Helper()
		if reasoning.Type == nil || *reasoning.Type != schemas.ResponsesMessageTypeReasoning {
			t.Fatalf("expected a reasoning item, got type %+v", reasoning.Type)
		}
		if reasoning.ID == nil || strings.TrimSpace(*reasoning.ID) == "" {
			t.Errorf("reasoning item has no id: OpenAI Responses clients require output[].id to be a string")
		} else if !strings.HasPrefix(*reasoning.ID, "rs_") {
			t.Errorf("reasoning item id %q does not use the rs_ prefix used across providers", *reasoning.ID)
		}
		// Bifrost serializes `summary` via the embedded *ResponsesReasoning, so a nil
		// ResponsesReasoning drops the key from the JSON entirely and a nil Summary
		// serializes as null instead of an array.
		if reasoning.ResponsesReasoning == nil {
			t.Errorf("reasoning item has nil ResponsesReasoning: `summary` will be absent from serialized JSON")
		} else if reasoning.ResponsesReasoning.Summary == nil {
			t.Errorf("reasoning item has nil Summary: `summary` serializes as null, not an array")
		}
	}

	t.Run("thought without signature", func(t *testing.T) {
		resp := buildResp([]*Part{
			{Thought: true, Text: "Thinking about which tool to call."},
			{FunctionCall: &FunctionCall{Name: "get_weather", Args: []byte(`{"location":"Paris"}`)}},
		}).ToResponsesBifrostResponsesResponse()
		if resp == nil || len(resp.Output) < 2 {
			t.Fatalf("expected reasoning + function_call output items, got %+v", resp)
		}

		reasoning := resp.Output[0]
		assertValidReasoningItem(t, reasoning)
		if reasoning.ResponsesReasoning != nil && reasoning.ResponsesReasoning.EncryptedContent != nil {
			t.Errorf("signature-less thought must not carry encrypted_content, got %q", *reasoning.ResponsesReasoning.EncryptedContent)
		}

		// The function call following the reasoning item stays consumable.
		fc := resp.Output[1]
		if fc.Type == nil || *fc.Type != schemas.ResponsesMessageTypeFunctionCall {
			t.Fatalf("output[1] is not a function_call item: %+v", fc.Type)
		}
	})

	t.Run("thought with signature", func(t *testing.T) {
		resp := buildResp([]*Part{
			{Thought: true, Text: "Thinking.", ThoughtSignature: []byte("opaque-signature-bytes")},
		}).ToResponsesBifrostResponsesResponse()
		if resp == nil || len(resp.Output) < 1 {
			t.Fatalf("expected a reasoning output item, got %+v", resp)
		}

		reasoning := resp.Output[0]
		assertValidReasoningItem(t, reasoning)
		if reasoning.ResponsesReasoning == nil || reasoning.ResponsesReasoning.EncryptedContent == nil {
			t.Errorf("thought signature must be preserved as encrypted_content")
		}
	})

	t.Run("standalone thought signature part", func(t *testing.T) {
		resp := buildResp([]*Part{
			{ThoughtSignature: []byte("opaque-signature-bytes")},
		}).ToResponsesBifrostResponsesResponse()
		if resp == nil || len(resp.Output) < 1 {
			t.Fatalf("expected a reasoning output item, got %+v", resp)
		}
		assertValidReasoningItem(t, resp.Output[0])
	})
}

// Reasoning items built by the Gemini converter carry their text as reasoning content
// blocks (summary stays an empty array for OpenAI-compat clients). Replay back to
// Gemini must read those blocks — the thinking guide requires thought blocks to be
// resent unmodified, so text and signature both have to survive the round trip.
func TestReasoningItemRoundTripToGeminiContents(t *testing.T) {
	roundTrip := func(t *testing.T, parts []*Part) []Content {
		t.Helper()
		resp := (&GenerateContentResponse{
			ModelVersion: "gemini-2.5-pro",
			Candidates: []*Candidate{
				{
					FinishReason: FinishReasonStop,
					Content:      &Content{Role: "model", Parts: parts},
				},
			},
		}).ToResponsesBifrostResponsesResponse()
		if resp == nil {
			t.Fatal("nil bifrost response")
		}
		contents, _, err := convertResponsesMessagesToGeminiContents(resp.Output, "gemini-2.5-pro", schemas.Gemini)
		if err != nil {
			t.Fatalf("convert back to gemini contents: %v", err)
		}
		return contents
	}

	collectThoughts := func(contents []Content) (texts []string, signatures int) {
		for _, c := range contents {
			for _, p := range c.Parts {
				if p.Thought && p.Text != "" {
					texts = append(texts, p.Text)
				}
				if len(p.ThoughtSignature) > 0 {
					signatures++
				}
			}
		}
		return texts, signatures
	}

	t.Run("unsigned thought text survives replay", func(t *testing.T) {
		contents := roundTrip(t, []*Part{
			{Thought: true, Text: "Reasoning about the answer."},
			{Text: "The answer is 42."},
		})
		texts, _ := collectThoughts(contents)
		if len(texts) != 1 || texts[0] != "Reasoning about the answer." {
			t.Errorf("unsigned thought text lost on replay, got thought texts %q", texts)
		}
	})

	t.Run("signed thought keeps text and signature", func(t *testing.T) {
		contents := roundTrip(t, []*Part{
			{Thought: true, Text: "Signed reasoning.", ThoughtSignature: []byte("opaque-signature-bytes")},
			{Text: "Done."},
		})
		texts, signatures := collectThoughts(contents)
		if len(texts) != 1 || texts[0] != "Signed reasoning." {
			t.Errorf("signed thought text lost on replay, got thought texts %q", texts)
		}
		if signatures != 1 {
			t.Errorf("expected the thought signature exactly once on replay, got %d", signatures)
		}
	})
}

// ToGeminiResponsesResponse is the other Responses -> Gemini reader. Its generic
// content-block path already emits reasoning content blocks as thought parts, so its
// reasoning branch must only fall back to the Summary array — an item carrying the
// same text in both places (content blocks from the Gemini converter, summary from an
// OpenAI-ingress mirror) must produce exactly one thought part, and a summary-only
// item must still produce it.
func TestReasoningItemNoDuplicateThoughtInGeminiResponse(t *testing.T) {
	collectThoughtTexts := func(resp *GenerateContentResponse) []string {
		var texts []string
		if resp == nil {
			return texts
		}
		for _, cand := range resp.Candidates {
			if cand == nil || cand.Content == nil {
				continue
			}
			for _, p := range cand.Content.Parts {
				if p.Thought && p.Text != "" {
					texts = append(texts, p.Text)
				}
			}
		}
		return texts
	}

	reasoningItem := func(withBlock, withSummary bool) schemas.ResponsesMessage {
		txt := "Reasoning here."
		msg := schemas.ResponsesMessage{
			ID:                 schemas.Ptr("rs_x"),
			Role:               schemas.Ptr(schemas.ResponsesInputMessageRoleAssistant),
			Type:               schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
			ResponsesReasoning: &schemas.ResponsesReasoning{Summary: []schemas.ResponsesReasoningSummary{}},
		}
		if withBlock {
			msg.Content = &schemas.ResponsesMessageContent{
				ContentBlocks: []schemas.ResponsesMessageContentBlock{
					{Type: schemas.ResponsesOutputMessageContentTypeReasoning, Text: &txt},
				},
			}
		}
		if withSummary {
			msg.ResponsesReasoning.Summary = []schemas.ResponsesReasoningSummary{
				{Type: schemas.ResponsesReasoningContentBlockTypeSummaryText, Text: txt},
			}
		}
		return msg
	}

	t.Run("text in both content blocks and summary emits one part", func(t *testing.T) {
		resp := ToGeminiResponsesResponse(&schemas.BifrostResponsesResponse{
			Model:  "gemini-2.5-pro",
			Output: []schemas.ResponsesMessage{reasoningItem(true, true)},
		})
		texts := collectThoughtTexts(resp)
		if len(texts) != 1 || texts[0] != "Reasoning here." {
			t.Errorf("expected exactly one thought part, got %q", texts)
		}
	})

	t.Run("summary-only item still emits its part", func(t *testing.T) {
		resp := ToGeminiResponsesResponse(&schemas.BifrostResponsesResponse{
			Model:  "gemini-2.5-pro",
			Output: []schemas.ResponsesMessage{reasoningItem(false, true)},
		})
		texts := collectThoughtTexts(resp)
		if len(texts) != 1 || texts[0] != "Reasoning here." {
			t.Errorf("expected the summary fallback to emit one thought part, got %q", texts)
		}
	})
}
