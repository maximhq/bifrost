package schemas

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDecisionScalarKeepsStringAndBooleanDistinct pins that a boolean choice
// value is not collapsed into the string of the same spelling, in either
// direction, since a string-keyed map could not tell them apart.
func TestDecisionScalarKeepsStringAndBooleanDistinct(t *testing.T) {
	var asString, asBool, asNumber DecisionScalar
	require.NoError(t, asString.UnmarshalJSON([]byte(`"true"`)))
	require.NoError(t, asBool.UnmarshalJSON([]byte(`true`)))
	require.NoError(t, asNumber.UnmarshalJSON([]byte(`2`)))

	require.NotNil(t, asString.Str)
	assert.Nil(t, asString.Bool)
	require.NotNil(t, asBool.Bool)
	assert.Nil(t, asBool.Str)
	require.NotNil(t, asNumber.Num)
	assert.Equal(t, 2.0, *asNumber.Num)

	for in, scalar := range map[string]DecisionScalar{`"true"`: asString, `true`: asBool, `2`: asNumber} {
		out, err := scalar.MarshalJSON()
		require.NoError(t, err)
		assert.JSONEq(t, in, string(out))
	}

	var invalid DecisionScalar
	assert.Error(t, invalid.UnmarshalJSON([]byte(`{"a":1}`)), "an object is not a valid scalar")
}

// TestDecisionInputAcceptsTextAndMessages pins both input forms, the inline
// image part, and that an unmodelled part type is re-emitted unchanged.
func TestDecisionInputAcceptsTextAndMessages(t *testing.T) {
	var text DecisionInput
	require.NoError(t, text.UnmarshalJSON([]byte(`"I was charged twice."`)))
	require.NotNil(t, text.Text)
	assert.Nil(t, text.Messages)

	body := `[{"role":"user","content":[
		{"type":"input_text","text":"Inspect the product."},
		{"type":"input_image","image_url":"data:image/png;base64,AAAA"},
		{"type":"input_audio","audio":{"data":"BBBB","format":"wav"}}
	]}]`
	var messages DecisionInput
	require.NoError(t, messages.UnmarshalJSON([]byte(body)))
	require.Len(t, messages.Messages, 1)
	parts := messages.Messages[0].Content.Parts
	require.Len(t, parts, 3)
	assert.Equal(t, DecisionInputPartTypeText, parts[0].Type)
	require.NotNil(t, parts[1].ImageURL)
	assert.Equal(t, "data:image/png;base64,AAAA", *parts[1].ImageURL)

	out, err := messages.MarshalJSON()
	require.NoError(t, err)
	assert.JSONEq(t, body, string(out), "an unfamiliar part type must survive unchanged")

	both := DecisionInput{Text: Ptr("a"), Messages: []DecisionInputMessage{{Role: "user"}}}
	_, err = both.MarshalJSON()
	assert.Error(t, err, "text and messages together are ambiguous")
}

// TestDecisionOrderedAnswersKeepOrderAndVariants pins answer order, a refusal
// carrying only its name, optional names, and that an answer of an unfamiliar
// type is preserved and flagged instead of being read as an answer.
func TestDecisionOrderedAnswersKeepOrderAndVariants(t *testing.T) {
	body := `[
		{"type":"predicate","name":"visible_damage","probability":0.92},
		{"type":"choice","name":"department","choice":"billing","probabilities":[{"value":"billing","probability":0.95},{"value":"other","probability":0.05}],"confidence":0.93},
		{"type":"choice","choice":true,"probabilities":[{"value":true,"probability":0.7},{"value":false,"probability":0.3}],"confidence":0.7},
		{"type":"score","name":"severity","score":1.1,"probabilities":[{"value":0,"label":"Cosmetic","probability":0.1},{"value":1,"label":"Workaround available","probability":0.7},{"value":2,"label":"Fully blocked","probability":0.2}],"confidence":0.55},
		{"type":"refusal","name":"declined"},
		{"type":"ranking","name":"future","order":["a","b"]}
	]`
	var answers []DecisionOrderedAnswer
	require.NoError(t, Unmarshal([]byte(body), &answers))
	require.Len(t, answers, 6)

	assert.Equal(t, DecisionOrderedKindPredicate, answers[0].Type)
	require.NotNil(t, answers[0].Probability)
	assert.Equal(t, 0.92, *answers[0].Probability)

	require.NotNil(t, answers[2].Choice)
	require.NotNil(t, answers[2].Choice.Bool, "a boolean choice must stay a boolean")
	assert.Nil(t, answers[2].Name, "an unnamed question has an unnamed answer")

	require.Len(t, answers[3].Probabilities, 3)
	require.NotNil(t, answers[3].Probabilities[1].Label)
	assert.Equal(t, "Workaround available", *answers[3].Probabilities[1].Label)

	assert.Equal(t, DecisionOrderedKindRefusal, answers[4].Type)
	assert.Nil(t, answers[4].Probability)
	assert.True(t, answers[4].IsRecognized())

	assert.False(t, answers[5].IsRecognized(), "an unfamiliar answer type must be flagged, not treated as an answer")

	out, err := MarshalSorted(answers)
	require.NoError(t, err)
	assert.JSONEq(t, body, string(out))
}

// TestBifrostDecisionRequestForms pins which form a
// request is written in; an explicit null state alone belongs to the map form.
func TestBifrostDecisionRequestForms(t *testing.T) {
	var nilRequest *BifrostDecisionRequest
	assert.False(t, nilRequest.UsesOrderedForm())
	assert.False(t, nilRequest.UsesMapForm())

	mapForm := &BifrostDecisionRequest{
		State:     nil,
		Questions: map[string]DecisionQuestion{"q": {Kind: DecisionKindNoul}},
	}
	assert.False(t, mapForm.UsesOrderedForm())
	assert.True(t, mapForm.UsesMapForm())

	ordered := &BifrostDecisionRequest{
		Input:            &DecisionInput{Text: Ptr("hello")},
		OrderedQuestions: []DecisionOrderedQuestion{{Type: DecisionOrderedKindPredicate, Instructions: "ok?"}},
	}
	assert.True(t, ordered.UsesOrderedForm())
	assert.False(t, ordered.UsesMapForm())

	assert.True(t, (&BifrostDecisionResponse{OrderedAnswers: []DecisionOrderedAnswer{{Type: DecisionOrderedKindRefusal}}}).UsesOrderedForm())
	assert.False(t, (&BifrostDecisionResponse{}).UsesOrderedForm())
}

// TestDecisionOrderedAnswerIsRecognizedByType pins that an answer built
// directly in Go is judged by its type, the same as a decoded one.
func TestDecisionOrderedAnswerIsRecognizedByType(t *testing.T) {
	assert.False(t, DecisionOrderedAnswer{Type: "future"}.IsRecognized())
	assert.False(t, DecisionOrderedAnswer{}.IsRecognized())
	assert.True(t, DecisionOrderedAnswer{Type: DecisionOrderedKindPredicate}.IsRecognized())
}
