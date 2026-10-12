import { describe, expect, it } from "vitest";
import {
	countActiveSidekiqJobs,
	finishedSidekiqJobIds,
	isNotificationsUnavailable,
	isSidekiqJobsUnavailable,
	shouldHideNotificationTrigger,
	SIDEKIQ_ACTIVE_POLL_MS,
	SIDEKIQ_IDLE_POLL_MS,
	sidekiqJobLabel,
	sidekiqJobPercent,
	sidekiqPollInterval,
	visibleSidekiqJobs,
} from "./notificationCenter.utils";

describe("isNotificationsUnavailable", () => {
	it("treats 503 as the feature being switched off", () => {
		expect(isNotificationsUnavailable({ status: 503, data: { error: "notification storage is unavailable" } })).toBe(true);
	});

	it("leaves every other failure retryable", () => {
		expect(isNotificationsUnavailable({ status: 500 })).toBe(false);
		expect(isNotificationsUnavailable({ status: "FETCH_ERROR", error: "offline" })).toBe(false);
		expect(isNotificationsUnavailable({ name: "SyntaxError", message: "bad json" })).toBe(false);
		expect(isNotificationsUnavailable(undefined)).toBe(false);
		expect(isNotificationsUnavailable(null)).toBe(false);
	});
});

describe("shouldHideNotificationTrigger", () => {
	const state = (overrides: Partial<Parameters<typeof shouldHideNotificationTrigger>[0]> = {}) =>
		shouldHideNotificationTrigger({ open: false, isLoading: false, ...overrides });

	it("hides only while the first load is still in flight", () => {
		expect(state({ isLoading: true })).toBe(true);
	});

	// The regression: a first load that fails leaves nothing to fall back on, so
	// hiding the trigger here makes the tray's "Try again" unreachable and the
	// feed silently stays empty until the user reloads the page.
	it("keeps a settled empty feed reachable so it can be retried", () => {
		expect(state()).toBe(false);
	});

	it("stays mounted while the popover is open, whatever the feed says", () => {
		expect(state({ open: true })).toBe(false);
		// A retry in flight behind an open popover must not yank the trigger away.
		expect(state({ open: true, isLoading: true })).toBe(false);
	});
});

describe("sidekiq job helpers", () => {
	it("polls fast while the tray is open or a job is active, slowly otherwise", () => {
		expect(sidekiqPollInterval({ open: true, activeCount: 0 })).toBe(SIDEKIQ_ACTIVE_POLL_MS);
		expect(sidekiqPollInterval({ open: false, activeCount: 2 })).toBe(SIDEKIQ_ACTIVE_POLL_MS);
		expect(sidekiqPollInterval({ open: false, activeCount: 0 })).toBe(SIDEKIQ_IDLE_POLL_MS);
	});

	it("hides the jobs section for 403 and 503 only", () => {
		expect(isSidekiqJobsUnavailable({ status: 403 })).toBe(true);
		expect(isSidekiqJobsUnavailable({ status: 503 })).toBe(true);
		expect(isSidekiqJobsUnavailable({ status: 500 })).toBe(false);
		expect(isSidekiqJobsUnavailable({ status: "FETCH_ERROR" })).toBe(false);
		expect(isSidekiqJobsUnavailable(undefined)).toBe(false);
	});

	it("counts only pending and running jobs as active", () => {
		expect(
			countActiveSidekiqJobs([
				{ status: "pending" },
				{ status: "running" },
				{ status: "completed" },
				{ status: "failed" },
				{ status: "cancelled" },
			]),
		).toBe(2);
		expect(countActiveSidekiqJobs([])).toBe(0);
	});

	it("labels known kinds and humanizes unknown ones", () => {
		expect(sidekiqJobLabel("logs_recalculate_cost")).toBe("Recalculating log costs");
		expect(sidekiqJobLabel("some_new_kind")).toBe("Some new kind");
		expect(sidekiqJobLabel("")).toBe("Background job");
	});

	it("computes a clamped percentage, and none without a total", () => {
		expect(sidekiqJobPercent({ status: "running", progress: { done: 25, total: 100 } })).toBe(25);
		expect(sidekiqJobPercent({ status: "running", progress: { done: 150, total: 100 } })).toBe(100);
		expect(sidekiqJobPercent({ status: "running", progress: { done: 5, total: 0 } })).toBeUndefined();
		expect(sidekiqJobPercent({ status: "running" })).toBeUndefined();
		// Totals are approximate, so a finished job reads 100 even when done fell short.
		expect(sidekiqJobPercent({ status: "completed", progress: { done: 90, total: 100 } })).toBe(100);
		// A cancelled job keeps the fraction it reached.
		expect(sidekiqJobPercent({ status: "cancelled", progress: { done: 30, total: 100 } })).toBe(30);
	});
});
describe("dismissing finished sidekiq jobs", () => {
	const jobs = [
		{ id: "a", status: "running" as const },
		{ id: "b", status: "completed" as const },
		{ id: "c", status: "failed" as const },
		{ id: "d", status: "cancelled" as const },
		{ id: "e", status: "pending" as const },
	];

	it("selects only terminal jobs for Clear finished", () => {
		expect(finishedSidekiqJobIds(jobs)).toEqual(["b", "c", "d"]);
		expect(finishedSidekiqJobIds([])).toEqual([]);
	});

	it("hides dismissed jobs and keeps the rest in order", () => {
		expect(visibleSidekiqJobs(jobs, ["b", "d"]).map((j) => j.id)).toEqual(["a", "c", "e"]);
		expect(visibleSidekiqJobs(jobs, [])).toBe(jobs);
		expect(visibleSidekiqJobs(jobs, ["unknown"])).toHaveLength(5);
	});
});