package routing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testJevCtx builds a real BifrostContext: Classify bounds every decision
// call with context.WithTimeout, which panics on a nil parent.
func testJevCtx() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), time.Now())
}

func testJevFallbackConfig(fallback string) *complexity.AnalyzerConfig {
	cfg := complexity.DefaultAnalyzerConfig()
	cfg.Semantic = &complexity.SemanticConfig{
		Provider:       "openai",
		EmbeddingModel: "text-embedding-3-small",
		Fallback:       fallback,
	}
	cfg.Jev = &complexity.JevConfig{}
	return &cfg
}

func testJevPlugin(fn complexity.SystemOneFunc, fallback string) *RoutingPlugin {
	plugin := &RoutingPlugin{jevClassifier: complexity.NewJevClassifier(nil)}
	plugin.jevClassifier.Configure(testJevFallbackConfig(fallback))
	plugin.jevClassifier.SetSystemOneFunc(fn)
	return plugin
}

func simpleConfidentSystemOne() complexity.SystemOneFunc {
	return func(_ context.Context, _ *complexity.JevConfig, _ *complexity.SystemOneRequest) (*complexity.SystemOneResponse, error) {
		score := 0.1
		return &complexity.SystemOneResponse{Answers: map[string]complexity.SystemOneAnswer{
			"tier":       {Type: complexity.SystemOneQuestionChoice, Choice: "SIMPLE", Confidence: 0.95},
			"complexity": {Type: complexity.SystemOneQuestionScore, Score: &score, Confidence: 0.9},
		}}, nil
	}
}

func TestClassifyJevComplexityPublishesConfidentTier(t *testing.T) {
	plugin := testJevPlugin(simpleConfidentSystemOne(), "jev")

	proposal := plugin.classifyJevComplexity(testJevCtx(), complexity.ComplexityInput{LastUserText: "what is 2+2?"})
	require.NotNil(t, proposal.Result)
	assert.Equal(t, "SIMPLE", proposal.Result.Tier)
	assert.Equal(t, complexity.MechanismJev, proposal.Mechanism)
}

func TestClassifyJevComplexityGateWithheldIsRoutineSkip(t *testing.T) {
	// The decision model answered but was not confident: no tier published,
	// recorded as skipped at Info (a routine outcome, not an operator alarm).
	plugin := testJevPlugin(func(_ context.Context, _ *complexity.JevConfig, _ *complexity.SystemOneRequest) (*complexity.SystemOneResponse, error) {
		score := 0.9
		return &complexity.SystemOneResponse{Answers: map[string]complexity.SystemOneAnswer{
			"tier":       {Type: complexity.SystemOneQuestionChoice, Choice: "SIMPLE", Confidence: 0.95},
			"complexity": {Type: complexity.SystemOneQuestionScore, Score: &score, Confidence: 0.9},
		}}, nil
	}, "jev")

	proposal := plugin.classifyJevComplexity(testJevCtx(), complexity.ComplexityInput{LastUserText: "redesign the billing system"})
	assert.Nil(t, proposal.Result)
	assert.Equal(t, complexity.MechanismSkipped, proposal.Mechanism)
	assert.Contains(t, proposal.LogMessage, complexity.JevReasonHighComplexity)
}

func TestClassifyJevComplexityTransportErrorFailsOpen(t *testing.T) {
	// The System One endpoint being down must never fail the request: the
	// proposal is a skip with a cause-named warn line, and no error escapes.
	plugin := testJevPlugin(func(_ context.Context, _ *complexity.JevConfig, _ *complexity.SystemOneRequest) (*complexity.SystemOneResponse, error) {
		return nil, errors.New("connection refused")
	}, "jev")

	proposal := plugin.classifyJevComplexity(testJevCtx(), complexity.ComplexityInput{LastUserText: "hello"})
	assert.Nil(t, proposal.Result)
	assert.Equal(t, complexity.MechanismSkipped, proposal.Mechanism)
	assert.Contains(t, proposal.LogMessage, "connection refused")
}

func TestClassifyJevComplexityMissingAPIKeyFailsOpen(t *testing.T) {
	plugin := testJevPlugin(complexity.HTTPSystemOneFunc, "jev")
	t.Setenv("TYPESAFE_API_KEY", "")

	proposal := plugin.classifyJevComplexity(testJevCtx(), complexity.ComplexityInput{LastUserText: "hello"})
	assert.Nil(t, proposal.Result)
	assert.Equal(t, complexity.MechanismSkipped, proposal.Mechanism)
	assert.Contains(t, proposal.LogMessage, "api_key")
}

func TestComputeJevNilForNonClassifiableAndErrors(t *testing.T) {
	// No classifier at all: nil request is not classifiable, so the plugin
	// must return nil without touching the classifier.
	plugin := &RoutingPlugin{}
	assert.Nil(t, plugin.computeJev(testJevCtx(), nil))

	// Classify error folds into nil — the engine renders it as CEL unknowns
	// so jev predicates never match and the request proceeds.
	failing := &RoutingPlugin{jevClassifier: complexity.NewJevClassifier(nil)}
	failing.jevClassifier.Configure(testJevFallbackConfig("jev"))
	failing.jevClassifier.SetSystemOneFunc(func(_ context.Context, _ *complexity.JevConfig, _ *complexity.SystemOneRequest) (*complexity.SystemOneResponse, error) {
		return nil, errors.New("boom")
	})
	req := classifiableChatRequest("Explain vector clocks")
	assert.Nil(t, failing.computeJev(testJevCtx(), req))

	// A confident verdict flows through unchanged.
	succeeding := &RoutingPlugin{jevClassifier: complexity.NewJevClassifier(nil)}
	succeeding.jevClassifier.Configure(testJevFallbackConfig("jev"))
	succeeding.jevClassifier.SetSystemOneFunc(simpleConfidentSystemOne())
	result := succeeding.computeJev(testJevCtx(), req)
	require.NotNil(t, result)
	assert.Equal(t, "SIMPLE", result.Tier)
}

// classifiableChatRequest builds a chat completion carrying a plain user turn,
// the minimum shape BuildInputWithDisposition classifies.
func classifiableChatRequest(userText string) *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Input: []schemas.ChatMessage{
				{
					Role:    schemas.ChatMessageRoleUser,
					Content: &schemas.ChatMessageContent{ContentStr: &userText},
				},
			},
		},
	}
}

func TestSetSystemOneFuncSwapsTransport(t *testing.T) {
	plugin := testJevPlugin(simpleConfidentSystemOne(), "jev")

	// Swap in a transport that answers COMPLEX and observe the new verdict.
	plugin.SetSystemOneFunc(func(_ context.Context, _ *complexity.JevConfig, _ *complexity.SystemOneRequest) (*complexity.SystemOneResponse, error) {
		score := 0.1
		return &complexity.SystemOneResponse{Answers: map[string]complexity.SystemOneAnswer{
			"tier":       {Type: complexity.SystemOneQuestionChoice, Choice: "COMPLEX", Confidence: 0.95},
			"complexity": {Type: complexity.SystemOneQuestionScore, Score: &score, Confidence: 0.9},
		}}, nil
	})

	proposal := plugin.classifyJevComplexity(testJevCtx(), complexity.ComplexityInput{LastUserText: "hello"})
	require.NotNil(t, proposal.Result)
	assert.Equal(t, "COMPLEX", proposal.Result.Tier)
}
