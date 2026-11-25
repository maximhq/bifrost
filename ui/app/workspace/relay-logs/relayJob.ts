import i18n from "@/lib/i18n";
import type { RelayLog } from "@/lib/types/relays";

export type RelayJobOutcome = "succeeded" | "failed" | "unpriced";

/** How a job row ended. Rows written before job_debug carried an outcome are read off their cost. */
export function relayJobOutcome(log: Pick<RelayLog, "job_debug" | "cost">): RelayJobOutcome {
	const outcome = log.job_debug?.outcome;
	if (outcome === "succeeded" || outcome === "failed" || outcome === "unpriced") return outcome;
	return log.cost == null ? "unpriced" : "succeeded";
}

/**
 * When a job row's job ended. The row's own time is the start call's, kept stable for log dedupe, so
 * this is what shows as the job's end; null for other rows and rows from before job_debug.
 */
export function relayJobSettledAt(log: Pick<RelayLog, "kind" | "job_debug">): Date | null {
	if (log.kind !== "job" || !log.job_debug?.settled_at) return null;
	const at = new Date(log.job_debug.settled_at);
	return Number.isNaN(at.getTime()) ? null : at;
}

const JOB_REASONS = [
	"relay_job_params",
	"relay_job_timeout",
	"relay_job_gone",
	"relay_job_access_denied",
	"relay_job_rejected",
	"relay_job_no_usage",
	"relay_job_row_missing",
] as const;
type JobReason = (typeof JOB_REASONS)[number];

/** Why an unpriced job row has no price, translated; an unknown code is shown as recorded. */
export function relayJobReason(log: Pick<RelayLog, "error_details">): string {
	const type = log.error_details?.error?.type;
	if (type && (JOB_REASONS as readonly string[]).includes(type))
		return i18n.t(`relayLogs.jobReasons.${type as JobReason}`, { ns: "observability" });
	return type ?? log.error_details?.error?.message ?? "";
}

export const RELAY_JOB_OUTCOME_CLASS: Record<RelayJobOutcome, string> = {
	succeeded: "bg-green-100 text-green-800 dark:bg-green-900 dark:text-green-200",
	failed: "bg-red-100 text-red-800 dark:bg-red-900 dark:text-red-200",
	unpriced: "bg-amber-100 text-amber-800 dark:bg-amber-900 dark:text-amber-200",
};

/** The status bar colour a job row's outcome maps to in the list. */
export const RELAY_JOB_OUTCOME_BAR: Record<RelayJobOutcome, string> = {
	succeeded: "bg-green-500",
	failed: "bg-red-500",
	unpriced: "bg-amber-500",
};