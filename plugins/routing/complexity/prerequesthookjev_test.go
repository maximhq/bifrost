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

// jevRecorder is a fake Typesafe decision executor. It answers with the tier
// mapped to the latest user message and records every request it receives.
type jevRecorder struct {
	mu       sync.Mutex
	requests []*schemas.BifrostDecisionRequest
	tiers    map[string]string
	err      *schemas.BifrostError
}

// execute implements routing.DecisionRequestExecutor.
func (r *jevRecorder) execute(_ *schemas.BifrostContext, req *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, req)
	if r.err != nil {
		return nil, r.err
	}
	state, _ := req.State.([]complexity.ConversationMessage)
	tier := "SIMPLE"
	if len(state) > 0 {
		if mapped, ok := r.tiers[state[len(state)-1].Content]; ok {
			tier = mapped
		}
	}
	confidence := 0.9
	return &schemas.BifrostDecisionResponse{
		Model: "jev-1.13.0",
		Answers: map[string]schemas.DecisionAnswer{
			"complexity_tier": {Kind: schemas.DecisionKindChoice, Value: tier, Confidence: &confidence},
		},
		Usage: &schemas.BifrostLLMUsage{PromptTokens: 30, CompletionTokens: 1},
	}, nil
}

// calls returns how many decision requests were made.
func (r *jevRecorder) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

// lastState returns the conversation state of the most recent decision request.
func (r *jevRecorder) lastState(t *testing.T) []complexity.ConversationMessage {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	require.NotEmpty(t, r.requests)
	state, ok := r.requests[len(r.requests)-1].State.([]complexity.ConversationMessage)
	require.True(t, ok)
	return state
}

// jevAnalyzerTestConfig selects Jev as the primary classifier.
func jevAnalyzerTestConfig() *complexity.AnalyzerConfig {
	return &complexity.AnalyzerConfig{
		Classifier: complexity.ClassifierJev,
		Keywords: complexity.EditableKeywordConfig{
			SimpleKeywords:  []string{"papaya amber"},
			MediumKeywords:  []string{"cedar cobalt"},
			ComplexKeywords: []string{"obsidian comet"},
		},
	}
}

// jevFallbackAnalyzerTestConfig is the llm-fallback fixture with Jev as the
// semantic fallback. The llm block stays saved so the tests can prove Jev,
// not the chat classifier, answers.
func jevFallbackAnalyzerTestConfig() *complexity.AnalyzerConfig {
	config := llmFallbackAnalyzerTestConfig()
	config.Semantic.Fallback = configstore.ComplexitySemanticFallbackJev
	return config
}

// jevTestPlugin builds a routing plugin whose one rule sends COMPLEX to
// gpt-4o-mini, with the given analyzer config and Jev fake installed.
func jevTestPlugin(t *testing.T, config *routing.Config, analyzerConfig *complexity.AnalyzerConfig, jev *jevRecorder) *routing.RoutingPlugin {
	t.Helper()
	provider := "openai"
	routeModel := "gpt-4o-mini"
	logger := rules.NewMockLogger()
	ruleStore, err := rules.NewLocalStore(context.Background(), logger, nil)
	require.NoError(t, err)
	require.NoError(t, ruleStore.UpsertRule(context.Background(), &configstoreTables.TableRoutingRule{
		ID:            "jev-complex-rule",
		Name:          "Jev complex route",
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
	plugin.SetDecisionRequestExecutor(jev.execute)
	require.NoError(t, plugin.ReloadComplexityAnalyzerConfig(analyzerConfig))
	return plugin
}

// failOnChatClassifier installs a chat executor that fails the test if the llm classifier runs.
func failOnChatClassifier(t *testing.T, plugin *routing.RoutingPlugin) {
	plugin.SetChatRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		t.Error("the llm classifier must not run when Jev is selected")
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

// TestPreRequestHook_JevPrimaryPublishesTierAndRoutes proves Jev as the
// primary classifier end to end: its tier drives the routing rule, the
// mechanism records jev, and a saved semantic block stays dormant (no
// warmup, no embedding call) while Jev is selected.
func TestPreRequestHook_JevPrimaryPublishesTierAndRoutes(t *testing.T) {
	var embedCalls atomic.Int64
	analyzerConfig := jevAnalyzerTestConfig()
	analyzerConfig.Semantic = &complexity.SemanticConfig{
		Provider:       schemas.OpenAI,
		EmbeddingModel: "test-embedding-model",
		VectorStore:    configstore.ComplexitySemanticVectorStoreEmbedded,
	}
	jev := &jevRecorder{tiers: map[string]string{"prove the scheduler is deadlock-free": complexity.TierComplex}}
	plugin := jevTestPlugin(t, nil, analyzerConfig, jev)
	plugin.SetEmbeddingRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		embedCalls.Add(1)
		return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "semantic must stay dormant"}}
	})
	failOnChatClassifier(t, plugin)

	req := llmComplexityChatRequest("prove the scheduler is deadlock-free")
	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(bfCtx, req))

	require.Equal(t, 1, jev.calls())
	require.Equal(t, complexity.TierComplex, bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))
	require.Equal(t, complexity.MechanismJev, bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
	require.Nil(t, bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityScore), "Jev has no similarity score to publish")
	_, modelOut, _ := req.GetRequestFields()
	require.Equal(t, "gpt-4o-mini", modelOut)
	require.Contains(t, routingLogs(bfCtx), "Jev complexity: tier=COMPLEX")
	require.Zero(t, embedCalls.Load(), "a saved semantic block must not warm or embed while Jev is primary")
}

// TestPreRequestHook_JevPrimaryLowerTierDoesNotRoute checks that a non-matching
// Jev tier leaves the request model untouched while still publishing the tier.
func TestPreRequestHook_JevPrimaryLowerTierDoesNotRoute(t *testing.T) {
	jev := &jevRecorder{}
	plugin := jevTestPlugin(t, nil, jevAnalyzerTestConfig(), jev)

	req := llmComplexityChatRequest("what is 2 + 2")
	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(bfCtx, req))

	require.Equal(t, complexity.TierSimple, bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))
	_, modelOut, _ := req.GetRequestFields()
	require.Equal(t, "gpt-4o", modelOut)
}

// TestPreRequestHook_JevReceivesOnlyUserTurns pins extraction into Jev's
// state for both Chat and Responses requests: system prompts and assistant
// replies are never sent, and the configured history window is honoured.
func TestPreRequestHook_JevReceivesOnlyUserTurns(t *testing.T) {
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
			analyzerConfig := jevAnalyzerTestConfig()
			analyzerConfig.Jev = &complexity.JevConfig{PreviousMessageCount: &count}
			jev := &jevRecorder{}
			plugin := jevTestPlugin(t, nil, analyzerConfig, jev)

			require.NoError(t, plugin.PreRequestHook(schemas.NewBifrostContext(context.Background(), schemas.NoDeadline), build()))

			require.Equal(t, []complexity.ConversationMessage{
				{Role: "user", Content: "second question"},
				{Role: "user", Content: "current question"},
			}, jev.lastState(t))
		})
	}
}

// TestPreRequestHook_JevPrimaryFailurePublishesNoTier checks that a failed Jev
// call leaves the request on its original model, records mechanism=skipped,
// and never cascades into the llm classifier, even when an llm block is saved.
func TestPreRequestHook_JevPrimaryFailurePublishesNoTier(t *testing.T) {
	analyzerConfig := jevAnalyzerTestConfig()
	analyzerConfig.LLM = &complexity.LLMConfig{Provider: schemas.OpenAI, Model: "test-classifier-model", Timeout: time.Second}
	jev := &jevRecorder{err: &schemas.BifrostError{Error: &schemas.ErrorField{Message: "no keys found that support model: jev-latest"}}}
	plugin := jevTestPlugin(t, nil, analyzerConfig, jev)
	failOnChatClassifier(t, plugin)

	req := llmComplexityChatRequest("prove the scheduler is deadlock-free")
	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(bfCtx, req))

	require.Equal(t, 1, jev.calls())
	require.Nil(t, bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))
	require.Equal(t, complexity.MechanismSkipped, bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
	_, modelOut, _ := req.GetRequestFields()
	require.Equal(t, "gpt-4o", modelOut)
	require.Contains(t, routingLogs(bfCtx), "no keys found that support model: jev-latest")
}

// TestPreRequestHook_SemanticFallsBackToJev proves the fallback flow: a
// confident semantic match never reaches Jev, a rejection is handed to Jev,
// and Jev wins over a saved llm block because only the selected fallback runs.
func TestPreRequestHook_SemanticFallsBackToJev(t *testing.T) {
	analyzerConfig := jevFallbackAnalyzerTestConfig()
	analyzerConfig.Semantic.MinSimilarity = 0.9
	jev := &jevRecorder{tiers: map[string]string{"prove the scheduler is deadlock-free": complexity.TierComplex}}
	plugin := jevTestPlugin(t, nil, analyzerConfig, jev)
	failOnChatClassifier(t, plugin)
	installSemanticEmbeddingFake(t, plugin)

	semanticCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(semanticCtx, llmComplexityChatRequest("papaya amber")))
	require.Equal(t, complexity.MechanismSemantic, semanticCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
	require.Zero(t, jev.calls(), "a confident semantic answer must not consult Jev")

	fallbackReq := llmComplexityChatRequest("prove the scheduler is deadlock-free")
	fallbackCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(fallbackCtx, fallbackReq))

	require.Equal(t, 1, jev.calls())
	require.Equal(t, complexity.TierComplex, fallbackCtx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))
	require.Equal(t, complexity.MechanismJev, fallbackCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
	require.Nil(t, fallbackCtx.Value(schemas.BifrostContextKeyGovernanceComplexityScore), "the rejected semantic score must not leak onto a Jev decision")
	_, modelOut, _ := fallbackReq.GetRequestFields()
	require.Equal(t, "gpt-4o-mini", modelOut)
	logs := routingLogs(fallbackCtx)
	require.Contains(t, logs, "below min_similarity")
	require.Contains(t, logs, "falling back to Jev")
	require.Contains(t, logs, "Jev complexity: tier=COMPLEX")
}

// TestPreRequestHook_JevFallbackCoversSemanticWarmup covers the startup gap:
// while semantic exemplars are still warming, Jev classifies instead of
// every request going unrouted.
func TestPreRequestHook_JevFallbackCoversSemanticWarmup(t *testing.T) {
	warmupStarted := make(chan struct{}, 1)
	releaseWarmup := make(chan struct{})
	defer close(releaseWarmup)

	jev := &jevRecorder{tiers: map[string]string{"prove the scheduler is deadlock-free": complexity.TierComplex}}
	plugin := jevTestPlugin(t, nil, jevAnalyzerTestConfig(), jev)
	plugin.SetEmbeddingRequestExecutor(func(ctx *schemas.BifrostContext, req *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		select {
		case warmupStarted <- struct{}{}:
		default:
		}
		<-releaseWarmup
		return testEmbeddingExecutor(ctx, req)
	})
	require.NoError(t, plugin.ReloadComplexityAnalyzerConfig(jevFallbackAnalyzerTestConfig()))
	failOnChatClassifier(t, plugin)

	select {
	case <-warmupStarted:
	case <-time.After(time.Second):
		t.Fatal("semantic warmup did not start")
	}

	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(bfCtx, llmComplexityChatRequest("prove the scheduler is deadlock-free")))

	require.Equal(t, 1, jev.calls())
	require.Equal(t, complexity.TierComplex, bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))
	require.Equal(t, complexity.MechanismJev, bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
	require.Contains(t, routingLogs(bfCtx), "falling back to Jev")
}

// TestPreRequestHook_JevFallbackFailureDoesNotCascade checks that when both
// semantic and its Jev fallback produce nothing, no tier is published and the
// saved llm block is not tried as a third classifier.
func TestPreRequestHook_JevFallbackFailureDoesNotCascade(t *testing.T) {
	analyzerConfig := jevFallbackAnalyzerTestConfig()
	analyzerConfig.Semantic.MinSimilarity = 0.9
	jev := &jevRecorder{err: &schemas.BifrostError{Error: &schemas.ErrorField{Message: "typesafe unavailable"}}}
	plugin := jevTestPlugin(t, nil, analyzerConfig, jev)
	failOnChatClassifier(t, plugin)
	installSemanticEmbeddingFake(t, plugin)

	req := llmComplexityChatRequest("prove the scheduler is deadlock-free")
	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(bfCtx, req))

	require.Equal(t, 1, jev.calls())
	require.Nil(t, bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))
	require.Equal(t, complexity.MechanismSkipped, bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
	_, modelOut, _ := req.GetRequestFields()
	require.Equal(t, "gpt-4o", modelOut)
}

// TestPreRequestHook_SemanticWithoutJevFallbackNeverCallsJev checks that a
// wired decision executor alone never runs Jev: semantic with fallback "none"
// publishes nothing on a rejection.
func TestPreRequestHook_SemanticWithoutJevFallbackNeverCallsJev(t *testing.T) {
	analyzerConfig := llmFallbackAnalyzerTestConfig()
	analyzerConfig.Semantic.Fallback = configstore.ComplexitySemanticFallbackNone
	analyzerConfig.Semantic.MinSimilarity = 0.9
	jev := &jevRecorder{tiers: map[string]string{"prove the scheduler is deadlock-free": complexity.TierComplex}}
	plugin := jevTestPlugin(t, nil, analyzerConfig, jev)
	failOnChatClassifier(t, plugin)
	installSemanticEmbeddingFake(t, plugin)

	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(bfCtx, llmComplexityChatRequest("prove the scheduler is deadlock-free")))

	require.Zero(t, jev.calls())
	require.Nil(t, bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))
	require.Equal(t, complexity.MechanismSkipped, bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
}

// TestPreRequestHook_SwitchingClassifierTakesEffect covers the UI toggle: a
// reload from Jev to semantic re-arms semantic warmup and stops Jev calls,
// and a reload back to Jev stops embedding requests.
func TestPreRequestHook_SwitchingClassifierTakesEffect(t *testing.T) {
	jev := &jevRecorder{}
	plugin := jevTestPlugin(t, nil, jevAnalyzerTestConfig(), jev)

	jevCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(jevCtx, llmComplexityChatRequest("papaya amber")))
	require.Equal(t, complexity.MechanismJev, jevCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
	require.Equal(t, 1, jev.calls())

	semanticConfig := llmFallbackAnalyzerTestConfig()
	semanticConfig.Semantic.Fallback = configstore.ComplexitySemanticFallbackNone
	require.NoError(t, plugin.ReloadComplexityAnalyzerConfig(semanticConfig))
	installSemanticEmbeddingFake(t, plugin)

	semanticCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(semanticCtx, llmComplexityChatRequest("papaya amber")))
	require.Equal(t, complexity.MechanismSemantic, semanticCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
	require.Equal(t, 1, jev.calls(), "Jev must stop being called once semantic is primary")

	var embedCalls atomic.Int64
	plugin.SetEmbeddingRequestExecutor(func(ctx *schemas.BifrostContext, req *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		embedCalls.Add(1)
		return testEmbeddingExecutor(ctx, req)
	})
	require.NoError(t, plugin.ReloadComplexityAnalyzerConfig(jevAnalyzerTestConfig()))
	backCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(backCtx, llmComplexityChatRequest("papaya amber")))
	require.Equal(t, complexity.MechanismJev, backCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
	require.Equal(t, 2, jev.calls())
	require.Zero(t, embedCalls.Load(), "switching back to Jev must not embed the request")
}

// TestPreRequestHook_JevSessionOnlyEscalates proves session routing with Jev
// as the primary classifier and no semantic block: lower Jev proposals are
// held at the session tier, COMPLEX escalates, and the COMPLEX ceiling skips
// further Jev calls entirely.
func TestPreRequestHook_JevSessionOnlyEscalates(t *testing.T) {
	store, err := kvstore.New(kvstore.Config{CleanupInterval: time.Hour})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	analyzerConfig := jevAnalyzerTestConfig()
	analyzerConfig.Session = &complexity.SessionConfig{Enabled: true}
	jev := &jevRecorder{tiers: map[string]string{
		"a medium request":  complexity.TierMedium,
		"a simple request":  complexity.TierSimple,
		"a complex request": complexity.TierComplex,
	}}
	plugin := jevTestPlugin(t, &routing.Config{KVStore: store}, analyzerConfig, jev)

	steps := []struct {
		text          string
		wantTier      string
		wantMechanism string
		wantJevCalls  int
		wantLogParts  []string
	}{
		{"a medium request", complexity.TierMedium, complexity.MechanismJev, 1, []string{"Session complexity initialized:", "source=jev", "proposed_confidence=0.90"}},
		{"a simple request", complexity.TierMedium, complexity.MechanismSession, 2, []string{"Session complexity held:", "effective=MEDIUM", "proposed=SIMPLE", "source=jev"}},
		{"a complex request", complexity.TierComplex, complexity.MechanismJev, 3, []string{"Session complexity escalated:", "previous=MEDIUM", "source=jev"}},
		{"a simple request", complexity.TierComplex, complexity.MechanismSession, 3, []string{"reason=complex-ceiling"}},
	}
	for _, step := range steps {
		ctx := complexitySessionContext("jev-session")
		require.NoError(t, plugin.PreRequestHook(ctx, chatRequest(step.text)))
		require.Equal(t, step.wantTier, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityTier), step.text)
		require.Equal(t, step.wantMechanism, ctx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism), step.text)
		require.Equal(t, step.wantJevCalls, jev.calls(), step.text)
		logs := routingLogs(ctx)
		for _, part := range step.wantLogParts {
			require.Contains(t, logs, part, step.text)
		}
	}
}

// TestValidateComplexityAnalyzerConfig_Jev pins save-time validation: Jev as
// primary or fallback needs the decision executor, and Jev as primary does
// not need the embedding executor even with a semantic block saved.
func TestValidateComplexityAnalyzerConfig_Jev(t *testing.T) {
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

	t.Run("jev primary without executor", func(t *testing.T) {
		err := newPlugin(t).ValidateComplexityAnalyzerConfig(jevAnalyzerTestConfig())
		require.ErrorContains(t, err, "Jev complexity decision executor is unavailable")
	})

	t.Run("jev fallback without executor", func(t *testing.T) {
		plugin := newPlugin(t)
		plugin.SetEmbeddingRequestExecutor(testEmbeddingExecutor)
		err := plugin.ValidateComplexityAnalyzerConfig(jevFallbackAnalyzerTestConfig())
		require.ErrorContains(t, err, "Jev complexity decision executor is unavailable")
	})

	t.Run("jev primary ignores a saved semantic block", func(t *testing.T) {
		plugin := newPlugin(t)
		plugin.SetDecisionRequestExecutor((&jevRecorder{}).execute)
		config := jevAnalyzerTestConfig()
		config.Semantic = &complexity.SemanticConfig{Provider: schemas.OpenAI, EmbeddingModel: "test-embedding-model"}
		require.NoError(t, plugin.ValidateComplexityAnalyzerConfig(config))
	})

	t.Run("jev session without semantic block", func(t *testing.T) {
		store, err := kvstore.New(kvstore.Config{CleanupInterval: time.Hour})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, store.Close()) })
		logger := rules.NewMockLogger()
		ruleStore, err := rules.NewLocalStore(context.Background(), logger, nil)
		require.NoError(t, err)
		plugin, err := routing.InitFromStore(context.Background(), &routing.Config{KVStore: store}, logger, nil, ruleStore, routing.NewMockGovernance())
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })
		plugin.SetDecisionRequestExecutor((&jevRecorder{}).execute)

		config := jevAnalyzerTestConfig()
		config.Session = &complexity.SessionConfig{Enabled: true}
		require.NoError(t, plugin.ValidateComplexityAnalyzerConfig(config))
	})
}
