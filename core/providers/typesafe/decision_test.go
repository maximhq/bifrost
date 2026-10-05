package typesafe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/bytedance/sonic"
	schemas "github.com/maximhq/bifrost/core/schemas"
)

// decisionRequest builds a BifrostDecisionRequest around the given questions.
func decisionRequest(state interface{}, questions map[string]schemas.DecisionQuestion) *schemas.BifrostDecisionRequest {
	return &schemas.BifrostDecisionRequest{
		Provider:  schemas.Typesafe,
		Model:     "jev-1.13.0",
		State:     state,
		Questions: questions,
	}
}

func TestToTypesafeDecisionRequestMixedKinds(t *testing.T) {
	req := decisionRequest("the user asked for a refund", map[string]schemas.DecisionQuestion{
		"is_angry": {
			Kind:         schemas.DecisionKindNoul,
			Instructions: "Is the user angry?",
		},
		"category": {
			Kind:         schemas.DecisionKindChoice,
			Instructions: "Pick the ticket category",
			Criteria:     map[string]interface{}{"billing": "money issues", "bug": "product defects", "other": "anything else"},
		},
		"severity": {
			Kind:         schemas.DecisionKindScore,
			Instructions: "Rate the severity",
			Criteria:     []interface{}{"cosmetic", "annoying", "blocking"},
		},
	})

	native, err := ToTypesafeDecisionRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if native.Model != "jev-1.13.0" {
		t.Errorf("model = %q, want bare jev-1.13.0", native.Model)
	}
	if native.State != "the user asked for a refund" {
		t.Errorf("state not preserved: %v", native.State)
	}
	if len(native.Questions) != 3 {
		t.Fatalf("expected 3 questions, got %d", len(native.Questions))
	}

	angry := native.Questions["is_angry"]
	if angry.Type != TypesafeQuestionTypeNoul {
		t.Errorf("is_angry type = %q, want noul", angry.Type)
	}
	if angry.Instructions != "Is the user angry?" {
		t.Errorf("is_angry instructions = %v", angry.Instructions)
	}

	category := native.Questions["category"]
	if category.Type != TypesafeQuestionTypeChoice {
		t.Errorf("category type = %q, want choice", category.Type)
	}
	criteria, ok := category.Criteria.(map[string]any)
	if !ok || len(criteria) != 3 || criteria["bug"] != "product defects" {
		t.Errorf("category criteria not preserved: %#v", category.Criteria)
	}

	severity := native.Questions["severity"]
	if severity.Type != TypesafeQuestionTypeScore {
		t.Errorf("severity type = %q, want score", severity.Type)
	}
	levels, ok := severity.Criteria.([]interface{})
	if !ok || len(levels) != 3 || levels[0] != "cosmetic" {
		t.Errorf("severity criteria not preserved losslessly: %#v", severity.Criteria)
	}
}

func TestToTypesafeDecisionRequestStructuredStateAndInstructions(t *testing.T) {
	state := map[string]interface{}{"ticket": map[string]interface{}{"id": 42, "body": "hello"}}
	structured := map[string]interface{}{"goal": "judge tone", "steps": []interface{}{"read", "decide"}}
	req := decisionRequest(state, map[string]schemas.DecisionQuestion{
		"tone_ok": {Kind: schemas.DecisionKindNoul, Instructions: structured},
	})

	native, err := ToTypesafeDecisionRequest(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(native.Questions["tone_ok"].Instructions, interface{}(structured)) {
		t.Errorf("structured instructions not lossless: %#v", native.Questions["tone_ok"].Instructions)
	}
	if !reflect.DeepEqual(native.State, interface{}(state)) {
		t.Errorf("structured state not lossless: %#v", native.State)
	}
}

func TestToTypesafeDecisionRequestStructuredCriteria(t *testing.T) {
	// The API spec types criteria descriptions as string | object | array for
	// noul and score, and string | object | array | null for choice options -
	// structured rubrics must pass through losslessly, not be coerced to strings.
	noulCriteria := map[string]any{
		"true":  map[string]any{"meaning": "approve", "signals": []any{"polite tone"}},
		"false": []any{"reject", "escalate"},
	}
	choiceCriteria := map[string]any{
		"billing": map[string]any{"rubric": "money issues", "examples": []any{"refund request"}},
		"bug":     []any{"crash", "defect"},
		"other":   nil, // spec: null when an option needs no extra detail
	}
	scoreCriteria := []any{
		"cosmetic",
		map[string]any{"level": "annoying", "examples": []any{"slow load"}},
		[]any{"blocking", "data loss"},
	}
	req := decisionRequest("state", map[string]schemas.DecisionQuestion{
		"approve":  {Kind: schemas.DecisionKindNoul, Instructions: "d", Criteria: noulCriteria},
		"category": {Kind: schemas.DecisionKindChoice, Instructions: "d", Criteria: choiceCriteria},
		"severity": {Kind: schemas.DecisionKindScore, Instructions: "d", Criteria: scoreCriteria},
	})

	native, err := ToTypesafeDecisionRequest(req)
	if err != nil {
		t.Fatalf("structured criteria rejected: %v", err)
	}
	if !reflect.DeepEqual(native.Questions["approve"].Criteria, any(noulCriteria)) {
		t.Errorf("noul criteria not lossless: %#v", native.Questions["approve"].Criteria)
	}
	if !reflect.DeepEqual(native.Questions["category"].Criteria, any(choiceCriteria)) {
		t.Errorf("choice criteria not lossless: %#v", native.Questions["category"].Criteria)
	}
	if !reflect.DeepEqual(native.Questions["severity"].Criteria, any(scoreCriteria)) {
		t.Errorf("score criteria not lossless: %#v", native.Questions["severity"].Criteria)
	}
}

// TestToTypesafeDecisionRequestCriteriaTypeMatrix exercises every allowed
// value type in every criteria slot: string | object | array for noul keys
// and score levels, plus null for choice options, and the rejected types
// (null where not allowed, numbers, booleans, unmarshalable values) per kind.
func TestToTypesafeDecisionRequestCriteriaTypeMatrix(t *testing.T) {
	str := "text description"
	obj := map[string]any{"rubric": "detail", "examples": []any{"one"}}
	arr := []any{"first", "second"}
	allowed := map[string]any{"string": str, "object": obj, "array": arr}
	rejectedNoNull := map[string]any{"number": 7, "boolean": true, "float": 3.5, "func": func() {}}

	accept := func(t *testing.T, q schemas.DecisionQuestion) *TypesafeQuestion {
		t.Helper()
		native, err := ToTypesafeDecisionRequest(decisionRequest("state", map[string]schemas.DecisionQuestion{"q": q}))
		if err != nil {
			t.Fatalf("expected acceptance, got %v", err)
		}
		question := native.Questions["q"]
		return &question
	}
	reject := func(t *testing.T, q schemas.DecisionQuestion, wantSub string) {
		t.Helper()
		_, err := ToTypesafeDecisionRequest(decisionRequest("state", map[string]schemas.DecisionQuestion{"q": q}))
		if err == nil || !strings.Contains(err.Error(), wantSub) {
			t.Fatalf("expected rejection containing %q, got %v", wantSub, err)
		}
	}

	t.Run("noul", func(t *testing.T) {
		for trueName, trueValue := range allowed {
			for falseName, falseValue := range allowed {
				t.Run("true="+trueName+"/false="+falseName, func(t *testing.T) {
					criteria := map[string]any{"true": trueValue, "false": falseValue}
					got := accept(t, schemas.DecisionQuestion{Kind: schemas.DecisionKindNoul, Instructions: "d", Criteria: criteria})
					if !reflect.DeepEqual(got.Criteria, any(criteria)) {
						t.Errorf("criteria not lossless: %#v", got.Criteria)
					}
				})
			}
			t.Run("single key true="+trueName, func(t *testing.T) {
				accept(t, schemas.DecisionQuestion{Kind: schemas.DecisionKindNoul, Instructions: "d", Criteria: map[string]any{"true": trueValue}})
			})
		}
		for name, value := range rejectedNoNull {
			t.Run("rejects "+name, func(t *testing.T) {
				reject(t, schemas.DecisionQuestion{Kind: schemas.DecisionKindNoul, Instructions: "d", Criteria: map[string]any{"true": value}}, "must be a string, object, or array")
			})
		}
		t.Run("accepts null", func(t *testing.T) {
			// SDK types: noul true/false descriptions may be null (#7599).
			native := accept(t, schemas.DecisionQuestion{Kind: schemas.DecisionKindNoul, Instructions: "d", Criteria: map[string]any{"false": nil}})
			if got := native.Criteria.(map[string]any)["false"]; got != nil {
				t.Fatalf("null description must stay null, got %#v", got)
			}
		})
	})

	t.Run("choice", func(t *testing.T) {
		for name, value := range allowed {
			t.Run("option="+name, func(t *testing.T) {
				criteria := map[string]any{"opt": value, "alt": "plain"}
				got := accept(t, schemas.DecisionQuestion{Kind: schemas.DecisionKindChoice, Instructions: "d", Criteria: criteria})
				if !reflect.DeepEqual(got.Criteria, any(criteria)) {
					t.Errorf("criteria not lossless: %#v", got.Criteria)
				}
			})
		}
		t.Run("option=null", func(t *testing.T) {
			criteria := map[string]any{"opt": nil, "alt": "plain"}
			got := accept(t, schemas.DecisionQuestion{Kind: schemas.DecisionKindChoice, Instructions: "d", Criteria: criteria})
			if !reflect.DeepEqual(got.Criteria, any(criteria)) {
				t.Errorf("criteria not lossless: %#v", got.Criteria)
			}
		})
		t.Run("all four types in one map", func(t *testing.T) {
			criteria := map[string]any{"s": str, "o": obj, "a": arr, "n": nil}
			got := accept(t, schemas.DecisionQuestion{Kind: schemas.DecisionKindChoice, Instructions: "d", Criteria: criteria})
			if !reflect.DeepEqual(got.Criteria, any(criteria)) {
				t.Errorf("criteria not lossless: %#v", got.Criteria)
			}
		})
		for name, value := range rejectedNoNull {
			t.Run("rejects "+name, func(t *testing.T) {
				reject(t, schemas.DecisionQuestion{Kind: schemas.DecisionKindChoice, Instructions: "d", Criteria: map[string]any{"opt": value}}, "must be a string, object, or array")
			})
		}
	})

	t.Run("score", func(t *testing.T) {
		// Each allowed type at each position of a three-level array.
		for name, value := range allowed {
			for position := 0; position < 3; position++ {
				t.Run(fmt.Sprintf("%s at level %d", name, position), func(t *testing.T) {
					levels := []any{"base low", "base mid", "base high"}
					levels[position] = value
					got := accept(t, schemas.DecisionQuestion{Kind: schemas.DecisionKindScore, Instructions: "d", Criteria: levels})
					if !reflect.DeepEqual(got.Criteria, any(levels)) {
						t.Errorf("levels not lossless: %#v", got.Criteria)
					}
				})
			}
		}
		t.Run("all three types in one array", func(t *testing.T) {
			levels := []any{str, obj, arr}
			got := accept(t, schemas.DecisionQuestion{Kind: schemas.DecisionKindScore, Instructions: "d", Criteria: levels})
			if !reflect.DeepEqual(got.Criteria, any(levels)) {
				t.Errorf("levels not lossless: %#v", got.Criteria)
			}
		})
		for name, value := range rejectedNoNull {
			t.Run("rejects "+name, func(t *testing.T) {
				reject(t, schemas.DecisionQuestion{Kind: schemas.DecisionKindScore, Instructions: "d", Criteria: []any{"low", value}}, "level 1 must be a string, object, or array")
			})
		}
		t.Run("accepts null level", func(t *testing.T) {
			// SDK types: score levels may be null (#7599).
			native := accept(t, schemas.DecisionQuestion{Kind: schemas.DecisionKindScore, Instructions: "d", Criteria: []any{"low", nil}})
			if levels := native.Criteria.([]any); len(levels) != 2 || levels[1] != nil {
				t.Fatalf("null level must stay null, got %#v", native.Criteria)
			}
		})
	})

	t.Run("typed Go maps with string keys accepted", func(t *testing.T) {
		// Go SDK callers pass typed maps; any map with string keys whose values
		// serialize to valid descriptions must be accepted, not only
		// map[string]string and map[string]any.
		type rubric struct {
			Meaning string `json:"meaning"`
		}
		got := accept(t, schemas.DecisionQuestion{Kind: schemas.DecisionKindChoice, Instructions: "d", Criteria: map[string]rubric{
			"billing": {Meaning: "money issues"},
			"bug":     {Meaning: "defects"},
		}})
		m, ok := got.Criteria.(map[string]any)
		if !ok || len(m) != 2 {
			t.Fatalf("typed struct map not normalized: %#v", got.Criteria)
		}
		if r, ok := m["billing"].(rubric); !ok || r.Meaning != "money issues" {
			t.Errorf("typed value not carried losslessly: %#v", m["billing"])
		}
		accept(t, schemas.DecisionQuestion{Kind: schemas.DecisionKindChoice, Instructions: "d", Criteria: map[string][]string{
			"billing": {"charges", "refunds"},
			"bug":     {"crash"},
		}})
		accept(t, schemas.DecisionQuestion{Kind: schemas.DecisionKindNoul, Instructions: "d", Criteria: map[string][]string{
			"true": {"clearly upset"},
		}})
		// Non-string keys stay rejected: not a map of named descriptions.
		reject(t, schemas.DecisionQuestion{Kind: schemas.DecisionKindChoice, Instructions: "d", Criteria: map[int]string{1: "first"}}, "must be a map of descriptions")
		// String-keyed but invalid values still fail per value.
		reject(t, schemas.DecisionQuestion{Kind: schemas.DecisionKindChoice, Instructions: "d", Criteria: map[string]int{"a": 1}}, "must be a string, object, or array")
	})

	t.Run("typed slices accepted for score criteria", func(t *testing.T) {
		// Go SDK callers pass typed slices; any slice or array whose elements
		// serialize to valid descriptions must be accepted, mirroring the
		// typed-map handling for noul and choice.
		type rubric struct {
			Level string `json:"level"`
		}
		got := accept(t, schemas.DecisionQuestion{Kind: schemas.DecisionKindScore, Instructions: "d", Criteria: []rubric{
			{Level: "low"}, {Level: "high"},
		}})
		levels, ok := got.Criteria.([]any)
		if !ok || len(levels) != 2 {
			t.Fatalf("typed struct slice not normalized: %#v", got.Criteria)
		}
		if r, ok := levels[0].(rubric); !ok || r.Level != "low" {
			t.Errorf("typed level not carried losslessly: %#v", levels[0])
		}
		accept(t, schemas.DecisionQuestion{Kind: schemas.DecisionKindScore, Instructions: "d", Criteria: [][]string{
			{"can wait"}, {"needs reply", "churn risk"},
		}})
		type namedLevels []string
		accept(t, schemas.DecisionQuestion{Kind: schemas.DecisionKindScore, Instructions: "d", Criteria: namedLevels{"low", "high"}})
		// Element validation still applies to typed slices.
		reject(t, schemas.DecisionQuestion{Kind: schemas.DecisionKindScore, Instructions: "d", Criteria: []int{1, 2}}, "level 0 must be a string, object, or array")
		reject(t, schemas.DecisionQuestion{Kind: schemas.DecisionKindScore, Instructions: "d", Criteria: []rubric{{Level: "only one"}}}, "between 2 and 10")
	})

	t.Run("typed nil pointers are JSON null descriptions", func(t *testing.T) {
		// map[string]*Rubric{"other": nil} wraps a nil pointer in a non-nil
		// interface; it serializes to null and must follow the null rules:
		// allowed for choice options and noul sides alike (#7599).
		type rubric struct {
			Meaning string `json:"meaning"`
		}
		got := accept(t, schemas.DecisionQuestion{Kind: schemas.DecisionKindChoice, Instructions: "d", Criteria: map[string]*rubric{
			"billing": {Meaning: "money issues"},
			"other":   nil,
		}})
		m, ok := got.Criteria.(map[string]any)
		if !ok || len(m) != 2 {
			t.Fatalf("typed pointer map not normalized: %#v", got.Criteria)
		}
		noul := accept(t, schemas.DecisionQuestion{Kind: schemas.DecisionKindNoul, Instructions: "d", Criteria: map[string]*rubric{
			"true": nil,
		}})
		if !isJSONNull(noul.Criteria.(map[string]any)["true"]) {
			t.Fatalf("typed nil noul description must serialize as null: %#v", noul.Criteria)
		}
	})

	t.Run("typed string maps accepted for noul and choice", func(t *testing.T) {
		// Go SDK callers pass map[string]string; both map input branches must work.
		accept(t, schemas.DecisionQuestion{Kind: schemas.DecisionKindNoul, Instructions: "d", Criteria: map[string]string{"true": "yes means this", "false": "no means this"}})
		got := accept(t, schemas.DecisionQuestion{Kind: schemas.DecisionKindChoice, Instructions: "d", Criteria: map[string]string{"a": "first", "b": "second"}})
		m, ok := got.Criteria.(map[string]any)
		if !ok || m["a"] != "first" || m["b"] != "second" {
			t.Errorf("typed string map not normalized losslessly: %#v", got.Criteria)
		}
	})
}

func TestIsJSONNull(t *testing.T) {
	type rubric struct {
		Meaning string `json:"meaning"`
	}
	var nilRubric *rubric
	var nilMap map[string]string
	var nilSlice []string
	var nilTypedSlice []rubric
	var nilIface any

	cases := []struct {
		name  string
		value any
		want  bool
	}{
		{"untyped nil", nil, true},
		{"nil interface variable", nilIface, true},
		{"typed nil struct pointer", nilRubric, true},
		{"nil map", nilMap, true},
		{"nil slice", nilSlice, true},
		{"nil typed slice", nilTypedSlice, true},
		{"raw json null", json.RawMessage("null"), true},
		{"raw json null with whitespace", json.RawMessage("  \n\tnull"), true},
		{"non-nil pointer", &rubric{Meaning: "x"}, false},
		{"zero struct", rubric{}, false},
		{"empty map", map[string]string{}, false},
		{"empty slice", []string{}, false},
		{"empty string", "", false},
		{"the string null", "null", false}, // serializes to "null" WITH quotes - a string, not null
		{"zero int", 0, false},
		{"false", false, false},
		{"unmarshalable func", func() {}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isJSONNull(tc.value); got != tc.want {
				t.Errorf("isJSONNull(%#v) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

func TestToTypesafeDecisionRequestRejections(t *testing.T) {
	cases := []struct {
		name     string
		question schemas.DecisionQuestion
		wantSub  string
	}{
		{
			name:     "unsupported kind",
			question: schemas.DecisionQuestion{Kind: schemas.DecisionKind("ranking"), Instructions: "d"},
			wantSub:  "unsupported kind",
		},
		{
			name:     "noul criteria with bad key",
			question: schemas.DecisionQuestion{Kind: schemas.DecisionKindNoul, Instructions: "d", Criteria: map[string]interface{}{"maybe": "x"}},
			wantSub:  `allows only "true" and "false" keys`,
		},
		{
			name:     "choice without criteria",
			question: schemas.DecisionQuestion{Kind: schemas.DecisionKindChoice, Instructions: "d"},
			wantSub:  "requires criteria options",
		},
		{
			name:     "choice criteria wrong shape",
			question: schemas.DecisionQuestion{Kind: schemas.DecisionKindChoice, Instructions: "d", Criteria: []interface{}{"a", "b"}},
			wantSub:  "must be a map of descriptions",
		},
		{
			name:     "choice criteria non-string description",
			question: schemas.DecisionQuestion{Kind: schemas.DecisionKindChoice, Instructions: "d", Criteria: map[string]interface{}{"a": 1}},
			wantSub:  "must be a string",
		},
		{
			name:     "score criteria missing",
			question: schemas.DecisionQuestion{Kind: schemas.DecisionKindScore, Instructions: "d"},
			wantSub:  "ordered array",
		},
		{
			name:     "score criteria too short",
			question: schemas.DecisionQuestion{Kind: schemas.DecisionKindScore, Instructions: "d", Criteria: []interface{}{"only one"}},
			wantSub:  "between 2 and 10",
		},
		{
			name:     "score criteria non-string level",
			question: schemas.DecisionQuestion{Kind: schemas.DecisionKindScore, Instructions: "d", Criteria: []interface{}{"low", 2, "high"}},
			wantSub:  "level 1 must be a string",
		},
		{
			name:     "instructions with unsupported shape",
			question: schemas.DecisionQuestion{Kind: schemas.DecisionKindNoul, Instructions: 42},
			wantSub:  "instructions must be a string, object, or array",
		},
		{
			name:     "noul criteria number description",
			question: schemas.DecisionQuestion{Kind: schemas.DecisionKindNoul, Instructions: "d", Criteria: map[string]any{"true": 7}},
			wantSub:  "must be a string, object, or array",
		},
		{
			name:     "choice criteria unmarshalable description",
			question: schemas.DecisionQuestion{Kind: schemas.DecisionKindChoice, Instructions: "d", Criteria: map[string]any{"a": func() {}}},
			wantSub:  "must be a string, object, or array",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := decisionRequest("state", map[string]schemas.DecisionQuestion{"q": tc.question})
			_, err := ToTypesafeDecisionRequest(req)
			if err == nil {
				t.Fatalf("expected rejection containing %q, got nil error", tc.wantSub)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantSub)
			}
		})
	}

	t.Run("state with unsupported shape", func(t *testing.T) {
		// Pointers to scalars marshal to JSON scalars; funcs cannot marshal at
		// all - both must be caller errors, not upstream 422s or marshal 500s.
		// nil is SDK-valid (EntryType null) and forwarded, see the SDK
		// fidelity tests.
		for _, bad := range []interface{}{true, 42, 3.14, new(42), func() {}} {
			req := decisionRequest(bad, map[string]schemas.DecisionQuestion{
				"q": {Kind: schemas.DecisionKindNoul, Instructions: "d"},
			})
			_, err := ToTypesafeDecisionRequest(req)
			if err == nil || !strings.Contains(err.Error(), "state must be a string, object, or array") {
				t.Fatalf("expected state shape rejection for %T, got %v", bad, err)
			}
		}
	})

	t.Run("instructions validated by serialized shape", func(t *testing.T) {
		type rubric struct {
			Goal string `json:"goal"`
		}
		for _, good := range []interface{}{rubric{Goal: "judge tone"}, []string{"read", "decide"}, map[string]string{"goal": "judge"}} {
			req := decisionRequest("state", map[string]schemas.DecisionQuestion{
				"q": {Kind: schemas.DecisionKindNoul, Instructions: good},
			})
			if _, err := ToTypesafeDecisionRequest(req); err != nil {
				t.Fatalf("expected %T instructions to be accepted, got %v", good, err)
			}
		}
		for _, bad := range []interface{}{7, new(3.5), func() {}} {
			req := decisionRequest("state", map[string]schemas.DecisionQuestion{
				"q": {Kind: schemas.DecisionKindNoul, Instructions: bad},
			})
			_, err := ToTypesafeDecisionRequest(req)
			if err == nil || !strings.Contains(err.Error(), "instructions must be a string, object, or array") {
				t.Fatalf("expected instructions shape rejection for %T, got %v", bad, err)
			}
		}
	})

	t.Run("typed string slice score criteria accepted", func(t *testing.T) {
		req := decisionRequest("state", map[string]schemas.DecisionQuestion{
			"q": {Kind: schemas.DecisionKindScore, Instructions: "d", Criteria: []string{"low", "medium", "high"}},
		})
		native, err := ToTypesafeDecisionRequest(req)
		if err != nil {
			t.Fatalf("expected []string score criteria to be accepted, got %v", err)
		}
		levels, ok := native.Questions["q"].Criteria.([]interface{})
		if !ok || len(levels) != 3 || levels[0] != "low" {
			t.Fatalf("score criteria not normalized to ordered array: %#v", native.Questions["q"].Criteria)
		}
	})

	t.Run("typed maps, slices, and structs are valid shapes", func(t *testing.T) {
		// Go SDK callers pass typed values; anything serializing to a JSON
		// object or array satisfies the contract, not only the interface types
		// an HTTP JSON decode produces.
		type ticket struct {
			ID   int    `json:"id"`
			Body string `json:"body"`
		}
		for _, good := range []interface{}{
			map[string]string{"key": "value"},
			[]string{"a", "b"},
			ticket{ID: 1, Body: "hello"},
		} {
			req := decisionRequest(good, map[string]schemas.DecisionQuestion{
				"q": {Kind: schemas.DecisionKindNoul, Instructions: "d"},
			})
			if _, err := ToTypesafeDecisionRequest(req); err != nil {
				t.Fatalf("expected %T to be a valid state shape, got %v", good, err)
			}
		}
	})
}

func TestToBifrostDecisionResponseAllKindsWithZeroAndFractionalValues(t *testing.T) {
	req := decisionRequest("state", map[string]schemas.DecisionQuestion{
		"is_spam": {Kind: schemas.DecisionKindNoul, Instructions: "d"},
		"lang":    {Kind: schemas.DecisionKindChoice, Instructions: "d", Criteria: map[string]interface{}{"en": "", "de": ""}},
		"quality": {Kind: schemas.DecisionKindScore, Instructions: "d", Criteria: []interface{}{"bad", "ok", "good"}},
	})

	zero := 0.0
	fractional := 1.75
	confidence := 0.9
	lang := "de"
	native := &TypesafeDecisionResponse{
		Model: "jev-1.13.0",
		Answers: map[string]TypesafeAnswer{
			// Zero is a legitimate evaluated value and must survive.
			"is_spam": {Type: TypesafeQuestionTypeNoul, Noul: &zero, Probabilities: map[string]float64{"true": 0.0, "false": 1.0}},
			"lang":    {Type: TypesafeQuestionTypeChoice, Choice: &lang, Probabilities: map[string]float64{"en": 0.1, "de": 0.9}, Confidence: &confidence},
			"quality": {Type: TypesafeQuestionTypeScore, Score: &fractional, Legend: map[string]any{"1": "bad", "2": "ok", "3": "good"}},
		},
		Usage: &TypesafeUsage{InputTokens: 120, OutputTokens: 0},
	}

	resp, bifrostErr := ToBifrostDecisionResponse(native, req)
	if bifrostErr != nil {
		t.Fatalf("unexpected error: %v", bifrostErr)
	}
	if resp.Model != "jev-1.13.0" {
		t.Errorf("model = %q", resp.Model)
	}

	spam := resp.Answers["is_spam"]
	if spam.Kind != schemas.DecisionKindNoul || spam.Value != 0.0 {
		t.Errorf("zero-valued noul answer lost: %+v", spam)
	}
	if spam.Probabilities["false"] != 1.0 {
		t.Errorf("noul probabilities lost: %+v", spam.Probabilities)
	}

	langAnswer := resp.Answers["lang"]
	if langAnswer.Value != "de" {
		t.Errorf("choice value = %v", langAnswer.Value)
	}
	if langAnswer.Confidence == nil || *langAnswer.Confidence != 0.9 {
		t.Errorf("confidence lost: %+v", langAnswer)
	}

	quality := resp.Answers["quality"]
	if quality.Value != 1.75 {
		t.Errorf("fractional score lost: %v", quality.Value)
	}
	if quality.Legend["3"] != "good" {
		t.Errorf("legend lost: %+v", quality.Legend)
	}

	if resp.Usage == nil || resp.Usage.PromptTokens != 120 || resp.Usage.CompletionTokens != 0 || resp.Usage.TotalTokens != 120 {
		t.Errorf("usage not normalized: %+v", resp.Usage)
	}
}

func TestToBifrostDecisionResponseStructuredLegendDecodes(t *testing.T) {
	// The legend echoes each score level verbatim - the SDKs type it as
	// dict[str, str | object | array] (Python) and { [score]: T[score] } (JS) -
	// so structured levels come back as objects and arrays, not strings. A
	// map[string]string legend fails to decode such a body at all.
	raw := []byte(`{
		"model": "jev-1.13.0",
		"answers": {
			"urgency": {
				"type": "score",
				"score": 1,
				"confidence": 0.8,
				"legend": {"0": "low", "1": {"examples": ["outage"], "level": "high"}, "2": ["critical", "churn risk"]}
			}
		},
		"usage": {"input_tokens": 10, "output_tokens": 0}
	}`)
	var native TypesafeDecisionResponse
	if err := sonic.Unmarshal(raw, &native); err != nil {
		t.Fatalf("structured legend failed to decode: %v", err)
	}

	req := decisionRequest("state", map[string]schemas.DecisionQuestion{
		"urgency": {Kind: schemas.DecisionKindScore, Instructions: "d", Criteria: []any{
			"low",
			map[string]any{"examples": []any{"outage"}, "level": "high"},
			[]any{"critical", "churn risk"},
		}},
	})
	resp, bifrostErr := ToBifrostDecisionResponse(&native, req)
	if bifrostErr != nil {
		t.Fatalf("unexpected error: %v", bifrostErr)
	}
	legend := resp.Answers["urgency"].Legend
	if legend["0"] != "low" {
		t.Errorf("string legend level lost: %#v", legend["0"])
	}
	if !reflect.DeepEqual(legend["1"], map[string]any{"examples": []any{"outage"}, "level": "high"}) {
		t.Errorf("object legend level not lossless: %#v", legend["1"])
	}
	if !reflect.DeepEqual(legend["2"], []any{"critical", "churn risk"}) {
		t.Errorf("array legend level not lossless: %#v", legend["2"])
	}

	// And straight back out through the native converter without truncation.
	back, err := ToTypesafeNativeDecisionResponse(resp)
	if err != nil {
		t.Fatalf("native round trip failed: %v", err)
	}
	if !reflect.DeepEqual(back.Answers["urgency"].Legend, legend) {
		t.Errorf("legend lost in native round trip: %#v", back.Answers["urgency"].Legend)
	}
}

func TestToBifrostDecisionResponseFailures(t *testing.T) {
	req := decisionRequest("state", map[string]schemas.DecisionQuestion{
		"is_spam": {Kind: schemas.DecisionKindNoul, Instructions: "d"},
	})

	t.Run("missing answer", func(t *testing.T) {
		native := &TypesafeDecisionResponse{Model: "jev-1.13.0", Answers: map[string]TypesafeAnswer{}}
		if _, err := ToBifrostDecisionResponse(native, req); err == nil {
			t.Fatal("expected error for missing answer")
		}
	})

	t.Run("answer type mismatch", func(t *testing.T) {
		choice := "yes"
		native := &TypesafeDecisionResponse{
			Model:   "jev-1.13.0",
			Answers: map[string]TypesafeAnswer{"is_spam": {Type: TypesafeQuestionTypeChoice, Choice: &choice}},
		}
		if _, err := ToBifrostDecisionResponse(native, req); err == nil {
			t.Fatal("expected error for mismatched answer type")
		}
	})

	t.Run("value missing for type", func(t *testing.T) {
		native := &TypesafeDecisionResponse{
			Model:   "jev-1.13.0",
			Answers: map[string]TypesafeAnswer{"is_spam": {Type: TypesafeQuestionTypeNoul}},
		}
		if _, err := ToBifrostDecisionResponse(native, req); err == nil {
			t.Fatal("expected error for noul answer without value")
		}
	})

	t.Run("noul value outside range", func(t *testing.T) {
		// The provider response is untrusted; a noul outside [0,1] violates the
		// documented contract and must not surface as a successful answer.
		for _, bad := range []float64{-0.2, 1.4} {
			value := bad
			native := &TypesafeDecisionResponse{
				Model:   "jev-1.13.0",
				Answers: map[string]TypesafeAnswer{"is_spam": {Type: TypesafeQuestionTypeNoul, Noul: &value}},
			}
			if _, err := ToBifrostDecisionResponse(native, req); err == nil {
				t.Fatalf("expected error for noul value %v outside [0,1]", bad)
			}
		}
	})
}

func TestNativeRoundTrip(t *testing.T) {
	native := &TypesafeDecisionRequest{
		State: "some state",
		Model: "jev-latest",
		Questions: map[string]TypesafeQuestion{
			"approve": {Type: TypesafeQuestionTypeNoul, Instructions: "Approve?", Criteria: map[string]interface{}{"true": "approve it", "false": "reject it"}},
			"bucket":  {Type: TypesafeQuestionTypeChoice, Instructions: "Bucket?", Criteria: map[string]interface{}{"a": "first", "b": "second"}},
			"rating":  {Type: TypesafeQuestionTypeScore, Instructions: "Rate it", Criteria: []interface{}{"low", "high"}},
		},
	}

	shared, err := native.ToBifrostDecisionRequest(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if shared.Provider != schemas.Typesafe || shared.Model != "jev-latest" {
		t.Errorf("routing = %s/%s", shared.Provider, shared.Model)
	}
	if len(shared.Questions) != 3 {
		t.Fatalf("expected 3 questions, got %d", len(shared.Questions))
	}

	// The shared shape must convert straight back to the native shape.
	back, err := ToTypesafeDecisionRequest(shared)
	if err != nil {
		t.Fatalf("round trip failed: %v", err)
	}
	for name, question := range native.Questions {
		got, ok := back.Questions[name]
		if !ok {
			t.Fatalf("question %q lost in round trip", name)
		}
		if got.Type != question.Type {
			t.Errorf("question %q type = %q, want %q", name, got.Type, question.Type)
		}
	}

	// Explicit provider prefix routes the same way.
	native.Model = "typesafe/jev-1.13.0"
	shared, err = native.ToBifrostDecisionRequest(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if shared.Provider != schemas.Typesafe || shared.Model != "jev-1.13.0" {
		t.Errorf("prefixed routing = %s/%s", shared.Provider, shared.Model)
	}
}

func TestToTypesafeNativeDecisionResponse(t *testing.T) {
	resp := &schemas.BifrostDecisionResponse{
		Model: "jev-1.13.0",
		Answers: map[string]schemas.DecisionAnswer{
			"approve": {Kind: schemas.DecisionKindNoul, Value: 0.25, Probabilities: map[string]float64{"true": 0.25, "false": 0.75}},
		},
		Usage: &schemas.BifrostLLMUsage{PromptTokens: 10, CompletionTokens: 0, TotalTokens: 10},
	}

	native, err := ToTypesafeNativeDecisionResponse(resp)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	answer := native.Answers["approve"]
	if answer.Type != TypesafeQuestionTypeNoul || answer.Noul == nil || *answer.Noul != 0.25 {
		t.Errorf("noul answer not rebuilt: %+v", answer)
	}
	if answer.Probabilities["false"] != 0.75 {
		t.Errorf("probabilities lost: %+v", answer.Probabilities)
	}
	if native.Usage == nil || native.Usage.InputTokens != 10 {
		t.Errorf("usage lost: %+v", native.Usage)
	}
}

// noopLogger satisfies schemas.Logger for constructor calls in unit tests.
// schemas cannot provide one and core's DefaultLogger would import-cycle here.
type noopLogger struct{}

func (noopLogger) Debug(string, ...any)                                            {}
func (noopLogger) Info(string, ...any)                                             {}
func (noopLogger) Warn(string, ...any)                                             {}
func (noopLogger) Error(string, ...any)                                            {}
func (noopLogger) Fatal(string, ...any)                                            {}
func (noopLogger) SetLevel(schemas.LogLevel)                                       {}
func (noopLogger) SetOutputType(schemas.LoggerOutputType)                          {}
func (noopLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder { return nil }

func TestListModelsEntriesCarryOwnerAndDescription(t *testing.T) {
	_, provider := newTypesafeFixture(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"models":[{"name":"jev-latest","description":"The latest iteration of TypeSafe's System One Model: Jev","release_date":"2026-09-10T18:38:01.391457+00:00"},{"name":"jev-preview","description":"A preview","release_date":"2026-09-10T18:39:06.057655+00:00"}]}`))
	})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.listModelsByKey(ctx, fixtureKey(), &schemas.BifrostListModelsRequest{})
	if bifrostErr != nil {
		t.Fatalf("unexpected error: %v", bifrostErr)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("expected the endpoint's 2 models, got %d", len(resp.Data))
	}
	for _, model := range resp.Data {
		if model.OwnedBy == nil || *model.OwnedBy != "typesafe" {
			t.Errorf("model %s missing owned_by: %v", model.ID, model.OwnedBy)
		}
		if model.Description == nil || *model.Description == "" {
			t.Errorf("model %s missing description", model.ID)
		}
		// Pricing intentionally absent here: it backfills from the datasheet in
		// the list-models handler, never from the provider.
		if model.Pricing != nil {
			t.Errorf("model %s carries hardcoded pricing; pricing must come from the datasheet", model.ID)
		}
	}
}

// TestListModelsDefaultEndpointMergesPinnedCatalog pins the default-endpoint
// rules: the live listing (aliases only) is merged with the pinned versioned
// entries, and the pinned catalog stands in when the live call fails.
func TestListModelsDefaultEndpointMergesPinnedCatalog(t *testing.T) {
	t.Run("merge keeps live metadata and adds pinned versioned entries", func(t *testing.T) {
		live := []TypesafeNativeModel{{Name: "jev-latest", Description: "live description", ReleaseDate: "2026-09-20"}}
		merged := mergePinnedCatalog(live)
		names := map[string]TypesafeNativeModel{}
		for _, m := range merged {
			names[m.Name] = m
		}
		if names["jev-latest"].Description != "live description" {
			t.Errorf("live entry must win: %+v", names["jev-latest"])
		}
		if _, ok := names["jev-1.13.0"]; !ok {
			t.Errorf("pinned versioned entry missing from %v", names)
		}
		if len(merged) != len(typesafeModels) {
			t.Errorf("expected %d models after merge, got %d", len(typesafeModels), len(merged))
		}
	})

	t.Run("pinned catalog served when the live call fails", func(t *testing.T) {
		provider, err := NewTypesafeProvider(&schemas.ProviderConfig{
			NetworkConfig: schemas.NetworkConfig{DefaultRequestTimeoutInSeconds: 5},
		}, noopLogger{})
		if err != nil {
			t.Fatalf("constructor failed: %v", err)
		}
		provider.client.Dial = func(addr string) (net.Conn, error) {
			return nil, errors.New("offline")
		}
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		resp, bifrostErr := provider.listModelsByKey(ctx, fixtureKey(), &schemas.BifrostListModelsRequest{})
		if bifrostErr != nil {
			t.Fatalf("default endpoint must fall back to the pinned catalog, got %v", bifrostErr)
		}
		if len(resp.Data) != len(typesafeModels) {
			t.Fatalf("expected the %d pinned models, got %d", len(typesafeModels), len(resp.Data))
		}
	})

	t.Run("custom endpoint failure is reported, not masked", func(t *testing.T) {
		_, provider := newTypesafeFixture(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"detail":"Not Found"}`))
		})
		ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
		if _, bifrostErr := provider.listModelsByKey(ctx, fixtureKey(), &schemas.BifrostListModelsRequest{}); bifrostErr == nil {
			t.Fatal("a custom endpoint without a catalog must not be answered with jev models")
		}
	})
}

// TestSDKFidelityNativeSuccessBodyRelayed pins that the provider keeps the
// endpoint's success body verbatim so the native route can relay answer and
// usage metadata the shared shape does not model.
func TestSDKFidelityNativeSuccessBodyRelayed(t *testing.T) {
	_, provider := newTypesafeFixture(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(fixtureSuccessBody))
	})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.Decision(ctx, fixtureKey(), decisionRequest("fixture", map[string]schemas.DecisionQuestion{
		"q": {Kind: schemas.DecisionKindNoul, Instructions: "Evaluate this state."},
	}))
	if bifrostErr != nil {
		t.Fatalf("unexpected error: %v", bifrostErr)
	}
	if string(resp.NativeResponse) != fixtureSuccessBody {
		t.Errorf("native body not kept verbatim:\n got %s\nwant %s", resp.NativeResponse, fixtureSuccessBody)
	}
	if resp.Answers["q"].Value != 0.25 {
		t.Errorf("shared shape must still be built: %+v", resp.Answers["q"])
	}
}

func TestListModelsNativeShape(t *testing.T) {
	name := "Jev (latest)"
	resp := &schemas.BifrostListModelsResponse{
		Data: []schemas.Model{{ID: "typesafe/jev-latest", Name: &name}},
	}
	native := ToTypesafeNativeListModelsResponse(resp)
	if len(native.Models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(native.Models))
	}
	if native.Models[0].Name != "jev-latest" {
		t.Errorf("native model name = %q, want bare jev-latest", native.Models[0].Name)
	}
	if native.Models[0].Description == "" || native.Models[0].ReleaseDate == "" {
		t.Errorf("catalog metadata not attached: %+v", native.Models[0])
	}
}

func TestToTypesafeNativeError(t *testing.T) {
	errType := "invalid_request_error"
	native := ToTypesafeNativeError(&schemas.BifrostError{
		Error: &schemas.ErrorField{Type: &errType, Message: "question \"q\" has unsupported kind"},
	})
	if native.Detail.ErrorType != "invalid_request_error" {
		t.Errorf("error_type = %q", native.Detail.ErrorType)
	}
	if native.Detail.Message == "" {
		t.Error("message lost")
	}

	// Missing pieces fall back rather than emitting empty fields.
	fallback := ToTypesafeNativeError(nil)
	if fallback.Detail.ErrorType != "api_error" || fallback.Detail.Message == "" {
		t.Errorf("nil error fallback = %+v", fallback.Detail)
	}
}

// --- SDK fidelity regressions (#7599) -------------------------------------
//
// The official TypeSafe JS SDK (v0.6.0 types.ts) allows EntryType to be null
// for state, instructions, and criteria descriptions, and forwards additional
// top-level request properties. The cases below pin that contract end to end:
// nullable inputs are accepted, native extensions reach the wire, and upstream
// response fidelity (headers, native error detail, live catalog) survives.

// TestSDKFidelityNullStateDistinctFromMissing pins that {"state": null} is an
// accepted SDK input while an absent state stays a 400.
func TestSDKFidelityNullStateDistinctFromMissing(t *testing.T) {
	var withNull TypesafeDecisionRequest
	if err := sonic.Unmarshal([]byte(`{"model":"jev-1.13.0","state":null,"questions":{"q":{"type":"noul","instructions":"Evaluate this state."}}}`), &withNull); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	shared, err := withNull.ToBifrostDecisionRequest(nil)
	if err != nil {
		t.Fatalf("null state is SDK-valid and must be accepted, got %v", err)
	}
	if shared.State != nil {
		t.Errorf("null state must stay null, got %#v", shared.State)
	}

	var missing TypesafeDecisionRequest
	if err := sonic.Unmarshal([]byte(`{"model":"jev-1.13.0","questions":{"q":{"type":"noul","instructions":"Evaluate this state."}}}`), &missing); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, err := missing.ToBifrostDecisionRequest(nil); err == nil || !strings.Contains(err.Error(), "state is required") {
		t.Fatalf("absent state must still be rejected, got %v", err)
	}
}

// TestSDKFidelityAcceptsNullables pins the nullable inputs the SDK types allow:
// null state, optional/null instructions, null noul descriptions, and null
// score levels. Choice options already accepted null.
func TestSDKFidelityAcceptsNullables(t *testing.T) {
	t.Run("null state serializes as null", func(t *testing.T) {
		native, err := ToTypesafeDecisionRequest(decisionRequest(nil, map[string]schemas.DecisionQuestion{
			"q": {Kind: schemas.DecisionKindNoul, Instructions: "Evaluate this state."},
		}))
		if err != nil {
			t.Fatalf("null state must be accepted, got %v", err)
		}
		body, err := sonic.Marshal(native)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if !strings.Contains(string(body), `"state":null`) {
			t.Errorf("null state must reach the wire as null, got %s", body)
		}
	})

	t.Run("nil instructions accepted", func(t *testing.T) {
		native, err := ToTypesafeDecisionRequest(decisionRequest("fixture", map[string]schemas.DecisionQuestion{
			"q": {Kind: schemas.DecisionKindChoice, Criteria: map[string]any{"a": nil, "b": nil}},
		}))
		if err != nil {
			t.Fatalf("optional instructions must be accepted, got %v", err)
		}
		if native.Questions["q"].Instructions != nil {
			t.Errorf("instructions must stay unset, got %#v", native.Questions["q"].Instructions)
		}
	})

	t.Run("null noul descriptions accepted", func(t *testing.T) {
		native, err := ToTypesafeDecisionRequest(decisionRequest("fixture", map[string]schemas.DecisionQuestion{
			"q": {Kind: schemas.DecisionKindNoul, Instructions: "d", Criteria: map[string]any{"true": nil, "false": "no"}},
		}))
		if err != nil {
			t.Fatalf("null noul description must be accepted, got %v", err)
		}
		criteria, ok := native.Questions["q"].Criteria.(map[string]any)
		if !ok || len(criteria) != 2 {
			t.Fatalf("noul criteria not carried: %#v", native.Questions["q"].Criteria)
		}
		if criteria["true"] != nil {
			t.Errorf("null description must stay null, got %#v", criteria["true"])
		}
	})

	t.Run("null score levels accepted", func(t *testing.T) {
		native, err := ToTypesafeDecisionRequest(decisionRequest("fixture", map[string]schemas.DecisionQuestion{
			"q": {Kind: schemas.DecisionKindScore, Instructions: "d", Criteria: []any{nil, "high"}},
		}))
		if err != nil {
			t.Fatalf("null score level must be accepted, got %v", err)
		}
		levels, ok := native.Questions["q"].Criteria.([]any)
		if !ok || len(levels) != 2 || levels[0] != nil {
			t.Fatalf("score levels not carried losslessly: %#v", native.Questions["q"].Criteria)
		}
	})
}

// TestSDKFidelityExtensionsForwarded pins that additional top-level native
// request properties (the SDK forwards them; classifier.dev uses "images")
// survive the native -> shared -> native round trip as passthrough params.
func TestSDKFidelityExtensionsForwarded(t *testing.T) {
	raw := `{"model":"jev-1.13.0","state":"fixture","images":["data:image/png;base64,iVBORw0KGgo="],"trace":{"id":"t-1"},"questions":{"q":{"type":"noul","instructions":"Does the image contain text?"}}}`
	var native TypesafeDecisionRequest
	if err := sonic.Unmarshal([]byte(raw), &native); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	shared, err := native.ToBifrostDecisionRequest(nil)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	back, err := ToTypesafeDecisionRequest(shared)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	extras := back.GetExtraParams()
	for _, key := range []string{"images", "trace"} {
		if _, ok := extras[key]; !ok {
			t.Errorf("native extension %q lost in round trip; extra params = %v", key, extras)
		}
	}
	for _, key := range []string{"model", "state", "questions"} {
		if _, ok := extras[key]; ok {
			t.Errorf("known field %q must not be treated as an extension", key)
		}
	}
}

// typesafeFixture is a loopback stand-in for a TypeSafe-compatible endpoint.
type typesafeFixture struct {
	*httptest.Server
	mu     sync.Mutex
	bodies [][]byte
	paths  []string
}

func (f *typesafeFixture) record(r *http.Request) []byte {
	buf := make([]byte, 1<<16)
	n, _ := r.Body.Read(buf)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bodies = append(f.bodies, append([]byte(nil), buf[:n]...))
	f.paths = append(f.paths, r.URL.Path)
	return buf[:n]
}

func (f *typesafeFixture) lastBody() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.bodies) == 0 {
		return ""
	}
	return string(f.bodies[len(f.bodies)-1])
}

func newTypesafeFixture(t *testing.T, handler func(w http.ResponseWriter, r *http.Request, body []byte)) (*typesafeFixture, *TypesafeProvider) {
	t.Helper()
	f := &typesafeFixture{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler(w, r, f.record(r))
	}))
	t.Cleanup(f.Close)
	provider, err := NewTypesafeProvider(&schemas.ProviderConfig{
		NetworkConfig: schemas.NetworkConfig{BaseURL: f.URL, DefaultRequestTimeoutInSeconds: 10},
	}, noopLogger{})
	if err != nil {
		t.Fatalf("NewTypesafeProvider: %v", err)
	}
	return f, provider
}

func fixtureKey() schemas.Key {
	return schemas.Key{Value: *schemas.NewSecretVar("test-key"), Models: []string{"*"}}
}

func headerValue(headers map[string]string, name string) string {
	for k, v := range headers {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

const fixtureSuccessBody = `{"model":"jev-1.13.0","answers":{"q":{"type":"noul","noul":0.25,"rationale":"fixture extra"}},"usage":{"input_tokens":3,"output_tokens":0,"billed":true},"request_id":"req-123"}`

// TestSDKFidelityFixtureHeaders pins that upstream response headers such as
// x-typesafe-request-id reach the response ExtraFields and the context so the
// transport forwards them to the SDK client.
func TestSDKFidelityFixtureHeaders(t *testing.T) {
	_, provider := newTypesafeFixture(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		w.Header().Set("x-typesafe-request-id", "req-123")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(fixtureSuccessBody))
	})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.Decision(ctx, fixtureKey(), decisionRequest("fixture", map[string]schemas.DecisionQuestion{
		"q": {Kind: schemas.DecisionKindNoul, Instructions: "Evaluate this state."},
	}))
	if bifrostErr != nil {
		t.Fatalf("unexpected error: %v", bifrostErr)
	}
	if got := headerValue(resp.ExtraFields.ProviderResponseHeaders, "x-typesafe-request-id"); got != "req-123" {
		t.Errorf("x-typesafe-request-id not on response headers: %v", resp.ExtraFields.ProviderResponseHeaders)
	}
	ctxHeaders, _ := ctx.Value(schemas.BifrostContextKeyProviderResponseHeaders).(map[string]string)
	if got := headerValue(ctxHeaders, "x-typesafe-request-id"); got != "req-123" {
		t.Errorf("x-typesafe-request-id not stored in context: %v", ctxHeaders)
	}
}

// TestSDKFidelityFixtureNativeError pins that a native error body
// {"detail":{"error_type","message"}} keeps its error_type and message, and that
// the retry and request-id headers are captured for forwarding.
func TestSDKFidelityFixtureNativeError(t *testing.T) {
	_, provider := newTypesafeFixture(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		w.Header().Set("Retry-After", "7")
		w.Header().Set("Retry-After-Ms", "7000")
		w.Header().Set("x-typesafe-request-id", "req-429")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"detail":{"error_type":"quota_exceeded","message":"Daily evaluation limit reached"},"billing":{"charged":false}}`))
	})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	_, bifrostErr := provider.Decision(ctx, fixtureKey(), decisionRequest("fixture", map[string]schemas.DecisionQuestion{
		"q": {Kind: schemas.DecisionKindNoul, Instructions: "Evaluate this state."},
	}))
	if bifrostErr == nil {
		t.Fatal("expected upstream 429 to surface as an error")
	}
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusTooManyRequests {
		t.Errorf("status = %v, want 429", bifrostErr.StatusCode)
	}
	if bifrostErr.Error == nil || bifrostErr.Error.Type == nil || *bifrostErr.Error.Type != "quota_exceeded" {
		t.Errorf("native error_type lost: %+v", bifrostErr.Error)
	}
	if bifrostErr.Error == nil || bifrostErr.Error.Message != "Daily evaluation limit reached" {
		t.Errorf("native message lost: %+v", bifrostErr.Error)
	}
	native := ToTypesafeNativeError(bifrostErr)
	if native.Detail.ErrorType != "quota_exceeded" || native.Detail.Message != "Daily evaluation limit reached" {
		t.Errorf("native error body rebuilt wrong: %+v", native.Detail)
	}
	ctxHeaders, _ := ctx.Value(schemas.BifrostContextKeyProviderResponseHeaders).(map[string]string)
	for name, want := range map[string]string{"Retry-After": "7", "Retry-After-Ms": "7000", "x-typesafe-request-id": "req-429"} {
		if got := headerValue(ctxHeaders, name); got != want {
			t.Errorf("header %s = %q, want %q (context headers: %v)", name, got, want, ctxHeaders)
		}
	}
}

// TestSDKFidelityFixtureImagesNotTextOnly pins that a native "images" extension
// reaches the wire instead of being silently dropped: a fixture that rejects
// image requests with a native 422 must see the images and reject, never
// answer a text-only request with 200.
func TestSDKFidelityFixtureImagesNotTextOnly(t *testing.T) {
	fixture, provider := newTypesafeFixture(t, func(w http.ResponseWriter, r *http.Request, body []byte) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(string(body), `"images"`) {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"detail":{"error_type":"images_unsupported","message":"jev cannot see images"}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(fixtureSuccessBody))
	})
	var native TypesafeDecisionRequest
	if err := sonic.Unmarshal([]byte(`{"model":"jev-1.13.0","state":"Describe this photo.","images":["data:image/png;base64,iVBORw0KGgo="],"questions":{"q":{"type":"noul","instructions":"Does the image contain text?"}}}`), &native); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	shared, err := native.ToBifrostDecisionRequest(nil)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	// The native /typesafe route asks for extensions to reach the wire.
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
	_, bifrostErr := provider.Decision(ctx, fixtureKey(), shared)
	if !strings.Contains(fixture.lastBody(), `"images"`) {
		t.Errorf("images extension never reached the wire; upstream body = %s", fixture.lastBody())
	}
	if bifrostErr == nil {
		t.Fatal("image request answered as text-only 200; expected the fixture's native 422")
	}
	if bifrostErr.StatusCode == nil || *bifrostErr.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status = %v, want 422", bifrostErr.StatusCode)
	}
	if bifrostErr.Error == nil || bifrostErr.Error.Type == nil || *bifrostErr.Error.Type != "images_unsupported" {
		t.Errorf("native error_type lost: %+v", bifrostErr.Error)
	}
}

// TestSDKFidelityListModelsUpstreamCatalog pins that a compatible endpoint's
// native GET /v1/models catalog is served instead of the built-in jev catalog,
// with release_date carried into the native listing shape.
func TestSDKFidelityListModelsUpstreamCatalog(t *testing.T) {
	fixture, provider := newTypesafeFixture(t, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"models":[{"name":"dgemma","description":"image classifier","release_date":"2026-09-01T00:00:00+00:00"}]}`))
	})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.ListModels(ctx, []schemas.Key{fixtureKey()}, &schemas.BifrostListModelsRequest{})
	if bifrostErr != nil {
		t.Fatalf("unexpected error: %v", bifrostErr)
	}
	fixture.mu.Lock()
	hits := len(fixture.paths)
	fixture.mu.Unlock()
	if hits == 0 {
		t.Errorf("upstream GET /v1/models was never called; static catalog served instead")
	}
	ids := make([]string, 0, len(resp.Data))
	for _, model := range resp.Data {
		ids = append(ids, model.ID)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "typesafe/dgemma" {
		t.Fatalf("expected only the endpoint's catalog [typesafe/dgemma], got %v", ids)
	}
	if resp.Data[0].Description == nil || *resp.Data[0].Description != "image classifier" {
		t.Errorf("upstream description lost: %+v", resp.Data[0])
	}
	native := ToTypesafeNativeListModelsResponse(resp)
	if len(native.Models) != 1 || native.Models[0].Name != "dgemma" || native.Models[0].ReleaseDate != "2026-09-01T00:00:00+00:00" {
		t.Errorf("native listing lost upstream metadata: %+v", native.Models)
	}
}

const customTypesafeProviderName = schemas.ModelProvider("my-typesafe")

// newCustomTypesafeFixture builds a provider that backs the custom provider
// my-typesafe (base_provider_type typesafe) against a loopback endpoint.
func newCustomTypesafeFixture(t *testing.T, custom schemas.CustomProviderConfig, handler func(w http.ResponseWriter, r *http.Request, body []byte)) (*typesafeFixture, *TypesafeProvider) {
	t.Helper()
	f := &typesafeFixture{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler(w, r, f.record(r))
	}))
	t.Cleanup(f.Close)
	custom.CustomProviderKey = string(customTypesafeProviderName)
	custom.BaseProviderType = schemas.Typesafe
	provider, err := NewTypesafeProvider(&schemas.ProviderConfig{
		NetworkConfig:        schemas.NetworkConfig{BaseURL: f.URL, DefaultRequestTimeoutInSeconds: 10},
		CustomProviderConfig: &custom,
	}, noopLogger{})
	if err != nil {
		t.Fatalf("NewTypesafeProvider: %v", err)
	}
	return f, provider
}

func writeFixtureSuccess(w http.ResponseWriter, r *http.Request, _ []byte) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodGet {
		_, _ = w.Write([]byte(`{"models":[{"name":"jev-latest","description":"custom endpoint jev"}]}`))
		return
	}
	_, _ = w.Write([]byte(fixtureSuccessBody))
}

// TestCustomProviderKeyIsCustomName pins that a Typesafe-backed custom provider
// reports its own name, which Bifrost uses for provider lookup and key selection.
func TestCustomProviderKeyIsCustomName(t *testing.T) {
	_, provider := newCustomTypesafeFixture(t, schemas.CustomProviderConfig{}, writeFixtureSuccess)
	if got := provider.GetProviderKey(); got != customTypesafeProviderName {
		t.Fatalf("GetProviderKey() = %q, want %q", got, customTypesafeProviderName)
	}
	_, standard := newTypesafeFixture(t, writeFixtureSuccess)
	if got := standard.GetProviderKey(); got != schemas.Typesafe {
		t.Fatalf("standard GetProviderKey() = %q, want %q", got, schemas.Typesafe)
	}
}

// TestCustomProviderAllowedRequestsGate pins that allowed_requests gates the
// decision and list-models operations without reaching the upstream.
func TestCustomProviderAllowedRequestsGate(t *testing.T) {
	fixture, provider := newCustomTypesafeFixture(t, schemas.CustomProviderConfig{
		AllowedRequests: &schemas.AllowedRequests{},
	}, writeFixtureSuccess)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)

	_, bifrostErr := provider.Decision(ctx, fixtureKey(), decisionRequest("fixture", map[string]schemas.DecisionQuestion{
		"q": {Kind: schemas.DecisionKindNoul, Instructions: "Evaluate this state."},
	}))
	if bifrostErr == nil || bifrostErr.Error == nil || bifrostErr.Error.Code == nil || *bifrostErr.Error.Code != "unsupported_operation" {
		t.Fatalf("decision must be refused as unsupported_operation, got %+v", bifrostErr)
	}
	if !strings.Contains(bifrostErr.Error.Message, string(customTypesafeProviderName)) {
		t.Errorf("error must name the custom provider: %q", bifrostErr.Error.Message)
	}

	_, bifrostErr = provider.ListModels(ctx, []schemas.Key{fixtureKey()}, &schemas.BifrostListModelsRequest{})
	if bifrostErr == nil || bifrostErr.Error == nil || bifrostErr.Error.Code == nil || *bifrostErr.Error.Code != "unsupported_operation" {
		t.Fatalf("list models must be refused as unsupported_operation, got %+v", bifrostErr)
	}

	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if len(fixture.paths) != 0 {
		t.Errorf("gated operations must not reach the upstream, got %v", fixture.paths)
	}

	_, allowed := newCustomTypesafeFixture(t, schemas.CustomProviderConfig{
		AllowedRequests: &schemas.AllowedRequests{Decision: true},
	}, writeFixtureSuccess)
	if _, bifrostErr := allowed.Decision(ctx, fixtureKey(), decisionRequest("fixture", map[string]schemas.DecisionQuestion{
		"q": {Kind: schemas.DecisionKindNoul, Instructions: "Evaluate this state."},
	})); bifrostErr != nil {
		t.Fatalf("decision allowed by allowed_requests must succeed: %v", bifrostErr)
	}
}

// TestCustomProviderRequestPathOverride pins that request_path_overrides
// redirect the decision call, as a path on base_url or as an absolute URL.
func TestCustomProviderRequestPathOverride(t *testing.T) {
	fixture, provider := newCustomTypesafeFixture(t, schemas.CustomProviderConfig{
		RequestPathOverrides: map[schemas.RequestType]string{schemas.DecisionRequest: "gateway/systemone"},
	}, writeFixtureSuccess)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	if _, bifrostErr := provider.Decision(ctx, fixtureKey(), decisionRequest("fixture", map[string]schemas.DecisionQuestion{
		"q": {Kind: schemas.DecisionKindNoul, Instructions: "Evaluate this state."},
	})); bifrostErr != nil {
		t.Fatalf("unexpected error: %v", bifrostErr)
	}
	fixture.mu.Lock()
	gotPaths := append([]string(nil), fixture.paths...)
	fixture.mu.Unlock()
	if len(gotPaths) != 1 || gotPaths[0] != "/gateway/systemone" {
		t.Fatalf("path override not applied, upstream saw %v", gotPaths)
	}

	absolute, _ := newCustomTypesafeFixture(t, schemas.CustomProviderConfig{}, writeFixtureSuccess)
	_, redirected := newCustomTypesafeFixture(t, schemas.CustomProviderConfig{
		RequestPathOverrides: map[schemas.RequestType]string{schemas.DecisionRequest: absolute.URL + "/elsewhere/systemone"},
	}, writeFixtureSuccess)
	if _, bifrostErr := redirected.Decision(ctx, fixtureKey(), decisionRequest("fixture", map[string]schemas.DecisionQuestion{
		"q": {Kind: schemas.DecisionKindNoul, Instructions: "Evaluate this state."},
	})); bifrostErr != nil {
		t.Fatalf("unexpected error: %v", bifrostErr)
	}
	absolute.mu.Lock()
	defer absolute.mu.Unlock()
	if len(absolute.paths) != 1 || absolute.paths[0] != "/elsewhere/systemone" {
		t.Fatalf("absolute URL override not applied, target saw %v", absolute.paths)
	}
}

// TestCustomProviderListModelsPrefixAndNativeShape pins that a custom
// provider's models are listed under its own name and that the native
// listing restores the bare upstream name.
func TestCustomProviderListModelsPrefixAndNativeShape(t *testing.T) {
	_, provider := newCustomTypesafeFixture(t, schemas.CustomProviderConfig{}, writeFixtureSuccess)
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.ListModels(ctx, []schemas.Key{fixtureKey()}, &schemas.BifrostListModelsRequest{})
	if bifrostErr != nil {
		t.Fatalf("unexpected error: %v", bifrostErr)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "my-typesafe/jev-latest" {
		ids := make([]string, 0, len(resp.Data))
		for _, model := range resp.Data {
			ids = append(ids, model.ID)
		}
		t.Fatalf("expected [my-typesafe/jev-latest], got %v", ids)
	}
	native := ToTypesafeNativeListModelsResponse(resp)
	if len(native.Models) != 1 || native.Models[0].Name != "jev-latest" || native.Models[0].Description != "custom endpoint jev" {
		t.Errorf("native listing must carry the bare name and upstream metadata: %+v", native.Models)
	}
}

// TestCustomProviderKeylessListModels pins that a keyless custom provider
// lists models without any configured key and sends no Authorization header.
func TestCustomProviderKeylessListModels(t *testing.T) {
	var sawAuth bool
	_, provider := newCustomTypesafeFixture(t, schemas.CustomProviderConfig{IsKeyLess: true}, func(w http.ResponseWriter, r *http.Request, body []byte) {
		if r.Header.Get("Authorization") != "" {
			sawAuth = true
		}
		writeFixtureSuccess(w, r, body)
	})
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	resp, bifrostErr := provider.ListModels(ctx, nil, &schemas.BifrostListModelsRequest{})
	if bifrostErr != nil {
		t.Fatalf("unexpected error: %v", bifrostErr)
	}
	if len(resp.Data) != 1 || resp.Data[0].ID != "my-typesafe/jev-latest" {
		t.Fatalf("keyless listing must serve the endpoint catalog, got %+v", resp.Data)
	}
	if sawAuth {
		t.Errorf("keyless provider must not send an Authorization header")
	}
}
