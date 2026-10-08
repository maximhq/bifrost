package schemas

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
)

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
	// DecisionKindRefusal marks a question the model declined to answer. It
	// only appears on answers, which then carry no value.
	DecisionKindRefusal DecisionKind = "refusal"
)

// DecisionQuestion is one named question in a decision request. Instructions
// accepts a string, object, or array, carried losslessly. Criteria is a map of
// descriptions for noul (only "true"/"false" keys) and choice (option ->
// description), and an ordered array of level descriptions for score.
//
// The optional fields carry what a request written as an ordered list (such as
// OpenAI's Decisions API) says beyond the map: its position, level labels, and
// boolean choice values. A provider that has no use for one ignores it.
type DecisionQuestion struct {
	Kind         DecisionKind `json:"kind"`
	Instructions interface{}  `json:"instructions,omitempty"`
	Criteria     interface{}  `json:"criteria,omitempty"`

	Order       *int     `json:"order,omitempty"`        // position in an ordered request; unset sorts by name
	LevelLabels []string `json:"level_labels,omitempty"` // score: a label per level, by index
	BoolChoices bool     `json:"bool_choices,omitempty"` // choice: the "true" and "false" options are booleans
	Unnamed     bool     `json:"-"`                      // the question was asked without a name; its key was generated
}

// BifrostDecisionRequest represents a request to evaluate state against a map
// of named questions. The shape mirrors Typesafe's System One endpoint, and
// every decision route and provider uses it: a route that speaks another
// shape converts into it, and a provider converts out of it to its own.
//
// State is a string, an object, an array, or null. Input given as messages
// with text and inline image parts is a []DecisionInputMessage, which only a
// provider that reads images sends as messages; others reject an image.
type BifrostDecisionRequest struct {
	Provider         ModelProvider               `json:"provider"`
	Model            string                      `json:"model"`
	State            interface{}                 `json:"state"`
	Questions        map[string]DecisionQuestion `json:"questions"`
	SafetyIdentifier *string                     `json:"safety_identifier,omitempty"` // stable end-user identifier, forwarded by providers that accept one
	Fallbacks        []Fallback                  `json:"fallbacks,omitempty"`
	RawRequestBody   []byte                      `json:"-"`
	ExtraParams      map[string]interface{}      `json:"-"` // native extensions; sent only under the passthrough-extra-params flag
}

// GetRawRequestBody returns the raw request body for the decision request.
func (r *BifrostDecisionRequest) GetRawRequestBody() []byte {
	return r.RawRequestBody
}

// DecisionAnswer is one evaluated answer. Value carries the decided value for
// the question's kind: a number in [0,1] for noul, an option for choice (a
// boolean for a BoolChoices question), a numeric rubric score for score, and
// nothing for a refusal. Confidence, Probabilities, and Legend carry per-field
// metadata when the provider supplies it. Legend echoes each score level's
// description verbatim, so its values are strings, objects, or arrays -
// whatever the criteria carried. An answer of a type Bifrost does not model is
// kept verbatim (see NewUnrecognizedDecisionAnswer) and re-emitted unchanged.
type DecisionAnswer struct {
	Kind          DecisionKind       `json:"kind"`
	Value         interface{}        `json:"value"`
	Confidence    *float64           `json:"confidence,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Legend        map[string]any     `json:"legend,omitempty"`

	// Laya-specific fields
	AnswerConfidence    *float64        `json:"answer_confidence,omitempty"`    // calibrated probability of the reported answer
	Action              json.RawMessage `json:"action,omitempty"`               // action head, e.g. {"act_probability":1.0}; passed through untouched
	Abstention          *string         `json:"abstention,omitempty"`           // "passed" | "abstained" | "unevaluated" when min_confidence is set
	AbstentionThreshold *float64        `json:"abstention_threshold,omitempty"` // the min_confidence the answer was gated on
	LowConfidence       *bool           `json:"low_confidence,omitempty"`       // answer_confidence fell below abstention_threshold

	raw json.RawMessage // verbatim body of an answer whose type is not modelled
}

// NewUnrecognizedDecisionAnswer wraps a provider answer of a type Bifrost does
// not model, so it is carried and re-emitted verbatim rather than rejected.
func NewUnrecognizedDecisionAnswer(kind DecisionKind, raw json.RawMessage) DecisionAnswer {
	return DecisionAnswer{Kind: kind, raw: append(json.RawMessage(nil), raw...)}
}

// MarshalJSON re-emits an unrecognized answer verbatim and otherwise marshals
// the typed fields with sorted keys, the encoding the HTTP layer uses, so an
// answer renders the same nested or not.
func (a DecisionAnswer) MarshalJSON() ([]byte, error) {
	if a.raw != nil {
		return a.raw, nil
	}
	type alias DecisionAnswer
	return MarshalSorted(alias(a))
}

// IsRecognized reports whether the answer is of a modelled kind. An
// unrecognized answer must never be read as an approval or completion of its
// question.
func (a DecisionAnswer) IsRecognized() bool {
	switch a.Kind {
	case DecisionKindNoul, DecisionKindChoice, DecisionKindScore, DecisionKindRefusal:
		return a.raw == nil
	}
	return false
}

// RawJSON returns the verbatim body of an unrecognized answer, or nil.
func (a DecisionAnswer) RawJSON() json.RawMessage {
	return a.raw
}

// BifrostDecisionResponse represents the response from a decision request.
// Answers is keyed by question identifier; every requested question produces
// an answer.
type BifrostDecisionResponse struct {
	ID             string                     `json:"id,omitempty"`
	Model          string                     `json:"model"`
	Answers        map[string]DecisionAnswer  `json:"answers"`
	Usage          *BifrostLLMUsage           `json:"usage,omitempty"`
	ExtraFields    BifrostResponseExtraFields `json:"extra_fields"`
	NativeResponse json.RawMessage            `json:"-"` // provider body verbatim for native drop-in routes; never serialized

	// Laya-specific fields
	Routing json.RawMessage `json:"routing,omitempty"` // checkpoint routing report (model, reason, detection); passed through untouched
}

// ChoiceValue returns the answer value for a choice option: the option itself,
// or its boolean for a BoolChoices question.
func (q DecisionQuestion) ChoiceValue(option string) interface{} {
	if q.BoolChoices && (option == "true" || option == "false") {
		return option == "true"
	}
	return option
}

// LabelledCriteria returns the question's criteria with its level labels
// folded into the score level descriptions, for a provider whose wire shape
// has no labels. A level without a description becomes its label, a text
// description becomes "label: description", and a structured one becomes a
// {label, description} object. Criteria without labels, or that are not an
// ordered list of levels, are returned unchanged for the provider to judge.
func (q DecisionQuestion) LabelledCriteria() interface{} {
	if q.Kind != DecisionKindScore || len(q.LevelLabels) == 0 {
		return q.Criteria
	}
	var levels []interface{}
	if q.Criteria == nil {
		levels = make([]interface{}, len(q.LevelLabels))
	} else {
		value := reflect.ValueOf(q.Criteria)
		if value.Kind() != reflect.Slice && value.Kind() != reflect.Array {
			return q.Criteria
		}
		levels = make([]interface{}, value.Len())
		for i := range levels {
			levels[i] = value.Index(i).Interface()
		}
	}
	for i, label := range q.LevelLabels {
		if i >= len(levels) || label == "" || label == strconv.Itoa(i) {
			continue
		}
		switch description := levels[i].(type) {
		case nil:
			levels[i] = label
		case string:
			levels[i] = label + ": " + description
		default:
			levels[i] = map[string]interface{}{"label": label, "description": description}
		}
	}
	return levels
}

// DecisionStateHasImage reports whether a state given as messages carries an
// inline image, which a provider that reads only text or JSON must reject
// rather than send as text.
func DecisionStateHasImage(state interface{}) bool {
	messages, ok := state.([]DecisionInputMessage)
	if !ok {
		return false
	}
	for _, message := range messages {
		for _, part := range message.Content.Parts {
			if part.Type == DecisionInputPartTypeImage {
				return true
			}
		}
	}
	return false
}

// DecisionInputMessage is one message of a decision state given as messages.
type DecisionInputMessage struct {
	Role    string               `json:"role"`
	Content DecisionInputContent `json:"content"`
}

// DecisionInputContent is a message body: a text string, or an array of parts.
// Exactly one member is set.
type DecisionInputContent struct {
	Text  *string
	Parts []DecisionInputPart
}

// MarshalJSON emits the text string or the part array.
func (c DecisionInputContent) MarshalJSON() ([]byte, error) {
	if c.Text != nil && c.Parts != nil {
		return nil, fmt.Errorf("both decision message text and parts are set; only one should be non-nil")
	}
	if c.Text != nil {
		return Marshal(*c.Text)
	}
	if c.Parts != nil {
		return Marshal(c.Parts)
	}
	return Marshal(nil)
}

// UnmarshalJSON accepts a string or an array of parts.
func (c *DecisionInputContent) UnmarshalJSON(data []byte) error {
	*c = DecisionInputContent{}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	if trimmed[0] == '"' {
		var text string
		if err := Unmarshal(trimmed, &text); err == nil {
			c.Text = &text
			return nil
		}
	} else if trimmed[0] == '[' {
		var parts []DecisionInputPart
		if err := Unmarshal(trimmed, &parts); err == nil {
			c.Parts = parts
			return nil
		}
	}
	return fmt.Errorf("decision message content must be a string or an array of parts")
}

// Decision input part types.
const (
	// DecisionInputPartTypeText is a text part.
	DecisionInputPartTypeText = "input_text"
	// DecisionInputPartTypeImage is an inline base64 image part.
	DecisionInputPartTypeImage = "input_image"
)

// DecisionInputPart is one content part of a decision input message. A part of
// a type this schema does not model is retained verbatim and re-emitted
// unchanged, so a newer client part type is neither rejected nor altered.
type DecisionInputPart struct {
	Type     string  `json:"type"`
	Text     *string `json:"text,omitempty"`
	ImageURL *string `json:"image_url,omitempty"` // inline base64 data URL

	raw json.RawMessage // verbatim body of a part whose type is not modelled
}

// MarshalJSON re-emits an unmodelled part verbatim and otherwise marshals the
// typed fields.
func (p DecisionInputPart) MarshalJSON() ([]byte, error) {
	if p.raw != nil {
		return p.raw, nil
	}
	type alias DecisionInputPart
	return Marshal(alias(p))
}

// UnmarshalJSON decodes the modelled part types and retains any other part
// verbatim. The type is read first, so an unfamiliar part is kept whole even
// when its fields do not fit the modelled ones.
func (p *DecisionInputPart) UnmarshalJSON(data []byte) error {
	var header struct {
		Type string `json:"type"`
	}
	if err := Unmarshal(data, &header); err != nil {
		return err
	}
	if header.Type != DecisionInputPartTypeText && header.Type != DecisionInputPartTypeImage {
		*p = DecisionInputPart{Type: header.Type, raw: append(json.RawMessage(nil), data...)}
		return nil
	}
	type alias DecisionInputPart
	var decoded alias
	if err := Unmarshal(data, &decoded); err != nil {
		return err
	}
	*p = DecisionInputPart(decoded)
	return nil
}
