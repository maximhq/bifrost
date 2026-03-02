package schemas

import (
	"fmt"
	"strings"
	"time"
)

type PricingOverrideScopeKind string

const (
	PricingOverrideScopeKindGlobal                PricingOverrideScopeKind = "global"
	PricingOverrideScopeKindProvider              PricingOverrideScopeKind = "provider"
	PricingOverrideScopeKindProviderKey           PricingOverrideScopeKind = "provider_key"
	PricingOverrideScopeKindVirtualKey            PricingOverrideScopeKind = "virtual_key"
	PricingOverrideScopeKindVirtualKeyProvider    PricingOverrideScopeKind = "virtual_key_provider"
	PricingOverrideScopeKindVirtualKeyProviderKey PricingOverrideScopeKind = "virtual_key_provider_key"
)

type PricingOverrideMatchType string

const (
	PricingOverrideMatchExact    PricingOverrideMatchType = "exact"
	PricingOverrideMatchWildcard PricingOverrideMatchType = "wildcard"
)

type PricingOverridePatch struct {
	InputCostPerToken          *float64 `json:"input_cost_per_token,omitempty"`
	OutputCostPerToken         *float64 `json:"output_cost_per_token,omitempty"`
	InputCostPerTokenPriority  *float64 `json:"input_cost_per_token_priority,omitempty"`
	OutputCostPerTokenPriority *float64 `json:"output_cost_per_token_priority,omitempty"`

	InputCostPerVideoPerSecond  *float64 `json:"input_cost_per_video_per_second,omitempty"`
	OutputCostPerVideoPerSecond *float64 `json:"output_cost_per_video_per_second,omitempty"`
	OutputCostPerSecond         *float64 `json:"output_cost_per_second,omitempty"`
	InputCostPerAudioPerSecond  *float64 `json:"input_cost_per_audio_per_second,omitempty"`
	InputCostPerSecond          *float64 `json:"input_cost_per_second,omitempty"`
	InputCostPerAudioToken      *float64 `json:"input_cost_per_audio_token,omitempty"`
	OutputCostPerAudioToken     *float64 `json:"output_cost_per_audio_token,omitempty"`

	InputCostPerCharacter  *float64 `json:"input_cost_per_character,omitempty"`
	OutputCostPerCharacter *float64 `json:"output_cost_per_character,omitempty"`

	InputCostPerTokenAbove128kTokens          *float64 `json:"input_cost_per_token_above_128k_tokens,omitempty"`
	InputCostPerCharacterAbove128kTokens      *float64 `json:"input_cost_per_character_above_128k_tokens,omitempty"`
	InputCostPerImageAbove128kTokens          *float64 `json:"input_cost_per_image_above_128k_tokens,omitempty"`
	InputCostPerVideoPerSecondAbove128kTokens *float64 `json:"input_cost_per_video_per_second_above_128k_tokens,omitempty"`
	InputCostPerAudioPerSecondAbove128kTokens *float64 `json:"input_cost_per_audio_per_second_above_128k_tokens,omitempty"`
	OutputCostPerTokenAbove128kTokens         *float64 `json:"output_cost_per_token_above_128k_tokens,omitempty"`
	OutputCostPerCharacterAbove128kTokens     *float64 `json:"output_cost_per_character_above_128k_tokens,omitempty"`

	InputCostPerTokenAbove200kTokens           *float64 `json:"input_cost_per_token_above_200k_tokens,omitempty"`
	OutputCostPerTokenAbove200kTokens          *float64 `json:"output_cost_per_token_above_200k_tokens,omitempty"`
	CacheCreationInputTokenCostAbove200kTokens *float64 `json:"cache_creation_input_token_cost_above_200k_tokens,omitempty"`
	CacheReadInputTokenCostAbove200kTokens     *float64 `json:"cache_read_input_token_cost_above_200k_tokens,omitempty"`

	CacheReadInputTokenCost                            *float64 `json:"cache_read_input_token_cost,omitempty"`
	CacheCreationInputTokenCost                        *float64 `json:"cache_creation_input_token_cost,omitempty"`
	CacheCreationInputTokenCostAbove1hr                *float64 `json:"cache_creation_input_token_cost_above_1hr,omitempty"`
	CacheCreationInputTokenCostAbove1hrAbove200kTokens *float64 `json:"cache_creation_input_token_cost_above_1hr_above_200k_tokens,omitempty"`
	CacheCreationInputAudioTokenCost                   *float64 `json:"cache_creation_input_audio_token_cost,omitempty"`
	CacheReadInputTokenCostPriority                    *float64 `json:"cache_read_input_token_cost_priority,omitempty"`
	InputCostPerTokenBatches                           *float64 `json:"input_cost_per_token_batches,omitempty"`
	OutputCostPerTokenBatches                          *float64 `json:"output_cost_per_token_batches,omitempty"`

	InputCostPerImageToken                        *float64 `json:"input_cost_per_image_token,omitempty"`
	OutputCostPerImageToken                       *float64 `json:"output_cost_per_image_token,omitempty"`
	InputCostPerImage                             *float64 `json:"input_cost_per_image,omitempty"`
	OutputCostPerImage                            *float64 `json:"output_cost_per_image,omitempty"`
	InputCostPerPixel                             *float64 `json:"input_cost_per_pixel,omitempty"`
	OutputCostPerPixel                            *float64 `json:"output_cost_per_pixel,omitempty"`
	OutputCostPerImagePremiumImage                *float64 `json:"output_cost_per_image_premium_image,omitempty"`
	OutputCostPerImageAbove512x512Pixels          *float64 `json:"output_cost_per_image_above_512_and_512_pixels,omitempty"`
	OutputCostPerImageAbove512x512PixelsPremium   *float64 `json:"output_cost_per_image_above_512_and_512_pixels_and_premium_image,omitempty"`
	OutputCostPerImageAbove1024x1024Pixels        *float64 `json:"output_cost_per_image_above_1024_and_1024_pixels,omitempty"`
	OutputCostPerImageAbove1024x1024PixelsPremium *float64 `json:"output_cost_per_image_above_1024_and_1024_pixels_and_premium_image,omitempty"`
	CacheReadInputImageTokenCost                  *float64 `json:"cache_read_input_image_token_cost,omitempty"`

	SearchContextCostPerQuery     *float64 `json:"search_context_cost_per_query,omitempty"`
	CodeInterpreterCostPerSession *float64 `json:"code_interpreter_cost_per_session,omitempty"`
}

type PricingOverride struct {
	ID            string                   `json:"id"`
	Name          string                   `json:"name"`
	ScopeKind     PricingOverrideScopeKind `json:"scope_kind"`
	VirtualKeyID  *string                  `json:"virtual_key_id,omitempty"`
	ProviderID    *string                  `json:"provider_id,omitempty"`
	ProviderKeyID *string                  `json:"provider_key_id,omitempty"`
	MatchType     PricingOverrideMatchType `json:"match_type"`
	Pattern       string                   `json:"pattern"`
	RequestTypes  []RequestType            `json:"request_types,omitempty"`
	Patch         PricingOverridePatch     `json:"patch,omitempty"`
	ConfigHash    string                   `json:"config_hash,omitempty"`
	CreatedAt     time.Time                `json:"created_at,omitempty"`
	UpdatedAt     time.Time                `json:"updated_at,omitempty"`
}

func IsSupportedPricingOverrideRequestType(requestType RequestType) bool {
	switch requestType {
	case TextCompletionRequest,
		TextCompletionStreamRequest,
		ChatCompletionRequest,
		ChatCompletionStreamRequest,
		ResponsesRequest,
		ResponsesStreamRequest,
		EmbeddingRequest,
		RerankRequest,
		SpeechRequest,
		SpeechStreamRequest,
		TranscriptionRequest,
		TranscriptionStreamRequest,
		ImageGenerationRequest,
		ImageGenerationStreamRequest,
		ImageEditRequest,
		ImageEditStreamRequest,
		ImageVariationRequest,
		VideoGenerationRequest:
		return true
	default:
		return false
	}
}

func ValidatePricingOverridePattern(matchType PricingOverrideMatchType, pattern string) (string, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	switch matchType {
	case PricingOverrideMatchExact:
		if strings.Contains(pattern, "*") {
			return "", fmt.Errorf("exact pattern cannot include '*'")
		}
	case PricingOverrideMatchWildcard:
		if !strings.HasSuffix(pattern, "*") || strings.Count(pattern, "*") != 1 {
			return "", fmt.Errorf("wildcard pattern supports a single trailing '*' only")
		}
		if strings.TrimSuffix(pattern, "*") == "" {
			return "", fmt.Errorf("wildcard prefix cannot be empty")
		}
	default:
		return "", fmt.Errorf("unsupported match_type %q", matchType)
	}
	return pattern, nil
}

func ValidatePricingOverrideRequestTypes(requestTypes []RequestType) error {
	for _, requestType := range requestTypes {
		if !IsSupportedPricingOverrideRequestType(requestType) {
			return fmt.Errorf("unsupported request_type %q", requestType)
		}
	}
	return nil
}

func ValidatePricingOverridePatchNonNegative(patch PricingOverridePatch) error {
	values := []struct {
		name  string
		value *float64
	}{
		{name: "input_cost_per_token", value: patch.InputCostPerToken},
		{name: "output_cost_per_token", value: patch.OutputCostPerToken},
		{name: "input_cost_per_token_priority", value: patch.InputCostPerTokenPriority},
		{name: "output_cost_per_token_priority", value: patch.OutputCostPerTokenPriority},
		{name: "input_cost_per_video_per_second", value: patch.InputCostPerVideoPerSecond},
		{name: "output_cost_per_video_per_second", value: patch.OutputCostPerVideoPerSecond},
		{name: "output_cost_per_second", value: patch.OutputCostPerSecond},
		{name: "input_cost_per_audio_per_second", value: patch.InputCostPerAudioPerSecond},
		{name: "input_cost_per_second", value: patch.InputCostPerSecond},
		{name: "input_cost_per_audio_token", value: patch.InputCostPerAudioToken},
		{name: "output_cost_per_audio_token", value: patch.OutputCostPerAudioToken},
		{name: "input_cost_per_character", value: patch.InputCostPerCharacter},
		{name: "output_cost_per_character", value: patch.OutputCostPerCharacter},
		{name: "input_cost_per_token_above_128k_tokens", value: patch.InputCostPerTokenAbove128kTokens},
		{name: "input_cost_per_character_above_128k_tokens", value: patch.InputCostPerCharacterAbove128kTokens},
		{name: "input_cost_per_image_above_128k_tokens", value: patch.InputCostPerImageAbove128kTokens},
		{name: "input_cost_per_video_per_second_above_128k_tokens", value: patch.InputCostPerVideoPerSecondAbove128kTokens},
		{name: "input_cost_per_audio_per_second_above_128k_tokens", value: patch.InputCostPerAudioPerSecondAbove128kTokens},
		{name: "output_cost_per_token_above_128k_tokens", value: patch.OutputCostPerTokenAbove128kTokens},
		{name: "output_cost_per_character_above_128k_tokens", value: patch.OutputCostPerCharacterAbove128kTokens},
		{name: "input_cost_per_token_above_200k_tokens", value: patch.InputCostPerTokenAbove200kTokens},
		{name: "output_cost_per_token_above_200k_tokens", value: patch.OutputCostPerTokenAbove200kTokens},
		{name: "cache_creation_input_token_cost_above_200k_tokens", value: patch.CacheCreationInputTokenCostAbove200kTokens},
		{name: "cache_read_input_token_cost_above_200k_tokens", value: patch.CacheReadInputTokenCostAbove200kTokens},
		{name: "cache_read_input_token_cost", value: patch.CacheReadInputTokenCost},
		{name: "cache_creation_input_token_cost", value: patch.CacheCreationInputTokenCost},
		{name: "cache_creation_input_token_cost_above_1hr", value: patch.CacheCreationInputTokenCostAbove1hr},
		{name: "cache_creation_input_token_cost_above_1hr_above_200k_tokens", value: patch.CacheCreationInputTokenCostAbove1hrAbove200kTokens},
		{name: "cache_creation_input_audio_token_cost", value: patch.CacheCreationInputAudioTokenCost},
		{name: "cache_read_input_token_cost_priority", value: patch.CacheReadInputTokenCostPriority},
		{name: "input_cost_per_token_batches", value: patch.InputCostPerTokenBatches},
		{name: "output_cost_per_token_batches", value: patch.OutputCostPerTokenBatches},
		{name: "input_cost_per_image_token", value: patch.InputCostPerImageToken},
		{name: "output_cost_per_image_token", value: patch.OutputCostPerImageToken},
		{name: "input_cost_per_image", value: patch.InputCostPerImage},
		{name: "output_cost_per_image", value: patch.OutputCostPerImage},
		{name: "input_cost_per_pixel", value: patch.InputCostPerPixel},
		{name: "output_cost_per_pixel", value: patch.OutputCostPerPixel},
		{name: "output_cost_per_image_premium_image", value: patch.OutputCostPerImagePremiumImage},
		{name: "output_cost_per_image_above_512_and_512_pixels", value: patch.OutputCostPerImageAbove512x512Pixels},
		{name: "output_cost_per_image_above_512_and_512_pixels_and_premium_image", value: patch.OutputCostPerImageAbove512x512PixelsPremium},
		{name: "output_cost_per_image_above_1024_and_1024_pixels", value: patch.OutputCostPerImageAbove1024x1024Pixels},
		{name: "output_cost_per_image_above_1024_and_1024_pixels_and_premium_image", value: patch.OutputCostPerImageAbove1024x1024PixelsPremium},
		{name: "cache_read_input_image_token_cost", value: patch.CacheReadInputImageTokenCost},
		{name: "search_context_cost_per_query", value: patch.SearchContextCostPerQuery},
		{name: "code_interpreter_cost_per_session", value: patch.CodeInterpreterCostPerSession},
	}
	for _, item := range values {
		if item.value != nil && *item.value < 0 {
			return fmt.Errorf("%s must be non-negative", item.name)
		}
	}
	return nil
}

func ValidatePricingOverrideScopeKind(scopeKind PricingOverrideScopeKind, virtualKeyID, providerID, providerKeyID *string) error {
	normalizedVK := normalizeOptionalID(virtualKeyID)
	normalizedProvider := normalizeOptionalID(providerID)
	normalizedProviderKey := normalizeOptionalID(providerKeyID)

	switch scopeKind {
	case PricingOverrideScopeKindGlobal:
		if normalizedVK != nil || normalizedProvider != nil || normalizedProviderKey != nil {
			return fmt.Errorf("global scope_kind must not include scope identifiers")
		}
	case PricingOverrideScopeKindProvider:
		if normalizedProvider == nil {
			return fmt.Errorf("provider_id is required for provider scope_kind")
		}
		if normalizedVK != nil || normalizedProviderKey != nil {
			return fmt.Errorf("provider scope_kind only supports provider_id")
		}
	case PricingOverrideScopeKindProviderKey:
		if normalizedProviderKey == nil {
			return fmt.Errorf("provider_key_id is required for provider_key scope_kind")
		}
		if normalizedVK != nil || normalizedProvider != nil {
			return fmt.Errorf("provider_key scope_kind only supports provider_key_id")
		}
	case PricingOverrideScopeKindVirtualKey:
		if normalizedVK == nil {
			return fmt.Errorf("virtual_key_id is required for virtual_key scope_kind")
		}
		if normalizedProvider != nil || normalizedProviderKey != nil {
			return fmt.Errorf("virtual_key scope_kind only supports virtual_key_id")
		}
	case PricingOverrideScopeKindVirtualKeyProvider:
		if normalizedVK == nil || normalizedProvider == nil {
			return fmt.Errorf("virtual_key_id and provider_id are required for virtual_key_provider scope_kind")
		}
		if normalizedProviderKey != nil {
			return fmt.Errorf("virtual_key_provider scope_kind does not support provider_key_id")
		}
	case PricingOverrideScopeKindVirtualKeyProviderKey:
		if normalizedVK == nil || normalizedProvider == nil || normalizedProviderKey == nil {
			return fmt.Errorf("virtual_key_id, provider_id, and provider_key_id are required for virtual_key_provider_key scope_kind")
		}
	default:
		return fmt.Errorf("unsupported scope_kind %q", scopeKind)
	}
	return nil
}

func normalizeOptionalID(id *string) *string {
	if id == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*id)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}
