package configstore

import (
	"encoding/json"
	"fmt"
	mathrand "math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/routing"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// migrationRuleTarget is a routing rule target; an empty provider or model keeps the request's.
func migrationRuleTarget(provider, model string, weight float64) tables.TableRoutingTarget {
	target := tables.TableRoutingTarget{Weight: weight}
	if provider != "" {
		target.Provider = bifrost.Ptr(provider)
	}
	if model != "" {
		target.Model = bifrost.Ptr(model)
	}
	return target
}

// migrationRule is an enabled global routing rule that sends the requests its condition holds for
// to its targets.
func migrationRule(id, condition string, priority int, targets ...tables.TableRoutingTarget) tables.TableRoutingRule {
	return tables.TableRoutingRule{
		ID: id, Name: "Rule " + id, CelExpression: condition, Enabled: bifrost.Ptr(true),
		Scope: "global", Priority: priority, Targets: targets,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

// scopedMigrationRule moves a rule to one entity's scope.
func scopedMigrationRule(rule tables.TableRoutingRule, scope, scopeID string) tables.TableRoutingRule {
	rule.Scope, rule.ScopeID = scope, bifrost.Ptr(scopeID)
	return rule
}

// migratedRouters maps rules with the semantic test settings and no existing routers.
func migratedRouters(t *testing.T, rules ...tables.TableRoutingRule) *RoutingRuleMigration {
	t.Helper()
	migration, err := ModelRoutersFromRoutingRules(rules, testSemanticAnalyzerConfig(), nil)
	require.NoError(t, err)
	return migration
}

// describeMigratedRouters renders routers as "id priority mode [condition] flags" lines, so the
// routers a rule set becomes and their order compare in one assertion.
func describeMigratedRouters(routers []ModelRouter) []string {
	lines := make([]string, 0, len(routers))
	for _, router := range routers {
		flags := ""
		if !router.Routing.Enabled {
			flags += " off"
		}
		if router.Routing.Chain {
			flags += " chain"
		}
		lines = append(lines, fmt.Sprintf("%s %d %s [%s]%s", router.ID, router.Routing.Priority, router.Mode, router.Routing.CelExpression, flags))
	}
	return lines
}

// migrationNote returns the note of one rule.
func migrationNote(t *testing.T, migration *RoutingRuleMigration, ruleID string) RoutingRuleMigrationNote {
	t.Helper()
	for _, note := range migration.Notes {
		if note.RuleID == ruleID {
			return note
		}
	}
	require.Failf(t, "no note", "rule %q has no note", ruleID)
	return RoutingRuleMigrationNote{}
}

// routingRuleTestVariables are the variables of one request, with no tier.
func routingRuleTestVariables(model string, headers map[string]string) map[string]any {
	if headers == nil {
		headers = map[string]string{}
	}
	return map[string]any{
		"model": model, "provider": "openai", "request_type": "chat_completion",
		"headers": headers, "params": map[string]string{},
		"virtual_key_id": "", "virtual_key_name": "", "user_id": "", "team_id": "", "team_name": "",
		"customer_id": "", "customer_name": "",
		"tokens_used": 0.0, "request": 0.0, "budget_used": 0.0, routingRuleTierVariable: "",
	}
}

// evaluateRoutingCondition evaluates a condition as the routing rules and the model routers both
// do: a condition that fails to evaluate, or has no value because the tier is not known, does not
// hold. decided is false in those two cases.
func evaluateRoutingCondition(t *testing.T, env *cel.Env, expr string, variables map[string]any, tierUnknown bool) (holds, decided bool) {
	t.Helper()
	if expr == "" {
		return true, true
	}
	ast, issues := env.Compile(routing.NormalizeMapKeysInCEL(expr))
	require.NoError(t, issues.Err(), expr)
	program, err := env.Program(ast, cel.EvalOptions(cel.OptPartialEval))
	require.NoError(t, err)
	activation := any(variables)
	if tierUnknown {
		activation, err = cel.PartialVars(variables, cel.AttributePattern(routingRuleTierVariable))
		require.NoError(t, err)
	}
	out, _, err := program.Eval(activation)
	if err != nil || types.IsUnknown(out) {
		return false, false
	}
	holds, _ = out.Value().(bool)
	return holds, true
}

// routingRuleTierTestConditions are conditions that read the tier, from the shapes the rule
// builder writes to ones only hand-written CEL has.
var routingRuleTierTestConditions = []string{
	`complexity_tier == "SIMPLE"`,
	`model == "a" && complexity_tier == "SIMPLE"`,
	`complexity_tier != "SIMPLE" && model == "a"`,
	`complexity_tier in ["SIMPLE", "MEDIUM"] && headers["x-a"] == "1"`,
	`!(complexity_tier in ["SIMPLE"]) && "x-a" in headers`,
	`model == "a" && headers["X-A"] == "1" && complexity_tier == "COMPLEX" && request_type == "chat_completion"`,
	`model == "a" || complexity_tier == "SIMPLE"`,
	`headers["x-plan"] == "pro" || complexity_tier == "COMPLEX"`,
	`(model == "a" || headers["x-a"] == "1") && complexity_tier == "SIMPLE"`,
	`(model == "a" && complexity_tier == "SIMPLE") || (model == "b" && complexity_tier == "COMPLEX")`,
	`!(model == "a" && complexity_tier == "SIMPLE")`,
	`!(model == "a" || complexity_tier == "SIMPLE")`,
	`complexity_tier == "SIMPLE" ? model == "a" : headers["x-a"] == "1"`,
	`model == "a" ? complexity_tier == "SIMPLE" : complexity_tier == "COMPLEX"`,
	`complexity_tier == headers["x-tier"]`,
	`complexity_tier == "simple"`,
	`complexity_tier in ["SIMPLE", "medium"] && model == "a"`,
	`complexity_tier != ""`,
	`complexity_tier == "SIMPLE" || complexity_tier != "SIMPLE"`,
	`model == "a" || complexity_tier in ["SIMPLE", "MEDIUM", "COMPLEX"]`,
	`complexity_tier.startsWith("S") && model == "a"`,
	`["SIMPLE","MEDIUM"].exists(t, t == complexity_tier) && model.startsWith("a")`,
	`int(headers["x-a"]) > 3 && complexity_tier == "SIMPLE"`,
	`headers["complexity_tier"] == "1" && complexity_tier == "MEDIUM"`,
	`complexity_tier == "SIMPLE" && complexity_tier == "MEDIUM"`,
	`budget_used > 80.0 && complexity_tier != "COMPLEX"`,
}

// TestRoutingRuleTierConditionsRouteAsTheRule proves that taking the tier out of a condition
// changes nothing a request can see. For every condition, request and tier, with the tier known or
// not, the routers the rule is laid out as take the request exactly when the rule matched it, and
// they classify the request only when the rule did.
//
// The rule side is evaluated the way the routing rules did it: the condition is first evaluated
// with the tier not known, and the request is classified only when that leaves it undecided.
func TestRoutingRuleTierConditionsRouteAsTheRule(t *testing.T) {
	env, err := routingRuleCELEnv()
	require.NoError(t, err)
	headerSets := []map[string]string{nil, {"x-a": "1"}, {"x-a": "9"}, {"x-a": "zz"}, {"x-plan": "pro"}, {"x-tier": "SIMPLE"}, {"complexity_tier": "1"}}
	budgets := []float64{0, 95}
	for _, condition := range routingRuleTierTestConditions {
		t.Run(condition, func(t *testing.T) {
			rule := migrationRule("r", condition, 0, migrationRuleTarget("openai", "gpt-4o", 1))
			plan := planRoutingRule(env, &rule)
			require.NotEmpty(t, plan.pieces, "%v", plan.note.Flagged)
			for _, piece := range plan.pieces {
				ast, issues := env.Compile(routing.NormalizeMapKeysInCEL(piece.condition + " "))
				if piece.condition != "" {
					require.NoError(t, issues.Err(), piece.condition)
					assert.False(t, routingConditionReadsTier(ast.NativeRep().Expr()), "a router's condition cannot read the tier: %s", piece.condition)
				}
			}
			for _, model := range []string{"a", "b"} {
				for _, headers := range headerSets {
					for _, budget := range budgets {
						for _, tier := range []string{"", "SIMPLE", "MEDIUM", "COMPLEX"} {
							variables := routingRuleTestVariables(model, headers)
							variables["budget_used"] = budget

							// The rule, as the routing rules evaluated it.
							matched, decided := evaluateRoutingCondition(t, env, condition, variables, true)
							classified := !decided
							if classified && tier != "" {
								variables[routingRuleTierVariable] = tier
								matched, _ = evaluateRoutingCondition(t, env, condition, variables, false)
								variables[routingRuleTierVariable] = ""
							}

							// Its routers, walked in order. A router that is off takes nothing.
							taken, classifies := false, false
							for _, piece := range plan.pieces {
								if plan.off || taken {
									break
								}
								holds, _ := evaluateRoutingCondition(t, env, piece.condition, variables, false)
								if !holds {
									continue
								}
								if piece.tiers == 0 {
									taken = true
									continue
								}
								classifies = true
								for i, name := range routingRuleTiers {
									taken = taken || (name == tier && piece.tiers&(1<<i) != 0)
								}
							}
							at := fmt.Sprintf("model=%s headers=%v budget=%v tier=%q", model, headers, budget, tier)
							assert.Equal(t, matched, taken, "the routers must take what the rule matched: %s", at)
							if classifies {
								assert.True(t, classified, "the routers must not classify a request the rule did not: %s", at)
							}
						}
					}
				}
			}
		})
	}
}

// TestRoutingRuleTierConditions pins what is left of a condition once the tier is taken out of it:
// the part that holds without a tier, and the condition each tier is left with.
func TestRoutingRuleTierConditions(t *testing.T) {
	env, err := routingRuleCELEnv()
	require.NoError(t, err)
	cases := []struct {
		condition string
		free      string
		perTier   [3]string
	}{
		{`complexity_tier == "SIMPLE"`, "false", [3]string{"true", "false", "false"}},
		{`model == "a" && complexity_tier == "SIMPLE"`, "false", [3]string{`model == "a"`, "false", "false"}},
		{`complexity_tier != "SIMPLE" && model == "a"`, "false", [3]string{"false", `model == "a"`, `model == "a"`}},
		{`complexity_tier in ["SIMPLE", "MEDIUM"] && headers["x-a"] == "1"`, "false", [3]string{`headers["x-a"] == "1"`, `headers["x-a"] == "1"`, "false"}},
		{`model == "a" || complexity_tier == "SIMPLE"`, `model == "a"`, [3]string{"true", `model == "a"`, `model == "a"`}},
		{`!(model == "a" && complexity_tier == "SIMPLE")`, `!(model == "a")`, [3]string{`!(model == "a")`, "true", "true"}},
		{`complexity_tier == "SIMPLE" ? model == "a" : headers["x-a"] == "1"`, "false", [3]string{`model == "a"`, `headers["x-a"] == "1"`, `headers["x-a"] == "1"`}},
		{`complexity_tier == headers["x-tier"]`, "false", [3]string{`"SIMPLE" == headers["x-tier"]`, `"MEDIUM" == headers["x-tier"]`, `"COMPLEX" == headers["x-tier"]`}},
		// Every tier holds, but a request with no tier does not: the rule matched nothing unclassified.
		{`complexity_tier == "SIMPLE" || complexity_tier != "SIMPLE"`, "false", [3]string{"true", "true", "true"}},
		{`complexity_tier == "simple"`, "false", [3]string{"false", "false", "false"}},
	}
	for _, tc := range cases {
		t.Run(tc.condition, func(t *testing.T) {
			ast, issues := env.Compile(tc.condition)
			require.NoError(t, issues.Err())
			conditions, err := splitRoutingRuleTiers(env, ast, tc.condition)
			require.NoError(t, err)
			assert.Equal(t, tc.free, conditions.free, "without a tier")
			assert.Equal(t, tc.perTier, conditions.perTier, "on SIMPLE, MEDIUM, COMPLEX")
			assert.False(t, conditions.inexact)
		})
	}

	t.Run("a tier a loop or a compared group could decide without is flagged", func(t *testing.T) {
		// Hand-written only. The routers decide once the request is classified, which is where
		// they can differ from the rule, so the rule's note says so.
		for _, condition := range []string{
			`["SIMPLE","MEDIUM"].exists(t, t == complexity_tier || model == "a")`,
			`["SIMPLE","MEDIUM"].all(t, t != complexity_tier || model == "a")`,
			`(complexity_tier == "SIMPLE" || model == "a") == true`,
		} {
			rule := migrationRule("r", condition, 0, migrationRuleTarget("openai", "gpt-4o", 1))
			plan := planRoutingRule(env, &rule)
			assert.NotEmpty(t, plan.pieces, condition)
			assert.NotEmpty(t, plan.note.Flagged, condition)
		}
	})

	t.Run("text that only looks like the tier is left alone", func(t *testing.T) {
		condition := `headers["complexity_tier"] == "complexity_tier" && complexity_tier == "MEDIUM"`
		ast, issues := env.Compile(condition)
		require.NoError(t, issues.Err())
		conditions, err := splitRoutingRuleTiers(env, ast, condition)
		require.NoError(t, err)
		assert.Equal(t, `headers["complexity_tier"] == "complexity_tier"`, conditions.perTier[1])
	})
}

// TestModelRoutersFromRoutingRulesCopiesARuleWithoutTheTier pins that a rule that never reads the
// tier becomes one conditional router that carries everything the rule had, unchanged.
func TestModelRoutersFromRoutingRulesCopiesARuleWithoutTheTier(t *testing.T) {
	target := migrationRuleTarget("azure", "gpt-4o", 1)
	target.KeyID = bifrost.Ptr("azure-key")
	target.TTFTTimeoutMs = bifrost.Ptr(1500)
	rule := scopedMigrationRule(migrationRule("support", `(model == "gpt-4o" || headers["X-App"] == "support") && budget_used < 80.0`, 30, target), "team", "team-1")
	rule.Description = "Support traffic"
	rule.ChainRule = true
	rule.ParsedQuery = map[string]any{"combinator": "and", "rules": []any{}}
	require.NoError(t, json.Unmarshal([]byte(`["anthropic/claude-sonnet", "openai/", {"provider": "openai", "model": "gpt-4o-mini", "key_id": "openai-key"}]`), &rule.ParsedFallbacks))

	migration := migratedRouters(t, rule)
	require.Len(t, migration.Routers, 1)
	router := migration.Routers[0]

	assert.Equal(t, "support", router.ID)
	assert.Equal(t, "Rule support", router.Name)
	assert.Equal(t, "Support traffic", router.Description)
	assert.Equal(t, tables.ModelRouterModeConditional, router.Mode)
	assert.Equal(t, &tables.ModelRouterMeta{RoutingRuleIDs: []string{"support"}}, router.Meta, "the router names the rule it was made from")
	assert.Equal(t, &ModelRouterRouting{
		Enabled: true, Scope: "team", ScopeID: bifrost.Ptr("team-1"), Priority: 30, Chain: true,
		CelExpression: rule.CelExpression, Query: rule.ParsedQuery,
	}, router.Routing, "the condition is the rule's own text, with the builder state that describes it")
	require.Len(t, router.Targets, 1)
	assert.Equal(t, []string{" azure/gpt-4o@azure-key 1"}, describeModelRouterTargets(router.Targets), "a conditional router's target has no tier")
	assert.Equal(t, 1500, *router.Targets[0].TTFTTimeoutMs)
	assert.Empty(t, router.Targets[0].ParsedFallbacks, "a conditional router's targets carry no fallbacks")
	fallbacks, err := json.Marshal(router.Fallbacks)
	require.NoError(t, err)
	assert.JSONEq(t, `["anthropic/claude-sonnet", "openai/", {"provider": "openai", "model": "gpt-4o-mini", "key_id": "openai-key"}]`, string(fallbacks), "the rule's fallbacks stay one list on its router, as they were written")

	note := migrationNote(t, migration, "support")
	assert.Equal(t, []string{"support"}, note.RouterIDs)
	assert.Empty(t, note.Adjusted)
	assert.Empty(t, note.Flagged)
}

// TestModelRoutersFromRoutingRulesConditions covers the conditions that are not ordinary CEL: none
// at all, blank space, and one that does not compile.
func TestModelRoutersFromRoutingRulesConditions(t *testing.T) {
	target := migrationRuleTarget("openai", "gpt-4o", 1)
	migration := migratedRouters(t,
		migrationRule("all", "", 0, target),
		migrationRule("blank", "  \n", 1, target),
		migrationRule("broken", `model == `, 2, target),
		migrationRule("wordy", `gpt`, 3, target),
	)
	assert.Equal(t, []string{
		"all 0 conditional []",
		"blank 1 conditional [false]",
	}, describeMigratedRouters(migration.Routers), "no condition takes every request; blank space never compiled, so it takes none")
	assert.NotEmpty(t, migrationNote(t, migration, "blank").Adjusted)
	for _, id := range []string{"broken", "wordy"} {
		note := migrationNote(t, migration, id)
		assert.Empty(t, note.RouterIDs, id)
		assert.NotEmpty(t, note.Flagged, "a rule that is not migrated says why: %s", id)
	}
}

// TestModelRoutersFromRoutingRulesTierRule pins the router a plain tier rule becomes: the rest of
// its condition, its targets on its tiers, a copy of the Complexity Router settings, and a request
// on any other tier passed on to the next router, as it went on to the next rule.
func TestModelRoutersFromRoutingRulesTierRule(t *testing.T) {
	settings := testSessionAnalyzerConfig()
	settings.Suggestions = &ComplexitySuggestionsConfig{Enabled: false}
	settings.ConfigHashes = ComplexityAnalyzerConfigHashes{SimpleKeywords: "hash"}
	settings.EmbeddingFingerprint = "fingerprint"
	rule := migrationRule("cheap", `model == "gpt-4o" && complexity_tier in ["SIMPLE", "MEDIUM"]`, 5,
		migrationRuleTarget("openai", "gpt-4o-mini", 0.75), migrationRuleTarget("groq", "llama", 0.25))
	require.NoError(t, json.Unmarshal([]byte(`{"combinator": "and", "rules": [
		{"field": "model", "operator": "=", "value": "gpt-4o"},
		{"field": "complexity_tier", "operator": "in", "value": "SIMPLE,MEDIUM"}
	]}`), &rule.ParsedQuery))
	require.NoError(t, json.Unmarshal([]byte(`["anthropic/claude-haiku"]`), &rule.ParsedFallbacks))

	migration, err := ModelRoutersFromRoutingRules([]tables.TableRoutingRule{rule}, settings, nil)
	require.NoError(t, err)
	require.Len(t, migration.Routers, 1)
	router := migration.Routers[0]

	assert.Equal(t, tables.ModelRouterModeClassifier, router.Mode)
	assert.Equal(t, &ModelRouterRouting{
		Enabled: true, Scope: "global", Priority: 5, CelExpression: `model == "gpt-4o"`,
		EmptyTarget: tables.ModelRouterEmptyTargetNextRouter,
		Query:       map[string]any{"combinator": "and", "rules": []any{map[string]any{"field": "model", "operator": "=", "value": "gpt-4o"}}},
	}, router.Routing, "the tier is gone from the condition and from the builder state that describes it")
	assert.Equal(t, []string{
		"SIMPLE openai/gpt-4o-mini@ 0.75", "SIMPLE groq/llama@ 0.25",
		"MEDIUM openai/gpt-4o-mini@ 0.75", "MEDIUM groq/llama@ 0.25",
	}, describeModelRouterTargets(router.Targets))
	for _, target := range router.Targets {
		assert.Equal(t, "anthropic/claude-haiku", target.ParsedFallbacks[0].String())
	}

	want := settings.Normalized()
	want.Suggestions, want.ConfigHashes, want.EmbeddingFingerprint = nil, ComplexityAnalyzerConfigHashes{}, ""
	assert.Equal(t, want, router.ComplexityAnalyzerConfig, "every setting that classifies is copied, session memory included; the suggestions block and the page's bookkeeping are not")
}

// TestModelRoutersFromRoutingRulesBuilderState pins what the dashboard opens a router with. A rule
// made in the condition builder keeps its builder state, so its router opens in the builder. A
// rule that does not read the tier keeps the state as it is. A rule that is a group joined by and
// with the tier as an entry of its own keeps the state without that entry, which is the rest of
// its condition. Any other state is not kept, and the router opens on its condition text.
func TestModelRoutersFromRoutingRulesBuilderState(t *testing.T) {
	target := migrationRuleTarget("openai", "gpt-4o-mini", 1)
	state := func(text string) map[string]any {
		var parsed map[string]any
		require.NoError(t, json.Unmarshal([]byte(text), &parsed))
		return parsed
	}
	const tierEntry = `{"id": "t", "field": "complexity_tier", "operator": "=", "value": "SIMPLE"}`
	const providerEntry = `{"id": "p", "field": "provider", "operator": "=", "value": "bedrock"}`
	cases := []struct {
		name      string
		condition string
		state     string
		want      string // "" when the builder state is not kept
	}{
		{"the tier and one more entry", `complexity_tier == "SIMPLE" && provider == "bedrock"`,
			`{"id": "g", "combinator": "and", "rules": [` + tierEntry + `, ` + providerEntry + `]}`,
			`{"id": "g", "combinator": "and", "rules": [` + providerEntry + `]}`},
		{"the tier between other entries and a group", `model == "gpt-4o" && complexity_tier == "SIMPLE" && (provider == "bedrock" || provider == "azure")`,
			`{"combinator": "and", "rules": [{"field": "model", "operator": "=", "value": "gpt-4o"}, ` + tierEntry + `, {"combinator": "or", "rules": [` + providerEntry + `, {"field": "provider", "operator": "=", "value": "azure"}]}]}`,
			`{"combinator": "and", "rules": [{"field": "model", "operator": "=", "value": "gpt-4o"}, {"combinator": "or", "rules": [` + providerEntry + `, {"field": "provider", "operator": "=", "value": "azure"}]}]}`},
		{"only the tier", `complexity_tier == "SIMPLE"`,
			`{"combinator": "and", "rules": [` + tierEntry + `]}`,
			`{"combinator": "and", "rules": []}`},
		{"two entries on the tier", `complexity_tier != "MEDIUM" && complexity_tier != "COMPLEX" && provider == "bedrock"`,
			`{"combinator": "and", "rules": [{"field": "complexity_tier", "operator": "!=", "value": "MEDIUM"}, {"field": "complexity_tier", "operator": "!=", "value": "COMPLEX"}, ` + providerEntry + `]}`,
			`{"combinator": "and", "rules": [` + providerEntry + `]}`},
		{"the tier inside a group", `provider == "bedrock" && (complexity_tier == "SIMPLE" && model == "gpt-4o")`,
			`{"combinator": "and", "rules": [` + providerEntry + `, {"combinator": "and", "rules": [` + tierEntry + `, {"field": "model", "operator": "=", "value": "gpt-4o"}]}]}`, ""},
		{"a group joined by or", `complexity_tier == "SIMPLE"`, `{"combinator": "or", "rules": [` + tierEntry + `]}`, ""},
		{"a negated group", `complexity_tier != "SIMPLE" && provider == "bedrock"`, `{"combinator": "and", "not": true, "rules": [` + tierEntry + `, ` + providerEntry + `]}`, ""},
		{"a state that does not mention the tier", `complexity_tier == "SIMPLE" && provider == "bedrock"`, `{"combinator": "and", "rules": [` + providerEntry + `]}`, ""},
		{"no state", `complexity_tier == "SIMPLE" && provider == "bedrock"`, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rule := migrationRule("r", tc.condition, 0, target)
			if tc.state != "" {
				rule.ParsedQuery = state(tc.state)
			}
			migration := migratedRouters(t, rule)
			require.Len(t, migration.Routers, 1)
			if tc.want == "" {
				assert.Nil(t, migration.Routers[0].Routing.Query)
			} else {
				assert.Equal(t, state(tc.want), migration.Routers[0].Routing.Query)
			}
			if tc.state != "" {
				assert.Equal(t, state(tc.state), rule.ParsedQuery, "the rule's own builder state is left as it is")
			}
		})
	}

	t.Run("a rule that became several routers opens on its condition text", func(t *testing.T) {
		rule := migrationRule("split", `provider == "bedrock" || complexity_tier == "SIMPLE"`, 0, target)
		rule.ParsedQuery = state(`{"combinator": "or", "rules": [` + providerEntry + `, ` + tierEntry + `]}`)
		for _, router := range migratedRouters(t, rule).Routers {
			assert.Nil(t, router.Routing.Query, router.ID)
		}
	})

	t.Run("rules that share a router show the first rule's state", func(t *testing.T) {
		simple := migrationRule("simple", `complexity_tier == "SIMPLE" && provider == "bedrock"`, 0, target)
		simple.ParsedQuery = state(`{"combinator": "and", "rules": [` + tierEntry + `, ` + providerEntry + `]}`)
		complexRule := migrationRule("complex", `complexity_tier == "COMPLEX" && provider == "bedrock"`, 1, target)
		complexRule.ParsedQuery = state(`{"combinator": "and", "rules": [{"field": "complexity_tier", "operator": "=", "value": "COMPLEX"}, ` + providerEntry + `]}`)
		migration := migratedRouters(t, simple, complexRule)
		require.Len(t, migration.Routers, 1)
		assert.Equal(t, state(`{"combinator": "and", "rules": [`+providerEntry+`]}`), migration.Routers[0].Routing.Query)
	})
}

// TestModelRoutersFromRoutingRulesMergesTierRules pins the merge: tier rules that sit next to each
// other with the same remaining condition and different tiers are one classification followed by
// a choice on the tier, so they become one router.
func TestModelRoutersFromRoutingRulesMergesTierRules(t *testing.T) {
	simple := migrationRule("simple", `complexity_tier == "SIMPLE"`, 10, migrationRuleTarget("openai", "gpt-4o-mini", 1))
	medium := migrationRule("medium", `complexity_tier == "MEDIUM"`, 11, migrationRuleTarget("openai", "gpt-4o", 1))
	complexRule := migrationRule("complex", `complexity_tier == "COMPLEX"`, 12, migrationRuleTarget("anthropic", "claude-opus", 1))
	require.NoError(t, json.Unmarshal([]byte(`["openai/gpt-4o"]`), &complexRule.ParsedFallbacks))

	t.Run("three tiers become one router", func(t *testing.T) {
		migration := migratedRouters(t, complexRule, simple, medium)
		assert.Equal(t, []string{"simple 10 classifier []"}, describeMigratedRouters(migration.Routers), "the router has the first rule's id and priority")
		router := migration.Routers[0]
		assert.Equal(t, "Rule simple", router.Name)
		assert.Equal(t, []string{"SIMPLE openai/gpt-4o-mini@ 1", "MEDIUM openai/gpt-4o@ 1", "COMPLEX anthropic/claude-opus@ 1"}, describeModelRouterTargets(router.Targets))
		assert.Empty(t, router.Targets[0].ParsedFallbacks, "a rule's fallbacks stay on that rule's targets")
		assert.Len(t, router.Targets[2].ParsedFallbacks, 1)
		assert.Equal(t, tables.ModelRouterEmptyTargetNextRouter, router.Routing.EmptyTarget)
		for _, id := range []string{"simple", "medium", "complex"} {
			assert.Equal(t, []string{"simple"}, migrationNote(t, migration, id).RouterIDs, id)
		}
		assert.Equal(t, []string{"simple", "medium", "complex"}, router.Meta.RoutingRuleIDs, "the router names every rule it took over, in their order")
		assert.Empty(t, migrationNote(t, migration, "simple").Adjusted)
		assert.Contains(t, strings.Join(migrationNote(t, migration, "complex").Adjusted, " "), `shares router "simple"`, "a rule whose id and name are not kept says so")
	})

	t.Run("the same condition beside the tier still merges", func(t *testing.T) {
		a := migrationRule("a", `model == "gpt-4o" && complexity_tier == "SIMPLE"`, 0, migrationRuleTarget("openai", "gpt-4o-mini", 1))
		b := migrationRule("b", `complexity_tier != "SIMPLE" && model == "gpt-4o"`, 1, migrationRuleTarget("anthropic", "claude-opus", 1))
		migration := migratedRouters(t, a, b)
		assert.Equal(t, []string{`a 0 classifier [model == "gpt-4o"]`}, describeMigratedRouters(migration.Routers))
		assert.Equal(t, []string{"SIMPLE openai/gpt-4o-mini@ 1", "MEDIUM anthropic/claude-opus@ 1", "COMPLEX anthropic/claude-opus@ 1"}, describeModelRouterTargets(migration.Routers[0].Targets))
	})

	// Anything that differs between two rules keeps them two routers.
	apart := []struct {
		name   string
		second func(rule tables.TableRoutingRule) []tables.TableRoutingRule
		want   []string
	}{
		{"another condition", func(rule tables.TableRoutingRule) []tables.TableRoutingRule {
			rule.CelExpression = `model == "gpt-4o" && complexity_tier == "MEDIUM"`
			return []tables.TableRoutingRule{rule}
		}, []string{"simple 10 classifier []", `medium 11 classifier [model == "gpt-4o"]`}},
		{"the same tier", func(rule tables.TableRoutingRule) []tables.TableRoutingRule {
			rule.CelExpression = `complexity_tier in ["SIMPLE", "MEDIUM"]`
			return []tables.TableRoutingRule{rule}
		}, []string{"simple 10 classifier []", "medium 11 classifier []"}},
		{"a rule that is off", func(rule tables.TableRoutingRule) []tables.TableRoutingRule {
			rule.Enabled = bifrost.Ptr(false)
			return []tables.TableRoutingRule{rule}
		}, []string{"simple 10 classifier []", "medium 11 classifier [] off"}},
		{"a rule that chains", func(rule tables.TableRoutingRule) []tables.TableRoutingRule {
			rule.ChainRule = true
			return []tables.TableRoutingRule{rule}
		}, []string{"simple 10 classifier []", "medium 11 classifier [] chain"}},
		{"another team", func(rule tables.TableRoutingRule) []tables.TableRoutingRule {
			return []tables.TableRoutingRule{scopedMigrationRule(rule, "team", "team-2")}
		}, []string{"simple 10 classifier []", "medium 11 classifier []"}},
		{"a rule between them", func(rule tables.TableRoutingRule) []tables.TableRoutingRule {
			rule.Priority = 12
			return []tables.TableRoutingRule{migrationRule("between", `model == "o3"`, 11, migrationRuleTarget("openai", "o3-mini", 1)), rule}
		}, []string{"simple 10 classifier []", `between 11 conditional [model == "o3"]`, "medium 12 classifier []"}},
		{"a rule that had to be split", func(rule tables.TableRoutingRule) []tables.TableRoutingRule {
			rule.CelExpression = `model == "o3" || complexity_tier == "MEDIUM"`
			return []tables.TableRoutingRule{rule}
		}, []string{"simple 10 classifier []", `medium 11 conditional [model == "o3"]`, "medium-2 12 classifier []"}},
	}
	for _, tc := range apart {
		t.Run("kept apart by "+tc.name, func(t *testing.T) {
			rules := append([]tables.TableRoutingRule{simple}, tc.second(medium)...)
			assert.Equal(t, tc.want, describeMigratedRouters(migratedRouters(t, rules...).Routers))
		})
	}

	t.Run("two teams with the same rules stay two routers", func(t *testing.T) {
		migration := migratedRouters(t, scopedMigrationRule(simple, "team", "team-1"), scopedMigrationRule(medium, "team", "team-2"))
		require.Len(t, migration.Routers, 2)
		assert.Equal(t, "team-1", *migration.Routers[0].Routing.ScopeID)
		assert.Equal(t, "team-2", *migration.Routers[1].Routing.ScopeID)
	})
}

// TestModelRoutersFromRoutingRulesSplitsATierUnderAnOr pins the split: a rule whose condition can
// hold without a tier becomes a conditional router for that part, placed first so those requests
// are still never classified, then a classifier router for what each tier is left with. They carry
// the rule's id as their origin, so a chained rule still fires once.
func TestModelRoutersFromRoutingRulesSplitsATierUnderAnOr(t *testing.T) {
	rule := migrationRule("pro", `headers["X-Plan"] == "pro" || complexity_tier == "COMPLEX"`, 3, migrationRuleTarget("anthropic", "claude-opus", 1))
	rule.ChainRule = true
	after := migrationRule("azure", `model == "claude-opus"`, 4, migrationRuleTarget("azure", "", 1))

	migration := migratedRouters(t, rule, after)
	assert.Equal(t, []string{
		`pro 3 conditional [headers["x-plan"] == "pro"] chain`,
		`pro-2 4 classifier [] chain`,
		`azure 5 conditional [model == "claude-opus"]`,
	}, describeMigratedRouters(migration.Routers), "SIMPLE and MEDIUM are left with the header part, which the first router already takes")
	assert.Equal(t, "Rule pro (2)", migration.Routers[1].Name)
	assert.Equal(t, []string{" anthropic/claude-opus@ 1"}, describeModelRouterTargets(migration.Routers[0].Targets))
	assert.Equal(t, []string{"COMPLEX anthropic/claude-opus@ 1"}, describeModelRouterTargets(migration.Routers[1].Targets))

	assert.Equal(t, []string{"pro", "pro-2"}, migrationNote(t, migration, "pro").RouterIDs)
	assert.Contains(t, strings.Join(migrationNote(t, migration, "azure").Adjusted, " "), "priority 4 became 5", "a rule pushed along says so")

	t.Run("a split rule with no target does not classify what it cannot route", func(t *testing.T) {
		idle := migrationRule("idle", `model == "o3" || complexity_tier == "COMPLEX"`, 0)
		migration := migratedRouters(t, idle)
		assert.Equal(t, []string{
			`idle 0 conditional [model == "o3"]`,
			`idle-2 1 classifier [] off`,
		}, describeMigratedRouters(migration.Routers), "the first router passes every request on; the second would classify the ones the rule decided without a tier")
		assert.Contains(t, strings.Join(migrationNote(t, migration, "idle").Adjusted, " "), "no target")
	})

	t.Run("each condition the tiers are left with gets its own router", func(t *testing.T) {
		rule := migrationRule("by-header", `complexity_tier == headers["x-tier"]`, 0, migrationRuleTarget("openai", "gpt-4o", 1))
		assert.Equal(t, []string{
			`by-header 0 classifier ["SIMPLE" == headers["x-tier"]]`,
			`by-header-2 1 classifier ["MEDIUM" == headers["x-tier"]]`,
			`by-header-3 2 classifier ["COMPLEX" == headers["x-tier"]]`,
		}, describeMigratedRouters(migratedRouters(t, rule).Routers))
	})
}

// TestModelRoutersFromRoutingRulesThatNeverMatched covers rules that route nothing today. Their
// routers must route nothing either, so they are created turned off, or not at all.
func TestModelRoutersFromRoutingRulesThatNeverMatched(t *testing.T) {
	target := migrationRuleTarget("openai", "gpt-4o-mini", 1)

	t.Run("a tier in the wrong letter case", func(t *testing.T) {
		// Tiers are upper case, so this rule never matched.
		migration := migratedRouters(t, migrationRule("lower", `model == "gpt-4o" && complexity_tier == "simple"`, 0, target))
		assert.Equal(t, []string{"lower 0 conditional [false] off"}, describeMigratedRouters(migration.Routers))
		assert.Equal(t, []string{" openai/gpt-4o-mini@ 1"}, describeModelRouterTargets(migration.Routers[0].Targets), "its targets are kept")
		assert.NotEmpty(t, migrationNote(t, migration, "lower").Flagged)
	})

	t.Run("a list with one tier in the wrong letter case routes as it does today", func(t *testing.T) {
		migration := migratedRouters(t, migrationRule("mixed", `complexity_tier in ["SIMPLE", "Medium"]`, 0, target))
		assert.Equal(t, []string{"mixed 0 classifier []"}, describeMigratedRouters(migration.Routers), "it matches SIMPLE today, so its router is on")
		assert.Equal(t, []string{"SIMPLE openai/gpt-4o-mini@ 1"}, describeModelRouterTargets(migration.Routers[0].Targets))
		assert.Empty(t, migrationNote(t, migration, "mixed").Flagged)
	})

	t.Run("a condition no tier satisfies", func(t *testing.T) {
		migration := migratedRouters(t, migrationRule("never", `complexity_tier == "SIMPLE" && complexity_tier == "MEDIUM"`, 0, target))
		assert.Equal(t, []string{"never 0 conditional [false] off"}, describeMigratedRouters(migration.Routers))
		assert.Equal(t, []string{" openai/gpt-4o-mini@ 1"}, describeModelRouterTargets(migration.Routers[0].Targets), "its targets are kept")
		assert.NotEmpty(t, migrationNote(t, migration, "never").Flagged)
	})

	t.Run("a scope no request has", func(t *testing.T) {
		unscoped := migrationRule("no-team", `model == "gpt-4o"`, 0, target)
		unscoped.Scope = "team"
		odd := migrationRule("odd", `model == "gpt-4o"`, 0, target)
		odd.Scope = "workspace"
		migration := migratedRouters(t, unscoped, odd)
		assert.Empty(t, migration.Routers)
		assert.NotEmpty(t, migrationNote(t, migration, "no-team").Flagged)
		assert.NotEmpty(t, migrationNote(t, migration, "odd").Flagged)
	})

	t.Run("a tier rule with no Complexity Router settings saved", func(t *testing.T) {
		migration, err := ModelRoutersFromRoutingRules([]tables.TableRoutingRule{migrationRule("tier", `complexity_tier == "SIMPLE"`, 0, target)}, nil, nil)
		require.NoError(t, err)
		require.Len(t, migration.Routers, 1)
		router := migration.Routers[0]
		assert.True(t, router.Routing.Enabled, "the rule was on; with no classifier its router takes nothing, as the rule did")
		assert.Nil(t, router.Semantic)
		assert.Nil(t, router.Decision)
		assert.Nil(t, router.LLM)
		assert.NotEmpty(t, router.Keywords.SimpleKeywords, "a classifier router is saved with phrases")
		assert.NoError(t, router.ComplexityAnalyzerConfig.Validate())
		assert.Contains(t, strings.Join(migrationNote(t, migration, "tier").Adjusted, " "), "no saved settings")
	})
}

// TestModelRoutersFromRoutingRulesPriorities pins the order. Rules keep their numbers wherever a
// router can hold them, the order within a scope never changes, and the routers that already exist
// stay after the ones made from the rules of their scope, because rules ran before routers.
func TestModelRoutersFromRoutingRulesPriorities(t *testing.T) {
	target := migrationRuleTarget("openai", "gpt-4o", 1)
	rule := func(id string, priority int, created time.Time) tables.TableRoutingRule {
		r := migrationRule(id, `model == "`+id+`"`, priority, target)
		r.CreatedAt = created
		return r
	}
	day := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

	t.Run("gaps are kept", func(t *testing.T) {
		migration := migratedRouters(t, rule("c", 5000, day), rule("a", 10, day), rule("b", 20, day))
		assert.Equal(t, []string{`a 10 conditional [model == "a"]`, `b 20 conditional [model == "b"]`, `c 5000 conditional [model == "c"]`}, describeMigratedRouters(migration.Routers))
		assert.Empty(t, migrationNote(t, migration, "c").Adjusted)
	})

	t.Run("a scope with a negative priority moves up as a whole", func(t *testing.T) {
		migration := migratedRouters(t, rule("b", 0, day), rule("a", -5, day), rule("c", 7, day))
		assert.Equal(t, []string{`a 0 conditional [model == "a"]`, `b 5 conditional [model == "b"]`, `c 12 conditional [model == "c"]`}, describeMigratedRouters(migration.Routers))
	})

	t.Run("rules with one priority keep the order they were evaluated in", func(t *testing.T) {
		// Newest first, then by id.
		migration := migratedRouters(t, rule("old", 1, day), rule("b-new", 1, day.Add(time.Hour)), rule("a-new", 1, day.Add(time.Hour)), rule("last", 2, day))
		assert.Equal(t, []string{
			`a-new 1 conditional [model == "a-new"]`, `b-new 2 conditional [model == "b-new"]`,
			`old 3 conditional [model == "old"]`, `last 4 conditional [model == "last"]`,
		}, describeMigratedRouters(migration.Routers))
	})

	t.Run("scopes are numbered on their own", func(t *testing.T) {
		migration := migratedRouters(t, rule("global", 0, day), scopedMigrationRule(rule("team", 0, day), "team", "team-1"), scopedMigrationRule(rule("key", 0, day), "virtual_key", "vk-1"))
		for _, router := range migration.Routers {
			assert.Equal(t, 0, router.Routing.Priority, router.ID)
		}
	})

	t.Run("existing routers stay after the rules of their scope", func(t *testing.T) {
		existing := []ModelRouter{
			*testModelRouterWithRouting("first", "First", ModelRouterRouting{Enabled: true, Scope: "global", Priority: 0}),
			*testModelRouterWithRouting("second", "Second", ModelRouterRouting{Enabled: true, Scope: "global", Priority: 1}),
			*testModelRouterWithRouting("far", "Far", ModelRouterRouting{Enabled: false, Scope: "global", Priority: 900}),
			*testModelRouterWithRouting("team", "Team", ModelRouterRouting{Enabled: true, Scope: "team", ScopeID: bifrost.Ptr("team-1"), Priority: 0}),
			*testModelRouter("unrouted", "Unrouted"),
		}
		migration, err := ModelRoutersFromRoutingRules([]tables.TableRoutingRule{rule("a", 0, day), rule("b", 4, day)}, testSemanticAnalyzerConfig(), existing)
		require.NoError(t, err)
		moved := make([]string, 0, len(migration.Moved))
		for _, change := range migration.Moved {
			moved = append(moved, fmt.Sprintf("%s %d", change.RouterID, change.Routing.Priority))
			assert.Equal(t, "global", change.Routing.Scope)
		}
		assert.Equal(t, []string{"first 5", "second 6"}, moved, "a router whose number is free keeps it, and a scope with no rules is left alone")
	})
}

// TestModelRoutersFromRoutingRulesIDs pins that a rule keeps its id as a router, and what happens
// when it cannot: a router or the reserved id already holds it.
func TestModelRoutersFromRoutingRulesIDs(t *testing.T) {
	target := migrationRuleTarget("openai", "gpt-4o", 1)
	odd := strings.Repeat("é", 250) + " a/b"
	rules := []tables.TableRoutingRule{
		migrationRule("3f0c7a52-6a1e-4c59-9b54-1c2d3e4f5a6b", `model == "a"`, 0, target),
		migrationRule(odd, `model == "b"`, 1, target),
		migrationRule("taken", `model == "c"`, 2, target),
		migrationRule("defaults", `model == "d"`, 3, target),
		migrationRule("split", `model == "e" || complexity_tier == "SIMPLE"`, 4, target),
		migrationRule("split-2", `model == "f"`, 6, target),
	}
	rules[1].Name = "An id of 254 characters with a space and a slash"
	migration, err := ModelRoutersFromRoutingRules(rules, testSemanticAnalyzerConfig(), []ModelRouter{*testModelRouter("taken", "Taken"), *testModelRouter("taken-rule", "Taken too")})
	require.NoError(t, err)

	ids := make([]string, 0, len(migration.Routers))
	for _, router := range migration.Routers {
		ids = append(ids, router.ID)
		assert.NoError(t, ValidateModelRouterIdentity(&router), router.ID)
	}
	assert.Equal(t, []string{
		"3f0c7a52-6a1e-4c59-9b54-1c2d3e4f5a6b", odd,
		"taken-rule-2", "defaults-rule",
		"split", "split-2-2", "split-2",
	}, ids, "a rule's own id is never given to another rule's router")
	byID := make(map[string]ModelRouter, len(migration.Routers))
	for _, router := range migration.Routers {
		byID[router.ID] = router
	}
	assert.Equal(t, []string{"taken"}, byID["taken-rule-2"].Meta.RoutingRuleIDs, "a router whose id changed remembers the rule")
	assert.Equal(t, []string{"defaults"}, byID["defaults-rule"].Meta.RoutingRuleIDs)
	assert.Equal(t, []string{"split"}, byID["split-2-2"].Meta.RoutingRuleIDs)
	assert.Equal(t, []string{"split-2"}, byID["split-2"].Meta.RoutingRuleIDs)
}

// TestModelRoutersFromRoutingRulesTargets pins how a rule's targets are written for a router. The
// routing rules picked in proportion to weight among targets that are not negative, and evenly
// when every weight is 0; a router's weights are above 0 and sum to 1, so the same pick is written
// that way.
func TestModelRoutersFromRoutingRulesTargets(t *testing.T) {
	weighted := func(weights ...float64) []tables.TableRoutingTarget {
		targets := make([]tables.TableRoutingTarget, len(weights))
		for i, weight := range weights {
			targets[i] = migrationRuleTarget("openai", "model-"+strconv.Itoa(i), weight)
		}
		return targets
	}
	cases := []struct {
		name     string
		targets  []tables.TableRoutingTarget
		want     []string
		adjusted bool
	}{
		{"weights that sum to 1 are kept", weighted(0.7, 0.3), []string{" openai/model-0@ 0.7", " openai/model-1@ 0.3"}, false},
		{"weights that do not are scaled", weighted(3, 1), []string{" openai/model-0@ 0.75", " openai/model-1@ 0.25"}, true},
		{"a weight of 0 beside others is never picked", weighted(0, 2, 2), []string{" openai/model-1@ 0.5", " openai/model-2@ 0.5"}, true},
		{"all weights 0 pick evenly", weighted(0, 0), []string{" openai/model-0@ 0.5", " openai/model-1@ 0.5"}, true},
		{"a negative weight is never picked", weighted(-1, 1), []string{" openai/model-1@ 1"}, true},
		{"only negative weights leave no target", weighted(-1, -2), []string{}, true},
		{"no targets", nil, []string{}, false},
		{"a target that keeps the request's provider and model", []tables.TableRoutingTarget{migrationRuleTarget("", "", 1)}, []string{" /@ 1"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			migration := migratedRouters(t, migrationRule("r", `model == "gpt-4o"`, 0, tc.targets...))
			require.Len(t, migration.Routers, 1, "a rule with no usable target still becomes a router, which passes every request on as the rule did")
			router := migration.Routers[0]
			assert.Equal(t, tc.want, describeModelRouterTargets(router.Targets))
			assert.NoError(t, ValidateModelRouterTargets(router.Mode, slices.Clone(router.Targets)))
			assert.Equal(t, tc.adjusted, len(migrationNote(t, migration, "r").Adjusted) > 0)
		})
	}

	t.Run("a first-token deadline of 0 is none", func(t *testing.T) {
		target := migrationRuleTarget("openai", "gpt-4o", 1)
		target.TTFTTimeoutMs = bifrost.Ptr(0)
		assert.Nil(t, migratedRouters(t, migrationRule("r", "", 0, target)).Routers[0].Targets[0].TTFTTimeoutMs)
	})
}

// generatedMigrationRules builds a rule set from a seed: every scope, condition shape, target
// shape and odd value the mapping has a case for, in an order of its own.
func generatedMigrationRules(seed uint64, count int) []tables.TableRoutingRule {
	random := mathrand.New(mathrand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	conditions := []string{
		"", `model == "gpt-4o"`, `model.startsWith("gpt") && headers["X-App"] == "support"`,
		`request_type == "chat_completion" || params["Region"] == "eu"`, `budget_used > 80.0`, `!("x-skip" in headers)`,
		`complexity_tier == "SIMPLE"`, `complexity_tier == "MEDIUM"`, `complexity_tier == "COMPLEX"`,
		`complexity_tier != "COMPLEX"`, `complexity_tier in ["MEDIUM", "COMPLEX"]`,
		`model == "gpt-4o" && complexity_tier == "SIMPLE"`, `model == "gpt-4o" && complexity_tier == "COMPLEX"`,
		`model == "o3" || complexity_tier == "COMPLEX"`, `complexity_tier == "SIMPLE" ? model == "a" : headers["x-a"] == "1"`,
		`complexity_tier == headers["x-tier"]`, `complexity_tier == "simple"`, `complexity_tier == "SIMPLE" && complexity_tier == "MEDIUM"`,
		"  ", `model ==`,
	}
	scopes := []struct{ scope, id string }{{"global", ""}, {"global", ""}, {"team", "team-1"}, {"team", "team-2"}, {"customer", "customer-1"}, {"virtual_key", "vk-1"}, {"user", "user-1"}}
	rules := make([]tables.TableRoutingRule, 0, count)
	for i := range count {
		rule := migrationRule("rule-"+strconv.Itoa(i), conditions[random.IntN(len(conditions))], random.IntN(40)-5)
		rule.CreatedAt = rule.CreatedAt.Add(time.Duration(random.IntN(4)) * time.Hour)
		rule.Enabled = bifrost.Ptr(random.IntN(6) != 0)
		rule.ChainRule = random.IntN(6) == 0
		if scope := scopes[random.IntN(len(scopes))]; scope.scope != "global" {
			rule = scopedMigrationRule(rule, scope.scope, scope.id)
		}
		// Each target of a rule names another model, as the rules API requires targets to differ.
		for n := random.IntN(4); n > 0; n-- {
			target := migrationRuleTarget([]string{"openai", "anthropic", ""}[random.IntN(3)], []string{"gpt-4o", "claude-opus", ""}[n-1], []float64{1, 0.5, 0.25, 0, 3}[random.IntN(5)])
			if target.Provider != nil && random.IntN(4) == 0 {
				target.KeyID = bifrost.Ptr("key-" + strconv.Itoa(random.IntN(3)))
			}
			if random.IntN(4) == 0 {
				target.TTFTTimeoutMs = bifrost.Ptr(random.IntN(3) * 1000)
			}
			rule.Targets = append(rule.Targets, target)
		}
		if random.IntN(3) == 0 {
			rule.ParsedFallbacks = []tables.RoutingFallback{{Fallback: schemas.Fallback{Provider: "openai", Model: "gpt-4o-mini"}}}
		}
		rules = append(rules, rule)
	}
	return rules
}

// TestModelRoutersFromRoutingRulesHoldForAnyRuleSet checks, on generated rule sets, what must be
// true of every migration whatever the rules are: each router can be saved as a router, no
// condition reads the tier, ids and priorities do not clash, routers keep the order of the rules
// they came from, every rule is accounted for, and the result does not depend on the order the
// rules were read in.
func TestModelRoutersFromRoutingRulesHoldForAnyRuleSet(t *testing.T) {
	env, err := routingRuleCELEnv()
	require.NoError(t, err)
	existing := []ModelRouter{
		*testModelRouterWithRouting("rule-3", "Holds a rule's id", ModelRouterRouting{Enabled: true, Scope: "global", Priority: 2}),
		*testModelRouterWithRouting("existing", "Existing", ModelRouterRouting{Enabled: true, Scope: "team", ScopeID: bifrost.Ptr("team-1"), Priority: 0}),
	}
	for seed := uint64(1); seed <= 60; seed++ {
		rules := generatedMigrationRules(seed, 40)
		migration, err := ModelRoutersFromRoutingRules(rules, testSemanticAnalyzerConfig(), existing)
		require.NoError(t, err, "seed %d", seed)

		ids := map[string]struct{}{"defaults": {}, "rule-3": {}, "existing": {}}
		priorities := map[string]struct{}{}
		var settings *ComplexityAnalyzerConfig
		for _, router := range migration.Routers {
			at := fmt.Sprintf("seed %d router %q", seed, router.ID)
			_, clash := ids[router.ID]
			require.False(t, clash, "%s: its id is in use", at)
			ids[router.ID] = struct{}{}

			identity, routing := router, *router.Routing
			require.NoError(t, ValidateModelRouterIdentity(&identity), at)
			require.NoError(t, ValidateModelRouterRouting(&routing), at)
			require.NoError(t, ValidateModelRouterTargets(router.Mode, slices.Clone(router.Targets)), at)
			if router.Routing.CelExpression != "" {
				ast, issues := env.Compile(router.Routing.CelExpression)
				require.NoError(t, issues.Err(), at)
				require.False(t, routingConditionReadsTier(ast.NativeRep().Expr()), "%s: its condition reads the tier", at)
			}
			if router.Mode == tables.ModelRouterModeClassifier {
				require.NoError(t, router.ComplexityAnalyzerConfig.Validate(), at)
				require.Nil(t, router.Suggestions, "%s: a suggestions block stops routers sharing a classification", at)
				require.Equal(t, tables.ModelRouterEmptyTargetNextRouter, router.Routing.EmptyTarget, at)
				if settings == nil {
					settings = &router.ComplexityAnalyzerConfig
				}
				require.Equal(t, *settings, router.ComplexityAnalyzerConfig, "%s: every router made from a tier rule classifies with the same settings", at)
			} else {
				require.Empty(t, router.Routing.EmptyTarget, at)
			}
			slot := routingRuleScopeKey(router.Routing.Scope, router.Routing.ScopeID) + "#" + strconv.Itoa(router.Routing.Priority)
			_, held := priorities[slot]
			require.False(t, held, "%s: priority %s is taken", at, slot)
			priorities[slot] = struct{}{}
		}
		for _, change := range migration.Moved {
			slot := routingRuleScopeKey(change.Routing.Scope, change.Routing.ScopeID) + "#" + strconv.Itoa(change.Routing.Priority)
			_, held := priorities[slot]
			require.False(t, held, "seed %d: moved router %q lands on a taken priority %s", seed, change.RouterID, slot)
			priorities[slot] = struct{}{}
		}

		// Each router's meta names exactly the rules whose notes point at it, in their order.
		fromRules := map[string][]string{}
		for _, note := range migration.Notes {
			for _, routerID := range note.RouterIDs {
				fromRules[routerID] = append(fromRules[routerID], note.RuleID)
			}
		}
		for _, router := range migration.Routers {
			require.NotNil(t, router.Meta, "seed %d router %q: a router made from rules has meta", seed, router.ID)
			require.Equal(t, fromRules[router.ID], router.Meta.RoutingRuleIDs, "seed %d router %q", seed, router.ID)
		}

		// Every rule has one note, and routers come out in the order their rules were evaluated.
		require.Len(t, migration.Notes, len(rules), "seed %d", seed)
		noted := map[string]struct{}{}
		position := map[string]int{}
		for i, router := range migration.Routers {
			position[router.ID] = i
		}
		last := map[string]int{}
		byID := map[string]tables.TableRoutingRule{}
		for _, rule := range rules {
			byID[rule.ID] = rule
		}
		for _, note := range migration.Notes {
			_, twice := noted[note.RuleID]
			require.False(t, twice, "seed %d: rule %q has two notes", seed, note.RuleID)
			noted[note.RuleID] = struct{}{}
			if len(note.RouterIDs) == 0 {
				require.NotEmpty(t, note.Flagged, "seed %d: rule %q became no router without a reason", seed, note.RuleID)
				continue
			}
			scope := routingRuleScopeKey(byID[note.RuleID].Scope, byID[note.RuleID].ScopeID)
			at, ok := position[note.RouterIDs[0]]
			require.True(t, ok, "seed %d: rule %q names a router that was not made", seed, note.RuleID)
			require.GreaterOrEqual(t, at, last[scope], "seed %d: rule %q's router comes before an earlier rule's", seed, note.RuleID)
			last[scope] = at
		}

		shuffled := slices.Clone(rules)
		mathrand.New(mathrand.NewPCG(seed, 7)).Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		again, err := ModelRoutersFromRoutingRules(shuffled, testSemanticAnalyzerConfig(), existing)
		require.NoError(t, err)
		require.Equal(t, describeMigratedRouters(migration.Routers), describeMigratedRouters(again.Routers), "seed %d: the order the rules are read in must not matter", seed)
	}
}

// BenchmarkModelRoutersFromRoutingRules measures mapping a large installation: 1,000 rules across
// scopes, one in five reading the tier. The migration runs it once, at start-up.
func BenchmarkModelRoutersFromRoutingRules(b *testing.B) {
	rules := generatedMigrationRules(1, 1000)
	settings := testSemanticAnalyzerConfig()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := ModelRoutersFromRoutingRules(rules, settings, nil); err != nil {
			b.Fatal(err)
		}
	}
}
