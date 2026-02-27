package tables

// TableModelPricing represents pricing information for AI models
type TableModelPricing struct {
	ID                 uint    `gorm:"primaryKey;autoIncrement" json:"id"`
	Model              string  `gorm:"type:varchar(255);not null;uniqueIndex:idx_model_provider_mode" json:"model"`
	BaseModel          string  `gorm:"type:varchar(255);default:null" json:"base_model,omitempty"`
	Provider           string  `gorm:"type:varchar(50);not null;uniqueIndex:idx_model_provider_mode" json:"provider"`
	InputCostPerToken  float64 `gorm:"not null" json:"input_cost_per_token"`
	OutputCostPerToken float64 `gorm:"not null" json:"output_cost_per_token"`
	Mode               string  `gorm:"type:varchar(50);not null;uniqueIndex:idx_model_provider_mode" json:"mode"`

	// Additional pricing for media
	InputCostPerVideoPerSecond  *float64 `gorm:"default:null" json:"input_cost_per_video_per_second,omitempty"`
	OutputCostPerVideoPerSecond *float64 `gorm:"default:null" json:"output_cost_per_video_per_second,omitempty"`
	OutputCostPerSecond         *float64 `gorm:"default:null" json:"output_cost_per_second,omitempty"`
	InputCostPerAudioPerSecond  *float64 `gorm:"default:null" json:"input_cost_per_audio_per_second,omitempty"`

	// Character-based pricing
	InputCostPerCharacter  *float64 `gorm:"default:null" json:"input_cost_per_character,omitempty"`
	OutputCostPerCharacter *float64 `gorm:"default:null" json:"output_cost_per_character,omitempty"`

	// Pricing above 128k tokens
	InputCostPerTokenAbove128kTokens          *float64 `gorm:"default:null" json:"input_cost_per_token_above_128k_tokens,omitempty"`
	InputCostPerCharacterAbove128kTokens      *float64 `gorm:"default:null" json:"input_cost_per_character_above_128k_tokens,omitempty"`
	InputCostPerImageAbove128kTokens          *float64 `gorm:"default:null" json:"input_cost_per_image_above_128k_tokens,omitempty"`
	InputCostPerVideoPerSecondAbove128kTokens *float64 `gorm:"default:null" json:"input_cost_per_video_per_second_above_128k_tokens,omitempty"`
	InputCostPerAudioPerSecondAbove128kTokens *float64 `gorm:"default:null" json:"input_cost_per_audio_per_second_above_128k_tokens,omitempty"`
	OutputCostPerTokenAbove128kTokens         *float64 `gorm:"default:null" json:"output_cost_per_token_above_128k_tokens,omitempty"`
	OutputCostPerCharacterAbove128kTokens     *float64 `gorm:"default:null" json:"output_cost_per_character_above_128k_tokens,omitempty"`

	//Pricing above 200k tokens (for gemini and claude models)
	InputCostPerTokenAbove200kTokens           *float64 `gorm:"default:null;column:input_cost_per_token_above_200k_tokens" json:"input_cost_per_token_above_200k_tokens,omitempty"`
	OutputCostPerTokenAbove200kTokens          *float64 `gorm:"default:null;column:output_cost_per_token_above_200k_tokens" json:"output_cost_per_token_above_200k_tokens,omitempty"`
	CacheCreationInputTokenCostAbove200kTokens *float64 `gorm:"default:null;column:cache_creation_input_token_cost_above_200k_tokens" json:"cache_creation_input_token_cost_above_200k_tokens,omitempty"`
	CacheReadInputTokenCostAbove200kTokens     *float64 `gorm:"default:null;column:cache_read_input_token_cost_above_200k_tokens" json:"cache_read_input_token_cost_above_200k_tokens,omitempty"`

	// Cache and batch pricing
	CacheReadInputTokenCost     *float64 `gorm:"default:null;column:cache_read_input_token_cost" json:"cache_read_input_token_cost,omitempty"`
	CacheCreationInputTokenCost *float64 `gorm:"default:null;column:cache_creation_input_token_cost" json:"cache_creation_input_token_cost,omitempty"`
	InputCostPerTokenBatches    *float64 `gorm:"default:null;column:input_cost_per_token_batches" json:"input_cost_per_token_batches,omitempty"`
	OutputCostPerTokenBatches   *float64 `gorm:"default:null;column:output_cost_per_token_batches" json:"output_cost_per_token_batches,omitempty"`

	// Image generation pricing
	InputCostPerImageToken       *float64 `gorm:"default:null;column:input_cost_per_image_token" json:"input_cost_per_image_token,omitempty"`
	OutputCostPerImageToken      *float64 `gorm:"default:null;column:output_cost_per_image_token" json:"output_cost_per_image_token,omitempty"`
	InputCostPerImage            *float64 `gorm:"default:null;column:input_cost_per_image" json:"input_cost_per_image,omitempty"`
	OutputCostPerImage           *float64 `gorm:"default:null;column:output_cost_per_image" json:"output_cost_per_image,omitempty"`
	CacheReadInputImageTokenCost *float64 `gorm:"default:null;column:cache_read_input_image_token_cost" json:"cache_read_input_image_token_cost,omitempty"`
}

// TableName sets the table name for each model
func (TableModelPricing) TableName() string { return "governance_model_pricing" }
