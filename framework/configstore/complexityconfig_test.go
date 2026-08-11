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

func testSessionConfig() *ComplexitySessionConfig {
	return &ComplexitySessionConfig{Mode: ComplexitySessionModePinned}
}

func TestComplexitySessionConfigTTLDecoding(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    time.Duration
		wantErr bool
	}{
		{name: "duration string", payload: `{"ttl":"30m"}`, want: 30 * time.Minute},
		{name: "number is milliseconds", payload: `{"ttl":250}`, want: 250 * time.Millisecond},
		{name: "absent keeps zero", payload: `{}`, want: 0},
		{name: "null keeps zero", payload: `{"ttl":null}`, want: 0},
		{name: "negative number rejected", payload: `{"ttl":-5}`, wantErr: true},
		{name: "bad string rejected", payload: `{"ttl":"a while"}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg ComplexitySessionConfig
			err := json.Unmarshal([]byte(tt.payload), &cfg)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, cfg.TTL)
		})
	}
}

// A default-encoded time.Duration is nanoseconds, which the millisecond decode
// path would misread by six orders of magnitude. The string encoding is what
// keeps a persisted TTL meaning what it said.
func TestComplexitySessionConfigTTLMarshalRoundTrip(t *testing.T) {
	cfg := testSessionConfig()
	cfg.TTL = 30 * time.Minute

	data, err := json.Marshal(cfg)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"ttl":"30m0s"`)

	var decoded ComplexitySessionConfig
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, cfg.TTL, decoded.TTL)
}

func TestComplexitySessionConfigNormalizedDefaults(t *testing.T) {
	normalized := testSessionConfig().normalized()

	assert.Equal(t, DefaultComplexitySessionTTL, normalized.TTL)
	assert.Equal(t, DefaultComplexitySessionIdentitySources(), normalized.IdentitySources)
	assert.Equal(t, DefaultComplexitySessionDowngradeAfterNTurns, normalized.DowngradeAfterNTurns)
	assert.Equal(t, DefaultComplexitySessionMinCachedTokensToHold, normalized.MinCachedTokensToHold)
	// Ships unset: no sensible absolute default exists until the exemplar set's
	// score distribution is measured.
	assert.Zero(t, normalized.SwitchMinSimilarity)
	assert.Zero(t, normalized.MaxSwitchesPerSession)
	require.NoError(t, normalized.Validate())
}

func TestComplexitySessionConfigEnabled(t *testing.T) {
	var nilCfg *ComplexitySessionConfig
	assert.False(t, nilCfg.Enabled())
	assert.False(t, (&ComplexitySessionConfig{}).Enabled())
	assert.False(t, (&ComplexitySessionConfig{Mode: ComplexitySessionModeOff}).Enabled())
	assert.True(t, (&ComplexitySessionConfig{Mode: ComplexitySessionModePinned}).Enabled())
	assert.True(t, (&ComplexitySessionConfig{Mode: ComplexitySessionModeCacheAware}).Enabled())
}

// Turning session behaviour off must not discard the tuning an admin did, so
// the toggle carries a mode rather than nilling the block.
func TestComplexitySessionConfigOffPreservesSettings(t *testing.T) {
	cfg := &ComplexitySessionConfig{
		Mode:                 ComplexitySessionModeOff,
		TTL:                  90 * time.Minute,
		DowngradeAfterNTurns: 5,
	}
	normalized := cfg.normalized()

	assert.False(t, normalized.Enabled())
	assert.Equal(t, 90*time.Minute, normalized.TTL)
	assert.Equal(t, 5, normalized.DowngradeAfterNTurns)
}

func TestComplexitySessionConfigIdentitySourceNormalization(t *testing.T) {
	cfg := &ComplexitySessionConfig{
		Mode:            ComplexitySessionModePinned,
		IdentitySources: []string{" HARNESS ", "header", "header", ""},
	}
	normalized := cfg.normalized()

	// De-duplicated, and reordered into resolution order regardless of input order.
	assert.Equal(t, []string{ComplexitySessionIdentityHeader, ComplexitySessionIdentityHarness}, normalized.IdentitySources)
	require.NoError(t, normalized.Validate())
}

// Unsupported sources are preserved through normalization so validation can
// name them rather than silently dropping stale or misspelled configuration.
func TestComplexitySessionConfigRejectsUnknownIdentitySource(t *testing.T) {
	for _, source := range []string{"fingerprint", "telepathy"} {
		t.Run(source, func(t *testing.T) {
			cfg := &ComplexitySessionConfig{
				Mode:            ComplexitySessionModePinned,
				IdentitySources: []string{"header", source},
			}
			err := cfg.normalized().Validate()

			require.Error(t, err)
			assert.Contains(t, err.Error(), source)
		})
	}
}

func TestComplexitySessionConfigValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ComplexitySessionConfig)
		errs   string
	}{
		{name: "valid pinned"},
		{name: "unknown mode", mutate: func(c *ComplexitySessionConfig) { c.Mode = "adaptive" }, errs: "session mode"},
		{name: "switch similarity at ceiling", mutate: func(c *ComplexitySessionConfig) { c.SwitchMinSimilarity = 1 }, errs: "switch_min_similarity"},
		{name: "negative switch similarity", mutate: func(c *ComplexitySessionConfig) { c.SwitchMinSimilarity = -0.1 }, errs: "switch_min_similarity"},
		{name: "negative max switches", mutate: func(c *ComplexitySessionConfig) { c.MaxSwitchesPerSession = -1 }, errs: "max_switches_per_session"},
		{name: "negative min cached tokens", mutate: func(c *ComplexitySessionConfig) { c.MinCachedTokensToHold = -1 }, errs: "min_cached_tokens_to_hold"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testSessionConfig()
			if tt.mutate != nil {
				tt.mutate(cfg)
			}
			normalized := cfg.normalized()
			// normalized must not launder an invalid value into a default.
			if tt.mutate != nil {
				tt.mutate(normalized)
			}
			err := normalized.Validate()
			if tt.errs == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.errs)
		})
	}
}

// Hysteresis: a classification too weak to be believed must not be strong
// enough to move a session.
func TestComplexityAnalyzerConfigRejectsSwitchSimilarityBelowMinSimilarity(t *testing.T) {
	cfg := testSemanticAnalyzerConfig()
	cfg.Semantic.MinSimilarity = 0.80
	cfg.Session = testSessionConfig()
	cfg.Session.SwitchMinSimilarity = 0.70

	normalized := cfg.Normalized()
	err := normalized.Validate()

	require.Error(t, err)
	assert.Contains(t, err.Error(), "switch_min_similarity")

	normalized.Session.SwitchMinSimilarity = 0.80
	require.NoError(t, normalized.Validate())
}

// Zero means the switch gate is off entirely, not a threshold of zero, so it is
// exempt from the ordering rule.
func TestComplexityAnalyzerConfigAllowsUnsetSwitchSimilarity(t *testing.T) {
	cfg := testSemanticAnalyzerConfig()
	cfg.Semantic.MinSimilarity = 0.80
	cfg.Session = testSessionConfig()

	normalized := cfg.Normalized()
	require.NoError(t, normalized.Validate())
	assert.Zero(t, normalized.Session.SwitchMinSimilarity)
}

// The record mirror is a separate struct from the public config; a field added
// to one and not the other round-trips as nothing.
func TestComplexityAnalyzerConfigSessionRoundTrip(t *testing.T) {
	cfg := testSemanticAnalyzerConfig()
	cfg.Session = &ComplexitySessionConfig{
		Mode:                  ComplexitySessionModeCacheAware,
		TTL:                   90 * time.Minute,
		IdentitySources:       []string{ComplexitySessionIdentityHeader},
		SwitchMinSimilarity:   0.9,
		MaxSwitchesPerSession: 4,
		AlwaysAllowEscalation: true,
	}

	encoded, err := encodeComplexityAnalyzerConfig(cfg.Normalized())
	require.NoError(t, err)

	decoded, err := DecodeComplexityAnalyzerConfig(encoded)
	require.NoError(t, err)
	require.NotNil(t, decoded.Session)
	assert.Equal(t, cfg.Session.Mode, decoded.Session.Mode)
	assert.Equal(t, cfg.Session.TTL, decoded.Session.TTL)
	assert.Equal(t, cfg.Session.IdentitySources, decoded.Session.IdentitySources)
	assert.Equal(t, cfg.Session.SwitchMinSimilarity, decoded.Session.SwitchMinSimilarity)
	assert.Equal(t, cfg.Session.MaxSwitchesPerSession, decoded.Session.MaxSwitchesPerSession)
	assert.True(t, decoded.Session.AlwaysAllowEscalation)
}

// A config.json without a session block means "no opinion", not removal.
func TestMergeComplexityAnalyzerConfigByHashesKeepsSessionWhenFileOmitsIt(t *testing.T) {
	base := testSemanticAnalyzerConfig()
	base.Session = testSessionConfig()
	base.ConfigHashes.SessionSettings = "hash-a"

	file := testSemanticAnalyzerConfig()

	merged, err := MergeComplexityAnalyzerConfigByHashes(base, file)
	require.NoError(t, err)
	require.NotNil(t, merged.Session)
	assert.Equal(t, ComplexitySessionModePinned, merged.Session.Mode)
	assert.Equal(t, "hash-a", merged.ConfigHashes.SessionSettings)
}

func TestMergeComplexityAnalyzerConfigByHashesAppliesChangedSessionSection(t *testing.T) {
	base := testSemanticAnalyzerConfig()
	base.Session = testSessionConfig()
	base.ConfigHashes.SessionSettings = "hash-a"

	file := testSemanticAnalyzerConfig()
	file.Session = &ComplexitySessionConfig{Mode: ComplexitySessionModeCacheAware}
	file.ConfigHashes.SessionSettings = "hash-b"

	merged, err := MergeComplexityAnalyzerConfigByHashes(base, file)
	require.NoError(t, err)
	require.NotNil(t, merged.Session)
	assert.Equal(t, ComplexitySessionModeCacheAware, merged.Session.Mode)
	assert.Equal(t, "hash-b", merged.ConfigHashes.SessionSettings)
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
