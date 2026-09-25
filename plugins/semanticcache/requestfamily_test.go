package semanticcache

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/vectorstore"
)

func TestRequestFamilyCoversSupportedTypes(t *testing.T) {
	tests := []struct {
		requestType schemas.RequestType
		family      string
	}{
		{schemas.TextCompletionRequest, "text_completions"},
		{schemas.TextCompletionStreamRequest, "text_completions"},
		{schemas.ChatCompletionRequest, "chat_completions"},
		{schemas.ChatCompletionStreamRequest, "chat_completions"},
		{schemas.ResponsesRequest, "responses"},
		{schemas.ResponsesStreamRequest, "responses"},
		{schemas.WebSocketResponsesRequest, "responses"},
		{schemas.SpeechRequest, "speech"},
		{schemas.SpeechStreamRequest, "speech"},
		{schemas.EmbeddingRequest, "embeddings"},
		{schemas.TranscriptionRequest, "transcriptions"},
		{schemas.TranscriptionStreamRequest, "transcriptions"},
		{schemas.ImageGenerationRequest, "image_generation"},
		{schemas.ImageGenerationStreamRequest, "image_generation"},
	}
	for _, tt := range tests {
		if got := requestFamily(tt.requestType); got != tt.family {
			t.Errorf("requestFamily(%q) = %q, want %q", tt.requestType, got, tt.family)
		}
		if !isSemanticCacheSupportedRequestType(tt.requestType) {
			t.Errorf("%q should be supported", tt.requestType)
		}
	}
	if got := requestFamily(schemas.PassthroughRequest); got != "" {
		t.Errorf("unsupported request family = %q", got)
	}
	if isSemanticCacheSupportedRequestType(schemas.PassthroughRequest) {
		t.Fatal("passthrough must remain unsupported")
	}
}

func familyTestRequest(family string, stream bool) *schemas.BifrostRequest {
	if family == "chat_completions" {
		typ := schemas.ChatCompletionRequest
		if stream {
			typ = schemas.ChatCompletionStreamRequest
		}
		request := CreateBasicChatRequest("same prompt", 0.5, 100)
		request.Params = nil
		return &schemas.BifrostRequest{RequestType: typ, ChatRequest: request}
	}
	typ := schemas.ResponsesRequest
	if stream {
		typ = schemas.ResponsesStreamRequest
	}
	request := CreateBasicResponsesRequest("same prompt", 0.5, 100)
	request.Model = "gpt-4o-mini"
	request.Params = nil
	return &schemas.BifrostRequest{RequestType: typ, ResponsesRequest: request}
}

func familyTestResponse(family string, stream bool) *schemas.BifrostResponse {
	typ := familyTestRequest(family, stream).RequestType
	extra := schemas.BifrostResponseExtraFields{RequestType: typ, Provider: schemas.OpenAI, OriginalModelRequested: "gpt-4o-mini"}
	if family == "chat_completions" {
		return &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{ExtraFields: extra}}
	}
	if stream {
		return &schemas.BifrostResponse{ResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{ExtraFields: extra}}
	}
	return &schemas.BifrostResponse{ResponsesResponse: &schemas.BifrostResponsesResponse{ExtraFields: extra}}
}

func checkFamilyHit(t *testing.T, plugin *Plugin, ctx *schemas.BifrostContext, req *schemas.BifrostRequest, wantHit bool) {
	t.Helper()
	_, hit, err := plugin.PreLLMHook(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if (hit != nil) != wantHit {
		t.Fatalf("%s: hit=%v, wantHit=%v", req.RequestType, hit != nil, wantHit)
	}
	if hit == nil {
		return
	}
	responses := requestFamily(req.RequestType) == "responses"
	if hit.Stream != nil {
		count := 0
		for chunk := range hit.Stream {
			count++
			if responses && chunk.BifrostResponsesStreamResponse == nil || !responses && chunk.BifrostChatResponse == nil {
				t.Fatalf("%s replay returned wrong stream dialect: %+v", req.RequestType, chunk)
			}
		}
		if count == 0 {
			t.Fatalf("%s replay returned no chunks", req.RequestType)
		}
		return
	}
	if hit.Response == nil || responses && hit.Response.ResponsesResponse == nil || !responses && hit.Response.ChatResponse == nil {
		t.Fatalf("%s replay returned wrong response dialect: %+v", req.RequestType, hit.Response)
	}
}

func storeFamilyResponse(t *testing.T, plugin *Plugin, ctx *schemas.BifrostContext, family string, stream bool) {
	t.Helper()
	if stream {
		ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	}
	if _, _, err := plugin.PostLLMHook(ctx, familyTestResponse(family, stream), nil); err != nil {
		t.Fatal(err)
	}
	plugin.WaitForPendingOperations()
}

func TestDirectReplayIsolatesChatAndResponsesInBothOrders(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, first := range []string{"chat_completions", "responses"} {
			t.Run(fmt.Sprintf("stream=%t/first=%s", stream, first), func(t *testing.T) {
				second := "responses"
				if first == "responses" {
					second = "chat_completions"
				}
				store := newObservableStore()
				plugin := newTestPlugin(t, store)
				ctx := func() *schemas.BifrostContext {
					return CreateContextWithCacheKeyAndType(t, "cross-family", CacheTypeDirect)
				}
				firstCtx := ctx()
				checkFamilyHit(t, plugin, firstCtx, familyTestRequest(first, stream), false)
				storeFamilyResponse(t, plugin, firstCtx, first, stream)
				checkFamilyHit(t, plugin, ctx(), familyTestRequest(first, stream), true)
				secondCtx := ctx()
				checkFamilyHit(t, plugin, secondCtx, familyTestRequest(second, stream), false)
				storeFamilyResponse(t, plugin, secondCtx, second, stream)
				checkFamilyHit(t, plugin, ctx(), familyTestRequest(first, stream), true)
				checkFamilyHit(t, plugin, ctx(), familyTestRequest(second, stream), true)
				store.mu.Lock()
				count := len(store.addIDs)
				store.mu.Unlock()
				if count != 2 {
					t.Fatalf("expected two independent writes, got %d", count)
				}
			})
		}
	}
}

type familyFilteringStore struct {
	*observableStore
	queries [][]vectorstore.Query
}

func (s *familyFilteringStore) GetNearest(_ context.Context, _ string, _ []float32, queries []vectorstore.Query, selectFields []string, _ float64, _ int64) ([]vectorstore.SearchResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries = append(s.queries, append([]vectorstore.Query(nil), queries...))
	for _, result := range s.chunks {
		matches := true
		for _, q := range queries {
			if result.Properties[q.Field] != q.Value {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		props := make(map[string]interface{}, len(selectFields))
		for _, field := range selectFields {
			props[field] = result.Properties[field]
		}
		score := 1.0
		return []vectorstore.SearchResult{{ID: result.ID, Score: &score, Properties: props}}, nil
	}
	return nil, nil
}

func TestSemanticReplayFiltersOutOtherRequestFamily(t *testing.T) {
	for _, first := range []string{"chat_completions", "responses"} {
		t.Run(first, func(t *testing.T) {
			second := "responses"
			if first == "responses" {
				second = "chat_completions"
			}
			store := &familyFilteringStore{observableStore: newObservableStore()}
			plugin := newTestPlugin(t, store)
			plugin.SetEmbeddingRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
				return &schemas.BifrostEmbeddingResponse{Data: []schemas.EmbeddingData{{Embedding: schemas.EmbeddingStruct{EmbeddingArray: []float64{1, 0, 0}}}}}, nil
			})
			ctx := func() *schemas.BifrostContext {
				return CreateContextWithCacheKeyAndType(t, "semantic-family", CacheTypeSemantic)
			}
			// A pre-fix semantic entry has the old params_hash. It must not be
			// returned even when its family and embedding are an exact match.
			oldMetadata, err := plugin.buildRequestMetadataForCaching(&cacheState{}, familyTestRequest(first, false))
			if err != nil {
				t.Fatal(err)
			}
			delete(oldMetadata, "request_family")
			oldHash, err := hashMap(oldMetadata)
			if err != nil {
				t.Fatal(err)
			}
			oldResponse, err := json.Marshal(familyTestResponse(first, false))
			if err != nil {
				t.Fatal(err)
			}
			store.chunks["pre-fix-semantic-entry"] = vectorstore.SearchResult{ID: "pre-fix-semantic-entry", Properties: map[string]interface{}{
				"cache_key": keyForTest(t, "semantic-family"), "params_hash": oldHash,
				"from_bifrost_semantic_cache_plugin": true, "response": string(oldResponse),
			}}
			firstCtx := ctx()
			checkFamilyHit(t, plugin, firstCtx, familyTestRequest(first, false), false)
			storeFamilyResponse(t, plugin, firstCtx, first, false)
			checkFamilyHit(t, plugin, ctx(), familyTestRequest(first, false), true)
			secondCtx := ctx()
			checkFamilyHit(t, plugin, secondCtx, familyTestRequest(second, false), false)
			storeFamilyResponse(t, plugin, secondCtx, second, false)
			checkFamilyHit(t, plugin, ctx(), familyTestRequest(second, false), true)
			store.mu.Lock()
			if len(store.queries) != 4 || len(store.addIDs) != 2 {
				t.Errorf("expected four searches and two writes, got %d searches and %d writes", len(store.queries), len(store.addIDs))
			}
			store.mu.Unlock()
		})
	}
}

func TestDirectCacheIDIsolatesEveryRequestFamily(t *testing.T) {
	plugin := newTestPlugin(t, newObservableStore())
	families := []string{"text_completions", "chat_completions", "responses", "speech", "embeddings", "transcriptions", "image_generation"}
	ids := map[string]string{}
	for _, family := range families {
		id, err := plugin.generateDirectCacheID(schemas.OpenAI, "same-model", "same-key", "same-request-hash", "same-params-hash", family)
		if err != nil {
			t.Fatal(err)
		}
		again, err := plugin.generateDirectCacheID(schemas.OpenAI, "same-model", "same-key", "same-request-hash", "same-params-hash", family)
		if err != nil || again != id {
			t.Fatalf("family %s produced unstable IDs: %q and %q (%v)", family, id, again, err)
		}
		for previousFamily, previousID := range ids {
			if id == previousID {
				t.Errorf("families %s and %s share direct cache ID %s", family, previousFamily, id)
			}
		}
		ids[family] = id
	}

	// An entry written by the old key schema cannot be fetched by the new ID.
	oldMaterial, err := schemas.MarshalDeeplySorted(struct {
		CacheKey    string `json:"cache_key"`
		RequestHash string `json:"request_hash"`
		ParamsHash  string `json:"params_hash"`
	}{"same-key", "same-request-hash", "same-params-hash"})
	if err != nil {
		t.Fatal(err)
	}
	oldID := uuid.NewSHA1(directCacheNamespace, oldMaterial).String()
	for family, id := range ids {
		if id == oldID {
			t.Errorf("family %s reused the pre-fix ID", family)
		}
	}
}

func TestRequestFamilySeparatesParamsHash(t *testing.T) {
	plugin := newTestPlugin(t, newObservableStore())
	chat := familyTestRequest("chat_completions", false)
	responses := familyTestRequest("responses", false)
	chatMetadata, err := plugin.buildRequestMetadataForCaching(&cacheState{}, chat)
	if err != nil {
		t.Fatal(err)
	}
	responsesMetadata, err := plugin.buildRequestMetadataForCaching(&cacheState{}, responses)
	if err != nil {
		t.Fatal(err)
	}
	if chatMetadata["request_family"] != "chat_completions" || responsesMetadata["request_family"] != "responses" {
		t.Fatalf("wrong family metadata: chat=%v responses=%v", chatMetadata, responsesMetadata)
	}
	chatHash, err := hashMap(chatMetadata)
	if err != nil {
		t.Fatal(err)
	}
	responsesHash, err := hashMap(responsesMetadata)
	if err != nil {
		t.Fatal(err)
	}
	if chatHash == responsesHash {
		t.Fatal("cross-family requests share params_hash and can cross-hit semantic search")
	}
	// The fixture must reproduce the old collision. Otherwise the test would
	// pass even if the new family dimension were irrelevant to the defect.
	delete(chatMetadata, "request_family")
	delete(responsesMetadata, "request_family")
	oldChatHash, err := hashMap(chatMetadata)
	if err != nil {
		t.Fatal(err)
	}
	oldResponsesHash, err := hashMap(responsesMetadata)
	if err != nil {
		t.Fatal(err)
	}
	if oldChatHash != oldResponsesHash {
		t.Fatalf("fixture does not reproduce old params_hash collision: chat=%s responses=%s", oldChatHash, oldResponsesHash)
	}
	oldChatRequestHash, err := plugin.generateRequestHash(chat, chatMetadata)
	if err != nil {
		t.Fatal(err)
	}
	oldResponsesRequestHash, err := plugin.generateRequestHash(responses, responsesMetadata)
	if err != nil {
		t.Fatal(err)
	}
	if oldChatRequestHash != oldResponsesRequestHash {
		t.Fatalf("fixture does not reproduce old request_hash collision: chat=%s responses=%s", oldChatRequestHash, oldResponsesRequestHash)
	}
}

func TestCachedResponseWithDifferentFamilyIsMiss(t *testing.T) {
	plugin := newTestPlugin(t, newObservableStore())
	for _, stream := range []bool{false, true} {
		name := "nonstream"
		requestType := schemas.ResponsesRequest
		response := &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{}}
		field := "response"
		if stream {
			name = "stream"
			requestType = schemas.ResponsesStreamRequest
			field = "stream_chunks"
		}
		t.Run(name, func(t *testing.T) {
			encoded, err := json.Marshal(response)
			if err != nil {
				t.Fatal(err)
			}
			var payload interface{} = string(encoded)
			if stream {
				payload = []string{string(encoded)}
			}
			result := vectorstore.SearchResult{ID: "wrong-family", Properties: map[string]interface{}{field: payload}}
			req := &schemas.BifrostRequest{RequestType: requestType, ResponsesRequest: CreateBasicResponsesRequest("same", 0.5, 100)}
			hit, err := plugin.buildResponseFromResult(newBaseTestContext(), &cacheState{}, req, result, CacheTypeDirect, nil, nil)
			if err != nil || hit != nil {
				t.Fatalf("wrong-family entry must miss, got hit=%v err=%v", hit, err)
			}
		})
	}
}
