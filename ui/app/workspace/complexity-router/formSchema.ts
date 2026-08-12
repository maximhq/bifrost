import {
	AnalyzerConfig,
	DEFAULT_LLM_CONFIG,
	DEFAULT_SEMANTIC_CONFIG,
	DEFAULT_SESSION_CONFIG,
	DEFAULT_SESSION_IDENTITY_SOURCES,
	KeywordListKey,
	MAX_LLM_PROMPT_CHARACTERS,
	MAX_LLM_MESSAGE_HISTORY,
	MAX_SEMANTIC_MESSAGE_HISTORY,
	MAX_SEMANTIC_PHRASE_CHARACTERS,
	MIN_LLM_MESSAGE_HISTORY,
	MIN_SEMANTIC_MESSAGE_HISTORY,
	parseLLMTimeoutMs,
	parseSemanticTimeoutMs,
	tryParseSessionTtlMinutes,
} from "@/lib/types/complexityRouter";
import { z } from "zod";

// Form-owned duration values are always a single unit (the controls append
// "ms"), so a plain positive-duration check is enough.
const positiveDurationPattern = /^[0-9]*\.?[0-9]+(ns|us|µs|ms|s|m|h)$/;

export function isPositiveDurationString(value: string | undefined): boolean {
	if (!value) return false;
	const trimmed = value.trim();
	return positiveDurationPattern.test(trimmed) && Number.parseFloat(trimmed) > 0;
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
			"Enter a timeout greater than 0",
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

const sessionSchema = z.object({
	mode: z.enum(["off", "pinned", "cache_aware"]),
	// Edited in minutes but stored as a Go duration, so a malformed or
	// non-positive entry is caught here rather than snapped back to the default
	// while the operator is still typing.
	ttl: z
		.string()
		.min(1, "Enter a session timeout")
		.refine(
			(value) => isPositiveDurationString(value),
			"Enter a timeout greater than 0",
		),
	// The server rejects an empty ladder, and with nothing able to identify a
	// session the feature silently does nothing.
	identity_sources: z.array(z.enum(["header", "harness"])).min(1, "Select at least one way to identify a session"),
	switch_min_similarity: z.number({ error: "Enter a number between 0 and 1" }).min(0, "Must be 0 or greater").lt(1, "Must be less than 1"),
	downgrade_after_n_turns: z.number({ error: "Enter a whole number of turns" }).int("Must be a whole number").min(1, "Must be at least 1"),
	min_cached_tokens_to_hold: z
		.number({ error: "Enter a whole number of tokens" })
		.int("Must be a whole number")
		.min(0, "Must be 0 or greater"),
	max_switches_per_session: z
		.number({ error: "Enter a whole number of switches" })
		.int("Must be a whole number")
		.min(0, "Must be 0 or greater"),
	always_allow_escalation: z.boolean(),
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
		session: sessionSchema,
	})
	.superRefine((data, ctx) => {
		// Mirrors the cross-field rule in ComplexityAnalyzerConfig.Validate. The
		// two numbers form hysteresis — a low bar to classify a turn, a higher bar
		// to move a whole session — so inverting them makes a session easier to
		// switch than a single turn is to classify.
		//
		// The other value is edited in the embedding sheet, so the message has to
		// name where it lives; an error pointing at a number the operator cannot
		// see from here is unactionable.
		if (data.session.switch_min_similarity > 0 && data.session.switch_min_similarity < data.semantic.min_similarity) {
			ctx.addIssue({
				code: "custom",
				message: `Must be at least the minimum similarity threshold (${data.semantic.min_similarity}), which is set in the embedding configuration.`,
				path: ["session", "switch_min_similarity"],
			});
		}

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
export type SessionFormValues = AnalyzerFormValues["session"];

export const DEFAULT_LLM_FORM_VALUES: LLMFormValues = {
	provider: DEFAULT_LLM_CONFIG.provider,
	model: DEFAULT_LLM_CONFIG.model,
	timeout: DEFAULT_LLM_CONFIG.timeout,
	prompt: DEFAULT_LLM_CONFIG.prompt ?? "",
	message_history_count: DEFAULT_LLM_CONFIG.message_history_count ?? MIN_LLM_MESSAGE_HISTORY,
	count_toward_budgets: DEFAULT_LLM_CONFIG.count_toward_budgets ?? false,
};

export const DEFAULT_SESSION_FORM_VALUES: SessionFormValues = {
	mode: DEFAULT_SESSION_CONFIG.mode,
	ttl: DEFAULT_SESSION_CONFIG.ttl ?? "60m",
	identity_sources: [...DEFAULT_SESSION_IDENTITY_SOURCES],
	switch_min_similarity: DEFAULT_SESSION_CONFIG.switch_min_similarity ?? 0,
	downgrade_after_n_turns: DEFAULT_SESSION_CONFIG.downgrade_after_n_turns ?? 2,
	min_cached_tokens_to_hold: DEFAULT_SESSION_CONFIG.min_cached_tokens_to_hold ?? 1024,
	max_switches_per_session: DEFAULT_SESSION_CONFIG.max_switches_per_session ?? 0,
	always_allow_escalation: DEFAULT_SESSION_CONFIG.always_allow_escalation ?? false,
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
	session: DEFAULT_SESSION_FORM_VALUES,
};

// Go serializes durations with compound units such as "1h0m0s", while the
// form owns a minutes-only value. Canonicalize valid saved durations so an
// untouched session config still passes the form schema.
function normalizeSessionTtl(ttl: string | undefined): string {
	const minutes = tryParseSessionTtlMinutes(ttl);
	return minutes === null ? DEFAULT_SESSION_FORM_VALUES.ttl : `${minutes}m`;
}

// Fills in the fields the API omitted so the semantic controls stay controlled.
export function toFormValues(config: AnalyzerConfig): AnalyzerFormValues {
	const saved = config.semantic;
	const savedLLM = config.llm;
	const savedSession = config.session;
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
		// Go omits every zero-valued session field, so a saved block arrives with
		// holes. Each one is filled from the same default the gateway's own
		// normalization would apply, rather than from the zero value: a 0 TTL or an
		// empty identity ladder reads as "disabled" while the mode still says
		// enabled.
		session: savedSession
			? {
					...DEFAULT_SESSION_FORM_VALUES,
					...savedSession,
					ttl: normalizeSessionTtl(savedSession.ttl),
					identity_sources:
						savedSession.identity_sources && savedSession.identity_sources.length > 0
							? savedSession.identity_sources
							: DEFAULT_SESSION_FORM_VALUES.identity_sources,
					switch_min_similarity: savedSession.switch_min_similarity ?? 0,
					downgrade_after_n_turns: savedSession.downgrade_after_n_turns ?? DEFAULT_SESSION_FORM_VALUES.downgrade_after_n_turns,
					min_cached_tokens_to_hold: savedSession.min_cached_tokens_to_hold ?? DEFAULT_SESSION_FORM_VALUES.min_cached_tokens_to_hold,
					max_switches_per_session: savedSession.max_switches_per_session ?? 0,
					always_allow_escalation: savedSession.always_allow_escalation ?? false,
				}
			: DEFAULT_SESSION_FORM_VALUES,
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

// Same round-trip as semanticTimeoutFieldValue, in minutes: a value this control
// wrote comes back digit for digit, a saved Go duration like "1h0m0s" is rendered
// as the equivalent minute count, and malformed raw state is left visibly blank
// instead of being masked as the default.
export function sessionTtlFieldValue(ttl: string | undefined): string | number {
	if (ttl === "") return "";
	const trimmed = ttl?.trim();
	if (!trimmed) return "";
	const minutes = trimmed.match(/^([0-9]*\.?[0-9]+)m$/);
	if (minutes) return minutes[1];
	const parsed = tryParseSessionTtlMinutes(trimmed);
	if (parsed !== null) return parsed;
	const numeric = Number(trimmed);
	return Number.isFinite(numeric) ? numeric : "";
}
