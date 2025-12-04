package bifrost

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// TestImageGenerationRequestSerialization tests Bifrost request JSON serialization
func TestImageGenerationRequestSerialization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		request *schemas.BifrostImageGenerationRequest
		check   func(t *testing.T, jsonBytes []byte)
	}{
		{
			name: "full request serializes correctly",
			request: &schemas.BifrostImageGenerationRequest{
				Provider: schemas.OpenAI,
				Model:    "dall-e-3",
				Input: &schemas.ImageGenerationInput{
					Prompt: "a cute cat",
				},
				Params: &schemas.ImageGenerationParameters{
					N:              schemas.Ptr(2),
					Size:           schemas.Ptr("1024x1024"),
					Quality:        schemas.Ptr("hd"),
					Style:          schemas.Ptr("vivid"),
					ResponseFormat: schemas.Ptr("b64_json"),
					User:           schemas.Ptr("test-user"),
				},
			},
			check: func(t *testing.T, jsonBytes []byte) {
				var data map[string]interface{}
				if err := json.Unmarshal(jsonBytes, &data); err != nil {
					t.Fatalf("Failed to unmarshal: %v", err)
				}
				if data["provider"] != "openai" {
					t.Errorf("Expected provider 'openai', got %v", data["provider"])
				}
				if data["model"] != "dall-e-3" {
					t.Errorf("Expected model 'dall-e-3', got %v", data["model"])
				}
				input := data["input"].(map[string]interface{})
				if input["prompt"] != "a cute cat" {
					t.Errorf("Expected prompt 'a cute cat', got %v", input["prompt"])
				}
				params := data["params"].(map[string]interface{})
				if params["size"] != "1024x1024" {
					t.Errorf("Expected size '1024x1024', got %v", params["size"])
				}
			},
		},
		{
			name: "minimal request omits nil fields",
			request: &schemas.BifrostImageGenerationRequest{
				Provider: schemas.OpenAI,
				Model:    "dall-e-2",
				Input: &schemas.ImageGenerationInput{
					Prompt: "test",
				},
			},
			check: func(t *testing.T, jsonBytes []byte) {
				var data map[string]interface{}
				if err := json.Unmarshal(jsonBytes, &data); err != nil {
					t.Fatalf("Failed to unmarshal: %v", err)
				}
				if _, exists := data["params"]; exists {
					t.Errorf("params should be omitted when nil")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			jsonBytes, err := sonic.Marshal(tt.request)
			if err != nil {
				t.Fatalf("Serialization failed: %v", err)
			}
			tt.check(t, jsonBytes)
		})
	}
}

// TestImageGenerationRequestDeserialization tests JSON to Bifrost request deserialization
func TestImageGenerationRequestDeserialization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		jsonInput string
		validate  func(t *testing.T, req *schemas.BifrostImageGenerationRequest)
		wantErr   bool
	}{
		{
			name: "full JSON deserializes correctly",
			jsonInput: `{
				"provider": "openai",
				"model": "dall-e-3",
				"input": {"prompt": "a beautiful sunset"},
				"params": {"size": "1024x1024", "quality": "hd", "n": 2}
			}`,
			validate: func(t *testing.T, req *schemas.BifrostImageGenerationRequest) {
				if req.Provider != schemas.OpenAI {
					t.Errorf("Expected provider OpenAI, got %s", req.Provider)
				}
				if req.Input.Prompt != "a beautiful sunset" {
					t.Errorf("Expected prompt 'a beautiful sunset', got '%s'", req.Input.Prompt)
				}
				if req.Params == nil || *req.Params.N != 2 {
					t.Errorf("Expected n=2")
				}
			},
			wantErr: false,
		},
		{
			name:      "invalid JSON returns error",
			jsonInput: `{invalid}`,
			validate:  func(t *testing.T, req *schemas.BifrostImageGenerationRequest) {},
			wantErr:   true,
		},
		{
			name: "missing optional fields succeeds",
			jsonInput: `{
				"provider": "openai",
				"model": "dall-e-2",
				"input": {"prompt": "test"}
			}`,
			validate: func(t *testing.T, req *schemas.BifrostImageGenerationRequest) {
				if req.Params != nil {
					t.Errorf("Expected params to be nil")
				}
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var req schemas.BifrostImageGenerationRequest
			err := sonic.Unmarshal([]byte(tt.jsonInput), &req)
			if (err != nil) != tt.wantErr {
				t.Errorf("Unmarshal() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				tt.validate(t, &req)
			}
		})
	}
}

// TestImageGenerationResponseSerialization tests response serialization
func TestImageGenerationResponseSerialization(t *testing.T) {
	t.Parallel()

	resp := &schemas.BifrostImageGenerationResponse{
		ID:      "img-123",
		Created: 1234567890,
		Model:   "dall-e-3",
		Data: []schemas.ImageData{
			{URL: "https://example.com/image.png", Index: 0, RevisedPrompt: "a cat revised"},
			{B64JSON: "iVBORw0KGgo=", Index: 1},
		},
		Usage: &schemas.ImageUsage{PromptTokens: 10, TotalTokens: 20},
	}

	jsonBytes, err := sonic.Marshal(resp)
	if err != nil {
		t.Fatalf("Serialization failed: %v", err)
	}

	var data map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &data); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}

	if data["id"] != "img-123" {
		t.Errorf("Expected id 'img-123', got %v", data["id"])
	}

	dataArr := data["data"].([]interface{})
	if len(dataArr) != 2 {
		t.Errorf("Expected 2 images, got %d", len(dataArr))
	}
}

// TestImageStreamResponseSerialization tests streaming response serialization
func TestImageStreamResponseSerialization(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		chunk  *schemas.BifrostImageGenerationStreamResponse
		verify func(t *testing.T, data map[string]interface{})
	}{
		{
			name: "partial chunk",
			chunk: &schemas.BifrostImageGenerationStreamResponse{
				ID:         "img-stream-1",
				Type:       "image_generation.partial_image",
				Index:      0,
				ChunkIndex: 3,
				PartialB64: "dGVzdGRhdGE=",
			},
			verify: func(t *testing.T, data map[string]interface{}) {
				if data["type"] != "image_generation.partial_image" {
					t.Errorf("Expected type 'image_generation.partial_image'")
				}
				if int(data["chunk_index"].(float64)) != 3 {
					t.Errorf("Expected chunk_index 3")
				}
			},
		},
		{
			name: "completed chunk with usage",
			chunk: &schemas.BifrostImageGenerationStreamResponse{
				ID:         "img-stream-1",
				Type:       "image_generation.completed",
				Index:      0,
				ChunkIndex: 10,
				Usage:      &schemas.ImageUsage{PromptTokens: 5, TotalTokens: 15},
			},
			verify: func(t *testing.T, data map[string]interface{}) {
				if data["type"] != "image_generation.completed" {
					t.Errorf("Expected type 'image_generation.completed'")
				}
				usage := data["usage"].(map[string]interface{})
				if int(usage["total_tokens"].(float64)) != 15 {
					t.Errorf("Expected total_tokens 15")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			jsonBytes, _ := sonic.Marshal(tt.chunk)
			var data map[string]interface{}
			json.Unmarshal(jsonBytes, &data)
			tt.verify(t, data)
		})
	}
}

// TestStreamChunkAccumulation tests that chunks can be accumulated and reconstructed
func TestStreamChunkAccumulation(t *testing.T) {
	t.Parallel()

	t.Run("single image multiple chunks", func(t *testing.T) {
		t.Parallel()

		originalB64 := strings.Repeat("ABCD", 50000) // ~200KB
		chunkSize := 64 * 1024

		// Simulate streaming chunks
		var chunks []*schemas.BifrostImageGenerationStreamResponse
		chunkIdx := 0
		for offset := 0; offset < len(originalB64); offset += chunkSize {
			end := offset + chunkSize
			if end > len(originalB64) {
				end = len(originalB64)
			}

			isLast := end >= len(originalB64)
			chunkType := "image_generation.partial_image"
			if isLast {
				chunkType = "image_generation.completed"
			}

			chunks = append(chunks, &schemas.BifrostImageGenerationStreamResponse{
				ID:         "img-123",
				Type:       chunkType,
				Index:      0,
				ChunkIndex: chunkIdx,
				PartialB64: originalB64[offset:end],
			})
			chunkIdx++
		}

		// Reconstruct from chunks
		var accumulated strings.Builder
		for _, chunk := range chunks {
			accumulated.WriteString(chunk.PartialB64)
		}

		if accumulated.String() != originalB64 {
			t.Errorf("Reconstructed data doesn't match original (len %d vs %d)",
				len(accumulated.String()), len(originalB64))
		}
	})

	t.Run("multiple images parallel chunks", func(t *testing.T) {
		t.Parallel()

		image0Data := strings.Repeat("A", 100000)
		image1Data := strings.Repeat("B", 100000)
		chunkSize := 64 * 1024

		// Interleaved chunks from 2 images
		var allChunks []*schemas.BifrostImageGenerationStreamResponse

		// Image 0 chunks
		for i, offset := 0, 0; offset < len(image0Data); i, offset = i+1, offset+chunkSize {
			end := offset + chunkSize
			if end > len(image0Data) {
				end = len(image0Data)
			}
			allChunks = append(allChunks, &schemas.BifrostImageGenerationStreamResponse{
				Index:      0,
				ChunkIndex: i,
				PartialB64: image0Data[offset:end],
			})
		}

		// Image 1 chunks
		for i, offset := 0, 0; offset < len(image1Data); i, offset = i+1, offset+chunkSize {
			end := offset + chunkSize
			if end > len(image1Data) {
				end = len(image1Data)
			}
			allChunks = append(allChunks, &schemas.BifrostImageGenerationStreamResponse{
				Index:      1,
				ChunkIndex: i,
				PartialB64: image1Data[offset:end],
			})
		}

		// Group by image index and sort by chunk index
		imageChunks := make(map[int][]*schemas.BifrostImageGenerationStreamResponse)
		for _, chunk := range allChunks {
			imageChunks[chunk.Index] = append(imageChunks[chunk.Index], chunk)
		}

		for imgIdx := range imageChunks {
			sort.Slice(imageChunks[imgIdx], func(i, j int) bool {
				return imageChunks[imgIdx][i].ChunkIndex < imageChunks[imgIdx][j].ChunkIndex
			})
		}

		// Reconstruct each image
		var image0Reconstructed, image1Reconstructed strings.Builder
		for _, chunk := range imageChunks[0] {
			image0Reconstructed.WriteString(chunk.PartialB64)
		}
		for _, chunk := range imageChunks[1] {
			image1Reconstructed.WriteString(chunk.PartialB64)
		}

		if image0Reconstructed.String() != image0Data {
			t.Errorf("Image 0 reconstruction failed")
		}
		if image1Reconstructed.String() != image1Data {
			t.Errorf("Image 1 reconstruction failed")
		}
	})

	t.Run("out of order chunks sorted correctly", func(t *testing.T) {
		t.Parallel()

		chunks := []*schemas.BifrostImageGenerationStreamResponse{
			{ChunkIndex: 3, PartialB64: "D"},
			{ChunkIndex: 0, PartialB64: "A"},
			{ChunkIndex: 2, PartialB64: "C"},
			{ChunkIndex: 1, PartialB64: "B"},
		}

		sort.Slice(chunks, func(i, j int) bool {
			return chunks[i].ChunkIndex < chunks[j].ChunkIndex
		})

		var result strings.Builder
		for _, c := range chunks {
			result.WriteString(c.PartialB64)
		}

		if result.String() != "ABCD" {
			t.Errorf("Expected 'ABCD', got '%s'", result.String())
		}
	})
}

// TestStreamChunkUsageOnFinal tests that usage is only on final chunk
func TestStreamChunkUsageOnFinal(t *testing.T) {
	t.Parallel()

	chunks := []*schemas.BifrostImageGenerationStreamResponse{
		{ChunkIndex: 0, Type: "image_generation.partial_image", Usage: nil},
		{ChunkIndex: 1, Type: "image_generation.partial_image", Usage: nil},
		{ChunkIndex: 2, Type: "image_generation.completed", Usage: &schemas.ImageUsage{
			PromptTokens: 10, TotalTokens: 100,
		}},
	}

	var finalUsage *schemas.ImageUsage
	for _, chunk := range chunks {
		if chunk.Type == "image_generation.completed" && chunk.Usage != nil {
			finalUsage = chunk.Usage
		}
	}

	if finalUsage == nil {
		t.Fatal("Expected usage on final chunk")
	}
	if finalUsage.TotalTokens != 100 {
		t.Errorf("Expected TotalTokens 100, got %d", finalUsage.TotalTokens)
	}
}

// TestImageCacheKeyComponents tests what components should go into cache key
func TestImageCacheKeyComponents(t *testing.T) {
	t.Parallel()

	// Cache key should be deterministic based on: prompt + params
	req1 := &schemas.BifrostImageGenerationRequest{
		Input:  &schemas.ImageGenerationInput{Prompt: "a cat"},
		Params: &schemas.ImageGenerationParameters{Size: schemas.Ptr("1024x1024")},
	}
	req2 := &schemas.BifrostImageGenerationRequest{
		Input:  &schemas.ImageGenerationInput{Prompt: "a cat"},
		Params: &schemas.ImageGenerationParameters{Size: schemas.Ptr("1024x1024")},
	}
	req3 := &schemas.BifrostImageGenerationRequest{
		Input:  &schemas.ImageGenerationInput{Prompt: "a dog"},
		Params: &schemas.ImageGenerationParameters{Size: schemas.Ptr("1024x1024")},
	}

	// Same request should produce same cache components
	key1 := generateTestCacheKey(req1)
	key2 := generateTestCacheKey(req2)
	key3 := generateTestCacheKey(req3)

	if key1 != key2 {
		t.Errorf("Identical requests should have same cache key")
	}
	if key1 == key3 {
		t.Errorf("Different prompts should have different cache keys")
	}
}

// generateTestCacheKey simulates cache key generation (actual impl in semanticcache)
func generateTestCacheKey(req *schemas.BifrostImageGenerationRequest) string {
	if req == nil || req.Input == nil {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(req.Input.Prompt)

	if req.Params != nil {
		if req.Params.Size != nil {
			sb.WriteString(*req.Params.Size)
		}
		if req.Params.Quality != nil {
			sb.WriteString(*req.Params.Quality)
		}
		if req.Params.Style != nil {
			sb.WriteString(*req.Params.Style)
		}
		if req.Params.N != nil {
			sb.WriteString(fmt.Sprintf("%d", *req.Params.N))
		}
	}

	return sb.String()
}

// TestCacheKeyDifferentParams tests that different params produce different keys
func TestCacheKeyDifferentParams(t *testing.T) {
	t.Parallel()

	baseReq := func() *schemas.BifrostImageGenerationRequest {
		return &schemas.BifrostImageGenerationRequest{
			Input:  &schemas.ImageGenerationInput{Prompt: "a cat"},
			Params: &schemas.ImageGenerationParameters{},
		}
	}

	tests := []struct {
		name   string
		modify func(r *schemas.BifrostImageGenerationRequest)
	}{
		{"different size", func(r *schemas.BifrostImageGenerationRequest) { r.Params.Size = schemas.Ptr("512x512") }},
		{"different quality", func(r *schemas.BifrostImageGenerationRequest) { r.Params.Quality = schemas.Ptr("hd") }},
		{"different style", func(r *schemas.BifrostImageGenerationRequest) { r.Params.Style = schemas.Ptr("vivid") }},
		{"different n", func(r *schemas.BifrostImageGenerationRequest) { r.Params.N = schemas.Ptr(2) }},
	}

	baseKey := generateTestCacheKey(baseReq())

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := baseReq()
			tt.modify(req)
			modifiedKey := generateTestCacheKey(req)

			if modifiedKey == baseKey {
				t.Errorf("Param change '%s' should produce different cache key", tt.name)
			}
		})
	}
}
