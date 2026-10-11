package llmtests

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
)

// BasicDecisionExpectations validates common decision invariants for provider
// tests: every requested question produces an answer of its declared type, in
// question order and under the question's name, with a well-typed value.
func BasicDecisionExpectations(t *testing.T, response *schemas.BifrostDecisionResponse, request *schemas.BifrostDecisionRequest) {
	t.Helper()

	if response == nil {
		t.Fatal("❌ Decision response is nil")
	}
	if len(response.Answers) != len(request.Questions) {
		t.Fatalf("❌ Decision response carries %d answers for %d questions", len(response.Answers), len(request.Questions))
	}

	for i, question := range request.Questions {
		answer := response.Answers[i]
		name := *question.Name
		if answer.Name == nil || *answer.Name != name {
			t.Fatalf("❌ Answer %d is not named %q", i, name)
		}
		if answer.Type != question.Type {
			t.Fatalf("❌ Answer %q has type %q; expected %q", name, answer.Type, question.Type)
		}
		switch answer.Type {
		case schemas.DecisionTypePredicate:
			if answer.Probability == nil || *answer.Probability < 0 || *answer.Probability > 1 {
				t.Fatalf("❌ Predicate answer %q carries no probability in [0,1]", name)
			}
		case schemas.DecisionTypeChoice:
			if answer.Choice == nil || answer.Choice.Str == nil {
				t.Fatalf("❌ Choice answer %q carries no option", name)
			}
		case schemas.DecisionTypeScore:
			if answer.Score == nil {
				t.Fatalf("❌ Score answer %q carries no score", name)
			}
		default:
			t.Fatalf("❌ Answer %q has unknown type %q", name, answer.Type)
		}
	}
}

// decisionTestQuestions are the questions both live decision scenarios ask: a
// predicate, a choice over the given options, and a score over the given
// levels.
func decisionTestQuestions(options map[string]string, levels []string) []schemas.DecisionQuestion {
	choices := make([]schemas.DecisionChoice, 0, len(options))
	for _, option := range []string{"billing", "bug", "other"} {
		choices = append(choices, schemas.DecisionChoice{Value: schemas.DecisionScalar{Str: schemas.Ptr(option)}, Description: schemas.NewDecisionText(options[option])})
	}
	scoreLevels := make([]schemas.DecisionLevel, len(levels))
	for i, level := range levels {
		scoreLevels[i] = schemas.DecisionLevel{Label: strconv.Itoa(i), Description: schemas.NewDecisionText(level)}
	}
	return []schemas.DecisionQuestion{
		{Type: schemas.DecisionTypePredicate, Name: schemas.Ptr("is_frustrated"), Instructions: schemas.NewDecisionText("Is the customer frustrated?")},
		{Type: schemas.DecisionTypeChoice, Name: schemas.Ptr("category"), Instructions: schemas.NewDecisionText("Pick the ticket category"), Choices: choices},
		{Type: schemas.DecisionTypeScore, Name: schemas.Ptr("urgency"), Instructions: schemas.NewDecisionText("Rate how urgently this ticket needs a human reply"), Levels: scoreLevels},
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
			Input:    schemas.DecisionInput{Text: &state},
			Questions: decisionTestQuestions(
				map[string]string{"billing": "charges, refunds, invoices", "bug": "product defects", "other": "anything else"},
				[]string{"can wait a week", "should be answered soon", "needs a reply today"},
			),
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
			Input:    schemas.DecisionInput{Text: schemas.Ptr("Customer message: I was double charged and support ignored my emails. I want a refund now or I cancel.")},
			Questions: decisionTestQuestions(
				map[string]string{"billing": "charges and refunds", "bug": "product defects", "other": "anything else"},
				[]string{"low", "medium", "high"},
			),
		}

		bfCtx := schemas.NewBifrostContext(ctx, schemas.NoDeadline)
		response, bifrostErr := client.DecisionRequest(bfCtx, request)
		if bifrostErr != nil {
			t.Fatalf("❌ Emulated decision failed: %v", GetErrorMessage(bifrostErr))
		}

		BasicDecisionExpectations(t, response, request)
		for _, answer := range response.Answers {
			if answer.Confidence == nil {
				t.Errorf("❌ Emulated answer %q has no confidence", *answer.Name)
			}
		}
		t.Logf("✅ Decision emulation passed via %s: %d answers", testConfig.DecisionEmulationModel, len(response.Answers))
	})
}
