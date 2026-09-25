package semanticcache

import (
	"encoding/json"
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
	chat := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: CreateBasicChatRequest("same prompt", 0.5, 100),
	}
	responses := &schemas.BifrostRequest{
		RequestType:      schemas.ResponsesRequest,
		ResponsesRequest: CreateBasicResponsesRequest("same prompt", 0.5, 100),
	}
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
