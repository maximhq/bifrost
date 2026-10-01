import { describe, expect, test } from "vitest";
import {
	analyzerConfigSchema,
	countCanonicalSemanticPhrases,
	DEFAULT_FORM_VALUES,
	getTypesafeState,
	isRouterConfigured,
	jevGuidanceFormValues,
	jevTimeoutFieldValue,
	shouldSeedLLMPrompt,
	toAnalyzerPayload,
	toFormValues,
} from "./formSchema";
import type { AnalyzerConfig, JevGuidanceDefaults } from "@/lib/types/complexityRouter";
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
			? {
					...DEFAULT_FORM_VALUES.semantic,
					provider: "openai",
					embedding_model: "text-embedding-3-small",
				}
			: { ...DEFAULT_FORM_VALUES.semantic },
	};
}

// Synthetic shipped guidance; the real defaults come from the status endpoint.
const JEV_DEFAULTS: JevGuidanceDefaults = {
	criteria: {
		SIMPLE: {
			definition: "simple definition",
			signals: ["s-signal"],
			examples: ["s-example"],
		},
		MEDIUM: {
			definition: "medium definition",
			signals: ["m-signal"],
			examples: ["m-example"],
		},
		COMPLEX: {
			definition: "complex definition",
			signals: ["c-signal"],
			examples: ["c-example"],
		},
	},
};

const EMPTY_JEV_GUIDANCE = jevGuidanceFormValues();

describe("Jev complexity configuration", () => {
	test("defaults to one prior user message and a 1500ms timeout", () => {
		expect(DEFAULT_FORM_VALUES.jev).toEqual({
			previous_message_count: 1,
			timeout: "1500ms",
			...EMPTY_JEV_GUIDANCE,
		});
	});

	test("restores the saved classifier and defaults an empty legacy value", () => {
		const saved: AnalyzerConfig = {
			keywords: {
				simple_keywords: ["simple"],
				medium_keywords: ["medium"],
				complex_keywords: ["complex"],
			},
			classifier: "jev",
		};
		expect(toFormValues(saved).classifier).toBe("jev");
		expect(toFormValues({ ...saved, classifier: "" as never }).classifier).toBe("semantic");
	});

	test("builds a valid Jev payload with the configured timeout", () => {
		const values = {
			...DEFAULT_FORM_VALUES,
			classifier: "jev" as const,
			keywords: {
				simple_keywords: ["simple"],
				medium_keywords: ["medium"],
				complex_keywords: ["complex"],
			},
			jev: {
				previous_message_count: 1,
				timeout: "400ms",
				...EMPTY_JEV_GUIDANCE,
			},
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
			keywords: {
				simple_keywords: ["simple"],
				medium_keywords: ["medium"],
				complex_keywords: ["complex"],
			},
			jev: {
				previous_message_count: Number.NaN,
				timeout: "",
				...EMPTY_JEV_GUIDANCE,
			},
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
			keywords: {
				simple_keywords: ["simple"],
				medium_keywords: ["medium"],
				complex_keywords: ["complex"],
			},
			semantic: { ...DEFAULT_FORM_VALUES.semantic, fallback: "jev" as const },
			jev: { previous_message_count: 9, timeout: "", ...EMPTY_JEV_GUIDANCE },
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
		expect(toFormValues({ keywords, classifier: "jev", jev: partial }).jev).toEqual({
			previous_message_count: 1,
			timeout: "900ms",
			...EMPTY_JEV_GUIDANCE,
		});
		expect(
			toFormValues({
				keywords,
				classifier: "jev",
				jev: { previous_message_count: 0 },
			}).jev,
		).toEqual({
			previous_message_count: 0,
			timeout: "1500ms",
			...EMPTY_JEV_GUIDANCE,
		});
	});

	test("sends the Jev block when Jev is the semantic fallback", () => {
		const values = {
			...DEFAULT_FORM_VALUES,
			keywords,
			semantic: {
				...DEFAULT_FORM_VALUES.semantic,
				provider: "openai",
				embedding_model: "text-embedding-3-small",
				fallback: "jev" as const,
			},
			jev: {
				previous_message_count: 3,
				timeout: "700ms",
				...EMPTY_JEV_GUIDANCE,
			},
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
		expect(
			analyzerConfigSchema.safeParse({
				...values,
				classifier: "semantic" as const,
			}).success,
		).toBe(false);
	});

	test("rejects out-of-range Jev history and non-positive timeouts when Jev is primary", () => {
		const base = {
			...DEFAULT_FORM_VALUES,
			classifier: "jev" as const,
			keywords,
		};
		for (const jev of [
			{ previous_message_count: 6, timeout: "1500ms" },
			{ previous_message_count: -1, timeout: "1500ms" },
			{ previous_message_count: 1.5, timeout: "1500ms" },
			{ previous_message_count: Number.NaN, timeout: "1500ms" },
			{ previous_message_count: 1, timeout: "0ms" },
			{ previous_message_count: 1, timeout: "" },
		]) {
			expect(
				analyzerConfigSchema.safeParse({
					...base,
					jev: { ...jev, ...EMPTY_JEV_GUIDANCE },
				}).success,
			).toBe(false);
		}
	});
});

describe("Jev classification guidance", () => {
	const keywords = { simple_keywords: ["simple"], medium_keywords: ["medium"], complex_keywords: ["complex"] };
	const jevValues = (guidance: ReturnType<typeof jevGuidanceFormValues>) => ({
		...DEFAULT_FORM_VALUES,
		classifier: "jev" as const,
		keywords,
		jev: { previous_message_count: 1, timeout: "1500ms", ...guidance },
	});

	test("seeds unset guidance from the shipped defaults", () => {
		expect(jevGuidanceFormValues(undefined, JEV_DEFAULTS)).toEqual({
			criteria: {
				SIMPLE: {
					definition: "simple definition",
					signals: ["s-signal"],
					examples: ["s-example"],
				},
				MEDIUM: {
					definition: "medium definition",
					signals: ["m-signal"],
					examples: ["m-example"],
				},
				COMPLEX: {
					definition: "complex definition",
					signals: ["c-signal"],
					examples: ["c-example"],
				},
			},
		});
	});

	test("keeps saved overrides field by field and defaults the rest", () => {
		const seeded = jevGuidanceFormValues(
			{
				previous_message_count: 1,
				criteria: {
					MEDIUM: { signals: ["custom signal"] },
					SIMPLE: { definition: "custom definition" },
				},
			},
			JEV_DEFAULTS,
		);
		expect(seeded.criteria.MEDIUM).toEqual({
			definition: "medium definition",
			signals: ["custom signal"],
			examples: ["m-example"],
		});
		expect(seeded.criteria.SIMPLE).toEqual({
			definition: "custom definition",
			signals: ["s-signal"],
			examples: ["s-example"],
		});
	});

	test("sends seeded guidance for the gateway to reduce against its defaults", () => {
		const parsed = analyzerConfigSchema.safeParse(jevValues(jevGuidanceFormValues(undefined, JEV_DEFAULTS)));
		expect(parsed.success).toBe(true);
		if (!parsed.success) return;
		const payload = toAnalyzerPayload(parsed.data);
		expect(payload.jev?.criteria?.COMPLEX).toEqual({
			definition: "complex definition",
			signals: ["c-signal"],
			examples: ["c-example"],
		});
	});

	test("omits unseeded guidance so the gateway sends its defaults", () => {
		const parsed = analyzerConfigSchema.safeParse(jevValues(EMPTY_JEV_GUIDANCE));
		expect(parsed.success).toBe(true);
		if (!parsed.success) return;
		expect(toAnalyzerPayload(parsed.data).jev).toEqual({
			previous_message_count: 1,
			timeout: "1500ms",
		});
	});

	test("rejects an emptied definition or list once guidance is seeded", () => {
		const seeded = jevGuidanceFormValues(undefined, JEV_DEFAULTS);
		const emptiedList = analyzerConfigSchema.safeParse(
			jevValues({
				...seeded,
				criteria: {
					...seeded.criteria,
					SIMPLE: { ...seeded.criteria.SIMPLE, signals: [] },
				},
			}),
		);
		expect(emptiedList.success).toBe(false);
		if (emptiedList.success) return;
		expect(emptiedList.error.issues.map((issue) => issue.path.join("."))).toContain("jev.criteria.SIMPLE.signals");
		const emptiedDefinition = analyzerConfigSchema.safeParse(
			jevValues({
				...seeded,
				criteria: {
					...seeded.criteria,
					SIMPLE: { ...seeded.criteria.SIMPLE, definition: " " },
				},
			}),
		);
		expect(emptiedDefinition.success).toBe(false);
		if (emptiedDefinition.success) return;
		expect(emptiedDefinition.error.issues.map((issue) => issue.path.join("."))).toContain("jev.criteria.SIMPLE.definition");
	});

	test("rejects guidance past the gateway's size bounds", () => {
		const seeded = jevGuidanceFormValues(undefined, JEV_DEFAULTS);
		const tooMany = Array.from({ length: 13 }, (_, i) => `signal ${i}`);
		expect(
			analyzerConfigSchema.safeParse(
				jevValues({
					...seeded,
					criteria: {
						...seeded.criteria,
						MEDIUM: { ...seeded.criteria.MEDIUM, signals: tooMany },
					},
				}),
			).success,
		).toBe(false);
		expect(
			analyzerConfigSchema.safeParse(
				jevValues({
					...seeded,
					criteria: {
						...seeded.criteria,
						MEDIUM: { ...seeded.criteria.MEDIUM, signals: ["x".repeat(301)] },
					},
				}),
			).success,
		).toBe(false);
		expect(
			analyzerConfigSchema.safeParse(
				jevValues({
					...seeded,
					criteria: {
						...seeded.criteria,
						MEDIUM: { ...seeded.criteria.MEDIUM, definition: "x".repeat(501) },
					},
				}),
			).success,
		).toBe(false);
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
		expect(
			isRouterConfigured({
				keywords: {
					simple_keywords: [],
					medium_keywords: [],
					complex_keywords: [],
				},
				classifier: "jev",
			}),
		).toBe(true);
	});

	test("does not treat phrases alone as configured", () => {
		expect(
			isRouterConfigured({
				keywords: {
					simple_keywords: ["a"],
					medium_keywords: ["b"],
					complex_keywords: ["c"],
				},
			}),
		).toBe(false);
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
