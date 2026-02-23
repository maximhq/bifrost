package tables

import (
	"fmt"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm"
)

type PricingOverrideScope string

const (
	PricingOverrideScopeGlobal      PricingOverrideScope = "global"
	PricingOverrideScopeProvider    PricingOverrideScope = "provider"
	PricingOverrideScopeProviderKey PricingOverrideScope = "provider_key"
	PricingOverrideScopeVirtualKey  PricingOverrideScope = "virtual_key"
)

// TablePricingOverride represents a scoped pricing override definition.
type TablePricingOverride struct {
	ID          string `gorm:"primaryKey;type:varchar(255)" json:"id"`
	ConfigHash  string `gorm:"type:varchar(255)" json:"config_hash"`
	Name        string `gorm:"type:varchar(255);not null;uniqueIndex:idx_pricing_override_scope_name" json:"name"`
	Description string `gorm:"type:text" json:"description"`
	Enabled     bool   `gorm:"not null;default:true;index" json:"enabled"`

	// Scope where this override applies.
	Scope   PricingOverrideScope `gorm:"type:varchar(50);not null;index:idx_pricing_override_scope" json:"scope"`
	ScopeID *string              `gorm:"type:varchar(255);index:idx_pricing_override_scope;uniqueIndex:idx_pricing_override_scope_name" json:"scope_id,omitempty"`

	ModelPattern string                           `gorm:"type:text;not null" json:"model_pattern"`
	MatchType    schemas.PricingOverrideMatchType `gorm:"type:varchar(50);not null" json:"match_type"`

	RequestTypesJSON *string               `gorm:"type:text" json:"-"`
	RequestTypes     []schemas.RequestType `gorm:"-" json:"request_types,omitempty"`

	// Basic token pricing
	InputCostPerToken  *float64 `json:"input_cost_per_token,omitempty"`
	OutputCostPerToken *float64 `json:"output_cost_per_token,omitempty"`

	// Additional pricing for media
	InputCostPerVideoPerSecond *float64 `json:"input_cost_per_video_per_second,omitempty"`
	InputCostPerAudioPerSecond *float64 `json:"input_cost_per_audio_per_second,omitempty"`

	// Character-based pricing
	InputCostPerCharacter  *float64 `json:"input_cost_per_character,omitempty"`
	OutputCostPerCharacter *float64 `json:"output_cost_per_character,omitempty"`

	// Pricing above 128k tokens
	InputCostPerTokenAbove128kTokens          *float64 `json:"input_cost_per_token_above_128k_tokens,omitempty"`
	InputCostPerCharacterAbove128kTokens      *float64 `json:"input_cost_per_character_above_128k_tokens,omitempty"`
	InputCostPerImageAbove128kTokens          *float64 `json:"input_cost_per_image_above_128k_tokens,omitempty"`
	InputCostPerVideoPerSecondAbove128kTokens *float64 `json:"input_cost_per_video_per_second_above_128k_tokens,omitempty"`
	InputCostPerAudioPerSecondAbove128kTokens *float64 `json:"input_cost_per_audio_per_second_above_128k_tokens,omitempty"`
	OutputCostPerTokenAbove128kTokens         *float64 `json:"output_cost_per_token_above_128k_tokens,omitempty"`
	OutputCostPerCharacterAbove128kTokens     *float64 `json:"output_cost_per_character_above_128k_tokens,omitempty"`

	// Pricing above 200k tokens
	InputCostPerTokenAbove200kTokens           *float64 `json:"input_cost_per_token_above_200k_tokens,omitempty"`
	OutputCostPerTokenAbove200kTokens          *float64 `json:"output_cost_per_token_above_200k_tokens,omitempty"`
	CacheCreationInputTokenCostAbove200kTokens *float64 `json:"cache_creation_input_token_cost_above_200k_tokens,omitempty"`
	CacheReadInputTokenCostAbove200kTokens     *float64 `json:"cache_read_input_token_cost_above_200k_tokens,omitempty"`

	// Cache and batch pricing
	CacheReadInputTokenCost     *float64 `json:"cache_read_input_token_cost,omitempty"`
	CacheCreationInputTokenCost *float64 `json:"cache_creation_input_token_cost,omitempty"`
	InputCostPerTokenBatches    *float64 `json:"input_cost_per_token_batches,omitempty"`
	OutputCostPerTokenBatches   *float64 `json:"output_cost_per_token_batches,omitempty"`

	// Image generation pricing
	InputCostPerImageToken       *float64 `json:"input_cost_per_image_token,omitempty"`
	OutputCostPerImageToken      *float64 `json:"output_cost_per_image_token,omitempty"`
	InputCostPerImage            *float64 `json:"input_cost_per_image,omitempty"`
	OutputCostPerImage           *float64 `json:"output_cost_per_image,omitempty"`
	CacheReadInputImageTokenCost *float64 `json:"cache_read_input_image_token_cost,omitempty"`

	CreatedAt time.Time `gorm:"index;not null" json:"created_at"`
	UpdatedAt time.Time `gorm:"index;not null" json:"updated_at"`
}

func (TablePricingOverride) TableName() string {
	return "pricing_overrides"
}

func (o *TablePricingOverride) BeforeSave(tx *gorm.DB) error {
	if strings.TrimSpace(o.Name) == "" {
		return fmt.Errorf("name cannot be empty")
	}
	if strings.TrimSpace(o.ModelPattern) == "" {
		return fmt.Errorf("model_pattern cannot be empty")
	}

	switch o.Scope {
	case PricingOverrideScopeGlobal:
		o.ScopeID = nil
	case PricingOverrideScopeProvider, PricingOverrideScopeProviderKey, PricingOverrideScopeVirtualKey:
		if o.ScopeID == nil || strings.TrimSpace(*o.ScopeID) == "" {
			return fmt.Errorf("scope_id is required for scope %q", o.Scope)
		}
	default:
		return fmt.Errorf("unsupported scope %q", o.Scope)
	}

	switch o.MatchType {
	case schemas.PricingOverrideMatchExact, schemas.PricingOverrideMatchWildcard, schemas.PricingOverrideMatchRegex:
	default:
		return fmt.Errorf("unsupported match_type %q", o.MatchType)
	}

	if len(o.RequestTypes) > 0 {
		data, err := sonic.Marshal(o.RequestTypes)
		if err != nil {
			return err
		}
		o.RequestTypesJSON = bifrost.Ptr(string(data))
	} else {
		o.RequestTypesJSON = nil
	}

	return nil
}

func (o *TablePricingOverride) AfterFind(tx *gorm.DB) error {
	if o.RequestTypesJSON == nil || strings.TrimSpace(*o.RequestTypesJSON) == "" {
		o.RequestTypes = nil
		return nil
	}
	var requestTypes []schemas.RequestType
	if err := sonic.Unmarshal([]byte(*o.RequestTypesJSON), &requestTypes); err != nil {
		return err
	}
	o.RequestTypes = requestTypes
	return nil
}

func (o TablePricingOverride) ToProviderPricingOverride() schemas.ProviderPricingOverride {
	return schemas.ProviderPricingOverride{
		ModelPattern: o.ModelPattern,
		MatchType:    o.MatchType,
		RequestTypes: o.RequestTypes,

		InputCostPerToken:  o.InputCostPerToken,
		OutputCostPerToken: o.OutputCostPerToken,

		InputCostPerVideoPerSecond: o.InputCostPerVideoPerSecond,
		InputCostPerAudioPerSecond: o.InputCostPerAudioPerSecond,

		InputCostPerCharacter:  o.InputCostPerCharacter,
		OutputCostPerCharacter: o.OutputCostPerCharacter,

		InputCostPerTokenAbove128kTokens:          o.InputCostPerTokenAbove128kTokens,
		InputCostPerCharacterAbove128kTokens:      o.InputCostPerCharacterAbove128kTokens,
		InputCostPerImageAbove128kTokens:          o.InputCostPerImageAbove128kTokens,
		InputCostPerVideoPerSecondAbove128kTokens: o.InputCostPerVideoPerSecondAbove128kTokens,
		InputCostPerAudioPerSecondAbove128kTokens: o.InputCostPerAudioPerSecondAbove128kTokens,
		OutputCostPerTokenAbove128kTokens:         o.OutputCostPerTokenAbove128kTokens,
		OutputCostPerCharacterAbove128kTokens:     o.OutputCostPerCharacterAbove128kTokens,

		InputCostPerTokenAbove200kTokens:           o.InputCostPerTokenAbove200kTokens,
		OutputCostPerTokenAbove200kTokens:          o.OutputCostPerTokenAbove200kTokens,
		CacheCreationInputTokenCostAbove200kTokens: o.CacheCreationInputTokenCostAbove200kTokens,
		CacheReadInputTokenCostAbove200kTokens:     o.CacheReadInputTokenCostAbove200kTokens,

		CacheReadInputTokenCost:     o.CacheReadInputTokenCost,
		CacheCreationInputTokenCost: o.CacheCreationInputTokenCost,
		InputCostPerTokenBatches:    o.InputCostPerTokenBatches,
		OutputCostPerTokenBatches:   o.OutputCostPerTokenBatches,

		InputCostPerImageToken:       o.InputCostPerImageToken,
		OutputCostPerImageToken:      o.OutputCostPerImageToken,
		InputCostPerImage:            o.InputCostPerImage,
		OutputCostPerImage:           o.OutputCostPerImage,
		CacheReadInputImageTokenCost: o.CacheReadInputImageTokenCost,
	}
}
