package rules

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/google/cel-go/cel"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/routing"
)

// ModelRouterMatcher holds the routing of every enabled model router and picks
// the router that takes a request, after the routing rules.
//
// It caches the way LocalStore caches routing rules: grouped by scope,
// priority ascending, in a sync.Map read without a lock, where a write swaps
// in a new slice rather than changing one requests may be reading. A router's
// compiled condition is kept beside its routing, so the two always change
// together. A disabled router is not cached at all, so a gateway whose routers
// are all off skips the step on one empty-map check (HasRoutes).
type ModelRouterMatcher struct {
	routes  sync.Map   // scopeKey(scope, scopeID) -> []modelRouterRoute, priority ASC
	writeMu sync.Mutex // serializes writes, so two saves cannot drop each other's route

	celEnv *cel.Env
}

// modelRouterRoute is one enabled router's routing and its compiled
// condition, nil when it has none.
type modelRouterRoute struct {
	routerID string
	name     string
	priority int
	program  cel.Program
}

// NewModelRouterMatcher builds an empty matcher; the plugin fills it as it
// serves each router.
func NewModelRouterMatcher() (*ModelRouterMatcher, error) {
	env, err := createCELEnvironment()
	if err != nil {
		return nil, fmt.Errorf("failed to create CEL environment: %w", err)
	}
	return &ModelRouterMatcher{celEnv: env}, nil
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
	m.routes.Store(key, updated)
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
	value, _ := m.routes.Load(key)
	group, _ := value.([]modelRouterRoute)
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
	m.routes.Range(func(key, value any) bool {
		group, _ := value.([]modelRouterRoute)
		if key == keep || !slices.ContainsFunc(group, func(route modelRouterRoute) bool { return route.routerID == routerID }) {
			return true
		}
		if kept := m.groupWithout(key.(string), routerID); len(kept) > 0 {
			m.routes.Store(key, kept)
		} else {
			m.routes.Delete(key)
		}
		return true
	})
}

// HasRoutes reports whether any router is enabled: the per-request gate that
// lets a gateway with none skip the step, as LocalStore.HasRules does.
func (m *ModelRouterMatcher) HasRoutes() bool {
	if m == nil {
		return false
	}
	hasAny := false
	m.routes.Range(func(_, _ any) bool {
		hasAny = true
		return false
	})
	return hasAny
}

// Match returns the id of the first enabled router whose condition holds,
// walking the routing rules' scope order (virtual key, user, team, customer,
// global) and priority within a scope; "" when none does. Only the first
// match counts. A condition that fails to evaluate is logged on the request
// and skipped.
func (m *ModelRouterMatcher) Match(ctx *schemas.BifrostContext, evaluation *EvaluationContext) (string, error) {
	if m == nil || evaluation == nil {
		return "", nil
	}
	variables, err := extractRoutingVariables(evaluation)
	if err != nil {
		return "", fmt.Errorf("failed to extract routing variables: %w", err)
	}
	for _, level := range buildScopeChain(evaluation.Scope) {
		value, _ := m.routes.Load(scopeKey(level.ScopeName, level.ScopeID))
		group, _ := value.([]modelRouterRoute)
		for _, route := range group {
			if route.program == nil {
				return route.routerID, nil
			}
			matched, err := evaluateCELExpression(route.program, variables)
			if err != nil {
				if ctx != nil {
					ctx.AppendRoutingEngineLog(schemas.RoutingEngineModelRouter, schemas.LogLevelWarn,
						fmt.Sprintf("Model router '%s' skipped: its condition failed to evaluate: %v", route.name, err))
				}
				continue
			}
			if matched {
				return route.routerID, nil
			}
		}
	}
	return "", nil
}

// SelectModelRouterTarget picks a router's target for tier, splitting by
// weight when the tier has several. ok is false when the router has no target
// for the tier.
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
