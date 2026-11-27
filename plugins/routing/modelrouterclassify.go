package routing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
)

// Classifying statements with a model router, outside any request.
//
// This answers "which tier would this router give this statement right now",
// so a router's configuration can be checked against a dataset before traffic
// depends on it. Each statement is wrapped in a chat request of user turns and
// goes through the same input extraction and the same classifier and fallback
// chain as live traffic. Two things are left out on purpose: session state,
// because a test must neither read nor move a live session's tier, and request
// capture, because test statements are not traffic and must not seed phrase
// suggestions.

const (
	// MaxModelRouterClassifyInputs bounds one call. A decision-model or LLM
	// router makes one model call per statement, so a dataset is sent in pages.
	MaxModelRouterClassifyInputs = 100
	// MaxModelRouterClassifyTextBytes bounds one statement, previous turns
	// included.
	MaxModelRouterClassifyTextBytes = 32 * 1024
	// modelRouterClassifyConcurrency is how many statements are classified at
	// once, so a page does not open a hundred embedding or model calls together.
	modelRouterClassifyConcurrency = 8
	// modelRouterClassifyTimeout is a backstop per statement; every classifier
	// already bounds its own call with its configured timeout.
	modelRouterClassifyTimeout = 60 * time.Second
)

// ErrInvalidModelRouterClassify reports a classify call that is empty, too
// large, or holds an empty statement.
var ErrInvalidModelRouterClassify = errors.New("invalid model router classify request")

// ModelRouterClassifyInput is one statement to classify: the latest user turn,
// and optionally the user turns before it, oldest first, for routers that read
// conversation history.
type ModelRouterClassifyInput struct {
	Text                 string
	PreviousUserMessages []string
}

// ModelRouterSemanticMiss is the nearest exemplar a semantic match rejected
// for falling below min_similarity.
type ModelRouterSemanticMiss struct {
	Tier            string  `json:"tier"`
	Score           float64 `json:"score"`
	MinSimilarity   float64 `json:"min_similarity"`
	MatchedExemplar string  `json:"matched_exemplar,omitempty"`
}

// ModelRouterClassification is what a router decided for one statement.
type ModelRouterClassification struct {
	Tier            string                          `json:"tier,omitempty"` // empty when no tier was published
	Mechanism       string                          `json:"mechanism"`      // semantic, decision, llm, or skipped
	Score           *float64                        `json:"score,omitempty"`
	Confidence      *float64                        `json:"confidence,omitempty"`
	MatchedExemplar string                          `json:"matched_exemplar,omitempty"`
	Model           string                          `json:"model,omitempty"`
	SemanticMiss    *ModelRouterSemanticMiss        `json:"semantic_miss,omitempty"`
	Trace           []schemas.RoutingEngineLogEntry `json:"trace"`
}

// ModelRouterClassifyResult is a classify call's answer, one classification
// per input in input order.
type ModelRouterClassifyResult struct {
	RouterID   string                      `json:"router_id"`
	RouterName string                      `json:"router_name"`
	Classifier string                      `json:"classifier"`
	Results    []ModelRouterClassification `json:"results"`
}

// ClassifyWithModelRouter classifies statements with one served router,
// without touching session state or request capture.
func (p *RoutingPlugin) ClassifyWithModelRouter(ctx context.Context, routerID string, inputs []ModelRouterClassifyInput) (ModelRouterClassifyResult, error) {
	rt, ok := p.modelRouter(routerID)
	if !ok {
		return ModelRouterClassifyResult{}, ErrUnknownComplexityRouter
	}
	if err := validateModelRouterClassifyInputs(inputs); err != nil {
		return ModelRouterClassifyResult{}, err
	}
	classifier := complexity.ClassifierSemantic
	if config := rt.config.Load(); config != nil && config.Classifier != "" {
		classifier = config.Classifier
	}
	results := make([]ModelRouterClassification, len(inputs))
	slots := make(chan struct{}, modelRouterClassifyConcurrency)
	var wg sync.WaitGroup
	for i := range inputs {
		slots <- struct{}{}
		wg.Go(func() {
			defer func() { <-slots }()
			results[i] = p.classifyModelRouterStatement(ctx, rt, inputs[i])
		})
	}
	wg.Wait()
	return ModelRouterClassifyResult{
		RouterID:   rt.id,
		RouterName: rt.displayName(),
		Classifier: classifier,
		Results:    results,
	}, nil
}

// validateModelRouterClassifyInputs checks the page size and every
// statement's size before anything is classified.
func validateModelRouterClassifyInputs(inputs []ModelRouterClassifyInput) error {
	if len(inputs) == 0 || len(inputs) > MaxModelRouterClassifyInputs {
		return fmt.Errorf("%w: send between 1 and %d inputs", ErrInvalidModelRouterClassify, MaxModelRouterClassifyInputs)
	}
	for i, input := range inputs {
		if strings.TrimSpace(input.Text) == "" {
			return fmt.Errorf("%w: input %d has no text", ErrInvalidModelRouterClassify, i)
		}
		size := len(input.Text)
		for _, previous := range input.PreviousUserMessages {
			size += len(previous)
		}
		if size > MaxModelRouterClassifyTextBytes {
			return fmt.Errorf("%w: input %d is larger than %d bytes", ErrInvalidModelRouterClassify, i, MaxModelRouterClassifyTextBytes)
		}
	}
	return nil
}

// classifyModelRouterStatement classifies one statement on a context of its
// own. The context carries an empty request ID, so request capture skips the
// embedding, and it is never given a session ID, so no session is read.
func (p *RoutingPlugin) classifyModelRouterStatement(parent context.Context, rt *modelRouterRuntime, statement ModelRouterClassifyInput) ModelRouterClassification {
	ctx := schemas.NewBifrostContext(parent, time.Now().Add(modelRouterClassifyTimeout))
	defer ctx.Cancel()
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "")
	ctx.SetValue(schemas.BifrostContextKeySessionID, "")

	input, disposition := complexity.BuildInputWithDisposition(ctx, modelRouterClassifyRequest(statement))
	if disposition != complexity.InputClassifiable {
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineRoutingRule, schemas.LogLevelInfo, noClassifiableComplexityInputLog)
		return ModelRouterClassification{Mechanism: complexity.MechanismSkipped, Trace: ctx.GetRoutingEngineLogs()}
	}
	proposal := p.classifyRouterInput(ctx, input, rt)
	if proposal.LogMessage != "" {
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineRoutingRule, proposal.LogLevel, proposal.LogMessage)
	}
	out := ModelRouterClassification{
		Mechanism:       proposal.Mechanism,
		Score:           proposal.Score,
		Confidence:      proposal.Confidence,
		MatchedExemplar: proposal.MatchedExemplar,
		Model:           proposal.Model,
		Trace:           ctx.GetRoutingEngineLogs(),
	}
	if proposal.Result != nil {
		out.Tier = proposal.Result.Tier
	}
	if miss := proposal.SemanticMiss; miss != nil {
		out.SemanticMiss = &ModelRouterSemanticMiss{
			Tier:            miss.Tier,
			Score:           miss.Score,
			MinSimilarity:   miss.MinSimilarity,
			MatchedExemplar: miss.MatchedExemplar,
		}
	}
	if out.Trace == nil {
		out.Trace = []schemas.RoutingEngineLogEntry{}
	}
	return out
}

// modelRouterClassifyRequest wraps a statement in a chat request of user
// turns, the shape input extraction reads for live chat traffic.
func modelRouterClassifyRequest(statement ModelRouterClassifyInput) *schemas.BifrostRequest {
	messages := make([]schemas.ChatMessage, 0, len(statement.PreviousUserMessages)+1)
	for _, text := range statement.PreviousUserMessages {
		messages = append(messages, schemas.ChatMessage{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr(text)}})
	}
	messages = append(messages, schemas.ChatMessage{Role: schemas.ChatMessageRoleUser, Content: &schemas.ChatMessageContent{ContentStr: schemas.Ptr(statement.Text)}})
	return &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Input: messages},
	}
}
