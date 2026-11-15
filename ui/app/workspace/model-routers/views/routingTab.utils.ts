import type { ModelRouterRouting } from "@/lib/types/complexityRouter";
import { type RoutingRule, RoutingRuleScope } from "@/lib/types/routingRules";

// What narrows the routing rules list to the rules worth reading: the list
// endpoint matches it against each rule's name and expression.
export const COMPLEXITY_RULES_SEARCH = "complexity_tier";

// How many rule names the notice spells out before it counts the rest.
export const MAX_NAMED_COMPLEXITY_RULES = 3;

const STRING_LITERALS = /"(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'/g;
const COMPLEXITY_TIER_IDENTIFIER = /(?:^|[^\w.])complexity_tier(?!\w)/;

// usesComplexityTier reports whether a routing rule's expression reads the
// complexity_tier variable, which is what makes the gateway classify the
// request for that rule. The name inside a string literal or as a field of
// something else (`x.complexity_tier`) is not a read, as the gateway's own
// check has it.
export function usesComplexityTier(celExpression: string | undefined): boolean {
	if (!celExpression) return false;
	return COMPLEXITY_TIER_IDENTIFIER.test(celExpression.replace(STRING_LITERALS, '""'));
}

// complexityRulesFor picks the routing rules that can classify a request this
// router then classifies again: the enabled ones that read complexity_tier.
// A rule scoped to a different entity of the router's own scope kind is left
// out, since no request is in both (one request has one virtual key, one
// team). Every other pairing can share requests: a global rule sees them all,
// and a team's rule sees the requests of a virtual key in that team.
export function complexityRulesFor(rules: RoutingRule[] | undefined, routing: ModelRouterRouting): RoutingRule[] {
	return (rules ?? []).filter((rule) => {
		if (!rule.enabled || !usesComplexityTier(rule.cel_expression)) return false;
		const sameScopeKind = rule.scope === routing.scope && rule.scope !== RoutingRuleScope.Global;
		return !(sameScopeKind && (rule.scope_id ?? "") !== (routing.scope_id ?? ""));
	});
}

// namedComplexityRules splits the rules into the ones the notice names and
// how many it only counts.
export function namedComplexityRules(rules: RoutingRule[]): { named: RoutingRule[]; more: number } {
	const named = rules.slice(0, MAX_NAMED_COMPLEXITY_RULES);
	return { named, more: rules.length - named.length };
}