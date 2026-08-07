/**
 * Complexity Router Type Definitions
 * Mirrors the AnalyzerConfig shape exchanged with /governance/complexity-analyzer-config.
 */

export interface TierBoundaries {
	simple_medium: number;
	medium_complex: number;
}

export interface EditableKeywordConfig {
	simple_keywords: string[];
	medium_keywords: string[];
	complex_keywords: string[];
}

export type SemanticVectorStore = "auto" | "embedded" | "external";

export interface SemanticConfig {
	provider: string;
	embedding_model: string;
	timeout?: string;
	// Similarity floor a nearest-exemplar match must clear; below it the request
	// resolves through `fallback`. 0 means the nearest exemplar always wins.
	min_similarity?: number;
	// How many of the most recent user messages are combined into the embedded
	// text. 1 (the default) embeds only the latest message.
	message_history_count?: number;
	count_toward_budgets?: boolean;
	vector_store?: SemanticVectorStore;
}

export interface SemanticStatusInfo {
	state: "disabled" | "warming" | "ready" | "failed";
	loaded: number;
	total: number;
	serving_previous?: boolean;
	error?: string;
}

export interface AnalyzerConfig {
	tier_boundaries: TierBoundaries;
	keywords: EditableKeywordConfig;
	semantic?: SemanticConfig;
}

export type KeywordListKey = keyof EditableKeywordConfig;

export const COMPLEXITY_TIER_VALUES = ["SIMPLE", "MEDIUM", "COMPLEX"] as const;

// REASONING was merged into COMPLEX and survives only in historical log rows.
// Kept out of COMPLEXITY_TIER_VALUES so the CEL builder never offers it; the
// logs filter renders it separately so those old rows stay reachable.
export const LEGACY_COMPLEXITY_TIER_VALUES = ["REASONING"] as const;

// Mirrors the complexity_mechanism values recorded by the gateway (plugins/governance/complexity).
// "skipped" means classification was demanded by a routing rule but produced no tier.
export const COMPLEXITY_MECHANISM_VALUES = ["lexical", "semantic", "skipped"] as const;

export const COMPLEXITY_MECHANISM_LABELS: Record<string, string> = {
	lexical: "Lexical",
	semantic: "Semantic",
	skipped: "Skipped",
};

export const KEYWORD_LIST_DEFINITIONS: Array<{
	key: KeywordListKey;
	label: string;
	lexicalDescription: string;
	semanticDescription: string;
}> = [
	{
		key: "simple_keywords",
		label: "Simple tier phrases",
		lexicalDescription: "Signals for short, direct requests. Matches reduce the lexical complexity score.",
		semanticDescription: "Examples of direct requests that need a short answer or one straightforward operation.",
	},
	{
		key: "medium_keywords",
		label: "Medium tier phrases",
		lexicalDescription:
			"Signals for implementation, technical work, and contained multi-step analysis. Matches increase the lexical complexity score.",
		semanticDescription: "Examples that need several steps, some analysis, or a contained implementation.",
	},
	{
		key: "complex_keywords",
		label: "Complex tier phrases",
		lexicalDescription:
			"High-confidence signals for deeper reasoning, trade-offs, and investigation. One match guarantees at least MEDIUM. Two matches classify as COMPLEX.",
		semanticDescription:
			"Examples that need deeper reasoning across constraints, trade-offs, failure modes, or multiple connected decisions.",
	},
];

export const DEFAULT_TIER_BOUNDARIES: TierBoundaries = {
	simple_medium: 0.2,
	medium_complex: 0.4,
};

// Mirrors DefaultComplexitySemanticTimeout in framework/configstore.
export const DEFAULT_SEMANTIC_TIMEOUT_MS = 1500;

// Server-side bounds from validateComplexitySemanticPhrases. Enforced here too
// so an over-long exemplar fails in the form instead of as an opaque 400.
export const MIN_SEMANTIC_MESSAGE_HISTORY = 1;
export const MAX_SEMANTIC_MESSAGE_HISTORY = 10;
export const MAX_SEMANTIC_PHRASE_CHARACTERS = 2000;

// Seeded when the user switches the page into semantic mode. Provider, model,
// and dimension stay blank because only the operator knows them.
export const DEFAULT_SEMANTIC_CONFIG: SemanticConfig = {
	provider: "",
	embedding_model: "",
	timeout: `${DEFAULT_SEMANTIC_TIMEOUT_MS}ms`,
	fallback: "lexical",
	min_similarity: 0,
	message_history_count: 1,
	count_toward_budgets: false,
	vector_store: "embedded",
};

export const SEMANTIC_FALLBACK_OPTIONS: Array<{ value: SemanticFallback; label: string; description: string }> = [
	{
		value: "lexical",
		label: "Fall back to lexical",
		description:
			"When semantic classification is unavailable, still warming, or below the similarity floor, score the request with the keyword analyzer.",
	},
	{
		value: "none",
		label: "No fallback",
		description:
			"Leave the request unclassified. Routing rules referencing complexity_tier will not match, so traffic follows your default routing.",
	},
];

export const SEMANTIC_VECTOR_STORE_OPTIONS: Array<{ value: SemanticVectorStore; label: string; description: string }> = [
	{
		value: "embedded",
		label: "Embedded",
		description: "Built-in in-process store. Needs no infrastructure, but exemplars are held in memory and re-embedded on every restart.",
	},
	{
		value: "auto",
		label: "Auto",
		description: "Use the configured vector store when one is available, otherwise fall back to the embedded store.",
	},
	{
		value: "external",
		label: "External",
		description:
			"Require the configured vector store. Exemplars persist across restarts, so warmup re-embeds only when the configuration changes.",
	},
];

export const SEMANTIC_STATUS_LABELS: Record<SemanticStatusInfo["state"], string> = {
	disabled: "Disabled",
	warming: "Warming up",
	ready: "Ready",
	failed: "Failed",
};

// Duration strings round-trip through the API as Go durations ("500ms"), but the
// form edits milliseconds. Anything unparseable falls back to the default rather
// than silently sending 0, which the server rejects.
export function parseSemanticTimeoutMs(timeout: string | undefined): number {
	if (!timeout) return DEFAULT_SEMANTIC_TIMEOUT_MS;
	const match = timeout.trim().match(/^([0-9]*\.?[0-9]+)(ns|us|µs|ms|s|m|h)$/);
	if (!match) {
		const numeric = Number(timeout);
		return Number.isFinite(numeric) && numeric > 0 ? numeric : DEFAULT_SEMANTIC_TIMEOUT_MS;
	}
	const value = Number(match[1]);
	const unitToMs: Record<string, number> = { ns: 1e-6, us: 1e-3, µs: 1e-3, ms: 1, s: 1000, m: 60000, h: 3600000 };
	const milliseconds = value * unitToMs[match[2]];
	return Number.isFinite(milliseconds) && milliseconds > 0 ? milliseconds : DEFAULT_SEMANTIC_TIMEOUT_MS;
}

export function formatSemanticTimeout(milliseconds: number): string {
	const safe = Number.isFinite(milliseconds) && milliseconds > 0 ? milliseconds : DEFAULT_SEMANTIC_TIMEOUT_MS;
	return `${safe}ms`;
}