package routing

import (
	"errors"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
)

const noClassifiableComplexityInputLog = "Complexity analysis skipped: no routable human-authored text detected"

// complexityProposal is one classifier's answer before monotonic session state is applied.
type complexityProposal struct {
	Result          *complexity.ComplexityResult
	Mechanism       string
	Score           *float64
	Confidence      *float64
	MatchedExemplar string
	// Model is the provider/model that classified, for decision-model and LLM
	// proposals. The mechanism stays generic ("decision", "llm") because it is a
	// metric label; logs name the model so operators can see which one answered.
	Model string
	// SemanticMiss is the nearest exemplar a semantic match rejected for
	// falling below min_similarity, kept when a fallback answered instead.
	SemanticMiss *complexity.SemanticResult
	LogLevel     schemas.LogLevel
	LogMessage   string
	// Reused marks an answer a router took from an earlier router's
	// classification of the same request (see classifyRouterInput). Its log
	// line is the earlier router's and is already in the routing log, so it
	// is not written again: the log shows one classification, then what each
	// router did with it.
	Reused bool
}

// sharedClassificationsKey keys the per-request record of what classifiers
// answered: a list of sharedClassification, one for each classifier key the
// request met. Routers whose classifier settings are the same would each get
// the same answer for one request, so the first one's is kept and the others
// reuse it: the request is embedded, or sent to the decision or llm model,
// once. A request meets one or two kinds of classifier, so the list is
// searched in order.
type sharedClassificationsKey struct{}

// sharedClassification is one classifier's answer for a request, under the
// key of the settings that produced it.
type sharedClassification struct {
	key      string
	proposal complexityProposal
}

// sharedClassificationFor returns what a classifier with the same settings
// already answered for this request, marked as reused.
func sharedClassificationFor(ctx *schemas.BifrostContext, key string) (complexityProposal, bool) {
	answers, _ := ctx.Value(sharedClassificationsKey{}).([]sharedClassification)
	for i := range answers {
		if answers[i].key == key {
			proposal := answers[i].proposal
			proposal.Reused = true
			return proposal, true
		}
	}
	return complexityProposal{}, false
}

// shareClassification keeps a classifier's answer for the routers after it. A
// classification that produced no tier is kept as well: the next router with
// the same settings would only fail, or time out, the same way.
func shareClassification(ctx *schemas.BifrostContext, key string, proposal complexityProposal) {
	answers, _ := ctx.Value(sharedClassificationsKey{}).([]sharedClassification)
	ctx.SetValue(sharedClassificationsKey{}, append(answers, sharedClassification{key: key, proposal: proposal}))
}

// computeRouterComplexity classifies the request with one classifier, at most once per
// request (see modelRouterTiersKey).
func (p *RoutingPlugin) computeRouterComplexity(
	ctx *schemas.BifrostContext,
	req *schemas.BifrostRequest,
	rt *modelRouterRuntime,
) *complexity.ComplexityResult {
	if cached, ok := cachedRouterTier(ctx, rt.id); ok {
		publishComplexityDecision(ctx, cached.result, cached.mechanism, cached.score)
		return cached.result
	}
	result := p.classifyRouterRequest(ctx, req, rt)
	cacheRouterTier(ctx, rt.id, result)
	return result
}

// classifyRouterRequest reuses continuation or session state before classifying the request.
func (p *RoutingPlugin) classifyRouterRequest(
	ctx *schemas.BifrostContext,
	req *schemas.BifrostRequest,
	rt *modelRouterRuntime,
) *complexity.ComplexityResult {
	input, disposition := complexity.BuildInputWithDisposition(ctx, req)
	sessionID, _ := ctx.Value(schemas.BifrostContextKeySessionID).(string)
	sessionActive := rt.sessionEnabled.Load() && sessionID != "" && p.sessions != nil

	if disposition == complexity.InputContinuation {
		if sessionID != "" && p.sessions != nil {
			tier, found, err := p.loadTurnTier(complexityTurnTierKeyFor(ctx, rt.id))
			if err != nil {
				p.logComplexitySessionError("load continuation tier", err)
			} else if found {
				if sessionActive {
					resolution, resolveErr := p.resolveSessionTier(complexitySessionKeyFor(ctx, rt.id), tier)
					if resolveErr != nil {
						p.logComplexitySessionError("refresh continuation", resolveErr)
					} else if resolution.EffectiveTier != "" {
						tier = resolution.EffectiveTier
					}
				}
				result := &complexity.ComplexityResult{Tier: tier}
				publishComplexityDecision(ctx, result, complexity.MechanismSession, nil)
				ctx.AppendRoutingEngineLog(rt.logEngine(), schemas.LogLevelInfo,
					fmt.Sprintf("Complexity reused this turn's tier: tier=%s reason=tool-continuation", tier))
				return result
			}
		}
		if sessionActive {
			key := complexitySessionKeyFor(ctx, rt.id)
			tier, found, err := p.loadSessionTier(key, true)
			if err != nil {
				p.logComplexitySessionError("refresh continuation", err)
			} else if found {
				result := &complexity.ComplexityResult{Tier: tier}
				publishComplexityDecision(ctx, result, complexity.MechanismSession, nil)
				ctx.AppendRoutingEngineLog(
					rt.logEngine(),
					schemas.LogLevelInfo,
					fmt.Sprintf("Session complexity reused: effective=%s reason=continuation", tier),
				)
				return result
			}
		}
		if input.LastUserText == "" {
			publishComplexityDecision(ctx, nil, complexity.MechanismSkipped, nil)
			ctx.AppendRoutingEngineLog(
				rt.logEngine(),
				schemas.LogLevelInfo,
				noClassifiableComplexityInputLog,
			)
			return nil
		}
	} else if disposition != complexity.InputClassifiable {

		publishComplexityDecision(ctx, nil, complexity.MechanismSkipped, nil)
		ctx.AppendRoutingEngineLog(
			rt.logEngine(),
			schemas.LogLevelInfo,
			noClassifiableComplexityInputLog,
		)
		return nil
	}

	if !sessionActive {
		proposal := p.classifyRouterInput(ctx, input, rt)
		rt.publishProposal(ctx, proposal)
		return proposal.Result
	}

	key := complexitySessionKeyFor(ctx, rt.id)
	priorTier, priorFound, loadErr := p.loadSessionTier(key, false)
	if loadErr != nil {
		p.logComplexitySessionError("inspect", loadErr)
		proposal := p.classifyRouterInput(ctx, input, rt)
		rt.publishProposal(ctx, proposal)
		return proposal.Result
	}

	if priorFound && priorTier == complexity.TierComplex {
		resolution, err := p.resolveSessionTier(key, "")
		if err == nil && resolution.EffectiveTier != "" {
			result := &complexity.ComplexityResult{Tier: resolution.EffectiveTier}
			publishComplexityDecision(ctx, result, complexity.MechanismSession, nil)
			ctx.AppendRoutingEngineLog(
				rt.logEngine(),
				schemas.LogLevelInfo,
				"Session complexity reused: effective=COMPLEX reason=complex-ceiling",
			)
			return result
		}
		if err != nil {
			p.logComplexitySessionError("refresh complex ceiling", err)
		}
		// The state could have expired between inspection and refresh. Classify the
		// current human turn instead of routing from a stale read.
	}

	proposal := p.classifyRouterInput(ctx, input, rt)
	proposedTier := ""
	if proposal.Result != nil {
		proposedTier = proposal.Result.Tier
	}
	resolution, err := p.resolveSessionTier(key, proposedTier)
	if err != nil {
		p.logComplexitySessionError("resolve", err)
		return p.publishLocalSessionFallback(ctx, rt, priorTier, priorFound, proposal)
	}

	if resolution.EffectiveTier == "" {
		rt.publishProposal(ctx, proposal)
		return proposal.Result
	}

	if proposal.Result != nil && resolution.EffectiveTier == proposal.Result.Tier {
		publishComplexityDecision(ctx, proposal.Result, proposal.Mechanism, proposal.Score)
		event := "confirmed"
		previousTier := ""
		switch {
		case !resolution.Existed:
			event = "initialized"
		case resolution.Escalated:
			event = "escalated"
			previousTier = resolution.PreviousTier
		}
		ctx.AppendRoutingEngineLog(
			rt.logEngine(),
			schemas.LogLevelInfo,
			formatSessionProposalLog(event, resolution.EffectiveTier, previousTier, proposal),
		)
		return proposal.Result
	}

	result := &complexity.ComplexityResult{Tier: resolution.EffectiveTier}
	publishComplexityDecision(ctx, result, complexity.MechanismSession, nil)
	if proposal.Result != nil {
		ctx.AppendRoutingEngineLog(
			rt.logEngine(),
			schemas.LogLevelInfo,
			formatSessionProposalLog("held", resolution.EffectiveTier, "", proposal),
		)
	} else {
		ctx.AppendRoutingEngineLog(
			rt.logEngine(),
			proposal.LogLevel,
			fmt.Sprintf(
				"Session complexity reused: effective=%s; current classifier produced no tier (%s)",
				resolution.EffectiveTier,
				proposal.LogMessage,
			),
		)
	}
	return result
}

// classifyComplexityInput classifies with the Complexity Router page's classifiers.
func (p *RoutingPlugin) classifyComplexityInput(ctx *schemas.BifrostContext, input complexity.ComplexityInput) complexityProposal {
	return p.classifyRouterInput(ctx, input, p.complexityRuntime())
}

// classifyRouterInput classifies the input with the router's classifiers, or
// reuses the answer an earlier router with the same classifier settings got
// for this request. Only the classifier's own answer is reused: session
// memory is applied by the caller, per router.
func (p *RoutingPlugin) classifyRouterInput(ctx *schemas.BifrostContext, input complexity.ComplexityInput, rt *modelRouterRuntime) complexityProposal {
	key := rt.classifierKey()
	if key == "" {
		return p.runRouterClassifier(ctx, input, rt)
	}
	if proposal, ok := sharedClassificationFor(ctx, key); ok {
		return proposal
	}
	proposal := p.runRouterClassifier(ctx, input, rt)
	shareClassification(ctx, key, proposal)
	return proposal
}

// runRouterClassifier selects the router's configured primary classifier and fallback chain.
func (p *RoutingPlugin) runRouterClassifier(ctx *schemas.BifrostContext, input complexity.ComplexityInput, rt *modelRouterRuntime) complexityProposal {
	config := rt.config.Load()
	if config != nil {
		switch config.Classifier {
		case complexity.ClassifierDecision:
			return p.classifyRouterDecision(ctx, input, rt)
		case complexity.ClassifierLLM:
			return p.classifyLLMComplexity(ctx, input, rt)
		}
	}
	return p.classifySemanticComplexity(ctx, input, rt)
}

// classifySemanticComplexity runs the router's semantic classifier and its configured fallback.
func (p *RoutingPlugin) classifySemanticComplexity(ctx *schemas.BifrostContext, input complexity.ComplexityInput, rt *modelRouterRuntime) complexityProposal {
	config := rt.config.Load()
	if rt.semantic == nil || !rt.semantic.IsConfigured() {
		if config != nil && config.Semantic != nil && config.Semantic.Fallback == configstore.ComplexitySemanticFallbackDecision {
			ctx.AppendRoutingEngineLog(
				rt.logEngine(),
				schemas.LogLevelInfo,
				noSemanticClassifierLog+"; falling back to the decision model",
			)
			return p.classifyRouterDecision(ctx, input, rt)
		}
		if p.logger != nil {
			p.logger.Debug("[Routing] %s", noSemanticClassifierLog)
		}
		return complexityProposal{
			Mechanism:  complexity.MechanismSkipped,
			LogLevel:   schemas.LogLevelInfo,
			LogMessage: noSemanticClassifierLog,
		}
	}

	semanticResult, err := rt.semantic.Classify(ctx, input)
	var rejectedResult *complexity.SemanticResult
	var timedOut bool
	if err != nil {
		if p.logger != nil {
			p.logger.Debug("[Routing] Semantic complexity classification unavailable: %v", err)
		}
		timedOut = errors.Is(err, ErrEmbeddingTimeout)
	} else if semanticResult != nil && !semanticResult.Accepted {
		rejectedResult = semanticResult
		if p.logger != nil {
			p.logger.Debug(
				"[Routing] Semantic complexity below min_similarity: tier=%s similarity=%.2f min=%.2f",
				semanticResult.Tier,
				semanticResult.Score,
				semanticResult.MinSimilarity,
			)
		}
	} else if semanticResult != nil {
		score := semanticResult.Score
		result := &complexity.ComplexityResult{Tier: semanticResult.Tier, Score: score}
		return complexityProposal{
			Result:          result,
			Mechanism:       complexity.MechanismSemantic,
			Score:           &score,
			MatchedExemplar: semanticResult.MatchedExemplar,
			LogLevel:        schemas.LogLevelInfo,
			LogMessage: withMatchedExemplar(
				fmt.Sprintf("Semantic complexity: tier=%s similarity=%.2f", result.Tier, result.Score),
				semanticResult.MatchedExemplar,
			),
		}
	}

	unavailableLevel := schemas.LogLevelWarn
	unavailableCause := "Semantic complexity classification unavailable"
	switch {
	case err == nil && semanticResult == nil:
		unavailableLevel = schemas.LogLevelInfo
	case rejectedResult != nil:
		unavailableLevel = schemas.LogLevelInfo
		unavailableCause = withMatchedExemplar(
			fmt.Sprintf(
				"Semantic complexity rejected: nearest tier=%s similarity=%.2f below min_similarity=%.2f",
				rejectedResult.Tier,
				rejectedResult.Score,
				rejectedResult.MinSimilarity,
			),
			rejectedResult.MatchedExemplar,
		)
	case timedOut:
		unavailableCause = fmt.Sprintf(
			"Semantic complexity classification timed out after %s",
			rt.semantic.Timeout(),
		)
	}

	if config != nil && config.Semantic != nil && config.Semantic.Fallback == configstore.ComplexitySemanticFallbackDecision {
		ctx.AppendRoutingEngineLog(
			rt.logEngine(),
			schemas.LogLevelInfo,
			unavailableCause+"; falling back to the decision model",
		)
		proposal := p.classifyRouterDecision(ctx, input, rt)
		proposal.SemanticMiss = rejectedResult
		return proposal
	}
	if rt.llm != nil && rt.llm.FallbackEnabled() {
		ctx.AppendRoutingEngineLog(
			rt.logEngine(),
			schemas.LogLevelInfo,
			unavailableCause+"; falling back to the LLM classifier",
		)
		proposal := p.classifyLLMComplexity(ctx, input, rt)
		proposal.SemanticMiss = rejectedResult
		return proposal
	}
	return complexityProposal{
		Mechanism:    complexity.MechanismSkipped,
		SemanticMiss: rejectedResult,
		LogLevel:     unavailableLevel,
		LogMessage:   unavailableCause + ", so no complexity tier is published",
	}
}

// publishLocalSessionFallback keeps the prior tier when session storage cannot be updated. It
// logs under rt's engine, as every other step of rt's classification does.
func (p *RoutingPlugin) publishLocalSessionFallback(
	ctx *schemas.BifrostContext,
	rt *modelRouterRuntime,
	priorTier string,
	priorFound bool,
	proposal complexityProposal,
) *complexity.ComplexityResult {
	if !priorFound {
		rt.publishProposal(ctx, proposal)
		return proposal.Result
	}
	if proposal.Result != nil && !complexityTierAtLeast(priorTier, proposal.Result.Tier) {
		rt.publishProposal(ctx, proposal)
		return proposal.Result
	}

	result := &complexity.ComplexityResult{Tier: priorTier}
	publishComplexityDecision(ctx, result, complexity.MechanismSession, nil)
	message := ""
	if proposal.Result != nil {
		message = formatSessionProposalLog("reused after state-store failure", priorTier, "", proposal)
	} else {
		message = fmt.Sprintf(
			"Session complexity reused after state-store failure: effective=%s; current classifier produced no tier (%s)",
			priorTier,
			proposal.LogMessage,
		)
	}
	ctx.AppendRoutingEngineLog(
		rt.logEngine(),
		schemas.LogLevelWarn,
		message,
	)
	return result
}

// logComplexitySessionError logs a failed session-state operation.
func (p *RoutingPlugin) logComplexitySessionError(operation string, err error) {
	if p.logger != nil {
		p.logger.Warn("[Routing] complexity session store %s failed: %v", operation, err)
	}
}

// publishComplexityProposal publishes a classifier result and its routing log.
func publishComplexityProposal(ctx *schemas.BifrostContext, proposal complexityProposal) {
	publishComplexityDecision(ctx, proposal.Result, proposal.Mechanism, proposal.Score)
	if proposal.LogMessage != "" {
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineRoutingRule, proposal.LogLevel, proposal.LogMessage)
	}
}

// publishComplexityDecision updates the request context with the selected tier and mechanism.
func publishComplexityDecision(
	ctx *schemas.BifrostContext,
	result *complexity.ComplexityResult,
	mechanism string,
	score *float64,
) {
	ctx.ClearValue(schemas.BifrostContextKeyGovernanceComplexityTier)
	ctx.ClearValue(schemas.BifrostContextKeyGovernanceComplexityScore)
	ctx.ClearValue(schemas.BifrostContextKeyGovernanceComplexityMechanism)
	if result != nil {
		ctx.SetValue(schemas.BifrostContextKeyGovernanceComplexityTier, result.Tier)
	}
	if score != nil {
		ctx.SetValue(schemas.BifrostContextKeyGovernanceComplexityScore, *score)
	}
	if mechanism != "" {
		ctx.SetValue(schemas.BifrostContextKeyGovernanceComplexityMechanism, mechanism)
	}
}

// formatSessionProposalLog uses one field vocabulary for every session event
// backed by a current classifier proposal. Semantic evidence is explicitly
// proposal-scoped because the effective tier can be retained from prior state.
func formatSessionProposalLog(event, effectiveTier, previousTier string, proposal complexityProposal) string {
	message := fmt.Sprintf("Session complexity %s: effective=%s", event, effectiveTier)
	if previousTier != "" {
		message += fmt.Sprintf(" previous=%s", previousTier)
	}
	message += fmt.Sprintf(" proposed=%s source=%s", proposal.Result.Tier, proposal.Mechanism)
	if proposal.Reused {
		message += " reused=true"
	}
	if proposal.Score != nil {
		message += fmt.Sprintf(" proposed_similarity=%.2f", *proposal.Score)
	}
	if proposal.Confidence != nil {
		message += fmt.Sprintf(" proposed_confidence=%.2f", *proposal.Confidence)
	}
	if proposal.Model != "" {
		message += fmt.Sprintf(" proposed_model=%s", proposal.Model)
	}
	if matched := truncateExemplarForLog(proposal.MatchedExemplar); matched != "" {
		message += fmt.Sprintf(" proposed_matched=%q", matched)
	}
	return message
}
