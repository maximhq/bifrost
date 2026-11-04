import i18n from "@/lib/i18n";
import {
	AnalyzerConfig,
	COMPLEXITY_TIER_VALUES,
	DEFAULT_DECISION_CONFIG,
	DecisionGuidanceDefaults,
	DecisionTier,
	MAX_DECISION_CRITERIA_ITEM_CHARACTERS,
	MAX_DECISION_CRITERIA_ITEMS,
	MAX_DECISION_DEFINITION_CHARACTERS,
	DEFAULT_LLM_CONFIG,
	DEFAULT_SEMANTIC_CONFIG,
	DEFAULT_SUGGESTION_AT,
	DEFAULT_SUGGESTION_EVERY,
	DEFAULT_SUGGESTION_LOOKBACK_DAYS,
	DEFAULT_SUGGESTION_MAX_CLUSTERED,
	DEFAULT_SUGGESTION_MIN_CLUSTER,
	DEFAULT_SUGGESTION_SAMPLE_RATE,
	MAX_SUGGESTION_INSTRUCTIONS_CHARACTERS,
	MAX_SUGGESTION_LOOKBACK_DAYS,
	MAX_SUGGESTION_MAX_CLUSTERED,
	MAX_SUGGESTION_MIN_CLUSTER,
	MIN_SUGGESTION_MAX_CLUSTERED,
	SUGGESTION_EVERY_OPTIONS,
	SUGGESTION_WEEKDAYS,
	MAX_DECISION_PREVIOUS_MESSAGE_COUNT,
	KeywordListKey,
	MAX_LLM_PROMPT_CHARACTERS,
	MAX_LLM_MESSAGE_HISTORY,
	MAX_SEMANTIC_MESSAGE_HISTORY,
	MAX_SEMANTIC_PHRASE_CHARACTERS,
	MAX_SEMANTIC_PHRASES,
	MAX_SEMANTIC_TIMEOUT_MS,
	MIN_LLM_MESSAGE_HISTORY,
	MIN_SEMANTIC_MESSAGE_HISTORY,
	parseLLMTimeoutMs,
	parseSemanticTimeoutMs,
} from "@/lib/types/complexityRouter";
import { ModelProvider } from "@/lib/types/config";
import { DBKey } from "@/lib/types/governance";
import { z } from "zod";

const complexityT = i18n.getFixedT(null, "models", "routing.complexityUi");

function tierLabel(key: KeywordListKey) {
	if (key === "simple_keywords") return complexityT("tierSimple");
	if (key === "medium_keywords") return complexityT("tierMedium");
	return complexityT("tierComplex");
}

// Form-owned duration values are always a single unit (the controls append
// "ms"), so a plain positive-duration check is enough.
const positiveDurationPattern = /^[0-9]*\.?[0-9]+(ns|us|µs|ms|s|m|h)$/;

export function countCanonicalSemanticPhrases(keywords: Record<KeywordListKey, string[]>) {
	const count = (phrases: string[]) => new Set(phrases.map((phrase) => phrase.trim().toLowerCase()).filter(Boolean)).size;
	const simple = count(keywords.simple_keywords);
	const medium = count(keywords.medium_keywords);
	const complex = count(keywords.complex_keywords);
	return { simple, medium, complex, total: simple + medium + complex };
}

export function isPositiveDurationString(value: string | undefined): boolean {
	if (!value) return false;
	const trimmed = value.trim();
	if (!positiveDurationPattern.test(trimmed) || Number.parseFloat(trimmed) <= 0) return false;
	// Past the int64-nanosecond ceiling the server cannot parse the duration at
	// all, so it is rejected here as a value rather than sent to fail as a 400.
	return parseSemanticTimeoutMs(trimmed) <= MAX_SEMANTIC_TIMEOUT_MS;
}

const semanticSchema = z.object({
	provider: z.string(),
	embedding_model: z.string(),
	// The control edits milliseconds but the value stays a Go duration, so a
	// non-positive or malformed entry is caught here rather than snapped back to
	// the default while the operator is still typing.
	timeout: z
		.string()
		.min(1, { error: () => complexityT("enterEmbeddingTimeout") })
		.refine((value) => isPositiveDurationString(value), { error: () => complexityT("timeoutGtZeroMax", { max: MAX_SEMANTIC_TIMEOUT_MS }) })
		.optional(),
	min_similarity: z
		.number({ error: () => complexityT("similarityNumber") })
		.min(0, { error: () => complexityT("mustBeZeroOrGreater") })
		.lt(1, { error: () => complexityT("mustBeLessThanOne") }),
	message_history_count: z
		.number({
			error: () => complexityT("historyRange", { min: MIN_SEMANTIC_MESSAGE_HISTORY, max: MAX_SEMANTIC_MESSAGE_HISTORY }),
		})
		.int({ error: () => complexityT("mustBeWholeNumber") })
		.min(MIN_SEMANTIC_MESSAGE_HISTORY, { error: () => complexityT("mustBeAtLeast", { min: MIN_SEMANTIC_MESSAGE_HISTORY }) })
		.max(MAX_SEMANTIC_MESSAGE_HISTORY, { error: () => complexityT("mustBeAtMost", { max: MAX_SEMANTIC_MESSAGE_HISTORY }) }),
	count_toward_budgets: z.boolean().optional(),
	vector_store: z.enum(["embedded", "vector_store"]).optional(),
	fallback: z.enum(["none", "llm", "decision"]),
});

// The editors always hold what the gateway will send, so an emptied field is
// rejected rather than saved: it would reload as the shipped default, silently
// undoing the deletion. Reset to default is the way back.
const decisionCriteriaListSchema = (kind: "signal" | "example") => {
	const signal = kind === "signal";
	return z
		.array(
			z.string().max(MAX_DECISION_CRITERIA_ITEM_CHARACTERS, {
				error: () =>
					signal
						? complexityT("criteriaSignalItemMax", { max: MAX_DECISION_CRITERIA_ITEM_CHARACTERS })
						: complexityT("criteriaExampleItemMax", { max: MAX_DECISION_CRITERIA_ITEM_CHARACTERS }),
			}),
		)
		.min(1, { error: () => (signal ? complexityT("criteriaSignalMin") : complexityT("criteriaExampleMin")) })
		.max(MAX_DECISION_CRITERIA_ITEMS, {
			error: () =>
				signal
					? complexityT("criteriaSignalMax", { count: MAX_DECISION_CRITERIA_ITEMS })
					: complexityT("criteriaExampleMax", { count: MAX_DECISION_CRITERIA_ITEMS }),
		});
};

const decisionTierCriteriaSchema = z.object({
	definition: z
		.string()
		.trim()
		.min(1, { error: () => complexityT("enterDefinition") })
		.max(MAX_DECISION_DEFINITION_CHARACTERS, { error: () => complexityT("promptMaxChars", { max: MAX_DECISION_DEFINITION_CHARACTERS }) }),
	signals: decisionCriteriaListSchema("signal"),
	examples: decisionCriteriaListSchema("example"),
});

// The unvalidated shape the form always holds; decisionGuidanceSchema checks it
// only once the editors are seeded.
const decisionTierFormShape = z.object({
	definition: z.string(),
	signals: z.array(z.string()),
	examples: z.array(z.string()),
});

const decisionGuidanceSchema = z.object({
	criteria: z.object({
		SIMPLE: decisionTierCriteriaSchema,
		MEDIUM: decisionTierCriteriaSchema,
		COMPLEX: decisionTierCriteriaSchema,
	}),
});

const decisionSchema = z.object({
	provider: z
		.string()
		.trim()
		.min(1, { error: () => complexityT("selectDecisionProvider") }),
	model: z
		.string()
		.trim()
		.min(1, { error: () => complexityT("selectDecisionModel") }),
	previous_message_count: z
		.number()
		.int({ error: () => complexityT("mustBeWholeNumber") })
		.min(0, { error: () => complexityT("mustBeAtLeast", { min: 0 }) })
		.max(MAX_DECISION_PREVIOUS_MESSAGE_COUNT, {
			error: () => complexityT("mustBeAtMost", { max: MAX_DECISION_PREVIOUS_MESSAGE_COUNT }),
		}),
	timeout: z
		.string()
		.min(1, { error: () => complexityT("enterDecisionTimeout") })
		.refine((value) => isPositiveDurationString(value), { error: () => complexityT("timeoutGtZero") }),
	min_confidence: z
		.number({ error: () => complexityT("similarityNumber") })
		.min(0, { error: () => complexityT("mustBeZeroOrGreater") })
		.max(1, { error: () => complexityT("mustBeAtMost", { max: 1 }) }),
});

const llmSchema = z.object({
	provider: z.string(),
	model: z.string(),
	// Same millisecond-edited Go duration treatment as the semantic timeout.
	timeout: z
		.string()
		.min(1, { error: () => complexityT("enterClassificationTimeout") })
		.refine((value) => isPositiveDurationString(value), { error: () => complexityT("timeoutGtZero") })
		.optional(),
	prompt: z.string().max(MAX_LLM_PROMPT_CHARACTERS, { error: () => complexityT("promptMaxChars", { max: MAX_LLM_PROMPT_CHARACTERS }) }),
	message_history_count: z
		.number({
			error: () => complexityT("historyRange", { min: MIN_LLM_MESSAGE_HISTORY, max: MAX_LLM_MESSAGE_HISTORY }),
		})
		.int({ error: () => complexityT("mustBeWholeNumber") })
		.min(MIN_LLM_MESSAGE_HISTORY, { error: () => complexityT("mustBeAtLeast", { min: MIN_LLM_MESSAGE_HISTORY }) })
		.max(MAX_LLM_MESSAGE_HISTORY, { error: () => complexityT("mustBeAtMost", { max: MAX_LLM_MESSAGE_HISTORY }) }),
	count_toward_budgets: z.boolean().optional(),
});

// suggestionsSchema holds the phrase-suggestion settings. The sample rate is
// edited as a percentage; toAnalyzerPayload sends it as the fraction the
// gateway stores.
const suggestionsSchema = z
	.object({
		enabled: z.boolean(),
		sample_percent: z
			.number({ error: () => complexityT("suggestions.samplePercentRange") })
			.gt(0, { error: () => complexityT("suggestions.samplePercentRange") })
			.max(100, { error: () => complexityT("suggestions.samplePercentRange") }),
		lookback_days: z
			.number({ error: () => complexityT("mustBeWholeNumber") })
			.int({ error: () => complexityT("mustBeWholeNumber") })
			.min(1, { error: () => complexityT("mustBeAtLeast", { min: 1 }) })
			.max(MAX_SUGGESTION_LOOKBACK_DAYS, { error: () => complexityT("mustBeAtMost", { max: MAX_SUGGESTION_LOOKBACK_DAYS }) }),
		every: z.enum(SUGGESTION_EVERY_OPTIONS),
		at: z.string().regex(/^([01][0-9]|2[0-3]):[0-5][0-9]$/, { error: () => complexityT("suggestions.atFormat") }),
		weekday: z.enum(SUGGESTION_WEEKDAYS),
		timezone: z
			.string()
			.trim()
			.min(1, { error: () => complexityT("suggestions.timezoneRequired") }),
		max_clustered_requests: z
			.number({ error: () => complexityT("mustBeWholeNumber") })
			.int({ error: () => complexityT("mustBeWholeNumber") })
			.min(MIN_SUGGESTION_MAX_CLUSTERED, { error: () => complexityT("mustBeAtLeast", { min: MIN_SUGGESTION_MAX_CLUSTERED }) })
			.max(MAX_SUGGESTION_MAX_CLUSTERED, { error: () => complexityT("mustBeAtMost", { max: MAX_SUGGESTION_MAX_CLUSTERED }) }),
		min_cluster_requests: z
			.number({ error: () => complexityT("mustBeWholeNumber") })
			.int({ error: () => complexityT("mustBeWholeNumber") })
			.min(2, { error: () => complexityT("mustBeAtLeast", { min: 2 }) })
			.max(MAX_SUGGESTION_MIN_CLUSTER, { error: () => complexityT("mustBeAtMost", { max: MAX_SUGGESTION_MIN_CLUSTER }) }),
		provider: z.string(),
		model: z.string(),
		instructions: z.string().max(MAX_SUGGESTION_INSTRUCTIONS_CHARACTERS, {
			error: () => complexityT("promptMaxChars", { max: MAX_SUGGESTION_INSTRUCTIONS_CHARACTERS }),
		}),
	})
	.superRefine((data, ctx) => {
		if (data.provider.trim() !== "" && data.model.trim() === "") {
			ctx.addIssue({ code: "custom", message: complexityT("suggestions.modelRequired"), path: ["model"] });
		}
		if (data.provider.trim() === "" && data.model.trim() !== "") {
			ctx.addIssue({ code: "custom", message: complexityT("suggestions.providerRequired"), path: ["provider"] });
		}
	});

// isTimezone reports whether the browser knows an IANA zone, which is the
// same database the gateway resolves it with.
export function isTimezone(zone: string): boolean {
	try {
		new Intl.DateTimeFormat("en-US", { timeZone: zone });
		return true;
	} catch {
		return false;
	}
}

// suggestionScheduleUnit names the unit of an interval: hours run from
// midnight, days and weeks at a time of day, weeks on a weekday.
export function suggestionScheduleUnit(every: string): "h" | "d" | "w" {
	const unit = every.slice(-1);
	return unit === "h" || unit === "w" ? unit : "d";
}

// usesDecision reports whether the Decision block is live: the primary classifier or the
// semantic fallback. Its controls are hidden and its values are not saved otherwise.
function usesDecision(values: { classifier: string; semantic: { fallback: string } }): boolean {
	return values.classifier === "decision" || values.semantic.fallback === "decision";
}

export const analyzerConfigSchema = z
	.object({
		classifier: z.enum(["semantic", "decision"]),
		keywords: z.object({
			simple_keywords: z.array(z.string()).min(1, { error: () => complexityT("phrasesEmpty", { tier: complexityT("tierSimple") }) }),
			medium_keywords: z.array(z.string()).min(1, { error: () => complexityT("phrasesEmpty", { tier: complexityT("tierMedium") }) }),
			complex_keywords: z.array(z.string()).min(1, { error: () => complexityT("phrasesEmpty", { tier: complexityT("tierComplex") }) }),
		}),
		semantic: semanticSchema,
		// Only shape-checked here: the Decision controls are hidden unless Decision is the
		// classifier or the fallback, so decisionSchema runs in superRefine under that
		// condition rather than letting an invisible error block Save. An emptied
		// number input registers as NaN, which must pass the shape check too.
		decision: z.object({
			provider: z.string(),
			model: z.string(),
			previous_message_count: z.number().or(z.nan()),
			timeout: z.string(),
			min_confidence: z.number().or(z.nan()),
			criteria: z.object({
				SIMPLE: decisionTierFormShape,
				MEDIUM: decisionTierFormShape,
				COMPLEX: decisionTierFormShape,
			}),
		}),
		llm: llmSchema,
		session: z.object({ enabled: z.boolean() }),
		suggestions: suggestionsSchema,
	})
	.superRefine((data, ctx) => {
		// A blank provider and model means the classifier simply is not configured
		// yet, which is a legal state: phrase edits still save. Half-filled is not,
		// because it cannot be turned into a working classifier.
		const hasProvider = data.semantic.provider.trim() !== "";
		const hasModel = data.semantic.embedding_model.trim() !== "";
		if (hasProvider || hasModel) {
			if (!hasProvider) {
				ctx.addIssue({
					code: "custom",
					message: complexityT("selectEmbeddingProvider"),
					path: ["semantic", "provider"],
				});
			}
			if (!hasModel) {
				ctx.addIssue({
					code: "custom",
					message: complexityT("selectEmbeddingModel"),
					path: ["semantic", "embedding_model"],
				});
			}
		}
		if (data.suggestions.timezone.trim() && !isTimezone(data.suggestions.timezone.trim())) {
			ctx.addIssue({ code: "custom", message: complexityT("suggestions.timezoneUnknown"), path: ["suggestions", "timezone"] });
		}
		if (data.session.enabled && data.classifier === "semantic" && (!hasProvider || !hasModel)) {
			ctx.addIssue({
				code: "custom",
				message: complexityT("sessionNeedsClassifier"),
				path: ["session", "enabled"],
			});
		}

		if (usesDecision(data)) {
			const decision = decisionSchema.safeParse(data.decision);
			const issues = decision.success ? [] : [...decision.error.issues];
			// Guidance that was never seeded (the status endpoint has not supplied
			// defaults) is hidden and saves as "use the defaults", so it is only
			// validated once the editors hold something.
			if (!isDecisionGuidanceEmpty(data.decision)) {
				const guidance = decisionGuidanceSchema.safeParse(data.decision);
				if (!guidance.success) issues.push(...guidance.error.issues);
			}
			for (const issue of issues) {
				ctx.addIssue({ code: "custom", message: issue.message, path: ["decision", ...issue.path] });
			}
		}

		// The llm block follows the same half-filled rule, with one addition:
		// switching the semantic fallback to "llm" makes the block mandatory,
		// because the server rejects that fallback without one.
		const hasLLMProvider = data.llm.provider.trim() !== "";
		const hasLLMModel = data.llm.model.trim() !== "";
		if (hasLLMProvider || hasLLMModel || data.semantic.fallback === "llm") {
			if (!hasLLMProvider) {
				ctx.addIssue({
					code: "custom",
					message: complexityT("selectFallbackProvider"),
					path: ["llm", "provider"],
				});
			}
			if (!hasLLMModel) {
				ctx.addIssue({
					code: "custom",
					message: complexityT("selectFallbackModel"),
					path: ["llm", "model"],
				});
			}
		}

		// Mirrors validateComplexitySemanticPhrases so invalid input fails in the
		// form instead of as an opaque 400.
		const lists: Array<{ key: KeywordListKey; label: string }> = [
			{ key: "simple_keywords", label: tierLabel("simple_keywords") },
			{ key: "medium_keywords", label: tierLabel("medium_keywords") },
			{ key: "complex_keywords", label: tierLabel("complex_keywords") },
		];

		const seen = new Map<string, string>();
		for (const { key, label } of lists) {
			for (const phrase of data.keywords[key]) {
				if (phrase.length > MAX_SEMANTIC_PHRASE_CHARACTERS) {
					ctx.addIssue({
						code: "custom",
						message: complexityT("phraseTooLong", { tier: label, max: MAX_SEMANTIC_PHRASE_CHARACTERS }),
						path: ["keywords", key],
					});
					break;
				}
				const normalized = phrase.trim().toLowerCase();
				const firstTier = seen.get(normalized);
				if (firstTier && firstTier !== label) {
					ctx.addIssue({
						code: "custom",
						message: complexityT("phraseDuplicate", { phrase, tier: firstTier }),
						path: ["keywords", key],
					});
				} else if (!firstTier) {
					seen.set(normalized, label);
				}
			}
		}

		if (hasProvider && hasModel) {
			const counts = countCanonicalSemanticPhrases(data.keywords);
			if (counts.total > MAX_SEMANTIC_PHRASES) {
				ctx.addIssue({
					code: "custom",
					message: complexityT("phraseCapExceeded", {
						total: counts.total,
						simple: counts.simple,
						medium: counts.medium,
						complex: counts.complex,
						max: MAX_SEMANTIC_PHRASES,
					}),
					path: ["keywords"],
				});
			}
		}
	});

// The form is stricter than the wire type: the API omits semantic fields left at
// their zero value (Go `omitempty`), but every control here is controlled and
// needs a concrete value, so the schema's inferred type is the source of truth.
export type AnalyzerFormValues = z.infer<typeof analyzerConfigSchema>;
export type SemanticFormValues = AnalyzerFormValues["semantic"];
export type LLMFormValues = AnalyzerFormValues["llm"];

export const DEFAULT_LLM_FORM_VALUES: LLMFormValues = {
	provider: DEFAULT_LLM_CONFIG.provider,
	model: DEFAULT_LLM_CONFIG.model,
	timeout: DEFAULT_LLM_CONFIG.timeout,
	prompt: DEFAULT_LLM_CONFIG.prompt ?? "",
	message_history_count: DEFAULT_LLM_CONFIG.message_history_count ?? MIN_LLM_MESSAGE_HISTORY,
	count_toward_budgets: DEFAULT_LLM_CONFIG.count_toward_budgets ?? false,
};

export const DEFAULT_SEMANTIC_FORM_VALUES: SemanticFormValues = {
	...DEFAULT_SEMANTIC_CONFIG,
	min_similarity: DEFAULT_SEMANTIC_CONFIG.min_similarity ?? 0,
	message_history_count: DEFAULT_SEMANTIC_CONFIG.message_history_count ?? MIN_SEMANTIC_MESSAGE_HISTORY,
	vector_store: "embedded",
	fallback: DEFAULT_SEMANTIC_CONFIG.fallback ?? "none",
};

export type SuggestionsFormValues = AnalyzerFormValues["suggestions"];

export const DEFAULT_SUGGESTIONS_FORM_VALUES: SuggestionsFormValues = {
	enabled: false,
	sample_percent: DEFAULT_SUGGESTION_SAMPLE_RATE * 100,
	lookback_days: DEFAULT_SUGGESTION_LOOKBACK_DAYS,
	every: DEFAULT_SUGGESTION_EVERY,
	at: DEFAULT_SUGGESTION_AT,
	weekday: "monday",
	timezone: "UTC",
	max_clustered_requests: DEFAULT_SUGGESTION_MAX_CLUSTERED,
	min_cluster_requests: DEFAULT_SUGGESTION_MIN_CLUSTER,
	provider: "",
	model: "",
	instructions: "",
};

// suggestionsFormValues reads a saved block into the form, defaults filled in.
export function suggestionsFormValues(saved?: AnalyzerConfig["suggestions"]): SuggestionsFormValues {
	if (!saved) return { ...DEFAULT_SUGGESTIONS_FORM_VALUES };
	const every = (SUGGESTION_EVERY_OPTIONS as readonly string[]).includes(saved.schedule?.every ?? "")
		? (saved.schedule?.every as SuggestionsFormValues["every"])
		: DEFAULT_SUGGESTION_EVERY;
	return {
		enabled: saved.enabled,
		// Rounded so a stored 0.1 reads back as 10, not 10.000000000000002.
		sample_percent: Math.round((saved.sample_rate ?? DEFAULT_SUGGESTION_SAMPLE_RATE) * 100 * 1000) / 1000,
		lookback_days: saved.lookback_days ?? DEFAULT_SUGGESTION_LOOKBACK_DAYS,
		every,
		at: saved.schedule?.at ?? DEFAULT_SUGGESTION_AT,
		weekday: saved.schedule?.weekday ?? "monday",
		timezone: saved.schedule?.timezone ?? "UTC",
		max_clustered_requests: saved.max_clustered_requests ?? DEFAULT_SUGGESTION_MAX_CLUSTERED,
		min_cluster_requests: saved.min_cluster_requests ?? DEFAULT_SUGGESTION_MIN_CLUSTER,
		provider: saved.provider ?? "",
		model: saved.model ?? "",
		instructions: saved.instructions ?? "",
	};
}

// toSuggestionsPayload sends only the settings the interval uses: the gateway
// refuses a time of day on an hourly schedule and a weekday on a daily one.
export function toSuggestionsPayload(values: SuggestionsFormValues): NonNullable<AnalyzerConfig["suggestions"]> {
	const unit = suggestionScheduleUnit(values.every);
	const provider = values.provider.trim();
	const model = values.model.trim();
	return {
		enabled: values.enabled,
		sample_rate: Math.round(values.sample_percent * 1000) / 100000,
		lookback_days: values.lookback_days,
		schedule: {
			every: values.every,
			...(unit !== "h" ? { at: values.at } : {}),
			...(unit === "w" ? { weekday: values.weekday } : {}),
			timezone: values.timezone.trim(),
		},
		max_clustered_requests: values.max_clustered_requests,
		min_cluster_requests: values.min_cluster_requests,
		...(provider && model ? { provider, model } : {}),
		...(values.instructions.trim() ? { instructions: values.instructions } : {}),
	};
}

export const DEFAULT_FORM_VALUES: AnalyzerFormValues = {
	keywords: {
		simple_keywords: [],
		medium_keywords: [],
		complex_keywords: [],
	},
	classifier: "semantic",
	semantic: DEFAULT_SEMANTIC_FORM_VALUES,
	decision: { ...DEFAULT_DECISION_CONFIG, ...decisionGuidanceFormValues() },
	llm: DEFAULT_LLM_FORM_VALUES,
	session: { enabled: false },
	suggestions: DEFAULT_SUGGESTIONS_FORM_VALUES,
};

export type DecisionGuidanceFormValues = Pick<AnalyzerFormValues["decision"], "criteria">;

// isDecisionTierEmpty reports a tier with no definition and no list entries.
function isDecisionTierEmpty({ definition, signals, examples }: DecisionGuidanceFormValues["criteria"][DecisionTier]): boolean {
	return definition.trim() === "" && signals.length === 0 && examples.length === 0;
}

// isDecisionGuidanceEmpty reports guidance the form never seeded: no tier content
// anywhere.
export function isDecisionGuidanceEmpty(decision: DecisionGuidanceFormValues): boolean {
	return COMPLEXITY_TIER_VALUES.every((tier) => isDecisionTierEmpty(decision.criteria[tier]));
}

// decisionGuidanceFormValues fills the Decision guidance editors: each saved override
// wins, and anything unset shows the shipped default so the editor always
// displays what the gateway will send. Without defaults (status not loaded)
// unset fields stay empty, which saves as "use the default".
export function decisionGuidanceFormValues(
	saved?: AnalyzerConfig["decision"],
	defaults?: DecisionGuidanceDefaults,
): DecisionGuidanceFormValues {
	const tier = (name: DecisionTier) => {
		const override = saved?.criteria?.[name];
		const shipped = defaults?.criteria[name];
		return {
			definition: override?.definition || shipped?.definition || "",
			signals: override?.signals?.length ? override.signals : (shipped?.signals ?? []),
			examples: override?.examples?.length ? override.examples : (shipped?.examples ?? []),
		};
	};
	return {
		criteria: {
			SIMPLE: tier("SIMPLE"),
			MEDIUM: tier("MEDIUM"),
			COMPLEX: tier("COMPLEX"),
		},
	};
}

// decisionCriteriaFromDefaults copies the shipped per-tier criteria into form
// values, so restoring them never shares arrays with the status response.
export function decisionCriteriaFromDefaults(defaults: DecisionGuidanceDefaults): DecisionGuidanceFormValues["criteria"] {
	const tier = (name: DecisionTier) => ({
		definition: defaults.criteria[name].definition,
		signals: [...defaults.criteria[name].signals],
		examples: [...defaults.criteria[name].examples],
	});
	return { SIMPLE: tier("SIMPLE"), MEDIUM: tier("MEDIUM"), COMPLEX: tier("COMPLEX") };
}

// isDecisionGuidanceDefault reports whether every tier's definition, signals, and
// examples equal the shipped criteria, in order.
export function isDecisionGuidanceDefault(criteria: DecisionGuidanceFormValues["criteria"], defaults: DecisionGuidanceDefaults): boolean {
	const sameList = (a: string[], b: string[]) => a.length === b.length && a.every((value, index) => value === b[index]);
	return COMPLEXITY_TIER_VALUES.every((tier) => {
		const shipped = defaults.criteria[tier];
		const current = criteria[tier];
		return (
			current.definition === shipped.definition &&
			sameList(current.signals, shipped.signals) &&
			sameList(current.examples, shipped.examples)
		);
	});
}

// Fills in the fields the API omitted so the semantic controls stay controlled.
export function toFormValues(config: AnalyzerConfig, decisionDefaults?: DecisionGuidanceDefaults): AnalyzerFormValues {
	const saved = config.semantic;
	const savedLLM = config.llm;
	const classifier = config.classifier?.trim().toLowerCase();
	return {
		classifier: classifier === "decision" ? "decision" : "semantic",
		keywords: config.keywords,
		decision: {
			// A block saved without a model means the gateway default.
			...(config.decision?.provider && config.decision?.model
				? { provider: config.decision.provider, model: config.decision.model }
				: { provider: DEFAULT_DECISION_CONFIG.provider, model: DEFAULT_DECISION_CONFIG.model }),
			previous_message_count: config.decision?.previous_message_count ?? DEFAULT_DECISION_CONFIG.previous_message_count,
			timeout: config.decision?.timeout ?? DEFAULT_DECISION_CONFIG.timeout,
			min_confidence: config.decision?.min_confidence ?? DEFAULT_DECISION_CONFIG.min_confidence,
			...decisionGuidanceFormValues(config.decision, decisionDefaults),
		},
		session: config.session ?? { enabled: false },
		suggestions: suggestionsFormValues(config.suggestions),
		llm: savedLLM
			? {
					...DEFAULT_LLM_FORM_VALUES,
					...savedLLM,
					timeout: savedLLM.timeout ?? DEFAULT_LLM_FORM_VALUES.timeout,
					prompt: savedLLM.prompt ?? "",
					message_history_count: savedLLM.message_history_count ?? MIN_LLM_MESSAGE_HISTORY,
					count_toward_budgets: savedLLM.count_toward_budgets ?? false,
				}
			: DEFAULT_LLM_FORM_VALUES,
		semantic: saved
			? {
					...DEFAULT_SEMANTIC_FORM_VALUES,
					...saved,
					min_similarity: saved.min_similarity ?? 0,
					message_history_count: saved.message_history_count ?? MIN_SEMANTIC_MESSAGE_HISTORY,
					vector_store: saved.vector_store ?? DEFAULT_SEMANTIC_FORM_VALUES.vector_store,
					fallback: saved.fallback ?? "none",
				}
			: DEFAULT_SEMANTIC_FORM_VALUES,
	};
}

// Builds the replacement payload without writing a disabled session block.
// Session is additive to the complexity API, and nil already means disabled;
// omitting it keeps ordinary semantic edits compatible with gateways that
// predate session-aware routing. Once enabled, the block is sent explicitly.
export function toAnalyzerPayload(values: AnalyzerFormValues, saved?: AnalyzerConfig): AnalyzerConfig {
	const semantic = values.semantic.provider && values.semantic.embedding_model ? values.semantic : (saved?.semantic ?? undefined);
	const llm = values.llm.provider && values.llm.model ? values.llm : (saved?.llm ?? undefined);

	return {
		classifier: values.classifier,
		keywords: values.keywords,
		// Unused Decision values are not validated, so keep the saved block instead of
		// sending hidden, possibly invalid edits.
		...(usesDecision(values) ? { decision: toDecisionPayload(values.decision) } : saved?.decision ? { decision: saved.decision } : {}),
		...(values.session.enabled ? { session: values.session } : {}),
		// Sent once suggestions have ever been turned on, so turning them off
		// keeps their settings; never sent for a router that has not used them,
		// so an ordinary save stays readable by gateways that predate them.
		...(values.suggestions.enabled || saved?.suggestions ? { suggestions: toSuggestionsPayload(values.suggestions) } : {}),
		...(semantic ? { semantic } : {}),
		...(llm ? { llm } : {}),
	};
}

// toDecisionPayload sends the editors' contents. The gateway drops anything equal
// to its shipped default, so saving untouched guidance stores no override and
// later default updates still apply. Empty fields are omitted only for the
// window before the status endpoint has supplied defaults to seed them.
function toDecisionPayload(decision: AnalyzerFormValues["decision"]): NonNullable<AnalyzerConfig["decision"]> {
	const criteria: NonNullable<NonNullable<AnalyzerConfig["decision"]>["criteria"]> = {};
	for (const tier of COMPLEXITY_TIER_VALUES) {
		const { definition, signals, examples } = decision.criteria[tier];
		if (isDecisionTierEmpty(decision.criteria[tier])) continue;
		criteria[tier] = {
			...(definition.trim() ? { definition } : {}),
			...(signals.length ? { signals } : {}),
			...(examples.length ? { examples } : {}),
		};
	}
	return {
		provider: decision.provider.trim(),
		model: decision.model.trim(),
		previous_message_count: decision.previous_message_count,
		timeout: decision.timeout,
		...(decision.min_confidence > 0 ? { min_confidence: decision.min_confidence } : {}),
		...(Object.keys(criteria).length ? { criteria } : {}),
	};
}

// The timeout control edits milliseconds while the form value stays a Go
// duration. A value this control wrote round-trips digit for digit, including a
// "0" the operator is midway through typing, which the schema rejects rather
// than the field silently rewriting. Anything else — a saved "1s", a blank —
// falls back to the parsed reading.
export function semanticTimeoutFieldValue(timeout: string | undefined): string | number {
	if (timeout === "") return "";
	const millis = timeout?.trim().match(/^([0-9]*\.?[0-9]+)ms$/);
	return millis ? millis[1] : parseSemanticTimeoutMs(timeout);
}

// decisionTimeoutFieldValue shows the stored Go duration as editable milliseconds.
export function decisionTimeoutFieldValue(timeout: string | undefined): string | number {
	if (timeout === "") return "";
	const millis = timeout?.trim().match(/^([0-9]*\.?[0-9]+)ms$/);
	return millis ? millis[1] : parseSemanticTimeoutMs(timeout ?? DEFAULT_DECISION_CONFIG.timeout);
}

// Same round-trip as semanticTimeoutFieldValue, with the llm default backstop.
export function llmTimeoutFieldValue(timeout: string | undefined): string | number {
	if (timeout === "") return "";
	const millis = timeout?.trim().match(/^([0-9]*\.?[0-9]+)ms$/);
	return millis ? millis[1] : parseLLMTimeoutMs(timeout);
}
// shouldSeedLLMPrompt decides whether the shipped guidance may initialize the draft.
export function shouldSeedLLMPrompt(enabled: boolean, defaultPrompt: string, prompt: string, edited: boolean): boolean {
	return enabled && defaultPrompt !== "" && prompt === "" && !edited;
}

// isRouterConfigured decides whether the page opens on the classifier choice
// (nothing set up yet) or straight on the configured classifier's settings.
// A router counts as configured once it can classify anything: a saved semantic
// block, or Decision chosen as the primary classifier. Phrases alone do not count;
// every install has them, and without a classifier they route nothing.
export function isRouterConfigured(config: AnalyzerConfig | undefined): boolean {
	if (!config) return false;
	if (config.classifier?.trim().toLowerCase() === "decision") return true;
	return Boolean(config.semantic?.provider && config.semantic?.embedding_model);
}
// DecisionProviderState is what the UI can actually tell about the provider the
// decision model runs through. "configured" is not a guarantee that decision
// calls succeed — only a live request proves that — so the UI never calls it "ready".
export type DecisionProviderState = "missing" | "failing" | "no-enabled-key" | "configured";

export function getDecisionProviderState(
	providers: ModelProvider[] | undefined,
	keys: DBKey[] | undefined,
	providerName: string,
): DecisionProviderState {
	const provider = (providers ?? []).find((candidate) => candidate.name === providerName);
	if (!provider) return "missing";
	// The provider failed to initialise. A model-listing failure is not checked:
	// it says nothing about the decisions endpoint, and the provider-level listing
	// status can be stale while the provider's keys work.
	if (provider.provider_status !== "active") return "failing";
	// A keyless custom provider (a self-hosted Laya, for one) needs no key.
	if (provider.custom_provider_config?.is_key_less) return "configured";
	// A key omits `enabled` when unset, which the Go side reads as enabled.
	if (!(keys ?? []).some((key) => key.provider === providerName && key.enabled !== false)) return "no-enabled-key";
	return "configured";
}