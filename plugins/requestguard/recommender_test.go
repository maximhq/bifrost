package requestguard

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

func TestRecommendModelsAllowsSameModelNameFromDifferentProvider(t *testing.T) {
	catalog := &fakeCatalog{
		requestTypeSupported: true,
		modelsByProvider: map[schemas.ModelProvider][]string{
			schemas.OpenAI:    {"same-model"},
			schemas.Anthropic: {"same-model"},
		},
		providersByModel: map[string][]schemas.ModelProvider{"same-model": {schemas.OpenAI, schemas.Anthropic}},
		pricingByRoute: map[string]*modelcatalog.PricingEntry{
			routeKey(schemas.OpenAI, "same-model"):    {},
			routeKey(schemas.Anthropic, "same-model"): {},
		},
		costByRoute: map[string]float64{
			routeKey(schemas.OpenAI, "same-model"):    0.02,
			routeKey(schemas.Anthropic, "same-model"): 0.005,
		},
	}
	features := RequestFeatures{RequestType: schemas.ChatCompletionRequest, Provider: schemas.OpenAI, Model: "same-model", Supported: true}
	usage := &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 10, TotalTokens: 20}
	recommendations := RecommendModels(
		catalog, CapabilityGuardConfig{Mode: PolicyOff}, BudgetGuardConfig{},
		RecommenderConfig{Enabled: true, MaxRecommendations: 3, MinSavingsPct: 10},
		features, CostEstimate{}, usage, nil, 0.02, false,
	)
	if len(recommendations) != 1 || recommendations[0].Provider != schemas.Anthropic || recommendations[0].Model != "same-model" {
		t.Fatalf("recommendations = %+v, want anthropic/same-model", recommendations)
	}
}

func TestRecommendModelsFiltersUnsupportedUnaryEndpoint(t *testing.T) {
	catalog := &fakeCatalog{
		modelsByProvider: map[schemas.ModelProvider][]string{schemas.OpenAI: {"candidate"}},
		pricingByRoute:   map[string]*modelcatalog.PricingEntry{routeKey(schemas.OpenAI, "candidate"): {}},
		costByRoute:      map[string]float64{routeKey(schemas.OpenAI, "candidate"): 0.001},
	}
	features := RequestFeatures{RequestType: schemas.ChatCompletionRequest, Provider: schemas.OpenAI, Model: "original", Supported: true}
	usage := &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 10, TotalTokens: 20}
	recommendations := RecommendModels(
		catalog, CapabilityGuardConfig{Mode: PolicyOff}, BudgetGuardConfig{},
		RecommenderConfig{Enabled: true, MaxRecommendations: 3, MinSavingsPct: 10},
		features, CostEstimate{}, usage, nil, 0.02, false,
	)
	if len(recommendations) != 0 {
		t.Fatalf("recommendations = %+v, want unsupported unary endpoint filtered", recommendations)
	}
}
