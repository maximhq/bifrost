export type PricingOverrideScope = "global" | "provider" | "provider_key" | "virtual_key";
export type PricingOverrideMatchType = "exact" | "wildcard" | "regex";

export type PricingOverrideRequestType =
	| "text_completion"
	| "text_completion_stream"
	| "chat_completion"
	| "chat_completion_stream"
	| "responses"
	| "responses_stream"
	| "embedding"
	| "rerank"
	| "speech"
	| "speech_stream"
	| "transcription"
	| "transcription_stream"
	| "image_generation"
	| "image_generation_stream";

export const PRICING_OVERRIDE_SCOPES: { value: PricingOverrideScope; label: string }[] = [
	{ value: "global", label: "Global" },
	{ value: "provider", label: "Provider" },
	{ value: "provider_key", label: "Provider Key" },
	{ value: "virtual_key", label: "Virtual Key" },
];

export const PRICING_OVERRIDE_REQUEST_TYPES: { value: PricingOverrideRequestType; label: string }[] = [
	{ value: "text_completion", label: "Text Completion" },
	{ value: "text_completion_stream", label: "Text Completion Stream" },
	{ value: "chat_completion", label: "Chat Completion" },
	{ value: "chat_completion_stream", label: "Chat Completion Stream" },
	{ value: "responses", label: "Responses" },
	{ value: "responses_stream", label: "Responses Stream" },
	{ value: "embedding", label: "Embedding" },
	{ value: "rerank", label: "Rerank" },
	{ value: "speech", label: "Speech" },
	{ value: "speech_stream", label: "Speech Stream" },
	{ value: "transcription", label: "Transcription" },
	{ value: "transcription_stream", label: "Transcription Stream" },
	{ value: "image_generation", label: "Image Generation" },
	{ value: "image_generation_stream", label: "Image Generation Stream" },
];

export interface PricingOverridePatch {
	model_pattern: string;
	match_type: PricingOverrideMatchType;
	request_types?: PricingOverrideRequestType[];
	input_cost_per_token?: number;
	output_cost_per_token?: number;
	input_cost_per_video_per_second?: number;
	input_cost_per_audio_per_second?: number;
	input_cost_per_character?: number;
	output_cost_per_character?: number;
	input_cost_per_token_above_128k_tokens?: number;
	input_cost_per_character_above_128k_tokens?: number;
	input_cost_per_image_above_128k_tokens?: number;
	input_cost_per_video_per_second_above_128k_tokens?: number;
	input_cost_per_audio_per_second_above_128k_tokens?: number;
	output_cost_per_token_above_128k_tokens?: number;
	output_cost_per_character_above_128k_tokens?: number;
	input_cost_per_token_above_200k_tokens?: number;
	output_cost_per_token_above_200k_tokens?: number;
	cache_creation_input_token_cost_above_200k_tokens?: number;
	cache_read_input_token_cost_above_200k_tokens?: number;
	cache_read_input_token_cost?: number;
	cache_creation_input_token_cost?: number;
	input_cost_per_token_batches?: number;
	output_cost_per_token_batches?: number;
	input_cost_per_image_token?: number;
	output_cost_per_image_token?: number;
	input_cost_per_image?: number;
	output_cost_per_image?: number;
	cache_read_input_image_token_cost?: number;
}

export interface PricingOverride extends PricingOverridePatch {
	id: string;
	config_hash?: string;
	name: string;
	enabled: boolean;
	scope: PricingOverrideScope;
	scope_id?: string;
	created_at: string;
	updated_at: string;
}

export interface CreatePricingOverrideRequest extends PricingOverridePatch {
	name: string;
	enabled?: boolean;
	scope: PricingOverrideScope;
	scope_id?: string;
}

export interface UpdatePricingOverrideRequest extends Partial<PricingOverridePatch> {
	name?: string;
	enabled?: boolean;
	scope?: PricingOverrideScope;
	scope_id?: string;
}

export interface GetPricingOverridesResponse {
	pricing_overrides: PricingOverride[];
	count: number;
}
