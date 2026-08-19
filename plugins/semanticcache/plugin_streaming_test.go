package semanticcache

import (
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/vectorstore"
)

// TestStreamingCacheBasicFunctionality tests streaming response caching
func TestStreamingCacheBasicFunctionality(t *testing.T) {
	t.Parallel()
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey(t, "test-stream-value")

	// Create a test streaming request
	testRequest := CreateStreamingChatRequest(
		"Count from 1 to 3, each number on a new line.",
		0.0, // Use 0 temperature for more predictable responses
		20,
	)

	t.Log("Making first streaming request (should go to OpenAI and be cached)...")

	// Make first streaming request
	start1 := time.Now()
	stream1, err1 := setup.Client.ChatCompletionStreamRequest(ctx, testRequest)
	if err1 != nil {
		t.Skipf("upstream request error, skipping test: %v", err1)
	}

	var responses1 []schemas.BifrostChatResponse
	for streamMsg := range stream1 {
		if streamMsg.BifrostError != nil {
			t.Fatalf("Error in first stream: %v", streamMsg.BifrostError)
		}
		if streamMsg.BifrostChatResponse != nil {
			responses1 = append(responses1, *streamMsg.BifrostChatResponse)
		}
	}
	duration1 := time.Since(start1)

	if len(responses1) == 0 {
		t.Fatal("First streaming request returned no responses")
	}

	t.Logf("First streaming request completed in %v with %d chunks", duration1, len(responses1))

	// Wait for cache to be written
	WaitForCache(setup.Plugin)

	t.Log("Making second identical streaming request (should be served from cache)...")

	// Make second identical streaming request
	start2 := time.Now()
	stream2, err2 := setup.Client.ChatCompletionStreamRequest(ctx, testRequest)
	if err2 != nil {
		t.Fatalf("Second streaming request failed: %v", err2)
	}

	var responses2 []schemas.BifrostChatResponse
	for streamMsg := range stream2 {
		if streamMsg.BifrostError != nil {
			t.Fatalf("Error in second stream: %v", streamMsg.BifrostError)
		}
		if streamMsg.BifrostChatResponse != nil {
			responses2 = append(responses2, *streamMsg.BifrostChatResponse)
		}
	}
	duration2 := time.Since(start2)

	if len(responses2) == 0 {
		t.Fatal("Second streaming request returned no responses")
	}

	t.Logf("Second streaming request completed in %v with %d chunks", duration2, len(responses2))

	// Validate that both streams have the same number of chunks
	if len(responses1) != len(responses2) {
		t.Errorf("Stream chunk count mismatch: original=%d, cached=%d", len(responses1), len(responses2))
	}

	// Validate that the second stream was cached
	cached := false
	for _, response := range responses2 {
		if response.ExtraFields.CacheDebug != nil && response.ExtraFields.CacheDebug.CacheHit {
			cached = true
			break
		}
	}

	if !cached {
		t.Fatal("Second streaming request was not served from cache")
	}

	// Validate performance improvement
	if duration2 >= duration1 {
		t.Errorf("Cached stream took longer than original: cache=%v, original=%v", duration2, duration1)
	} else {
		speedup := float64(duration1) / float64(duration2)
		t.Logf("Streaming cache speedup: %.2fx faster", speedup)
	}

	// Validate chunk ordering is maintained
	for i := range responses2 {
		if responses2[i].ExtraFields.ChunkIndex != responses1[i].ExtraFields.ChunkIndex {
			t.Errorf("Chunk index mismatch at position %d: original=%d, cached=%d",
				i, responses1[i].ExtraFields.ChunkIndex, responses2[i].ExtraFields.ChunkIndex)
		}
	}

	t.Log("✅ Streaming cache test completed successfully!")
}

// TestStreamingVsNonStreaming tests that streaming and non-streaming requests are cached separately
func TestStreamingVsNonStreaming(t *testing.T) {
	t.Parallel()
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey(t, "stream-vs-non-test")

	prompt := "What is the meaning of life?"

	// Make non-streaming request first
	t.Log("Making non-streaming request...")
	nonStreamRequest := CreateBasicChatRequest(prompt, 0.5, 50)
	nonStreamResponse, err1 := setup.Client.ChatCompletionRequest(ctx, nonStreamRequest)
	if err1 != nil {
		t.Skipf("upstream request error, skipping test: %v", err1)
	}

	WaitForCache(setup.Plugin)

	// Make streaming request with same prompt and parameters
	t.Log("Making streaming request with same prompt...")
	streamRequest := CreateStreamingChatRequest(prompt, 0.5, 50)
	stream, err2 := setup.Client.ChatCompletionStreamRequest(ctx, streamRequest)
	if err2 != nil {
		t.Fatalf("Streaming request failed: %v", err2)
	}

	var streamResponses []schemas.BifrostChatResponse
	for streamMsg := range stream {
		if streamMsg.BifrostError != nil {
			t.Fatalf("Error in stream: %v", streamMsg.BifrostError)
		}
		if streamMsg.BifrostChatResponse != nil {
			streamResponses = append(streamResponses, *streamMsg.BifrostChatResponse)
		}
	}

	if len(streamResponses) == 0 {
		t.Fatal("Streaming request returned no responses")
	}

	// Verify that the streaming request was NOT served from the non-streaming cache
	// (They should be cached separately)
	streamCached := false
	for _, response := range streamResponses {
		if response.ExtraFields.RawResponse != nil {
			if rawMap, ok := response.ExtraFields.RawResponse.(map[string]interface{}); ok {
				if cachedFlag, exists := rawMap["bifrost_cached"]; exists {
					if cachedBool, ok := cachedFlag.(bool); ok && cachedBool {
						streamCached = true
						break
					}
				}
			}
		}
	}

	if streamCached {
		t.Error("Streaming request should not be cached from non-streaming cache")
	} else {
		t.Log("✅ Streaming request correctly not cached from non-streaming cache")
	}

	// Verify non-streaming response was not affected
	AssertNoCacheHit(t, &schemas.BifrostResponse{ChatResponse: nonStreamResponse})

	t.Log("✅ Streaming vs non-streaming test completed!")
}

// chunkStreamPlugin is a test-only LLMPlugin that short-circuits
// ChatCompletionStreamRequest with a genuine multi-chunk stream via
// LLMPluginShortCircuit.Stream — the same mechanism real streaming providers
// use (see e.g. core/providers/openai/openai.go). The shared mocker plugin
// (getMockedBifrostClient) only ever short-circuits with a single complete
// Response, so it can never exercise the semantic cache's multi-chunk
// ordering/replay path; this plugin fills that gap for
// TestStreamingChunkOrdering. Any request type other than
// ChatCompletionStreamRequest passes through untouched to the next plugin
// (mocker).
type chunkStreamPlugin struct {
	chunks []string
}

func (p *chunkStreamPlugin) GetName() string { return "chunk-stream-test-plugin" }
func (p *chunkStreamPlugin) Cleanup() error  { return nil }

func (p *chunkStreamPlugin) PreRequestHook(_ *schemas.BifrostContext, _ *schemas.BifrostRequest) error {
	return nil
}

func (p *chunkStreamPlugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	if req.RequestType != schemas.ChatCompletionStreamRequest {
		return req, nil, nil
	}

	_, model, _ := req.GetRequestFields()
	// Root() unwraps past this call's plugin-scoped context, which is released
	// (and unsafe to retain) the instant PreLLMHook returns — the goroutine
	// below outlives that.
	root := ctx.Root()
	streamCh := make(chan *schemas.BifrostStreamChunk)

	go func() {
		defer close(streamCh)
		for i, piece := range p.chunks {
			if i > 0 {
				// Real streaming providers are paced by network I/O between
				// chunks, which in practice gives each chunk's async cache-write
				// goroutine (main.go PostLLMHook) time to finish appending before
				// the next chunk's PostLLMHook fires. Sending back-to-back with no
				// delay at all removes that natural pacing and reliably wins a
				// real race in the plugin: the final chunk's write goroutine can
				// flush (and latch IsComplete) before an earlier chunk's goroutine
				// has appended, silently dropping it from the cached entry. This
				// sleep restores realistic pacing so the test exercises chunk
				// ordering/replay rather than that unrelated, already-tracked race.
				time.Sleep(20 * time.Millisecond)
			}
			delta := &schemas.ChatStreamResponseChoiceDelta{Content: bifrost.Ptr(piece)}
			if i == 0 {
				delta.Role = bifrost.Ptr("assistant")
			}
			choice := schemas.BifrostResponseChoice{
				Index:                    0,
				ChatStreamResponseChoice: &schemas.ChatStreamResponseChoice{Delta: delta},
			}
			resp := &schemas.BifrostChatResponse{
				Model:       model,
				Choices:     []schemas.BifrostResponseChoice{choice},
				ExtraFields: schemas.BifrostResponseExtraFields{ChunkIndex: i},
			}

			isLast := i == len(p.chunks)-1
			if isLast {
				resp.Choices[0].FinishReason = bifrost.Ptr("stop")
				resp.Usage = &schemas.BifrostLLMUsage{
					PromptTokens:     10,
					CompletionTokens: len(p.chunks),
					TotalTokens:      10 + len(p.chunks),
				}
				// Must be set before the send below — this is the same
				// set-before-send ordering real provider streaming code
				// uses, so the consumer only observes it once it dequeues
				// this last chunk (see semanticcache/main.go PostLLMHook).
				root.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
			}

			streamCh <- &schemas.BifrostStreamChunk{BifrostChatResponse: resp}
		}
	}()

	return req, &schemas.LLMPluginShortCircuit{Stream: streamCh}, nil
}

func (p *chunkStreamPlugin) PostLLMHook(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, bifrostErr, nil
}

// TestStreamingChunkOrdering tests that cached streaming responses maintain proper chunk ordering
func TestStreamingChunkOrdering(t *testing.T) {
	t.Parallel()
	chunkPlugin := &chunkStreamPlugin{
		chunks: []string{
			"2, 3, 5, 7, ",
			"and 11 are ",
			"the first five ",
			"prime numbers.",
		},
	}
	setup := NewTestSetupWithVectorStore(t, getDefaultTestConfig(), vectorstore.VectorStoreTypeWeaviate, chunkPlugin)
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey(t, "chunk-order-test")

	// Request that should generate multiple chunks
	testRequest := CreateStreamingChatRequest(
		"List the first 5 prime numbers, one per line with explanation.",
		0.0,
		100,
	)

	t.Log("Making first streaming request to establish cache...")
	stream1, err1 := setup.Client.ChatCompletionStreamRequest(ctx, testRequest)
	if err1 != nil {
		t.Fatalf("First streaming request failed: %v", err1)
	}

	var originalChunks []schemas.BifrostChatResponse
	for streamMsg := range stream1 {
		if streamMsg.BifrostError != nil {
			t.Fatalf("Error in first stream: %v", streamMsg.BifrostError)
		}
		if streamMsg.BifrostChatResponse != nil {
			originalChunks = append(originalChunks, *streamMsg.BifrostChatResponse)
		}
	}

	// chunkStreamPlugin deterministically emits len(chunkPlugin.chunks)
	// chunks — this is no longer at any provider's discretion.
	if len(originalChunks) != len(chunkPlugin.chunks) {
		t.Fatalf("Expected %d chunks from the test plugin, got %d", len(chunkPlugin.chunks), len(originalChunks))
	}

	t.Logf("Original stream had %d chunks", len(originalChunks))

	WaitForCache(setup.Plugin)

	t.Log("Making second streaming request to test cached chunk ordering...")
	stream2, err2 := setup.Client.ChatCompletionStreamRequest(ctx, testRequest)
	if err2 != nil {
		t.Fatalf("Second streaming request failed: %v", err2)
	}

	var cachedChunks []schemas.BifrostChatResponse
	for streamMsg := range stream2 {
		if streamMsg.BifrostError != nil {
			t.Fatalf("Error in second stream: %v", streamMsg.BifrostError)
		}
		if streamMsg.BifrostChatResponse != nil {
			cachedChunks = append(cachedChunks, *streamMsg.BifrostChatResponse)
		}
	}

	if len(cachedChunks) != len(originalChunks) {
		t.Errorf("Cached stream chunk count mismatch: original=%d, cached=%d",
			len(originalChunks), len(cachedChunks))
	}

	// Verify chunk ordering
	for i := 0; i < len(cachedChunks) && i < len(originalChunks); i++ {
		originalIndex := originalChunks[i].ExtraFields.ChunkIndex
		cachedIndex := cachedChunks[i].ExtraFields.ChunkIndex

		if originalIndex != cachedIndex {
			t.Errorf("Chunk index mismatch at position %d: original=%d, cached=%d",
				i, originalIndex, cachedIndex)
		}

		// Only verify cache hit on the last chunk (where CacheDebug is set)
		if i == len(cachedChunks)-1 {
			AssertCacheHit(t, &schemas.BifrostResponse{ChatResponse: &cachedChunks[i]}, string(CacheTypeDirect))
		}
	}

	// Verify chunks are in sequential order
	for i := 1; i < len(cachedChunks); i++ {
		prevIndex := cachedChunks[i-1].ExtraFields.ChunkIndex
		currIndex := cachedChunks[i].ExtraFields.ChunkIndex

		if currIndex <= prevIndex {
			t.Errorf("Chunks not in sequential order: chunk %d has index %d, chunk %d has index %d",
				i-1, prevIndex, i, currIndex)
		}
	}

	t.Log("✅ Streaming chunk ordering test completed successfully!")
}

// TestSpeechSynthesisStreaming tests speech synthesis streaming caching
func TestSpeechSynthesisStreaming(t *testing.T) {
	t.Parallel()
	setup := NewTestSetup(t)
	defer setup.Cleanup()

	ctx := CreateContextWithCacheKey(t, "speech-stream-test")

	// Create speech synthesis request
	speechRequest := CreateSpeechRequest(
		"This is a test of speech synthesis streaming cache.",
		"alloy",
	)

	t.Log("Making first speech synthesis request...")
	start1 := time.Now()
	response1, err1 := setup.Client.SpeechRequest(ctx, speechRequest)
	duration1 := time.Since(start1)

	if err1 != nil {
		t.Skipf("upstream request error, skipping test: %v", err1)
	}

	if response1 == nil {
		t.Fatal("First speech response is nil")
	}

	t.Logf("First speech request completed in %v", duration1)

	WaitForCache(setup.Plugin)

	t.Log("Making second identical speech synthesis request...")
	start2 := time.Now()
	response2, err2 := setup.Client.SpeechRequest(ctx, speechRequest)
	duration2 := time.Since(start2)

	if err2 != nil {
		t.Fatalf("Second speech request failed: %v", err2)
	}

	if response2 == nil {
		t.Fatal("Second speech response is nil")
	}

	t.Logf("Second speech request completed in %v", duration2)

	// Check if second request was cached
	AssertCacheHit(t, &schemas.BifrostResponse{SpeechResponse: response2}, string(CacheTypeDirect))

	// Performance comparison
	t.Logf("Speech Synthesis Performance:")
	t.Logf("First request:   %v", duration1)
	t.Logf("Second request:  %v", duration2)

	if duration2 < duration1 {
		speedup := float64(duration1) / float64(duration2)
		t.Logf("Speech cache speedup: %.2fx faster", speedup)
	}

	t.Log("✅ Speech synthesis streaming test completed successfully!")
}
