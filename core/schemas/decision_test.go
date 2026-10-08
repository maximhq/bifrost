package schemas

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDecisionWireUnchangedWithoutNewFields pins that a request and answer
// that set none of the ordered-request fields encode exactly as before, so
// /v1/decisions callers see no new keys.
func TestDecisionWireUnchangedWithoutNewFields(t *testing.T) {
	question, err := MarshalSorted(DecisionQuestion{Kind: DecisionKindChoice, Instructions: "Pick", Criteria: map[string]any{"a": "first"}})
	require.NoError(t, err)
	assert.JSONEq(t, `{"kind":"choice","instructions":"Pick","criteria":{"a":"first"}}`, string(question))

	request, err := MarshalSorted(BifrostDecisionRequest{Provider: Typesafe, Model: "jev-1.13.0", State: "s", Questions: map[string]DecisionQuestion{}})
	require.NoError(t, err)
	assert.JSONEq(t, `{"provider":"typesafe","model":"jev-1.13.0","state":"s","questions":{}}`, string(request))

	answer, err := MarshalSorted(DecisionAnswer{Kind: DecisionKindNoul, Value: 0.25, Probabilities: map[string]float64{"true": 0.25, "false": 0.75}})
	require.NoError(t, err)
	assert.JSONEq(t, `{"kind":"noul","value":0.25,"probabilities":{"false":0.75,"true":0.25}}`, string(answer))

	unnamed, err := MarshalSorted(DecisionQuestion{Kind: DecisionKindNoul, Unnamed: true})
	require.NoError(t, err)
	assert.JSONEq(t, `{"kind":"noul"}`, string(unnamed), "the generated-name marker never reaches the wire")
}

// TestDecisionQuestionLabelledCriteria pins how level labels are folded into
// score level descriptions for a provider with no labels: an index label or
// no label leaves the description alone, and criteria without labels or that
// are not a list come back unchanged.
func TestDecisionQuestionLabelledCriteria(t *testing.T) {
	question := DecisionQuestion{
		Kind:        DecisionKindScore,
		Criteria:    []any{"kept", nil, "nothing works", map[string]any{"scope": "all"}, "unlabelled"},
		LevelLabels: []string{"0", "Workaround", "Blocked", "Outage"},
	}
	assert.Equal(t, []any{
		"kept",
		"Workaround",
		"Blocked: nothing works",
		map[string]any{"label": "Outage", "description": map[string]any{"scope": "all"}},
		"unlabelled",
	}, question.LabelledCriteria())
	assert.Equal(t, []any{"kept", nil, "nothing works", map[string]any{"scope": "all"}, "unlabelled"}, question.Criteria, "the question's own criteria are not modified")

	labelsOnly := DecisionQuestion{Kind: DecisionKindScore, LevelLabels: []string{"Low", "High"}}
	assert.Equal(t, []any{"Low", "High"}, labelsOnly.LabelledCriteria())

	typed := DecisionQuestion{Kind: DecisionKindScore, Criteria: []string{"a", "b"}, LevelLabels: []string{"Low", "High"}}
	assert.Equal(t, []any{"Low: a", "High: b"}, typed.LabelledCriteria())

	plain := []string{"a", "b"}
	assert.Equal(t, plain, DecisionQuestion{Kind: DecisionKindScore, Criteria: plain}.LabelledCriteria())
	notList := map[string]any{"a": "b"}
	assert.Equal(t, notList, DecisionQuestion{Kind: DecisionKindScore, Criteria: notList, LevelLabels: []string{"x"}}.LabelledCriteria())
	choice := map[string]any{"a": "b"}
	assert.Equal(t, choice, DecisionQuestion{Kind: DecisionKindChoice, Criteria: choice, LevelLabels: []string{"x"}}.LabelledCriteria())
}

// TestDecisionQuestionChoiceValue pins that only a BoolChoices question turns
// the "true" and "false" options into booleans.
func TestDecisionQuestionChoiceValue(t *testing.T) {
	boolean := DecisionQuestion{Kind: DecisionKindChoice, BoolChoices: true}
	assert.Equal(t, true, boolean.ChoiceValue("true"))
	assert.Equal(t, false, boolean.ChoiceValue("false"))
	assert.Equal(t, "other", boolean.ChoiceValue("other"))
	assert.Equal(t, "true", DecisionQuestion{Kind: DecisionKindChoice}.ChoiceValue("true"))
}

// TestDecisionStateHasImage pins that only a state given as messages with an
// image part counts, so a structured state shaped like messages is untouched.
func TestDecisionStateHasImage(t *testing.T) {
	withImage := []DecisionInputMessage{{Role: "user", Content: DecisionInputContent{Parts: []DecisionInputPart{
		{Type: DecisionInputPartTypeText, Text: Ptr("look")},
		{Type: DecisionInputPartTypeImage, ImageURL: Ptr("data:image/png;base64,AAAA")},
	}}}}
	assert.True(t, DecisionStateHasImage(withImage))
	assert.False(t, DecisionStateHasImage([]DecisionInputMessage{{Role: "user", Content: DecisionInputContent{Text: Ptr("text only")}}}))
	assert.False(t, DecisionStateHasImage([]any{map[string]any{"type": "input_image", "image_url": "x"}}))
	assert.False(t, DecisionStateHasImage("text"))
	assert.False(t, DecisionStateHasImage(nil))
}

// TestDecisionInputMessagesKeepParts pins both message content forms, the
// inline image part, and that an unmodelled part type is re-emitted unchanged.
// The payload is synthetic.
func TestDecisionInputMessagesKeepParts(t *testing.T) {
	body := `[{"role":"user","content":[
		{"type":"input_text","text":"Inspect the product."},
		{"type":"input_image","image_url":"data:image/png;base64,AAAA"},
		{"type":"input_audio","text":{"transcript":"hi"},"audio":{"data":"BBBB"}}
	]},{"role":"user","content":"And this note."}]`
	var messages []DecisionInputMessage
	require.NoError(t, Unmarshal([]byte(body), &messages))
	require.Len(t, messages, 2)
	parts := messages[0].Content.Parts
	require.Len(t, parts, 3)
	assert.Equal(t, DecisionInputPartTypeText, parts[0].Type)
	require.NotNil(t, parts[1].ImageURL)
	assert.Equal(t, "input_audio", parts[2].Type)
	require.NotNil(t, messages[1].Content.Text)

	out, err := Marshal(messages)
	require.NoError(t, err)
	assert.JSONEq(t, body, string(out), "an unfamiliar part type must survive unchanged, even with clashing field shapes")

	_, err = Marshal(DecisionInputContent{Text: Ptr("a"), Parts: []DecisionInputPart{}})
	assert.Error(t, err, "text and parts together are ambiguous")
}

// TestDecisionAnswerRecognition pins that refusals are modelled answers, that
// an answer of an unknown kind is never recognized, and that an unrecognized
// provider answer is re-emitted verbatim.
func TestDecisionAnswerRecognition(t *testing.T) {
	assert.True(t, DecisionAnswer{Kind: DecisionKindRefusal}.IsRecognized())
	assert.True(t, DecisionAnswer{Kind: DecisionKindScore}.IsRecognized())
	assert.False(t, DecisionAnswer{Kind: "future"}.IsRecognized())

	raw := json.RawMessage(`{"type":"ranking","name":"future","order":["a","b"]}`)
	unknown := NewUnrecognizedDecisionAnswer("ranking", raw)
	assert.False(t, unknown.IsRecognized())
	assert.False(t, NewUnrecognizedDecisionAnswer(DecisionKindNoul, raw).IsRecognized(), "a wrapped answer stays unrecognized whatever its kind")
	out, err := MarshalSorted(map[string]DecisionAnswer{"future": unknown})
	require.NoError(t, err)
	assert.JSONEq(t, `{"future":{"type":"ranking","name":"future","order":["a","b"]}}`, string(out))
}
