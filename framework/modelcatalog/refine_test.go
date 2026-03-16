package modelcatalog

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRefineModelForProvider_ReplicateRefinesOpenAIModel verifies that
// Replicate can recover nested provider slugs for provider-pinned OpenAI-family models.
func TestRefineModelForProvider_ReplicateRefinesOpenAIModel(t *testing.T) {
	mc := newTestCatalog(map[schemas.ModelProvider][]string{
		schemas.Replicate: {"openai/gpt-5-nano"},
	}, map[string]string{
		"openai/gpt-5-nano": "gpt-5-nano",
	})

	refined, err := mc.RefineModelForProvider(schemas.Replicate, "gpt-5-nano")
	require.NoError(t, err)
	assert.Equal(t, "openai/gpt-5-nano", refined)
}

// TestRefineModelForProvider_ReplicatePreservesOwnerSlashModel verifies that
// standard Replicate owner/model slugs are not mistaken for nested provider slugs.
func TestRefineModelForProvider_ReplicatePreservesOwnerSlashModel(t *testing.T) {
	mc := newTestCatalog(map[schemas.ModelProvider][]string{
		schemas.Replicate: {"meta/meta-llama-3-8b"},
	}, nil)

	refined, err := mc.RefineModelForProvider(schemas.Replicate, "meta/meta-llama-3-8b")
	require.NoError(t, err)
	assert.Equal(t, "meta/meta-llama-3-8b", refined)
}

// TestRefineModelForProvider_ReplicateReturnsAmbiguousMatchError verifies that
// refinement fails fast when multiple nested provider slugs match the same base model.
func TestRefineModelForProvider_ReplicateReturnsAmbiguousMatchError(t *testing.T) {
	mc := newTestCatalog(map[schemas.ModelProvider][]string{
		schemas.Replicate: {
			"openai/gpt-5-nano",
			"xai/gpt-5-nano",
		},
	}, nil)

	refined, err := mc.RefineModelForProvider(schemas.Replicate, "gpt-5-nano")
	require.Error(t, err)
	assert.Empty(t, refined)
	assert.Contains(t, err.Error(), "multiple compatible models found")
}
