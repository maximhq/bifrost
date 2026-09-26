package configstore

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testJevAnalyzerConfig() *ComplexityAnalyzerConfig {
	cfg := testSemanticAnalyzerConfig()
	cfg.Semantic.Fallback = ComplexitySemanticFallbackJev
	cfg.Jev = &ComplexityJevConfig{}
	return cfg
}

func TestComplexitySemanticFallbackAcceptsJev(t *testing.T) {
	cfg := testJevAnalyzerConfig()
	normalized := cfg.Normalized()
	require.NoError(t, normalized.Validate())
	assert.Equal(t, ComplexitySemanticFallbackJev, normalized.Semantic.Fallback)
}

func TestComplexityJevFallbackRequiresJevBlock(t *testing.T) {
	cfg := testJevAnalyzerConfig()
	normalized := cfg.Normalized()
	normalized.Jev = nil
	err := normalized.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires a jev config block")
}

func TestComplexityJevConfigNormalizedDefaults(t *testing.T) {
	normalized := (&ComplexityJevConfig{}).normalized()
	require.NotNil(t, normalized)
	assert.Equal(t, DefaultComplexityJevBaseURL, normalized.BaseURL)
	assert.Equal(t, DefaultComplexityJevModel, normalized.Model)
	assert.Equal(t, DefaultComplexityJevTimeout, normalized.Timeout)
	assert.InDelta(t, DefaultJevMinConfidenceToDegrade, normalized.MinConfidenceToDegrade, 1e-9)
	assert.InDelta(t, DefaultJevMaxComplexityForDegrade, normalized.MaxComplexityForDegrade, 1e-9)
	assert.InDelta(t, DefaultJevMinComplexityConfidence, normalized.MinComplexityConfidence, 1e-9)
	assert.Equal(t, DefaultComplexityJevMessageHistoryCount, normalized.MessageHistoryCount)
}

func TestComplexityJevConfigTimeoutDecoding(t *testing.T) {
	var asString ComplexityJevConfig
	require.NoError(t, json.Unmarshal([]byte(`{"timeout":"2s"}`), &asString))
	assert.Equal(t, 2*time.Second, asString.Timeout)

	var asNumber ComplexityJevConfig
	require.NoError(t, json.Unmarshal([]byte(`{"timeout":1500}`), &asNumber))
	assert.Equal(t, 1500*time.Millisecond, asNumber.Timeout)

	var negative ComplexityJevConfig
	err := json.Unmarshal([]byte(`{"timeout":"-1s"}`), &negative)
	require.Error(t, err)
}

func TestComplexityJevConfigRejectsUnknownFields(t *testing.T) {
	var cfg ComplexityJevConfig
	err := json.Unmarshal([]byte(`{"model":"jev-latest","prompt":"sneaky"}`), &cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown jev complexity field")
}

func TestComplexityJevConfigMarshalRoundTrip(t *testing.T) {
	cfg := ComplexityJevConfig{Timeout: 2 * time.Second, Model: "jev-latest"}
	data, err := json.Marshal(&cfg)
	require.NoError(t, err)

	var decoded ComplexityJevConfig
	require.NoError(t, json.Unmarshal(data, &decoded))
	// Gate fields always marshal (no omitempty) so an explicitly configured
	// zero survives persistence. Decoding those emitted zeros marks the gates
	// explicitly present, which is presence metadata, not a value change, so
	// align the flags before comparing values.
	decoded.gatesExplicitInJSON = cfg.gatesExplicitInJSON
	assert.Equal(t, cfg, decoded)
}

func TestComplexityJevConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*ComplexityJevConfig)
		wantErr string
	}{
		{
			name:   "defaults are valid",
			mutate: func(*ComplexityJevConfig) {},
		},
		{
			name:    "timeout must be positive",
			mutate:  func(c *ComplexityJevConfig) { c.Timeout = 0 },
			wantErr: "timeout must be positive",
		},
		{
			name:    "base_url required",
			mutate:  func(c *ComplexityJevConfig) { c.BaseURL = " " },
			wantErr: "requires a base_url",
		},
		{
			name:    "model required",
			mutate:  func(c *ComplexityJevConfig) { c.Model = "" },
			wantErr: "requires a model",
		},
		{
			name:    "gate threshold above one rejected",
			mutate:  func(c *ComplexityJevConfig) { c.MinConfidenceToDegrade = 1.5 },
			wantErr: "min_confidence_to_degrade",
		},
		{
			name:    "negative gate threshold rejected",
			mutate:  func(c *ComplexityJevConfig) { c.MaxComplexityForDegrade = -0.1 },
			wantErr: "max_complexity_for_degrade",
		},
		{
			name:    "message_history_count bounded",
			mutate:  func(c *ComplexityJevConfig) { c.MessageHistoryCount = 11 },
			wantErr: "message_history_count",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := (&ComplexityJevConfig{}).normalized()
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

func TestComplexityJevFallbackEnabled(t *testing.T) {
	// selector + block → enabled.
	assert.True(t, testJevAnalyzerConfig().JevFallbackEnabled())

	// block retained but selector on none → not enabled.
	retained := testJevAnalyzerConfig()
	retained.Semantic.Fallback = ComplexitySemanticFallbackNone
	assert.False(t, retained.JevFallbackEnabled())

	// selector on jev but block missing → not enabled (Validate rejects this
	// shape, but the predicate itself must not claim enabled).
	orphan := testJevAnalyzerConfig()
	orphan.Jev = nil
	assert.False(t, orphan.JevFallbackEnabled())
}

func TestComplexityJevConfigSurvivesAnalyzerNormalize(t *testing.T) {
	// A dormant jev block with the fallback on "none" must survive
	// normalization so toggling the fallback never loses settings.
	cfg := testJevAnalyzerConfig()
	cfg.Semantic.Fallback = ComplexitySemanticFallbackNone
	normalized := cfg.Normalized()
	require.NotNil(t, normalized.Jev)
	require.NoError(t, normalized.Validate())
}

func TestComplexityJevBlockRidesSemanticRow(t *testing.T) {
	// Encode a config with a jev block into the semantic row, decode it back,
	// and verify the block and its section hash survive the round trip.
	cfg := testJevAnalyzerConfig()
	normalized := cfg.Normalized()
	normalized.ConfigHashes = ComplexityAnalyzerConfigHashes{JevSettings: "jevhash"}

	row, err := encodeComplexitySemanticConfigRow(normalized)
	require.NoError(t, err)
	decoded, err := decodeComplexitySemanticConfigRow(row)
	require.NoError(t, err)
	require.NotNil(t, decoded.Jev)
	assert.Equal(t, DefaultComplexityJevModel, decoded.Jev.Model)
	assert.Equal(t, "jevhash", decoded.ConfigHashes.JevSettings)

	combined := applyComplexitySemanticConfigRow(&ComplexityAnalyzerConfig{TierBoundaries: DefaultComplexityTierBoundaries()}, decoded)
	require.NotNil(t, combined.Jev)
	assert.Equal(t, "jevhash", combined.ConfigHashes.JevSettings)
}

func TestMergeComplexityAnalyzerConfigPreservesJevBlock(t *testing.T) {
	// Regression: the non-hash merge path builds its result as a struct
	// literal and used to omit Jev, so any file-level complexity update
	// silently dropped the base's Jev block — failing validation when
	// semantic.fallback was "jev" and discarding a dormant block otherwise.
	base := testJevAnalyzerConfig().Normalized()

	// A file that never mentions the semantic or jev sections must not undo
	// the fallback wiring: the merge still validates and keeps Jev.
	file := testComplexityAnalyzerConfig()
	merged, err := MergeComplexityAnalyzerConfig(&base, file)
	require.NoError(t, err)
	require.NotNil(t, merged.Jev)
	assert.Equal(t, ComplexitySemanticFallbackJev, merged.Semantic.Fallback)
	assert.True(t, merged.JevFallbackEnabled())
	assert.Equal(t, DefaultComplexityJevBaseURL, merged.Jev.BaseURL)

	// A dormant Jev block (fallback "none") is also preserved, not discarded.
	dormant := testJevAnalyzerConfig()
	dormant.Semantic.Fallback = ComplexitySemanticFallbackNone
	dormant.Jev = &ComplexityJevConfig{Model: "jev-rc"}
	dormantMerged, err := MergeComplexityAnalyzerConfig(dormant, file)
	require.NoError(t, err)
	require.NotNil(t, dormantMerged.Jev)
	assert.Equal(t, "jev-rc", dormantMerged.Jev.Model)
	assert.False(t, dormantMerged.JevFallbackEnabled())

	// A file that states the jev section replaces the base's.
	fileWithJev := testComplexityAnalyzerConfig()
	fileWithJev.Jev = &ComplexityJevConfig{Timeout: 3 * time.Second}
	merged, err = MergeComplexityAnalyzerConfig(&base, fileWithJev)
	require.NoError(t, err)
	require.NotNil(t, merged.Jev)
	assert.Equal(t, 3*time.Second, merged.Jev.Timeout)
	assert.True(t, merged.JevFallbackEnabled())
}

func TestComplexityJevConfigExplicitZeroGatesPreserved(t *testing.T) {
	// The config schema admits 0 for all three gates, and 0 is a real
	// setting (e.g. publish the tier regardless of choice confidence):
	// normalization must preserve an explicit 0 instead of swapping in the
	// defaults.
	var cfg ComplexityJevConfig
	require.NoError(t, json.Unmarshal([]byte(`{
		"min_confidence_to_degrade": 0,
		"max_complexity_for_degrade": 0,
		"min_complexity_confidence": 0
	}`), &cfg))
	normalized := cfg.normalized()
	require.NotNil(t, normalized)
	assert.InDelta(t, 0, normalized.MinConfidenceToDegrade, 1e-9)
	assert.InDelta(t, 0, normalized.MaxComplexityForDegrade, 1e-9)
	assert.InDelta(t, 0, normalized.MinComplexityConfidence, 1e-9)
	require.NoError(t, normalized.Validate())

	// Absent fields still resolve to the defaults.
	var absent ComplexityJevConfig
	require.NoError(t, json.Unmarshal([]byte(`{}`), &absent))
	absentNormalized := absent.normalized()
	assert.InDelta(t, DefaultJevMinConfidenceToDegrade, absentNormalized.MinConfidenceToDegrade, 1e-9)
	assert.InDelta(t, DefaultJevMaxComplexityForDegrade, absentNormalized.MaxComplexityForDegrade, 1e-9)
	assert.InDelta(t, DefaultJevMinComplexityConfidence, absentNormalized.MinComplexityConfidence, 1e-9)

	// An explicit zero survives persistence: the gates marshal without
	// omitempty, so a reload restores the same gates instead of defaults.
	data, err := json.Marshal(normalized)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"min_confidence_to_degrade":0`)
	var reloaded ComplexityJevConfig
	require.NoError(t, json.Unmarshal(data, &reloaded))
	reloadedNormalized := reloaded.normalized()
	assert.InDelta(t, 0, reloadedNormalized.MinConfidenceToDegrade, 1e-9)
	assert.InDelta(t, 0, reloadedNormalized.MaxComplexityForDegrade, 1e-9)
	assert.InDelta(t, 0, reloadedNormalized.MinComplexityConfidence, 1e-9)
}
