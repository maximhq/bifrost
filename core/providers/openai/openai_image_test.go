package openai_test

import (
	"strings"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
)

// TestImageGenerationStreamingRequestConversion
func TestImageGenerationStreamingRequestConversion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		request  *schemas.BifrostImageGenerationRequest
		wantNil  bool
		validate func(t *testing.T, req *openai.OpenAIImageGenerationRequest)
	}{
		{
			name: "all parameters mapped",
			request: &schemas.BifrostImageGenerationRequest{
				Provider: schemas.OpenAI,
				Model:    "dall-e-3",
				Input:    &schemas.ImageGenerationInput{Prompt: "test prompt"},
				Params: &schemas.ImageGenerationParameters{
					N:              schemas.Ptr(3),
					Size:           schemas.Ptr("1024x1792"),
					Quality:        schemas.Ptr("hd"),
					Style:          schemas.Ptr("natural"),
					ResponseFormat: schemas.Ptr("b64_json"),
					User:           schemas.Ptr("user-123"),
				},
			},
			wantNil: false,
			validate: func(t *testing.T, req *openai.OpenAIImageGenerationRequest) {
				if req.Model != "dall-e-3" {
					t.Errorf("Model mismatch")
				}
				if req.Prompt != "test prompt" {
					t.Errorf("Prompt mismatch")
				}
				if *req.N != 3 {
					t.Errorf("N mismatch: expected 3, got %d", *req.N)
				}
				if *req.Size != "1024x1792" {
					t.Errorf("Size mismatch")
				}
				if *req.Quality != "hd" {
					t.Errorf("Quality mismatch")
				}
				if *req.Style != "natural" {
					t.Errorf("Style mismatch")
				}
				if *req.ResponseFormat != "b64_json" {
					t.Errorf("ResponseFormat mismatch")
				}
				if *req.User != "user-123" {
					t.Errorf("User mismatch")
				}
			},
		},
		{
			name:    "nil request returns nil",
			request: nil,
			wantNil: true,
		},
		{
			name: "nil input returns nil",
			request: &schemas.BifrostImageGenerationRequest{
				Model: "dall-e-3",
				Input: nil,
			},
			wantNil: true,
		},
		{
			name: "nil params still works",
			request: &schemas.BifrostImageGenerationRequest{
				Model:  "dall-e-2",
				Input:  &schemas.ImageGenerationInput{Prompt: "minimal"},
				Params: nil,
			},
			wantNil: false,
			validate: func(t *testing.T, req *openai.OpenAIImageGenerationRequest) {
				if req.N != nil || req.Size != nil {
					t.Errorf("Optional params should be nil")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := openai.ToOpenAIImageGenerationRequest(tt.request)
			if (got == nil) != tt.wantNil {
				t.Errorf("ToOpenAIImageGenerationRequest() nil = %v, want %v", got == nil, tt.wantNil)
				return
			}
			if !tt.wantNil && tt.validate != nil {
				tt.validate(t, got)
			}
		})
	}
}

// TestOpenAIRequestJSONOutput tests that OpenAI request serializes to correct JSON
func TestOpenAIRequestJSONOutput(t *testing.T) {
	t.Parallel()

	req := openai.ToOpenAIImageGenerationRequest(&schemas.BifrostImageGenerationRequest{
		Model: "gpt-image-1",
		Input: &schemas.ImageGenerationInput{Prompt: "a cat"},
		Params: &schemas.ImageGenerationParameters{
			Size:    schemas.Ptr("1024x1024"),
			Quality: schemas.Ptr("auto"),
		},
	})

	jsonBytes, err := sonic.Marshal(req)
	if err != nil {
		t.Fatalf("Serialization failed: %v", err)
	}
	jsonStr := string(jsonBytes)

	// Verify JSON structure matches OpenAI API
	if !strings.Contains(jsonStr, `"model":"gpt-image-1"`) {
		t.Errorf("JSON should contain model field")
	}
	if !strings.Contains(jsonStr, `"prompt":"a cat"`) {
		t.Errorf("JSON should contain prompt field")
	}
	if !strings.Contains(jsonStr, `"size":"1024x1024"`) {
		t.Errorf("JSON should contain size field")
	}
}

// =============================================================================
// 3. RESPONSE TRANSFORMATION (OpenAI → Bifrost)
// =============================================================================

// TestToBifrostImageResponse tests OpenAI to Bifrost response conversion
func TestToBifrostImageResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		openai   *openai.OpenAIImageGenerationResponse
		model    string
		latency  time.Duration
		validate func(t *testing.T, resp *schemas.BifrostImageGenerationResponse)
	}{
		{
			name: "full response converts correctly",
			openai: &openai.OpenAIImageGenerationResponse{
				Created: 1699999999,
				Data: []struct {
					URL           string `json:"url,omitempty"`
					B64JSON       string `json:"b64_json,omitempty"`
					RevisedPrompt string `json:"revised_prompt,omitempty"`
				}{
					{URL: "https://example.com/1.png", RevisedPrompt: "revised prompt 1"},
					{B64JSON: "base64data", RevisedPrompt: "revised prompt 2"},
				},
				Usage: &openai.OpenAIImageGenerationUsage{
					InputTokens: 10,
					TotalTokens: 50,
				},
			},
			model:   "dall-e-3",
			latency: 500 * time.Millisecond,
			validate: func(t *testing.T, resp *schemas.BifrostImageGenerationResponse) {
				if resp.Created != 1699999999 {
					t.Errorf("Created mismatch")
				}
				if len(resp.Data) != 2 {
					t.Errorf("Expected 2 images, got %d", len(resp.Data))
				}
				if resp.Data[0].URL != "https://example.com/1.png" {
					t.Errorf("URL mismatch")
				}
				if resp.Data[0].Index != 0 {
					t.Errorf("First image index should be 0")
				}
				if resp.Data[1].B64JSON != "base64data" {
					t.Errorf("B64JSON mismatch")
				}
				if resp.Data[1].Index != 1 {
					t.Errorf("Second image index should be 1")
				}
				if resp.Usage.PromptTokens != 10 {
					t.Errorf("PromptTokens should be mapped from InputTokens")
				}
			},
		},
		{
			name:   "nil response returns nil",
			openai: nil,
			validate: func(t *testing.T, resp *schemas.BifrostImageGenerationResponse) {
				if resp != nil {
					t.Errorf("Expected nil response")
				}
			},
		},
		{
			name: "nil usage is preserved",
			openai: &openai.OpenAIImageGenerationResponse{
				Created: 123,
				Data: []struct {
					URL           string `json:"url,omitempty"`
					B64JSON       string `json:"b64_json,omitempty"`
					RevisedPrompt string `json:"revised_prompt,omitempty"`
				}{},
				Usage: nil,
			},
			validate: func(t *testing.T, resp *schemas.BifrostImageGenerationResponse) {
				if resp.Usage != nil {
					t.Errorf("Usage should be nil")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resp := openai.ToBifrostImageResponse(tt.openai, tt.model, tt.latency)
			tt.validate(t, resp)
		})
	}
}

// TestToBifrostImageGenerationRequest tests OpenAI to Bifrost request conversion
func TestToBifrostImageGenerationRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		request  *openai.OpenAIImageGenerationRequest
		validate func(t *testing.T, req *schemas.BifrostImageGenerationRequest)
	}{
		{
			name: "full request with all parameters converts correctly",
			request: &openai.OpenAIImageGenerationRequest{
				Model:  "openai/dall-e-3",
				Prompt: "a beautiful sunset",
				ImageGenerationParameters: schemas.ImageGenerationParameters{
					N:              schemas.Ptr(2),
					Size:           schemas.Ptr("1024x1792"),
					Quality:        schemas.Ptr("hd"),
					Style:          schemas.Ptr("vivid"),
					ResponseFormat: schemas.Ptr("b64_json"),
					User:           schemas.Ptr("user-123"),
				},
				Fallbacks: []string{"azure/dall-e-3", "openai/dall-e-2"},
			},
			validate: func(t *testing.T, req *schemas.BifrostImageGenerationRequest) {
				if req == nil {
					t.Fatal("Expected non-nil request")
				}
				if req.Provider != schemas.OpenAI {
					t.Errorf("Provider mismatch: expected %s, got %s", schemas.OpenAI, req.Provider)
				}
				if req.Model != "dall-e-3" {
					t.Errorf("Model mismatch: expected dall-e-3, got %s", req.Model)
				}
				if req.Input == nil {
					t.Fatal("Expected non-nil Input")
				}
				if req.Input.Prompt != "a beautiful sunset" {
					t.Errorf("Prompt mismatch: expected 'a beautiful sunset', got %s", req.Input.Prompt)
				}
				if req.Params == nil {
					t.Fatal("Expected non-nil Params")
				}
				if *req.Params.N != 2 {
					t.Errorf("N mismatch: expected 2, got %d", *req.Params.N)
				}
				if *req.Params.Size != "1024x1792" {
					t.Errorf("Size mismatch: expected 1024x1792, got %s", *req.Params.Size)
				}
				if *req.Params.Quality != "hd" {
					t.Errorf("Quality mismatch: expected hd, got %s", *req.Params.Quality)
				}
				if *req.Params.Style != "vivid" {
					t.Errorf("Style mismatch: expected vivid, got %s", *req.Params.Style)
				}
				if *req.Params.ResponseFormat != "b64_json" {
					t.Errorf("ResponseFormat mismatch: expected b64_json, got %s", *req.Params.ResponseFormat)
				}
				if *req.Params.User != "user-123" {
					t.Errorf("User mismatch: expected user-123, got %s", *req.Params.User)
				}
				if len(req.Fallbacks) != 2 {
					t.Errorf("Expected 2 fallbacks, got %d", len(req.Fallbacks))
				}
				if req.Fallbacks[0].Provider != schemas.Azure || req.Fallbacks[0].Model != "dall-e-3" {
					t.Errorf("First fallback mismatch: expected azure/dall-e-3, got %s/%s", req.Fallbacks[0].Provider, req.Fallbacks[0].Model)
				}
				if req.Fallbacks[1].Provider != schemas.OpenAI || req.Fallbacks[1].Model != "dall-e-2" {
					t.Errorf("Second fallback mismatch: expected openai/dall-e-2, got %s/%s", req.Fallbacks[1].Provider, req.Fallbacks[1].Model)
				}
			},
		},
		{
			name: "model without provider prefix defaults to OpenAI",
			request: &openai.OpenAIImageGenerationRequest{
				Model:  "dall-e-2",
				Prompt: "minimal prompt",
			},
			validate: func(t *testing.T, req *schemas.BifrostImageGenerationRequest) {
				if req.Provider != schemas.OpenAI {
					t.Errorf("Provider should default to OpenAI, got %s", req.Provider)
				}
				if req.Model != "dall-e-2" {
					t.Errorf("Model mismatch: expected dall-e-2, got %s", req.Model)
				}
				if req.Input.Prompt != "minimal prompt" {
					t.Errorf("Prompt mismatch")
				}
			},
		},
		{
			name: "request with nil params still works",
			request: &openai.OpenAIImageGenerationRequest{
				Model:                     "gpt-image-1",
				Prompt:                    "test prompt",
				ImageGenerationParameters: schemas.ImageGenerationParameters{},
			},
			validate: func(t *testing.T, req *schemas.BifrostImageGenerationRequest) {
				if req.Params == nil {
					t.Fatal("Expected non-nil Params even when empty")
				}
				if req.Params.N != nil {
					t.Errorf("N should be nil when not set")
				}
				if req.Params.Size != nil {
					t.Errorf("Size should be nil when not set")
				}
			},
		},
		{
			name: "request with empty fallbacks",
			request: &openai.OpenAIImageGenerationRequest{
				Model:     "dall-e-3",
				Prompt:    "test",
				Fallbacks: []string{},
			},
			validate: func(t *testing.T, req *schemas.BifrostImageGenerationRequest) {
				if len(req.Fallbacks) != 0 {
					t.Errorf("Expected empty fallbacks, got %d", len(req.Fallbacks))
				}
			},
		},
		{
			name: "request with nil fallbacks",
			request: &openai.OpenAIImageGenerationRequest{
				Model:     "dall-e-3",
				Prompt:    "test",
				Fallbacks: nil,
			},
			validate: func(t *testing.T, req *schemas.BifrostImageGenerationRequest) {
				if len(req.Fallbacks) != 0 {
					t.Errorf("Expected nil or empty fallbacks, got %d", len(req.Fallbacks))
				}
			},
		},
		{
			name: "request with partial parameters",
			request: &openai.OpenAIImageGenerationRequest{
				Model:  "dall-e-3",
				Prompt: "partial params",
				ImageGenerationParameters: schemas.ImageGenerationParameters{
					Size:    schemas.Ptr("1024x1024"),
					Quality: schemas.Ptr("standard"),
				},
			},
			validate: func(t *testing.T, req *schemas.BifrostImageGenerationRequest) {
				if req.Params.Size == nil || *req.Params.Size != "1024x1024" {
					t.Errorf("Size should be preserved")
				}
				if req.Params.Quality == nil || *req.Params.Quality != "standard" {
					t.Errorf("Quality should be preserved")
				}
				if req.Params.N != nil {
					t.Errorf("N should be nil when not set")
				}
				if req.Params.Style != nil {
					t.Errorf("Style should be nil when not set")
				}
			},
		},
		{
			name: "azure provider prefix in model",
			request: &openai.OpenAIImageGenerationRequest{
				Model:  "azure/dall-e-3",
				Prompt: "azure model",
			},
			validate: func(t *testing.T, req *schemas.BifrostImageGenerationRequest) {
				if req.Provider != schemas.Azure {
					t.Errorf("Provider should be Azure, got %s", req.Provider)
				}
				if req.Model != "dall-e-3" {
					t.Errorf("Model should be dall-e-3, got %s", req.Model)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := tt.request.ToBifrostImageGenerationRequest()
			tt.validate(t, req)
		})
	}
}
