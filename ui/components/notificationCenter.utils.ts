import type { SidekiqJob } from "@/lib/types/sidekiq";

/**
 * True when the backend has told us notifications can never be served, as
 * opposed to having failed this once.
 *
 * NotificationService is constructed with the config store, and lib.Config
 * leaves ConfigStore nil whenever the store is disabled, so both routes answer
 * 503 for the whole life of the process. That is a supported deployment, not an
 * outage, and the UI should hide the feature instead of showing an error the
 * user can never clear. Every other failure stays retryable.
 */
export function isNotificationsUnavailable(error: unknown): boolean {
	return typeof error === "object" && error !== null && "status" in error && (error as { status?: unknown }).status === 503;
}

/**
 * Whether the topbar trigger should be hidden entirely.
 *
 * Empty deployments should not flash an icon, so the trigger stays out of the
 * topbar while the first load is in flight and whenever the feed is genuinely
 * empty. A failed load is not one of those: with no rows to fall back on it is
 * the only state that can reach the tray's "Try again" retry, so hiding it
 * there strands the user on a feed that silently stays empty until a reload.
 *
 * `open` overrides all of it, so dismissing the last row does not yank the
 * popover out from under the pointer.
 */
export function shouldHideNotificationTrigger({ open, isLoading }: { open: boolean; isLoading: boolean }): boolean {
	if (open) return false;
	if (isLoading) return true;
	return false;
}

/** How often the jobs list is refreshed while the tray is open or a job is active. */
export const SIDEKIQ_ACTIVE_POLL_MS = 3000;
/** Slow refresh used otherwise, so a job started elsewhere is still noticed. */
export const SIDEKIQ_IDLE_POLL_MS = 15000;

export function sidekiqPollInterval({ open, activeCount }: { open: boolean; activeCount: number }): number {
	return open || activeCount > 0 ? SIDEKIQ_ACTIVE_POLL_MS : SIDEKIQ_IDLE_POLL_MS;
}

/**
 * True when the jobs endpoint will never serve this caller: 403 (not an administrator)
 * or 503 (no config store / job runner). Both hide the jobs section instead of
 * surfacing an error the user cannot clear.
 */
export function isSidekiqJobsUnavailable(error: unknown): boolean {
	if (typeof error !== "object" || error === null || !("status" in error)) return false;
	const status = (error as { status?: unknown }).status;
	return status === 403 || status === 503;
}

export function countActiveSidekiqJobs(jobs: Pick<SidekiqJob, "status">[]): number {
	return jobs.filter((job) => job.status === "pending" || job.status === "running").length;
}

const SIDEKIQ_JOB_LABELS: Record<string, string> = {
	logs_recalculate_cost: "Recalculating log costs",
	warp_log_embedding_backfill: "Indexing logs for Warp",
	vk_expiry_cleanup: "Virtual key expiry cleanup",
};

/** Friendly name for a job kind; unknown kinds fall back to a humanized kind string. */
export function sidekiqJobLabel(kind: string): string {
	const known = SIDEKIQ_JOB_LABELS[kind];
	if (known) return known;
	const words = kind.replace(/[_-]+/g, " ").trim();
	return words ? words.charAt(0).toUpperCase() + words.slice(1) : "Background job";
}

/**
 * Whole-number completion percentage, or undefined when the job reports no usable total
 * (the bar is then omitted). Totals are approximate, so the value is clamped to 100, and a
 * completed job always reads 100.
 */
export function sidekiqJobPercent(job: Pick<SidekiqJob, "status" | "progress">): number | undefined {
	const progress = job.progress;
	if (!progress || progress.total <= 0) return undefined;
	if (job.status === "completed") return 100;
	return Math.min(100, Math.max(0, Math.round((progress.done / progress.total) * 100)));
}

/** Jobs the user has not dismissed from the tray. Dismissal is per browser, like notifications. */
export function visibleSidekiqJobs<T extends Pick<SidekiqJob, "id">>(jobs: T[], dismissedIds: string[]): T[] {
	if (dismissedIds.length === 0) return jobs;
	const dismissed = new Set(dismissedIds);
	return jobs.filter((job) => !dismissed.has(job.id));
}

/** IDs of jobs that have reached a terminal status, i.e. the ones "Clear finished" removes. */
export function finishedSidekiqJobIds(jobs: Pick<SidekiqJob, "id" | "status">[]): string[] {
	return jobs.filter((job) => job.status !== "pending" && job.status !== "running").map((job) => job.id);
}