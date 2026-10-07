package routing

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// decisionTestPlugin returns a plugin with the decision model as the primary classifier and the given executor.
func decisionTestPlugin(decision *complexity.DecisionConfig, executor DecisionRequestExecutor) *RoutingPlugin {
	p := &RoutingPlugin{}
	p.complexityConfig.Store(&complexity.AnalyzerConfig{Classifier: complexity.ClassifierDecision, Decision: decision})
	p.SetDecisionRequestExecutor(executor)
	return p
}

// decisionChoiceResponse builds a decision response carrying one complexity_tier answer.
func decisionChoiceResponse(value interface{}) *schemas.BifrostDecisionResponse {
	return &schemas.BifrostDecisionResponse{
		Model: "jev-1.13.0",
		Answers: map[string]schemas.DecisionAnswer{
			decisionComplexityQuestion: {Kind: schemas.DecisionKindChoice, Value: value},
		},
		Usage: &schemas.BifrostLLMUsage{PromptTokens: 30, CompletionTokens: 1},
	}
}

// decisionTestInput is a multi-turn conversation with assistant replies between user turns.
func decisionTestInput() complexity.ComplexityInput {
	return complexity.ComplexityInput{
		LastUserText: "current question",
		Conversation: []complexity.ConversationMessage{
			{Role: "user", Content: "oldest question"},
			{Role: "assistant", Content: "oldest answer"},
			{Role: "user", Content: "older question"},
			{Role: "assistant", Content: "older answer"},
			{Role: "user", Content: "previous question"},
			{Role: "assistant", Content: "previous answer"},
			{Role: "user", Content: "current question"},
		},
	}
}

// decisionTestContext returns a request context that is cancelled with the test.
func decisionTestContext(t *testing.T) *schemas.BifrostContext {
	t.Helper()
	ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
	t.Cleanup(ctx.Cancel)
	return ctx
}

// TestClassifyDecisionComplexityRequestShape pins what Bifrost sends to the decision model:
// the default model when none is configured, the user-only history window, one choice
// question over the three tiers, and a deadline bounded by the decision-model timeout.
func TestClassifyDecisionComplexityRequestShape(t *testing.T) {
	count := 2
	var captured *schemas.BifrostDecisionRequest
	var remaining time.Duration
	p := decisionTestPlugin(&complexity.DecisionConfig{PreviousMessageCount: &count, Timeout: 250 * time.Millisecond},
		func(ctx *schemas.BifrostContext, req *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
			captured = req
			deadline, ok := ctx.Deadline()
			require.True(t, ok, "The decision model call must carry the configured timeout as a deadline")
			remaining = time.Until(deadline)
			return decisionChoiceResponse("MEDIUM"), nil
		})

	proposal := p.classifyDecisionComplexity(decisionTestContext(t), decisionTestInput())

	require.NotNil(t, captured)
	assert.Equal(t, schemas.Typesafe, captured.Provider)
	assert.Equal(t, "jev-latest", captured.Model)
	assert.Equal(t, []complexity.ConversationMessage{
		{Role: "user", Content: "older question"},
		{Role: "user", Content: "previous question"},
		{Role: "user", Content: "current question"},
	}, captured.State)
	assert.Greater(t, remaining, time.Duration(0))
	assert.LessOrEqual(t, remaining, 250*time.Millisecond)

	require.Len(t, captured.Questions, 1)
	question, ok := captured.Questions[decisionComplexityQuestion]
	require.True(t, ok)
	assert.Equal(t, schemas.DecisionKindChoice, question.Kind)
	criteria, ok := question.Criteria.(map[string]interface{})
	require.True(t, ok)
	assert.ElementsMatch(t, []string{complexity.TierSimple, complexity.TierMedium, complexity.TierComplex}, mapKeys(criteria))
	instructions, ok := question.Instructions.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, []string{complexity.TierSimple, complexity.TierMedium, complexity.TierComplex}, instructions["tier_order"])

	require.NotNil(t, proposal.Result)
	assert.Equal(t, complexity.TierMedium, proposal.Result.Tier)
	assert.Equal(t, complexity.MechanismDecision, proposal.Mechanism)
}

// TestClassifyDecisionComplexityGuidance checks the request carries the shipped
// guidance by default and the administrator's overrides list by list, while
// the fixed question and context rule never change.
func TestClassifyDecisionComplexityGuidance(t *testing.T) {
	capture := func(decision *complexity.DecisionConfig) schemas.DecisionQuestion {
		var captured *schemas.BifrostDecisionRequest
		p := decisionTestPlugin(decision, func(_ *schemas.BifrostContext, req *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
			captured = req
			return decisionChoiceResponse("SIMPLE"), nil
		})
		p.classifyDecisionComplexity(decisionTestContext(t), decisionTestInput())
		require.NotNil(t, captured)
		return captured.Questions[decisionComplexityQuestion]
	}
	defaults := configstore.DefaultComplexityDecisionGuidance()

	shipped := capture(nil)
	instructions := shipped.Instructions.(map[string]interface{})
	criteria := shipped.Criteria.(map[string]interface{})
	assert.Equal(t, map[string]interface{}{
		"definition": defaults.Criteria[complexity.TierMedium].Definition,
		"signals":    defaults.Criteria[complexity.TierMedium].Signals,
		"examples":   defaults.Criteria[complexity.TierMedium].Examples,
	}, criteria[complexity.TierMedium])

	edited := capture(&complexity.DecisionConfig{
		Criteria: map[string]configstore.ComplexityDecisionTierCriteria{
			complexity.TierSimple:  {Definition: "custom definition"},
			complexity.TierComplex: {Signals: []string{"custom signal"}},
		},
	})
	editedInstructions := edited.Instructions.(map[string]interface{})
	assert.Equal(t, instructions["question"], editedInstructions["question"], "the question is fixed")
	assert.Equal(t, instructions["decision_rule"], editedInstructions["decision_rule"], "the decision rule is fixed")
	assert.Equal(t, instructions["context_rule"], editedInstructions["context_rule"], "the context rule is fixed")
	simpleCriteria := edited.Criteria.(map[string]interface{})[complexity.TierSimple].(map[string]interface{})
	assert.Equal(t, "custom definition", simpleCriteria["definition"])
	assert.Equal(t, defaults.Criteria[complexity.TierSimple].Signals, simpleCriteria["signals"])
	complexCriteria := edited.Criteria.(map[string]interface{})[complexity.TierComplex].(map[string]interface{})
	assert.Equal(t, []string{"custom signal"}, complexCriteria["signals"])
	assert.Equal(t, defaults.Criteria[complexity.TierComplex].Examples, complexCriteria["examples"])
	assert.Equal(t, defaults.Criteria[complexity.TierComplex].Definition, complexCriteria["definition"])
	assert.NotContains(t, complexCriteria, "not_for")
}

// TestClassifyDecisionComplexityStateSerializesAsRoleContentArray pins the wire
// shape of the state: Typesafe accepts only a JSON string, object, or array,
// and reads each message by its lowercase role/content keys.
func TestClassifyDecisionComplexityStateSerializesAsRoleContentArray(t *testing.T) {
	var captured *schemas.BifrostDecisionRequest
	p := decisionTestPlugin(nil, func(_ *schemas.BifrostContext, req *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
		captured = req
		return decisionChoiceResponse("SIMPLE"), nil
	})

	p.classifyDecisionComplexity(decisionTestContext(t), decisionTestInput())

	require.NotNil(t, captured)
	data, err := json.Marshal(captured.State)
	require.NoError(t, err)
	assert.JSONEq(t, `[{"role":"user","content":"previous question"},{"role":"user","content":"current question"}]`, string(data))
}

// TestClassifyDecisionComplexityDefaultsWithoutDecisionBlock covers a runtime config that
// selects the decision model but carries no decision block: the defaults (one earlier user message,
// 1500ms) must apply instead of an unbounded call or an empty history.
func TestClassifyDecisionComplexityDefaultsWithoutDecisionBlock(t *testing.T) {
	for name, decision := range map[string]*complexity.DecisionConfig{
		"nil block":    nil,
		"zero timeout": {},
	} {
		t.Run(name, func(t *testing.T) {
			var captured *schemas.BifrostDecisionRequest
			var remaining time.Duration
			p := decisionTestPlugin(decision, func(ctx *schemas.BifrostContext, req *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
				captured = req
				deadline, ok := ctx.Deadline()
				require.True(t, ok)
				remaining = time.Until(deadline)
				return decisionChoiceResponse("SIMPLE"), nil
			})

			p.classifyDecisionComplexity(decisionTestContext(t), decisionTestInput())

			require.NotNil(t, captured)
			assert.Len(t, captured.State, configstore.DefaultComplexityDecisionPreviousMessageCount+1)
			assert.Greater(t, remaining, configstore.DefaultComplexityDecisionTimeout-200*time.Millisecond)
			assert.LessOrEqual(t, remaining, configstore.DefaultComplexityDecisionTimeout)
		})
	}
}

// TestClassifyDecisionComplexityFallsBackToLastUserText covers requests with no
// role-preserving conversation (text completions): the current prompt alone is sent.
func TestClassifyDecisionComplexityFallsBackToLastUserText(t *testing.T) {
	var captured *schemas.BifrostDecisionRequest
	p := decisionTestPlugin(nil, func(_ *schemas.BifrostContext, req *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
		captured = req
		return decisionChoiceResponse("SIMPLE"), nil
	})

	proposal := p.classifyDecisionComplexity(decisionTestContext(t), complexity.ComplexityInput{LastUserText: "complete this sentence"})

	require.NotNil(t, captured)
	assert.Equal(t, []complexity.ConversationMessage{{Role: "user", Content: "complete this sentence"}}, captured.State)
	require.NotNil(t, proposal.Result)
}

// TestClassifyDecisionComplexitySkipsWithoutUserText checks that the decision model is never
// called (and never billed) when there is no human-authored text to classify.
func TestClassifyDecisionComplexitySkipsWithoutUserText(t *testing.T) {
	called := false
	p := decisionTestPlugin(nil, func(_ *schemas.BifrostContext, _ *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
		called = true
		return decisionChoiceResponse("SIMPLE"), nil
	})

	proposal := p.classifyDecisionComplexity(decisionTestContext(t), complexity.ComplexityInput{
		Conversation: []complexity.ConversationMessage{{Role: "assistant", Content: "only an assistant turn"}},
	})

	assert.False(t, called)
	assert.Nil(t, proposal.Result)
	assert.Equal(t, complexity.MechanismSkipped, proposal.Mechanism)
}

// TestClassifyDecisionComplexitySkipsWithoutExecutor covers a gateway where the
// decision executor was never wired (the enterprise Bootstrap regression).
func TestClassifyDecisionComplexitySkipsWithoutExecutor(t *testing.T) {
	p := decisionTestPlugin(nil, nil)

	proposal := p.classifyDecisionComplexity(decisionTestContext(t), decisionTestInput())

	assert.Nil(t, proposal.Result)
	assert.Equal(t, complexity.MechanismSkipped, proposal.Mechanism)
	assert.Equal(t, schemas.LogLevelWarn, proposal.LogLevel)
	assert.Contains(t, proposal.LogMessage, "executor is not configured")
}

// TestClassifyDecisionComplexityParsesTierAnswers pins which answers publish a tier.
// Anything but the three tier names, in any case, must publish nothing rather
// than route on a guess.
func TestClassifyDecisionComplexityParsesTierAnswers(t *testing.T) {
	cases := []struct {
		name  string
		value interface{}
		want  string
	}{
		{name: "exact", value: "COMPLEX", want: complexity.TierComplex},
		{name: "lowercase", value: "simple", want: complexity.TierSimple},
		{name: "padded", value: "  Medium \n", want: complexity.TierMedium},
		{name: "unknown tier", value: "EXTREME"},
		{name: "empty", value: ""},
		{name: "number", value: 2.0},
		{name: "null", value: nil},
		{name: "object", value: map[string]interface{}{"tier": "SIMPLE"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := decisionTestPlugin(nil, func(_ *schemas.BifrostContext, _ *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
				return decisionChoiceResponse(tc.value), nil
			})

			proposal := p.classifyDecisionComplexity(decisionTestContext(t), decisionTestInput())

			if tc.want == "" {
				assert.Nil(t, proposal.Result)
				assert.Equal(t, complexity.MechanismSkipped, proposal.Mechanism)
				assert.Equal(t, schemas.LogLevelWarn, proposal.LogLevel)
				return
			}
			require.NotNil(t, proposal.Result)
			assert.Equal(t, tc.want, proposal.Result.Tier)
			assert.Equal(t, complexity.MechanismDecision, proposal.Mechanism)
		})
	}
}

// TestClassifyDecisionComplexityRejectsMalformedResponses covers upstream shapes
// that carry no usable choice answer.
func TestClassifyDecisionComplexityRejectsMalformedResponses(t *testing.T) {
	cases := map[string]*schemas.BifrostDecisionResponse{
		"nil response": nil,
		"missing answer": {
			Answers: map[string]schemas.DecisionAnswer{"other_question": {Kind: schemas.DecisionKindChoice, Value: "SIMPLE"}},
		},
		"wrong kind": {
			Answers: map[string]schemas.DecisionAnswer{decisionComplexityQuestion: {Kind: schemas.DecisionKindScore, Value: "SIMPLE"}},
		},
	}
	for name, response := range cases {
		t.Run(name, func(t *testing.T) {
			p := decisionTestPlugin(nil, func(_ *schemas.BifrostContext, _ *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
				return response, nil
			})

			proposal := p.classifyDecisionComplexity(decisionTestContext(t), decisionTestInput())

			assert.Nil(t, proposal.Result)
			assert.Equal(t, complexity.MechanismSkipped, proposal.Mechanism)
			assert.Equal(t, schemas.LogLevelWarn, proposal.LogLevel)
		})
	}
}

// TestClassifyDecisionComplexityCarriesConfidence checks that the decision model's confidence
// reaches the proposal, where session logs report it.
func TestClassifyDecisionComplexityCarriesConfidence(t *testing.T) {
	confidence := 0.87
	p := decisionTestPlugin(nil, func(_ *schemas.BifrostContext, _ *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
		response := decisionChoiceResponse("COMPLEX")
		answer := response.Answers[decisionComplexityQuestion]
		answer.Confidence = &confidence
		response.Answers[decisionComplexityQuestion] = answer
		return response, nil
	})

	proposal := p.classifyDecisionComplexity(decisionTestContext(t), decisionTestInput())

	require.NotNil(t, proposal.Confidence)
	assert.InDelta(t, confidence, *proposal.Confidence, 1e-9)
	assert.Contains(t, proposal.LogMessage, "confidence=0.87")
}

// TestClassifyDecisionComplexityTimesOut checks that a slow decision-model call is cut at
// the configured timeout and reported as a timeout, not a generic failure.
func TestClassifyDecisionComplexityTimesOut(t *testing.T) {
	p := decisionTestPlugin(&complexity.DecisionConfig{Timeout: 20 * time.Millisecond},
		func(ctx *schemas.BifrostContext, _ *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
			<-ctx.Done()
			return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "context deadline exceeded"}}
		})

	start := time.Now()
	proposal := p.classifyDecisionComplexity(decisionTestContext(t), decisionTestInput())

	assert.Less(t, time.Since(start), time.Second)
	assert.Nil(t, proposal.Result)
	assert.Equal(t, complexity.MechanismSkipped, proposal.Mechanism)
	assert.Equal(t, schemas.LogLevelWarn, proposal.LogLevel)
	assert.Contains(t, proposal.LogMessage, "timed out after 20ms (model=typesafe/jev-latest)")
}

// TestClassifyDecisionComplexityProviderErrorPublishesNothing covers an upstream
// failure (bad key, 5xx): no tier, and the cause is surfaced in the log.
func TestClassifyDecisionComplexityProviderErrorPublishesNothing(t *testing.T) {
	p := decisionTestPlugin(nil, func(_ *schemas.BifrostContext, _ *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
		return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "invalid api key"}}
	})

	proposal := p.classifyDecisionComplexity(decisionTestContext(t), decisionTestInput())

	assert.Nil(t, proposal.Result)
	assert.Equal(t, complexity.MechanismSkipped, proposal.Mechanism)
	assert.Contains(t, proposal.LogMessage, "unavailable")
	assert.Contains(t, proposal.LogMessage, "invalid api key")
}

// TestClassifyDecisionComplexityRecordsUsage pins decision-call cost attribution:
// a successful call is recorded under the resolved model, and a failed call
// that Typesafe still billed is recorded too, while an unbilled failure is not.
// TestClassifyDecisionComplexityUsesConfiguredModel checks that a configured
// provider and model reach the decision request, and that usage is recorded under
// the provider that served the call rather than the Typesafe default.
func TestClassifyDecisionComplexityUsesConfiguredModel(t *testing.T) {
	var captured *schemas.BifrostDecisionRequest
	p := decisionTestPlugin(&complexity.DecisionConfig{Provider: "nimble", Model: "nimble-latest"}, func(_ *schemas.BifrostContext, request *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
		captured = request
		response := decisionChoiceResponse("MEDIUM")
		response.Model = "nimble-latest"
		response.ExtraFields.Provider = "nimble"
		return response, nil
	})
	ctx := decisionTestContext(t)

	proposal := p.classifyDecisionComplexity(ctx, decisionTestInput())

	require.NotNil(t, captured)
	assert.Equal(t, schemas.ModelProvider("nimble"), captured.Provider)
	assert.Equal(t, "nimble-latest", captured.Model)
	require.NotNil(t, proposal.Result)
	assert.Equal(t, complexity.TierMedium, proposal.Result.Tier)
	assert.Equal(t, "nimble/nimble-latest", proposal.Model, "the proposal names the model that classified")
	assert.Contains(t, proposal.LogMessage, "model=nimble/nimble-latest")
	usage, ok := schemas.RoutingMetadataFromContext(ctx)
	require.True(t, ok)
	require.Len(t, usage.Calls, 1)
	assert.Equal(t, "nimble", *usage.Calls[0].ProviderUsed)
	assert.Equal(t, "nimble-latest", *usage.Calls[0].ModelUsed)
}

// TestClassifyDecisionComplexityNimbleCriteriaAsText pins that a Nimble model,
// whose server accepts only string descriptions, receives each tier's guidance as
// one text description, while other models keep the structured object.
func TestClassifyDecisionComplexityNimbleCriteriaAsText(t *testing.T) {
	capture := func(decision *complexity.DecisionConfig) map[string]interface{} {
		var captured *schemas.BifrostDecisionRequest
		p := decisionTestPlugin(decision, func(_ *schemas.BifrostContext, request *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
			captured = request
			return decisionChoiceResponse("SIMPLE"), nil
		})
		p.classifyDecisionComplexity(decisionTestContext(t), decisionTestInput())
		require.NotNil(t, captured)
		criteria, ok := captured.Questions[decisionComplexityQuestion].Criteria.(map[string]interface{})
		require.True(t, ok)
		return criteria
	}

	defaults := configstore.DefaultComplexityDecisionGuidance().Criteria[complexity.TierComplex]
	for _, model := range []string{"nimble-latest", "bespokelabs/Bespoke-Nimble-9B"} {
		criteria := capture(&complexity.DecisionConfig{Provider: "nimble", Model: model})
		text, ok := criteria[complexity.TierComplex].(string)
		require.True(t, ok, "%s must receive string criteria, got %T", model, criteria[complexity.TierComplex])
		assert.True(t, strings.HasPrefix(text, defaults.Definition+"\nSignals:\n- "+defaults.Signals[0]), "text: %q", text)
		assert.Contains(t, text, "\nExamples:\n- "+defaults.Examples[0])
	}

	criteria := capture(nil)
	_, isObject := criteria[complexity.TierComplex].(map[string]interface{})
	assert.True(t, isObject, "the default Jev model keeps object criteria")
}

func TestClassifyDecisionComplexityRecordsUsage(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		p := decisionTestPlugin(nil, func(_ *schemas.BifrostContext, _ *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
			return decisionChoiceResponse("SIMPLE"), nil
		})
		ctx := decisionTestContext(t)

		p.classifyDecisionComplexity(ctx, decisionTestInput())

		usage, ok := schemas.RoutingMetadataFromContext(ctx)
		require.True(t, ok)
		require.Len(t, usage.Calls, 1)
		call := usage.Calls[0]
		assert.Equal(t, schemas.DecisionRequest, call.RequestType)
		assert.Equal(t, string(schemas.Typesafe), *call.ProviderUsed)
		assert.Equal(t, "jev-1.13.0", *call.ModelUsed)
		assert.Equal(t, 30, *call.InputTokens)
		assert.Equal(t, 1, *call.OutputTokens)
		assert.True(t, call.CountTowardBudgets)
	})

	t.Run("billed failure", func(t *testing.T) {
		p := decisionTestPlugin(nil, func(_ *schemas.BifrostContext, _ *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
			bifrostErr := &schemas.BifrostError{Error: &schemas.ErrorField{Message: "upstream closed"}}
			bifrostErr.ExtraFields.BilledUsage = &schemas.BifrostLLMUsage{PromptTokens: 12}
			return nil, bifrostErr
		})
		ctx := decisionTestContext(t)

		p.classifyDecisionComplexity(ctx, decisionTestInput())

		usage, ok := schemas.RoutingMetadataFromContext(ctx)
		require.True(t, ok)
		require.Len(t, usage.Calls, 1)
		assert.Equal(t, configstore.DefaultComplexityDecisionModel, *usage.Calls[0].ModelUsed)
		assert.Equal(t, 12, *usage.Calls[0].InputTokens)
	})

	t.Run("unbilled failure", func(t *testing.T) {
		p := decisionTestPlugin(nil, func(_ *schemas.BifrostContext, _ *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
			return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "no keys found"}}
		})
		ctx := decisionTestContext(t)

		p.classifyDecisionComplexity(ctx, decisionTestInput())

		if usage, ok := schemas.RoutingMetadataFromContext(ctx); ok {
			assert.Empty(t, usage.Calls)
		}
	})
}

// TestClassifyComplexityInputDispatchesByClassifier pins classifier selection:
// The decision model as primary never touches semantic, and semantic with fallback "decision"
// reaches the decision model only when semantic has no tier.
func TestClassifyComplexityInputDispatchesByClassifier(t *testing.T) {
	calls := 0
	executor := func(_ *schemas.BifrostContext, _ *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
		calls++
		return decisionChoiceResponse("COMPLEX"), nil
	}

	t.Run("decision primary", func(t *testing.T) {
		calls = 0
		p := decisionTestPlugin(nil, executor)
		proposal := p.classifyComplexityInput(decisionTestContext(t), decisionTestInput())
		assert.Equal(t, 1, calls)
		assert.Equal(t, complexity.MechanismDecision, proposal.Mechanism)
	})

	t.Run("semantic unconfigured with decision fallback", func(t *testing.T) {
		calls = 0
		p := &RoutingPlugin{}
		p.complexityConfig.Store(&complexity.AnalyzerConfig{
			Classifier: complexity.ClassifierSemantic,
			Semantic:   &complexity.SemanticConfig{Fallback: configstore.ComplexitySemanticFallbackDecision},
		})
		p.SetDecisionRequestExecutor(executor)
		ctx := decisionTestContext(t)

		proposal := p.classifyComplexityInput(ctx, decisionTestInput())

		assert.Equal(t, 1, calls)
		assert.Equal(t, complexity.MechanismDecision, proposal.Mechanism)
		assert.True(t, hasRoutingLog(ctx, "falling back to the decision model"))
	})

	t.Run("semantic unconfigured without fallback", func(t *testing.T) {
		calls = 0
		p := &RoutingPlugin{}
		p.complexityConfig.Store(&complexity.AnalyzerConfig{Classifier: complexity.ClassifierSemantic})
		p.SetDecisionRequestExecutor(executor)

		proposal := p.classifyComplexityInput(decisionTestContext(t), decisionTestInput())

		assert.Zero(t, calls, "a configured decision executor must not run unless the decision model is selected")
		assert.Equal(t, complexity.MechanismSkipped, proposal.Mechanism)
	})
}

// hasRoutingLog reports whether any routing-engine log entry contains the fragment.
func hasRoutingLog(ctx *schemas.BifrostContext, fragment string) bool {
	for _, entry := range ctx.GetRoutingEngineLogs() {
		if strings.Contains(entry.Message, fragment) {
			return true
		}
	}
	return false
}

// mapKeys returns the keys of a criteria map.
func mapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}
