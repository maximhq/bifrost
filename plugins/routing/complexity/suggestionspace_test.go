package complexity

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A suggestion run sees the serving phrases with the vectors requests were
// routed against, and the requests captured in its window with theirs.
func TestSuggestionSpaceReadsPhrasesAndCapturedRequests(t *testing.T) {
	classifier := readySemanticClassifier(t)
	capture := newCaptureTest(t)
	capture.Configure(&CaptureConfig{SampleRate: 1})
	classifier.SetRequestCapture(capture)
	for _, id := range []string{"r1", "r2", "r3"} {
		_, err := classifier.Classify(requestCtx(id), ComplexityInput{LastUserText: "medium request"})
		require.NoError(t, err)
	}
	waitWritten(t, capture, 3)

	ctx := context.Background()
	space, err := classifier.SuggestionSpace(ctx)
	require.NoError(t, err)
	defer space.Close()
	assert.Equal(t, CaptureNamespace("openai", "test-embedding-model", testSemanticDimension, 1), space.CaptureNamespace)
	var texts []string
	for _, p := range space.Phrases {
		texts = append(texts, p.Tier+":"+p.Text)
		assert.Len(t, p.Vector, testSemanticDimension)
	}
	sort.Strings(texts)
	assert.Equal(t, []string{"COMPLEX:complex exemplar", "MEDIUM:medium exemplar", "SIMPLE:simple exemplar"}, texts)

	vectors, err := space.Embed(ctx, []string{"simple request", "medium request"})
	require.NoError(t, err)
	assert.Equal(t, [][]float32{{1, 0}, {0, 1}}, vectors)

	var got []string
	now := time.Now()
	require.NoError(t, space.EachCaptured(ctx, now.Add(-time.Hour), now.Add(time.Minute), func(page []CapturedRequest) error {
		for _, r := range page {
			got = append(got, r.LogID)
			assert.Equal(t, []float32{0, 1}, r.Vector)
			assert.WithinDuration(t, now, r.At, time.Minute)
		}
		return nil
	}))
	sort.Strings(got)
	assert.Equal(t, []string{"r1", "r2", "r3"}, got)

	got = nil
	require.NoError(t, space.EachCaptured(ctx, now.Add(-2*time.Hour), now.Add(-time.Hour), func(page []CapturedRequest) error {
		got = append(got, page[0].LogID)
		return nil
	}))
	assert.Empty(t, got, "nothing was captured before the window")
	space.Close()
	space.Close()
}

func TestSuggestionSpaceNeedsAServingGeneration(t *testing.T) {
	classifier := NewSemanticClassifier(context.Background(), nil)
	t.Cleanup(func() { _ = classifier.Close() })
	_, err := classifier.SuggestionSpace(context.Background())
	require.ErrorIs(t, err, ErrNoServingGeneration)
}
