import type { Notification } from "@/lib/types/notifications";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import reducer, {
	addNotification,
	clearAllNotifications,
	hydrateNotificationPreferences,
	markAllNotificationsRead,
	pruneExpiredNotifications,
	removeNotification,
	setNotifications,
} from "./appSlice";

// The reducer filters against Date.now(), so the clock is pinned rather than
// left to run: a fixture that hardcodes a future expiry only stays "live" until
// that date passes, and the suite starts failing on a calendar day nobody
// touched. Expiries are derived from the pinned instant instead of written out.
const NOW = new Date("2026-08-17T12:00:00Z");
const fromNow = (ms: number) => new Date(Date.now() + ms).toISOString();

beforeEach(() => {
	vi.useFakeTimers();
	vi.setSystemTime(NOW);
});

afterEach(() => {
	vi.useRealTimers();
});

const notification = (id: string, createdAt: string, expiresAt = fromNow(60 * 60 * 1000)): Notification => ({
	id,
	audience: "all",
	severity: "info",
	title: id,
	message: "message",
	created_at: createdAt,
	expires_at: expiresAt,
});

const expired = (id: string, createdAt: string) => notification(id, createdAt, fromNow(-60 * 60 * 1000));

describe("notification state", () => {
	it("merges history and live events without duplicates", () => {
		let state = reducer(undefined, setNotifications([notification("older", "2026-08-17T10:00:00Z")]));
		state = reducer(state, addNotification(notification("newer", "2026-08-17T11:00:00Z")));
		state = reducer(state, setNotifications([notification("newer", "2026-08-17T11:00:00Z")]));
		expect(state.notifications.map(({ id }) => id)).toEqual(["newer", "older"]);
	});

	it("keeps read, dismiss, and clear state local", () => {
		let state = reducer(undefined, hydrateNotificationPreferences({ scope: "user-a", readIds: [], dismissedIds: [] }));
		state = reducer(state, setNotifications([notification("one", "2026-08-17T10:00:00Z"), notification("two", "2026-08-17T11:00:00Z")]));
		state = reducer(state, markAllNotificationsRead());
		expect(state.notificationPreferences.readIds).toEqual(["two", "one"]);
		state = reducer(state, removeNotification("one"));
		expect(state.notificationPreferences.dismissedIds).toEqual(["one"]);
		state = reducer(state, clearAllNotifications());
		expect(state.notificationPreferences.dismissedIds).toEqual(["one", "two"]);
		expect(state.notifications).toHaveLength(2);
	});

	// Go strips trailing zeros from the RFC3339 fraction, so within one second a
	// string compare orders "." before "Z" and puts the older row on top.
	it("orders by instant, not by timestamp string", () => {
		const state = reducer(
			undefined,
			setNotifications([notification("whole", "2026-08-17T10:00:00Z"), notification("fraction", "2026-08-17T10:00:00.5Z")]),
		);
		expect(state.notifications.map(({ id }) => id)).toEqual(["fraction", "whole"]);
	});

	it("drops rows the server has already expired", () => {
		let state = reducer(undefined, setNotifications([notification("live", "2026-08-17T10:00:00Z"), expired("stale", "2026-08-17T11:00:00Z")]));
		expect(state.notifications.map(({ id }) => id)).toEqual(["live"]);

		state = reducer(state, addNotification(expired("stale-push", "2026-08-17T12:00:00Z")));
		expect(state.notifications.map(({ id }) => id)).toEqual(["live"]);
	});

	it("prunes rows that expire while the tab stays open", () => {
		// Seeded past the reducer's own filter, as a long-lived tab accumulates them.
		const seeded = { notifications: [notification("live", "2026-08-17T10:00:00Z"), expired("stale", "2026-08-17T11:00:00Z")] };
		const state = reducer(seeded as never, pruneExpiredNotifications());
		expect(state.notifications.map(({ id }) => id)).toEqual(["live"]);
	});

	it("keeps rows whose expires_at is unparseable rather than hiding them", () => {
		const state = reducer(undefined, setNotifications([notification("odd", "2026-08-17T10:00:00Z", "not-a-date")]));
		expect(state.notifications.map(({ id }) => id)).toEqual(["odd"]);
	});
});
