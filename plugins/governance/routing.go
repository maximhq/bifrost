package governance

import (
	"fmt"
	"strings"

	"github.com/google/cel-go/cel"
	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
)

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
	Fallbacks       []string // Fallback chain: ["provider/model", ...]
	MatchedRuleID   string   // ID of the rule that matched
	MatchedRuleName string   // Name of the rule that matched
}

// RoutingContext holds all data needed for routing rule evaluation
// Reuses existing configstore table types for VirtualKey, Team, Customer
type RoutingContext struct {
	VirtualKey               *configstoreTables.TableVirtualKey // nil if no VK
	Provider                 schemas.ModelProvider              // Current provider
	Model                    string                             // Current model
	Fallbacks                []string                           // Fallback chain: ["provider/model", ...]
	Headers                  map[string]string                  // Request headers for dynamic routing
	QueryParams              map[string]string                  // Query parameters for dynamic routing
	BudgetAndRateLimitStatus *BudgetAndRateLimitStatus          // Budget and rate limit status by provider/model
}

type RoutingEngine struct {
	store  GovernanceStore
	logger schemas.Logger
}

// NewRoutingEngine creates a new RoutingEngine
func NewRoutingEngine(store GovernanceStore, logger schemas.Logger) (*RoutingEngine, error) {
	if store == nil {
		return nil, fmt.Errorf("store cannot be nil")
	}

	if logger == nil {
		return nil, fmt.Errorf("logger cannot be nil")
	}

	return &RoutingEngine{
		store:  store,
		logger: logger,
	}, nil
}

// EvaluateRoutingRules evaluates routing rules for a given context and returns routing decision
// Implements scope precedence: VirtualKey > Team > Customer > Global (first-match-wins)
func (re *RoutingEngine) EvaluateRoutingRules(ctx *schemas.BifrostContext, routingCtx *RoutingContext) (*RoutingDecision, error) {
	if routingCtx == nil {
		return nil, fmt.Errorf("routing context cannot be nil")
	}

	re.logger.Debug("[RoutingEngine] Starting rule evaluation for provider=%s, model=%s", routingCtx.Provider, routingCtx.Model)

	// Extract CEL variables from routing context
	variables, err := extractRoutingVariables(routingCtx)
	if err != nil {
		re.logger.Error("[RoutingEngine] Failed to extract routing variables: %v", err)
		return nil, fmt.Errorf("failed to extract routing variables: %w", err)
	}

	// Determine scope chain based on organizational hierarchy
	scopeChain := buildScopeChain(routingCtx.VirtualKey)
	re.logger.Debug("[RoutingEngine] Scope chain: %v", scopeChainToStrings(scopeChain))

	// Evaluate rules in scope precedence order (first-match-wins)
	for _, scope := range scopeChain {
		scopeID := scope.ScopeID

		// Get all enabled rules for this scope, ordered by priority ASC
		rules := re.store.GetScopedRoutingRules(scope.ScopeName, scopeID)
		re.logger.Debug("[RoutingEngine] Evaluating scope=%s, scopeID=%s, ruleCount=%d", scope.ScopeName, scopeID, len(rules))

		// Evaluate each rule
		for _, rule := range rules {
			re.logger.Debug("[RoutingEngine] Evaluating rule: id=%s, name=%s, expression=%s", rule.ID, rule.Name, rule.CelExpression)

			// Get or compile and cache the CEL program
			program, err := re.store.GetRoutingProgram(rule)
			if err != nil {
				re.logger.Warn("[RoutingEngine] Failed to compile rule %s: %v", rule.ID, err)
				continue
			}

			// Evaluate the CEL expression
			matched, err := evaluateCELExpression(program, variables)
			if err != nil {
				re.logger.Warn("[RoutingEngine] Failed to evaluate rule %s: %v", rule.ID, err)
				continue
			}

			re.logger.Debug("[RoutingEngine] Rule %s evaluation result: matched=%v", rule.ID, matched)

			// If rule matched, return routing decision
			if matched {
				// Use incoming provider/model if rule's are empty
				provider := rule.Provider
				if provider == "" {
					provider = string(routingCtx.Provider)
				}

				model := rule.Model
				if model == "" {
					model = routingCtx.Model
				}

				decision := &RoutingDecision{
					Provider:        provider,
					Model:           model,
					Fallbacks:       rule.ParsedFallbacks,
					MatchedRuleID:   rule.ID,
					MatchedRuleName: rule.Name,
				}

				ctx.SetValue(schemas.BifrostContextKeyGovernanceRoutingRuleID, rule.ID)
				ctx.SetValue(schemas.BifrostContextKeyGovernanceRoutingRuleName, rule.Name)

				re.logger.Debug("[RoutingEngine] Rule matched! Decision: provider=%s, model=%s, fallbacks=%v", provider, model, rule.ParsedFallbacks)
				return decision, nil
			}

		}
	}

	// No rule matched - return nil decision (caller should use default routing)
	re.logger.Debug("[RoutingEngine] No routing rule matched, using default routing")
	return nil, nil
}

// buildScopeChain builds the scope evaluation chain based on organizational hierarchy
// Returns scope levels in precedence order (highest to lowest)
// VirtualKey > Team > Customer > Global
func buildScopeChain(virtualKey *configstoreTables.TableVirtualKey) []ScopeLevel {
	var chain []ScopeLevel

	// VirtualKey level (highest precedence)
	if virtualKey != nil {
		chain = append(chain, ScopeLevel{
			ScopeName: "virtual_key",
			ScopeID:   virtualKey.ID,
		})

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
func evaluateCELExpression(program cel.Program, variables map[string]interface{}) (bool, error) {
	if program == nil {
		return false, fmt.Errorf("CEL program is nil")
	}

	// Evaluate the program
	out, _, err := program.Eval(variables)
	if err != nil {
		// Gracefully handle "no such key" errors - when a header/param is missing, treat as non-match
		if strings.Contains(err.Error(), "no such key") {
			return false, nil
		}
		return false, fmt.Errorf("CEL evaluation error: %w", err)
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

	if ctx.QueryParams == nil {
		ctx.QueryParams = make(map[string]string)
	}
	variables["params"] = ctx.QueryParams

	// Extract VirtualKey context if available
	if ctx.VirtualKey != nil {
		variables["virtual_key_id"] = ctx.VirtualKey.ID
		variables["virtual_key_name"] = ctx.VirtualKey.Name
	} else {
		variables["virtual_key_id"] = ""
		variables["virtual_key_name"] = ""
	}

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

// validateCELExpression performs basic validation on CEL expression format
func validateCELExpression(expr string) error {
	if expr == "" || expr == "true" || expr == "false" {
		return nil // Empty, true, or false are valid
	}

	// List of allowed operators and keywords
	validPatterns := []string{
		"==", "!=", "&&", "||", ">", "<", ">=", "<=",
		"in ", "matches ", ".startsWith(", ".contains(", ".endsWith(",
		"[", "]", "(", ")", "!",
	}

	// Check if expression contains at least one valid operator
	hasPattern := false
	for _, pattern := range validPatterns {
		if strings.Contains(expr, pattern) {
			hasPattern = true
			break
		}
	}

	if !hasPattern {
		return fmt.Errorf("expression must contain at least one operator: %s", expr)
	}

	return nil
}

// createCELEnvironment creates a new CEL environment for routing rules
func createCELEnvironment() (*cel.Env, error) {
	return cel.NewEnv(
		// Basic request context
		cel.Variable("model", cel.StringType),
		cel.Variable("provider", cel.StringType),

		// Headers and params (dynamic from request)
		cel.Variable("headers", cel.MapType(cel.StringType, cel.StringType)),
		cel.Variable("params", cel.MapType(cel.StringType, cel.StringType)),

		// VirtualKey/Team/Customer context
		cel.Variable("virtual_key_id", cel.StringType),
		cel.Variable("virtual_key_name", cel.StringType),
		cel.Variable("team_id", cel.StringType),
		cel.Variable("team_name", cel.StringType),
		cel.Variable("customer_id", cel.StringType),
		cel.Variable("customer_name", cel.StringType),

		// Rate limit & budget status (real-time capacity metrics as percentages)
		cel.Variable("tokens_used", cel.DoubleType),
		cel.Variable("request", cel.DoubleType),
		cel.Variable("budget_used", cel.DoubleType),
	)
}
