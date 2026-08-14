// Shared pricing-field metadata for override editing and display.
//
// Extracted from pricingOverrideSheet.tsx so read-only consumers (e.g. the
// model-catalog detail sheet) can reuse the labels without pulling that
// component's form/mutation dependencies into their bundle.

export const REQUEST_TYPE_GROUPS = [
	{
		label: "对话 / 文本 / 响应",
		types: ["chat_completion", "text_completion", "responses"],
	},
	{
		label: "Embedding",
		types: ["embedding"],
	},
	{
		label: "重排序",
		types: ["rerank"],
	},
	{
		label: "音频",
		types: ["speech", "transcription"],
	},
	{
		label: "图片",
		types: ["image_generation", "image_variation", "image_edit"],
	},
	{
		label: "视频",
		types: ["video_generation", "video_remix"],
	},
	{
		label: "OCR",
		types: ["ocr"],
	},
] as const;

export const REQUEST_TYPE_OPTIONS = REQUEST_TYPE_GROUPS.flatMap((g) => g.types);

export function getRequestTypeGroup(rt: string): string | undefined {
	return REQUEST_TYPE_GROUPS.find((g) => (g.types as readonly string[]).includes(rt))?.label;
}

export const PRICING_FIELDS = [
	// Chat / Text / Responses fields
	{
		key: "input_cost_per_token",
		label: "输入 / token",
		group: "chat",
		requestTypeGroups: ["chat", "embedding", "rerank", "audio", "image", "video"],
	},
	{
		key: "output_cost_per_token",
		label: "输出 / token",
		group: "chat",
		requestTypeGroups: ["chat", "rerank", "audio", "image", "video"],
	},
	{
		key: "input_cost_per_token_batches",
		label: "输入 / token（batch）",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "output_cost_per_token_batches",
		label: "输出 / token（batch）",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "input_cost_per_token_priority",
		label: "输入 / token（priority）",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "output_cost_per_token_priority",
		label: "输出 / token（priority）",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "input_cost_per_token_flex",
		label: "输入 / token（flex）",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "output_cost_per_token_flex",
		label: "输出 / token（flex）",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "input_cost_per_token_fast",
		label: "输入 / token（fast）",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "output_cost_per_token_fast",
		label: "输出 / token（fast）",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "input_cost_per_token_above_128k_tokens",
		label: "输入 / token（>128k）",
		group: "chat",
		requestTypeGroups: ["chat", "embedding", "rerank"],
	},
	{
		key: "output_cost_per_token_above_128k_tokens",
		label: "输出 / token（>128k）",
		group: "chat",
		requestTypeGroups: ["chat", "rerank", "audio"],
	},
	{
		key: "input_cost_per_token_above_200k_tokens",
		label: "输入 / token（>200k）",
		group: "chat",
		requestTypeGroups: ["chat", "embedding", "rerank"],
	},
	{
		key: "input_cost_per_token_above_200k_tokens_priority",
		label: "输入 / token（>200k, priority）",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "output_cost_per_token_above_200k_tokens",
		label: "输出 / token（>200k）",
		group: "chat",
		requestTypeGroups: ["chat", "rerank", "audio"],
	},
	{
		key: "output_cost_per_token_above_200k_tokens_priority",
		label: "输出 / token（>200k, priority）",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "input_cost_per_token_above_272k_tokens",
		label: "输入 / token（>272k）",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "input_cost_per_token_above_272k_tokens_priority",
		label: "输入 / token（>272k, priority）",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "output_cost_per_token_above_272k_tokens",
		label: "输出 / token（>272k）",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "output_cost_per_token_above_272k_tokens_priority",
		label: "输出 / token（>272k, priority）",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "input_cost_per_token_flex_above_272k_tokens",
		label: "输入 / token（>272k, flex）",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "output_cost_per_token_flex_above_272k_tokens",
		label: "输出 / token（>272k, flex）",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_creation_input_token_cost",
		label: "缓存创建 / token",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_read_input_token_cost",
		label: "缓存读取 / token",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_creation_input_token_cost_above_200k_tokens",
		label: "缓存创建 / token（>200k）",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_read_input_token_cost_above_200k_tokens",
		label: "缓存读取 / token（>200k）",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_creation_input_token_cost_above_1hr",
		label: "缓存创建 / token（>1hr）",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_creation_input_token_cost_above_1hr_above_200k_tokens",
		label: "缓存创建 / token（>1hr, >200k）",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_read_input_token_cost_priority",
		label: "缓存读取 / token（priority）",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_read_input_token_cost_flex",
		label: "缓存读取 / token（flex）",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_creation_input_token_cost_fast",
		label: "缓存创建 / token（fast）",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_creation_input_token_cost_above_1hr_fast",
		label: "缓存创建 / token（>1hr, fast）",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_read_input_token_cost_fast",
		label: "缓存读取 / token（fast）",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_read_input_token_cost_above_200k_tokens_priority",
		label: "缓存读取 / token（>200k, priority）",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_read_input_token_cost_above_272k_tokens",
		label: "缓存读取 / token（>272k）",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_read_input_token_cost_above_272k_tokens_priority",
		label: "缓存读取 / token（>272k, priority）",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_read_input_token_cost_flex_above_272k_tokens",
		label: "缓存读取 / token（>272k, flex）",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_creation_input_token_cost_priority",
		label: "缓存创建 / token（priority）",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_creation_input_token_cost_flex",
		label: "缓存创建 / token（flex）",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_creation_input_token_cost_above_272k_tokens",
		label: "缓存创建 / token（>272k）",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_creation_input_token_cost_flex_above_272k_tokens",
		label: "缓存创建 / token（>272k, flex）",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "search_context_cost_per_query",
		label: "搜索上下文 / 查询",
		group: "chat",
		requestTypeGroups: ["chat", "rerank"],
	},
	{
		key: "code_interpreter_cost_per_session",
		label: "代码解释器 / 会话",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "inference_geo_us_multiplier",
		label: "推理地理（美国）倍数",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cost_per_request",
		label: "固定费用 / 请求",
		group: "chat",
		requestTypeGroups: ["chat", "embedding", "rerank", "audio", "image", "video", "ocr"],
	},
	// Audio fields
	{
		key: "input_cost_per_character",
		label: "输入 / 字符",
		group: "audio",
		requestTypeGroups: ["audio"],
	},
	{
		key: "input_cost_per_audio_token",
		label: "输入 / 音频 token",
		group: "audio",
		requestTypeGroups: ["audio"],
	},
	{
		key: "input_cost_per_audio_per_second",
		label: "输入 / 音频秒",
		group: "audio",
		requestTypeGroups: ["audio"],
	},
	{
		key: "input_cost_per_audio_per_second_above_128k_tokens",
		label: "输入 / 音频秒（>128k）",
		group: "audio",
		requestTypeGroups: ["audio"],
	},
	{
		key: "input_cost_per_second",
		label: "输入 / 秒",
		group: "audio",
		requestTypeGroups: ["audio", "video"],
	},
	{
		key: "output_cost_per_audio_token",
		label: "输出 / 音频 token",
		group: "audio",
		requestTypeGroups: ["audio"],
	},
	{
		key: "output_cost_per_second",
		label: "输出 / 秒",
		group: "audio",
		requestTypeGroups: ["audio", "video"],
	},
	{
		key: "cache_creation_input_audio_token_cost",
		label: "缓存创建 / 音频 token",
		group: "audio",
		requestTypeGroups: ["audio"],
	},
	// Image fields
	{
		key: "input_cost_per_image_token",
		label: "输入 / 图片 token",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "input_cost_per_image",
		label: "输入 / 图片",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "input_cost_per_image_above_128k_tokens",
		label: "输入 / 图片（>128k）",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "input_cost_per_pixel",
		label: "输入 / 像素",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_token",
		label: "输出 / 图片 token",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image",
		label: "输出 / 图片",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_pixel",
		label: "输出 / 像素",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_premium_image",
		label: "输出 / 图片（premium）",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_512_and_512_pixels",
		label: "输出 / 图片（>512px）",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_512_and_512_pixels_and_premium_image",
		label: "输出 / 图片（>512px, premium）",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_1024_and_1024_pixels",
		label: "输出 / 图片（>1024px）",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_1024_and_1024_pixels_and_premium_image",
		label: "输出 / 图片（>1024px, premium）",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_2048_and_2048_pixels",
		label: "输出 / 图片（>2048px）",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_4096_and_4096_pixels",
		label: "输出 / 图片（>4096px）",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_low_quality",
		label: "输出 / 图片（低质量）",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_medium_quality",
		label: "输出 / 图片（中等质量）",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_high_quality",
		label: "输出 / 图片（高质量）",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_auto_quality",
		label: "输出 / 图片（自动质量）",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "cache_read_input_image_token_cost",
		label: "缓存读取 / 图片 token",
		group: "image",
		requestTypeGroups: ["image"],
	},
	// Video fields
	{
		key: "input_cost_per_video_per_second",
		label: "输入 / 视频秒",
		group: "video",
		requestTypeGroups: ["video"],
	},
	{
		key: "input_cost_per_video_per_second_above_128k_tokens",
		label: "输入 / 视频秒（>128k）",
		group: "video",
		requestTypeGroups: ["video"],
	},
	{
		key: "output_cost_per_video_per_second",
		label: "输出 / 视频秒",
		group: "video",
		requestTypeGroups: ["video"],
	},
	// OCR fields
	{
		key: "ocr_cost_per_page",
		label: "OCR / 页",
		group: "ocr",
		requestTypeGroups: ["ocr"],
	},
	{
		key: "annotation_cost_per_page",
		label: "注释 / 页",
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

export const fieldLabelByKey = Object.fromEntries(PRICING_FIELDS.map((field) => [field.key, field.label])) as Record<
	PricingFieldKey,
	string
>;
export const patchKeys = PRICING_FIELDS.map((field) => field.key) as PricingFieldKey[];

export type FieldErrors = Partial<Record<PricingFieldKey | "name" | "scope" | "pattern" | "patch", string>>;