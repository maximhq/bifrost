package tables

// TableModelPricing represents pricing information for AI models
type TableModelPricing struct {
	ID                 uint    `gorm:"primaryKey;autoIncrement" json:"id"`
	Model              string  `gorm:"type:varchar(255);not null;uniqueIndex:idx_model_provider_mode" json:"model"`
	Provider           string  `gorm:"type:varchar(50);not null;uniqueIndex:idx_model_provider_mode" json:"provider"`
	InputCostPerToken  float64 `gorm:"not null" json:"input_cost_per_token"`
	OutputCostPerToken float64 `gorm:"not null" json:"output_cost_per_token"`
	Mode               string  `gorm:"type:varchar(50);not null;uniqueIndex:idx_model_provider_mode" json:"mode"`

	// Additional pricing for media
	InputCostPerImage          *float64 `gorm:"default:null" json:"input_cost_per_image,omitempty"`
	InputCostPerVideoPerSecond *float64 `gorm:"default:null" json:"input_cost_per_video_per_second,omitempty"`
	InputCostPerAudioPerSecond *float64 `gorm:"default:null" json:"input_cost_per_audio_per_second,omitempty"`

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

	// Cache and batch pricing
	CacheReadInputTokenCost     *float64 `gorm:"default:null" json:"cache_read_input_token_cost,omitempty"`
	CacheCreationInputTokenCost *float64 `gorm:"default:null" json:"cache_creation_input_token_cost,omitempty"`
	InputCostPerTokenBatches    *float64 `gorm:"default:null" json:"input_cost_per_token_batches,omitempty"`
	OutputCostPerTokenBatches   *float64 `gorm:"default:null" json:"output_cost_per_token_batches,omitempty"`
}

// TableName sets the table name for each model
func (TableModelPricing) TableName() string { return "governance_model_pricing" }
