import { describe, expect, test } from "vitest";
import {
	analyzerConfigSchema,
	countCanonicalSemanticPhrases,
	DEFAULT_FORM_VALUES,
	getTypesafeState,
	isRouterConfigured,
	jevTimeoutFieldValue,
	shouldSeedLLMPrompt,
	toAnalyzerPayload,
	toFormValues,
} from "./formSchema";
import type { AnalyzerConfig } from "@/lib/types/complexityRouter";
import type { ModelProvider } from "@/lib/types/config";
import type { DBKey } from "@/lib/types/governance";

describe("fallback prompt initialization", () => {
	test("initializes an untouched empty prompt", () => {
		expect(shouldSeedLLMPrompt(true, "default", "", false)).toBe(true);
	});
	test("does not refill an intentionally cleared prompt, including a late default response", () => {
		expect(shouldSeedLLMPrompt(true, "default", "", true)).toBe(false);
	});
	test("does not replace custom text or initialize a disabled fallback", () => {
		expect(shouldSeedLLMPrompt(true, "default", "custom", false)).toBe(false);
		expect(shouldSeedLLMPrompt(false, "default", "", false)).toBe(false);
		expect(shouldSeedLLMPrompt(true, "", "", false)).toBe(false);
	});
});

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

describe("Jev complexity configuration", () => {
	test("defaults to one prior user message and a 1500ms timeout", () => {
		expect(DEFAULT_FORM_VALUES.jev).toEqual({ previous_message_count: 1, timeout: "1500ms" });
	});

	test("restores the saved classifier and defaults an empty legacy value", () => {
		const saved: AnalyzerConfig = {
			keywords: { simple_keywords: ["simple"], medium_keywords: ["medium"], complex_keywords: ["complex"] },
			classifier: "jev",
		};
		expect(toFormValues(saved).classifier).toBe("jev");
		expect(toFormValues({ ...saved, classifier: "" as never }).classifier).toBe("semantic");
	});

	test("builds a valid Jev payload with the configured timeout", () => {
		const values = {
			...DEFAULT_FORM_VALUES,
			classifier: "jev" as const,
			keywords: { simple_keywords: ["simple"], medium_keywords: ["medium"], complex_keywords: ["complex"] },
			jev: { previous_message_count: 1, timeout: "400ms" },
		};
		const parsed = analyzerConfigSchema.safeParse(values);
		expect(parsed.success).toBe(true);
		if (!parsed.success) return;

		const payload = toAnalyzerPayload(parsed.data);
		expect(payload.classifier).toBe("jev");
		expect(payload.jev).toEqual({ previous_message_count: 1, timeout: "400ms" });
		expect(payload.semantic).toBeUndefined();
	});

	test("hidden Jev fields do not block a semantic save without a Jev fallback", () => {
		const values = {
			...DEFAULT_FORM_VALUES,
			keywords: { simple_keywords: ["simple"], medium_keywords: ["medium"], complex_keywords: ["complex"] },
			jev: { previous_message_count: Number.NaN, timeout: "" },
		};
		const parsed = analyzerConfigSchema.safeParse(values);
		expect(parsed.success).toBe(true);
		if (!parsed.success) return;

		const saved: AnalyzerConfig = { ...parsed.data, jev: { previous_message_count: 2, timeout: "900ms" } };
		expect(toAnalyzerPayload(parsed.data, saved).jev).toEqual(saved.jev);
		expect(toAnalyzerPayload(parsed.data).jev).toBeUndefined();
	});

	test("rejects invalid Jev fields when Jev is the semantic fallback", () => {
		const values = {
			...DEFAULT_FORM_VALUES,
			keywords: { simple_keywords: ["simple"], medium_keywords: ["medium"], complex_keywords: ["complex"] },
			semantic: { ...DEFAULT_FORM_VALUES.semantic, fallback: "jev" as const },
			jev: { previous_message_count: 9, timeout: "" },
		};
		const parsed = analyzerConfigSchema.safeParse(values);
		expect(parsed.success).toBe(false);
		if (parsed.success) return;
		expect(parsed.error.issues.map((issue) => issue.path.join("."))).toEqual(
			expect.arrayContaining(["jev.previous_message_count", "jev.timeout"]),
		);
	});
});

describe("Jev complexity form state", () => {
	const keywords = { simple_keywords: ["simple"], medium_keywords: ["medium"], complex_keywords: ["complex"] };

	test("fills a partial saved Jev block with defaults", () => {
		// Older gateways may omit the count; the cast models that wire shape.
		const partial = { timeout: "900ms" } as AnalyzerConfig["jev"];
		expect(toFormValues({ keywords, classifier: "jev", jev: partial }).jev).toEqual({ previous_message_count: 1, timeout: "900ms" });
		expect(toFormValues({ keywords, classifier: "jev", jev: { previous_message_count: 0 } }).jev).toEqual({
			previous_message_count: 0,
			timeout: "1500ms",
		});
	});

	test("sends the Jev block when Jev is the semantic fallback", () => {
		const values = {
			...DEFAULT_FORM_VALUES,
			keywords,
			semantic: { ...DEFAULT_FORM_VALUES.semantic, provider: "openai", embedding_model: "text-embedding-3-small", fallback: "jev" as const },
			jev: { previous_message_count: 3, timeout: "700ms" },
		};
		const parsed = analyzerConfigSchema.safeParse(values);
		expect(parsed.success).toBe(true);
		if (!parsed.success) return;
		const payload = toAnalyzerPayload(parsed.data);
		expect(payload.classifier).toBe("semantic");
		expect(payload.jev).toEqual({ previous_message_count: 3, timeout: "700ms" });
	});

	test("allows session routing with Jev as the classifier and no semantic setup", () => {
		const values = { ...DEFAULT_FORM_VALUES, classifier: "jev" as const, keywords, session: { enabled: true } };
		expect(analyzerConfigSchema.safeParse(values).success).toBe(true);
		expect(analyzerConfigSchema.safeParse({ ...values, classifier: "semantic" as const }).success).toBe(false);
	});

	test("rejects out-of-range Jev history and non-positive timeouts when Jev is primary", () => {
		const base = { ...DEFAULT_FORM_VALUES, classifier: "jev" as const, keywords };
		for (const jev of [
			{ previous_message_count: 6, timeout: "1500ms" },
			{ previous_message_count: -1, timeout: "1500ms" },
			{ previous_message_count: 1.5, timeout: "1500ms" },
			{ previous_message_count: Number.NaN, timeout: "1500ms" },
			{ previous_message_count: 1, timeout: "0ms" },
			{ previous_message_count: 1, timeout: "" },
		]) {
			expect(analyzerConfigSchema.safeParse({ ...base, jev }).success).toBe(false);
		}
	});

	test("shows the saved Jev timeout as editable milliseconds", () => {
		expect(jevTimeoutFieldValue("400ms")).toBe("400");
		expect(jevTimeoutFieldValue("1.5s")).toBe(1500);
		expect(jevTimeoutFieldValue(undefined)).toBe(1500);
		expect(jevTimeoutFieldValue("")).toBe("");
	});
});

describe("router configuration state", () => {
	test("treats Jev as configured without a semantic block", () => {
		expect(isRouterConfigured({ keywords: { simple_keywords: [], medium_keywords: [], complex_keywords: [] }, classifier: "jev" })).toBe(true);
	});

	test("does not treat phrases alone as configured", () => {
		expect(isRouterConfigured({ keywords: { simple_keywords: ["a"], medium_keywords: ["b"], complex_keywords: ["c"] } })).toBe(false);
		expect(isRouterConfigured(undefined)).toBe(false);
	});
});

describe("Typesafe provider state for Jev", () => {
	const provider = (overrides: Partial<ModelProvider> = {}) => ({ name: "typesafe", provider_status: "active", ...overrides }) as ModelProvider;
	const key = (overrides: Partial<DBKey> = {}) => ({ provider: "typesafe", ...overrides }) as DBKey;

	test("reports a missing provider", () => {
		expect(getTypesafeState([], [key()])).toBe("missing");
		expect(getTypesafeState(undefined, undefined)).toBe("missing");
	});

	test("reports a provider that failed to initialise or list models", () => {
		expect(getTypesafeState([provider({ provider_status: "error" } as Partial<ModelProvider>)], [key()])).toBe("failing");
		expect(getTypesafeState([provider({ status: "list_models_failed" } as Partial<ModelProvider>)], [key()])).toBe("failing");
	});

	test("requires an enabled Typesafe key, treating an omitted flag as enabled", () => {
		expect(getTypesafeState([provider()], [key({ enabled: false } as Partial<DBKey>)])).toBe("no-enabled-key");
		expect(getTypesafeState([provider()], [key({ provider: "openai" } as Partial<DBKey>)])).toBe("no-enabled-key");
		expect(getTypesafeState([provider()], [key()])).toBe("configured");
		expect(getTypesafeState([provider()], [key({ enabled: false } as Partial<DBKey>), key({ enabled: true } as Partial<DBKey>)])).toBe("configured");
	});
});

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
