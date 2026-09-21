package anthropic

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

const anthropicGuardrailInterventionThenStop = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_guardrail","type":"message","role":"assistant","model":"claude-repro","content":[],"amazon-bedrock-guardrailAction":"INTERVENED","usage":{"input_tokens":5,"output_tokens":0}}}` + "\n\n" +
	"event: message_delta\n" +
	`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}` + "\n\n" +
	"event: message_stop\n" +
	`data: {"type":"message_stop"}` + "\n\n"

func TestAnthropicChatStreamLatchesBedrockGuardrailIntervention(t *testing.T) {
	server := anthropicSSEServer(t, anthropicGuardrailInterventionThenStop, false)
	defer server.Close()

	provider := newTruncationTestProvider(server.URL)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	stream, bifrostErr := provider.ChatCompletionStream(ctx, truncationPassthroughPostHook, nil,
		schemas.Key{Value: *schemas.NewSecretVar("test-key")}, truncationChatRequest())
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	chunks := collectTruncationChunks(t, stream)
	if len(chunks) == 0 {
		t.Fatal("expected chunks from a guarded stream")
	}
	for i, chunk := range chunks {
		if chunk.BifrostError != nil {
			t.Fatalf("chunk %d unexpectedly carried an error: %+v", i, chunk.BifrostError)
		}
	}

	final := chunks[len(chunks)-1].BifrostChatResponse
	if final == nil || len(final.Choices) != 1 || final.Choices[0].FinishReason == nil {
		t.Fatalf("expected a terminal choice with a finish reason, got %+v", final)
	}
	if got := *final.Choices[0].FinishReason; got != anthropicBedrockGuardrailIntervenedStopReason {
		t.Fatalf("finish reason = %q, want %q after a later end_turn delta", got, anthropicBedrockGuardrailIntervenedStopReason)
	}
}
