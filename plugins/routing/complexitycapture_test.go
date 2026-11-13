package routing

import (
	"context"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func captureTestEmbedding(_ context.Context, _ *complexity.SemanticConfig, text string) ([]float32, error) {
	switch text {
	case "simple exemplar", "simple request":
		return []float32{1, 0}, nil
	case "medium exemplar":
		return []float32{0, 1}, nil
	default:
		return []float32{-1, 0}, nil
	}
}

func captureTestConfig(suggestions *configstore.ComplexitySuggestionsConfig) *complexity.AnalyzerConfig {
	return &complexity.AnalyzerConfig{
		TierBoundaries: complexity.DefaultTierBoundaries(),
		Keywords: complexity.EditableKeywordConfig{
			SimpleKeywords:  []string{"simple exemplar"},
			MediumKeywords:  []string{"medium exemplar"},
			ComplexKeywords: []string{"complex exemplar"},
		},
		Semantic: &complexity.SemanticConfig{
			Provider: "openai", EmbeddingModel: "test-embedding-model", Timeout: time.Second,
			VectorStore: configstore.ComplexitySemanticVectorStoreEmbedded,
		},
		Suggestions: suggestions,
	}
}

// newCapturePlugin builds a plugin with a ready semantic classifier and
// capture wired, without the upkeep timer (tests drive it). Capture only
// writes to Bifrost's configured vector store, never the classifier's
// embedded one, so the plugin gets an in-memory Chromem store as that.
func newCapturePlugin(t *testing.T, suggestions *configstore.ComplexitySuggestionsConfig) *RoutingPlugin {
	t.Helper()
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	p := &RoutingPlugin{logger: logger, semanticClassifier: complexity.NewSemanticClassifier(context.Background(), logger)}
	p.semanticClassifier.SetEmbeddingFunc(captureTestEmbedding)
	store, err := vectorstore.NewVectorStore(context.Background(), &vectorstore.Config{
		Enabled: true, Type: vectorstore.VectorStoreTypeChromem, Config: vectorstore.ChromemConfig{},
	}, logger)
	require.NoError(t, err)
	p.SetComplexityVectorStore(store)
	p.startComplexityCapture(context.Background(), time.Hour, time.Hour)
	t.Cleanup(func() { _ = p.Cleanup() })
	require.NoError(t, p.storeComplexityAnalyzerConfig(captureTestConfig(suggestions)))
	require.Eventually(t, func() bool { return p.semanticClassifier.Status().State == complexity.SemanticStatusReady },
		2*time.Second, 5*time.Millisecond)
	return p
}

func classifyAs(t *testing.T, p *RoutingPlugin, id string) {
	t.Helper()
	ctx := context.WithValue(context.Background(), schemas.BifrostContextKeyRequestID, id)
	_, err := p.semanticClassifier.Classify(ctx, complexity.ComplexityInput{LastUserText: "simple request"})
	require.NoError(t, err)
}

func TestComplexityCaptureFollowsSuggestionsConfig(t *testing.T) {
	p := newCapturePlugin(t, nil)
	classifyAs(t, p, "off")
	assert.False(t, p.ComplexityCaptureStats().Enabled, "capture is off without a suggestions block")
	assert.Zero(t, p.ComplexityCaptureStats().Queued)

	require.NoError(t, p.storeComplexityAnalyzerConfig(captureTestConfig(&configstore.ComplexitySuggestionsConfig{Enabled: true, SampleRate: 1})))
	classifyAs(t, p, "on")
	require.Eventually(t, func() bool { return p.ComplexityCaptureStats().Written == 1 }, 2*time.Second, 5*time.Millisecond)
	assert.EqualValues(t, 7, p.capture.lookbackDays.Load(), "an unset lookback takes the default")

	require.NoError(t, p.storeComplexityAnalyzerConfig(captureTestConfig(&configstore.ComplexitySuggestionsConfig{Enabled: false})))
	classifyAs(t, p, "off-again")
	assert.False(t, p.ComplexityCaptureStats().Enabled)
	assert.EqualValues(t, 1, p.ComplexityCaptureStats().Queued)
}

// Turning sampling off keeps the lookback the operator set for what was
// already captured: a 3-day one must not be stretched to the 7-day default,
// nor a 30-day one cut to it.
func TestComplexityCaptureKeepsConfiguredLookbackWhenSamplingIsOff(t *testing.T) {
	p := newCapturePlugin(t, nil)
	for _, days := range []int{3, 30} {
		require.NoError(t, p.storeComplexityAnalyzerConfig(captureTestConfig(&configstore.ComplexitySuggestionsConfig{Enabled: true, SampleRate: 1, LookbackDays: days})))
		require.NoError(t, p.storeComplexityAnalyzerConfig(captureTestConfig(&configstore.ComplexitySuggestionsConfig{Enabled: false, LookbackDays: days})))
		assert.False(t, p.ComplexityCaptureStats().Enabled, "sampling is off")
		assert.Equal(t, days, p.capture.keepDays(), "cleanup keeps the configured %d-day lookback", days)
	}

	// No lookback configured: the default still bounds what was captured.
	require.NoError(t, p.storeComplexityAnalyzerConfig(captureTestConfig(&configstore.ComplexitySuggestionsConfig{Enabled: false})))
	assert.Equal(t, complexity.DefaultSuggestionsLookbackDays, p.capture.keepDays())
}

func TestComplexityCaptureKeepsVectorsNoLongerThanLogs(t *testing.T) {
	cc := &complexityCapture{}
	assert.Equal(t, complexity.DefaultSuggestionsLookbackDays, cc.keepDays(), "suggestions off still bounds what was captured")

	cc.lookbackDays.Store(14)
	assert.Equal(t, 14, cc.keepDays(), "logs kept forever: the lookback bounds vectors")

	days := 30
	retention := func() int { return days }
	cc.logRetention.Store(&retention)
	assert.Equal(t, 14, cc.keepDays())
	days = 3
	assert.Equal(t, 3, cc.keepDays(), "a vector must not outlive its log")
}

func TestComplexityCaptureUpkeepSweepsAndForgets(t *testing.T) {
	p := newCapturePlugin(t, &configstore.ComplexitySuggestionsConfig{Enabled: true, SampleRate: 1, LookbackDays: 7})
	for _, id := range []string{"a", "b", "c"} {
		classifyAs(t, p, id)
	}
	require.Eventually(t, func() bool { return p.ComplexityCaptureStats().Written == 3 }, 2*time.Second, 5*time.Millisecond)
	ctx := context.Background()

	p.ForgetComplexityCapturedRequests(ctx, []string{"a"})

	// A pass now keeps everything younger than the lookback: b and c are
	// still there for a sweep of everything to find, a is not.
	p.captureUpkeepOnce(ctx, time.Now())
	n, err := p.semanticClassifier.SweepCapturedRequests(ctx, time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 2, n, "upkeep kept b and c; a was forgotten with its log")

	// Eight days on, the 7-day lookback has passed: the pass sweeps it.
	classifyAs(t, p, "d")
	require.Eventually(t, func() bool { return p.ComplexityCaptureStats().Written == 4 }, 2*time.Second, 5*time.Millisecond)
	p.captureUpkeepOnce(ctx, time.Now().Add(8*24*time.Hour))
	n, err = p.semanticClassifier.SweepCapturedRequests(ctx, time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.Zero(t, n, "the upkeep pass should already have swept d")
}

func TestComplexityCaptureClosesWithPlugin(t *testing.T) {
	p := newCapturePlugin(t, &configstore.ComplexitySuggestionsConfig{Enabled: true, SampleRate: 1})
	classifyAs(t, p, "a")
	require.NoError(t, p.Cleanup())
	st := p.ComplexityCaptureStats()
	assert.False(t, st.Enabled)
	assert.EqualValues(t, st.Queued, st.Written+st.Failed, "cleanup drains accepted samples")
}
