package metrics

import (
	"fmt"
	"math"
	"math/rand/v2"
	"runtime"
	"slices"
	"strings"
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

// terTexts builds a reference of n words and a candidate that differs from it by
// scattered substitutions and a few short phrases moved within the shift distance, so
// TER searches shifts the way it does on real output.
func terTexts(n int, seed uint64) (candidate, reference string) {
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	ref := make([]string, n)
	for i := range ref {
		ref[i] = fmt.Sprintf("w%d", rng.IntN(1000))
	}
	hyp := slices.Clone(ref)
	for i := 0; i < n; i += 97 {
		hyp[i] = "x"
	}
	for start := 40; start+40 < n; start += n/5 + 1 {
		hyp = terShift(hyp, start, 4, start+30)
	}
	return strings.Join(hyp, " "), strings.Join(ref, " ")
}

// One edit-distance table on two 2,000-word texts stays small: only each row's beam
// window is stored, not the full len(hyp) by len(ref) table (64 MB at this size).
func TestTEREditDistanceStoresOnlyTheBeam(t *testing.T) {
	candidate, reference := terTexts(2000, 1)
	hyp, ref := strings.Fields(candidate), strings.Fields(reference)
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	distance, trace := terEditDistance(hyp, ref)
	runtime.ReadMemStats(&after)
	require.NotNil(t, trace)
	require.Positive(t, distance)
	allocated := after.TotalAlloc - before.TotalAlloc
	assert.Less(t, allocated, uint64(8<<20), "one table allocated %d bytes", allocated)
}

// The windowed table gives the same distance, trace and score as the full table it
// replaced, on random texts of many lengths and length ratios, long ones included.
func TestTERMatchesTheFullTable(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 11))
	vocabulary := func(size int) func() string {
		return func() string { return fmt.Sprintf("v%d", rng.IntN(size)) }
	}
	words := func(n int, next func() string) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = next()
		}
		return out
	}
	for round := range 300 {
		next := vocabulary(3 + rng.IntN(40))
		refLen := rng.IntN(120)
		hypLen := rng.IntN(120)
		if round%30 == 0 {
			refLen, hypLen = 200+rng.IntN(200), 200+rng.IntN(200)
		}
		if round%7 == 0 {
			hypLen = rng.IntN(8) // a long reference against a short candidate widens the beam
		}
		ref := words(refLen, next)
		hyp := words(hypLen, next)
		if round%3 == 0 && refLen > 0 {
			hyp = slices.Clone(ref) // mostly aligned, with a few edits and moves
			for i := 0; i < len(hyp); i += 5 + rng.IntN(10) {
				hyp[i] = next()
			}
			if len(hyp) > 12 {
				hyp = terShift(hyp, 2, 3, len(hyp)-4)
			}
		}
		gotDistance, gotTrace := terEditDistance(hyp, ref)
		wantDistance, wantTrace := terEditDistanceFull(hyp, ref)
		require.Equal(t, wantDistance, gotDistance, "round %d: distance", round)
		require.Equal(t, wantTrace, gotTrace, "round %d: trace", round)
		candidate, reference := strings.Join(hyp, " "), strings.Join(ref, " ")
		require.Equal(t, terFull(candidate, reference), TER(candidate, reference), "round %d: score", round)
	}
	candidate, reference := terTexts(400, 3)
	require.Equal(t, terFull(candidate, reference), TER(candidate, reference), "400-word texts")

	// A shift whose target overlaps the moved span and runs past the end clips the
	// slice, as sacrebleu's Python slices do, instead of panicking.
	require.NotPanics(t, func() { TER("a b a b", "a a b b") })
	require.InDelta(t, 25.0, TER("a b a b", "a a b b"), 1e-9, "overlapping shift past the end")
	require.Equal(t, terFull("a b a b", "a a b b"), TER("a b a b", "a a b b"), "overlapping shift past the end")
}

// BenchmarkTER measures TER on two 10,000-word texts, the case whose full table took
// about 1.6 GB per edit-distance call. Run with -benchmem.
func BenchmarkTER(b *testing.B) {
	candidate, reference := terTexts(10000, 1)
	hyp, ref := strings.Fields(candidate), strings.Fields(reference)
	b.Run("edit distance 10000 words", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			terEditDistance(hyp, ref)
		}
	})
	b.Run("score 10000 words", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			TER(candidate, reference)
		}
	})
}

// terFull is TER as it was before the edit-distance table kept only the beam: the full
// table, and a shift search that visits every reference position. It pins the score.
func terFull(candidate, reference string) float64 {
	hyp := strings.Fields(strings.ToLower(candidate))
	ref := strings.Fields(strings.ToLower(reference))
	if len(ref) == 0 {
		if len(hyp) > 0 {
			return 100
		}
		return 0
	}
	shifts, checked := 0, 0
	for {
		delta, shifted, nowChecked := terBestShiftFull(hyp, ref, checked)
		checked = nowChecked
		if checked >= terMaxShiftCandidates || delta <= 0 {
			break
		}
		shifts++
		hyp = shifted
	}
	distance, _ := terEditDistanceFull(hyp, ref)
	return 100 * float64(shifts+distance) / float64(len(ref))
}

func terEditDistanceFull(hyp, ref []string) (int, []byte) {
	rows, cols := len(hyp)+1, len(ref)+1
	dist := make([]terCell, rows*cols)
	for i := range dist {
		dist[i] = terCell{terInfinity, terOpUndefined}
	}
	at := func(i, j int) *terCell { return &dist[i*cols+j] }
	for j := 0; j < cols; j++ {
		*at(0, j) = terCell{j, terOpInsert}
	}
	lengthRatio := 1.0
	if len(hyp) > 0 {
		lengthRatio = float64(len(ref)) / float64(len(hyp))
	}
	beam := terBeamWidth
	if float64(terBeamWidth) < lengthRatio/2 {
		beam = int(math.Ceil(lengthRatio/2 + terBeamWidth))
	}
	for i := 1; i < rows; i++ {
		diagonal := int(math.Floor(float64(i) * lengthRatio))
		minJ := max(0, diagonal-beam)
		maxJ := min(cols, diagonal+beam)
		if i == len(hyp) {
			maxJ = cols
		}
		for j := minJ; j < maxJ; j++ {
			cell := at(i, j)
			if j == 0 {
				*cell = terCell{at(i-1, j).cost + 1, terOpDelete}
				continue
			}
			substitute := terCell{at(i-1, j-1).cost + 1, terOpSubstitute}
			if hyp[i-1] == ref[j-1] {
				substitute = terCell{at(i-1, j-1).cost, terOpMatch}
			}
			for _, option := range [3]terCell{substitute, {at(i-1, j).cost + 1, terOpDelete}, {at(i, j-1).cost + 1, terOpInsert}} {
				if cell.cost > option.cost {
					*cell = option
				}
			}
		}
	}
	trace := make([]byte, 0, rows+cols)
	for i, j := len(hyp), len(ref); i > 0 || j > 0; {
		op := at(i, j).op
		trace = append(trace, op)
		switch op {
		case terOpSubstitute, terOpMatch:
			i--
			j--
		case terOpInsert:
			j--
		case terOpDelete:
			i--
		default:
			return at(len(hyp), len(ref)).cost, nil
		}
	}
	slices.Reverse(trace)
	return at(len(hyp), len(ref)).cost, trace
}

func terBestShiftFull(hyp, ref []string, checked int) (int, []string, int) {
	preScore, trace := terEditDistanceFull(hyp, ref)
	if trace == nil {
		return 0, hyp, checked
	}
	align, refErr, hypErr := terAlignment(trace)
	var best *terCandidate
search:
	for startH := 0; startH < len(hyp); startH++ {
		for startR := 0; startR < len(ref); startR++ {
			if abs(startR-startH) > terMaxShiftDistance {
				continue
			}
			for length := 1; length <= terMaxShiftSize && hyp[startH+length-1] == ref[startR+length-1]; length++ {
				atEnd := startH+length == len(hyp) || startR+length == len(ref)
				if terConsiderShiftFull(hyp, ref, align, refErr, hypErr, startH, startR, length, preScore, &best, &checked) {
					break search
				}
				if atEnd {
					break
				}
			}
		}
	}
	if best == nil {
		return 0, hyp, checked
	}
	return best.gain, best.shifted, checked
}

func terConsiderShiftFull(hyp, ref []string, align map[int]int, refErr, hypErr []int, startH, startR, length, preScore int, best **terCandidate, checked *int) bool {
	if sum(hypErr[startH:min(startH+length, len(hypErr))]) == 0 {
		return *checked >= terMaxShiftCandidates
	}
	if sum(refErr[startR:min(startR+length, len(refErr))]) == 0 {
		return *checked >= terMaxShiftCandidates
	}
	if aligned := align[startR]; startH <= aligned && aligned < startH+length {
		return *checked >= terMaxShiftCandidates
	}
	previous := -1
	for offset := -1; offset < length; offset++ {
		var target int
		if startR+offset == -1 {
			target = 0
		} else if aligned, ok := align[startR+offset]; ok {
			target = aligned + 1
		} else {
			break
		}
		if target == previous {
			continue
		}
		previous = target
		shifted := terShift(hyp, startH, length, target)
		distance, _ := terEditDistanceFull(shifted, ref)
		candidate := &terCandidate{gain: preScore - distance, length: length, startH: startH, target: target, shifted: shifted}
		*checked++
		if *best == nil || candidate.better(*best) {
			*best = candidate
		}
	}
	return *checked >= terMaxShiftCandidates
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
