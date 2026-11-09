package metrics

import (
	"errors"
	"math"
)

// Errors for vectors a distance is undefined on.
var (
	ErrVectorLength = errors.New("the two embeddings have different lengths")
	ErrZeroVector   = errors.New("an embedding has zero magnitude")
	ErrEmptyVector  = errors.New("an embedding is empty")
)

func checkVectors(a, b []float64) error {
	if len(a) == 0 || len(b) == 0 {
		return ErrEmptyVector
	}
	if len(a) != len(b) {
		return ErrVectorLength
	}
	return nil
}

// CosineSimilarity is the cosine of the angle between two vectors, from -1 to 1.
func CosineSimilarity(a, b []float64) (float64, error) {
	if err := checkVectors(a, b); err != nil {
		return 0, err
	}
	var dot, normA, normB float64
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0, ErrZeroVector
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB)), nil
}

// CosineDistance is 1 minus the cosine similarity: 0 for the same direction.
func CosineDistance(a, b []float64) (float64, error) {
	similarity, err := CosineSimilarity(a, b)
	if err != nil {
		return 0, err
	}
	return 1 - similarity, nil
}

// EuclideanDistance is the straight-line distance between two vectors.
func EuclideanDistance(a, b []float64) (float64, error) {
	if err := checkVectors(a, b); err != nil {
		return 0, err
	}
	var total float64
	for i := range a {
		diff := a[i] - b[i]
		total += diff * diff
	}
	return math.Sqrt(total), nil
}

// ManhattanDistance is the sum of the absolute differences per dimension.
func ManhattanDistance(a, b []float64) (float64, error) {
	if err := checkVectors(a, b); err != nil {
		return 0, err
	}
	var total float64
	for i := range a {
		total += math.Abs(a[i] - b[i])
	}
	return total, nil
}

// ChebyshevDistance is the largest absolute difference in any one dimension.
func ChebyshevDistance(a, b []float64) (float64, error) {
	if err := checkVectors(a, b); err != nil {
		return 0, err
	}
	var largest float64
	for i := range a {
		largest = math.Max(largest, math.Abs(a[i]-b[i]))
	}
	return largest, nil
}

// HammingDistance is the share of dimensions in which the two vectors differ.
func HammingDistance(a, b []float64) (float64, error) {
	if err := checkVectors(a, b); err != nil {
		return 0, err
	}
	differing := 0
	for i := range a {
		if a[i] != b[i] {
			differing++
		}
	}
	return float64(differing) / float64(len(a)), nil
}
