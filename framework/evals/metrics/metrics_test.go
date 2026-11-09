package metrics

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tolerance is how far a Go score may sit from the Python library's. The ports follow
// the same arithmetic, so only floating-point noise is allowed.
const tolerance = 1e-9

func TestPorterStemMatchesNLTK(t *testing.T) {
	for word, want := range referenceStems {
		assert.Equal(t, want, PorterStem(word), "stem of %q", word)
	}
}

func TestTokenizersMatchReference(t *testing.T) {
	for _, row := range referenceTokens {
		assert.Equal(t, row.bleu13a, Tokenize13a(row.text), "13a tokens of %q", row.text)
		assert.Equal(t, row.rouge, TokenizeRouge(row.text), "rouge tokens of %q", row.text)
		assert.Equal(t, row.words, TokenizeWords(row.text), "word tokens of %q", row.text)
	}
}

func TestMetricsMatchReference(t *testing.T) {
	for _, row := range referenceRows {
		t.Run(row.candidate, func(t *testing.T) {
			bleu, err := BLEU(row.candidate, row.reference)
			require.NoError(t, err)
			assert.InDelta(t, row.bleu, bleu, tolerance, "bleu")

			rouge := Rouge(row.candidate, row.reference)
			assert.InDelta(t, row.rouge1, rouge.Rouge1, tolerance, "rouge1")
			assert.InDelta(t, row.rouge2, rouge.Rouge2, tolerance, "rouge2")
			assert.InDelta(t, row.rougeL, rouge.RougeL, tolerance, "rougeL")
			assert.InDelta(t, row.rougeLsum, rouge.RougeLsum, tolerance, "rougeLsum")

			precision, err := Precision(row.candidate, row.reference)
			require.NoError(t, err)
			assert.InDelta(t, row.precision, precision, tolerance, "precision")
			recall, err := Recall(row.candidate, row.reference)
			require.NoError(t, err)
			assert.InDelta(t, row.recall, recall, tolerance, "recall")
			f1, err := F1(row.candidate, row.reference)
			require.NoError(t, err)
			assert.InDelta(t, row.f1, f1, tolerance, "f1")

			wer, err := WER(row.candidate, row.reference)
			require.NoError(t, err)
			assert.InDelta(t, row.wer, wer, tolerance, "wer")
			assert.InDelta(t, row.ter, TER(row.candidate, row.reference), tolerance, "ter")
			assert.InDelta(t, row.meteor, METEOR(row.candidate, row.reference), tolerance, "meteor")
		})
	}
}

func TestMetricsOnEmptyInput(t *testing.T) {
	_, err := BLEU("", "reference")
	assert.ErrorIs(t, err, ErrEmptyCandidate)
	_, err = BLEU("candidate", "  ")
	assert.ErrorIs(t, err, ErrEmptyReference)
	_, err = Precision("", "reference")
	assert.ErrorIs(t, err, ErrEmptyCandidate)
	_, err = Recall("candidate", "")
	assert.ErrorIs(t, err, ErrEmptyReference)
	_, err = F1("", "")
	assert.ErrorIs(t, err, ErrEmptyCandidate)
	_, err = WER("candidate", "")
	assert.ErrorIs(t, err, ErrEmptyReference)

	assert.Zero(t, Rouge("", "reference").Rouge1)
	assert.Zero(t, Rouge("candidate", "").RougeLsum)
	assert.Zero(t, METEOR("", "reference"))
	assert.InDelta(t, 100, TER("candidate", ""), tolerance, "a non-empty output against nothing is all error")
	assert.Zero(t, TER("", ""))
	assert.InDelta(t, 100, TER("", "two words"), tolerance, "every reference word must be inserted")
}

// jiwer cuts words at single spaces only, so a lone newline joins two words into one.
// These values come from jiwer 3.0.3; a whitespace-splitting WER gets all three wrong.
func TestWERTreatsLoneNewlineAsPartOfAWord(t *testing.T) {
	cases := []struct {
		candidate, reference string
		want                 float64
	}{
		{"Dog\nlazy cats", "Dog\nran unused", 1},
		{"capital quickly within ponies\nrelates been -- delivered", "capital within an quickly ponies\nrelates mat been -- sits", 0.5555555555555556},
		{"a  b\t\tc", "a b c", 0}, // runs of two or more whitespace characters do collapse
	}
	for _, tc := range cases {
		got, err := WER(tc.candidate, tc.reference)
		require.NoError(t, err)
		assert.InDelta(t, tc.want, got, tolerance, "%q vs %q", tc.candidate, tc.reference)
	}
}

func TestVectorDistances(t *testing.T) {
	a, b := []float64{1, 2, 3, 4}, []float64{1, 0, 3, 8}

	cosine, err := CosineSimilarity(a, b)
	require.NoError(t, err)
	assert.InDelta(t, 42/(5.477225575051661*8.602325267042627), cosine, tolerance)
	distance, err := CosineDistance(a, b)
	require.NoError(t, err)
	assert.InDelta(t, 1-cosine, distance, tolerance)

	euclidean, err := EuclideanDistance(a, b)
	require.NoError(t, err)
	assert.InDelta(t, 4.47213595499958, euclidean, tolerance, "sqrt(0 + 4 + 0 + 16)")
	manhattan, err := ManhattanDistance(a, b)
	require.NoError(t, err)
	assert.InDelta(t, 6, manhattan, tolerance)
	chebyshev, err := ChebyshevDistance(a, b)
	require.NoError(t, err)
	assert.InDelta(t, 4, chebyshev, tolerance)
	hamming, err := HammingDistance(a, b)
	require.NoError(t, err)
	assert.InDelta(t, 0.5, hamming, tolerance, "two of four dimensions differ")

	same, err := CosineSimilarity(a, a)
	require.NoError(t, err)
	assert.InDelta(t, 1, same, tolerance)

	_, err = CosineSimilarity(a, []float64{1, 2})
	assert.ErrorIs(t, err, ErrVectorLength)
	_, err = EuclideanDistance(nil, a)
	assert.ErrorIs(t, err, ErrEmptyVector)
	_, err = CosineSimilarity(a, []float64{0, 0, 0, 0})
	assert.ErrorIs(t, err, ErrZeroVector)
}
