import { TZDate } from "@date-fns/tz";
import { startOfDay, startOfMonth, startOfWeek } from "date-fns";
import { getPreferredTimezone } from "@/lib/timezones";

export const TIME_PERIODS = [
	{ label: "Last hour", value: "1h" },
	{ label: "Last 6 hours", value: "6h" },
	{ label: "Last 24 hours", value: "24h" },
	{ label: "Last 7 days", value: "7d" },
	{ label: "Last 30 days", value: "30d" },
	{ label: "Today", value: "today" },
	{ label: "This week", value: "this_week" },
	{ label: "This month", value: "this_month" },
];

export type TimePeriod = (typeof TIME_PERIODS)[number]["value"];

const CALENDAR_PERIODS = new Set(["today", "this_week", "this_month"]);

/**
 * Calendar periods (today, this week, this month) start at a wall-clock boundary,
 * so unlike the sliding periods they depend on the viewer's timezone. Requests
 * send them with a `timezone` param (see withPeriodTimezone) so the server can
 * find that midnight.
 */
export function isCalendarPeriod(period: string | undefined): boolean {
	return !!period && CALENDAR_PERIODS.has(period);
}

/**
 * Returns a fresh { from, to } Date pair for the given period string. Calendar
 * periods start at midnight of the current day, week (Monday) or month in
 * `timezone`, which defaults to the user's preferred timezone.
 */
export function getRangeForPeriod(period: string, timezone: string = getPreferredTimezone()): { from: Date; to: Date } {
	const to = new Date();
	if (isCalendarPeriod(period)) {
		const zonedNow = new TZDate(to.getTime(), timezone);
		const start =
			period === "today"
				? startOfDay(zonedNow)
				: period === "this_week"
					? startOfWeek(zonedNow, { weekStartsOn: 1 })
					: startOfMonth(zonedNow);
		return { from: new Date(start.getTime()), to };
	}
	const from = new Date(to.getTime());
	switch (period) {
		case "1h":
			from.setHours(from.getHours() - 1);
			break;
		case "6h":
			from.setHours(from.getHours() - 6);
			break;
		case "24h":
			from.setHours(from.getHours() - 24);
			break;
		case "7d":
			from.setDate(from.getDate() - 7);
			break;
		case "30d":
			from.setDate(from.getDate() - 30);
			break;
		default:
			from.setHours(from.getHours() - 1);
	}
	return { from, to };
}

/**
 * Returns the timezone to send with `period` so the server resolves a calendar
 * period from the same midnight the picker shows: the user's preferred zone for
 * calendar periods, undefined for sliding periods, which do not depend on it.
 */
export function getPeriodTimezone(period: string | undefined): string | undefined {
	return isCalendarPeriod(period) ? getPreferredTimezone() : undefined;
}

/** Adds the period's timezone (see getPeriodTimezone) to filters sent as a JSON body. */
export function withPeriodTimezone<T extends { period?: string }>(filters: T): T & { timezone?: string } {
	const timezone = getPeriodTimezone(filters.period);
	return timezone ? { ...filters, timezone } : filters;
}

/** Returns unix timestamps (seconds) for the given relative period string. */
export function getUnixRangeForPeriod(period: string): { start: number; end: number } {
	const { from, to } = getRangeForPeriod(period);
	return {
		start: Math.floor(from.getTime() / 1000),
		end: Math.floor(to.getTime() / 1000),
	};
}

/**
 * URL state for flipping a page's Live toggle. Polling a fixed absolute range
 * whose end is already in the past re-fetches the same rows forever, so turning
 * Live on from an absolute range switches back to a sliding relative period.
 */
export function getLiveToggleState(
	enabled: boolean,
	currentPeriod: string,
	defaultPeriod = "1h",
): { polling: boolean; period?: string; offset?: number } {
	if (!enabled || currentPeriod) return { polling: enabled };
	return { polling: true, period: defaultPeriod, offset: 0 };
}