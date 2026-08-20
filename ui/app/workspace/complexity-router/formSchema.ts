import {
	AnalyzerConfig,
	DEFAULT_LLM_CONFIG,
	DEFAULT_SEMANTIC_CONFIG,
	KeywordListKey,
	MAX_LLM_PROMPT_CHARACTERS,
	MAX_LLM_MESSAGE_HISTORY,
	MAX_SEMANTIC_MESSAGE_HISTORY,
	MAX_SEMANTIC_PHRASE_CHARACTERS,
	MAX_SEMANTIC_TIMEOUT_MS,
	MIN_LLM_MESSAGE_HISTORY,
	MIN_SEMANTIC_MESSAGE_HISTORY,
	parseLLMTimeoutMs,
	parseSemanticTimeoutMs,
} from "@/lib/types/complexityRouter";
import { z } from "zod";

// Form-owned duration values are always a single unit (the controls append
// "ms"), so a plain positive-duration check is enough.
const positiveDurationPattern = /^[0-9]*\.?[0-9]+(ns|us|µs|ms|s|m|h)$/;

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
		.min(1, "Enter an embedding timeout")
		.refine(
			(value) => isPositiveDurationString(value),
			`Enter a timeout greater than 0 and at most ${MAX_SEMANTIC_TIMEOUT_MS}ms`,
		)
		.optional(),
	min_similarity: z.number({ error: "Enter a number between 0 and 1" }).min(0, "Must be 0 or greater").lt(1, "Must be less than 1"),
	message_history_count: z
		.number({ error: `Enter a number between ${MIN_SEMANTIC_MESSAGE_HISTORY} and ${MAX_SEMANTIC_MESSAGE_HISTORY}` })
		.int("Must be a whole number")
		.min(MIN_SEMANTIC_MESSAGE_HISTORY, `Must be at least ${MIN_SEMANTIC_MESSAGE_HISTORY}`)
		.max(MAX_SEMANTIC_MESSAGE_HISTORY, `Must be at most ${MAX_SEMANTIC_MESSAGE_HISTORY}`),
	count_toward_budgets: z.boolean().optional(),
	vector_store: z.enum(["embedded", "vector_store"]).optional(),
	fallback: z.enum(["none", "llm"]),
});

const llmSchema = z.object({
	provider: z.string(),
	model: z.string(),
	// Same millisecond-edited Go duration treatment as the semantic timeout.
	timeout: z
		.string()
		.min(1, "Enter a classification timeout")
		.refine(
			(value) => isPositiveDurationString(value),
			"Enter a timeout greater than 0",
		)
		.optional(),
	prompt: z.string().max(MAX_LLM_PROMPT_CHARACTERS, `Must be at most ${MAX_LLM_PROMPT_CHARACTERS} characters`),
	message_history_count: z
		.number({ error: `Enter a number between ${MIN_LLM_MESSAGE_HISTORY} and ${MAX_LLM_MESSAGE_HISTORY}` })
		.int("Must be a whole number")
		.min(MIN_LLM_MESSAGE_HISTORY, `Must be at least ${MIN_LLM_MESSAGE_HISTORY}`)
		.max(MAX_LLM_MESSAGE_HISTORY, `Must be at most ${MAX_LLM_MESSAGE_HISTORY}`),
	count_toward_budgets: z.boolean().optional(),
});

export const analyzerConfigSchema = z
	.object({
		keywords: z.object({
			simple_keywords: z.array(z.string()).min(1, "Simple phrases cannot be empty"),
			medium_keywords: z.array(z.string()).min(1, "Medium phrases cannot be empty"),
			complex_keywords: z.array(z.string()).min(1, "Complex phrases cannot be empty"),
		}),
		semantic: semanticSchema,
		llm: llmSchema,
	})
	.superRefine((data, ctx) => {
		// A blank provider and model means the classifier simply is not configured
		// yet, which is a legal state: phrase edits still save. Half-filled is not,
		// because it cannot be turned into a working classifier.
		const hasProvider = data.semantic.provider.trim() !== "";
		const hasModel = data.semantic.embedding_model.trim() !== "";
		if (hasProvider || hasModel) {
			if (!hasProvider) {
				ctx.addIssue({ code: "custom", message: "Select an embedding provider", path: ["semantic", "provider"] });
			}
			if (!hasModel) {
				ctx.addIssue({ code: "custom", message: "Select an embedding model", path: ["semantic", "embedding_model"] });
			}
		}

		// The llm block follows the same half-filled rule, with one addition:
		// switching the semantic fallback to "llm" makes the block mandatory,
		// because the server rejects that fallback without one.
		const hasLLMProvider = data.llm.provider.trim() !== "";
		const hasLLMModel = data.llm.model.trim() !== "";
		if (hasLLMProvider || hasLLMModel || data.semantic.fallback === "llm") {
			if (!hasLLMProvider) {
				ctx.addIssue({ code: "custom", message: "Select a fallback provider", path: ["llm", "provider"] });
			}
			if (!hasLLMModel) {
				ctx.addIssue({ code: "custom", message: "Select a fallback model", path: ["llm", "model"] });
			}
		}

		// Mirrors validateComplexitySemanticPhrases so invalid input fails in the
		// form instead of as an opaque 400.
		const lists: Array<{ key: KeywordListKey; label: string }> = [
			{ key: "simple_keywords", label: "Simple" },
			{ key: "medium_keywords", label: "Medium" },
			{ key: "complex_keywords", label: "Complex" },
		];

		const seen = new Map<string, string>();
		for (const { key, label } of lists) {
			for (const phrase of data.keywords[key]) {
				if (phrase.length > MAX_SEMANTIC_PHRASE_CHARACTERS) {
					ctx.addIssue({
						code: "custom",
						message: `A ${label} phrase exceeds the ${MAX_SEMANTIC_PHRASE_CHARACTERS}-character limit.`,
						path: ["keywords", key],
					});
					break;
				}
				const normalized = phrase.trim().toLowerCase();
				const firstTier = seen.get(normalized);
				if (firstTier && firstTier !== label) {
					ctx.addIssue({
						code: "custom",
						message: `"${phrase}" is also in the ${firstTier} list. Each phrase must belong to exactly one tier.`,
						path: ["keywords", key],
					});
				} else if (!firstTier) {
					seen.set(normalized, label);
				}
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

export const DEFAULT_FORM_VALUES: AnalyzerFormValues = {
	keywords: {
		simple_keywords: [],
		medium_keywords: [],
		complex_keywords: [],
	},
	semantic: DEFAULT_SEMANTIC_FORM_VALUES,
	llm: DEFAULT_LLM_FORM_VALUES,
};

// Fills in the fields the API omitted so the semantic controls stay controlled.
export function toFormValues(config: AnalyzerConfig): AnalyzerFormValues {
	const saved = config.semantic;
	const savedLLM = config.llm;
	return {
		keywords: config.keywords,
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

// Same round-trip as semanticTimeoutFieldValue, with the llm default backstop.
export function llmTimeoutFieldValue(timeout: string | undefined): string | number {
	if (timeout === "") return "";
	const millis = timeout?.trim().match(/^([0-9]*\.?[0-9]+)ms$/);
	return millis ? millis[1] : parseLLMTimeoutMs(timeout);
}
