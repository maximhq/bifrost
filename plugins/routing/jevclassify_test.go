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

// jevTestPlugin returns a plugin with Jev as the primary classifier and the given executor.
func jevTestPlugin(jev *complexity.JevConfig, executor DecisionRequestExecutor) *RoutingPlugin {
	p := &RoutingPlugin{}
	p.complexityConfig.Store(&complexity.AnalyzerConfig{Classifier: complexity.ClassifierJev, Jev: jev})
	p.SetDecisionRequestExecutor(executor)
	return p
}

// jevChoiceResponse builds a decision response carrying one complexity_tier answer.
func jevChoiceResponse(value interface{}) *schemas.BifrostDecisionResponse {
	return &schemas.BifrostDecisionResponse{
		Model: "jev-1.13.0",
		Answers: map[string]schemas.DecisionAnswer{
			jevComplexityQuestion: {Kind: schemas.DecisionKindChoice, Value: value},
		},
		Usage: &schemas.BifrostLLMUsage{PromptTokens: 30, CompletionTokens: 1},
	}
}

// jevTestInput is a multi-turn conversation with assistant replies between user turns.
func jevTestInput() complexity.ComplexityInput {
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

// jevTestContext returns a request context that is cancelled with the test.
func jevTestContext(t *testing.T) *schemas.BifrostContext {
	t.Helper()
	ctx := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
	t.Cleanup(ctx.Cancel)
	return ctx
}

// TestClassifyJevComplexityRequestShape pins what Bifrost sends to Typesafe:
// the fixed model, the configured user-only history window, one choice
// question over the three tiers, and a deadline bounded by the Jev timeout.
func TestClassifyJevComplexityRequestShape(t *testing.T) {
	count := 2
	var captured *schemas.BifrostDecisionRequest
	var remaining time.Duration
	p := jevTestPlugin(&complexity.JevConfig{PreviousMessageCount: &count, Timeout: 250 * time.Millisecond},
		func(ctx *schemas.BifrostContext, req *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
			captured = req
			deadline, ok := ctx.Deadline()
			require.True(t, ok, "Jev call must carry the configured timeout as a deadline")
			remaining = time.Until(deadline)
			return jevChoiceResponse("MEDIUM"), nil
		})

	proposal := p.classifyJevComplexity(jevTestContext(t), jevTestInput())

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
	question, ok := captured.Questions[jevComplexityQuestion]
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
	assert.Equal(t, complexity.MechanismJev, proposal.Mechanism)
}

// TestClassifyJevComplexityGuidance checks the request carries the shipped
// guidance by default and the administrator's overrides list by list, while
// the fixed question and context rule never change.
func TestClassifyJevComplexityGuidance(t *testing.T) {
	capture := func(jev *complexity.JevConfig) schemas.DecisionQuestion {
		var captured *schemas.BifrostDecisionRequest
		p := jevTestPlugin(jev, func(_ *schemas.BifrostContext, req *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
			captured = req
			return jevChoiceResponse("SIMPLE"), nil
		})
		p.classifyJevComplexity(jevTestContext(t), jevTestInput())
		require.NotNil(t, captured)
		return captured.Questions[jevComplexityQuestion]
	}
	defaults := configstore.DefaultComplexityJevGuidance()

	shipped := capture(nil)
	instructions := shipped.Instructions.(map[string]interface{})
	criteria := shipped.Criteria.(map[string]interface{})
	assert.Equal(t, map[string]interface{}{
		"definition": defaults.Criteria[complexity.TierMedium].Definition,
		"signals":    defaults.Criteria[complexity.TierMedium].Signals,
		"examples":   defaults.Criteria[complexity.TierMedium].Examples,
	}, criteria[complexity.TierMedium])

	edited := capture(&complexity.JevConfig{
		Criteria: map[string]configstore.ComplexityJevTierCriteria{
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

// TestClassifyJevComplexityStateSerializesAsRoleContentArray pins the wire
// shape of the state: Typesafe accepts only a JSON string, object, or array,
// and reads each message by its lowercase role/content keys.
func TestClassifyJevComplexityStateSerializesAsRoleContentArray(t *testing.T) {
	var captured *schemas.BifrostDecisionRequest
	p := jevTestPlugin(nil, func(_ *schemas.BifrostContext, req *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
		captured = req
		return jevChoiceResponse("SIMPLE"), nil
	})

	p.classifyJevComplexity(jevTestContext(t), jevTestInput())

	require.NotNil(t, captured)
	data, err := json.Marshal(captured.State)
	require.NoError(t, err)
	assert.JSONEq(t, `[{"role":"user","content":"previous question"},{"role":"user","content":"current question"}]`, string(data))
}

// TestClassifyJevComplexityDefaultsWithoutJevBlock covers a runtime config that
// selects Jev but carries no jev block: the defaults (one earlier user message,
// 1500ms) must apply instead of an unbounded call or an empty history.
func TestClassifyJevComplexityDefaultsWithoutJevBlock(t *testing.T) {
	for name, jev := range map[string]*complexity.JevConfig{
		"nil block":    nil,
		"zero timeout": {},
	} {
		t.Run(name, func(t *testing.T) {
			var captured *schemas.BifrostDecisionRequest
			var remaining time.Duration
			p := jevTestPlugin(jev, func(ctx *schemas.BifrostContext, req *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
				captured = req
				deadline, ok := ctx.Deadline()
				require.True(t, ok)
				remaining = time.Until(deadline)
				return jevChoiceResponse("SIMPLE"), nil
			})

			p.classifyJevComplexity(jevTestContext(t), jevTestInput())

			require.NotNil(t, captured)
			assert.Len(t, captured.State, configstore.DefaultComplexityJevPreviousMessageCount+1)
			assert.Greater(t, remaining, configstore.DefaultComplexityJevTimeout-200*time.Millisecond)
			assert.LessOrEqual(t, remaining, configstore.DefaultComplexityJevTimeout)
		})
	}
}

// TestClassifyJevComplexityFallsBackToLastUserText covers requests with no
// role-preserving conversation (text completions): the current prompt alone is sent.
func TestClassifyJevComplexityFallsBackToLastUserText(t *testing.T) {
	var captured *schemas.BifrostDecisionRequest
	p := jevTestPlugin(nil, func(_ *schemas.BifrostContext, req *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
		captured = req
		return jevChoiceResponse("SIMPLE"), nil
	})

	proposal := p.classifyJevComplexity(jevTestContext(t), complexity.ComplexityInput{LastUserText: "complete this sentence"})

	require.NotNil(t, captured)
	assert.Equal(t, []complexity.ConversationMessage{{Role: "user", Content: "complete this sentence"}}, captured.State)
	require.NotNil(t, proposal.Result)
}

// TestClassifyJevComplexitySkipsWithoutUserText checks that Jev is never
// called (and never billed) when there is no human-authored text to classify.
func TestClassifyJevComplexitySkipsWithoutUserText(t *testing.T) {
	called := false
	p := jevTestPlugin(nil, func(_ *schemas.BifrostContext, _ *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
		called = true
		return jevChoiceResponse("SIMPLE"), nil
	})

	proposal := p.classifyJevComplexity(jevTestContext(t), complexity.ComplexityInput{
		Conversation: []complexity.ConversationMessage{{Role: "assistant", Content: "only an assistant turn"}},
	})

	assert.False(t, called)
	assert.Nil(t, proposal.Result)
	assert.Equal(t, complexity.MechanismSkipped, proposal.Mechanism)
}

// TestClassifyJevComplexitySkipsWithoutExecutor covers a gateway where the
// decision executor was never wired (the enterprise Bootstrap regression).
func TestClassifyJevComplexitySkipsWithoutExecutor(t *testing.T) {
	p := jevTestPlugin(nil, nil)

	proposal := p.classifyJevComplexity(jevTestContext(t), jevTestInput())

	assert.Nil(t, proposal.Result)
	assert.Equal(t, complexity.MechanismSkipped, proposal.Mechanism)
	assert.Equal(t, schemas.LogLevelWarn, proposal.LogLevel)
	assert.Contains(t, proposal.LogMessage, "executor is not configured")
}

// TestClassifyJevComplexityParsesTierAnswers pins which answers publish a tier.
// Anything but the three tier names, in any case, must publish nothing rather
// than route on a guess.
func TestClassifyJevComplexityParsesTierAnswers(t *testing.T) {
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
			p := jevTestPlugin(nil, func(_ *schemas.BifrostContext, _ *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
				return jevChoiceResponse(tc.value), nil
			})

			proposal := p.classifyJevComplexity(jevTestContext(t), jevTestInput())

			if tc.want == "" {
				assert.Nil(t, proposal.Result)
				assert.Equal(t, complexity.MechanismSkipped, proposal.Mechanism)
				assert.Equal(t, schemas.LogLevelWarn, proposal.LogLevel)
				return
			}
			require.NotNil(t, proposal.Result)
			assert.Equal(t, tc.want, proposal.Result.Tier)
			assert.Equal(t, complexity.MechanismJev, proposal.Mechanism)
		})
	}
}

// TestClassifyJevComplexityRejectsMalformedResponses covers upstream shapes
// that carry no usable choice answer.
func TestClassifyJevComplexityRejectsMalformedResponses(t *testing.T) {
	cases := map[string]*schemas.BifrostDecisionResponse{
		"nil response": nil,
		"missing answer": {
			Answers: map[string]schemas.DecisionAnswer{"other_question": {Kind: schemas.DecisionKindChoice, Value: "SIMPLE"}},
		},
		"wrong kind": {
			Answers: map[string]schemas.DecisionAnswer{jevComplexityQuestion: {Kind: schemas.DecisionKindScore, Value: "SIMPLE"}},
		},
	}
	for name, response := range cases {
		t.Run(name, func(t *testing.T) {
			p := jevTestPlugin(nil, func(_ *schemas.BifrostContext, _ *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
				return response, nil
			})

			proposal := p.classifyJevComplexity(jevTestContext(t), jevTestInput())

			assert.Nil(t, proposal.Result)
			assert.Equal(t, complexity.MechanismSkipped, proposal.Mechanism)
			assert.Equal(t, schemas.LogLevelWarn, proposal.LogLevel)
		})
	}
}

// TestClassifyJevComplexityCarriesConfidence checks that Jev's confidence
// reaches the proposal, where session logs report it.
func TestClassifyJevComplexityCarriesConfidence(t *testing.T) {
	confidence := 0.87
	p := jevTestPlugin(nil, func(_ *schemas.BifrostContext, _ *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
		response := jevChoiceResponse("COMPLEX")
		answer := response.Answers[jevComplexityQuestion]
		answer.Confidence = &confidence
		response.Answers[jevComplexityQuestion] = answer
		return response, nil
	})

	proposal := p.classifyJevComplexity(jevTestContext(t), jevTestInput())

	require.NotNil(t, proposal.Confidence)
	assert.InDelta(t, confidence, *proposal.Confidence, 1e-9)
	assert.Contains(t, proposal.LogMessage, "confidence=0.87")
}

// TestClassifyJevComplexityTimesOut checks that a slow Typesafe call is cut at
// the configured timeout and reported as a timeout, not a generic failure.
func TestClassifyJevComplexityTimesOut(t *testing.T) {
	p := jevTestPlugin(&complexity.JevConfig{Timeout: 20 * time.Millisecond},
		func(ctx *schemas.BifrostContext, _ *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
			<-ctx.Done()
			return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "context deadline exceeded"}}
		})

	start := time.Now()
	proposal := p.classifyJevComplexity(jevTestContext(t), jevTestInput())

	assert.Less(t, time.Since(start), time.Second)
	assert.Nil(t, proposal.Result)
	assert.Equal(t, complexity.MechanismSkipped, proposal.Mechanism)
	assert.Equal(t, schemas.LogLevelWarn, proposal.LogLevel)
	assert.Contains(t, proposal.LogMessage, "timed out after 20ms")
}

// TestClassifyJevComplexityProviderErrorPublishesNothing covers an upstream
// failure (bad key, 5xx): no tier, and the cause is surfaced in the log.
func TestClassifyJevComplexityProviderErrorPublishesNothing(t *testing.T) {
	p := jevTestPlugin(nil, func(_ *schemas.BifrostContext, _ *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
		return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "invalid api key"}}
	})

	proposal := p.classifyJevComplexity(jevTestContext(t), jevTestInput())

	assert.Nil(t, proposal.Result)
	assert.Equal(t, complexity.MechanismSkipped, proposal.Mechanism)
	assert.Contains(t, proposal.LogMessage, "unavailable")
	assert.Contains(t, proposal.LogMessage, "invalid api key")
}

// TestClassifyJevComplexityRecordsUsage pins decision-call cost attribution:
// a successful call is recorded under the resolved model, and a failed call
// that Typesafe still billed is recorded too, while an unbilled failure is not.
func TestClassifyJevComplexityRecordsUsage(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		p := jevTestPlugin(nil, func(_ *schemas.BifrostContext, _ *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
			return jevChoiceResponse("SIMPLE"), nil
		})
		ctx := jevTestContext(t)

		p.classifyJevComplexity(ctx, jevTestInput())

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
		p := jevTestPlugin(nil, func(_ *schemas.BifrostContext, _ *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
			bifrostErr := &schemas.BifrostError{Error: &schemas.ErrorField{Message: "upstream closed"}}
			bifrostErr.ExtraFields.BilledUsage = &schemas.BifrostLLMUsage{PromptTokens: 12}
			return nil, bifrostErr
		})
		ctx := jevTestContext(t)

		p.classifyJevComplexity(ctx, jevTestInput())

		usage, ok := schemas.RoutingMetadataFromContext(ctx)
		require.True(t, ok)
		require.Len(t, usage.Calls, 1)
		assert.Equal(t, jevComplexityModel, *usage.Calls[0].ModelUsed)
		assert.Equal(t, 12, *usage.Calls[0].InputTokens)
	})

	t.Run("unbilled failure", func(t *testing.T) {
		p := jevTestPlugin(nil, func(_ *schemas.BifrostContext, _ *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
			return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "no keys found"}}
		})
		ctx := jevTestContext(t)

		p.classifyJevComplexity(ctx, jevTestInput())

		if usage, ok := schemas.RoutingMetadataFromContext(ctx); ok {
			assert.Empty(t, usage.Calls)
		}
	})
}

// TestClassifyComplexityInputDispatchesByClassifier pins classifier selection:
// Jev as primary never touches semantic, and semantic with fallback "jev"
// reaches Jev only when semantic has no tier.
func TestClassifyComplexityInputDispatchesByClassifier(t *testing.T) {
	calls := 0
	executor := func(_ *schemas.BifrostContext, _ *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
		calls++
		return jevChoiceResponse("COMPLEX"), nil
	}

	t.Run("jev primary", func(t *testing.T) {
		calls = 0
		p := jevTestPlugin(nil, executor)
		proposal := p.classifyComplexityInput(jevTestContext(t), jevTestInput())
		assert.Equal(t, 1, calls)
		assert.Equal(t, complexity.MechanismJev, proposal.Mechanism)
	})

	t.Run("semantic unconfigured with jev fallback", func(t *testing.T) {
		calls = 0
		p := &RoutingPlugin{}
		p.complexityConfig.Store(&complexity.AnalyzerConfig{
			Classifier: complexity.ClassifierSemantic,
			Semantic:   &complexity.SemanticConfig{Fallback: configstore.ComplexitySemanticFallbackJev},
		})
		p.SetDecisionRequestExecutor(executor)
		ctx := jevTestContext(t)

		proposal := p.classifyComplexityInput(ctx, jevTestInput())

		assert.Equal(t, 1, calls)
		assert.Equal(t, complexity.MechanismJev, proposal.Mechanism)
		assert.True(t, hasRoutingLog(ctx, "falling back to Jev"))
	})

	t.Run("semantic unconfigured without fallback", func(t *testing.T) {
		calls = 0
		p := &RoutingPlugin{}
		p.complexityConfig.Store(&complexity.AnalyzerConfig{Classifier: complexity.ClassifierSemantic})
		p.SetDecisionRequestExecutor(executor)

		proposal := p.classifyComplexityInput(jevTestContext(t), jevTestInput())

		assert.Zero(t, calls, "a configured Jev executor must not run unless Jev is selected")
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
