package schemas

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// BifrostDecisionRequest represents a request to evaluate some evidence against
// a set of questions.
//
// A request is written in one of two forms, and must use exactly one:
//
//   - Map form: State plus the Questions map, keyed by question name. This is
//     TypeSafe's System One shape and what POST /v1/decisions accepts.
//   - Ordered form: Input plus OrderedQuestions, a list in request order. This is
//     OpenAI's Decisions API shape. It keeps what a map cannot: question order,
//     optional question names, labeled score levels, boolean choice values, and
//     image input.
//
// Use UsesMapForm and UsesOrderedForm to tell them apart. Today only OpenAI
// serves the ordered form (see core.rejectUnservableOrderedDecision); every
// other decision provider and the emulation path read the map form.
type BifrostDecisionRequest struct {
	Provider         ModelProvider               `json:"provider"`
	Model            string                      `json:"model"`
	State            interface{}                 `json:"state"`                       // map form: string, object, or array
	Questions        map[string]DecisionQuestion `json:"questions"`                   // map form: questions keyed by name
	Input            *DecisionInput              `json:"input,omitempty"`             // ordered form: text, or user messages with text and image parts
	OrderedQuestions []DecisionOrderedQuestion   `json:"ordered_questions,omitempty"` // ordered form: questions in request order
	SafetyIdentifier *string                     `json:"safety_identifier,omitempty"` // ordered form: stable end-user identifier forwarded to the provider
	Fallbacks        []Fallback                  `json:"fallbacks,omitempty"`
	RawRequestBody   []byte                      `json:"-"`
	ExtraParams      map[string]interface{}      `json:"-"` // native extensions; sent only under the passthrough-extra-params flag
}

// UsesOrderedForm reports whether the request is written in the ordered form
// (Input and OrderedQuestions) rather than the map form (State and Questions).
// It is true as soon as either ordered field is set.
func (r *BifrostDecisionRequest) UsesOrderedForm() bool {
	return r != nil && (r.Input != nil || len(r.OrderedQuestions) > 0)
}

// UsesMapForm reports whether the request is written in the map form, that is,
// whether it carries a non-empty Questions map. An explicit null State alone
// does not count: it is a valid State value, not a sign of the form. A request
// that reports true for both UsesMapForm and UsesOrderedForm is ambiguous and
// is rejected at the core entry point.
func (r *BifrostDecisionRequest) UsesMapForm() bool {
	return r != nil && len(r.Questions) > 0
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

	// Laya-specific fields
	AnswerConfidence    *float64        `json:"answer_confidence,omitempty"`    // calibrated probability of the reported answer
	Action              json.RawMessage `json:"action,omitempty"`               // action head, e.g. {"act_probability":1.0}; passed through untouched
	Abstention          *string         `json:"abstention,omitempty"`           // "passed" | "abstained" | "unevaluated" when min_confidence is set
	AbstentionThreshold *float64        `json:"abstention_threshold,omitempty"` // the min_confidence the answer was gated on
	LowConfidence       *bool           `json:"low_confidence,omitempty"`       // answer_confidence fell below abstention_threshold
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

	// OrderedAnswers carries the answers to an ordered-form request, in the
	// order the questions were asked. It is set instead of Answers, never
	// alongside it.
	OrderedAnswers []DecisionOrderedAnswer `json:"ordered_answers,omitempty"`

	// Laya-specific fields
	Routing json.RawMessage `json:"routing,omitempty"` // checkpoint routing report (model, reason, detection); passed through untouched
}

// UsesOrderedForm reports whether the response answers an ordered-form request,
// that is, whether it carries OrderedAnswers instead of the Answers map.
func (r *BifrostDecisionResponse) UsesOrderedForm() bool {
	return r != nil && len(r.OrderedAnswers) > 0
}

// DecisionOrderedKind identifies the type of an ordered question or answer.
// The vocabulary mirrors OpenAI's Decisions API.
type DecisionOrderedKind string

const (
	// DecisionOrderedKindPredicate yields the probability that a condition is true.
	DecisionOrderedKindPredicate DecisionOrderedKind = "predicate"
	// DecisionOrderedKindChoice yields one of the supplied choice values.
	DecisionOrderedKindChoice DecisionOrderedKind = "choice"
	// DecisionOrderedKindScore yields a probability-weighted rubric score.
	DecisionOrderedKindScore DecisionOrderedKind = "score"
	// DecisionOrderedKindRefusal marks a question the model declined to answer.
	// It only appears on answers.
	DecisionOrderedKindRefusal DecisionOrderedKind = "refusal"
)

// IsQuestionKind reports whether the kind is valid on an ordered question.
func (k DecisionOrderedKind) IsQuestionKind() bool {
	return k == DecisionOrderedKindPredicate || k == DecisionOrderedKindChoice || k == DecisionOrderedKindScore
}

// isAnswerKind reports whether the kind is a recognized ordered answer type.
func (k DecisionOrderedKind) isAnswerKind() bool {
	return k.IsQuestionKind() || k == DecisionOrderedKindRefusal
}

// DecisionScalar is a JSON string, boolean, or number. Choice values are a
// string or a boolean; the value of a score probability entry is a number.
type DecisionScalar struct {
	Str  *string
	Bool *bool
	Num  *float64
}

// MarshalJSON emits whichever member is set, or null when none is.
func (s DecisionScalar) MarshalJSON() ([]byte, error) {
	set := 0
	for _, present := range []bool{s.Str != nil, s.Bool != nil, s.Num != nil} {
		if present {
			set++
		}
	}
	if set > 1 {
		return nil, fmt.Errorf("decision scalar has more than one value set; only one should be non-nil")
	}
	switch {
	case s.Str != nil:
		return MarshalSorted(*s.Str)
	case s.Bool != nil:
		return MarshalSorted(*s.Bool)
	case s.Num != nil:
		return MarshalSorted(*s.Num)
	}
	return MarshalSorted(nil)
}

// UnmarshalJSON decodes a JSON string, boolean, or number and rejects any
// other shape.
func (s *DecisionScalar) UnmarshalJSON(data []byte) error {
	*s = DecisionScalar{}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	var str string
	if err := Unmarshal(trimmed, &str); err == nil {
		s.Str = &str
		return nil
	}
	var b bool
	if err := Unmarshal(trimmed, &b); err == nil {
		s.Bool = &b
		return nil
	}
	var n float64
	if err := Unmarshal(trimmed, &n); err == nil {
		s.Num = &n
		return nil
	}
	return fmt.Errorf("decision scalar must be a string, boolean, or number")
}

// DecisionInput is the shared evidence of an ordered decision request: a text
// string, or user messages carrying text and inline image parts. Exactly one
// member is set.
type DecisionInput struct {
	Text     *string
	Messages []DecisionInputMessage
}

// MarshalJSON emits the text string or the message array.
func (in DecisionInput) MarshalJSON() ([]byte, error) {
	if in.Text != nil && in.Messages != nil {
		return nil, fmt.Errorf("both decision input text and messages are set; only one should be non-nil")
	}
	if in.Text != nil {
		return MarshalSorted(*in.Text)
	}
	if in.Messages != nil {
		return MarshalSorted(in.Messages)
	}
	return MarshalSorted(nil)
}

// UnmarshalJSON accepts a string or an array of messages.
func (in *DecisionInput) UnmarshalJSON(data []byte) error {
	*in = DecisionInput{}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	var text string
	if err := Unmarshal(trimmed, &text); err == nil {
		in.Text = &text
		return nil
	}
	var messages []DecisionInputMessage
	if err := Unmarshal(trimmed, &messages); err == nil {
		in.Messages = messages
		return nil
	}
	return fmt.Errorf("decision input must be a string or an array of messages")
}

// DecisionInputMessage is one user message of a decision input.
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
		return MarshalSorted(*c.Text)
	}
	if c.Parts != nil {
		return MarshalSorted(c.Parts)
	}
	return MarshalSorted(nil)
}

// UnmarshalJSON accepts a string or an array of parts.
func (c *DecisionInputContent) UnmarshalJSON(data []byte) error {
	*c = DecisionInputContent{}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	var text string
	if err := Unmarshal(trimmed, &text); err == nil {
		c.Text = &text
		return nil
	}
	var parts []DecisionInputPart
	if err := Unmarshal(trimmed, &parts); err == nil {
		c.Parts = parts
		return nil
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
	return MarshalSorted(alias(p))
}

// UnmarshalJSON decodes the modelled part types and retains any other part
// verbatim.
func (p *DecisionInputPart) UnmarshalJSON(data []byte) error {
	type alias DecisionInputPart
	var decoded alias
	if err := Unmarshal(data, &decoded); err != nil {
		return err
	}
	*p = DecisionInputPart(decoded)
	p.raw = nil
	if p.Type != DecisionInputPartTypeText && p.Type != DecisionInputPartTypeImage {
		p.raw = append(json.RawMessage(nil), data...)
	}
	return nil
}

// DecisionOrderedQuestion is one question of an ordered decision request. Name
// is optional; Choices applies to choice questions and Levels to score
// questions.
type DecisionOrderedQuestion struct {
	Type         DecisionOrderedKind    `json:"type"`
	Name         *string                `json:"name,omitempty"`
	Instructions string                 `json:"instructions"`
	Choices      []DecisionChoiceOption `json:"choices,omitempty"`
	Levels       []DecisionScoreLevel   `json:"levels,omitempty"`
}

// DecisionChoiceOption is one selectable value of a choice question. Value is
// a string or a boolean.
type DecisionChoiceOption struct {
	Value       DecisionScalar `json:"value"`
	Description *string        `json:"description,omitempty"`
}

// DecisionScoreLevel is one ordered level of a score question.
type DecisionScoreLevel struct {
	Label       string  `json:"label"`
	Description *string `json:"description,omitempty"`
}

// DecisionProbability is one entry of an answer's probability distribution. For
// a choice answer Value is the option value; for a score answer it is the
// numeric level index and Label names the level.
type DecisionProbability struct {
	Value       DecisionScalar `json:"value"`
	Label       *string        `json:"label,omitempty"`
	Probability float64        `json:"probability"`
}

// DecisionOrderedAnswer is one answer of an ordered decision response, in the
// order of the request's questions. Which of Probability, Choice, and Score is
// set follows Type; a refusal carries only Type and Name. An answer of a type
// this schema does not model is retained verbatim and re-emitted unchanged.
type DecisionOrderedAnswer struct {
	Type          DecisionOrderedKind   `json:"type"`
	Name          *string               `json:"name,omitempty"`
	Probability   *float64              `json:"probability,omitempty"`
	Choice        *DecisionScalar       `json:"choice,omitempty"`
	Score         *float64              `json:"score,omitempty"`
	Probabilities []DecisionProbability `json:"probabilities,omitempty"`
	Confidence    *float64              `json:"confidence,omitempty"`

	raw json.RawMessage // verbatim body of an answer whose type is not modelled
}

// MarshalJSON re-emits an unmodelled answer verbatim and otherwise marshals the
// typed fields.
func (a DecisionOrderedAnswer) MarshalJSON() ([]byte, error) {
	if a.raw != nil {
		return a.raw, nil
	}
	type alias DecisionOrderedAnswer
	return MarshalSorted(alias(a))
}

// UnmarshalJSON decodes the modelled answer types and retains any other answer
// verbatim, so an unfamiliar variant is preserved rather than rejected.
func (a *DecisionOrderedAnswer) UnmarshalJSON(data []byte) error {
	type alias DecisionOrderedAnswer
	var decoded alias
	if err := Unmarshal(data, &decoded); err != nil {
		return err
	}
	*a = DecisionOrderedAnswer(decoded)
	a.raw = nil
	if !a.Type.isAnswerKind() {
		a.raw = append(json.RawMessage(nil), data...)
	}
	return nil
}

// IsRecognized reports whether the answer is of a modelled type. An
// unrecognized answer is preserved verbatim and must never be read as an
// approval or completion of its question. The type is checked as well as the
// retained body so an answer built directly in Go is judged the same way as a
// decoded one.
func (a DecisionOrderedAnswer) IsRecognized() bool {
	return a.Type.isAnswerKind() && a.raw == nil
}
