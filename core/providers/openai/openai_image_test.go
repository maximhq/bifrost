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

	jsonBytes, _ := sonic.Marshal(req)
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
