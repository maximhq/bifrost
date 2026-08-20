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
