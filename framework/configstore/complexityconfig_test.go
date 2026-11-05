package configstore

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func testSemanticConfig() *ComplexitySemanticConfig {
	return &ComplexitySemanticConfig{
		Provider:       "openai",
		EmbeddingModel: "text-embedding-3-small",
	}
}

func testSemanticAnalyzerConfig() *ComplexityAnalyzerConfig {
	cfg := testComplexityAnalyzerConfig()
	cfg.Semantic = testSemanticConfig()
	return cfg
}

func testSessionAnalyzerConfig() *ComplexityAnalyzerConfig {
	cfg := testSemanticAnalyzerConfig()
	cfg.Session = &ComplexitySessionConfig{Enabled: true}
	return cfg
}

func TestComplexitySessionConfigDecoding(t *testing.T) {
	var cfg ComplexitySessionConfig
	require.NoError(t, json.Unmarshal([]byte(`{"enabled":true}`), &cfg))
	assert.True(t, cfg.Enabled)

	err := json.Unmarshal([]byte(`{"enable":true}`), &cfg)
	require.ErrorContains(t, err, `unknown complexity session field "enable"`)

	err = json.Unmarshal([]byte(`{}`), &cfg)
	require.ErrorContains(t, err, "requires enabled")
}

func TestComplexitySessionConfigRequiresSemanticWhenEnabled(t *testing.T) {
	cfg := testComplexityAnalyzerConfig()
	cfg.Session = &ComplexitySessionConfig{Enabled: true}
	normalized := cfg.Normalized()
	require.ErrorContains(t, normalized.Validate(), "requires a semantic config block")

	cfg.Session.Enabled = false
	normalized = cfg.Normalized()
	require.NoError(t, normalized.Validate())
}

func TestComplexitySemanticConfigTimeoutDecoding(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    time.Duration
		wantErr bool
	}{
		{name: "duration string", payload: `{"timeout":"250ms"}`, want: 250 * time.Millisecond},
		{name: "number is milliseconds", payload: `{"timeout":250}`, want: 250 * time.Millisecond},
		{name: "absent keeps zero", payload: `{}`, want: 0},
		{name: "null keeps zero", payload: `{"timeout":null}`, want: 0},
		{name: "negative number rejected", payload: `{"timeout":-5}`, wantErr: true},
		{name: "bad string rejected", payload: `{"timeout":"soon"}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg ComplexitySemanticConfig
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

// TestComplexityDecisionConfigDefaults verifies the default user-history window and timeout.
func TestComplexityDecisionConfigDefaults(t *testing.T) {
	config := (&ComplexityDecisionConfig{}).normalized()
	require.NotNil(t, config.PreviousMessageCount)
	assert.Equal(t, 1, *config.PreviousMessageCount)
	assert.Equal(t, 1500*time.Millisecond, config.Timeout)
}

// "fallback" is deliberately absent here: it was removed with the lexical
// fallback and later reintroduced for the llm fallback classifier, so it is a
// live field again (decoding covered by TestComplexitySemanticFallbackValidation).
func TestComplexitySemanticConfigRejectsRemovedFields(t *testing.T) {
	for _, field := range []string{"dimension"} {
		t.Run(field, func(t *testing.T) {
			var cfg ComplexitySemanticConfig
			err := json.Unmarshal([]byte(`{"provider":"openai","embedding_model":"text-embedding-3-small","`+field+`":true}`), &cfg)
			require.ErrorContains(t, err, `unknown semantic complexity field "`+field+`"`)
		})
	}
}

func TestComplexitySemanticConfigTimeoutMarshalRoundTrip(t *testing.T) {
	cfg := testSemanticConfig()
	cfg.Timeout = 250 * time.Millisecond

	data, err := json.Marshal(cfg)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"timeout":"250ms"`)

	var decoded ComplexitySemanticConfig
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, cfg.Timeout, decoded.Timeout)
}

func TestComplexitySemanticConfigNormalizedDefaults(t *testing.T) {
	normalized := testSemanticConfig().normalized()

	assert.Equal(t, DefaultComplexitySemanticTimeout, normalized.Timeout)
	assert.Equal(t, ComplexitySemanticVectorStoreEmbedded, normalized.VectorStore)
	require.NoError(t, normalized.Validate())
}

// TestComplexityConfigNormalizedProviderCase pins that built-in provider names
// are canonicalized to lowercase while custom provider names keep their case,
// since custom providers are registered (and looked up) under their exact name.
func TestComplexityConfigNormalizedProviderCase(t *testing.T) {
	tests := []struct {
		name     string
		provider schemas.ModelProvider
		want     schemas.ModelProvider
	}{
		{name: "standard provider is lowercased", provider: " OpenAI ", want: "openai"},
		{name: "custom provider keeps its case", provider: " DeepInfra-Embeddings ", want: "DeepInfra-Embeddings"},
		{name: "lowercase custom provider is unchanged", provider: "deepinfra-embeddings", want: "deepinfra-embeddings"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			semantic := testSemanticConfig()
			semantic.Provider = tt.provider
			assert.Equal(t, tt.want, semantic.normalized().Provider)

			llm := &ComplexityLLMConfig{Provider: tt.provider, Model: "gpt-4.1-mini"}
			assert.Equal(t, tt.want, llm.normalized().Provider)
		})
	}
}

func TestComplexitySemanticConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ComplexitySemanticConfig)
	}{
		{name: "missing provider", mutate: func(c *ComplexitySemanticConfig) { c.Provider = "" }},
		{name: "missing embedding model", mutate: func(c *ComplexitySemanticConfig) { c.EmbeddingModel = " " }},
		{name: "unknown vector store", mutate: func(c *ComplexitySemanticConfig) { c.VectorStore = "pgvector" }},
		{name: "negative min similarity", mutate: func(c *ComplexitySemanticConfig) { c.MinSimilarity = -0.1 }},
		// 1 is arithmetically legal but rejects every real match, which is a
		// misconfiguration rather than a way to disable semantic routing.
		{name: "min similarity at one", mutate: func(c *ComplexitySemanticConfig) { c.MinSimilarity = 1 }},
		{name: "min similarity above one", mutate: func(c *ComplexitySemanticConfig) { c.MinSimilarity = 1.5 }},
		// Every comparison against NaN is false, so a plain range check would
		// accept it and the floor would silently never apply.
		{name: "min similarity not a number", mutate: func(c *ComplexitySemanticConfig) { c.MinSimilarity = math.NaN() }},
		{name: "negative message history count", mutate: func(c *ComplexitySemanticConfig) { c.MessageHistoryCount = -1 }},
		{
			name: "message history count above the ceiling",
			mutate: func(c *ComplexitySemanticConfig) {
				c.MessageHistoryCount = MaxComplexitySemanticMessageHistoryCount + 1
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testSemanticConfig()
			tt.mutate(cfg)
			require.Error(t, cfg.normalized().Validate())
		})
	}
}

// TestComplexitySemanticConfigMessageHistoryCountDefaults keeps an omitted
// window meaning "embed the latest message only", the pre-existing behavior.
func TestComplexitySemanticConfigMessageHistoryCountDefaults(t *testing.T) {
	normalized := testSemanticConfig().normalized()
	assert.Equal(t, DefaultComplexitySemanticMessageHistoryCount, normalized.MessageHistoryCount)
	require.NoError(t, normalized.Validate())

	for _, count := range []int{1, 5, MaxComplexitySemanticMessageHistoryCount} {
		cfg := testSemanticConfig()
		cfg.MessageHistoryCount = count
		resolved := cfg.normalized()
		require.NoError(t, resolved.Validate())
		assert.Equal(t, count, resolved.MessageHistoryCount)
	}
}

// TestComplexitySemanticConfigMinSimilarityAccepted covers the in-range values,
// including the zero default that keeps "nearest exemplar always wins".
func TestComplexitySemanticConfigMinSimilarityAccepted(t *testing.T) {
	for _, minSimilarity := range []float64{0, 0.35, 0.999} {
		cfg := testSemanticConfig()
		cfg.MinSimilarity = minSimilarity
		normalized := cfg.normalized()
		require.NoError(t, normalized.Validate())
		assert.Equal(t, minSimilarity, normalized.MinSimilarity)
	}
}

// TestComplexityAnalyzerConfigNormalizedPreservesLexicalCrossTierDuplicates
// keeps the legacy lexical multi-mask behavior when semantic routing is off.
func TestComplexityAnalyzerConfigNormalizedPreservesLexicalCrossTierDuplicates(t *testing.T) {
	cfg := testComplexityAnalyzerConfig()
	cfg.Keywords = ComplexityEditableKeywordConfig{
		SimpleKeywords:  []string{"Shared", "simple-only", "medium-only"},
		MediumKeywords:  []string{"shared", "medium-only", "complex-only"},
		ComplexKeywords: []string{"shared", "complex-only"},
	}

	normalized := cfg.Normalized()
	assert.Equal(t, []string{"medium-only", "shared", "simple-only"}, normalized.Keywords.SimpleKeywords)
	assert.Equal(t, []string{"complex-only", "medium-only", "shared"}, normalized.Keywords.MediumKeywords)
	assert.Equal(t, []string{"complex-only", "shared"}, normalized.Keywords.ComplexKeywords)
	require.NoError(t, normalized.Validate())
}

// TestComplexityAnalyzerConfigNormalizedDefaultsOmittedLegacyBoundaries keeps
// the deprecated boundary block optional without leaving the dormant analyzer
// in an invalid zero-value state.
func TestComplexityAnalyzerConfigNormalizedDefaultsOmittedLegacyBoundaries(t *testing.T) {
	cfg := testComplexityAnalyzerConfig()
	cfg.TierBoundaries = ComplexityTierBoundaries{}

	normalized := cfg.Normalized()
	assert.Equal(t, DefaultComplexityTierBoundaries(), normalized.TierBoundaries)
	require.NoError(t, normalized.Validate())
}

// roundTripComplexityAnalyzerConfig persists a config through both
// governance_config rows and reads it back the way the store does, so tests
// exercise the split rather than one row in isolation.
func roundTripComplexityAnalyzerConfig(t *testing.T, cfg ComplexityAnalyzerConfig) (*ComplexityAnalyzerConfig, error) {
	t.Helper()

	analyzerRaw, err := encodeComplexityAnalyzerConfig(cfg)
	require.NoError(t, err)
	semanticRaw, err := encodeComplexitySemanticConfigRow(cfg)
	require.NoError(t, err)

	decoded, err := DecodeComplexityAnalyzerConfig(analyzerRaw)
	if err != nil {
		return nil, err
	}
	semantic, err := decodeComplexitySemanticConfigRow(semanticRaw)
	if err != nil {
		return nil, err
	}
	combined := applyComplexitySemanticConfigRow(decoded, semantic)
	require.NotNil(t, combined, "both rows are written above, so both must read back")

	normalized := combined.Normalized()
	if err := normalized.Validate(); err != nil {
		return nil, err
	}
	return &normalized, nil
}

func TestComplexityAnalyzerConfigRejectsSemanticCrossTierDuplicates(t *testing.T) {
	cfg := testSemanticAnalyzerConfig()
	cfg.Keywords.SimpleKeywords = []string{"Shared   phrase", "simple-only"}
	cfg.Keywords.MediumKeywords = []string{"shared phrase", "medium-only"}

	_, err := roundTripComplexityAnalyzerConfig(t, *cfg)
	require.ErrorContains(t, err, `semantic phrase "shared phrase" appears in both simple_keywords and medium_keywords`)
}

func TestComplexityAnalyzerConfigSemanticPhraseValidation(t *testing.T) {
	t.Run("allows exactly the combined phrase limit", func(t *testing.T) {
		cfg := testSemanticAnalyzerConfig()
		cfg.Keywords.SimpleKeywords = make([]string, MaxComplexitySemanticPhrases-2)
		for index := range cfg.Keywords.SimpleKeywords {
			cfg.Keywords.SimpleKeywords[index] = fmt.Sprintf("simple-%d", index)
		}
		cfg.Keywords.MediumKeywords = []string{"medium"}
		cfg.Keywords.ComplexKeywords = []string{"complex"}

		normalized := cfg.Normalized()
		require.NoError(t, normalized.Validate())
	})

	t.Run("rejects more than the combined phrase limit", func(t *testing.T) {
		cfg := testSemanticAnalyzerConfig()
		cfg.Keywords.SimpleKeywords = make([]string, MaxComplexitySemanticPhrases-1)
		for index := range cfg.Keywords.SimpleKeywords {
			cfg.Keywords.SimpleKeywords[index] = fmt.Sprintf("simple-%d", index)
		}
		cfg.Keywords.MediumKeywords = []string{"medium"}
		cfg.Keywords.ComplexKeywords = []string{"complex"}

		normalized := cfg.Normalized()
		require.ErrorContains(t, normalized.Validate(), "contains 751 phrases (simple=749, medium=1, complex=1); maximum is 750")
	})

	t.Run("counts canonical phrases", func(t *testing.T) {
		cfg := testSemanticAnalyzerConfig()
		cfg.Keywords.SimpleKeywords = make([]string, MaxComplexitySemanticPhrases-2)
		for index := range cfg.Keywords.SimpleKeywords {
			cfg.Keywords.SimpleKeywords[index] = fmt.Sprintf("simple-%d", index)
		}
		cfg.Keywords.SimpleKeywords = append(cfg.Keywords.SimpleKeywords, " SIMPLE-0 ", "")
		cfg.Keywords.MediumKeywords = []string{"medium"}
		cfg.Keywords.ComplexKeywords = []string{"complex"}

		normalized := cfg.Normalized()
		require.Len(t, normalized.Keywords.SimpleKeywords, MaxComplexitySemanticPhrases-2)
		require.NoError(t, normalized.Validate())
	})

	t.Run("does not cap lexical-only lists", func(t *testing.T) {
		cfg := testSemanticAnalyzerConfig()
		cfg.Semantic = nil
		cfg.Keywords.SimpleKeywords = make([]string, MaxComplexitySemanticPhrases)
		for index := range cfg.Keywords.SimpleKeywords {
			cfg.Keywords.SimpleKeywords[index] = fmt.Sprintf("simple-%d", index)
		}
		cfg.Keywords.MediumKeywords = []string{"medium"}
		cfg.Keywords.ComplexKeywords = []string{"complex"}

		normalized := cfg.Normalized()
		require.Greater(t, len(normalized.Keywords.SimpleKeywords)+len(normalized.Keywords.MediumKeywords)+len(normalized.Keywords.ComplexKeywords), MaxComplexitySemanticPhrases)
		require.NoError(t, normalized.Validate())
	})

	t.Run("per phrase character cap", func(t *testing.T) {
		cfg := testSemanticAnalyzerConfig()
		cfg.Keywords.SimpleKeywords = []string{strings.Repeat("界", MaxComplexitySemanticPhraseCharacters+1)}

		normalized := cfg.Normalized()
		require.ErrorContains(t, normalized.Validate(), "exceeds the 2000-character limit")
	})
}

func TestDecodeComplexityAnalyzerConfigSemanticRoundTrip(t *testing.T) {
	cfg := testSemanticAnalyzerConfig()
	cfg.Session = &ComplexitySessionConfig{Enabled: true}
	cfg.ConfigHashes = ComplexityAnalyzerConfigHashes{
		TierBoundaries:   "tier-hash",
		SimpleKeywords:   "simple-hash",
		MediumKeywords:   "medium-hash",
		ComplexKeywords:  "complex-hash",
		SemanticSettings: "settings-hash",
		SessionSettings:  "session-hash",
	}
	cfg.EmbeddingFingerprint = "fingerprint-1"

	analyzerRaw, err := encodeComplexityAnalyzerConfig(cfg.Normalized())
	require.NoError(t, err)
	semanticRaw, err := encodeComplexitySemanticConfigRow(cfg.Normalized())
	require.NoError(t, err)

	// Everything only the semantic router understands belongs to the semantic
	// row. Anything of it that leaks into the analyzer row is something an older
	// Bifrost would silently drop the next time it saved.
	assert.Contains(t, string(semanticRaw), `"_embedding_fingerprint":"fingerprint-1"`)
	assert.Contains(t, string(semanticRaw), `"session":{"enabled":true}`)
	assert.NotContains(t, string(analyzerRaw), "_embedding_fingerprint")
	assert.NotContains(t, string(analyzerRaw), "semantic")
	assert.NotContains(t, string(analyzerRaw), "session")

	decoded, err := roundTripComplexityAnalyzerConfig(t, cfg.Normalized())
	require.NoError(t, err)
	require.NotNil(t, decoded.Semantic)
	assert.Equal(t, cfg.Normalized().Semantic, decoded.Semantic)
	assert.Equal(t, cfg.Session, decoded.Session)
	assert.Equal(t, cfg.ConfigHashes, decoded.ConfigHashes)
	assert.Equal(t, "fingerprint-1", decoded.EmbeddingFingerprint)
}

func TestDecodeComplexityAnalyzerConfigWithoutSemantic(t *testing.T) {
	raw, err := encodeComplexityAnalyzerConfig(testComplexityAnalyzerConfig().Normalized())
	require.NoError(t, err)

	decoded, err := DecodeComplexityAnalyzerConfig(raw)
	require.NoError(t, err)
	assert.Nil(t, decoded.Semantic)
	assert.Empty(t, decoded.EmbeddingFingerprint)
}

func TestGenerateComplexityAnalyzerConfigHashesSemantic(t *testing.T) {
	base := testSemanticAnalyzerConfig()
	baseHashes, err := GenerateComplexityAnalyzerConfigHashes(base)
	require.NoError(t, err)
	require.NotEmpty(t, baseHashes.SemanticSettings)

	// Keyword edits must not move the semantic settings hash: the shared lists
	// are tracked by the keyword section hashes.
	keywordEdit := testSemanticAnalyzerConfig()
	keywordEdit.Keywords.SimpleKeywords = append(keywordEdit.Keywords.SimpleKeywords, "weather")
	keywordHashes, err := GenerateComplexityAnalyzerConfigHashes(keywordEdit)
	require.NoError(t, err)
	assert.Equal(t, baseHashes.SemanticSettings, keywordHashes.SemanticSettings)
	assert.NotEqual(t, baseHashes.SimpleKeywords, keywordHashes.SimpleKeywords)

	// Semantic scalar edits must not move the keyword hashes.
	scalarEdit := testSemanticAnalyzerConfig()
	scalarEdit.Semantic.EmbeddingModel = "text-embedding-3-large"
	scalarHashes, err := GenerateComplexityAnalyzerConfigHashes(scalarEdit)
	require.NoError(t, err)
	assert.NotEqual(t, baseHashes.SemanticSettings, scalarHashes.SemanticSettings)
	assert.Equal(t, baseHashes.SimpleKeywords, scalarHashes.SimpleKeywords)

	// No semantic section means no semantic hash.
	plainHashes, err := GenerateComplexityAnalyzerConfigHashes(testComplexityAnalyzerConfig())
	require.NoError(t, err)
	assert.Empty(t, plainHashes.SemanticSettings)
}

func TestGenerateComplexityAnalyzerConfigHashesSession(t *testing.T) {
	enabled := testSessionAnalyzerConfig()
	enabledHashes, err := GenerateComplexityAnalyzerConfigHashes(enabled)
	require.NoError(t, err)
	require.NotEmpty(t, enabledHashes.SessionSettings)

	disabled := testSessionAnalyzerConfig()
	disabled.Session.Enabled = false
	disabledHashes, err := GenerateComplexityAnalyzerConfigHashes(disabled)
	require.NoError(t, err)
	assert.NotEqual(t, enabledHashes.SessionSettings, disabledHashes.SessionSettings)
	assert.Equal(t, enabledHashes.SemanticSettings, disabledHashes.SemanticSettings)

	withoutSession, err := GenerateComplexityAnalyzerConfigHashes(testSemanticAnalyzerConfig())
	require.NoError(t, err)
	assert.Empty(t, withoutSession.SessionSettings)
}

func TestMergeComplexityAnalyzerConfigByHashesSession(t *testing.T) {
	withHashes := func(cfg *ComplexityAnalyzerConfig) *ComplexityAnalyzerConfig {
		hashes, err := GenerateComplexityAnalyzerConfigHashes(cfg)
		require.NoError(t, err)
		cfg.ConfigHashes = hashes
		return cfg
	}

	t.Run("file omission preserves persisted session setting", func(t *testing.T) {
		base := withHashes(testSessionAnalyzerConfig())
		file := withHashes(testSemanticAnalyzerConfig())

		merged, err := MergeComplexityAnalyzerConfigByHashes(base, file)
		require.NoError(t, err)
		require.NotNil(t, merged.Session)
		assert.True(t, merged.Session.Enabled)
		assert.Equal(t, base.ConfigHashes.SessionSettings, merged.ConfigHashes.SessionSettings)
	})

	t.Run("explicit false overrides enabled", func(t *testing.T) {
		base := withHashes(testSessionAnalyzerConfig())
		file := testSessionAnalyzerConfig()
		file.Session.Enabled = false
		withHashes(file)

		merged, err := MergeComplexityAnalyzerConfigByHashes(base, file)
		require.NoError(t, err)
		require.NotNil(t, merged.Session)
		assert.False(t, merged.Session.Enabled)
		assert.Equal(t, file.ConfigHashes.SessionSettings, merged.ConfigHashes.SessionSettings)
	})
}

func TestMergeComplexityAnalyzerConfigByHashesSemantic(t *testing.T) {
	fileConfig := func() *ComplexityAnalyzerConfig {
		cfg := testSemanticAnalyzerConfig()
		hashes, err := GenerateComplexityAnalyzerConfigHashes(cfg)
		require.NoError(t, err)
		cfg.ConfigHashes = hashes
		return cfg
	}

	t.Run("file adds semantic to base without one", func(t *testing.T) {
		base := testComplexityAnalyzerConfig()
		file := fileConfig()

		merged, err := MergeComplexityAnalyzerConfigByHashes(base, file)
		require.NoError(t, err)
		require.NotNil(t, merged.Semantic)
		assert.Equal(t, file.Normalized().Semantic, merged.Semantic)
		assert.Equal(t, file.ConfigHashes.SemanticSettings, merged.ConfigHashes.SemanticSettings)
	})

	t.Run("unchanged hash preserves DB edits", func(t *testing.T) {
		file := fileConfig()
		base := fileConfig()
		// Simulate a UI edit persisted after the last file sync.
		base.Semantic.EmbeddingModel = "runtime-model"

		merged, err := MergeComplexityAnalyzerConfigByHashes(base, file)
		require.NoError(t, err)
		assert.Equal(t, "runtime-model", merged.Semantic.EmbeddingModel)
	})

	t.Run("settings change replaces the semantic block", func(t *testing.T) {
		base := fileConfig()

		file := fileConfig()
		file.Semantic.EmbeddingModel = "text-embedding-3-large"
		fileHashes, err := GenerateComplexityAnalyzerConfigHashes(file)
		require.NoError(t, err)
		file.ConfigHashes = fileHashes

		merged, err := MergeComplexityAnalyzerConfigByHashes(base, file)
		require.NoError(t, err)
		assert.Equal(t, "text-embedding-3-large", merged.Semantic.EmbeddingModel)
		assert.Equal(t, fileHashes.SemanticSettings, merged.ConfigHashes.SemanticSettings)
	})

	t.Run("file without semantic preserves DB semantic", func(t *testing.T) {
		base := fileConfig()
		base.EmbeddingFingerprint = "fingerprint-1"

		file := testComplexityAnalyzerConfig()
		fileHashes, err := GenerateComplexityAnalyzerConfigHashes(file)
		require.NoError(t, err)
		file.ConfigHashes = fileHashes

		merged, err := MergeComplexityAnalyzerConfigByHashes(base, file)
		require.NoError(t, err)
		require.NotNil(t, merged.Semantic)
		assert.Equal(t, base.Normalized().Semantic, merged.Semantic)
		assert.Equal(t, base.ConfigHashes.SemanticSettings, merged.ConfigHashes.SemanticSettings)
		assert.Equal(t, "fingerprint-1", merged.EmbeddingFingerprint)
	})
}

func TestRDBConfigStore_ComplexityAnalyzerConfigSemanticPersistence(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	cfg := testSemanticAnalyzerConfig()
	cfg.EmbeddingFingerprint = "fingerprint-1"
	require.NoError(t, store.UpdateComplexityAnalyzerConfig(ctx, cfg))

	got, err := store.GetComplexityAnalyzerConfig(ctx)
	require.NoError(t, err)
	require.NotNil(t, got.Semantic)
	assert.Equal(t, cfg.Normalized().Semantic, got.Semantic)
	assert.Equal(t, "fingerprint-1", got.EmbeddingFingerprint)

	// A UI-style write without a fingerprint must not wipe the stored one.
	update := testSemanticAnalyzerConfig()
	update.Semantic.EmbeddingModel = "text-embedding-3-large"
	require.NoError(t, store.UpdateComplexityAnalyzerConfig(ctx, update))

	got, err = store.GetComplexityAnalyzerConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, "text-embedding-3-large", got.Semantic.EmbeddingModel)
	assert.Equal(t, "fingerprint-1", got.EmbeddingFingerprint)
}

func TestRDBConfigStore_ComplexitySessionPersistenceAndReset(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	cfg := testSessionAnalyzerConfig()
	hashes, err := GenerateComplexityAnalyzerConfigHashes(cfg)
	require.NoError(t, err)
	cfg.ConfigHashes = hashes
	require.NoError(t, store.UpdateComplexityAnalyzerConfig(ctx, cfg))

	got, err := store.GetComplexityAnalyzerConfig(ctx)
	require.NoError(t, err)
	require.NotNil(t, got.Session)
	assert.True(t, got.Session.Enabled)
	assert.Equal(t, hashes.SessionSettings, got.ConfigHashes.SessionSettings)

	// UI payloads omit internal hashes. The split-row carry-over path must keep
	// the session hash beside the session setting in the semantic row.
	update := testSessionAnalyzerConfig()
	require.NoError(t, store.UpdateComplexityAnalyzerConfig(ctx, update))
	got, err = store.GetComplexityAnalyzerConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, hashes.SessionSettings, got.ConfigHashes.SessionSettings)

	restored, err := store.ResetComplexityAnalyzerConfig(ctx, testComplexityAnalyzerConfig())
	require.NoError(t, err)
	require.NotNil(t, restored.Session)
	assert.True(t, restored.Session.Enabled)
}

// A writer that carries ConfigHashes/EmbeddingFingerprint over from the stored row must not
// clobber a concurrent writer that is setting fresh ones. The carry-over read and the save
// have to be one atomic unit; if they are not, the carrying writer can read the pre-update
// values, sleep through the other writer's save, and then persist the stale copy.
func TestRDBConfigStore_UpdateComplexityAnalyzerConfigConcurrentCarryOver(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	// :memory: SQLite gives every pooled connection its own database, so pin the pool to one
	// connection. Transactions still hold it for their whole span, which is what serializes
	// the two writers below.
	sqlDB, err := store.DB().DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)

	seed := testSemanticAnalyzerConfig()
	seed.EmbeddingFingerprint = "fingerprint-old"
	seedHashes, err := GenerateComplexityAnalyzerConfigHashes(seed)
	require.NoError(t, err)
	seed.ConfigHashes = seedHashes
	require.NoError(t, store.UpdateComplexityAnalyzerConfig(ctx, seed))

	// Widen the window between the carry-over read and the save so an unserialized update
	// would reliably lose the race. Only armed for the concurrent phase below.
	var armed atomic.Bool
	require.NoError(t, store.DB().Callback().Query().After("gorm:query").
		Register("test:delay_governance_config_read", func(db *gorm.DB) {
			if armed.Load() && db.Statement.Table == "governance_config" {
				time.Sleep(50 * time.Millisecond)
			}
		}))
	t.Cleanup(func() {
		_ = store.DB().Callback().Query().Remove("test:delay_governance_config_read")
	})

	// Writer A supplies both fields, so it never reads.
	writerA := testSemanticAnalyzerConfig()
	writerA.Semantic.EmbeddingModel = "text-embedding-3-large"
	writerA.EmbeddingFingerprint = "fingerprint-new"
	hashesA, err := GenerateComplexityAnalyzerConfigHashes(writerA)
	require.NoError(t, err)
	writerA.ConfigHashes = hashesA

	// Writer B is a UI-style update: it omits both fields and carries them over.
	writerB := testSemanticAnalyzerConfig()
	writerB.Keywords.SimpleKeywords = []string{"hi"}

	armed.Store(true)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, cfg := range []*ComplexityAnalyzerConfig{writerA, writerB} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = store.UpdateComplexityAnalyzerConfig(ctx, cfg)
		}()
	}
	wg.Wait()
	armed.Store(false)
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])

	// Whichever order the two writers land in, writer A's values must survive: it either
	// wrote last, or writer B read them under the same lock and carried them forward.
	got, err := store.GetComplexityAnalyzerConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, "fingerprint-new", got.EmbeddingFingerprint)
	assert.Equal(t, hashesA, got.ConfigHashes)
}

// TestRDBConfigStore_ResetComplexityAnalyzerConfigConcurrentSemanticEdit pins the reason the
// reset performs its read inside the write transaction. A reset that read the record first and
// wrote it back afterwards would carry a stale copy of the semantic block — the section it
// exists to preserve — over an edit committed in the window between the two.
func TestRDBConfigStore_ResetComplexityAnalyzerConfigConcurrentSemanticEdit(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	// :memory: SQLite gives every pooled connection its own database, so pin the pool to one
	// connection, matching the carry-over race test above.
	sqlDB, err := store.DB().DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)

	seed := testSemanticAnalyzerConfig()
	seed.Keywords.SimpleKeywords = []string{"operator phrase"}
	require.NoError(t, store.UpdateComplexityAnalyzerConfig(ctx, seed))

	// Widen the window between the reset's read and its save so an unserialized reset would
	// reliably lose the concurrent edit.
	var armed atomic.Bool
	require.NoError(t, store.DB().Callback().Query().After("gorm:query").
		Register("test:delay_reset_config_read", func(db *gorm.DB) {
			if armed.Load() && db.Statement.Table == "governance_config" {
				time.Sleep(50 * time.Millisecond)
			}
		}))
	t.Cleanup(func() {
		_ = store.DB().Callback().Query().Remove("test:delay_reset_config_read")
	})

	// The competing writer repoints the classifier at a different embedding model.
	editor := testSemanticAnalyzerConfig()
	editor.Semantic.EmbeddingModel = "text-embedding-3-large"

	defaults := testComplexityAnalyzerConfig()
	armed.Store(true)
	var wg sync.WaitGroup
	var resetErr, editErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, resetErr = store.ResetComplexityAnalyzerConfig(ctx, defaults)
	}()
	go func() {
		defer wg.Done()
		editErr = store.UpdateComplexityAnalyzerConfig(ctx, editor)
	}()
	wg.Wait()
	armed.Store(false)
	require.NoError(t, resetErr)
	require.NoError(t, editErr)

	got, err := store.GetComplexityAnalyzerConfig(ctx)
	require.NoError(t, err)
	require.NotNil(t, got.Semantic)
	// Whichever order the two land in, the newer embedding model survives: either the edit
	// wrote last, or the reset read it under the same lock and preserved it. The one outcome
	// ruled out is the reset resurrecting the seeded model it read before the edit committed.
	assert.Equal(t, "text-embedding-3-large", got.Semantic.EmbeddingModel)
	// And the reset still did its own job.
	assert.Equal(t, defaults.Keywords.SimpleKeywords, got.Keywords.SimpleKeywords)
	assert.Equal(t, defaults.TierBoundaries, got.TierBoundaries)
}

// TestRDBConfigStore_ResetComplexityAnalyzerConfigConcurrentFirstWrite covers the same race
// on a fresh install, where no row exists yet. This is the case a FOR UPDATE read cannot
// serialize on its own: there is no row to lock, so without the placeholder insert both
// transactions read "absent" and the reset writes its defaults over the semantic block the
// first-time save just committed.
//
// This pins the contract; it does not by itself reproduce the failure. The race is specific
// to Postgres, where dbForUpdate actually emits FOR UPDATE, and these tests run against
// in-memory SQLite pinned to one connection, which serializes the two transactions outright.
// Removing lockComplexityAnalyzerConfigRow leaves this test green — verifying the fix needs
// a Postgres-backed run.
func TestRDBConfigStore_ResetComplexityAnalyzerConfigConcurrentFirstWrite(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	sqlDB, err := store.DB().DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)

	// Deliberately no seed: the row must not exist when the two transactions start.
	existing, err := store.GetComplexityAnalyzerConfig(ctx)
	require.NoError(t, err)
	require.Nil(t, existing)

	var armed atomic.Bool
	require.NoError(t, store.DB().Callback().Query().After("gorm:query").
		Register("test:delay_first_write_config_read", func(db *gorm.DB) {
			if armed.Load() && db.Statement.Table == "governance_config" {
				time.Sleep(50 * time.Millisecond)
			}
		}))
	t.Cleanup(func() {
		_ = store.DB().Callback().Query().Remove("test:delay_first_write_config_read")
	})

	// The competing writer is the very first configuration ever saved.
	editor := testSemanticAnalyzerConfig()
	editor.Semantic.EmbeddingModel = "text-embedding-3-large"

	defaults := testComplexityAnalyzerConfig()
	armed.Store(true)
	var wg sync.WaitGroup
	var resetErr, editErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, resetErr = store.ResetComplexityAnalyzerConfig(ctx, defaults)
	}()
	go func() {
		defer wg.Done()
		editErr = store.UpdateComplexityAnalyzerConfig(ctx, editor)
	}()
	wg.Wait()
	armed.Store(false)
	require.NoError(t, resetErr)
	require.NoError(t, editErr)

	got, err := store.GetComplexityAnalyzerConfig(ctx)
	require.NoError(t, err)
	// If the save landed first, the reset must have read it under the same lock and kept
	// the semantic block. If the reset landed first, the save is simply the later writer.
	// Either way the committed semantic configuration survives.
	require.NotNil(t, got.Semantic)
	assert.Equal(t, "text-embedding-3-large", got.Semantic.EmbeddingModel)
}

// preSemanticAnalyzerRow mirrors how a Bifrost that predates the semantic router
// reads the analyzer row: four keyword lists and three tier boundaries, with
// every field required. It is deliberately a local copy rather than a call into
// the current types — the point is to pin the shape that older binaries parse,
// so it must not move when the current shape does.
type preSemanticAnalyzerRow struct {
	TierBoundaries struct {
		SimpleMedium     float64 `json:"simple_medium"`
		MediumComplex    float64 `json:"medium_complex"`
		ComplexReasoning float64 `json:"complex_reasoning"`
	} `json:"tier_boundaries"`
	Keywords struct {
		CodeKeywords      []string `json:"code_keywords"`
		ReasoningKeywords []string `json:"reasoning_keywords"`
		TechnicalKeywords []string `json:"technical_keywords"`
		SimpleKeywords    []string `json:"simple_keywords"`
	} `json:"keywords"`
}

func (r preSemanticAnalyzerRow) validate() error {
	b := r.TierBoundaries
	if !(0 < b.SimpleMedium && b.SimpleMedium < b.MediumComplex &&
		b.MediumComplex < b.ComplexReasoning && b.ComplexReasoning < 1) {
		return fmt.Errorf(
			"tier boundaries must satisfy 0 < simple_medium (%.4f) < medium_complex (%.4f) < complex_reasoning (%.4f) < 1",
			b.SimpleMedium, b.MediumComplex, b.ComplexReasoning,
		)
	}
	var missing []string
	if len(r.Keywords.CodeKeywords) == 0 {
		missing = append(missing, "code_keywords")
	}
	if len(r.Keywords.ReasoningKeywords) == 0 {
		missing = append(missing, "reasoning_keywords")
	}
	if len(r.Keywords.TechnicalKeywords) == 0 {
		missing = append(missing, "technical_keywords")
	}
	if len(r.Keywords.SimpleKeywords) == 0 {
		missing = append(missing, "simple_keywords")
	}
	if len(missing) > 0 {
		return fmt.Errorf("keyword lists must be non-empty: %s", strings.Join(missing, ", "))
	}
	return nil
}

// TestPersistedAnalyzerRowStaysReadableByPreSemanticBifrost is the regression
// guard for the rollback path: this shape shipped, so a row this version writes
// has to stay parseable and valid for a binary that predates the semantic
// router. Deleting the dual-write breaks this test, which is the point.
func TestPersistedAnalyzerRowStaysReadableByPreSemanticBifrost(t *testing.T) {
	t.Run("semantic config", func(t *testing.T) {
		raw, err := encodeComplexityAnalyzerConfig(testSemanticAnalyzerConfig().Normalized())
		require.NoError(t, err)

		var row preSemanticAnalyzerRow
		require.NoError(t, json.Unmarshal(raw, &row))
		require.NoError(t, row.validate())

		// All four lists carry released defaults, not exemplars: an older Bifrost
		// scores lexically against whatever is in them, and exemplars are whole
		// sentences that match nothing.
		assert.Equal(t, legacyCodeKeywords, row.Keywords.CodeKeywords)
		assert.Equal(t, legacyReasoningKeywords, row.Keywords.ReasoningKeywords)
		assert.Equal(t, legacyTechnicalKeywords, row.Keywords.TechnicalKeywords)
		assert.Equal(t, legacySimpleKeywords, row.Keywords.SimpleKeywords)

		// Nothing canonical leaks into this row. The lists are un-shared, so an
		// older Bifrost cannot reach an exemplar even by accident.
		assert.NotContains(t, string(raw), "medium_keywords")
		assert.NotContains(t, string(raw), "complex_keywords")
	})

	t.Run("operator raised medium_complex above the released default", func(t *testing.T) {
		cfg := testSemanticAnalyzerConfig()
		cfg.TierBoundaries = ComplexityTierBoundaries{SimpleMedium: 0.7, MediumComplex: 0.8}

		raw, err := encodeComplexityAnalyzerConfig(cfg.Normalized())
		require.NoError(t, err)

		var row preSemanticAnalyzerRow
		require.NoError(t, json.Unmarshal(raw, &row))
		require.NoError(t, row.validate(),
			"a synthesized third boundary must satisfy the old validator at any medium_complex")
		assert.Greater(t, row.TierBoundaries.ComplexReasoning, 0.8)
	})
}

func TestLegacyComplexReasoningBoundary(t *testing.T) {
	// The released default is preferred, so a rolled-back Bifrost behaves the way
	// it would on a fresh install.
	assert.Equal(t, DefaultComplexityComplexReasoningBoundary,
		legacyComplexReasoningBoundary(DefaultComplexityMediumComplexBoundary))

	// It stops being usable once an operator raises medium_complex past it, and a
	// value the old validator rejects is the breakage this exists to prevent.
	assert.Equal(t, 0.85, legacyComplexReasoningBoundary(0.7))
	assert.Greater(t, legacyComplexReasoningBoundary(0.99), 0.99)
	assert.Less(t, legacyComplexReasoningBoundary(0.99), 1.0)
}

// TestComplexityKeywordConfigStillRejectsMixedFieldsFromFile pins that mixing
// the two spellings in config.json is still an operator mistake. Persisted rows
// never mix them -- each row holds one shape -- so this is the only place the
// rule has to hold.
func TestComplexityKeywordConfigStillRejectsMixedFieldsFromFile(t *testing.T) {
	var keywords ComplexityEditableKeywordConfig
	err := json.Unmarshal([]byte(`{"medium_keywords":["a"],"code_keywords":["b"]}`), &keywords)
	require.ErrorContains(t, err, "cannot mix canonical and legacy fields")
}

// TestPreSemanticRewriteOfAnalyzerRowLeavesSemanticRowIntact is the failure the
// split exists to prevent: an older Bifrost rewriting the analyzer row in its
// own shape must not be able to take the exemplars or the semantic config with
// it.
func TestPreSemanticRewriteOfAnalyzerRowLeavesSemanticRowIntact(t *testing.T) {
	cfg := testSemanticAnalyzerConfig()
	cfg.EmbeddingFingerprint = "fingerprint-1"
	cfg.ConfigHashes.SemanticSettings = "settings-hash"

	semanticRaw, err := encodeComplexitySemanticConfigRow(cfg.Normalized())
	require.NoError(t, err)

	// What an older Bifrost writes back: its own four-list shape, with its own
	// lexical keywords, and no exemplars anywhere because it has no concept of
	// one.
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
	semantic, err := decodeComplexitySemanticConfigRow(semanticRaw)
	require.NoError(t, err)

	combined := applyComplexitySemanticConfigRow(decoded, semantic)
	require.NotNil(t, combined)

	// Every exemplar survives verbatim, because the older binary never had them.
	assert.Equal(t, cfg.Normalized().Keywords, combined.Keywords)
	assert.Equal(t, cfg.Normalized().Semantic, combined.Semantic)
	assert.Equal(t, "settings-hash", combined.ConfigHashes.SemanticSettings)
	assert.Equal(t, "fingerprint-1", combined.EmbeddingFingerprint)

	// Its own edits are respected for the one thing it does own.
	assert.Equal(t, 0.15, combined.TierBoundaries.SimpleMedium)
	assert.Equal(t, 0.35, combined.TierBoundaries.MediumComplex)
}

// TestGetComplexityConfigNeedsSemanticRow pins that an analyzer row on its own
// is not a usable config: it has boundaries but no exemplars, so callers must
// see "nothing configured" and fall back to defaults.
func TestGetComplexityConfigNeedsSemanticRow(t *testing.T) {
	analyzerRaw, err := encodeComplexityAnalyzerConfig(testSemanticAnalyzerConfig().Normalized())
	require.NoError(t, err)

	decoded, err := DecodeComplexityAnalyzerConfig(analyzerRaw)
	require.NoError(t, err)
	require.NotNil(t, decoded)
	assert.Empty(t, decoded.Keywords.SimpleKeywords)

	assert.Nil(t, applyComplexitySemanticConfigRow(decoded, nil))
}

// testDecisionAnalyzerConfig selects the decision model as the primary classifier with no semantic block.
func testDecisionAnalyzerConfig() *ComplexityAnalyzerConfig {
	cfg := testComplexityAnalyzerConfig()
	cfg.Classifier = ComplexityClassifierDecision
	return cfg
}

// TestComplexityDecisionConfigDecoding pins the accepted decision-model JSON shapes: a
// duration string or milliseconds for the timeout, and no unknown fields, so
// a typo in config.json fails loudly instead of silently using defaults.
func TestComplexityDecisionConfigDecoding(t *testing.T) {
	var cfg ComplexityDecisionConfig
	require.NoError(t, json.Unmarshal([]byte(`{"previous_message_count":3,"timeout":"2s"}`), &cfg))
	require.NotNil(t, cfg.PreviousMessageCount)
	assert.Equal(t, 3, *cfg.PreviousMessageCount)
	assert.Equal(t, 2*time.Second, cfg.Timeout)

	cfg = ComplexityDecisionConfig{}
	require.NoError(t, json.Unmarshal([]byte(`{"timeout":750}`), &cfg))
	assert.Equal(t, 750*time.Millisecond, cfg.Timeout)
	assert.Nil(t, cfg.PreviousMessageCount, "an omitted count must stay distinguishable from an explicit 0")

	cfg = ComplexityDecisionConfig{}
	require.NoError(t, json.Unmarshal([]byte(`{"previous_message_count":0}`), &cfg))
	require.NotNil(t, cfg.PreviousMessageCount)
	assert.Equal(t, 0, *cfg.PreviousMessageCount)
	assert.Equal(t, 0, *cfg.normalized().PreviousMessageCount, "an explicit 0 must not be replaced by the default")

	for name, raw := range map[string]string{
		"unknown field":     `{"api_key":"sk-test"}`,
		"negative timeout":  `{"timeout":"-1s"}`,
		"negative millis":   `{"timeout":-5}`,
		"malformed timeout": `{"timeout":"soon"}`,
		"boolean timeout":   `{"timeout":true}`,
	} {
		t.Run(name, func(t *testing.T) {
			var cfg ComplexityDecisionConfig
			require.Error(t, json.Unmarshal([]byte(raw), &cfg))
		})
	}
}

// TestComplexityDecisionConfigMarshalRoundTrip checks that a saved decision-model block reads back unchanged.
func TestComplexityDecisionConfigMarshalRoundTrip(t *testing.T) {
	count := 4
	original := ComplexityDecisionConfig{Provider: "nimble", Model: "nimble-latest", PreviousMessageCount: &count, Timeout: 1200 * time.Millisecond}
	data, err := json.Marshal(original)
	require.NoError(t, err)
	assert.JSONEq(t, `{"provider":"nimble","model":"nimble-latest","previous_message_count":4,"timeout":"1.2s"}`, string(data))

	var decoded ComplexityDecisionConfig
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, original, decoded)
}

// TestComplexityDecisionConfigModelSelection pins how the decision model is chosen:
// both fields empty selects the Typesafe Jev default, a custom provider keeps its
// exact name, a built-in provider is lowercased, and half a selection is rejected.
func TestComplexityDecisionConfigModelSelection(t *testing.T) {
	var cfg ComplexityDecisionConfig
	require.NoError(t, json.Unmarshal([]byte(`{"provider":" Laya ","model":" english "}`), &cfg))
	normalized := cfg.normalized()
	assert.Equal(t, schemas.ModelProvider("Laya"), normalized.Provider, "a custom provider is looked up under its exact name")
	assert.Equal(t, "english", normalized.Model)
	require.NoError(t, normalized.Validate())

	defaulted := (&ComplexityDecisionConfig{}).normalized()
	assert.Equal(t, DefaultComplexityDecisionProvider, defaulted.Provider)
	assert.Equal(t, DefaultComplexityDecisionModel, defaulted.Model)

	builtin := (&ComplexityDecisionConfig{Provider: "TypeSafe", Model: "jev-1.13.0"}).normalized()
	assert.Equal(t, schemas.Typesafe, builtin.Provider)

	for name, partial := range map[string]ComplexityDecisionConfig{
		"provider only": {Provider: "nimble"},
		"model only":    {Model: "nimble-latest"},
		"blank model":   {Provider: "nimble", Model: "  "},
	} {
		t.Run(name, func(t *testing.T) {
			assert.ErrorContains(t, partial.Validate(), "set together")
		})
	}
}

// TestComplexityDecisionConfigValidation pins the history-window bounds shared with the UI.
func TestComplexityDecisionConfigValidation(t *testing.T) {
	for _, count := range []int{0, 1, MaxComplexityDecisionPreviousMessageCount} {
		value := count
		assert.NoError(t, (&ComplexityDecisionConfig{PreviousMessageCount: &value}).Validate(), "count %d", count)
	}
	for _, count := range []int{-1, MaxComplexityDecisionPreviousMessageCount + 1} {
		value := count
		assert.ErrorContains(t, (&ComplexityDecisionConfig{PreviousMessageCount: &value}).Validate(), "previous_message_count", "count %d", count)
	}
	assert.Error(t, (&ComplexityDecisionConfig{Timeout: -time.Millisecond}).Validate())

	cfg := testDecisionAnalyzerConfig()
	outOfRange := MaxComplexityDecisionPreviousMessageCount + 1
	cfg.Decision = &ComplexityDecisionConfig{PreviousMessageCount: &outOfRange}
	normalized := cfg.Normalized()
	assert.ErrorContains(t, normalized.Validate(), "previous_message_count", "an analyzer config must reject an invalid decision block")
}

// TestComplexityAnalyzerConfigNormalizedDecision pins when a decision-model block exists at
// runtime: it is defaulted whenever the decision model can run (primary or semantic
// fallback) and left absent otherwise, and the classifier name is canonical.
func TestComplexityAnalyzerConfigNormalizedDecision(t *testing.T) {
	t.Run("decision primary without block gets defaults", func(t *testing.T) {
		normalized := testDecisionAnalyzerConfig().Normalized()
		require.NotNil(t, normalized.Decision)
		assert.Equal(t, DefaultComplexityDecisionPreviousMessageCount, *normalized.Decision.PreviousMessageCount)
		assert.Equal(t, DefaultComplexityDecisionTimeout, normalized.Decision.Timeout)
		require.NoError(t, normalized.Validate())
	})

	t.Run("semantic fallback decision without block gets defaults", func(t *testing.T) {
		cfg := testSemanticAnalyzerConfig()
		cfg.Semantic.Fallback = ComplexitySemanticFallbackDecision
		normalized := cfg.Normalized()
		require.NotNil(t, normalized.Decision)
		assert.Equal(t, ComplexityClassifierSemantic, normalized.Classifier)
	})

	t.Run("semantic without decision fallback has no block", func(t *testing.T) {
		normalized := testSemanticAnalyzerConfig().Normalized()
		assert.Nil(t, normalized.Decision)
		assert.Equal(t, ComplexityClassifierSemantic, normalized.Classifier, "an omitted classifier defaults to semantic")
	})

	t.Run("classifier name is canonicalized", func(t *testing.T) {
		cfg := testDecisionAnalyzerConfig()
		cfg.Classifier = "  DECISION "
		normalized := cfg.Normalized()
		assert.Equal(t, ComplexityClassifierDecision, normalized.Classifier)
		require.NoError(t, normalized.Validate())
	})

	t.Run("unknown classifier is rejected", func(t *testing.T) {
		cfg := testComplexityAnalyzerConfig()
		cfg.Classifier = "llm"
		normalized := cfg.Normalized()
		assert.ErrorContains(t, normalized.Validate(), "complexity classifier must be")
	})

	t.Run("decision session needs no semantic block", func(t *testing.T) {
		cfg := testDecisionAnalyzerConfig()
		cfg.Session = &ComplexitySessionConfig{Enabled: true}
		normalized := cfg.Normalized()
		require.NoError(t, normalized.Validate())

		cfg.Classifier = ComplexityClassifierSemantic
		normalized = cfg.Normalized()
		assert.ErrorContains(t, normalized.Validate(), "requires a semantic config block")
	})
}

// TestRDBConfigStore_ComplexityDecisionPersistence checks that the classifier
// choice and decision-model block survive a save and reload, including a UI-style save
// that carries no hashes, and that switching back to semantic sticks.
func TestRDBConfigStore_ComplexityDecisionPersistence(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	count := 3
	cfg := testDecisionAnalyzerConfig()
	cfg.Semantic = testSemanticConfig()
	cfg.Decision = &ComplexityDecisionConfig{PreviousMessageCount: &count, Timeout: 900 * time.Millisecond}
	hashes, err := GenerateComplexityAnalyzerConfigHashes(cfg)
	require.NoError(t, err)
	cfg.ConfigHashes = hashes
	require.NoError(t, store.UpdateComplexityAnalyzerConfig(ctx, cfg))

	got, err := store.GetComplexityAnalyzerConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, ComplexityClassifierDecision, got.Classifier)
	require.NotNil(t, got.Decision)
	assert.Equal(t, 3, *got.Decision.PreviousMessageCount)
	assert.Equal(t, 900*time.Millisecond, got.Decision.Timeout)
	require.NotNil(t, got.Semantic, "the dormant semantic block must be kept for switching back")
	assert.Equal(t, hashes.ClassifierSettings, got.ConfigHashes.ClassifierSettings)
	assert.Equal(t, hashes.DecisionSettings, got.ConfigHashes.DecisionSettings)

	// A UI save omits hashes; the file-sync hashes must be carried over.
	uiUpdate := testDecisionAnalyzerConfig()
	uiUpdate.Semantic = testSemanticConfig()
	uiUpdate.Decision = &ComplexityDecisionConfig{PreviousMessageCount: &count, Timeout: 900 * time.Millisecond}
	require.NoError(t, store.UpdateComplexityAnalyzerConfig(ctx, uiUpdate))
	got, err = store.GetComplexityAnalyzerConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, hashes.ClassifierSettings, got.ConfigHashes.ClassifierSettings)
	assert.Equal(t, hashes.DecisionSettings, got.ConfigHashes.DecisionSettings)

	switched := testSemanticAnalyzerConfig()
	switched.Classifier = ComplexityClassifierSemantic
	require.NoError(t, store.UpdateComplexityAnalyzerConfig(ctx, switched))
	got, err = store.GetComplexityAnalyzerConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, ComplexityClassifierSemantic, got.Classifier)

	// Restart with the unchanged config.json: the UI switch must survive the
	// file sync rather than being reverted to the file's classifier.
	merged, err := MergeComplexityAnalyzerConfigByHashes(got, cfg)
	require.NoError(t, err)
	assert.Equal(t, ComplexityClassifierSemantic, merged.Classifier)
}

// TestMergeComplexityAnalyzerConfigByHashesDecision pins config.json sync for the
// classifier choice and decision-model block: an unchanged file keeps UI edits, a
// changed file wins, and a file that says nothing keeps the DB choice.
func TestMergeComplexityAnalyzerConfigByHashesDecision(t *testing.T) {
	withHashes := func(cfg *ComplexityAnalyzerConfig) *ComplexityAnalyzerConfig {
		hashes, err := GenerateComplexityAnalyzerConfigHashes(cfg)
		require.NoError(t, err)
		cfg.ConfigHashes = hashes
		return cfg
	}
	decisionFile := func(count int) *ComplexityAnalyzerConfig {
		cfg := testDecisionAnalyzerConfig()
		cfg.Semantic = testSemanticConfig()
		cfg.Decision = &ComplexityDecisionConfig{PreviousMessageCount: &count}
		return withHashes(cfg)
	}

	t.Run("file switches a semantic DB to decision", func(t *testing.T) {
		base := withHashes(testSemanticAnalyzerConfig())
		merged, err := MergeComplexityAnalyzerConfigByHashes(base, decisionFile(2))
		require.NoError(t, err)
		assert.Equal(t, ComplexityClassifierDecision, merged.Classifier)
		require.NotNil(t, merged.Decision)
		assert.Equal(t, 2, *merged.Decision.PreviousMessageCount)
	})

	t.Run("unchanged file keeps UI edits", func(t *testing.T) {
		base := decisionFile(2)
		// Simulate UI edits persisted after the last file sync.
		base.Classifier = ComplexityClassifierSemantic
		edited := 4
		base.Decision.PreviousMessageCount = &edited

		merged, err := MergeComplexityAnalyzerConfigByHashes(base, decisionFile(2))
		require.NoError(t, err)
		assert.Equal(t, ComplexityClassifierSemantic, merged.Classifier)
		assert.Equal(t, 4, *merged.Decision.PreviousMessageCount)
	})

	t.Run("changed decision block replaces the DB block", func(t *testing.T) {
		merged, err := MergeComplexityAnalyzerConfigByHashes(decisionFile(2), decisionFile(5))
		require.NoError(t, err)
		assert.Equal(t, 5, *merged.Decision.PreviousMessageCount)
	})

	t.Run("file without classifier keeps DB choice", func(t *testing.T) {
		file := withHashes(testSemanticAnalyzerConfig())
		file.Classifier = ""
		merged, err := MergeComplexityAnalyzerConfigByHashes(decisionFile(2), file)
		require.NoError(t, err)
		assert.Equal(t, ComplexityClassifierDecision, merged.Classifier)
		require.NotNil(t, merged.Decision)
		assert.Equal(t, 2, *merged.Decision.PreviousMessageCount)
	})
}

// TestComplexityDecisionConfigMinConfidence pins the decode, normalize, and
// validation path of decision.min_confidence, including that leaving it unset
// changes neither the JSON nor the section hash of an existing config.
func TestComplexityDecisionConfigMinConfidence(t *testing.T) {
	var cfg ComplexityDecisionConfig
	require.NoError(t, json.Unmarshal([]byte(`{"min_confidence":0.75}`), &cfg))
	assert.Equal(t, 0.75, cfg.MinConfidence)
	assert.Equal(t, 0.75, cfg.normalized().MinConfidence, "normalizing must not drop the threshold")

	unset, err := json.Marshal(ComplexityDecisionConfig{Model: "jev-latest"})
	require.NoError(t, err)
	assert.NotContains(t, string(unset), "min_confidence")
	unsetHash, err := hashComplexityValue(ComplexityDecisionConfig{Model: "jev-latest"})
	require.NoError(t, err)
	setHash, err := hashComplexityValue(ComplexityDecisionConfig{Model: "jev-latest", MinConfidence: 0.75})
	require.NoError(t, err)
	assert.NotEqual(t, unsetHash, setHash, "changing the threshold must register as a decision settings change")

	for _, value := range []float64{0, 0.5, 1} {
		assert.NoError(t, (&ComplexityDecisionConfig{MinConfidence: value}).Validate(), "value %v", value)
	}
	for _, value := range []float64{-0.1, 1.1, math.NaN()} {
		assert.ErrorContains(t, (&ComplexityDecisionConfig{MinConfidence: value}).Validate(), "min_confidence", "value %v", value)
	}
}

// TestComplexitySemanticRowDecisionMinConfidenceRollback pins the storage shape:
// the threshold round-trips through the row, but never inside the decision
// block, whose released decoder rejects any field it does not know.
func TestComplexitySemanticRowDecisionMinConfidenceRollback(t *testing.T) {
	config := testDecisionAnalyzerConfig()
	config.Decision = &ComplexityDecisionConfig{Provider: "typesafe", Model: "jev-latest", MinConfidence: 0.6}

	row, err := encodeComplexitySemanticConfigRow(*config)
	require.NoError(t, err)
	assert.Equal(t, 0.6, config.Decision.MinConfidence, "encoding must not mutate the live config")

	var stored struct {
		Decision              map[string]json.RawMessage `json:"decision"`
		DecisionMinConfidence float64                    `json:"decision_min_confidence"`
	}
	require.NoError(t, json.Unmarshal(row, &stored))
	assert.NotContains(t, stored.Decision, "min_confidence", "a released binary would mark the whole row unreadable")
	for field := range stored.Decision {
		assert.Contains(t, []string{"provider", "model", "previous_message_count", "timeout", "criteria"}, field, "a released binary rejects this field")
	}
	assert.Equal(t, 0.6, stored.DecisionMinConfidence)

	decoded, err := decodeComplexitySemanticConfigRow(row)
	require.NoError(t, err)
	combined := applyComplexitySemanticConfigRow(&ComplexityAnalyzerConfig{}, decoded)
	require.NotNil(t, combined.Decision)
	assert.Equal(t, 0.6, combined.Decision.MinConfidence)

	config.Decision.MinConfidence = 0
	row, err = encodeComplexitySemanticConfigRow(*config)
	require.NoError(t, err)
	assert.NotContains(t, string(row), "min_confidence", "an unset threshold must leave the row byte-compatible")
}

// TestRDBConfigStore_ComplexityDecisionMinConfidencePersistence pins the
// threshold through a real save and load.
func TestRDBConfigStore_ComplexityDecisionMinConfidencePersistence(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()
	cfg := testDecisionAnalyzerConfig()
	cfg.Decision = &ComplexityDecisionConfig{MinConfidence: 0.8}
	require.NoError(t, store.UpdateComplexityAnalyzerConfig(ctx, cfg))

	got, err := store.GetComplexityAnalyzerConfig(ctx)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.NotNil(t, got.Decision)
	assert.Equal(t, 0.8, got.Decision.MinConfidence)
}

func testSuggestionsAnalyzerConfig() *ComplexityAnalyzerConfig {
	cfg := testSemanticAnalyzerConfig()
	cfg.Suggestions = &ComplexitySuggestionsConfig{Enabled: true, SampleRate: 0.25, LookbackDays: 14, Provider: "openai", Model: "gpt-5"}
	return cfg
}

func TestComplexitySuggestionsConfigDecoding(t *testing.T) {
	var cfg ComplexitySuggestionsConfig
	require.NoError(t, json.Unmarshal([]byte(`{"enabled":true,"sample_rate":0.5,"lookback_days":3}`), &cfg))
	assert.Equal(t, ComplexitySuggestionsConfig{Enabled: true, SampleRate: 0.5, LookbackDays: 3}, cfg)

	require.ErrorContains(t, json.Unmarshal([]byte(`{"enabled":true,"samplerate":0.5}`), &cfg), `unknown complexity suggestions field "samplerate"`)
	require.ErrorContains(t, json.Unmarshal([]byte(`{"sample_rate":0.5}`), &cfg), "requires enabled")

	// An explicit zero is out of range, not a request for the default: the
	// schema rejects it, and quietly defaulting it would capture at a rate
	// nobody asked for.
	require.ErrorContains(t, json.Unmarshal([]byte(`{"enabled":true,"sample_rate":0}`), &cfg), "sample_rate")
	require.ErrorContains(t, json.Unmarshal([]byte(`{"enabled":true,"lookback_days":0}`), &cfg), "lookback_days")
	require.ErrorContains(t, json.Unmarshal([]byte(`{"enabled":true,"sample_rate":null}`), &cfg), "sample_rate")
	require.ErrorContains(t, json.Unmarshal([]byte(`{"enabled":true,"max_clustered_requests":0}`), &cfg), "max_clustered_requests")
	require.ErrorContains(t, json.Unmarshal([]byte(`{"enabled":true,"min_cluster_requests":0}`), &cfg), "min_cluster_requests")
	require.ErrorContains(t, json.Unmarshal([]byte(`{"enabled":true,"min_cluster_requests":null}`), &cfg), "min_cluster_requests")

	// Omitted fields still take the defaults.
	cfg = ComplexitySuggestionsConfig{}
	require.NoError(t, json.Unmarshal([]byte(`{"enabled":true}`), &cfg))
	assert.Equal(t, ComplexitySuggestionsConfig{Enabled: true}, cfg)
	analyzer := testSemanticAnalyzerConfig()
	analyzer.LLM = testLLMConfig() // the suggestion model when the block names none
	analyzer.Suggestions = &cfg
	normalized := analyzer.Normalized()
	require.NoError(t, normalized.Validate())
	assert.Equal(t, DefaultComplexitySuggestionsSampleRate, normalized.Suggestions.SampleRate)
	assert.Equal(t, DefaultComplexitySuggestionsLookbackDays, normalized.Suggestions.LookbackDays)
}

func TestComplexitySuggestionsConfigDefaultsAndValidation(t *testing.T) {
	cfg := testSemanticAnalyzerConfig()
	cfg.LLM = testLLMConfig() // the suggestion model when the block names none
	cfg.Suggestions = &ComplexitySuggestionsConfig{Enabled: true}
	normalized := cfg.Normalized()
	require.NoError(t, normalized.Validate())
	assert.Equal(t, DefaultComplexitySuggestionsSampleRate, normalized.Suggestions.SampleRate)
	assert.Equal(t, DefaultComplexitySuggestionsLookbackDays, normalized.Suggestions.LookbackDays)
	assert.True(t, normalized.SuggestionsEnabled())

	for _, bad := range []ComplexitySuggestionsConfig{
		{Enabled: true, SampleRate: 1.5},
		{Enabled: true, SampleRate: -0.1},
		{Enabled: true, LookbackDays: MaxComplexitySuggestionsLookbackDays + 1},
		{Enabled: true, LookbackDays: -1},
	} {
		cfg.Suggestions = &bad
		n := cfg.Normalized()
		assert.Error(t, n.Validate(), "%+v should be rejected", bad)
	}

	cfg.Suggestions = nil
	n := cfg.Normalized()
	assert.False(t, n.SuggestionsEnabled())
	require.NoError(t, n.Validate())
}

// The run settings decode strictly, take their defaults when omitted, and are
// refused out of range.
func TestComplexitySuggestionsRunSettings(t *testing.T) {
	var cfg ComplexitySuggestionsConfig
	require.NoError(t, json.Unmarshal([]byte(`{"enabled":true,"schedule":{"every":"1w","start_at":"2026-11-06T04:30:00Z"},`+
		`"max_clustered_requests":5000,"min_cluster_requests":10,"provider":"openai","model":"gpt-5","instructions":"be brief"}`), &cfg))
	start := time.Date(2026, time.November, 6, 4, 30, 0, 0, time.UTC)
	assert.Equal(t, ComplexitySuggestionsConfig{
		Enabled:              true,
		Schedule:             &ComplexitySuggestionsSchedule{Every: "1w", StartAt: &start},
		MaxClusteredRequests: 5000, MinClusterRequests: 10, Provider: "openai", Model: "gpt-5", Instructions: "be brief",
	}, cfg)
	require.ErrorContains(t, json.Unmarshal([]byte(`{"enabled":true,"schedule":{"cron":"0 3 * * *"}}`), &cfg), `unknown complexity suggestions schedule field "cron"`)

	analyzer := testSemanticAnalyzerConfig()
	analyzer.LLM = testLLMConfig() // the suggestion model when the block names none
	analyzer.Suggestions = &ComplexitySuggestionsConfig{Enabled: true, Instructions: "   "}
	n := analyzer.Normalized()
	require.NoError(t, n.Validate())
	assert.Equal(t, DefaultComplexitySuggestionsMaxClusteredRequests, n.Suggestions.MaxClusteredRequests)
	assert.Equal(t, DefaultComplexitySuggestionsMinClusterRequests, n.Suggestions.MinClusterRequests)
	assert.Equal(t, &ComplexitySuggestionsSchedule{Every: "1d"}, n.Suggestions.Schedule, "a missing start stays missing; the default anchor is applied when slots are read")
	assert.Empty(t, n.Suggestions.Instructions, "blank instructions use the shipped ones")

	analyzer.Suggestions = &ComplexitySuggestionsConfig{Enabled: true, Provider: " OpenAI ", Model: "gpt-5"}
	n = analyzer.Normalized()
	assert.Equal(t, schemas.OpenAI, n.Suggestions.Provider, "standard provider names are lowercased like the other blocks'")
	assert.Equal(t, ComplexitySuggestionsSchedule{Every: "1d"}, (*ComplexitySuggestionsConfig)(nil).EffectiveSchedule())
	assert.True(t, (*ComplexitySuggestionsConfig)(nil).EffectiveSchedule().Start().Equal(DefaultComplexitySuggestionsStartAt), "daily at 03:00 UTC by default")

	for name, bad := range map[string]ComplexitySuggestionsConfig{
		"budget too small":       {Enabled: true, MaxClusteredRequests: 10},
		"budget too large":       {Enabled: true, MaxClusteredRequests: MaxComplexitySuggestionsMaxClusteredRequests + 1},
		"cluster minimum of one": {Enabled: true, MinClusterRequests: 1},
		"provider without model": {Enabled: true, Provider: "openai"},
		"model without provider": {Enabled: true, Model: "gpt-5"},
		"instructions too long":  {Enabled: true, Instructions: strings.Repeat("x", MaxComplexitySuggestionsInstructionsChars+1)},
		"hours past a day":       {Enabled: true, Schedule: &ComplexitySuggestionsSchedule{Every: "24h"}},
		"interval too long":      {Enabled: true, Schedule: &ComplexitySuggestionsSchedule{Every: "31d"}},
		"bad unit":               {Enabled: true, Schedule: &ComplexitySuggestionsSchedule{Every: "1m"}},
	} {
		analyzer.Suggestions = &bad
		n := analyzer.Normalized()
		assert.Error(t, n.Validate(), name)
	}
}

// Enabled suggestions capture the semantic classifier's vectors and ask a
// model, so they need the semantic classifier as primary and a model: their
// own provider and model, or the llm block's. Disabled, they need neither.
func TestComplexitySuggestionsRequireSemanticClassifierAndModel(t *testing.T) {
	validate := func(mutate func(*ComplexityAnalyzerConfig)) error {
		cfg := testSemanticAnalyzerConfig()
		mutate(cfg)
		n := cfg.Normalized()
		return n.Validate()
	}
	own := func() *ComplexitySuggestionsConfig {
		return &ComplexitySuggestionsConfig{Enabled: true, Provider: "openai", Model: "gpt-5"}
	}

	require.NoError(t, validate(func(c *ComplexityAnalyzerConfig) { c.Suggestions = own() }), "their own model")
	require.NoError(t, validate(func(c *ComplexityAnalyzerConfig) {
		c.Suggestions, c.LLM = &ComplexitySuggestionsConfig{Enabled: true}, testLLMConfig()
	}), "the llm block's model")

	assert.ErrorContains(t, validate(func(c *ComplexityAnalyzerConfig) {
		c.Suggestions = &ComplexitySuggestionsConfig{Enabled: true}
	}), "need a model")
	for name, partial := range map[string]*ComplexitySuggestionsConfig{
		"provider only": {Enabled: true, Provider: "openai"},
		"model only":    {Enabled: true, Model: "gpt-5"},
	} {
		assert.ErrorContains(t, validate(func(c *ComplexityAnalyzerConfig) { c.Suggestions = partial }), "set together", name)
	}

	assert.ErrorContains(t, validate(func(c *ComplexityAnalyzerConfig) {
		c.Semantic, c.Suggestions = nil, own()
	}), "require the semantic classifier", "no semantic block")
	assert.ErrorContains(t, validate(func(c *ComplexityAnalyzerConfig) {
		c.Classifier, c.Decision, c.Suggestions = ComplexityClassifierDecision, &ComplexityDecisionConfig{}, own()
	}), "require the semantic classifier", "decision primary")

	// Disabled suggestions keep their settings without either.
	require.NoError(t, validate(func(c *ComplexityAnalyzerConfig) {
		c.Semantic, c.Suggestions = nil, &ComplexitySuggestionsConfig{Enabled: false}
	}))
	require.NoError(t, validate(func(c *ComplexityAnalyzerConfig) {
		c.Suggestions = &ComplexitySuggestionsConfig{Enabled: false}
	}))
}

// The suggestions block rides the semantic row as a top-level key, which the
// row record decodes leniently; it must never land inside the strictly
// decoded semantic block, or a rolled-back binary loses the whole row.
func TestComplexitySuggestionsPersistOutsideTheStrictSemanticBlock(t *testing.T) {
	cfg := testSuggestionsAnalyzerConfig()
	data, err := encodeComplexitySemanticConfigRow(cfg.Normalized())
	require.NoError(t, err)

	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &raw))
	require.Contains(t, raw, "suggestions")
	var semantic map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw["semantic"], &semantic))
	assert.NotContains(t, semantic, "suggestions")

	// A binary predating suggestions decodes the row with this exact record
	// minus the field; unknown top-level keys must not fail it.
	type preSuggestionsRecord struct {
		Keywords ComplexityEditableKeywordConfig `json:"keywords"`
		Semantic *ComplexitySemanticConfig       `json:"semantic,omitempty"`
	}
	var old preSuggestionsRecord
	require.NoError(t, json.Unmarshal(data, &old))
	require.NotNil(t, old.Semantic)

	row, err := decodeComplexitySemanticConfigRow(data)
	require.NoError(t, err)
	combined := applyComplexitySemanticConfigRow(&ComplexityAnalyzerConfig{TierBoundaries: cfg.TierBoundaries}, row)
	assert.Equal(t, cfg.Normalized().Suggestions, combined.Suggestions)
}

func TestGenerateComplexityAnalyzerConfigHashesSuggestions(t *testing.T) {
	on, err := GenerateComplexityAnalyzerConfigHashes(testSuggestionsAnalyzerConfig())
	require.NoError(t, err)
	require.NotEmpty(t, on.SuggestionsSettings)

	changed := testSuggestionsAnalyzerConfig()
	changed.Suggestions.SampleRate = 0.5
	changedHashes, err := GenerateComplexityAnalyzerConfigHashes(changed)
	require.NoError(t, err)
	assert.NotEqual(t, on.SuggestionsSettings, changedHashes.SuggestionsSettings)
	assert.Equal(t, on.SemanticSettings, changedHashes.SemanticSettings)

	none, err := GenerateComplexityAnalyzerConfigHashes(testSemanticAnalyzerConfig())
	require.NoError(t, err)
	assert.Empty(t, none.SuggestionsSettings)
}

func TestMergeComplexityAnalyzerConfigByHashesSuggestions(t *testing.T) {
	withHashes := func(cfg *ComplexityAnalyzerConfig) *ComplexityAnalyzerConfig {
		hashes, err := GenerateComplexityAnalyzerConfigHashes(cfg)
		require.NoError(t, err)
		cfg.ConfigHashes = hashes
		return cfg
	}

	t.Run("file omission preserves persisted suggestions", func(t *testing.T) {
		base := withHashes(testSuggestionsAnalyzerConfig())
		merged, err := MergeComplexityAnalyzerConfigByHashes(base, withHashes(testSemanticAnalyzerConfig()))
		require.NoError(t, err)
		require.NotNil(t, merged.Suggestions)
		assert.Equal(t, 0.25, merged.Suggestions.SampleRate)
	})

	t.Run("changed file section overrides", func(t *testing.T) {
		base := withHashes(testSuggestionsAnalyzerConfig())
		file := testSuggestionsAnalyzerConfig()
		file.Suggestions.Enabled = false
		withHashes(file)
		merged, err := MergeComplexityAnalyzerConfigByHashes(base, file)
		require.NoError(t, err)
		assert.False(t, merged.Suggestions.Enabled)
		assert.Equal(t, file.ConfigHashes.SuggestionsSettings, merged.ConfigHashes.SuggestionsSettings)
	})

	t.Run("unchanged file section keeps a UI edit", func(t *testing.T) {
		file := withHashes(testSuggestionsAnalyzerConfig())
		base := testSuggestionsAnalyzerConfig()
		base.Suggestions.SampleRate = 0.9 // edited in the UI after config.json was applied
		base.ConfigHashes = file.ConfigHashes
		merged, err := MergeComplexityAnalyzerConfigByHashes(base, file)
		require.NoError(t, err)
		assert.Equal(t, 0.9, merged.Suggestions.SampleRate)
	})
}

func TestRDBConfigStore_ComplexitySuggestionsPersistenceAndReset(t *testing.T) {
	store := setupRDBTestStore(t)
	ctx := context.Background()

	cfg := testSuggestionsAnalyzerConfig()
	hashes, err := GenerateComplexityAnalyzerConfigHashes(cfg)
	require.NoError(t, err)
	cfg.ConfigHashes = hashes
	require.NoError(t, store.UpdateComplexityAnalyzerConfig(ctx, cfg))

	got, err := store.GetComplexityAnalyzerConfig(ctx)
	require.NoError(t, err)
	require.NotNil(t, got.Suggestions)
	assert.Equal(t, *testSuggestionsAnalyzerConfig().Normalized().Suggestions, *got.Suggestions, "stored with defaults filled in")
	assert.Equal(t, DefaultComplexitySuggestionsMaxClusteredRequests, got.Suggestions.MaxClusteredRequests)
	assert.Equal(t, hashes.SuggestionsSettings, got.ConfigHashes.SuggestionsSettings)

	// A UI-style update omits hashes; the carry-over keeps them with the block.
	require.NoError(t, store.UpdateComplexityAnalyzerConfig(ctx, testSuggestionsAnalyzerConfig()))
	got, err = store.GetComplexityAnalyzerConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, hashes.SuggestionsSettings, got.ConfigHashes.SuggestionsSettings)

	// Resetting phrases and boundaries leaves suggestions as they were.
	restored, err := store.ResetComplexityAnalyzerConfig(ctx, testComplexityAnalyzerConfig())
	require.NoError(t, err)
	require.NotNil(t, restored.Suggestions)
	assert.True(t, restored.Suggestions.Enabled)
}
