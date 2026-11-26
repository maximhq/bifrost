import { dateMatchModifiers } from "react-day-picker";
import { describe, expect, it } from "vitest";

import { dateOnlyDisabledDays, formatDateOnly, parseDateOnly } from "./datePicker.utils";

const day = (y: number, m: number, d: number) => new Date(y, m - 1, d);

describe("parseDateOnly", () => {
	it("reads YYYY-MM-DD as local midnight of that day", () => {
		expect(parseDateOnly("2026-03-09")).toEqual(day(2026, 3, 9));
	});

	it("treats an empty value as no date, so the placeholder shows", () => {
		expect(parseDateOnly("")).toBeUndefined();
	});

	// new Date() rolls overflowing fields into the next month, so Feb 31 would show as Mar 3.
	it("rejects days that do not exist instead of rolling them over", () => {
		expect(parseDateOnly("2026-02-31")).toBeUndefined();
		expect(parseDateOnly("2026-04-31")).toBeUndefined();
		expect(parseDateOnly("2026-13-01")).toBeUndefined();
		expect(parseDateOnly("2026-00-10")).toBeUndefined();
		expect(parseDateOnly("2026-02-29")).toBeUndefined();
	});

	it("accepts a leap day in a leap year", () => {
		expect(parseDateOnly("2028-02-29")).toEqual(day(2028, 2, 29));
	});

	it("rejects anything that is not a zero-padded calendar date", () => {
		expect(parseDateOnly("2026-3-9")).toBeUndefined();
		expect(parseDateOnly("2026-03-09T00:00:00Z")).toBeUndefined();
	});
});

describe("formatDateOnly", () => {
	it("emits the local calendar day even late in the evening", () => {
		expect(formatDateOnly(new Date(2026, 0, 31, 23, 30))).toBe("2026-01-31");
	});

	it("round-trips through parseDateOnly", () => {
		expect(formatDateOnly(parseDateOnly("2026-12-01")!)).toBe("2026-12-01");
	});
});

describe("dateOnlyDisabledDays", () => {
	const bounds = { disabledBefore: day(2026, 2, 10), disabledAfter: day(2026, 2, 20) };

	it("leaves every day selectable without bounds", () => {
		expect(dateOnlyDisabledDays({})).toBeUndefined();
	});

	it("refuses days outside the bounds and keeps both bounds selectable", () => {
		const matcher = dateOnlyDisabledDays(bounds)!;
		expect(dateMatchModifiers(day(2026, 2, 9), matcher)).toBe(true);
		expect(dateMatchModifiers(day(2026, 2, 10), matcher)).toBe(false);
		expect(dateMatchModifiers(day(2026, 2, 20), matcher)).toBe(false);
		expect(dateMatchModifiers(day(2026, 2, 21), matcher)).toBe(true);
	});

	// One combined {before, after} matcher turns into an AND interval when before is later than after.
	it("keeps both bounds when they fall on the same day at different times", () => {
		const matcher = dateOnlyDisabledDays({ disabledBefore: new Date(2026, 1, 10, 18), disabledAfter: new Date(2026, 1, 10, 0) })!;
		expect(dateMatchModifiers(day(2026, 2, 5), matcher)).toBe(true);
		expect(dateMatchModifiers(day(2026, 2, 10), matcher)).toBe(false);
		expect(dateMatchModifiers(day(2026, 2, 20), matcher)).toBe(true);
	});

	// A parent can disable the picker while its calendar is open, e.g. during a save.
	it("refuses every day while the picker is disabled", () => {
		const matcher = dateOnlyDisabledDays({ ...bounds, disabled: true })!;
		expect(dateMatchModifiers(day(2026, 2, 15), matcher)).toBe(true);
		expect(dateMatchModifiers(day(2026, 2, 10), matcher)).toBe(true);
	});
});