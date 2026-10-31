import { PRICING_FIELDS } from "@/app/workspace/custom-pricing/overrides/pricingFields";
import type { DatasheetOverride, SaveDatasheetOverrideRequest } from "@/lib/store/apis/providersApi";
import type { DatasheetOverrideFormSchema } from "@/lib/types/schemas";

// Field catalog and form <-> patch logic for the datasheet editor. Kept free of
// React so the patch round trip is unit tested on its own.

export const LIMIT_FIELDS = ["context_length", "max_input_tokens", "max_output_tokens"] as const;
export type LimitField = (typeof LIMIT_FIELDS)[number];

// The capability flags the editor renders as toggles. Every other
// model-parameters key stays editable through the advanced JSON patch.
export const CAPABILITY_FLAGS = [
	"supports_function_calling",
	"supports_parallel_function_calling",
	"supports_tool_choice",
	"supports_response_schema",
	"supports_system_messages",
	"supports_reasoning",
	"supports_reasoning_effort",
	"supports_prompt_caching",
	"supports_web_search",
	"supports_code_execution",
	"supports_files_api",
	"supports_mcp",
] as const;
export type CapabilityFlag = (typeof CAPABILITY_FLAGS)[number];

// Datasheet modes a custom entry can be created for.
export const DATASHEET_MODES = [
	"chat",
	"responses",
	"completion",
	"embedding",
	"rerank",
	"image_generation",
	"image_edit",
	"audio_speech",
	"audio_transcription",
	"video_generation",
	"ocr",
] as const;

/** "upstream" leaves the key out of the patch. */
export type TriState = "upstream" | "on" | "off";

/**
 * The editor form. otherParamsJSON holds params patch keys without a dedicated
 * control; preservedPricing carries pricing patch keys without one
 * (peak_hours, architecture, explicit nulls) verbatim.
 */
export type DatasheetFormState = DatasheetOverrideFormSchema;

const PRICING_FIELD_KEYS = new Set<string>(PRICING_FIELDS.map((f) => f.key));
const LIMIT_FIELD_KEYS = new Set<string>(LIMIT_FIELDS);
const FLAG_KEYS = new Set<string>(CAPABILITY_FLAGS);

export const emptyLimits = (): Record<LimitField, string> => ({ context_length: "", max_input_tokens: "", max_output_tokens: "" });
export const upstreamFlags = (): Record<string, TriState> => Object.fromEntries(CAPABILITY_FLAGS.map((flag) => [flag, "upstream"]));

const toTriState = (value: unknown): TriState => (value === true ? "on" : value === false ? "off" : "upstream");

/** Builds the form for a target, filled from its saved override when there is one. */
export function formStateFromOverride(
	target: { provider: string; model: string; mode: string; isCustom: boolean },
	override?: DatasheetOverride,
): DatasheetFormState {
	const form: DatasheetFormState = {
		...target,
		limits: emptyLimits(),
		deprecated: "upstream",
		pricingValues: {},
		flags: upstreamFlags(),
		otherParamsJSON: "",
		preservedPricing: {},
	};
	for (const [key, value] of Object.entries(override?.pricing_patch ?? {})) {
		if (LIMIT_FIELD_KEYS.has(key) && typeof value === "number") {
			form.limits[key as LimitField] = String(value);
		} else if (key === "is_deprecated" && typeof value === "boolean") {
			form.deprecated = toTriState(value);
		} else if (PRICING_FIELD_KEYS.has(key) && typeof value === "number") {
			form.pricingValues[key] = String(value);
		} else {
			form.preservedPricing[key] = value;
		}
	}
	const otherParams: Record<string, unknown> = {};
	for (const [key, value] of Object.entries(override?.params_patch ?? {})) {
		if (FLAG_KEYS.has(key) && typeof value === "boolean") {
			form.flags[key] = toTriState(value);
		} else if (key === "max_output_tokens" && String(value) === form.limits.max_output_tokens) {
			// Mirrored from the limits section on save; not a separate params edit.
		} else {
			otherParams[key] = value;
		}
	}
	if (Object.keys(otherParams).length > 0) form.otherParamsJSON = JSON.stringify(otherParams, null, 2);
	return form;
}

/** Parses the advanced params JSON; empty text is an empty patch. Returns null when it is not a JSON object. */
export function parseOtherParams(text: string): Record<string, unknown> | null {
	if (text.trim() === "") return {};
	try {
		const parsed: unknown = JSON.parse(text);
		return parsed !== null && typeof parsed === "object" && !Array.isArray(parsed) ? (parsed as Record<string, unknown>) : null;
	} catch {
		return null;
	}
}

/**
 * Turns the form into the save request. Empty inputs and "upstream" states are
 * left out of the patches, so they keep following the datasheet. A max output
 * tokens limit is written to both patches: the pricing row and the
 * model-parameters entry each carry one, and providers read the latter.
 * Assumes the form passed validation.
 */
export function buildSaveRequest(form: DatasheetFormState): SaveDatasheetOverrideRequest {
	const pricing: Record<string, unknown> = { ...form.preservedPricing };
	for (const field of LIMIT_FIELDS) {
		const raw = form.limits[field].trim();
		if (raw !== "") pricing[field] = Number(raw);
	}
	if (form.deprecated !== "upstream") pricing.is_deprecated = form.deprecated === "on";
	for (const [key, raw] of Object.entries(form.pricingValues)) {
		if (raw != null && raw.trim() !== "") pricing[key] = Number(raw);
	}

	const params: Record<string, unknown> = { ...(parseOtherParams(form.otherParamsJSON) ?? {}) };
	for (const flag of CAPABILITY_FLAGS) {
		if (form.flags[flag] !== "upstream") params[flag] = form.flags[flag] === "on";
	}
	if (pricing.max_output_tokens !== undefined) params.max_output_tokens = pricing.max_output_tokens;

	return {
		provider: form.provider.trim(),
		model: form.model.trim(),
		mode: form.mode.trim(),
		...(form.isCustom && { is_custom: true }),
		pricing_patch: pricing,
		params_patch: params,
	};
}

/** Splits drifted_fields ("pricing.x", "params.y") into the bare keys per patch. */
export function splitDriftedFields(drifted: string[] | undefined): { pricing: Set<string>; params: Set<string> } {
	const pricing = new Set<string>();
	const params = new Set<string>();
	for (const field of drifted ?? []) {
		if (field.startsWith("pricing.")) pricing.add(field.slice("pricing.".length));
		else if (field.startsWith("params.")) params.add(field.slice("params.".length));
	}
	return { pricing, params };
}

/** The override saved for exactly this mode; mode-wide ones (config.json) are shown read-only. */
export function overrideForMode(overrides: DatasheetOverride[] | undefined, mode: string): DatasheetOverride | undefined {
	return overrides?.find((o) => o.mode === mode);
}

/** Renders a datasheet value for "upstream: …" hints. */
export function formatDatasheetValue(value: unknown, locale?: string): string {
	if (value === undefined || value === null) return "—";
	if (typeof value === "number") return value.toLocaleString(locale, { maximumSignificantDigits: 12 });
	if (typeof value === "boolean") return String(value);
	if (typeof value === "string") return value;
	return JSON.stringify(value);
}