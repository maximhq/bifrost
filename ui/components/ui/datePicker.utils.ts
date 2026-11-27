import { format } from "date-fns";
import type { Matcher } from "react-day-picker";

// Pure helpers behind DatePicker, kept out of the component so they can be tested without rendering.

/** Parses a `YYYY-MM-DD` calendar date into local midnight, or undefined for "" or anything else. */
export function parseDateOnly(value: string): Date | undefined {
	const match = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value);
	if (!match) return undefined;
	const [year, month, day] = [Number(match[1]), Number(match[2]), Number(match[3])];
	const date = new Date(year, month - 1, day);
	// new Date() rolls overflow into the next month (Feb 31 -> Mar 3), so a mismatch means the day does not exist.
	return date.getFullYear() === year && date.getMonth() === month - 1 && date.getDate() === day ? date : undefined;
}

/** Formats a picked day as `YYYY-MM-DD`, reading the local calendar fields so no timezone shifts it. */
export function formatDateOnly(date: Date): string {
	return format(date, "yyyy-MM-dd");
}

/** The days the calendar refuses. The bounds themselves stay selectable. */
export function dateOnlyDisabledDays(opts: {
	disabled?: boolean;
	disabledBefore?: Date;
	disabledAfter?: Date;
}): Matcher | Matcher[] | undefined {
	const { disabled, disabledBefore, disabledAfter } = opts;
	// The parent can disable the picker while its calendar is open.
	if (disabled) return true;
	// Separate matchers, never one {before, after}: react-day-picker reads that as an AND interval when before is later than after.
	const matchers: Matcher[] = [];
	if (disabledBefore) matchers.push({ before: disabledBefore });
	if (disabledAfter) matchers.push({ after: disabledAfter });
	return matchers.length > 0 ? matchers : undefined;
}