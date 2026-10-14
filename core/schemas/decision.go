package schemas

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// BifrostDecisionRequest asks a list of questions about some evidence (Input).
// It is the one shape every decision route converts into and every provider
// converts out of: a route speaking another wire shape (Typesafe's
// named-question map, OpenAI's Decisions API) converts at its edge, and a
// provider that cannot express part of a request rejects or flattens it only
// on its own attempt.
type BifrostDecisionRequest struct {
	Provider         ModelProvider          `json:"provider"`
	Model            string                 `json:"model"`
	Input            DecisionInput          `json:"input"`
	Questions        []DecisionQuestion     `json:"questions"`                   // in the order asked
	SafetyIdentifier *string                `json:"safety_identifier,omitempty"` // stable end-user identifier, forwarded by providers that accept one
	Fallbacks        []Fallback             `json:"fallbacks,omitempty"`
	RawRequestBody   []byte                 `json:"-"`
	ExtraParams      map[string]interface{} `json:"-"` // native extensions; sent only under the passthrough-extra-params flag
}

// GetRawRequestBody returns the raw request body for the decision request.
func (r *BifrostDecisionRequest) GetRawRequestBody() []byte {
	return r.RawRequestBody
}

// DecisionType identifies how a question is decided, and what an answer is.
type DecisionType string

const (
	// DecisionTypePredicate yields the probability that a condition is true.
	// Typesafe calls it "noul".
	DecisionTypePredicate DecisionType = "predicate"
	// DecisionTypeChoice yields one of the question's choices.
	DecisionTypeChoice DecisionType = "choice"
	// DecisionTypeScore yields a probability-weighted rubric score over the
	// question's level indexes, so it may fall between levels.
	DecisionTypeScore DecisionType = "score"
	// DecisionTypeRefusal marks a question the model declined to answer. It
	// only appears on answers.
	DecisionTypeRefusal DecisionType = "refusal"
)

// IsQuestionType reports whether the type is valid on a question.
func (t DecisionType) IsQuestionType() bool {
	return t == DecisionTypePredicate || t == DecisionTypeChoice || t == DecisionTypeScore
}

// DecisionInput is the evidence a decision is judged on. At most one member is
// set: Text, Messages carrying text and inline image parts, or Structured (a
// JSON object or array, such as a Typesafe state). None set is an explicit
// null, which providers that accept one forward as such.
//
// On the wire it is a string, an array of messages, an object, or null. An
// array always reads back as messages, so a structured array is written but
// never read; routes that accept array-shaped state build the input in Go.
type DecisionInput struct {
	Text       *string
	Messages   []DecisionInputMessage
	Structured interface{}
}

// IsEmpty reports whether no member is set, that is, whether the input is an
// explicit null.
func (in DecisionInput) IsEmpty() bool {
	return in.Text == nil && in.Messages == nil && in.Structured == nil
}

// Validate rejects an input with more than one member set.
func (in DecisionInput) Validate() error {
	set := 0
	for _, present := range []bool{in.Text != nil, in.Messages != nil, in.Structured != nil} {
		if present {
			set++
		}
	}
	if set > 1 {
		return fmt.Errorf("decision input carries more than one of text, messages, and structured input; send exactly one")
	}
	return nil
}

// NonTextPartType returns the type of the first message part that is not
// text (an image, or a part of a type Bifrost does not model, such as audio),
// "untyped" for a part sent without a type, or "" when every part is text. A
// provider that reads only text or JSON must reject such a part rather than
// send its encoding as text, which would let the model answer without reading
// the evidence.
func (in DecisionInput) NonTextPartType() string {
	for _, message := range in.Messages {
		for _, part := range message.Content.Parts {
			if part.Type == "" {
				return "untyped"
			}
			if part.Type != DecisionInputPartTypeText {
				return part.Type
			}
		}
	}
	return ""
}

// MarshalJSON emits the set member, or null.
func (in DecisionInput) MarshalJSON() ([]byte, error) {
	if err := in.Validate(); err != nil {
		return nil, err
	}
	switch {
	case in.Text != nil:
		return Marshal(*in.Text)
	case in.Messages != nil:
		return Marshal(in.Messages)
	case in.Structured != nil:
		return Marshal(in.Structured)
	}
	return Marshal(nil)
}

// UnmarshalJSON reads a string as text, an array as messages, an object as
// structured input, and null as an empty input.
func (in *DecisionInput) UnmarshalJSON(data []byte) error {
	*in = DecisionInput{}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	switch trimmed[0] {
	case '"':
		var text string
		if err := Unmarshal(trimmed, &text); err != nil {
			return err
		}
		in.Text = &text
		return nil
	case '[':
		var messages []DecisionInputMessage
		if err := Unmarshal(trimmed, &messages); err != nil {
			return fmt.Errorf("decision input array must be a list of messages: %w", err)
		}
		in.Messages = messages
		return nil
	case '{':
		var structured map[string]interface{}
		if err := Unmarshal(trimmed, &structured); err != nil {
			return err
		}
		in.Structured = structured
		return nil
	}
	return fmt.Errorf("decision input must be a string, an array of messages, or an object")
}

// DecisionInputMessage is one message of a decision input. Type is OpenAI's
// optional item type ("message"), kept as sent.
type DecisionInputMessage struct {
	Type    *string              `json:"type,omitempty"`
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
	// DecisionInputPartTypeImage is an image part: a base64 data URL or an
	// HTTP(S) URL.
	DecisionInputPartTypeImage = "input_image"
)

// DecisionInputPart is one content part of a decision input message: text or
// an image. A part of another type keeps only its type, by which providers
// reject it.
type DecisionInputPart struct {
	Type     string  `json:"type"`
	Text     *string `json:"text,omitempty"`
	ImageURL *string `json:"image_url,omitempty"` // base64 data URL or HTTP(S) URL
	Detail   *string `json:"detail,omitempty"`    // image detail level: "low", "high", "auto", or "original"
}

// DecisionQuestion is one question of a decision request. Name is optional;
// answers carry it back. Criteria applies to predicate questions only, Choices
// to choice questions, and Levels to score questions.
type DecisionQuestion struct {
	Type         DecisionType      `json:"type"`
	Name         *string           `json:"name,omitempty"`
	Instructions *DecisionText     `json:"instructions,omitempty"`
	Criteria     *DecisionCriteria `json:"criteria,omitempty"`
	Choices      []DecisionChoice  `json:"choices,omitempty"`
	Levels       []DecisionLevel   `json:"levels,omitempty"`
}

// DecisionText is a piece of question text: a string, or a structured value
// (an object or array) that Typesafe-family models accept as given and other
// providers receive as JSON text. At most one member is set; with neither set
// it is an explicit null.
type DecisionText struct {
	Text       *string
	Structured interface{}
}

// NewDecisionText returns a DecisionText holding text.
func NewDecisionText(text string) *DecisionText {
	return &DecisionText{Text: &text}
}

// Value returns the text, the structured value, or nil, for a provider that
// sends it on as a plain JSON value.
func (t *DecisionText) Value() interface{} {
	if t == nil {
		return nil
	}
	if t.Text != nil {
		return *t.Text
	}
	return t.Structured
}

// MarshalJSON emits the text, the structured value, or null.
func (t DecisionText) MarshalJSON() ([]byte, error) {
	if t.Text != nil && t.Structured != nil {
		return nil, fmt.Errorf("both decision text and structured value are set; only one should be non-nil")
	}
	return Marshal(t.Value())
}

// UnmarshalJSON reads a string as text and an object or array as a structured
// value; null leaves both empty. Any other value is rejected.
func (t *DecisionText) UnmarshalJSON(data []byte) error {
	*t = DecisionText{}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}
	switch trimmed[0] {
	case '"':
		var text string
		if err := Unmarshal(trimmed, &text); err != nil {
			return err
		}
		t.Text = &text
		return nil
	case '{', '[':
		var structured interface{}
		if err := Unmarshal(trimmed, &structured); err != nil {
			return err
		}
		t.Structured = structured
		return nil
	}
	return fmt.Errorf("decision text must be a string, object, or array")
}

// DecisionCriteria describes a predicate question's outcomes. Typesafe-family
// models read it; other providers fold it into the instructions. A nil side
// was not sent; a side that is an empty DecisionText was sent as null and is
// forwarded as null.
type DecisionCriteria struct {
	True  *DecisionText `json:"true,omitempty"`
	False *DecisionText `json:"false,omitempty"`
}

// UnmarshalJSON decodes the two sides, keeping a side sent as null apart from
// an absent one. A plain decode sets a null pointer field to nil without
// consulting DecisionText, which would forward {"false": null} to Typesafe as
// if "false" had not been sent; the map form keeps it, and so must this.
// Keys other than "true" and "false" are ignored.
func (c *DecisionCriteria) UnmarshalJSON(data []byte) error {
	var sides map[string]json.RawMessage
	if err := Unmarshal(data, &sides); err != nil {
		return err
	}
	*c = DecisionCriteria{}
	for key, side := range map[string]**DecisionText{"true": &c.True, "false": &c.False} {
		raw, present := sides[key]
		if !present {
			continue
		}
		text := &DecisionText{}
		if err := text.UnmarshalJSON(raw); err != nil {
			return fmt.Errorf("criteria %q: %w", key, err)
		}
		*side = text
	}
	return nil
}

// DecisionChoice is one selectable value of a choice question. Value is a
// string or a boolean; Description is optional.
type DecisionChoice struct {
	Value       DecisionScalar `json:"value"`
	Description *DecisionText  `json:"description,omitempty"`
}

// Key is the choice's option name for a provider that keys choices by name:
// the string itself, or "true" or "false" for a boolean.
func (c DecisionChoice) Key() (string, error) {
	switch {
	case c.Value.Str != nil && c.Value.Bool == nil && c.Value.Num == nil:
		return *c.Value.Str, nil
	case c.Value.Bool != nil && c.Value.Str == nil && c.Value.Num == nil:
		return strconv.FormatBool(*c.Value.Bool), nil
	}
	return "", fmt.Errorf("choice value must be exactly one of a string or a boolean")
}

// DecisionLevel is one ordered level of a score question. Label names the
// level; a level written without one is labelled by its index. Description is
// optional.
type DecisionLevel struct {
	Label       string        `json:"label"`
	Description *DecisionText `json:"description,omitempty"`
}

// DecisionQuestionNames returns a distinct name for each question, in order:
// its own name, or "question_<position>" for an unnamed one, suffixed until it
// collides with no other name. Providers that key questions by name send them
// under these names and match answers back by them; the names are
// deterministic, so recomputing them gives the same result. A name used by
// more than one question is an error, since their answers could not be told
// apart.
func DecisionQuestionNames(questions []DecisionQuestion) ([]string, error) {
	names := make([]*string, len(questions))
	for i, question := range questions {
		names[i] = question.Name
	}
	return assignDecisionNames(names)
}

// DecisionAnswerNames returns a distinct name for each answer, in order: its
// own name, or the name DecisionQuestionNames gives an unnamed question at the
// same position. Answers whose names repeat keep the first; the rest take
// generated names, since a response is rendered rather than rejected.
func DecisionAnswerNames(answers []DecisionAnswer) []string {
	names := make([]*string, len(answers))
	seen := make(map[string]struct{}, len(answers))
	for i, answer := range answers {
		if answer.Name == nil {
			continue
		}
		if _, repeated := seen[*answer.Name]; repeated {
			continue
		}
		seen[*answer.Name] = struct{}{}
		names[i] = answer.Name
	}
	assigned, _ := assignDecisionNames(names)
	return assigned
}

// assignDecisionNames names each position: its own name when it has one, or a
// generated "question_<position>" that collides with no other name. A name
// given to more than one position is an error.
func assignDecisionNames(names []*string) ([]string, error) {
	taken := make(map[string]struct{}, len(names))
	for _, name := range names {
		if name == nil {
			continue
		}
		if _, seen := taken[*name]; seen {
			return nil, fmt.Errorf("question name %q is used more than once", *name)
		}
		taken[*name] = struct{}{}
	}
	assigned := make([]string, len(names))
	for i, name := range names {
		if name != nil {
			assigned[i] = *name
			continue
		}
		candidate := fmt.Sprintf("question_%d", i+1)
		for suffix := 2; ; suffix++ {
			if _, seen := taken[candidate]; !seen {
				break
			}
			candidate = fmt.Sprintf("question_%d_%d", i+1, suffix)
		}
		taken[candidate] = struct{}{}
		assigned[i] = candidate
	}
	return assigned, nil
}

// DecisionScalar is a JSON string, boolean, or number. Choice values are a
// string or a boolean; the value of a score probability entry is a number.
type DecisionScalar struct {
	Str  *string
	Bool *bool
	Num  *float64
}

// Key is the scalar as a distribution key: the string, "true"/"false", or the
// number in decimal form. A scalar with no value has none.
func (s DecisionScalar) Key() (string, bool) {
	switch {
	case s.Str != nil:
		return *s.Str, true
	case s.Bool != nil:
		return strconv.FormatBool(*s.Bool), true
	case s.Num != nil:
		return strconv.FormatFloat(*s.Num, 'f', -1, 64), true
	}
	return "", false
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
		return Marshal(*s.Str)
	case s.Bool != nil:
		return Marshal(*s.Bool)
	case s.Num != nil:
		return Marshal(*s.Num)
	}
	return Marshal(nil)
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

// DecisionProbability is one entry of an answer's probability distribution. For
// a choice answer Value is the option value; for a score answer it is the
// numeric level index and Label names the level.
type DecisionProbability struct {
	Value       DecisionScalar `json:"value"`
	Label       *string        `json:"label,omitempty"`
	Probability float64        `json:"probability"`
}

// DecisionAnswer is one answer of a decision response, in the order of the
// request's questions. Which of Probability, Choice, and Score is set follows
// Type; a refusal carries only Type and Name. Confidence, Probabilities, and
// Legend carry per-answer metadata when the provider supplies it; Legend
// echoes each score level's description verbatim, keyed by level index.
type DecisionAnswer struct {
	Type          DecisionType          `json:"type"`
	Name          *string               `json:"name,omitempty"`
	Probability   *float64              `json:"probability,omitempty"`
	Choice        *DecisionScalar       `json:"choice,omitempty"`
	Score         *float64              `json:"score,omitempty"`
	Probabilities []DecisionProbability `json:"probabilities,omitempty"`
	Confidence    *float64              `json:"confidence,omitempty"`
	Legend        map[string]any        `json:"legend,omitempty"`

	// Laya-specific fields
	AnswerConfidence    *float64        `json:"answer_confidence,omitempty"`    // calibrated probability of the reported answer
	Action              json.RawMessage `json:"action,omitempty"`               // action head, e.g. {"act_probability":1.0}; passed through untouched
	Abstention          *string         `json:"abstention,omitempty"`           // "passed" | "abstained" | "unevaluated" when min_confidence is set
	AbstentionThreshold *float64        `json:"abstention_threshold,omitempty"` // the min_confidence the answer was gated on
	LowConfidence       *bool           `json:"low_confidence,omitempty"`       // answer_confidence fell below abstention_threshold
}

// BifrostDecisionResponse represents the response from a decision request.
// Answers follow the request's question order.
type BifrostDecisionResponse struct {
	ID             string                     `json:"id,omitempty"`
	Model          string                     `json:"model"`
	Answers        []DecisionAnswer           `json:"answers"`
	Usage          *BifrostLLMUsage           `json:"usage,omitempty"`
	ExtraFields    BifrostResponseExtraFields `json:"extra_fields"`
	NativeResponse json.RawMessage            `json:"-"` // provider body verbatim for native drop-in routes; never serialized

	// Laya-specific fields
	Routing json.RawMessage `json:"routing,omitempty"` // checkpoint routing report (model, reason, detection); passed through untouched
}
