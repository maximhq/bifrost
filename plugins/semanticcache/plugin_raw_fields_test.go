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
// Client-stripped raw payloads (raw_request / raw_response) must not be
// persisted in cache entries nor replayed to a later request's client.
//
// When a deployment captures raw provider payloads for internal logging only
// (store_raw_request_response on, send_back_raw_* off), core sets
// BifrostContextKeyDropRawRequestFromClient /
// BifrostContextKeyDropRawResponseFromClient on the request context and nils
// ExtraFields.RawRequest/RawResponse on the delivered response AFTER the
// post-hook chain returns (core/bifrost.go, applyRawCaptureSignals +
// tryRequest onResult). PostLLMHook serializes inside the hook, before that
// strip lands, so the serialized snapshot must not keep the fields — a cache
// entry is replayed verbatim on a hit, and would hand logging-only payloads
// to a different request's client.
// -----------------------------------------------------------------------------

// dropRawContext builds a cache-keyed test context carrying both drop flags,
// mirroring a request whose raw payloads are captured for logging only.
func dropRawContext(t testing.TB, suffix string) *schemas.BifrostContext {
	t.Helper()
	ctx := CreateContextWithCacheKeyAndType(t, suffix, CacheTypeDirect)
	ctx.SetValue(schemas.BifrostContextKeyDropRawRequestFromClient, true)
	ctx.SetValue(schemas.BifrostContextKeyDropRawResponseFromClient, true)
	return ctx
}

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

func TestPostLLMHook_StoreOnlyRawFieldsAreNotCached(t *testing.T) {
	store := newObservableStore()
	plugin := newTestPlugin(t, store)

	ctx := dropRawContext(t, "drop-raw-unary")
	mustPreLLMHookMiss(t, plugin, ctx, &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: CreateBasicChatRequest("raw fields drop", 0.7, 50),
	})

	res := chatCacheResponse("cached answer",
		json.RawMessage(`{"upstream_request":true}`),
		json.RawMessage(`{"upstream_response":true}`),
	)
	if _, _, err := plugin.PostLLMHook(ctx, res, nil); err != nil {
		t.Fatalf("PostLLMHook failed: %v", err)
	}

	// Core performs this cleanup after the post-hook chain returns
	// (core/bifrost.go onResult). The stored bytes are already written by
	// then, so this only exercises the live response.
	res.ChatResponse.ExtraFields.RawRequest = nil
	res.ChatResponse.ExtraFields.RawResponse = nil

	plugin.WaitForPendingOperations()

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.addIDs) != 1 {
		t.Fatalf("expected one cache write, got %d", len(store.addIDs))
	}
	payload, _ := store.chunks[store.addIDs[0]].Properties["response"].(string)
	if strings.Contains(payload, `"raw_request"`) || strings.Contains(payload, `"raw_response"`) {
		t.Fatalf("cache entry persisted raw fields the client must never see; stored payload: %s", payload)
	}
	if !strings.Contains(payload, "cached answer") {
		t.Fatalf("cache entry lost the response body while dropping raw fields; stored payload: %s", payload)
	}
}

func TestPostLLMHook_StoreOnlyRawFieldsAreNotCachedInStreamChunks(t *testing.T) {
	store := newObservableStore()
	plugin := newTestPlugin(t, store)

	ctx := dropRawContext(t, "drop-raw-stream")
	mustPreLLMHookMiss(t, plugin, ctx, &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionStreamRequest,
		ChatRequest: CreateBasicChatRequest("raw fields drop stream", 0.7, 50),
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
		// Mirror core's post-hook cleanup on the delivered chunk.
		chunk.ChatResponse.ExtraFields.RawRequest = nil
		chunk.ChatResponse.ExtraFields.RawResponse = nil
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
			t.Fatalf("cached stream chunk %d persisted raw fields the client must never see: %s", i, c)
		}
	}
}

func TestPostLLMHook_PartialDropKeyStripsOnlyThatSide(t *testing.T) {
	store := newObservableStore()
	plugin := newTestPlugin(t, store)

	// Only the response side is marked for strip; the request side is sent
	// back, so raw_request must stay in the entry while raw_response is gone.
	ctx := CreateContextWithCacheKeyAndType(t, "drop-raw-partial", CacheTypeDirect)
	ctx.SetValue(schemas.BifrostContextKeyDropRawResponseFromClient, true)
	mustPreLLMHookMiss(t, plugin, ctx, &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: CreateBasicChatRequest("raw fields partial", 0.7, 50),
	})

	res := chatCacheResponse("cached answer",
		json.RawMessage(`{"upstream_request":true}`),
		json.RawMessage(`{"upstream_response":true}`),
	)
	if _, _, err := plugin.PostLLMHook(ctx, res, nil); err != nil {
		t.Fatalf("PostLLMHook failed: %v", err)
	}
	res.ChatResponse.ExtraFields.RawRequest = nil
	res.ChatResponse.ExtraFields.RawResponse = nil
	plugin.WaitForPendingOperations()

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.addIDs) != 1 {
		t.Fatalf("expected one cache write, got %d", len(store.addIDs))
	}
	payload, _ := store.chunks[store.addIDs[0]].Properties["response"].(string)
	if strings.Contains(payload, `"raw_response"`) {
		t.Fatalf("cache entry kept the client-stripped raw_response; stored payload: %s", payload)
	}
	if !strings.Contains(payload, `"raw_request":{"upstream_request":true}`) {
		t.Fatalf("cache entry lost the send-back raw_request; stored payload: %s", payload)
	}
}

func TestPostLLMHook_SendBackRawFieldsStillCachedAndReplayed(t *testing.T) {
	store := newObservableStore()
	plugin := newTestPlugin(t, store)

	// No drop keys: this request asked for the raw payloads back, so they
	// belong in the cache entry and in a later replay.
	ctx := CreateContextWithCacheKeyAndTTL(t, "keep-raw-unary", time.Hour)
	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: CreateBasicChatRequest("raw fields keep", 0.7, 50),
	}
	mustPreLLMHookMiss(t, plugin, ctx, req)

	res := chatCacheResponse("cached answer",
		json.RawMessage(`{"upstream_request":true}`),
		json.RawMessage(`{"upstream_response":true}`),
	)
	if _, _, err := plugin.PostLLMHook(ctx, res, nil); err != nil {
		t.Fatalf("PostLLMHook failed: %v", err)
	}
	plugin.WaitForPendingOperations()

	store.mu.Lock()
	entry := store.chunks[store.addIDs[0]]
	store.mu.Unlock()
	payload, _ := entry.Properties["response"].(string)
	if !strings.Contains(payload, `"raw_request":{"upstream_request":true}`) {
		t.Fatalf("send-back request lost its cached raw_request; stored payload: %s", payload)
	}
	if !strings.Contains(payload, `"raw_response":{"upstream_response":true}`) {
		t.Fatalf("send-back request lost its cached raw_response; stored payload: %s", payload)
	}

	// A later request without drop flags replays the stored raws unchanged.
	hitCtx := newBaseTestContext()
	sc, err := plugin.buildResponseFromResult(hitCtx, &cacheState{}, req, entry, CacheTypeDirect, nil, nil)
	if err != nil {
		t.Fatalf("buildResponseFromResult failed: %v", err)
	}
	if sc == nil || sc.Response == nil {
		t.Fatal("expected a non-stream short-circuit on replay")
	}
	ef := sc.Response.GetExtraFields()
	if ef.RawRequest == nil || ef.RawResponse == nil {
		t.Fatalf("replay dropped raw fields for a send-back request: %+v", ef)
	}
}

func TestPostLLMHook_DropKeysLeaveLiveResponseUntouched(t *testing.T) {
	store := newObservableStore()
	plugin := newTestPlugin(t, store)

	ctx := dropRawContext(t, "drop-raw-live")
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
	// Core still needs the raw fields for logging and performs its own strip
	// after the post-hook chain — the plugin must not have nil'd them.
	ef := res.GetExtraFields()
	if ef.RawRequest == nil || ef.RawResponse == nil {
		t.Fatalf("PostLLMHook mutated the live response's raw fields: %+v", ef)
	}
	plugin.WaitForPendingOperations()
}

func TestCacheHit_DropsStoredRawFieldsWhenRequestMarkedForStrip(t *testing.T) {
	plugin := newTestPlugin(t, newObservableStore())

	// Seed an entry that carries raw payloads — written by a request that
	// sent them back (or by a version without the write-side strip).
	entryJSON, err := json.Marshal(chatCacheResponse("cached answer",
		json.RawMessage(`{"upstream_request":true}`),
		json.RawMessage(`{"upstream_response":true}`),
	))
	if err != nil {
		t.Fatalf("failed to build seeded entry: %v", err)
	}
	entry := vectorstore.SearchResult{
		ID: "raw-entry-1",
		Properties: map[string]interface{}{
			"response":   string(entryJSON),
			"expires_at": time.Now().Add(time.Hour).Unix(),
		},
	}

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: CreateBasicChatRequest("raw fields drop", 0.7, 50),
	}
	ctx := newBaseTestContext()
	ctx.SetValue(schemas.BifrostContextKeyDropRawRequestFromClient, true)
	ctx.SetValue(schemas.BifrostContextKeyDropRawResponseFromClient, true)

	sc, err := plugin.buildResponseFromResult(ctx, &cacheState{}, req, entry, CacheTypeDirect, nil, nil)
	if err != nil {
		t.Fatalf("buildResponseFromResult failed: %v", err)
	}
	if sc == nil || sc.Response == nil {
		t.Fatal("expected a non-stream short-circuit on replay")
	}
	ef := sc.Response.GetExtraFields()
	if ef.RawRequest != nil || ef.RawResponse != nil {
		t.Fatalf("replay handed raw fields to a client-stripped request: raw_request=%v raw_response=%v", ef.RawRequest, ef.RawResponse)
	}
}

func TestStreamHit_DropsStoredRawFieldsWhenRequestMarkedForStrip(t *testing.T) {
	plugin := newTestPlugin(t, newObservableStore())

	// Same as the non-stream case, on the streaming replay path.
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
		ChatRequest: CreateBasicChatRequest("raw fields drop stream", 0.7, 50),
	}
	ctx := newBaseTestContext()
	ctx.SetValue(schemas.BifrostContextKeyDropRawRequestFromClient, true)
	ctx.SetValue(schemas.BifrostContextKeyDropRawResponseFromClient, true)

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
	for chunk := range sc.Stream {
		if chunk == nil || chunk.BifrostChatResponse == nil {
			continue
		}
		ef := chunk.BifrostChatResponse.ExtraFields
		if ef.RawRequest != nil || ef.RawResponse != nil {
			t.Fatalf("stream replay handed raw fields to a client-stripped request: %+v", ef)
		}
	}
}
