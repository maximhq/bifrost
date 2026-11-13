import { type AnalyzerFormValues, analyzerConfigSchema, DEFAULT_FORM_VALUES } from "@/app/workspace/complexity-router/formSchema";
import { zodResolver } from "@hookform/resolvers/zod";
import type { FieldErrors } from "react-hook-form";
import { describe, expect, test } from "vitest";
import { countFieldErrors } from "./useRouterConfigForm";

const keywords = { simple_keywords: ["hi"], medium_keywords: ["summarize this"], complex_keywords: ["prove this"] };

// A draft that saves: phrases in every tier and no classifier half set up.
const valid: AnalyzerFormValues = { ...DEFAULT_FORM_VALUES, keywords };

// errorsFor is the error tree a submit of these values hands the form, as the
// form's own resolver builds it.
async function errorsFor(values: AnalyzerFormValues): Promise<FieldErrors<AnalyzerFormValues>> {
	const result = await zodResolver(analyzerConfigSchema)(values, undefined, { fields: {}, shouldUseNativeValidation: false });
	return result.errors;
}

describe("countFieldErrors", () => {
	test("counts nothing when there is nothing to fix", () => {
		expect(countFieldErrors(undefined)).toBe(0);
		expect(countFieldErrors(null)).toBe(0);
		expect(countFieldErrors({})).toBe(0);
		expect(countFieldErrors("required")).toBe(0);
	});

	test("counts a field once, however deep it sits", () => {
		expect(countFieldErrors({ name: { type: "too_small", message: "Name is required" } })).toBe(1);
		expect(
			countFieldErrors({ suggestions: { tier_criteria: { SIMPLE: { definition: { type: "custom", message: "Enter a definition" } } } } }),
		).toBe(1);
	});

	test("counts each invalid field across sections", () => {
		const errors = {
			semantic: { embedding_model: { type: "custom", message: "Select an embedding model" } },
			llm: {
				provider: { type: "custom", message: "Select a provider" },
				model: { type: "custom", message: "Select a model" },
			},
			session: { enabled: { type: "custom", message: "Needs a classifier" } },
		};
		expect(countFieldErrors(errors)).toBe(4);
	});

	test("does not count the input a field's error points at", () => {
		const errors = {
			llm: {
				model: { type: "custom", message: "Select a model", ref: { name: "llm.model", message: "not an error" } },
			},
		};
		expect(countFieldErrors(errors)).toBe(1);
	});

	test("counts the invalid rows of a list, skipping the rows that are fine", () => {
		const errors = {
			decision: {
				criteria: {
					SIMPLE: { signals: [undefined, { type: "too_big", message: "Too long" }, null, { type: "too_big", message: "Too long" }] },
				},
			},
		};
		expect(countFieldErrors(errors)).toBe(2);
	});

	test("does not count a section that holds no message", () => {
		expect(countFieldErrors({ semantic: {}, llm: { provider: undefined }, decision: { criteria: { SIMPLE: {} } } })).toBe(0);
	});
});

describe("how many fields a router's save reports", () => {
	test("none for a draft that saves", async () => {
		expect(countFieldErrors(await errorsFor(valid))).toBe(0);
	});

	test("one per tier left without phrases", async () => {
		expect(countFieldErrors(await errorsFor(DEFAULT_FORM_VALUES))).toBe(3);
		expect(countFieldErrors(await errorsFor({ ...valid, keywords: { ...keywords, medium_keywords: [] } }))).toBe(1);
	});

	test("two for the llm classifier chosen without its provider and model, one once the provider is picked", async () => {
		const llm: AnalyzerFormValues = { ...valid, classifier: "llm" };
		expect(countFieldErrors(await errorsFor(llm))).toBe(2);
		expect(countFieldErrors(await errorsFor({ ...llm, llm: { ...llm.llm, provider: "openai" } }))).toBe(1);
		expect(countFieldErrors(await errorsFor({ ...llm, llm: { ...llm.llm, provider: "openai", model: "gpt-4o-mini" } }))).toBe(0);
	});

	test("one for an embedding provider picked without its model", async () => {
		const half: AnalyzerFormValues = { ...valid, semantic: { ...valid.semantic, provider: "openai" } };
		expect(countFieldErrors(await errorsFor(half))).toBe(1);
	});

	test("two for the decision model chosen with its provider and model cleared", async () => {
		const decision: AnalyzerFormValues = { ...valid, classifier: "decision", decision: { ...valid.decision, provider: "", model: " " } };
		expect(countFieldErrors(await errorsFor(decision))).toBe(2);
	});

	test("adds up errors on the page and inside its sheets", async () => {
		const broken: AnalyzerFormValues = {
			...valid,
			classifier: "llm",
			keywords: { ...keywords, complex_keywords: [] },
			llm: { ...valid.llm, timeout: "0ms" },
		};
		// The empty tier, the llm provider, the llm model and the llm timeout.
		expect(countFieldErrors(await errorsFor(broken))).toBe(4);
	});

	test("does not count the settings of a classifier that is not in use", async () => {
		// Decision fields are hidden while semantic is the classifier with no decision fallback.
		const hidden: AnalyzerFormValues = { ...valid, decision: { ...valid.decision, provider: "", model: "", timeout: "" } };
		expect(countFieldErrors(await errorsFor(hidden))).toBe(0);
	});
});