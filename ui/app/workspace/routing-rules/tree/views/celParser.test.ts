import { describe, expect, it } from "vitest";
import { evalChainCondition, expandCEL, normalizeCond } from "./celParser";

describe("expandCEL", () => {
	it("keeps a literal containing || as one condition", () => {
		expect(expandCEL('model == "x" && headers["h"] == "a||b"')).toEqual([['model == "x"', 'headers["h"] == "a||b"']]);
	});

	it("keeps a literal containing && as one condition", () => {
		expect(expandCEL('headers["x-env"] == "staging&&canary"')).toEqual([['headers["x-env"] == "staging&&canary"']]);
	});

	it("splits on && when a literal holds unbalanced brackets", () => {
		expect(expandCEL('model.matches("[(]") && provider == "openai"')).toEqual([['model.matches("[(]")', 'provider == "openai"']]);
	});

	it("reads a single-quoted literal the same way", () => {
		expect(expandCEL("model == 'a||b' && provider == 'openai'")).toEqual([["model == 'a||b'", "provider == 'openai'"]]);
	});

	it("does not end a literal on an escaped quote", () => {
		expect(expandCEL('model == "a\\"||b" && provider == "openai"')).toEqual([['model == "a\\"||b"', 'provider == "openai"']]);
	});

	it("still fans out on a real ||", () => {
		expect(expandCEL('model == "a" || model == "b"')).toEqual([['model == "a"'], ['model == "b"']]);
	});

	it("still expands a parenthesised or", () => {
		expect(expandCEL('a == "1" && (b == "2" || c == "3")')).toEqual([
			['a == "1"', 'b == "2"'],
			['a == "1"', 'c == "3"'],
		]);
	});
});

describe("normalizeCond", () => {
	it("leaves comparison characters inside a literal alone", () => {
		expect(normalizeCond('model == "a>b"')).toBe('model == "a>b"');
	});

	it("preserves whitespace inside a literal", () => {
		expect(normalizeCond('model == "a  b"')).toBe('model == "a  b"');
	});

	it("still pads operators outside literals", () => {
		expect(normalizeCond('model=="x"')).toBe('model == "x"');
		expect(normalizeCond("tokens>=100")).toBe("tokens >= 100");
		expect(normalizeCond("tokens>100")).toBe("tokens > 100");
	});

	it("still collapses whitespace outside literals", () => {
		expect(normalizeCond('model   ==   "x"')).toBe('model == "x"');
	});
});

describe("evalChainCondition", () => {
	it("matches a value whose literal contains a comparison character", () => {
		expect(evalChainCondition(normalizeCond('model == "a>b"'), { model: "a>b" })).toBe(true);
	});
});