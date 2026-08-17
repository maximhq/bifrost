import type { WarpConfigInput } from "@/lib/types/warp";
/**
 * Validation helpers for the Warp settings form.
 *
 * These live outside the component because the form itself has no render
 * harness in this repo, and a rule that cannot be tested is a rule that quietly
 * stops holding.
 */

/**
 * Rejects a numeric field that was cleared rather than filled in.
 *
 * `valueAsNumber: true` hands React Hook Form `NaN` for an empty input, and RHF
 * treats the empty DOM value as "no value" and skips `min`/`max` entirely. With
 * nothing else checking, `NaN` reaches the PUT body, where `JSON.stringify`
 * writes it as `null` - so clearing the box silently sends null for a field the
 * server reads as a number.
 */
export function requireFiniteNumber(value: unknown, message: string): true | string {
	return typeof value === "number" && Number.isFinite(value) ? true : message;
}
/**
 * Days to keep saved chats.
 *
 * Zero is a supported value, not a missing one: the server reads 0 as "use the
 * default" (`WarpDefaultHistoryRetentionDays`), which is exactly how a config
 * that never set the field behaves. A `min: 1` rule made that documented value
 * unreachable, so once an operator had typed a number they could not get back
 * to default retention from the form at all.
 *
 * No upper bound: the per-owner conversation cap already limits the table, so
 * how long a transcript stays readable is a policy choice with no ceiling.
 */
export function validateWarpRetentionDays(value: unknown): true | string {
	if (typeof value !== "number" || !Number.isFinite(value)) return "A value is required";
	if (!Number.isInteger(value)) return "Must be a whole number of days";
	if (value < 0) return "Must be 0 or more days (0 keeps the default)";
	return true;
}

/**
 * Validates the Base URL as it will actually be submitted.
 *
 * The inline rule checked the raw field while `buildWarpConfigPayload` trims it,
 * so a pasted `" https://api.example.com "` was rejected by the form and would
 * have been accepted by the server. The userinfo rule mirrors
 * `ValidateConfigInput`: that column is stored and returned unredacted, so a
 * credential in the URL is a 400 - worth saying here rather than letting the
 * save fail with the server's wording.
 */
export function validateWarpBaseURL(value: string | undefined): true | string {
	const trimmed = (value ?? "").trim();
	if (!trimmed) return true;
	if (!trimmed.startsWith("http://") && !trimmed.startsWith("https://")) {
		return "URL must start with http:// or https://";
	}
	try {
		if (new URL(trimmed).username || new URL(trimmed).password) {
			return "Base URL must not contain credentials. Use the API Key field to name a configured provider key.";
		}
	} catch {
		return "URL must start with http:// or https://";
	}
	return true;
}

/**
 * The subset of the Warp form this module builds a payload from.
 *
 * Structural so the rule can be tested without rendering: the form itself has
 * no harness in this repo.
 */
export interface WarpConfigPayloadFields {
	enabled: boolean;
	provider: string;
	model: string;
	base_url: string;
	api_key_id: string;
	max_iterations: number;
	request_timeout_seconds: number;
	history_retention_days: number;
	system_prompt_suffix: string;
}

/**
 * Builds the PUT body.
 *
 * `api_key_id` round-trips like every other field. It names one of the
 * deployment's already-configured provider keys - a reference, not a secret -
 * so there is none of the omitted-versus-empty ambiguity a write-only credential
 * forces, and no reason to withhold it from an ordinary save.
 */
export function buildWarpConfigPayload(form: WarpConfigPayloadFields): WarpConfigInput {
	return {
		enabled: form.enabled,
		provider: form.provider.trim(),
		model: form.model.trim(),
		base_url: form.base_url.trim(),
		api_key_id: form.api_key_id.trim(),
		max_iterations: form.max_iterations,
		request_timeout_seconds: form.request_timeout_seconds,
		history_retention_days: form.history_retention_days,
		system_prompt_suffix: form.system_prompt_suffix,
	};
}