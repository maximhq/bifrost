package streaming

import (
	"context"
	"fmt"
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
