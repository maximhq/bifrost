/**
 * Complexity Router Type Definitions
 * Mirrors the AnalyzerConfig shape exchanged with /api/governance/complexity.
 */

export interface TierBoundaries {
	simple_medium: number;
	medium_complex: number;
	complex_reasoning: number;
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
		description:
			"Strong reasoning triggers. Matching these phrases can override tier selection toward the REASONING tier.",
	},
];

export const DEFAULT_TIER_BOUNDARIES: TierBoundaries = {
	simple_medium: 0.15,
	medium_complex: 0.35,
	complex_reasoning: 0.6,
};
