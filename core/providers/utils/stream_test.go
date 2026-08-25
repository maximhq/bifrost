package utils

import (
	"context"
	"strings"
	"testing"
	"time"

	schemas "github.com/maximhq/bifrost/core/schemas"
)

func TestCheckFirstStreamChunk_ErrorInFirstChunk(t *testing.T) {
	stream := make(chan *schemas.BifrostStreamChunk, 2)
	stream <- &schemas.BifrostStreamChunk{
		BifrostError: &schemas.BifrostError{
			Error: &schemas.ErrorField{
				Code:    schemas.Ptr("limit_burst_rate"),
				Message: "Request rate increased too quickly",
			},
		},
	}
	close(stream)

	_, drainDone, err := CheckFirstStreamChunkForError(context.Background(), stream)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	<-drainDone
	if err.Error.Message != "Request rate increased too quickly" {
		t.Errorf("unexpected error message: %s", err.Error.Message)
	}
	if err.Error.Code == nil || *err.Error.Code != "limit_burst_rate" {
		t.Errorf("unexpected error code: %v", err.Error.Code)
	}
}

func TestCheckFirstStreamChunk_ValidFirstChunk(t *testing.T) {
	stream := make(chan *schemas.BifrostStreamChunk, 3)
	chunk1 := &schemas.BifrostStreamChunk{
		BifrostChatResponse: &schemas.BifrostChatResponse{
			ID: "chatcmpl-123",
		},
	}
	chunk2 := &schemas.BifrostStreamChunk{
		BifrostChatResponse: &schemas.BifrostChatResponse{
			ID: "chatcmpl-123",
		},
	}
	stream <- chunk1
	stream <- chunk2
	close(stream)

	wrapped, _, err := CheckFirstStreamChunkForError(context.Background(), stream)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// First chunk should be re-injected
	got1 := <-wrapped
	if got1.BifrostChatResponse == nil || got1.BifrostChatResponse.ID != "chatcmpl-123" {
		t.Error("first chunk not re-injected correctly")
	}

	// Second chunk should follow
	got2 := <-wrapped
	if got2.BifrostChatResponse == nil || got2.BifrostChatResponse.ID != "chatcmpl-123" {
		t.Error("second chunk not forwarded correctly")
	}

	// Channel should be closed
	_, ok := <-wrapped
	if ok {
		t.Error("expected wrapped channel to be closed")
	}
}

func TestCheckFirstStreamChunk_EmptyStream(t *testing.T) {
	stream := make(chan *schemas.BifrostStreamChunk)
	close(stream)

	wrapped, drainDone, err := CheckFirstStreamChunkForError(context.Background(), stream)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Empty stream should return nil channel
	if wrapped != nil {
		t.Error("expected nil channel for empty stream")
	}

	// drainDone should be already closed
	select {
	case <-drainDone:
	default:
		t.Error("expected drainDone to be closed for empty stream")
	}
}

func TestCheckFirstStreamChunk_ErrorInSecondChunk(t *testing.T) {
	stream := make(chan *schemas.BifrostStreamChunk, 3)
	stream <- &schemas.BifrostStreamChunk{
		BifrostChatResponse: &schemas.BifrostChatResponse{
			ID: "chatcmpl-123",
		},
	}
	stream <- &schemas.BifrostStreamChunk{
		BifrostError: &schemas.BifrostError{
			Error: &schemas.ErrorField{
				Message: "some error in second chunk",
			},
		},
	}
	close(stream)

	// Should NOT return error — only first chunk matters for retry
	wrapped, _, err := CheckFirstStreamChunkForError(context.Background(), stream)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Read all chunks
	got1 := <-wrapped
	if got1.BifrostChatResponse == nil {
		t.Error("first chunk should be valid data")
	}
	got2 := <-wrapped
	if got2.BifrostError == nil {
		t.Error("second chunk should be the error")
	}

	_, ok := <-wrapped
	if ok {
		t.Error("expected wrapped channel to be closed")
	}
}

func TestCheckFirstStreamChunk_ErrorDrainsSource(t *testing.T) {
	stream := make(chan *schemas.BifrostStreamChunk, 5)
	stream <- &schemas.BifrostStreamChunk{
		BifrostError: &schemas.BifrostError{
			Error: &schemas.ErrorField{
				Message: "rate limit error",
			},
		},
	}
	// Add more chunks that should be drained
	stream <- &schemas.BifrostStreamChunk{
		BifrostChatResponse: &schemas.BifrostChatResponse{ID: "1"},
	}
	stream <- &schemas.BifrostStreamChunk{
		BifrostChatResponse: &schemas.BifrostChatResponse{ID: "2"},
	}
	close(stream)

	_, drainDone, err := CheckFirstStreamChunkForError(context.Background(), stream)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	<-drainDone
	if err.Error.Message != "rate limit error" {
		t.Errorf("unexpected error message: %s", err.Error.Message)
	}
	if drainDone == nil {
		t.Fatal("expected drainDone channel, got nil")
	}
	// Wait for drain to complete — verifies the channel signals properly
	<-drainDone
}

func TestCheckFirstStreamChunk_ErrorWithEmptyMessage(t *testing.T) {
	// Error with empty message and no code/type should NOT be treated as an error
	stream := make(chan *schemas.BifrostStreamChunk, 2)
	stream <- &schemas.BifrostStreamChunk{
		BifrostError: &schemas.BifrostError{
			Error: &schemas.ErrorField{
				Message: "",
			},
		},
	}
	close(stream)

	wrapped, _, err := CheckFirstStreamChunkForError(context.Background(), stream)
	if err != nil {
		t.Fatalf("unexpected error for empty message: %v", err)
	}
	// Should be treated as valid chunk
	<-wrapped
}

func TestCheckFirstStreamChunk_CtxCancelUnblocksWrapper(t *testing.T) {
	// Source with cap=1 so wrapped also has cap=1. wrapped is left full by
	// the re-injected first chunk, which makes the forwarder goroutine block
	// on its next send — the exact leak condition this test guards against.
	src := make(chan *schemas.BifrostStreamChunk, 1)
	src <- &schemas.BifrostStreamChunk{
		BifrostChatResponse: &schemas.BifrostChatResponse{ID: "1"},
	}

	ctx, cancel := context.WithCancel(context.Background())

	wrapped, drainDone, err := CheckFirstStreamChunkForError(ctx, src)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if wrapped == nil {
		t.Fatal("expected wrapped channel, got nil")
	}

	// Push a second chunk; forwarder will read it from src and then block
	// trying to send into the full wrapped channel (we intentionally never
	// read from wrapped).
	src <- &schemas.BifrostStreamChunk{
		BifrostChatResponse: &schemas.BifrostChatResponse{ID: "2"},
	}

	// Cancel — forwarder must stop trying to send to wrapped and drain src.
	cancel()

	// Simulate the upstream producer still emitting, then closing. The
	// drain loop should consume these and terminate.
	src <- &schemas.BifrostStreamChunk{
		BifrostChatResponse: &schemas.BifrostChatResponse{ID: "3"},
	}
	close(src)

	select {
	case <-drainDone:
	case <-time.After(time.Second):
		t.Fatal("drainDone did not close after ctx cancel; forwarder goroutine leaked")
	}
}

func TestAttachBilledUsageFromContext_KeepsUsageWithOnlyDetails(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	usage := &schemas.BifrostLLMUsage{
		// top-level counters all zero, but cache details are present
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{CachedReadTokens: 5},
	}
	ctx.SetValue(schemas.BifrostContextKeyStreamAccumulatedUsage, usage)

	bifrostErr := &schemas.BifrostError{}
	attachBilledUsageFromContext(ctx, bifrostErr)

	if bifrostErr.ExtraFields.BilledUsage == nil {
		t.Fatal("BilledUsage should be attached when cache details are present")
	}
	if bifrostErr.ExtraFields.BilledUsage.PromptTokensDetails == nil ||
		bifrostErr.ExtraFields.BilledUsage.PromptTokensDetails.CachedReadTokens != 5 {
		t.Fatalf("cached read tokens not preserved: %+v", bifrostErr.ExtraFields.BilledUsage)
	}
}

func TestAttachBilledUsageFromContext_CopiesUsage(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	usage := &schemas.BifrostLLMUsage{
		PromptTokens: 10,
		TotalTokens:  10,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedReadTokens: 3,
			CachedWriteTokenDetails: &schemas.ChatCachedWriteTokenDetails{
				CachedWriteTokens5m: 4,
			},
		},
	}
	ctx.SetValue(schemas.BifrostContextKeyStreamAccumulatedUsage, usage)

	bifrostErr := &schemas.BifrostError{}
	attachBilledUsageFromContext(ctx, bifrostErr)

	// Mutating the original (top-level AND nested pointers) must not change the
	// billed snapshot - BilledUsage is meant to be a fully decoupled record.
	usage.PromptTokens = 999
	usage.PromptTokensDetails.CachedReadTokens = 999
	usage.PromptTokensDetails.CachedWriteTokenDetails.CachedWriteTokens5m = 999

	billed := bifrostErr.ExtraFields.BilledUsage
	if billed.PromptTokens != 10 {
		t.Fatalf("BilledUsage aliases the context handle: got %d, want 10", billed.PromptTokens)
	}
	if billed.PromptTokensDetails.CachedReadTokens != 3 {
		t.Fatalf("BilledUsage aliases nested details: got %d, want 3", billed.PromptTokensDetails.CachedReadTokens)
	}
	if billed.PromptTokensDetails.CachedWriteTokenDetails.CachedWriteTokens5m != 4 {
		t.Fatalf("BilledUsage aliases deeply-nested details: got %d, want 4", billed.PromptTokensDetails.CachedWriteTokenDetails.CachedWriteTokens5m)
	}
}

func TestAttachBilledUsageFromContext_NoOpWhenEmpty(t *testing.T) {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyStreamAccumulatedUsage, &schemas.BifrostLLMUsage{})
	bifrostErr := &schemas.BifrostError{}
	attachBilledUsageFromContext(ctx, bifrostErr)
	if bifrostErr.ExtraFields.BilledUsage != nil {
		t.Fatal("BilledUsage should stay nil when nothing measurable accumulated")
	}
}

func TestCheckFirstStreamChunk_CodeOnlyError(t *testing.T) {
	// Error with code but no message should be treated as an error
	stream := make(chan *schemas.BifrostStreamChunk, 2)
	stream <- &schemas.BifrostStreamChunk{
		BifrostError: &schemas.BifrostError{
			Error: &schemas.ErrorField{
				Code: schemas.Ptr("limit_burst_rate"),
			},
		},
	}
	close(stream)

	_, drainDone, err := CheckFirstStreamChunkForError(context.Background(), stream)
	if err == nil {
		t.Fatal("expected error for code-only error, got nil")
	}
	<-drainDone
	if err.Error.Code == nil || *err.Error.Code != "limit_burst_rate" {
		t.Errorf("unexpected error code: %v", err.Error.Code)
	}
}

func TestStreamThroughputGuard_HealthyStreamPreservesChunkOrder(t *testing.T) {
	stream := make(chan *schemas.BifrostStreamChunk, 3)
	stream <- chatStreamChunk("first-" + strings.Repeat("a", 122))
	stream <- chatStreamChunk("second-" + strings.Repeat("b", 121))
	stream <- chatStreamChunk("tail")
	close(stream)

	wrapped, _, bifrostErr := CheckFirstStreamChunkForError(context.Background(), stream, &schemas.StreamThroughputGuardConfig{
		MinimumOutputCharactersPerSecond: 1000,
		ProbeWindowInSeconds:             1,
	})
	if bifrostErr != nil {
		t.Fatalf("healthy stream rejected: %v", bifrostErr)
	}
	var content strings.Builder
	for chunk := range wrapped {
		content.WriteString(*chunk.BifrostChatResponse.Choices[0].Delta.Content)
	}
	want := "first-" + strings.Repeat("a", 122) + "second-" + strings.Repeat("b", 121) + "tail"
	if got := content.String(); got != want {
		t.Fatalf("stream content = %q, want %q", got, want)
	}
}

func TestStreamThroughputGuard_RejectsSlowStreamBeforeOutput(t *testing.T) {
	stream := make(chan *schemas.BifrostStreamChunk, 1)
	stream <- &schemas.BifrostStreamChunk{BifrostChatResponse: &schemas.BifrostChatResponse{
		Choices: []schemas.BifrostResponseChoice{{ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
			Delta: &schemas.ChatStreamResponseChoiceDelta{Role: schemas.Ptr("assistant")},
		}}},
	}}

	wrapped, drainDone, bifrostErr := CheckFirstStreamChunkForError(context.Background(), stream, &schemas.StreamThroughputGuardConfig{
		MinimumOutputCharactersPerSecond: 100000,
		ProbeWindowInSeconds:             1,
	})
	if wrapped != nil {
		t.Fatal("slow primary output was exposed")
	}
	if bifrostErr == nil || bifrostErr.Error == nil || bifrostErr.Error.Code == nil || *bifrostErr.Error.Code != schemas.ErrCodeStreamThroughputBelowMinimum {
		t.Fatalf("unexpected guard error: %+v", bifrostErr)
	}
	close(stream)
	select {
	case <-drainDone:
	case <-time.After(time.Second):
		t.Fatal("slow stream did not drain")
	}
}

func TestStreamThroughputGuard_DetectsErrorAfterLifecycleChunk(t *testing.T) {
	stream := make(chan *schemas.BifrostStreamChunk, 2)
	stream <- &schemas.BifrostStreamChunk{BifrostResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{
		Type: schemas.ResponsesStreamResponseTypeCreated,
	}}
	stream <- &schemas.BifrostStreamChunk{BifrostError: &schemas.BifrostError{Error: &schemas.ErrorField{Message: "upstream failed"}}}
	close(stream)

	wrapped, drainDone, bifrostErr := CheckFirstStreamChunkForError(context.Background(), stream, &schemas.StreamThroughputGuardConfig{
		MinimumOutputCharactersPerSecond: 10,
		ProbeWindowInSeconds:             1,
	})
	if wrapped != nil {
		t.Fatal("lifecycle chunk was exposed before the upstream error")
	}
	if bifrostErr == nil || bifrostErr.Error == nil || bifrostErr.Error.Message != "upstream failed" {
		t.Fatalf("unexpected stream error: %+v", bifrostErr)
	}
	<-drainDone
}

func TestStreamThroughputGuard_ShortCompletedStreamIsAllowed(t *testing.T) {
	stream := make(chan *schemas.BifrostStreamChunk, 1)
	stream <- chatStreamChunk("ok")
	close(stream)

	wrapped, _, bifrostErr := CheckFirstStreamChunkForError(context.Background(), stream, &schemas.StreamThroughputGuardConfig{
		MinimumOutputCharactersPerSecond: 100000,
		ProbeWindowInSeconds:             1,
	})
	if bifrostErr != nil {
		t.Fatalf("short completed stream rejected: %+v", bifrostErr)
	}
	chunk, ok := <-wrapped
	if !ok || chunk == nil || *chunk.BifrostChatResponse.Choices[0].Delta.Content != "ok" {
		t.Fatalf("short stream chunk = %+v, want ok", chunk)
	}
	if _, ok := <-wrapped; ok {
		t.Fatal("short stream wrapper remained open")
	}
}

func TestStreamThroughputGuard_ProbeBufferIsBounded(t *testing.T) {
	stream := make(chan *schemas.BifrostStreamChunk, maxStreamProbeChunks)
	for i := 0; i < maxStreamProbeChunks; i++ {
		stream <- &schemas.BifrostStreamChunk{BifrostResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeInProgress,
			SequenceNumber: i,
		}}
	}
	close(stream)

	wrapped, drainDone, bifrostErr := CheckFirstStreamChunkForError(context.Background(), stream, &schemas.StreamThroughputGuardConfig{
		MinimumOutputCharactersPerSecond: 1,
		ProbeWindowInSeconds:             60,
	})
	if wrapped != nil {
		t.Fatal("buffer-limited probe exposed unverified output")
	}
	if bifrostErr == nil || bifrostErr.Error == nil || bifrostErr.Error.Code == nil || *bifrostErr.Error.Code != schemas.ErrCodeStreamThroughputProbeBufferExceeded {
		t.Fatalf("unexpected buffer guard error: %+v", bifrostErr)
	}
	select {
	case <-drainDone:
	case <-time.After(time.Second):
		t.Fatal("probe stream did not drain")
	}
}

func chatStreamChunk(content string) *schemas.BifrostStreamChunk {
	return &schemas.BifrostStreamChunk{BifrostChatResponse: &schemas.BifrostChatResponse{
		Choices: []schemas.BifrostResponseChoice{{ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
			Delta: &schemas.ChatStreamResponseChoiceDelta{Content: &content},
		}}},
	}}
}
