package configstore

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testLLMConfig() *ComplexityLLMConfig {
	return &ComplexityLLMConfig{
		Provider: "openai",
		Model:    "gpt-4.1-mini",
	}
}

// testLLMFallbackAnalyzerConfig is a semantic-primary config with the llm
// fallback switched on — the only wiring in which the llm block ever runs.
func testLLMFallbackAnalyzerConfig() *ComplexityAnalyzerConfig {
	cfg := testComplexityAnalyzerConfig()
	cfg.Semantic = testSemanticConfig()
	cfg.Semantic.Fallback = ComplexitySemanticFallbackLLM
	cfg.LLM = testLLMConfig()
	return cfg
}

func TestComplexityLLMConfigTimeoutDecoding(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    time.Duration
		wantErr bool
	}{
		{name: "duration string", payload: `{"timeout":"2s"}`, want: 2 * time.Second},
		{name: "number is milliseconds", payload: `{"timeout":2500}`, want: 2500 * time.Millisecond},
		{name: "absent keeps zero", payload: `{}`, want: 0},
		{name: "null keeps zero", payload: `{"timeout":null}`, want: 0},
		{name: "negative number rejected", payload: `{"timeout":-5}`, wantErr: true},
		{name: "bad string rejected", payload: `{"timeout":"soon"}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg ComplexityLLMConfig
			err := json.Unmarshal([]byte(tt.payload), &cfg)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.Timeout)
		})
	}
}

func TestComplexityLLMConfigRejectsUnknownFields(t *testing.T) {
	var cfg ComplexityLLMConfig
	err := json.Unmarshal([]byte(`{"provider":"openai","model":"gpt-4.1-mini","instructions":"appended"}`), &cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown llm complexity field")
}

func TestComplexityLLMConfigTimeoutMarshalRoundTrip(t *testing.T) {
	cfg := testLLMConfig()
	cfg.Timeout = 2 * time.Second
	data, err := json.Marshal(cfg)
	require.NoError(t, err)

	var decoded ComplexityLLMConfig
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, cfg.Timeout, decoded.Timeout)
}

func TestComplexityLLMConfigNormalizedDefaults(t *testing.T) {
	cfg := (&ComplexityLLMConfig{
		Provider: " OpenAI ",
		Model:    " gpt-4.1-mini ",
		Prompt:   "  route legal work to COMPLEX  ",
	}).normalized()
	assert.Equal(t, "openai", string(cfg.Provider))
	assert.Equal(t, "gpt-4.1-mini", cfg.Model)
	assert.Equal(t, DefaultComplexityLLMTimeout, cfg.Timeout)
	assert.Equal(t, DefaultComplexityLLMMessageHistoryCount, cfg.MessageHistoryCount)
	assert.Equal(t, "route legal work to COMPLEX", cfg.Prompt)
}

func TestComplexityLLMConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*ComplexityLLMConfig)
		wantErr string
	}{
		{name: "valid", mutate: func(c *ComplexityLLMConfig) {}},
		{name: "missing provider", mutate: func(c *ComplexityLLMConfig) { c.Provider = "" }, wantErr: "requires a provider"},
		{name: "missing model", mutate: func(c *ComplexityLLMConfig) { c.Model = "" }, wantErr: "requires a model"},
		{
			name:    "prompt over limit",
			mutate:  func(c *ComplexityLLMConfig) { c.Prompt = strings.Repeat("x", MaxComplexityLLMPromptCharacters+1) },
			wantErr: "prompt exceeds",
		},
		{
			name:    "history over ceiling",
			mutate:  func(c *ComplexityLLMConfig) { c.MessageHistoryCount = MaxComplexityLLMMessageHistoryCount + 1 },
			wantErr: "message_history_count",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testLLMConfig().normalized()
			tt.mutate(cfg)
			err := cfg.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestComplexitySemanticFallbackValidation(t *testing.T) {
	t.Run("llm fallback requires llm block", func(t *testing.T) {
		cfg := testSemanticAnalyzerConfig()
		cfg.Semantic.Fallback = ComplexitySemanticFallbackLLM
		normalized := cfg.Normalized()
		err := normalized.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "requires an llm config block")
	})

	t.Run("unknown fallback rejected", func(t *testing.T) {
		cfg := testSemanticAnalyzerConfig()
		cfg.Semantic.Fallback = "lexical"
		normalized := cfg.Normalized()
		err := normalized.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "semantic fallback must be")
	})

	t.Run("absent fallback normalizes to none", func(t *testing.T) {
		cfg := testSemanticAnalyzerConfig()
		normalized := cfg.Normalized()
		require.NoError(t, normalized.Validate())
		assert.Equal(t, ComplexitySemanticFallbackNone, normalized.Semantic.Fallback)
		assert.False(t, normalized.LLMFallbackEnabled())
	})

	t.Run("dormant llm block with fallback none is legal and disabled", func(t *testing.T) {
		cfg := testSemanticAnalyzerConfig()
		cfg.LLM = testLLMConfig()
		normalized := cfg.Normalized()
		require.NoError(t, normalized.Validate())
		assert.False(t, normalized.LLMFallbackEnabled())
	})

	t.Run("enabled fallback reports enabled", func(t *testing.T) {
		normalized := testLLMFallbackAnalyzerConfig().Normalized()
		require.NoError(t, normalized.Validate())
		assert.True(t, normalized.LLMFallbackEnabled())
	})

	t.Run("llm block without semantic block never enables", func(t *testing.T) {
		cfg := testComplexityAnalyzerConfig()
		cfg.LLM = testLLMConfig()
		normalized := cfg.Normalized()
		require.NoError(t, normalized.Validate())
		assert.False(t, normalized.LLMFallbackEnabled())
	})
}

func TestMergeComplexityAnalyzerConfigLLMNoOpinion(t *testing.T) {
	base := testLLMFallbackAnalyzerConfig().Normalized()

	// A file that never mentions the semantic or llm sections must not undo
	// the fallback wiring.
	file := testComplexityAnalyzerConfig()
	merged, err := MergeComplexityAnalyzerConfig(&base, file)
	require.NoError(t, err)
	assert.Equal(t, ComplexitySemanticFallbackLLM, merged.Semantic.Fallback)
	require.NotNil(t, merged.LLM)
	assert.Equal(t, "gpt-4.1-mini", merged.LLM.Model)
	assert.True(t, merged.LLMFallbackEnabled())

	// A file that states them replaces them.
	file = testComplexityAnalyzerConfig()
	file.Semantic = testSemanticConfig()
	file.LLM = &ComplexityLLMConfig{Provider: "anthropic", Model: "claude-haiku-4-5-20251001"}
	merged, err = MergeComplexityAnalyzerConfig(&base, file)
	require.NoError(t, err)
	assert.Equal(t, ComplexitySemanticFallbackNone, merged.Semantic.Fallback)
	assert.Equal(t, "claude-haiku-4-5-20251001", merged.LLM.Model)
	assert.False(t, merged.LLMFallbackEnabled())
}

func TestMergeComplexityAnalyzerConfigByHashesLLMSections(t *testing.T) {
	base := testLLMFallbackAnalyzerConfig()
	baseHashes, err := GenerateComplexityAnalyzerConfigHashes(base)
	require.NoError(t, err)
	base.ConfigHashes = baseHashes
	normalizedBase := base.Normalized()

	t.Run("absent file sections leave db state alone", func(t *testing.T) {
		file := testComplexityAnalyzerConfig()
		fileHashes, err := GenerateComplexityAnalyzerConfigHashes(file)
		require.NoError(t, err)
		file.ConfigHashes = fileHashes

		merged, err := MergeComplexityAnalyzerConfigByHashes(&normalizedBase, file)
		require.NoError(t, err)
		assert.True(t, merged.LLMFallbackEnabled())
		require.NotNil(t, merged.LLM)
	})

	t.Run("unchanged hashes keep db edits", func(t *testing.T) {
		// The DB carries a runtime edit (a different model) under the same
		// section hash the file was last synced with.
		dbState := normalizedBase
		dbState.LLM = &ComplexityLLMConfig{Provider: "openai", Model: "runtime-edited"}

		file := testLLMFallbackAnalyzerConfig()
		fileHashes, err := GenerateComplexityAnalyzerConfigHashes(file)
		require.NoError(t, err)
		file.ConfigHashes = fileHashes
		dbState.ConfigHashes.LLMSettings = fileHashes.LLMSettings
		dbState.ConfigHashes.SemanticSettings = fileHashes.SemanticSettings

		merged, err := MergeComplexityAnalyzerConfigByHashes(&dbState, file)
		require.NoError(t, err)
		assert.Equal(t, "runtime-edited", merged.LLM.Model)
	})

	t.Run("changed hash overlays file section", func(t *testing.T) {
		file := testLLMFallbackAnalyzerConfig()
		file.LLM.Model = "file-updated"
		fileHashes, err := GenerateComplexityAnalyzerConfigHashes(file)
		require.NoError(t, err)
		file.ConfigHashes = fileHashes

		merged, err := MergeComplexityAnalyzerConfigByHashes(&normalizedBase, file)
		require.NoError(t, err)
		assert.Equal(t, "file-updated", merged.LLM.Model)
		assert.Equal(t, fileHashes.LLMSettings, merged.ConfigHashes.LLMSettings)
	})
}

func TestDecodeComplexityAnalyzerConfigRoundTripsLLM(t *testing.T) {
	cfg := testLLMFallbackAnalyzerConfig()
	cfg.LLM.Prompt = "route payroll to COMPLEX"
	normalized := cfg.Normalized()

	decoded, err := roundTripComplexityAnalyzerConfig(t, normalized)
	require.NoError(t, err)
	assert.Equal(t, ComplexitySemanticFallbackLLM, decoded.Semantic.Fallback)
	require.NotNil(t, decoded.LLM)
	assert.Equal(t, normalized.LLM.Model, decoded.LLM.Model)
	assert.Equal(t, "route payroll to COMPLEX", decoded.LLM.Prompt)
	assert.Equal(t, DefaultComplexityLLMTimeout, decoded.LLM.Timeout)
}

// TestLLMConfigLivesInTheSemanticRow pins where the llm block is stored. It
// backs the semantic classifier and is unreadable to a Bifrost that predates
// it, so it must not sit in the analyzer row that such a version rewrites.
func TestLLMConfigLivesInTheSemanticRow(t *testing.T) {
	normalized := testLLMFallbackAnalyzerConfig().Normalized()

	analyzerRaw, err := encodeComplexityAnalyzerConfig(normalized)
	require.NoError(t, err)
	semanticRaw, err := encodeComplexitySemanticConfigRow(normalized)
	require.NoError(t, err)

	assert.NotContains(t, string(analyzerRaw), "llm")
	assert.Contains(t, string(semanticRaw), `"llm"`)

	// And it survives an older Bifrost rewriting the analyzer row underneath it.
	rewritten := []byte(`{
		"tier_boundaries": {"simple_medium": 0.15, "medium_complex": 0.35, "complex_reasoning": 0.6},
		"keywords": {
			"code_keywords": ["function"],
			"reasoning_keywords": ["step by step"],
			"technical_keywords": ["architecture"],
			"simple_keywords": ["hello"]
		}
	}`)
	decoded, err := DecodeComplexityAnalyzerConfig(rewritten)
	require.NoError(t, err)
	row, err := decodeComplexitySemanticConfigRow(semanticRaw)
	require.NoError(t, err)

	combined := applyComplexitySemanticConfigRow(decoded, row)
	require.NotNil(t, combined)
	require.NotNil(t, combined.LLM)
	assert.Equal(t, normalized.LLM.Model, combined.LLM.Model)
	assert.True(t, combined.LLMFallbackEnabled())
}
