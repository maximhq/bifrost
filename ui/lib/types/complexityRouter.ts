/**
 * Complexity Router Type Definitions
 * Mirrors the AnalyzerConfig shape exchanged with /governance/complexity-analyzer-config.
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

export const COMPLEXITY_TIER_VALUES = ["简单", "中等", "复杂", "推理"] as const;

export const KEYWORD_LIST_DEFINITIONS: Array<{
	key: KeywordListKey;
	label: string;
	description: string;
}> = [
	{
		key: "simple_keywords",
		label: "简单关键词",
		description: "将请求偏向简单级的短语（问候、闲聊、寒暄）。",
	},
	{
		key: "code_keywords",
		label: "代码关键词",
		description: "表示请求涉及代码、调试或编程产物的信号。",
	},
	{
		key: "technical_keywords",
		label: "技术关键词",
		description: "提高复杂度分数的架构、基础设施和运维术语。",
	},
	{
		key: "reasoning_keywords",
		label: "推理关键词",
		description: "强推理触发词。匹配这些短语可将级别选择覆盖为推理级。",
	},
];

export const DEFAULT_TIER_BOUNDARIES: TierBoundaries = {
	simple_medium: 0.15,
	medium_complex: 0.35,
	complex_reasoning: 0.6,
};