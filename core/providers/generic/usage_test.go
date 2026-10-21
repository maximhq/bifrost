package generic

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractUsageOptimisticDefaults(t *testing.T) {
	u := resolveUsage(nil, nil)

	t.Run("openai shape", func(t *testing.T) {
		usage := extractUsage(u, "application/json", []byte(`{"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
		require.NotNil(t, usage.LLMUsage)
		assert.Equal(t, 10, usage.LLMUsage.PromptTokens)
		assert.Equal(t, 5, usage.LLMUsage.CompletionTokens)
		assert.Equal(t, 15, usage.LLMUsage.TotalTokens)
		assert.Nil(t, usage.Cost)
	})
	t.Run("anthropic shape sums input and output", func(t *testing.T) {
		usage := extractUsage(u, "application/json; charset=utf-8", []byte(`{"usage":{"input_tokens":3,"output_tokens":4}}`))
		require.NotNil(t, usage.LLMUsage)
		assert.Equal(t, 7, usage.LLMUsage.TotalTokens)
	})
	t.Run("cost in usage or at the top level", func(t *testing.T) {
		usage := extractUsage(u, "application/json", []byte(`{"usage":{"cost":0.25}}`))
		require.NotNil(t, usage.Cost)
		assert.InDelta(t, 0.25, usage.Cost.TotalCost, 1e-9)
		usage = extractUsage(u, "application/json", []byte(`{"cost":1.5}`))
		require.NotNil(t, usage.Cost)
		assert.InDelta(t, 1.5, usage.Cost.TotalCost, 1e-9)
	})
	t.Run("nothing found is still a non-nil usage", func(t *testing.T) {
		usage := extractUsage(u, "application/json", []byte(`{"results":[]}`))
		require.NotNil(t, usage)
		assert.Nil(t, usage.LLMUsage)
		assert.Nil(t, usage.Cost)
	})
	t.Run("non-json content type is not scanned", func(t *testing.T) {
		usage := extractUsage(u, "text/plain", []byte(`{"usage":{"total_tokens":9}}`))
		assert.Nil(t, usage.LLMUsage)
	})
	t.Run("missing content type falls back to sniffing json", func(t *testing.T) {
		usage := extractUsage(u, "", []byte(`{"usage":{"total_tokens":9}}`))
		require.NotNil(t, usage.LLMUsage)
		assert.Equal(t, 9, usage.LLMUsage.TotalTokens)
	})
	t.Run("zero tokens is no usage", func(t *testing.T) {
		usage := extractUsage(u, "application/json", []byte(`{"usage":{"total_tokens":0}}`))
		assert.Nil(t, usage.LLMUsage)
	})
}

func TestExtractUsageConfiguredPaths(t *testing.T) {
	u := resolveUsage(&schemas.GenericUsageConfig{
		InputTokensPath:  "meta.in",
		OutputTokensPath: "meta.out",
		CostPath:         "data.#.cost",
		CostPerRequest:   0.01,
	}, nil)
	usage := extractUsage(u, "application/json", []byte(`{"meta":{"in":2,"out":3},"data":[{"cost":0.1},{"cost":0.2},{"cost":"n/a"}],"usage":{"total_tokens":99}}`))
	require.NotNil(t, usage.LLMUsage)
	assert.Equal(t, 5, usage.LLMUsage.TotalTokens, "configured input/output paths win and total is derived")
	require.NotNil(t, usage.Cost)
	assert.InDelta(t, 0.31, usage.Cost.TotalCost, 1e-9, "array costs are summed and the per-request fee is added")

	t.Run("configured path overrides optimistic even when absent", func(t *testing.T) {
		usage := extractUsage(u, "application/json", []byte(`{"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
		assert.Nil(t, usage.LLMUsage, "the optimistic usage block is not read for a field with a configured path")
	})
}

func TestExtractUsageDisableOptimistic(t *testing.T) {
	u := resolveUsage(&schemas.GenericUsageConfig{DisableOptimisticParsing: true, CostPerRequest: 0.004}, nil)
	usage := extractUsage(u, "application/json", []byte(`{"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2},"cost":5}`))
	assert.Nil(t, usage.LLMUsage)
	require.NotNil(t, usage.Cost)
	assert.InDelta(t, 0.004, usage.Cost.TotalCost, 1e-9, "only the flat fee remains")
}

func TestResolveUsageRouteOverride(t *testing.T) {
	base := &schemas.GenericUsageConfig{ModelPath: "model", CostPath: "cost", CostPerRequest: 0.01}
	override := &schemas.GenericUsageConfig{CostPerRequest: 0.05, TotalTokensPath: "tokens"}
	u := resolveUsage(base, override)
	assert.Equal(t, "model", u.modelPath, "untouched fields come from the provider")
	assert.Equal(t, "cost", u.costPath)
	assert.Equal(t, "tokens", u.totalTokensPath)
	assert.InDelta(t, 0.05, u.costPerRequest, 1e-9, "the route's fee replaces the provider's")
	assert.True(t, u.optimistic)
}

func TestStreamUsageObservers(t *testing.T) {
	u := resolveUsage(&schemas.GenericUsageConfig{CostPerRequest: 0.01}, nil)
	assert.False(t, u.hasUsageEvent([]byte(`{"delta":"x"}`)))
	assert.True(t, u.hasUsageEvent([]byte(`{"usage":{"total_tokens":1}}`)))
	assert.True(t, u.hasUsageEvent([]byte(`{"cost":0.2}`)))

	assert.Nil(t, u.observeUsage([]byte(`{"delta":"x"}`)), "a content-only event never clears what was seen")
	observed := u.observeUsage([]byte(`{"usage":{"total_tokens":4}}`))
	require.NotNil(t, observed)
	assert.Equal(t, 4, observed.LLMUsage.TotalTokens)
	assert.InDelta(t, 0.01, observed.Cost.TotalCost, 1e-9)

	fallback := u.defaultUsage()
	require.NotNil(t, fallback)
	assert.Nil(t, fallback.LLMUsage)
	assert.InDelta(t, 0.01, fallback.Cost.TotalCost, 1e-9)
	assert.Nil(t, resolveUsage(nil, nil).defaultUsage().Cost, "no fee means an empty usage")
}
