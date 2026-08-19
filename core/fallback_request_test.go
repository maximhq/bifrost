package bifrost

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
)

func TestPrepareFallbackRequestImageEditUsesFallbackRouteWithoutMutatingOriginal(t *testing.T) {
	account := NewMockAccount()
	account.AddProvider(schemas.Bedrock, 1, 1)
	bifrost := &Bifrost{account: account}
	originalImageEditRequest := &schemas.BifrostImageEditRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-image-1",
	}
	originalRequest := &schemas.BifrostRequest{
		ImageEditRequest: originalImageEditRequest,
	}

	fallbackRequest := bifrost.prepareFallbackRequest(originalRequest, schemas.Fallback{
		Provider: schemas.Bedrock,
		Model:    "amazon.nova-canvas-v1:0",
	})

	if fallbackRequest == nil {
		t.Fatal("expected fallback request, got nil")
	}
	if fallbackRequest.ImageEditRequest == originalImageEditRequest {
		t.Fatal("expected fallback image edit request to be a copy")
	}
	if fallbackRequest.ImageEditRequest.Provider != schemas.Bedrock {
		t.Errorf("expected fallback provider %q, got %q", schemas.Bedrock, fallbackRequest.ImageEditRequest.Provider)
	}
	if fallbackRequest.ImageEditRequest.Model != "amazon.nova-canvas-v1:0" {
		t.Errorf("expected fallback model %q, got %q", "amazon.nova-canvas-v1:0", fallbackRequest.ImageEditRequest.Model)
	}
	if originalImageEditRequest.Provider != schemas.OpenAI {
		t.Errorf("expected original provider %q, got %q", schemas.OpenAI, originalImageEditRequest.Provider)
	}
	if originalImageEditRequest.Model != "gpt-image-1" {
		t.Errorf("expected original model %q, got %q", "gpt-image-1", originalImageEditRequest.Model)
	}
}

func TestPrepareFallbackRequestImageVariationUsesFallbackRouteWithoutMutatingOriginal(t *testing.T) {
	account := NewMockAccount()
	account.AddProvider(schemas.Bedrock, 1, 1)
	bifrost := &Bifrost{account: account}
	originalImageVariationRequest := &schemas.BifrostImageVariationRequest{
		Provider: schemas.OpenAI,
		Model:    "dall-e-2",
	}
	originalRequest := &schemas.BifrostRequest{
		ImageVariationRequest: originalImageVariationRequest,
	}

	fallbackRequest := bifrost.prepareFallbackRequest(originalRequest, schemas.Fallback{
		Provider: schemas.Bedrock,
		Model:    "amazon.titan-image-generator-v2:0",
	})

	if fallbackRequest == nil {
		t.Fatal("expected fallback request, got nil")
	}
	if fallbackRequest.ImageVariationRequest == originalImageVariationRequest {
		t.Fatal("expected fallback image variation request to be a copy")
	}
	if fallbackRequest.ImageVariationRequest.Provider != schemas.Bedrock {
		t.Errorf("expected fallback provider %q, got %q", schemas.Bedrock, fallbackRequest.ImageVariationRequest.Provider)
	}
	if fallbackRequest.ImageVariationRequest.Model != "amazon.titan-image-generator-v2:0" {
		t.Errorf("expected fallback model %q, got %q", "amazon.titan-image-generator-v2:0", fallbackRequest.ImageVariationRequest.Model)
	}
	if originalImageVariationRequest.Provider != schemas.OpenAI {
		t.Errorf("expected original provider %q, got %q", schemas.OpenAI, originalImageVariationRequest.Provider)
	}
	if originalImageVariationRequest.Model != "dall-e-2" {
		t.Errorf("expected original model %q, got %q", "dall-e-2", originalImageVariationRequest.Model)
	}
}

func TestPrepareFallbackRequestImageGenerationUsesFallbackRouteWithoutMutatingOriginal(t *testing.T) {
	account := NewMockAccount()
	account.AddProvider(schemas.Bedrock, 1, 1)
	bifrost := &Bifrost{account: account}
	originalImageGenerationRequest := &schemas.BifrostImageGenerationRequest{
		Provider: schemas.OpenAI,
		Model:    "gpt-image-1",
	}
	originalRequest := &schemas.BifrostRequest{
		ImageGenerationRequest: originalImageGenerationRequest,
	}

	fallbackRequest := bifrost.prepareFallbackRequest(originalRequest, schemas.Fallback{
		Provider: schemas.Bedrock,
		Model:    "amazon.nova-canvas-v1:0",
	})

	if fallbackRequest == nil {
		t.Fatal("expected fallback request, got nil")
	}
	if fallbackRequest.ImageGenerationRequest == originalImageGenerationRequest {
		t.Fatal("expected fallback image generation request to be a copy")
	}
	if fallbackRequest.ImageGenerationRequest.Provider != schemas.Bedrock {
		t.Errorf("expected fallback provider %q, got %q", schemas.Bedrock, fallbackRequest.ImageGenerationRequest.Provider)
	}
	if fallbackRequest.ImageGenerationRequest.Model != "amazon.nova-canvas-v1:0" {
		t.Errorf("expected fallback model %q, got %q", "amazon.nova-canvas-v1:0", fallbackRequest.ImageGenerationRequest.Model)
	}
	if originalImageGenerationRequest.Provider != schemas.OpenAI {
		t.Errorf("expected original provider %q, got %q", schemas.OpenAI, originalImageGenerationRequest.Provider)
	}
	if originalImageGenerationRequest.Model != "gpt-image-1" {
		t.Errorf("expected original model %q, got %q", "gpt-image-1", originalImageGenerationRequest.Model)
	}
}
