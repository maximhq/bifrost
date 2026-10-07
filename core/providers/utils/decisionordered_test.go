package utils

import (
	"testing"

	schemas "github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDecisionMapToOrderedConvertsEveryKind pins the map-to-ordered mapping:
// state becomes the input text, questions are sorted by name and keep their
// name, a noul becomes a predicate with its criteria folded into the
// instructions, choice options are sorted with structured descriptions as
// JSON text, and score levels are labelled by index. The payload is synthetic.
func TestDecisionMapToOrderedConvertsEveryKind(t *testing.T) {
	request := &schemas.BifrostDecisionRequest{
		Model: "gpt-6-luna",
		State: map[string]interface{}{"ticket": "double charge", "channel": "email"},
		Questions: map[string]schemas.DecisionQuestion{
			"urgency": {Kind: schemas.DecisionKindScore, Instructions: "How urgent?", Criteria: []string{"can wait", "today"}},
			"is_frustrated": {
				Kind:         schemas.DecisionKindNoul,
				Instructions: "Is the customer frustrated?",
				Criteria:     map[string]interface{}{"true": "clearly upset"},
			},
			"category": {
				Kind:         schemas.DecisionKindChoice,
				Instructions: map[string]interface{}{"question": "Ticket category"},
				Criteria:     map[string]interface{}{"bug": "defects", "billing": map[string]interface{}{"definition": "charges"}, "other": nil},
			},
		},
	}

	input, questions, err := DecisionMapToOrdered(request)
	require.NoError(t, err)
	require.NotNil(t, input)
	require.NotNil(t, input.Text)
	assert.Equal(t, `{"channel":"email","ticket":"double charge"}`, *input.Text)

	require.Len(t, questions, 3)
	assert.Equal(t, []string{"category", "is_frustrated", "urgency"}, []string{*questions[0].Name, *questions[1].Name, *questions[2].Name})

	category := questions[0]
	assert.Equal(t, schemas.DecisionOrderedKindChoice, category.Type)
	assert.Equal(t, `{"question":"Ticket category"}`, category.Instructions)
	require.Len(t, category.Choices, 3)
	assert.Equal(t, "billing", *category.Choices[0].Value.Str)
	assert.Equal(t, `{"definition":"charges"}`, *category.Choices[0].Description)
	assert.Equal(t, "bug", *category.Choices[1].Value.Str)
	assert.Equal(t, "other", *category.Choices[2].Value.Str)
	assert.Nil(t, category.Choices[2].Description, "an option without a description sends none")

	predicate := questions[1]
	assert.Equal(t, schemas.DecisionOrderedKindPredicate, predicate.Type)
	assert.Equal(t, "Is the customer frustrated? Meaning (answer: meaning): true=clearly upset", predicate.Instructions)

	score := questions[2]
	assert.Equal(t, schemas.DecisionOrderedKindScore, score.Type)
	assert.Equal(t, []schemas.DecisionScoreLevel{
		{Label: "0", Description: schemas.Ptr("can wait")},
		{Label: "1", Description: schemas.Ptr("today")},
	}, score.Levels)

	// Converting again yields the same questions, so the upstream body is stable.
	_, again, err := DecisionMapToOrdered(request)
	require.NoError(t, err)
	assert.Equal(t, questions, again)
}

// TestDecisionMapToOrderedRejectsInvalidQuestions pins that a map question the
// shared decision contract does not allow is a 400, and that a missing state
// leaves the input empty for the provider's own input check.
func TestDecisionMapToOrderedRejectsInvalidQuestions(t *testing.T) {
	cases := map[string]schemas.DecisionQuestion{
		"choice without options": {Kind: schemas.DecisionKindChoice, Instructions: "x"},
		"score with one level":   {Kind: schemas.DecisionKindScore, Instructions: "x", Criteria: []string{"only"}},
		"unsupported kind":       {Kind: "ranking", Instructions: "x"},
	}
	for name, question := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := DecisionMapToOrdered(&schemas.BifrostDecisionRequest{State: "x", Questions: map[string]schemas.DecisionQuestion{"q": question}})
			require.Error(t, err)
			assert.True(t, IsInvalidRequestError(err), "a caller's invalid question must be a 400")
		})
	}

	input, _, err := DecisionMapToOrdered(&schemas.BifrostDecisionRequest{Questions: map[string]schemas.DecisionQuestion{"q": {Kind: schemas.DecisionKindNoul}}})
	require.NoError(t, err)
	require.NotNil(t, input, "an absent state is sent as null, never as a missing input")
	assert.Equal(t, "null", *input.Text)

	_, _, err = DecisionMapToOrdered(&schemas.BifrostDecisionRequest{Input: &schemas.DecisionInput{Text: schemas.Ptr("x")}})
	assert.Error(t, err, "an ordered-form request has no map to convert")
}

// TestDecisionOrderedAnswersToMapConvertsEveryKind pins the answer mapping
// back to the map form, matched by name regardless of answer order.
func TestDecisionOrderedAnswersToMapConvertsEveryKind(t *testing.T) {
	questions := map[string]schemas.DecisionQuestion{
		"is_frustrated": {Kind: schemas.DecisionKindNoul},
		"category":      {Kind: schemas.DecisionKindChoice, Criteria: map[string]interface{}{"billing": "", "bug": ""}},
		"urgency":       {Kind: schemas.DecisionKindScore, Criteria: []interface{}{"can wait", map[string]interface{}{"level": "today"}}},
	}
	answers := []schemas.DecisionOrderedAnswer{
		{
			Type: schemas.DecisionOrderedKindScore, Name: schemas.Ptr("urgency"), Score: schemas.Ptr(0.9), Confidence: schemas.Ptr(0.9),
			Probabilities: []schemas.DecisionProbability{
				{Value: schemas.DecisionScalar{Num: schemas.Ptr(0.0)}, Label: schemas.Ptr("0"), Probability: 0.1},
				{Value: schemas.DecisionScalar{Num: schemas.Ptr(1.0)}, Label: schemas.Ptr("1"), Probability: 0.9},
			},
		},
		{Type: schemas.DecisionOrderedKindPredicate, Name: schemas.Ptr("is_frustrated"), Probability: schemas.Ptr(0.3)},
		{
			Type: schemas.DecisionOrderedKindChoice, Name: schemas.Ptr("category"), Choice: &schemas.DecisionScalar{Str: schemas.Ptr("bug")}, Confidence: schemas.Ptr(0.7),
			Probabilities: []schemas.DecisionProbability{
				{Value: schemas.DecisionScalar{Str: schemas.Ptr("billing")}, Probability: 0.3},
				{Value: schemas.DecisionScalar{Str: schemas.Ptr("bug")}, Probability: 0.7},
			},
		},
	}

	converted, err := DecisionOrderedAnswersToMap(answers, questions)
	require.NoError(t, err)
	assert.Equal(t, map[string]schemas.DecisionAnswer{
		"is_frustrated": {Kind: schemas.DecisionKindNoul, Value: 0.3},
		"category": {
			Kind: schemas.DecisionKindChoice, Value: "bug", Confidence: schemas.Ptr(0.7),
			Probabilities: map[string]float64{"billing": 0.3, "bug": 0.7},
		},
		"urgency": {
			Kind: schemas.DecisionKindScore, Value: 0.9, Confidence: schemas.Ptr(0.9),
			Probabilities: map[string]float64{"0": 0.1, "1": 0.9},
			Legend:        map[string]any{"0": "can wait", "1": map[string]interface{}{"level": "today"}},
		},
	}, converted)
}

// TestDecisionOrderedAnswersToMapRejectsUnusableAnswers pins that an answer
// the map form cannot represent fails the request rather than producing a
// map with a missing or invented answer.
func TestDecisionOrderedAnswersToMapRejectsUnusableAnswers(t *testing.T) {
	questions := map[string]schemas.DecisionQuestion{
		"q": {Kind: schemas.DecisionKindChoice, Criteria: map[string]interface{}{"yes": "", "no": ""}},
	}
	var unknown schemas.DecisionOrderedAnswer
	require.NoError(t, schemas.Unmarshal([]byte(`{"type":"ranking","name":"q","order":["yes"]}`), &unknown))

	cases := map[string][]schemas.DecisionOrderedAnswer{
		"missing answer": nil,
		"refusal":        {{Type: schemas.DecisionOrderedKindRefusal, Name: schemas.Ptr("q")}},
		"unknown type":   {unknown},
		"wrong type":     {{Type: schemas.DecisionOrderedKindPredicate, Name: schemas.Ptr("q"), Probability: schemas.Ptr(0.5)}},
		"option not offered": {{
			Type: schemas.DecisionOrderedKindChoice, Name: schemas.Ptr("q"), Choice: &schemas.DecisionScalar{Str: schemas.Ptr("maybe")},
		}},
	}
	for name, answers := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := DecisionOrderedAnswersToMap(answers, questions)
			assert.Error(t, err)
		})
	}

	_, err := DecisionOrderedAnswersToMap(
		[]schemas.DecisionOrderedAnswer{{Type: schemas.DecisionOrderedKindPredicate, Name: schemas.Ptr("p"), Probability: schemas.Ptr(1.5)}},
		map[string]schemas.DecisionQuestion{"p": {Kind: schemas.DecisionKindNoul}},
	)
	assert.Error(t, err, "a probability outside [0,1] is not a valid noul value")
}

// TestDecisionMapToOrderedKeepsNullAndEmptyState pins that a null or empty
// state, both accepted by the map route, still produce an input rather than a
// missing one that the ordered converter would reject.
func TestDecisionMapToOrderedKeepsNullAndEmptyState(t *testing.T) {
	questions := map[string]schemas.DecisionQuestion{"q": {Kind: schemas.DecisionKindNoul}}
	for name, tc := range map[string]struct {
		state any
		want  string
	}{"null": {nil, "null"}, "empty string": {"", ""}} {
		t.Run(name, func(t *testing.T) {
			input, _, err := DecisionMapToOrdered(&schemas.BifrostDecisionRequest{State: tc.state, Questions: questions})
			require.NoError(t, err)
			require.NotNil(t, input)
			require.NotNil(t, input.Text)
			assert.Equal(t, tc.want, *input.Text)
		})
	}
}

// TestDecisionOrderedAnswersToMapRejectsMalformedProbabilities pins that a
// distribution with a fractional or negative score index, a wrong-typed value,
// or a repeated key fails the conversion instead of yielding a partial map.
func TestDecisionOrderedAnswersToMapRejectsMalformedProbabilities(t *testing.T) {
	score := func(probs ...schemas.DecisionProbability) []schemas.DecisionOrderedAnswer {
		return []schemas.DecisionOrderedAnswer{{Type: schemas.DecisionOrderedKindScore, Name: schemas.Ptr("s"), Score: schemas.Ptr(1.0), Probabilities: probs}}
	}
	num := func(n float64) schemas.DecisionScalar { return schemas.DecisionScalar{Num: schemas.Ptr(n)} }
	scoreQuestions := map[string]schemas.DecisionQuestion{"s": {Kind: schemas.DecisionKindScore, Criteria: []string{"a", "b"}}}

	for name, answers := range map[string][]schemas.DecisionOrderedAnswer{
		"fractional index": score(schemas.DecisionProbability{Value: num(1.9), Probability: 0.5}),
		"negative index":   score(schemas.DecisionProbability{Value: num(-1), Probability: 0.5}),
		"wrong type":       score(schemas.DecisionProbability{Value: schemas.DecisionScalar{Str: schemas.Ptr("1")}, Probability: 0.5}),
		"duplicate index":  score(schemas.DecisionProbability{Value: num(1), Probability: 0.5}, schemas.DecisionProbability{Value: num(1), Probability: 0.5}),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := DecisionOrderedAnswersToMap(answers, scoreQuestions)
			assert.Error(t, err)
		})
	}

	_, err := DecisionOrderedAnswersToMap(
		[]schemas.DecisionOrderedAnswer{{
			Type: schemas.DecisionOrderedKindChoice, Name: schemas.Ptr("c"), Choice: &schemas.DecisionScalar{Str: schemas.Ptr("yes")},
			Probabilities: []schemas.DecisionProbability{
				{Value: schemas.DecisionScalar{Str: schemas.Ptr("yes")}, Probability: 0.6},
				{Value: schemas.DecisionScalar{Str: schemas.Ptr("yes")}, Probability: 0.4},
			},
		}},
		map[string]schemas.DecisionQuestion{"c": {Kind: schemas.DecisionKindChoice, Criteria: map[string]interface{}{"yes": "", "no": ""}}},
	)
	assert.Error(t, err, "a repeated choice key must not overwrite the earlier entry")
}

// TestDecisionMapToOrderedRejectsUnserializableState pins that a state that
// cannot be encoded is a 400, as in emulation, instead of empty input that
// would be answered and billed without the caller's evidence.
func TestDecisionMapToOrderedRejectsUnserializableState(t *testing.T) {
	_, _, err := DecisionMapToOrdered(&schemas.BifrostDecisionRequest{
		State:     map[string]any{"bad": make(chan int)},
		Questions: map[string]schemas.DecisionQuestion{"q": {Kind: schemas.DecisionKindNoul}},
	})
	require.Error(t, err)
	assert.True(t, IsInvalidRequestError(err))
	assert.Contains(t, err.Error(), "decision state could not be serialized")
}
