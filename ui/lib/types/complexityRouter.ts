/**
 * Complexity Router Type Definitions
 * Mirrors the AnalyzerConfig shape exchanged with /governance/complexity-analyzer-config.
 */

export interface EditableKeywordConfig {
	simple_keywords: string[];
	medium_keywords: string[];
	complex_keywords: string[];
}

export type SemanticVectorStore = "embedded" | "vector_store";

export interface SemanticConfig {
	provider: string;
	embedding_model: string;
	timeout?: string;
	// Similarity floor the nearest reference phrase must clear; below it no tier
	// is published. 0 means the nearest phrase always wins.
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
	// How many phrase vectors the gateway currently holds for the configured
	// provider/model. The cache is in-process only — vectors cannot be read back
	// out of a vector store — so a restart empties it while the saved phrases look
	// unchanged. Reuse cannot be inferred from the persisted config alone; zero
	// means the next save re-embeds every phrase regardless of what changed.
	cached_phrases?: number;
}

export interface AnalyzerConfig {
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
// The filter offers exactly these, with no legacy entry alongside them (unlike
// LEGACY_COMPLEXITY_TIER_VALUES): the complexity_mechanism column ships with the
// semantic classifier, so no row was ever written with the retired "lexical"
// mechanism and filtering on it could only ever return nothing.
export const COMPLEXITY_MECHANISM_VALUES = ["semantic", "skipped"] as const;

// Labels cover "lexical" even though nothing filters on it. Rows predating the
// structured columns record their decision only in the prose routing log, and
// deriveComplexityRouting (logs/sheets/logDetailView.tsx) reconstructs those as
// "lexical" — the classifier that actually wrote them — for the detail view.
export const COMPLEXITY_MECHANISM_LABELS: Record<string, string> = {
	lexical: "Lexical",
	semantic: "Semantic",
	skipped: "Skipped",
};

// One card per tier in the Phrase to Tier Mapping section.
//
// The descriptions anchor on the model the tier should route to, not on what
// makes a request "simple" or "complex". That keeps the judgment with the
// operator — it is their model lineup and their cost tolerance — while still
// giving them something to sort against. Describing the tiers themselves put
// Bifrost in charge of the definition, and restating the tier name back at them
// ("phrases you deem simple") is not a description at all: the three cards have
// to differ in something the operator can act on.
export const TIER_PHRASE_LIST_DEFINITIONS: Array<{
	key: KeywordListKey;
	label: string;
	description: string;
}> = [
	{
		key: "simple_keywords",
		label: "Simple",
		description: "Requests to route to your cheapest, fastest model.",
	},
	{
		key: "medium_keywords",
		label: "Medium",
		description: "Requests that need more capability than your cheapest model, but not your most capable.",
	},
	{
		key: "complex_keywords",
		label: "Complex",
		description: "Requests that justify your most capable model.",
	},
];

// Mirrors DefaultComplexitySemanticTimeout in framework/configstore.
export const DEFAULT_SEMANTIC_TIMEOUT_MS = 1500;

// Server-side bounds from validateComplexitySemanticPhrases. Enforced here too
// so an over-long phrase fails in the form instead of as an opaque 400.
export const MIN_SEMANTIC_MESSAGE_HISTORY = 1;
export const MAX_SEMANTIC_MESSAGE_HISTORY = 10;
export const MAX_SEMANTIC_PHRASE_CHARACTERS = 2000;

// Seeded when a deployment has no semantic block saved yet. Provider and model
// stay blank because only the operator knows them.
export const DEFAULT_SEMANTIC_CONFIG: SemanticConfig = {
	provider: "",
	embedding_model: "",
	timeout: `${DEFAULT_SEMANTIC_TIMEOUT_MS}ms`,
	min_similarity: 0,
	message_history_count: 1,
	count_toward_budgets: false,
	vector_store: "embedded",
};

// These are the wire values, not display-only aliases: config.json, the Helm
// chart, and the governance API all take the same two strings.
export const SEMANTIC_VECTOR_STORE_OPTIONS: Array<{ value: SemanticVectorStore; label: string; tooltip: string }> = [
	{
		value: "embedded",
		label: "Embedded",
		tooltip: "Keeps phrase vectors in Bifrost's own memory. No infrastructure to run, but every restart re-embeds them.",
	},
	{
		value: "vector_store",
		label: "Vector Store",
		tooltip:
			"Keeps phrase vectors in the vector store configured for Bifrost, so they survive restarts. Falls back to Embedded if no vector store is available.",
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
