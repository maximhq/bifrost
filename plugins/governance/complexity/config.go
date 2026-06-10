// Package complexity provides request-complexity scoring for governance routing.
package complexity

// ComplexityInput is the normalized input for the analyzer.
// The caller is responsible for extracting text from request payloads.
type ComplexityInput struct {
	LastUserText   string   // last user message text
	PriorUserTexts []string // previous user message texts
	SystemText     string   // concatenated system/developer prompt text
}

// ComplexityResult holds the computed complexity scores and tier classification.
type ComplexityResult struct {
	Score     float64
	Tier      string
	WordCount int
}

const (
	TierSimple    = "SIMPLE"
	TierMedium    = "MEDIUM"
	TierComplex   = "COMPLEX"
	TierReasoning = "REASONING"
)

const (
	simpleMediumBoundary     = 0.15
	mediumComplexBoundary    = 0.35
	complexReasoningBoundary = 0.60
)
