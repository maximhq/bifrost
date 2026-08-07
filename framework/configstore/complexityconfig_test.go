package configstore

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

func TestComplexitySemanticConfigRejectsRemovedFields(t *testing.T) {
	for _, field := range []string{"dimension", "fallback"} {
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
	assert.Equal(t, ComplexitySemanticFallbackLexical, normalized.Fallback)
	assert.Equal(t, ComplexitySemanticVectorStoreEmbedded, normalized.VectorStore)
	require.NoError(t, normalized.Validate())
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

func TestComplexityAnalyzerConfigRejectsSemanticCrossTierDuplicates(t *testing.T) {
	cfg := testSemanticAnalyzerConfig()
	cfg.Keywords.SimpleKeywords = []string{"Shared   phrase", "simple-only"}
	cfg.Keywords.MediumKeywords = []string{"shared phrase", "medium-only"}

	raw, err := encodeComplexityAnalyzerConfig(*cfg)
	require.NoError(t, err)
	_, err = DecodeComplexityAnalyzerConfig(raw)
	require.ErrorContains(t, err, `semantic phrase "shared phrase" appears in both simple_keywords and medium_keywords`)
}

func TestComplexityAnalyzerConfigSemanticPhraseValidation(t *testing.T) {
	t.Run("allows more than 500 phrases", func(t *testing.T) {
		cfg := testSemanticAnalyzerConfig()
		cfg.Keywords.SimpleKeywords = make([]string, 501)
		for index := range cfg.Keywords.SimpleKeywords {
			cfg.Keywords.SimpleKeywords[index] = fmt.Sprintf("simple-%d", index)
		}
		cfg.Keywords.MediumKeywords = []string{"medium"}
		cfg.Keywords.ComplexKeywords = []string{"complex"}

		normalized := cfg.Normalized()
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
	cfg.ConfigHashes = ComplexityAnalyzerConfigHashes{
		TierBoundaries:   "tier-hash",
		SimpleKeywords:   "simple-hash",
		MediumKeywords:   "medium-hash",
		ComplexKeywords:  "complex-hash",
		SemanticSettings: "settings-hash",
	}
	cfg.EmbeddingFingerprint = "fingerprint-1"

	raw, err := encodeComplexityAnalyzerConfig(cfg.Normalized())
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"_embedding_fingerprint":"fingerprint-1"`)

	decoded, err := DecodeComplexityAnalyzerConfig(raw)
	require.NoError(t, err)
	require.NotNil(t, decoded.Semantic)
	assert.Equal(t, cfg.Normalized().Semantic, decoded.Semantic)
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
		assert.Equal(t, ComplexitySemanticFallbackLexical, merged.Semantic.Fallback)
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
