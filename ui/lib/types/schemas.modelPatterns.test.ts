import { describe, expect, it } from "vitest";

import { modelPatternSchema, modelProviderKeySchema } from "./schemas";

describe("modelPatternSchema", () => {
	// validateModelRegex trims its own copy before compiling, so a padded pattern
	// passes validation. The backend embeds the stored pattern in an anchored
	// expression ("(?i)^(?:<pattern>)$"), where the padding it kept stops it
	// matching anything: a padded block pattern silently blocks nothing.
	it("strips surrounding whitespace from a valid pattern", () => {
		const result = modelPatternSchema.safeParse("  gpt-.*  ");
		expect(result.success).toBe(true);
		expect(result.data).toBe("gpt-.*");
	});

	it("leaves a pattern with no surrounding whitespace alone", () => {
		expect(modelPatternSchema.safeParse("^o1-.*$").data).toBe("^o1-.*$");
	});

	it("rejects a whitespace-only pattern", () => {
		expect(modelPatternSchema.safeParse("   ").success).toBe(false);
	});

	it("rejects the wildcard, which belongs in the exact model list", () => {
		expect(modelPatternSchema.safeParse("*").success).toBe(false);
	});

	it("rejects a pattern RE2 cannot compile", () => {
		expect(modelPatternSchema.safeParse("gpt-(").success).toBe(false);
	});

	// Both the provider-key form and the virtual-key form submit schema output
	// straight to the API, so the trim has to survive the surrounding object.
	it("submits trimmed patterns from a provider key", () => {
		const result = modelProviderKeySchema.safeParse({
			id: "key-1",
			name: "key",
			value: { value: "sk-test" },
			weight: 1,
			models_patterns: [" gpt-.* "],
			blacklisted_models_patterns: ["\tclaude-.*\n"],
		});
		expect(result.success).toBe(true);
		expect(result.data?.models_patterns).toEqual(["gpt-.*"]);
		expect(result.data?.blacklisted_models_patterns).toEqual(["claude-.*"]);
	});
});