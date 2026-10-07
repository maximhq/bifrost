package llmtests

import (
	"context"
	"os"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// BasicDecisionExpectations validates common decision invariants for provider
// tests: every requested question produces an answer of its declared kind with
// a well-typed value.
func BasicDecisionExpectations(t *testing.T, response *schemas.BifrostDecisionResponse, request *schemas.BifrostDecisionRequest) {
	t.Helper()

	if response == nil {
		t.Fatal("❌ Decision response is nil")
	}
	if len(response.Answers) == 0 {
		t.Fatal("❌ Decision response carries no answers")
	}

	for name, question := range request.Questions {
		answer, ok := response.Answers[name]
		if !ok {
			t.Fatalf("❌ Question %q produced no answer", name)
		}
		if answer.Kind != question.Kind {
			t.Fatalf("❌ Answer %q has kind %q; expected %q", name, answer.Kind, question.Kind)
		}
		switch answer.Kind {
		case schemas.DecisionKindNoul:
			number, ok := answer.Value.(float64)
			if !ok || number < 0 || number > 1 {
				t.Fatalf("❌ Noul answer %q is not a number in [0,1]: %v", name, answer.Value)
			}
		case schemas.DecisionKindChoice:
			if _, ok := answer.Value.(string); !ok {
				t.Fatalf("❌ Choice answer %q is not a string: %v", name, answer.Value)
			}
		case schemas.DecisionKindScore:
			if _, ok := answer.Value.(float64); !ok {
				t.Fatalf("❌ Score answer %q is not a number: %v", name, answer.Value)
			}
		default:
			t.Fatalf("❌ Answer %q has unknown kind %q", name, answer.Kind)
		}
	}
}

// RunDecisionTest executes the decision test scenario against a live provider.
func RunDecisionTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.Decision {
		t.Logf("Decision not supported for provider %s", testConfig.Provider)
		return
	}

	if strings.TrimSpace(testConfig.DecisionModel) == "" {
		t.Skipf("Decision enabled but model is not configured for provider %s; skipping", testConfig.Provider)
	}

	t.Run("Decision", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		state := "Customer message: I was double charged for my subscription last month and nobody has replied to my two previous emails. I want a refund today or I am cancelling."

		request := &schemas.BifrostDecisionRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.DecisionModel,
			State:    state,
			Questions: map[string]schemas.DecisionQuestion{
				"is_frustrated": {
					Kind:         schemas.DecisionKindNoul,
					Instructions: "Is the customer frustrated?",
				},
				"category": {
					Kind:         schemas.DecisionKindChoice,
					Instructions: "Pick the ticket category",
					Criteria: map[string]interface{}{
						"billing": "charges, refunds, invoices",
						"bug":     "product defects",
						"other":   "anything else",
					},
				},
				"urgency": {
					Kind:         schemas.DecisionKindScore,
					Instructions: "Rate how urgently this ticket needs a human reply",
					Criteria:     []interface{}{"can wait a week", "should be answered soon", "needs a reply today"},
				},
			},
			Fallbacks: testConfig.DecisionFallbacks,
		}

		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		response, bifrostErr := client.DecisionRequest(bfCtx, request)

		if bifrostErr != nil {
			t.Fatalf("❌ Decision request failed: %v", GetErrorMessage(bifrostErr))
		}

		BasicDecisionExpectations(t, response, request)

		if response.Usage != nil {
			t.Logf("📊 Usage: prompt_tokens=%d, total_tokens=%d", response.Usage.PromptTokens, response.Usage.TotalTokens)
		}
		t.Logf("✅ Decision test passed: model=%s, answers=%d", response.Model, len(response.Answers))
	})
}

// BasicOrderedDecisionExpectations validates the ordered-form invariants: one
// answer per question, in request order, each of the type its question asked
// for, with names echoed for named questions. A refusal is a valid answer to
// any question but carries no value.
func BasicOrderedDecisionExpectations(t *testing.T, response *schemas.BifrostDecisionResponse, request *schemas.BifrostDecisionRequest) {
	t.Helper()

	if response == nil {
		t.Fatal("❌ Decision response is nil")
	}
	if !response.UsesOrderedForm() {
		t.Fatal("❌ Ordered decision request did not produce ordered answers")
	}
	if len(response.OrderedAnswers) != len(request.OrderedQuestions) {
		t.Fatalf("❌ Got %d answers for %d questions", len(response.OrderedAnswers), len(request.OrderedQuestions))
	}

	for i, question := range request.OrderedQuestions {
		answer := response.OrderedAnswers[i]
		if !answer.IsRecognized() {
			t.Fatalf("❌ Answer %d has unrecognized type %q", i, answer.Type)
		}
		if question.Name != nil && (answer.Name == nil || *answer.Name != *question.Name) {
			t.Fatalf("❌ Answer %d is not named %q (answers must follow request order)", i, *question.Name)
		}
		if question.Name == nil && answer.Name != nil {
			t.Fatalf("❌ Answer %d is named %q but its question is unnamed", i, *answer.Name)
		}
		if answer.Type == schemas.DecisionOrderedKindRefusal {
			if answer.Probability != nil || answer.Choice != nil || answer.Score != nil {
				t.Fatalf("❌ Refusal answer %d carries a value", i)
			}
			continue
		}
		if answer.Type != question.Type {
			t.Fatalf("❌ Answer %d has type %q; expected %q", i, answer.Type, question.Type)
		}
		switch answer.Type {
		case schemas.DecisionOrderedKindPredicate:
			if answer.Probability == nil || *answer.Probability < 0 || *answer.Probability > 1 {
				t.Fatalf("❌ Predicate answer %d carries no probability in [0,1]", i)
			}
		case schemas.DecisionOrderedKindChoice:
			if answer.Choice == nil || (answer.Choice.Str == nil && answer.Choice.Bool == nil) {
				t.Fatalf("❌ Choice answer %d carries no string or boolean choice", i)
			}
			if !decisionChoiceIsOffered(question.Choices, *answer.Choice) {
				t.Fatalf("❌ Choice answer %d is not one of the options its question offered", i)
			}
		case schemas.DecisionOrderedKindScore:
			if answer.Score == nil {
				t.Fatalf("❌ Score answer %d carries no score", i)
			}
		}
	}
}

// decisionChoiceIsOffered reports whether the chosen scalar equals one of the
// question's options, comparing the type as well as the value so a boolean
// never satisfies a string-choice question or the reverse.
func decisionChoiceIsOffered(options []schemas.DecisionChoiceOption, choice schemas.DecisionScalar) bool {
	for _, option := range options {
		switch {
		case choice.Str != nil && option.Value.Str != nil && *choice.Str == *option.Value.Str:
			return true
		case choice.Bool != nil && option.Value.Bool != nil && *choice.Bool == *option.Value.Bool:
			return true
		}
	}
	return false
}

// RunDecisionOrderedTest executes the ordered decision form against a live
// provider: text input, predicate, string choice, boolean choice, and score
// questions, with one question left unnamed.
func RunDecisionOrderedTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.DecisionOrdered {
		t.Logf("Ordered decision not supported for provider %s", testConfig.Provider)
		return
	}

	if strings.TrimSpace(testConfig.DecisionModel) == "" {
		t.Skipf("Ordered decision enabled but model is not configured for provider %s; skipping", testConfig.Provider)
	}

	t.Run("DecisionOrdered", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		input := "I was double charged for my subscription last month and nobody has replied to my two previous emails. I want a refund today or I am cancelling."
		category := "category"
		urgency := "urgency"
		frustrated := "is_frustrated"

		request := &schemas.BifrostDecisionRequest{
			Provider: testConfig.Provider,
			Model:    testConfig.DecisionModel,
			Input:    &schemas.DecisionInput{Text: &input},
			OrderedQuestions: []schemas.DecisionOrderedQuestion{
				{Type: schemas.DecisionOrderedKindPredicate, Name: &frustrated, Instructions: "Is the customer frustrated?"},
				{
					Type:         schemas.DecisionOrderedKindChoice,
					Name:         &category,
					Instructions: "Pick the ticket category",
					Choices: []schemas.DecisionChoiceOption{
						{Value: schemas.DecisionScalar{Str: schemas.Ptr("billing")}, Description: schemas.Ptr("charges, refunds, invoices")},
						{Value: schemas.DecisionScalar{Str: schemas.Ptr("bug")}},
						{Value: schemas.DecisionScalar{Str: schemas.Ptr("other")}},
					},
				},
				{
					Type:         schemas.DecisionOrderedKindChoice,
					Instructions: "Does the customer ask for a refund?",
					Choices: []schemas.DecisionChoiceOption{
						{Value: schemas.DecisionScalar{Bool: schemas.Ptr(true)}},
						{Value: schemas.DecisionScalar{Bool: schemas.Ptr(false)}},
					},
				},
				{
					Type:         schemas.DecisionOrderedKindScore,
					Name:         &urgency,
					Instructions: "Rate how urgently this ticket needs a human reply",
					Levels: []schemas.DecisionScoreLevel{
						{Label: "Low", Description: schemas.Ptr("can wait a week")},
						{Label: "Medium"},
						{Label: "High", Description: schemas.Ptr("needs a reply today")},
					},
				},
			},
			Fallbacks: testConfig.DecisionFallbacks,
		}

		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		response, bifrostErr := client.DecisionRequest(bfCtx, request)
		if bifrostErr != nil {
			t.Fatalf("❌ Ordered decision request failed: %v", GetErrorMessage(bifrostErr))
		}

		BasicOrderedDecisionExpectations(t, response, request)
		t.Logf("✅ Ordered decision test passed: model=%s, answers=%d", response.Model, len(response.OrderedAnswers))
	})
}

// RunDecisionEmulationTest runs a decision against a general LLM model, exercising
// the emulation path (the provider has no native decision support, so Bifrost
// answers via tool-calling / structured output). Every question must come back
// as a typed answer with an LLM-estimated confidence.
func RunDecisionEmulationTest(t *testing.T, client *bifrost.Bifrost, ctx context.Context, testConfig ComprehensiveTestConfig) {
	if !testConfig.Scenarios.DecisionEmulation {
		t.Logf("Decision emulation not enabled for provider %s", testConfig.Provider)
		return
	}
	if strings.TrimSpace(testConfig.DecisionEmulationModel) == "" {
		t.Skipf("Decision emulation enabled but no model configured for provider %s; skipping", testConfig.Provider)
	}

	t.Run("DecisionEmulation", func(t *testing.T) {
		if os.Getenv("SKIP_PARALLEL_TESTS") != "true" {
			t.Parallel()
		}

		provider, model := schemas.ParseModelString(testConfig.DecisionEmulationModel, testConfig.Provider)
		request := &schemas.BifrostDecisionRequest{
			Provider: provider,
			Model:    model,
			State:    "Customer message: I was double charged and support ignored my emails. I want a refund now or I cancel.",
			Questions: map[string]schemas.DecisionQuestion{
				"is_frustrated": {Kind: schemas.DecisionKindNoul, Instructions: "Is the customer frustrated?"},
				"category": {
					Kind:         schemas.DecisionKindChoice,
					Instructions: "Pick the ticket category",
					Criteria:     map[string]interface{}{"billing": "charges and refunds", "bug": "product defects", "other": "anything else"},
				},
				"urgency": {
					Kind:         schemas.DecisionKindScore,
					Instructions: "Rate how urgently this needs a human reply",
					Criteria:     []interface{}{"low", "medium", "high"},
				},
			},
		}

		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		response, bifrostErr := client.DecisionRequest(bfCtx, request)
		if bifrostErr != nil {
			t.Fatalf("❌ Emulated decision failed: %v", GetErrorMessage(bifrostErr))
		}

		BasicDecisionExpectations(t, response, request)
		for name, answer := range response.Answers {
			if answer.Confidence == nil {
				t.Errorf("❌ Emulated answer %q has no confidence", name)
			}
		}
		t.Logf("✅ Decision emulation passed via %s: %d answers", testConfig.DecisionEmulationModel, len(response.Answers))
	})
}
