package routing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
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
// *RoutingPlugin satisfy this via method promotion.
type ChatExecutorSetter interface {
	SetChatRequestExecutor(ChatRequestExecutor)
}

// ResponsesRequestExecutor invokes the Responses endpoint on the bifrost
// client. It is the /v1/responses counterpart to ChatRequestExecutor, used
// only as a fallback when a judge model rejects the chat endpoint. It mirrors
// the signature of bifrost.Client.ResponsesRequest.
type ResponsesRequestExecutor func(ctx *schemas.BifrostContext, req *schemas.BifrostResponsesRequest) (*schemas.BifrostResponsesResponse, *schemas.BifrostError)

// ResponsesExecutorSetter is the Responses-API analogue of ChatExecutorSetter,
// wired by the HTTP server at the same point. It is optional: a plugin without
// it simply never retries a chat-rejected classification on /v1/responses.
type ResponsesExecutorSetter interface {
	SetResponsesRequestExecutor(ResponsesRequestExecutor)
}

// llmClassifierMaxCompletionTokens bounds the classification answer. The
// contract answer is a dozen tokens of JSON; the headroom exists for models
// that spend reasoning tokens inside the same budget before answering.
const llmClassifierMaxCompletionTokens = 512

// SetChatRequestExecutor wires up the function used to call the classifier
// chat model. Without it, llm complexity classification publishes no tier.
// Safe for concurrent use with classification and plugin reloads.
func (p *RoutingPlugin) SetChatRequestExecutor(executor ChatRequestExecutor) {
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
func (p *RoutingPlugin) chatExecutor() ChatRequestExecutor {
	if ptr := p.chatRequestExecutor.Load(); ptr != nil {
		return *ptr
	}
	return nil
}

// SetResponsesRequestExecutor wires up the function used to retry a classifier
// completion on /v1/responses when the chat endpoint refuses the judge model.
// It is optional; without it, a chat-rejected classification simply fails.
// Safe for concurrent use with classification and plugin reloads.
func (p *RoutingPlugin) SetResponsesRequestExecutor(executor ResponsesRequestExecutor) {
	if executor == nil {
		p.responsesRequestExecutor.Store(nil)
		return
	}
	p.responsesRequestExecutor.Store(&executor)
}

// responsesExecutor returns the currently wired responses executor, or nil.
func (p *RoutingPlugin) responsesExecutor() ResponsesRequestExecutor {
	if ptr := p.responsesRequestExecutor.Load(); ptr != nil {
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
func (p *RoutingPlugin) classifyComplexityTextViaLLM(ctx context.Context, llm *complexity.LLMConfig, systemPrompt, userText string) (string, error) {
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

	response, bifrostErr := p.runClassifierCompletion(chatCtx, executor, req)
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

// runClassifierCompletion runs the classification request on chat completions,
// falling back to the Responses API when the provider rejects the chat request
// in a way the Responses retry fixes — a judge model that requires
// /v1/responses, or one that refuses the temperature=0 the chat request sets
// (see llmClassifierShouldRetryWithResponses). This mirrors the prompt-guardrail
// judge's two-endpoint strategy: some reasoning models only accept a request
// shape like this one on /v1/responses, and a classifier pinned to such a model
// would otherwise always report "unavailable". Any other chat failure (timeout,
// auth, bad model) is returned unchanged so the caller can classify it. The
// Responses answer is converted back to the chat shape so the caller's
// usage-accounting and text-extraction paths are shared.
func (p *RoutingPlugin) runClassifierCompletion(
	chatCtx *schemas.BifrostContext,
	chat ChatRequestExecutor,
	req *schemas.BifrostChatRequest,
) (*schemas.BifrostChatResponse, *schemas.BifrostError) {
	response, chatErr := chat(chatCtx, req)
	if chatErr == nil {
		return response, nil
	}
	if !llmClassifierShouldRetryWithResponses(chatErr) {
		return nil, chatErr
	}
	responses := p.responsesExecutor()
	if responses == nil {
		// The provider asked for /v1/responses but no executor is wired. Return
		// the original chat error so the caller surfaces the actionable message
		// rather than a generic "not configured".
		return nil, chatErr
	}

	responsesReq := req.ToResponsesRequest()
	if responsesReq.Params != nil {
		// Reasoning models on the Responses API reject a non-default temperature,
		// and a deterministic tier vote does not need sampling control anyway.
		responsesReq.Params.Temperature = nil
	}
	responsesResponse, responsesErr := responses(chatCtx, responsesReq)
	if responsesErr != nil {
		// Prefer the Responses error: the chat endpoint already told us it could
		// not serve this model, so the Responses failure is the informative one.
		return nil, responsesErr
	}
	return responsesResponse.ToBifrostChatResponse(), nil
}

// llmClassifierShouldRetryWithResponses reports whether a failed chat
// completion should be retried on the Responses API. Two provider signals
// qualify, and the Responses retry resolves both — it targets /v1/responses and
// sends no temperature:
//   - an explicit instruction to switch endpoints, which reasoning models give
//     when they cannot serve this request shape on /v1/chat/completions (the
//     same heuristic the prompt guardrail uses); and
//   - a rejection of temperature=0, which reasoning models require to be the
//     default. The classifier keeps temperature=0 on chat for deterministic
//     routing on models that accept it; a model that refuses it is a reasoning
//     model, and the Responses retry drops the parameter entirely.
func llmClassifierShouldRetryWithResponses(bifrostErr *schemas.BifrostError) bool {
	if bifrostErr == nil {
		return false
	}
	message := strings.ToLower(bifrostErr.GetErrorString())
	switch {
	case strings.Contains(message, "/v1/responses") &&
		(strings.Contains(message, "/v1/chat/completions") || strings.Contains(message, "function tool")):
		return true
	case strings.Contains(message, "temperature") &&
		(strings.Contains(message, "does not support") || strings.Contains(message, "only the default")):
		return true
	default:
		return false
	}
}

// computeLLMComplexity runs the llm fallback classifier for one request —
// always after a semantic non-answer, never as the primary — and publishes
// its outcome, following the semantic branch's logging discipline: every
// failure funnels to one MechanismSkipped and one routing-engine log line
// naming the cause.
func (p *RoutingPlugin) computeLLMComplexity(ctx *schemas.BifrostContext, input complexity.ComplexityInput) *complexity.ComplexityResult {
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
	case err != nil:
		// Any other failure is a provider or wiring problem — a rejected
		// endpoint, an auth error, an unreachable model. The cause is the whole
		// point, so name it here instead of leaving it in a debug-only line: a
		// generic "unavailable" sends the operator reading server logs they may
		// not have. Provider error strings carry no secrets.
		unavailableLog = fmt.Sprintf("LLM complexity classification unavailable: %v; no complexity tier is published", err)
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
