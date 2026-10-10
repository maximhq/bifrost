package complexity_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/kvstore"
	"github.com/maximhq/bifrost/plugins/routing"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/maximhq/bifrost/plugins/routing/rules"
)

// decisionRecorder is a fake decision executor. It answers with the tier
// mapped to the latest user message and records every request it receives.
type decisionRecorder struct {
	mu       sync.Mutex
	requests []*schemas.BifrostDecisionRequest
	tiers    map[string]string
	err      *schemas.BifrostError
}

// execute implements routing.DecisionRequestExecutor.
func (r *decisionRecorder) execute(_ *schemas.BifrostContext, req *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, req)
	if r.err != nil {
		return nil, r.err
	}
	state, _ := req.Input.Structured.([]complexity.ConversationMessage)
	tier := "SIMPLE"
	if len(state) > 0 {
		if mapped, ok := r.tiers[state[len(state)-1].Content]; ok {
			tier = mapped
		}
	}
	confidence := 0.9
	return &schemas.BifrostDecisionResponse{
		Model: "jev-1.13.0",
		Answers: []schemas.DecisionAnswer{
			{Type: schemas.DecisionTypeChoice, Name: schemas.Ptr("complexity_tier"), Choice: &schemas.DecisionScalar{Str: &tier}, Confidence: &confidence},
		},
		Usage: &schemas.BifrostLLMUsage{PromptTokens: 30, CompletionTokens: 1},
	}, nil
}

// calls returns how many decision requests were made.
func (r *decisionRecorder) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

// lastState returns the conversation state of the most recent decision request.
func (r *decisionRecorder) lastState(t *testing.T) []complexity.ConversationMessage {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	require.NotEmpty(t, r.requests)
	state, ok := r.requests[len(r.requests)-1].Input.Structured.([]complexity.ConversationMessage)
	require.True(t, ok)
	return state
}

// decisionAnalyzerTestConfig selects the decision model as the primary classifier.
func decisionAnalyzerTestConfig() *complexity.AnalyzerConfig {
	return &complexity.AnalyzerConfig{
		Classifier: complexity.ClassifierDecision,
		Keywords: complexity.EditableKeywordConfig{
			SimpleKeywords:  []string{"papaya amber"},
			MediumKeywords:  []string{"cedar cobalt"},
			ComplexKeywords: []string{"obsidian comet"},
		},
	}
}

// decisionFallbackAnalyzerTestConfig is the llm-fallback fixture with the decision model as the
// semantic fallback. The llm block stays saved so the tests can prove the decision model,
// not the chat classifier, answers.
func decisionFallbackAnalyzerTestConfig() *complexity.AnalyzerConfig {
	config := llmFallbackAnalyzerTestConfig()
	config.Semantic.Fallback = configstore.ComplexitySemanticFallbackDecision
	return config
}

// decisionTestPlugin builds a routing plugin whose one rule sends COMPLEX to
// gpt-4o-mini, with the given analyzer config and decision-model fake installed.
func decisionTestPlugin(t *testing.T, config *routing.Config, analyzerConfig *complexity.AnalyzerConfig, decision *decisionRecorder) *routing.RoutingPlugin {
	t.Helper()
	provider := "openai"
	routeModel := "gpt-4o-mini"
	logger := rules.NewMockLogger()
	ruleStore, err := rules.NewLocalStore(context.Background(), logger, nil)
	require.NoError(t, err)
	require.NoError(t, ruleStore.UpsertRule(context.Background(), &configstoreTables.TableRoutingRule{
		ID:            "decision-complex-rule",
		Name:          "Decision model complex route",
		CelExpression: `complexity_tier == "COMPLEX"`,
		Targets: []configstoreTables.TableRoutingTarget{
			{Provider: &provider, Model: &routeModel, Weight: 1.0},
		},
		Enabled:  schemas.Ptr(true),
		Scope:    "global",
		Priority: 0,
	}))
	plugin, err := routing.InitFromStore(context.Background(), config, logger, nil, ruleStore, routing.NewMockGovernance())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })
	plugin.SetDecisionRequestExecutor(decision.execute)
	require.NoError(t, plugin.ReloadComplexityAnalyzerConfig(analyzerConfig))
	return plugin
}

// failOnChatClassifier installs a chat executor that fails the test if the llm classifier runs.
func failOnChatClassifier(t *testing.T, plugin *routing.RoutingPlugin) {
	plugin.SetChatRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		t.Error("the llm classifier must not run when the decision model is selected")
		return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "unexpected llm classifier call"}}
	})
}

// routingLogs joins every routing-engine log message on the context.
func routingLogs(ctx *schemas.BifrostContext) string {
	var messages []string
	for _, entry := range ctx.GetRoutingEngineLogs() {
		messages = append(messages, entry.Message)
	}
	return strings.Join(messages, "\n")
}

// TestPreRequestHook_DecisionPrimaryPublishesTierAndRoutes proves the decision model as the
// primary classifier end to end: its tier drives the routing rule, the
// mechanism records decision, and a saved semantic block stays dormant (no
// warmup, no embedding call) while the decision model is selected.
func TestPreRequestHook_DecisionPrimaryPublishesTierAndRoutes(t *testing.T) {
	var embedCalls atomic.Int64
	analyzerConfig := decisionAnalyzerTestConfig()
	analyzerConfig.Semantic = &complexity.SemanticConfig{
		Provider:       schemas.OpenAI,
		EmbeddingModel: "test-embedding-model",
		VectorStore:    configstore.ComplexitySemanticVectorStoreEmbedded,
	}
	decision := &decisionRecorder{tiers: map[string]string{"prove the scheduler is deadlock-free": complexity.TierComplex}}
	plugin := decisionTestPlugin(t, nil, analyzerConfig, decision)
	plugin.SetEmbeddingRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		embedCalls.Add(1)
		return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "semantic must stay dormant"}}
	})
	failOnChatClassifier(t, plugin)

	req := llmComplexityChatRequest("prove the scheduler is deadlock-free")
	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(bfCtx, req))

	require.Equal(t, 1, decision.calls())
	require.Equal(t, complexity.TierComplex, bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))
	require.Equal(t, complexity.MechanismDecision, bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
	require.Nil(t, bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityScore), "The decision model has no similarity score to publish")
	_, modelOut, _ := req.GetRequestFields()
	require.Equal(t, "gpt-4o-mini", modelOut)
	require.Contains(t, routingLogs(bfCtx), "Decision model complexity: tier=COMPLEX")
	require.Zero(t, embedCalls.Load(), "a saved semantic block must not warm or embed while the decision model is primary")
}

// TestPreRequestHook_DecisionPrimaryLowerTierDoesNotRoute checks that a non-matching
// decision-model tier leaves the request model untouched while still publishing the tier.
func TestPreRequestHook_DecisionPrimaryLowerTierDoesNotRoute(t *testing.T) {
	decision := &decisionRecorder{}
	plugin := decisionTestPlugin(t, nil, decisionAnalyzerTestConfig(), decision)

	req := llmComplexityChatRequest("what is 2 + 2")
	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(bfCtx, req))

	require.Equal(t, complexity.TierSimple, bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))
	_, modelOut, _ := req.GetRequestFields()
	require.Equal(t, "gpt-4o", modelOut)
}

// TestPreRequestHook_DecisionReceivesOnlyUserTurns pins extraction into the decision model's
// state for both Chat and Responses requests: system prompts and assistant
// replies are never sent, and the configured history window is honoured.
func TestPreRequestHook_DecisionReceivesOnlyUserTurns(t *testing.T) {
	userRole := schemas.ResponsesInputMessageRoleUser
	assistantRole := schemas.ResponsesInputMessageRoleAssistant
	systemRole := schemas.ResponsesInputMessageRoleSystem
	responsesText := func(text string) *schemas.ResponsesMessageContent {
		return &schemas.ResponsesMessageContent{ContentStr: &text}
	}

	requests := map[string]func() *schemas.BifrostRequest{
		"chat": func() *schemas.BifrostRequest {
			return &schemas.BifrostRequest{
				RequestType: schemas.ChatCompletionRequest,
				ChatRequest: &schemas.BifrostChatRequest{
					Provider: schemas.OpenAI,
					Model:    "gpt-4o",
					Input: []schemas.ChatMessage{
						{Role: schemas.ChatMessageRoleSystem, Content: chatString("you are a helpful bot")},
						{Role: schemas.ChatMessageRoleUser, Content: chatString("first question")},
						{Role: schemas.ChatMessageRoleAssistant, Content: chatString("first answer")},
						{Role: schemas.ChatMessageRoleUser, Content: chatString("second question")},
						{Role: schemas.ChatMessageRoleAssistant, Content: chatString("second answer")},
						{Role: schemas.ChatMessageRoleUser, Content: chatString("current question")},
					},
				},
			}
		},
		"responses": func() *schemas.BifrostRequest {
			return &schemas.BifrostRequest{
				RequestType: schemas.ResponsesRequest,
				ResponsesRequest: &schemas.BifrostResponsesRequest{
					Provider: schemas.OpenAI,
					Model:    "gpt-4o",
					Input: []schemas.ResponsesMessage{
						{Role: &systemRole, Content: responsesText("you are a helpful bot")},
						{Role: &userRole, Content: responsesText("first question")},
						{Role: &assistantRole, Content: responsesText("first answer")},
						{Role: &userRole, Content: responsesText("second question")},
						{Role: &assistantRole, Content: responsesText("second answer")},
						{Role: &userRole, Content: responsesText("current question")},
					},
				},
			}
		},
	}

	for name, build := range requests {
		t.Run(name, func(t *testing.T) {
			count := 1
			analyzerConfig := decisionAnalyzerTestConfig()
			analyzerConfig.Decision = &complexity.DecisionConfig{PreviousMessageCount: &count}
			decision := &decisionRecorder{}
			plugin := decisionTestPlugin(t, nil, analyzerConfig, decision)

			require.NoError(t, plugin.PreRequestHook(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), build()))

			require.Equal(t, []complexity.ConversationMessage{
				{Role: "user", Content: "second question"},
				{Role: "user", Content: "current question"},
			}, decision.lastState(t))
		})
	}
}

// TestPreRequestHook_DecisionPrimaryFailurePublishesNoTier checks that a failed decision-model call
// call leaves the request on its original model, records mechanism=skipped,
// and never cascades into the llm classifier, even when an llm block is saved.
func TestPreRequestHook_DecisionPrimaryFailurePublishesNoTier(t *testing.T) {
	analyzerConfig := decisionAnalyzerTestConfig()
	analyzerConfig.LLM = &complexity.LLMConfig{Provider: schemas.OpenAI, Model: "test-classifier-model", Timeout: time.Second}
	decision := &decisionRecorder{err: &schemas.BifrostError{Error: &schemas.ErrorField{Message: "no keys found that support model: jev-latest"}}}
	plugin := decisionTestPlugin(t, nil, analyzerConfig, decision)
	failOnChatClassifier(t, plugin)

	req := llmComplexityChatRequest("prove the scheduler is deadlock-free")
	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(bfCtx, req))

	require.Equal(t, 1, decision.calls())
	require.Nil(t, bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))
	require.Equal(t, complexity.MechanismSkipped, bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
	_, modelOut, _ := req.GetRequestFields()
	require.Equal(t, "gpt-4o", modelOut)
	require.Contains(t, routingLogs(bfCtx), "no keys found that support model: jev-latest")
}

// TestPreRequestHook_SemanticFallsBackToDecision proves the fallback flow: a
// confident semantic match never reaches the decision model, a rejection is handed to the decision model,
// and the decision model wins over a saved llm block because only the selected fallback runs.
func TestPreRequestHook_SemanticFallsBackToDecision(t *testing.T) {
	analyzerConfig := decisionFallbackAnalyzerTestConfig()
	analyzerConfig.Semantic.MinSimilarity = 0.9
	decision := &decisionRecorder{tiers: map[string]string{"prove the scheduler is deadlock-free": complexity.TierComplex}}
	plugin := decisionTestPlugin(t, nil, analyzerConfig, decision)
	failOnChatClassifier(t, plugin)
	installSemanticEmbeddingFake(t, plugin)

	semanticCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(semanticCtx, llmComplexityChatRequest("papaya amber")))
	require.Equal(t, complexity.MechanismSemantic, semanticCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
	require.Zero(t, decision.calls(), "a confident semantic answer must not consult the decision model")

	fallbackReq := llmComplexityChatRequest("prove the scheduler is deadlock-free")
	fallbackCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(fallbackCtx, fallbackReq))

	require.Equal(t, 1, decision.calls())
	require.Equal(t, complexity.TierComplex, fallbackCtx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))
	require.Equal(t, complexity.MechanismDecision, fallbackCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
	require.Nil(t, fallbackCtx.Value(schemas.BifrostContextKeyGovernanceComplexityScore), "the rejected semantic score must not leak onto a decision-model decision")
	_, modelOut, _ := fallbackReq.GetRequestFields()
	require.Equal(t, "gpt-4o-mini", modelOut)
	logs := routingLogs(fallbackCtx)
	require.Contains(t, logs, "below min_similarity")
	require.Contains(t, logs, "falling back to the decision model")
	require.Contains(t, logs, "Decision model complexity: tier=COMPLEX")
}

// TestPreRequestHook_DecisionFallbackCoversSemanticWarmup covers the startup gap:
// while semantic exemplars are still warming, the decision model classifies instead of
// every request going unrouted.
func TestPreRequestHook_DecisionFallbackCoversSemanticWarmup(t *testing.T) {
	warmupStarted := make(chan struct{}, 1)
	releaseWarmup := make(chan struct{})
	defer close(releaseWarmup)

	decision := &decisionRecorder{tiers: map[string]string{"prove the scheduler is deadlock-free": complexity.TierComplex}}
	plugin := decisionTestPlugin(t, nil, decisionAnalyzerTestConfig(), decision)
	plugin.SetEmbeddingRequestExecutor(func(ctx *schemas.BifrostContext, req *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		select {
		case warmupStarted <- struct{}{}:
		default:
		}
		<-releaseWarmup
		return testEmbeddingExecutor(ctx, req)
	})
	require.NoError(t, plugin.ReloadComplexityAnalyzerConfig(decisionFallbackAnalyzerTestConfig()))
	failOnChatClassifier(t, plugin)

	select {
	case <-warmupStarted:
	case <-time.After(time.Second):
		t.Fatal("semantic warmup did not start")
	}

	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(bfCtx, llmComplexityChatRequest("prove the scheduler is deadlock-free")))

	require.Equal(t, 1, decision.calls())
	require.Equal(t, complexity.TierComplex, bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))
	require.Equal(t, complexity.MechanismDecision, bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
	require.Contains(t, routingLogs(bfCtx), "falling back to the decision model")
}

// TestPreRequestHook_DecisionFallbackFailureDoesNotCascade checks that when both
// semantic and its decision-model fallback produce nothing, no tier is published and the
// saved llm block is not tried as a third classifier.
func TestPreRequestHook_DecisionFallbackFailureDoesNotCascade(t *testing.T) {
	analyzerConfig := decisionFallbackAnalyzerTestConfig()
	analyzerConfig.Semantic.MinSimilarity = 0.9
	decision := &decisionRecorder{err: &schemas.BifrostError{Error: &schemas.ErrorField{Message: "typesafe unavailable"}}}
	plugin := decisionTestPlugin(t, nil, analyzerConfig, decision)
	failOnChatClassifier(t, plugin)
	installSemanticEmbeddingFake(t, plugin)

	req := llmComplexityChatRequest("prove the scheduler is deadlock-free")
	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(bfCtx, req))

	require.Equal(t, 1, decision.calls())
	require.Nil(t, bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))
	require.Equal(t, complexity.MechanismSkipped, bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
	_, modelOut, _ := req.GetRequestFields()
	require.Equal(t, "gpt-4o", modelOut)
}

// TestPreRequestHook_SemanticWithoutDecisionFallbackNeverCallsDecision checks that a
// wired decision executor alone never runs the decision model: semantic with fallback "none"
// publishes nothing on a rejection.
func TestPreRequestHook_SemanticWithoutDecisionFallbackNeverCallsDecision(t *testing.T) {
	analyzerConfig := llmFallbackAnalyzerTestConfig()
	analyzerConfig.Semantic.Fallback = configstore.ComplexitySemanticFallbackNone
	analyzerConfig.Semantic.MinSimilarity = 0.9
	decision := &decisionRecorder{tiers: map[string]string{"prove the scheduler is deadlock-free": complexity.TierComplex}}
	plugin := decisionTestPlugin(t, nil, analyzerConfig, decision)
	failOnChatClassifier(t, plugin)
	installSemanticEmbeddingFake(t, plugin)

	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(bfCtx, llmComplexityChatRequest("prove the scheduler is deadlock-free")))

	require.Zero(t, decision.calls())
	require.Nil(t, bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))
	require.Equal(t, complexity.MechanismSkipped, bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
}

// TestPreRequestHook_SwitchingClassifierTakesEffect covers the UI toggle: a
// reload from the decision model to semantic re-arms semantic warmup and stops decision-model calls,
// and a reload back to the decision model stops embedding requests.
func TestPreRequestHook_SwitchingClassifierTakesEffect(t *testing.T) {
	decision := &decisionRecorder{}
	plugin := decisionTestPlugin(t, nil, decisionAnalyzerTestConfig(), decision)

	decisionCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(decisionCtx, llmComplexityChatRequest("papaya amber")))
	require.Equal(t, complexity.MechanismDecision, decisionCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
	require.Equal(t, 1, decision.calls())

	semanticConfig := llmFallbackAnalyzerTestConfig()
	semanticConfig.Semantic.Fallback = configstore.ComplexitySemanticFallbackNone
	require.NoError(t, plugin.ReloadComplexityAnalyzerConfig(semanticConfig))
	installSemanticEmbeddingFake(t, plugin)

	semanticCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(semanticCtx, llmComplexityChatRequest("papaya amber")))
	require.Equal(t, complexity.MechanismSemantic, semanticCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
	require.Equal(t, 1, decision.calls(), "The decision model must stop being called once semantic is primary")

	var embedCalls atomic.Int64
	plugin.SetEmbeddingRequestExecutor(func(ctx *schemas.BifrostContext, req *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		embedCalls.Add(1)
		return testEmbeddingExecutor(ctx, req)
	})
	require.NoError(t, plugin.ReloadComplexityAnalyzerConfig(decisionAnalyzerTestConfig()))
	backCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(backCtx, llmComplexityChatRequest("papaya amber")))
	require.Equal(t, complexity.MechanismDecision, backCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
	require.Equal(t, 2, decision.calls())
	require.Zero(t, embedCalls.Load(), "switching back to the decision model must not embed the request")
}

// TestPreRequestHook_DecisionSessionOnlyEscalates proves session routing with the decision model
// as the primary classifier and no semantic block: lower decision-model proposals are
// held at the session tier, COMPLEX escalates, and the COMPLEX ceiling skips
// further decision-model calls entirely.
func TestPreRequestHook_DecisionSessionOnlyEscalates(t *testing.T) {
	store, err := kvstore.New(kvstore.Config{CleanupInterval: time.Hour})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	analyzerConfig := decisionAnalyzerTestConfig()
	analyzerConfig.Session = &complexity.SessionConfig{Enabled: true}
	decision := &decisionRecorder{tiers: map[string]string{
		"a medium request":  complexity.TierMedium,
		"a simple request":  complexity.TierSimple,
		"a complex request": complexity.TierComplex,
	}}
	plugin := decisionTestPlugin(t, &routing.Config{KVStore: store}, analyzerConfig, decision)

	steps := []struct {
		text              string
		wantTier          string
		wantMechanism     string
		wantDecisionCalls int
		wantLogParts      []string
	}{
		{"a medium request", complexity.TierMedium, complexity.MechanismDecision, 1, []string{"Session complexity initialized:", "source=decision", "proposed_confidence=0.90", "proposed_model=typesafe/jev-latest"}},
		{"a simple request", complexity.TierMedium, complexity.MechanismSession, 2, []string{"Session complexity held:", "effective=MEDIUM", "proposed=SIMPLE", "source=decision"}},
		{"a complex request", complexity.TierComplex, complexity.MechanismDecision, 3, []string{"Session complexity escalated:", "previous=MEDIUM", "source=decision"}},
		{"a simple request", complexity.TierComplex, complexity.MechanismSession, 3, []string{"reason=complex-ceiling"}},
	}
	for _, step := range steps {
		ctx := complexitySessionContext("decision-session")
		require.NoError(t, plugin.PreRequestHook(ctx, chatRequest(step.text)))
		require.Equal(t, step.wantTier, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityTier), step.text)
		require.Equal(t, step.wantMechanism, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism), step.text)
		require.Equal(t, step.wantDecisionCalls, decision.calls(), step.text)
		logs := routingLogs(ctx)
		for _, part := range step.wantLogParts {
			require.Contains(t, logs, part, step.text)
		}
	}
}

// TestValidateComplexityAnalyzerConfig_Decision pins save-time validation: the decision model as
// primary or fallback needs the decision executor, and the decision model as primary does
// not need the embedding executor even with a semantic block saved.
func TestValidateComplexityAnalyzerConfig_Decision(t *testing.T) {
	newPlugin := func(t *testing.T) *routing.RoutingPlugin {
		t.Helper()
		logger := rules.NewMockLogger()
		ruleStore, err := rules.NewLocalStore(context.Background(), logger, nil)
		require.NoError(t, err)
		plugin, err := routing.InitFromStore(context.Background(), nil, logger, nil, ruleStore, routing.NewMockGovernance())
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })
		return plugin
	}

	t.Run("decision primary without executor", func(t *testing.T) {
		err := newPlugin(t).ValidateComplexityAnalyzerConfig(decisionAnalyzerTestConfig())
		require.ErrorContains(t, err, "decision-model executor is unavailable")
	})

	t.Run("decision fallback without executor", func(t *testing.T) {
		plugin := newPlugin(t)
		plugin.SetEmbeddingRequestExecutor(testEmbeddingExecutor)
		err := plugin.ValidateComplexityAnalyzerConfig(decisionFallbackAnalyzerTestConfig())
		require.ErrorContains(t, err, "decision-model executor is unavailable")
	})

	t.Run("decision primary ignores a saved semantic block", func(t *testing.T) {
		plugin := newPlugin(t)
		plugin.SetDecisionRequestExecutor((&decisionRecorder{}).execute)
		config := decisionAnalyzerTestConfig()
		config.Semantic = &complexity.SemanticConfig{Provider: schemas.OpenAI, EmbeddingModel: "test-embedding-model"}
		require.NoError(t, plugin.ValidateComplexityAnalyzerConfig(config))
	})

	t.Run("decision session without semantic block", func(t *testing.T) {
		store, err := kvstore.New(kvstore.Config{CleanupInterval: time.Hour})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, store.Close()) })
		logger := rules.NewMockLogger()
		ruleStore, err := rules.NewLocalStore(context.Background(), logger, nil)
		require.NoError(t, err)
		plugin, err := routing.InitFromStore(context.Background(), &routing.Config{KVStore: store}, logger, nil, ruleStore, routing.NewMockGovernance())
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })
		plugin.SetDecisionRequestExecutor((&decisionRecorder{}).execute)

		config := decisionAnalyzerTestConfig()
		config.Session = &complexity.SessionConfig{Enabled: true}
		require.NoError(t, plugin.ValidateComplexityAnalyzerConfig(config))
	})
}
