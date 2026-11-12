package metrics

import (
	"math"
	"strings"
)

// bleuMaxOrder is the longest n-gram BLEU counts.
const bleuMaxOrder = 4

// BLEU scores a candidate against one reference: the geometric mean of 1- to 4-gram
// precision times a brevity penalty, over 13a tokens, with no smoothing. This is the
// definition behind Hugging Face's "bleu" metric, which Maxim calls. Without smoothing
// the score is 0 whenever the candidate shares no 4-gram with the reference, which is
// common for short texts.
func BLEU(candidate, reference string) (float64, error) {
	candidateTokens, referenceTokens := Tokenize13a(candidate), Tokenize13a(reference)
	if len(candidateTokens) == 0 {
		return 0, ErrEmptyCandidate
	}
	if len(referenceTokens) == 0 {
		return 0, ErrEmptyReference
	}
	logSum := 0.0
	for order := 1; order <= bleuMaxOrder; order++ {
		possible := len(candidateTokens) - order + 1
		if possible <= 0 {
			return 0, nil
		}
		matches := ngramMatches(candidateTokens, referenceTokens, order)
		if matches == 0 {
			return 0, nil
		}
		logSum += math.Log(float64(matches)/float64(possible)) / bleuMaxOrder
	}
	ratio := float64(len(candidateTokens)) / float64(len(referenceTokens))
	brevity := 1.0
	if ratio <= 1.0 {
		brevity = math.Exp(1 - 1/ratio)
	}
	return math.Exp(logSum) * brevity, nil
}

// ngramCounts counts the n-grams of one order. An n-gram is keyed by its tokens joined
// with a separator no tokenizer here can produce.
func ngramCounts(tokens []string, order int) map[string]int {
	counts := make(map[string]int, max(len(tokens)-order+1, 0))
	for i := 0; i+order <= len(tokens); i++ {
		counts[strings.Join(tokens[i:i+order], "\x00")]++
	}
	return counts
}

// ngramMatches counts candidate n-grams that also occur in the reference, clipped to
// the number of times each occurs there.
func ngramMatches(candidate, reference []string, order int) int {
	referenceCounts := ngramCounts(reference, order)
	matches := 0
	for ngram, count := range ngramCounts(candidate, order) {
		matches += min(count, referenceCounts[ngram])
	}
	return matches
}
