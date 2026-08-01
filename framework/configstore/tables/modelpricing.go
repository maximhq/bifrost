package tables

import (
	"encoding/json"

	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm"
)

// TableModelPricing represents pricing information for AI models
type TableModelPricing struct {
	ID              uint                  `gorm:"primaryKey;autoIncrement" json:"id"`
	Model           string                `gorm:"type:varchar(255);not null;uniqueIndex:idx_model_provider_mode" json:"model"`
	BaseModel       string                `gorm:"type:varchar(255);default:null" json:"base_model,omitempty"`
	Provider        string                `gorm:"type:varchar(50);not null;uniqueIndex:idx_model_provider_mode" json:"provider"`
	Mode            string                `gorm:"type:varchar(50);not null;uniqueIndex:idx_model_provider_mode" json:"mode"`
	ContextLength   *int                  `gorm:"default:null" json:"context_length,omitempty"`
	MaxInputTokens  *int                  `gorm:"default:null" json:"max_input_tokens,omitempty"`
	MaxOutputTokens *int                  `gorm:"default:null" json:"max_output_tokens,omitempty"`
	Architecture    *schemas.Architecture `gorm:"type:text;serializer:json;default:null" json:"architecture,omitempty"`
	IsDeprecated    bool                  `gorm:"default:false;column:is_deprecated" json:"is_deprecated"`

	// Costs - Text
	InputCostPerToken          *float64 `gorm:"default:null" json:"input_cost_per_token,omitempty"`
	OutputCostPerToken         *float64 `gorm:"default:null" json:"output_cost_per_token,omitempty"`
	InputCostPerTokenBatches   *float64 `gorm:"default:null;column:input_cost_per_token_batches" json:"input_cost_per_token_batches,omitempty"`
	OutputCostPerTokenBatches  *float64 `gorm:"default:null;column:output_cost_per_token_batches" json:"output_cost_per_token_batches,omitempty"`
	InputCostPerTokenPriority  *float64 `gorm:"default:null;column:input_cost_per_token_priority" json:"input_cost_per_token_priority,omitempty"`
	OutputCostPerTokenPriority *float64 `gorm:"default:null;column:output_cost_per_token_priority" json:"output_cost_per_token_priority,omitempty"`
	InputCostPerTokenFlex      *float64 `gorm:"default:null;column:input_cost_per_token_flex" json:"input_cost_per_token_flex,omitempty"`
	OutputCostPerTokenFlex     *float64 `gorm:"default:null;column:output_cost_per_token_flex" json:"output_cost_per_token_flex,omitempty"`
	// Fast mode (Anthropic research preview, speed:"fast" on Opus 4.6/4.7/4.8).
	// Flat rate across the full context window; cache tokens use the _fast cache columns below.
	InputCostPerTokenFast  *float64 `gorm:"default:null;column:input_cost_per_token_fast" json:"input_cost_per_token_fast,omitempty"`
	OutputCostPerTokenFast *float64 `gorm:"default:null;column:output_cost_per_token_fast" json:"output_cost_per_token_fast,omitempty"`
	InputCostPerCharacter  *float64 `gorm:"default:null;column:input_cost_per_character" json:"input_cost_per_character,omitempty"`
	// Costs - 128k Tier
	InputCostPerTokenAbove128kTokens          *float64 `gorm:"default:null;column:input_cost_per_token_above_128k_tokens" json:"input_cost_per_token_above_128k_tokens,omitempty"`
	InputCostPerImageAbove128kTokens          *float64 `gorm:"default:null;column:input_cost_per_image_above_128k_tokens" json:"input_cost_per_image_above_128k_tokens,omitempty"`
	InputCostPerVideoPerSecondAbove128kTokens *float64 `gorm:"default:null;column:input_cost_per_video_per_second_above_128k_tokens" json:"input_cost_per_video_per_second_above_128k_tokens,omitempty"`
	InputCostPerAudioPerSecondAbove128kTokens *float64 `gorm:"default:null;column:input_cost_per_audio_per_second_above_128k_tokens" json:"input_cost_per_audio_per_second_above_128k_tokens,omitempty"`
	OutputCostPerTokenAbove128kTokens         *float64 `gorm:"default:null;column:output_cost_per_token_above_128k_tokens" json:"output_cost_per_token_above_128k_tokens,omitempty"`
	// Costs - 200k Tier
	InputCostPerTokenAbove200kTokens          *float64 `gorm:"default:null;column:input_cost_per_token_above_200k_tokens" json:"input_cost_per_token_above_200k_tokens,omitempty"`
	InputCostPerTokenAbove200kTokensPriority  *float64 `gorm:"default:null;column:input_cost_per_token_above_200k_tokens_priority" json:"input_cost_per_token_above_200k_tokens_priority,omitempty"`
	OutputCostPerTokenAbove200kTokens         *float64 `gorm:"default:null;column:output_cost_per_token_above_200k_tokens" json:"output_cost_per_token_above_200k_tokens,omitempty"`
	OutputCostPerTokenAbove200kTokensPriority *float64 `gorm:"default:null;column:output_cost_per_token_above_200k_tokens_priority" json:"output_cost_per_token_above_200k_tokens_priority,omitempty"`
	// Costs - 272k Tier
	InputCostPerTokenAbove272kTokens          *float64 `gorm:"default:null;column:input_cost_per_token_above_272k_tokens" json:"input_cost_per_token_above_272k_tokens,omitempty"`
	InputCostPerTokenAbove272kTokensPriority  *float64 `gorm:"default:null;column:input_cost_per_token_above_272k_tokens_priority" json:"input_cost_per_token_above_272k_tokens_priority,omitempty"`
	InputCostPerTokenFlexAbove272kTokens      *float64 `gorm:"default:null;column:input_cost_per_token_flex_above_272k_tokens" json:"input_cost_per_token_flex_above_272k_tokens,omitempty"`
	OutputCostPerTokenAbove272kTokens         *float64 `gorm:"default:null;column:output_cost_per_token_above_272k_tokens" json:"output_cost_per_token_above_272k_tokens,omitempty"`
	OutputCostPerTokenAbove272kTokensPriority *float64 `gorm:"default:null;column:output_cost_per_token_above_272k_tokens_priority" json:"output_cost_per_token_above_272k_tokens_priority,omitempty"`
	OutputCostPerTokenFlexAbove272kTokens     *float64 `gorm:"default:null;column:output_cost_per_token_flex_above_272k_tokens" json:"output_cost_per_token_flex_above_272k_tokens,omitempty"`

	// Costs - Cache
	CacheCreationInputTokenCost                        *float64 `gorm:"default:null;column:cache_creation_input_token_cost" json:"cache_creation_input_token_cost,omitempty"`
	CacheReadInputTokenCost                            *float64 `gorm:"default:null;column:cache_read_input_token_cost" json:"cache_read_input_token_cost,omitempty"`
	CacheCreationInputTokenCostAbove200kTokens         *float64 `gorm:"default:null;column:cache_creation_input_token_cost_above_200k_tokens" json:"cache_creation_input_token_cost_above_200k_tokens,omitempty"`
	CacheReadInputTokenCostAbove200kTokens             *float64 `gorm:"default:null;column:cache_read_input_token_cost_above_200k_tokens" json:"cache_read_input_token_cost_above_200k_tokens,omitempty"`
	CacheReadInputTokenCostAbove200kTokensPriority     *float64 `gorm:"default:null;column:cache_read_input_token_cost_above_200k_tokens_priority" json:"cache_read_input_token_cost_above_200k_tokens_priority,omitempty"`
	CacheCreationInputTokenCostAbove1hr                *float64 `gorm:"default:null;column:cache_creation_input_token_cost_above_1hr" json:"cache_creation_input_token_cost_above_1hr,omitempty"`
	CacheCreationInputTokenCostAbove1hrAbove200kTokens *float64 `gorm:"default:null;column:cache_creation_input_token_cost_above_1hr_above_200k_tokens" json:"cache_creation_input_token_cost_above_1hr_above_200k_tokens,omitempty"`
	CacheCreationInputAudioTokenCost                   *float64 `gorm:"default:null;column:cache_creation_input_audio_token_cost" json:"cache_creation_input_audio_token_cost,omitempty"`
	CacheReadInputTokenCostPriority                    *float64 `gorm:"default:null;column:cache_read_input_token_cost_priority" json:"cache_read_input_token_cost_priority,omitempty"`
	CacheReadInputTokenCostFlex                        *float64 `gorm:"default:null;column:cache_read_input_token_cost_flex" json:"cache_read_input_token_cost_flex,omitempty"`
	CacheReadInputImageTokenCost                       *float64 `gorm:"default:null;column:cache_read_input_image_token_cost" json:"cache_read_input_image_token_cost,omitempty"`
	CacheReadInputTokenCostAbove272kTokens             *float64 `gorm:"default:null;column:cache_read_input_token_cost_above_272k_tokens" json:"cache_read_input_token_cost_above_272k_tokens,omitempty"`
	CacheReadInputTokenCostAbove272kTokensPriority     *float64 `gorm:"default:null;column:cache_read_input_token_cost_above_272k_tokens_priority" json:"cache_read_input_token_cost_above_272k_tokens_priority,omitempty"`
	CacheReadInputTokenCostFlexAbove272kTokens         *float64 `gorm:"default:null;column:cache_read_input_token_cost_flex_above_272k_tokens" json:"cache_read_input_token_cost_flex_above_272k_tokens,omitempty"`
	// OpenAI cache-write (cache-creation) tiered rates, added with gpt-5.6.
	CacheCreationInputTokenCostAbove272kTokens     *float64 `gorm:"default:null;column:cache_creation_input_token_cost_above_272k_tokens" json:"cache_creation_input_token_cost_above_272k_tokens,omitempty"`
	CacheCreationInputTokenCostFlex                *float64 `gorm:"default:null;column:cache_creation_input_token_cost_flex" json:"cache_creation_input_token_cost_flex,omitempty"`
	CacheCreationInputTokenCostFlexAbove272kTokens *float64 `gorm:"default:null;column:cache_creation_input_token_cost_flex_above_272k_tokens" json:"cache_creation_input_token_cost_flex_above_272k_tokens,omitempty"`
	CacheCreationInputTokenCostPriority            *float64 `gorm:"default:null;column:cache_creation_input_token_cost_priority" json:"cache_creation_input_token_cost_priority,omitempty"`
	// Fast mode (Anthropic) cache rates — flat across the full context window, no tiering.
	CacheCreationInputTokenCostFast         *float64 `gorm:"default:null;column:cache_creation_input_token_cost_fast" json:"cache_creation_input_token_cost_fast,omitempty"`
	CacheCreationInputTokenCostAbove1hrFast *float64 `gorm:"default:null;column:cache_creation_input_token_cost_above_1hr_fast" json:"cache_creation_input_token_cost_above_1hr_fast,omitempty"`
	CacheReadInputTokenCostFast             *float64 `gorm:"default:null;column:cache_read_input_token_cost_fast" json:"cache_read_input_token_cost_fast,omitempty"`

	// Costs - Image
	InputCostPerImage                             *float64 `gorm:"default:null;column:input_cost_per_image" json:"input_cost_per_image,omitempty"`
	InputCostPerPixel                             *float64 `gorm:"default:null;column:input_cost_per_pixel" json:"input_cost_per_pixel,omitempty"`
	OutputCostPerImage                            *float64 `gorm:"default:null;column:output_cost_per_image" json:"output_cost_per_image,omitempty"`
	OutputCostPerPixel                            *float64 `gorm:"default:null;column:output_cost_per_pixel" json:"output_cost_per_pixel,omitempty"`
	OutputCostPerImagePremiumImage                *float64 `gorm:"default:null;column:output_cost_per_image_premium_image" json:"output_cost_per_image_premium_image,omitempty"`
	OutputCostPerImageAbove512x512Pixels          *float64 `gorm:"default:null;column:output_cost_per_image_above_512_and_512_pixels" json:"output_cost_per_image_above_512_and_512_pixels,omitempty"`
	OutputCostPerImageAbove512x512PixelsPremium   *float64 `gorm:"default:null;column:output_cost_per_image_above_512x512_pixels_premium" json:"output_cost_per_image_above_512_and_512_pixels_and_premium_image,omitempty"`
	OutputCostPerImageAbove1024x1024Pixels        *float64 `gorm:"default:null;column:output_cost_per_image_above_1024_and_1024_pixels" json:"output_cost_per_image_above_1024_and_1024_pixels,omitempty"`
	OutputCostPerImageAbove1024x1024PixelsPremium *float64 `gorm:"default:null;column:output_cost_per_image_above_1024x1024_pixels_premium" json:"output_cost_per_image_above_1024_and_1024_pixels_and_premium_image,omitempty"`
	OutputCostPerImageAbove2048x2048Pixels        *float64 `gorm:"default:null;column:output_cost_per_image_above_2048_and_2048_pixels" json:"output_cost_per_image_above_2048_and_2048_pixels,omitempty"`
	OutputCostPerImageAbove4096x4096Pixels        *float64 `gorm:"default:null;column:output_cost_per_image_above_4096_and_4096_pixels" json:"output_cost_per_image_above_4096_and_4096_pixels,omitempty"`
	OutputCostPerImageLowQuality                  *float64 `gorm:"default:null;column:output_cost_per_image_low_quality" json:"output_cost_per_image_low_quality,omitempty"`
	OutputCostPerImageMediumQuality               *float64 `gorm:"default:null;column:output_cost_per_image_medium_quality" json:"output_cost_per_image_medium_quality,omitempty"`
	OutputCostPerImageHighQuality                 *float64 `gorm:"default:null;column:output_cost_per_image_high_quality" json:"output_cost_per_image_high_quality,omitempty"`
	OutputCostPerImageAutoQuality                 *float64 `gorm:"default:null;column:output_cost_per_image_auto_quality" json:"output_cost_per_image_auto_quality,omitempty"`
	InputCostPerImageToken                        *float64 `gorm:"default:null;column:input_cost_per_image_token" json:"input_cost_per_image_token,omitempty"`
	OutputCostPerImageToken                       *float64 `gorm:"default:null;column:output_cost_per_image_token" json:"output_cost_per_image_token,omitempty"`

	// Costs - Audio/Video
	InputCostPerAudioToken      *float64 `gorm:"default:null;column:input_cost_per_audio_token" json:"input_cost_per_audio_token,omitempty"`
	InputCostPerAudioPerSecond  *float64 `gorm:"default:null;column:input_cost_per_audio_per_second" json:"input_cost_per_audio_per_second,omitempty"`
	InputCostPerSecond          *float64 `gorm:"default:null;column:input_cost_per_second" json:"input_cost_per_second,omitempty"` // Only for transcription models
	InputCostPerVideoPerSecond  *float64 `gorm:"default:null;column:input_cost_per_video_per_second" json:"input_cost_per_video_per_second,omitempty"`
	OutputCostPerAudioToken     *float64 `gorm:"default:null;column:output_cost_per_audio_token" json:"output_cost_per_audio_token,omitempty"`
	OutputCostPerVideoPerSecond *float64 `gorm:"default:null;column:output_cost_per_video_per_second" json:"output_cost_per_video_per_second,omitempty"`
	OutputCostPerSecond         *float64 `gorm:"default:null;column:output_cost_per_second" json:"output_cost_per_second,omitempty"` // For both speech and video models

	// Costs - Other
	SearchContextCostPerQuery     *float64 `gorm:"default:null;column:search_context_cost_per_query" json:"search_context_cost_per_query,omitempty"`
	CodeInterpreterCostPerSession *float64 `gorm:"default:null;column:code_interpreter_cost_per_session" json:"code_interpreter_cost_per_session,omitempty"`
	// Data-residency multiplier scaling all token/cache costs when Anthropic serves inference_geo:"us" (1.1x); nil = no multiplier.
	InferenceGeoUSMultiplier *float64 `gorm:"default:null;column:inference_geo_us_multiplier" json:"inference_geo_us_multiplier,omitempty"`

	// Costs - OCR
	OCRCostPerPage        *float64 `gorm:"default:null;column:ocr_cost_per_page" json:"ocr_cost_per_page,omitempty"`
	AnnotationCostPerPage *float64 `gorm:"default:null;column:annotation_cost_per_page" json:"annotation_cost_per_page,omitempty"`

	// AdditionalAttributes holds editorial per-model metadata (e.g. description,
	// tags). Persisted as a JSON string in the additional_attributes column and
	// surfaced as a typed map via BeforeSave/AfterFind. This column is
	// intentionally excluded from the pricing-sync upsert path so the 24-hour
	// datasheet sync never overwrites user-set values.
	AdditionalAttributesJSON string            `gorm:"type:text;column:additional_attributes" json:"-"`
	AdditionalAttributes     map[string]string `gorm:"-" json:"additional_attributes,omitempty"`
}

// TableName sets the table name for each model
func (TableModelPricing) TableName() string { return "governance_model_pricing" }

// BeforeSave marshals AdditionalAttributes → AdditionalAttributesJSON. A nil
// or empty map serializes to "{}" so the column always holds a valid JSON
// object; reads round-trip back to a nil map via AfterFind. Mirrors the
// convention used by TableMCPClient.HeadersJSON.
func (p *TableModelPricing) BeforeSave(tx *gorm.DB) error {
	if len(p.AdditionalAttributes) == 0 {
		p.AdditionalAttributesJSON = "{}"
		return nil
	}
	data, err := json.Marshal(p.AdditionalAttributes)
	if err != nil {
		return err
	}
	p.AdditionalAttributesJSON = string(data)
	return nil
}

// AfterFind unmarshals AdditionalAttributesJSON → AdditionalAttributes.
// Empty/missing JSON resolves to a nil map so callers can use len() and
// idiomatic nil checks.
func (p *TableModelPricing) AfterFind(tx *gorm.DB) error {
	if p.AdditionalAttributesJSON == "" || p.AdditionalAttributesJSON == "{}" {
		p.AdditionalAttributes = nil
		return nil
	}
	var attrs map[string]string
	if err := json.Unmarshal([]byte(p.AdditionalAttributesJSON), &attrs); err != nil {
		return err
	}
	p.AdditionalAttributes = attrs
	return nil
}
