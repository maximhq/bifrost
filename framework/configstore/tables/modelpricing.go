package tables

// TableModelPricing represents pricing information for AI models
type TableModelPricing struct {
	ID        uint   `gorm:"primaryKey;autoIncrement" json:"id"`
	Model     string `gorm:"type:varchar(255);not null;uniqueIndex:idx_model_provider_mode" json:"model"`
	BaseModel string `gorm:"type:varchar(255);default:null" json:"base_model,omitempty"`
	Provider  string `gorm:"type:varchar(50);not null;uniqueIndex:idx_model_provider_mode" json:"provider"`
	Mode      string `gorm:"type:varchar(50);not null;uniqueIndex:idx_model_provider_mode" json:"mode"`

	// Costs - Text
	InputCostPerToken                 float64  `gorm:"not null" json:"input_cost_per_token"`
	OutputCostPerToken                float64  `gorm:"not null" json:"output_cost_per_token"`
	InputCostPerTokenBatches          *float64 `gorm:"default:null;column:input_cost_per_token_batches" json:"input_cost_per_token_batches,omitempty"`
	OutputCostPerTokenBatches         *float64 `gorm:"default:null;column:output_cost_per_token_batches" json:"output_cost_per_token_batches,omitempty"`
	InputCostPerTokenPriority         *float64 `gorm:"default:null;column:input_cost_per_token_priority" json:"input_cost_per_token_priority,omitempty"`
	OutputCostPerTokenPriority        *float64 `gorm:"default:null;column:output_cost_per_token_priority" json:"output_cost_per_token_priority,omitempty"`
	InputCostPerTokenAbove200kTokens  *float64 `gorm:"default:null;column:input_cost_per_token_above_200k_tokens" json:"input_cost_per_token_above_200k_tokens,omitempty"`
	OutputCostPerTokenAbove200kTokens *float64 `gorm:"default:null;column:output_cost_per_token_above_200k_tokens" json:"output_cost_per_token_above_200k_tokens,omitempty"`

	// Costs - Cache
	CacheCreationInputTokenCost                        *float64 `gorm:"default:null;column:cache_creation_input_token_cost" json:"cache_creation_input_token_cost,omitempty"`
	CacheReadInputTokenCost                            *float64 `gorm:"default:null;column:cache_read_input_token_cost" json:"cache_read_input_token_cost,omitempty"`
	CacheCreationInputTokenCostAbove200kTokens         *float64 `gorm:"default:null;column:cache_creation_input_token_cost_above_200k_tokens" json:"cache_creation_input_token_cost_above_200k_tokens,omitempty"`
	CacheReadInputTokenCostAbove200kTokens             *float64 `gorm:"default:null;column:cache_read_input_token_cost_above_200k_tokens" json:"cache_read_input_token_cost_above_200k_tokens,omitempty"`
	CacheCreationInputTokenCostAbove1hr                *float64 `gorm:"default:null;column:cache_creation_input_token_cost_above_1hr" json:"cache_creation_input_token_cost_above_1hr,omitempty"`
	CacheCreationInputTokenCostAbove1hrAbove200kTokens *float64 `gorm:"default:null;column:cache_creation_input_token_cost_above_1hr_above_200k_tokens" json:"cache_creation_input_token_cost_above_1hr_above_200k_tokens,omitempty"`
	CacheCreationInputAudioTokenCost                   *float64 `gorm:"default:null;column:cache_creation_input_audio_token_cost" json:"cache_creation_input_audio_token_cost,omitempty"`
	CacheReadInputTokenCostPriority                    *float64 `gorm:"default:null;column:cache_read_input_token_cost_priority" json:"cache_read_input_token_cost_priority,omitempty"`

	// Costs - Image
	InputCostPerImage                             *float64 `gorm:"default:null;column:input_cost_per_image" json:"input_cost_per_image,omitempty"`
	InputCostPerPixel                             *float64 `gorm:"default:null;column:input_cost_per_pixel" json:"input_cost_per_pixel,omitempty"`
	OutputCostPerImage                            *float64 `gorm:"default:null;column:output_cost_per_image" json:"output_cost_per_image,omitempty"`
	OutputCostPerPixel                            *float64 `gorm:"default:null;column:output_cost_per_pixel" json:"output_cost_per_pixel,omitempty"`
	OutputCostPerImagePremiumImage                *float64 `gorm:"default:null;column:output_cost_per_image_premium_image" json:"output_cost_per_image_premium_image,omitempty"`
	OutputCostPerImageAbove512x512Pixels          *float64 `gorm:"default:null;column:output_cost_per_image_above_512_and_512_pixels" json:"output_cost_per_image_above_512_and_512_pixels,omitempty"`
	OutputCostPerImageAbove512x512PixelsPremium   *float64 `gorm:"default:null;column:output_cost_per_image_above_512_and_512_pixels_and_premium_image" json:"output_cost_per_image_above_512_and_512_pixels_and_premium_image,omitempty"`
	OutputCostPerImageAbove1024x1024Pixels        *float64 `gorm:"default:null;column:output_cost_per_image_above_1024_and_1024_pixels" json:"output_cost_per_image_above_1024_and_1024_pixels,omitempty"`
	OutputCostPerImageAbove1024x1024PixelsPremium *float64 `gorm:"default:null;column:output_cost_per_image_above_1024_and_1024_pixels_and_premium_image" json:"output_cost_per_image_above_1024_and_1024_pixels_and_premium_image,omitempty"`

	// Costs - Audio/Video
	InputCostPerAudioToken      *float64 `gorm:"default:null;column:input_cost_per_audio_token" json:"input_cost_per_audio_token,omitempty"`
	InputCostPerAudioPerSecond  *float64 `gorm:"default:null;column:input_cost_per_audio_per_second" json:"input_cost_per_audio_per_second,omitempty"`
	InputCostPerSecond          *float64 `gorm:"default:null;column:input_cost_per_second" json:"input_cost_per_second,omitempty"`
	InputCostPerVideoPerSecond  *float64 `gorm:"default:null;column:input_cost_per_video_per_second" json:"input_cost_per_video_per_second,omitempty"`
	OutputCostPerAudioToken     *float64 `gorm:"default:null;column:output_cost_per_audio_token" json:"output_cost_per_audio_token,omitempty"`
	OutputCostPerVideoPerSecond *float64 `gorm:"default:null;column:output_cost_per_video_per_second" json:"output_cost_per_video_per_second,omitempty"`
	OutputCostPerSecond         *float64 `gorm:"default:null;column:output_cost_per_second" json:"output_cost_per_second,omitempty"`

	// Costs - Character
	InputCostPerCharacter  *float64 `gorm:"default:null;column:input_cost_per_character" json:"input_cost_per_character,omitempty"`
	OutputCostPerCharacter *float64 `gorm:"default:null;column:output_cost_per_character" json:"output_cost_per_character,omitempty"`

	// Costs - 128k Tier
	InputCostPerTokenAbove128kTokens          *float64 `gorm:"default:null;column:input_cost_per_token_above_128k_tokens" json:"input_cost_per_token_above_128k_tokens,omitempty"`
	InputCostPerCharacterAbove128kTokens      *float64 `gorm:"default:null;column:input_cost_per_character_above_128k_tokens" json:"input_cost_per_character_above_128k_tokens,omitempty"`
	InputCostPerImageAbove128kTokens          *float64 `gorm:"default:null;column:input_cost_per_image_above_128k_tokens" json:"input_cost_per_image_above_128k_tokens,omitempty"`
	InputCostPerVideoPerSecondAbove128kTokens *float64 `gorm:"default:null;column:input_cost_per_video_per_second_above_128k_tokens" json:"input_cost_per_video_per_second_above_128k_tokens,omitempty"`
	InputCostPerAudioPerSecondAbove128kTokens *float64 `gorm:"default:null;column:input_cost_per_audio_per_second_above_128k_tokens" json:"input_cost_per_audio_per_second_above_128k_tokens,omitempty"`
	OutputCostPerTokenAbove128kTokens         *float64 `gorm:"default:null;column:output_cost_per_token_above_128k_tokens" json:"output_cost_per_token_above_128k_tokens,omitempty"`
	OutputCostPerCharacterAbove128kTokens     *float64 `gorm:"default:null;column:output_cost_per_character_above_128k_tokens" json:"output_cost_per_character_above_128k_tokens,omitempty"`

	// Costs - Image Token
	InputCostPerImageToken       *float64 `gorm:"default:null;column:input_cost_per_image_token" json:"input_cost_per_image_token,omitempty"`
	OutputCostPerImageToken      *float64 `gorm:"default:null;column:output_cost_per_image_token" json:"output_cost_per_image_token,omitempty"`
	CacheReadInputImageTokenCost *float64 `gorm:"default:null;column:cache_read_input_image_token_cost" json:"cache_read_input_image_token_cost,omitempty"`

	// Costs - Other
	SearchContextCostPerQuery     *float64 `gorm:"default:null;column:search_context_cost_per_query" json:"search_context_cost_per_query,omitempty"`
	CodeInterpreterCostPerSession *float64 `gorm:"default:null;column:code_interpreter_cost_per_session" json:"code_interpreter_cost_per_session,omitempty"`
}

// TableName sets the table name for each model
func (TableModelPricing) TableName() string { return "governance_model_pricing" }
