package routing

import (
	"context"
	"errors"
	"fmt"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
)

// SetSystemOneFunc swaps the decision transport used by the jev classifier.
// The plugin wires complexity.HTTPSystemOneFunc at construction; tests supply
// fakes so classification is provable without a real endpoint. Safe for
// concurrent use with classification and plugin reloads.
func (p *RoutingPlugin) SetSystemOneFunc(fn complexity.SystemOneFunc) {
	p.jevClassifier.SetSystemOneFunc(fn)
}

// computeJev answers the rules engine's lazy jev_* variables. It returns nil —
// which the engine renders as CEL unknowns, so jev predicates simply do not
// match — whenever there is no classifiable input, no verdict, or a transport
// failure. A routing rule must never be able to fail a request, so every
// error path is folded into "no opinion".
func (p *RoutingPlugin) computeJev(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) *complexity.JevResult {
	input, disposition := complexity.BuildInputWithDisposition(ctx, req)
	if disposition != complexity.InputClassifiable {
		return nil
	}
	result, err := p.jevClassifier.Classify(ctx, input)
	if err != nil {
		if p.logger != nil {
			p.logger.Debug("[Routing] Jev decision unavailable: %v", err)
		}
		return nil
	}
	return result
}

// classifyJevComplexity runs the jev fallback classifier for one request —
// always after a semantic non-answer, never as the primary — and returns a
// proposal without publishing context telemetry. The caller applies monotonic
// session state before publishing the effective tier.
func (p *RoutingPlugin) classifyJevComplexity(ctx *schemas.BifrostContext, input complexity.ComplexityInput) complexityProposal {
	result, err := p.jevClassifier.Classify(ctx, input)
	if err == nil && result != nil && result.Tier != "" {
		// No score is published, mirroring the llm fallback: the decision
		// model's complexity score is not a similarity and would invite
		// comparisons against thresholds tuned for the vector backends.
		out := &complexity.ComplexityResult{Tier: result.Tier}
		return complexityProposal{
			Result:     out,
			Mechanism:  complexity.MechanismJev,
			LogLevel:   schemas.LogLevelInfo,
			LogMessage: fmt.Sprintf("Jev complexity: tier=%s confidence=%.2f complexity=%.2f", out.Tier, result.Confidence, result.Complexity),
		}
	}

	if err == nil && result != nil {
		// The decision model answered but withheld the tier (gate failure or
		// unknown tier name). This is routine, not an operator problem: the
		// verdict was not confident enough to publish, so the request keeps
		// whatever the routing rules decide without a complexity tier.
		return complexityProposal{
			Mechanism:  complexity.MechanismSkipped,
			LogLevel:   schemas.LogLevelInfo,
			LogMessage: fmt.Sprintf("Jev complexity withheld: reason=%s confidence=%.2f complexity=%.2f", result.Reason, result.Confidence, result.Complexity),
		}
	}

	if err != nil && p.logger != nil {
		p.logger.Debug("[Routing] Jev complexity classification unavailable: %v", err)
	}
	// One line per decision, naming the cause, mirroring the llm fallback.
	// Every branch here is an operator problem — a missing API key, a budget
	// to raise, an unreachable endpoint — so the cause is named at warn level
	// instead of hiding in a debug-only line.
	unavailableLog := "Jev complexity classification unavailable, so no complexity tier is published"
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		unavailableLog = fmt.Sprintf(
			"Jev complexity classification timed out after %s, so no complexity tier is published",
			p.jevClassifier.Timeout(),
		)
	case errors.Is(err, complexity.ErrJevAPIKeyMissing):
		unavailableLog = "Jev complexity classifier has no API key: set the jev api_key or TYPESAFE_API_KEY; no complexity tier is published"
	case err != nil:
		unavailableLog = fmt.Sprintf("Jev complexity classification unavailable: %v; no complexity tier is published", err)
	}
	return complexityProposal{
		Mechanism:  complexity.MechanismSkipped,
		LogLevel:   schemas.LogLevelWarn,
		LogMessage: unavailableLog,
	}
}
