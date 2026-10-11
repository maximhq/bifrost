import type { WarpBackfillJob, WarpConfig } from "@/lib/types/warp";
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
	return isFiniteNumber(value) ? true : message;
}

/**
 * Narrows to a real, comparable number.
 *
 * The form parses its numeric inputs with `Number(...)`, and every comparison
 * against `NaN` is false - so a range check alone reports a non-numeric value
 * as valid. This is the guard that has to run before the range check, not after.
 */
export function isFiniteNumber(value: unknown): value is number {
	return typeof value === "number" && Number.isFinite(value);
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
 * The saved embedding space as one comparable string, so "did the space
 * change" is a single equality on the four fields that define it. Uses NUL as
 * the separator because it cannot appear in a config value.
 */
export function warpSavedSpaceKey(
	config:
		| Partial<Pick<WarpConfig, "embedding_provider" | "embedding_model" | "embedding_dimension" | "log_vector_store_namespace">>
		| undefined,
): string {
	return [
		config?.embedding_provider ?? "",
		config?.embedding_model ?? "",
		config?.embedding_dimension ?? 0,
		config?.log_vector_store_namespace ?? "",
	].join("\u0000");
}

/** A finished backfill kept on the client, tagged with the space it ran under. */
export interface RetainedWarpBackfill {
	job: WarpBackfillJob;
	spaceKey: string;
}

/**
 * Decides whether a job that just reached a terminal status is worth keeping.
 *
 * A job belongs to the space that was configured while it ran; the server
 * refuses to change the space while a job is in flight, so any saved space
 * observed during the run is the job's space. The status endpoint hides a
 * finished job from the id-less read once the space changes, but the id-pinned
 * poll the view uses during a run does not, so a run that finished under the
 * old space can still arrive as "completed" after the new space was saved.
 * Keeping it would show a full bar for rows the new space never indexed, so it
 * is dropped unless the job's space is still the saved one. A job whose space
 * was never observed (null) is assumed to belong to the current space: the
 * only way to pin an id is to start or discover a running job, and both
 * happen under the configured space.
 */
export function retainFinishedWarpBackfill(
	job: WarpBackfillJob,
	jobSpaceKey: string | null,
	savedSpaceKey: string,
): RetainedWarpBackfill | null {
	const spaceKey = jobSpaceKey ?? savedSpaceKey;
	return spaceKey === savedSpaceKey ? { job, spaceKey } : null;
}

/**
 * The retained job, if it still describes the saved space. Checked at every
 * read rather than cleared by an effect on change: an effect fires once, and a
 * terminal status that lands after it has fired would be retained untouched.
 */
export function retainedWarpBackfillForSpace(retained: RetainedWarpBackfill | null, savedSpaceKey: string): WarpBackfillJob | null {
	return retained && retained.spaceKey === savedSpaceKey ? retained.job : null;
}