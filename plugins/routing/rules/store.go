package rules

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/google/cel-go/cel"
	"github.com/maximhq/bifrost/core/schemas"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/maximhq/bifrost/framework/livestore/registry"
	"github.com/maximhq/bifrost/framework/routing"
)

// Names of the store's tables. A process runs one routing plugin, since the plugins API does not
// create, change or delete it, so the names are fixed.
const (
	ruleScopesTable   = "routing_rule_scopes"
	ruleProgramsTable = "routing_rule_programs"
)

// Store serves the routing rules the live store holds, and the CEL programs compiled from them.
// The rules of each scope are kept in a view, sorted for evaluation as the database orders them
// (priority ascending, then newest first, then ID), and rebuilt when a rule of that scope changes,
// so the evaluator takes the first match without sorting per request. A program is compiled when
// its rule loads or changes and is dropped with the rule.
type Store struct {
	live     *livestore.Store
	rules    *livestore.Collection[registry.RoutingRule]
	scopes   *livestore.Table[string, []*configstoreTables.TableRoutingRule]
	programs *livestore.Table[string, *compiledRule]
	cancel   func()

	mu   sync.Mutex   // serializes scope view rebuilds, so held counts the views exactly
	held atomic.Int64 // scope views held: zero exactly when there are no rules

	celEnv *cel.Env // one CEL environment for every compilation
	logger schemas.Logger
}

// compiledRule is a rule's compiled program, the expression it was compiled from, and whether that
// expression reads complexity_tier, which decides whether evaluating it may need the complexity
// analyzer.
type compiledRule struct {
	expr                 string
	program              cel.Program
	referencesComplexity bool
}

// NewStore serves the routing rules of live. It compiles every rule's program up front, so the
// first request a rule matches does not pay for compilation, and keeps the scope views and
// programs current as rules are refreshed. Close releases it.
func NewStore(live *livestore.Store, logger schemas.Logger) (*Store, error) {
	if logger == nil {
		return nil, fmt.Errorf("logger cannot be nil")
	}
	if live == nil {
		return nil, fmt.Errorf("routing rule store: %w", livestore.ErrNoLiveStore)
	}
	env, err := createCELEnvironment()
	if err != nil {
		return nil, fmt.Errorf("failed to create CEL environment: %w", err)
	}
	rs := &Store{live: live, rules: live.Collections.RoutingRules, celEnv: env, logger: logger}
	if rs.scopes, err = live.NewTable[string, []*configstoreTables.TableRoutingRule](context.Background(), ruleScopesTable); err != nil {
		return nil, err
	}
	if rs.programs, err = live.RegisterState[compiledRule](context.Background(), ruleProgramsTable, livestore.KindRoutingRule, livestore.HoldsHandles()); err != nil {
		_ = live.DropTable(context.Background(), rs.scopes.Name())
		return nil, err
	}
	rs.cancel = rs.rules.Subscribe(rs.onChange)
	scopes := map[string]struct{}{}
	rs.rules.Range(func(r *registry.RoutingRule) bool {
		scopes[registry.RoutingRuleScopes(r)[0]] = struct{}{}
		rs.precompile(&r.TableRoutingRule)
		return true
	})
	for key := range scopes {
		rs.rebuildScope(key)
	}
	return rs, nil
}

// LiveStore returns the live store the rules are served from, which holds the other routing
// configuration too.
func (rs *Store) LiveStore() *livestore.Store {
	return rs.live
}

// Close stops following the live store and drops the store's tables.
func (rs *Store) Close() {
	if rs == nil {
		return
	}
	rs.cancel()
	_ = rs.live.DropTable(context.Background(), rs.scopes.Name())
	_ = rs.live.DropTable(context.Background(), rs.programs.Name())
}

// onChange rebuilds the views of the scopes changed rules left or joined, and recompiles the
// programs of rules that were created or changed. A removed rule's program goes with it.
func (rs *Store) onChange(_ context.Context, changes []livestore.Change[registry.RoutingRule]) {
	scopes := map[string]struct{}{}
	for _, c := range changes {
		if c.Old != nil {
			scopes[registry.RoutingRuleScopes(c.Old)[0]] = struct{}{}
		}
		if c.New == nil {
			continue
		}
		scopes[registry.RoutingRuleScopes(c.New)[0]] = struct{}{}
		rs.precompile(&c.New.TableRoutingRule)
	}
	for key := range scopes {
		rs.rebuildScope(key)
	}
}

// rebuildScope stores the rules of the scope key names, sorted for evaluation, or drops the view
// when the scope has none.
func (rs *Store) rebuildScope(key string) {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	_, had := rs.scopes.Get(key)
	recs := rs.rules.List(registry.RoutingRuleByScope, key)
	if len(recs) == 0 {
		if had {
			rs.scopes.Delete(key)
			rs.held.Add(-1)
		}
		return
	}
	registry.SortRoutingRules(recs)
	group := make([]*configstoreTables.TableRoutingRule, len(recs))
	for i, r := range recs {
		group[i] = &r.TableRoutingRule
	}
	rs.scopes.Put(key, group)
	if !had {
		rs.held.Add(1)
	}
}

// precompile compiles rule's program ahead of the first request, logging a rule that does not
// compile; that rule is skipped at evaluation, where the error is reported again.
func (rs *Store) precompile(rule *configstoreTables.TableRoutingRule) {
	if _, err := rs.compile(rule); err != nil {
		rs.logger.Warn("Failed to pre-compile routing program for rule %s: %v", rule.Name, err)
	}
}

// scopeKey is the key of one scope level, in the rule scope views and the model router routes alike.
// Global keys ignore the scope ID.
func scopeKey(scope, scopeID string) string {
	return registry.RoutingRuleScopeKey(scope, scopeID)
}

// HasRules reports whether any rule is configured. It is the per-request gate: when it returns
// false the request skips rule evaluation entirely.
func (rs *Store) HasRules(ctx context.Context) bool {
	return rs.held.Load() > 0
}

// GetScopedRules returns the enabled rules of one scope level, in evaluation order. Global rules
// are found whatever scope ID they were stored with. The rules are shared: callers must not change
// them.
func (rs *Store) GetScopedRules(ctx context.Context, scope string, scopeID string) []*configstoreTables.TableRoutingRule {
	group, ok := rs.scopes.Get(scopeKey(scope, scopeID))
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

// compile returns rule's compiled program, compiling and keeping it when there is none for the
// rule's current expression. A compile error is returned and nothing is kept.
func (rs *Store) compile(rule *configstoreTables.TableRoutingRule) (*compiledRule, error) {
	if rule == nil {
		return nil, fmt.Errorf("routing rule cannot be nil")
	}
	if cached, ok := rs.programs.Get(rule.ID); ok && cached.expr == rule.CelExpression {
		return cached, nil
	}

	// An empty expression matches every request.
	expr := rule.CelExpression
	if expr == "" {
		expr = "true"
	}

	// Normalize header and param keys to lowercase so CEL expressions match normalized map keys
	expr = routing.NormalizeMapKeysInCEL(expr)

	if err := routing.ValidateCELExpression(expr); err != nil {
		return nil, fmt.Errorf("invalid CEL expression: %w", err)
	}

	ast, issues := rs.celEnv.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("CEL compile error: %s", issues.Err().Error())
	}

	// Partial evaluation is only needed for complexity rules, where routing treats an unavailable
	// complexity_tier as unknown instead of leaking an empty-string sentinel.
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

	compiled := &compiledRule{
		expr:                 rule.CelExpression,
		program:              program,
		referencesComplexity: celExpressionReferencesIdentifier(rule.CelExpression, "complexity_tier"),
	}
	rs.programs.Put(rule.ID, compiled)
	return compiled, nil
}
