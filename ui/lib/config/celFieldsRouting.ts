/**
 * CEL Fields Configuration for Routing Rules
 * Defines available fields for building routing rule expressions
 */

import { getProviderLabel } from "@/lib/constants/logs";
import { COMPLEXITY_TIER_VALUES } from "@/lib/types/complexityRouter";

export interface CELFieldDefinition {
	name: string;
	label: string;
	placeholder?: string;
	inputType?: "text" | "select" | "keyValue" | "number";
	valueEditorType?:
	| "text"
	| "select"
	| "keyValue"
	| "number"
	| "textarea"
	| "budgetNumber"
	| ((operator: string) => "text" | "select" | "keyValue" | "number" | "textarea" | "budgetNumber");
	operators?: string[];
	defaultOperator?: string;
	defaultValue?: any;
	values?: Array<{ name: string; label: string; disabled?: boolean }>;
	metricOptions?: Array<{ name: string; label: string }>; // For budgetNumber type
	description?: string; // Helpful note for the user
}

export const baseRoutingFields: CELFieldDefinition[] = [
	{
		name: "model",
		label: "模型",
		placeholder: "例如 gpt-4、claude-3-sonnet",
		inputType: "text",
		valueEditorType: (operator: string) =>
			operator === "=" || operator === "!=" ? "select" : operator === "in" || operator === "notIn" ? "select" : "text",
		operators: ["=", "!=", "in", "notIn", "contains", "beginsWith", "endsWith", "matches"],
		defaultOperator: "=",
	},
	{
		name: "provider",
		label: "提供商",
		placeholder: "选择提供商",
		inputType: "select",
		valueEditorType: (operator: string) =>
			operator === "matches" ? "text" : operator === "in" || operator === "notIn" ? "select" : "select",
		operators: ["=", "!=", "in", "notIn", "matches", "null", "notNull"],
		defaultOperator: "=",
	},
	{
		name: "request_type",
		label: "请求类型",
		placeholder: "Select request type",
		inputType: "select",
		valueEditorType: (operator: string) =>
			operator === "matches" ? "text" : operator === "in" || operator === "notIn" ? "select" : "select",
		operators: ["=", "!=", "in", "notIn", "matches"],
		defaultOperator: "=",
		values: [
			{ name: "text_completion", label: "文本补全" },
			{ name: "text_completion_stream", label: "文本补全（流式）" },
			{ name: "chat_completion", label: "对话补全" },
			{ name: "chat_completion_stream", label: "对话补全（流式）" },
			{ name: "responses", label: "响应" },
			{ name: "responses_stream", label: "响应（流式）" },
			{ name: "embedding", label: "Embeddings" },
			{ name: "image_generation", label: "图像生成" },
			{ name: "image_generation_stream", label: "图像生成（流式）" },
			{ name: "image_edit", label: "图像编辑" },
			{ name: "image_edit_stream", label: "图像编辑（流式）" },
			{ name: "image_variation", label: "图像变体" },
			{ name: "speech", label: "语音" },
			{ name: "speech_stream", label: "语音（流式）" },
			{ name: "transcription", label: "转录" },
			{ name: "transcription_stream", label: "转录（流式）" },
			{ name: "count_tokens", label: "统计 Token" },
			{ name: "rerank", label: "重排序" },
			{ name: "video_generation", label: "视频生成" },
		],
		description:
			"Filter rules by the type of API request (chat, text, embeddings, images, audio, etc.). Streaming and non-streaming requests are distinct types: select both to cover all requests of a kind.",
	},
	{
		name: "headers",
		label: "请求头",
		placeholder: "例如 authorization、x-custom-header（使用小写）",
		inputType: "keyValue",
		valueEditorType: "keyValue",
		operators: ["=", "!=", "contains", "beginsWith", "endsWith", "matches", "null", "notNull"],
		defaultOperator: "=",
	},
	{
		name: "tokens_used",
		label: "Token 使用率 (%)",
		placeholder: "例如 80",
		inputType: "text",
		valueEditorType: "number",
		operators: ["=", "!=", ">", "<", ">=", "<="],
		defaultOperator: ">=",
		description: "按百分比检查 Token 使用率。与模型和提供商配置的最大值比较。",
	},
	{
		name: "request",
		label: "请求 (%)",
		placeholder: "例如 80",
		inputType: "text",
		valueEditorType: "number",
		operators: ["=", "!=", ">", "<", ">=", "<="],
		defaultOperator: ">=",
		description: "按百分比检查请求使用率。与模型和提供商配置的最大值比较。",
	},
	{
		name: "budget_used",
		label: "预算使用率 (%)",
		placeholder: "例如 50",
		inputType: "text",
		valueEditorType: "number",
		operators: ["=", "!=", ">", "<", ">=", "<="],
		defaultOperator: ">=",
		description: "按百分比检查预算使用率。与模型和提供商配置的最大值比较。",
	},
	{
		name: "complexity_tier",
		label: "复杂度级别",
		placeholder: "选择复杂度级别",
		inputType: "select",
		valueEditorType: "select",
		operators: ["=", "!=", "in", "notIn"],
		defaultOperator: "=",
		values: COMPLEXITY_TIER_VALUES.map((tier) => ({ name: tier, label: tier.charAt(0) + tier.slice(1).toLowerCase() })),
	},
	{
		name: "params",
		label: "查询参数",
		placeholder: "例如 api_key、user_id",
		inputType: "keyValue",
		valueEditorType: "keyValue",
		operators: ["=", "!=", "contains", "beginsWith", "endsWith", "matches", "null", "notNull"],
		defaultOperator: "=",
	},
];

/**
 * Get routing fields with dynamic providers and models
 * Provider field values are populated dynamically from available providers
 * Metric options for rate limits and budget are populated from available providers and models
 */
export function getRoutingFields(providers: string[] = [], models: string[] = []): CELFieldDefinition[] {
	// Create provider field values
	const providerValues =
		providers.length > 0
			? providers.map((provider) => ({
				name: provider,
				label: getProviderLabel(provider),
			}))
			: [{ name: "_no_providers", label: "未配置提供商", disabled: true }];

	// Create model field values
	const modelValues =
		models.length > 0
			? models.map((model) => ({
				name: model,
				label: model,
			}))
			: [];

	// Create metric options for scope input: providers + models
	const scopeOptions = [
		{ name: "", label: "(provider-level)" }, // Empty scope for provider-level
		...providers.map((provider) => ({
			name: provider,
			label: `${provider} (provider)`,
		})),
		...models.map((model) => ({
			name: model,
			label: `${model} (model)`,
		})),
	];

	// Update provider field with dynamic values and rate limit/budget fields with scope options
	const fieldsWithDynamicValues = baseRoutingFields.map((field) => {
		if (field.name === "provider") {
			return {
				...field,
				values: providerValues,
			};
		}
		if (field.name === "model") {
			return {
				...field,
				values: modelValues,
			};
		}
		if (field.name === "tokens_used" || field.name === "request" || field.name === "budget_used") {
			return {
				...field,
				metricOptions: scopeOptions,
			};
		}
		return field;
	});

	return fieldsWithDynamicValues;
}

export const PROVIDER_DISPLAY_NAMES: Record<string, string> = {
	openai: "OpenAI",
	anthropic: "Anthropic",
	azure: "Azure OpenAI",
	gemini: "Google Gemini",
	vertex: "Vertex AI",
	cohere: "Cohere",
};