import type { RequestType } from "@/lib/types/config";
import type { PricingOverrideMatchType, PricingOverridePatch } from "@/lib/types/governance";

// Shared pricing-field metadata for override editing and display.
//
// Extracted from pricingOverrideSheet.tsx so read-only consumers (e.g. the
// model-catalog detail sheet) can reuse the labels without pulling that
// component's form/mutation dependencies into their bundle.

export const REQUEST_TYPE_GROUPS = [
	{
		label: "Chat / Text / Responses",
		types: ["chat_completion", "text_completion", "responses"],
	},
	{
		label: "Embedding",
		types: ["embedding"],
	},
	{
		label: "Rerank",
		types: ["rerank"],
	},
	{
		label: "Audio",
		types: ["speech", "transcription"],
	},
	{
		label: "Image",
		types: ["image_generation", "image_variation", "image_edit"],
	},
	{
		label: "Video",
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
		label: "Input / token",
		group: "chat",
		requestTypeGroups: ["chat", "embedding", "rerank", "audio", "image", "video"],
	},
	{
		key: "output_cost_per_token",
		label: "Output / token",
		group: "chat",
		requestTypeGroups: ["chat", "rerank", "audio", "image", "video"],
	},
	{
		key: "input_cost_per_token_batches",
		label: "Input / token (batch)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "output_cost_per_token_batches",
		label: "Output / token (batch)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "input_cost_per_token_priority",
		label: "Input / token (priority)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "output_cost_per_token_priority",
		label: "Output / token (priority)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "input_cost_per_token_ultrafast",
		label: "Input / token (ultrafast)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "output_cost_per_token_ultrafast",
		label: "Output / token (ultrafast)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "input_cost_per_token_flex",
		label: "Input / token (flex)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "output_cost_per_token_flex",
		label: "Output / token (flex)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "input_cost_per_token_fast",
		label: "Input / token (fast)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "output_cost_per_token_fast",
		label: "Output / token (fast)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "input_cost_per_token_above_128k_tokens",
		label: "Input / token (>128k)",
		group: "chat",
		requestTypeGroups: ["chat", "embedding", "rerank"],
	},
	{
		key: "output_cost_per_token_above_128k_tokens",
		label: "Output / token (>128k)",
		group: "chat",
		requestTypeGroups: ["chat", "rerank", "audio"],
	},
	{
		key: "input_cost_per_token_above_200k_tokens",
		label: "Input / token (>200k)",
		group: "chat",
		requestTypeGroups: ["chat", "embedding", "rerank"],
	},
	{
		key: "input_cost_per_token_above_200k_tokens_priority",
		label: "Input / token (>200k, priority)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "output_cost_per_token_above_200k_tokens",
		label: "Output / token (>200k)",
		group: "chat",
		requestTypeGroups: ["chat", "rerank", "audio"],
	},
	{
		key: "output_cost_per_token_above_200k_tokens_priority",
		label: "Output / token (>200k, priority)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "input_cost_per_token_above_272k_tokens",
		label: "Input / token (>272k)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "input_cost_per_token_above_272k_tokens_priority",
		label: "Input / token (>272k, priority)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "output_cost_per_token_above_272k_tokens",
		label: "Output / token (>272k)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "output_cost_per_token_above_272k_tokens_priority",
		label: "Output / token (>272k, priority)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "input_cost_per_token_flex_above_272k_tokens",
		label: "Input / token (>272k, flex)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "output_cost_per_token_flex_above_272k_tokens",
		label: "Output / token (>272k, flex)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_creation_input_token_cost",
		label: "Cache creation / token",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_read_input_token_cost",
		label: "Cache read / token",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_creation_input_token_cost_above_200k_tokens",
		label: "Cache creation / token (>200k)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_read_input_token_cost_above_200k_tokens",
		label: "Cache read / token (>200k)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_creation_input_token_cost_above_1hr",
		label: "Cache creation / token (>1hr)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_creation_input_token_cost_above_1hr_above_200k_tokens",
		label: "Cache creation / token (>1hr, >200k)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_read_input_token_cost_priority",
		label: "Cache read / token (priority)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_read_input_token_cost_ultrafast",
		label: "Cache read / token (ultrafast)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_read_input_token_cost_flex",
		label: "Cache read / token (flex)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_creation_input_token_cost_fast",
		label: "Cache creation / token (fast)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_creation_input_token_cost_above_1hr_fast",
		label: "Cache creation / token (>1hr, fast)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_read_input_token_cost_fast",
		label: "Cache read / token (fast)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_read_input_token_cost_above_200k_tokens_priority",
		label: "Cache read / token (>200k, priority)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_read_input_token_cost_above_272k_tokens",
		label: "Cache read / token (>272k)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_read_input_token_cost_above_272k_tokens_priority",
		label: "Cache read / token (>272k, priority)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_read_input_token_cost_flex_above_272k_tokens",
		label: "Cache read / token (>272k, flex)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_creation_input_token_cost_priority",
		label: "Cache creation / token (priority)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_creation_input_token_cost_ultrafast",
		label: "Cache creation / token (ultrafast)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_creation_input_token_cost_flex",
		label: "Cache creation / token (flex)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_creation_input_token_cost_above_272k_tokens",
		label: "Cache creation / token (>272k)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cache_creation_input_token_cost_flex_above_272k_tokens",
		label: "Cache creation / token (>272k, flex)",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "search_context_cost_per_query",
		label: "Search context / query",
		group: "chat",
		requestTypeGroups: ["chat", "rerank"],
	},
	{
		key: "input_cost_per_query",
		label: "Rerank / query",
		group: "chat",
		requestTypeGroups: ["rerank"],
	},
	{
		key: "code_interpreter_cost_per_session",
		label: "Code interpreter / session",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "inference_geo_us_multiplier",
		label: "Inference geo US multiplier",
		group: "chat",
		requestTypeGroups: ["chat"],
	},
	{
		key: "cost_per_request",
		label: "Flat fee / request",
		group: "chat",
		requestTypeGroups: ["chat", "embedding", "rerank", "audio", "image", "video", "ocr"],
	},
	{
		key: "off_peak_cost_multiplier",
		label: "Off-peak multiplier",
		group: "chat",
		requestTypeGroups: ["chat", "embedding", "rerank", "audio", "image", "video", "ocr"],
	},
	// Audio fields
	{
		key: "input_cost_per_character",
		label: "Input / character",
		group: "audio",
		requestTypeGroups: ["audio"],
	},
	{
		key: "input_cost_per_audio_token",
		label: "Input / audio token",
		group: "audio",
		requestTypeGroups: ["audio"],
	},
	{
		key: "input_cost_per_audio_per_second",
		label: "Input / audio second",
		group: "audio",
		requestTypeGroups: ["audio"],
	},
	{
		key: "input_cost_per_audio_per_second_above_128k_tokens",
		label: "Input / audio second (>128k)",
		group: "audio",
		requestTypeGroups: ["audio"],
	},
	{
		key: "input_cost_per_second",
		label: "Input / second",
		group: "audio",
		requestTypeGroups: ["audio", "video"],
	},
	{
		key: "output_cost_per_audio_token",
		label: "Output / audio token",
		group: "audio",
		requestTypeGroups: ["audio"],
	},
	{
		key: "output_cost_per_second",
		label: "Output / second",
		group: "audio",
		requestTypeGroups: ["audio", "video"],
	},
	{
		key: "cache_creation_input_audio_token_cost",
		label: "Cache creation / audio token",
		group: "audio",
		requestTypeGroups: ["audio"],
	},
	// Image fields
	{
		key: "input_cost_per_image_token",
		label: "Input / image token",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "input_cost_per_image",
		label: "Input / image",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "input_cost_per_image_above_128k_tokens",
		label: "Input / image (>128k)",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "input_cost_per_pixel",
		label: "Input / pixel",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_token",
		label: "Output / image token",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image",
		label: "Output / image",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_pixel",
		label: "Output / pixel",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_premium_image",
		label: "Output / image (premium)",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_512_and_512_pixels",
		label: "Output / image (>512px)",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_512_and_512_pixels_and_premium_image",
		label: "Output / image (>512px, premium)",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_1024_and_1024_pixels",
		label: "Output / image (>1024px)",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_1024_and_1024_pixels_and_premium_image",
		label: "Output / image (>1024px, premium)",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_1024_and_1536_pixels",
		label: "Output / image (>1024x1536px)",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_1536_and_1024_pixels",
		label: "Output / image (>1536x1024px)",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_2048_and_2048_pixels",
		label: "Output / image (>2048px)",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_4096_and_4096_pixels",
		label: "Output / image (>4096px)",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_4_megapixels",
		label: "Output / image (>4MP)",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_8_megapixels",
		label: "Output / image (>8MP)",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_16_megapixels",
		label: "Output / image (>16MP)",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_32_megapixels",
		label: "Output / image (>32MP)",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_64_megapixels",
		label: "Output / image (>64MP)",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_low_quality",
		label: "Output / image (low quality)",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_medium_quality",
		label: "Output / image (medium quality)",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_high_quality",
		label: "Output / image (high quality)",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_auto_quality",
		label: "Output / image (auto quality)",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_1024_and_1024_pixels_low_quality",
		label: "Output / image (>1024x1024px, low quality)",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_1024_and_1536_pixels_low_quality",
		label: "Output / image (>1024x1536px, low quality)",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_1536_and_1024_pixels_low_quality",
		label: "Output / image (>1536x1024px, low quality)",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_1024_and_1024_pixels_medium_quality",
		label: "Output / image (>1024x1024px, medium quality)",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_1024_and_1536_pixels_medium_quality",
		label: "Output / image (>1024x1536px, medium quality)",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_1536_and_1024_pixels_medium_quality",
		label: "Output / image (>1536x1024px, medium quality)",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_1024_and_1024_pixels_high_quality",
		label: "Output / image (>1024x1024px, high quality)",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_1024_and_1536_pixels_high_quality",
		label: "Output / image (>1024x1536px, high quality)",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_1536_and_1024_pixels_high_quality",
		label: "Output / image (>1536x1024px, high quality)",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_1024_and_1024_pixels_standard_quality",
		label: "Output / image (>1024x1024px, standard quality)",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_1024_and_1536_pixels_standard_quality",
		label: "Output / image (>1024x1536px, standard quality)",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "output_cost_per_image_above_1536_and_1024_pixels_standard_quality",
		label: "Output / image (>1536x1024px, standard quality)",
		group: "image",
		requestTypeGroups: ["image"],
	},
	{
		key: "cache_read_input_image_token_cost",
		label: "Cache read / image token",
		group: "image",
		requestTypeGroups: ["image"],
	},
	// Video fields
	{
		key: "input_cost_per_video_per_second",
		label: "Input / video second",
		group: "video",
		requestTypeGroups: ["video"],
	},
	{
		key: "input_cost_per_video_per_second_above_128k_tokens",
		label: "Input / video second (>128k)",
		group: "video",
		requestTypeGroups: ["video"],
	},
	{
		key: "output_cost_per_video_per_second",
		label: "Output / video second",
		group: "video",
		requestTypeGroups: ["video"],
	},
	{
		key: "output_cost_per_video_per_second_480p",
		label: "Output / video second (480p)",
		group: "video",
		requestTypeGroups: ["video"],
	},
	{
		key: "output_cost_per_video_per_second_720p",
		label: "Output / video second (720p)",
		group: "video",
		requestTypeGroups: ["video"],
	},
	{
		key: "output_cost_per_video_per_second_1024p",
		label: "Output / video second (1024p)",
		group: "video",
		requestTypeGroups: ["video"],
	},
	{
		key: "output_cost_per_video_per_second_1080p",
		label: "Output / video second (1080p)",
		group: "video",
		requestTypeGroups: ["video"],
	},
	{
		key: "output_cost_per_video_per_second_4k",
		label: "Output / video second (4K)",
		group: "video",
		requestTypeGroups: ["video"],
	},
	// OCR fields
	{
		key: "ocr_cost_per_page",
		label: "OCR / page",
		group: "ocr",
		requestTypeGroups: ["ocr"],
	},
	{
		key: "annotation_cost_per_page",
		label: "Annotation / page",
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

/**
 * Per-field numeric bounds for fields that are not plain "any non-negative
 * cost". Fields absent from this map keep the default `>= 0` rule.
 */
export const FIELD_BOUNDS: Partial<Record<PricingFieldKey, { min?: number; max?: number; message: string }>> = {
	// Every base rate is the peak price, so the off-peak multiplier can only
	// ever scale downward. A value of 0 would make off-peak requests free and a
	// value above 1 would make them cost more than peak; the pricing engine
	// rejects both and bills at peak, so reject them here too rather than
	// silently accepting a setting that will never take effect.
	off_peak_cost_multiplier: { min: 0, max: 1, message: "Must be greater than 0 and at most 1" },
};

/**
 * Validates one raw form value for a pricing field. Returns an error message,
 * or undefined when the value is acceptable. An empty value is not an error —
 * it means "do not override this field".
 */
export function pricingFieldError(key: PricingFieldKey, raw: string | undefined): string | undefined {
	if (raw == null || raw.trim() === "") return undefined;
	const parsed = Number(raw);
	if (!Number.isFinite(parsed)) return "Must be a number";

	const bounds = FIELD_BOUNDS[key];
	if (bounds) {
		if (bounds.min != null && parsed <= bounds.min) return bounds.message;
		if (bounds.max != null && parsed > bounds.max) return bounds.message;
		return undefined;
	}
	if (parsed < 0) return "Must be >= 0";
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

export function buildPatchFromForm(form: FormState): { patch: PricingOverridePatch; errors: FieldErrors } {
	const errors: FieldErrors = {};
	const patch: PricingOverridePatch = {};

	for (const [key, value] of Object.entries(form.preservedPatch ?? {})) {
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
