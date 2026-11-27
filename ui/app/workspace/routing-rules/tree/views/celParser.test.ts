import { describe, expect, it } from "vitest";
import { evalChainCondition, expandCEL, normalizeCond } from "./celParser";

describe("celParser - Regression Tests", () => {
	it("expands simple AND expression", () => {
		expect(expandCEL('a == "1" && b == "2"')).toEqual([['a == "1"', 'b == "2"']]);
	});

	it("expands simple OR expression", () => {
		expect(expandCEL('a == "1" || b == "2"')).toEqual([['a == "1"'], ['b == "2"']]);
	});

	it("respects OR/AND precedence in mixed expressions", () => {
		expect(expandCEL('a == "1" || b == "2" && c == "3"')).toEqual([['a == "1"'], ['b == "2"', 'c == "3"']]);
	});

	it("expands parenthesized OR inside AND (Cartesian product)", () => {
		expect(expandCEL('a == "1" && (b == "2" || c == "3")')).toEqual([
			['a == "1"', 'b == "2"'],
			['a == "1"', 'c == "3"'],
		]);
	});

	it("collapses whitespace around operators in normalizeCond", () => {
		expect(normalizeCond("a==b")).toBe("a == b");
		expect(normalizeCond("a  !=  b")).toBe("a != b");
		expect(normalizeCond("a>=b")).toBe("a >= b");
		expect(normalizeCond("a<=b")).toBe("a <= b");
		expect(normalizeCond("a>b")).toBe("a > b");
		expect(normalizeCond("a<b")).toBe("a < b");
	});

	it("handles empty or whitespace-only expressions", () => {
		expect(expandCEL("")).toEqual([[]]);
		expect(expandCEL("   ")).toEqual([[]]);
	});

	describe("evalChainCondition", () => {
		it("evaluates equality and inequality", () => {
			expect(evalChainCondition('provider == "openai"', { provider: "openai" })).toBe(true);
			expect(evalChainCondition('provider == "openai"', { provider: "azure" })).toBe(false);
			expect(evalChainCondition('provider != "openai"', { provider: "azure" })).toBe(true);
			expect(evalChainCondition('provider != "openai"', { provider: "openai" })).toBe(false);
		});

		it("evaluates startsWith and contains", () => {
			expect(evalChainCondition('model.startsWith("gpt-")', { model: "gpt-4" })).toBe(true);
			expect(evalChainCondition('model.startsWith("gpt-")', { model: "claude-3" })).toBe(false);
			expect(evalChainCondition('model.contains("sonnet")', { model: "claude-3-sonnet" })).toBe(true);
			expect(evalChainCondition('model.contains("sonnet")', { model: "gpt-4" })).toBe(false);
		});

		it("evaluates in-list condition", () => {
			expect(evalChainCondition('model in ["gpt-4", "gpt-3.5-turbo"]', { model: "gpt-4" })).toBe(true);
			expect(evalChainCondition('model in ["gpt-4", "gpt-3.5-turbo"]', { model: "claude-3" })).toBe(false);
		});

		it("evaluates header conditions", () => {
			expect(evalChainCondition('headers["x-env"] == "prod"', { "headers.x-env": "prod" })).toBe(true);
			expect(evalChainCondition('headers["x-env"] == "prod"', { "headers.x-env": "dev" })).toBe(false);
		});

		it("evaluates numeric comparisons", () => {
			expect(evalChainCondition("tokens_used >= 80", { tokens_used: "85" })).toBe(true);
			expect(evalChainCondition("tokens_used >= 80", { tokens_used: "70" })).toBe(false);
			expect(evalChainCondition("budget_used < 50", { budget_used: "40" })).toBe(true);
		});

		it("returns null for expressions that are too complex", () => {
			expect(evalChainCondition("customFunc(model)", { model: "gpt-4" })).toBeNull();
		});
	});
});

describe("celParser - Bug Fix Cases (Quoted Literals & Escape Handling)", () => {
	it("does not split on || or && inside double-quoted strings", () => {
		expect(expandCEL('model == "x" && headers["h"] == "a||b"')).toEqual([['model == "x"', 'headers["h"] == "a||b"']]);
	});

	it("does not split on || or && inside single-quoted strings", () => {
		expect(expandCEL("model == 'a||b'")).toEqual([["model == 'a||b'"]]);
		expect(expandCEL("model == 'a&&b' && provider == 'openai'")).toEqual([["model == 'a&&b'", "provider == 'openai'"]]);
	});

	it("handles escaped quotes inside string literals without splitting early", () => {
		expect(expandCEL('model == "he said \\"a||b\\""')).toEqual([['model == "he said \\"a||b\\""']]);
	});

	it("does not mutate operators inside quoted string literals during normalization", () => {
		expect(normalizeCond('model == "a>b"')).toBe('model == "a>b"');
		expect(normalizeCond('headers["x"] == "a==b"')).toBe('headers["x"] == "a==b"');
		expect(normalizeCond("model == 'a!=b'")).toBe("model == 'a!=b'");
		expect(normalizeCond('model=="a >= b"')).toBe('model == "a >= b"');
	});

	it("does not let unbalanced brackets or parentheses inside string literals corrupt depth tracking", () => {
		expect(expandCEL('model.matches("[(]") && provider == "openai"')).toEqual([['model.matches("[(]")', 'provider == "openai"']]);
		expect(expandCEL('model.matches("^[0-9]+$") && provider == "openai"')).toEqual([['model.matches("^[0-9]+$")', 'provider == "openai"']]);
	});

	it("handles regex with unbalanced parentheses in string literals", () => {
		expect(expandCEL('model.matches("a)b") && provider == "openai"')).toEqual([['model.matches("a)b")', 'provider == "openai"']]);
	});

	describe("boundary and malformed inputs", () => {
		it("handles empty string literals safely", () => {
			expect(expandCEL('model == "" && provider == "openai"')).toEqual([['model == ""', 'provider == "openai"']]);
			expect(normalizeCond('model == ""')).toBe('model == ""');
		});

		it("handles unterminated string literals without infinite loops or errors", () => {
			expect(expandCEL('model == "unclosed')).toEqual([['model == "unclosed']]);
			expect(normalizeCond('model == "unclosed')).toBe('model == "unclosed');
		});

		it("handles trailing backslashes safely", () => {
			expect(expandCEL('model == "test\\')).toEqual([['model == "test\\']]);
			expect(normalizeCond('model == "test\\')).toBe('model == "test\\');
		});

		it("handles mixed single and double quotes inside one another", () => {
			expect(expandCEL(`model == "he said 'hello'" && provider == 'openai'`)).toEqual([
				[`model == "he said 'hello'"`, "provider == 'openai'"],
			]);
		});

		it("handles triple-quoted strings with nested quotes and operators", () => {
			expect(expandCEL('model == """He said "x || y" today""" && provider == "openai"')).toEqual([
				['model == """He said "x || y" today"""', 'provider == "openai"'],
			]);
			expect(normalizeCond('model == """He said "a > b" today"""')).toBe('model == """He said "a > b" today"""');
			expect(expandCEL("model == '''He said 'x && y' today'''")).toEqual([["model == '''He said 'x && y' today'''"]]);
		});
	});
});