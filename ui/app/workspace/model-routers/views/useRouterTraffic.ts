import { type MetricsState, metricsState } from "@/app/workspace/logs/views/metricStrip.utils";
import { useTimezonePreference } from "@/lib/hooks/useTimezonePreference";
import { useGetDimensionRankingsQuery } from "@/lib/store/apis/logsApi";
import { COMPLEXITY_TIER_VALUES } from "@/lib/types/complexityRouter";
import { dateUtils, type DimensionRankingEntry } from "@/lib/types/logs";
import { getRangeForPeriod, TIME_PERIODS } from "@/lib/utils/timeRange";
import { parseAsInteger, parseAsString, useQueryStates } from "nuqs";
import { useCallback, useMemo } from "react";

// The period the Routers pages open on.
export const DEFAULT_TRAFFIC_PERIOD = "7d";

// TrafficWindow is the period the traffic figures cover, in the shape the logs
// filters take: a preset period the gateway resolves against its own clock, or
// the two ends of a custom range.
export interface TrafficWindow {
	period?: string;
	start_time?: string;
	end_time?: string;
}

// TrafficSelection is what the period picker holds: a preset period, or the
// two ends of a custom range in unix seconds.
export type TrafficSelection = { period: string; start?: undefined; end?: undefined } | { period?: undefined; start: number; end: number };

// trafficSelection reads the selection out of the URL. A preset the picker
// offers is taken as it is; with no preset, a range with both ends in order is
// a custom range. Anything else, an empty URL included, is the default period.
export function trafficSelection(period: string | null, start: number | null, end: number | null): TrafficSelection {
	if (period && TIME_PERIODS.some((preset) => preset.value === period)) return { period };
	if (!period && start != null && end != null && start < end) return { start, end };
	return { period: DEFAULT_TRAFFIC_PERIOD };
}

// trafficWindow is the selection as the logs filters take it.
export function trafficWindow(selection: TrafficSelection): TrafficWindow {
	if (selection.period !== undefined) return { period: selection.period };
	return { start_time: dateUtils.toISOString(selection.start), end_time: dateUtils.toISOString(selection.end) };
}

// trafficLogsSearch is the selection as the logs page reads it from its URL,
// so a link into a router's requests opens them over the same period.
export function trafficLogsSearch(selection: TrafficSelection): { period?: string; start_time?: number; end_time?: number } {
	if (selection.period !== undefined) return { period: selection.period };
	return { start_time: selection.start, end_time: selection.end };
}

// customRangeLabel names a custom range by its two dates, in the UI language
// and in the timezone the picker shows the range in, so the two never name
// different days for a range that ends near midnight.
export function customRangeLabel(start: number, end: number, locale: string, timezone: string): string {
	return new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeZone: timezone }).formatRange(
		new Date(start * 1000),
		new Date(end * 1000),
	);
}

// TrafficFilter is the period the Routers pages show traffic for, with what
// the period picker needs to show and change it.
export interface TrafficFilter {
	selection: TrafficSelection;
	window: TrafficWindow;
	// The dates the picker shows; for a preset, the range it covers right now.
	dateRange: { from: Date; to: Date };
	setPeriod: (period: string | undefined) => void;
	setDateRange: (range: { from?: Date; to?: Date }) => void;
	// The timezone the picker shows dates in, held here so a label of the same range reads them the same way.
	timezone: string;
	setTimezone: (timezone: string) => void;
}

// useTrafficFilter keeps the period in the URL, as the dashboard and logs
// pages keep theirs, so a link carries it and the routers list and a router's
// page share one period and so one cached read. The default period leaves the
// URL clean. A preset is sent to the gateway by name, so the query argument
// stays the same across renders with no date minted here.
export function useTrafficFilter(): TrafficFilter {
	const [url, setUrl] = useQueryStates({ period: parseAsString, start_time: parseAsInteger, end_time: parseAsInteger });
	const [timezone, setTimezone] = useTimezonePreference();
	const selection = useMemo(() => trafficSelection(url.period, url.start_time, url.end_time), [url.period, url.start_time, url.end_time]);
	const window = useMemo(() => trafficWindow(selection), [selection]);
	const dateRange = useMemo(
		() =>
			selection.period !== undefined
				? getRangeForPeriod(selection.period)
				: { from: new Date(selection.start * 1000), to: new Date(selection.end * 1000) },
		[selection],
	);
	const setPeriod = useCallback(
		(period: string | undefined) => {
			if (!period) return;
			void setUrl({ period: period === DEFAULT_TRAFFIC_PERIOD ? null : period, start_time: null, end_time: null });
		},
		[setUrl],
	);
	const setDateRange = useCallback(
		(range: { from?: Date; to?: Date }) => {
			if (!range.from || !range.to) return;
			void setUrl({
				period: null,
				start_time: dateUtils.toUnixTimestamp(range.from) ?? null,
				end_time: dateUtils.toUnixTimestamp(range.to) ?? null,
			});
		},
		[setUrl],
	);
	return useMemo(
		() => ({ selection, window, dateRange, setPeriod, setDateRange, timezone, setTimezone }),
		[selection, window, dateRange, setPeriod, setDateRange, timezone, setTimezone],
	);
}

// RouterTraffic is what one router took over the window.
export interface RouterTraffic {
	requests: number;
	cost: number;
	// Percent change in cost against the period of the same length before; null when there is nothing to compare with.
	costTrend: number | null;
}

// toRouterTraffic reads one router's rankings row. The cost change is only
// kept when the router took requests in the period before; with a free period
// before it, the gateway sends no change at all.
export function toRouterTraffic(entry: DimensionRankingEntry): RouterTraffic {
	const trend = entry.trend;
	return {
		requests: entry.total_requests,
		cost: entry.total_cost,
		costTrend: trend?.has_previous_period && trend.cost_trend != null ? trend.cost_trend : null,
	};
}

// RouterTrafficRead is the routers' figures and the state of the read behind
// them, in the logs metric strip's terms: pending while the first read is in
// flight, unavailable when it failed with nothing to show, ready once byRouter
// holds the figures.
export interface RouterTrafficRead {
	// Keyed by router id; undefined until the read answers. A router missing from it took nothing.
	byRouter: Map<string, RouterTraffic> | undefined;
	state: MetricsState;
}

// useRouterTraffic reads how many requests each model router took over the
// window, what they cost, and how that cost moved against the period before,
// keyed by router id. One rankings call covers every router; `all` so a
// router outside any top-N still gets its figures. The Routers page calls it
// once and hands the figures to the list and to a router's page, as the
// projects page does, so moving between them does not read the logs again.
export function useRouterTraffic(window: TrafficWindow): RouterTrafficRead {
	const { data, isLoading, error } = useGetDimensionRankingsQuery({ filters: window, dimension: "model_router", all: true });
	const byRouter = useMemo(() => {
		if (!data) return undefined;
		const traffic = new Map<string, RouterTraffic>();
		for (const entry of data.rankings ?? []) traffic.set(entry.id, toRouterTraffic(entry));
		return traffic;
	}, [data]);
	const state = metricsState(data, isLoading, error);
	return useMemo(() => ({ byRouter, state }), [byRouter, state]);
}

// routerRequests is what the routers list shows for one router's requests:
// nothing until the read has answered (or when it failed), then its count,
// with zero for a router the read does not list, as the router's own page
// reads it.
export function routerRequests(traffic: Map<string, RouterTraffic> | undefined, routerId: string): number | undefined {
	if (!traffic) return undefined;
	return traffic.get(routerId)?.requests ?? 0;
}

export type ComplexityTierValue = (typeof COMPLEXITY_TIER_VALUES)[number];

// TierCount is how many of a router's requests one tier took.
export interface TierCount {
	tier: ComplexityTierValue;
	requests: number;
}

// tierSplit orders a router's tier rankings as the tiers run, Simple to
// Complex, with a zero for a tier that took nothing. A row outside the three
// tiers (the retired REASONING tier on old logs) is left out.
export function tierSplit(rankings: DimensionRankingEntry[]): TierCount[] {
	const counts = new Map(rankings.map((entry) => [entry.id, entry.total_requests]));
	return COMPLEXITY_TIER_VALUES.map((tier) => ({ tier, requests: counts.get(tier) ?? 0 }));
}

// useRouterTierSplit reads how one router's requests over the window split
// across tiers: the tier rankings filtered to this router, which reads only
// that router's rows through the model_router_id index. A request the
// classifier gave no tier is not counted, so the split can sum to less than
// the router's requests. Undefined while it loads or fails.
export function useRouterTierSplit(routerId: string, window: TrafficWindow): TierCount[] | undefined {
	const filters = useMemo(() => ({ ...window, model_router_ids: [routerId] }), [window, routerId]);
	const { data } = useGetDimensionRankingsQuery({ filters, dimension: "complexity_tier", all: true });
	return useMemo(() => (data ? tierSplit(data.rankings ?? []) : undefined), [data]);
}