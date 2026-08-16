import { describe, expect, it } from "vitest";
import { isNotificationsUnavailable } from "./notificationCenter.utils";

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
