package handlers

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

func validatePricingOverrides(overrides []schemas.ProviderPricingOverride) error {
	for i, override := range overrides {
		if strings.TrimSpace(override.ModelPattern) == "" {
			return fmt.Errorf("override[%d]: model_pattern is required", i)
		}

		switch override.MatchType {
		case schemas.PricingOverrideMatchExact:
			if strings.Contains(override.ModelPattern, "*") {
				return fmt.Errorf("override[%d]: exact match_type cannot include '*'", i)
			}
		case schemas.PricingOverrideMatchWildcard:
			if !strings.Contains(override.ModelPattern, "*") {
				return fmt.Errorf("override[%d]: wildcard match_type requires '*' in model_pattern", i)
			}
		case schemas.PricingOverrideMatchRegex:
			if _, err := regexp.Compile(override.ModelPattern); err != nil {
				return fmt.Errorf("override[%d]: invalid regex pattern: %w", i, err)
			}
		default:
			return fmt.Errorf("override[%d]: unsupported match_type %q", i, override.MatchType)
		}

		for _, requestType := range override.RequestTypes {
			if !isSupportedOverrideRequestType(requestType) {
				return fmt.Errorf("override[%d]: unsupported request_type %q", i, requestType)
			}
		}

		if err := validatePricingOverrideNonNegativeFields(i, override); err != nil {
			return err
		}
	}

	return nil
}

func isSupportedOverrideRequestType(requestType schemas.RequestType) bool {
	switch requestType {
	case schemas.TextCompletionRequest,
		schemas.TextCompletionStreamRequest,
		schemas.ChatCompletionRequest,
		schemas.ChatCompletionStreamRequest,
		schemas.ResponsesRequest,
		schemas.ResponsesStreamRequest,
		schemas.EmbeddingRequest,
		schemas.RerankRequest,
		schemas.SpeechRequest,
		schemas.SpeechStreamRequest,
		schemas.TranscriptionRequest,
		schemas.TranscriptionStreamRequest,
		schemas.ImageGenerationRequest,
		schemas.ImageGenerationStreamRequest:
		return true
	default:
		return false
	}
}

func validatePricingOverrideNonNegativeFields(index int, override schemas.ProviderPricingOverride) error {
	optionalValues := map[string]*float64{
		"input_cost_per_token":                              override.InputCostPerToken,
		"output_cost_per_token":                             override.OutputCostPerToken,
		"input_cost_per_video_per_second":                   override.InputCostPerVideoPerSecond,
		"input_cost_per_audio_per_second":                   override.InputCostPerAudioPerSecond,
		"input_cost_per_character":                          override.InputCostPerCharacter,
		"output_cost_per_character":                         override.OutputCostPerCharacter,
		"input_cost_per_token_above_128k_tokens":            override.InputCostPerTokenAbove128kTokens,
		"input_cost_per_character_above_128k_tokens":        override.InputCostPerCharacterAbove128kTokens,
		"input_cost_per_image_above_128k_tokens":            override.InputCostPerImageAbove128kTokens,
		"input_cost_per_video_per_second_above_128k_tokens": override.InputCostPerVideoPerSecondAbove128kTokens,
		"input_cost_per_audio_per_second_above_128k_tokens": override.InputCostPerAudioPerSecondAbove128kTokens,
		"output_cost_per_token_above_128k_tokens":           override.OutputCostPerTokenAbove128kTokens,
		"output_cost_per_character_above_128k_tokens":       override.OutputCostPerCharacterAbove128kTokens,
		"input_cost_per_token_above_200k_tokens":            override.InputCostPerTokenAbove200kTokens,
		"output_cost_per_token_above_200k_tokens":           override.OutputCostPerTokenAbove200kTokens,
		"cache_creation_input_token_cost_above_200k_tokens": override.CacheCreationInputTokenCostAbove200kTokens,
		"cache_read_input_token_cost_above_200k_tokens":     override.CacheReadInputTokenCostAbove200kTokens,
		"cache_read_input_token_cost":                       override.CacheReadInputTokenCost,
		"cache_creation_input_token_cost":                   override.CacheCreationInputTokenCost,
		"input_cost_per_token_batches":                      override.InputCostPerTokenBatches,
		"output_cost_per_token_batches":                     override.OutputCostPerTokenBatches,
		"input_cost_per_image_token":                        override.InputCostPerImageToken,
		"output_cost_per_image_token":                       override.OutputCostPerImageToken,
		"input_cost_per_image":                              override.InputCostPerImage,
		"output_cost_per_image":                             override.OutputCostPerImage,
		"cache_read_input_image_token_cost":                 override.CacheReadInputImageTokenCost,
	}

	for fieldName, value := range optionalValues {
		if value != nil && *value < 0 {
			return fmt.Errorf("override[%d]: %s must be non-negative", index, fieldName)
		}
	}

	return nil
}

func hasPricingOverridePatchFields(override schemas.ProviderPricingOverride) bool {
	optionalValues := []*float64{
		override.InputCostPerToken,
		override.OutputCostPerToken,
		override.InputCostPerVideoPerSecond,
		override.InputCostPerAudioPerSecond,
		override.InputCostPerCharacter,
		override.OutputCostPerCharacter,
		override.InputCostPerTokenAbove128kTokens,
		override.InputCostPerCharacterAbove128kTokens,
		override.InputCostPerImageAbove128kTokens,
		override.InputCostPerVideoPerSecondAbove128kTokens,
		override.InputCostPerAudioPerSecondAbove128kTokens,
		override.OutputCostPerTokenAbove128kTokens,
		override.OutputCostPerCharacterAbove128kTokens,
		override.InputCostPerTokenAbove200kTokens,
		override.OutputCostPerTokenAbove200kTokens,
		override.CacheCreationInputTokenCostAbove200kTokens,
		override.CacheReadInputTokenCostAbove200kTokens,
		override.CacheReadInputTokenCost,
		override.CacheCreationInputTokenCost,
		override.InputCostPerTokenBatches,
		override.OutputCostPerTokenBatches,
		override.InputCostPerImageToken,
		override.OutputCostPerImageToken,
		override.InputCostPerImage,
		override.OutputCostPerImage,
		override.CacheReadInputImageTokenCost,
	}

	for _, value := range optionalValues {
		if value != nil {
			return true
		}
	}
	return false
}
