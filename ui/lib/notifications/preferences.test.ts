import { beforeEach, describe, expect, it, vi } from "vitest";
import { notificationPreferenceKey, readNotificationPreferences, writeNotificationPreferences } from "./preferences";

describe("notification preferences", () => {
	beforeEach(() => {
		const values = new Map<string, string>();
		vi.stubGlobal("localStorage", {
			getItem: (key: string) => values.get(key) ?? null,
			setItem: (key: string, value: string) => values.set(key, value),
		});
		vi.stubGlobal("window", {});
	});

	it("isolates read and dismissed IDs by user scope", () => {
		writeNotificationPreferences("user-a", { readIds: ["n1"], dismissedIds: ["n2"] });
		expect(readNotificationPreferences("user-a")).toEqual({ readIds: ["n1"], dismissedIds: ["n2"] });
		expect(readNotificationPreferences("user-b")).toEqual({ readIds: [], dismissedIds: [] });
		expect(notificationPreferenceKey("user@example.com")).toContain("user%40example.com");
	});

	it("fails closed to empty preferences for malformed storage", () => {
		localStorage.setItem(notificationPreferenceKey("user-a"), "not-json");
		expect(readNotificationPreferences("user-a")).toEqual({ readIds: [], dismissedIds: [] });
	});
});