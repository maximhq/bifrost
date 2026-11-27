package configstore

import (
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/google/cel-go/cel"
	celast "github.com/google/cel-go/common/ast"
	"github.com/google/cel-go/common/operators"
	"github.com/google/cel-go/parser"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/routing"
)

// This file turns routing rules into the model routers that route as they do. It is what
// migrationRoutingRulesToModelRouters runs, kept here so the migration reads like any other and
// the mapping can be tested against the routing rules it replaces.
//
// A model router's condition cannot read complexity_tier: the router decides the tier once it has
// taken the request. So most of the file is about a rule that does read it, whose condition has to
// be told apart into the part that holds without a tier and what is left of it on each tier.

// routingRuleTierVariable is the variable a routing rule read a request's complexity tier from.
const routingRuleTierVariable = "complexity_tier"

// routingRuleTiers are the tiers a request is classified into. A set of tiers is one bit per entry.
var routingRuleTiers = [...]string{tables.ModelRouterTierSimple, tables.ModelRouterTierMedium, tables.ModelRouterTierComplex}

// RoutingRuleMigration is what an installation's routing rules become as model routers.
type RoutingRuleMigration struct {
	Routers []ModelRouter              // routers to create, each with its routing and targets
	Moved   []ModelRouterRoutingChange // existing routers whose priority moves so they stay after the rules of their scope
	Notes   []RoutingRuleMigrationNote // one per rule, in the order the rules are evaluated
}

// RoutingRuleMigrationNote says what one routing rule became.
type RoutingRuleMigrationNote struct {
	RuleID    string
	RuleName  string
	RouterIDs []string // the routers the rule became, or the one it shares; none when it became no router
	Adjusted  []string // values written differently that route the same
	Flagged   []string // why its routers do not route exactly as the rule did, or why it has none
}

// routingRulePiece is one router a rule becomes.
type routingRulePiece struct {
	condition string // the router's condition; empty takes every request in its scope
	asWritten bool   // the condition is the rule's own text, so its builder state still describes it
	tiers     uint8  // the tiers the rule's targets go on; 0 for a conditional router
}

// routingRulePlan is one rule as the mapping read it.
type routingRulePlan struct {
	rule   *tables.TableRoutingRule
	note   RoutingRuleMigrationNote
	pieces []routingRulePiece // none when the rule becomes no router
	off    bool               // its routers are created turned off, whatever the rule was
}

// routingRuleTierConditions is a condition that reads the tier, with the tier taken out of it.
type routingRuleTierConditions struct {
	free    string    // holds while the tier is not known; "false" when nothing does
	perTier [3]string // what is left of the condition on each tier, in routingRuleTiers order
	inexact bool      // free leaves out requests the condition could decide without a tier
}

// ModelRoutersFromRoutingRules returns the model routers that route as an installation's routing
// rules do, given the Complexity Router settings the rules read complexity_tier from (nil when
// none were saved) and the routers that already exist. It reads and writes no database.
//
// Rules are taken scope by scope in the order they are evaluated: priority, newest first, then id.
//
//   - A rule that does not read the tier becomes a conditional router with the same id, name,
//     scope, condition, builder state, targets and chaining.
//   - A rule that is its condition followed by a choice on the tier becomes a classifier router:
//     the rest of its condition, with the builder state for it when the rule was made in the
//     builder, its targets on those tiers, a copy of the settings, and empty_target
//     "next_router", so a request on another tier goes on to the next router as it went on to the
//     next rule.
//   - Such rules that sit next to each other, are on, do not chain, share the rest of their
//     condition and choose different tiers are one classification followed by a choice, so they
//     share one classifier router, with the first one's id, name and priority.
//   - Any other rule that reads the tier becomes up to four routers whose meta names it, so they
//     chain as one rule: a conditional router for what holds without a tier,
//     placed first so those requests are still not classified, then a classifier router for each
//     condition the tiers are left with.
//
// A rule's fallbacks go on each of its targets. A priority is kept unless it is negative, taken,
// or pushed along by a rule before it that became several routers; the order never changes.
// Existing routers move after the rule routers of their scope only when their priority is taken,
// because rules ran before routers. Every rule has a note, which lists what was written
// differently and what does not route exactly as the rule did.
func ModelRoutersFromRoutingRules(rules []tables.TableRoutingRule, settings *ComplexityAnalyzerConfig, existing []ModelRouter) (*RoutingRuleMigration, error) {
	out := &RoutingRuleMigration{}
	if len(rules) == 0 {
		return out, nil
	}
	env, err := routingRuleCELEnv()
	if err != nil {
		return nil, fmt.Errorf("routing rule conditions: %w", err)
	}
	classifier := routerSettingsFromComplexityConfig(settings)

	// No new router takes an id that is in use, the reserved id, or another rule's own id.
	taken := make(map[string]struct{}, len(existing)+len(rules)+1)
	taken[reservedModelRouterID] = struct{}{}
	routersByScope := make(map[string][]*ModelRouter)
	for i := range existing {
		taken[existing[i].ID] = struct{}{}
		if existing[i].Routing != nil {
			key := routingRuleScopeKey(existing[i].Routing.Scope, existing[i].Routing.ScopeID)
			routersByScope[key] = append(routersByScope[key], &existing[i])
		}
	}
	ruleIDs := make(map[string]struct{}, len(rules))
	rulesByScope := make(map[string][]*tables.TableRoutingRule)
	var scopes []string
	for i := range rules {
		ruleIDs[rules[i].ID] = struct{}{}
		key := routingRuleScopeKey(rules[i].Scope, rules[i].ScopeID)
		if _, seen := rulesByScope[key]; !seen {
			scopes = append(scopes, key)
		}
		rulesByScope[key] = append(rulesByScope[key], &rules[i])
	}
	sort.Strings(scopes)

	for _, scope := range scopes {
		scoped := rulesByScope[scope]
		sort.SliceStable(scoped, func(i, j int) bool {
			a, b := scoped[i], scoped[j]
			if a.Priority != b.Priority {
				return a.Priority < b.Priority
			}
			if !a.CreatedAt.Equal(b.CreatedAt) {
				return a.CreatedAt.After(b.CreatedAt)
			}
			return a.ID < b.ID
		})
		// A router's priority is 0 or above, so a scope with a negative one moves up as a whole.
		shift := max(0, -scoped[0].Priority)
		next := 0             // the lowest priority still free in the scope
		made := false         // the scope has a router made from a rule
		open := -1            // the classifier router the next rule may share, by its index in out.Routers
		openTiers := uint8(0) // the tiers that router's rules have chosen
		for _, rule := range scoped {
			plan := planRoutingRule(env, rule)
			if len(plan.pieces) == 0 {
				out.Notes = append(out.Notes, plan.note)
				open = -1
				continue
			}
			targets := routerTargetsFromRule(rule, &plan.note)
			if open >= 0 && plan.shares() && plan.pieces[0].condition == out.Routers[open].Routing.CelExpression && plan.pieces[0].tiers&openTiers == 0 {
				shared := &out.Routers[open]
				shared.Targets = append(shared.Targets, targetsOnTiers(targets, plan.pieces[0].tiers)...)
				shared.Meta.RoutingRuleIDs = append(shared.Meta.RoutingRuleIDs, rule.ID)
				openTiers |= plan.pieces[0].tiers
				plan.note.RouterIDs = []string{shared.ID}
				plan.note.Adjusted = append(plan.note.Adjusted, fmt.Sprintf("it shares router %q with the rule before it, which has the same condition; the router has that rule's id, name and priority", shared.ID))
				out.Notes = append(out.Notes, plan.note)
				continue
			}
			routers := routersFromRulePlan(&plan, targets, max(next, rule.Priority+shift), taken, ruleIDs, &classifier, settings != nil)
			for i := range routers {
				taken[routers[i].ID] = struct{}{}
			}
			next = routers[len(routers)-1].Routing.Priority + 1
			made = true
			open = -1
			if plan.shares() {
				open, openTiers = len(out.Routers), plan.pieces[0].tiers
			}
			out.Routers = append(out.Routers, routers...)
			out.Notes = append(out.Notes, plan.note)
		}
		if !made {
			continue
		}
		// Rules ran before routers, so the routers of this scope stay after the ones made from
		// its rules, in the order they have.
		following := routersByScope[scope]
		sort.SliceStable(following, func(i, j int) bool {
			if following[i].Routing.Priority != following[j].Routing.Priority {
				return following[i].Routing.Priority < following[j].Routing.Priority
			}
			return following[i].ID < following[j].ID
		})
		for _, router := range following {
			priority := max(next, router.Routing.Priority)
			next = priority + 1
			if priority == router.Routing.Priority {
				continue
			}
			moved := *router.Routing
			moved.Priority = priority
			out.Moved = append(out.Moved, ModelRouterRoutingChange{RouterID: router.ID, Routing: moved})
		}
	}
	return out, nil
}

// routingRuleScopeKey groups rules and routers by the scope they are evaluated in. The global
// scope is one group whatever scope id a row was stored with.
func routingRuleScopeKey(scope string, scopeID *string) string {
	if scope == "" || scope == "global" {
		return "global:"
	}
	if scopeID == nil {
		return scope + ":"
	}
	return scope + ":" + *scopeID
}

// routerSettingsFromComplexityConfig returns the classifier settings every router made from a tier
// rule is given: the Complexity Router's, which is what classified for the rules. Phrase
// suggestions are left out, since the Complexity Router made none and routers that carry them
// cannot share one classification. An installation that never saved settings classified nothing,
// so its routers get the stored default phrases and no classifier, and classify nothing either.
func routerSettingsFromComplexityConfig(settings *ComplexityAnalyzerConfig) ComplexityAnalyzerConfig {
	base := ComplexityAnalyzerConfig{Keywords: legacyComplexityExemplarsV2()}
	if settings != nil {
		base = *settings
	}
	base.Suggestions = nil
	base.ConfigHashes = ComplexityAnalyzerConfigHashes{}
	base.EmbeddingFingerprint = ""
	return base.Normalized()
}

// planRoutingRule reads one rule and decides the routers it becomes. A rule that could not match
// any request becomes none: its scope names nothing a request has, or its condition does not
// compile, which the routing rules skipped on every request.
func planRoutingRule(env *cel.Env, rule *tables.TableRoutingRule) routingRulePlan {
	plan := routingRulePlan{rule: rule, note: RoutingRuleMigrationNote{RuleID: rule.ID, RuleName: rule.Name}}
	skip := func(reason string) routingRulePlan {
		plan.pieces = nil
		plan.note.Flagged = append(plan.note.Flagged, reason)
		return plan
	}
	if strings.TrimSpace(rule.ID) == "" {
		return skip("it has no id")
	}
	if !validModelRouterScopes[rule.Scope] {
		return skip(fmt.Sprintf("its scope %q is not one a request has, so it never matched", rule.Scope))
	}
	if rule.Scope != "global" && (rule.ScopeID == nil || strings.TrimSpace(*rule.ScopeID) == "") {
		return skip(fmt.Sprintf("it names no %s, so it never matched", rule.Scope))
	}
	expr := rule.CelExpression
	if expr == "" {
		plan.pieces = []routingRulePiece{{asWritten: true}}
		return plan
	}
	if strings.TrimSpace(expr) == "" {
		// Only an empty condition matched every request; blank space failed to compile.
		plan.pieces = []routingRulePiece{{condition: "false"}}
		plan.note.Adjusted = append(plan.note.Adjusted, "its condition was blank space, which never matched; the router's condition is false")
		return plan
	}
	normalized := routing.NormalizeMapKeysInCEL(expr)
	if err := routing.ValidateCELExpression(normalized); err != nil {
		return skip(fmt.Sprintf("its condition is not valid, so it never matched: %v", err))
	}
	ast, issues := env.Compile(normalized)
	if issues != nil && issues.Err() != nil {
		return skip(fmt.Sprintf("its condition does not compile, so it never matched: %v", issues.Err()))
	}
	if !routingConditionReadsTier(ast.NativeRep().Expr()) {
		plan.pieces = []routingRulePiece{{condition: expr, asWritten: true}}
		return plan
	}

	conditions, err := splitRoutingRuleTiers(env, ast, normalized)
	if err != nil {
		return skip(fmt.Sprintf("%s could not be taken out of its condition: %v", routingRuleTierVariable, err))
	}
	if conditions.inexact {
		plan.note.Flagged = append(plan.note.Flagged, fmt.Sprintf("its condition reads %s inside a loop or a compared group, which the rule could decide without a tier; its routers decide only once the request is classified", routingRuleTierVariable))
	}
	// A conditional router for what holds without a tier, then a classifier router for each
	// condition the tiers are left with. Tiers left with the same condition share a router, and a
	// tier left with the tier-free part needs none, since the router before it takes those.
	if conditions.free != "false" {
		plan.pieces = append(plan.pieces, routingRulePiece{condition: routerConditionText(conditions.free)})
	}
	for tier, left := range conditions.perTier {
		if left == "false" || left == conditions.free {
			continue
		}
		at := slices.IndexFunc(plan.pieces, func(piece routingRulePiece) bool {
			return piece.tiers != 0 && piece.condition == routerConditionText(left)
		})
		if at < 0 {
			plan.pieces = append(plan.pieces, routingRulePiece{condition: routerConditionText(left)})
			at = len(plan.pieces) - 1
		}
		plan.pieces[at].tiers |= 1 << tier
	}
	if len(plan.pieces) == 0 {
		// The condition holds on no tier and without one, so the rule never matched.
		plan.pieces = []routingRulePiece{{condition: "false"}}
		plan.off = true
		plan.note.Flagged = append(plan.note.Flagged, "its condition holds on no tier, so it never matched; its router is turned off with the condition false")
	}
	return plan
}

// shares reports whether the rule can share one classifier router with the rules next to it: it
// is on, it does not chain, and it is its condition followed by a choice on the tier.
func (p *routingRulePlan) shares() bool {
	return !p.off && len(p.pieces) == 1 && p.pieces[0].tiers != 0 && p.rule.EnabledValue() && !p.rule.ChainRule
}

// routerConditionText is a condition as a router stores it: one that always holds is no condition.
func routerConditionText(condition string) string {
	if condition == "true" {
		return ""
	}
	return condition
}

// routersFromRulePlan builds the routers one rule becomes, starting at priority first, and fills in
// the rule's note. Each router's meta names the rule, so routers made from one rule chain as the
// rule did, and the rule's earlier logs are listed under them.
func routersFromRulePlan(plan *routingRulePlan, targets []tables.TableModelRouterTarget, first int, taken, ruleIDs map[string]struct{}, settings *ComplexityAnalyzerConfig, settingsSaved bool) []ModelRouter {
	rule := plan.rule
	name := strings.TrimSpace(rule.Name)
	if name == "" {
		name = rule.ID
	}
	// A rule with no target routed nothing. When part of it holds without a tier, the conditional
	// router made for that part passes those requests on, and the classifier routers after it
	// would classify them, which the rule never did. They are created turned off.
	idle := len(targets) == 0 && len(plan.pieces) > 1 && plan.pieces[0].tiers == 0
	routers := make([]ModelRouter, 0, len(plan.pieces))
	for i, piece := range plan.pieces {
		id, label := rule.ID, name
		switch {
		case i > 0:
			id = freeRouterID(rule.ID+"-"+strconv.Itoa(i+1), taken, ruleIDs)
			label = name + " (" + strconv.Itoa(i+1) + ")"
		case isTaken(taken, id):
			id = freeRouterID(rule.ID+"-rule", taken, ruleIDs)
			plan.note.Adjusted = append(plan.note.Adjusted, fmt.Sprintf("its id is taken by a router, so its router's id is %q", id))
		}
		router := ModelRouter{ID: id, Name: label, Description: rule.Description, Mode: tables.ModelRouterModeConditional,
			Meta: &tables.ModelRouterMeta{RoutingRuleIDs: []string{rule.ID}}}
		router.Routing = &ModelRouterRouting{
			Enabled:       rule.EnabledValue() && !plan.off,
			Scope:         rule.Scope,
			CelExpression: piece.condition,
			Priority:      first + i,
			Chain:         rule.ChainRule,
		}
		if rule.Scope != "global" {
			router.Routing.ScopeID = bifrost.Ptr(strings.TrimSpace(*rule.ScopeID))
		}
		switch {
		case piece.asWritten:
			router.Routing.Query = rule.ParsedQuery
		case len(plan.pieces) == 1 && piece.tiers != 0:
			router.Routing.Query = builderStateWithoutTier(rule.ParsedQuery)
		}
		if piece.tiers == 0 {
			router.Targets = slices.Clone(targets)
		} else {
			router.Mode = tables.ModelRouterModeClassifier
			router.ComplexityAnalyzerConfig = settings.Normalized()
			router.Routing.EmptyTarget = tables.ModelRouterEmptyTargetNextRouter
			router.Routing.Enabled = router.Routing.Enabled && !idle
			router.Targets = targetsOnTiers(targets, piece.tiers)
		}
		plan.note.RouterIDs = append(plan.note.RouterIDs, id)
		routers = append(routers, router)
	}
	if first != rule.Priority {
		plan.note.Adjusted = append(plan.note.Adjusted, fmt.Sprintf("priority %d became %d; the order in its scope is the same", rule.Priority, first))
	}
	if idle && rule.EnabledValue() {
		plan.note.Adjusted = append(plan.note.Adjusted, "it has no target, so it routed nothing; its routers for the tier are turned off so they do not classify requests they cannot route")
	}
	if !settingsSaved && slices.ContainsFunc(plan.pieces, func(piece routingRulePiece) bool { return piece.tiers != 0 }) {
		plan.note.Adjusted = append(plan.note.Adjusted, "the Complexity Router had no saved settings, so no tier was ever known and the rule never matched on one; its router has the default phrases and no classifier")
	}
	return routers
}

// builderStateWithoutTier returns a rule's condition builder state with the tier taken out of it,
// so the router of a rule made in the builder still opens in the builder, showing the rest of the
// condition. It handles the shape the builder gives a rule that is its condition followed by a
// choice on the tier: a group joined by and, with the tier as entries of its own. Any other state
// returns nil, and the router opens on its condition text instead. The rule's own state is left as
// it is.
func builderStateWithoutTier(state map[string]any) map[string]any {
	entries, ok := state["rules"].([]any)
	if !ok || state["combinator"] != "and" || state["not"] == true {
		return nil
	}
	kept := make([]any, 0, len(entries))
	for _, entry := range entries {
		if fields, ok := entry.(map[string]any); ok && fields["field"] == routingRuleTierVariable {
			continue
		}
		if builderStateReadsTier(entry) {
			return nil
		}
		kept = append(kept, entry)
	}
	if len(kept) == len(entries) {
		// The condition reads the tier and the state does not, so the state does not describe it.
		return nil
	}
	without := make(map[string]any, len(state))
	for key, value := range state {
		without[key] = value
	}
	without["rules"] = kept
	return without
}

// builderStateReadsTier reports whether an entry of a condition builder state, or one inside it,
// is on the tier.
func builderStateReadsTier(entry any) bool {
	fields, ok := entry.(map[string]any)
	if !ok {
		return false
	}
	if fields["field"] == routingRuleTierVariable {
		return true
	}
	nested, _ := fields["rules"].([]any)
	return slices.ContainsFunc(nested, builderStateReadsTier)
}

// isTaken reports whether a router id is in use.
func isTaken(taken map[string]struct{}, id string) bool {
	_, used := taken[id]
	return used
}

// freeRouterID returns id when no router has it and no rule keeps it as its own, and otherwise
// id with the first number after it that is free.
func freeRouterID(id string, taken, ruleIDs map[string]struct{}) string {
	candidate := id
	for n := 2; isTaken(taken, candidate) || isTaken(ruleIDs, candidate); n++ {
		candidate = id + "-" + strconv.Itoa(n)
	}
	return candidate
}

// routerTargetsFromRule returns a rule's targets as a router's, with the rule's fallbacks on each.
// The routing rules picked a target in proportion to its weight among the ones that are not
// negative, and evenly when every weight is 0. A router's weights are above 0 and sum to 1, so the
// same pick is written that way: a target that was never picked is left out and the rest are
// scaled.
func routerTargetsFromRule(rule *tables.TableRoutingRule, note *RoutingRuleMigrationNote) []tables.TableModelRouterTarget {
	trimmed := func(value *string) *string {
		if value == nil || strings.TrimSpace(*value) == "" {
			return nil
		}
		return bifrost.Ptr(strings.TrimSpace(*value))
	}
	total, even := 0.0, true
	for _, from := range rule.Targets {
		if from.Weight > 0 {
			total, even = total+from.Weight, false
		}
	}
	rewritten := false
	targets := make([]tables.TableModelRouterTarget, 0, len(rule.Targets))
	for _, from := range rule.Targets {
		// Every weight being 0 picks evenly; otherwise only a weight above 0 is ever picked.
		weight := from.Weight
		switch {
		case even && from.Weight == 0:
			weight = 1
		case !(from.Weight > 0):
			rewritten = true
			continue
		}
		target := tables.TableModelRouterTarget{Provider: trimmed(from.Provider), Model: trimmed(from.Model), KeyID: trimmed(from.KeyID), Weight: weight}
		if from.TTFTTimeoutMs != nil && *from.TTFTTimeoutMs > 0 {
			target.TTFTTimeoutMs = bifrost.Ptr(*from.TTFTTimeoutMs)
		}
		target.ParsedFallbacks = slices.Clone(rule.ParsedFallbacks)
		targets = append(targets, target)
	}
	if even {
		total = float64(len(targets))
	}
	if len(targets) > 0 && math.Abs(total-1) > 0.001 {
		for i := range targets {
			targets[i].Weight /= total
		}
		rewritten = true
	}
	if rewritten {
		note.Adjusted = append(note.Adjusted, "its target weights are written as a router takes them, above 0 and summing to 1; each target is picked as often as before, and one that was never picked is left out")
	}
	return targets
}

// targetsOnTiers returns a copy of a rule's targets for each of the tiers.
func targetsOnTiers(targets []tables.TableModelRouterTarget, tiers uint8) []tables.TableModelRouterTarget {
	var out []tables.TableModelRouterTarget
	for i, tier := range routingRuleTiers {
		if tiers&(1<<i) == 0 {
			continue
		}
		for _, target := range targets {
			target.Tier = tier
			target.ParsedFallbacks = slices.Clone(target.ParsedFallbacks)
			out = append(out, target)
		}
	}
	return out
}

// routingRuleCELEnv declares the variables a routing rule's condition could read when rules became
// routers. It is the mapping's own copy, so a condition is read the same way whatever the routing
// plugin declares later.
func routingRuleCELEnv() (*cel.Env, error) {
	return cel.NewEnv(
		cel.Variable("model", cel.StringType),
		cel.Variable("provider", cel.StringType),
		cel.Variable("request_type", cel.StringType),
		cel.Variable("headers", cel.MapType(cel.StringType, cel.StringType)),
		cel.Variable("params", cel.MapType(cel.StringType, cel.StringType)),
		cel.Variable("virtual_key_id", cel.StringType),
		cel.Variable("virtual_key_name", cel.StringType),
		cel.Variable("user_id", cel.StringType),
		cel.Variable("team_id", cel.StringType),
		cel.Variable("team_name", cel.StringType),
		cel.Variable("customer_id", cel.StringType),
		cel.Variable("customer_name", cel.StringType),
		cel.Variable("tokens_used", cel.DoubleType),
		cel.Variable("request", cel.DoubleType),
		cel.Variable("budget_used", cel.DoubleType),
		cel.Variable(routingRuleTierVariable, cel.StringType),
	)
}

// splitRoutingRuleTiers takes the tier out of a condition that reads it: what is left of the
// condition on each tier, and the part that holds while the tier is not known.
func splitRoutingRuleTiers(env *cel.Env, ast *cel.Ast, expr string) (routingRuleTierConditions, error) {
	var out routingRuleTierConditions
	for i, tier := range routingRuleTiers {
		onTier, ok := replaceRoutingTierReads(ast, expr, strconv.Quote(tier))
		if !ok {
			return out, fmt.Errorf("its reads of %s could not be located", routingRuleTierVariable)
		}
		left, err := foldRoutingCondition(env, onTier)
		if err != nil {
			return out, fmt.Errorf("its condition on tier %s: %w", tier, err)
		}
		out.perTier[i] = left
	}
	writer := &routingTierFreeWriter{info: ast.NativeRep().SourceInfo()}
	free := writer.definitely(ast.NativeRep().Expr(), true)
	if writer.err != nil {
		return out, fmt.Errorf("its condition without a tier: %w", writer.err)
	}
	free, err := foldRoutingCondition(env, free)
	if err != nil {
		return out, fmt.Errorf("its condition without a tier: %w", err)
	}
	out.free, out.inexact = free, writer.inexact
	return out, nil
}

// routingTierFreeWriter writes the part of a condition that holds while the tier is not known.
type routingTierFreeWriter struct {
	info    *celast.SourceInfo
	inexact bool  // a part that reads the tier may have a value without it, and was written as having none
	err     error // the first part that could not be written back as text
}

// text writes an expression back as condition text.
func (w *routingTierFreeWriter) text(e celast.Expr) string {
	out, err := parser.Unparse(e, w.info)
	if err != nil && w.err == nil {
		w.err = err
	}
	return out
}

// definitely returns a condition that does not read the tier and holds exactly when e evaluates to
// want while the tier is not known. The routing rules evaluated an unknown tier that way: an and
// is false once one side is, an or is true once one side is, and anything else that reads the tier
// has no value, so it is neither.
func (w *routingTierFreeWriter) definitely(e celast.Expr, want bool) string {
	if !routingConditionReadsTier(e) {
		if want {
			return w.text(e)
		}
		return "!(" + w.text(e) + ")"
	}
	if e.Kind() == celast.CallKind {
		call := e.AsCall()
		args := call.Args()
		switch call.FunctionName() {
		case operators.LogicalAnd, operators.LogicalOr:
			// An and is true when every side is and false when any is; an or is the reverse.
			every := (call.FunctionName() == operators.LogicalAnd) == want
			parts := make([]string, len(args))
			for i, arg := range args {
				parts[i] = "(" + w.definitely(arg, want) + ")"
			}
			if every {
				return strings.Join(parts, " && ")
			}
			return strings.Join(parts, " || ")
		case operators.LogicalNot:
			if len(args) == 1 {
				return w.definitely(args[0], !want)
			}
		case operators.Conditional:
			if len(args) == 3 {
				return "((" + w.definitely(args[0], true) + ") && (" + w.definitely(args[1], want) + ")) || ((" +
					w.definitely(args[0], false) + ") && (" + w.definitely(args[2], want) + "))"
			}
		}
	}
	// Any other expression has no value when one of its parts has none, unless an and, an or, a
	// conditional or a loop inside it decides without the tier. That case is rare and only
	// hand-written, so it is written as having no value and the rule is flagged.
	walkRoutingCondition(e, false, func(part celast.Expr, _ bool) {
		if !routingConditionReadsTier(part) {
			return
		}
		switch part.Kind() {
		case celast.ComprehensionKind:
			w.inexact = true
		case celast.CallKind:
			switch part.AsCall().FunctionName() {
			case operators.LogicalAnd, operators.LogicalOr, operators.Conditional:
				w.inexact = true
			}
		}
	})
	return "false"
}

// foldRoutingCondition evaluates what is constant in a condition and returns what is left. A
// condition that cannot be folded, because a constant part of it fails to evaluate, is returned as
// it was written: it still evaluates the same.
func foldRoutingCondition(env *cel.Env, expr string) (string, error) {
	ast, issues := env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return "", issues.Err()
	}
	folder, err := cel.NewConstantFoldingOptimizer()
	if err != nil {
		return "", err
	}
	optimizer, err := cel.NewStaticOptimizer(folder)
	if err != nil {
		return "", err
	}
	folded, issues := optimizer.Optimize(env, ast)
	if issues != nil && issues.Err() != nil {
		return expr, nil
	}
	out, err := cel.AstToString(folded)
	if err != nil {
		return expr, nil
	}
	if _, issues := env.Compile(out); issues != nil && issues.Err() != nil {
		return expr, nil
	}
	return out, nil
}

// replaceRoutingTierReads returns expr with every read of the tier replaced by replacement. It
// reports false when a read cannot be found in the text, so a condition is never rewritten on a
// guess.
func replaceRoutingTierReads(ast *cel.Ast, expr string, replacement string) (string, bool) {
	info := ast.NativeRep().SourceInfo()
	runes := []rune(expr)
	width := int32(len(routingRuleTierVariable))
	var starts []int32
	found := true
	walkRoutingCondition(ast.NativeRep().Expr(), false, func(part celast.Expr, shadowed bool) {
		if !isRoutingTierRead(part, shadowed) {
			return
		}
		at, ok := info.GetOffsetRange(part.ID())
		if !ok || at.Start < 0 || int(at.Start+width) > len(runes) || string(runes[at.Start:at.Start+width]) != routingRuleTierVariable {
			found = false
			return
		}
		starts = append(starts, at.Start)
	})
	if !found || len(starts) == 0 {
		return "", false
	}
	// From the end of the text, so an earlier read keeps its place. A loop macro can hold one
	// read in several places of the tree.
	slices.Sort(starts)
	starts = slices.Compact(starts)
	for i := len(starts) - 1; i >= 0; i-- {
		runes = slices.Replace(runes, int(starts[i]), int(starts[i]+width), []rune(replacement)...)
	}
	return string(runes), true
}

// routingConditionReadsTier reports whether any part of e reads the tier.
func routingConditionReadsTier(e celast.Expr) bool {
	reads := false
	walkRoutingCondition(e, false, func(part celast.Expr, shadowed bool) {
		reads = reads || isRoutingTierRead(part, shadowed)
	})
	return reads
}

// isRoutingTierRead reports whether e is a read of the tier variable itself.
func isRoutingTierRead(e celast.Expr, shadowed bool) bool {
	return e.Kind() == celast.IdentKind && e.AsIdent() == routingRuleTierVariable && !shadowed
}

// walkRoutingCondition visits every expression of a condition, parents first. shadowed is true
// inside a loop macro whose own variable is named like the tier, where that name is not the tier.
func walkRoutingCondition(e celast.Expr, shadowed bool, visit func(e celast.Expr, shadowed bool)) {
	if e == nil {
		return
	}
	visit(e, shadowed)
	switch e.Kind() {
	case celast.CallKind:
		call := e.AsCall()
		walkRoutingCondition(call.Target(), shadowed, visit)
		for _, arg := range call.Args() {
			walkRoutingCondition(arg, shadowed, visit)
		}
	case celast.ComprehensionKind:
		loop := e.AsComprehension()
		walkRoutingCondition(loop.IterRange(), shadowed, visit)
		inner := shadowed || loop.IterVar() == routingRuleTierVariable || loop.IterVar2() == routingRuleTierVariable || loop.AccuVar() == routingRuleTierVariable
		walkRoutingCondition(loop.AccuInit(), inner, visit)
		walkRoutingCondition(loop.LoopCondition(), inner, visit)
		walkRoutingCondition(loop.LoopStep(), inner, visit)
		walkRoutingCondition(loop.Result(), inner, visit)
	case celast.ListKind:
		for _, element := range e.AsList().Elements() {
			walkRoutingCondition(element, shadowed, visit)
		}
	case celast.MapKind:
		for _, entry := range e.AsMap().Entries() {
			if entry.Kind() == celast.MapEntryKind {
				walkRoutingCondition(entry.AsMapEntry().Key(), shadowed, visit)
				walkRoutingCondition(entry.AsMapEntry().Value(), shadowed, visit)
			}
		}
	case celast.SelectKind:
		walkRoutingCondition(e.AsSelect().Operand(), shadowed, visit)
	}
}
