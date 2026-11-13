import { type MetricsState, metricsState } from "@/app/workspace/logs/views/metricStrip.utils";
import { useGetDimensionRankingsQuery } from "@/lib/store/apis/logsApi";
import { COMPLEXITY_TIER_VALUES } from "@/lib/types/complexityRouter";
import type { DimensionRankingEntry } from "@/lib/types/logs";
import { useMemo, useState } from "react";

const WEEK_MS = 7 * 24 * 60 * 60 * 1000;

// TrafficWindow is the last seven days, in the shape the logs filters take.
export interface TrafficWindow {
	start_time: string;
	end_time: string;
}

// useTrafficWindow freezes the last seven days on mount. A date minted in
// render would change the query argument every render and re-subscribe
// forever. The Routers page holds it, so the list and a router's page share
// one window and so one cached read.
export function useTrafficWindow(): TrafficWindow {
	const [window] = useState(() => {
		const now = Date.now();
		return { start_time: new Date(now - WEEK_MS).toISOString(), end_time: new Date(now).toISOString() };
	});
	return window;
}

// RouterTraffic is what one router took over the window.
export interface RouterTraffic {
	requests: number;
	cost: number;
	// Percent change in cost against the seven days before; null when there is nothing to compare with.
	costTrend: number | null;
}

// toRouterTraffic reads one router's rankings row. The cost change is only
// kept when the router took requests in the week before; with a free week
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
// window, what they cost, and how that cost moved against the week before,
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