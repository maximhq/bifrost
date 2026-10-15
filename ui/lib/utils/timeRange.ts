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
 * so unlike the sliding periods they depend on the viewer's timezone. The server
 * cannot resolve them, so callers send them as an absolute range instead.
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
 * Replaces a calendar period with the absolute range it covers right now, since
 * the server only resolves sliding periods. Call it when building a request, not
 * when building cache keys, so every poll re-resolves "now" and a new day rolls
 * over on its own. Other filters pass through unchanged.
 */
export function withResolvedCalendarPeriod<T extends { period?: string; start_time?: string; end_time?: string }>(filters: T): T {
	if (!isCalendarPeriod(filters.period)) return filters;
	const { from, to } = getRangeForPeriod(filters.period as string);
	return { ...filters, period: undefined, start_time: from.toISOString(), end_time: to.toISOString() };
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