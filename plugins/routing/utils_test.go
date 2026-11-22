package routing

import (
	"context"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A routing config override written before phrase suggestions moved to model
// routers keeps its other settings: its suggestions block, which the config no
// longer takes, is left out before validation instead of failing it, and the
// caller's override is not changed.
func TestResolveAnalyzerConfigIgnoresLegacySuggestions(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	// Enabled with no suggestions model and no llm block: invalid on its own.
	legacy := &configstore.ComplexitySuggestionsConfig{Enabled: true, SampleRate: 0.1, LookbackDays: 7}
	override := captureTestConfig(legacy)
	override.LLM = nil
	override.Keywords.SimpleKeywords = []string{"from the override"}

	got := resolveAnalyzerConfigFromStoreOrArg(context.Background(), logger, nil, override)
	require.NotNil(t, got, "the override is used, not dropped for its suggestions block")
	assert.Equal(t, []string{"from the override"}, got.Keywords.SimpleKeywords)
	assert.Nil(t, got.Suggestions)
	assert.Same(t, legacy, override.Suggestions, "the caller's override keeps its block")
}
