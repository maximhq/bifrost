package rules

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/google/cel-go/cel"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/routing"
)

// globalScope is the scope name for rules that apply to every request.
const globalScope = "global"

// Store owns the in-memory routing rule set and the compiled CEL programs derived
// from it. Rules are grouped by scope so evaluation walks the scope chain with one map
// lookup per level, and each group is kept sorted by priority ASC (0 is highest) so the
// evaluator can take the first match without re-sorting per request.
type Store interface {
	// HasRules reports whether any rule is cached. It is the per-request gate: when it
	// returns false the request skips rule evaluation entirely.
	HasRules(ctx context.Context) bool
	// GetAllRules returns every cached rule, sorted by priority ascending and, within a
	// priority, newest first.
	GetAllRules(ctx context.Context) []*configstoreTables.TableRoutingRule
	// GetScopedRules returns the enabled rules for one scope level, priority ASC.
	GetScopedRules(ctx context.Context, scope string, scopeID string) []*configstoreTables.TableRoutingRule
	// GetProgram returns the compiled CEL program for a rule, compiling and caching on
	// first use.
	GetProgram(ctx context.Context, rule *configstoreTables.TableRoutingRule) (cel.Program, error)
	// UpsertRule replaces a rule in the cache, moving it between scopes when its scope
	// changed, and recompiles its program.
	UpsertRule(ctx context.Context, rule *configstoreTables.TableRoutingRule) error
	// DeleteRule drops a rule and its compiled program from the cache.
	DeleteRule(ctx context.Context, id string) error
}

// LocalStore is the in-process Store implementation. Reads are lock-free: both
// maps are sync.Maps holding immutable slices, so a rule edit swaps a whole slice rather
// than mutating one in place while requests read it.
type LocalStore struct {
	rules    sync.Map // string -> []*TableRoutingRule (key: "scope:scopeID", scopeID="" for global)
	programs sync.Map // string -> cel.Program (key: ruleID)

	celEnv *cel.Env // Singleton CEL environment reused for all compilations
	logger schemas.Logger
}

// NewLocalStore builds the rule cache and warms it from configStore. Every rule's CEL
// program is compiled up front so the first request that matches a rule does not pay
// compilation latency. With no config store there are no rules to serve, and the plugin
// short-circuits on every request.
func NewLocalStore(ctx context.Context, logger schemas.Logger, configStore configstore.ConfigStore) (*LocalStore, error) {
	if logger == nil {
		return nil, fmt.Errorf("logger cannot be nil")
	}

	env, err := createCELEnvironment()
	if err != nil {
		return nil, fmt.Errorf("failed to create CEL environment: %w", err)
	}

	store := &LocalStore{
		celEnv: env,
		logger: logger,
	}

	if configStore != nil {
		rules, err := configStore.GetRoutingRules(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to load routing rules: %w", err)
		}
		store.load(ctx, rules)
	}

	return store, nil
}

// load rebuilds the scope groups and pre-compiles every program. Called once at
// construction; a live edit goes through UpsertRule/DeleteRule instead.
func (rs *LocalStore) load(ctx context.Context, rules []configstoreTables.TableRoutingRule) {
	// Group by scope in a single pass. Key format: "scope:scopeID" (scopeID empty for global).
	grouped := make(map[string][]*configstoreTables.TableRoutingRule)
	for i := range rules {
		rule := &rules[i]
		grouped[scopeKeyFromRule(rule)] = append(grouped[scopeKeyFromRule(rule)], rule)
	}

	for key, group := range grouped {
		sort.Slice(group, func(i, j int) bool {
			return group[i].Priority < group[j].Priority
		})
		rs.rules.Store(key, group)
	}

	// Pre-compile all routing rule programs to avoid first-request latency
	rs.rules.Range(func(_, value any) bool {
		group, ok := value.([]*configstoreTables.TableRoutingRule)
		if !ok {
			return true
		}
		for _, rule := range group {
			if _, err := rs.GetProgram(ctx, rule); err != nil {
				rs.logger.Warn("Failed to pre-compile routing program for rule %s: %v", rule.Name, err)
			}
		}
		return true
	})
}

// HasRules checks if there are any routing rules configured.
// Quick check to determine if we need to run routing evaluation at all.
func (rs *LocalStore) HasRules(ctx context.Context) bool {
	hasAny := false
	rs.rules.Range(func(_, _ any) bool {
		hasAny = true
		return false // stop after first entry
	})
	return hasAny
}

// GetAllRules gets all routing rules from the in-memory cache.
func (rs *LocalStore) GetAllRules(ctx context.Context) []*configstoreTables.TableRoutingRule {
	var result []*configstoreTables.TableRoutingRule

	rs.rules.Range(func(_, value any) bool {
		group, ok := value.([]*configstoreTables.TableRoutingRule)
		if !ok {
			return true
		}
		result = append(result, group...)
		return true
	})

	// Sort by priority ASC (0 is highest priority, higher numbers are lower priority), then
	// newest first within a priority, so a rule added later shadows an equally-prioritised
	// older one in listings.
	sort.Slice(result, func(i, j int) bool {
		if result[i].Priority != result[j].Priority {
			return result[i].Priority < result[j].Priority
		}
		return result[i].CreatedAt.After(result[j].CreatedAt)
	})

	return result
}

// GetScopedRules retrieves routing rules by scope and scope ID from the in-memory cache.
// Rules are already sorted by priority ASC (0 is highest priority).
func (rs *LocalStore) GetScopedRules(ctx context.Context, scope string, scopeID string) []*configstoreTables.TableRoutingRule {
	value, ok := rs.rules.Load(scopeKey(scope, scopeID))
	if !ok {
		return nil
	}

	group, ok := value.([]*configstoreTables.TableRoutingRule)
	if !ok {
		return nil
	}

	var enabledRules []*configstoreTables.TableRoutingRule
	for _, rule := range group {
		if rule.EnabledValue() {
			enabledRules = append(enabledRules, rule)
		}
	}

	return enabledRules
}

// GetProgram compiles a CEL expression and caches the resulting program.
// Uses the singleton CEL environment for efficiency.
// Returns error if compilation fails.
func (rs *LocalStore) GetProgram(ctx context.Context, rule *configstoreTables.TableRoutingRule) (cel.Program, error) {
	if rule == nil {
		return nil, fmt.Errorf("routing rule cannot be nil")
	}

	// Check cache first to avoid recompilation
	if prog, ok := rs.programs.Load(rule.ID); ok {
		if celProg, ok := prog.(cel.Program); ok {
			return celProg, nil
		}
	}

	// Get CEL expression, default to "true" if empty
	expr := rule.CelExpression
	if expr == "" {
		expr = "true"
	}

	// Normalize header and param keys to lowercase so CEL expressions match normalized map keys
	expr = routing.NormalizeMapKeysInCEL(expr)

	// Validate expression format
	if err := routing.ValidateCELExpression(expr); err != nil {
		return nil, fmt.Errorf("invalid CEL expression: %w", err)
	}

	// Compile using singleton environment
	ast, issues := rs.celEnv.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("CEL compile error: %s", issues.Err().Error())
	}

	// Create program. Partial evaluation is only needed for complexity rules,
	// where routing treats unavailable complexity_tier as unknown instead of
	// leaking an empty-string sentinel.
	var program cel.Program
	var err error
	if celASTReferencesIdentifier(ast, "complexity_tier") {
		program, err = rs.celEnv.Program(ast, cel.EvalOptions(cel.OptPartialEval))
	} else {
		program, err = rs.celEnv.Program(ast)
	}
	if err != nil {
		return nil, fmt.Errorf("CEL program creation error: %w", err)
	}

	// Cache the compiled program
	rs.programs.Store(rule.ID, program)

	return program, nil
}

// UpsertRule updates a routing rule in the in-memory cache.
func (rs *LocalStore) UpsertRule(ctx context.Context, rule *configstoreTables.TableRoutingRule) error {
	if rule == nil {
		return fmt.Errorf("routing rule cannot be nil")
	}
	// First, remove the rule from ALL scopes (in case it was moved from one scope to another)
	rs.removeFromAllScopes(rule.ID)

	key := scopeKeyFromRule(rule)
	// Load existing rules for the rule's current scope
	var group []*configstoreTables.TableRoutingRule
	if value, ok := rs.rules.Load(key); ok {
		if existing, ok := value.([]*configstoreTables.TableRoutingRule); ok {
			group = existing
		}
	}
	// Copy before appending: the loaded slice is being read by in-flight requests,
	// and append can write into its spare capacity.
	updated := make([]*configstoreTables.TableRoutingRule, 0, len(group)+1)
	updated = append(updated, group...)
	updated = append(updated, rule)
	// Sort by priority ASC (0 is highest priority, higher numbers are lower priority)
	sort.Slice(updated, func(i, j int) bool {
		return updated[i].Priority < updated[j].Priority
	})
	rs.rules.Store(key, updated)

	// Invalidate compiled program cache for this rule (expression may have changed)
	rs.programs.Delete(rule.ID)
	// Recompile the program immediately to update cache with fresh compilation
	if _, err := rs.GetProgram(ctx, rule); err != nil {
		rs.logger.Warn("Failed to recompile routing program for rule %s: %v", rule.Name, err)
	}
	return nil
}

// DeleteRule removes a routing rule from the in-memory cache.
func (rs *LocalStore) DeleteRule(ctx context.Context, id string) error {
	rs.removeFromAllScopes(id)
	// Invalidate compiled program cache for this rule
	rs.programs.Delete(id)
	return nil
}

// removeFromAllScopes drops every copy of a rule ID from the scope groups, deleting a
// group entirely once it becomes empty so HasRules stays accurate.
func (rs *LocalStore) removeFromAllScopes(id string) {
	rs.rules.Range(func(key, value any) bool {
		group, ok := value.([]*configstoreTables.TableRoutingRule)
		if !ok {
			return true
		}

		filtered := make([]*configstoreTables.TableRoutingRule, 0, len(group))
		for _, r := range group {
			if r.ID != id {
				filtered = append(filtered, r)
			}
		}

		if len(filtered) == len(group) {
			return true
		}
		if len(filtered) == 0 {
			rs.rules.Delete(key)
		} else {
			rs.rules.Store(key, filtered)
		}
		return true
	})
}

// scopeKey builds the cache key for a scope level. Global rules always key to "global:"
// regardless of the scope ID they were stored with.
func scopeKey(scope string, scopeID string) string {
	if scope == globalScope {
		return globalScope + ":"
	}
	return scope + ":" + scopeID
}

// scopeKeyFromRule builds the cache key a rule belongs under.
func scopeKeyFromRule(rule *configstoreTables.TableRoutingRule) string {
	scopeID := ""
	if rule.ScopeID != nil {
		scopeID = *rule.ScopeID
	}
	return scopeKey(rule.Scope, scopeID)
}
