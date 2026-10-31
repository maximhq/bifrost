package anthropic

import (
	"context"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

// Anthropic reports the served tier on the message_start usage block and nowhere
// else. The per-event converter drops it (message_start only yields a role delta,
// message_delta yields nothing), and BifrostLLMUsage has no service_tier field for
// it to ride along in, so the streaming loop has to latch it and stamp it onto the
// final chunk the same way it already does for speed and inference_geo. Without
// that, every streamed Anthropic request logged an empty service_tier and repriced
// at standard rates.

const anthropicMessageStartPriority = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_repro","type":"message","role":"assistant","model":"claude-repro","content":[],` +
	`"usage":{"input_tokens":5,"output_tokens":0,"service_tier":"priority","speed":"fast","inference_geo":"us"}}}` + "\n\n" +
	"event: content_block_start\n" +
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n\n"

func TestAnthropicChatStreamFinalChunkCarriesServedServiceTier(t *testing.T) {
	server := anthropicSSEServer(t, anthropicMessageStartPriority+anthropicTextDelta+anthropicMessageStop, false)
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
		t.Fatal("expected at least one chunk")
	}
	for _, chunk := range chunks {
		if chunk.BifrostError != nil {
			t.Fatalf("unexpected stream error: %+v", chunk.BifrostError)
		}
	}

	final := chunks[len(chunks)-1].BifrostChatResponse
	if final == nil {
		t.Fatal("expected the last chunk to carry a chat response")
	}
	if final.ServiceTier == nil || *final.ServiceTier != schemas.BifrostServiceTierPriority {
		t.Fatalf("final chunk service_tier = %v, want priority", final.ServiceTier)
	}
	// The two siblings already worked; assert them so the latch cannot regress into
	// clobbering what it sits next to.
	if final.Speed == nil || *final.Speed != "fast" {
		t.Errorf("final chunk speed = %v, want fast", final.Speed)
	}
	if final.InferenceGeo == nil || *final.InferenceGeo != "us" {
		t.Errorf("final chunk inference_geo = %v, want us", final.InferenceGeo)
	}
}
