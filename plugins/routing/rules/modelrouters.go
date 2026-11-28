package rules

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/google/cel-go/cel"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/livestore"
	"github.com/maximhq/bifrost/framework/routing"
)

// ModelRouterMatcher holds the routing of every enabled model router and picks
// the router that takes a request, after the routing rules.
//
// It keeps each router's last routing that compiled, grouped by scope,
// priority ascending, in a live-store table read without a lock, where a write
// swaps in a new slice rather than changing one requests may be reading. A
// router's compiled condition is kept beside its routing, so the two always
// change together. A disabled router is not kept at all, so a gateway whose
// routers are all off skips the step on one counter read (HasRoutes).
type ModelRouterMatcher struct {
	live    *livestore.Store
	routes  *livestore.Table[string, []modelRouterRoute] // scopeKey(scope, scopeID) -> routes, priority ASC
	groups  atomic.Int64                                 // scopes holding routes
	writeMu sync.Mutex                                   // serializes writes, so two saves cannot drop each other's route

	celEnv *cel.Env
}

// modelRouterRoutesTable names the matcher's table. A process runs one routing plugin, since the
// plugins API does not create, change or delete it, so the name is fixed.
const modelRouterRoutesTable = "routing_model_router_routes"

// modelRouterRoute is one enabled router's routing and its compiled
// condition, nil when it has none.
type modelRouterRoute struct {
	routerID string
	name     string
	priority int
	program  cel.Program
}

// NewModelRouterMatcher builds an empty matcher over a table in live; the plugin fills it as it
// serves each router. Close drops the table.
func NewModelRouterMatcher(live *livestore.Store) (*ModelRouterMatcher, error) {
	if live == nil {
		return nil, fmt.Errorf("model router matcher: %w", livestore.ErrNoLiveStore)
	}
	env, err := createCELEnvironment()
	if err != nil {
		return nil, fmt.Errorf("failed to create CEL environment: %w", err)
	}
	routes, err := live.NewTable[string, []modelRouterRoute](context.Background(), modelRouterRoutesTable, livestore.HoldsHandles())
	if err != nil {
		return nil, err
	}
	return &ModelRouterMatcher{live: live, routes: routes, celEnv: env}, nil
}

// Close drops the matcher's table.
func (m *ModelRouterMatcher) Close() {
	if m == nil {
		return
	}
	_ = m.live.DropTable(context.Background(), m.routes.Name())
}

// storeGroupLocked stores a scope's routes, or drops the scope when it has none, keeping the
// count of scopes with routes. writeMu must be held.
func (m *ModelRouterMatcher) storeGroupLocked(key string, group []modelRouterRoute) {
	_, had := m.routes.Get(key)
	if len(group) == 0 {
		if had {
			m.routes.Delete(key)
			m.groups.Add(-1)
		}
		return
	}
	m.routes.Put(key, group)
	if !had {
		m.groups.Add(1)
	}
}

// ValidateModelRouterCEL checks a router's routing condition: it must compile
// like a routing rule's, and it must not use complexity_tier, which the
// router decides only once it has taken the request. Empty is valid and takes
// every request in the router's scope.
func ValidateModelRouterCEL(expr string) error {
	if strings.TrimSpace(expr) == "" {
		return nil
	}
	if err := ValidateCELExpression(expr); err != nil {
		return err
	}
	if celExpressionReferencesIdentifier(expr, "complexity_tier") {
		return fmt.Errorf("a model router's condition cannot use complexity_tier: the router decides the tier once it takes the request")
	}
	return nil
}

// compile builds a condition program, or nil for no condition.
func (m *ModelRouterMatcher) compile(expr string) (cel.Program, error) {
	if strings.TrimSpace(expr) == "" {
		return nil, nil
	}
	if err := ValidateModelRouterCEL(expr); err != nil {
		return nil, err
	}
	ast, issues := m.celEnv.Compile(routing.NormalizeMapKeysInCEL(expr))
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("CEL compile error: %s", issues.Err().Error())
	}
	program, err := m.celEnv.Program(ast)
	if err != nil {
		return nil, fmt.Errorf("CEL program creation error: %w", err)
	}
	return program, nil
}

// Set records a router's routing, replacing what was recorded for it and
// moving it between scopes if its scope changed. A disabled router is
// removed. A condition that does not compile is an error and leaves the
// matcher as it was.
func (m *ModelRouterMatcher) Set(routerID, name string, routing configstore.ModelRouterRouting) error {
	if !routing.Enabled {
		m.Delete(routerID)
		return nil
	}
	program, err := m.compile(routing.CelExpression)
	if err != nil {
		return err
	}
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	key := scopeKey(routing.Scope, valueOrEmpty(routing.ScopeID))
	updated := append(m.groupWithout(key, routerID), modelRouterRoute{routerID: routerID, name: name, priority: routing.Priority, program: program})
	sort.SliceStable(updated, func(i, j int) bool { return updated[i].priority < updated[j].priority })
	m.storeGroupLocked(key, updated)
	m.removeFromScopes(routerID, key)
	return nil
}

// Delete forgets a router; forgetting one not recorded is a no-op.
func (m *ModelRouterMatcher) Delete(routerID string) {
	m.writeMu.Lock()
	defer m.writeMu.Unlock()
	m.removeFromScopes(routerID, "")
}

// groupWithout returns a new copy of a scope's group without the router, so
// the slice requests may be reading is never written to.
func (m *ModelRouterMatcher) groupWithout(key, routerID string) []modelRouterRoute {
	group, _ := m.routes.Get(key)
	kept := make([]modelRouterRoute, 0, len(group)+1)
	for _, route := range group {
		if route.routerID != routerID {
			kept = append(kept, route)
		}
	}
	return kept
}

// removeFromScopes drops a router from every scope but keep, deleting a group
// once it is empty so HasRoutes stays accurate. writeMu must be held.
func (m *ModelRouterMatcher) removeFromScopes(routerID, keep string) {
	var touched []string
	m.routes.Range(func(key string, group []modelRouterRoute) bool {
		if key != keep && slices.ContainsFunc(group, func(route modelRouterRoute) bool { return route.routerID == routerID }) {
			touched = append(touched, key)
		}
		return true
	})
	for _, key := range touched {
		m.storeGroupLocked(key, m.groupWithout(key, routerID))
	}
}

// HasRoutes reports whether any router is enabled: the per-request gate that
// lets a gateway with none skip the step, as LocalStore.HasRules does.
func (m *ModelRouterMatcher) HasRoutes() bool {
	if m == nil {
		return false
	}
	return m.groups.Load() > 0
}

// Match returns the id of the first enabled router whose condition holds, in
// the order Walk visits them; "" when none does.
func (m *ModelRouterMatcher) Match(ctx *schemas.BifrostContext, evaluation *EvaluationContext) (string, error) {
	matched := ""
	err := m.Walk(ctx, evaluation, nil, func(routerID string) bool {
		matched = routerID
		return true
	})
	return matched, err
}

// Walk visits each enabled router whose condition holds on evaluation, in the
// routing rules' order: by scope (virtual key, user, team, customer, global),
// then by priority within a scope. visit is given the router's id and reports
// whether the walk ends there; a router that passes the request on returns
// false, and the walk goes on to the next one that matches. skip, when not
// nil, names routers the walk passes over without evaluating their
// condition, which is how a chained router that already fired stays out of a
// later pass. A condition that fails to evaluate is logged on the request and
// its router skipped.
//
// Conditions are evaluated only as far as the walk gets, so a request the
// first router takes costs one evaluation, as it did when only the first
// match counted.
func (m *ModelRouterMatcher) Walk(ctx *schemas.BifrostContext, evaluation *EvaluationContext, skip func(routerID string) bool, visit func(routerID string) (stop bool)) error {
	if m == nil || evaluation == nil || visit == nil {
		return nil
	}
	variables, err := extractRoutingVariables(evaluation)
	if err != nil {
		return fmt.Errorf("failed to extract routing variables: %w", err)
	}
	for _, level := range buildScopeChain(evaluation.Scope) {
		group, _ := m.routes.Get(scopeKey(level.ScopeName, level.ScopeID))
		for _, route := range group {
			if skip != nil && skip(route.routerID) {
				continue
			}
			if route.program != nil {
				matched, err := evaluateCELExpression(route.program, variables)
				if err != nil {
					if ctx != nil {
						ctx.AppendRoutingEngineLog(schemas.RoutingEngineModelRouter, schemas.LogLevelWarn,
							fmt.Sprintf("Model router '%s' skipped: its condition failed to evaluate: %v", route.name, err))
					}
					continue
				}
				if !matched {
					continue
				}
			}
			if visit(route.routerID) {
				return nil
			}
		}
	}
	return nil
}

// SelectModelRouterTarget picks a router's target for tier, splitting by
// weight when the tier has several. ok is false when the router has no target
// for the tier. A conditional router's targets carry no tier, so its pick is
// the one for the empty tier.
func SelectModelRouterTarget(targets []configstoreTables.TableModelRouterTarget, tier string) (configstoreTables.TableModelRouterTarget, bool) {
	candidates := make([]configstoreTables.TableModelRouterTarget, 0, len(targets))
	for _, t := range targets {
		if t.Tier == tier {
			candidates = append(candidates, t)
		}
	}
	return pickWeighted(candidates, func(t configstoreTables.TableModelRouterTarget) float64 { return t.Weight })
}

// valueOrEmpty dereferences an optional scope ID.
func valueOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
