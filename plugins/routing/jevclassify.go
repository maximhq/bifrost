package routing

import (
	"context"
	"errors"
	"fmt"
	"sync"

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

// jevDecisionMemo memoizes at most one Jev classification per request. The
// rules engine's lazy jev_* variables and the complexity fallback (a semantic
// non-answer retried as jev) both end in JevClassifier.Classify, and the
// classifier is stateless between calls — so a rule referencing both
// complexity_tier and jev_* variables used to send two identical System One
// requests for the same request. Nil results and errors are memoized equally:
// both are real outcomes, not retries. A nil memo computes without sharing.
type jevDecisionMemo struct {
	once   sync.Once
	result *complexity.JevResult
	err    error
}

func (m *jevDecisionMemo) compute(compute func() (*complexity.JevResult, error)) (*complexity.JevResult, error) {
	if m == nil {
		return compute()
	}
	m.once.Do(func() { m.result, m.err = compute() })
	return m.result, m.err
}

// computeJev answers the rules engine's lazy jev_* variables. It returns nil —
// which the engine renders as CEL unknowns, so jev predicates simply do not
// match — whenever there is no classifiable input, no verdict, or a transport
// failure. A routing rule must never be able to fail a request, so every
// error path is folded into "no opinion".
func (p *RoutingPlugin) computeJev(ctx *schemas.BifrostContext, req *schemas.BifrostRequest, memo *jevDecisionMemo) *complexity.JevResult {
	result, _ := memo.compute(func() (*complexity.JevResult, error) {
		input, disposition := complexity.BuildInputWithDisposition(ctx, req)
		if disposition != complexity.InputClassifiable {
			return nil, nil
		}
		result, err := p.jevClassifier.Classify(ctx, input)
		if err != nil && p.logger != nil {
			p.logger.Debug("[Routing] Jev decision unavailable: %v", err)
		}
		return result, err
	})
	return result
}

// classifyJevComplexity runs the jev fallback classifier for one request —
// always after a semantic non-answer, never as the primary — and returns a
// proposal without publishing context telemetry. The caller applies monotonic
// session state before publishing the effective tier. Sharing the request's
// memo means a rule referencing both complexity_tier and jev_* reuses this
// decision instead of sending a second System One request.
func (p *RoutingPlugin) classifyJevComplexity(ctx *schemas.BifrostContext, input complexity.ComplexityInput, memo *jevDecisionMemo) complexityProposal {
	result, err := memo.compute(func() (*complexity.JevResult, error) {
		result, err := p.jevClassifier.Classify(ctx, input)
		if err != nil && p.logger != nil {
			p.logger.Debug("[Routing] Jev complexity classification unavailable: %v", err)
		}
		return result, err
	})
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
