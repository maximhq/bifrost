import { describe, expect, it } from "vitest";

import { patternError } from "./pricingOverridePattern";

describe("patternError", () => {
	it.each([
		["exact", "gpt-4o"],
		["wildcard", "gpt-4*"],
		["wildcard", "*-free"],
		["wildcard", "*sonnet*"],
		["wildcard", "*"],
	] as const)("accepts %s pattern %s", (matchType, pattern) => {
		expect(patternError(matchType, pattern)).toBeUndefined();
	});

	it.each([
		["exact", "gpt-*", "Exact pattern cannot contain *"],
		["wildcard", "gpt-4o", "Wildcard pattern must include * at the beginning, end, or both ends"],
		["wildcard", "gpt-*-mini", "Wildcard (*) is only supported at the beginning, end, or both ends"],
		["wildcard", "**-free", "Wildcard (*) is only supported at the beginning, end, or both ends"],
		["wildcard", "gpt-**", "Wildcard (*) is only supported at the beginning, end, or both ends"],
		["wildcard", "**", "Wildcard pattern must include at least one non-wildcard character"],
	] as const)("rejects %s pattern %s", (matchType, pattern, error) => {
		expect(patternError(matchType, pattern)).toBe(error);
	});
});