package schemas

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDecisionRequestJSON pins the normalized request's wire shape: every
// field under its own key, list-only details (an unnamed question, boolean
// choices, labelled levels) kept, and Typesafe-only ones (predicate criteria,
// structured instructions) carried as given.
func TestDecisionRequestJSON(t *testing.T) {
	body := `{
		"provider": "typesafe",
		"model": "jev-1.13.0",
		"input": "I was charged twice.",
		"questions": [
			{"type": "predicate", "instructions": {"goal": "judge"}, "criteria": {"true": "upset"}},
			{"type": "choice", "name": "refund", "instructions": "Refund?", "choices": [{"value": true, "description": "yes"}, {"value": false}]},
			{"type": "score", "name": "severity", "instructions": "Rate", "levels": [{"label": "Low"}, {"label": "High", "description": ["urgent", "blocked"]}]}
		],
		"safety_identifier": "user-1"
	}`
	var request BifrostDecisionRequest
	require.NoError(t, Unmarshal([]byte(body), &request))
	require.NotNil(t, request.Input.Text)
	require.Len(t, request.Questions, 3)
	assert.Nil(t, request.Questions[0].Name)
	assert.Equal(t, &DecisionCriteria{True: NewDecisionText("upset")}, request.Questions[0].Criteria)
	assert.Equal(t, map[string]any{"goal": "judge"}, request.Questions[0].Instructions.Structured)
	assert.True(t, *request.Questions[1].Choices[0].Value.Bool)
	assert.Equal(t, "High", request.Questions[2].Levels[1].Label)

	out, err := Marshal(request)
	require.NoError(t, err)
	assert.JSONEq(t, body, string(out))
}

// TestDecisionInputJSON pins each input form: a string is text, an array is
// messages, an object is structured input, and null is an empty input; any
// other value is rejected.
func TestDecisionInputJSON(t *testing.T) {
	var text, messages, structured, null DecisionInput
	require.NoError(t, Unmarshal([]byte(`"hello"`), &text))
	assert.Equal(t, "hello", *text.Text)

	require.NoError(t, Unmarshal([]byte(`[{"role":"user","content":"hi"}]`), &messages))
	require.Len(t, messages.Messages, 1)
	assert.Equal(t, "hi", *messages.Messages[0].Content.Text)

	require.NoError(t, Unmarshal([]byte(`{"ticket":{"id":42}}`), &structured))
	assert.Equal(t, map[string]interface{}{"ticket": map[string]interface{}{"id": float64(42)}}, structured.Structured)

	require.NoError(t, Unmarshal([]byte(`null`), &null))
	assert.True(t, null.IsEmpty())

	var number DecisionInput
	assert.Error(t, Unmarshal([]byte(`7`), &number))
	var notMessages DecisionInput
	assert.Error(t, Unmarshal([]byte(`["a","b"]`), &notMessages), "an array is always read as messages")

	for in, input := range map[string]DecisionInput{`"hello"`: text, `{"ticket":{"id":42}}`: structured, `null`: null} {
		out, err := Marshal(input)
		require.NoError(t, err)
		assert.JSONEq(t, in, string(out))
	}
	structuredArray, err := Marshal(DecisionInput{Structured: []any{map[string]any{"role": "user"}}})
	require.NoError(t, err)
	assert.JSONEq(t, `[{"role":"user"}]`, string(structuredArray))
}

// TestDecisionTextJSON pins the text forms: a string is text, an object or
// array is a structured value, null is none, and a number or boolean is
// rejected. Value hands each to a provider as a plain JSON value.
func TestDecisionTextJSON(t *testing.T) {
	var text, object, array, null DecisionText
	require.NoError(t, Unmarshal([]byte(`"Is it urgent?"`), &text))
	assert.Equal(t, "Is it urgent?", text.Value())
	require.NoError(t, Unmarshal([]byte(`{"goal":"judge"}`), &object))
	assert.Equal(t, map[string]interface{}{"goal": "judge"}, object.Value())
	require.NoError(t, Unmarshal([]byte(`["read","decide"]`), &array))
	assert.Equal(t, []interface{}{"read", "decide"}, array.Value())
	require.NoError(t, Unmarshal([]byte(`null`), &null))
	assert.Nil(t, null.Value())
	var nilText *DecisionText
	assert.Nil(t, nilText.Value())

	var number, boolean DecisionText
	assert.Error(t, Unmarshal([]byte(`7`), &number))
	assert.Error(t, Unmarshal([]byte(`true`), &boolean))

	for in, value := range map[string]DecisionText{`"Is it urgent?"`: text, `{"goal":"judge"}`: object, `["read","decide"]`: array} {
		out, err := Marshal(value)
		require.NoError(t, err)
		assert.JSONEq(t, in, string(out))
	}
	_, err := Marshal(DecisionText{Text: Ptr("a"), Structured: map[string]any{}})
	assert.Error(t, err, "text and a structured value together are ambiguous")
}

// TestDecisionCriteriaJSON pins a predicate's criteria: only the "true" and
// "false" outcomes, each optional, with a side sent as null kept apart from an
// absent one so it is forwarded as null.
func TestDecisionCriteriaJSON(t *testing.T) {
	var criteria DecisionCriteria
	require.NoError(t, Unmarshal([]byte(`{"true":"upset","false":{"meaning":"calm"}}`), &criteria))
	assert.Equal(t, "upset", criteria.True.Value())
	assert.Equal(t, map[string]interface{}{"meaning": "calm"}, criteria.False.Value())
	out, err := Marshal(DecisionCriteria{True: NewDecisionText("upset")})
	require.NoError(t, err)
	assert.JSONEq(t, `{"true":"upset"}`, string(out))

	var withNull DecisionCriteria
	require.NoError(t, Unmarshal([]byte(`{"true":"upset","false":null}`), &withNull))
	require.NotNil(t, withNull.False, "a side sent as null is not dropped")
	assert.Nil(t, withNull.False.Value())
	out, err = Marshal(withNull)
	require.NoError(t, err)
	assert.JSONEq(t, `{"true":"upset","false":null}`, string(out))

	var absent DecisionCriteria
	require.NoError(t, Unmarshal([]byte(`{"true":"upset"}`), &absent))
	assert.Nil(t, absent.False, "an absent side stays unset")

	assert.ErrorContains(t, Unmarshal([]byte(`{"true":7}`), &absent), `criteria "true"`)
}

// TestDecisionInputValidateAndImages pins that at most one input member may be
// set and that only an image part counts as an image.
func TestDecisionInputValidateAndImages(t *testing.T) {
	assert.NoError(t, DecisionInput{}.Validate())
	assert.NoError(t, DecisionInput{Structured: map[string]any{"a": 1}}.Validate())
	assert.Error(t, DecisionInput{Text: Ptr("a"), Structured: []any{"b"}}.Validate())
	_, err := Marshal(DecisionInput{Text: Ptr("a"), Messages: []DecisionInputMessage{}})
	assert.Error(t, err)

	withImage := DecisionInput{Messages: []DecisionInputMessage{{Role: "user", Content: DecisionInputContent{Parts: []DecisionInputPart{
		{Type: DecisionInputPartTypeText, Text: Ptr("look")},
		{Type: DecisionInputPartTypeImage, ImageURL: Ptr("data:image/png;base64,AAAA")},
	}}}}}
	assert.Equal(t, DecisionInputPartTypeImage, withImage.NonTextPartType())
	var withAudio DecisionInput
	require.NoError(t, Unmarshal([]byte(`[{"role":"user","content":[{"type":"input_text","text":"listen"},{"type":"input_audio","input_audio":{"data":"AAAA","format":"wav"}}]}]`), &withAudio))
	assert.Equal(t, "input_audio", withAudio.NonTextPartType(), "a part Bifrost does not model is not text either")
	var untyped DecisionInput
	require.NoError(t, Unmarshal([]byte(`[{"role":"user","content":[{"image_url":"data:image/png;base64,AAAA"}]}]`), &untyped))
	assert.Equal(t, "untyped", untyped.NonTextPartType(), "a part without a type is not text")
	assert.Empty(t, DecisionInput{Messages: []DecisionInputMessage{{Role: "user", Content: DecisionInputContent{Text: Ptr("hi")}}}}.NonTextPartType())
	assert.Empty(t, DecisionInput{Structured: []any{map[string]any{"type": "input_image"}}}.NonTextPartType())
}

// TestDecisionInputMessagesKeepUnknownPartType pins that a message part of a
// type Bifrost does not model keeps its type, by which providers reject it.
// The payload is synthetic.
func TestDecisionInputMessagesKeepUnknownPartType(t *testing.T) {
	body := `[{"role":"user","content":[
		{"type":"input_text","text":"Inspect the product."},
		{"type":"input_audio","audio":{"data":"BBBB"}}
	]}]`
	var messages []DecisionInputMessage
	require.NoError(t, Unmarshal([]byte(body), &messages))
	assert.Equal(t, "input_audio", messages[0].Content.Parts[1].Type)
}

// TestDecisionInputMessagesKeepOpenAIFields pins that the fields of OpenAI's
// input messages survive decoding and re-encoding: the message's optional item
// type and an image part's detail level, for an HTTP(S) image URL as well as a
// data URL.
func TestDecisionInputMessagesKeepOpenAIFields(t *testing.T) {
	body := `[{"type":"message","role":"user","content":[
		{"type":"input_text","text":"Compare the two photos."},
		{"type":"input_image","image_url":"https://example.com/front.png","detail":"high"},
		{"type":"input_image","image_url":"data:image/png;base64,AAAA","detail":"low"}
	]}]`
	var messages []DecisionInputMessage
	require.NoError(t, Unmarshal([]byte(body), &messages))
	require.Len(t, messages, 1)
	assert.Equal(t, "message", *messages[0].Type)
	assert.Equal(t, "high", *messages[0].Content.Parts[1].Detail)
	out, err := Marshal(messages)
	require.NoError(t, err)
	assert.JSONEq(t, body, string(out))
}

// TestDecisionQuestionNames pins the names map-shaped providers send questions
// under: a question's own name, or a generated one that avoids every real
// name, and an error for a repeated name.
func TestDecisionQuestionNames(t *testing.T) {
	names, err := DecisionQuestionNames([]DecisionQuestion{
		{Type: DecisionTypePredicate},
		{Type: DecisionTypePredicate, Name: Ptr("question_1")},
		{Type: DecisionTypePredicate, Name: Ptr("refund")},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"question_1_2", "question_1", "refund"}, names)

	_, err = DecisionQuestionNames([]DecisionQuestion{{Name: Ptr("q")}, {Name: Ptr("q")}})
	assert.ErrorContains(t, err, "used more than once")

	assert.Equal(t, []string{"question_1", "q", "question_3"},
		DecisionAnswerNames([]DecisionAnswer{{}, {Name: Ptr("q")}, {Name: Ptr("q")}}),
		"a repeated answer name is renamed rather than rejected")
}

// TestDecisionChoiceAndScalarKeys pins how choices and distribution values are
// keyed for providers that key them by name.
func TestDecisionChoiceAndScalarKeys(t *testing.T) {
	key, err := DecisionChoice{Value: DecisionScalar{Bool: Ptr(false)}}.Key()
	require.NoError(t, err)
	assert.Equal(t, "false", key)
	_, err = DecisionChoice{Value: DecisionScalar{Num: Ptr(1.0)}}.Key()
	assert.Error(t, err, "a choice value is a string or a boolean")

	number, ok := DecisionScalar{Num: Ptr(2.0)}.Key()
	assert.True(t, ok)
	assert.Equal(t, "2", number)
	_, ok = DecisionScalar{}.Key()
	assert.False(t, ok)
}

// TestDecisionAnswersKeepOrderAndVariants pins answer decoding: a boolean
// choice stays a boolean, and a refusal is a modelled answer with no value.
// The payloads are synthetic.
func TestDecisionAnswersKeepOrderAndVariants(t *testing.T) {
	body := `[
		{"type":"predicate","name":"damaged","probability":0.92},
		{"type":"choice","choice":true,"probabilities":[{"value":true,"probability":0.7},{"value":false,"probability":0.3}],"confidence":0.7},
		{"type":"score","name":"severity","score":1.1,"probabilities":[{"value":1,"label":"Workaround","probability":1}],"legend":{"1":"Workaround"},"answer_confidence":0.9},
		{"type":"refusal","name":"declined"}
	]`
	var answers []DecisionAnswer
	require.NoError(t, Unmarshal([]byte(body), &answers))
	require.Len(t, answers, 4)
	assert.Equal(t, 0.92, *answers[0].Probability)
	assert.True(t, *answers[1].Choice.Bool)
	assert.Nil(t, answers[1].Name)
	assert.Equal(t, "Workaround", *answers[2].Probabilities[0].Label)
	assert.Equal(t, DecisionTypeRefusal, answers[3].Type)
	assert.Nil(t, answers[3].Probability)
	assert.Nil(t, answers[3].Choice)

	out, err := Marshal(answers)
	require.NoError(t, err)
	assert.JSONEq(t, body, string(out))
}
