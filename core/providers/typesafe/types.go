package typesafe

// Typesafe question types accepted by POST /v1/systemone.
const (
	TypesafeQuestionTypeNoul   = "noul"
	TypesafeQuestionTypeChoice = "choice"
	TypesafeQuestionTypeScore  = "score"
)

// TypesafeQuestion is one named question in a systemone request.
// Instructions accepts a string, object, or array. Criteria is a map for noul
// (optional "true"/"false" descriptions) and choice (option -> description,
// max 255 options), and an ordered array of 2-10 level descriptions for score.
type TypesafeQuestion struct {
	Type         string      `json:"type"`
	Instructions interface{} `json:"instructions,omitempty"`
	Criteria     interface{} `json:"criteria,omitempty"`
}

// TypesafeDecisionRequest is the body of POST /v1/systemone.
type TypesafeDecisionRequest struct {
	State     interface{}                 `json:"state"`
	Model     string                      `json:"model"`
	Questions map[string]TypesafeQuestion `json:"questions"`
}

// GetExtraParams implements providerUtils.RequestBodyWithExtraParams. Typesafe
// takes no passthrough params.
func (r *TypesafeDecisionRequest) GetExtraParams() map[string]interface{} {
	return nil
}

// TypesafeAnswer is one answer in a systemone response. Exactly one of Noul,
// Choice, or Score is set, matching Type.
type TypesafeAnswer struct {
	Type          string             `json:"type"`
	Noul          *float64           `json:"noul,omitempty"`
	Choice        *string            `json:"choice,omitempty"`
	Score         *float64           `json:"score,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	// Legend echoes each score level's description verbatim (string, object,
	// or array), keyed by level number.
	Legend     map[string]any `json:"legend,omitempty"`
	Confidence *float64       `json:"confidence,omitempty"`
}

// TypesafeUsage reports token consumption. Typesafe bills input tokens only.
type TypesafeUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// TypesafeDecisionResponse is the body of a successful systemone response.
type TypesafeDecisionResponse struct {
	Model   string                    `json:"model"`
	Answers map[string]TypesafeAnswer `json:"answers"`
	Usage   *TypesafeUsage            `json:"usage,omitempty"`
}

// TypesafeError is the JSON error body Typesafe returns on 401/422/429/529.
type TypesafeError struct {
	Message string      `json:"message,omitempty"`
	Detail  interface{} `json:"detail,omitempty"`
	Error   *struct {
		Message string `json:"message,omitempty"`
	} `json:"error,omitempty"`
}
