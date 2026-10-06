package semanticcache

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/vectorstore"
)

// -----------------------------------------------------------------------------
// Raw provider payloads (raw_request / raw_response) must not be persisted in
// cache entries nor replayed on a hit.
//
// They describe the upstream call made for the request that wrote the entry.
// A hit makes no upstream call, and the hitting request's own send-back policy
// is not known yet when the hit is served: core derives the drop flags per
// attempt in the request worker (applyRawCaptureSignals), after the pre-hooks
// have run. So the entry never keeps them, and replay clears any an older
// entry still carries.
// -----------------------------------------------------------------------------

// chatCacheResponse builds a minimal non-streaming chat response carrying the
// given raw payloads in ExtraFields.
func chatCacheResponse(text string, rawRequest, rawResponse json.RawMessage) *schemas.BifrostResponse {
	return &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Choices: []schemas.BifrostResponseChoice{
				{
					ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
						Message: &schemas.ChatMessage{
							Role:    schemas.ChatMessageRoleAssistant,
							Content: &schemas.ChatMessageContent{ContentStr: &text},
						},
					},
				},
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ChatCompletionRequest,
				RawRequest:  rawRequest,
				RawResponse: rawResponse,
			},
		},
	}
}

func TestPostLLMHook_RawFieldsAreNotCached(t *testing.T) {
	store := newObservableStore()
	plugin := newTestPlugin(t, store)

	// No drop flags: even a request that asked for the raw payloads back must
	// not leave them in an entry a later caller can hit.
	ctx := CreateContextWithCacheKeyAndType(t, "raw-unary", CacheTypeDirect)
	mustPreLLMHookMiss(t, plugin, ctx, &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: CreateBasicChatRequest("raw fields unary", 0.7, 50),
	})

	res := chatCacheResponse("cached answer",
		json.RawMessage(`{"upstream_request":true}`),
		json.RawMessage(`{"upstream_response":true}`),
	)
	if _, _, err := plugin.PostLLMHook(ctx, res, nil); err != nil {
		t.Fatalf("PostLLMHook failed: %v", err)
	}
	plugin.WaitForPendingOperations()

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.addIDs) != 1 {
		t.Fatalf("expected one cache write, got %d", len(store.addIDs))
	}
	payload, _ := store.chunks[store.addIDs[0]].Properties["response"].(string)
	if strings.Contains(payload, `"raw_request"`) || strings.Contains(payload, `"raw_response"`) {
		t.Fatalf("cache entry persisted raw fields; stored payload: %s", payload)
	}
	if !strings.Contains(payload, "cached answer") {
		t.Fatalf("cache entry lost the response body while dropping raw fields; stored payload: %s", payload)
	}
}

func TestPostLLMHook_RawFieldsAreNotCachedInStreamChunks(t *testing.T) {
	store := newObservableStore()
	plugin := newTestPlugin(t, store)

	ctx := CreateContextWithCacheKeyAndType(t, "raw-stream", CacheTypeDirect)
	mustPreLLMHookMiss(t, plugin, ctx, &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionStreamRequest,
		ChatRequest: CreateBasicChatRequest("raw fields stream", 0.7, 50),
	})

	chunks := []*schemas.BifrostResponse{
		newChatStreamChunk(0, "chunk zero", json.RawMessage(`{"chunk":0}`)),
		newChatStreamChunk(1, "chunk one", json.RawMessage(`{"chunk":1}`)),
	}
	chunks[1].ChatResponse.ExtraFields.RawRequest = json.RawMessage(`{"upstream_request":true}`)
	for i, chunk := range chunks {
		ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, i == len(chunks)-1)
		if _, _, err := plugin.PostLLMHook(ctx, chunk, nil); err != nil {
			t.Fatalf("PostLLMHook failed for chunk %d: %v", i, err)
		}
	}
	plugin.WaitForPendingOperations()

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.addIDs) != 1 {
		t.Fatalf("expected one cache write for the flushed stream, got %d", len(store.addIDs))
	}
	cached, ok := store.chunks[store.addIDs[0]].Properties["stream_chunks"].([]string)
	if !ok || len(cached) != len(chunks) {
		t.Fatalf("expected %d cached stream chunks, got %v", len(chunks), store.chunks[store.addIDs[0]].Properties["stream_chunks"])
	}
	for i, c := range cached {
		if strings.Contains(c, `"raw_request"`) || strings.Contains(c, `"raw_response"`) {
			t.Fatalf("cached stream chunk %d persisted raw fields: %s", i, c)
		}
	}
}

func TestPostLLMHook_LeavesLiveResponseRawFieldsUntouched(t *testing.T) {
	store := newObservableStore()
	plugin := newTestPlugin(t, store)

	ctx := CreateContextWithCacheKeyAndType(t, "raw-live", CacheTypeDirect)
	mustPreLLMHookMiss(t, plugin, ctx, &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: CreateBasicChatRequest("live response untouched", 0.7, 50),
	})

	res := chatCacheResponse("cached answer",
		json.RawMessage(`{"upstream_request":true}`),
		json.RawMessage(`{"upstream_response":true}`),
	)
	got, _, err := plugin.PostLLMHook(ctx, res, nil)
	if err != nil {
		t.Fatalf("PostLLMHook failed: %v", err)
	}
	if got != res {
		t.Fatal("PostLLMHook returned a different response object")
	}
	// Core still needs the raw fields for logging and decides what the client
	// gets after the post-hook chain; the plugin must not have nil'd them.
	ef := res.GetExtraFields()
	if ef.RawRequest == nil || ef.RawResponse == nil {
		t.Fatalf("PostLLMHook mutated the live response's raw fields: %+v", ef)
	}
	plugin.WaitForPendingOperations()
}

// seededRawEntry is an entry that still carries raw payloads, as written by a
// version without the write-side strip.
func seededRawEntry(t *testing.T) vectorstore.SearchResult {
	t.Helper()
	entryJSON, err := json.Marshal(chatCacheResponse("cached answer",
		json.RawMessage(`{"upstream_request":true}`),
		json.RawMessage(`{"upstream_response":true}`),
	))
	if err != nil {
		t.Fatalf("failed to build seeded entry: %v", err)
	}
	return vectorstore.SearchResult{
		ID: "raw-entry-1",
		Properties: map[string]interface{}{
			"response":   string(entryJSON),
			"expires_at": time.Now().Add(time.Hour).Unix(),
		},
	}
}

func TestCacheHit_ReplayDropsStoredRawFields(t *testing.T) {
	plugin := newTestPlugin(t, newObservableStore())

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: CreateBasicChatRequest("raw fields replay", 0.7, 50),
	}
	// A plain context, as a hit sees it: core has not derived the drop flags
	// for this request yet, so replay cannot rely on them.
	ctx := newBaseTestContext()

	sc, err := plugin.buildResponseFromResult(ctx, &cacheState{}, req, seededRawEntry(t), CacheTypeDirect, nil, nil)
	if err != nil {
		t.Fatalf("buildResponseFromResult failed: %v", err)
	}
	if sc == nil || sc.Response == nil {
		t.Fatal("expected a non-stream short-circuit on replay")
	}
	ef := sc.Response.GetExtraFields()
	if ef.RawRequest != nil || ef.RawResponse != nil {
		t.Fatalf("replay handed stored raw fields to a different request: raw_request=%s raw_response=%s", ef.RawRequest, ef.RawResponse)
	}
	if text := sc.Response.ChatResponse.Choices[0].Message.Content.ContentStr; text == nil || *text != "cached answer" {
		t.Fatalf("replay lost the response body: %v", text)
	}
}

func TestStreamHit_ReplayDropsStoredRawFields(t *testing.T) {
	plugin := newTestPlugin(t, newObservableStore())

	var streamArray []string
	for i, text := range []string{"chunk zero", "chunk one"} {
		chunkJSON, err := json.Marshal(newChatStreamChunk(i, text, json.RawMessage(fmt.Sprintf(`{"chunk":%d}`, i))))
		if err != nil {
			t.Fatalf("failed to build seeded stream chunk: %v", err)
		}
		streamArray = append(streamArray, string(chunkJSON))
	}

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionStreamRequest,
		ChatRequest: CreateBasicChatRequest("raw fields replay stream", 0.7, 50),
	}
	ctx := newBaseTestContext()

	sc, err := plugin.buildStreamingResponseFromResult(
		ctx, &cacheState{}, req,
		vectorstore.SearchResult{ID: "raw-stream-entry-1"},
		streamArray, CacheTypeDirect, nil, nil, nil,
	)
	if err != nil {
		t.Fatalf("buildStreamingResponseFromResult failed: %v", err)
	}
	if sc == nil || sc.Stream == nil {
		t.Fatal("expected a stream short-circuit on replay")
	}
	seen := 0
	for chunk := range sc.Stream {
		if chunk == nil || chunk.BifrostChatResponse == nil {
			continue
		}
		seen++
		ef := chunk.BifrostChatResponse.ExtraFields
		if ef.RawRequest != nil || ef.RawResponse != nil {
			t.Fatalf("stream replay handed stored raw fields to a different request: %+v", ef)
		}
	}
	if seen != len(streamArray) {
		t.Fatalf("expected %d replayed chunks, got %d", len(streamArray), seen)
	}
}
