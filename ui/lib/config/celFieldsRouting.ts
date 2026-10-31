/**
 * CEL Fields Configuration for Routing Rules
 * Defines available fields for building routing rule expressions
 */

import { getProviderLabel } from "@/lib/constants/logs";
import i18n from "@/lib/i18n";
import { COMPLEXITY_TIER_VALUES } from "@/lib/types/complexityRouter";
import type { ParseKeys } from "i18next";

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

type ModelsKey = ParseKeys<"models">;

const COMPLEXITY_TIER_LABEL_KEYS: Record<(typeof COMPLEXITY_TIER_VALUES)[number], ModelsKey> = {
	SIMPLE: "routing.complexityUi.tierSimple",
	MEDIUM: "routing.complexityUi.tierMedium",
	COMPLEX: "routing.complexityUi.tierComplex",
};

// Labels resolve through i18n when read (getters), so the shared field list follows the
// active locale without every consumer having to thread a translator through.
const tm = (key: ModelsKey, options?: Record<string, string>): string => i18n.t(key, { ns: "models", ...options });

function opt(name: string, labelKey: ModelsKey): { name: string; label: string } {
	return {
		name,
		get label(): string {
			return tm(labelKey);
		},
	};
}

interface FieldTextKeys {
	label: ModelsKey;
	placeholder?: ModelsKey;
	description?: ModelsKey;
}

function field(definition: Omit<CELFieldDefinition, "label" | "placeholder" | "description">, keys: FieldTextKeys): CELFieldDefinition {
	return {
		...definition,
		get label(): string {
			return tm(keys.label);
		},
		get placeholder(): string | undefined {
			return keys.placeholder ? tm(keys.placeholder) : undefined;
		},
		get description(): string | undefined {
			return keys.description ? tm(keys.description) : undefined;
		},
	};
}

export const baseRoutingFields: CELFieldDefinition[] = [
	field(
		{
			name: "model",
			inputType: "text",
			valueEditorType: (operator: string) =>
				operator === "=" || operator === "!=" ? "select" : operator === "in" || operator === "notIn" ? "select" : "text",
			operators: ["=", "!=", "in", "notIn", "contains", "beginsWith", "endsWith", "matches"],
			defaultOperator: "=",
		},
		{ label: "routing.cel.fields.model", placeholder: "routing.cel.placeholders.model" },
	),
	field(
		{
			name: "provider",
			inputType: "select",
			valueEditorType: (operator: string) =>
				operator === "matches" ? "text" : operator === "in" || operator === "notIn" ? "select" : "select",
			operators: ["=", "!=", "in", "notIn", "matches", "null", "notNull"],
			defaultOperator: "=",
		},
		{ label: "routing.cel.fields.provider", placeholder: "routing.complexityUi.selectProvider" },
	),
	field(
		{
			name: "request_type",
			inputType: "select",
			valueEditorType: (operator: string) =>
				operator === "matches" ? "text" : operator === "in" || operator === "notIn" ? "select" : "select",
			operators: ["=", "!=", "in", "notIn", "matches"],
			defaultOperator: "=",
			values: [
				opt("text_completion", "routing.cel.requestTypes.text_completion"),
				opt("text_completion_stream", "routing.cel.requestTypes.text_completion_stream"),
				opt("chat_completion", "routing.cel.requestTypes.chat_completion"),
				opt("chat_completion_stream", "routing.cel.requestTypes.chat_completion_stream"),
				opt("responses", "routing.cel.requestTypes.responses"),
				opt("responses_stream", "routing.cel.requestTypes.responses_stream"),
				opt("embedding", "routing.cel.requestTypes.embedding"),
				opt("image_generation", "routing.cel.requestTypes.image_generation"),
				opt("image_generation_stream", "routing.cel.requestTypes.image_generation_stream"),
				opt("image_edit", "routing.cel.requestTypes.image_edit"),
				opt("image_edit_stream", "routing.cel.requestTypes.image_edit_stream"),
				opt("image_variation", "routing.cel.requestTypes.image_variation"),
				opt("speech", "routing.cel.requestTypes.speech"),
				opt("speech_stream", "routing.cel.requestTypes.speech_stream"),
				opt("transcription", "routing.cel.requestTypes.transcription"),
				opt("transcription_stream", "routing.cel.requestTypes.transcription_stream"),
				opt("count_tokens", "routing.cel.requestTypes.count_tokens"),
				opt("rerank", "routing.cel.requestTypes.rerank"),
				opt("decisions", "routing.cel.requestTypes.decisions"),
				opt("video_generation", "routing.cel.requestTypes.video_generation"),
			],
		},
		{
			label: "routing.cel.fields.requestType",
			placeholder: "routing.cel.placeholders.requestType",
			description: "routing.cel.descriptions.requestType",
		},
	),
	field(
		{
			name: "headers",
			inputType: "keyValue",
			valueEditorType: "keyValue",
			operators: ["=", "!=", "contains", "beginsWith", "endsWith", "matches", "null", "notNull"],
			defaultOperator: "=",
		},
		{ label: "routing.cel.fields.header", placeholder: "routing.cel.placeholders.header" },
	),
	field(
		{
			name: "tokens_used",
			inputType: "text",
			valueEditorType: "number",
			operators: ["=", "!=", ">", "<", ">=", "<="],
			defaultOperator: ">=",
		},
		{
			label: "routing.cel.fields.tokensUsed",
			placeholder: "routing.cel.placeholders.percentExample80",
			description: "routing.cel.descriptions.tokensUsed",
		},
	),
	field(
		{
			name: "request",
			inputType: "text",
			valueEditorType: "number",
			operators: ["=", "!=", ">", "<", ">=", "<="],
			defaultOperator: ">=",
		},
		{
			label: "routing.cel.fields.request",
			placeholder: "routing.cel.placeholders.percentExample80",
			description: "routing.cel.descriptions.request",
		},
	),
	field(
		{
			name: "budget_used",
			inputType: "text",
			valueEditorType: "number",
			operators: ["=", "!=", ">", "<", ">=", "<="],
			defaultOperator: ">=",
		},
		{
			label: "routing.cel.fields.budgetUsed",
			placeholder: "routing.cel.placeholders.percentExample50",
			description: "routing.cel.descriptions.budgetUsed",
		},
	),
	field(
		{
			name: "complexity_tier",
			inputType: "select",
			valueEditorType: "select",
			operators: ["=", "!=", "in", "notIn"],
			defaultOperator: "=",
			values: COMPLEXITY_TIER_VALUES.map((tier) => opt(tier, COMPLEXITY_TIER_LABEL_KEYS[tier])),
		},
		{ label: "routing.cel.fields.complexityTier", placeholder: "routing.cel.placeholders.complexityTier" },
	),
	field(
		{
			name: "params",
			inputType: "keyValue",
			valueEditorType: "keyValue",
			operators: ["=", "!=", "contains", "beginsWith", "endsWith", "matches", "null", "notNull"],
			defaultOperator: "=",
		},
		{ label: "routing.cel.fields.queryParameter", placeholder: "routing.cel.placeholders.queryParameter" },
	),
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
			: [{ name: "_no_providers", label: tm("routing.cel.noProvidersConfigured"), disabled: true }];

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
		{ name: "", label: tm("routing.cel.providerLevel") }, // Empty scope for provider-level
		...providers.map((provider) => ({
			name: provider,
			label: tm("routing.cel.scopeProvider", { provider }),
		})),
		...models.map((model) => ({
			name: model,
			label: tm("routing.cel.scopeModel", { model }),
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