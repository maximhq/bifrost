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
