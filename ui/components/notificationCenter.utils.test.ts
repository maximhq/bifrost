import { describe, expect, it } from "vitest";
import { isNotificationsUnavailable, shouldHideNotificationTrigger } from "./notificationCenter.utils";

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
		shouldHideNotificationTrigger({ open: false, isLoading: false, isError: false, count: 0, ...overrides });

	// The regression: a first load that fails leaves nothing to fall back on, so
	// hiding the trigger here makes the tray's "Try again" unreachable and the
	// feed silently stays empty until the user reloads the page.
	it("keeps a failed empty load reachable so it can be retried", () => {
		expect(state({ isError: true })).toBe(false);
	});

	it("hides while the first load is still in flight", () => {
		expect(state({ isLoading: true })).toBe(true);
		// A retry in flight after a failure must not flip the trigger back off.
		expect(state({ isLoading: true, open: true, isError: true })).toBe(false);
	});

	it("hides a genuinely empty deployment", () => {
		expect(state()).toBe(true);
	});

	it("shows the trigger once there is anything to open", () => {
		expect(state({ count: 3 })).toBe(false);
		expect(state({ count: 3, isError: true })).toBe(false);
	});

	it("stays mounted while the popover is open, whatever the feed says", () => {
		expect(state({ open: true })).toBe(false);
		expect(state({ open: true, isLoading: true })).toBe(false);
	});
});
