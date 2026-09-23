package utils

import (
	"math"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

func mixedQuestions() map[string]schemas.DecisionQuestion {
	return map[string]schemas.DecisionQuestion{
		"is_frustrated": {Kind: schemas.DecisionKindNoul, Instructions: "Is the customer frustrated?"},
		"category": {
			Kind:         schemas.DecisionKindChoice,
			Instructions: "Pick the ticket category",
			Criteria:     map[string]interface{}{"billing": "money", "bug": "defects", "other": "else"},
		},
		"urgency": {
			Kind:         schemas.DecisionKindScore,
			Instructions: "How urgent?",
			Criteria:     []interface{}{"low", "medium", "high"},
		},
	}
}

func TestBuildDecisionSchemaShape(t *testing.T) {
	params, err := BuildDecisionSchema(mixedQuestions())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Marshal and re-read so we assert on the emitted JSON schema.
	raw, err := sonic.Marshal(params)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var schema map[string]interface{}
	if err := sonic.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if schema["type"] != "object" {
		t.Errorf("top type = %v", schema["type"])
	}
	props := schema["properties"].(map[string]interface{})
	for _, name := range []string{"is_frustrated", "category", "urgency"} {
		if _, ok := props[name]; !ok {
			t.Fatalf("missing property %q", name)
		}
	}

	noul := props["is_frustrated"].(map[string]interface{})["properties"].(map[string]interface{})
	if _, ok := noul["value"]; !ok {
		t.Error("noul missing value")
	}
	// A native noul answer is {type, noul} only - the value near 0.5 is the
	// uncertainty signal - so confidence stays an optional extra the model may
	// volunteer, never a required field.
	if _, ok := noul["confidence"]; !ok {
		t.Error("noul confidence should remain available as an optional field")
	}
	noulRequired := props["is_frustrated"].(map[string]interface{})["required"].([]interface{})
	if len(noulRequired) != 1 || noulRequired[0] != "value" {
		t.Errorf("noul required = %v, want [value] only", noulRequired)
	}

	choice := props["category"].(map[string]interface{})["properties"].(map[string]interface{})
	enum := choice["choice"].(map[string]interface{})["enum"].([]interface{})
	if len(enum) != 3 {
		t.Errorf("choice enum = %v", enum)
	}
	if _, ok := choice["probabilities"]; !ok {
		t.Error("choice missing probabilities")
	}
	// Probabilities are a required field on native choice and score answers,
	// so the tool schema must require them from the emulating model too.
	choiceRequired := props["category"].(map[string]interface{})["required"].([]interface{})
	if !containsAnyString(choiceRequired, "probabilities") {
		t.Errorf("choice required = %v, want probabilities included", choiceRequired)
	}

	score := props["urgency"].(map[string]interface{})["properties"].(map[string]interface{})
	if !strings.Contains(score["value"].(map[string]interface{})["description"].(string), "high") {
		t.Error("score value description should mention the levels")
	}
	scoreRequired := props["urgency"].(map[string]interface{})["required"].([]interface{})
	if !containsAnyString(scoreRequired, "probabilities") {
		t.Errorf("score required = %v, want probabilities included", scoreRequired)
	}
}

// containsAnyString reports whether a decoded []interface{} contains s.
func containsAnyString(list []interface{}, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// structuredQuestions carries the spec's richer criteria shapes: object and
// null choice descriptions, and object levels in a score array.
func structuredQuestions() map[string]schemas.DecisionQuestion {
	return map[string]schemas.DecisionQuestion{
		"approve": {
			Kind:         schemas.DecisionKindNoul,
			Instructions: "Approve the refund?",
			Criteria: map[string]any{
				"true":  map[string]any{"meaning": "refund it"},
				"false": []any{"deny it", "no evidence"},
			},
		},
		"category": {
			Kind:         schemas.DecisionKindChoice,
			Instructions: "Pick the ticket category",
			Criteria: map[string]any{
				"billing": map[string]any{"rubric": "money issues"},
				"bug":     []any{"crash", "defect"},
				"support": "service questions",
				"other":   nil,
			},
		},
		"urgency": {
			Kind:         schemas.DecisionKindScore,
			Instructions: "How urgent?",
			Criteria: []any{
				"low",
				map[string]any{"examples": []any{"outage"}, "level": "high"},
				[]any{"critical", "churn risk"},
			},
		},
	}
}

func TestBuildDecisionSchemaStructuredCriteria(t *testing.T) {
	params, err := BuildDecisionSchema(structuredQuestions())
	if err != nil {
		t.Fatalf("structured criteria rejected: %v", err)
	}
	raw, err := sonic.Marshal(params)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var schema map[string]any
	if err := sonic.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	props := schema["properties"].(map[string]any)

	noulDesc := props["approve"].(map[string]any)["properties"].(map[string]any)["value"].(map[string]any)["description"].(string)
	// The true/false rubric descriptions must reach the model: objects and
	// arrays render as sorted JSON.
	if !strings.Contains(noulDesc, `true={"meaning":"refund it"}`) || !strings.Contains(noulDesc, `false=["deny it","no evidence"]`) {
		t.Errorf("noul description missing true/false criteria: %q", noulDesc)
	}

	choice := props["category"].(map[string]any)["properties"].(map[string]any)["choice"].(map[string]any)
	enum := choice["enum"].([]any)
	if len(enum) != 4 {
		t.Fatalf("choice enum should keep all 4 options including the null-described one: %v", enum)
	}
	choiceDesc := choice["description"].(string)
	// Option rubric descriptions must reach the model: strings verbatim,
	// objects and arrays as JSON, null options simply skipped.
	for _, want := range []string{`{"rubric":"money issues"}`, `["crash","defect"]`, "service questions"} {
		if !strings.Contains(choiceDesc, want) {
			t.Errorf("choice description missing option rubric %q: %q", want, choiceDesc)
		}
	}

	scoreDesc := props["urgency"].(map[string]any)["properties"].(map[string]any)["value"].(map[string]any)["description"].(string)
	for _, want := range []string{"0=low", `{"examples":["outage"],"level":"high"}`, `["critical","churn risk"]`} {
		if !strings.Contains(scoreDesc, want) {
			t.Errorf("score description missing level %q: %q", want, scoreDesc)
		}
	}
	if strings.Contains(scoreDesc, "map[") {
		t.Errorf("score description leaks Go map syntax: %q", scoreDesc)
	}
}

func TestParseDecisionAnswersStructuredScoreLegend(t *testing.T) {
	args := `{
		"approve": {"value": 0.9, "confidence": 0.8},
		"category": {"choice": "other", "confidence": 0.7, "probabilities": {"billing": 0.1, "bug": 0.1, "support": 0.1, "other": 0.7}},
		"urgency": {"value": 1, "confidence": 0.6, "probabilities": {"0": 0.1, "1": 0.8, "2": 0.1}}
	}`
	answers, err := ParseDecisionAnswers([]byte(args), structuredQuestions())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if answers["category"].Value.(string) != "other" {
		t.Errorf("null-described choice option rejected: %+v", answers["category"])
	}
	// The legend echoes each level's description verbatim, matching the native
	// API's shape (values are strings, objects, or arrays - not stringified).
	legend := answers["urgency"].Legend
	if legend["0"] != "low" {
		t.Errorf("string legend level should pass through verbatim, got %#v", legend["0"])
	}
	if !reflect.DeepEqual(legend["1"], map[string]any{"examples": []any{"outage"}, "level": "high"}) {
		t.Errorf("object legend level should echo verbatim, got %#v", legend["1"])
	}
	if !reflect.DeepEqual(legend["2"], []any{"critical", "churn risk"}) {
		t.Errorf("array legend level should echo verbatim, got %#v", legend["2"])
	}
}

// TestBuildDecisionSchemaTypedStringMapCriteria covers the map[string]string
// input branch Go SDK callers use for noul and choice criteria.
func TestBuildDecisionSchemaTypedStringMapCriteria(t *testing.T) {
	questions := map[string]schemas.DecisionQuestion{
		"approve": {
			Kind:         schemas.DecisionKindNoul,
			Instructions: "Approve?",
			Criteria:     map[string]string{"true": "grant it", "false": "deny it"},
		},
		"bucket": {
			Kind:         schemas.DecisionKindChoice,
			Instructions: "Bucket?",
			Criteria:     map[string]string{"a": "first bucket", "b": "second bucket"},
		},
	}
	params, err := BuildDecisionSchema(questions)
	if err != nil {
		t.Fatalf("typed string map criteria rejected: %v", err)
	}
	raw, err := sonic.Marshal(params)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var schema map[string]any
	if err := sonic.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	props := schema["properties"].(map[string]any)

	noulDesc := props["approve"].(map[string]any)["properties"].(map[string]any)["value"].(map[string]any)["description"].(string)
	if !strings.Contains(noulDesc, "true=grant it") || !strings.Contains(noulDesc, "false=deny it") {
		t.Errorf("noul description missing typed-map criteria: %q", noulDesc)
	}
	choiceDesc := props["bucket"].(map[string]any)["properties"].(map[string]any)["choice"].(map[string]any)["description"].(string)
	if !strings.Contains(choiceDesc, "a=first bucket") || !strings.Contains(choiceDesc, "b=second bucket") {
		t.Errorf("choice description missing typed-map rubrics: %q", choiceDesc)
	}
}

// TestBuildDecisionSchemaProbabilitiesClosed pins the probability sub-schemas:
// the actual option names / level indexes as enumerated required properties,
// additionalProperties: false, and each probability bounded to [0,1] - so a
// schema-enforcing provider rejects stray keys and out-of-range values before
// the parser ever sees them.
func TestBuildDecisionSchemaProbabilitiesClosed(t *testing.T) {
	params, err := BuildDecisionSchema(mixedQuestions())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	raw, err := sonic.Marshal(params)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var schema map[string]any
	if err := sonic.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	props := schema["properties"].(map[string]any)

	check := func(t *testing.T, question string, wantKeys []string) {
		t.Helper()
		probs, ok := props[question].(map[string]any)["properties"].(map[string]any)["probabilities"].(map[string]any)
		if !ok {
			t.Fatalf("question %q has no probabilities schema", question)
		}
		if ap, ok := probs["additionalProperties"].(bool); !ok || ap {
			t.Errorf("%q probabilities additionalProperties = %v, want false", question, probs["additionalProperties"])
		}
		keyProps, ok := probs["properties"].(map[string]any)
		if !ok || len(keyProps) != len(wantKeys) {
			t.Fatalf("%q probabilities properties = %v, want keys %v", question, probs["properties"], wantKeys)
		}
		for _, key := range wantKeys {
			entry, ok := keyProps[key].(map[string]any)
			if !ok {
				t.Errorf("%q probabilities missing key %q", question, key)
				continue
			}
			if entry["minimum"] != float64(0) || entry["maximum"] != float64(1) {
				t.Errorf("%q probability %q not bounded [0,1]: %v", question, key, entry)
			}
		}
		required, ok := probs["required"].([]any)
		if !ok || len(required) != len(wantKeys) {
			t.Errorf("%q probabilities required = %v, want all keys", question, probs["required"])
		}
	}

	check(t, "category", []string{"billing", "bug", "other"})
	check(t, "urgency", []string{"0", "1", "2"})
}

func TestBuildDecisionSchemaEmpty(t *testing.T) {
	if _, err := BuildDecisionSchema(map[string]schemas.DecisionQuestion{}); err == nil {
		t.Fatal("expected error for empty questions")
	}
}

func TestBuildDecisionSchemaRejectsEmptyChoiceCriteria(t *testing.T) {
	// An empty criteria map would emit enum: [] and make every answer fail
	// downstream; it must be a local error before any model is called.
	questions := map[string]schemas.DecisionQuestion{
		"pick": {Kind: schemas.DecisionKindChoice, Instructions: "d", Criteria: map[string]any{}},
	}
	if _, err := BuildDecisionSchema(questions); err == nil || !strings.Contains(err.Error(), "at least one option") {
		t.Fatalf("empty choice criteria must be rejected, got %v", err)
	}
	args := `{"pick": {"choice": "anything", "confidence": 0.5, "probabilities": {"anything": 1}}}`
	if _, err := ParseDecisionAnswers([]byte(args), questions); err == nil || !strings.Contains(err.Error(), "at least one option") {
		t.Fatalf("empty choice criteria must be rejected in the parser too, got %v", err)
	}
}

func TestParseDecisionAnswersValid(t *testing.T) {
	args := `{
		"is_frustrated": {"value": 0.9, "confidence": 0.8},
		"category": {"choice": "billing", "confidence": 0.7, "probabilities": {"billing": 0.7, "bug": 0.2, "other": 0.1}},
		"urgency": {"value": 2, "confidence": 0.6, "probabilities": {"0": 0.1, "1": 0.1, "2": 0.8}}
	}`
	answers, err := ParseDecisionAnswers([]byte(args), mixedQuestions())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if answers["is_frustrated"].Kind != schemas.DecisionKindNoul || answers["is_frustrated"].Value.(float64) != 0.9 {
		t.Errorf("noul = %+v", answers["is_frustrated"])
	}
	if *answers["is_frustrated"].Confidence != 0.8 {
		t.Errorf("noul confidence = %v", answers["is_frustrated"].Confidence)
	}
	if answers["category"].Value.(string) != "billing" {
		t.Errorf("choice = %+v", answers["category"])
	}
	if answers["category"].Probabilities["billing"] != 0.7 {
		t.Errorf("choice probabilities = %+v", answers["category"].Probabilities)
	}
	// Score is derived from the distribution (0*0.1 + 1*0.1 + 2*0.8 = 1.7),
	// not taken from the supplied value.
	if math.Abs(answers["urgency"].Value.(float64)-1.7) > 1e-9 {
		t.Errorf("score = %+v", answers["urgency"])
	}
	if answers["urgency"].Legend["2"] != "high" {
		t.Errorf("score legend = %+v", answers["urgency"].Legend)
	}
}

func TestParseDecisionAnswersNoulConfidenceOptional(t *testing.T) {
	// The schema no longer requires confidence on noul (the native answer is
	// {type, noul} only), so a model omitting it must parse fine; one it
	// volunteers is still kept and validated.
	args := `{
		"is_frustrated": {"value": 0.9},
		"category": {"choice": "billing", "confidence": 0.7, "probabilities": {"billing": 0.8, "bug": 0.1, "other": 0.1}},
		"urgency": {"value": 1, "confidence": 0.5, "probabilities": {"0": 0.1, "1": 0.8, "2": 0.1}}
	}`
	answers, err := ParseDecisionAnswers([]byte(args), mixedQuestions())
	if err != nil {
		t.Fatalf("noul answer without confidence rejected: %v", err)
	}
	if answers["is_frustrated"].Confidence != nil {
		t.Errorf("omitted noul confidence should stay nil, got %v", *answers["is_frustrated"].Confidence)
	}

	// A volunteered but invalid noul confidence is still rejected.
	bad := `{
		"is_frustrated": {"value": 0.9, "confidence": 1.4},
		"category": {"choice": "billing", "confidence": 0.7, "probabilities": {"billing": 0.8, "bug": 0.1, "other": 0.1}},
		"urgency": {"value": 1, "confidence": 0.5, "probabilities": {"0": 0.1, "1": 0.8, "2": 0.1}}
	}`
	if _, err := ParseDecisionAnswers([]byte(bad), mixedQuestions()); err == nil {
		t.Fatal("volunteered out-of-range noul confidence must be rejected")
	}
}

func TestParseDecisionAnswersRequiresProbabilities(t *testing.T) {
	// Probabilities are required on native choice and score answers, so an
	// emulated answer omitting them must be rejected - a response without them
	// would fail strict SDK clients - while noul needs none.
	missingChoice := `{
		"is_frustrated": {"value": 0.9},
		"category": {"choice": "billing", "confidence": 0.7},
		"urgency": {"value": 1, "confidence": 0.5, "probabilities": {"0": 0.1, "1": 0.8, "2": 0.1}}
	}`
	if _, err := ParseDecisionAnswers([]byte(missingChoice), mixedQuestions()); err == nil || !strings.Contains(err.Error(), "probabilities") {
		t.Fatalf("choice answer without probabilities must be rejected, got %v", err)
	}

	missingScore := `{
		"is_frustrated": {"value": 0.9},
		"category": {"choice": "billing", "confidence": 0.7, "probabilities": {"billing": 0.8, "bug": 0.1, "other": 0.1}},
		"urgency": {"value": 1, "confidence": 0.5}
	}`
	if _, err := ParseDecisionAnswers([]byte(missingScore), mixedQuestions()); err == nil || !strings.Contains(err.Error(), "probabilities") {
		t.Fatalf("score answer without probabilities must be rejected, got %v", err)
	}

	// A partial distribution that happens to sum to 1 is still incomplete:
	// the contract is a probability for EVERY option and level.
	partialChoice := `{
		"is_frustrated": {"value": 0.9},
		"category": {"choice": "billing", "confidence": 0.7, "probabilities": {"billing": 1}},
		"urgency": {"value": 1, "confidence": 0.5, "probabilities": {"0": 0.1, "1": 0.8, "2": 0.1}}
	}`
	if _, err := ParseDecisionAnswers([]byte(partialChoice), mixedQuestions()); err == nil || !strings.Contains(err.Error(), "probabilities") {
		t.Fatalf("one-hot partial choice distribution must be rejected, got %v", err)
	}

	partialScore := `{
		"is_frustrated": {"value": 0.9},
		"category": {"choice": "billing", "confidence": 0.7, "probabilities": {"billing": 0.8, "bug": 0.1, "other": 0.1}},
		"urgency": {"value": 1, "confidence": 0.5, "probabilities": {"1": 1}}
	}`
	if _, err := ParseDecisionAnswers([]byte(partialScore), mixedQuestions()); err == nil || !strings.Contains(err.Error(), "probabilities") {
		t.Fatalf("partial score distribution must be rejected, got %v", err)
	}

	complete := `{
		"is_frustrated": {"value": 0.9},
		"category": {"choice": "billing", "confidence": 0.7, "probabilities": {"billing": 0.8, "bug": 0.1, "other": 0.1}},
		"urgency": {"value": 1, "confidence": 0.5, "probabilities": {"0": 0.1, "1": 0.8, "2": 0.1}}
	}`
	answers, err := ParseDecisionAnswers([]byte(complete), mixedQuestions())
	if err != nil {
		t.Fatalf("complete answers rejected: %v", err)
	}
	if answers["urgency"].Probabilities["1"] != 0.8 {
		t.Errorf("score probabilities lost: %+v", answers["urgency"].Probabilities)
	}
}

// TestParseDecisionAnswersDerivesScoreFromDistribution pins the documented
// score contract: "Expected score: the probability-weighted average of the
// rubric levels." The emulated answer's score is derived from the validated
// distribution, not taken from the model's separately supplied value.
func TestParseDecisionAnswersDerivesScoreFromDistribution(t *testing.T) {
	args := `{
		"is_frustrated": {"value": 0.9},
		"category": {"choice": "billing", "confidence": 0.7, "probabilities": {"billing": 0.8, "bug": 0.1, "other": 0.1}},
		"urgency": {"value": 2, "confidence": 0.6, "probabilities": {"0": 0.1, "1": 0.1, "2": 0.8}}
	}`
	answers, err := ParseDecisionAnswers([]byte(args), mixedQuestions())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// 0*0.1 + 1*0.1 + 2*0.8 = 1.7 - the supplied 2 does not survive.
	got := answers["urgency"].Value.(float64)
	if math.Abs(got-1.7) > 1e-9 {
		t.Errorf("score = %v, want the probability-weighted mean 1.7", got)
	}
}

func TestParseDecisionAnswersChoiceMatchesHighestProbability(t *testing.T) {
	questions := map[string]schemas.DecisionQuestion{
		"route": {Kind: schemas.DecisionKindChoice, Criteria: map[string]string{"a": "Alpha", "b": "Beta"}},
	}
	wrong := `{"route":{"choice":"a","confidence":0.8,"probabilities":{"a":0.1,"b":0.9}}}`
	if _, err := ParseDecisionAnswers([]byte(wrong), questions); err == nil || !strings.Contains(err.Error(), "highest probability") {
		t.Fatalf("choice below another option must be rejected, got %v", err)
	}
	tied := `{"route":{"choice":"a","confidence":0.8,"probabilities":{"a":0.5,"b":0.5}}}`
	if _, err := ParseDecisionAnswers([]byte(tied), questions); err != nil {
		t.Fatalf("a choice tied for highest probability must be accepted: %v", err)
	}
}

func TestParseDecisionAnswersNormalizesNearOneDistributions(t *testing.T) {
	questions := map[string]schemas.DecisionQuestion{
		"route":    {Kind: schemas.DecisionKindChoice, Criteria: map[string]string{"a": "Alpha", "b": "Beta"}},
		"severity": {Kind: schemas.DecisionKindScore, Criteria: []string{"low", "medium", "high"}},
	}
	args := `{"route":{"choice":"a","confidence":0.8,"probabilities":{"a":0.8,"b":0.19}},"severity":{"value":2,"confidence":0.8,"probabilities":{"0":0,"1":0.05,"2":0.99}}}`
	answers, err := ParseDecisionAnswers([]byte(args), questions)
	if err != nil {
		t.Fatalf("near-one distributions should be accepted: %v", err)
	}
	for _, name := range []string{"route", "severity"} {
		var sum float64
		for _, probability := range answers[name].Probabilities {
			sum += probability
		}
		if math.Abs(sum-1) > 1e-9 {
			t.Errorf("%s probabilities sum to %v, want 1", name, sum)
		}
	}
	wantScore := (0.05 + 2*0.99) / 1.04
	if score := answers["severity"].Value.(float64); math.Abs(score-wantScore) > 1e-9 {
		t.Errorf("score = %v, want normalized expected value %v", score, wantScore)
	}
	// Exactly 5% below one is within the documented tolerance despite binary
	// floating-point rounding of decimal probabilities.
	boundary := `{"route":{"choice":"a","confidence":0.8,"probabilities":{"a":0.9,"b":0.05}},"severity":{"value":2,"confidence":0.8,"probabilities":{"0":0,"1":0.05,"2":0.95}}}`
	if _, err := ParseDecisionAnswers([]byte(boundary), questions); err != nil {
		t.Fatalf("distribution at the tolerance boundary should be accepted: %v", err)
	}
	tooFar := `{"route":{"choice":"a","confidence":0.8,"probabilities":{"a":0.8,"b":0.1}},"severity":{"value":2,"confidence":0.8,"probabilities":{"0":0,"1":0.05,"2":0.95}}}`
	if _, err := ParseDecisionAnswers([]byte(tooFar), questions); err == nil || !strings.Contains(err.Error(), "sum") {
		t.Fatalf("distribution far from one must be rejected, got %v", err)
	}
}

func TestParseDecisionAnswersRejections(t *testing.T) {
	q := mixedQuestions()
	// Every fixture is valid EXCEPT for the one named defect, and each case
	// asserts its expected error text - otherwise Go's randomized map
	// iteration can surface a different validation error first and a
	// regression in the named check would leave the case green.
	cases := map[string]struct {
		args    string
		wantSub string
	}{
		"noul out of range": {
			args:    `{"is_frustrated":{"value":1.4},"category":{"choice":"billing","confidence":0.5,"probabilities":{"billing":0.8,"bug":0.1,"other":0.1}},"urgency":{"value":1,"confidence":0.5,"probabilities":{"0":0.1,"1":0.8,"2":0.1}}}`,
			wantSub: "outside [0,1]",
		},
		"unknown choice": {
			args:    `{"is_frustrated":{"value":0.5},"category":{"choice":"nope","confidence":0.5,"probabilities":{"billing":0.8,"bug":0.1,"other":0.1}},"urgency":{"value":1,"confidence":0.5,"probabilities":{"0":0.1,"1":0.8,"2":0.1}}}`,
			wantSub: "not an allowed option",
		},
		"missing question": {
			args:    `{"is_frustrated":{"value":0.5},"category":{"choice":"billing","confidence":0.5,"probabilities":{"billing":0.8,"bug":0.1,"other":0.1}}}`,
			wantSub: "no answer for question",
		},
		"noul not a number": {
			args:    `{"is_frustrated":{"value":"high"},"category":{"choice":"billing","confidence":0.5,"probabilities":{"billing":0.8,"bug":0.1,"other":0.1}},"urgency":{"value":1,"confidence":0.5,"probabilities":{"0":0.1,"1":0.8,"2":0.1}}}`,
			wantSub: "not a number",
		},
		"malformed json": {
			args:    `not json`,
			wantSub: "not a JSON object",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseDecisionAnswers([]byte(tc.args), q)
			if err == nil {
				t.Fatalf("expected rejection for %q", name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not contain %q - a different validation fired first", err.Error(), tc.wantSub)
			}
		})
	}
}

func TestBuildDecisionToolName(t *testing.T) {
	tool, err := BuildDecisionResponsesTool(mixedQuestions())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tool.Type != schemas.ResponsesToolTypeFunction {
		t.Errorf("tool type = %v", tool.Type)
	}
	if tool.Name == nil || *tool.Name != DecisionToolName {
		t.Errorf("tool name = %v", tool.Name)
	}
	if tool.ResponsesToolFunction == nil || tool.ResponsesToolFunction.Parameters == nil {
		t.Errorf("tool function params missing: %+v", tool)
	}
}

// flattenedQuestions mirrors the harness decision-emulation matrix request that
// openrouter/anthropic/claude-opus-4.1 answered in flattened form.
func flattenedQuestions() map[string]schemas.DecisionQuestion {
	q := map[string]schemas.DecisionQuestion{}
	for _, name := range []string{
		"expresses_urgency", "has_attached_evidence", "is_angry", "is_billing_related", "is_first_time_issue",
		"is_frustrated", "is_long_time_customer", "is_polite", "is_repeat_contact", "is_satisfied",
		"mentions_competitor", "mentions_data_loss", "mentions_refund", "mentions_specific_amount",
		"reports_product_crash", "requests_human_response", "threatens_cancellation", "threatens_chargeback",
		"threatens_public_complaint", "willing_to_wait",
	} {
		q[name] = schemas.DecisionQuestion{Kind: schemas.DecisionKindNoul, Instructions: name}
	}
	choice := func(opts ...string) schemas.DecisionQuestion {
		criteria := map[string]any{}
		for _, o := range opts {
			criteria[o] = o
		}
		return schemas.DecisionQuestion{Kind: schemas.DecisionKindChoice, Instructions: "pick", Criteria: criteria}
	}
	score := func(n int) schemas.DecisionQuestion {
		levels := make([]any, n)
		for i := range levels {
			levels[i] = strconv.Itoa(i)
		}
		return schemas.DecisionQuestion{Kind: schemas.DecisionKindScore, Instructions: "rate", Criteria: levels}
	}
	q["category"] = choice("billing", "bug", "account", "other")
	q["contact_channel"] = choice("email", "phone", "chat", "social")
	q["customer_tier"] = choice("free", "pro", "enterprise")
	q["primary_emotion"] = choice("anger", "confusion", "sadness", "joy")
	q["recommended_action"] = choice("issue_refund", "request_more_info", "close_ticket", "ignore")
	q["sentiment"] = choice("positive", "neutral", "negative")
	q["churn_risk"] = score(4)
	q["customer_satisfaction"] = score(5)
	q["severity"] = score(4)
	q["urgency"] = score(4)
	return q
}

// flattenedOpus41Args is the verbatim emit_decision arguments returned by
// openrouter/anthropic/claude-opus-4.1 (captured 2026-09-23). The model flattened
// every nested answer object into the top level, leaked its XML
// "<parameter name=...>" syntax into the first field, and repeated
// "confidence"/"probabilities" as duplicate keys.
const flattenedOpus41Args = `{"category": "<parameter name=\"choice\">billing", "confidence": 1.0, "probabilities": {"account":0.0,"billing":1.0,"bug":0.0,"other":0.0}, "churn_risk": "<parameter name=\"value\">3", "confidence": 0.95, "probabilities": {"0":0.0,"1":0.0,"2":0.05,"3":0.95}, "contact_channel": "<parameter name=\"choice\">email", "confidence": 1.0, "probabilities": {"chat":0.0,"email":1.0,"phone":0.0,"social":0.0}, "customer_satisfaction": "<parameter name=\"value\">0", "confidence": 1.0, "probabilities": {"0":1.0,"1":0.0,"2":0.0,"3":0.0,"4":0.0}, "customer_tier": "<parameter name=\"choice\">pro", "confidence": 1.0, "probabilities": {"enterprise":0.0,"free":0.0,"pro":1.0}, "expresses_urgency": "<parameter name=\"value\">1.0", "confidence": 1.0, "has_attached_evidence": "<parameter name=\"value\">1.0", "confidence": 1.0, "is_angry": "<parameter name=\"value\">1.0", "confidence": 0.95, "is_billing_related": "<parameter name=\"value\">1.0", "confidence": 1.0, "is_first_time_issue": "<parameter name=\"value\">0.0", "confidence": 1.0, "is_frustrated": "<parameter name=\"value\">1.0", "confidence": 1.0, "is_long_time_customer": "<parameter name=\"value\">1.0", "confidence": 1.0, "is_polite": "<parameter name=\"value\">0.0", "confidence": 0.95, "is_repeat_contact": "<parameter name=\"value\">1.0", "confidence": 1.0, "is_satisfied": "<parameter name=\"value\">0.0", "confidence": 1.0, "mentions_competitor": "<parameter name=\"value\">1.0", "confidence": 1.0, "mentions_data_loss": "<parameter name=\"value\">0.0", "confidence": 1.0, "mentions_refund": "<parameter name=\"value\">1.0", "confidence": 1.0, "mentions_specific_amount": "<parameter name=\"value\">1.0", "confidence": 1.0, "primary_emotion": "<parameter name=\"choice\">anger", "confidence": 0.95, "probabilities": {"anger":0.95,"confusion":0.0,"joy":0.0,"sadness":0.05}, "recommended_action": "<parameter name=\"choice\">issue_refund", "confidence": 0.95, "probabilities": {"close_ticket":0.0,"ignore":0.0,"issue_refund":0.95,"request_more_info":0.05}, "reports_product_crash": "<parameter name=\"value\">0.0", "confidence": 1.0, "requests_human_response": "<parameter name=\"value\">1.0", "confidence": 1.0, "sentiment": "<parameter name=\"choice\">negative", "confidence": 1.0, "probabilities": {"negative":1.0,"neutral":0.0,"positive":0.0}, "severity": "<parameter name=\"value\">3", "confidence": 0.95, "probabilities": {"0":0.0,"1":0.0,"2":0.05,"3":0.95}, "threatens_cancellation": "<parameter name=\"value\">1.0", "confidence": 1.0, "threatens_chargeback": "<parameter name=\"value\">1.0", "confidence": 1.0, "threatens_public_complaint": "<parameter name=\"value\">1.0", "confidence": 1.0, "urgency": "<parameter name=\"value\">3", "confidence": 1.0, "probabilities": {"0":0.0,"1":0.0,"2":0.0,"3":1.0}, "willing_to_wait": "<parameter name=\"value\">0.0", "confidence": 1.0}`

func TestParseDecisionAnswersRecoversFlattenedParameterTags(t *testing.T) {
	answers, err := ParseDecisionAnswers([]byte(flattenedOpus41Args), flattenedQuestions())
	if err != nil {
		t.Fatalf("flattened answers must be recovered: %v", err)
	}
	if len(answers) != 30 {
		t.Fatalf("expected 30 answers, got %d", len(answers))
	}
	if answers["category"].Value != "billing" || answers["category"].Probabilities["billing"] != 1 {
		t.Errorf("category = %+v", answers["category"])
	}
	if answers["is_polite"].Value != 0.0 || *answers["is_polite"].Confidence != 0.95 {
		t.Errorf("is_polite = %+v", answers["is_polite"])
	}
	if answers["is_angry"].Value != 1.0 || *answers["is_angry"].Confidence != 0.95 {
		t.Errorf("is_angry = %+v", answers["is_angry"])
	}
	// Score derives from its own distribution: 2*0.05 + 3*0.95 = 2.95.
	if math.Abs(answers["churn_risk"].Value.(float64)-2.95) > 1e-9 || *answers["churn_risk"].Confidence != 0.95 {
		t.Errorf("churn_risk = %+v", answers["churn_risk"])
	}
	if answers["primary_emotion"].Value != "anger" || answers["primary_emotion"].Probabilities["sadness"] != 0.05 {
		t.Errorf("primary_emotion = %+v", answers["primary_emotion"])
	}
}

func TestParseDecisionAnswersFlattenedRejections(t *testing.T) {
	q := mixedQuestions()
	cases := map[string]struct {
		args    string
		wantSub string
	}{
		// The recovered answers still pass through every normal check.
		"recovered choice missing probabilities": {
			args:    `{"category": "<parameter name=\"choice\">billing", "confidence": 1.0, "is_frustrated": "<parameter name=\"value\">0.9", "urgency": "<parameter name=\"value\">2", "confidence": 0.6, "probabilities": {"0":0.1,"1":0.1,"2":0.8}}`,
			wantSub: "missing probabilities",
		},
		"field before any question": {
			args:    `{"confidence": 1.0, "category": "<parameter name=\"choice\">billing", "confidence": 1.0, "probabilities": {"billing":1,"bug":0,"other":0}}`,
			wantSub: "not a JSON object",
		},
		"field repeated within one answer": {
			args:    `{"is_frustrated": "<parameter name=\"value\">0.9", "value": 0.1}`,
			wantSub: "not a JSON object",
		},
		"unknown top-level key": {
			args:    `{"is_frustrated": "<parameter name=\"value\">0.9", "mood": "bad"}`,
			wantSub: "not a JSON object",
		},
		"string answer without a parameter tag": {
			args:    `{"is_frustrated": "0.9"}`,
			wantSub: "not a JSON object",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseDecisionAnswers([]byte(tc.args), q)
			if err == nil {
				t.Fatalf("expected rejection for %q", name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantSub)
			}
		})
	}
}
