package governance

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/routing"
	"github.com/maximhq/bifrost/plugins/governance/complexity"
)

// DefaultRoutingChainMaxDepth is the default maximum depth for routing rule chain evaluation.
const DefaultRoutingChainMaxDepth = 10

// ScopeLevel represents a level in the scope precedence hierarchy
type ScopeLevel struct {
	ScopeName string // "virtual_key", "team", "customer", or "global"
	ScopeID   string // empty string for global scope
}

// RoutingDecision is the output of routing rule evaluation
// Represents which provider/model to route to and fallback chain
type RoutingDecision struct {
	Provider        string   // Primary provider (e.g., "openai", "azure")
	Model           string   // Model to use (or empty to use original)
	KeyID           string   // Optional: pin a specific API key by UUID ("" = no pin)
	Fallbacks       []string // Fallback chain: ["provider/model", ...]
	MatchedRuleID   string   // ID of the rule that matched
	MatchedRuleName string   // Name of the rule that matched
}

// RoutingContext holds all data needed for routing rule evaluation
// Reuses existing configstore table types for VirtualKey, Team, Customer
type RoutingContext struct {
	VirtualKey               *configstoreTables.TableVirtualKey  // nil if no VK
	UserID                   string                              // Resolved calling user id; empty when the request carries no user identity
	Provider                 schemas.ModelProvider               // Current provider
	Model                    string                              // Current model
	RequestType              string                              // Request type (e.g., "chat_completion", "embedding"); streaming requests carry a distinct "_stream" suffix (e.g., "chat_completion_stream")
	Fallbacks                []string                            // Fallback chain: ["provider/model", ...]
	Headers                  map[string]string                   // Request headers for dynamic routing
	QueryParams              map[string]string                   // Query parameters for dynamic routing
	BudgetAndRateLimitStatus *BudgetAndRateLimitStatus           // Budget and rate limit status by provider/model
	computeComplexity        func() *complexity.ComplexityResult // Lazy complexity computation; called at most once when a rule references "complexity_tier"
	// SessionID identifies the conversation this request belongs to. Empty
	// means "unknown", which is the normal case and simply restores random
	// weighted target selection. Populated from x-bf-session-id or a verified
	// harness-native conversation ID.
	SessionID string
}

type RoutingEngine struct {
	store         GovernanceStore
	logger        schemas.Logger
	chainMaxDepth *int // pointer to live config value; changes are reflected immediately
}

// NewRoutingEngine creates a new RoutingEngine
func NewRoutingEngine(store GovernanceStore, logger schemas.Logger, chainMaxDepth *int) (*RoutingEngine, error) {
	if store == nil {
		return nil, fmt.Errorf("store cannot be nil")
	}
	if logger == nil {
		return nil, fmt.Errorf("logger cannot be nil")
	}
	if chainMaxDepth == nil {
		return nil, fmt.Errorf("chainMaxDepth cannot be nil")
	}
	if *chainMaxDepth <= 0 {
		return nil, fmt.Errorf("chainMaxDepth must be greater than 0")
	}

	return &RoutingEngine{
		store:         store,
		logger:        logger,
		chainMaxDepth: chainMaxDepth,
	}, nil
}

// EvaluateRoutingRules evaluates routing rules for a given context and returns a routing decision.
// Implements scope precedence: VirtualKey > Team > Customer > Global (first-match-wins within each iteration).
// When a matched rule has chain_rule=true, the resolved provider/model is fed back into the evaluator
// and the full scope chain is re-evaluated with the updated context. This repeats until:
//  1. No rule matches the current context
//  2. A terminal rule matches (chain_rule=false, the default)
//  3. Every chain-rule that could match has already fired once (all candidates exhausted)
//  4. The chain exceeds the configured max depth (chainMaxDepth, default 10)
func (re *RoutingEngine) EvaluateRoutingRules(ctx *schemas.BifrostContext, routingCtx *RoutingContext) (*RoutingDecision, error) {
	if routingCtx == nil {
		return nil, fmt.Errorf("routing context cannot be nil")
	}

	re.logger.Debug("[RoutingEngine] Starting rule evaluation for provider=%s, model=%s", routingCtx.Provider, routingCtx.Model)

	// Mutable provider/model that advances through the chain; all other context fields are immutable.
	currentProvider := routingCtx.Provider
	currentModel := routingCtx.Model

	// Track which rule IDs have already fired to prevent a rule from matching more than once per chain.
	// This allows a self-looping rule (target == current state) to fire once and then let subsequent
	// rules in the chain run, rather than halting with a cycle error.
	visitedRuleIDs := map[string]struct{}{}

	// Build scope chain once — it's based on the immutable VirtualKey and user
	// identity and won't change across chain steps.
	scopeChain := buildScopeChain(routingCtx.VirtualKey, routingCtx.UserID)

	// Cache rules per scope upfront to avoid redundant store lookups when rules chain
	// and we re-evaluate the scope hierarchy on subsequent steps.
	rulesPerScope := make(map[ScopeLevel][]*configstoreTables.TableRoutingRule, len(scopeChain))
	for _, scope := range scopeChain {
		rules := re.store.GetScopedRoutingRules(ctx, scope.ScopeName, scope.ScopeID)
		if len(rules) == 0 {
			continue
		}
		re.logger.Debug("[RoutingEngine] Loaded %d rules for scope=%s, scopeID=%s", len(rules), scope.ScopeName, scope.ScopeID)
		rulesPerScope[scope] = rules
	}

	if len(rulesPerScope) == 0 {
		re.logger.Debug("[RoutingEngine] No routing rules found for any scope, skipping evaluation")
		return nil, nil
	}

	ctx.AppendRoutingEngineLog(schemas.RoutingEngineRoutingRule, schemas.LogLevelInfo,
		fmt.Sprintf("Evaluating routing rules for model=%s, provider=%s, requestType=%s", routingCtx.Model, routingCtx.Provider, routingCtx.RequestType))
	ctx.AppendRoutingEngineLog(schemas.RoutingEngineRoutingRule, schemas.LogLevelInfo, fmt.Sprintf("Scope chain: %v", scopeChainToStrings(scopeChain)))

	var finalDecision *RoutingDecision
	var complexityResult *complexity.ComplexityResult
	computeComplexity := routingCtx.computeComplexity

	for chainStep := 0; ; chainStep++ {
		// TERMINATION 4: Chain exceeded configured max depth.
		maxDepth := *re.chainMaxDepth
		if chainStep >= maxDepth {
			re.logger.Warn("[RoutingEngine] Routing rule chain exceeded max depth (%d), stopping", maxDepth)
			ctx.AppendRoutingEngineLog(schemas.RoutingEngineRoutingRule, schemas.LogLevelWarn, fmt.Sprintf("Chain exceeded max depth (%d) at step %d, stopping. Final resolved: provider=%s, model=%s", maxDepth, chainStep, currentProvider, currentModel))
			break
		}

		if chainStep > 0 {
			ctx.AppendRoutingEngineLog(schemas.RoutingEngineRoutingRule, schemas.LogLevelInfo, fmt.Sprintf("Chain step %d: re-evaluating with provider=%s, model=%s", chainStep, currentProvider, currentModel))
		}

		// Build CEL variables for the current chain step's provider/model.
		iterCtx := *routingCtx
		iterCtx.Provider = currentProvider
		iterCtx.Model = currentModel
		// Refresh budget/rate-limit status for the current provider/model so chained
		// rules that test budget_used, tokens_used, or request see fresh data.
		iterCtx.BudgetAndRateLimitStatus = re.store.GetBudgetAndRateLimitStatus(ctx, currentModel, currentProvider, routingCtx.VirtualKey, nil, nil, nil)

		variables, err := extractRoutingVariables(&iterCtx)
		if err != nil {
			re.logger.Error("[RoutingEngine] Failed to extract routing variables: %v", err)
			ctx.AppendRoutingEngineLog(schemas.RoutingEngineRoutingRule, schemas.LogLevelError, fmt.Sprintf("Failed to extract routing variables: %v", err))
			return nil, fmt.Errorf("failed to extract routing variables: %w", err)
		}
		if complexityResult != nil {
			variables["complexity_tier"] = complexityResult.Tier
		}

		re.logger.Debug("[RoutingEngine] Chain Step: %d", chainStep)

		var stepDecision *RoutingDecision
		var matchedRule *configstoreTables.TableRoutingRule
		var matchedTargetWeight float64

	outerLoop:
		for _, scope := range scopeChain {
			rules, ok := rulesPerScope[scope]
			if !ok {
				continue
			}
			re.logger.Debug("[RoutingEngine] Evaluating scope=%s, scopeID=%s, ruleCount=%d", scope.ScopeName, scope.ScopeID, len(rules))

			ruleNames := make([]string, 0, len(rules))
			for _, r := range rules {
				ruleNames = append(ruleNames, r.Name)
			}

			ctx.AppendRoutingEngineLog(schemas.RoutingEngineRoutingRule, schemas.LogLevelInfo, fmt.Sprintf("Evaluating scope %s: %d rules [%s]", scope.ScopeName, len(rules), strings.Join(ruleNames, ", ")))

			for _, rule := range rules {
				if _, fired := visitedRuleIDs[rule.ID]; fired {
					re.logger.Debug("[RoutingEngine] Skipping rule %s (already fired this chain)", rule.Name)
					ctx.AppendRoutingEngineLog(schemas.RoutingEngineRoutingRule, schemas.LogLevelInfo, fmt.Sprintf("Rule '%s' skipped: already fired in this chain", rule.Name))
					continue
				}
				re.logger.Debug("[RoutingEngine] Evaluating rule: name=%s, expression=%s", rule.Name, rule.CelExpression)

				referencesComplexity := celExpressionReferencesIdentifier(rule.CelExpression, "complexity_tier")

				// Lazy complexity: compute only when a rule references complexity and it hasn't been computed yet
				if complexityResult == nil && computeComplexity != nil && referencesComplexity {
					complexityResult = computeComplexity()
					computeComplexity = nil // compute at most once
					if complexityResult != nil {
						variables["complexity_tier"] = complexityResult.Tier
					}
				}

				program, err := re.store.GetRoutingProgram(ctx, rule)
				if err != nil {
					re.logger.Warn("[RoutingEngine] Failed to compile rule %s: %v", rule.Name, err)
					ctx.AppendRoutingEngineLog(schemas.RoutingEngineRoutingRule, schemas.LogLevelError, fmt.Sprintf("Rule '%s' skipped: compile error: %v", rule.Name, err))
					continue
				}

				var unknowns []*cel.AttributePatternType
				if referencesComplexity && complexityResult == nil {
					// Covers both "classifier ran but produced no tier" (already
					// recorded by the compute closure) and "analyzer disabled but a
					// rule demands complexity_tier" — either way the request log
					// should show the classification was skipped.
					ctx.SetValue(schemas.BifrostContextKeyGovernanceComplexityMechanism, complexity.MechanismSkipped)
					unknowns = append(unknowns, cel.AttributePattern("complexity_tier"))
				}

				matched, err := evaluateCELExpression(program, variables, unknowns...)
				if err != nil {
					re.logger.Warn("[RoutingEngine] Failed to evaluate rule %s: %v", rule.Name, err)
					ctx.AppendRoutingEngineLog(schemas.RoutingEngineRoutingRule, schemas.LogLevelError, fmt.Sprintf("Rule '%s' skipped: eval error: %v", rule.Name, err))
					continue
				}

				re.logger.Debug("[RoutingEngine] Rule %s evaluation result: matched=%v", rule.Name, matched)

				if !matched {
					ctx.AppendRoutingEngineLog(schemas.RoutingEngineRoutingRule, schemas.LogLevelInfo,
						fmt.Sprintf("Rule '%s' [%s] → no match (%s)", rule.Name, rule.CelExpression, buildNoMatchContext(rule.CelExpression, variables)))
					continue
				}

				target, ok := selectWeightedTarget(rule.Targets, routingCtx.SessionID, rule.ID)
				if !ok {
					re.logger.Debug("[RoutingEngine] Rule %s matched but has no valid targets (empty list or all-negative weights), skipping — note: all-zero weights use uniform selection and would not reach here", rule.Name)
					ctx.AppendRoutingEngineLog(schemas.RoutingEngineRoutingRule, schemas.LogLevelError, fmt.Sprintf("Rule '%s' [%s] → matched but no valid targets (empty or all-negative weights), skipping", rule.Name, rule.CelExpression))
					continue
				}

				provider := string(currentProvider)
				if target.Provider != nil && *target.Provider != "" {
					provider = *target.Provider
				}

				model := currentModel
				if target.Model != nil && *target.Model != "" {
					model = *target.Model
				}

				keyID := ""
				if target.KeyID != nil {
					keyID = *target.KeyID
				}

				stepDecision = &RoutingDecision{
					Provider:        provider,
					Model:           model,
					KeyID:           keyID,
					Fallbacks:       rule.ParsedFallbacks,
					MatchedRuleID:   rule.ID,
					MatchedRuleName: rule.Name,
				}
				matchedRule = rule
				matchedTargetWeight = target.Weight
				break outerLoop
			}
		}

		// TERMINATION 1: No rule matched this iteration.
		if stepDecision == nil {
			break
		}

		// Accumulate: last match wins for all fields.
		finalDecision = stepDecision
		ctx.SetValue(schemas.BifrostContextKeyGovernanceRoutingRuleID, stepDecision.MatchedRuleID)
		ctx.SetValue(schemas.BifrostContextKeyGovernanceRoutingRuleName, stepDecision.MatchedRuleName)

		chainSuffix := ""
		if matchedRule.ChainRule {
			chainSuffix = " [chain_rule=true, continuing]"
		}
		re.logger.Debug("[RoutingEngine] Rule matched! Selected target (weight=%.2f): provider=%s, model=%s, fallbacks=%v%s", matchedTargetWeight, stepDecision.Provider, stepDecision.Model, stepDecision.Fallbacks, chainSuffix)
		ctx.AppendRoutingEngineLog(schemas.RoutingEngineRoutingRule, schemas.LogLevelInfo, fmt.Sprintf("Rule '%s' [%s] → matched, selected target (weight=%.2f): provider=%s, model=%s, fallbacks=%v%s", matchedRule.Name, matchedRule.CelExpression, matchedTargetWeight, stepDecision.Provider, stepDecision.Model, stepDecision.Fallbacks, chainSuffix))

		// TERMINATION 2: Rule is terminal (chain_rule=false, the default).
		if !matchedRule.ChainRule {
			break
		}

		// Mark this chain-rule as fired; it will be skipped in all subsequent chain steps.
		visitedRuleIDs[matchedRule.ID] = struct{}{}

		// Advance context for next chain iteration.
		currentProvider = schemas.ModelProvider(stepDecision.Provider)
		currentModel = stepDecision.Model
	}

	if finalDecision == nil {
		re.logger.Debug("[RoutingEngine] No routing rule matched, using default routing")
	}
	return finalDecision, nil
}

// selectWeightedTarget picks one target from the slice using weighted selection.
// Each target's Weight contributes proportionally to its probability of being chosen.
// Weights do not need to be normalised to 100; the function normalises internally.
// Returns ok=false only when len(targets)==0 or all targets have negative weights (filtered out).
// When all valid targets have weight==0 the function falls back to uniform selection
// and still returns ok=true, so zero-weight targets are valid and handled.
//
// When sessionID is non-empty the selection point is derived from
// hash(sessionID, ruleID) instead of rand, so every request in a session picks
// the same target for a given rule. That stickiness is what makes tier state
// worth having: a tier resolving to two weighted targets would otherwise re-roll
// every turn and discard the provider-side prompt cache anyway, one layer below
// the tier. The hash is stateless, so it agrees across nodes and survives
// restarts without storage, and it stays proportional as weights change.
func selectWeightedTarget(targets []configstoreTables.TableRoutingTarget, sessionID, ruleID string) (configstoreTables.TableRoutingTarget, bool) {
	if len(targets) == 0 {
		return configstoreTables.TableRoutingTarget{}, false
	}

	// Filter out negative weights as a precaution against malformed DB data.
	// Negative weights are blocked at write time by validateRoutingTargets, but
	// we guard here defensively so a bad row cannot corrupt the cumulative range.
	valid := make([]configstoreTables.TableRoutingTarget, 0, len(targets))
	for _, t := range targets {
		if t.Weight >= 0 {
			valid = append(valid, t)
		}
	}
	if len(valid) == 0 {
		return configstoreTables.TableRoutingTarget{}, false
	}

	if len(valid) == 1 {
		return valid[0], true
	}

	sticky := sessionID != ""
	if sticky {
		// The cumulative walk below maps a point in [0,1) to a position in the
		// slice, so a stable point only yields a stable target if the slice
		// order is also stable. Targets carry no ID column and arrive in
		// whatever order the DB returned them, so sort by the columns that
		// uniquely identify a target within a rule. Without this, stickiness
		// silently degrades to random on any query that reorders rows.
		sort.SliceStable(valid, func(i, j int) bool {
			return routingTargetIdentity(valid[i]) < routingTargetIdentity(valid[j])
		})
	}

	total := 0.0
	for _, t := range valid {
		total += t.Weight
	}

	// All weights are 0 — select uniformly among valid targets.
	if total == 0 {
		if sticky {
			index := int(sessionSelectionPoint(sessionID, ruleID) * float64(len(valid)))
			if index >= len(valid) {
				index = len(valid) - 1
			}
			return valid[index], true
		}
		return valid[rand.IntN(len(valid))], true
	}

	point := rand.Float64()
	if sticky {
		point = sessionSelectionPoint(sessionID, ruleID)
	}

	r := point * total
	cumulative := 0.0
	for _, t := range valid {
		cumulative += t.Weight
		if r < cumulative {
			return t, true
		}
	}
	return valid[len(valid)-1], true
}

// routingTargetIdentity renders the columns of the routing target unique index
// as a sortable string.
//
// A nil field and an empty one collapse to the same identity, deliberately:
// both mean "inherit from the request" at selection time, so two targets
// differing only that way resolve to the same provider, model, and key. Sorting
// them as equal is therefore stable in the only sense that matters.
func routingTargetIdentity(target configstoreTables.TableRoutingTarget) string {
	var b strings.Builder
	for _, field := range []*string{target.Provider, target.Model, target.KeyID} {
		if field != nil {
			b.WriteString(*field)
		}
		b.WriteByte(0)
	}
	return b.String()
}

// sessionSelectionPoint maps a session and rule to a uniform point in [0,1).
//
// Keying on the rule as well as the session means a session that matches two
// different rules gets two independent draws, rather than landing on the same
// slice position in both — which would correlate the choices and skew the
// aggregate distribution away from the configured weights.
func sessionSelectionPoint(sessionID, ruleID string) float64 {
	sum := sha256.Sum256([]byte(sessionID + "\x00" + ruleID))
	// Take 53 bits, the exact-integer range of a float64, so every
	// representable value is equally likely and the result is never 1.0.
	return float64(binary.BigEndian.Uint64(sum[:8])>>11) / float64(uint64(1)<<53)
}

// buildScopeChain builds the scope evaluation chain based on organizational hierarchy
// Returns scope levels in precedence order (highest to lowest)
// VirtualKey > User > Team > Customer > Global
func buildScopeChain(virtualKey *configstoreTables.TableVirtualKey, userID string) []ScopeLevel {
	var chain []ScopeLevel

	// VirtualKey level (highest precedence)
	if virtualKey != nil {
		chain = append(chain, ScopeLevel{
			ScopeName: "virtual_key",
			ScopeID:   virtualKey.ID,
		})
	}

	// User level: the resolved calling user. Independent of VK presence so
	// session-authenticated requests without a virtual key still match
	// user-scoped rules.
	if userID != "" {
		chain = append(chain, ScopeLevel{
			ScopeName: "user",
			ScopeID:   userID,
		})
	}

	if virtualKey != nil {
		// Team level
		if virtualKey.Team != nil {
			chain = append(chain, ScopeLevel{
				ScopeName: "team",
				ScopeID:   virtualKey.Team.ID,
			})

			// Customer level (from Team)
			if virtualKey.Team.Customer != nil {
				chain = append(chain, ScopeLevel{
					ScopeName: "customer",
					ScopeID:   virtualKey.Team.Customer.ID,
				})
			}
		} else if virtualKey.Customer != nil {
			// Customer level (VK attached directly to customer, no Team)
			chain = append(chain, ScopeLevel{
				ScopeName: "customer",
				ScopeID:   virtualKey.Customer.ID,
			})
		}
	}

	// Global level (lowest precedence)
	chain = append(chain, ScopeLevel{
		ScopeName: "global",
		ScopeID:   "",
	})

	return chain
}

// evaluateCELExpression evaluates a compiled CEL program with given variables
func evaluateCELExpression(program cel.Program, variables map[string]any, unknowns ...*cel.AttributePatternType) (bool, error) {
	if program == nil {
		return false, fmt.Errorf("CEL program is nil")
	}

	activation := any(variables)
	if len(unknowns) > 0 {
		partial, err := cel.PartialVars(variables, unknowns...)
		if err != nil {
			return false, fmt.Errorf("CEL partial activation error: %w", err)
		}
		activation = partial
	}

	// Evaluate the program
	out, _, err := program.Eval(activation)
	if err != nil {
		// Gracefully handle "no such key" errors - when a header/param is missing, treat as non-match
		if strings.Contains(err.Error(), "no such key") {
			return false, nil
		}
		return false, fmt.Errorf("CEL evaluation error: %w", err)
	}

	// Unknown means the expression depends on a value that is unavailable for
	// this request. For routing safety, treat it as a no-match rather than
	// allowing sentinels like complexity_tier == "" to leak into product logic.
	if types.IsUnknown(out) {
		return false, nil
	}

	// Convert result to boolean
	matched, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("CEL expression did not return boolean, got: %T", out.Value())
	}

	return matched, nil
}

// extractRoutingVariables builds a map of CEL variables from routing context
// This map is used to evaluate CEL expressions in routing rules
func extractRoutingVariables(ctx *RoutingContext) (map[string]interface{}, error) {
	if ctx == nil {
		return nil, fmt.Errorf("routing context cannot be nil")
	}

	variables := make(map[string]interface{})

	// Basic request context
	variables["model"] = ctx.Model
	variables["provider"] = string(ctx.Provider)
	variables["request_type"] = ctx.RequestType // Request type as-is; streaming variants keep their "_stream" suffix (e.g., "chat_completion_stream")

	// Headers and params - normalize headers to lowercase keys for case-insensitive CEL matching
	// This allows CEL expressions like headers["content-type"] to work regardless of how the header was sent
	normalizedHeaders := make(map[string]string)
	if ctx.Headers != nil {
		for k, v := range ctx.Headers {
			// Store with lowercase key for case-insensitive matching in CEL
			normalizedHeaders[strings.ToLower(k)] = v
		}
	}
	variables["headers"] = normalizedHeaders

	// Normalize query params to lowercase keys for case-insensitive CEL matching
	normalizedParams := make(map[string]string)
	if ctx.QueryParams != nil {
		for k, v := range ctx.QueryParams {
			normalizedParams[strings.ToLower(k)] = v
		}
	}
	variables["params"] = normalizedParams

	// Extract VirtualKey context if available
	if ctx.VirtualKey != nil {
		variables["virtual_key_id"] = ctx.VirtualKey.ID
		variables["virtual_key_name"] = ctx.VirtualKey.Name
	} else {
		variables["virtual_key_id"] = ""
		variables["virtual_key_name"] = ""
	}

	// Resolved calling user id; empty when the request carries no user identity
	variables["user_id"] = ctx.UserID

	// Extract Team context if available (from VirtualKey)
	if ctx.VirtualKey != nil && ctx.VirtualKey.Team != nil {
		variables["team_id"] = ctx.VirtualKey.Team.ID
		variables["team_name"] = ctx.VirtualKey.Team.Name
	} else {
		variables["team_id"] = ""
		variables["team_name"] = ""
	}

	// Extract Customer context if available (from Team or directly from VirtualKey)
	if ctx.VirtualKey != nil {
		if ctx.VirtualKey.Team != nil && ctx.VirtualKey.Team.Customer != nil {
			variables["customer_id"] = ctx.VirtualKey.Team.Customer.ID
			variables["customer_name"] = ctx.VirtualKey.Team.Customer.Name
		} else if ctx.VirtualKey.Customer != nil {
			variables["customer_id"] = ctx.VirtualKey.Customer.ID
			variables["customer_name"] = ctx.VirtualKey.Customer.Name
		} else {
			variables["customer_id"] = ""
			variables["customer_name"] = ""
		}
	} else {
		variables["customer_id"] = ""
		variables["customer_name"] = ""
	}

	// Populate budget and rate limit variables for current provider/model combination
	if ctx.BudgetAndRateLimitStatus != nil {
		variables["budget_used"] = ctx.BudgetAndRateLimitStatus.BudgetPercentUsed
		variables["tokens_used"] = ctx.BudgetAndRateLimitStatus.RateLimitTokenPercentUsed
		variables["request"] = ctx.BudgetAndRateLimitStatus.RateLimitRequestPercentUsed
	} else {
		// No budget/rate limit configured, provide 0 values
		variables["budget_used"] = 0.0
		variables["tokens_used"] = 0.0
		variables["request"] = 0.0
	}

	// Placeholder only: EvaluateRoutingRules fills this lazily when a rule
	// actually references complexity_tier. If complexity is unavailable, it is
	// evaluated as a CEL unknown so negative predicates do not accidentally match.
	variables["complexity_tier"] = ""

	return variables, nil
}

// scopeChainToStrings converts a scope chain to a string representation for logging
func scopeChainToStrings(chain []ScopeLevel) []string {
	scopes := make([]string, 0, len(chain))
	for _, scope := range chain {
		if scope.ScopeID == "" {
			scopes = append(scopes, scope.ScopeName)
		} else {
			scopes = append(scopes, fmt.Sprintf("%s(%s)", scope.ScopeName, scope.ScopeID))
		}
	}
	return scopes
}

// buildNoMatchContext builds a compact debug string of scalar variables plus
// only the headers/params keys actually referenced in the CEL expression.
func buildNoMatchContext(expr string, variables map[string]any) string {
	parts := []string{
		fmt.Sprintf("model=%q", variables["model"]),
		fmt.Sprintf("provider=%q", variables["provider"]),
		fmt.Sprintf("request_type=%q", variables["request_type"]),
		fmt.Sprintf("budget_used=%.1f%%", variables["budget_used"]),
		fmt.Sprintf("tokens_used=%.1f%%", variables["tokens_used"]),
		fmt.Sprintf("request=%.1f%%", variables["request"]),
	}
	for _, mapName := range []string{"headers", "params"} {
		keys := extractMapKeysFromCEL(expr, mapName)
		if len(keys) == 0 {
			continue
		}
		if m, ok := variables[mapName].(map[string]string); ok {
			kvs := make([]string, 0, len(keys))
			for _, k := range keys {
				if _, exists := m[k]; exists {
					kvs = append(kvs, k+"=<present>")
				} else {
					kvs = append(kvs, k+"=<missing>")
				}
			}
			parts = append(parts, mapName+"("+strings.Join(kvs, ", ")+")")
		}
	}
	return strings.Join(parts, ", ")
}

// celMapKeyRegexCache caches one *regexp.Regexp per mapName to avoid
// recompiling on every call. Lazy and concurrent-safe via sync.Map's
// LoadOrStore atomicity; benign duplicate compiles on first concurrent miss.
var celMapKeyRegexCache sync.Map // map[string]*regexp.Regexp

// extractMapKeysFromCEL extracts unique map access keys for mapName from a CEL expression.
// Handles mapName["key"], mapName['key'], and mapName.key patterns.
func extractMapKeysFromCEL(expr, mapName string) []string {
	v, ok := celMapKeyRegexCache.Load(mapName)
	if !ok {
		quoted := regexp.QuoteMeta(mapName)
		compiled := regexp.MustCompile(quoted + `\["([^"]+)"\]|` + quoted + `\['([^']+)'\]|` + quoted + `\.([a-zA-Z_][a-zA-Z0-9_]*)`)
		v, _ = celMapKeyRegexCache.LoadOrStore(mapName, compiled)
	}
	re := v.(*regexp.Regexp)
	seen := map[string]struct{}{}
	var keys []string
	for _, m := range re.FindAllStringSubmatch(expr, -1) {
		for _, cap := range m[1:] {
			if cap != "" {
				if _, dup := seen[cap]; !dup {
					seen[cap] = struct{}{}
					keys = append(keys, cap)
				}
				break
			}
		}
	}
	return keys
}

var (
	routingValidationEnv     *cel.Env
	routingValidationEnvErr  error
	routingValidationEnvOnce sync.Once
)

// ValidateRoutingCELExpression compiles a routing CEL expression against the routing
// environment and returns an error describing any syntax or type problem. An empty (or
// whitespace-only) expression is treated as match-all ("true") and is considered valid.
//
// This mirrors the compilation performed lazily in GetRoutingProgram so that HTTP handlers
// can reject a malformed expression at write time instead of it silently failing at first
// evaluation. The environment is built once and reused across calls.
func ValidateRoutingCELExpression(expr string) error {
	if strings.TrimSpace(expr) == "" {
		return nil
	}

	routingValidationEnvOnce.Do(func() {
		routingValidationEnv, routingValidationEnvErr = createCELEnvironment()
	})
	if routingValidationEnvErr != nil {
		return fmt.Errorf("failed to initialize CEL environment: %w", routingValidationEnvErr)
	}

	// Match the normalization and format check applied at evaluation time.
	normalized := routing.NormalizeMapKeysInCEL(expr)
	if err := routing.ValidateCELExpression(normalized); err != nil {
		return err
	}

	ast, issues := routingValidationEnv.Compile(normalized)
	if issues != nil && issues.Err() != nil {
		return fmt.Errorf("CEL compile error: %s", issues.Err().Error())
	}

	// Semantic lint: complexity_tier has a closed value set, and a comparison
	// against anything else compiles fine but can never match at runtime.
	validTiers := map[string]struct{}{
		complexity.TierSimple:  {},
		complexity.TierMedium:  {},
		complexity.TierComplex: {},
	}
	if invalid := invalidComplexityTierLiterals(ast, validTiers); len(invalid) > 0 {
		return fmt.Errorf(
			"complexity_tier is compared against invalid value(s): %q; valid tiers are %s, %s, and %s",
			invalid, complexity.TierSimple, complexity.TierMedium, complexity.TierComplex,
		)
	}
	return nil
}

// createCELEnvironment creates a new CEL environment for routing rules
func createCELEnvironment() (*cel.Env, error) {
	return cel.NewEnv(
		// Basic request context
		cel.Variable("model", cel.StringType),
		cel.Variable("provider", cel.StringType),
		cel.Variable("request_type", cel.StringType), // Request type (e.g., "chat_completion", "embedding"); streaming variants are distinct values with a "_stream" suffix

		// Headers and params (dynamic from request)
		cel.Variable("headers", cel.MapType(cel.StringType, cel.StringType)),
		cel.Variable("params", cel.MapType(cel.StringType, cel.StringType)),

		// VirtualKey/Team/Customer context
		cel.Variable("virtual_key_id", cel.StringType),
		cel.Variable("virtual_key_name", cel.StringType),
		cel.Variable("user_id", cel.StringType),
		cel.Variable("team_id", cel.StringType),
		cel.Variable("team_name", cel.StringType),
		cel.Variable("customer_id", cel.StringType),
		cel.Variable("customer_name", cel.StringType),

		// Rate limit & budget status (real-time capacity metrics as percentages)
		cel.Variable("tokens_used", cel.DoubleType),
		cel.Variable("request", cel.DoubleType),
		cel.Variable("budget_used", cel.DoubleType),

		// Complexity tier. When analysis is unavailable, evaluation marks this
		// variable as CEL unknown so complexity-dependent predicates do not match.
		cel.Variable("complexity_tier", cel.StringType),
	)
}
