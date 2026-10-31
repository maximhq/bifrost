import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import { getLiveToggleState, getPeriodTimezone, getRangeForPeriod, isCalendarPeriod, TIME_PERIODS, withPeriodTimezone } from "./timeRange";

describe("getLiveToggleState", () => {
	test("turning live on from an absolute range switches to the default period", () => {
		expect(getLiveToggleState(true, "")).toEqual({ polling: true, period: "1h", offset: 0 });
		expect(getLiveToggleState(true, "", "24h")).toEqual({ polling: true, period: "24h", offset: 0 });
	});

	test("turning live on keeps an active relative period", () => {
		expect(getLiveToggleState(true, "6h")).toEqual({ polling: true });
	});

	test("turning live off never touches the time range", () => {
		expect(getLiveToggleState(false, "")).toEqual({ polling: false });
		expect(getLiveToggleState(false, "7d")).toEqual({ polling: false });
	});
});
describe("calendar periods", () => {
	beforeEach(() => {
		vi.useFakeTimers();
		// Thursday 2026-10-15 03:30 UTC, which is still Wednesday evening in New York.
		vi.setSystemTime(new Date("2026-10-15T03:30:00Z"));
	});
	afterEach(() => {
		vi.useRealTimers();
	});

	test("are offered by the picker", () => {
		const values = TIME_PERIODS.map((p) => p.value);
		expect(values).toEqual(expect.arrayContaining(["today", "this_week", "this_month"]));
	});

	test("only calendar values are calendar periods", () => {
		expect(isCalendarPeriod("today")).toBe(true);
		expect(isCalendarPeriod("this_week")).toBe(true);
		expect(isCalendarPeriod("this_month")).toBe(true);
		expect(isCalendarPeriod("24h")).toBe(false);
		expect(isCalendarPeriod("")).toBe(false);
		expect(isCalendarPeriod(undefined)).toBe(false);
	});

	test("today starts at midnight in the given timezone and ends now", () => {
		expect(getRangeForPeriod("today", "UTC")).toEqual({
			from: new Date("2026-10-15T00:00:00Z"),
			to: new Date("2026-10-15T03:30:00Z"),
		});
		// New York is UTC-4 in October, so its "today" is still the 14th.
		expect(getRangeForPeriod("today", "America/New_York").from).toEqual(new Date("2026-10-14T04:00:00Z"));
		// Kolkata is UTC+5:30, already 09:00 on the 15th.
		expect(getRangeForPeriod("today", "Asia/Kolkata").from).toEqual(new Date("2026-10-14T18:30:00Z"));
	});

	test("this week starts on Monday", () => {
		expect(getRangeForPeriod("this_week", "UTC").from).toEqual(new Date("2026-10-12T00:00:00Z"));
		expect(getRangeForPeriod("this_week", "America/New_York").from).toEqual(new Date("2026-10-12T04:00:00Z"));
	});

	test("this month starts on the first", () => {
		expect(getRangeForPeriod("this_month", "UTC").from).toEqual(new Date("2026-10-01T00:00:00Z"));
		expect(getRangeForPeriod("this_month", "Asia/Kolkata").from).toEqual(new Date("2026-09-30T18:30:00Z"));
	});

	test("on the first of the month, this month starts today", () => {
		vi.setSystemTime(new Date("2026-11-01T10:00:00Z"));
		expect(getRangeForPeriod("this_month", "UTC").from).toEqual(new Date("2026-11-01T00:00:00Z"));
		expect(getRangeForPeriod("today", "UTC").from).toEqual(new Date("2026-11-01T00:00:00Z"));
	});

	test("on a Sunday, this week still starts on the previous Monday", () => {
		vi.setSystemTime(new Date("2026-10-18T12:00:00Z"));
		expect(getRangeForPeriod("this_week", "UTC").from).toEqual(new Date("2026-10-12T00:00:00Z"));
	});

	test("sliding periods ignore the timezone", () => {
		expect(getRangeForPeriod("24h", "Asia/Kolkata")).toEqual(getRangeForPeriod("24h", "UTC"));
	});

	test("calendar periods are sent with the timezone the user picked", () => {
		// The server resolves the period; the timezone persisted by useTimezonePreference tells it whose midnight.
		vi.stubGlobal("localStorage", { getItem: (key: string) => (key === "bifrost.timezone" ? "America/New_York" : null) });
		try {
			expect(getPeriodTimezone("today")).toBe("America/New_York");
			expect(getPeriodTimezone("this_month")).toBe("America/New_York");
			expect(withPeriodTimezone({ period: "this_week", models: ["gpt-4o"] })).toEqual({
				period: "this_week",
				models: ["gpt-4o"],
				timezone: "America/New_York",
			});
		} finally {
			vi.unstubAllGlobals();
		}
	});

	test("a stored timezone alias the browser does not list is still honoured", () => {
		// Runtimes list legacy names (Europe/Kiev) and may omit the current one (Europe/Kyiv),
		// but Intl accepts both. A preference saved under either name must not fall back to
		// the browser's zone, or "today" silently starts at the wrong midnight.
		vi.stubGlobal("localStorage", { getItem: (key: string) => (key === "bifrost.timezone" ? "Europe/Kyiv" : null) });
		try {
			// Kyiv is UTC+3 in mid October, so its midnight is 21:00 UTC the day before.
			expect(getRangeForPeriod("today").from).toEqual(new Date("2026-10-14T21:00:00Z"));
		} finally {
			vi.unstubAllGlobals();
		}
	});

	test("an unknown stored timezone falls back to the browser's zone", () => {
		vi.stubGlobal("localStorage", { getItem: (key: string) => (key === "bifrost.timezone" ? "Not/AZone" : null) });
		try {
			const browserZone = Intl.DateTimeFormat().resolvedOptions().timeZone;
			expect(getRangeForPeriod("today")).toEqual(getRangeForPeriod("today", browserZone));
		} finally {
			vi.unstubAllGlobals();
		}
	});

	test("sliding periods and absolute ranges carry no timezone", () => {
		expect(getPeriodTimezone("24h")).toBeUndefined();
		expect(getPeriodTimezone(undefined)).toBeUndefined();
		const sliding = { period: "24h" };
		expect(withPeriodTimezone(sliding)).toBe(sliding);
		const absolute: { period?: string; start_time: string; end_time: string } = {
			start_time: "2026-10-01T00:00:00Z",
			end_time: "2026-10-02T00:00:00Z",
		};
		expect(withPeriodTimezone(absolute)).toBe(absolute);
	});
});