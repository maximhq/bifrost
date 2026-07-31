package complexity

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testSemanticDimension = 2

// TestSemanticClassifierClassifiesNearestSharedTierPhrase verifies semantic
// routing labels the existing tier lists instead of maintaining a second list.
func TestSemanticClassifierClassifiesNearestSharedTierPhrase(t *testing.T) {
	classifier := NewSemanticClassifier(context.Background(), bifrost.NewDefaultLogger(schemas.LogLevelError))
	t.Cleanup(func() {
		require.NoError(t, classifier.Close())
	})
	classifier.SetEmbeddingFunc(testSemanticEmbedding)
	config := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreEmbedded)
	classifier.Configure(&config)

	require.Eventually(t, func() bool {
		return classifier.Status().State == SemanticStatusReady
	}, time.Second, 10*time.Millisecond, "semantic classifier did not finish warmup")

	result, err := classifier.Classify(context.Background(), "simple request")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, TierSimple, result.Tier)
	assert.InDelta(t, 1, result.Score, 0.0001)
	assert.Equal(t, "simple exemplar", result.MatchedExemplar, "a classification must name the phrase it matched")
}

// TestSemanticClassifierNamesMatchedExemplarFromReusedGeneration proves the
// matched phrase comes from the in-process index rather than stored metadata:
// a classifier that adopts an already-warmed namespace embeds nothing, yet must
// still report which exemplar the request landed on.
func TestSemanticClassifierNamesMatchedExemplarFromReusedGeneration(t *testing.T) {
	store, err := vectorstore.NewVectorStore(context.Background(), &vectorstore.Config{
		Enabled: true,
		Type:    vectorstore.VectorStoreTypeChromem,
		Config:  vectorstore.ChromemConfig{},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close(context.Background(), SemanticVectorStoreNamespace))
	})

	config := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreExternal)
	_, _, err = warmSemanticExemplars(context.Background(), store, &config, testSemanticEmbedding, nil)
	require.NoError(t, err)

	classifier := NewSemanticClassifier(context.Background(), bifrost.NewDefaultLogger(schemas.LogLevelError))
	t.Cleanup(func() {
		require.NoError(t, classifier.Close())
	})
	var exemplarEmbeddings atomic.Int32
	classifier.SetEmbeddingFunc(func(ctx context.Context, semantic *SemanticConfig, text string) ([]float32, error) {
		if text == "medium exemplar" {
			exemplarEmbeddings.Add(1)
		}
		return testSemanticEmbedding(ctx, semantic, text)
	})
	classifier.SetConfiguredStore(store)
	classifier.Configure(&config)

	require.Eventually(t, func() bool {
		return classifier.Status().State == SemanticStatusReady
	}, time.Second, 10*time.Millisecond, "semantic classifier did not adopt the warmed generation")

	result, err := classifier.Classify(context.Background(), "medium request")
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, TierMedium, result.Tier)
	assert.Equal(t, "medium exemplar", result.MatchedExemplar)
	assert.EqualValues(t, 0, exemplarEmbeddings.Load(), "adopting a warmed generation must not re-embed exemplars")
}

// TestSemanticClassifierAppliesMinSimilarity covers the floor that stops the
// nearest exemplar from winning by default however unrelated it is. A rejected
// match must still report its tier and score so the near miss is diagnosable.
func TestSemanticClassifierAppliesMinSimilarity(t *testing.T) {
	tests := []struct {
		name          string
		minSimilarity float64
		wantAccepted  bool
	}{
		{name: "no floor accepts a weak match", minSimilarity: 0, wantAccepted: true},
		{name: "floor below the score accepts", minSimilarity: 0.5, wantAccepted: true},
		{name: "floor at the score accepts", minSimilarity: 0.7071068, wantAccepted: true},
		{name: "floor above the score rejects", minSimilarity: 0.9, wantAccepted: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			classifier := NewSemanticClassifier(context.Background(), bifrost.NewDefaultLogger(schemas.LogLevelError))
			t.Cleanup(func() {
				require.NoError(t, classifier.Close())
			})
			classifier.SetEmbeddingFunc(testSemanticEmbedding)
			config := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreEmbedded)
			config.Semantic.MinSimilarity = test.minSimilarity
			classifier.Configure(&config)

			require.Eventually(t, func() bool {
				return classifier.Status().State == SemanticStatusReady
			}, time.Second, 10*time.Millisecond, "semantic classifier did not finish warmup")

			result, err := classifier.Classify(context.Background(), "borderline request")
			require.NoError(t, err)
			require.NotNil(t, result, "a rejected match must still be reported so callers can log the near miss")
			assert.Equal(t, test.wantAccepted, result.Accepted)
			assert.Equal(t, test.minSimilarity, result.MinSimilarity)
			assert.InDelta(t, 0.7071068, result.Score, 0.001)
			assert.True(t, isComplexityTier(result.Tier))
		})
	}
}

// TestSemanticClassifierRejectsUnavailableExternalStore keeps the external
// mode fail-closed rather than silently replacing an operator-selected backend.
func TestSemanticClassifierRejectsUnavailableExternalStore(t *testing.T) {
	classifier := NewSemanticClassifier(context.Background(), bifrost.NewDefaultLogger(schemas.LogLevelError))
	config := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreExternal)

	err := classifier.ValidateConfig(&config)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "requires a configured Bifrost VectorStore")
}

// TestWarmSemanticExemplarsReusesMatchingMarker reuses a persisted generation
// after one embedding establishes the model's runtime vector width.
func TestWarmSemanticExemplarsReusesMatchingMarker(t *testing.T) {
	store, err := vectorstore.NewVectorStore(context.Background(), &vectorstore.Config{
		Enabled: true,
		Type:    vectorstore.VectorStoreTypeChromem,
		Config:  vectorstore.ChromemConfig{},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close(context.Background(), SemanticVectorStoreNamespace))
	})

	config := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreEmbedded)
	var embeddingCalls atomic.Int32
	embed := func(ctx context.Context, semantic *SemanticConfig, text string) ([]float32, error) {
		embeddingCalls.Add(1)
		return testSemanticEmbedding(ctx, semantic, text)
	}

	loaded, _, err := warmSemanticExemplars(context.Background(), store, &config, embed, nil)
	require.NoError(t, err)
	assert.Equal(t, len(semanticExemplars(&config)), loaded)
	assert.EqualValues(t, loaded, embeddingCalls.Load())

	loaded, _, err = warmSemanticExemplars(context.Background(), store, &config, embed, nil)
	require.NoError(t, err)
	assert.Equal(t, len(semanticExemplars(&config)), loaded)
	assert.EqualValues(t, loaded+1, embeddingCalls.Load(), "reusing a persisted generation needs one embedding to rediscover its width")
}

// TestWarmSemanticExemplarsUsesBackendCompatibleLifecycle proves warmup does
// not depend on Chromem's missing-record error or an existing dimension.
func TestWarmSemanticExemplarsUsesBackendCompatibleLifecycle(t *testing.T) {
	backingStore, err := vectorstore.NewVectorStore(context.Background(), &vectorstore.Config{
		Enabled: true,
		Type:    vectorstore.VectorStoreTypeChromem,
		Config:  vectorstore.ChromemConfig{},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, backingStore.Close(context.Background(), SemanticVectorStoreNamespace))
	})

	store := &semanticLifecycleProbeStore{VectorStore: backingStore}
	config := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreExternal)

	loaded, namespace, err := warmSemanticExemplars(context.Background(), store, &config, testSemanticEmbedding, nil)
	require.NoError(t, err)
	assert.Equal(t, len(semanticExemplars(&config)), loaded)
	assert.True(t, store.createdBeforeRead, "a generation namespace must exist before its marker is read")
	assert.False(t, store.deleted, "an older generation must not be deleted during warmup")
	assert.Equal(t, semanticGenerationNamespace(semanticFingerprint(&config, semanticExemplars(&config), dimension)), namespace)
	require.Len(t, store.markerIDs, 1)
	markerID, err := uuid.Parse(store.markerIDs[0])
	require.NoError(t, err)
	assert.Equal(t, uuid.Version(5), markerID.Version())
}

func TestSemanticClassifierDefersAutoStoreUntilDependenciesAreWired(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	classifier := NewSemanticClassifier(context.Background(), logger)
	config := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreAuto)
	classifier.Configure(&config)

	assert.Nil(t, classifier.ownedStore)
	assert.Nil(t, classifier.store)

	configuredStore, err := vectorstore.NewVectorStore(context.Background(), &vectorstore.Config{
		Enabled: true,
		Type:    vectorstore.VectorStoreTypeChromem,
		Config:  vectorstore.ChromemConfig{},
	}, logger)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, classifier.Close())
		require.NoError(t, configuredStore.Close(context.Background(), SemanticVectorStoreNamespace))
	})

	classifier.SetConfiguredStore(configuredStore)
	assert.Nil(t, classifier.ownedStore, "auto mode must not allocate a private store before the embedding executor is wired")
	classifier.SetEmbeddingFunc(testSemanticEmbedding)

	require.Eventually(t, func() bool {
		return classifier.Status().State == SemanticStatusReady
	}, time.Second, 10*time.Millisecond)
	assert.Same(t, configuredStore, classifier.store)
	assert.Nil(t, classifier.ownedStore)
}

func TestSemanticClassifierSwitchesGenerationsOnlyAfterSuccessfulWarmup(t *testing.T) {
	classifier := NewSemanticClassifier(context.Background(), bifrost.NewDefaultLogger(schemas.LogLevelError))
	t.Cleanup(func() {
		require.NoError(t, classifier.Close())
	})

	releaseV2 := make(chan struct{})
	var requestModel atomic.Value
	embed := func(ctx context.Context, semantic *SemanticConfig, text string) ([]float32, error) {
		switch semantic.EmbeddingModel {
		case "model-v1":
			switch text {
			case "simple exemplar", "routing request":
				if text == "routing request" {
					requestModel.Store(semantic.EmbeddingModel)
				}
				return []float32{1, 0}, nil
			case "medium exemplar":
				return []float32{0, 1}, nil
			case "complex exemplar":
				return []float32{-1, 0}, nil
			}
		case "model-v2":
			switch text {
			case "v2 simple":
				select {
				case <-releaseV2:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				return []float32{0, 1}, nil
			case "v2 medium":
				return []float32{-1, 0}, nil
			case "v2 complex", "routing request":
				if text == "routing request" {
					requestModel.Store(semantic.EmbeddingModel)
				}
				return []float32{1, 0}, nil
			}
		case "model-v3":
			return nil, fmt.Errorf("synthetic v3 warmup failure")
		}
		return nil, fmt.Errorf("unexpected semantic test input model=%q text=%q", semantic.EmbeddingModel, text)
	}
	classifier.SetEmbeddingFunc(embed)

	v1 := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreEmbedded)
	v1.Semantic.EmbeddingModel = "model-v1"
	classifier.Configure(&v1)
	require.Eventually(t, func() bool {
		return classifier.Status().State == SemanticStatusReady
	}, time.Second, 10*time.Millisecond)

	classifier.mu.Lock()
	v1Namespace := classifier.active.namespace
	classifier.mu.Unlock()
	v1Result, err := classifier.Classify(context.Background(), "routing request")
	require.NoError(t, err)
	require.NotNil(t, v1Result)
	assert.Equal(t, TierSimple, v1Result.Tier)
	assert.Equal(t, "model-v1", requestModel.Load())

	v2 := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreEmbedded)
	v2.Semantic.EmbeddingModel = "model-v2"
	v2.Keywords = EditableKeywordConfig{
		SimpleKeywords:  []string{"v2 simple"},
		MediumKeywords:  []string{"v2 medium"},
		ComplexKeywords: []string{"v2 complex"},
	}
	classifier.Configure(&v2)
	require.Eventually(t, func() bool {
		status := classifier.Status()
		return status.State == SemanticStatusWarming && status.ServingPrevious
	}, time.Second, 10*time.Millisecond)

	// F1 remains queryable while the first F2 embedding is deliberately blocked.
	duringWarmup, err := classifier.Classify(context.Background(), "routing request")
	require.NoError(t, err)
	require.NotNil(t, duringWarmup)
	assert.Equal(t, TierSimple, duringWarmup.Tier)
	assert.Equal(t, "model-v1", requestModel.Load())

	close(releaseV2)
	require.Eventually(t, func() bool {
		return classifier.Status().State == SemanticStatusReady
	}, time.Second, 10*time.Millisecond)

	classifier.mu.Lock()
	v2Namespace := classifier.active.namespace
	activeStore := classifier.active.store
	classifier.mu.Unlock()
	assert.NotEqual(t, v1Namespace, v2Namespace)
	v2Result, err := classifier.Classify(context.Background(), "routing request")
	require.NoError(t, err)
	require.NotNil(t, v2Result)
	assert.Equal(t, TierComplex, v2Result.Tier)
	assert.Equal(t, "model-v2", requestModel.Load())

	// The classifier owns this embedded store, so an unused F1 is reclaimed as
	// soon as F2 becomes active.
	v1Fingerprint := semanticFingerprint(&v1, semanticExemplars(&v1), testSemanticDimension)
	_, err = activeStore.GetChunks(context.Background(), v1Namespace, []string{semanticMarkerID(v1Fingerprint)})
	require.ErrorIs(t, err, vectorstore.ErrNotFound)

	v3 := v2
	v3.Semantic = cloneSemanticConfig(v2.Semantic)
	v3.Semantic.EmbeddingModel = "model-v3"
	classifier.Configure(&v3)
	require.Eventually(t, func() bool {
		status := classifier.Status()
		return status.State == SemanticStatusFailed && status.ServingPrevious
	}, time.Second, 10*time.Millisecond)

	// A failed F3 keeps both the F2 namespace and F2 embedding configuration.
	afterFailure, err := classifier.Classify(context.Background(), "routing request")
	require.NoError(t, err)
	require.NotNil(t, afterFailure)
	assert.Equal(t, TierComplex, afterFailure.Tier)
	assert.Equal(t, "model-v2", requestModel.Load())
	classifier.mu.Lock()
	assert.Equal(t, v2Namespace, classifier.active.namespace)
	classifier.mu.Unlock()

	v3Fingerprint := semanticFingerprint(&v3, semanticExemplars(&v3), testSemanticDimension)
	_, err = activeStore.GetChunks(
		context.Background(),
		semanticGenerationNamespace(v3Fingerprint),
		[]string{semanticMarkerID(v3Fingerprint)},
	)
	require.ErrorIs(t, err, vectorstore.ErrNotFound, "a failed private generation must be discarded")
}

func TestSemanticClassifierDefersEmbeddedCleanupUntilInFlightRequestFinishes(t *testing.T) {
	classifier := NewSemanticClassifier(context.Background(), bifrost.NewDefaultLogger(schemas.LogLevelError))
	t.Cleanup(func() {
		require.NoError(t, classifier.Close())
	})

	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	embed := func(ctx context.Context, semantic *SemanticConfig, text string) ([]float32, error) {
		if text == "blocked request" {
			close(requestStarted)
			select {
			case <-releaseRequest:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return []float32{1, 0}, nil
		}
		return testSemanticEmbedding(ctx, semantic, text)
	}
	classifier.SetEmbeddingFunc(embed)

	v1 := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreEmbedded)
	v1.Semantic.EmbeddingModel = "model-v1"
	classifier.Configure(&v1)
	require.Eventually(t, func() bool {
		return classifier.Status().State == SemanticStatusReady
	}, time.Second, 10*time.Millisecond)

	classifier.mu.Lock()
	v1Namespace := classifier.active.namespace
	activeStore := classifier.active.store
	classifier.mu.Unlock()
	v1Fingerprint := semanticFingerprint(&v1, semanticExemplars(&v1), testSemanticDimension)
	v1MarkerID := semanticMarkerID(v1Fingerprint)

	type classification struct {
		result *SemanticResult
		err    error
	}
	classificationDone := make(chan classification, 1)
	go func() {
		result, err := classifier.Classify(context.Background(), ComplexityInput{LastUserText: "blocked request"})
		classificationDone <- classification{result: result, err: err}
	}()
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("classification did not acquire F1")
	}

	v2 := v1
	v2.Semantic = cloneSemanticConfig(v1.Semantic)
	v2.Semantic.EmbeddingModel = "model-v2"
	classifier.Configure(&v2)
	require.Eventually(t, func() bool {
		return classifier.Status().State == SemanticStatusReady
	}, time.Second, 10*time.Millisecond)

	v1Markers, err := activeStore.GetChunks(context.Background(), v1Namespace, []string{v1MarkerID})
	require.NoError(t, err)
	require.Len(t, v1Markers, 1, "F1 must remain queryable while an earlier request still holds it")

	close(releaseRequest)
	classified := <-classificationDone
	require.NoError(t, classified.err)
	require.NotNil(t, classified.result)
	assert.Equal(t, TierSimple, classified.result.Tier)
	require.Eventually(t, func() bool {
		_, err := activeStore.GetChunks(context.Background(), v1Namespace, []string{v1MarkerID})
		return errors.Is(err, vectorstore.ErrNotFound)
	}, time.Second, 10*time.Millisecond, "F1 must be reclaimed after its last request releases it")
}

func TestSemanticClassifierRetainsPreviousExternalGeneration(t *testing.T) {
	logger := bifrost.NewDefaultLogger(schemas.LogLevelError)
	store, err := vectorstore.NewVectorStore(context.Background(), &vectorstore.Config{
		Enabled: true,
		Type:    vectorstore.VectorStoreTypeChromem,
		Config:  vectorstore.ChromemConfig{},
	}, logger)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close(context.Background(), SemanticVectorStoreNamespace))
	})

	classifier := NewSemanticClassifier(context.Background(), logger)
	t.Cleanup(func() {
		require.NoError(t, classifier.Close())
	})
	classifier.SetConfiguredStore(store)
	classifier.SetEmbeddingFunc(testSemanticEmbedding)

	v1 := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreExternal)
	v1.Semantic.EmbeddingModel = "model-v1"
	classifier.Configure(&v1)
	require.Eventually(t, func() bool {
		return classifier.Status().State == SemanticStatusReady
	}, time.Second, 10*time.Millisecond)
	v1Fingerprint := semanticFingerprint(&v1, semanticExemplars(&v1), testSemanticDimension)
	v1Namespace := semanticGenerationNamespace(v1Fingerprint)
	v1MarkerID := semanticMarkerID(v1Fingerprint)

	v2 := v1
	v2.Semantic = cloneSemanticConfig(v1.Semantic)
	v2.Semantic.EmbeddingModel = "model-v2"
	classifier.Configure(&v2)
	require.Eventually(t, func() bool {
		return classifier.Status().State == SemanticStatusReady
	}, time.Second, 10*time.Millisecond)

	v1Markers, err := store.GetChunks(context.Background(), v1Namespace, []string{v1MarkerID})
	require.NoError(t, err)
	require.Len(t, v1Markers, 1, "shared stores retain F1 because another replica may still serve it")
}

func TestSemanticClassifierReusesEmbeddedNamespaceForScalarOnlyChange(t *testing.T) {
	classifier := NewSemanticClassifier(context.Background(), bifrost.NewDefaultLogger(schemas.LogLevelError))
	t.Cleanup(func() {
		require.NoError(t, classifier.Close())
	})
	classifier.SetEmbeddingFunc(testSemanticEmbedding)

	config := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreEmbedded)
	classifier.Configure(&config)
	require.Eventually(t, func() bool {
		return classifier.Status().State == SemanticStatusReady
	}, time.Second, 10*time.Millisecond)

	classifier.mu.Lock()
	namespace := classifier.active.namespace
	store := classifier.active.store
	classifier.mu.Unlock()

	config.Semantic.MinSimilarity = 0.8
	classifier.Configure(&config)
	require.Eventually(t, func() bool {
		return classifier.Status().State == SemanticStatusReady
	}, time.Second, 10*time.Millisecond)

	classifier.mu.Lock()
	assert.Equal(t, namespace, classifier.active.namespace)
	classifier.mu.Unlock()
	fingerprint := semanticFingerprint(&config, semanticExemplars(&config), testSemanticDimension)
	markers, err := store.GetChunks(context.Background(), namespace, []string{semanticMarkerID(fingerprint)})
	require.NoError(t, err)
	require.Len(t, markers, 1, "scalar-only changes must reuse, not delete, the existing vectors")
}

func TestWarmSemanticExemplarsUsesBoundedBatches(t *testing.T) {
	store, err := vectorstore.NewVectorStore(context.Background(), &vectorstore.Config{
		Enabled: true,
		Type:    vectorstore.VectorStoreTypeChromem,
		Config:  vectorstore.ChromemConfig{},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close(context.Background(), SemanticVectorStoreNamespace))
	})

	config := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreEmbedded)
	config.Keywords.SimpleKeywords = makeSemanticTestPhrases("simple", 24)
	config.Keywords.MediumKeywords = makeSemanticTestPhrases("medium", 23)
	config.Keywords.ComplexKeywords = makeSemanticTestPhrases("complex", 23)

	var batchSizes []int
	batch := func(_ context.Context, _ *SemanticConfig, texts []string) ([][]float32, error) {
		batchSizes = append(batchSizes, len(texts))
		embeddings := make([][]float32, len(texts))
		for index := range texts {
			embeddings[index] = []float32{1, 0}
		}
		return embeddings, nil
	}
	single := func(_ context.Context, _ *SemanticConfig, text string) ([]float32, error) {
		return nil, fmt.Errorf("unexpected single-input call for %q", text)
	}

	loaded, _, err := warmSemanticExemplars(context.Background(), store, &config, single, batch)
	require.NoError(t, err)
	assert.Equal(t, 70, loaded)
	assert.Equal(t, []int{32, 32, 6}, batchSizes)
}

func TestWarmSemanticExemplarsFallsBackWhenBatchingIsUnsupported(t *testing.T) {
	store, err := vectorstore.NewVectorStore(context.Background(), &vectorstore.Config{
		Enabled: true,
		Type:    vectorstore.VectorStoreTypeChromem,
		Config:  vectorstore.ChromemConfig{},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close(context.Background(), SemanticVectorStoreNamespace))
	})

	config := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreEmbedded)
	config.Keywords.SimpleKeywords = makeSemanticTestPhrases("simple", 14)
	config.Keywords.MediumKeywords = makeSemanticTestPhrases("medium", 13)
	config.Keywords.ComplexKeywords = makeSemanticTestPhrases("complex", 13)

	var batchCalls atomic.Int32
	var singleCalls atomic.Int32
	batch := func(_ context.Context, _ *SemanticConfig, _ []string) ([][]float32, error) {
		batchCalls.Add(1)
		return nil, ErrBatchEmbeddingsUnsupported
	}
	single := func(_ context.Context, _ *SemanticConfig, _ string) ([]float32, error) {
		singleCalls.Add(1)
		return []float32{1, 0}, nil
	}

	loaded, _, err := warmSemanticExemplars(context.Background(), store, &config, single, batch)
	require.NoError(t, err)
	assert.Equal(t, 40, loaded)
	assert.EqualValues(t, 1, batchCalls.Load())
	assert.EqualValues(t, 40, singleCalls.Load())
}

func TestWarmSemanticExemplarsRejectsInconsistentDetectedDimensions(t *testing.T) {
	store, err := vectorstore.NewVectorStore(context.Background(), &vectorstore.Config{
		Enabled: true,
		Type:    vectorstore.VectorStoreTypeChromem,
		Config:  vectorstore.ChromemConfig{},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close(context.Background(), SemanticVectorStoreNamespace))
	})

	config := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreEmbedded)
	batch := func(_ context.Context, _ *SemanticConfig, texts []string) ([][]float32, error) {
		embeddings := make([][]float32, len(texts))
		for index := range texts {
			embeddings[index] = []float32{1, 0}
		}
		embeddings[len(embeddings)-1] = []float32{1, 0, 0}
		return embeddings, nil
	}

	_, _, _, err = warmSemanticExemplars(context.Background(), store, &config, testSemanticEmbedding, batch, nil)
	require.ErrorContains(t, err, "returned dimension 3, expected 2")
}

// TestSemanticRecordIDsAreUUIDs keeps deterministic records compatible with
// backends such as Qdrant and Weaviate that require UUID identifiers.
func TestSemanticRecordIDsAreUUIDs(t *testing.T) {
	exemplar := semanticExemplar{Tier: TierMedium, Phrase: "example phrase"}
	for _, id := range []string{semanticMarkerID("fingerprint"), semanticExemplarID("fingerprint", exemplar)} {
		parsed, err := uuid.Parse(id)
		require.NoError(t, err)
		assert.Equal(t, uuid.Version(5), parsed.Version())
	}
}

func TestSemanticFingerprintIgnoresPhraseOrder(t *testing.T) {
	config := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreEmbedded)
	exemplars := semanticExemplars(&config)
	original := append([]semanticExemplar(nil), exemplars...)
	reordered := []semanticExemplar{exemplars[2], exemplars[0], exemplars[1]}

	assert.Equal(t, semanticFingerprint(&config, exemplars, testSemanticDimension), semanticFingerprint(&config, reordered, testSemanticDimension))
	assert.Equal(t, original, exemplars, "fingerprinting must not reorder the caller's slice")
}

// semanticLifecycleProbeStore simulates a backend that cannot read a marker
// before its generation namespace exists.
type semanticLifecycleProbeStore struct {
	vectorstore.VectorStore
	created           bool
	createdBeforeRead bool
	deleted           bool
	markerIDs         []string
}

// GetChunks rejects read-before-create and otherwise returns a missing marker.
func (s *semanticLifecycleProbeStore) GetChunks(_ context.Context, _ string, ids []string) ([]vectorstore.SearchResult, error) {
	s.createdBeforeRead = s.created
	if !s.created {
		return nil, fmt.Errorf("namespace does not exist")
	}
	s.markerIDs = append([]string(nil), ids...)
	return []vectorstore.SearchResult{}, nil
}

// DeleteNamespace records any destructive lifecycle behavior.
func (s *semanticLifecycleProbeStore) DeleteNamespace(ctx context.Context, namespace string) error {
	s.deleted = true
	return s.VectorStore.DeleteNamespace(ctx, namespace)
}

// CreateNamespace records that the immutable generation exists before reads.
func (s *semanticLifecycleProbeStore) CreateNamespace(ctx context.Context, namespace string, dimension int, properties map[string]vectorstore.VectorStoreProperties) error {
	s.created = true
	return s.VectorStore.CreateNamespace(ctx, namespace, dimension, properties)
}

// testSemanticClassifierConfig returns the smallest valid semantic config with
// one distinct shared phrase per routing tier.
func testSemanticClassifierConfig(vectorStore string) AnalyzerConfig {
	return AnalyzerConfig{
		TierBoundaries: DefaultTierBoundaries(),
		Keywords: EditableKeywordConfig{
			SimpleKeywords:  []string{"simple exemplar"},
			MediumKeywords:  []string{"medium exemplar"},
			ComplexKeywords: []string{"complex exemplar"},
		},
		Semantic: &SemanticConfig{
			Provider:       schemas.ModelProvider("openai"),
			EmbeddingModel: "test-embedding-model",
			Timeout:        time.Second,
			VectorStore:    vectorStore,
		},
	}
}

// testSemanticEmbedding returns deterministic unit vectors for the phrases in
// testSemanticClassifierConfig and their corresponding synthetic requests.
func testSemanticEmbedding(_ context.Context, _ *SemanticConfig, text string) ([]float32, error) {
	switch text {
	case "simple exemplar", "simple request":
		return []float32{1, 0}, nil
	case "medium exemplar", "medium request":
		return []float32{0, 1}, nil
	case "complex exemplar", "complex request":
		return []float32{-1, 0}, nil
	case "borderline request":
		// Equidistant from the simple and medium exemplars: cosine ~0.707 to
		// both, which is a real match but a weak one.
		return []float32{0.7071068, 0.7071068}, nil
	default:
		return nil, fmt.Errorf("unexpected semantic test text %q", text)
	}
}

func makeSemanticTestPhrases(prefix string, count int) []string {
	phrases := make([]string, count)
	for index := range phrases {
		phrases[index] = fmt.Sprintf("%s exemplar %d", prefix, index)
	}
	return phrases
}
