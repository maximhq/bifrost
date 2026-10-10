package typesafe

import (
	"bytes"
	"encoding/json"

	"github.com/bytedance/sonic"
	"github.com/tidwall/gjson"
)

// Typesafe question types accepted by POST /v1/systemone.
const (
	TypesafeQuestionTypeNoul   = "noul"
	TypesafeQuestionTypeChoice = "choice"
	TypesafeQuestionTypeScore  = "score"
)

// TypesafeQuestion is one named question in a systemone request.
type TypesafeQuestion struct {
	Type         string      `json:"type"`
	Instructions interface{} `json:"instructions,omitempty"` // string, object, array, or absent
	Criteria     interface{} `json:"criteria,omitempty"`     // map for noul/choice, ordered array for score; null descriptions allowed
}

// UnmarshalJSON decodes a question, reading "kind" as the type when "type" is
// absent: the deprecated /v1/decisions map body writes this same question
// with "kind", and it is decoded through this type.
func (q *TypesafeQuestion) UnmarshalJSON(data []byte) error {
	type alias TypesafeQuestion
	var decoded struct {
		alias
		Kind string `json:"kind"`
	}
	if err := sonic.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*q = TypesafeQuestion(decoded.alias)
	if q.Type == "" {
		q.Type = decoded.Kind
	}
	return nil
}

// TypesafeDecisionRequest is the body of POST /v1/systemone.
type TypesafeDecisionRequest struct {
	State       interface{}                 `json:"state"`
	Model       string                      `json:"model"`
	Questions   map[string]TypesafeQuestion `json:"questions"`
	Images      []interface{}               `json:"images,omitempty"` // base64 data URLs or {content_type, base64}; read by vision endpoints such as Clef
	ExtraParams map[string]interface{}      `json:"-"`                // native extensions, merged onto the wire by the provider
	stateSet    bool                        // "state" key present on decode; keeps an explicit null distinct from absent
}

// typesafeDecisionRequestKnownFields are the modelled top-level keys; anything
// else is a native extension carried in ExtraParams.
var typesafeDecisionRequestKnownFields = map[string]bool{
	"state":     true,
	"model":     true,
	"questions": true,
	"images":    true,
}

// UnmarshalJSON decodes the modelled fields, records whether "state" was
// present, and captures unknown top-level properties verbatim (compacted) so
// they can be forwarded losslessly.
func (r *TypesafeDecisionRequest) UnmarshalJSON(data []byte) error {
	type Alias TypesafeDecisionRequest
	if err := sonic.Unmarshal(data, (*Alias)(r)); err != nil {
		return err
	}
	r.ExtraParams = nil
	r.stateSet = false
	gjson.ParseBytes(data).ForEach(func(key, value gjson.Result) bool {
		name := key.String()
		if name == "state" {
			r.stateSet = true
		}
		if typesafeDecisionRequestKnownFields[name] {
			return true
		}
		if r.ExtraParams == nil {
			r.ExtraParams = make(map[string]interface{})
		}
		var buf bytes.Buffer
		if err := json.Compact(&buf, []byte(value.Raw)); err == nil {
			r.ExtraParams[name] = json.RawMessage(buf.Bytes())
		} else {
			r.ExtraParams[name] = json.RawMessage(value.Raw)
		}
		return true
	})
	return nil
}

// GetExtraParams implements providerUtils.RequestBodyWithExtraParams: the
// native extensions the SDK forwarded, merged onto the wire body by
// CheckContextAndGetRequestBody when passthrough is enabled.
func (r *TypesafeDecisionRequest) GetExtraParams() map[string]interface{} {
	return r.ExtraParams
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

	// Laya-specific fields
	AnswerConfidence    *float64        `json:"answer_confidence,omitempty"`
	Action              json.RawMessage `json:"action,omitempty"`
	Abstention          *string         `json:"abstention,omitempty"`
	AbstentionThreshold *float64        `json:"abstention_threshold,omitempty"`
	LowConfidence       *bool           `json:"low_confidence,omitempty"`
}

// TypesafeUsage reports token consumption. Typesafe bills input tokens only.
type TypesafeUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`

	// Laya-specific fields
	StateTokens        *int     `json:"state_tokens,omitempty"`
	StateTokensDropped *int     `json:"state_tokens_dropped,omitempty"`
	Truncated          *bool    `json:"truncated,omitempty"`
	TruncatedQuestions []string `json:"truncated_questions,omitempty"`
}

// TypesafeDecisionResponse is the body of a successful systemone response.
type TypesafeDecisionResponse struct {
	Model   string                    `json:"model"`
	Answers map[string]TypesafeAnswer `json:"answers"`
	Usage   *TypesafeUsage            `json:"usage,omitempty"`

	// Laya-specific fields
	Routing json.RawMessage `json:"routing,omitempty"`
}

// TypesafeError is the JSON error body Typesafe returns on 400/401/422/429/529.
type TypesafeError struct {
	Message string      `json:"message,omitempty"`
	Detail  interface{} `json:"detail,omitempty"` // string, {error_type, message} object, or validation array
	Error   *struct {
		Message string `json:"message,omitempty"`
	} `json:"error,omitempty"`
	Errors []TypesafeEnvelopeError `json:"errors,omitempty"` // Cloudflare Workers AI REST envelope
}

// TypesafeEnvelopeError is one entry of the errors array in a Cloudflare
// Workers AI REST envelope, which serves Jev-compatible models such as Clef.
type TypesafeEnvelopeError struct {
	Code    int    `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}
