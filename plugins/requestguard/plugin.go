package requestguard

import (
	"context"
	"fmt"
	"strings"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/modelcatalog"
)

const PluginName = "requestguard"

type pluginContextKey struct{}

var (
	guardDecisionContextKey = pluginContextKey{}
	flightHandleContextKey  = struct {
		pluginContextKey
		flight bool
	}{flight: true}
)

// Plugin evaluates request capabilities and budgets, optionally recommends or
// selects alternatives, and coalesces duplicate chat/Responses work.
type Plugin struct {
	config    Config
	logger    schemas.Logger
	catalog   Catalog
	coalescer *Coalescer
	metrics   *Metrics
}

var _ schemas.LLMPlugin = (*Plugin)(nil)
var _ schemas.HTTPTransportPlugin = (*Plugin)(nil)

// Init validates request-guard configuration and initializes its bounded
// coalescer and Prometheus collectors.
func Init(config Config, logger schemas.Logger, catalog Catalog) (*Plugin, error) {
	if err := config.NormalizeAndValidate(); err != nil {
		return nil, err
	}
	metrics, err := NewMetrics(config.Metrics)
	if err != nil {
		return nil, err
	}
	metrics.catalog = catalog
	return &Plugin{
		config:    config,
		logger:    logger,
		catalog:   catalog,
		coalescer: NewCoalescer(config.Coalescer),
		metrics:   metrics,
	}, nil
}

func (p *Plugin) GetName() string { return PluginName }

func (p *Plugin) Cleanup() error {
	if p != nil && p.coalescer != nil {
		p.coalescer.Close()
	}
	return nil
}

// PreRequestHook performs the top-level routing decision. The request is only
// inspected synchronously; no request pointer or nested payload is retained.
func (p *Plugin) PreRequestHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) error {
	if !p.active() || req == nil {
		return nil
	}
	started := time.Now()
	evaluation := p.evaluate(ctx, req)
	previous, _ := guardDecisionFromContext(ctx)
	decision := decisionForEvaluation(previous, evaluation)

	shouldDowngrade := evaluation.capabilityProblem && (p.config.Recommender.AutoDowngradeOnCapabilityFailure || p.config.CapabilityGuard.Mode == PolicyDowngrade) ||
		evaluation.budgetProblem && (p.config.Recommender.AutoDowngradeOnBudgetExceeded || p.config.BudgetGuard.Mode == PolicyDowngrade)
	if shouldDowngrade {
		forCapabilityFailure := evaluation.capabilityProblem
		recommendations := p.recommend(ctx, evaluation, forCapabilityFailure)
		decision.Recommendations = recommendations
		if recommendation, ok := p.pickDowngrade(evaluation, recommendations); ok && rewriteRequestRoute(req, recommendation.Provider, recommendation.Model) {
			decision.Downgraded = true
			decision.Action = CapabilityActionDowngrade
			decision.EffectiveProvider = recommendation.Provider
			decision.EffectiveModel = recommendation.Model
			reason := "capability_failure"
			if evaluation.budgetProblem && !evaluation.capabilityProblem {
				reason = "budget_exceeded"
			}
			decision.Reasons = appendUnique(decision.Reasons, "request downgraded after "+strings.ReplaceAll(reason, "_", " "))
			p.metrics.RecordDowngrade(reason, decision.OriginalProvider, decision.OriginalModel, recommendation.Provider, recommendation.Model)
			p.metrics.RecordEstimatedSavingsUSD("downgrade", recommendation.Provider, recommendation.Model, recommendation.EstimatedSavingsUSD)
		}
	}
	if ctx != nil {
		ctx.SetValue(guardDecisionContextKey, decision)
	}
	p.metrics.ObserveEvalDuration("pre_request", time.Since(started))
	return nil
}

// PreLLMHook evaluates the final request shape (including MCP-injected tools),
// blocks enforced violations, and joins an eligible coalesced execution.
func (p *Plugin) PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error) {
	if !p.active() || req == nil {
		return req, nil, nil
	}
	started := time.Now()
	evaluation := p.evaluate(ctx, req)
	previous, _ := guardDecisionFromContext(ctx)
	decision := decisionForEvaluation(previous, evaluation)
	for _, issue := range evaluation.issues {
		p.metrics.RecordCapabilityFailure(issue.Capability, evaluation.features.Provider, evaluation.features.Model)
	}

	if evaluation.capabilityProblem || evaluation.budgetProblem {
		decision.Recommendations = p.recommend(ctx, evaluation, evaluation.capabilityProblem)
	}
	decision.Action = decisionAction(evaluation)
	if previous != nil && previous.Downgraded && !evaluation.blocked {
		decision.Action = CapabilityActionDowngrade
	}
	decision.Blocked = evaluation.blocked
	if evaluation.blocked {
		decision.Action = CapabilityActionBlock
		decision.BlockReason = evaluation.blockReason()
		p.metrics.RecordRequestBlocked(decision.BlockReason, evaluation.features.Provider, evaluation.features.Model)
		if ctx != nil {
			ctx.SetValue(guardDecisionContextKey, decision)
			ctx.Log(schemas.LogLevelWarn, "request blocked by requestguard: "+decision.BlockReason)
		}
		p.metrics.ObserveEvalDuration("pre_llm", time.Since(started))
		allowFallbacks := (!evaluation.capabilityBlocking || p.config.CapabilityGuard.AllowFallbacksOnBlock) &&
			(!evaluation.budgetBlocking || p.config.BudgetGuard.AllowFallbacksOnBlock)
		return req, &schemas.LLMPluginShortCircuit{Error: buildBlockError(decision, allowFallbacks, evaluation.budgetBlocking && !evaluation.capabilityBlocking)}, nil
	}

	if ctx != nil {
		ctx.SetValue(guardDecisionContextKey, decision)
	}
	p.metrics.ObserveEvalDuration("pre_llm", time.Since(started))

	if !p.config.Coalescer.Enabled {
		return req, nil, nil
	}
	if ctx != nil && ctx.Err() != nil {
		return req, &schemas.LLMPluginShortCircuit{Error: requestGuardContextError(ctx.Err())}, nil
	}
	fingerprintStarted := time.Now()
	fingerprint, eligible, _ := ComputeFingerprint(ctx, req, evaluation.features, p.config.Coalescer)
	p.metrics.ObserveEvalDuration("fingerprint", time.Since(fingerprintStarted))
	if !eligible {
		return req, nil, nil
	}

	var requestContext context.Context = context.Background()
	if ctx != nil {
		requestContext = ctx
	}
	handle, leader := p.coalescer.Acquire(requestContext, fingerprint, evaluation.features.IsStreaming)
	if handle != nil {
		contextErr := handle.ContextErr
		if contextErr == nil && ctx != nil {
			contextErr = ctx.Err()
		}
		if contextErr != nil {
			if leader {
				p.coalescer.Abort(handle, requestGuardContextError(contextErr))
			}
			return req, &schemas.LLMPluginShortCircuit{Error: requestGuardContextError(contextErr)}, nil
		}
	}
	if handle == nil {
		decision.CoalesceRole = "bypass"
		if ctx != nil {
			ctx.SetValue(guardDecisionContextKey, decision)
		}
		return req, nil, nil
	}
	if leader {
		decision.CoalesceRole = "leader"
		if ctx != nil {
			ctx.SetValue(guardDecisionContextKey, decision)
			ctx.SetValue(flightHandleContextKey, handle)
		}
		return req, nil, nil
	}

	decision.CoalesceRole = "follower"
	if evaluation.features.IsStreaming {
		if handle.Chunks == nil {
			decision.CoalesceRole = "bypass"
			if ctx != nil {
				ctx.SetValue(guardDecisionContextKey, decision)
			}
			return req, nil, nil
		}
		p.recordCoalescedSavings(decision, evaluation.features, true)
		if ctx != nil {
			ctx.SetValue(guardDecisionContextKey, decision)
		}
		streamCtx := ctx
		if ctx != nil {
			streamCtx = ctx.Root()
		}
		return req, &schemas.LLMPluginShortCircuit{Stream: forwardCoalescedFollowerStream(streamCtx, handle)}, nil
	}
	if handle.Response == nil && handle.Err == nil {
		handle.Err = coalescerError("coalesced leader completed without a response")
	}
	p.recordCoalescedSavings(decision, evaluation.features, false)
	if ctx != nil {
		ctx.SetValue(guardDecisionContextKey, decision)
	}
	return req, &schemas.LLMPluginShortCircuit{Response: handle.Response, Error: handle.Err}, nil
}

// PostLLMHook completes leader flights and annotates responses with a compact
// request-guard intervention when a policy or coalescing decision was made.
func (p *Plugin) PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	if p == nil {
		return resp, bifrostErr, nil
	}
	handle, _ := flightHandleFromContext(ctx)
	if handle != nil && p.coalescer != nil {
		if handle.streaming {
			if bifrostErr != nil {
				p.coalescer.FinishStream(handle, bifrostErr)
				clearFlightHandle(ctx)
			} else {
				if chunk := streamChunkFromResponse(resp); chunk != nil {
					p.coalescer.PublishChunk(handle, chunk)
				}
				if bifrost.IsFinalChunk(ctx) {
					p.coalescer.FinishStream(handle)
					clearFlightHandle(ctx)
				}
			}
		} else {
			p.coalescer.CompleteUnary(handle, resp, bifrostErr)
			clearFlightHandle(ctx)
		}
	}
	decision, _ := guardDecisionFromContext(ctx)
	if resp != nil && decision != nil && !decision.MetadataAppended && decisionNeedsMetadata(decision) {
		if AppendGuardrailIntervention(resp, schemas.BifrostGuardrailIntervention{
			Phase:         "request",
			GuardrailName: "request_guard",
			Reason:        decisionSummary(decision),
		}) {
			decision.MetadataAppended = true
		}
	}
	return resp, bifrostErr, nil
}

func (p *Plugin) HTTPTransportPreAuthHook(_ *schemas.BifrostContext, _ *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	return nil, nil
}

func (p *Plugin) HTTPTransportPreHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	if p == nil || ctx == nil || req == nil {
		return nil, nil
	}
	header := strings.TrimSpace(p.config.Coalescer.BypassHeader)
	if header == "" {
		return nil, nil
	}
	for name := range req.Headers {
		if strings.EqualFold(name, header) {
			ctx.SetValue(bypassHeaderContextKey, true)
			break
		}
	}
	return nil, nil
}

func (p *Plugin) HTTPTransportPostHook(_ *schemas.BifrostContext, _ *schemas.HTTPRequest, _ *schemas.HTTPResponse) error {
	return nil
}

func (p *Plugin) HTTPTransportResponseHeadersHook(ctx *schemas.BifrostContext, _ *schemas.HTTPRequest, resp *schemas.HTTPResponseMetadata) error {
	if p == nil || !p.config.Headers.Enabled || resp == nil {
		return nil
	}
	decision, ok := guardDecisionFromContext(ctx)
	if !ok || decision == nil {
		return nil
	}
	prefix := strings.TrimRight(strings.TrimSpace(p.config.Headers.Prefix), "-")
	if prefix == "" {
		prefix = DefaultConfig().Headers.Prefix
	}
	resp.SetHeader(prefix+"-Action", decision.Action)
	if decision.CostEstimate != nil && decision.CostEstimate.PricingAvailable {
		resp.SetHeader(prefix+"-Estimated-Cost", formatCost(decision.CostEstimate.EstimatedCostUSD))
	} else {
		resp.SetHeader(prefix+"-Estimated-Cost", "unknown")
	}
	recommendation := "none"
	if len(decision.Recommendations) > 0 {
		recommendation = string(decision.Recommendations[0].Provider) + "/" + decision.Recommendations[0].Model
	}
	resp.SetHeader(prefix+"-Recommendation", recommendation)
	coalesced := "none"
	if decision.CoalesceRole != "" {
		coalesced = decision.CoalesceRole
	}
	resp.SetHeader(prefix+"-Coalesced", coalesced)
	if decision.Downgraded {
		resp.SetHeader(prefix+"-Original-Model", decision.OriginalModel)
	}
	return nil
}

func (p *Plugin) HTTPTransportStreamChunkHook(_ *schemas.BifrostContext, _ *schemas.HTTPRequest, chunk *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error) {
	return chunk, nil
}

func (p *Plugin) active() bool { return p != nil && p.config.Enabled }

type requestEvaluation struct {
	features           RequestFeatures
	estimate           CostEstimate
	usage              *schemas.BifrostLLMUsage
	capability         CapabilityResult
	contextIssue       *CapabilityIssue
	budget             BudgetResult
	issues             []CapabilityIssue
	capabilityProblem  bool
	budgetProblem      bool
	blocked            bool
	capabilityBlocking bool
	budgetBlocking     bool
}

func (p *Plugin) evaluate(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) requestEvaluation {
	features := ExtractRequestFeatures(req)
	capabilityStarted := time.Now()
	capability := EvaluateCapabilities(p.catalog, p.config.CapabilityGuard, features)
	estimate, usage := EstimateTokens(ctx, req, features, p.config.BudgetGuard, p.catalog)
	contextIssue := EvaluateContextLimit(p.catalog, p.config.CapabilityGuard, features, estimate)
	p.metrics.ObserveEvalDuration("capability", time.Since(capabilityStarted))

	budgetStarted := time.Now()
	budget := EvaluateBudget(p.config.BudgetGuard, estimate)
	p.metrics.ObserveEvalDuration("budget", time.Since(budgetStarted))

	issues := append([]CapabilityIssue(nil), capability.Issues...)
	if contextIssue != nil {
		issues = append(issues, *contextIssue)
	}
	capabilityProblem := len(issues) > 0
	budgetProblem := len(budget.Violations) > 0
	capabilityBlocking := capability.Blocked
	for _, issue := range issues {
		if issue.Severity == IssueSeverityBlock || issue.Severity == IssueSeverityDowngrade {
			capabilityBlocking = true
		}
	}
	budgetBlocking := budget.ShouldBlock || budget.ShouldDowngrade
	blocked := capabilityBlocking || budgetBlocking
	if blocked {
		return requestEvaluation{
			features: features, estimate: estimate, usage: usage, capability: capability,
			contextIssue: contextIssue, budget: budget, issues: issues,
			capabilityProblem: capabilityProblem, budgetProblem: budgetProblem,
			blocked: true, capabilityBlocking: capabilityBlocking, budgetBlocking: budgetBlocking,
		}
	}
	return requestEvaluation{
		features: features, estimate: estimate, usage: usage, capability: capability,
		contextIssue: contextIssue, budget: budget, issues: issues,
		capabilityProblem: capabilityProblem, budgetProblem: budgetProblem,
	}
}

func decisionForEvaluation(previous *GuardDecision, evaluation requestEvaluation) *GuardDecision {
	decision := &GuardDecision{
		Action:            decisionAction(evaluation),
		OriginalProvider:  evaluation.features.Provider,
		OriginalModel:     evaluation.features.Model,
		EffectiveProvider: evaluation.features.Provider,
		EffectiveModel:    evaluation.features.Model,
		CapabilityIssues:  append([]CapabilityIssue(nil), evaluation.issues...),
		CostEstimate:      &evaluation.estimate,
		BudgetViolations:  append([]BudgetViolation(nil), evaluation.budget.Violations...),
		HasWarnings:       evaluation.budget.ShouldWarn,
	}
	for _, issue := range evaluation.issues {
		if issue.Severity == IssueSeverityWarning {
			decision.HasWarnings = true
		}
		if issue.Reason != "" {
			decision.Reasons = appendUnique(decision.Reasons, issue.Reason)
		}
	}
	for _, violation := range evaluation.budget.Violations {
		if violation.Reason != "" {
			decision.Reasons = appendUnique(decision.Reasons, violation.Reason)
		}
	}
	if previous != nil {
		if previous.OriginalProvider != "" {
			decision.OriginalProvider = previous.OriginalProvider
		}
		if previous.OriginalModel != "" {
			decision.OriginalModel = previous.OriginalModel
		}
		decision.Downgraded = previous.Downgraded
		decision.Recommendations = append([]ModelRecommendation(nil), previous.Recommendations...)
		decision.Reasons = appendUniqueMany(decision.Reasons, previous.Reasons...)
		if previous.Downgraded {
			decision.EffectiveProvider = evaluation.features.Provider
			decision.EffectiveModel = evaluation.features.Model
			decision.Action = CapabilityActionDowngrade
		}
	}
	return decision
}

func decisionAction(evaluation requestEvaluation) string {
	if evaluation.blocked || evaluation.capability.NeedsDowngrade || evaluation.budget.ShouldDowngrade {
		return CapabilityActionDowngrade
	}
	if evaluation.budget.ShouldWarn {
		return CapabilityActionWarn
	}
	for _, issue := range evaluation.issues {
		switch issue.Severity {
		case IssueSeverityWarning:
			return CapabilityActionWarn
		case IssueSeverityAudit:
			return CapabilityActionAudit
		case IssueSeverityDowngrade:
			return CapabilityActionDowngrade
		}
	}
	if evaluation.budget.Action == CapabilityActionAudit || evaluation.budget.Action == CapabilityActionWarn {
		return evaluation.budget.Action
	}
	return CapabilityActionAllow
}

func (evaluation requestEvaluation) blockReason() string {
	var reasons []string
	for _, issue := range evaluation.issues {
		if issue.Severity == IssueSeverityBlock || issue.Severity == IssueSeverityDowngrade {
			reasons = append(reasons, issue.Capability+": "+issue.Reason)
		}
	}
	for _, violation := range evaluation.budget.Violations {
		if evaluation.budget.ShouldBlock || evaluation.budget.ShouldDowngrade {
			reasons = append(reasons, violation.Metric+": "+violation.Reason)
		}
	}
	if len(reasons) == 0 {
		return "request violates a configured requestguard policy"
	}
	return strings.Join(reasons, "; ")
}

func (p *Plugin) recommend(ctx *schemas.BifrostContext, evaluation requestEvaluation, capabilityFailure bool) []ModelRecommendation {
	recConfig := p.config.Recommender
	if !recConfig.Enabled && !recConfig.AutoDowngradeOnCapabilityFailure && !recConfig.AutoDowngradeOnBudgetExceeded && p.config.CapabilityGuard.Mode != PolicyDowngrade && p.config.BudgetGuard.Mode != PolicyDowngrade {
		return nil
	}
	recConfig.Enabled = true
	started := time.Now()
	recommendations := RecommendModels(
		p.catalog, p.config.CapabilityGuard, p.config.BudgetGuard, recConfig,
		evaluation.features, evaluation.estimate, evaluation.usage,
		BuildPricingScopes(ctx, evaluation.features.Provider), evaluation.estimate.EstimatedCostUSD,
		capabilityFailure,
	)
	p.metrics.ObserveEvalDuration("recommend", time.Since(started))
	return recommendations
}

func (p *Plugin) pickDowngrade(evaluation requestEvaluation, recommendations []ModelRecommendation) (ModelRecommendation, bool) {
	for _, recommendation := range recommendations {
		if evaluation.budgetProblem {
			candidateEstimate := evaluation.estimate
			candidateEstimate.EstimatedCostUSD = recommendation.EstimatedCostUSD
			candidateEstimate.PricingAvailable = true
			if len(EvaluateBudget(p.config.BudgetGuard, candidateEstimate).Violations) > 0 {
				continue
			}
		}
		return recommendation, true
	}
	return ModelRecommendation{}, false
}

func rewriteRequestRoute(req *schemas.BifrostRequest, provider schemas.ModelProvider, model string) bool {
	if req == nil || provider == "" || strings.TrimSpace(model) == "" {
		return false
	}
	switch {
	case req.ChatRequest != nil:
		req.ChatRequest.Provider = provider
		req.ChatRequest.Model = model
	case req.ResponsesRequest != nil:
		req.ResponsesRequest.Provider = provider
		req.ResponsesRequest.Model = model
	default:
		return false
	}
	return true
}

func (p *Plugin) recordCoalescedSavings(decision *GuardDecision, features RequestFeatures, streaming bool) {
	p.metrics.RecordCoalescedExecutionAvoided(features.Provider, features.Model, streaming)
	if decision.CostEstimate == nil || !decision.CostEstimate.PricingAvailable {
		return
	}
	savings := decision.CostEstimate.EstimatedCostUSD
	decision.CoalescedSavingsUSD = savings
	p.metrics.RecordEstimatedSavingsUSD("coalescing", features.Provider, features.Model, savings)
}

func guardDecisionFromContext(ctx *schemas.BifrostContext) (*GuardDecision, bool) {
	if ctx == nil {
		return nil, false
	}
	decision, ok := ctx.Value(guardDecisionContextKey).(*GuardDecision)
	return decision, ok && decision != nil
}

func flightHandleFromContext(ctx *schemas.BifrostContext) (*FlightHandle, bool) {
	if ctx == nil {
		return nil, false
	}
	handle, ok := ctx.Value(flightHandleContextKey).(*FlightHandle)
	return handle, ok && handle != nil
}

func clearFlightHandle(ctx *schemas.BifrostContext) {
	if ctx != nil {
		ctx.SetValue(flightHandleContextKey, (*FlightHandle)(nil))
	}
}

func buildBlockError(decision *GuardDecision, allowFallbacks, budgetBlocking bool) *schemas.BifrostError {
	reason := "request blocked by request guard policy"
	if decision != nil && decision.BlockReason != "" {
		reason = decision.BlockReason
	}
	bifrostErr := NewBlockingError(reason, allowFallbacks)
	if budgetBlocking {
		bifrostErr.ExtraFields.ErrorType = schemas.ErrorTypePolicyBudgetExceeded
	}
	if decision != nil && bifrostErr.Error != nil {
		structured := map[string]any{
			"provider":           decision.EffectiveProvider,
			"model":              decision.EffectiveModel,
			"capability_issues":  decision.CapabilityIssues,
			"budget_violations":  decision.BudgetViolations,
			"recommendations":    decision.Recommendations,
			"estimated_cost_usd": nil,
			"pricing_available":  false,
		}
		if decision.CostEstimate != nil {
			structured["estimated_cost_usd"] = decision.CostEstimate.EstimatedCostUSD
			structured["pricing_available"] = decision.CostEstimate.PricingAvailable
		}
		bifrostErr.Error.Param = structured
	}
	return bifrostErr
}

func requestGuardContextError(err error) *schemas.BifrostError {
	message := "request context canceled"
	status := 499
	if err == context.DeadlineExceeded {
		message = "request context deadline exceeded"
		status = 504
	} else if err != nil && err != context.Canceled {
		message = err.Error()
	}
	typeName := "request_guard_context"
	allowFallbacks := false
	return &schemas.BifrostError{
		Type:           &typeName,
		IsBifrostError: true,
		StatusCode:     &status,
		Error:          &schemas.ErrorField{Message: message},
		AllowFallbacks: &allowFallbacks,
	}
}

func forwardCoalescedFollowerStream(ctx *schemas.BifrostContext, handle *FlightHandle) chan *schemas.BifrostStreamChunk {
	stream := make(chan *schemas.BifrostStreamChunk)
	var done <-chan struct{}
	if ctx != nil {
		done = ctx.Done()
	}
	go func() {
		defer close(stream)
		for {
			if ctx != nil && ctx.Err() != nil {
				return
			}
			select {
			case chunk, open := <-handle.Chunks:
				if !open {
					if ctx != nil && ctx.Err() != nil {
						return
					}
					if err := handle.coalescer.streamFollowerError(handle); err != nil {
						sendCoalescedTerminalError(stream, err, done)
					}
					return
				}
				if chunk == nil {
					continue
				}
				select {
				case stream <- chunk:
				case <-done:
					return
				}
			case <-handle.streamDone:
				if ctx != nil && ctx.Err() != nil {
					return
				}
				if err := handle.coalescer.streamFollowerError(handle); err != nil {
					sendCoalescedTerminalError(stream, err, done)
				}
				return
			case <-done:
				return
			}
		}
	}()
	return stream
}

func sendCoalescedTerminalError(stream chan<- *schemas.BifrostStreamChunk, err *schemas.BifrostError, done <-chan struct{}) {
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case stream <- &schemas.BifrostStreamChunk{BifrostError: err}:
	case <-done:
	case <-timer.C:
	}
}

func streamChunkFromResponse(resp *schemas.BifrostResponse) *schemas.BifrostStreamChunk {
	if resp == nil {
		return nil
	}
	chunk := &schemas.BifrostStreamChunk{
		BifrostTextCompletionResponse:        resp.TextCompletionResponse,
		BifrostChatResponse:                  resp.ChatResponse,
		BifrostResponsesStreamResponse:       resp.ResponsesStreamResponse,
		BifrostSpeechStreamResponse:          resp.SpeechStreamResponse,
		BifrostTranscriptionStreamResponse:   resp.TranscriptionStreamResponse,
		BifrostImageGenerationStreamResponse: resp.ImageGenerationStreamResponse,
		BifrostPassthroughResponse:           resp.PassthroughResponse,
	}
	if chunk.BifrostTextCompletionResponse == nil && chunk.BifrostChatResponse == nil && chunk.BifrostResponsesStreamResponse == nil && chunk.BifrostSpeechStreamResponse == nil && chunk.BifrostTranscriptionStreamResponse == nil && chunk.BifrostImageGenerationStreamResponse == nil && chunk.BifrostPassthroughResponse == nil {
		return nil
	}
	return chunk
}

func decisionNeedsMetadata(decision *GuardDecision) bool {
	return decision.HasWarnings || decision.Downgraded || len(decision.Recommendations) > 0 || decision.CoalesceRole != ""
}

func decisionSummary(decision *GuardDecision) string {
	if decision.Downgraded {
		return fmt.Sprintf("model downgraded from %s to %s", decision.OriginalModel, decision.EffectiveModel)
	}
	if decision.CoalesceRole != "" {
		return "request coalescing role: " + decision.CoalesceRole
	}
	if decision.HasWarnings {
		return "request guard policy warnings were observed"
	}
	return "compatible model recommendations were attached"
}

func formatCost(value float64) string { return fmt.Sprintf("%.8f", value) }

func appendUnique(values []string, value string) []string {
	if strings.TrimSpace(value) == "" {
		return values
	}
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func appendUniqueMany(values []string, additions ...string) []string {
	for _, value := range additions {
		values = appendUnique(values, value)
	}
	return values
}

var _ Catalog = (*modelcatalog.ModelCatalog)(nil)
