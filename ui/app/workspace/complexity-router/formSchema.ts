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
	DEFAULT_SUGGESTION_EVERY,
	DEFAULT_SUGGESTION_LOOKBACK_DAYS,
	DEFAULT_SUGGESTION_SAMPLE_RATE,
	MAX_SUGGESTION_INSTRUCTIONS_CHARACTERS,
	MAX_SUGGESTION_LOOKBACK_DAYS,
	DEFAULT_SUGGESTION_START_AT,
	MAX_SUGGESTION_EVERY,
	SUGGESTION_EVERY_PATTERN,
	SUGGESTION_EVERY_UNITS,
	SuggestionEveryUnit,
	SUGGESTION_TIER_CLASSIFIERS,
	MAX_SUGGESTION_TIER_PROMPT_CHARACTERS,
	SuggestionTierDefaults,
	SuggestionsTierConfig,
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
		// The interval as a count of a unit; the count's range depends on the
		// unit and is checked in superRefine.
		every_count: z
			.number({ error: () => complexityT("mustBeWholeNumber") })
			.int({ error: () => complexityT("mustBeWholeNumber") })
			.min(1, { error: () => complexityT("mustBeAtLeast", { min: 1 }) }),
		every_unit: z.enum(SUGGESTION_EVERY_UNITS),
		// The first run in the browser's local time, as a datetime-local input
		// holds it; sent as UTC. Empty is the gateway's default first run.
		start_at: z.string().refine((value) => value === "" || fromLocalDateTime(value) !== undefined, {
			error: () => complexityT("suggestions.firstRunRequired"),
		}),
		provider: z.string(),
		model: z.string(),
		instructions: z.string().max(MAX_SUGGESTION_INSTRUCTIONS_CHARACTERS, {
			error: () => complexityT("promptMaxChars", { max: MAX_SUGGESTION_INSTRUCTIONS_CHARACTERS }),
		}),
		// The tier step: an LLM or a decision model, its model, and every word
		// it reads. Empty prompt and instructions mean generated; the criteria
		// editors always hold what will be sent, seeded with the generated ones.
		tier_classifier: z.enum(SUGGESTION_TIER_CLASSIFIERS),
		tier_provider: z.string(),
		tier_model: z.string(),
		tier_prompt: z.string().max(MAX_SUGGESTION_TIER_PROMPT_CHARACTERS, {
			error: () => complexityT("promptMaxChars", { max: MAX_SUGGESTION_TIER_PROMPT_CHARACTERS }),
		}),
		tier_instructions: z.string().max(MAX_SUGGESTION_INSTRUCTIONS_CHARACTERS, {
			error: () => complexityT("promptMaxChars", { max: MAX_SUGGESTION_INSTRUCTIONS_CHARACTERS }),
		}),
		tier_criteria: z.object({
			SIMPLE: decisionTierFormShape,
			MEDIUM: decisionTierFormShape,
			COMPLEX: decisionTierFormShape,
		}),
	})
	.superRefine((data, ctx) => {
		const maxEvery = MAX_SUGGESTION_EVERY[data.every_unit];
		if (data.every_count > maxEvery) {
			ctx.addIssue({
				code: "custom",
				message: complexityT(`suggestions.everyMax.${data.every_unit}`, { max: maxEvery }),
				path: ["every_count"],
			});
		}
		if (data.provider.trim() !== "" && data.model.trim() === "") {
			ctx.addIssue({ code: "custom", message: complexityT("suggestions.modelRequired"), path: ["model"] });
		}
		if (data.provider.trim() === "" && data.model.trim() !== "") {
			ctx.addIssue({ code: "custom", message: complexityT("suggestions.providerRequired"), path: ["provider"] });
		}
		if (data.tier_provider.trim() !== "" && data.tier_model.trim() === "") {
			ctx.addIssue({ code: "custom", message: complexityT("suggestions.modelRequired"), path: ["tier_model"] });
		}
		if (data.tier_provider.trim() === "" && data.tier_model.trim() !== "") {
			ctx.addIssue({ code: "custom", message: complexityT("suggestions.providerRequired"), path: ["tier_provider"] });
		}
		// Running suggestions needs both models named: nothing is borrowed from
		// elsewhere in the router at run time.
		if (data.enabled && data.provider.trim() === "" && data.model.trim() === "") {
			ctx.addIssue({ code: "custom", message: complexityT("suggestions.phraseModelRequired"), path: ["model"] });
		}
		if (data.enabled && data.tier_provider.trim() === "" && data.tier_model.trim() === "") {
			ctx.addIssue({ code: "custom", message: complexityT("suggestions.tierModelRequired"), path: ["tier_model"] });
		}
		// A decision tier step sends the criteria editors as written. Examples
		// and signals may be empty (a tier with no phrases of the router's own
		// has none), but a filled tier needs its definition. A tier not filled
		// yet (its generated criteria have not arrived) is left generated.
		if (data.tier_classifier === "decision") {
			for (const tier of COMPLEXITY_TIER_VALUES) {
				const criteria = data.tier_criteria[tier];
				if (isDecisionTierEmpty(criteria)) continue;
				if (!criteria.definition.trim()) {
					ctx.addIssue({ code: "custom", message: complexityT("enterDefinition"), path: ["tier_criteria", tier, "definition"] });
				} else if (criteria.definition.length > MAX_DECISION_DEFINITION_CHARACTERS) {
					ctx.addIssue({
						code: "custom",
						message: complexityT("promptMaxChars", { max: MAX_DECISION_DEFINITION_CHARACTERS }),
						path: ["tier_criteria", tier, "definition"],
					});
				}
				for (const field of ["signals", "examples"] as const) {
					const list = criteria[field];
					if (list.length > MAX_DECISION_CRITERIA_ITEMS) {
						ctx.addIssue({
							code: "custom",
							message:
								field === "signals"
									? complexityT("criteriaSignalMax", { count: MAX_DECISION_CRITERIA_ITEMS })
									: complexityT("criteriaExampleMax", { count: MAX_DECISION_CRITERIA_ITEMS }),
							path: ["tier_criteria", tier, field],
						});
					} else if (list.some((item) => item.length > MAX_DECISION_CRITERIA_ITEM_CHARACTERS)) {
						ctx.addIssue({
							code: "custom",
							message:
								field === "signals"
									? complexityT("criteriaSignalItemMax", { max: MAX_DECISION_CRITERIA_ITEM_CHARACTERS })
									: complexityT("criteriaExampleItemMax", { max: MAX_DECISION_CRITERIA_ITEM_CHARACTERS }),
							path: ["tier_criteria", tier, field],
						});
					}
				}
			}
		}
	});

// suggestionEvery spells an interval as the gateway reads it, such as "6h".
export function suggestionEvery(count: number, unit: SuggestionEveryUnit): string {
	return `${count}${unit}`;
}

// parseSuggestionEvery splits an interval the gateway accepts into its count
// and unit; undefined for anything else.
export function parseSuggestionEvery(every: string): { count: number; unit: SuggestionEveryUnit } | undefined {
	if (!SUGGESTION_EVERY_PATTERN.test(every)) return undefined;
	return { count: Number(every.slice(0, -1)), unit: every.slice(-1) as SuggestionEveryUnit };
}

// suggestionIntervalMs is an interval's length: the gateway counts hours,
// days and weeks as fixed durations, not calendar ones. Zero when it is not
// an interval the gateway accepts.
export function suggestionIntervalMs(every: string): number {
	if (!SUGGESTION_EVERY_PATTERN.test(every)) return 0;
	const n = Number(every.slice(0, -1));
	const unit = every.slice(-1);
	const hour = 60 * 60 * 1000;
	return n * (unit === "h" ? hour : unit === "d" ? 24 * hour : 7 * 24 * hour);
}

// nextSuggestionRun is the first run at or after now on a schedule: the first
// run itself while it is ahead, else the next one an interval apart from it.
export function nextSuggestionRun(every: string, start: Date, now: Date): Date | undefined {
	const interval = suggestionIntervalMs(every);
	if (!interval || Number.isNaN(start.getTime())) return undefined;
	if (start.getTime() >= now.getTime()) return start;
	const steps = Math.ceil((now.getTime() - start.getTime()) / interval);
	return new Date(start.getTime() + steps * interval);
}

const pad = (n: number) => String(n).padStart(2, "0");

// toLocalDateTime renders a time as a datetime-local input holds it: the
// browser's local time, to the minute.
export function toLocalDateTime(time: Date): string {
	return `${time.getFullYear()}-${pad(time.getMonth() + 1)}-${pad(time.getDate())}T${pad(time.getHours())}:${pad(time.getMinutes())}`;
}

// fromLocalDateTime reads a datetime-local value back; undefined when it is
// empty or not a time.
export function fromLocalDateTime(value: string): Date | undefined {
	if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}$/.test(value)) return undefined;
	const time = new Date(value);
	return Number.isNaN(time.getTime()) ? undefined : time;
}

// suggestionFirstRunValue is what the first-run field starts as: the saved
// first run, or, when none is saved, the next run the gateway's default
// schedule gives for this interval. Either way the field shows exactly the
// schedule that is sent.
export function suggestionFirstRunValue(every: string, savedStartAt: string | undefined, now: Date): string {
	const saved = savedStartAt ? new Date(savedStartAt) : undefined;
	if (saved && !Number.isNaN(saved.getTime())) return toLocalDateTime(saved);
	const next = nextSuggestionRun(every, new Date(DEFAULT_SUGGESTION_START_AT), now) ?? new Date(DEFAULT_SUGGESTION_START_AT);
	return toLocalDateTime(next);
}

// usesDecision reports whether the Decision block is live: the primary classifier or the
// semantic fallback. Its controls are hidden and its values are not saved otherwise.
function usesDecision(values: { classifier: string; semantic: { fallback: string } }): boolean {
	return values.classifier === "decision" || values.semantic.fallback === "decision";
}

// usesLLM reports whether the llm block is live: the primary classifier or the semantic
// fallback. As with the Decision block, its controls are hidden and a half-picked block is
// not saved otherwise.
function usesLLM(values: { classifier: string; semantic: { fallback: string } }): boolean {
	return values.classifier === "llm" || values.semantic.fallback === "llm";
}

export const analyzerConfigSchema = z
	.object({
		classifier: z.enum(["semantic", "decision", "llm"]),
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
		if (data.session.enabled && data.classifier === "semantic" && (!hasProvider || !hasModel)) {
			ctx.addIssue({
				code: "custom",
				message: complexityT("sessionNeedsClassifier"),
				path: ["session", "enabled"],
			});
		}
		// Phrase suggestions cluster in the semantic classifier's embedding space,
		// so the gateway refuses enabled suggestions without its embedding model.
		// A blank one is called out where it is set; half-filled is flagged above.
		if (data.suggestions.enabled && data.classifier === "semantic" && !hasProvider && !hasModel) {
			ctx.addIssue({ code: "custom", message: complexityT("selectEmbeddingProvider"), path: ["semantic", "provider"] });
			ctx.addIssue({ code: "custom", message: complexityT("selectEmbeddingModel"), path: ["semantic", "embedding_model"] });
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

		// Choosing the llm classifier, or the semantic fallback "llm", makes the llm
		// block mandatory, because the server rejects either without one. While
		// neither is chosen its controls are hidden, so a block left half picked is
		// not checked: an error on it would have nowhere to show and would block
		// Save, and toAnalyzerPayload does not send a half-picked block.
		const hasLLMProvider = data.llm.provider.trim() !== "";
		const hasLLMModel = data.llm.model.trim() !== "";
		const llmIsPrimary = data.classifier === "llm";
		if (usesLLM(data)) {
			if (!hasLLMProvider) {
				ctx.addIssue({
					code: "custom",
					message: complexityT(llmIsPrimary ? "selectLlmProvider" : "selectFallbackProvider"),
					path: ["llm", "provider"],
				});
			}
			if (!hasLLMModel) {
				ctx.addIssue({
					code: "custom",
					message: complexityT(llmIsPrimary ? "selectLlmModel" : "selectFallbackModel"),
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
	every_count: 1,
	every_unit: "d",
	// Filled from the default schedule when the form is built: see
	// suggestionsFormValues.
	start_at: "",
	provider: "",
	model: "",
	instructions: "",
	tier_classifier: "llm",
	tier_provider: "",
	tier_model: "",
	tier_prompt: "",
	tier_instructions: "",
	tier_criteria: emptyTierCriteria(),
};

// rateToPercent and percentToRate convert the sample rate for the percent
// field. Kept to 12 significant digits, which drops float noise (a stored 0.1
// reads back as 10, not 10.000000000000002) without rounding a small valid
// rate to 0.
export function rateToPercent(rate: number): number {
	return Number((rate * 100).toPrecision(12));
}

export function percentToRate(percent: number): number {
	return Number((percent / 100).toPrecision(12));
}

type TierCriteriaFormValues = SuggestionsFormValues["tier_criteria"];

function emptyTierCriteria(): TierCriteriaFormValues {
	return {
		SIMPLE: { definition: "", signals: [], examples: [] },
		MEDIUM: { definition: "", signals: [], examples: [] },
		COMPLEX: { definition: "", signals: [], examples: [] },
	};
}

// isTierCriteriaEmpty reports criteria the form never seeded: the generated
// ones had not arrived, and nothing was saved.
export function isTierCriteriaEmpty(criteria: TierCriteriaFormValues): boolean {
	return COMPLEXITY_TIER_VALUES.every((tier) => isDecisionTierEmpty(criteria[tier]));
}

// tierCriteriaFormValues fills the tier criteria editors: a saved (edited)
// tier as it was saved, every other tier with what the gateway generates for
// it. Without generated criteria, unsaved tiers stay empty until they arrive.
export function tierCriteriaFormValues(
	saved?: NonNullable<AnalyzerConfig["suggestions"]>["tier"],
	generated?: SuggestionTierDefaults["criteria"],
): TierCriteriaFormValues {
	const out = emptyTierCriteria();
	for (const tier of COMPLEXITY_TIER_VALUES) {
		const edited = saved?.criteria?.[tier];
		const source = edited ?? generated?.[tier];
		if (!source) continue;
		out[tier] = { definition: source.definition, signals: [...(source.signals ?? [])], examples: [...(source.examples ?? [])] };
	}
	return out;
}

// suggestionsFormValues reads a saved block into the form, defaults filled in.
export function suggestionsFormValues(
	saved?: AnalyzerConfig["suggestions"],
	tierDefaults?: SuggestionTierDefaults,
	now: Date = new Date(),
): SuggestionsFormValues {
	if (!saved) {
		return {
			...DEFAULT_SUGGESTIONS_FORM_VALUES,
			start_at: suggestionFirstRunValue(DEFAULT_SUGGESTION_EVERY, undefined, now),
			tier_criteria: tierCriteriaFormValues(undefined, tierDefaults?.criteria),
		};
	}
	// An interval the gateway would refuse reads as the default one.
	const { count, unit } = parseSuggestionEvery(saved.schedule?.every ?? "") ?? {
		count: DEFAULT_SUGGESTIONS_FORM_VALUES.every_count,
		unit: DEFAULT_SUGGESTIONS_FORM_VALUES.every_unit,
	};
	const every = suggestionEvery(count, unit);
	return {
		enabled: saved.enabled,
		sample_percent: rateToPercent(saved.sample_rate ?? DEFAULT_SUGGESTION_SAMPLE_RATE),
		lookback_days: saved.lookback_days ?? DEFAULT_SUGGESTION_LOOKBACK_DAYS,
		every_count: count,
		every_unit: unit,
		start_at: suggestionFirstRunValue(every, saved.schedule?.start_at, now),
		provider: saved.provider ?? "",
		model: saved.model ?? "",
		instructions: saved.instructions ?? "",
		tier_classifier: saved.tier?.classifier === "decision" ? "decision" : "llm",
		tier_provider: saved.tier?.provider ?? "",
		tier_model: saved.tier?.model ?? "",
		tier_prompt: saved.tier?.prompt ?? "",
		tier_instructions: saved.tier?.instructions ?? "",
		tier_criteria: tierCriteriaFormValues(saved.tier, tierDefaults?.criteria),
	};
}

function sameTierCriteria(a: TierCriteriaFormValues[DecisionTier], b: SuggestionTierDefaults["criteria"][DecisionTier]): boolean {
	const same = (x: string[], y: string[]) => x.length === y.length && x.every((value, index) => value.trim() === y[index]);
	return a.definition.trim() === b.definition && same(a.signals, b.signals) && same(a.examples, b.examples);
}

// tierCriteriaToReseed lists the tiers the editors should take new generated
// criteria into: those still empty, and those still showing the previous
// generated criteria (untouched). Moving them with the baseline Save compares
// against keeps an untouched tier from reading as an edit of the old text and
// being saved as an override. Tiers the operator edited are left as written.
// previous is the generated criteria the editors were last seeded from;
// undefined means none yet, so only empty tiers are filled.
export function tierCriteriaToReseed(
	current: TierCriteriaFormValues,
	previous: SuggestionTierDefaults["criteria"] | undefined,
): DecisionTier[] {
	return COMPLEXITY_TIER_VALUES.filter(
		(tier) => isDecisionTierEmpty(current[tier]) || (previous?.[tier] !== undefined && sameTierCriteria(current[tier], previous[tier])),
	);
}

// tierCriteriaReseedValues is what each tier tierCriteriaToReseed lists takes
// once generated criteria arrive. A tier still empty takes its saved edit, or
// the generated criteria when it has none. A tier showing the previous
// generated criteria takes the new ones even when it has a saved edit: the
// operator put the generated text back, and the saved edit would undo that.
export function tierCriteriaReseedValues(
	current: TierCriteriaFormValues,
	previous: SuggestionTierDefaults["criteria"] | undefined,
	saved: NonNullable<AnalyzerConfig["suggestions"]>["tier"] | undefined,
	generated: SuggestionTierDefaults["criteria"],
): Array<[DecisionTier, TierCriteriaFormValues[DecisionTier]]> {
	const seeded = tierCriteriaFormValues(saved, generated);
	const following = tierCriteriaFormValues(undefined, generated);
	return tierCriteriaToReseed(current, previous).map((tier) => [tier, isDecisionTierEmpty(current[tier]) ? seeded[tier] : following[tier]]);
}

// withGeneratedTierCriteriaCleared is what the form validates: tiers whose
// criteria are still the generated ones are not sent (see
// tierCriteriaPayload), so their limits are the gateway's to keep, not the
// form's. A router's own phrases, which the generated examples are, may be
// longer than an edited example may. Those tiers are cleared, which the
// decision-tier check skips; every other tier is checked as written.
export function withGeneratedTierCriteriaCleared(
	values: AnalyzerFormValues,
	generated?: SuggestionTierDefaults["criteria"],
): AnalyzerFormValues {
	if (!generated) return values;
	const criteria = { ...values.suggestions.tier_criteria };
	let cleared = false;
	for (const tier of COMPLEXITY_TIER_VALUES) {
		if (generated[tier] && sameTierCriteria(criteria[tier], generated[tier])) {
			criteria[tier] = { definition: "", signals: [], examples: [] };
			cleared = true;
		}
	}
	return cleared ? { ...values, suggestions: { ...values.suggestions, tier_criteria: criteria } } : values;
}

// KnownTierDefaults is the generated tier text last known for one router.
export interface KnownTierDefaults {
	routerId: string;
	defaults: SuggestionTierDefaults;
}

// lastKnownTierDefaults is the generated tier text Save compares edits with:
// the gateway's latest, or, when a status refresh leaves it out (it could not
// read the router to render it), the last text known for the same router.
// Without that, Save would fall back to the saved criteria and drop edits
// made against the text shown. Another router's text never carries over.
export function lastKnownTierDefaults(
	previous: KnownTierDefaults | undefined,
	routerId: string | undefined,
	live: SuggestionTierDefaults | undefined,
): KnownTierDefaults | undefined {
	if (!routerId) return undefined;
	if (live) return { routerId, defaults: live };
	return previous?.routerId === routerId ? previous : undefined;
}

// tierCriteriaPayload sends the tiers whose criteria differ from what the
// gateway would generate; the rest stay generated, and so keep following the
// router. Until the generated criteria are known, what was saved is kept.
function tierCriteriaPayload(
	values: SuggestionsFormValues,
	saved?: NonNullable<AnalyzerConfig["suggestions"]>["tier"],
	generated?: SuggestionTierDefaults["criteria"],
): SuggestionsTierConfig["criteria"] {
	if (!generated) return saved?.criteria && Object.keys(saved.criteria).length ? saved.criteria : undefined;
	const out: NonNullable<SuggestionsTierConfig["criteria"]> = {};
	for (const tier of COMPLEXITY_TIER_VALUES) {
		const criteria = values.tier_criteria[tier];
		if (isDecisionTierEmpty(criteria) || sameTierCriteria(criteria, generated[tier])) continue;
		out[tier] = {
			definition: criteria.definition.trim(),
			signals: criteria.signals.map((item) => item.trim()).filter(Boolean),
			examples: criteria.examples.map((item) => item.trim()).filter(Boolean),
		};
	}
	return Object.keys(out).length ? out : undefined;
}

// toSuggestionsPayload sends the schedule as the interval and the first run in
// UTC, which is what the gateway counts runs from. The cluster sizes are not edited here; whatever was saved for them (through
// config.json or the API) is sent back unchanged, and nothing is filled in, so
// the gateway's defaults apply.
export function toSuggestionsPayload(
	values: SuggestionsFormValues,
	saved?: AnalyzerConfig["suggestions"],
	tierDefaults?: SuggestionTierDefaults,
): NonNullable<AnalyzerConfig["suggestions"]> {
	const startAt = fromLocalDateTime(values.start_at);
	const provider = values.provider.trim();
	const model = values.model.trim();
	const tierProvider = values.tier_provider.trim();
	const tierModel = values.tier_model.trim();
	// The prompt editor shows the generated prompt while it is empty; text
	// identical to it is the generated prompt, and stays generated.
	const tierPrompt = values.tier_prompt.trim() && values.tier_prompt.trim() !== tierDefaults?.prompt.trim() ? values.tier_prompt : "";
	// Criteria are the decision step's. With the llm step they are hidden, so
	// edits left in them from a switch are not sent; what was saved is kept.
	const tierCriteria =
		values.tier_classifier === "decision"
			? tierCriteriaPayload(values, saved?.tier, tierDefaults?.criteria)
			: saved?.tier?.criteria && Object.keys(saved.tier.criteria).length
				? saved.tier.criteria
				: undefined;
	return {
		enabled: values.enabled,
		sample_rate: percentToRate(values.sample_percent),
		lookback_days: values.lookback_days,
		schedule: {
			every: suggestionEvery(values.every_count, values.every_unit),
			...(startAt ? { start_at: startAt.toISOString().replace(".000Z", "Z") } : {}),
		},
		...(saved?.max_clustered_requests !== undefined ? { max_clustered_requests: saved.max_clustered_requests } : {}),
		...(saved?.min_cluster_requests !== undefined ? { min_cluster_requests: saved.min_cluster_requests } : {}),
		...(provider && model ? { provider, model } : {}),
		...(values.instructions.trim() ? { instructions: values.instructions } : {}),
		tier: {
			classifier: values.tier_classifier,
			...(tierProvider && tierModel ? { provider: tierProvider, model: tierModel } : {}),
			...(tierPrompt ? { prompt: tierPrompt } : {}),
			...(values.tier_instructions.trim() ? { instructions: values.tier_instructions } : {}),
			...(tierCriteria ? { criteria: tierCriteria } : {}),
		},
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
export function isDecisionTierEmpty({ definition, signals, examples }: DecisionGuidanceFormValues["criteria"][DecisionTier]): boolean {
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
export function toFormValues(
	config: AnalyzerConfig,
	decisionDefaults?: DecisionGuidanceDefaults,
	tierDefaults?: SuggestionTierDefaults,
): AnalyzerFormValues {
	const saved = config.semantic;
	const savedLLM = config.llm;
	const classifier = config.classifier?.trim().toLowerCase();
	return {
		classifier: classifier === "decision" || classifier === "llm" ? classifier : "semantic",
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
		suggestions: suggestionsFormValues(config.suggestions, tierDefaults),
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
// toComplexityConfigPayload is toAnalyzerPayload for the Complexity Router
// page's config, which makes no phrase suggestions (only model routers do), so
// its suggestions block is never sent; the gateway refuses one.
export function toComplexityConfigPayload(values: AnalyzerFormValues, saved?: AnalyzerConfig): AnalyzerConfig {
	const { suggestions: _suggestions, ...payload } = toAnalyzerPayload(values, saved);
	return payload;
}

export function toAnalyzerPayload(
	values: AnalyzerFormValues,
	saved?: AnalyzerConfig,
	tierDefaults?: SuggestionTierDefaults,
): AnalyzerConfig {
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
		// Suggestions cluster in the semantic classifier's embedding space and
		// the gateway refuses them on any other, so another classifier saves
		// them turned off, settings kept.
		...(values.suggestions.enabled || saved?.suggestions
			? {
					suggestions: toSuggestionsPayload(
						values.classifier === "semantic" ? values.suggestions : { ...values.suggestions, enabled: false },
						saved?.suggestions,
						tierDefaults,
					),
				}
			: {}),
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

// isRouterConfigured reports whether a router can classify anything: a saved
// semantic block, Decision chosen as the primary classifier, or the llm
// classifier with its model. Phrases alone do not count; every install has
// them, and without a classifier they route nothing.
export function isRouterConfigured(config: AnalyzerConfig | undefined): boolean {
	if (!config) return false;
	const classifier = config.classifier?.trim().toLowerCase();
	if (classifier === "decision") return true;
	if (classifier === "llm") return Boolean(config.llm?.provider && config.llm?.model);
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