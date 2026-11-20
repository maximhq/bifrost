package routing

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/maximhq/bifrost/plugins/routing/rules"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// classifyTestVector puts each tier's exemplar on its own axis, so the nearest
// exemplar and its similarity are exact.
func classifyTestVector(text string) []float64 {
	switch {
	case strings.Contains(text, "casual greeting"):
		return []float64{1, 0, 0}
	case strings.Contains(text, "implementation detail"):
		return []float64{0, 1, 0}
	case strings.Contains(text, "architectural tradeoff"):
		return []float64{0, 0, 1}
	case strings.Contains(text, "refactor the service"):
		return []float64{0, 1, 0}
	case strings.Contains(text, "somewhere in between"):
		return []float64{0.5, 0.7, 0.5} // nearest MEDIUM at ~0.70
	default:
		return []float64{0.9, 0.1, 0} // nearest SIMPLE at ~0.99
	}
}

// classifyTestEmbeddings embeds every input text with classifyTestVector.
func classifyTestEmbeddings(_ *schemas.BifrostContext, req *schemas.BifrostEmbeddingRequest) (*schemas.BifrostEmbeddingResponse, *schemas.BifrostError) {
	var data []schemas.EmbeddingData
	for _, item := range req.Input {
		for _, part := range item.Content {
			if part.Text != nil {
				data = append(data, schemas.EmbeddingData{Index: len(data), Embedding: schemas.EmbeddingStruct{EmbeddingArray: classifyTestVector(*part.Text)}})
			}
		}
	}
	return &schemas.BifrostEmbeddingResponse{Data: data}, nil
}

// classifyTestRouterID is the router the classify tests' plugins serve on
// their own classifiers.
const classifyTestRouterID = "general"

// serveOwnClassifierAs serves the classifiers a test configured on the
// plugin's own fields as the model router id, so the classify tests reuse the
// decision and semantic test plugins instead of building a router each.
func serveOwnClassifierAs(p *RoutingPlugin, id string) {
	rt := p.complexityRuntime()
	rt.id = id
	routers := map[string]*modelRouterRuntime{id: rt}
	p.routers.Store(&routers)
}

// semanticClassifyTestPlugin serves a ready semantic router whose
// min_similarity rejects a statement between tiers.
func semanticClassifyTestPlugin(t *testing.T) *RoutingPlugin {
	t.Helper()
	return semanticClassifyTestPluginWithFallback(t, "", nil)
}

// semanticClassifyTestPluginWithFallback is semanticClassifyTestPlugin with a
// semantic fallback and the decision executor it calls.
func semanticClassifyTestPluginWithFallback(t *testing.T, fallback string, decide DecisionRequestExecutor) *RoutingPlugin {
	t.Helper()
	logger := rules.NewMockLogger()
	ruleStore, err := rules.NewLocalStore(context.Background(), logger, nil)
	require.NoError(t, err)
	p, err := InitFromStore(context.Background(), nil, logger, nil, ruleStore, NewMockGovernance())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, p.Cleanup()) })
	p.SetEmbeddingRequestExecutor(classifyTestEmbeddings)
	if decide != nil {
		p.SetDecisionRequestExecutor(decide)
	}
	require.NoError(t, p.ReloadComplexityAnalyzerConfig(&complexity.AnalyzerConfig{
		Keywords: configstore.ComplexityEditableKeywordConfig{
			SimpleKeywords:  []string{"a casual greeting"},
			MediumKeywords:  []string{"an implementation detail question"},
			ComplexKeywords: []string{"a deep architectural tradeoff analysis"},
		},
		Semantic: &configstore.ComplexitySemanticConfig{
			Provider:       "openai",
			EmbeddingModel: "test-embedding-model",
			MinSimilarity:  0.9,
			Fallback:       fallback,
		},
	}))
	require.Eventually(t, func() bool {
		return p.ComplexitySemanticStatus().State == complexity.SemanticStatusReady
	}, 5*time.Second, 10*time.Millisecond, "semantic warmup should become ready")
	serveOwnClassifierAs(p, classifyTestRouterID)
	return p
}

// TestClassifyWithModelRouterSemantic classifies a page against a semantic
// router: answers come back in input order with the matched exemplar and
// score, and a statement below min_similarity gets no tier but names the
// nearest exemplar it missed.
func TestClassifyWithModelRouterSemantic(t *testing.T) {
	p := semanticClassifyTestPlugin(t)

	result, err := p.ClassifyWithModelRouter(t.Context(), classifyTestRouterID, []ModelRouterClassifyInput{
		{Text: "hey, quick question"},
		{Text: "refactor the service layer"},
		{Text: "somewhere in between"},
	})
	require.NoError(t, err)
	require.Equal(t, classifyTestRouterID, result.RouterID)
	require.Equal(t, complexity.ClassifierSemantic, result.Classifier)
	require.Len(t, result.Results, 3)

	simple := result.Results[0]
	require.Equal(t, complexity.TierSimple, simple.Tier)
	require.Equal(t, complexity.MechanismSemantic, simple.Mechanism)
	require.Equal(t, "a casual greeting", simple.MatchedExemplar)
	require.NotNil(t, simple.Score)
	require.Greater(t, *simple.Score, 0.9)
	require.Nil(t, simple.SemanticMiss)
	require.NotEmpty(t, simple.Trace, "the classifier's routing log is returned")

	require.Equal(t, complexity.TierMedium, result.Results[1].Tier)
	require.Equal(t, "an implementation detail question", result.Results[1].MatchedExemplar)

	miss := result.Results[2]
	require.Empty(t, miss.Tier, "a match below min_similarity publishes no tier")
	require.Equal(t, complexity.MechanismSkipped, miss.Mechanism)
	require.NotNil(t, miss.SemanticMiss, "the rejected nearest exemplar is reported")
	require.Equal(t, complexity.TierMedium, miss.SemanticMiss.Tier)
	require.Equal(t, "an implementation detail question", miss.SemanticMiss.MatchedExemplar)
	require.InDelta(t, 0.9, miss.SemanticMiss.MinSimilarity, 1e-9)
	require.Less(t, miss.SemanticMiss.Score, 0.9)
}

// TestClassifyWithModelRouterFallbackKeepsSemanticMiss classifies a statement
// whose nearest exemplar is below min_similarity on a router that falls back
// to the decision model: the answer carries the fallback's tier and, beside
// it, the exemplar semantic rejected, with its score and the threshold it
// missed, so the test shows why the fallback was asked.
func TestClassifyWithModelRouterFallbackKeepsSemanticMiss(t *testing.T) {
	var calls atomic.Int32
	p := semanticClassifyTestPluginWithFallback(t, configstore.ComplexitySemanticFallbackDecision,
		func(*schemas.BifrostContext, *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
			calls.Add(1)
			return decisionChoiceResponse("COMPLEX"), nil
		})

	result, err := p.ClassifyWithModelRouter(t.Context(), classifyTestRouterID, []ModelRouterClassifyInput{
		{Text: "hey, quick question"},
		{Text: "somewhere in between"},
	})
	require.NoError(t, err)
	require.Len(t, result.Results, 2)
	require.Equal(t, complexity.MechanismSemantic, result.Results[0].Mechanism, "a confident semantic match never falls back")
	require.Nil(t, result.Results[0].SemanticMiss)

	got := result.Results[1]
	require.Equal(t, complexity.TierComplex, got.Tier, "the fallback's tier is published")
	require.Equal(t, complexity.MechanismDecision, got.Mechanism)
	require.NotNil(t, got.SemanticMiss, "the exemplar semantic rejected is kept beside the fallback's answer")
	require.Equal(t, complexity.TierMedium, got.SemanticMiss.Tier)
	require.Equal(t, "an implementation detail question", got.SemanticMiss.MatchedExemplar)
	require.InDelta(t, 0.9, got.SemanticMiss.MinSimilarity, 1e-9)
	require.Less(t, got.SemanticMiss.Score, 0.9)
	require.Greater(t, got.SemanticMiss.Score, 0.0)
	require.EqualValues(t, 1, calls.Load(), "only the rejected statement reaches the decision model")
}

// TestClassifyWithModelRouterDecisionIsolatedFromLiveState runs a decision
// router: previous user turns reach the model, and the statement is
// classified on a context with no request or session ID even when the
// caller's context has both, so it can neither be captured for phrase
// suggestions nor move a live session's tier.
func TestClassifyWithModelRouterDecisionIsolatedFromLiveState(t *testing.T) {
	count := 3
	var mu sync.Mutex
	var seen [][]complexity.ConversationMessage
	p := decisionTestPlugin(&complexity.DecisionConfig{PreviousMessageCount: &count, Timeout: time.Second},
		func(ctx *schemas.BifrostContext, req *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
			requestID, _ := ctx.Value(schemas.BifrostContextKeyRequestID).(string)
			sessionID, _ := ctx.Value(schemas.BifrostContextKeySessionID).(string)
			// The classifier calls this on its own goroutines, where FailNow
			// must not run: report and carry on.
			assert.Empty(t, requestID, "a test statement must carry no request ID, so capture skips it")
			assert.Empty(t, sessionID, "a test statement must carry no session ID")
			mu.Lock()
			seen = append(seen, conversationOf(req.Input))
			mu.Unlock()
			return decisionChoiceResponse("COMPLEX"), nil
		})
	serveOwnClassifierAs(p, classifyTestRouterID)
	parent := schemas.NewBifrostContext(t.Context(), schemas.NoDeadline)
	parent.SetValue(schemas.BifrostContextKeyRequestID, "live-request")
	parent.SetValue(schemas.BifrostContextKeySessionID, "live-session")

	result, err := p.ClassifyWithModelRouter(parent, classifyTestRouterID, []ModelRouterClassifyInput{
		{Text: "now design the sharding plan", PreviousUserMessages: []string{"we run postgres", "writes are 50k/s"}},
	})
	require.NoError(t, err)
	require.Equal(t, complexity.ClassifierDecision, result.Classifier)
	require.Len(t, result.Results, 1)
	got := result.Results[0]
	require.Equal(t, complexity.TierComplex, got.Tier)
	require.Equal(t, complexity.MechanismDecision, got.Mechanism)
	require.Equal(t, string(configstore.DefaultComplexityDecisionProvider)+"/"+configstore.DefaultComplexityDecisionModel, got.Model)
	require.Equal(t, [][]complexity.ConversationMessage{{
		{Role: "user", Content: "we run postgres"},
		{Role: "user", Content: "writes are 50k/s"},
		{Role: "user", Content: "now design the sharding plan"},
	}}, seen)
}

// TestClassifyWithModelRouterKeepsInputOrder classifies more statements than
// run at once and gets each answer back at its own index.
func TestClassifyWithModelRouterKeepsInputOrder(t *testing.T) {
	tiers := []string{complexity.TierSimple, complexity.TierMedium, complexity.TierComplex}
	p := decisionTestPlugin(nil, func(_ *schemas.BifrostContext, req *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
		text := *req.Input.Messages[len(req.Input.Messages)-1].Content.Text
		return decisionChoiceResponse(tiers[len(text)%3]), nil
	})
	serveOwnClassifierAs(p, classifyTestRouterID)
	inputs := make([]ModelRouterClassifyInput, 3*modelRouterClassifyConcurrency)
	for i := range inputs {
		inputs[i] = ModelRouterClassifyInput{Text: strings.Repeat("x", i+1)}
	}
	result, err := p.ClassifyWithModelRouter(t.Context(), classifyTestRouterID, inputs)
	require.NoError(t, err)
	require.Len(t, result.Results, len(inputs))
	for i, got := range result.Results {
		require.Equal(t, tiers[(i+1)%3], got.Tier, "input %d", i)
	}
}

// TestClassifyWithModelRouterRejectsBadRequests refuses an unknown router and
// pages that are empty, too large, or hold an empty or oversized statement,
// before any classifier is called.
func TestClassifyWithModelRouterRejectsBadRequests(t *testing.T) {
	p := decisionTestPlugin(nil, func(*schemas.BifrostContext, *schemas.BifrostDecisionRequest) (*schemas.BifrostDecisionResponse, *schemas.BifrostError) {
		t.Error("no classifier may run for a rejected request")
		return nil, &schemas.BifrostError{Error: &schemas.ErrorField{Message: "unexpected classify call"}}
	})
	serveOwnClassifierAs(p, classifyTestRouterID)
	_, err := p.ClassifyWithModelRouter(t.Context(), "no-such-router", []ModelRouterClassifyInput{{Text: "hi"}})
	require.ErrorIs(t, err, ErrUnknownComplexityRouter)

	tooMany := make([]ModelRouterClassifyInput, MaxModelRouterClassifyInputs+1)
	for i := range tooMany {
		tooMany[i].Text = "hi"
	}
	for name, inputs := range map[string][]ModelRouterClassifyInput{
		"empty page":     nil,
		"too many":       tooMany,
		"blank text":     {{Text: "hi"}, {Text: "  \n"}},
		"oversized text": {{Text: "hi", PreviousUserMessages: []string{strings.Repeat("x", MaxModelRouterClassifyTextBytes)}}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := p.ClassifyWithModelRouter(t.Context(), classifyTestRouterID, inputs)
			require.ErrorIs(t, err, ErrInvalidModelRouterClassify)
		})
	}
}
