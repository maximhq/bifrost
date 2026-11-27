package evals

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/maximhq/bifrost/framework/evals/metrics"
)

// Catalog keys of the statistical built-ins.
const (
	BuiltinBLEU               = "bleu"
	BuiltinRouge1             = "rouge_1"
	BuiltinRouge2             = "rouge_2"
	BuiltinRougeL             = "rouge_l"
	BuiltinRougeLsum          = "rouge_lsum"
	BuiltinPrecision          = "precision"
	BuiltinRecall             = "recall"
	BuiltinF1                 = "f1_score"
	BuiltinMETEOR             = "meteor"
	BuiltinWER                = "wer"
	BuiltinTER                = "ter"
	BuiltinSemanticSimilarity = "semantic_similarity"
	BuiltinCosineDistance     = "cosine_embedding_distance"
	BuiltinEuclideanDistance  = "euclidean_embedding_distance"
	BuiltinManhattanDistance  = "manhattan_embedding_distance"
	BuiltinChebyshevDistance  = "chebyshev_embedding_distance"
	BuiltinHammingDistance    = "hamming_embedding_distance"
)

// Variables every reference-based metric reads.
const (
	VariableOutput         = "output"
	VariableExpectedOutput = "expected_output"
)

// EmbeddingResponse is the result of embedding a batch of texts.
type EmbeddingResponse struct {
	Vectors [][]float64 // one per input text, in order
	Usage   *Usage
	Cost    *float64
}

// Embedder embeds texts on behalf of an evaluator. NewBifrostEmbedder adapts the
// gateway client to this shape.
type Embedder func(ctx context.Context, provider, model string, texts []string) (EmbeddingResponse, error)

// textMetric scores an output against an expected output.
type textMetric func(candidate, reference string) (float64, error)

// vectorMetric scores two embeddings.
type vectorMetric func(a, b []float64) (float64, error)

func infallible(metric func(candidate, reference string) float64) textMetric {
	return func(candidate, reference string) (float64, error) { return metric(candidate, reference), nil }
}

func rougeVariant(pick func(metrics.RougeScores) float64) textMetric {
	return func(candidate, reference string) (float64, error) {
		return pick(metrics.Rouge(candidate, reference)), nil
	}
}

// textMetrics are the built-ins computed from the two texts alone.
var textMetrics = map[string]textMetric{
	BuiltinBLEU:      metrics.BLEU,
	BuiltinRouge1:    rougeVariant(func(s metrics.RougeScores) float64 { return s.Rouge1 }),
	BuiltinRouge2:    rougeVariant(func(s metrics.RougeScores) float64 { return s.Rouge2 }),
	BuiltinRougeL:    rougeVariant(func(s metrics.RougeScores) float64 { return s.RougeL }),
	BuiltinRougeLsum: rougeVariant(func(s metrics.RougeScores) float64 { return s.RougeLsum }),
	BuiltinPrecision: metrics.Precision,
	BuiltinRecall:    metrics.Recall,
	BuiltinF1:        metrics.F1,
	BuiltinMETEOR:    infallible(metrics.METEOR),
	BuiltinWER:       metrics.WER,
	BuiltinTER:       infallible(metrics.TER),
}

// embeddingMetrics are the built-ins computed from embeddings of the two texts. The
// second value is how many decimals Maxim reports the score to.
var embeddingMetrics = map[string]struct {
	metric   vectorMetric
	decimals int
}{
	BuiltinSemanticSimilarity: {metrics.CosineSimilarity, 2},
	BuiltinCosineDistance:     {metrics.CosineDistance, 4},
	BuiltinEuclideanDistance:  {metrics.EuclideanDistance, 4},
	BuiltinManhattanDistance:  {metrics.ManhattanDistance, 4},
	BuiltinChebyshevDistance:  {metrics.ChebyshevDistance, 4},
	BuiltinHammingDistance:    {metrics.HammingDistance, 4},
}

// isEmbeddingMetric reports whether a statistical built-in calls an embedding model.
func isEmbeddingMetric(builtinKey string) bool {
	_, ok := embeddingMetrics[builtinKey]
	return ok
}

// RegisterStatisticalEvaluators installs the statistical built-ins on an engine. The
// embedder may be nil, in which case the embedding-based metrics report an error.
func RegisterStatisticalEvaluators(engine *Engine, embedder Embedder) {
	for key, metric := range textMetrics {
		engine.Register(EvaluatorTypeStatistical, key, func(Definition, Config) (Evaluator, error) {
			return textMetricEvaluator{metric: metric}, nil
		})
	}
	engine.Register(EvaluatorTypeStatistical, BuiltinSQLiteValidation, func(Definition, Config) (Evaluator, error) {
		return sqliteValidationEvaluator{}, nil
	})
	engine.Register(EvaluatorTypeStatistical, BuiltinToolCallAccuracy, func(Definition, Config) (Evaluator, error) {
		return toolCallAccuracyEvaluator{}, nil
	})
	for key, entry := range embeddingMetrics {
		engine.Register(EvaluatorTypeStatistical, key, func(_ Definition, cfg Config) (Evaluator, error) {
			statistical, ok := cfg.(*StatisticalConfig)
			if !ok {
				return nil, fmt.Errorf("a statistical evaluator needs a statistical config, got %T", cfg)
			}
			if statistical.EmbeddingProvider == "" || statistical.EmbeddingModel == "" {
				return nil, errors.New("this metric needs embedding_provider and embedding_model in its config")
			}
			if embedder == nil {
				return nil, errors.New("no embedding client is configured")
			}
			return embeddingMetricEvaluator{
				metric:   entry.metric,
				decimals: entry.decimals,
				provider: statistical.EmbeddingProvider,
				model:    statistical.EmbeddingModel,
				embedder: embedder,
			}, nil
		})
	}
}

// referenceVariables are the inputs of every metric that compares against a reference.
var referenceVariables = []string{VariableOutput, VariableExpectedOutput}

type textMetricEvaluator struct {
	metric textMetric
}

func (textMetricEvaluator) RequiredVariables() []string { return referenceVariables }

func (e textMetricEvaluator) Evaluate(_ context.Context, in Input) (Result, error) {
	score, err := e.metric(in.Variables[VariableOutput], in.Variables[VariableExpectedOutput])
	if err != nil {
		return Result{}, err
	}
	return Result{Score: NumberScore(roundTo(score, 4))}, nil
}

type embeddingMetricEvaluator struct {
	metric   vectorMetric
	decimals int
	provider string
	model    string
	embedder Embedder
}

func (embeddingMetricEvaluator) RequiredVariables() []string { return referenceVariables }

func (e embeddingMetricEvaluator) Evaluate(ctx context.Context, in Input) (Result, error) {
	texts := []string{in.Variables[VariableOutput], in.Variables[VariableExpectedOutput]}
	response, err := e.embedder(ctx, e.provider, e.model, texts)
	if err != nil {
		return Result{}, err
	}
	// The call was billed whatever came back, so usage rides along on a failure too.
	result := Result{Usage: response.Usage, Cost: response.Cost}
	if len(response.Vectors) != len(texts) {
		result.Score = ErrorScore()
		result.Reasoning = fmt.Sprintf("expected %d embeddings, the provider returned %d", len(texts), len(response.Vectors))
		return result, nil
	}
	score, err := e.metric(response.Vectors[0], response.Vectors[1])
	if err != nil {
		result.Score = ErrorScore()
		result.Reasoning = err.Error()
		return result, nil
	}
	result.Score = NumberScore(roundTo(score, e.decimals))
	return result, nil
}

// roundTo rounds to a number of decimals, which is how Maxim reports metric scores.
func roundTo(value float64, decimals int) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return value
	}
	scale := math.Pow(10, float64(decimals))
	return math.Round(value*scale) / scale
}
