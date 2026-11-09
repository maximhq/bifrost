package evals

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newStatisticalEngine(embedder Embedder) *Engine {
	engine := NewEngine(EngineOptions{})
	RegisterStatisticalEvaluators(engine, embedder)
	return engine
}

func statisticalDefinition(key, config string) Definition {
	if config == "" {
		config = `{}`
	}
	return Definition{ID: "stat-" + key, Type: EvaluatorTypeStatistical, BuiltinKey: key, Version: 1, Config: []byte(config)}
}

// Scores are rounded to four decimals, as Maxim reports them. The expected values come
// from the reference table in the metrics package.
func TestStatistical_TextMetrics(t *testing.T) {
	in := Input{Variables: map[string]string{
		"output":          "on the mat the cat sat quietly today",
		"expected_output": "the cat sat quietly on the mat today",
	}}
	cases := map[string]float64{
		BuiltinBLEU:      0.517,
		BuiltinRouge1:    1,
		BuiltinRouge2:    0.7143,
		BuiltinRougeL:    0.625,
		BuiltinRougeLsum: 0.625,
		BuiltinPrecision: 1,
		BuiltinRecall:    1,
		BuiltinF1:        1,
		BuiltinWER:       0.75,
		BuiltinTER:       12.5,
		BuiltinMETEOR:    0.7891,
	}
	engine := newStatisticalEngine(nil)
	for key, want := range cases {
		t.Run(key, func(t *testing.T) {
			result := engine.Run(context.Background(), statisticalDefinition(key, ""), in)
			require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
			require.NotNil(t, result.Score.Number)
			assert.InDelta(t, want, *result.Score.Number, 1e-12)
		})
	}
}

func TestStatistical_RequiresOutputAndExpectedOutput(t *testing.T) {
	result := newStatisticalEngine(nil).Run(context.Background(), statisticalDefinition(BuiltinBLEU, ""), Input{Variables: map[string]string{"output": "x"}})
	assert.Equal(t, ScoreStatusNA, result.Score.Status)
	assert.Equal(t, "Missing variables: expected_output", result.Reasoning)
}

func TestStatistical_UndefinedInputIsAnError(t *testing.T) {
	result := newStatisticalEngine(nil).Run(context.Background(), statisticalDefinition(BuiltinPrecision, ""),
		Input{Variables: map[string]string{"output": "   ", "expected_output": "words"}})
	assert.Equal(t, ScoreStatusError, result.Score.Status)
	assert.Contains(t, result.Reasoning, "no words")
}

func TestStatistical_UnknownMetric(t *testing.T) {
	result := newStatisticalEngine(nil).Run(context.Background(), statisticalDefinition("vibes", ""), Input{})
	assert.Equal(t, ScoreStatusError, result.Score.Status)
	assert.Contains(t, result.Reasoning, "vibes")
}

const embeddingConfig = `{"embedding_provider":"openai","embedding_model":"text-embedding-3-small"}`

func TestStatistical_EmbeddingMetrics(t *testing.T) {
	var gotProvider, gotModel string
	var gotTexts []string
	embedder := func(_ context.Context, provider, model string, texts []string) (EmbeddingResponse, error) {
		gotProvider, gotModel, gotTexts = provider, model, texts
		return EmbeddingResponse{
			Vectors: [][]float64{{1, 2, 3, 4}, {1, 0, 3, 8}},
			Usage:   &Usage{PromptTokens: 12, TotalTokens: 12},
			Cost:    new(0.00001),
		}, nil
	}
	in := Input{Variables: map[string]string{"output": "the reply", "expected_output": "the reference"}}
	cases := map[string]float64{
		BuiltinSemanticSimilarity: 0.89, // cosine similarity, two decimals as in Maxim
		BuiltinCosineDistance:     0.1086,
		BuiltinEuclideanDistance:  4.4721,
		BuiltinManhattanDistance:  6,
		BuiltinChebyshevDistance:  4,
		BuiltinHammingDistance:    0.5,
	}
	engine := newStatisticalEngine(embedder)
	for key, want := range cases {
		t.Run(key, func(t *testing.T) {
			result := engine.Run(context.Background(), statisticalDefinition(key, embeddingConfig), in)
			require.Equal(t, ScoreStatusOK, result.Score.Status, result.Reasoning)
			assert.InDelta(t, want, *result.Score.Number, 1e-12)
			assert.Equal(t, &Usage{PromptTokens: 12, TotalTokens: 12}, result.Usage)
			require.NotNil(t, result.Cost)
		})
	}
	assert.Equal(t, "openai", gotProvider)
	assert.Equal(t, "text-embedding-3-small", gotModel)
	assert.Equal(t, []string{"the reply", "the reference"}, gotTexts, "both texts are embedded in one call")
}

func TestStatistical_EmbeddingFailures(t *testing.T) {
	in := Input{Variables: map[string]string{"output": "a", "expected_output": "b"}}

	t.Run("no embedding model configured", func(t *testing.T) {
		embedder := func(context.Context, string, string, []string) (EmbeddingResponse, error) {
			return EmbeddingResponse{}, errors.New("must not be called")
		}
		result := newStatisticalEngine(embedder).Run(context.Background(), statisticalDefinition(BuiltinCosineDistance, ""), in)
		assert.Equal(t, ScoreStatusError, result.Score.Status)
		assert.Contains(t, result.Reasoning, "embedding_model")
	})

	t.Run("no embedder wired", func(t *testing.T) {
		result := newStatisticalEngine(nil).Run(context.Background(), statisticalDefinition(BuiltinCosineDistance, embeddingConfig), in)
		assert.Equal(t, ScoreStatusError, result.Score.Status)
	})

	t.Run("provider returns the wrong number of vectors", func(t *testing.T) {
		embedder := func(context.Context, string, string, []string) (EmbeddingResponse, error) {
			return EmbeddingResponse{Vectors: [][]float64{{1, 2}}}, nil
		}
		result := newStatisticalEngine(embedder).Run(context.Background(), statisticalDefinition(BuiltinCosineDistance, embeddingConfig), in)
		assert.Equal(t, ScoreStatusError, result.Score.Status)
		assert.Contains(t, result.Reasoning, "2 embeddings")
	})

	t.Run("provider fails", func(t *testing.T) {
		embedder := func(context.Context, string, string, []string) (EmbeddingResponse, error) {
			return EmbeddingResponse{}, errors.New("quota exceeded")
		}
		result := newStatisticalEngine(embedder).Run(context.Background(), statisticalDefinition(BuiltinCosineDistance, embeddingConfig), in)
		assert.Equal(t, ScoreStatusError, result.Score.Status)
		assert.Contains(t, result.Reasoning, "quota exceeded")
	})
}

// A metric that calls a model is placed on an eval key; one that does not runs locally.
func TestStatistical_OnlyEmbeddingMetricsNeedAKey(t *testing.T) {
	engine := newStatisticalEngine(nil)
	_, _, needsModel := engine.modelOf(statisticalDefinition(BuiltinBLEU, embeddingConfig))
	assert.False(t, needsModel, "BLEU never calls a model, even if an embedding model is configured")
	provider, model, needsModel := engine.modelOf(statisticalDefinition(BuiltinCosineDistance, embeddingConfig))
	assert.True(t, needsModel)
	assert.Equal(t, "openai", provider)
	assert.Equal(t, "text-embedding-3-small", model)
}
