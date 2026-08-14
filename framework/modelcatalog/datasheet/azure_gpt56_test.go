package datasheet

import (
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFillAzureGPT56CacheCreation(t *testing.T) {
	input := 2e-6
	above272k := 4e-6
	priority := 4e-6
	existing := 9e-6

	data := map[string]Entry{
		"azure/gpt-5.6-terra": {
			Provider: "azure",
			Mode:     "chat",
			Options: Options{
				InputCostPerToken:                &input,
				InputCostPerTokenAbove272kTokens: &above272k,
				InputCostPerTokenPriority:        &priority,
				CacheReadInputTokenCost:          bifrost.Ptr(2e-7),
			},
		},
		"azure/us/gpt-5.6-terra": {
			Provider: "azure",
			Mode:     "chat",
			Options: Options{
				InputCostPerToken: bifrost.Ptr(2.2e-6),
			},
		},
		"azure/gpt-5.6-terra-preset": {
			Provider: "azure",
			Mode:     "chat",
			Options: Options{
				InputCostPerToken:           &input,
				CacheCreationInputTokenCost: &existing,
			},
		},
		"gpt-5.6-terra": {
			Provider: "openai",
			Mode:     "chat",
			Options: Options{
				InputCostPerToken: &input,
			},
		},
		"azure/gpt-4o": {
			Provider: "azure",
			Mode:     "chat",
			Options: Options{
				InputCostPerToken: bifrost.Ptr(2.5e-6),
			},
		},
		"azure/claude-sonnet-4-5": {
			Provider: "azure",
			Mode:     "chat",
			Options: Options{
				InputCostPerToken:           bifrost.Ptr(3e-6),
				CacheCreationInputTokenCost: bifrost.Ptr(3.75e-6),
			},
		},
	}

	fillAzureGPT56CacheCreation(data)

	terra := data["azure/gpt-5.6-terra"]
	require.NotNil(t, terra.CacheCreationInputTokenCost)
	assert.InDelta(t, 2.5e-6, *terra.CacheCreationInputTokenCost, 1e-15)
	require.NotNil(t, terra.CacheCreationInputTokenCostAbove272kTokens)
	assert.InDelta(t, 5e-6, *terra.CacheCreationInputTokenCostAbove272kTokens, 1e-15)
	require.NotNil(t, terra.CacheCreationInputTokenCostPriority)
	assert.InDelta(t, 5e-6, *terra.CacheCreationInputTokenCostPriority, 1e-15)

	us := data["azure/us/gpt-5.6-terra"]
	require.NotNil(t, us.CacheCreationInputTokenCost)
	assert.InDelta(t, 2.75e-6, *us.CacheCreationInputTokenCost, 1e-15)

	preset := data["azure/gpt-5.6-terra-preset"]
	require.NotNil(t, preset.CacheCreationInputTokenCost)
	assert.Equal(t, existing, *preset.CacheCreationInputTokenCost)

	openai := data["gpt-5.6-terra"]
	assert.Nil(t, openai.CacheCreationInputTokenCost)

	gpt4o := data["azure/gpt-4o"]
	assert.Nil(t, gpt4o.CacheCreationInputTokenCost)

	claude := data["azure/claude-sonnet-4-5"]
	require.NotNil(t, claude.CacheCreationInputTokenCost)
	assert.Equal(t, 3.75e-6, *claude.CacheCreationInputTokenCost)
}

func TestComputeTextCost_AzureGPT56_FilledCacheWrite(t *testing.T) {
	entry := Entry{
		Provider: "azure",
		Mode:     "chat",
		Options: Options{
			InputCostPerToken:       bifrost.Ptr(2e-6),
			OutputCostPerToken:      bifrost.Ptr(1.2e-5),
			CacheReadInputTokenCost: bifrost.Ptr(2e-7),
		},
	}
	fillGPT56CacheCreation(&entry)
	require.NotNil(t, entry.CacheCreationInputTokenCost)

	pricing := convertEntryToTablePricing("azure/gpt-5.6-terra", entry)
	usage := &schemas.BifrostLLMUsage{
		PromptTokens:     55390 + 126 + 1670650,
		CompletionTokens: 2872,
		TotalTokens:      55390 + 126 + 1670650 + 2872,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedReadTokens:  1670650,
			CachedWriteTokens: 55390,
		},
	}
	cost := computeTextCost(&pricing, usage, serviceTier{})
	// uncached 126 * $2/M + write 55390 * $2.50/M + read 1670650 * $0.20/M + out 2872 * $12/M
	want := 126*2e-6 + 55390*2.5e-6 + 1670650*2e-7 + 2872*1.2e-5
	assert.InDelta(t, want, cost, 1e-9)
}
