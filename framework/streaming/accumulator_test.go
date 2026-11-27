package streaming

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// TestChatStreamingFinalChunkNoDeadlock tests that processing the final chunk doesn't deadlock
// This is a regression test for the issue where getLastChatChunk() was trying to acquire
// a lock that was already held by processAccumulatedChatStreamingChunks()
func TestChatStreamingFinalChunkNoDeadlock(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelDebug)
	accumulator := NewAccumulator(nil, logger)

	requestID := "test-request-123"
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyAccumulatorID, requestID)

	// Create accumulator with some chunks
	for i := 0; i < 10; i++ {
		chunk := &ChatStreamChunk{
			ChunkIndex: i,
			Timestamp:  time.Now(),
			Delta: &schemas.ChatStreamResponseChoiceDelta{
				Content: bifrost.Ptr(fmt.Sprintf("chunk %d", i)),
			},
		}
		if i == 9 {
			// Last chunk has usage
			chunk.TokenUsage = &schemas.BifrostLLMUsage{
				PromptTokens:     100,
				CompletionTokens: 50,
				TotalTokens:      150,
			}
		}
		err := accumulator.addChatStreamChunk(requestID, StreamTypeChat, chunk, i == 9)
		if err != nil {
			t.Fatalf("Failed to add chunk %d: %v", i, err)
		}
	}

	// Create a mock response for the final chunk
	response := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ID:     "msg_123",
			Object: "chat.completion.chunk",
			Choices: []schemas.BifrostResponseChoice{
				{
					ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{
						Delta: &schemas.ChatStreamResponseChoiceDelta{},
					},
					FinishReason: bifrost.Ptr("stop"),
				},
			},
			Usage: &schemas.BifrostLLMUsage{
				PromptTokens:     100,
				CompletionTokens: 50,
				TotalTokens:      150,
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:            schemas.ChatCompletionStreamRequest,
				Provider:               schemas.Anthropic,
				OriginalModelRequested: "claude-opus-4",
				ChunkIndex:             9,
			},
		},
	}

	// Set final chunk indicator
	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)

	// Use a timeout to detect deadlock
	done := make(chan struct{})
	var processErr error

	go func() {
		defer close(done)
		_, processErr = accumulator.processChatStreamingResponse(ctx, response, nil)
	}()

	select {
	case <-done:
		if processErr != nil {
			t.Fatalf("Failed to process final chunk: %v", processErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Deadlock detected: processChatStreamingResponse took too long (>5s)")
	}
}

func TestAccumulatedChatStreamPreservesServiceTierBeforeUsageOnlyChunk(t *testing.T) {
	accumulator := NewAccumulator(nil, bifrost.NewDefaultLogger(schemas.LogLevelError))
	t.Cleanup(accumulator.Cleanup)

	requestID := "chat-service-tier"
	priority := schemas.BifrostServiceTierPriority
	requireNoError := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	requireNoError(accumulator.addChatStreamChunk(requestID, StreamTypeChat, &ChatStreamChunk{
		ChunkIndex:  1,
		Timestamp:   time.Now(),
		ServiceTier: &priority,
	}, false))
	requireNoError(accumulator.addChatStreamChunk(requestID, StreamTypeChat, &ChatStreamChunk{
		ChunkIndex: 2,
		Timestamp:  time.Now(),
		TokenUsage: &schemas.BifrostLLMUsage{TotalTokens: 1},
	}, true))

	data, err := accumulator.processAccumulatedChatStreamingChunks(requestID, nil, true)
	requireNoError(err)
	if data.ServiceTier == nil || *data.ServiceTier != schemas.BifrostServiceTierPriority {
		t.Fatalf("expected priority service tier, got %v", data.ServiceTier)
	}
}

// TestAccumulatedChatStreamKeepsRawResponseFormat verifies the joined raw keeps the format its chunks declared, so a
// chunk without a declaration (a synthetic tail) does not clear it.
func TestAccumulatedChatStreamKeepsRawResponseFormat(t *testing.T) {
	accumulator := NewAccumulator(nil, bifrost.NewDefaultLogger(schemas.LogLevelError))
	t.Cleanup(accumulator.Cleanup)
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyAccumulatorID, "chat-raw-format")

	declared := newChatChunkResponse(0, "Hello", nil, nil)
	declared.ChatResponse.ExtraFields.RawResponse = `{"choices":[{"index":0,"delta":{"content":"Hello"}}]}`
	declared.ChatResponse.ExtraFields.RawResponseFormat = schemas.RawResponseFormatOpenAIChatCompletionChunk
	if _, err := accumulator.processChatStreamingResponse(ctx, declared, nil); err != nil {
		t.Fatalf("declared chunk: %v", err)
	}
	tail := newChatChunkResponse(1, "", bifrost.Ptr("stop"), nil)
	tail.ChatResponse.ExtraFields.RawResponse = `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`
	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	processed, err := accumulator.processChatStreamingResponse(ctx, tail, nil)
	if err != nil {
		t.Fatalf("tail chunk: %v", err)
	}

	if processed == nil || processed.Data == nil || processed.Data.RawResponse == nil {
		t.Fatal("expected accumulated raw response on final chunk")
	}
	if want := `{"choices":[{"index":0,"delta":{"content":"Hello"}}]}` + "\n\n" + `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`; *processed.Data.RawResponse != want {
		t.Fatalf("accumulated raw = %q, want %q", *processed.Data.RawResponse, want)
	}
	if processed.Data.RawResponseFormat != schemas.RawResponseFormatOpenAIChatCompletionChunk {
		t.Fatalf("accumulated raw format = %q, want %q", processed.Data.RawResponseFormat, schemas.RawResponseFormatOpenAIChatCompletionChunk)
	}
}

// TestResponsesStreamingFinalChunkNoDeadlock tests Responses streaming doesn't deadlock
func TestResponsesStreamingFinalChunkNoDeadlock(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelDebug)
	accumulator := NewAccumulator(nil, logger)

	requestID := "test-responses-request"
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyAccumulatorID, requestID)

	// Add some chunks
	for i := 0; i < 5; i++ {
		chunk := &ResponsesStreamChunk{
			ChunkIndex: i,
			Timestamp:  time.Now(),
			StreamResponse: &schemas.BifrostResponsesStreamResponse{
				Type: "message_delta",
				Response: &schemas.BifrostResponsesResponse{
					Usage: &schemas.ResponsesResponseUsage{
						InputTokens:  100,
						OutputTokens: 50,
					},
				},
			},
		}
		if i == 4 {
			chunk.TokenUsage = &schemas.BifrostLLMUsage{
				PromptTokens:     100,
				CompletionTokens: 50,
				TotalTokens:      150,
			}
		}
		err := accumulator.addResponsesStreamChunk(requestID, chunk, i == 4)
		if err != nil {
			t.Fatalf("Failed to add chunk: %v", err)
		}
	}

	// Create final chunk response
	response := &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			ID: bifrost.Ptr("msg_456"),
			Usage: &schemas.ResponsesResponseUsage{
				InputTokens:  100,
				OutputTokens: 50,
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:            schemas.ResponsesStreamRequest,
				Provider:               schemas.Anthropic,
				OriginalModelRequested: "claude-opus-4",
				ChunkIndex:             4,
			},
		},
	}

	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)

	done := make(chan struct{})
	var processErr error

	go func() {
		defer close(done)
		_, processErr = accumulator.processResponsesStreamingResponse(ctx, response, nil)
	}()

	select {
	case <-done:
		if processErr != nil {
			t.Fatalf("Failed to process final chunk: %v", processErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Deadlock detected: processResponsesStreamingResponse took too long (>5s)")
	}
}

// TestConcurrentChunkAddition tests that adding chunks concurrently is safe
func TestConcurrentChunkAddition(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelDebug)
	accumulator := NewAccumulator(nil, logger)

	requestID := "test-concurrent-add"
	const numGoroutines = 10
	const chunksPerGoroutine = 10

	var wg sync.WaitGroup
	errors := make(chan error, numGoroutines)

	for g := 0; g < numGoroutines; g++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for i := 0; i < chunksPerGoroutine; i++ {
				chunk := &ChatStreamChunk{
					ChunkIndex: goroutineID*chunksPerGoroutine + i,
					Timestamp:  time.Now(),
					Delta: &schemas.ChatStreamResponseChoiceDelta{
						Content: bifrost.Ptr(fmt.Sprintf("g%d-c%d", goroutineID, i)),
					},
				}
				err := accumulator.addChatStreamChunk(requestID, StreamTypeChat, chunk, false)
				if err != nil {
					errors <- err
					return
				}
			}
		}(g)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		close(errors)
		for err := range errors {
			t.Errorf("Concurrent add error: %v", err)
		}

		// Verify all chunks were added
		acc := accumulator.getOrCreateStreamAccumulator(requestID)
		acc.mu.Lock()
		chunkCount := len(acc.ChatStreamChunks)
		acc.mu.Unlock()

		if chunkCount != numGoroutines*chunksPerGoroutine {
			t.Errorf("Expected %d chunks, got %d", numGoroutines*chunksPerGoroutine, chunkCount)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Deadlock detected: concurrent chunk addition took too long (>10s)")
	}
}

// TestGetLastChunkMethodsSafe tests that the getLast*Chunk methods don't cause deadlock
func TestGetLastChunkMethodsSafe(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelDebug)
	accumulator := NewAccumulator(nil, logger)

	requestID := "test-last-chunk"

	// Add a chat chunk
	chunk := &ChatStreamChunk{
		ChunkIndex: 0,
		Timestamp:  time.Now(),
		TokenUsage: &schemas.BifrostLLMUsage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
		},
	}
	err := accumulator.addChatStreamChunk(requestID, StreamTypeChat, chunk, false)
	if err != nil {
		t.Fatalf("Failed to add chunk: %v", err)
	}

	// Get the accumulator
	acc := accumulator.getOrCreateStreamAccumulator(requestID)

	// This should not deadlock - getLastChatChunk doesn't acquire locks anymore
	lastChunk := acc.getLastChatChunk()
	if lastChunk == nil {
		t.Error("Expected to get last chunk, got nil")
	}
	if lastChunk.ChunkIndex != 0 {
		t.Errorf("Expected chunk index 0, got %d", lastChunk.ChunkIndex)
	}
}

func TestAccumulateToolCallsInterleavedParallel(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelDebug)
	accumulator := NewAccumulator(nil, logger)

	makeChunk := func(index int, toolCalls []schemas.ChatAssistantMessageToolCall) *ChatStreamChunk {
		return &ChatStreamChunk{
			ChunkIndex: index,
			Delta: &schemas.ChatStreamResponseChoiceDelta{
				ToolCalls: toolCalls,
			},
		}
	}

	makeDelta := func(index uint16, id *string, name *string, args string) schemas.ChatAssistantMessageToolCall {
		return schemas.ChatAssistantMessageToolCall{
			Index: index,
			ID:    id,
			Type:  schemas.Ptr("function"),
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name:      name,
				Arguments: args,
			},
		}
	}

	toolCallID0 := "call_0"
	toolCallID1 := "call_1"
	toolNameAdd := "add"
	toolNameMultiply := "multiply"

	// Interleaved deltas for parallel tool calls
	chunks := []*ChatStreamChunk{
		makeChunk(0, []schemas.ChatAssistantMessageToolCall{makeDelta(0, &toolCallID0, &toolNameAdd, "")}),
		makeChunk(1, []schemas.ChatAssistantMessageToolCall{makeDelta(1, &toolCallID1, &toolNameMultiply, "")}),
		makeChunk(2, []schemas.ChatAssistantMessageToolCall{makeDelta(0, nil, nil, "{\"a\": 1")}),
		makeChunk(3, []schemas.ChatAssistantMessageToolCall{makeDelta(1, nil, nil, "{\"a\": 2")}),
		makeChunk(4, []schemas.ChatAssistantMessageToolCall{makeDelta(0, nil, nil, ", \"b\": 3}")}),
		makeChunk(5, []schemas.ChatAssistantMessageToolCall{makeDelta(1, nil, nil, ", \"b\": 4}")}),
	}

	message := accumulator.buildCompleteMessageFromChatStreamChunks(chunks)

	if message.ChatAssistantMessage == nil {
		t.Fatal("expected ChatAssistantMessage to be initialized")
	}

	toolCalls := message.ChatAssistantMessage.ToolCalls
	if len(toolCalls) != 2 {
		t.Fatalf("expected 2 tool calls, got %d", len(toolCalls))
	}

	var addCall *schemas.ChatAssistantMessageToolCall
	var multiplyCall *schemas.ChatAssistantMessageToolCall
	for i := range toolCalls {
		if toolCalls[i].Function.Name != nil {
			switch *toolCalls[i].Function.Name {
			case "add":
				addCall = &toolCalls[i]
			case "multiply":
				multiplyCall = &toolCalls[i]
			}
		}
	}

	if addCall == nil || multiplyCall == nil {
		t.Fatalf("expected both add and multiply tool calls, got add=%v multiply=%v", addCall != nil, multiplyCall != nil)
	}

	if addCall.Function.Arguments != "{\"a\": 1, \"b\": 3}" {
		t.Fatalf("unexpected add arguments: %s", addCall.Function.Arguments)
	}
	if multiplyCall.Function.Arguments != "{\"a\": 2, \"b\": 4}" {
		t.Fatalf("unexpected multiply arguments: %s", multiplyCall.Function.Arguments)
	}
}

// TestBuildCompleteMessageFromResponsesStreamChunksParallelToolCalls tests that
// parallel function call argument deltas are routed to the correct message by ItemID,
// preventing arguments from being merged across different tool calls.
func TestBuildCompleteMessageFromResponsesStreamChunksParallelToolCalls(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelDebug)
	accumulator := NewAccumulator(nil, logger)

	itemID0 := "call_0"
	itemID1 := "call_1"
	fnName0 := "add"
	fnName1 := "multiply"

	makeChunk := func(idx int, resp *schemas.BifrostResponsesStreamResponse) *ResponsesStreamChunk {
		return &ResponsesStreamChunk{
			ChunkIndex:     idx,
			Timestamp:      time.Now(),
			StreamResponse: resp,
		}
	}

	ptr := func(s string) *string { return &s }

	chunks := []*ResponsesStreamChunk{
		// OutputItemAdded for call_0 (add)
		makeChunk(0, &schemas.BifrostResponsesStreamResponse{
			Type: schemas.ResponsesStreamResponseTypeOutputItemAdded,
			Item: &schemas.ResponsesMessage{
				ID:   ptr(itemID0),
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					Name: ptr(fnName0),
				},
			},
		}),
		// OutputItemAdded for call_1 (multiply)
		makeChunk(1, &schemas.BifrostResponsesStreamResponse{
			Type: schemas.ResponsesStreamResponseTypeOutputItemAdded,
			Item: &schemas.ResponsesMessage{
				ID:   ptr(itemID1),
				Type: schemas.Ptr(schemas.ResponsesMessageTypeFunctionCall),
				ResponsesToolMessage: &schemas.ResponsesToolMessage{
					Name: ptr(fnName1),
				},
			},
		}),
		// Argument delta for call_0
		makeChunk(2, &schemas.BifrostResponsesStreamResponse{
			Type:   schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
			ItemID: ptr(itemID0),
			Delta:  ptr(`{"a": 1`),
		}),
		// Argument delta for call_1
		makeChunk(3, &schemas.BifrostResponsesStreamResponse{
			Type:   schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
			ItemID: ptr(itemID1),
			Delta:  ptr(`{"a": 2`),
		}),
		// Argument delta continuation for call_0
		makeChunk(4, &schemas.BifrostResponsesStreamResponse{
			Type:   schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
			ItemID: ptr(itemID0),
			Delta:  ptr(`, "b": 3}`),
		}),
		// Argument delta continuation for call_1
		makeChunk(5, &schemas.BifrostResponsesStreamResponse{
			Type:   schemas.ResponsesStreamResponseTypeFunctionCallArgumentsDelta,
			ItemID: ptr(itemID1),
			Delta:  ptr(`, "b": 4}`),
		}),
	}

	messages := accumulator.buildCompleteMessageFromResponsesStreamChunks(chunks)

	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}

	var addMsg *schemas.ResponsesMessage
	var multiplyMsg *schemas.ResponsesMessage
	for i := range messages {
		if messages[i].ID != nil && *messages[i].ID == itemID0 {
			addMsg = &messages[i]
		}
		if messages[i].ID != nil && *messages[i].ID == itemID1 {
			multiplyMsg = &messages[i]
		}
	}

	if addMsg == nil || multiplyMsg == nil {
		t.Fatalf("expected both add and multiply messages, got add=%v multiply=%v", addMsg != nil, multiplyMsg != nil)
	}

	if addMsg.ResponsesToolMessage == nil || addMsg.ResponsesToolMessage.Arguments == nil {
		t.Fatalf("add message missing arguments")
	}
	if multiplyMsg.ResponsesToolMessage == nil || multiplyMsg.ResponsesToolMessage.Arguments == nil {
		t.Fatalf("multiply message missing arguments")
	}

	if *addMsg.ResponsesToolMessage.Arguments != `{"a": 1, "b": 3}` {
		t.Fatalf("unexpected add arguments: %s", *addMsg.ResponsesToolMessage.Arguments)
	}
	if *multiplyMsg.ResponsesToolMessage.Arguments != `{"a": 2, "b": 4}` {
		t.Fatalf("unexpected multiply arguments: %s", *multiplyMsg.ResponsesToolMessage.Arguments)
	}
}

// TestAudioStreamingFinalChunkNoDeadlock tests that audio streaming doesn't deadlock on final chunk
func TestAudioStreamingFinalChunkNoDeadlock(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelDebug)
	accumulator := NewAccumulator(nil, logger)

	requestID := "test-audio-request"
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyAccumulatorID, requestID)

	// Add some audio chunks
	for i := 0; i < 8; i++ {
		chunk := &AudioStreamChunk{
			ChunkIndex: i,
			Timestamp:  time.Now(),
			Delta: &schemas.BifrostSpeechStreamResponse{
				Type:  schemas.SpeechStreamResponseTypeDelta,
				Audio: []byte(fmt.Sprintf("audio-data-%d", i)),
			},
		}
		if i == 7 {
			chunk.TokenUsage = &schemas.SpeechUsage{
				InputTokens:  100,
				OutputTokens: 50,
				TotalTokens:  150,
			}
		}
		err := accumulator.addAudioStreamChunk(requestID, chunk, i == 7)
		if err != nil {
			t.Fatalf("Failed to add audio chunk: %v", err)
		}
	}

	// Create final chunk response
	response := &schemas.BifrostResponse{
		SpeechResponse: &schemas.BifrostSpeechResponse{
			Audio: []byte("final-audio-data"),
			Usage: &schemas.SpeechUsage{
				InputTokens:  100,
				OutputTokens: 50,
				TotalTokens:  150,
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:            schemas.SpeechStreamRequest,
				Provider:               schemas.OpenAI,
				OriginalModelRequested: "tts-1",
				ChunkIndex:             7,
			},
		},
	}

	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)

	done := make(chan struct{})
	var processErr error

	go func() {
		defer close(done)
		_, processErr = accumulator.processAudioStreamingResponse(ctx, response, nil)
	}()

	select {
	case <-done:
		if processErr != nil {
			t.Fatalf("Failed to process final audio chunk: %v", processErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Deadlock detected: processAudioStreamingResponse took too long (>5s)")
	}
}

// TestTranscriptionStreamingFinalChunkNoDeadlock tests that transcription streaming doesn't deadlock on final chunk
func TestTranscriptionStreamingFinalChunkNoDeadlock(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelDebug)
	accumulator := NewAccumulator(nil, logger)

	requestID := "test-transcription-request"
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyAccumulatorID, requestID)

	// Add some transcription chunks
	for i := 0; i < 6; i++ {
		delta := fmt.Sprintf("transcribed text %d ", i)
		chunk := &TranscriptionStreamChunk{
			ChunkIndex: i,
			Timestamp:  time.Now(),
			Delta: &schemas.BifrostTranscriptionStreamResponse{
				Type:  schemas.TranscriptionStreamResponseTypeDelta,
				Delta: &delta,
				Text:  delta,
			},
		}
		if i == 5 {
			inputTokens := 100
			outputTokens := 50
			totalTokens := 150
			chunk.TokenUsage = &schemas.TranscriptionUsage{
				Type:         "tokens",
				InputTokens:  &inputTokens,
				OutputTokens: &outputTokens,
				TotalTokens:  &totalTokens,
			}
		}
		err := accumulator.addTranscriptionStreamChunk(requestID, chunk, i == 5)
		if err != nil {
			t.Fatalf("Failed to add transcription chunk: %v", err)
		}
	}

	// Create final chunk response
	response := &schemas.BifrostResponse{
		TranscriptionResponse: &schemas.BifrostTranscriptionResponse{
			Text: "Complete transcription",
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:            schemas.TranscriptionStreamRequest,
				Provider:               schemas.OpenAI,
				OriginalModelRequested: "whisper-1",
				ChunkIndex:             5,
			},
		},
	}

	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)

	done := make(chan struct{})
	var processErr error

	go func() {
		defer close(done)
		_, processErr = accumulator.processTranscriptionStreamingResponse(ctx, response, nil)
	}()

	select {
	case <-done:
		if processErr != nil {
			t.Fatalf("Failed to process final transcription chunk: %v", processErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Deadlock detected: processTranscriptionStreamingResponse took too long (>5s)")
	}
}

// TestGetLastAudioAndTranscriptionChunksSafe tests that getLastAudioChunk and getLastTranscriptionChunk are safe
func TestGetLastAudioAndTranscriptionChunksSafe(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelDebug)
	accumulator := NewAccumulator(nil, logger)

	requestID := "test-last-audio-transcription"

	// Add audio chunk
	audioChunk := &AudioStreamChunk{
		ChunkIndex: 5,
		Timestamp:  time.Now(),
		Delta: &schemas.BifrostSpeechStreamResponse{
			Type:  schemas.SpeechStreamResponseTypeDelta,
			Audio: []byte("audio-data"),
		},
		TokenUsage: &schemas.SpeechUsage{
			InputTokens:  100,
			OutputTokens: 50,
			TotalTokens:  150,
		},
	}
	err := accumulator.addAudioStreamChunk(requestID, audioChunk, false)
	if err != nil {
		t.Fatalf("Failed to add audio chunk: %v", err)
	}

	// Add transcription chunk
	delta := "transcribed text"
	inputTokens := 100
	outputTokens := 50
	totalTokens := 150
	transcriptionChunk := &TranscriptionStreamChunk{
		ChunkIndex: 3,
		Timestamp:  time.Now(),
		Delta: &schemas.BifrostTranscriptionStreamResponse{
			Type:  schemas.TranscriptionStreamResponseTypeDelta,
			Delta: &delta,
			Text:  delta,
		},
		TokenUsage: &schemas.TranscriptionUsage{
			Type:         "tokens",
			InputTokens:  &inputTokens,
			OutputTokens: &outputTokens,
			TotalTokens:  &totalTokens,
		},
	}
	err = accumulator.addTranscriptionStreamChunk(requestID, transcriptionChunk, false)
	if err != nil {
		t.Fatalf("Failed to add transcription chunk: %v", err)
	}

	// Get the accumulator
	acc := accumulator.getOrCreateStreamAccumulator(requestID)

	// Test getLastAudioChunk - should not deadlock
	lastAudio := acc.getLastAudioChunk()
	if lastAudio == nil {
		t.Error("Expected to get last audio chunk, got nil")
	}
	if lastAudio != nil && lastAudio.ChunkIndex != 5 {
		t.Errorf("Expected audio chunk index 5, got %d", lastAudio.ChunkIndex)
	}

	// Test getLastTranscriptionChunk - should not deadlock
	lastTranscription := acc.getLastTranscriptionChunk()
	if lastTranscription == nil {
		t.Error("Expected to get last transcription chunk, got nil")
	}
	if lastTranscription != nil && lastTranscription.ChunkIndex != 3 {
		t.Errorf("Expected transcription chunk index 3, got %d", lastTranscription.ChunkIndex)
	}
}

func TestProcessStreamingResponsePreservesTerminalResponsesUsageWhenChunkIndexIsReused(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelDebug)
	accumulator := NewAccumulator(nil, logger)

	requestID := "test-responses-terminal-usage-reused-index"
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyAccumulatorID, requestID)

	priorChunks := []*ResponsesStreamChunk{
		{
			ChunkIndex: 0,
			Timestamp:  time.Now(),
			StreamResponse: &schemas.BifrostResponsesStreamResponse{
				Type: schemas.ResponsesStreamResponseTypeCreated,
				Response: &schemas.BifrostResponsesResponse{
					ID: bifrost.Ptr("resp_terminal_usage"),
				},
			},
		},
		{
			ChunkIndex: 7,
			Timestamp:  time.Now(),
			StreamResponse: &schemas.BifrostResponsesStreamResponse{
				Type:  schemas.ResponsesStreamResponseTypeOutputTextDelta,
				Delta: bifrost.Ptr("partial"),
			},
		},
		{
			ChunkIndex: 7,
			Timestamp:  time.Now(),
			StreamResponse: &schemas.BifrostResponsesStreamResponse{
				Type:  schemas.ResponsesStreamResponseTypeOutputTextDelta,
				Delta: bifrost.Ptr("duplicate index ignored"),
			},
		},
	}
	for _, chunk := range priorChunks {
		if err := accumulator.addResponsesStreamChunk(requestID, chunk, false); err != nil {
			t.Fatalf("addResponsesStreamChunk() error = %v", err)
		}
	}

	response := &schemas.BifrostResponse{
		ResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeCompleted,
			SequenceNumber: 0,
			Response: &schemas.BifrostResponsesResponse{
				ID: bifrost.Ptr("resp_terminal_usage"),
				Usage: &schemas.ResponsesResponseUsage{
					InputTokens:  31,
					OutputTokens: 17,
					TotalTokens:  48,
				},
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:            schemas.ResponsesStreamRequest,
				Provider:               schemas.OpenAI,
				OriginalModelRequested: "gpt-4o-mini",
				ChunkIndex:             0,
			},
		},
	}

	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)

	processed, err := accumulator.ProcessStreamingResponse(ctx, response, nil)
	if err != nil {
		t.Fatalf("ProcessStreamingResponse() error = %v", err)
	}
	assertTokenUsage := func(t *testing.T, processed *ProcessedStreamResponse) {
		t.Helper()
		if processed == nil {
			t.Fatal("expected processed response, got nil")
		}
		if processed.Data == nil || processed.Data.TokenUsage == nil {
			t.Fatal("expected terminal response.completed usage to be accumulated")
		}
		if processed.Data.TokenUsage.PromptTokens != 31 {
			t.Fatalf("expected prompt tokens 31, got %d", processed.Data.TokenUsage.PromptTokens)
		}
		if processed.Data.TokenUsage.CompletionTokens != 17 {
			t.Fatalf("expected completion tokens 17, got %d", processed.Data.TokenUsage.CompletionTokens)
		}
		if processed.Data.TokenUsage.TotalTokens != 48 {
			t.Fatalf("expected total tokens 48, got %d", processed.Data.TokenUsage.TotalTokens)
		}
	}
	assertTokenUsage(t, processed)

	processedAgain, err := accumulator.ProcessStreamingResponse(ctx, response, nil)
	if err != nil {
		t.Fatalf("second ProcessStreamingResponse() error = %v", err)
	}
	assertTokenUsage(t, processedAgain)

	streamAccumulator := accumulator.getOrCreateStreamAccumulator(requestID)
	streamAccumulator.mu.Lock()
	defer streamAccumulator.mu.Unlock()
	completedChunks := 0
	for _, chunk := range streamAccumulator.ResponsesStreamChunks {
		if chunk.StreamResponse != nil && chunk.StreamResponse.Type == schemas.ResponsesStreamResponseTypeCompleted {
			completedChunks++
		}
	}
	if completedChunks != 1 {
		t.Fatalf("expected exactly one terminal response.completed chunk after duplicate processing, got %d", completedChunks)
	}
}

// On a monotonic stream the terminal chunk arrives with a unique highest index, so
// the first delivery is stored as-is — the reservation must still be seeded then,
// or a second plugin delivering the same terminal chunk (logging + maxim both call
// ProcessStreamingChunk with the final result) mints a fresh index and the chunk is
// double-appended into the persisted raw log.
func TestProcessStreamingResponseDedupesDuplicateTerminalChunkWithUniqueIndex(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelDebug)
	accumulator := NewAccumulator(nil, logger)

	requestID := "test-responses-terminal-monotonic-duplicate"
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyAccumulatorID, requestID)

	for i := 0; i < 3; i++ {
		chunk := &ResponsesStreamChunk{
			ChunkIndex: i,
			Timestamp:  time.Now(),
			StreamResponse: &schemas.BifrostResponsesStreamResponse{
				Type:  schemas.ResponsesStreamResponseTypeOutputTextDelta,
				Delta: bifrost.Ptr("chunk"),
			},
		}
		if err := accumulator.addResponsesStreamChunk(requestID, chunk, false); err != nil {
			t.Fatalf("addResponsesStreamChunk() error = %v", err)
		}
	}

	response := &schemas.BifrostResponse{
		ResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{
			Type:           schemas.ResponsesStreamResponseTypeCompleted,
			SequenceNumber: 3,
			Response: &schemas.BifrostResponsesResponse{
				ID: bifrost.Ptr("resp_monotonic_terminal"),
				Usage: &schemas.ResponsesResponseUsage{
					InputTokens:  11,
					OutputTokens: 7,
					TotalTokens:  18,
				},
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:            schemas.ResponsesStreamRequest,
				Provider:               schemas.OpenAI,
				OriginalModelRequested: "gpt-4o-mini",
				ChunkIndex:             3,
			},
		},
	}

	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)

	for call := 1; call <= 2; call++ {
		processed, err := accumulator.ProcessStreamingResponse(ctx, response, nil)
		if err != nil {
			t.Fatalf("ProcessStreamingResponse() call %d error = %v", call, err)
		}
		if processed == nil || processed.Data == nil || processed.Data.TokenUsage == nil || processed.Data.TokenUsage.TotalTokens != 18 {
			t.Fatalf("call %d: expected terminal usage 18 total tokens, got %+v", call, processed)
		}
	}

	streamAccumulator := accumulator.getOrCreateStreamAccumulator(requestID)
	streamAccumulator.mu.Lock()
	defer streamAccumulator.mu.Unlock()
	completedChunks := 0
	for _, chunk := range streamAccumulator.ResponsesStreamChunks {
		if chunk.StreamResponse != nil && chunk.StreamResponse.Type == schemas.ResponsesStreamResponseTypeCompleted {
			completedChunks++
		}
	}
	if completedChunks != 1 {
		t.Fatalf("expected exactly one terminal chunk after duplicate delivery of a unique-index final chunk, got %d", completedChunks)
	}
	if got := len(streamAccumulator.ResponsesStreamChunks); got != 4 {
		t.Fatalf("expected 4 stored chunks (3 deltas + 1 terminal), got %d", got)
	}
}

func TestProcessStreamingResponseSupportsWebSocketResponsesRequest(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelDebug)
	accumulator := NewAccumulator(nil, logger)

	requestID := "test-ws-responses-request"
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyAccumulatorID, requestID)
	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)

	chunk := &ResponsesStreamChunk{
		ChunkIndex: 0,
		Timestamp:  time.Now(),
		StreamResponse: &schemas.BifrostResponsesStreamResponse{
			Type: "response.completed",
			Response: &schemas.BifrostResponsesResponse{
				Usage: &schemas.ResponsesResponseUsage{
					InputTokens:  12,
					OutputTokens: 7,
					TotalTokens:  19,
				},
			},
		},
		TokenUsage: &schemas.BifrostLLMUsage{
			PromptTokens:     12,
			CompletionTokens: 7,
			TotalTokens:      19,
		},
	}
	if err := accumulator.addResponsesStreamChunk(requestID, chunk, true); err != nil {
		t.Fatalf("addResponsesStreamChunk() error = %v", err)
	}

	responseID := "resp_ws_123"
	response := &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			ID: &responseID,
			Usage: &schemas.ResponsesResponseUsage{
				InputTokens:  12,
				OutputTokens: 7,
				TotalTokens:  19,
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType:            schemas.WebSocketResponsesRequest,
				Provider:               schemas.OpenAI,
				OriginalModelRequested: "gpt-4o-mini",
				ChunkIndex:             0,
			},
		},
	}

	processed, err := accumulator.ProcessStreamingResponse(ctx, response, nil)
	if err != nil {
		t.Fatalf("ProcessStreamingResponse() error = %v", err)
	}
	if processed == nil {
		t.Fatal("expected processed response, got nil")
	}
	if processed.Data == nil || processed.Data.TokenUsage == nil {
		t.Fatal("expected token usage to be accumulated for websocket responses")
	}
	if processed.Data.TokenUsage.TotalTokens != 19 {
		t.Fatalf("expected total tokens 19, got %d", processed.Data.TokenUsage.TotalTokens)
	}
}

// newChatChunkResponse builds a chat streaming chunk mirroring what the
// OpenAI-compatible provider handler emits (content in the delta, an optional
// finish_reason on the choice, an optional usage block, and a chunk index).
func newChatChunkResponse(idx int, content string, finishReason *string, usage *schemas.BifrostLLMUsage) *schemas.BifrostResponse {
	delta := &schemas.ChatStreamResponseChoiceDelta{}
	if content != "" {
		delta.Content = bifrost.Ptr(content)
	}
	return &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ID:     "msg_finish",
			Object: "chat.completion.chunk",
			Choices: []schemas.BifrostResponseChoice{{
				ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{Delta: delta},
				FinishReason:             finishReason,
			}},
			Usage: usage,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ChatCompletionStreamRequest,
				Provider:    schemas.OpenAI,
				ChunkIndex:  idx,
			},
		},
	}
}

// newChatChoicesChunkResponse builds a chat streaming event carrying the given choices, the way n>1 streams send them.
func newChatChoicesChunkResponse(idx int, usage *schemas.BifrostLLMUsage, choices ...schemas.BifrostResponseChoice) *schemas.BifrostResponse {
	return &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			ID:      "msg_choices",
			Object:  "chat.completion.chunk",
			Choices: choices,
			Usage:   usage,
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ChatCompletionStreamRequest,
				Provider:    schemas.OpenAI,
				ChunkIndex:  idx,
			},
		},
	}
}

// chatStreamChoice builds one streamed chat choice with the given content and finish reason.
func chatStreamChoice(index int, content string, finishReason *string) schemas.BifrostResponseChoice {
	delta := &schemas.ChatStreamResponseChoiceDelta{}
	if content != "" {
		delta.Content = bifrost.Ptr(content)
	}
	return schemas.BifrostResponseChoice{
		Index:                    index,
		FinishReason:             finishReason,
		ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{Delta: delta},
	}
}

// chatToolCallChoice builds one streamed chat choice carrying a tool call fragment at tool index 0.
func chatToolCallChoice(index int, name *string, arguments string) schemas.BifrostResponseChoice {
	return schemas.BifrostResponseChoice{
		Index: index,
		ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{Delta: &schemas.ChatStreamResponseChoiceDelta{
			ToolCalls: []schemas.ChatAssistantMessageToolCall{{
				Index:    0,
				Function: schemas.ChatAssistantMessageToolCallFunction{Name: name, Arguments: arguments},
			}},
		}},
	}
}

// chatChoiceContent returns the assembled content of a non-streaming chat choice, or "" when it has none.
func chatChoiceContent(choice schemas.BifrostResponseChoice) string {
	if choice.ChatNonStreamResponseChoice == nil || choice.ChatNonStreamResponseChoice.Message == nil {
		return ""
	}
	content := choice.ChatNonStreamResponseChoice.Message.Content
	if content == nil || content.ContentStr == nil {
		return ""
	}
	return *content.ContentStr
}

// TestChatStreamingKeepsChoicesSeparate covers an n=2 chat stream whose choices interleave, as OpenAI sends them. Each
// choice must keep its own content, tool call, finish reason and logprobs, and the top-level output must describe
// choice 0 only.
func TestChatStreamingKeepsChoicesSeparate(t *testing.T) {
	accumulator := NewAccumulator(nil, bifrost.NewDefaultLogger(schemas.LogLevelError))
	t.Cleanup(accumulator.Cleanup)
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyAccumulatorID, "chat-n2")

	firstHello := chatStreamChoice(0, "Hello ", nil)
	firstHello.LogProbs = &schemas.BifrostLogProbs{Content: []schemas.ContentLogProb{{Token: "Hello"}}}
	secondSecret := chatStreamChoice(1, "SECRET ", nil)
	secondSecret.LogProbs = &schemas.BifrostLogProbs{Content: []schemas.ContentLogProb{{Token: "SECRET"}}}
	events := []*schemas.BifrostResponse{
		newChatChoicesChunkResponse(0, nil, firstHello),
		newChatChoicesChunkResponse(1, nil, secondSecret),
		newChatChoicesChunkResponse(2, nil, chatStreamChoice(0, "world", nil)),
		newChatChoicesChunkResponse(3, nil, chatStreamChoice(1, "PASSWORD", nil)),
		newChatChoicesChunkResponse(4, nil, chatToolCallChoice(0, bifrost.Ptr("get_weather"), `{"city":`)),
		newChatChoicesChunkResponse(5, nil, chatToolCallChoice(1, bifrost.Ptr("send_email"), `{"to":`)),
		newChatChoicesChunkResponse(6, nil, chatToolCallChoice(0, nil, `"Paris"}`)),
		newChatChoicesChunkResponse(7, nil, chatToolCallChoice(1, nil, `"bob"}`)),
		newChatChoicesChunkResponse(8, nil, chatStreamChoice(0, "", bifrost.Ptr("stop"))),
		newChatChoicesChunkResponse(9, nil, chatStreamChoice(1, "", bifrost.Ptr("length"))),
	}
	for _, event := range events {
		if _, err := accumulator.processChatStreamingResponse(ctx, event, nil); err != nil {
			t.Fatal(err)
		}
	}
	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	processed, err := accumulator.processChatStreamingResponse(ctx, newChatChoicesChunkResponse(10, &schemas.BifrostLLMUsage{TotalTokens: 12}), nil)
	if err != nil {
		t.Fatal(err)
	}

	data := processed.Data
	if got := *data.OutputMessage.Content.ContentStr; got != "Hello world" {
		t.Fatalf("OutputMessage content = %q, want choice 0's %q", got, "Hello world")
	}
	if data.FinishReason == nil || *data.FinishReason != "stop" {
		t.Fatalf("FinishReason = %v, want choice 0's stop", data.FinishReason)
	}
	if len(data.ToolCalls) != 1 || *data.ToolCalls[0].Function.Name != "get_weather" || data.ToolCalls[0].Function.Arguments != `{"city":"Paris"}` {
		t.Fatalf("ToolCalls = %+v, want only choice 0's get_weather call", data.ToolCalls)
	}
	if data.LogProbs == nil || len(data.LogProbs.Content) != 1 || data.LogProbs.Content[0].Token != "Hello" {
		t.Fatalf("LogProbs = %+v, want only choice 0's", data.LogProbs)
	}
	if data.TokenUsage == nil || data.TokenUsage.TotalTokens != 12 {
		t.Fatalf("TokenUsage = %+v, want the usage chunk's", data.TokenUsage)
	}

	choices := processed.ToBifrostResponse().ChatResponse.Choices
	if len(choices) != 2 {
		t.Fatalf("got %d choices, want 2", len(choices))
	}
	want := []struct {
		content, finishReason, toolName, toolArguments string
	}{
		{"Hello world", "stop", "get_weather", `{"city":"Paris"}`},
		{"SECRET PASSWORD", "length", "send_email", `{"to":"bob"}`},
	}
	for i, choice := range choices {
		if choice.Index != i {
			t.Fatalf("choice %d has index %d", i, choice.Index)
		}
		if got := chatChoiceContent(choice); got != want[i].content {
			t.Fatalf("choice %d content = %q, want %q", i, got, want[i].content)
		}
		if choice.FinishReason == nil || *choice.FinishReason != want[i].finishReason {
			t.Fatalf("choice %d finish reason = %v, want %q", i, choice.FinishReason, want[i].finishReason)
		}
		toolCalls := choice.ChatNonStreamResponseChoice.Message.ChatAssistantMessage.ToolCalls
		if len(toolCalls) != 1 || *toolCalls[0].Function.Name != want[i].toolName || toolCalls[0].Function.Arguments != want[i].toolArguments {
			t.Fatalf("choice %d tool calls = %+v, want one %s call with %s", i, toolCalls, want[i].toolName, want[i].toolArguments)
		}
	}
	if choices[1].LogProbs == nil || len(choices[1].LogProbs.Content) != 1 || choices[1].LogProbs.Content[0].Token != "SECRET" {
		t.Fatalf("choice 1 logprobs = %+v, want only its own", choices[1].LogProbs)
	}
}

// TestChatStreamingTopLevelOutputIsEmptyWithoutChoiceZero covers an n>1 stream that has only emitted a later choice
// (a paused snapshot taken before choice 0 spoke). The top-level output describes choice 0 alone, so it must stay
// empty instead of borrowing choice 1's content, tool call, finish reason or logprobs.
func TestChatStreamingTopLevelOutputIsEmptyWithoutChoiceZero(t *testing.T) {
	accumulator := NewAccumulator(nil, bifrost.NewDefaultLogger(schemas.LogLevelError))
	t.Cleanup(accumulator.Cleanup)
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyAccumulatorID, "chat-no-choice-zero")

	withLogProbs := chatStreamChoice(1, "SECRET ", nil)
	withLogProbs.LogProbs = &schemas.BifrostLogProbs{Content: []schemas.ContentLogProb{{Token: "SECRET"}}}
	for _, event := range []*schemas.BifrostResponse{
		newChatChoicesChunkResponse(0, nil, withLogProbs),
		newChatChoicesChunkResponse(1, nil, chatToolCallChoice(1, bifrost.Ptr("send_email"), `{"to":"bob"}`)),
	} {
		if _, err := accumulator.processChatStreamingResponse(ctx, event, nil); err != nil {
			t.Fatal(err)
		}
	}
	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	processed, err := accumulator.processChatStreamingResponse(ctx, newChatChoicesChunkResponse(2, nil, chatStreamChoice(1, "", bifrost.Ptr("length"))), nil)
	if err != nil {
		t.Fatal(err)
	}

	data := processed.Data
	if data.OutputMessage == nil || data.OutputMessage.Content == nil || data.OutputMessage.Content.ContentStr != nil || data.OutputMessage.ChatAssistantMessage != nil {
		t.Fatalf("OutputMessage = %+v, want an empty assistant message", data.OutputMessage)
	}
	if data.FinishReason != nil || data.LogProbs != nil || len(data.ToolCalls) != 0 {
		t.Fatalf("top-level output borrowed choice 1: finish %v, logprobs %+v, tool calls %+v", data.FinishReason, data.LogProbs, data.ToolCalls)
	}
	if len(data.OutputChoices) != 1 || data.OutputChoices[0].Index != 1 || *data.OutputChoices[0].Message.Content.ContentStr != "SECRET " {
		t.Fatalf("OutputChoices = %+v, want choice 1 kept under its own index", data.OutputChoices)
	}
	if *data.OutputChoices[0].FinishReason != "length" || data.OutputChoices[0].LogProbs == nil {
		t.Fatalf("choice 1 lost its own finish reason or logprobs: %+v", data.OutputChoices[0])
	}
}

// TestChatStreamingSplitsBundledChoices covers an event that carries several choices at once. Each choice is kept
// apart while the event's own fields (usage, raw response) are recorded once.
func TestChatStreamingSplitsBundledChoices(t *testing.T) {
	accumulator := NewAccumulator(nil, bifrost.NewDefaultLogger(schemas.LogLevelError))
	t.Cleanup(accumulator.Cleanup)
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyAccumulatorID, "chat-bundled")

	bundled := newChatChoicesChunkResponse(0, nil, chatStreamChoice(0, "first", nil), chatStreamChoice(1, "second", nil))
	bundled.ChatResponse.ExtraFields.RawResponse = "frame-0"
	if _, err := accumulator.processChatStreamingResponse(ctx, bundled, nil); err != nil {
		t.Fatal(err)
	}
	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	final := newChatChoicesChunkResponse(1, &schemas.BifrostLLMUsage{TotalTokens: 4},
		chatStreamChoice(0, "", bifrost.Ptr("stop")), chatStreamChoice(1, "", bifrost.Ptr("stop")))
	processed, err := accumulator.processChatStreamingResponse(ctx, final, nil)
	if err != nil {
		t.Fatal(err)
	}

	choices := processed.ToBifrostResponse().ChatResponse.Choices
	if len(choices) != 2 || chatChoiceContent(choices[0]) != "first" || chatChoiceContent(choices[1]) != "second" {
		t.Fatalf("choices = %+v, want first and second kept apart", choices)
	}
	if processed.Data.RawResponse == nil || *processed.Data.RawResponse != "frame-0" {
		t.Fatalf("RawResponse = %v, want the bundled frame once", processed.Data.RawResponse)
	}
	if processed.Data.TokenUsage == nil || processed.Data.TokenUsage.TotalTokens != 4 {
		t.Fatalf("TokenUsage = %+v, want 4 total tokens", processed.Data.TokenUsage)
	}
}

// TestTextStreamingKeepsChoicesSeparate covers an n=2 text completion stream: each choice keeps its own text and
// finish reason in the text completion response.
func TestTextStreamingKeepsChoicesSeparate(t *testing.T) {
	accumulator := NewAccumulator(nil, bifrost.NewDefaultLogger(schemas.LogLevelError))
	t.Cleanup(accumulator.Cleanup)
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyAccumulatorID, "text-n2")

	textEvent := func(idx, choiceIndex int, text string, finishReason *string) *schemas.BifrostResponse {
		return &schemas.BifrostResponse{TextCompletionResponse: &schemas.BifrostTextCompletionResponse{
			Choices: []schemas.BifrostResponseChoice{{
				Index:                        choiceIndex,
				FinishReason:                 finishReason,
				TextCompletionResponseChoice: &schemas.TextCompletionResponseChoice{Text: bifrost.Ptr(text)},
			}},
			ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.TextCompletionStreamRequest, ChunkIndex: idx},
		}}
	}
	for _, event := range []*schemas.BifrostResponse{
		textEvent(0, 0, "Hello ", nil),
		textEvent(1, 1, "SECRET ", nil),
		textEvent(2, 0, "world", bifrost.Ptr("stop")),
	} {
		if _, err := accumulator.processChatStreamingResponse(ctx, event, nil); err != nil {
			t.Fatal(err)
		}
	}
	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	processed, err := accumulator.processChatStreamingResponse(ctx, textEvent(3, 1, "PASSWORD", bifrost.Ptr("length")), nil)
	if err != nil {
		t.Fatal(err)
	}

	choices := processed.ToBifrostResponse().TextCompletionResponse.Choices
	want := []struct{ text, finishReason string }{{"Hello world", "stop"}, {"SECRET PASSWORD", "length"}}
	if len(choices) != len(want) {
		t.Fatalf("got %d choices, want %d", len(choices), len(want))
	}
	for i, choice := range choices {
		if choice.Index != i || *choice.Text != want[i].text || *choice.FinishReason != want[i].finishReason {
			t.Fatalf("choice %d = index %d, text %q, finish %q; want %q, %q", i, choice.Index, *choice.Text, *choice.FinishReason, want[i].text, want[i].finishReason)
		}
	}
}

// TestPutChatStreamChunkResetsEveryField guards the pool reset: a recycled chunk must not carry any field, including
// ones added to the struct later, into another request.
func TestPutChatStreamChunkResetsEveryField(t *testing.T) {
	accumulator := NewAccumulator(nil, bifrost.NewDefaultLogger(schemas.LogLevelError))
	t.Cleanup(accumulator.Cleanup)
	chunk := &ChatStreamChunk{
		Timestamp:      time.Now(),
		Delta:          &schemas.ChatStreamResponseChoiceDelta{},
		FinishReason:   bifrost.Ptr("stop"),
		LogProbs:       &schemas.BifrostLogProbs{},
		GuardrailDebug: &schemas.BifrostGuardrailMetadata{},
		ChunkIndex:     3,
		ChoiceIndex:    1,
		ExtraChoices:   []ChatStreamChunk{{ChoiceIndex: 2}},
	}
	accumulator.putChatStreamChunk(chunk)
	if !reflect.ValueOf(*chunk).IsZero() {
		t.Fatalf("chunk not fully reset: %+v", *chunk)
	}
}

// TestChatStreamingFinishReasonForwardedOnContentChunk covers providers that
// send finish_reason together with the final content delta. The provider
// forwards that terminal chunk as-is and then appends a synthetic tail chunk
// (higher index) whose finish_reason is nil to avoid a duplicate client-facing
// emission. The accumulated response must still keep the finish_reason rather
// than reading nil from the max-index tail chunk.
func TestChatStreamingFinishReasonForwardedOnContentChunk(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	accumulator := NewAccumulator(nil, logger)

	requestID := "finish-glued-request"
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyAccumulatorID, requestID)

	stop := "stop"
	usage := &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}

	if _, err := accumulator.processChatStreamingResponse(ctx, newChatChunkResponse(0, "Hello", nil, nil), nil); err != nil {
		t.Fatalf("chunk 0: %v", err)
	}
	// Terminal content chunk carries the finish_reason.
	if _, err := accumulator.processChatStreamingResponse(ctx, newChatChunkResponse(1, " world", &stop, nil), nil); err != nil {
		t.Fatalf("chunk 1: %v", err)
	}
	// Synthetic tail chunk: empty delta, nil finish_reason, usage, final chunk.
	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	processed, err := accumulator.processChatStreamingResponse(ctx, newChatChunkResponse(2, "", nil, usage), nil)
	if err != nil {
		t.Fatalf("final chunk: %v", err)
	}
	if processed == nil || processed.Data == nil {
		t.Fatal("expected accumulated data on final chunk")
	}
	if processed.Data.FinishReason == nil {
		t.Fatal("finish_reason was dropped from the accumulated response")
	}
	if *processed.Data.FinishReason != "stop" {
		t.Fatalf("accumulated finish_reason = %q, want %q", *processed.Data.FinishReason, "stop")
	}
}

// TestChatStreamingFinishReasonOnTerminalChunk is the standard OpenAI shape: the
// finish_reason arrives on the terminal (highest-index) chunk. This guards
// against the fallback over-reaching and changing the common path.
func TestChatStreamingFinishReasonOnTerminalChunk(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	accumulator := NewAccumulator(nil, logger)

	requestID := "finish-tail-request"
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyAccumulatorID, requestID)

	stop := "stop"
	usage := &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}

	if _, err := accumulator.processChatStreamingResponse(ctx, newChatChunkResponse(0, "Hello", nil, nil), nil); err != nil {
		t.Fatalf("chunk 0: %v", err)
	}
	if _, err := accumulator.processChatStreamingResponse(ctx, newChatChunkResponse(1, " world", nil, nil), nil); err != nil {
		t.Fatalf("chunk 1: %v", err)
	}
	// Terminal chunk carries the finish_reason and usage.
	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	processed, err := accumulator.processChatStreamingResponse(ctx, newChatChunkResponse(2, "", &stop, usage), nil)
	if err != nil {
		t.Fatalf("final chunk: %v", err)
	}
	if processed == nil || processed.Data == nil || processed.Data.FinishReason == nil {
		t.Fatal("expected finish_reason on the accumulated response")
	}
	if *processed.Data.FinishReason != "stop" {
		t.Fatalf("accumulated finish_reason = %q, want %q", *processed.Data.FinishReason, "stop")
	}
}

// A retried stream attempt reuses its request's accumulator, and its chunk indexes
// restart at 0. Reset must drop the failed attempt's chunks so the retry's are not
// discarded as duplicates, and keep the accumulator alive for the plugins that own it.
func TestResetStreamAccumulatorKeepsRetryChunks(t *testing.T) {
	accumulator := NewAccumulator(nil, bifrost.NewDefaultLogger(schemas.LogLevelError))
	requestID := "retried-request"
	acc := accumulator.CreateStreamAccumulator(requestID, time.Now())
	add := func(index int, content string) {
		t.Helper()
		chunk := &ChatStreamChunk{
			ChunkIndex: index,
			Timestamp:  time.Now(),
			Delta:      &schemas.ChatStreamResponseChoiceDelta{Content: new(content)},
		}
		if err := accumulator.addChatStreamChunk(requestID, StreamTypeChat, chunk, false); err != nil {
			t.Fatalf("failed to add chunk %d: %v", index, err)
		}
	}

	add(0, "failed-attempt")
	accumulator.ResetStreamAccumulator(requestID)
	add(0, "he")
	add(1, "llo")

	if current, _ := accumulator.streamAccumulators.Load(requestID); current != acc {
		t.Fatal("reset replaced the accumulator instead of clearing it")
	}
	if got := acc.refCount.Load(); got != 1 {
		t.Fatalf("refCount = %d, want 1: reset must keep the owners' references", got)
	}
	var content []string
	for _, chunk := range acc.ChatStreamChunks {
		content = append(content, *chunk.Delta.Content)
	}
	if len(content) != 2 || content[0] != "he" || content[1] != "llo" {
		t.Fatalf("accumulated chunks = %q, want the retry's [he llo]", content)
	}
}

// TestBuildChatMessageFromChunksWithoutAccumulator pins the exported builder used to rebuild
// recorded streams outside a live Accumulator: out-of-order chunks are sorted and content, reasoning
// and role are assembled exactly as the Accumulator method does.
func TestBuildChatMessageFromChunksWithoutAccumulator(t *testing.T) {
	makeChunks := func() []*ChatStreamChunk {
		return []*ChatStreamChunk{
			{ChunkIndex: 2, Delta: &schemas.ChatStreamResponseChoiceDelta{Content: schemas.Ptr("there")}},
			{ChunkIndex: 0, Delta: &schemas.ChatStreamResponseChoiceDelta{Role: schemas.Ptr(string(schemas.ChatMessageRoleAssistant)), Reasoning: schemas.Ptr("think")}},
			{ChunkIndex: 1, Delta: &schemas.ChatStreamResponseChoiceDelta{Content: schemas.Ptr("hi ")}},
		}
	}

	message := BuildChatMessageFromChunks(makeChunks())
	if message.Content == nil || message.Content.ContentStr == nil || *message.Content.ContentStr != "hi there" {
		t.Fatalf("unexpected content: %+v", message.Content)
	}
	if message.Role != schemas.ChatMessageRoleAssistant {
		t.Fatalf("unexpected role %q", message.Role)
	}
	if message.ChatAssistantMessage == nil || message.ChatAssistantMessage.Reasoning == nil || *message.ChatAssistantMessage.Reasoning != "think" {
		t.Fatalf("unexpected reasoning: %+v", message.ChatAssistantMessage)
	}

	accumulator := NewAccumulator(nil, bifrost.NewDefaultLogger(schemas.LogLevelError))
	if viaAccumulator := accumulator.buildCompleteMessageFromChatStreamChunks(makeChunks()); !reflect.DeepEqual(viaAccumulator, message) {
		t.Fatalf("exported builder diverged from Accumulator method:\n got %+v\nwant %+v", message, viaAccumulator)
	}
}

// Streamed delta.images (OpenRouter image models) must survive the delta copy and
// land on the accumulated message that logging and post-hooks read.
func TestBuildChatMessageFromChunksKeepsGeneratedImages(t *testing.T) {
	const imageURL = "data:image/png;base64,iVBORw0KGgo="
	frames := []string{
		`{"role":"assistant","content":"Here is a cat. "}`,
		`{"role":"assistant","content":"","images":[{"type":"image_url","image_url":{"url":"` + imageURL + `"}}]}`,
		`{"role":"assistant","content":"Enjoy!"}`,
	}
	chunks := make([]*ChatStreamChunk, 0, len(frames))
	for i, frame := range frames {
		var delta schemas.ChatStreamResponseChoiceDelta
		if err := schemas.Unmarshal([]byte(frame), &delta); err != nil {
			t.Fatalf("decode frame %d: %v", i, err)
		}
		chunks = append(chunks, &ChatStreamChunk{ChunkIndex: i, Delta: deepCopyChatStreamDelta(&delta)})
	}

	encoded, err := json.Marshal(BuildChatMessageFromChunks(chunks))
	if err != nil {
		t.Fatalf("encode message: %v", err)
	}
	var got struct {
		Content *string `json:"content"`
		Images  []struct {
			ImageURL struct {
				URL string `json:"url"`
			} `json:"image_url"`
		} `json:"images"`
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("re-decode %s: %v", encoded, err)
	}
	if got.Content == nil || *got.Content != "Here is a cat. Enjoy!" {
		t.Fatalf("unexpected content: %s", encoded)
	}
	if len(got.Images) != 1 || got.Images[0].ImageURL.URL != imageURL {
		t.Fatalf("streamed image missing from accumulated message: %s", encoded)
	}
}

// passthroughStreamChunk returns a passthrough stream chunk carrying body at chunkIndex.
func passthroughStreamChunk(chunkIndex int, body string) *schemas.BifrostResponse {
	return &schemas.BifrostResponse{PassthroughResponse: &schemas.BifrostPassthroughResponse{
		StatusCode:  200,
		Body:        []byte(body),
		ExtraFields: schemas.BifrostResponseExtraFields{RequestType: schemas.PassthroughStreamRequest, ChunkIndex: chunkIndex},
	}}
}

// TestPassthroughStreamKeepsEachChunkOnceInOrder verifies a passthrough body fed by several plugins, out of order, is
// accumulated once per chunk in ChunkIndex order, in the snapshot and on the final chunk, and that a reset lets a
// retried attempt start again from index 0.
func TestPassthroughStreamKeepsEachChunkOnceInOrder(t *testing.T) {
	accumulator := NewAccumulator(nil, bifrost.NewDefaultLogger(schemas.LogLevelError))
	t.Cleanup(accumulator.Cleanup)
	ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
	ctx.SetValue(schemas.BifrostContextKeyAccumulatorID, "passthrough-dedupe")
	feed := func(result *schemas.BifrostResponse) *ProcessedStreamResponse {
		t.Helper()
		processed, err := accumulator.processPassthroughStreamingResponse(ctx, result, nil)
		if err != nil {
			t.Fatal(err)
		}
		return processed
	}

	for _, chunk := range []*schemas.BifrostResponse{passthroughStreamChunk(1, "b"), passthroughStreamChunk(0, "a"), passthroughStreamChunk(1, "b"), passthroughStreamChunk(0, "a"), passthroughStreamChunk(2, "c")} {
		feed(chunk)
	}
	snapshot := accumulator.GetAccumulatedResponse("passthrough-dedupe")
	if snapshot == nil || snapshot.PassthroughResponse == nil || string(snapshot.PassthroughResponse.Body) != "abc" {
		t.Fatalf("snapshot = %+v, want body %q", snapshot, "abc")
	}
	if snapshot.PassthroughResponse.ExtraFields.RequestType != schemas.PassthroughStreamRequest {
		t.Fatalf("snapshot request type = %q", snapshot.PassthroughResponse.ExtraFields.RequestType)
	}

	accumulator.ResetStreamAccumulator("passthrough-dedupe")
	feed(passthroughStreamChunk(0, "x"))
	feed(passthroughStreamChunk(1, "y"))
	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	processed := feed(passthroughStreamChunk(2, ""))
	if processed.Data == nil || string(processed.Data.PassthroughOutput.Body) != "xy" {
		t.Fatalf("final body = %+v, want %q", processed.Data, "xy")
	}
}

// BenchmarkPassthroughStreamAccumulate measures accumulating a 1 MB passthrough stream (256 chunks of 4 KB, each fed by
// two plugins) and assembling its body on the final chunk.
func BenchmarkPassthroughStreamAccumulate(b *testing.B) {
	accumulator := NewAccumulator(nil, bifrost.NewDefaultLogger(schemas.LogLevelError))
	b.Cleanup(accumulator.Cleanup)
	chunks := make([]*schemas.BifrostResponse, 256)
	for index := range chunks {
		chunks[index] = passthroughStreamChunk(index, strings.Repeat("x", 4096))
	}
	final := passthroughStreamChunk(len(chunks), "")
	b.SetBytes(256 * 4096)
	b.ReportAllocs()
	for b.Loop() {
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
		ctx.SetValue(schemas.BifrostContextKeyAccumulatorID, "passthrough-bench")
		for _, chunk := range chunks {
			for range 2 {
				if _, err := accumulator.processPassthroughStreamingResponse(ctx, chunk, nil); err != nil {
					b.Fatal(err)
				}
			}
		}
		ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
		if _, err := accumulator.processPassthroughStreamingResponse(ctx, final, nil); err != nil {
			b.Fatal(err)
		}
		accumulator.ResetStreamAccumulator("passthrough-bench")
	}
}

// BenchmarkChatStreamAccumulate measures accumulating a single-choice chat stream (256 content chunks and a usage
// chunk) and building its final response, the common n=1 path.
func BenchmarkChatStreamAccumulate(b *testing.B) {
	accumulator := NewAccumulator(nil, bifrost.NewDefaultLogger(schemas.LogLevelError))
	b.Cleanup(accumulator.Cleanup)
	chunks := make([]*schemas.BifrostResponse, 256)
	for index := range chunks {
		chunks[index] = newChatChunkResponse(index, "token ", nil, nil)
	}
	final := newChatChunkResponse(len(chunks), "", bifrost.Ptr("stop"), &schemas.BifrostLLMUsage{TotalTokens: 256})
	b.ReportAllocs()
	for b.Loop() {
		ctx := schemas.NewBifrostContext(context.Background(), time.Time{})
		ctx.SetValue(schemas.BifrostContextKeyAccumulatorID, "chat-bench")
		for _, chunk := range chunks {
			if _, err := accumulator.processChatStreamingResponse(ctx, chunk, nil); err != nil {
				b.Fatal(err)
			}
		}
		ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
		processed, err := accumulator.processChatStreamingResponse(ctx, final, nil)
		if err != nil {
			b.Fatal(err)
		}
		processed.ToBifrostResponse()
		accumulator.ResetStreamAccumulator("chat-bench")
	}
}

// Streamed custom tool input accumulates into one custom call, as OpenAI sends it on chat.
func TestBuildChatMessageFromChunks_CustomToolCall(t *testing.T) {
	call := func(tc schemas.ChatAssistantMessageToolCall) *ChatStreamChunk {
		return &ChatStreamChunk{Delta: &schemas.ChatStreamResponseChoiceDelta{ToolCalls: []schemas.ChatAssistantMessageToolCall{tc}}}
	}
	chunks := []*ChatStreamChunk{
		call(schemas.ChatAssistantMessageToolCall{ID: bifrost.Ptr("call_1"), Type: bifrost.Ptr("custom"), Custom: &schemas.ChatAssistantMessageToolCallCustom{Name: bifrost.Ptr("code_exec")}}),
		call(schemas.ChatAssistantMessageToolCall{Custom: &schemas.ChatAssistantMessageToolCallCustom{Input: "print("}}),
		call(schemas.ChatAssistantMessageToolCall{Custom: &schemas.ChatAssistantMessageToolCallCustom{Input: "1)"}}),
	}
	message := BuildChatMessageFromChunks(chunks)
	if message.ChatAssistantMessage == nil || len(message.ChatAssistantMessage.ToolCalls) != 1 {
		t.Fatalf("tool calls not accumulated: %+v", message.ChatAssistantMessage)
	}
	tc := message.ChatAssistantMessage.ToolCalls[0]
	if tc.Custom == nil || tc.Custom.Name == nil || *tc.Custom.Name != "code_exec" || tc.Custom.Input != "print(1)" {
		t.Fatalf("custom call = %+v, want code_exec/print(1)", tc.Custom)
	}
	if body, _ := schemas.Marshal(message); strings.Contains(string(body), `"function"`) {
		t.Fatalf("custom call carries a function object: %s", body)
	}
}
