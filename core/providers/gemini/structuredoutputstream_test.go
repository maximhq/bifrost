package gemini

import (
	"context"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/providers/anthropic"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// End-to-end guard for the reported symptom: a schema-constrained streaming request
// through /genai to a provider that emulates structured output with a tool call
// (Vertex, Bedrock Mantle, Azure) came back with no text at all --
//
//	POST /genai/v1beta/models/bedrock_mantle/anthropic.claude-opus-4-8:streamGenerateContent
//	  {"generationConfig":{"response_schema":{...}}}
//	→ data: {"candidates":[{"content":{"role":"model"},"finishReason":"STOP"}],
//	         "usageMetadata":{"candidatesTokenCount":33,...}}
//
// Tokens were billed, the model answered, and the client saw an empty candidate.
// The two converters are exercised together because neither is wrong alone: the
// Anthropic side owns emitting output_text.delta for the reassembled tool call, and
// the Gemini side turns exactly that event into a candidate part.
func TestGeminiStream_CarriesToolBasedStructuredOutput(t *testing.T) {
	const toolName = "bf_so_Greeting"
	const wantJSON = `{"text":"hi"}`

	index := 0
	wire := []*anthropic.AnthropicStreamEvent{
		{
			Type:  anthropic.AnthropicStreamEventTypeContentBlockStart,
			Index: &index,
			ContentBlock: &anthropic.AnthropicContentBlock{
				Type: anthropic.AnthropicContentBlockTypeToolUse,
				ID:   schemas.Ptr("toolu_bdrk_01Su19x6xQrV44fPpJHzBhVm"),
				Name: schemas.Ptr(toolName),
			},
		},
		{
			Type:  anthropic.AnthropicStreamEventTypeContentBlockDelta,
			Index: &index,
			Delta: &anthropic.AnthropicStreamDelta{
				Type:        anthropic.AnthropicStreamDeltaTypeInputJSON,
				PartialJSON: schemas.Ptr(`{"text":`),
			},
		},
		{
			Type:  anthropic.AnthropicStreamEventTypeContentBlockDelta,
			Index: &index,
			Delta: &anthropic.AnthropicStreamDelta{
				Type:        anthropic.AnthropicStreamDeltaTypeInputJSON,
				PartialJSON: schemas.Ptr(`"hi"}`),
			},
		},
		{
			Type:  anthropic.AnthropicStreamEventTypeContentBlockStop,
			Index: &index,
		},
	}

	anthropicState := anthropic.AcquireAnthropicResponsesStreamState()
	defer anthropic.ReleaseAnthropicResponsesStreamState(anthropicState)
	anthropicState.StructuredOutputToolName = toolName

	geminiState := NewBifrostToGeminiStreamState()

	var text strings.Builder
	sequenceNumber := 0
	for _, event := range wire {
		bifrostEvents, bifrostErr, _ := event.ToBifrostResponsesStream(context.Background(), sequenceNumber, anthropicState)
		if bifrostErr != nil {
			t.Fatalf("anthropic converter failed on %s: %v", event.Type, bifrostErr)
		}
		sequenceNumber += len(bifrostEvents)

		for _, bifrostEvent := range bifrostEvents {
			geminiEvent := ToGeminiResponsesStreamResponse(bifrostEvent, geminiState)
			if geminiEvent == nil {
				continue
			}
			for _, candidate := range geminiEvent.Candidates {
				if candidate.Content == nil {
					continue
				}
				for _, part := range candidate.Content.Parts {
					// Thought parts are the model's reasoning, not its answer.
					if part.Thought {
						continue
					}
					text.WriteString(part.Text)
				}
			}
		}
	}

	if got := text.String(); got != wantJSON {
		t.Errorf("expected the genai stream to carry %q, got %q", wantJSON, got)
	}
}
