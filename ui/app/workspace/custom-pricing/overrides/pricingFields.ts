import i18n from "@/lib/i18n";
import type { RequestType } from "@/lib/types/config";
import type { PricingOverrideMatchType, PricingOverridePatch } from "@/lib/types/governance";
import type { ParseKeys } from "i18next";

// Shared pricing-field metadata for override editing and display.
//
// Extracted from pricingOverrideSheet.tsx so read-only consumers (e.g. the
// model-catalog detail sheet) can reuse the labels without pulling that
// component's form/mutation dependencies into their bundle.

export const REQUEST_TYPE_GROUPS = [
	{
		labelKey: "customPricing.groupChat",
		types: ["chat_completion", "text_completion", "responses"],
	},
	{
		labelKey: "customPricing.groupEmbedding",
		types: ["embedding"],
	},
	{
		labelKey: "customPricing.groupRerank",
		types: ["rerank"],
	},
	{
		labelKey: "customPricing.groupDecisions",
		types: ["decisions"],
	},
	{
		labelKey: "customPricing.groupAudio",
		types: ["speech", "transcription", "live"],
	},
	{
		labelKey: "customPricing.groupImage",
		types: ["image_generation", "image_variation", "image_edit"],
	},
	{
		labelKey: "customPricing.groupVideo",
		types: ["video_generation", "video_remix"],
	},
	{
		labelKey: "customPricing.groupOcr",
		types: ["ocr"],
	},
] as const;

export const REQUEST_TYPE_OPTIONS = REQUEST_TYPE_GROUPS.flatMap((g) => g.types);

export function getRequestTypeGroup(rt: string): ParseKeys<"models"> | undefined {
	return REQUEST_TYPE_GROUPS.find((g) => (g.types as readonly string[]).includes(rt))?.labelKey;
}

export const PRICING_FIELDS = [
	// Chat / Text / Responses fields
	{
		key: "input_cost_per_token",
		labelKey: "customPricing.fields.input_cost_per_token",
		group: "chat",
		requestTypeGroups: ["chat", "embedding", "rerank", "audio", "image", "video"],
	},
	{
		key: "output_cost_per_token",
		labelKey: "customPricing.fields.output_cost_per_token",
		group: "chat",
		requestTypeGroups: ["chat", "rerank", "audio", "image", "video"],
	},
	{
		key: "input_cost_per_token_batches",
		labelKey: "customPricing.fields.input_cost_per_token_batches",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "output_cost_per_token_batches",
		labelKey: "customPricing.fields.output_cost_per_token_batches",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "input_cost_per_token_priority",
		labelKey: "customPricing.fields.input_cost_per_token_priority",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "output_cost_per_token_priority",
		labelKey: "customPricing.fields.output_cost_per_token_priority",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "input_cost_per_token_ultrafast",
		labelKey: "customPricing.fields.input_cost_per_token_ultrafast",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "output_cost_per_token_ultrafast",
		labelKey: "customPricing.fields.output_cost_per_token_ultrafast",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "input_cost_per_token_flex",
		labelKey: "customPricing.fields.input_cost_per_token_flex",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "output_cost_per_token_flex",
		labelKey: "customPricing.fields.output_cost_per_token_flex",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "input_cost_per_token_decisions",
		labelKey: "customPricing.fields.input_cost_per_token_decisions",
		group: "chat",
		requestTypeGroups: ["decisions"],
	},
	{
		key: "output_cost_per_token_decisions",
		labelKey: "customPricing.fields.output_cost_per_token_decisions",
		group: "chat",
		requestTypeGroups: ["decisions"],
	},
	{
		key: "input_cost_per_token_fast",
		labelKey: "customPricing.fields.input_cost_per_token_fast",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "output_cost_per_token_fast",
		labelKey: "customPricing.fields.output_cost_per_token_fast",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "input_cost_per_token_above_128k_tokens",
		labelKey: "customPricing.fields.input_cost_per_token_above_128k_tokens",
		group: "chat",
		requestTypeGroups: ["chat", "embedding", "rerank"],
	},
	{
		key: "output_cost_per_token_above_128k_tokens",
		labelKey: "customPricing.fields.output_cost_per_token_above_128k_tokens",
		group: "chat",
		requestTypeGroups: ["chat", "rerank", "audio"],
	},
	{
		key: "input_cost_per_token_above_100k_tokens",
		labelKey: "customPricing.fields.input_cost_per_token_above_100k_tokens",
		group: "chat",
		requestTypeGroups: ["chat", "embedding", "rerank"],
	},
	{
		key: "output_cost_per_token_above_100k_tokens",
		labelKey: "customPricing.fields.output_cost_per_token_above_100k_tokens",
		group: "chat",
		requestTypeGroups: ["chat", "rerank", "audio"],
	},
	{
		key: "input_cost_per_token_above_200k_tokens",
		labelKey: "customPricing.fields.input_cost_per_token_above_200k_tokens",
		group: "chat",
		requestTypeGroups: ["chat", "embedding", "rerank"],
	},
	{
		key: "input_cost_per_token_above_200k_tokens_priority",
		labelKey: "customPricing.fields.input_cost_per_token_above_200k_tokens_priority",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "output_cost_per_token_above_200k_tokens",
		labelKey: "customPricing.fields.output_cost_per_token_above_200k_tokens",
		group: "chat",
		requestTypeGroups: ["chat", "rerank", "audio"],
	},
	{
		key: "output_cost_per_token_above_200k_tokens_priority",
		labelKey: "customPricing.fields.output_cost_per_token_above_200k_tokens_priority",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "input_cost_per_token_above_272k_tokens",
		labelKey: "customPricing.fields.input_cost_per_token_above_272k_tokens",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "input_cost_per_token_above_272k_tokens_priority",
		labelKey: "customPricing.fields.input_cost_per_token_above_272k_tokens_priority",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "output_cost_per_token_above_272k_tokens",
		labelKey: "customPricing.fields.output_cost_per_token_above_272k_tokens",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "output_cost_per_token_above_272k_tokens_priority",
		labelKey: "customPricing.fields.output_cost_per_token_above_272k_tokens_priority",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "input_cost_per_token_flex_above_272k_tokens",
		labelKey: "customPricing.fields.input_cost_per_token_flex_above_272k_tokens",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "output_cost_per_token_flex_above_272k_tokens",
		labelKey: "customPricing.fields.output_cost_per_token_flex_above_272k_tokens",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "input_cost_per_token_above_272k_tokens_ultrafast",
		labelKey: "customPricing.fields.input_cost_per_token_above_272k_tokens_ultrafast",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "output_cost_per_token_above_272k_tokens_ultrafast",
		labelKey: "customPricing.fields.output_cost_per_token_above_272k_tokens_ultrafast",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_creation_input_token_cost",
		labelKey: "customPricing.fields.cache_creation_input_token_cost",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_read_input_token_cost",
		labelKey: "customPricing.fields.cache_read_input_token_cost",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_creation_input_token_cost_above_200k_tokens",
		labelKey: "customPricing.fields.cache_creation_input_token_cost_above_200k_tokens",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_read_input_token_cost_above_200k_tokens",
		labelKey: "customPricing.fields.cache_read_input_token_cost_above_200k_tokens",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_creation_input_token_cost_above_1hr",
		labelKey: "customPricing.fields.cache_creation_input_token_cost_above_1hr",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_creation_input_token_cost_above_1hr_above_200k_tokens",
		labelKey: "customPricing.fields.cache_creation_input_token_cost_above_1hr_above_200k_tokens",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_creation_input_token_cost_above_100k_tokens",
		labelKey: "customPricing.fields.cache_creation_input_token_cost_above_100k_tokens",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_read_input_token_cost_above_100k_tokens",
		labelKey: "customPricing.fields.cache_read_input_token_cost_above_100k_tokens",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_creation_input_token_cost_above_1hr_above_100k_tokens",
		labelKey: "customPricing.fields.cache_creation_input_token_cost_above_1hr_above_100k_tokens",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_read_input_token_cost_priority",
		labelKey: "customPricing.fields.cache_read_input_token_cost_priority",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_read_input_token_cost_ultrafast",
		labelKey: "customPricing.fields.cache_read_input_token_cost_ultrafast",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_read_input_token_cost_flex",
		labelKey: "customPricing.fields.cache_read_input_token_cost_flex",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_creation_input_token_cost_fast",
		labelKey: "customPricing.fields.cache_creation_input_token_cost_fast",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_creation_input_token_cost_above_1hr_fast",
		labelKey: "customPricing.fields.cache_creation_input_token_cost_above_1hr_fast",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_read_input_token_cost_fast",
		labelKey: "customPricing.fields.cache_read_input_token_cost_fast",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_read_input_token_cost_above_200k_tokens_priority",
		labelKey: "customPricing.fields.cache_read_input_token_cost_above_200k_tokens_priority",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_read_input_token_cost_above_272k_tokens",
		labelKey: "customPricing.fields.cache_read_input_token_cost_above_272k_tokens",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_read_input_token_cost_above_272k_tokens_priority",
		labelKey: "customPricing.fields.cache_read_input_token_cost_above_272k_tokens_priority",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_read_input_token_cost_flex_above_272k_tokens",
		labelKey: "customPricing.fields.cache_read_input_token_cost_flex_above_272k_tokens",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_read_input_token_cost_above_272k_tokens_ultrafast",
		labelKey: "customPricing.fields.cache_read_input_token_cost_above_272k_tokens_ultrafast",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_creation_input_token_cost_priority",
		labelKey: "customPricing.fields.cache_creation_input_token_cost_priority",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_creation_input_token_cost_ultrafast",
		labelKey: "customPricing.fields.cache_creation_input_token_cost_ultrafast",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_creation_input_token_cost_flex",
		labelKey: "customPricing.fields.cache_creation_input_token_cost_flex",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_creation_input_token_cost_above_272k_tokens",
		labelKey: "customPricing.fields.cache_creation_input_token_cost_above_272k_tokens",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_creation_input_token_cost_flex_above_272k_tokens",
		labelKey: "customPricing.fields.cache_creation_input_token_cost_flex_above_272k_tokens",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_creation_input_token_cost_above_272k_tokens_ultrafast",
		labelKey: "customPricing.fields.cache_creation_input_token_cost_above_272k_tokens_ultrafast",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_creation_input_token_cost_above_272k_tokens_priority",
		labelKey: "customPricing.fields.cache_creation_input_token_cost_above_272k_tokens_priority",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "web_search_cost_per_request",
		labelKey: "customPricing.fields.web_search_cost_per_request",
		group: "chat",
		requestTypeGroups: ["chat", "rerank"],
	},
	{
		key: "input_cost_per_query",
		labelKey: "customPricing.fields.input_cost_per_query",
		group: "chat",
		requestTypeGroups: ["rerank"],
	},
	{
		key: "code_interpreter_cost_per_session",
		labelKey: "customPricing.fields.code_interpreter_cost_per_session",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "inference_geo_us_multiplier",
		labelKey: "customPricing.fields.inference_geo_us_multiplier",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cost_per_request",
		labelKey: "customPricing.fields.cost_per_request",
		group: "chat",
		requestTypeGroups: ["chat", "embedding", "rerank", "audio", "image", "video", "ocr"],
	},
	{
		key: "off_peak_cost_multiplier",
		labelKey: "customPricing.fields.off_peak_cost_multiplier",
		group: "chat",
		requestTypeGroups: ["chat", "embedding", "rerank", "audio", "image", "video", "ocr"],
	},
	// Audio fields
	{
		key: "input_cost_per_character",
		labelKey: "customPricing.fields.input_cost_per_character",
		group: "audio",
		requestTypeGroups: ["audio"],
	},
	{
		key: "input_cost_per_audio_token",
		labelKey: "customPricing.fields.input_cost_per_audio_token",
		group: "audio",
		requestTypeGroups: ["audio"],
	},
	{
		key: "input_cost_per_audio_per_second",
		labelKey: "customPricing.fields.input_cost_per_audio_per_second",
		group: "audio",
		requestTypeGroups: ["audio"],
	},
	{
		key: "input_cost_per_audio_per_second_above_128k_tokens",
		labelKey: "customPricing.fields.input_cost_per_audio_per_second_above_128k_tokens",
		group: "audio",
		requestTypeGroups: ["audio"],
	},
	{
		key: "input_cost_per_second",
		labelKey: "customPricing.fields.input_cost_per_second",
		group: "audio",
		requestTypeGroups: ["audio", "video"],
	},
	{
		key: "output_cost_per_audio_token",
		labelKey: "customPricing.fields.output_cost_per_audio_token",
		group: "audio",
		requestTypeGroups: ["audio"],
	},
	{
		key: "output_cost_per_second",
		labelKey: "customPricing.fields.output_cost_per_second",
		group: "audio",
		requestTypeGroups: ["audio", "video"],
	},
	{
		key: "cache_creation_input_audio_token_cost",
		labelKey: "customPricing.fields.cache_creation_input_audio_token_cost",
		group: "audio",
		requestTypeGroups: ["audio"],
	},
	{
		key: "input_cost_per_token_voice_live",
		labelKey: "customPricing.fields.input_cost_per_token_voice_live",
		group: "audio",
		requestTypeGroups: ["chat", "audio"],
	},
	{
		key: "output_cost_per_token_voice_live",
		labelKey: "customPricing.fields.output_cost_per_token_voice_live",
		group: "audio",
		requestTypeGroups: ["chat", "audio"],
	},
	{
		key: "cache_read_input_token_cost_voice_live",
		labelKey: "customPricing.fields.cache_read_input_token_cost_voice_live",
		group: "audio",
		requestTypeGroups: ["chat", "audio"],
	},
	{
		key: "input_cost_per_audio_token_voice_live",
		labelKey: "customPricing.fields.input_cost_per_audio_token_voice_live",
		group: "audio",
		requestTypeGroups: ["chat", "audio"],
	},
	{
		key: "output_cost_per_audio_token_voice_live",
		labelKey: "customPricing.fields.output_cost_per_audio_token_voice_live",
		group: "audio",
		requestTypeGroups: ["chat", "audio"],
	},
	{
		key: "input_cost_per_standard_speech_audio_token_voice_live",
		labelKey: "customPricing.fields.input_cost_per_standard_speech_audio_token_voice_live",
		group: "audio",
		requestTypeGroups: ["chat", "audio"],
	},
	{
		key: "output_cost_per_standard_speech_audio_token_voice_live",
		labelKey: "customPricing.fields.output_cost_per_standard_speech_audio_token_voice_live",
		group: "audio",
		requestTypeGroups: ["chat", "audio"],
	},
	{
		key: "input_cost_per_custom_speech_audio_token_voice_live",
		labelKey: "customPricing.fields.input_cost_per_custom_speech_audio_token_voice_live",
		group: "audio",
		requestTypeGroups: ["chat", "audio"],
	},
	{
		key: "output_cost_per_custom_speech_audio_token_voice_live",
		labelKey: "customPricing.fields.output_cost_per_custom_speech_audio_token_voice_live",
		group: "audio",
		requestTypeGroups: ["chat", "audio"],
	},
	// Image fields
	{
		key: "input_cost_per_image_token",
		labelKey: "customPricing.fields.input_cost_per_image_token",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "input_cost_per_image",
		labelKey: "customPricing.fields.input_cost_per_image",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "input_cost_per_image_above_128k_tokens",
		labelKey: "customPricing.fields.input_cost_per_image_above_128k_tokens",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "input_cost_per_pixel",
		labelKey: "customPricing.fields.input_cost_per_pixel",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_token",
		labelKey: "customPricing.fields.output_cost_per_image_token",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image",
		labelKey: "customPricing.fields.output_cost_per_image",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_pixel",
		labelKey: "customPricing.fields.output_cost_per_pixel",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_premium_image",
		labelKey: "customPricing.fields.output_cost_per_image_premium_image",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_512_and_512_pixels",
		labelKey: "customPricing.fields.output_cost_per_image_above_512_and_512_pixels",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_512_and_512_pixels_and_premium_image",
		labelKey: "customPricing.fields.output_cost_per_image_above_512_and_512_pixels_and_premium_image",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_1024_and_1024_pixels",
		labelKey: "customPricing.fields.output_cost_per_image_above_1024_and_1024_pixels",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_1024_and_1024_pixels_and_premium_image",
		labelKey: "customPricing.fields.output_cost_per_image_above_1024_and_1024_pixels_and_premium_image",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_1024_and_1536_pixels",
		labelKey: "customPricing.fields.output_cost_per_image_above_1024_and_1536_pixels",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_1536_and_1024_pixels",
		labelKey: "customPricing.fields.output_cost_per_image_above_1536_and_1024_pixels",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_2048_and_2048_pixels",
		labelKey: "customPricing.fields.output_cost_per_image_above_2048_and_2048_pixels",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_4096_and_4096_pixels",
		labelKey: "customPricing.fields.output_cost_per_image_above_4096_and_4096_pixels",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_4_megapixels",
		labelKey: "customPricing.fields.output_cost_per_image_above_4_megapixels",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_8_megapixels",
		labelKey: "customPricing.fields.output_cost_per_image_above_8_megapixels",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_16_megapixels",
		labelKey: "customPricing.fields.output_cost_per_image_above_16_megapixels",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_32_megapixels",
		labelKey: "customPricing.fields.output_cost_per_image_above_32_megapixels",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_64_megapixels",
		labelKey: "customPricing.fields.output_cost_per_image_above_64_megapixels",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_low_quality",
		labelKey: "customPricing.fields.output_cost_per_image_low_quality",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_medium_quality",
		labelKey: "customPricing.fields.output_cost_per_image_medium_quality",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_high_quality",
		labelKey: "customPricing.fields.output_cost_per_image_high_quality",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_auto_quality",
		labelKey: "customPricing.fields.output_cost_per_image_auto_quality",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_1024_and_1024_pixels_low_quality",
		labelKey: "customPricing.fields.output_cost_per_image_above_1024_and_1024_pixels_low_quality",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_1024_and_1536_pixels_low_quality",
		labelKey: "customPricing.fields.output_cost_per_image_above_1024_and_1536_pixels_low_quality",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_1536_and_1024_pixels_low_quality",
		labelKey: "customPricing.fields.output_cost_per_image_above_1536_and_1024_pixels_low_quality",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_1024_and_1024_pixels_medium_quality",
		labelKey: "customPricing.fields.output_cost_per_image_above_1024_and_1024_pixels_medium_quality",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_1024_and_1536_pixels_medium_quality",
		labelKey: "customPricing.fields.output_cost_per_image_above_1024_and_1536_pixels_medium_quality",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_1536_and_1024_pixels_medium_quality",
		labelKey: "customPricing.fields.output_cost_per_image_above_1536_and_1024_pixels_medium_quality",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_1024_and_1024_pixels_high_quality",
		labelKey: "customPricing.fields.output_cost_per_image_above_1024_and_1024_pixels_high_quality",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_1024_and_1536_pixels_high_quality",
		labelKey: "customPricing.fields.output_cost_per_image_above_1024_and_1536_pixels_high_quality",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_1536_and_1024_pixels_high_quality",
		labelKey: "customPricing.fields.output_cost_per_image_above_1536_and_1024_pixels_high_quality",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_1024_and_1024_pixels_standard_quality",
		labelKey: "customPricing.fields.output_cost_per_image_above_1024_and_1024_pixels_standard_quality",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_1024_and_1536_pixels_standard_quality",
		labelKey: "customPricing.fields.output_cost_per_image_above_1024_and_1536_pixels_standard_quality",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_1536_and_1024_pixels_standard_quality",
		labelKey: "customPricing.fields.output_cost_per_image_above_1536_and_1024_pixels_standard_quality",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "cache_read_input_image_token_cost",
		labelKey: "customPricing.fields.cache_read_input_image_token_cost",
		group: "image",
		requestTypeGroups: ["image"],
	},
	// Video fields
	{
		key: "input_cost_per_video_per_second",
		labelKey: "customPricing.fields.input_cost_per_video_per_second",
		group: "video",
		requestTypeGroups: ["video"],
	},
	{
		key: "input_cost_per_video_per_second_above_128k_tokens",
		labelKey: "customPricing.fields.input_cost_per_video_per_second_above_128k_tokens",
		group: "video",
		requestTypeGroups: ["video"],
	},
	{
		key: "output_cost_per_video_per_second",
		labelKey: "customPricing.fields.output_cost_per_video_per_second",
		group: "video",
		requestTypeGroups: ["video"],
	},
	{
		key: "output_cost_per_video_per_second_480p",
		labelKey: "customPricing.fields.output_cost_per_video_per_second_480p",
		group: "video",
		requestTypeGroups: ["video"],
	},
	{
		key: "output_cost_per_video_per_second_720p",
		labelKey: "customPricing.fields.output_cost_per_video_per_second_720p",
		group: "video",
		requestTypeGroups: ["video"],
	},
	{
		key: "output_cost_per_video_per_second_1024p",
		labelKey: "customPricing.fields.output_cost_per_video_per_second_1024p",
		group: "video",
		requestTypeGroups: ["video"],
	},
	{
		key: "output_cost_per_video_per_second_1080p",
		labelKey: "customPricing.fields.output_cost_per_video_per_second_1080p",
		group: "video",
		requestTypeGroups: ["video"],
	},
	{
		key: "output_cost_per_video_per_second_4k",
		labelKey: "customPricing.fields.output_cost_per_video_per_second_4k",
		group: "video",
		requestTypeGroups: ["video"],
	},
	{
		key: "output_cost_per_video_token",
		labelKey: "customPricing.fields.output_cost_per_video_token",
		group: "video",
		requestTypeGroups: ["video"],
	},
	// OCR fields
	{
		key: "ocr_cost_per_page",
		labelKey: "customPricing.fields.ocr_cost_per_page",
		group: "ocr",
		requestTypeGroups: ["ocr"],
	},
	{
		key: "annotation_cost_per_page",
		labelKey: "customPricing.fields.annotation_cost_per_page",
		group: "ocr",
		requestTypeGroups: ["ocr"],
	},
] as const;

/** What a pricing field's number means, which decides how it is rendered. */
export type PricingFieldUnit = "token" | "character" | "currency" | "multiplier";

/**
 * Classifies a pricing field key by unit.
 *
 * Note the `_above_NNNk_tokens` strip: those suffixes name the *context tier* a
 * rate applies above, not the unit being priced. Without removing them first,
 * fields like `input_cost_per_audio_per_second_above_128k_tokens` and
 * `input_cost_per_image_above_128k_tokens` look token-priced when they are
 * priced per second and per image.
 */
export function pricingFieldUnit(key: string): PricingFieldUnit {
	if (key.endsWith("_multiplier")) return "multiplier";
	const withoutContextTier = key.replace(/_above_\d+k_tokens/g, "");
	// Before the token check: character-priced fields are scaled per 1M like
	// token pricing, but naming their unit "tokens" contradicts their label.
	if (withoutContextTier.includes("_per_character")) return "character";
	if (withoutContextTier.includes("token")) return "token";
	return "currency";
}

export type PricingFieldKey = (typeof PRICING_FIELDS)[number]["key"];

/** Catalog key of each pricing field's label; translate with `t(fieldLabelKeyByKey[key])`. */
export const fieldLabelKeyByKey = Object.fromEntries(PRICING_FIELDS.map((field) => [field.key, field.labelKey])) as Record<
	PricingFieldKey,
	ParseKeys<"models">
>;
export const patchKeys = PRICING_FIELDS.map((field) => field.key) as PricingFieldKey[];

/**
 * Per-field numeric bounds for fields that are not plain "any non-negative
 * cost". Fields absent from this map keep the default `>= 0` rule.
 */
export const FIELD_BOUNDS: Partial<Record<PricingFieldKey, { min?: number; max?: number; messageKey: ParseKeys<"models"> }>> = {
	// Every base rate is the peak price, so the off-peak multiplier can only
	// ever scale downward. A value of 0 would make off-peak requests free and a
	// value above 1 would make them cost more than peak; the pricing engine
	// rejects both and bills at peak, so reject them here too rather than
	// silently accepting a setting that will never take effect.
	off_peak_cost_multiplier: { min: 0, max: 1, messageKey: "customPricing.offPeakRange" },
};

/**
 * Validates one raw form value for a pricing field. Returns an error message,
 * or undefined when the value is acceptable. An empty value is not an error —
 * it means "do not override this field".
 */
export function pricingFieldError(key: PricingFieldKey, raw: string | undefined): string | undefined {
	if (raw == null || raw.trim() === "") return undefined;
	const parsed = Number(raw);
	if (!Number.isFinite(parsed)) return i18n.t("modelLimits.mustBeNumber", { ns: "models" });

	const bounds = FIELD_BOUNDS[key];
	if (bounds) {
		if (bounds.min != null && parsed <= bounds.min) return i18n.t(bounds.messageKey, { ns: "models" });
		if (bounds.max != null && parsed > bounds.max) return i18n.t(bounds.messageKey, { ns: "models" });
		return undefined;
	}
	if (parsed < 0) return i18n.t("modelLimits.mustBeGteZero", { ns: "models" });
	return undefined;
}

export type FieldErrors = Partial<Record<PricingFieldKey | "name" | "scope" | "pattern" | "patch", string>>;

// ---------------------------------------------------------------------------
// Override form state
//
// Lives here rather than in pricingOverrideSheet.tsx so the patch-building
// logic can be unit tested without pulling the sheet's React tree in.
// ---------------------------------------------------------------------------

export type ScopeRoot = "global" | "virtual_key" | "user";

export interface FormState {
	name: string;
	scopeRoot: ScopeRoot;
	userID: string;
	virtualKeyID: string;
	providerID: string;
	providerKeyID: string;
	matchType: PricingOverrideMatchType;
	pattern: string;
	requestTypes: RequestType[];
	pricingValues: Partial<Record<PricingFieldKey, string>>;
	/**
	 * Patch fields this form does not render — currently `peak_hours`, which is
	 * a schedule object rather than a number and is set via the API or the
	 * datasheet. Carried through verbatim so opening and saving an override in
	 * the UI never silently drops what the form cannot display.
	 */
	preservedPatch: Record<string, unknown>;
}

export const defaultFormState: FormState = {
	name: "",
	scopeRoot: "global",
	userID: "",
	virtualKeyID: "",
	providerID: "",
	providerKeyID: "",
	matchType: "exact",
	pattern: "",
	requestTypes: [],
	pricingValues: {},
	preservedPatch: {},
};

/**
 * Patch keys the form cannot render as a number but still round-trips. Today
 * only the `peak_hours` schedule object. Anything outside this list that is
 * also outside `patchKeys` is a typo, and the JSON editor rejects it rather
 * than saving a key the pricing engine will ignore forever.
 */
export const PRESERVED_PATCH_KEYS: readonly string[] = ["peak_hours"];

/**
 * `__proto__` and friends are never valid pricing field names, and assigning
 * them onto a plain object walks into `Object.prototype`'s accessor instead of
 * creating an own property: the value is silently dropped and the object's
 * prototype is mutated. JSON.parse does create `__proto__` as an own property,
 * so a patch fetched from the API can carry one. Refuse to carry these keys at
 * every point where an arbitrary patch key is copied.
 */
export function isUnsafePatchKey(key: string): boolean {
	return key === "__proto__" || key === "constructor" || key === "prototype";
}

export function buildPatchFromForm(form: FormState): { patch: PricingOverridePatch; errors: FieldErrors } {
	const errors: FieldErrors = {};
	const patch: PricingOverridePatch = {};

	// Only keys the form cannot render ride through. A key the form does render
	// can still reach preservedPatch when the stored value was not a number, and
	// copying it here would resurrect that stale value even after the user
	// cleared the field. The rendered form stays authoritative for its own keys.
	for (const [key, value] of Object.entries(form.preservedPatch ?? {})) {
		if (isUnsafePatchKey(key) || patchKeys.includes(key as PricingFieldKey)) continue;
		(patch as Record<string, unknown>)[key] = value;
	}

	for (const key of patchKeys) {
		const raw = form.pricingValues[key];
		if (raw == null || raw.trim() === "") continue;
		const err = pricingFieldError(key, raw);
		if (err) {
			errors[key] = err;
			continue;
		}
		(patch as Record<string, number>)[key] = Number(raw);
	}

	return { patch, errors };
}