package typesafe

import (
	"encoding/json"
	"strings"
	"testing"

	providerUtils "github.com/maximhq/bifrost/core/providers/utils"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

func locateRequest(questions ...schemas.DecisionQuestion) *schemas.BifrostDecisionRequest {
	return &schemas.BifrostDecisionRequest{
		Provider:  schemas.Perplexity,
		Model:     "pplx-decider-v1.3",
		Input:     schemas.DecisionInput{Text: schemas.Ptr("O2: 2L nasal cannula")},
		Questions: questions,
	}
}

func locateQuestion(name, instructions string) schemas.DecisionQuestion {
	return schemas.DecisionQuestion{Type: schemas.DecisionTypeLocate, Name: schemas.Ptr(name), Instructions: schemas.NewDecisionText(instructions)}
}

// TestToTypesafeDecisionRequestRejectsLocate pins that endpoints which do not opt in
// (Typesafe itself, Azure, Cloudflare, Ollama, OpenRouter, Databricks) refuse a locate
// question with a 400 that names where it is served.
func TestToTypesafeDecisionRequestRejectsLocate(t *testing.T) {
	_, err := ToTypesafeDecisionRequest(locateRequest(locateQuestion("value", "the oxygen text")))
	if err == nil {
		t.Fatal("expected locate to be rejected")
	}
	bifrostErr, ok := providerUtils.AsBifrostBadRequestError(err)
	if !ok {
		t.Fatalf("error %v is not a bad-request error", err)
	}
	if !strings.Contains(bifrostErr.Error.Message, "pplx-decider-v1.3") || !strings.Contains(bifrostErr.Error.Message, `"value"`) {
		t.Errorf("message = %q, want the question name and where locate is served", bifrostErr.Error.Message)
	}
}

// TestToTypesafeDecisionRequestWithLocate pins the native locate shape: type and
// instructions only, alongside other question types.
func TestToTypesafeDecisionRequestWithLocate(t *testing.T) {
	req := locateRequest(
		locateQuestion("value", "The text this value was read from."),
		schemas.DecisionQuestion{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("on_o2"), Instructions: schemas.NewDecisionText("On oxygen?")},
	)
	native, err := ToTypesafeDecisionRequestWithLocate(req)
	if err != nil {
		t.Fatalf("ToTypesafeDecisionRequestWithLocate: %v", err)
	}
	body, err := providerUtils.MarshalProviderRequest(native)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		State     string                     `json:"state"`
		Questions map[string]json.RawMessage `json:"questions"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatal(err)
	}
	if got := string(decoded.Questions["value"]); got != `{"type":"locate","instructions":"The text this value was read from."}` {
		t.Errorf("locate question = %s", got)
	}
	if got := string(decoded.Questions["on_o2"]); !strings.Contains(got, `"type":"noul"`) {
		t.Errorf("predicate question = %s", got)
	}
	if decoded.State != "O2: 2L nasal cannula" {
		t.Errorf("state = %q", decoded.State)
	}
}

func TestToTypesafeDecisionRequestWithLocateRejectsCriteria(t *testing.T) {
	for name, question := range map[string]schemas.DecisionQuestion{
		"criteria": {Type: schemas.DecisionTypeLocate, Name: schemas.Ptr("q"), Instructions: schemas.NewDecisionText("x"), Criteria: &schemas.DecisionCriteria{True: schemas.NewDecisionText("y")}},
		"choices":  {Type: schemas.DecisionTypeLocate, Name: schemas.Ptr("q"), Instructions: schemas.NewDecisionText("x"), Choices: []schemas.DecisionChoice{{Value: schemas.DecisionScalar{Str: schemas.Ptr("a")}}}},
		"levels":   {Type: schemas.DecisionTypeLocate, Name: schemas.Ptr("q"), Instructions: schemas.NewDecisionText("x"), Levels: []schemas.DecisionLevel{{Label: "low"}}},
	} {
		_, err := ToTypesafeDecisionRequestWithLocate(locateRequest(question))
		if _, ok := providerUtils.AsBifrostBadRequestError(err); !ok {
			t.Errorf("%s: err = %v, want a 400", name, err)
		}
	}
}

// TestToBifrostDecisionResponseLocateSpans pins that spans come back exactly as the
// endpoint sent them, and that no match stays an empty (not absent) list on the wire.
func TestToBifrostDecisionResponseLocateSpans(t *testing.T) {
	req := locateRequest(locateQuestion("value", "oxygen"), locateQuestion("missing", "a phone number"))
	var native TypesafeDecisionResponse
	if err := json.Unmarshal([]byte(`{"model":"pplx-decider-v1.3","answers":{
		"value":{"type":"locate","spans":[{"text":"2L nasal cannula","start":4,"end":20,"score":0.97},{"text":"O2","start":0,"end":2,"score":1}]},
		"missing":{"type":"locate","spans":[]}},
		"usage":{"input_tokens":120,"output_tokens":3}}`), &native); err != nil {
		t.Fatal(err)
	}
	resp, bifrostErr := ToBifrostDecisionResponse(&native, req)
	if bifrostErr != nil {
		t.Fatalf("ToBifrostDecisionResponse: %v", bifrostErr.Error.Message)
	}
	value := resp.Answers[0]
	if value.Type != schemas.DecisionTypeLocate || value.Spans == nil || len(*value.Spans) != 2 {
		t.Fatalf("value answer = %+v", value)
	}
	first := (*value.Spans)[0]
	if first.Text != "2L nasal cannula" || first.Start != 4 || first.End != 20 || first.Score == nil || *first.Score != 0.97 {
		t.Errorf("first span = %+v", first)
	}
	if resp.Answers[1].Spans == nil || len(*resp.Answers[1].Spans) != 0 {
		t.Errorf("missing answer spans = %v, want an empty list", resp.Answers[1].Spans)
	}
	wire, err := schemas.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`{"type":"locate","name":"value","spans":[{"text":"2L nasal cannula","start":4,"end":20,"score":0.97},{"text":"O2","start":0,"end":2,"score":1}]}`,
		`{"type":"locate","name":"missing","spans":[]}`,
	} {
		if !strings.Contains(string(wire), want) {
			t.Errorf("wire =\n%s\nwant %s", wire, want)
		}
	}
	if resp.Usage == nil || resp.Usage.PromptTokens != 120 || resp.Usage.CompletionTokens != 3 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestToBifrostDecisionResponseLocateFailures(t *testing.T) {
	req := locateRequest(locateQuestion("value", "oxygen"))
	for name, body := range map[string]string{
		"no spans key": `{"answers":{"value":{"type":"locate"}}}`,
		"wrong type":   `{"answers":{"value":{"type":"noul","noul":0.5}}}`,
	} {
		var native TypesafeDecisionResponse
		if err := json.Unmarshal([]byte(body), &native); err != nil {
			t.Fatal(err)
		}
		if _, bifrostErr := ToBifrostDecisionResponse(&native, req); bifrostErr == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// TestNonLocateAnswersOmitSpans pins that every other answer type keeps its wire shape.
func TestNonLocateAnswersOmitSpans(t *testing.T) {
	wire, err := schemas.Marshal(schemas.DecisionAnswer{Type: schemas.DecisionTypePredicate, Probability: schemas.Ptr(0.5)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), "spans") {
		t.Errorf("predicate answer = %s, want no spans key", wire)
	}
}

// TestToBifrostDecisionQuestionsLocate pins the map form (Perplexity's own body shape):
// a locate question normalizes to locate, and criteria Perplexity ignores are dropped.
func TestToBifrostDecisionQuestionsLocate(t *testing.T) {
	var questions map[string]TypesafeQuestion
	if err := json.Unmarshal([]byte(`{"value":{"type":"locate","instructions":"oxygen"},"dob":{"kind":"locate","instructions":"date of birth","criteria":{"true":"x"}}}`), &questions); err != nil {
		t.Fatal(err)
	}
	normalized, err := ToBifrostDecisionQuestions(questions)
	if err != nil {
		t.Fatalf("ToBifrostDecisionQuestions: %v", err)
	}
	if len(normalized) != 2 {
		t.Fatalf("questions = %+v", normalized)
	}
	for _, question := range normalized {
		if question.Type != schemas.DecisionTypeLocate || question.Criteria != nil {
			t.Errorf("question %s = %+v", *question.Name, question)
		}
	}
	if *normalized[1].Name != "value" || *normalized[1].Instructions.Text != "oxygen" {
		t.Errorf("value question = %+v", normalized[1])
	}
}

func TestToTypesafeAnswersLocate(t *testing.T) {
	spans := []schemas.DecisionSpan{{Text: "O2", Start: 0, End: 2, Score: schemas.Ptr(1.0)}}
	empty := []schemas.DecisionSpan{}
	native := ToTypesafeAnswers([]schemas.DecisionAnswer{
		{Type: schemas.DecisionTypeLocate, Name: schemas.Ptr("value"), Spans: &spans},
		{Type: schemas.DecisionTypeLocate, Name: schemas.Ptr("none"), Spans: &empty},
	})
	if got := native["value"]; got.Type != TypesafeQuestionTypeLocate || got.Spans == nil || (*got.Spans)[0].Text != "O2" {
		t.Errorf("value = %+v", got)
	}
	wire, err := schemas.Marshal(native["none"])
	if err != nil {
		t.Fatal(err)
	}
	if string(wire) != `{"type":"locate","spans":[]}` {
		t.Errorf("none = %s", wire)
	}
}
