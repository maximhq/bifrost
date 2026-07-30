package governance

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
	"github.com/maximhq/bifrost/plugins/governance/complexity"
)

func TestPreRequestHook_ComplexityAnalyzerFeedsCELVariable(t *testing.T) {
	logger := NewMockLogger()
	provider := "openai"
	model := "gpt-4o-mini"

	plugin, err := Init(
		context.Background(),
		&Config{IsVkMandatory: boolPtr(false)},
		logger,
		nil,
		&configstore.GovernanceConfig{
			RoutingRules: []configstoreTables.TableRoutingRule{
				{
					ID:            "rule-1",
					Name:          "Complexity Available",
					CelExpression: `complexity_tier != ""`,
					Targets: []configstoreTables.TableRoutingTarget{
						{Provider: &provider, Model: &model, Weight: 1.0},
					},
					Enabled:  schemas.Ptr(true),
					Scope:    "global",
					Priority: 0,
				},
			},
		},
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, plugin.Cleanup())
	}()

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
			Input: []schemas.ChatMessage{
				{
					Role:    schemas.ChatMessageRoleUser,
					Content: complexityChatString("What is a vector database?"),
				},
			},
		},
	}

	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(bfCtx, req))

	engines, ok := bfCtx.Value(schemas.BifrostContextKeyRoutingEnginesUsed).([]string)
	require.True(t, ok, "routing engines used should be tracked")
	require.Contains(t, engines, schemas.RoutingEngineRoutingRule)

	providerOut, modelOut, _ := req.GetRequestFields()
	require.Equal(t, schemas.OpenAI, providerOut)
	require.Equal(t, "gpt-4o-mini", modelOut)

	tier, ok := bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityTier).(string)
	require.True(t, ok, "complexity tier should be recorded in context")
	require.Contains(t, []string{complexity.TierSimple, complexity.TierMedium, complexity.TierComplex}, tier)
	mechanism, ok := bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism).(string)
	require.True(t, ok, "routing mechanism should be recorded in context")
	require.Equal(t, complexity.MechanismLexical, mechanism)
	_, ok = bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityScore).(float64)
	require.True(t, ok, "complexity score should be recorded in context")
}

// TestPreRequestHook_SemanticComplexityUsesLastMessageAndFallsBack verifies
// that semantic routing reaches a Governance rule, embeds only the latest user
// turn, and returns to the shared lexical lists when semantic is unavailable.
func TestPreRequestHook_SemanticComplexityUsesLastMessageAndFallsBack(t *testing.T) {
	logger := NewMockLogger()
	provider := "openai"
	routeModel := "gpt-4o-mini"
	semanticConfig := complexity.AnalyzerConfig{
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
		},
	}

	plugin, err := Init(
		context.Background(),
		&Config{IsVkMandatory: boolPtr(false)},
		logger,
		nil,
		&configstore.GovernanceConfig{
			ComplexityAnalyzerConfig: &semanticConfig,
			RoutingRules: []configstoreTables.TableRoutingRule{
				{
					ID:            "semantic-simple-rule",
					Name:          "Semantic simple route",
					CelExpression: `complexity_tier == "SIMPLE"`,
					Targets: []configstoreTables.TableRoutingTarget{
						{Provider: &provider, Model: &routeModel, Weight: 1.0},
					},
					Enabled:  schemas.Ptr(true),
					Scope:    "global",
					Priority: 0,
				},
			},
		},
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, plugin.Cleanup())
	}()

	var embeddedMu sync.Mutex
	var embeddedTexts []string
	plugin.SetEmbeddingRequestExecutor(func(_ *schemas.BifrostContext, req *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
		if req == nil || req.Input == nil {
			return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "semantic embedding request did not contain text"}}
		}
		var texts []string
		if req.Input.Text != nil {
			texts = []string{*req.Input.Text}
		} else {
			texts = req.Input.Texts
		}
		if len(texts) == 0 {
			return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "semantic embedding request did not contain text"}}
		}
		embeddedMu.Lock()
		embeddedTexts = append(embeddedTexts, texts...)
		embeddedMu.Unlock()

		data := make([]schemas.EmbeddingData, len(texts))
		for index, text := range texts {
			vector := []float64{0.5, 0.5}
			switch text {
			case "papaya amber":
				vector = []float64{1, 0}
			case "cedar cobalt":
				vector = []float64{0, 1}
			case "obsidian comet":
				vector = []float64{-1, 0}
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

	embeddedMu.Lock()
	embeddedTexts = nil
	embeddedMu.Unlock()
	semanticRequest := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
			Input: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleUser, Content: complexityChatString("obsidian comet")},
				{Role: schemas.ChatMessageRoleUser, Content: complexityChatString("papaya amber")},
			},
		},
	}
	semanticCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(semanticCtx, semanticRequest))

	providerOut, modelOut, _ := semanticRequest.GetRequestFields()
	require.Equal(t, schemas.OpenAI, providerOut)
	require.Equal(t, routeModel, modelOut)
	require.Equal(t, complexity.TierSimple, semanticCtx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))
	require.Equal(t, complexity.MechanismSemantic, semanticCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
	embeddedMu.Lock()
	gotEmbeddedTexts := append([]string(nil), embeddedTexts...)
	embeddedMu.Unlock()
	require.Equal(t, []string{"papaya amber"}, gotEmbeddedTexts)

	// The routing log has to name the exemplar behind the tier: SIMPLE alone
	// cannot tell a reader whether the request resembled the phrase the operator
	// configured or merely won an argmax over unrelated ones.
	var semanticLogMessages []string
	for _, entry := range semanticCtx.GetRoutingEngineLogs() {
		semanticLogMessages = append(semanticLogMessages, entry.Message)
	}
	require.Contains(t, strings.Join(semanticLogMessages, "\n"), `matched="papaya amber"`)

	plugin.SetEmbeddingRequestExecutor(nil)
	lexicalRequest := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
			Input: []schemas.ChatMessage{
				{Role: schemas.ChatMessageRoleUser, Content: complexityChatString("papaya amber")},
			},
		},
	}
	lexicalCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(lexicalCtx, lexicalRequest))

	providerOut, modelOut, _ = lexicalRequest.GetRequestFields()
	require.Equal(t, schemas.OpenAI, providerOut)
	require.Equal(t, routeModel, modelOut)
	require.Equal(t, complexity.TierSimple, lexicalCtx.Value(schemas.BifrostContextKeyGovernanceComplexityTier))
	require.Equal(t, complexity.MechanismLexical, lexicalCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism))
}

func TestPreRequestHook_ComplexitySkippedWhenNoRulesReferenceIt(t *testing.T) {
	logger := NewMockLogger()
	provider := "openai"
	model := "gpt-4o-mini"

	plugin, err := Init(
		context.Background(),
		&Config{IsVkMandatory: boolPtr(false)},
		logger,
		nil,
		&configstore.GovernanceConfig{
			RoutingRules: []configstoreTables.TableRoutingRule{
				{
					ID:            "rule-1",
					Name:          "Always match",
					CelExpression: "true",
					Targets: []configstoreTables.TableRoutingTarget{
						{Provider: &provider, Model: &model, Weight: 1.0},
					},
					Enabled:  schemas.Ptr(true),
					Scope:    "global",
					Priority: 0,
				},
			},
		},
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, plugin.Cleanup())
	}()

	req := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
			Input: []schemas.ChatMessage{
				{
					Role:    schemas.ChatMessageRoleUser,
					Content: complexityChatString("Hello"),
				},
			},
		},
	}

	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(bfCtx, req))

	logs := bfCtx.GetRoutingEngineLogs()
	for _, entry := range logs {
		if entry.Engine == schemas.RoutingEngineRoutingRule && strings.Contains(entry.Message, "Complexity") {
			t.Fatalf("expected no complexity logs when no rules reference complexity_tier, got: %s", entry.Message)
		}
	}

	require.Nil(t, bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityTier), "no tier should be recorded when complexity is never demanded")
	require.Nil(t, bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism), "no mechanism should be recorded when complexity is never demanded")
	require.Nil(t, bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityScore), "no score should be recorded when complexity is never demanded")
}

func TestPreRequestHook_ComplexityUnsupportedInputRecordsSkippedMechanism(t *testing.T) {
	logger := NewMockLogger()
	provider := "openai"
	model := "gpt-4o-mini"

	plugin, err := Init(
		context.Background(),
		&Config{IsVkMandatory: boolPtr(false)},
		logger,
		nil,
		&configstore.GovernanceConfig{
			RoutingRules: []configstoreTables.TableRoutingRule{
				{
					ID:            "rule-1",
					Name:          "Complexity Available",
					CelExpression: `complexity_tier != ""`,
					Targets: []configstoreTables.TableRoutingTarget{
						{Provider: &provider, Model: &model, Weight: 1.0},
					},
					Enabled:  schemas.Ptr(true),
					Scope:    "global",
					Priority: 0,
				},
			},
		},
		nil,
		nil,
		nil,
	)
	require.NoError(t, err)
	defer func() {
		require.NoError(t, plugin.Cleanup())
	}()

	// Embedding requests carry no text-bearing input the analyzer supports, so
	// classification is demanded (the rule references complexity_tier) but skipped.
	req := &schemas.BifrostRequest{
		RequestType: schemas.EmbeddingRequest,
		EmbeddingRequest: &schemas.BifrostEmbeddingRequest{
			Provider: schemas.OpenAI,
			Model:    "text-embedding-3-small",
		},
	}

	bfCtx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	require.NoError(t, plugin.PreRequestHook(bfCtx, req))

	mechanism, ok := bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityMechanism).(string)
	require.True(t, ok, "mechanism should be recorded when complexity is demanded but skipped")
	require.Equal(t, complexity.MechanismSkipped, mechanism)
	require.Nil(t, bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityTier), "no tier should be recorded on the skip path")
	require.Nil(t, bfCtx.Value(schemas.BifrostContextKeyGovernanceComplexityScore), "no score should be recorded on the skip path")
}
