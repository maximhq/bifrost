import type { ModelRouterRouting } from "@/lib/types/complexityRouter";
import type { RoutingRule } from "@/lib/types/routingRules";
import { createElement, type ReactNode } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { ComplexityRulesNotice } from "./routingTab";
import { complexityRulesFor, MAX_NAMED_COMPLEXITY_RULES, namedComplexityRules, usesComplexityTier } from "./routingTab.utils";

// The notice only links out: the router is not under test.
vi.mock("@tanstack/react-router", async (importOriginal) => ({
	...(await importOriginal<typeof import("@tanstack/react-router")>()),
	Link: ({ to, children }: { to: string; children: ReactNode }) => createElement("a", { href: to }, children),
}));

const rule = (rest: Partial<RoutingRule> = {}) =>
	({
		id: "rule-1",
		name: "Simple to mini",
		cel_expression: 'complexity_tier == "SIMPLE"',
		scope: "global",
		enabled: true,
		...rest,
	}) as RoutingRule;

const routing = (rest: Partial<ModelRouterRouting> = {}): ModelRouterRouting => ({
	enabled: true,
	scope: "global",
	priority: 0,
	...rest,
});

describe("usesComplexityTier", () => {
	it("is true when the expression reads the variable", () => {
		expect(usesComplexityTier('complexity_tier == "SIMPLE"')).toBe(true);
		expect(usesComplexityTier('model == "gpt-4o" && (complexity_tier in ["MEDIUM", "COMPLEX"])')).toBe(true);
		expect(usesComplexityTier("!(complexity_tier=='SIMPLE')")).toBe(true);
	});

	it("is false when the name is only text, a field or part of another name", () => {
		expect(usesComplexityTier(undefined)).toBe(false);
		expect(usesComplexityTier("")).toBe(false);
		expect(usesComplexityTier('model == "complexity_tier"')).toBe(false);
		expect(usesComplexityTier("headers['complexity_tier'] == 'x'")).toBe(false);
		expect(usesComplexityTier('request.complexity_tier == "SIMPLE"')).toBe(false);
		expect(usesComplexityTier('my_complexity_tier == "SIMPLE"')).toBe(false);
		expect(usesComplexityTier('complexity_tier_v2 == "SIMPLE"')).toBe(false);
		expect(usesComplexityTier('model == "a \\" complexity_tier"')).toBe(false);
	});
});

describe("complexityRulesFor", () => {
	it("keeps the enabled rules that read complexity_tier", () => {
		const rules = [
			rule({ id: "on" }),
			rule({ id: "off", enabled: false }),
			rule({ id: "plain", cel_expression: 'model == "gpt-4o"' }),
			rule({ id: "named", name: "complexity_tier", cel_expression: "" }),
		];
		expect(complexityRulesFor(rules, routing()).map((r) => r.id)).toEqual(["on"]);
	});

	it("is empty with no rules loaded", () => {
		expect(complexityRulesFor(undefined, routing())).toEqual([]);
	});

	it("leaves out a rule scoped to another entity of the router's scope kind", () => {
		const rules = [
			rule({ id: "same-key", scope: "virtual_key", scope_id: "vk-1" }),
			rule({ id: "other-key", scope: "virtual_key", scope_id: "vk-2" }),
			rule({ id: "team", scope: "team", scope_id: "team-1" }),
			rule({ id: "global" }),
		];
		const kept = complexityRulesFor(rules, routing({ scope: "virtual_key", scope_id: "vk-1" }));
		expect(kept.map((r) => r.id)).toEqual(["same-key", "team", "global"]);
	});

	it("keeps every scoped rule for a global router", () => {
		const rules = [rule({ id: "key", scope: "virtual_key", scope_id: "vk-2" }), rule({ id: "global" })];
		expect(complexityRulesFor(rules, routing()).map((r) => r.id)).toEqual(["key", "global"]);
	});
});

describe("namedComplexityRules", () => {
	const namesOf = (rules: RoutingRule[]) => {
		const { named, more } = namedComplexityRules(rules);
		return { names: named.map((r) => r.name), more };
	};

	it("names every rule up to the limit", () => {
		expect(namesOf([rule({ name: "a" }), rule({ id: "2", name: "b" })])).toEqual({ names: ["a", "b"], more: 0 });
	});

	it("counts the rules past the limit", () => {
		const rules = Array.from({ length: MAX_NAMED_COMPLEXITY_RULES + 2 }, (_, i) => rule({ id: `r${i}`, name: `rule ${i}` }));
		expect(namesOf(rules)).toEqual({ names: ["rule 0", "rule 1", "rule 2"], more: 2 });
	});
});

describe("ComplexityRulesNotice", () => {
	const render = (rules: RoutingRule[]) => renderToStaticMarkup(createElement(ComplexityRulesNotice, { rules }));
	const namesIn = (html: string) => /data-testid="model-router-routing-complexity-rules-names">([^<]*)</.exec(html)?.[1];

	it("names the one rule inside its sentence", () => {
		const html = render([rule({ name: "Simple to mini" })]);
		expect(namesIn(html)).toBe("Simple to mini");
		expect(html).toContain("Routing rules with complexity tier detected");
		expect(html).toContain("The routing rule ");
		expect(html).toContain(">complexity_tier</code> and is enabled.");
		expect(html).toContain('href="/workspace/routing-rules"');
		expect(html).toContain('href="https://docs.getbifrost.ai/features/complexity-router"');
	});

	it("lists several rules and counts the ones it does not name", () => {
		const few = render([rule({ name: "a" }), rule({ id: "2", name: "b" })]);
		expect(namesIn(few)).toBe("a and b");
		expect(few).toContain("The following routing rules ");

		const many = render(Array.from({ length: 5 }, (_, i) => rule({ id: `r${i}`, name: `rule ${i}` })));
		expect(namesIn(many)).toBe("rule 0, rule 1, rule 2, and 2 more");
	});

	it("shows a rule name as text, never as markup", () => {
		const html = render([rule({ name: "<code>x</code>" })]);
		expect(namesIn(html)).toBe("&lt;code&gt;x&lt;/code&gt;");
	});
});