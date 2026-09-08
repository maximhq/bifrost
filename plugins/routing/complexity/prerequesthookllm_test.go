package complexity_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/plugins/routing"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/maximhq/bifrost/plugins/routing/rules"
)

// llmFallbackAnalyzerTestConfig wires the llm classifier the only way it can
// run: as the semantic classifier's fallback.
func llmFallbackAnalyzerTestConfig() *complexity.AnalyzerConfig {
	return &complexity.AnalyzerConfig{
		TierBoundaries: complexity.DefaultTierBoundaries(),
		Keywords: complexity.EditableKeywordConfig{
			SimpleKeywords:  []string{"papaya amber"},
			MediumKeywords:  []string{"cedar cobalt"},
			ComplexKeywords: []string{"obsidian comet"},
		},
		Semantic: &complexity.SemanticConfig{
			Provider:       schemas.OpenAI,
			EmbeddingModel: "test-embedding-model",
			Timeout:        time.Second,
			VectorStore:    configstore.ComplexitySemanticVectorStoreEmbedded,
			Fallback:       configstore.ComplexitySemanticFallbackLLM,
		},
		LLM: &complexity.LLMConfig{
			Provider: schemas.OpenAI,
			Model:    "test-classifier-model",
			Timeout:  time.Second,
		},
	}
}

func llmFallbackTestPlugin(t *testing.T, analyzerConfig *complexity.AnalyzerConfig) *routing.RoutingPlugin {
	t.Helper()
	provider := "openai"
	routeModel := "gpt-4o-mini"
	logger := rules.NewMockLogger()
	ruleStore, err := rules.NewLocalStore(context.Background(), logger, nil)
	require.NoError(t, err)
	require.NoError(t, ruleStore.UpsertRule(context.Background(), &configstoreTables.TableRoutingRule{
		ID:            "llm-complex-rule",
		Name:          "LLM complex route",
		CelExpression: `complexity_tier == "COMPLEX"`,
		Targets: []configstoreTables.TableRoutingTarget{
			{Provider: &provider, Model: &routeModel, Weight: 1.0},
		},
		Enabled:  schemas.Ptr(true),
		Scope:    "global",
		Priority: 0,
	}))
	plugin, err := routing.InitFromStore(context.Background(), nil, logger, nil, ruleStore, routing.NewMockGovernance())
	require.NoError(t, err)
	plugin.ReloadComplexityAnalyzerConfig(analyzerConfig)
	t.Cleanup(func() {
		require.NoError(t, plugin.Cleanup())
	})
	return plugin
}

// installSemanticEmbeddingFake wires an embedding executor whose exemplar
// vectors sit on distinct axes and whose request vector is controlled per
// text, then waits for warmup. Unknown texts land far from every exemplar so
// a min_similarity floor can force a rejection on demand.
func installSemanticEmbeddingFake(t *testing.T, plugin *routing.RoutingPlugin) {
	t.Helper()
	plugin.SetEmbeddingRequestExecutor(func(_ *schemas.BifrostContext, req *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		if req == nil || req.Input == nil {
			return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "embedding request did not contain text"}}
		}
		var texts []string
		if req.Input.Text != nil {
			texts = []string{*req.Input.Text}
		} else {
			texts = req.Input.Texts
		}
		data := make([]schemas.EmbeddingData, len(texts))
		for index, text := range texts {
			// Barely closer to "papaya amber" than to anything else, so the
			// argmax still resolves while the similarity stays far below a
			// strict floor.
			vector := []float64{0.3, 0.2, 0.2}
			switch text {
			case "papaya amber":
				vector = []float64{1, 0, 0}
			case "cedar cobalt":
				vector = []float64{0, 1, 0}
			case "obsidian comet":
				vector = []float64{0, 0, 1}
			}
			data[index] = schemas.EmbeddingData{
				Index:     index,
				Embedding: schemas.EmbeddingStruct{EmbeddingArray: vector},
			}
		}
		return &schemas.BifrostEmbeddingResponse{
			Data:  data,
			Usage: &schemas.BifrostLLMUsage{TotalTokens: len(texts)},
		}, nil
	})
	require.Eventually(t, func() bool {
		return plugin.ComplexitySemanticStatus().State == complexity.SemanticStatusReady
	}, time.Second, 10*time.Millisecond, "semantic classifier did not finish warmup")
}

func llmClassifierChatResponse(text string) *schemas.BifrostChatResponse {
	return &schemas.BifrostChatResponse{
		Choices: []schemas.BifrostResponseChoice{
			{
				ChatNonStreamResponseChoice: &schemas.ChatNonStreamResponseChoice{
					Message: &schemas.ChatMessage{
						Role:    schemas.ChatMessageRoleAssistant,
						Content: &schemas.ChatMessageContent{ContentStr: &text},
					},
				},
			},
		},
		Usage: &schemas.BifrostLLMUsage{PromptTokens: 20, CompletionTokens: 6},
	}
}

func llmComplexityChatRequest(text string) *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
			Input: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleUser, Content: chatString(text)},
			},
		},
	}
}

// TestPreRequestHook_LLMFallbackClassifiesSemanticRejections proves the
// fallback flow end to end: a request below the semantic similarity floor is
// handed to the chat model, whose tier routes the request under mechanism
// "llm", while a request semantic answers confidently never reaches the chat
// model at all.
func TestPreRequestHook_LLMFallbackClassifiesSemanticRejections(t *testing.T) {
	analyzerConfig := llmFallbackAnalyzerTestConfig()
	analyzerConfig.Semantic.MinSimilarity = 0.9
	plugin := llmFallbackTestPlugin(t, analyzerConfig)
	installSemanticEmbeddingFake(t, plugin)

	var executorMu sync.Mutex
	var gotRequests []*schemas.BifrostChatRequest
	plugin.SetChatRequestExecutor(func(_ *schemas.BifrostContext, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		executorMu.Lock()
		gotRequests = append(gotRequests, req)
		executorMu.Unlock()
		return llmClassifierChatResponse(`{"tier": "COMPLEX"}`), nil
	})

	// An exemplar phrase clears the 0.9 floor: semantic answers, the llm
	// executor stays silent, and the mechanism records semantic.
	semanticReq := llmComplexityChatRequest("papaya amber")
	semanticCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(semanticCtx, semanticReq))
	require.Equal(t, complexity.MechanismSemantic, semanticCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
	executorMu.Lock()
	require.Empty(t, gotRequests, "a confident semantic answer must not consult the llm fallback")
	executorMu.Unlock()

	// Unknown text lands below the floor: semantic abstains and the fallback
	// answers COMPLEX, which the routing rule then acts on.
	fallbackReq := llmComplexityChatRequest("prove the scheduler is deadlock-free")
	fallbackCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(fallbackCtx, fallbackReq))

	providerOut, modelOut, _ := fallbackReq.GetRequestFields()
	require.Equal(t, schemas.OpenAI, providerOut)
	require.Equal(t, "gpt-4o-mini", modelOut)
	require.Equal(t, complexity.TierComplex, fallbackCtx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))
	require.Equal(t, complexity.MechanismLLM, fallbackCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
	_, hasScore := fallbackCtx.Value(schemas.BifrostContextKeyGovernanceComplexityScore).(float64)
	require.False(t, hasScore, "the llm classifier has no similarity score to publish")

	// The routing log records the handoff and the fallback's own outcome as
	// separate decisions.
	var logMessages []string
	for _, entry := range fallbackCtx.GetRoutingEngineLogs() {
		logMessages = append(logMessages, entry.Message)
	}
	joined := strings.Join(logMessages, "\n")
	require.Contains(t, joined, "falling back to the LLM classifier")
	require.Contains(t, joined, "LLM complexity: tier=COMPLEX")

	executorMu.Lock()
	defer executorMu.Unlock()
	require.Len(t, gotRequests, 1)
	classifierReq := gotRequests[0]
	require.Equal(t, "test-classifier-model", classifierReq.Model)
	require.Len(t, classifierReq.Input, 2)
	require.Equal(t, schemas.ChatMessageRoleSystem, classifierReq.Input[0].Role)
	require.Equal(t, schemas.ChatMessageRoleUser, classifierReq.Input[1].Role)
	require.Equal(t, "prove the scheduler is deadlock-free", *classifierReq.Input[1].Content.ContentStr)
	require.NotNil(t, classifierReq.Params)
	require.NotNil(t, classifierReq.Params.Temperature)
	require.Zero(t, *classifierReq.Params.Temperature)
}

// TestPreRequestHook_LLMFallbackCoversSemanticUnavailability pins that the
// fallback engages on every semantic non-answer alike: with no embedding
// executor wired the semantic classifier cannot run at all, and the chat
// model still classifies the request.
func TestPreRequestHook_LLMFallbackCoversSemanticUnavailability(t *testing.T) {
	plugin := llmFallbackTestPlugin(t, llmFallbackAnalyzerTestConfig())

	plugin.SetChatRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		return llmClassifierChatResponse(`{"tier": "COMPLEX"}`), nil
	})

	req := llmComplexityChatRequest("prove the scheduler is deadlock-free")
	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(bfCtx, req))

	_, modelOut, _ := req.GetRequestFields()
	require.Equal(t, "gpt-4o-mini", modelOut)
	require.Equal(t, complexity.MechanismLLM, bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
}

// TestPreRequestHook_LLMFallbackCustomPromptReplacesGuidance pins the prompt
// split at the request boundary: the operator's prompt replaces the shipped
// guidance while the fixed reinforcement still closes the system message.
func TestPreRequestHook_LLMFallbackCustomPromptReplacesGuidance(t *testing.T) {
	analyzerConfig := llmFallbackAnalyzerTestConfig()
	analyzerConfig.LLM.Prompt = "Treat anything mentioning payroll as COMPLEX."
	plugin := llmFallbackTestPlugin(t, analyzerConfig)

	var executorMu sync.Mutex
	var gotSystem string
	plugin.SetChatRequestExecutor(func(_ *schemas.BifrostContext, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		executorMu.Lock()
		gotSystem = *req.Input[0].Content.ContentStr
		executorMu.Unlock()
		return llmClassifierChatResponse(`{"tier": "COMPLEX"}`), nil
	})

	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(bfCtx, llmComplexityChatRequest("run payroll")))

	executorMu.Lock()
	defer executorMu.Unlock()
	require.True(t, strings.HasPrefix(gotSystem, "Treat anything mentioning payroll as COMPLEX."))
	require.NotContains(t, gotSystem, "You are the request-complexity classifier")
	require.Contains(t, gotSystem, `{"tier": "SIMPLE"}`, "the fixed reinforcement must survive a custom prompt")
}

// TestPreRequestHook_LLMFallbackSkipsWhenUnavailable covers the fallback's own
// failure funnel: an unwired chat executor, an answer naming no tier, and a
// timeout all publish no tier, record "skipped", and name their cause.
func TestPreRequestHook_LLMFallbackSkipsWhenUnavailable(t *testing.T) {
	tests := []struct {
		name      string
		configure func(plugin *routing.RoutingPlugin)
		wantLog   string
	}{
		{
			name:      "executor not wired",
			configure: func(plugin *routing.RoutingPlugin) {},
			wantLog:   "LLM complexity classification unavailable",
		},
		{
			name: "answer names no tier",
			configure: func(plugin *routing.RoutingPlugin) {
				plugin.SetChatRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
					return llmClassifierChatResponse("this one is fairly hard, maybe medium-ish?"), nil
				})
			},
			wantLog: "answered without naming a tier",
		},
		{
			name: "classification times out",
			configure: func(plugin *routing.RoutingPlugin) {
				plugin.SetChatRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
					return nil, &schemas.BifrostError{Type: schemas.Ptr(schemas.RequestTimedOut)}
				})
			},
			wantLog: "timed out after 1s",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plugin := llmFallbackTestPlugin(t, llmFallbackAnalyzerTestConfig())
			tt.configure(plugin)

			req := llmComplexityChatRequest("prove the scheduler is deadlock-free")
			bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
			require.NoError(t, plugin.PreRequestHook(bfCtx, req))

			_, modelOut, _ := req.GetRequestFields()
			require.Equal(t, "gpt-4o", modelOut, "an unavailable fallback must not reroute the request")
			require.Nil(t, bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))
			require.Equal(t, complexity.MechanismSkipped, bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))

			var logMessages []string
			for _, entry := range bfCtx.GetRoutingEngineLogs() {
				logMessages = append(logMessages, entry.Message)
			}
			require.Contains(t, strings.Join(logMessages, "\n"), tt.wantLog)
		})
	}
}

// TestPreRequestHook_DormantLLMBlockNeverRuns pins that an llm block retained
// while the fallback selector says "none" never classifies: a semantic
// non-answer stays "skipped" exactly as it would with no llm block at all.
func TestPreRequestHook_DormantLLMBlockNeverRuns(t *testing.T) {
	analyzerConfig := llmFallbackAnalyzerTestConfig()
	analyzerConfig.Semantic.Fallback = configstore.ComplexitySemanticFallbackNone
	plugin := llmFallbackTestPlugin(t, analyzerConfig)

	chatCalled := false
	plugin.SetChatRequestExecutor(func(_ *schemas.BifrostContext, _ *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
		chatCalled = true
		return llmClassifierChatResponse(`{"tier": "COMPLEX"}`), nil
	})

	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(bfCtx, llmComplexityChatRequest("prove the scheduler is deadlock-free")))

	require.False(t, chatCalled, "a dormant llm block must not classify")
	require.Equal(t, complexity.MechanismSkipped, bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
}
