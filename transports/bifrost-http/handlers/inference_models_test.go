package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

func TestApplyCatalogCapabilityMetadata_DerivesSurfaceFromModeWithoutClobberingProviderMethods(t *testing.T) {
	modality := "text"
	model := schemas.Model{
		SupportedMethods: []string{"provider-native"},
	}
	capability := &modelcatalog.PricingEntry{
		Mode:            "responses",
		ContextLength:   intPtr(200000),
		MaxInputTokens:  intPtr(128000),
		MaxOutputTokens: intPtr(32000),
		Architecture: &schemas.Architecture{
			Modality: &modality,
		},
	}

	applyCatalogCapabilityMetadata(&model, capability)

	if model.Mode == nil || *model.Mode != "responses" {
		t.Fatalf("expected mode=responses, got %#v", model.Mode)
	}
	if len(model.SupportedEndpoints) != 1 || model.SupportedEndpoints[0] != "/v1/responses" {
		t.Fatalf("expected supported_endpoints=[/v1/responses], got %#v", model.SupportedEndpoints)
	}
	if len(model.SupportedMethods) != 1 || model.SupportedMethods[0] != "provider-native" {
		t.Fatalf("expected provider-native supported_methods to be preserved, got %#v", model.SupportedMethods)
	}
	if model.ContextLength == nil || *model.ContextLength != 200000 {
		t.Fatalf("expected context_length=200000, got %#v", model.ContextLength)
	}
	if model.MaxInputTokens == nil || *model.MaxInputTokens != 128000 {
		t.Fatalf("expected max_input_tokens=128000, got %#v", model.MaxInputTokens)
	}
	if model.MaxOutputTokens == nil || *model.MaxOutputTokens != 32000 {
		t.Fatalf("expected max_output_tokens=32000, got %#v", model.MaxOutputTokens)
	}
	if model.Architecture == nil || model.Architecture.Modality == nil || *model.Architecture.Modality != modality {
		t.Fatalf("expected architecture modality=%q, got %#v", modality, model.Architecture)
	}
}

func TestApplyCatalogCapabilityMetadata_DoesNotCreateEmptyPricing(t *testing.T) {
	model := schemas.Model{}
	capability := &modelcatalog.PricingEntry{
		Mode:          "chat",
		ContextLength: intPtr(8192),
	}

	applyCatalogCapabilityMetadata(&model, capability)

	if model.Pricing != nil {
		t.Fatalf("expected capability-only enrichment to leave pricing nil, got %#v", model.Pricing)
	}
	if model.ContextLength == nil || *model.ContextLength != 8192 {
		t.Fatalf("expected context_length=8192, got %#v", model.ContextLength)
	}
	if model.Mode == nil || *model.Mode != "chat" {
		t.Fatalf("expected mode=chat, got %#v", model.Mode)
	}
}

func TestApplyCatalogCapabilityMetadata_BackfillsBifrostRequestTypesWhenProviderMethodsMissing(t *testing.T) {
	testCases := []struct {
		name     string
		mode     string
		endpoint string
		method   string
	}{
		{
			name:     "responses",
			mode:     "responses",
			endpoint: "/v1/responses",
			method:   string(schemas.ResponsesRequest),
		},
		{
			name:     "chat",
			mode:     "chat",
			endpoint: "/v1/chat/completions",
			method:   string(schemas.ChatCompletionRequest),
		},
		{
			name:     "audio speech",
			mode:     "audio_speech",
			endpoint: "/v1/audio/speech",
			method:   string(schemas.SpeechRequest),
		},
		{
			name:     "image generation",
			mode:     "image_generation",
			endpoint: "/v1/images/generations",
			method:   string(schemas.ImageGenerationRequest),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			model := schemas.Model{}
			capability := &modelcatalog.PricingEntry{Mode: tc.mode}

			applyCatalogCapabilityMetadata(&model, capability)

			if model.Mode == nil || *model.Mode != tc.mode {
				t.Fatalf("expected mode=%s, got %#v", tc.mode, model.Mode)
			}
			if len(model.SupportedEndpoints) != 1 || model.SupportedEndpoints[0] != tc.endpoint {
				t.Fatalf("expected supported_endpoints=[%s], got %#v", tc.endpoint, model.SupportedEndpoints)
			}
			if len(model.SupportedMethods) != 1 || model.SupportedMethods[0] != tc.method {
				t.Fatalf("expected supported_methods=[%s], got %#v", tc.method, model.SupportedMethods)
			}
		})
	}
}

func TestApplyCatalogCapabilityMetadata_SkipsUnknownModeSurfaceBackfill(t *testing.T) {
	model := schemas.Model{}
	capability := &modelcatalog.PricingEntry{
		Mode:          "unknown",
		ContextLength: intPtr(4096),
	}

	applyCatalogCapabilityMetadata(&model, capability)

	if model.ContextLength == nil || *model.ContextLength != 4096 {
		t.Fatalf("expected context_length=4096, got %#v", model.ContextLength)
	}
	if model.Mode != nil {
		t.Fatalf("expected mode to remain nil for unknown mode, got %#v", model.Mode)
	}
	if len(model.SupportedEndpoints) != 0 {
		t.Fatalf("expected supported_endpoints to remain empty, got %#v", model.SupportedEndpoints)
	}
	if len(model.SupportedMethods) != 0 {
		t.Fatalf("expected supported_methods to remain empty, got %#v", model.SupportedMethods)
	}
}

func TestApplyCatalogCapabilityMetadata_PreservesExistingSurface(t *testing.T) {
	existingMode := "responses"
	model := schemas.Model{
		Mode:               &existingMode,
		SupportedEndpoints: []string{"/v1/responses"},
		SupportedMethods:   []string{"provider-native"},
	}
	capability := &modelcatalog.PricingEntry{
		Mode:          "chat",
		ContextLength: intPtr(8192),
	}

	applyCatalogCapabilityMetadata(&model, capability)

	if model.Mode == nil || *model.Mode != existingMode {
		t.Fatalf("expected existing mode=%s to be preserved, got %#v", existingMode, model.Mode)
	}
	if len(model.SupportedEndpoints) != 1 || model.SupportedEndpoints[0] != "/v1/responses" {
		t.Fatalf("expected existing supported_endpoints to be preserved, got %#v", model.SupportedEndpoints)
	}
	if len(model.SupportedMethods) != 1 || model.SupportedMethods[0] != "provider-native" {
		t.Fatalf("expected existing supported_methods to be preserved, got %#v", model.SupportedMethods)
	}
	if model.ContextLength == nil || *model.ContextLength != 8192 {
		t.Fatalf("expected context_length=8192, got %#v", model.ContextLength)
	}
}

func TestApplyCatalogCapabilityMetadata_PreservesProviderMethodsWithoutCatalogEntry(t *testing.T) {
	model := schemas.Model{
		SupportedMethods: []string{"provider-native"},
	}

	applyCatalogCapabilityMetadata(&model, nil)

	if model.Mode != nil {
		t.Fatalf("expected mode to remain nil, got %#v", model.Mode)
	}
	if len(model.SupportedEndpoints) != 0 {
		t.Fatalf("expected supported_endpoints to remain empty, got %#v", model.SupportedEndpoints)
	}
	if len(model.SupportedMethods) != 1 || model.SupportedMethods[0] != "provider-native" {
		t.Fatalf("expected provider-native supported_methods to be preserved, got %#v", model.SupportedMethods)
	}
}

func TestEnrichListModelsResponse_BackfillsCapabilitiesFromAliasEntry(t *testing.T) {
	modality := "text"
	catalog := newHTTPBackedTestModelCatalog(t, map[string]modelcatalog.PricingEntry{
		"deployment-model": {
			Provider:        string(schemas.OpenAI),
			Mode:            "chat",
			ContextLength:   intPtr(16384),
			MaxInputTokens:  intPtr(12000),
			MaxOutputTokens: intPtr(4096),
			Architecture: &schemas.Architecture{
				Modality: &modality,
			},
		},
	})
	resp := &schemas.BifrostListModelsResponse{
		Data: []schemas.Model{{
			ID:      "openai/alias-model",
			Alias:   schemas.Ptr("deployment-model"),
			Pricing: &schemas.Pricing{Prompt: schemas.Ptr("existing")},
		}},
	}

	enrichListModelsResponse(resp, catalog)

	model := resp.Data[0]
	if model.Mode == nil || *model.Mode != "chat" {
		t.Fatalf("expected mode=chat from alias fallback, got %#v", model.Mode)
	}
	if len(model.SupportedEndpoints) != 1 || model.SupportedEndpoints[0] != "/v1/chat/completions" {
		t.Fatalf("expected supported_endpoints=[/v1/chat/completions], got %#v", model.SupportedEndpoints)
	}
	if len(model.SupportedMethods) != 1 || model.SupportedMethods[0] != string(schemas.ChatCompletionRequest) {
		t.Fatalf("expected supported_methods=[%s], got %#v", string(schemas.ChatCompletionRequest), model.SupportedMethods)
	}
	if model.ContextLength == nil || *model.ContextLength != 16384 {
		t.Fatalf("expected context_length=16384, got %#v", model.ContextLength)
	}
	if model.MaxInputTokens == nil || *model.MaxInputTokens != 12000 {
		t.Fatalf("expected max_input_tokens=12000, got %#v", model.MaxInputTokens)
	}
	if model.MaxOutputTokens == nil || *model.MaxOutputTokens != 4096 {
		t.Fatalf("expected max_output_tokens=4096, got %#v", model.MaxOutputTokens)
	}
	if model.Architecture == nil || model.Architecture.Modality == nil || *model.Architecture.Modality != modality {
		t.Fatalf("expected architecture modality=%q, got %#v", modality, model.Architecture)
	}
	if model.Pricing == nil || model.Pricing.Prompt == nil || *model.Pricing.Prompt != "existing" {
		t.Fatalf("expected existing pricing to be preserved, got %#v", model.Pricing)
	}
}

func TestEnrichListModelsResponse_PricingUsesPricingRowButCapabilitiesUseCapabilityRow(t *testing.T) {
	modality := "text"
	catalog := newHTTPBackedTestModelCatalog(t, map[string]modelcatalog.PricingEntry{
		"alias-model": {
			Provider:           string(schemas.OpenAI),
			Mode:               "responses",
			BaseModel:          "alias-model",
			ContextLength:      intPtr(4096),
			MaxInputTokens:     intPtr(2048),
			MaxOutputTokens:    intPtr(256),
			InputCostPerToken:  floatPtr(0.1),
			OutputCostPerToken: floatPtr(0.2),
		},
		"canonical-model": {
			Provider:        string(schemas.OpenAI),
			Mode:            "chat",
			BaseModel:       "canonical-model",
			ContextLength:   intPtr(16384),
			MaxInputTokens:  intPtr(12000),
			MaxOutputTokens: intPtr(4096),
			Architecture: &schemas.Architecture{
				Modality: &modality,
			},
		},
	})
	canonicalModel := "canonical-model"
	catalog.SetKeyConfigForProvider(schemas.OpenAI, []schemas.Key{{
		ID:      "openai-key",
		Enabled: schemas.Ptr(true),
		Models:  schemas.WhiteList{"*"},
		Aliases: schemas.KeyAliases{
			"alias-model": {
				ModelID:   "provider-alias-model",
				ModelName: &canonicalModel,
			},
		},
	}})
	resp := &schemas.BifrostListModelsResponse{
		Data: []schemas.Model{{
			ID: "openai/alias-model",
		}},
	}

	enrichListModelsResponse(resp, catalog)

	model := resp.Data[0]
	if model.Pricing == nil || model.Pricing.Prompt == nil || *model.Pricing.Prompt != "0.1000000000" {
		t.Fatalf("expected prompt pricing from alias pricing row, got %#v", model.Pricing)
	}
	if model.Pricing.Completion == nil || *model.Pricing.Completion != "0.2000000000" {
		t.Fatalf("expected completion pricing from alias pricing row, got %#v", model.Pricing)
	}
	if model.Mode == nil || *model.Mode != "chat" {
		t.Fatalf("expected mode=chat from canonical capability row, got %#v", model.Mode)
	}
	if len(model.SupportedEndpoints) != 1 || model.SupportedEndpoints[0] != "/v1/chat/completions" {
		t.Fatalf("expected supported_endpoints=[/v1/chat/completions], got %#v", model.SupportedEndpoints)
	}
	if len(model.SupportedMethods) != 1 || model.SupportedMethods[0] != string(schemas.ChatCompletionRequest) {
		t.Fatalf("expected supported_methods=[%s], got %#v", string(schemas.ChatCompletionRequest), model.SupportedMethods)
	}
	if model.ContextLength == nil || *model.ContextLength != 16384 {
		t.Fatalf("expected context_length=16384 from canonical capability row, got %#v", model.ContextLength)
	}
	if model.MaxInputTokens == nil || *model.MaxInputTokens != 12000 {
		t.Fatalf("expected max_input_tokens=12000 from canonical capability row, got %#v", model.MaxInputTokens)
	}
	if model.MaxOutputTokens == nil || *model.MaxOutputTokens != 4096 {
		t.Fatalf("expected max_output_tokens=4096 from canonical capability row, got %#v", model.MaxOutputTokens)
	}
	if model.Architecture == nil || model.Architecture.Modality == nil || *model.Architecture.Modality != modality {
		t.Fatalf("expected architecture modality=%q from canonical capability row, got %#v", modality, model.Architecture)
	}
}

func TestEnrichListModelsResponse_KeepsCapabilityPricingWhenSelectedPricingRowHasNoPrices(t *testing.T) {
	catalog := newHTTPBackedTestModelCatalog(t, map[string]modelcatalog.PricingEntry{
		"alias-model": {
			Provider:        string(schemas.OpenAI),
			Mode:            "responses",
			BaseModel:       "alias-model",
			ContextLength:   intPtr(4096),
			MaxInputTokens:  intPtr(2048),
			MaxOutputTokens: intPtr(256),
		},
		"canonical-model": {
			Provider:           string(schemas.OpenAI),
			Mode:               "chat",
			BaseModel:          "canonical-model",
			InputCostPerToken:  floatPtr(0.3),
			OutputCostPerToken: floatPtr(0.4),
		},
	})
	canonicalModel := "canonical-model"
	catalog.SetKeyConfigForProvider(schemas.OpenAI, []schemas.Key{{
		ID:      "openai-key",
		Enabled: schemas.Ptr(true),
		Models:  schemas.WhiteList{"*"},
		Aliases: schemas.KeyAliases{
			"alias-model": {
				ModelID:   "provider-alias-model",
				ModelName: &canonicalModel,
			},
		},
	}})
	resp := &schemas.BifrostListModelsResponse{
		Data: []schemas.Model{{
			ID: "openai/alias-model",
		}},
	}

	enrichListModelsResponse(resp, catalog)

	model := resp.Data[0]
	if model.Pricing == nil || model.Pricing.Prompt == nil || *model.Pricing.Prompt != "0.3000000000" {
		t.Fatalf("expected prompt pricing from canonical capability row to be preserved, got %#v", model.Pricing)
	}
	if model.Pricing.Completion == nil || *model.Pricing.Completion != "0.4000000000" {
		t.Fatalf("expected completion pricing from canonical capability row to be preserved, got %#v", model.Pricing)
	}
}

func TestApplyCatalogPricingMetadata_FillsMissingPricingOnly(t *testing.T) {
	imageCost := 0.03
	cacheRead := 0.004
	model := schemas.Model{}
	pricing := &modelcatalog.PricingEntry{
		InputCostPerToken:       floatPtr(0.1),
		OutputCostPerToken:      floatPtr(0.2),
		InputCostPerImage:       &imageCost,
		CacheReadInputTokenCost: &cacheRead,
	}

	applyCatalogPricingMetadata(&model, pricing, false)

	if model.Pricing == nil {
		t.Fatal("expected pricing to be backfilled")
	}
	if model.Pricing.Prompt == nil || *model.Pricing.Prompt != "0.1000000000" {
		t.Fatalf("expected prompt price 0.1000000000, got %#v", model.Pricing)
	}
	if model.Pricing.Completion == nil || *model.Pricing.Completion != "0.2000000000" {
		t.Fatalf("expected completion price 0.2000000000, got %#v", model.Pricing)
	}
	if model.Pricing.Image == nil || *model.Pricing.Image != "0.0300000000" {
		t.Fatalf("expected image price 0.0300000000, got %#v", model.Pricing)
	}
	if model.Pricing.InputCacheRead == nil || *model.Pricing.InputCacheRead != "0.0040000000" {
		t.Fatalf("expected input_cache_read price 0.0040000000, got %#v", model.Pricing)
	}

	existingPrompt := "already-set"
	model.Pricing = &schemas.Pricing{Prompt: &existingPrompt}
	applyCatalogPricingMetadata(&model, &modelcatalog.PricingEntry{InputCostPerToken: floatPtr(9), OutputCostPerToken: floatPtr(9)}, true)
	if model.Pricing.Prompt == nil || *model.Pricing.Prompt != existingPrompt {
		t.Fatalf("expected existing pricing to be preserved, got %#v", model.Pricing)
	}
	if model.Pricing.Completion != nil {
		t.Fatalf("expected existing pricing object to remain untouched, got %#v", model.Pricing)
	}
}

func newHTTPBackedTestModelCatalog(t *testing.T, pricingData map[string]modelcatalog.PricingEntry) *modelcatalog.ModelCatalog {
	t.Helper()

	payload, err := json.Marshal(pricingData)
	if err != nil {
		t.Fatalf("failed to marshal pricing data: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(payload)
	}))
	t.Cleanup(server.Close)

	pricingURL := server.URL
	catalog, err := modelcatalog.Init(t.Context(), &modelcatalog.Config{PricingURL: &pricingURL}, nil, &mockLogger{})
	if err != nil {
		t.Fatalf("failed to initialize model catalog: %v", err)
	}
	t.Cleanup(func() {
		if err := catalog.Cleanup(); err != nil {
			t.Errorf("failed to clean up model catalog: %v", err)
		}
	})

	return catalog
}

func intPtr(v int) *int { return &v }

func floatPtr(v float64) *float64 { return &v }
