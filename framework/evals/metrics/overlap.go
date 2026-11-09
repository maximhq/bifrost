package metrics

import (
	"errors"
	"strings"
)

// Errors for inputs a metric is undefined on. Maxim reports these as evaluator errors.
var (
	ErrEmptyCandidate = errors.New("the output has no words to score")
	ErrEmptyReference = errors.New("the expected output has no words to score against")
)

// wordSet returns the distinct whitespace-separated words of text.
func wordSet(text string) map[string]struct{} {
	fields := strings.Fields(text)
	set := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		set[field] = struct{}{}
	}
	return set
}

func overlap(candidate, reference string) (shared, candidateWords, referenceWords int) {
	candidateSet, referenceSet := wordSet(candidate), wordSet(reference)
	for word := range candidateSet {
		if _, ok := referenceSet[word]; ok {
			shared++
		}
	}
	return shared, len(candidateSet), len(referenceSet)
}

// Precision is the share of the candidate's distinct words that appear in the reference.
func Precision(candidate, reference string) (float64, error) {
	shared, candidateWords, _ := overlap(candidate, reference)
	if candidateWords == 0 {
		return 0, ErrEmptyCandidate
	}
	return float64(shared) / float64(candidateWords), nil
}

// Recall is the share of the reference's distinct words that appear in the candidate.
func Recall(candidate, reference string) (float64, error) {
	shared, _, referenceWords := overlap(candidate, reference)
	if referenceWords == 0 {
		return 0, ErrEmptyReference
	}
	return float64(shared) / float64(referenceWords), nil
}

// F1 is the harmonic mean of Precision and Recall.
func F1(candidate, reference string) (float64, error) {
	shared, candidateWords, referenceWords := overlap(candidate, reference)
	if candidateWords == 0 {
		return 0, ErrEmptyCandidate
	}
	if referenceWords == 0 {
		return 0, ErrEmptyReference
	}
	if shared == 0 {
		return 0, nil
	}
	precision := float64(shared) / float64(candidateWords)
	recall := float64(shared) / float64(referenceWords)
	return 1 / (0.5/precision + 0.5/recall), nil
}

// fmeasure is the harmonic mean used by the ROUGE scorer.
func fmeasure(precision, recall float64) float64 {
	if precision+recall > 0 {
		return 2 * precision * recall / (precision + recall)
	}
	return 0
}
