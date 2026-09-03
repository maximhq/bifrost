package routing

import (
	"errors"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
)

const noClassifiableComplexityInputLog = "Complexity analysis skipped: no routable human-authored text detected"

// complexityProposal is one request classifier's answer before monotonic
// session state is applied. Score is nil for mechanisms, such as the LLM
// fallback, that do not produce a meaningful numeric confidence.
type complexityProposal struct {
	Result          *complexity.ComplexityResult
	Mechanism       string
	Score           *float64
	MatchedExemplar string
	LogLevel        schemas.LogLevel
	LogMessage      string
}

func (p *RoutingPlugin) computeComplexity(
	ctx *schemas.BifrostContext,
	req *schemas.BifrostRequest,
	virtualKeyID string,
) *complexity.ComplexityResult {
	input, disposition := complexity.BuildInputWithDisposition(ctx, req)
	sessionID, hasSessionID := complexity.ResolveComplexitySessionID(ctx)
	sessionActive := p.sessionEnabled.Load() && hasSessionID && p.sessionStore != nil

	if disposition != complexity.InputClassifiable {
		if sessionActive && disposition == complexity.InputContinuation {
			key := buildComplexitySessionKey(ctx, virtualKeyID, sessionID)
			tier, found, err := p.sessionStore.load(key, true)
			if err != nil {
				p.logComplexitySessionStoreError("refresh continuation", err)
			} else if found {
				result := &complexity.ComplexityResult{Tier: tier}
				publishComplexityDecision(ctx, result, complexity.MechanismSession, nil)
				ctx.AppendRoutingEngineLog(
					schemas.RoutingEngineRoutingRule,
					schemas.LogLevelInfo,
					fmt.Sprintf("Session complexity reused: effective=%s reason=continuation", tier),
				)
				return result
			}
		}

		publishComplexityDecision(ctx, nil, complexity.MechanismSkipped, nil)
		ctx.AppendRoutingEngineLog(
			schemas.RoutingEngineRoutingRule,
			schemas.LogLevelInfo,
			noClassifiableComplexityInputLog,
		)
		return nil
	}

	if !sessionActive {
		proposal := p.classifyComplexityInput(ctx, input)
		publishComplexityProposal(ctx, proposal)
		return proposal.Result
	}

	key := buildComplexitySessionKey(ctx, virtualKeyID, sessionID)
	priorTier, priorFound, loadErr := p.sessionStore.load(key, false)
	if loadErr != nil {
		p.logComplexitySessionStoreError("inspect", loadErr)
		proposal := p.classifyComplexityInput(ctx, input)
		publishComplexityProposal(ctx, proposal)
		return proposal.Result
	}

	if priorFound && priorTier == complexity.TierComplex {
		resolution, err := p.sessionStore.resolve(key, "")
		if err == nil && resolution.EffectiveTier != "" {
			result := &complexity.ComplexityResult{Tier: resolution.EffectiveTier}
			publishComplexityDecision(ctx, result, complexity.MechanismSession, nil)
			ctx.AppendRoutingEngineLog(
				schemas.RoutingEngineRoutingRule,
				schemas.LogLevelInfo,
				"Session complexity reused: effective=COMPLEX reason=complex-ceiling",
			)
			return result
		}
		if err != nil {
			p.logComplexitySessionStoreError("refresh complex ceiling", err)
		}
		// The state could have expired between inspection and refresh. Classify the
		// current human turn instead of routing from a stale read.
	}

	proposal := p.classifyComplexityInput(ctx, input)
	proposedTier := ""
	if proposal.Result != nil {
		proposedTier = proposal.Result.Tier
	}
	resolution, err := p.sessionStore.resolve(key, proposedTier)
	if err != nil {
		p.logComplexitySessionStoreError("resolve", err)
		return p.publishLocalSessionFallback(ctx, priorTier, priorFound, proposal)
	}

	if resolution.EffectiveTier == "" {
		publishComplexityProposal(ctx, proposal)
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
			schemas.RoutingEngineRoutingRule,
			schemas.LogLevelInfo,
			formatSessionProposalLog(event, resolution.EffectiveTier, previousTier, proposal),
		)
		return proposal.Result
	}

	result := &complexity.ComplexityResult{Tier: resolution.EffectiveTier}
	publishComplexityDecision(ctx, result, complexity.MechanismSession, nil)
	if proposal.Result != nil {
		ctx.AppendRoutingEngineLog(
			schemas.RoutingEngineRoutingRule,
			schemas.LogLevelInfo,
			formatSessionProposalLog("held", resolution.EffectiveTier, "", proposal),
		)
	} else {
		ctx.AppendRoutingEngineLog(
			schemas.RoutingEngineRoutingRule,
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

func (p *RoutingPlugin) classifyComplexityInput(ctx *schemas.BifrostContext, input complexity.ComplexityInput) complexityProposal {
	if p.semanticClassifier == nil || !p.semanticClassifier.IsConfigured() {
		if p.logger != nil {
			p.logger.Debug("[Routing] %s", noSemanticClassifierLog)
		}
		return complexityProposal{
			Mechanism:  complexity.MechanismSkipped,
			LogLevel:   schemas.LogLevelInfo,
			LogMessage: noSemanticClassifierLog,
		}
	}

	semanticResult, err := p.semanticClassifier.Classify(ctx, input)
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
			p.semanticClassifier.Timeout(),
		)
	}

	if p.llmClassifier != nil && p.llmClassifier.FallbackEnabled() {
		ctx.AppendRoutingEngineLog(
			schemas.RoutingEngineRoutingRule,
			schemas.LogLevelInfo,
			unavailableCause+"; falling back to the LLM classifier",
		)
		return p.classifyLLMComplexity(ctx, input)
	}
	return complexityProposal{
		Mechanism:  complexity.MechanismSkipped,
		LogLevel:   unavailableLevel,
		LogMessage: unavailableCause + ", so no complexity tier is published",
	}
}

func (p *RoutingPlugin) publishLocalSessionFallback(
	ctx *schemas.BifrostContext,
	priorTier string,
	priorFound bool,
	proposal complexityProposal,
) *complexity.ComplexityResult {
	if !priorFound {
		publishComplexityProposal(ctx, proposal)
		return proposal.Result
	}
	if proposal.Result != nil && !complexityTierAtLeast(priorTier, proposal.Result.Tier) {
		publishComplexityProposal(ctx, proposal)
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
		schemas.RoutingEngineRoutingRule,
		schemas.LogLevelWarn,
		message,
	)
	return result
}

func (p *RoutingPlugin) logComplexitySessionStoreError(operation string, err error) {
	if p.logger != nil {
		p.logger.Warn("[Routing] complexity session store %s failed: %v", operation, err)
	}
}

func publishComplexityProposal(ctx *schemas.BifrostContext, proposal complexityProposal) {
	publishComplexityDecision(ctx, proposal.Result, proposal.Mechanism, proposal.Score)
	if proposal.LogMessage != "" {
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineRoutingRule, proposal.LogLevel, proposal.LogMessage)
	}
}

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
	if proposal.Score != nil {
		message += fmt.Sprintf(" proposed_similarity=%.2f", *proposal.Score)
	}
	if matched := truncateExemplarForLog(proposal.MatchedExemplar); matched != "" {
		message += fmt.Sprintf(" proposed_matched=%q", matched)
	}
	return message
}
