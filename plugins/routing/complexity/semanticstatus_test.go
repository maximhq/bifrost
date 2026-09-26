package complexity

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// warnCapturingLogger records warnings and delegates everything else, so a test
// can assert on operator-facing guidance without reimplementing schemas.Logger.
type warnCapturingLogger struct {
	schemas.Logger
	mu    sync.Mutex
	warns []string
}

func newWarnCapturingLogger() *warnCapturingLogger {
	return &warnCapturingLogger{Logger: bifrost.NewDefaultLogger(schemas.LogLevelError)}
}

func (l *warnCapturingLogger) Warn(msg string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.warns = append(l.warns, fmt.Sprintf(msg, args...))
}

// matching returns the recorded warnings containing substring.
func (l *warnCapturingLogger) matching(substring string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	var found []string
	for _, warn := range l.warns {
		if strings.Contains(warn, substring) {
			found = append(found, warn)
		}
	}
	return found
}

const downgradeWarningMarker = "falling back to the embedded in-memory store"

func newTestChromemStore(t *testing.T, logger schemas.Logger) vectorstore.VectorStore {
	t.Helper()
	store, err := vectorstore.NewVectorStore(context.Background(), &vectorstore.Config{
		Enabled: true,
		Type:    vectorstore.VectorStoreTypeChromem,
		Config:  vectorstore.ChromemConfig{},
	}, logger)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close(context.Background(), SemanticVectorStoreNamespace))
	})
	return store
}

func requireSemanticReady(t *testing.T, classifier *SemanticClassifier) {
	t.Helper()
	require.Eventually(t, func() bool {
		return classifier.Status().State == SemanticStatusReady
	}, time.Second, 10*time.Millisecond)
}

// TestSemanticClassifierStatusNamesTheServingNamespace guards the only handle an
// operator has on the records this classifier owns in a shared vector store. The
// backend stores no phrase text, so without the namespace there is no way to
// tell which collection belongs to the live generation and which are retired.
func TestSemanticClassifierStatusNamesTheServingNamespace(t *testing.T) {
	logger := newWarnCapturingLogger()
	classifier := NewSemanticClassifier(context.Background(), logger)
	t.Cleanup(func() {
		require.NoError(t, classifier.Close())
	})

	// Nothing configured: no storage has been chosen, so none is claimed.
	initial := classifier.Status()
	assert.Equal(t, SemanticStatusDisabled, initial.State)
	assert.Empty(t, initial.StorageMode)
	assert.Empty(t, initial.Namespace)

	classifier.SetEmbeddingFunc(testSemanticEmbedding)
	config := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreEmbedded)
	classifier.Configure(&config)
	requireSemanticReady(t, classifier)

	status := classifier.Status()
	assert.Equal(t, configstore.ComplexitySemanticVectorStoreEmbedded, status.StorageMode)
	expected := semanticGenerationNamespace(semanticFingerprint(&config, semanticExemplars(&config), testSemanticDimension))
	assert.Equal(t, expected, status.Namespace, "the reported namespace must be the one requests actually query")
	assert.Empty(t, logger.matching(downgradeWarningMarker), "embedded storage was asked for and delivered")
}

// TestSemanticClassifierStatusReportsConfiguredStorage covers the case the
// downgrade check must not fire on: "vector_store" asked for, and supplied.
func TestSemanticClassifierStatusReportsConfiguredStorage(t *testing.T) {
	logger := newWarnCapturingLogger()
	store := newTestChromemStore(t, logger)

	classifier := NewSemanticClassifier(context.Background(), logger)
	t.Cleanup(func() {
		require.NoError(t, classifier.Close())
	})
	classifier.SetConfiguredStore(store)
	classifier.SetEmbeddingFunc(testSemanticEmbedding)

	config := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreConfigured)
	classifier.Configure(&config)
	requireSemanticReady(t, classifier)

	status := classifier.Status()
	assert.Equal(t, configstore.ComplexitySemanticVectorStoreConfigured, status.StorageMode)
	assert.NotEmpty(t, status.Namespace)
	assert.Empty(t, logger.matching(downgradeWarningMarker), "the configured store was available, so nothing degraded")
}

// TestSemanticClassifierReportsSilentStorageDowngrade is the point of the field.
// A deployment that asked for shared storage and never configured one keeps
// classifying correctly, so the misconfiguration is invisible from the outside:
// the symptom is only that every node re-embeds every phrase on restart and
// shares nothing. Status has to name what is actually in use, and the operator
// has to be told once.
func TestSemanticClassifierReportsSilentStorageDowngrade(t *testing.T) {
	logger := newWarnCapturingLogger()
	classifier := NewSemanticClassifier(context.Background(), logger)
	t.Cleanup(func() {
		require.NoError(t, classifier.Close())
	})
	classifier.SetEmbeddingFunc(testSemanticEmbedding)

	// "vector_store" requested, but SetConfiguredStore was never called.
	config := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreConfigured)
	classifier.Configure(&config)
	requireSemanticReady(t, classifier)

	status := classifier.Status()
	assert.Equal(t, SemanticStatusReady, status.State, "a missing vector store must not take classification offline")
	assert.Equal(t, configstore.ComplexitySemanticVectorStoreEmbedded, status.StorageMode,
		"status must report where vectors actually are, not what was asked for")
	require.Len(t, logger.matching(downgradeWarningMarker), 1, "the degrade must be reported once")
	assert.Contains(t, logger.matching(downgradeWarningMarker)[0], "vector_store",
		"the warning must name the setting the operator has to change")

	// Re-saving an unchanged configuration re-resolves the store. The condition
	// has not changed, so it must not produce a second warning.
	classifier.Configure(&config)
	requireSemanticReady(t, classifier)
	assert.Len(t, logger.matching(downgradeWarningMarker), 1, "a persisting degrade must not repeat on every reload")

	// Supplying the store resolves it, and status has to follow.
	classifier.SetConfiguredStore(newTestChromemStore(t, logger))
	requireSemanticReady(t, classifier)
	assert.Equal(t, configstore.ComplexitySemanticVectorStoreConfigured, classifier.Status().StorageMode)
}

// TestSemanticClassifierWarmupDependenciesLeaveNoDowngradeWindow is the reason
// the combined setter exists. Bootstrap holds both collaborators at once; wiring
// them one at a time makes the classifier briefly answer for a store it has not
// been given, so it degrades to embedded, warms a whole generation there, and
// warns about a misconfiguration that does not exist.
func TestSemanticClassifierWarmupDependenciesLeaveNoDowngradeWindow(t *testing.T) {
	logger := newWarnCapturingLogger()
	store := newTestChromemStore(t, logger)

	classifier := NewSemanticClassifier(context.Background(), logger)
	t.Cleanup(func() {
		require.NoError(t, classifier.Close())
	})
	config := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreConfigured)
	classifier.Configure(&config)

	// The order that used to be load-bearing, now expressed as one transition.
	classifier.SetWarmupDependencies(store, testSemanticEmbedding, nil)
	requireSemanticReady(t, classifier)

	assert.Equal(t, configstore.ComplexitySemanticVectorStoreConfigured, classifier.Status().StorageMode)
	assert.Empty(t, logger.matching(downgradeWarningMarker),
		"the store was supplied with the embedding adapter, so no state degraded to embedded")
}

// TestSemanticClassifierSeparateSettersStillWarnOnRealDowngrade guards against
// the combined setter being read as "the degrade check is gone". A caller that
// genuinely has no store must still be told.
func TestSemanticClassifierSeparateSettersStillWarnOnRealDowngrade(t *testing.T) {
	logger := newWarnCapturingLogger()
	classifier := NewSemanticClassifier(context.Background(), logger)
	t.Cleanup(func() {
		require.NoError(t, classifier.Close())
	})
	config := testSemanticClassifierConfig(configstore.ComplexitySemanticVectorStoreConfigured)
	classifier.Configure(&config)

	classifier.SetWarmupDependencies(nil, testSemanticEmbedding, nil)
	requireSemanticReady(t, classifier)

	assert.Equal(t, configstore.ComplexitySemanticVectorStoreEmbedded, classifier.Status().StorageMode)
	assert.Len(t, logger.matching(downgradeWarningMarker), 1)
}
