package schemas

// DecisionKind identifies how a single question is decided. The vocabulary
// mirrors Typesafe's System One question types.
type DecisionKind string

const (
	// DecisionKindNoul yields a probability between 0 and 1.
	DecisionKindNoul DecisionKind = "noul"
	// DecisionKindChoice yields one option from the question's criteria.
	DecisionKindChoice DecisionKind = "choice"
	// DecisionKindScore yields a numeric rubric score, including fractions.
	DecisionKindScore DecisionKind = "score"
)

// DecisionQuestion is one named question in a decision request. Instructions
// accepts a string, object, or array, carried losslessly. Criteria is a map of
// descriptions for noul (only "true"/"false" keys) and choice (option ->
// description), and an ordered array of level descriptions for score.
type DecisionQuestion struct {
	Kind         DecisionKind `json:"kind"`
	Instructions interface{}  `json:"instructions,omitempty"`
	Criteria     interface{}  `json:"criteria,omitempty"`
}

// BifrostDecisionRequest represents a request to evaluate state against a map
// of named questions. The shape mirrors Typesafe's System One endpoint.
type BifrostDecisionRequest struct {
	Provider       ModelProvider               `json:"provider"`
	Model          string                      `json:"model"`
	State          interface{}                 `json:"state"` // string, object, or array
	Questions      map[string]DecisionQuestion `json:"questions"`
	Fallbacks      []Fallback                  `json:"fallbacks,omitempty"`
	RawRequestBody []byte                      `json:"-"`
}

// GetRawRequestBody returns the raw request body for the decision request.
func (r *BifrostDecisionRequest) GetRawRequestBody() []byte {
	return r.RawRequestBody
}

// DecisionAnswer is one evaluated answer. Value carries the decided value for
// the question's kind: a number in [0,1] for noul, an option string for
// choice, a numeric rubric score for score. Confidence, Probabilities, and
// Legend carry per-field metadata when the provider supplies it. Legend echoes
// each score level's description verbatim, so its values are strings, objects,
// or arrays - whatever the criteria carried.
type DecisionAnswer struct {
	Kind          DecisionKind       `json:"kind"`
	Value         interface{}        `json:"value"`
	Confidence    *float64           `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Legend        map[string]any     `json:"legend,omitempty"`
}

// BifrostDecisionResponse represents the response from a decision request.
// Answers is keyed by question identifier; every requested question produces
// an answer.
type BifrostDecisionResponse struct {
	ID          string                     `json:"id,omitempty"`
	Model       string                     `json:"model"`
	Answers     map[string]DecisionAnswer  `json:"answers"`
	Usage       *BifrostLLMUsage           `json:"usage,omitempty"`
	ExtraFields BifrostResponseExtraFields `json:"extra_fields"`
}
