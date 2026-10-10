import { describe, expect, test } from "vitest";
import {
	analyzerConfigSchema,
	countCanonicalSemanticPhrases,
	DEFAULT_FORM_VALUES,
	getDecisionProviderState,
	clefModelFromProvider,
	defaultDecisionModel,
	isDecisionProvider,
	isOpenAIDecisionProvider,
	selfHostedModelGroups,
	isRouterConfigured,
	decisionGuidanceFormValues,
	decisionTimeoutFieldValue,
	shouldSeedLLMPrompt,
	toAnalyzerPayload,
	toFormValues,
} from "./formSchema";
import { DEFAULT_DECISION_CONFIG, type AnalyzerConfig, type DecisionGuidanceDefaults } from "@/lib/types/complexityRouter";
import type { ModelProvider } from "@/lib/types/config";
import type { DBKey } from "@/lib/types/governance";

// The default decision model, as the form holds it and the gateway receives it.
const DEFAULT_MODEL = { provider: DEFAULT_DECISION_CONFIG.provider, model: DEFAULT_DECISION_CONFIG.model };

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
const DECISION_DEFAULTS: DecisionGuidanceDefaults = {
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

const EMPTY_DECISION_GUIDANCE = decisionGuidanceFormValues();

describe("Decision complexity configuration", () => {
	test("defaults to one prior user message and a 1500ms timeout", () => {
		expect(DEFAULT_FORM_VALUES.decision).toEqual({
			...DEFAULT_MODEL,
			previous_message_count: 1,
			timeout: "1500ms",
			...EMPTY_DECISION_GUIDANCE,
		});
	});

	test("restores the saved classifier and defaults an empty legacy value", () => {
		const saved: AnalyzerConfig = {
			keywords: {
				simple_keywords: ["simple"],
				medium_keywords: ["medium"],
				complex_keywords: ["complex"],
			},
			classifier: "decision",
		};
		expect(toFormValues(saved).classifier).toBe("decision");
		expect(toFormValues({ ...saved, classifier: "" as never }).classifier).toBe("semantic");
	});

	test("builds a valid Decision payload with the configured timeout", () => {
		const values = {
			...DEFAULT_FORM_VALUES,
			classifier: "decision" as const,
			keywords: {
				simple_keywords: ["simple"],
				medium_keywords: ["medium"],
				complex_keywords: ["complex"],
			},
			decision: {
				...DEFAULT_MODEL,
				previous_message_count: 1,
				timeout: "400ms",
				...EMPTY_DECISION_GUIDANCE,
			},
		};
		const parsed = analyzerConfigSchema.safeParse(values);
		expect(parsed.success).toBe(true);
		if (!parsed.success) return;

		const payload = toAnalyzerPayload(parsed.data);
		expect(payload.classifier).toBe("decision");
		expect(payload.decision).toEqual({ ...DEFAULT_MODEL, previous_message_count: 1, timeout: "400ms" });
		expect(payload.semantic).toBeUndefined();
	});

	test("hidden Decision fields do not block a semantic save without a Decision fallback", () => {
		const values = {
			...DEFAULT_FORM_VALUES,
			keywords: {
				simple_keywords: ["simple"],
				medium_keywords: ["medium"],
				complex_keywords: ["complex"],
			},
			decision: {
				provider: "",
				model: "",
				previous_message_count: Number.NaN,
				timeout: "",
				...EMPTY_DECISION_GUIDANCE,
			},
		};
		const parsed = analyzerConfigSchema.safeParse(values);
		expect(parsed.success).toBe(true);
		if (!parsed.success) return;

		const saved: AnalyzerConfig = { ...parsed.data, decision: { previous_message_count: 2, timeout: "900ms" } };
		expect(toAnalyzerPayload(parsed.data, saved).decision).toEqual(saved.decision);
		expect(toAnalyzerPayload(parsed.data).decision).toBeUndefined();
	});

	test("rejects invalid Decision fields when Decision is the semantic fallback", () => {
		const values = {
			...DEFAULT_FORM_VALUES,
			keywords: {
				simple_keywords: ["simple"],
				medium_keywords: ["medium"],
				complex_keywords: ["complex"],
			},
			semantic: { ...DEFAULT_FORM_VALUES.semantic, fallback: "decision" as const },
			decision: { ...DEFAULT_MODEL, previous_message_count: 9, timeout: "", ...EMPTY_DECISION_GUIDANCE },
		};
		const parsed = analyzerConfigSchema.safeParse(values);
		expect(parsed.success).toBe(false);
		if (parsed.success) return;
		expect(parsed.error.issues.map((issue) => issue.path.join("."))).toEqual(
			expect.arrayContaining(["decision.previous_message_count", "decision.timeout"]),
		);
	});
});

describe("Decision complexity form state", () => {
	const keywords = { simple_keywords: ["simple"], medium_keywords: ["medium"], complex_keywords: ["complex"] };

	test("fills a partial saved Decision block with defaults", () => {
		// Older gateways may omit the count; the cast models that wire shape.
		const partial = { timeout: "900ms" } as AnalyzerConfig["decision"];
		expect(toFormValues({ keywords, classifier: "decision", decision: partial }).decision).toEqual({
			...DEFAULT_MODEL,
			previous_message_count: 1,
			timeout: "900ms",
			...EMPTY_DECISION_GUIDANCE,
		});
		expect(
			toFormValues({
				keywords,
				classifier: "decision",
				decision: { previous_message_count: 0 },
			}).decision,
		).toEqual({
			...DEFAULT_MODEL,
			previous_message_count: 0,
			timeout: "1500ms",
			...EMPTY_DECISION_GUIDANCE,
		});
	});

	test("keeps a saved decision model and round-trips it to the payload", () => {
		const form = toFormValues({
			keywords,
			classifier: "decision",
			decision: { provider: "nimble", model: "nimble-latest", previous_message_count: 1, timeout: "1500ms" },
		});
		expect(form.decision.provider).toBe("nimble");
		expect(form.decision.model).toBe("nimble-latest");
		const parsed = analyzerConfigSchema.safeParse(form);
		expect(parsed.success).toBe(true);
		if (!parsed.success) return;
		expect(toAnalyzerPayload(parsed.data).decision).toMatchObject({ provider: "nimble", model: "nimble-latest" });
	});

	test("rejects a blank decision provider or model while the decision model is in use", () => {
		const base = { ...DEFAULT_FORM_VALUES, classifier: "decision" as const, keywords };
		for (const blank of [{ provider: "" }, { model: "  " }]) {
			const parsed = analyzerConfigSchema.safeParse({ ...base, decision: { ...base.decision, ...blank } });
			expect(parsed.success).toBe(false);
			if (parsed.success) continue;
			expect(parsed.error.issues.map((issue) => issue.path.join("."))).toContain(`decision.${Object.keys(blank)[0]}`);
		}
	});

	test("sends the Decision block when Decision is the semantic fallback", () => {
		const values = {
			...DEFAULT_FORM_VALUES,
			keywords,
			semantic: {
				...DEFAULT_FORM_VALUES.semantic,
				provider: "openai",
				embedding_model: "text-embedding-3-small",
				fallback: "decision" as const,
			},
			decision: {
				...DEFAULT_MODEL,
				previous_message_count: 3,
				timeout: "700ms",
				...EMPTY_DECISION_GUIDANCE,
			},
		};
		const parsed = analyzerConfigSchema.safeParse(values);
		expect(parsed.success).toBe(true);
		if (!parsed.success) return;
		const payload = toAnalyzerPayload(parsed.data);
		expect(payload.classifier).toBe("semantic");
		expect(payload.decision).toEqual({ ...DEFAULT_MODEL, previous_message_count: 3, timeout: "700ms" });
	});

	test("allows session routing with Decision as the classifier and no semantic setup", () => {
		const values = { ...DEFAULT_FORM_VALUES, classifier: "decision" as const, keywords, session: { enabled: true } };
		expect(analyzerConfigSchema.safeParse(values).success).toBe(true);
		expect(
			analyzerConfigSchema.safeParse({
				...values,
				classifier: "semantic" as const,
			}).success,
		).toBe(false);
	});

	test("rejects out-of-range Decision history and non-positive timeouts when Decision is primary", () => {
		const base = {
			...DEFAULT_FORM_VALUES,
			classifier: "decision" as const,
			keywords,
		};
		for (const decision of [
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
					decision: { ...DEFAULT_MODEL, ...decision, ...EMPTY_DECISION_GUIDANCE },
				}).success,
			).toBe(false);
		}
	});
});

describe("Decision classification guidance", () => {
	const keywords = { simple_keywords: ["simple"], medium_keywords: ["medium"], complex_keywords: ["complex"] };
	const decisionValues = (guidance: ReturnType<typeof decisionGuidanceFormValues>) => ({
		...DEFAULT_FORM_VALUES,
		classifier: "decision" as const,
		keywords,
		decision: { ...DEFAULT_MODEL, previous_message_count: 1, timeout: "1500ms", ...guidance },
	});

	test("seeds unset guidance from the shipped defaults", () => {
		expect(decisionGuidanceFormValues(undefined, DECISION_DEFAULTS)).toEqual({
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
		const seeded = decisionGuidanceFormValues(
			{
				previous_message_count: 1,
				criteria: {
					MEDIUM: { signals: ["custom signal"] },
					SIMPLE: { definition: "custom definition" },
				},
			},
			DECISION_DEFAULTS,
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
		const parsed = analyzerConfigSchema.safeParse(decisionValues(decisionGuidanceFormValues(undefined, DECISION_DEFAULTS)));
		expect(parsed.success).toBe(true);
		if (!parsed.success) return;
		const payload = toAnalyzerPayload(parsed.data);
		expect(payload.decision?.criteria?.COMPLEX).toEqual({
			definition: "complex definition",
			signals: ["c-signal"],
			examples: ["c-example"],
		});
	});

	test("omits unseeded guidance so the gateway sends its defaults", () => {
		const parsed = analyzerConfigSchema.safeParse(decisionValues(EMPTY_DECISION_GUIDANCE));
		expect(parsed.success).toBe(true);
		if (!parsed.success) return;
		expect(toAnalyzerPayload(parsed.data).decision).toEqual({
			...DEFAULT_MODEL,
			previous_message_count: 1,
			timeout: "1500ms",
		});
	});

	test("rejects an emptied definition or list once guidance is seeded", () => {
		const seeded = decisionGuidanceFormValues(undefined, DECISION_DEFAULTS);
		const emptiedList = analyzerConfigSchema.safeParse(
			decisionValues({
				...seeded,
				criteria: {
					...seeded.criteria,
					SIMPLE: { ...seeded.criteria.SIMPLE, signals: [] },
				},
			}),
		);
		expect(emptiedList.success).toBe(false);
		if (emptiedList.success) return;
		expect(emptiedList.error.issues.map((issue) => issue.path.join("."))).toContain("decision.criteria.SIMPLE.signals");
		const emptiedDefinition = analyzerConfigSchema.safeParse(
			decisionValues({
				...seeded,
				criteria: {
					...seeded.criteria,
					SIMPLE: { ...seeded.criteria.SIMPLE, definition: " " },
				},
			}),
		);
		expect(emptiedDefinition.success).toBe(false);
		if (emptiedDefinition.success) return;
		expect(emptiedDefinition.error.issues.map((issue) => issue.path.join("."))).toContain("decision.criteria.SIMPLE.definition");
	});

	test("rejects guidance past the gateway's size bounds", () => {
		const seeded = decisionGuidanceFormValues(undefined, DECISION_DEFAULTS);
		const tooMany = Array.from({ length: 13 }, (_, i) => `signal ${i}`);
		expect(
			analyzerConfigSchema.safeParse(
				decisionValues({
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
				decisionValues({
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
				decisionValues({
					...seeded,
					criteria: {
						...seeded.criteria,
						MEDIUM: { ...seeded.criteria.MEDIUM, definition: "x".repeat(501) },
					},
				}),
			).success,
		).toBe(false);
	});

	test("shows the saved Decision timeout as editable milliseconds", () => {
		expect(decisionTimeoutFieldValue("400ms")).toBe("400");
		expect(decisionTimeoutFieldValue("1.5s")).toBe(1500);
		expect(decisionTimeoutFieldValue(undefined)).toBe(1500);
		expect(decisionTimeoutFieldValue("")).toBe("");
	});
});

describe("router configuration state", () => {
	test("treats Decision as configured without a semantic block", () => {
		expect(
			isRouterConfigured({
				keywords: {
					simple_keywords: [],
					medium_keywords: [],
					complex_keywords: [],
				},
				classifier: "decision",
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

describe("decision model provider state", () => {
	const provider = (overrides: Partial<ModelProvider> = {}) =>
		({ name: "typesafe", provider_status: "active", ...overrides }) as ModelProvider;
	const key = (overrides: Partial<DBKey> = {}) => ({ provider: "typesafe", ...overrides }) as DBKey;

	test("reports a missing provider", () => {
		expect(getDecisionProviderState([], [key()], "typesafe")).toBe("missing");
		expect(getDecisionProviderState(undefined, undefined, "typesafe")).toBe("missing");
		expect(getDecisionProviderState([provider()], [key()], "nimble")).toBe("missing");
	});

	test("reports a provider that failed to initialise or list models", () => {
		expect(getDecisionProviderState([provider({ provider_status: "error" } as Partial<ModelProvider>)], [key()], "typesafe")).toBe(
			"failing",
		);
		expect(getDecisionProviderState([provider({ status: "list_models_failed" } as Partial<ModelProvider>)], [key()], "typesafe")).toBe(
			"failing",
		);
	});

	test("requires an enabled key on the selected provider, treating an omitted flag as enabled", () => {
		expect(getDecisionProviderState([provider()], [key({ enabled: false } as Partial<DBKey>)], "typesafe")).toBe("no-enabled-key");
		expect(getDecisionProviderState([provider()], [key({ provider: "openai" } as Partial<DBKey>)], "typesafe")).toBe("no-enabled-key");
		expect(getDecisionProviderState([provider()], [key()], "typesafe")).toBe("configured");
		expect(
			getDecisionProviderState(
				[provider()],
				[key({ enabled: false } as Partial<DBKey>), key({ enabled: true } as Partial<DBKey>)],
				"typesafe",
			),
		).toBe("configured");
	});

	test("checks the selected custom provider and accepts a keyless one without keys", () => {
		const nimble = provider({ name: "nimble", custom_provider_config: { base_provider_type: "typesafe" } } as Partial<ModelProvider>);
		const laya = provider({
			name: "Laya",
			custom_provider_config: { base_provider_type: "typesafe", is_key_less: true },
		} as Partial<ModelProvider>);
		expect(getDecisionProviderState([provider(), nimble], [key()], "nimble")).toBe("no-enabled-key");
		expect(getDecisionProviderState([provider(), nimble], [key({ provider: "nimble" } as Partial<DBKey>)], "nimble")).toBe("configured");
		expect(getDecisionProviderState([laya], [], "Laya")).toBe("configured");
	});

	test("ignores a stale listing failure on a provider with model listing turned off", () => {
		const laya = provider({
			name: "Laya",
			status: "list_models_failed",
			custom_provider_config: {
				base_provider_type: "typesafe",
				is_key_less: true,
				allowed_requests: { list_models: false, decisions: true },
			},
		} as Partial<ModelProvider>);
		expect(getDecisionProviderState([laya], [], "Laya")).toBe("configured");
		const listing = provider({
			name: "nimble",
			status: "list_models_failed",
			custom_provider_config: { base_provider_type: "typesafe", is_key_less: true },
		} as Partial<ModelProvider>);
		expect(getDecisionProviderState([listing], [], "nimble")).toBe("failing");
	});
});

describe("decision model providers", () => {
	const keywords = { simple_keywords: ["simple"], medium_keywords: ["medium"], complex_keywords: ["complex"] };
	const provider = (overrides: Partial<ModelProvider>) => ({ provider_status: "active", ...overrides }) as ModelProvider;
	const custom = (name: string, decisionsURL?: string) =>
		provider({
			name,
			custom_provider_config: {
				base_provider_type: "typesafe",
				...(decisionsURL && { request_path_overrides: { decisions: decisionsURL } }),
			},
		} as Partial<ModelProvider>);
	const typesafe = provider({ name: "typesafe" } as Partial<ModelProvider>);
	const openrouter = provider({ name: "openrouter" } as Partial<ModelProvider>);
	const openai = provider({ name: "openai" } as Partial<ModelProvider>);
	const laya = custom("Laya");
	const clef = custom("cloudflare clev", "https://api.cloudflare.com/client/v4/accounts/acct/ai/run/@cf/cloudflare/clef");
	const clefFlash = custom("clef-flash", "https://api.cloudflare.com/client/v4/accounts/acct/ai/run/@cf/cloudflare/clef-flash/");

	test("offers only providers that answer decisions natively", () => {
		for (const candidate of [typesafe, openrouter, openai, laya, clef]) expect(isDecisionProvider(candidate)).toBe(true);
		expect(isDecisionProvider(provider({ name: "anthropic" } as Partial<ModelProvider>))).toBe(false);
		expect(
			isDecisionProvider(
				provider({ name: "my-anthropic", custom_provider_config: { base_provider_type: "anthropic" } } as Partial<ModelProvider>),
			),
		).toBe(false);
	});

	test("treats a custom OpenAI-based provider like OpenAI", () => {
		const customOpenAI = (name: string) =>
			provider({ name, custom_provider_config: { base_provider_type: "openai" } } as Partial<ModelProvider>);
		expect(isDecisionProvider(customOpenAI("my-openai"))).toBe(true);
		expect(isOpenAIDecisionProvider(customOpenAI("my-openai"))).toBe(true);
		expect(isOpenAIDecisionProvider(openai)).toBe(true);
		expect(isOpenAIDecisionProvider(typesafe)).toBe(false);
		// Only its listing says which decision models it serves, whatever it is named.
		expect(defaultDecisionModel(customOpenAI("my-openai"))).toBe("");
		expect(defaultDecisionModel(customOpenAI("laya-gateway"))).toBe("");
	});

	test("reads the Clef model from the provider's Cloudflare URL", () => {
		expect(clefModelFromProvider(clef)).toBe("clef");
		expect(clefModelFromProvider(clefFlash)).toBe("clef-flash");
		expect(clefModelFromProvider(laya)).toBeUndefined();
		expect(clefModelFromProvider(undefined)).toBeUndefined();
	});

	test("offers only the checkpoints of the model a provider is named after", () => {
		expect(selfHostedModelGroups("Laya").map((group) => group.label)).toEqual(["Laya"]);
		expect(selfHostedModelGroups("nimble-gpu").map((group) => group.label)).toEqual(["Nimble"]);
		expect(selfHostedModelGroups("ollama-clef").map((group) => group.label)).toEqual(["Clef"]);
		expect(selfHostedModelGroups("decisions-eu").map((group) => group.label)).toEqual(["Laya", "Nimble", "Clef"]);
	});

	test("starts each provider on its known model", () => {
		expect(defaultDecisionModel(typesafe)).toBe("jev-latest");
		expect(defaultDecisionModel(openrouter)).toBe("~typesafe/jev-latest");
		expect(defaultDecisionModel(openai)).toBe("gpt-6-luna");
		expect(defaultDecisionModel(clefFlash)).toBe("clef-flash");
		expect(defaultDecisionModel(laya)).toBe("english");
		expect(defaultDecisionModel(custom("nimble-gpu"))).toBe("nimble-latest");
		expect(defaultDecisionModel(custom("ollama-clef"))).toBe("clef-flash");
	});

	test("leaves OpenRouter's decision model to the decisions listing", () => {
		// The picker offers only the models /api/models marks as serving decisions, so the
		// schema accepts any OpenRouter model rather than a fixed list.
		const base = { ...DEFAULT_FORM_VALUES, classifier: "decision" as const, keywords };
		const parse = (model: string) =>
			analyzerConfigSchema.safeParse({ ...base, decision: { ...base.decision, provider: "openrouter", model } });
		expect(parse("~typesafe/jev-latest").success).toBe(true);
		expect(parse("cloudflare/clef").success).toBe(true);
	});

	test("starts a self-hosted provider whose name says nothing on none", () => {
		expect(defaultDecisionModel(custom("decisions-eu"))).toBe("");
		expect(defaultDecisionModel(undefined)).toBe("");
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