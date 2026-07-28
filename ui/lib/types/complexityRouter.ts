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

export interface AnalyzerConfig {
	tier_boundaries: TierBoundaries;
	keywords: EditableKeywordConfig;
}

export type KeywordListKey = keyof EditableKeywordConfig;

export const COMPLEXITY_TIER_VALUES = ["SIMPLE", "MEDIUM", "COMPLEX"] as const;

// REASONING was merged into COMPLEX and survives only in historical log rows.
// Kept out of COMPLEXITY_TIER_VALUES so the CEL builder never offers it; the
// logs filter renders it separately so those old rows stay reachable.
export const LEGACY_COMPLEXITY_TIER_VALUES = ["REASONING"] as const;

// Mirrors the complexity_mechanism values recorded by the gateway (plugins/governance/complexity).
// "skipped" means classification was demanded by a routing rule but produced no tier.
// Future classifiers add their values here (e.g. "semantic", "llm").
export const COMPLEXITY_MECHANISM_VALUES = ["lexical", "skipped"] as const;

export const COMPLEXITY_MECHANISM_LABELS: Record<string, string> = {
	lexical: "Lexical",
	skipped: "Skipped",
};

export const KEYWORD_LIST_DEFINITIONS: Array<{
	key: KeywordListKey;
	label: string;
	description: string;
}> = [
	{
		key: "simple_keywords",
		label: "Simple keywords",
		description: "Lightweight-language signals that slightly lower the lexical complexity score.",
	},
	{
		key: "medium_keywords",
		label: "Medium keywords",
		description: "Implementation and technical signals that raise the lexical complexity score gradually.",
	},
	{
		key: "complex_keywords",
		label: "Complex keywords",
		description: "High-confidence reasoning signals. One guarantees at least MEDIUM; two route to COMPLEX.",
	},
];

export const DEFAULT_TIER_BOUNDARIES: TierBoundaries = {
	simple_medium: 0.2,
	medium_complex: 0.4,
};