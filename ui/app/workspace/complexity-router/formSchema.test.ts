import i18n from "@/lib/i18n";
import { describe, expect, test } from "vitest";
import {
	analyzerConfigSchema,
	countCanonicalSemanticPhrases,
	DEFAULT_FORM_VALUES,
	DEFAULT_SUGGESTIONS_FORM_VALUES,
	getDecisionProviderState,
	isRouterConfigured,
	isTierCriteriaEmpty,
	lastKnownTierDefaults,
	decisionGuidanceFormValues,
	decisionTimeoutFieldValue,
	fromLocalDateTime,
	nextSuggestionRun,
	shouldSeedLLMPrompt,
	suggestionsFormValues,
	tierCriteriaFormValues,
	tierCriteriaReseedValues,
	tierCriteriaToReseed,
	toAnalyzerPayload,
	toComplexityConfigPayload,
	toFormValues,
	toSuggestionsPayload,
	withGeneratedTierCriteriaCleared,
} from "./formSchema";
import {
	DEFAULT_DECISION_CONFIG,
	MAX_DECISION_CRITERIA_ITEM_CHARACTERS,
	MAX_DECISION_PREVIOUS_MESSAGE_COUNT,
	MAX_LLM_MESSAGE_HISTORY,
	MAX_LLM_PROMPT_CHARACTERS,
	MAX_SEMANTIC_MESSAGE_HISTORY,
	MAX_SEMANTIC_PHRASE_CHARACTERS,
	MAX_SEMANTIC_TIMEOUT_MS,
	type AnalyzerConfig,
	type DecisionGuidanceDefaults,
	type ModelRouter,
	type SuggestionTierDefaults,
} from "@/lib/types/complexityRouter";
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
			min_confidence: 0,
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
		expect(toFormValues({ ...saved, classifier: "llm" }).classifier).toBe("llm");
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
				min_confidence: 0,
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
				min_confidence: 0,
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

	test("round-trips the saved suggestions block through the form", () => {
		const saved: AnalyzerConfig = {
			keywords: { simple_keywords: ["simple"], medium_keywords: ["medium"], complex_keywords: ["complex"] },
			semantic: { provider: "openai", embedding_model: "text-embedding-3-small" },
			suggestions: {
				enabled: true,
				sample_rate: 0.1,
				lookback_days: 14,
				schedule: { every: "5h", start_at: "2026-11-12T03:30:00Z" },
				max_clustered_requests: 5000,
				min_cluster_requests: 8,
				provider: "openai",
				model: "gpt-5",
				instructions: "be brief",
				tier: { classifier: "decision", provider: "typesafe", model: "jev-latest", instructions: "judge the work" },
			},
		};
		const values = toFormValues(saved);
		expect(values.suggestions.sample_percent).toBe(10);
		// Any interval the gateway accepts reads back as its count and unit,
		// and the first run is shown in local time and sent back in UTC.
		expect(values.suggestions.every_count).toBe(5);
		expect(values.suggestions.every_unit).toBe("h");
		expect(toSuggestionsPayload(values.suggestions, saved.suggestions).schedule).toEqual({ every: "5h", start_at: "2026-11-12T03:30:00Z" });
		expect(values.suggestions.tier_classifier).toBe("decision");
		const parsed = analyzerConfigSchema.safeParse(values);
		expect(parsed.success).toBe(true);
		if (!parsed.success) return;
		expect(toAnalyzerPayload(parsed.data, saved).suggestions).toEqual(saved.suggestions);
	});

	test("keeps a small sample rate through the form instead of rounding it to zero", () => {
		const keywords = { simple_keywords: ["simple"], medium_keywords: ["medium"], complex_keywords: ["complex"] };
		for (const rate of [0.000001, 0.00012345, 0.1, 0.333, 1]) {
			const saved: AnalyzerConfig = {
				keywords,
				semantic: { provider: "openai", embedding_model: "text-embedding-3-small" },
				suggestions: {
					enabled: true,
					sample_rate: rate,
					lookback_days: 7,
					provider: "openai",
					model: "gpt-5",
					tier: { classifier: "decision", provider: "typesafe", model: "jev-latest" },
				},
			};
			const values = toFormValues(saved);
			expect(values.suggestions.sample_percent).toBeGreaterThan(0);
			const parsed = analyzerConfigSchema.safeParse(values);
			expect(parsed.success, `rate ${rate} must stay valid in the form`).toBe(true);
			if (!parsed.success) return;
			expect(toAnalyzerPayload(parsed.data, saved).suggestions?.sample_rate).toBe(rate);
		}
	});

	test("turns suggestions off, keeping their settings, when the classifier is not semantic", () => {
		// The gateway refuses suggestions on any other classifier: they cluster
		// in the semantic classifier's embedding space.
		const saved: AnalyzerConfig = {
			classifier: "semantic",
			keywords: { simple_keywords: ["simple"], medium_keywords: ["medium"], complex_keywords: ["complex"] },
			semantic: { provider: "openai", embedding_model: "text-embedding-3-small" },
			suggestions: {
				enabled: true,
				sample_rate: 0.1,
				lookback_days: 7,
				schedule: { every: "1d", start_at: "2026-11-12T03:00:00Z" },
				provider: "openai",
				model: "gpt-4.1-mini",
			},
		};
		const values = toFormValues(saved);
		for (const classifier of ["llm", "decision"] as const) {
			const switched = {
				...values,
				classifier,
				llm: { ...values.llm, ...DEFAULT_MODEL },
				decision: { ...values.decision, ...DEFAULT_MODEL },
			};
			const payload = toAnalyzerPayload(switched, saved);
			expect(payload.suggestions?.enabled, classifier).toBe(false);
			expect(payload.suggestions?.model, classifier).toBe("gpt-4.1-mini");
		}
		expect(toAnalyzerPayload(values, saved).suggestions?.enabled).toBe(true);
	});

	test("leaves the cluster sizes to the gateway: not edited, kept as saved, never filled in", () => {
		const saved: AnalyzerConfig = {
			keywords: { simple_keywords: ["simple"], medium_keywords: ["medium"], complex_keywords: ["complex"] },
			semantic: { provider: "openai", embedding_model: "text-embedding-3-small" },
			suggestions: {
				enabled: true,
				lookback_days: 7,
				provider: "openai",
				model: "gpt-5",
				tier: { classifier: "llm", provider: "openai", model: "gpt-5" },
			},
		};
		const values = toFormValues(saved);
		expect(values.suggestions).not.toHaveProperty("max_clustered_requests");
		expect(values.suggestions).not.toHaveProperty("min_cluster_requests");
		const parsed = analyzerConfigSchema.safeParse(values);
		expect(parsed.success).toBe(true);
		if (!parsed.success) return;
		const payload = toAnalyzerPayload(parsed.data, saved).suggestions;
		expect(payload).not.toHaveProperty("max_clustered_requests");
		expect(payload).not.toHaveProperty("min_cluster_requests");
	});

	test("needs both models named while suggestions are on, and each provider paired with a model", () => {
		const keywords = { simple_keywords: ["simple"], medium_keywords: ["medium"], complex_keywords: ["complex"] };
		const semantic = { ...DEFAULT_FORM_VALUES.semantic, provider: "openai", embedding_model: "text-embedding-3-small" };
		const issues = (suggestions: Partial<typeof DEFAULT_FORM_VALUES.suggestions>) => {
			const parsed = analyzerConfigSchema.safeParse({
				...DEFAULT_FORM_VALUES,
				keywords,
				semantic,
				suggestions: { ...DEFAULT_FORM_VALUES.suggestions, ...suggestions },
			});
			return parsed.success ? [] : parsed.error.issues.map((issue) => issue.path.join("."));
		};
		// Off, nothing needs a model: the settings are only kept.
		expect(issues({ enabled: false })).toEqual([]);
		// On, neither model may be left to a fallback.
		expect(issues({ enabled: true })).toEqual(["suggestions.model", "suggestions.tier_model"]);
		const named = { enabled: true, provider: "openai", model: "gpt-5", tier_provider: "typesafe", tier_model: "jev-latest" };
		expect(issues({ ...named, tier_classifier: "decision" })).toEqual([]);
		expect(issues({ ...named, tier_model: "" })).toEqual(["suggestions.tier_model"]);
		expect(issues({ ...named, tier_provider: "" })).toEqual(["suggestions.tier_provider"]);
	});

	test("needs the semantic classifier's embedding model while suggestions are on", () => {
		const keywords = { simple_keywords: ["simple"], medium_keywords: ["medium"], complex_keywords: ["complex"] };
		const named = {
			enabled: true,
			provider: "openai",
			model: "gpt-5",
			tier_provider: "typesafe",
			tier_model: "jev-latest",
			tier_classifier: "decision" as const,
		};
		const issues = (
			semantic: Partial<typeof DEFAULT_FORM_VALUES.semantic>,
			suggestions: Partial<typeof DEFAULT_FORM_VALUES.suggestions>,
		) => {
			const parsed = analyzerConfigSchema.safeParse({
				...DEFAULT_FORM_VALUES,
				keywords,
				semantic: { ...DEFAULT_FORM_VALUES.semantic, ...semantic },
				suggestions: { ...DEFAULT_FORM_VALUES.suggestions, ...suggestions },
			});
			return parsed.success ? [] : parsed.error.issues.map((issue) => issue.path.join("."));
		};
		// The gateway refuses enabled suggestions without it, so the form says so where it is set.
		expect(issues({}, named)).toEqual(["semantic.provider", "semantic.embedding_model"]);
		expect(issues({ provider: "openai", embedding_model: "text-embedding-3-small" }, named)).toEqual([]);
		// With suggestions off, an unconfigured classifier is still a legal draft.
		expect(issues({}, { enabled: false })).toEqual([]);
	});

	test("always sends the tier step, and only the model and prompt it has", () => {
		const parsed = analyzerConfigSchema.safeParse({
			...DEFAULT_FORM_VALUES,
			keywords: { simple_keywords: ["simple"], medium_keywords: ["medium"], complex_keywords: ["complex"] },
			semantic: { ...DEFAULT_FORM_VALUES.semantic, provider: "openai", embedding_model: "text-embedding-3-small" },
			suggestions: {
				...DEFAULT_FORM_VALUES.suggestions,
				enabled: true,
				provider: "openai",
				model: "gpt-5",
				tier_provider: "openai",
				tier_model: "gpt-5",
			},
		});
		expect(parsed.success).toBe(true);
		if (!parsed.success) return;
		expect(toAnalyzerPayload(parsed.data).suggestions?.tier).toEqual({ classifier: "llm", provider: "openai", model: "gpt-5" });
		const prompted = { ...parsed.data, suggestions: { ...parsed.data.suggestions, tier_instructions: "judge the work" } };
		expect(toAnalyzerPayload(prompted).suggestions?.tier?.instructions).toBe("judge the work");
	});

	describe("tier step text", () => {
		const keywords = { simple_keywords: ["simple"], medium_keywords: ["medium"], complex_keywords: ["complex"] };
		// Enabled suggestions need the semantic classifier's embedding model.
		const semantic = { provider: "openai", embedding_model: "text-embedding-3-small" };
		const generated: SuggestionTierDefaults = {
			prompt: "Pick a tier.\n\nTier definitions:\n...\n\nMEDIUM:\n- medium",
			criteria: {
				SIMPLE: { definition: "One short answer.", signals: ["a lookup"], examples: [] },
				MEDIUM: { definition: "Routine work.", signals: ["a draft"], examples: ["medium"] },
				COMPLEX: { definition: "Design work.", signals: ["trade-offs"], examples: [] },
			},
		};
		const decision = {
			enabled: true,
			provider: "openai",
			model: "gpt-5",
			tier: { classifier: "decision" as const, provider: "typesafe", model: "jev-latest" },
		};

		test("seeds each tier from its saved edit, the rest from what the gateway generates", () => {
			const saved: AnalyzerConfig = {
				keywords,
				semantic,
				suggestions: {
					...decision,
					tier: { ...decision.tier, criteria: { COMPLEX: { definition: "Our own.", examples: ["design failover"] } } },
				},
			};
			const criteria = toFormValues(saved, undefined, generated).suggestions.tier_criteria;
			expect(criteria.SIMPLE).toEqual(generated.criteria.SIMPLE);
			expect(criteria.COMPLEX).toEqual({ definition: "Our own.", signals: [], examples: ["design failover"] });
			// Before the generated criteria arrive, only saved tiers are filled.
			const early = toFormValues(saved).suggestions.tier_criteria;
			expect(early.SIMPLE.definition).toBe("");
			expect(isTierCriteriaEmpty(early)).toBe(false);
		});

		test("sends only what differs from the generated text, so the rest keeps following the router", () => {
			const saved: AnalyzerConfig = { keywords, semantic, suggestions: decision };
			const values = toFormValues(saved, undefined, generated);
			const parsed = analyzerConfigSchema.safeParse(values);
			expect(parsed.success).toBe(true);
			if (!parsed.success) return;
			// Untouched: nothing frozen.
			expect(toAnalyzerPayload(parsed.data, saved, generated).suggestions?.tier).toEqual(decision.tier);
			// One tier edited: only that tier is sent, as written.
			const edited = structuredClone(parsed.data);
			edited.suggestions.tier_criteria.MEDIUM.examples = ["medium", "draft a refund reply"];
			expect(toAnalyzerPayload(edited, saved, generated).suggestions?.tier?.criteria).toEqual({
				MEDIUM: { definition: "Routine work.", signals: ["a draft"], examples: ["medium", "draft a refund reply"] },
			});
			// An llm prompt identical to the generated one is the generated one.
			const llm = { ...edited, suggestions: { ...edited.suggestions, tier_classifier: "llm" as const, tier_prompt: generated.prompt } };
			expect(toAnalyzerPayload(llm, saved, generated).suggestions?.tier?.prompt).toBeUndefined();
			const rewritten = { ...llm, suggestions: { ...llm.suggestions, tier_prompt: "Refunds are SIMPLE." } };
			expect(toAnalyzerPayload(rewritten, saved, generated).suggestions?.tier?.prompt).toBe("Refunds are SIMPLE.");
		});

		test("keeps saved edits when the generated text is not known yet", () => {
			const criteria = { COMPLEX: { definition: "Our own.", examples: ["design failover"] } };
			const saved: AnalyzerConfig = { keywords, semantic, suggestions: { ...decision, tier: { ...decision.tier, criteria } } };
			const parsed = analyzerConfigSchema.safeParse(toFormValues(saved));
			expect(parsed.success).toBe(true);
			if (!parsed.success) return;
			expect(toAnalyzerPayload(parsed.data, saved).suggestions?.tier?.criteria).toEqual(criteria);
		});

		test("compares edits with the router's last known generated text when a refresh leaves it out", () => {
			const known = lastKnownTierDefaults(undefined, "support", generated);
			expect(known?.defaults).toBe(generated);
			expect(lastKnownTierDefaults(known, "support", undefined)?.defaults).toBe(generated);
			expect(lastKnownTierDefaults(known, "billing", undefined), "another router's text never carries over").toBeUndefined();
			const fresher = { ...generated, prompt: "Pick one." };
			expect(lastKnownTierDefaults(known, "support", fresher)?.defaults).toBe(fresher);
			// An edit made against the known text is sent, not replaced by the saved criteria.
			const saved: AnalyzerConfig = { keywords, semantic, suggestions: decision };
			const values = toFormValues(saved, undefined, generated);
			values.suggestions.tier_criteria.MEDIUM.examples = ["medium", "draft a refund reply"];
			const kept = lastKnownTierDefaults(known, "support", undefined)?.defaults;
			expect(toAnalyzerPayload(values, saved, kept).suggestions?.tier?.criteria).toEqual({
				MEDIUM: { definition: "Routine work.", signals: ["a draft"], examples: ["medium", "draft a refund reply"] },
			});
		});

		test("moves untouched tiers to new generated text with the baseline, keeping edited ones", () => {
			const saved: AnalyzerConfig = { keywords, semantic, suggestions: decision };
			const values = toFormValues(saved, undefined, generated);
			values.suggestions.tier_criteria.MEDIUM.examples = ["medium", "draft a refund reply"];
			// The router's phrases changed: the gateway now generates other examples.
			const fresher: SuggestionTierDefaults = {
				...generated,
				criteria: {
					SIMPLE: { ...generated.criteria.SIMPLE, examples: ["simple", "track my parcel"] },
					MEDIUM: { ...generated.criteria.MEDIUM, examples: ["medium", "summarize the call"] },
					COMPLEX: generated.criteria.COMPLEX,
				},
			};
			expect(tierCriteriaToReseed(values.suggestions.tier_criteria, generated.criteria)).toEqual(["SIMPLE", "COMPLEX"]);
			const reseed = tierCriteriaReseedValues(
				values.suggestions.tier_criteria,
				generated.criteria,
				saved.suggestions?.tier,
				fresher.criteria,
			);
			for (const [tier, criteria] of reseed) values.suggestions.tier_criteria[tier] = criteria;
			// Only the edited tier is sent; the untouched ones keep following the router.
			expect(toAnalyzerPayload(values, saved, fresher).suggestions?.tier?.criteria).toEqual({
				MEDIUM: { definition: "Routine work.", signals: ["a draft"], examples: ["medium", "draft a refund reply"] },
			});
			// With nothing seeded before, only empty tiers are filled.
			expect(tierCriteriaToReseed(values.suggestions.tier_criteria, undefined)).toEqual([]);
		});

		test("a tier put back to the generated text follows newer generated text, not its saved edit", () => {
			const ours = { definition: "Our own.", signals: ["a refund"], examples: ["refund reply"] };
			const saved: AnalyzerConfig = {
				keywords,
				semantic,
				suggestions: { ...decision, tier: { ...decision.tier, criteria: { MEDIUM: ours } } },
			};
			const values = toFormValues(saved, undefined, generated);
			expect(values.suggestions.tier_criteria.MEDIUM).toEqual(ours);
			// The operator puts the generated criteria back in the editor.
			values.suggestions.tier_criteria.MEDIUM = { definition: "Routine work.", signals: ["a draft"], examples: ["medium"] };
			const fresher: SuggestionTierDefaults = {
				...generated,
				criteria: { ...generated.criteria, MEDIUM: { ...generated.criteria.MEDIUM, examples: ["medium", "summarize the call"] } },
			};
			const reseed = tierCriteriaReseedValues(
				values.suggestions.tier_criteria,
				generated.criteria,
				saved.suggestions?.tier,
				fresher.criteria,
			);
			expect(new Map(reseed).get("MEDIUM"), "the saved edit is not put back over the operator's change").toEqual(fresher.criteria.MEDIUM);
			for (const [tier, criteria] of reseed) values.suggestions.tier_criteria[tier] = criteria;
			// The tier follows the router again, so its saved edit is no longer sent.
			expect(toAnalyzerPayload(values, saved, fresher).suggestions?.tier?.criteria).toBeUndefined();

			// A tier still empty is seeded from its saved edit, as on first load.
			const empty = toFormValues(saved, undefined, undefined);
			empty.suggestions.tier_criteria.MEDIUM = { definition: "", signals: [], examples: [] };
			const first = tierCriteriaReseedValues(empty.suggestions.tier_criteria, undefined, saved.suggestions?.tier, generated.criteria);
			expect(new Map(first).get("MEDIUM")).toEqual(ours);
			expect(new Map(first).get("SIMPLE")).toEqual(generated.criteria.SIMPLE);
		});

		test("needs a definition for every tier a decision step sends, but lets lists be empty", () => {
			const saved: AnalyzerConfig = { keywords, semantic, suggestions: decision };
			const values = toFormValues(saved, undefined, generated);
			expect(analyzerConfigSchema.safeParse(values).success).toBe(true);
			values.suggestions.tier_criteria.SIMPLE.definition = " ";
			const parsed = analyzerConfigSchema.safeParse(values);
			expect(parsed.success).toBe(false);
			if (parsed.success) return;
			expect(parsed.error.issues.map((issue) => issue.path.join("."))).toEqual(["suggestions.tier_criteria.SIMPLE.definition"]);
		});

		test("does not hold generated examples to the edited-example limit, since they are not sent", () => {
			// A router phrase may be longer than an edited example may.
			const long = "x".repeat(MAX_DECISION_CRITERIA_ITEM_CHARACTERS + 1);
			const withLong: SuggestionTierDefaults = {
				...generated,
				criteria: { ...generated.criteria, MEDIUM: { ...generated.criteria.MEDIUM, examples: [long] } },
			};
			const saved: AnalyzerConfig = { keywords, semantic, suggestions: decision };
			const values = toFormValues(saved, undefined, withLong);
			expect(analyzerConfigSchema.safeParse(values).success).toBe(false);
			const checked = withGeneratedTierCriteriaCleared(values, withLong.criteria);
			expect(analyzerConfigSchema.safeParse(checked).success).toBe(true);
			// The values themselves are left as written.
			expect(values.suggestions.tier_criteria.MEDIUM.examples).toEqual([long]);
			// Once edited, the tier is sent, and checked as written.
			values.suggestions.tier_criteria.MEDIUM.definition = "Routine work, edited.";
			expect(analyzerConfigSchema.safeParse(withGeneratedTierCriteriaCleared(values, withLong.criteria)).success).toBe(false);
			// Before the generated criteria arrive, nothing is cleared.
			expect(withGeneratedTierCriteriaCleared(values, undefined)).toBe(values);
		});
	});

	test("sends suggestions only once they have been used, and keeps them when turned off", () => {
		const parsed = analyzerConfigSchema.safeParse({
			...DEFAULT_FORM_VALUES,
			keywords: { simple_keywords: ["simple"], medium_keywords: ["medium"], complex_keywords: ["complex"] },
		});
		expect(parsed.success).toBe(true);
		if (!parsed.success) return;
		expect(toAnalyzerPayload(parsed.data).suggestions).toBeUndefined();

		const saved: AnalyzerConfig = { ...toAnalyzerPayload(parsed.data), suggestions: { enabled: true, lookback_days: 30 } };
		const off = { ...toFormValues(saved).suggestions, enabled: false };
		const payload = toAnalyzerPayload({ ...parsed.data, suggestions: off }, saved).suggestions;
		expect(payload?.enabled).toBe(false);
		expect(payload?.lookback_days).toBe(30);
	});

	test("an unset first run shows the next run of the gateway's default schedule", () => {
		const now = new Date("2026-11-10T10:00:00Z");
		const daily = suggestionsFormValues({ enabled: true }, undefined, now);
		expect(fromLocalDateTime(daily.start_at)?.toISOString()).toBe("2026-11-11T03:00:00.000Z");
		const sixHourly = suggestionsFormValues({ enabled: true, schedule: { every: "6h" } }, undefined, now);
		expect(fromLocalDateTime(sixHourly.start_at)?.toISOString()).toBe("2026-11-10T15:00:00.000Z");
		// An interval the gateway would refuse reads as the default.
		const bad = suggestionsFormValues({ enabled: true, schedule: { every: "90m" } }, undefined, now);
		expect([bad.every_count, bad.every_unit]).toEqual([1, "d"]);
	});

	test("schedules send the interval and the first run in UTC", () => {
		const base = { ...DEFAULT_SUGGESTIONS_FORM_VALUES, enabled: true };
		const firstRun = "2026-11-12T03:30";
		expect(toSuggestionsPayload({ ...base, every_count: 6, every_unit: "h", start_at: firstRun }).schedule).toEqual({
			every: "6h",
			start_at: new Date(firstRun).toISOString().replace(".000Z", "Z"),
		});
		expect(toSuggestionsPayload({ ...base, every_count: 2, every_unit: "w", start_at: "" }).schedule).toEqual({ every: "2w" });
		expect(toSuggestionsPayload({ ...base, sample_percent: 2.5 }).sample_rate).toBe(0.025);
		expect(toSuggestionsPayload({ ...base, provider: "openai", model: "" })).not.toHaveProperty("provider");
	});

	test("holds the interval to what the gateway accepts for its unit, and a half-chosen suggestion model", () => {
		const values = {
			...DEFAULT_FORM_VALUES,
			keywords: { simple_keywords: ["simple"], medium_keywords: ["medium"], complex_keywords: ["complex"] },
		};
		const parse = (suggestions: Partial<typeof DEFAULT_SUGGESTIONS_FORM_VALUES>) =>
			analyzerConfigSchema.safeParse({ ...values, suggestions: { ...DEFAULT_SUGGESTIONS_FORM_VALUES, ...suggestions } });
		const issueAt = (result: ReturnType<typeof parse>, path: string) => result.error?.issues.find((i) => i.path.join(".") === path);

		expect(parse({ every_count: 23, every_unit: "h" }).success).toBe(true);
		expect(parse({ every_count: 30, every_unit: "d" }).success).toBe(true);
		expect(parse({ every_count: 4, every_unit: "w" }).success).toBe(true);
		const hours = parse({ every_count: 24, every_unit: "h" });
		expect(issueAt(hours, "suggestions.every_count")?.message).toBe(
			i18n.t("routing.complexityUi.suggestions.everyMax.h", { ns: "models", max: 23 }),
		);
		expect(issueAt(parse({ every_count: 5, every_unit: "w" }), "suggestions.every_count")).toBeDefined();
		expect(issueAt(parse({ every_count: 0, every_unit: "d" }), "suggestions.every_count")).toBeDefined();

		expect(parse({ start_at: "" }).success).toBe(true);
		expect(issueAt(parse({ start_at: "tomorrow" }), "suggestions.start_at")).toBeDefined();
		expect(issueAt(parse({ provider: "openai" }), "suggestions.model")).toBeDefined();
	});

	test("the next run is the first run while it is ahead, then one interval apart from it", () => {
		const start = new Date("2026-11-10T03:00:00Z");
		expect(nextSuggestionRun("1d", start, new Date("2026-11-09T00:00:00Z"))).toEqual(start);
		expect(nextSuggestionRun("6h", start, new Date("2026-11-10T10:00:00Z"))?.toISOString()).toBe("2026-11-10T15:00:00.000Z");
		expect(nextSuggestionRun("2w", start, new Date("2026-11-10T03:00:01Z"))?.toISOString()).toBe("2026-11-24T03:00:00.000Z");
		expect(nextSuggestionRun("24h", start, new Date())).toBeUndefined();
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
			decision: { ...DEFAULT_MODEL, previous_message_count: 9, timeout: "", min_confidence: 2, ...EMPTY_DECISION_GUIDANCE },
		};
		const parsed = analyzerConfigSchema.safeParse(values);
		expect(parsed.success).toBe(false);
		if (parsed.success) return;
		expect(parsed.error.issues.map((issue) => issue.path.join("."))).toEqual(
			expect.arrayContaining(["decision.previous_message_count", "decision.timeout", "decision.min_confidence"]),
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
			min_confidence: 0,
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
			min_confidence: 0,
			...EMPTY_DECISION_GUIDANCE,
		});
	});

	test("keeps a saved min_confidence and writes it to the payload only when set", () => {
		const saved: AnalyzerConfig = {
			classifier: "decision",
			keywords: { simple_keywords: ["simple"], medium_keywords: ["medium"], complex_keywords: ["complex"] },
			decision: { ...DEFAULT_MODEL, previous_message_count: 1, timeout: "1500ms", min_confidence: 0.7 },
		};
		const values = toFormValues(saved);
		expect(values.decision.min_confidence).toBe(0.7);
		expect(toAnalyzerPayload(values, saved).decision?.min_confidence).toBe(0.7);

		values.decision.min_confidence = 0;
		expect(toAnalyzerPayload(values, saved).decision).not.toHaveProperty("min_confidence");
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
				min_confidence: 0,
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
			{ previous_message_count: 1, timeout: "1500ms", min_confidence: 1.01 },
			{ previous_message_count: 1, timeout: "1500ms", min_confidence: -0.1 },
			{ previous_message_count: 1, timeout: "1500ms", min_confidence: Number.NaN },
		]) {
			expect(
				analyzerConfigSchema.safeParse({
					...base,
					decision: { ...DEFAULT_MODEL, min_confidence: 0, ...decision, ...EMPTY_DECISION_GUIDANCE },
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
		decision: { ...DEFAULT_MODEL, previous_message_count: 1, timeout: "1500ms", min_confidence: 0, ...guidance },
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

	test("treats the llm classifier as configured once it has its model", () => {
		const keywords = { simple_keywords: ["a"], medium_keywords: ["b"], complex_keywords: ["c"] };
		expect(isRouterConfigured({ keywords, classifier: "llm" })).toBe(false);
		expect(isRouterConfigured({ keywords, classifier: "llm", llm: { provider: "openai", model: "gpt-4o-mini" } })).toBe(true);
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

	test("reports a provider that failed to initialise", () => {
		expect(getDecisionProviderState([provider({ provider_status: "error" } as Partial<ModelProvider>)], [key()], "typesafe")).toBe(
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

	test("ignores a model-listing failure", () => {
		const cloudflare = provider({ name: "cloudflare", status: "list_models_failed" } as Partial<ModelProvider>);
		expect(getDecisionProviderState([cloudflare], [key({ provider: "cloudflare" } as Partial<DBKey>)], "cloudflare")).toBe("configured");
		const laya = provider({
			name: "Laya",
			status: "list_models_failed",
			custom_provider_config: { base_provider_type: "typesafe", is_key_less: true },
		} as Partial<ModelProvider>);
		expect(getDecisionProviderState([laya], [], "Laya")).toBe("configured");
	});
});

describe("decision model providers", () => {
	const keywords = { simple_keywords: ["simple"], medium_keywords: ["medium"], complex_keywords: ["complex"] };

	test("leaves OpenRouter's decision model to the decisions listing", () => {
		// The picker offers only the models /api/models marks as serving decisions, so the
		// schema accepts any OpenRouter model rather than a fixed list.
		const base = { ...DEFAULT_FORM_VALUES, classifier: "decision" as const, keywords };
		const parse = (model: string) =>
			analyzerConfigSchema.safeParse({ ...base, decision: { ...base.decision, provider: "openrouter", model } });
		expect(parse("~typesafe/jev-latest").success).toBe(true);
		expect(parse("cloudflare/clef").success).toBe(true);
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
describe("llm classifier", () => {
	const keywords = { simple_keywords: ["a"], medium_keywords: ["b"], complex_keywords: ["c"] };

	test("needs its model and names the classifier in the error", () => {
		const parsed = analyzerConfigSchema.safeParse({ ...DEFAULT_FORM_VALUES, classifier: "llm", keywords });
		expect(parsed.success).toBe(false);
		const paths = parsed.success ? [] : parsed.error.issues.map((issue) => issue.path.join("."));
		expect(paths).toEqual(expect.arrayContaining(["llm.provider", "llm.model"]));
	});

	test("saves with its model and needs no semantic block", () => {
		const values = {
			...DEFAULT_FORM_VALUES,
			classifier: "llm" as const,
			keywords,
			session: { enabled: true },
			llm: { ...DEFAULT_FORM_VALUES.llm, provider: "openai", model: "gpt-4o-mini" },
		};
		expect(analyzerConfigSchema.safeParse(values).success).toBe(true);
		const payload = toAnalyzerPayload(values);
		expect(payload.classifier).toBe("llm");
		expect(payload.llm).toMatchObject({ provider: "openai", model: "gpt-4o-mini" });
		expect(payload.semantic).toBeUndefined();
	});
});
describe("tier criteria with the llm tier step", () => {
	const generated = {
		SIMPLE: { definition: "gen simple", signals: [], examples: [] },
		MEDIUM: { definition: "gen medium", signals: [], examples: [] },
		COMPLEX: { definition: "gen complex", signals: [], examples: [] },
	};
	const tierDefaults = { prompt: "generated prompt", criteria: generated };
	// Edits left in the criteria editors from a switch to the decision step.
	const edited = { ...generated, SIMPLE: { definition: "hidden edit", signals: [], examples: [] } };

	test("hidden criteria edits are not sent; what was saved is kept", () => {
		const saved = { enabled: true, tier: { classifier: "llm" as const, criteria: { MEDIUM: { definition: "saved medium" } } } };
		const values = { ...DEFAULT_SUGGESTIONS_FORM_VALUES, enabled: true, tier_classifier: "llm" as const, tier_criteria: edited };
		expect(toSuggestionsPayload(values, saved, tierDefaults).tier?.criteria).toEqual({ MEDIUM: { definition: "saved medium" } });
		expect(toSuggestionsPayload(values, { enabled: true }, tierDefaults).tier?.criteria).toBeUndefined();
	});

	test("the decision step still sends the tiers edited", () => {
		const values = { ...DEFAULT_SUGGESTIONS_FORM_VALUES, enabled: true, tier_classifier: "decision" as const, tier_criteria: edited };
		expect(toSuggestionsPayload(values, { enabled: true }, tierDefaults).tier?.criteria).toEqual({
			SIMPLE: { definition: "hidden edit", signals: [], examples: [] },
		});
	});
});

describe("complexity config payload", () => {
	test("never sends suggestions, which only model routers make, while a router's payload keeps them", () => {
		const saved: AnalyzerConfig = {
			classifier: "semantic",
			keywords: { simple_keywords: ["simple"], medium_keywords: ["medium"], complex_keywords: ["complex"] },
			semantic: { provider: "openai", embedding_model: "text-embedding-3-small" },
			suggestions: { enabled: true, sample_rate: 0.1, lookback_days: 7, provider: "openai", model: "gpt-5" },
		};
		const values = toFormValues(saved);
		expect(toComplexityConfigPayload(values, saved)).not.toHaveProperty("suggestions");
		expect(toComplexityConfigPayload(values, saved).keywords).toEqual(toAnalyzerPayload(values, saved).keywords);
		expect(toAnalyzerPayload(values, saved).suggestions?.enabled).toBe(true);
	});
});

describe("a model router's classifier form", () => {
	const keywords = { simple_keywords: ["hi"], medium_keywords: ["summarize this"], complex_keywords: ["prove this"] };
	const base = { ...DEFAULT_FORM_VALUES, keywords };
	const embedding = { ...base.semantic, provider: "openai", embedding_model: "text-embedding-3-small" };
	const llmModel = { ...base.llm, provider: "openai", model: "gpt-4o-mini" };
	// The fields a submit of these values marks invalid.
	const invalid = (values: unknown) => {
		const parsed = analyzerConfigSchema.safeParse(values);
		return parsed.success ? [] : [...new Set(parsed.error.issues.map((issue) => issue.path.join(".")))].sort();
	};

	test("saves with phrases alone: a classifier not set up yet is not an error", () => {
		expect(invalid(base)).toEqual([]);
	});

	test("needs at least one phrase in each tier, and names the tier left empty", () => {
		expect(invalid({ ...base, keywords: { ...keywords, simple_keywords: [] } })).toEqual(["keywords.simple_keywords"]);
		expect(invalid({ ...base, keywords: { simple_keywords: [], medium_keywords: [], complex_keywords: [] } })).toEqual([
			"keywords.complex_keywords",
			"keywords.medium_keywords",
			"keywords.simple_keywords",
		]);
	});

	test("refuses the same phrase in two tiers, on the tier that repeats it, whatever its case or spacing", () => {
		expect(invalid({ ...base, keywords: { ...keywords, complex_keywords: ["prove this", " HI "] } })).toEqual([
			"keywords.complex_keywords",
		]);
		// Repeated inside one tier, the gateway keeps one; that is not an error.
		expect(invalid({ ...base, keywords: { ...keywords, simple_keywords: ["hi", "Hi "] } })).toEqual([]);
	});

	test("holds a phrase to the length the gateway accepts", () => {
		const phrase = (length: number) => ({ ...base, keywords: { ...keywords, medium_keywords: ["m".repeat(length)] } });
		expect(invalid(phrase(MAX_SEMANTIC_PHRASE_CHARACTERS))).toEqual([]);
		expect(invalid(phrase(MAX_SEMANTIC_PHRASE_CHARACTERS + 1))).toEqual(["keywords.medium_keywords"]);
	});

	test("an embedding provider and model are picked together or not at all", () => {
		expect(invalid({ ...base, semantic: { ...base.semantic, provider: "openai" } })).toEqual(["semantic.embedding_model"]);
		expect(invalid({ ...base, semantic: { ...base.semantic, embedding_model: "text-embedding-3-small" } })).toEqual(["semantic.provider"]);
		expect(invalid({ ...base, semantic: { ...base.semantic, provider: "openai", embedding_model: "   " } })).toEqual([
			"semantic.embedding_model",
		]);
		expect(invalid({ ...base, semantic: embedding })).toEqual([]);
	});

	test("session routing on the semantic classifier waits for its embedding model", () => {
		expect(invalid({ ...base, session: { enabled: true } })).toEqual(["session.enabled"]);
		expect(invalid({ ...base, session: { enabled: true }, semantic: embedding })).toEqual([]);
	});

	test("the similarity floor runs from 0 up to, but not including, 1", () => {
		const floor = (min_similarity: number) => invalid({ ...base, semantic: { ...embedding, min_similarity } });
		expect(floor(0)).toEqual([]);
		expect(floor(0.99)).toEqual([]);
		expect(floor(1)).toEqual(["semantic.min_similarity"]);
		expect(floor(-0.01)).toEqual(["semantic.min_similarity"]);
		// An emptied number field.
		expect(floor(Number.NaN)).toEqual(["semantic.min_similarity"]);
	});

	test("the embedding message history is a whole number within the gateway's range", () => {
		const history = (message_history_count: number) => invalid({ ...base, semantic: { ...embedding, message_history_count } });
		expect(history(1)).toEqual([]);
		expect(history(MAX_SEMANTIC_MESSAGE_HISTORY)).toEqual([]);
		expect(history(0)).toEqual(["semantic.message_history_count"]);
		expect(history(MAX_SEMANTIC_MESSAGE_HISTORY + 1)).toEqual(["semantic.message_history_count"]);
		expect(history(1.5)).toEqual(["semantic.message_history_count"]);
		expect(history(Number.NaN)).toEqual(["semantic.message_history_count"]);
	});

	test("the embedding timeout is a positive duration the gateway can hold", () => {
		const timeout = (value: string) => invalid({ ...base, semantic: { ...embedding, timeout: value } });
		expect(timeout("1ms")).toEqual([]);
		expect(timeout("0.5ms")).toEqual([]);
		expect(timeout(`${MAX_SEMANTIC_TIMEOUT_MS}ms`)).toEqual([]);
		expect(timeout("")).toEqual(["semantic.timeout"]);
		expect(timeout("0ms")).toEqual(["semantic.timeout"]);
		expect(timeout("-5ms")).toEqual(["semantic.timeout"]);
		expect(timeout("250")).toEqual(["semantic.timeout"]);
		expect(timeout("soon")).toEqual(["semantic.timeout"]);
		expect(timeout(`${MAX_SEMANTIC_TIMEOUT_MS + 1000}ms`)).toEqual(["semantic.timeout"]);
	});

	test("an llm fallback for the semantic classifier needs its provider and model", () => {
		const withFallback = { ...base, semantic: { ...embedding, fallback: "llm" as const } };
		expect(invalid(withFallback)).toEqual(["llm.model", "llm.provider"]);
		expect(invalid({ ...withFallback, llm: { ...base.llm, provider: "openai" } })).toEqual(["llm.model"]);
		expect(invalid({ ...withFallback, llm: llmModel })).toEqual([]);
	});

	test("an llm left half picked does not block the save while the llm is not in use", () => {
		// Picking the llm's provider clears its model, and the llm's fields are
		// only on screen while it is the classifier or the semantic fallback. Once
		// another classifier is chosen, an error on them has nowhere to show, and
		// the half-picked block is not sent anyway: toAnalyzerPayload drops it.
		const halfPicked = { ...base.llm, provider: "openai" };
		expect.soft(invalid({ ...base, classifier: "decision", llm: halfPicked }), "the decision model is the classifier").toEqual([]);
		expect
			.soft(invalid({ ...base, semantic: { ...embedding, fallback: "none" }, llm: halfPicked }), "semantic without a fallback")
			.toEqual([]);
	});

	test("the llm classifier's history, timeout and prompt stay within the gateway's bounds", () => {
		const llm = (changes: Partial<typeof llmModel>) => invalid({ ...base, classifier: "llm", llm: { ...llmModel, ...changes } });
		expect(llm({})).toEqual([]);
		expect(llm({ message_history_count: MAX_LLM_MESSAGE_HISTORY })).toEqual([]);
		expect(llm({ message_history_count: MAX_LLM_MESSAGE_HISTORY + 1 })).toEqual(["llm.message_history_count"]);
		expect(llm({ message_history_count: 0 })).toEqual(["llm.message_history_count"]);
		expect(llm({ message_history_count: 2.5 })).toEqual(["llm.message_history_count"]);
		expect(llm({ timeout: "0ms" })).toEqual(["llm.timeout"]);
		expect(llm({ timeout: "" })).toEqual(["llm.timeout"]);
		expect(llm({ prompt: "" }), "an empty prompt means the gateway's own").toEqual([]);
		expect(llm({ prompt: "p".repeat(MAX_LLM_PROMPT_CHARACTERS) })).toEqual([]);
		expect(llm({ prompt: "p".repeat(MAX_LLM_PROMPT_CHARACTERS + 1) })).toEqual(["llm.prompt"]);
	});

	test("the decision model accepts the ends of its history and confidence ranges", () => {
		const decision = (changes: Partial<typeof base.decision>) =>
			invalid({ ...base, classifier: "decision", decision: { ...base.decision, ...changes } });
		expect(decision({})).toEqual([]);
		expect(decision({ previous_message_count: 0 })).toEqual([]);
		expect(decision({ previous_message_count: MAX_DECISION_PREVIOUS_MESSAGE_COUNT })).toEqual([]);
		expect(decision({ min_confidence: 0 })).toEqual([]);
		expect(decision({ min_confidence: 1 })).toEqual([]);
		expect(decision({ previous_message_count: MAX_DECISION_PREVIOUS_MESSAGE_COUNT + 1, min_confidence: 1.01 })).toEqual([
			"decision.min_confidence",
			"decision.previous_message_count",
		]);
	});

	test("each classifier asks for its own model, and an unknown classifier is refused", () => {
		expect(invalid({ ...base, classifier: "decision" })).toEqual([]);
		expect(invalid({ ...base, classifier: "llm" })).toEqual(["llm.model", "llm.provider"]);
		expect(invalid({ ...base, classifier: "llm", llm: llmModel })).toEqual([]);
		expect(invalid({ ...base, classifier: "telepathy" })).toEqual(["classifier"]);
	});
});

describe("what a model router's classifier save sends", () => {
	const keywords = { simple_keywords: ["hi"], medium_keywords: ["summarize this"], complex_keywords: ["prove this"] };
	// What GET /routing/model-routers/defaults hands the creation wizard.
	const builtIn: AnalyzerConfig = { classifier: "semantic", keywords };
	// A router as the gateway returns it: identity, classifier, models and routing together.
	const savedRouter: ModelRouter = {
		id: "support-desk",
		name: "Support",
		description: "Customer tickets",
		classifier: "semantic",
		keywords,
		semantic: { provider: "openai", embedding_model: "text-embedding-3-small", fallback: "llm" },
		llm: { provider: "openai", model: "gpt-4o-mini" },
		targets: [{ tier: "SIMPLE", provider: "openai", model: "gpt-4o-mini", weight: 1 }],
		routing: { enabled: true, scope: "team", scope_id: "team-1", cel_expression: "model == 'gpt-4o'", priority: 3 },
	};

	test("a new router's body is its name, description and classifier: no id, no models, no routing", () => {
		// The wizard saves nothing until its last step, then sends the classifier
		// draft with the name and description; models and routing are set on the
		// router's own page afterwards.
		const payload = toAnalyzerPayload(toFormValues(builtIn), undefined);
		expect(Object.keys(payload).sort()).toEqual(["classifier", "keywords"]);
		const body = { ...payload, name: "Support", description: "" };
		expect(Object.keys(body).sort()).toEqual(["classifier", "description", "keywords", "name"]);
	});

	test("a new router carries the classifier that was set up in the wizard, and only that one", () => {
		const values = {
			...toFormValues(builtIn),
			classifier: "llm" as const,
			llm: { ...DEFAULT_FORM_VALUES.llm, provider: "openai", model: "gpt-4o-mini" },
		};
		const payload = toAnalyzerPayload(values, undefined);
		expect(Object.keys(payload).sort()).toEqual(["classifier", "keywords", "llm"]);
		expect(payload.llm).toMatchObject({ provider: "openai", model: "gpt-4o-mini" });
	});

	test("a new router has nothing saved to fall back on, so a classifier left blank is left out", () => {
		const payload = toAnalyzerPayload({ ...toFormValues(builtIn), classifier: "decision" as const }, undefined);
		expect(payload).not.toHaveProperty("semantic");
		expect(payload).not.toHaveProperty("llm");
		expect(payload).not.toHaveProperty("suggestions");
		expect(payload).not.toHaveProperty("session");
		expect(payload.decision).toMatchObject(DEFAULT_MODEL);
	});

	test("the form never holds a saved router's name, models or routing", () => {
		expect(Object.keys(toFormValues(savedRouter)).sort()).toEqual([
			"classifier",
			"decision",
			"keywords",
			"llm",
			"semantic",
			"session",
			"suggestions",
		]);
	});

	test("a classifier save does not write back the router's name, models or routing", () => {
		// Each is saved on its own endpoint; a rename, a models save or the on/off
		// switch made since this page loaded must not be undone by this one.
		const payload = toAnalyzerPayload(toFormValues(savedRouter), savedRouter);
		for (const field of ["id", "name", "description", "targets", "routing"]) {
			expect(payload, field).not.toHaveProperty(field);
		}
		expect(payload.semantic).toMatchObject({ provider: "openai", embedding_model: "text-embedding-3-small", fallback: "llm" });
		expect(payload.llm).toMatchObject({ provider: "openai", model: "gpt-4o-mini" });
	});

	test("a save from the page keeps the stored embedding and llm setup the form holds blank", () => {
		// The endpoint replaces the whole classifier config, so a block the form
		// could not fill must go back as it is stored, not be dropped.
		const values = { ...toFormValues(savedRouter), semantic: DEFAULT_FORM_VALUES.semantic, llm: DEFAULT_FORM_VALUES.llm };
		const payload = toAnalyzerPayload(values, savedRouter);
		expect(payload.semantic).toEqual(savedRouter.semantic);
		expect(payload.llm).toEqual(savedRouter.llm);
	});

	test("an llm block left half picked is not sent: the stored one is kept, and a new router sends none", () => {
		const values = { ...toFormValues(savedRouter), llm: { ...DEFAULT_FORM_VALUES.llm, provider: "anthropic" } };
		expect(toAnalyzerPayload(values, savedRouter).llm).toEqual(savedRouter.llm);
		expect(toAnalyzerPayload(values, undefined)).not.toHaveProperty("llm");
	});

	test("changing the classifier of a saved router keeps its other classifiers' settings for a switch back", () => {
		const values = { ...toFormValues(savedRouter), classifier: "decision" as const };
		const payload = toAnalyzerPayload(values, savedRouter);
		expect(payload.classifier).toBe("decision");
		expect(payload.semantic).toMatchObject({ provider: "openai", embedding_model: "text-embedding-3-small" });
		expect(payload.llm).toMatchObject({ provider: "openai", model: "gpt-4o-mini" });
		expect(payload.decision).toMatchObject(DEFAULT_MODEL);
	});

	test("session routing is sent only while it is on", () => {
		const values = toFormValues(savedRouter);
		expect(toAnalyzerPayload(values, savedRouter)).not.toHaveProperty("session");
		expect(toAnalyzerPayload({ ...values, session: { enabled: true } }, savedRouter).session).toEqual({ enabled: true });
	});
});