package datasheet

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/stretchr/testify/assert"
)

// azureModelRouterPricing mirrors the real "azure/model-router" datasheet
// entry: a flat per-input-token infra surcharge, no output surcharge.
func azureModelRouterPricing() configstoreTables.TableModelPricing {
	return configstoreTables.TableModelPricing{
		Model:              "model-router",
		Provider:           "azure",
		Mode:               "chat",
		InputCostPerToken:  new(0.00000014), // $0.14 / M input tokens
		OutputCostPerToken: nil,             // no output surcharge
	}
}

func TestCalculateCost_AzureModelRouter_ChatCompletion(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("model-router", "azure", "chat"): azureModelRouterPricing(),
		makeKey("gpt-4.1-mini", "azure", "chat"): {
			Model: "gpt-4.1-mini", Provider: "azure", Mode: "chat",
			InputCostPerToken:  new(0.0000004),
			OutputCostPerToken: new(0.0000016),
		},
	})

	resp := makeChatResponse(schemas.Azure, "model-router", &schemas.BifrostLLMUsage{
		PromptTokens:     10000,
		CompletionTokens: 2000,
		TotalTokens:      12000,
	})
	resp.ChatResponse.Model = "gpt-4.1-mini" // the model Model Router actually served

	cost := s.CalculateCost(resp, nil)
	// Surcharge:  10000 * 0.00000014                       = 0.0014
	// Underlying: 10000 * 0.0000004 + 2000 * 0.0000016      = 0.004 + 0.0032 = 0.0072
	// Total: 0.0086
	assert.InDelta(t, 0.0086, cost, 1e-12)
}

func TestCalculateCost_AzureModelRouter_Responses(t *testing.T) {
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("model-router", "azure", "responses"): {
			Model: "model-router", Provider: "azure", Mode: "responses",
			InputCostPerToken: new(0.00000014),
		},
		makeKey("gpt-4.1-mini", "azure", "responses"): {
			Model: "gpt-4.1-mini", Provider: "azure", Mode: "responses",
			InputCostPerToken:  new(0.0000004),
			OutputCostPerToken: new(0.0000016),
		},
	})

	resp := &schemas.BifrostResponse{
		ResponsesResponse: &schemas.BifrostResponsesResponse{
			Model: "gpt-4.1-mini", // the model Model Router actually served
			Usage: &schemas.ResponsesResponseUsage{
				InputTokens:  10000,
				OutputTokens: 2000,
				TotalTokens:  12000,
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ResponsesRequest,
				RoutingInfo: routingInfoFor(schemas.Azure, "model-router"),
			},
		},
	}

	cost := s.CalculateCost(resp, nil)
	// Surcharge:  10000 * 0.00000014                  = 0.0014
	// Underlying: 10000 * 0.0000004 + 2000 * 0.0000016 = 0.0072
	// Total: 0.0086
	assert.InDelta(t, 0.0086, cost, 1e-12)
}

func TestCalculateCost_AzureModelRouter_ResponsesStream(t *testing.T) {
	// Streaming Responses carries usage/model wrapped one level deeper, in
	// ResponsesStreamResponse.Response — the shape framework/streaming/responses.go
	// passes to CalculateCost on the final chunk.
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("model-router", "azure", "responses"): {
			Model: "model-router", Provider: "azure", Mode: "responses",
			InputCostPerToken: new(0.00000014),
		},
		makeKey("gpt-4.1-mini", "azure", "responses"): {
			Model: "gpt-4.1-mini", Provider: "azure", Mode: "responses",
			InputCostPerToken:  new(0.0000004),
			OutputCostPerToken: new(0.0000016),
		},
	})

	resp := &schemas.BifrostResponse{
		ResponsesStreamResponse: &schemas.BifrostResponsesStreamResponse{
			Response: &schemas.BifrostResponsesResponse{
				Model: "gpt-4.1-mini", // the model Model Router actually served
				Usage: &schemas.ResponsesResponseUsage{
					InputTokens:  10000,
					OutputTokens: 2000,
					TotalTokens:  12000,
				},
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.ResponsesStreamRequest,
				RoutingInfo: routingInfoFor(schemas.Azure, "model-router"),
			},
		},
	}

	cost := s.CalculateCost(resp, nil)
	// Surcharge:  10000 * 0.00000014                  = 0.0014
	// Underlying: 10000 * 0.0000004 + 2000 * 0.0000016 = 0.0072
	// Total: 0.0086
	assert.InDelta(t, 0.0086, cost, 1e-12)
}

func TestCalculateCost_AzureModelRouter_TextCompletion_ConvertedToChat(t *testing.T) {
	// Model Router has no native /completions support: the compat plugin
	// always converts a text completion into a chat call under the hood, so
	// a successful response arrives as TextCompletionResponse with
	// ExtraFields.RequestType == TextCompletionRequest, but must still price
	// against the catalog's "chat" mode rows (there is no "completion" mode
	// row for either model-router or the model it routed to).
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("model-router", "azure", "chat"): azureModelRouterPricing(),
		makeKey("gpt-4.1-mini", "azure", "chat"): {
			Model: "gpt-4.1-mini", Provider: "azure", Mode: "chat",
			InputCostPerToken:  new(0.0000004),
			OutputCostPerToken: new(0.0000016),
		},
	})

	resp := &schemas.BifrostResponse{
		TextCompletionResponse: &schemas.BifrostTextCompletionResponse{
			Model: "gpt-4.1-mini", // the model Model Router actually served
			Usage: &schemas.BifrostLLMUsage{
				PromptTokens:     10000,
				CompletionTokens: 2000,
				TotalTokens:      12000,
			},
			ExtraFields: schemas.BifrostResponseExtraFields{
				RequestType: schemas.TextCompletionRequest,
				RoutingInfo: routingInfoFor(schemas.Azure, "model-router"),
			},
		},
	}

	cost := s.CalculateCost(resp, nil)
	// Surcharge:  10000 * 0.00000014                  = 0.0014
	// Underlying: 10000 * 0.0000004 + 2000 * 0.0000016 = 0.0072
	// Total: 0.0086
	assert.InDelta(t, 0.0086, cost, 1e-12)
}

func TestCalculateCost_AzureModelRouter_NoUnderlyingPricing(t *testing.T) {
	// Only the model-router row exists — underlying model has no catalog entry.
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("model-router", "azure", "chat"): azureModelRouterPricing(),
	})

	resp := makeChatResponse(schemas.Azure, "model-router", &schemas.BifrostLLMUsage{
		PromptTokens:     10000,
		CompletionTokens: 2000,
		TotalTokens:      12000,
	})
	resp.ChatResponse.Model = "some-unpriced-model"

	cost := s.CalculateCost(resp, nil)
	// Only the surcharge applies: 10000 * 0.00000014 = 0.0014
	assert.InDelta(t, 0.0014, cost, 1e-12)
}

func TestCalculateCost_AzureModelRouter_ServedModelSameAsRouter_NoDoubleCounting(t *testing.T) {
	// Defensive: if the response echoes "model-router" as its own model (should
	// not happen in practice), the underlying-model cost must not be added a
	// second time on top of the surcharge.
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("model-router", "azure", "chat"): azureModelRouterPricing(),
	})

	resp := makeChatResponse(schemas.Azure, "model-router", &schemas.BifrostLLMUsage{
		PromptTokens:     10000,
		CompletionTokens: 2000,
		TotalTokens:      12000,
	})
	resp.ChatResponse.Model = "model-router"

	cost := s.CalculateCost(resp, nil)
	assert.InDelta(t, 0.0014, cost, 1e-12)
}

func TestCalculateCost_AzureModelRouter_NonAzureProviderUnaffected(t *testing.T) {
	// A model name containing "model-router" under a non-Azure provider must
	// not trigger the surcharge-plus-underlying-model split.
	s := testStoreWithPricing(map[string]configstoreTables.TableModelPricing{
		makeKey("model-router", "openai", "chat"): {
			Model: "model-router", Provider: "openai", Mode: "chat",
			InputCostPerToken:  new(0.000005),
			OutputCostPerToken: new(0.000015),
		},
	})

	resp := makeChatResponse(schemas.OpenAI, "model-router", &schemas.BifrostLLMUsage{
		PromptTokens:     10000,
		CompletionTokens: 2000,
		TotalTokens:      12000,
	})
	resp.ChatResponse.Model = "gpt-4.1-mini"

	cost := s.CalculateCost(resp, nil)
	// Plain pricing: 10000*0.000005 + 2000*0.000015 = 0.05 + 0.03 = 0.08
	assert.InDelta(t, 0.08, cost, 1e-12)
}
