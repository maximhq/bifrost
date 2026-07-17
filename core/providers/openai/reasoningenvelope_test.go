package openai

import (
	"testing"

	"github.com/maximhq/bifrost/core/providers/utils"
	"github.com/maximhq/bifrost/core/schemas"
)

// ToOpenAIResponsesRequest is shared by every OpenAI-compatible destination (Azure,
// OpenRouter, ...) and fallback preparation reuses the same input while swapping only
// provider/model, so the deferred decode of the Bifrost redacted-thinking envelope must
// be gated on the destination: only requests bound for OpenAI itself may carry the raw
// ciphertext and original item id; any other destination drops the envelope item.
func TestToOpenAIResponsesRequest_EnvelopeDestinationGate(t *testing.T) {
	t.Parallel()

	const itemID = "rs_orig_gate"
	const payload = "OPENAI_ENCRYPTED_GATE"
	envelope := utils.WrapEncryptedReasoning(string(schemas.OpenAI), itemID, payload)
	const rawPayload = "RAW_NON_ENVELOPE_STATE"

	reasoningItem := func(encrypted string) schemas.ResponsesMessage {
		return schemas.ResponsesMessage{
			ID:   schemas.Ptr(itemID),
			Type: schemas.Ptr(schemas.ResponsesMessageTypeReasoning),
			ResponsesReasoning: &schemas.ResponsesReasoning{
				Summary:          []schemas.ResponsesReasoningSummary{},
				EncryptedContent: schemas.Ptr(encrypted),
			},
		}
	}

	tests := map[string]struct {
		destination   schemas.ModelProvider
		encrypted     string
		wantDropped   bool
		wantEncrypted string
	}{
		"envelope to openai unwraps":   {destination: schemas.OpenAI, encrypted: envelope, wantEncrypted: payload},
		"envelope to azure drops item": {destination: schemas.Azure, encrypted: envelope, wantDropped: true},
		"raw payload to openai kept":   {destination: schemas.OpenAI, encrypted: rawPayload, wantEncrypted: rawPayload},
		"raw payload to azure kept":    {destination: schemas.Azure, encrypted: rawPayload, wantEncrypted: rawPayload},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			req := ToOpenAIResponsesRequest(nil, &schemas.BifrostResponsesRequest{
				Provider: tt.destination,
				Model:    "o3",
				Input:    []schemas.ResponsesMessage{reasoningItem(tt.encrypted)},
			})

			var sent *schemas.ResponsesMessage
			for i := range req.Input.OpenAIResponsesRequestInputArray {
				msg := &req.Input.OpenAIResponsesRequestInputArray[i]
				if msg.Type != nil && *msg.Type == schemas.ResponsesMessageTypeReasoning {
					sent = msg
				}
			}

			if tt.wantDropped {
				if sent != nil {
					t.Fatalf("envelope item must be dropped for destination %s, got item with content %v", tt.destination, sent.ResponsesReasoning.EncryptedContent)
				}
				return
			}
			if sent == nil {
				t.Fatal("reasoning item missing from the outgoing request")
			}
			if sent.ID == nil || *sent.ID != itemID {
				t.Errorf("outgoing item id = %v, want %q", sent.ID, itemID)
			}
			if sent.ResponsesReasoning == nil || sent.ResponsesReasoning.EncryptedContent == nil ||
				*sent.ResponsesReasoning.EncryptedContent != tt.wantEncrypted {
				t.Errorf("outgoing encrypted_content = %v, want %q", sent.ResponsesReasoning.EncryptedContent, tt.wantEncrypted)
			}
		})
	}
}
