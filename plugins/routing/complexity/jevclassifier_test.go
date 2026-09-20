package complexity

import (
	"context"
	"errors"
	"testing"
	"unicode/utf8"

	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testJevFallbackAnalyzerConfig() *AnalyzerConfig {
	cfg := DefaultAnalyzerConfig()
	cfg.Semantic = &SemanticConfig{
		Provider:       "openai",
		EmbeddingModel: "text-embedding-3-small",
		Fallback:       configstore.ComplexitySemanticFallbackJev,
	}
	cfg.Jev = &JevConfig{}
	return &cfg
}

// stubSystemOne returns a SystemOneFunc answering from the given fixture maps.
func stubSystemOne(answers map[string]SystemOneAnswer, err error) SystemOneFunc {
	return func(_ context.Context, _ *JevConfig, _ *SystemOneRequest) (*SystemOneResponse, error) {
		if err != nil {
			return nil, err
		}
		return &SystemOneResponse{Answers: answers}, nil
	}
}

func confidentTierAnswer(tier string) SystemOneAnswer {
	return SystemOneAnswer{Type: SystemOneQuestionChoice, Choice: tier, Confidence: 0.95}
}

func hardComplexityAnswer() SystemOneAnswer {
	score := 0.9
	return SystemOneAnswer{Type: SystemOneQuestionScore, Score: &score, Confidence: 0.9}
}

func simpleComplexityAnswer() SystemOneAnswer {
	score := 0.1
	return SystemOneAnswer{Type: SystemOneQuestionScore, Score: &score, Confidence: 0.9}
}

func newWiredJevClassifier(t *testing.T, config *AnalyzerConfig, fn SystemOneFunc) *JevClassifier {
	t.Helper()
	classifier := NewJevClassifier(nil)
	classifier.Configure(config)
	classifier.SetSystemOneFunc(fn)
	return classifier
}

func TestJevClassifierPublishesConfidentTier(t *testing.T) {
	classifier := newWiredJevClassifier(t, testJevFallbackAnalyzerConfig(), stubSystemOne(map[string]SystemOneAnswer{
		"tier":       confidentTierAnswer(TierSimple),
		"complexity": simpleComplexityAnswer(),
	}, nil))

	result, err := classifier.Classify(context.Background(), ComplexityInput{LastUserText: "what is 2+2?"})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, TierSimple, result.Tier)
	assert.Equal(t, JevReasonConfident, result.Reason)
	assert.InDelta(t, 0.1, result.Complexity, 1e-9)
	assert.InDelta(t, 0.95, result.Confidence, 1e-9)
}

func TestJevClassifierGates(t *testing.T) {
	tests := []struct {
		name         string
		answers      map[string]SystemOneAnswer
		wantReason   string
		wantEmptTier bool
	}{
		{
			name: "low tier confidence withholds tier",
			answers: map[string]SystemOneAnswer{
				"tier":       {Type: SystemOneQuestionChoice, Choice: TierSimple, Confidence: 0.4},
				"complexity": simpleComplexityAnswer(),
			},
			wantReason:   JevReasonLowConfidence,
			wantEmptTier: true,
		},
		{
			name: "high complexity withholds tier",
			answers: map[string]SystemOneAnswer{
				"tier":       confidentTierAnswer(TierSimple),
				"complexity": hardComplexityAnswer(),
			},
			wantReason:   JevReasonHighComplexity,
			wantEmptTier: true,
		},
		{
			name: "low complexity confidence withholds tier",
			answers: map[string]SystemOneAnswer{
				"tier":       confidentTierAnswer(TierSimple),
				"complexity": {Type: SystemOneQuestionScore, Score: floatPtr(0.1), Confidence: 0.2},
			},
			wantReason:   JevReasonLowConfidence,
			wantEmptTier: true,
		},
		{
			name: "missing complexity answer counts as hard",
			answers: map[string]SystemOneAnswer{
				"tier": confidentTierAnswer(TierSimple),
			},
			wantReason:   JevReasonHighComplexity,
			wantEmptTier: true,
		},
		{
			name: "unknown tier name publishes nothing",
			answers: map[string]SystemOneAnswer{
				"tier":       confidentTierAnswer("REASONING"),
				"complexity": simpleComplexityAnswer(),
			},
			wantReason:   JevReasonUnknownTier,
			wantEmptTier: true,
		},
		{
			name:         "missing tier answer is engine-unavailable",
			answers:      map[string]SystemOneAnswer{"complexity": simpleComplexityAnswer()},
			wantReason:   JevReasonEngineUnavailable,
			wantEmptTier: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			classifier := newWiredJevClassifier(t, testJevFallbackAnalyzerConfig(), stubSystemOne(tt.answers, nil))
			result, err := classifier.Classify(context.Background(), ComplexityInput{LastUserText: "do the thing"})
			require.NoError(t, err)
			require.NotNil(t, result)
			assert.Equal(t, tt.wantReason, result.Reason)
			assert.Empty(t, result.Tier)
		})
	}
}

func floatPtr(v float64) *float64 { return &v }

func TestJevClassifierTransportErrorReturnsError(t *testing.T) {
	classifier := newWiredJevClassifier(t, testJevFallbackAnalyzerConfig(), stubSystemOne(nil, errors.New("connection refused")))

	result, err := classifier.Classify(context.Background(), ComplexityInput{LastUserText: "hello"})
	require.Error(t, err)
	assert.Nil(t, result)
}

func TestJevClassifierUnconfiguredReturnsNilNil(t *testing.T) {
	unconfigured := NewJevClassifier(nil)
	result, err := unconfigured.Classify(context.Background(), ComplexityInput{LastUserText: "hello"})
	require.NoError(t, err)
	assert.Nil(t, result)

	// Configured but unwired behaves the same: a dormant block never runs.
	configuredOnly := NewJevClassifier(nil)
	configuredOnly.Configure(testJevFallbackAnalyzerConfig())
	result, err = configuredOnly.Classify(context.Background(), ComplexityInput{LastUserText: "hello"})
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestJevClassifierBlankInputReturnsNilNil(t *testing.T) {
	classifier := newWiredJevClassifier(t, testJevFallbackAnalyzerConfig(), stubSystemOne(map[string]SystemOneAnswer{
		"tier": confidentTierAnswer(TierSimple),
	}, nil))
	result, err := classifier.Classify(context.Background(), ComplexityInput{LastUserText: "   "})
	require.NoError(t, err)
	assert.Nil(t, result)
}

func TestJevClassifierFallbackAndConfigured(t *testing.T) {
	classifier := NewJevClassifier(nil)
	classifier.Configure(testJevFallbackAnalyzerConfig())
	assert.True(t, classifier.IsConfigured())
	assert.True(t, classifier.FallbackEnabled())

	// Fallback on none with a retained block: configured but not fallback-enabled.
	cfg := testJevFallbackAnalyzerConfig()
	cfg.Semantic.Fallback = configstore.ComplexitySemanticFallbackNone
	classifier.Configure(cfg)
	assert.True(t, classifier.IsConfigured())
	assert.False(t, classifier.FallbackEnabled())

	// No jev block at all.
	defaults := DefaultAnalyzerConfig()
	classifier.Configure(&defaults)
	assert.False(t, classifier.IsConfigured())
	assert.False(t, classifier.FallbackEnabled())
}

func TestJevClassifierDecisionRequestShape(t *testing.T) {
	var captured *SystemOneRequest
	classifier := newWiredJevClassifier(t, testJevFallbackAnalyzerConfig(), func(_ context.Context, cfg *JevConfig, req *SystemOneRequest) (*SystemOneResponse, error) {
		captured = req
		assert.Equal(t, configstore.DefaultComplexityJevModel, cfg.Model)
		assert.Equal(t, configstore.DefaultComplexityJevTimeout, cfg.Timeout)
		return &SystemOneResponse{Answers: map[string]SystemOneAnswer{
			"tier":       confidentTierAnswer(TierComplex),
			"complexity": simpleComplexityAnswer(),
		}}, nil
	})

	result, err := classifier.Classify(context.Background(), ComplexityInput{LastUserText: "architect a migration"})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, captured)

	assert.Equal(t, configstore.DefaultComplexityJevModel, captured.Model)
	assert.Contains(t, captured.State, "architect a migration")
	assert.Len(t, captured.Questions, 2)

	tier := captured.Questions["tier"]
	assert.Equal(t, SystemOneQuestionChoice, tier.Type)
	criteria, ok := tier.Criteria.(map[string]string)
	require.True(t, ok)
	for _, tierName := range []string{TierSimple, TierMedium, TierComplex} {
		assert.Contains(t, criteria, tierName)
	}

	complexityQ := captured.Questions["complexity"]
	assert.Equal(t, SystemOneQuestionScore, complexityQ.Type)
	levels, ok := complexityQ.Criteria.([]string)
	require.True(t, ok)
	assert.Len(t, levels, 3)

	// A confident verdict on a simple score must not publish COMPLEX: the
	// complexity axis caps the tier choice only via the gates, but here the
	// gate passes (0.1 <= 0.5) so the named tier stands.
	assert.Equal(t, TierComplex, result.Tier)
}

func TestClipJevState(t *testing.T) {
	assert.Equal(t, "short", clipJevState("short"))

	long := make([]rune, configstore.MaxComplexityJevStateCharacters+500)
	for i := range long {
		long[i] = 'a'
	}
	clipped := clipJevState(string(long))
	assert.LessOrEqual(t, len(clipped), configstore.MaxComplexityJevStateCharacters)

	// Multi-byte text is never split mid-rune: a clip budget landing inside a
	// 3-byte rune must round down to the rune boundary.
	multi := "é" + makeRuneString('日', configstore.MaxComplexityJevStateCharacters/3) // 2-byte + 3-byte runes
	clippedMulti := clipJevState(multi)
	assert.LessOrEqual(t, len(clippedMulti), configstore.MaxComplexityJevStateCharacters)
	assert.True(t, utf8.ValidString(clippedMulti))
}

func makeRuneString(r rune, n int) string {
	out := make([]rune, n)
	for i := range out {
		out[i] = r
	}
	return string(out)
}
