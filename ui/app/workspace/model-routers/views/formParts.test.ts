import type { TFunction } from "i18next";
import type { RuleGroupType } from "react-querybuilder";
import { describe, expect, test } from "vitest";
import { initialConditionMode, scopeNoun } from "./formParts";

const builderQuery: RuleGroupType = {
	combinator: "and",
	rules: [{ field: "model", operator: "=", value: "gpt-4o" }],
};
const emptyQuery: RuleGroupType = { combinator: "and", rules: [] };

describe("initialConditionMode", () => {
	test("a router with no routing yet opens in the builder", () => {
		expect(initialConditionMode(undefined)).toBe("builder");
		expect(initialConditionMode(null)).toBe("builder");
	});

	test("a router that takes every request opens in the builder", () => {
		expect(initialConditionMode({})).toBe("builder");
		expect(initialConditionMode({ cel_expression: "" })).toBe("builder");
		expect(initialConditionMode({ cel_expression: "", query: emptyQuery })).toBe("builder");
	});

	test("a condition made in the builder reopens in the builder", () => {
		expect(initialConditionMode({ cel_expression: "model == 'gpt-4o'", query: builderQuery })).toBe("builder");
	});

	test("a condition written as CEL, with no builder state, opens as CEL so it is not cleared", () => {
		// Saved through the API or config.json, or hand-edited in CEL mode, which clears the builder state.
		expect(initialConditionMode({ cel_expression: "headers['x-tier'] == 'gold'" })).toBe("cel");
	});

	test("a condition whose builder state holds no rules opens as CEL", () => {
		expect(initialConditionMode({ cel_expression: "headers['x-tier'] == 'gold'", query: emptyQuery })).toBe("cel");
	});

	test("a condition whose builder state is not a query opens as CEL", () => {
		const notAQuery = { foo: "bar" } as unknown as RuleGroupType;
		const noCombinator = { rules: [{ field: "model", operator: "=", value: "x" }] } as unknown as RuleGroupType;
		expect(initialConditionMode({ cel_expression: "true", query: notAQuery })).toBe("cel");
		expect(initialConditionMode({ cel_expression: "true", query: noCombinator })).toBe("cel");
	});

	test("an expression of only spaces is no condition", () => {
		expect(initialConditionMode({ cel_expression: "   " })).toBe("builder");
	});

	test("builder state is enough for the builder, even before its expression is there", () => {
		expect(initialConditionMode({ query: builderQuery })).toBe("builder");
		expect(initialConditionMode({ cel_expression: "  ", query: { ...builderQuery, combinator: "or" } })).toBe("builder");
	});
});

describe("scopeNoun", () => {
	// t answers with the key it was asked for.
	const t = ((key: string) => key) as unknown as TFunction<"models">;

	test("names the entity each scope points at", () => {
		expect(scopeNoun("team", t)).toBe("routing.team");
		expect(scopeNoun("customer", t)).toBe("routing.customer");
		expect(scopeNoun("user", t)).toBe("routing.user");
		expect(scopeNoun("virtual_key", t)).toBe("routing.scopeVirtualKey");
	});

	test("gives each scope its own name, so a missing-entity error says which one", () => {
		const nouns = ["team", "customer", "user", "virtual_key"].map((scope) => scopeNoun(scope, t));
		expect(new Set(nouns).size).toBe(4);
	});
});