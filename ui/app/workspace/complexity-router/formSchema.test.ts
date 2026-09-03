import { describe, expect, test } from "vitest";
import { analyzerConfigSchema, countCanonicalSemanticPhrases, DEFAULT_FORM_VALUES } from "./formSchema";

function phraseList(prefix: string, count: number): string[] {
	return Array.from({ length: count }, (_, index) => `${prefix}-${index}`);
}

function formValues(simpleCount: number, semantic: boolean) {
	return {
		...DEFAULT_FORM_VALUES,
		keywords: {
			simple_keywords: phraseList("simple", simpleCount),
			medium_keywords: ["medium"],
			complex_keywords: ["complex"],
		},
		semantic: semantic
			? { ...DEFAULT_FORM_VALUES.semantic, provider: "openai", embedding_model: "text-embedding-3-small" }
			: { ...DEFAULT_FORM_VALUES.semantic },
	};
}

describe("semantic complexity phrase limit", () => {
	test("accepts exactly 750 canonical phrases", () => {
		expect(analyzerConfigSchema.safeParse(formValues(748, true)).success).toBe(true);
	});

	test("rejects 751 canonical phrases with tier counts", () => {
		const result = analyzerConfigSchema.safeParse(formValues(749, true));
		expect(result.success).toBe(false);
		if (result.success) return;
		expect(result.error.issues.some((issue) => issue.message.includes("751 phrases (Simple=749, Medium=1, Complex=1)"))).toBe(true);
	});

	test("does not count blanks and same-tier duplicates twice", () => {
		const values = formValues(748, true);
		values.keywords.simple_keywords.push(" SIMPLE-0 ", "");
		expect(countCanonicalSemanticPhrases(values.keywords).total).toBe(750);
		expect(analyzerConfigSchema.safeParse(values).success).toBe(true);
	});

	test("does not cap a form that will omit the semantic block", () => {
		expect(analyzerConfigSchema.safeParse(formValues(750, false)).success).toBe(true);
	});
});
