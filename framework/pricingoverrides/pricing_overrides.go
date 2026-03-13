// Package pricingoverrides defines the shared pricing override contract used by
// config storage, model catalog compilation, and HTTP governance handlers.
package pricingoverrides

import (
	"fmt"
	"strings"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// ScopeKind identifies which governance scope an override applies to.
type ScopeKind string

const (
	ScopeKindGlobal                ScopeKind = "global"
	ScopeKindProvider              ScopeKind = "provider"
	ScopeKindProviderKey           ScopeKind = "provider_key"
	ScopeKindVirtualKey            ScopeKind = "virtual_key"
	ScopeKindVirtualKeyProvider    ScopeKind = "virtual_key_provider"
	ScopeKindVirtualKeyProviderKey ScopeKind = "virtual_key_provider_key"
)

// MatchType controls how an override pattern is matched against model names.
type MatchType string

const (
	MatchTypeExact    MatchType = "exact"
	MatchTypeWildcard MatchType = "wildcard"
)

// Patch is a sparse pricing override payload.
//
// Nil fields mean "leave the base pricing unchanged".
type Patch struct {
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

// Override describes a scoped pricing override shared across config storage,
// model catalog compilation, and governance APIs.
type Override struct {
	ID            string                `json:"id"`
	Name          string                `json:"name"`
	ScopeKind     ScopeKind             `json:"scope_kind"`
	VirtualKeyID  *string               `json:"virtual_key_id,omitempty"`
	ProviderID    *string               `json:"provider_id,omitempty"`
	ProviderKeyID *string               `json:"provider_key_id,omitempty"`
	MatchType     MatchType             `json:"match_type"`
	Pattern       string                `json:"pattern"`
	RequestTypes  []schemas.RequestType `json:"request_types,omitempty"`
	Patch         Patch                 `json:"patch,omitempty"`
	CreatedAt     time.Time             `json:"created_at,omitempty"`
	UpdatedAt     time.Time             `json:"updated_at,omitempty"`
}

// NormalizeOverride trims identifiers and validates the shared pricing override
// contract before persistence or runtime compilation.
func NormalizeOverride(override Override) (Override, error) {
	override.Name = strings.TrimSpace(override.Name)
	if override.Name == "" {
		return Override{}, fmt.Errorf("name is required")
	}

	override.VirtualKeyID = normalizeOptionalID(override.VirtualKeyID)
	override.ProviderID = normalizeOptionalID(override.ProviderID)
	override.ProviderKeyID = normalizeOptionalID(override.ProviderKeyID)

	if err := ValidateScopeKind(override.ScopeKind, override.VirtualKeyID, override.ProviderID, override.ProviderKeyID); err != nil {
		return Override{}, err
	}

	normalizedPattern, err := ValidatePattern(override.MatchType, override.Pattern)
	if err != nil {
		return Override{}, err
	}
	override.Pattern = normalizedPattern

	if err := ValidateRequestTypes(override.RequestTypes); err != nil {
		return Override{}, err
	}
	if err := ValidatePatchNonNegative(override.Patch); err != nil {
		return Override{}, err
	}

	return override, nil
}

// MergePatch overlays non-nil fields from updates onto base.
func MergePatch(base, updates Patch) Patch {
	merged := base

	if updates.InputCostPerToken != nil {
		merged.InputCostPerToken = updates.InputCostPerToken
	}
	if updates.OutputCostPerToken != nil {
		merged.OutputCostPerToken = updates.OutputCostPerToken
	}
	if updates.InputCostPerTokenPriority != nil {
		merged.InputCostPerTokenPriority = updates.InputCostPerTokenPriority
	}
	if updates.OutputCostPerTokenPriority != nil {
		merged.OutputCostPerTokenPriority = updates.OutputCostPerTokenPriority
	}
	if updates.InputCostPerVideoPerSecond != nil {
		merged.InputCostPerVideoPerSecond = updates.InputCostPerVideoPerSecond
	}
	if updates.OutputCostPerVideoPerSecond != nil {
		merged.OutputCostPerVideoPerSecond = updates.OutputCostPerVideoPerSecond
	}
	if updates.OutputCostPerSecond != nil {
		merged.OutputCostPerSecond = updates.OutputCostPerSecond
	}
	if updates.InputCostPerAudioPerSecond != nil {
		merged.InputCostPerAudioPerSecond = updates.InputCostPerAudioPerSecond
	}
	if updates.InputCostPerSecond != nil {
		merged.InputCostPerSecond = updates.InputCostPerSecond
	}
	if updates.InputCostPerAudioToken != nil {
		merged.InputCostPerAudioToken = updates.InputCostPerAudioToken
	}
	if updates.OutputCostPerAudioToken != nil {
		merged.OutputCostPerAudioToken = updates.OutputCostPerAudioToken
	}
	if updates.InputCostPerCharacter != nil {
		merged.InputCostPerCharacter = updates.InputCostPerCharacter
	}
	if updates.OutputCostPerCharacter != nil {
		merged.OutputCostPerCharacter = updates.OutputCostPerCharacter
	}
	if updates.InputCostPerTokenAbove128kTokens != nil {
		merged.InputCostPerTokenAbove128kTokens = updates.InputCostPerTokenAbove128kTokens
	}
	if updates.InputCostPerCharacterAbove128kTokens != nil {
		merged.InputCostPerCharacterAbove128kTokens = updates.InputCostPerCharacterAbove128kTokens
	}
	if updates.InputCostPerImageAbove128kTokens != nil {
		merged.InputCostPerImageAbove128kTokens = updates.InputCostPerImageAbove128kTokens
	}
	if updates.InputCostPerVideoPerSecondAbove128kTokens != nil {
		merged.InputCostPerVideoPerSecondAbove128kTokens = updates.InputCostPerVideoPerSecondAbove128kTokens
	}
	if updates.InputCostPerAudioPerSecondAbove128kTokens != nil {
		merged.InputCostPerAudioPerSecondAbove128kTokens = updates.InputCostPerAudioPerSecondAbove128kTokens
	}
	if updates.OutputCostPerTokenAbove128kTokens != nil {
		merged.OutputCostPerTokenAbove128kTokens = updates.OutputCostPerTokenAbove128kTokens
	}
	if updates.OutputCostPerCharacterAbove128kTokens != nil {
		merged.OutputCostPerCharacterAbove128kTokens = updates.OutputCostPerCharacterAbove128kTokens
	}
	if updates.InputCostPerTokenAbove200kTokens != nil {
		merged.InputCostPerTokenAbove200kTokens = updates.InputCostPerTokenAbove200kTokens
	}
	if updates.OutputCostPerTokenAbove200kTokens != nil {
		merged.OutputCostPerTokenAbove200kTokens = updates.OutputCostPerTokenAbove200kTokens
	}
	if updates.CacheCreationInputTokenCostAbove200kTokens != nil {
		merged.CacheCreationInputTokenCostAbove200kTokens = updates.CacheCreationInputTokenCostAbove200kTokens
	}
	if updates.CacheReadInputTokenCostAbove200kTokens != nil {
		merged.CacheReadInputTokenCostAbove200kTokens = updates.CacheReadInputTokenCostAbove200kTokens
	}
	if updates.CacheReadInputTokenCost != nil {
		merged.CacheReadInputTokenCost = updates.CacheReadInputTokenCost
	}
	if updates.CacheCreationInputTokenCost != nil {
		merged.CacheCreationInputTokenCost = updates.CacheCreationInputTokenCost
	}
	if updates.CacheCreationInputTokenCostAbove1hr != nil {
		merged.CacheCreationInputTokenCostAbove1hr = updates.CacheCreationInputTokenCostAbove1hr
	}
	if updates.CacheCreationInputTokenCostAbove1hrAbove200kTokens != nil {
		merged.CacheCreationInputTokenCostAbove1hrAbove200kTokens = updates.CacheCreationInputTokenCostAbove1hrAbove200kTokens
	}
	if updates.CacheCreationInputAudioTokenCost != nil {
		merged.CacheCreationInputAudioTokenCost = updates.CacheCreationInputAudioTokenCost
	}
	if updates.CacheReadInputTokenCostPriority != nil {
		merged.CacheReadInputTokenCostPriority = updates.CacheReadInputTokenCostPriority
	}
	if updates.InputCostPerTokenBatches != nil {
		merged.InputCostPerTokenBatches = updates.InputCostPerTokenBatches
	}
	if updates.OutputCostPerTokenBatches != nil {
		merged.OutputCostPerTokenBatches = updates.OutputCostPerTokenBatches
	}
	if updates.InputCostPerImageToken != nil {
		merged.InputCostPerImageToken = updates.InputCostPerImageToken
	}
	if updates.OutputCostPerImageToken != nil {
		merged.OutputCostPerImageToken = updates.OutputCostPerImageToken
	}
	if updates.InputCostPerImage != nil {
		merged.InputCostPerImage = updates.InputCostPerImage
	}
	if updates.OutputCostPerImage != nil {
		merged.OutputCostPerImage = updates.OutputCostPerImage
	}
	if updates.InputCostPerPixel != nil {
		merged.InputCostPerPixel = updates.InputCostPerPixel
	}
	if updates.OutputCostPerPixel != nil {
		merged.OutputCostPerPixel = updates.OutputCostPerPixel
	}
	if updates.OutputCostPerImagePremiumImage != nil {
		merged.OutputCostPerImagePremiumImage = updates.OutputCostPerImagePremiumImage
	}
	if updates.OutputCostPerImageAbove512x512Pixels != nil {
		merged.OutputCostPerImageAbove512x512Pixels = updates.OutputCostPerImageAbove512x512Pixels
	}
	if updates.OutputCostPerImageAbove512x512PixelsPremium != nil {
		merged.OutputCostPerImageAbove512x512PixelsPremium = updates.OutputCostPerImageAbove512x512PixelsPremium
	}
	if updates.OutputCostPerImageAbove1024x1024Pixels != nil {
		merged.OutputCostPerImageAbove1024x1024Pixels = updates.OutputCostPerImageAbove1024x1024Pixels
	}
	if updates.OutputCostPerImageAbove1024x1024PixelsPremium != nil {
		merged.OutputCostPerImageAbove1024x1024PixelsPremium = updates.OutputCostPerImageAbove1024x1024PixelsPremium
	}
	if updates.CacheReadInputImageTokenCost != nil {
		merged.CacheReadInputImageTokenCost = updates.CacheReadInputImageTokenCost
	}
	if updates.SearchContextCostPerQuery != nil {
		merged.SearchContextCostPerQuery = updates.SearchContextCostPerQuery
	}
	if updates.CodeInterpreterCostPerSession != nil {
		merged.CodeInterpreterCostPerSession = updates.CodeInterpreterCostPerSession
	}

	return merged
}

// IsSupportedRequestType reports whether requestType can be used in pricing
// override request filters.
func IsSupportedRequestType(requestType schemas.RequestType) bool {
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
		schemas.ImageGenerationStreamRequest,
		schemas.ImageEditRequest,
		schemas.ImageEditStreamRequest,
		schemas.ImageVariationRequest,
		schemas.VideoGenerationRequest:
		return true
	default:
		return false
	}
}

// ValidatePattern trims and validates a model pattern for the given match type.
func ValidatePattern(matchType MatchType, pattern string) (string, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	switch matchType {
	case MatchTypeExact:
		if strings.Contains(pattern, "*") {
			return "", fmt.Errorf("exact pattern cannot include '*'")
		}
	case MatchTypeWildcard:
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

// ValidateRequestTypes validates that every request type in requestTypes is
// supported by pricing overrides.
func ValidateRequestTypes(requestTypes []schemas.RequestType) error {
	for _, requestType := range requestTypes {
		if !IsSupportedRequestType(requestType) {
			return fmt.Errorf("unsupported request_type %q", requestType)
		}
	}
	return nil
}

// ValidatePatchNonNegative validates that all populated pricing values are
// non-negative.
func ValidatePatchNonNegative(patch Patch) error {
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

// ValidateScopeKind validates the scope identifiers required by scopeKind.
func ValidateScopeKind(scopeKind ScopeKind, virtualKeyID, providerID, providerKeyID *string) error {
	normalizedVK := normalizeOptionalID(virtualKeyID)
	normalizedProvider := normalizeOptionalID(providerID)
	normalizedProviderKey := normalizeOptionalID(providerKeyID)

	switch scopeKind {
	case ScopeKindGlobal:
		if normalizedVK != nil || normalizedProvider != nil || normalizedProviderKey != nil {
			return fmt.Errorf("global scope_kind must not include scope identifiers")
		}
	case ScopeKindProvider:
		if normalizedProvider == nil {
			return fmt.Errorf("provider_id is required for provider scope_kind")
		}
		if normalizedVK != nil || normalizedProviderKey != nil {
			return fmt.Errorf("provider scope_kind only supports provider_id")
		}
	case ScopeKindProviderKey:
		if normalizedProviderKey == nil {
			return fmt.Errorf("provider_key_id is required for provider_key scope_kind")
		}
		if normalizedVK != nil || normalizedProvider != nil {
			return fmt.Errorf("provider_key scope_kind only supports provider_key_id")
		}
	case ScopeKindVirtualKey:
		if normalizedVK == nil {
			return fmt.Errorf("virtual_key_id is required for virtual_key scope_kind")
		}
		if normalizedProvider != nil || normalizedProviderKey != nil {
			return fmt.Errorf("virtual_key scope_kind only supports virtual_key_id")
		}
	case ScopeKindVirtualKeyProvider:
		if normalizedVK == nil || normalizedProvider == nil {
			return fmt.Errorf("virtual_key_id and provider_id are required for virtual_key_provider scope_kind")
		}
		if normalizedProviderKey != nil {
			return fmt.Errorf("virtual_key_provider scope_kind does not support provider_key_id")
		}
	case ScopeKindVirtualKeyProviderKey:
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
