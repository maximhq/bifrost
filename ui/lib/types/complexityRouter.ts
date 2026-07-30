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
	// Preserved while editing but intentionally not exposed until embedding
	// budget attribution has a defined accounting owner and behavior.
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
	description: string;
}> = [
	{
		key: "simple_keywords",
		label: "Simple tier phrases",
		description: "Lightweight language signals for lexical scoring and semantic SIMPLE examples.",
	},
	{
		key: "medium_keywords",
		label: "Medium tier phrases",
		description: "Implementation and technical signals for lexical scoring and semantic MEDIUM examples.",
	},
	{
		key: "complex_keywords",
		label: "Complex tier phrases",
		description:
			"High-confidence reasoning signals and semantic COMPLEX examples. One lexical match guarantees at least MEDIUM; two route to COMPLEX.",
	},
];

export const DEFAULT_TIER_BOUNDARIES: TierBoundaries = {
	simple_medium: 0.2,
	medium_complex: 0.4,
};