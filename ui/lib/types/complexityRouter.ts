/**
 * Complexity Router Type Definitions
 * Mirrors the AnalyzerConfig shape exchanged with /governance/complexity-analyzer-config.
 */

export interface TierBoundaries {
	simple_medium: number;
	medium_complex: number;
}

export interface EditableKeywordConfig {
	code_keywords: string[];
	reasoning_keywords: string[];
	technical_keywords: string[];
	simple_keywords: string[];
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
		description: "Phrases that bias the request toward the SIMPLE tier (greetings, trivia, small talk).",
	},
	{
		key: "code_keywords",
		label: "Code keywords",
		description: "Signals that the request involves code, debugging, or programming artifacts.",
	},
	{
		key: "technical_keywords",
		label: "Technical keywords",
		description: "Architecture, infra, and operational terms that raise the complexity score.",
	},
	{
		key: "reasoning_keywords",
		label: "Reasoning keywords",
		description: "Strong reasoning triggers. Matching these phrases can override tier selection toward the COMPLEX tier.",
	},
];

export const DEFAULT_TIER_BOUNDARIES: TierBoundaries = {
	simple_medium: 0.17,
	medium_complex: 0.39,
};