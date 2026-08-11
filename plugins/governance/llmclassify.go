package governance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/plugins/governance/complexity"
)

// ErrChatRequestExecutorNotConfigured means the HTTP server has not finished
// wiring the governance plugin to Bifrost's chat completion path. Mirrors
// ErrEmbeddingRequestExecutorNotConfigured for the llm classifier.
var ErrChatRequestExecutorNotConfigured = errors.New("chat request executor is not configured")

// ErrLLMClassificationTimeout reports that an llm classification exhausted its
// configured budget instead of failing for a provider or configuration
// reason. The distinction matters for the same reason ErrEmbeddingTimeout
// exists: a timeout says llm routing works but is too slow for its budget,
// every other failure says it is not working at all.
var ErrLLMClassificationTimeout = errors.New("llm classification request timed out")

// ChatRequestExecutor invokes the chat completion endpoint on the bifrost
// client. The plugin calls it to ask the classifier model for a tier. It
// mirrors the signature of bifrost.Client.ChatCompletionRequest.
type ChatRequestExecutor func(ctx *schemas.BifrostContext, req *schemas.BifrostChatRequest) (*schemas.BifrostChatResponse, *schemas.BifrostError)

// ChatExecutorSetter is implemented by governance plugins that accept a chat
// request executor, wired by the HTTP server after the bifrost client is
// constructed exactly like EmbeddingExecutorSetter. Wrappers that embed
// *GovernancePlugin satisfy this via method promotion.
type ChatExecutorSetter interface {
	SetChatRequestExecutor(ChatRequestExecutor)
}

// llmClassifierMaxCompletionTokens bounds the classification answer. The
// contract answer is a dozen tokens of JSON; the headroom exists for models
// that spend reasoning tokens inside the same budget before answering.
const llmClassifierMaxCompletionTokens = 512

// SetChatRequestExecutor wires up the function used to call the classifier
// chat model. Without it, llm complexity classification publishes no tier.
// Safe for concurrent use with classification and plugin reloads.
func (p *GovernancePlugin) SetChatRequestExecutor(executor ChatRequestExecutor) {
	if executor == nil {
		p.chatRequestExecutor.Store(nil)
		if p.llmClassifier != nil {
			p.llmClassifier.SetChatFunc(nil)
		}
		return
	}
	p.chatRequestExecutor.Store(&executor)
	if p.llmClassifier != nil {
		p.llmClassifier.SetChatFunc(p.classifyComplexityTextViaLLM)
	}
}

// chatExecutor returns the currently wired executor, or nil.
func (p *GovernancePlugin) chatExecutor() ChatRequestExecutor {
	if ptr := p.chatRequestExecutor.Load(); ptr != nil {
		return *ptr
	}
	return nil
}

// requestLLMClassificationTimeout is the configured hot-path budget for one
// classification completion, which a live request is waiting on.
func requestLLMClassificationTimeout(llm *complexity.LLMConfig) time.Duration {
	if llm != nil && llm.Timeout > 0 {
		return llm.Timeout
	}
	return configstore.DefaultComplexityLLMTimeout
}

// classifyComplexityTextViaLLM adapts Governance's Bifrost-aware chat path to
// the classifier's context-based dependency, mirroring embedComplexityText.
// Unlike embedding, there is no warmup caller: every invocation is a live
// request blocked on the answer, so the configured hot-path budget always
// applies.
func (p *GovernancePlugin) classifyComplexityTextViaLLM(ctx context.Context, llm *complexity.LLMConfig, systemPrompt, userText string) (string, error) {
	executor := p.chatExecutor()
	if executor == nil {
		return "", ErrChatRequestExecutorNotConfigured
	}
	if llm == nil || llm.Provider == "" || llm.Model == "" {
		return "", fmt.Errorf("llm classification is not configured")
	}

	timeout := requestLLMClassificationTimeout(llm)

	chatCtx := schemas.NewBifrostContext(ctx, time.Now().Add(timeout))
	// Cancel the derived context once we're done. NewBifrostContext starts a
	// watchCancellation goroutine that holds a reference to ctx (the scoped
	// plugin context). Without this, that goroutine outlives the plugin call
	// and may dereference fields on a parent context that has already been
	// released back to its sync.Pool — see core/schemas.ReleasePluginScope.
	defer chatCtx.Cancel()
	// The classification request targets the configured classifier
	// provider/model, not the caller's. Mark it as an internal sub-request: it
	// skips the plugin pipeline (so it cannot recurse back through governance)
	// and sheds the caller's key-routing and body-transport state so it
	// behaves like a fresh external /v1/chat/completions call.
	bifrost.PrepareContextForInternalRequest(chatCtx)

	temperature := 0.0
	maxCompletionTokens := llmClassifierMaxCompletionTokens
	req := &schemas.BifrostChatRequest{
		Provider: llm.Provider,
		Model:    llm.Model,
		Input: []schemas.ChatMessage{
			{
				Role:    schemas.ChatMessageRoleSystem,
				Content: &schemas.ChatMessageContent{ContentStr: &systemPrompt},
			},
			{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: &userText},
			},
		},
		Params: &schemas.ChatParameters{
			Temperature:         &temperature,
			MaxCompletionTokens: &maxCompletionTokens,
		},
	}

	response, bifrostErr := executor(chatCtx, req)
	if bifrostErr != nil {
		// Same tagging rationale as the embedding path: the executor reports
		// every failure as a *BifrostError, and only this frame knows which
		// deadline was set and why.
		if isEmbeddingTimeout(chatCtx, bifrostErr) {
			return "", fmt.Errorf("%w after %s", ErrLLMClassificationTimeout, timeout)
		}
		return "", fmt.Errorf("failed to run llm classification: %v", bifrostErr)
	}
	if response == nil {
		return "", fmt.Errorf("no response returned from llm classifier provider")
	}

	// A response that arrived was billed, whatever its shape: record usage
	// before validating the answer, matching the embedding path's rule.
	inputTokens, outputTokens := 0, 0
	if response.Usage != nil {
		// Provider-reported usage is untrusted input: negative counts would
		// flow into the RoutingDebug stamp and subtract from billed cost.
		if response.Usage.PromptTokens > 0 {
			inputTokens = response.Usage.PromptTokens
		}
		if response.Usage.CompletionTokens > 0 {
			outputTokens = response.Usage.CompletionTokens
		}
	}
	recordRoutingLLMUsage(ctx, llm, inputTokens, outputTokens)

	text := chatResponseText(response)
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("llm classifier response contained no text")
	}
	return text, nil
}

// computeLLMComplexity runs the llm fallback classifier for one request —
// always after a semantic non-answer, never as the primary — and publishes
// its outcome, following the semantic branch's logging discipline: every
// failure funnels to one MechanismSkipped and one routing-engine log line
// naming the cause.
func (p *GovernancePlugin) computeLLMComplexity(ctx *schemas.BifrostContext, input complexity.ComplexityInput) *complexity.ComplexityResult {
	result, err := p.llmClassifier.Classify(ctx, input)
	if err == nil && result != nil {
		// No score is published: a chat completion has no similarity, and a
		// synthetic one would invite comparisons against thresholds tuned for
		// the vector backends.
		out := &complexity.ComplexityResult{Tier: result.Tier}
		ctx.SetValue(schemas.BifrostContextKeyGovernanceComplexityTier, out.Tier)
		ctx.SetValue(schemas.BifrostContextKeyGovernanceComplexityMechanism, complexity.MechanismLLM)
		ctx.AppendRoutingEngineLog(
			schemas.RoutingEngineRoutingRule,
			schemas.LogLevelInfo,
			fmt.Sprintf("LLM complexity: tier=%s", out.Tier),
		)
		return out
	}

	if err != nil && p.logger != nil {
		p.logger.Debug("[Governance] LLM complexity classification unavailable: %v", err)
	}
	ctx.SetValue(schemas.BifrostContextKeyGovernanceComplexityMechanism, complexity.MechanismSkipped)
	// One line per decision, naming the cause. Every branch is an operator
	// problem — a budget to raise, a model that ignores the response contract,
	// or wiring that has not finished — so unlike the semantic branch there is
	// no routine Info case.
	unavailableLog := "LLM complexity classification unavailable, so no complexity tier is published"
	switch {
	case errors.Is(err, ErrLLMClassificationTimeout):
		// An exhausted budget is a tuning problem with an obvious remedy;
		// naming it as merely "unavailable" sends the operator hunting for a
		// broken provider instead of raising llm.timeout.
		unavailableLog = fmt.Sprintf(
			"LLM complexity classification timed out after %s, so no complexity tier is published",
			p.llmClassifier.Timeout(),
		)
	case errors.Is(err, complexity.ErrLLMTierUnparseable):
		// The model answered but named no tier: the classifier model or the
		// appended instructions are the thing to fix, not connectivity.
		unavailableLog = "LLM complexity classifier answered without naming a tier, so no complexity tier is published"
	}
	ctx.AppendRoutingEngineLog(schemas.RoutingEngineRoutingRule, schemas.LogLevelWarn, unavailableLog)
	return nil
}

// chatResponseText extracts the assistant text from the first choice of a
// non-stream chat response. Classification never streams, so a stream-shaped
// choice yields nothing and the caller reports an empty answer.
func chatResponseText(response *schemas.BifrostChatResponse) string {
	for _, choice := range response.Choices {
		if choice.ChatNonStreamResponseChoice == nil || choice.Message == nil || choice.Message.Content == nil {
			continue
		}
		content := choice.Message.Content
		if content.ContentStr != nil {
			return *content.ContentStr
		}
		var parts []string
		for _, block := range content.ContentBlocks {
			if block.Text != nil && *block.Text != "" {
				parts = append(parts, *block.Text)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
	}
	return ""
}
