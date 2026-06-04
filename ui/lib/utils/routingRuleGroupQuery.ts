/**
 * Normalizes the optional `query` field on routing rules for the CEL / react-querybuilder UI.
 * The API stores arbitrary JSON; only objects with a valid `combinator` and `rules` array are query-builder state.
 */
import { RuleGroupType } from "react-querybuilder";

export const EMPTY_ROUTING_RULE_GROUP: RuleGroupType = {
	combinator: "and",
	rules: [],
};

export function isValidRuleGroupType(q: unknown): q is RuleGroupType {
	if (!q || typeof q !== "object") {
		return false;
	}
	const candidate = q as RuleGroupType;
	return (candidate.combinator === "and" || candidate.combinator === "or") && Array.isArray(candidate.rules);
}

export function normalizeRoutingRuleGroupQuery(q: unknown): RuleGroupType {
	return isValidRuleGroupType(q) ? q : EMPTY_ROUTING_RULE_GROUP;
}